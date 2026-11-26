// knowledge_inject.go implements scoped internal experience selection, deterministic
// relevance ranking, UTF-8 rune budget trimming, and dual-track formatting for AI Assistant turns.
package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/help"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
)

const (
	maxAssistantKnowledgeEntries    = 8
	maxAssistantKnowledgeTotalRunes = 2000
	maxAssistantKnowledgeEntryRunes = 400
)

// AssistantKnowledgeItem is a single scoped internal experience note injected into an assistant turn.
type AssistantKnowledgeItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ScopeType   string `json:"scope_type"`
	MaxSeverity string `json:"max_severity"`
	Text        string `json:"text"`
}

// selectAndTrimAssistantKnowledge deterministically ranks matched datasource/table entries and trims
// them to at most 8 entries, 400 UTF-8 runes per entry, and 2,000 total UTF-8 runes.
func selectAndTrimAssistantKnowledge(
	entries []*knowledge.Entry,
	prompt string,
	inputSQL string,
	describedTables []string,
) ([]*knowledge.Entry, int) {
	ranked := rankAssistantKnowledgeEntries(entries, prompt, inputSQL, describedTables)
	if len(ranked) == 0 {
		return nil, 0
	}
	return trimAssistantKnowledgeBudget(ranked)
}

func rankAssistantKnowledgeEntries(
	entries []*knowledge.Entry,
	prompt string,
	inputSQL string,
	describedTables []string,
) []*knowledge.Entry {
	candidates := make([]*knowledge.Entry, 0, len(entries))
	for _, e := range entries {
		if e != nil && e.State == knowledge.StateEnabled && e.ScopeType != knowledge.ScopeGlobal {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	stmtClasses := detectAssistantStatementClasses(prompt, inputSQL)
	queryKeywords := extractAssistantKeywords(prompt, inputSQL, describedTables)

	type scoredEntry struct {
		entry     *knowledge.Entry
		scopeRank int
		relevance int
		sevRank   int
	}
	scored := make([]scoredEntry, 0, len(candidates))
	for _, e := range candidates {
		sRank := 1
		if e.ScopeType == knowledge.ScopeTable {
			sRank = 2
		}
		scored = append(scored, scoredEntry{
			entry:     e,
			scopeRank: sRank,
			relevance: scoreAssistantKnowledgeRelevance(e, stmtClasses, queryKeywords),
			sevRank:   advisorySeverityRank(e.Definition.EffectiveAdvisorySeverity()),
		})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].scopeRank != scored[j].scopeRank {
			return scored[i].scopeRank > scored[j].scopeRank
		}
		if scored[i].relevance != scored[j].relevance {
			return scored[i].relevance > scored[j].relevance
		}
		if scored[i].sevRank != scored[j].sevRank {
			return scored[i].sevRank > scored[j].sevRank
		}
		return scored[i].entry.ID < scored[j].entry.ID
	})

	ranked := make([]*knowledge.Entry, len(scored))
	for i, item := range scored {
		ranked[i] = item.entry
	}
	return ranked
}

func trimAssistantKnowledgeBudget(ranked []*knowledge.Entry) ([]*knowledge.Entry, int) {
	selected := make([]*knowledge.Entry, 0, min(len(ranked), maxAssistantKnowledgeEntries))
	usedRunes := 0
	truncatedCount := 0
	for _, entry := range ranked {
		if len(selected) >= maxAssistantKnowledgeEntries {
			truncatedCount++
			continue
		}
		trimmed := strings.TrimSpace(entry.Definition.KnowledgeText)
		if trimmed == "" {
			continue
		}
		text, fit, truncated := fitAssistantKnowledgeEntryText(trimmed, maxAssistantKnowledgeTotalRunes-usedRunes, len(selected) == 0)
		if truncated {
			truncatedCount++
		}
		if !fit {
			continue
		}
		cp := *entry
		cp.Definition.KnowledgeText = text
		selected = append(selected, &cp)
		usedRunes += utf8.RuneCountInString(text)
	}
	return selected, truncatedCount
}

func fitAssistantKnowledgeEntryText(trimmed string, remainingRunes int, allowShortFallback bool) (string, bool, bool) {
	if remainingRunes <= 0 {
		return "", false, true
	}
	rawRunes := utf8.RuneCountInString(trimmed)
	text := truncateUTF8Runes(trimmed, maxAssistantKnowledgeEntryRunes)
	itemRunes := utf8.RuneCountInString(text)
	truncated := rawRunes > itemRunes
	if itemRunes > remainingRunes {
		if !allowShortFallback && remainingRunes < 64 {
			return "", false, true
		}
		text = truncateUTF8Runes(text, remainingRunes)
		return text, true, true
	}
	return text, true, truncated
}

func advisorySeverityRank(sev string) int {
	switch sev {
	case "medium":
		return 3
	case "low":
		return 2
	default:
		return 1
	}
}

func truncateUTF8Runes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

func detectAssistantStatementClasses(prompt, inputSQL string) map[string]bool {
	combined := strings.ToLower(prompt + " " + inputSQL)
	classes := map[string]bool{}
	for _, kw := range []string{"create ", "alter ", "drop ", "index", "ddl", "建表", "加字段", "索引", "表结构"} {
		if strings.Contains(combined, kw) {
			classes["ddl"] = true
			break
		}
	}
	for _, kw := range []string{"insert", "update", "delete", "dml", "新增", "插入", "更新", "修改", "删除"} {
		if strings.Contains(combined, kw) {
			classes["dml"] = true
			break
		}
	}
	for _, kw := range []string{"select", "query", "with ", "查询", "统计", "报表"} {
		if strings.Contains(combined, kw) {
			classes["select"] = true
			break
		}
	}
	return classes
}

func extractAssistantKeywords(prompt, inputSQL string, tables []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		low := strings.ToLower(strings.TrimSpace(tok))
		if len(low) < 2 || seen[low] {
			return
		}
		seen[low] = true
		out = append(out, low)
	}
	for _, tbl := range tables {
		add(tbl)
	}
	for _, src := range []string{prompt, inputSQL} {
		var cur strings.Builder
		for _, r := range src {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
				cur.WriteRune(r)
			} else if cur.Len() > 0 {
				add(cur.String())
				cur.Reset()
			}
		}
		if cur.Len() > 0 {
			add(cur.String())
		}
	}
	return out
}

func scoreAssistantKnowledgeRelevance(e *knowledge.Entry, stmtClasses map[string]bool, keywords []string) int {
	haystack := strings.ToLower(e.Name + " " + e.Purpose + " " + e.TableName + " " + e.Definition.KnowledgeText)
	score := 0
	if stmtClasses["ddl"] && (strings.Contains(haystack, "ddl") || strings.Contains(haystack, "create") || strings.Contains(haystack, "alter") || strings.Contains(haystack, "index") || strings.Contains(haystack, "索引") || strings.Contains(haystack, "建表")) {
		score += 3
	}
	if stmtClasses["dml"] && (strings.Contains(haystack, "dml") || strings.Contains(haystack, "insert") || strings.Contains(haystack, "update") || strings.Contains(haystack, "delete") || strings.Contains(haystack, "批量") || strings.Contains(haystack, "更新") || strings.Contains(haystack, "删除")) {
		score += 3
	}
	if stmtClasses["select"] && (strings.Contains(haystack, "select") || strings.Contains(haystack, "query") || strings.Contains(haystack, "查询") || strings.Contains(haystack, "扫描")) {
		score += 3
	}
	for _, kw := range keywords {
		if strings.Contains(haystack, kw) {
			score += 2
		}
	}
	return score
}

func buildDefaultAssistantKnowledge(
	priv *privacy.TurnPrivacySession,
	entries []*knowledge.Entry,
	extraCatalogTables []string,
) ([]AssistantKnowledgeItem, error) {
	if priv == nil || len(entries) == 0 {
		return nil, nil
	}
	out := make([]AssistantKnowledgeItem, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		anonText, err := priv.AnonymizeKnowledgeEntryText(e.Definition.KnowledgeText, extraCatalogTables)
		if err != nil {
			if errors.Is(err, privacy.ErrKnowledgeRefOutOfScope) {
				continue
			}
			return nil, err
		}
		anonName, err := priv.AnonymizeKnowledgeEntryText(e.Name, extraCatalogTables)
		if err != nil {
			if errors.Is(err, privacy.ErrKnowledgeRefOutOfScope) {
				continue
			}
			return nil, err
		}
		out = append(out, AssistantKnowledgeItem{
			ID:          e.ID,
			Name:        anonName,
			ScopeType:   string(e.ScopeType),
			MaxSeverity: e.Definition.EffectiveAdvisorySeverity(),
			Text:        anonText,
		})
	}
	return out, nil
}

func expandSANKnowledgeText(text string) (string, error) {
	return revprivacy.RewritePolicyReferences(text, func(ref revprivacy.PolicyObjectReference) (string, error) {
		switch ref.Kind {
		case revprivacy.PolicyReferenceSchema:
			return ref.Schema, nil
		case revprivacy.PolicyReferenceTable, revprivacy.PolicyReferenceView:
			return ref.Schema + "." + ref.Relation, nil
		case revprivacy.PolicyReferenceColumn, revprivacy.PolicyReferenceIndex, revprivacy.PolicyReferenceConstraint:
			return ref.Schema + "." + ref.Relation + "." + ref.Name, nil
		default:
			return "", revprivacy.ErrInvalidPolicyReference
		}
	})
}

func buildSANAssistantKnowledge(entries []*knowledge.Entry) []AssistantKnowledgeItem {
	if len(entries) == 0 {
		return nil
	}
	out := make([]AssistantKnowledgeItem, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		expandedText, err := expandSANKnowledgeText(e.Definition.KnowledgeText)
		if err != nil {
			continue
		}
		expandedName, err := expandSANKnowledgeText(e.Name)
		if err != nil {
			expandedName = e.Name
		}
		out = append(out, AssistantKnowledgeItem{
			ID:          e.ID,
			Name:        expandedName,
			ScopeType:   string(e.ScopeType),
			MaxSeverity: e.Definition.EffectiveAdvisorySeverity(),
			Text:        expandedText,
		})
	}
	return out
}

func escapeSANKnowledgeBody(s string) string {
	s = strings.ReplaceAll(s, "</internal_knowledge", "&lt;/internal_knowledge")
	s = strings.ReplaceAll(s, "<internal_knowledge", "&lt;internal_knowledge")
	return s
}

func formatSANKnowledgePrefix(items []AssistantKnowledgeItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<internal_knowledge>\n")
	for _, item := range items {
		fmt.Fprintf(&b, "- [%s|%s|%s] %s\n",
			escapeSANKnowledgeBody(item.ScopeType),
			escapeSANKnowledgeBody(item.MaxSeverity),
			escapeSANKnowledgeBody(item.Name),
			escapeSANKnowledgeBody(item.Text),
		)
	}
	b.WriteString("</internal_knowledge>\n\n")
	return b.String()
}

func buildSANAssistantMessages(in SynthesizeInput) (string, []byte, error) {
	actorRole := "authenticated_user"
	if in.Actor.IsBuiltinAdmin {
		actorRole = "builtin_admin"
	}
	articles := help.ToPromptProfileArticles(in.PlatformKnowledge)
	if in.Capability != domain.CapabilityPlatformHelp {
		// RetrieveRelevantArticles assigns PrimaryMatch=true to topK fallback items even when score == 0.
		// Keep <help_context> omitted on non-help turns unless at least one keyword-matched or synthetic test article matched.
		for i := range in.PlatformKnowledge {
			if len(in.PlatformKnowledge[i].Keywords) > 0 && in.PlatformKnowledge[i].RetrievalScore == 0 {
				articles[i].PrimaryMatch = false
			}
		}
	}
	sysPrompt, err := promptprofile.BuildSANAssistantSystemPrompt(promptprofile.AssistantSystemPromptInput{
		OutputLocale:   in.OutputLocale,
		ActorRole:      actorRole,
		CapabilityHint: string(in.Capability),
		HasMetadata:    len(in.RawMetadata) > 0 || len(in.AnonymizedMetadata) > 0,
		Articles:       articles,
	})
	if err != nil {
		return "", nil, err
	}
	promptText := in.RawPrompt
	if promptText == "" {
		promptText = in.AnonymizedPrompt
	}
	if len(in.RawKnowledge) > 0 {
		promptText = formatSANKnowledgePrefix(in.RawKnowledge) + promptText
	}
	payload := map[string]any{
		"response_format": "json",
		"capability_hint": string(in.Capability),
		"engine_profile":  in.EngineProfile,
		"output_locale":   promptprofile.NormalizeOutputLocale(in.OutputLocale),
		"prompt":          promptText,
	}
	if strings.TrimSpace(in.RawInputSQL) != "" {
		payload["input_sql"] = promptprofile.TruncateSQL(in.RawInputSQL)
	}
	if strings.TrimSpace(in.RawPlanText) != "" {
		payload["plan_text"] = in.RawPlanText
	}
	if len(in.RawMetadata) > 0 {
		payload["metadata"] = in.RawMetadata
	}
	if len(in.RawHistory) > 0 {
		payload["history"] = in.RawHistory
	}
	if strings.TrimSpace(in.RawSummary) != "" {
		payload["summary"] = in.RawSummary
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	return sysPrompt, raw, nil
}
