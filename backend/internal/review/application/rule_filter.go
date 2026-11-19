package application

import (
	"regexp"
	"sort"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// canonicalStatementKinds enumerates the concrete SQL statement kinds recognized
// by the implicit conservative rule fast-filter. Any statement outside this set
// triggers fail-open behavior so no rule is ever dropped erroneously.
var canonicalStatementKinds = map[string]bool{
	"create":   true,
	"alter":    true,
	"drop":     true,
	"truncate": true,
	"insert":   true,
	"update":   true,
	"delete":   true,
	"merge":    true,
	"select":   true,
}

var (
	reShapeOpToken = regexp.MustCompile(`(?i)(?:^|\s)op=([a-z0-9_]+)`)

	// Cross-statement generic phrases: matching any of these immediately classifies
	// a skill as "all" (always included).
	genericRulePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\ball\s+statements?\b`),
		regexp.MustCompile(`(?i)\bevery\s+statements?\b`),
		regexp.MustCompile(`(?i)\bany\s+statements?\b`),
		regexp.MustCompile(`(?i)\bgeneral\s+rule\b`),
		regexp.MustCompile(`(?i)\bglobal\s+rule\b`),
		regexp.MustCompile(`(?i)\bsubselect\b`),
		regexp.MustCompile(`(?i)\bsubquer(?:y|ies)\b`),
	}

	// Chinese cross-statement generic markers encoded with Unicode escapes to
	// maintain pure-English source text.
	genericRuleChineseMarkers = []string{
		"\u901a\u7528",             // generic / universal
		"\u6240\u6709\u8bed\u53e5", // all statements
		"\u5168\u90e8\u8bed\u53e5", // every statement
		"\u4efb\u610f\u8bed\u53e5", // any statement
		"\u6bcf\u6761\u8bed\u53e5", // each statement
		"\u6240\u6709sql",          // all SQL (no space)
		"\u6240\u6709 sql",         // all SQL (with space)
		"\u5b50\u67e5\u8be2",       // subquery
	}

	// English word-boundary patterns per statement bucket. Because Go regexp \b
	// treats ASCII letters, digits, and '_' as word characters, identifiers such as
	// updated_at, created_at, deleted_at, or ddl_is_create_view never match.
	statementBucketPatterns = map[string]*regexp.Regexp{
		"create":   regexp.MustCompile(`(?i)\bcreate\b`),
		"alter":    regexp.MustCompile(`(?i)\balter\b`),
		"drop":     regexp.MustCompile(`(?i)\bdrop\b`),
		"truncate": regexp.MustCompile(`(?i)\btruncate\b`),
		"insert":   regexp.MustCompile(`(?i)\binsert\b`),
		"update":   regexp.MustCompile(`(?i)\bupdate\b`),
		"delete":   regexp.MustCompile(`(?i)\bdelete\b`),
		"merge":    regexp.MustCompile(`(?i)\bmerge\b`),
		"select":   regexp.MustCompile(`(?i)\bselect\b`),
	}

	// Explicit Chinese SQL verb phrases mapped to concrete statement buckets.
	statementBucketChineseMarkers = map[string][]string{
		"create": {
			"\u5efa\u8868",             // create table (short)
			"\u521b\u5efa\u8868",       // create table
			"\u521b\u5efa\u7d22\u5f15", // create index
			"\u521b\u5efa\u89c6\u56fe", // create view
		},
		"alter": {
			"\u4fee\u6539\u8868\u7ed3\u6784", // alter table schema
			"\u53d8\u66f4\u8868\u7ed3\u6784", // change table schema
		},
		"drop": {
			"\u5220\u8868",       // drop table (short)
			"\u5220\u5e93",       // drop database (short)
			"\u5220\u9664\u8868", // drop table
			"\u5220\u9664\u5e93", // drop database
		},
		"truncate": {
			"\u6e05\u7a7a\u8868", // truncate table
		},
	}

	// Guard patterns preventing rules about views or embedded DML/DDL SELECT clauses
	// from being misclassified as SELECT-only rules.
	reSelectCrossContext = regexp.MustCompile(`(?i)\b(?:ddl|dml|view)\b`)
	chineseViewMarker    = "\u89c6\u56fe" // view

	// Hyphenated/underscored column or attribute compounds (e.g. update-time,
	// create-at, delete-flag) refer to field names rather than SQL operations.
	reNonVerbCompound = regexp.MustCompile(`(?i)\b(?:create|alter|drop|truncate|insert|update|delete|merge|select)[-_](?:at|time|date|ts|timestamp|flag|status|col|column|field|by|user|id)\b`)
)

// compatibleStatementOpGroups defines explicit multi-operation combinations that
// belong to the same SQL family and may be safely filtered together when none of
// the operations appear in the batch. Any multi-bucket match that does not fit one
// of these groups is conservatively classified as "all".
var compatibleStatementOpGroups = []map[string]bool{
	{"update": true, "delete": true},
	{"drop": true, "truncate": true},
	{"insert": true, "update": true},
	{"insert": true, "update": true, "delete": true},
	{"insert": true, "update": true, "delete": true, "merge": true},
	{"create": true, "alter": true},
	{"create": true, "alter": true, "drop": true},
	{"create": true, "alter": true, "drop": true, "truncate": true},
}

// extractBatchStatementKinds extracts the set of canonical StatementKinds present
// across all fingerprint groups in a batch. If groups is empty or any group has an
// empty, unknown, or non-canonical kind, it returns (nil, true) to fail open.
func extractBatchStatementKinds(groups []provider.AnonymizedGroup) (map[string]bool, bool) {
	if len(groups) == 0 {
		return nil, true
	}
	ops := make(map[string]bool, len(groups))
	for _, g := range groups {
		kind, ok := statementKindOfGroup(g)
		if !ok {
			return nil, true
		}
		ops[kind] = true
	}
	if len(ops) == 0 {
		return nil, true
	}
	return ops, false
}

func statementKindOfGroup(g provider.AnonymizedGroup) (string, bool) {
	if m := reShapeOpToken.FindStringSubmatch(g.RepresentativeShape); len(m) == 2 {
		if k, ok := normalizeCanonicalStatementKind(m[1]); ok {
			return k, true
		}
		return "", false
	}
	if g.Statistics != nil {
		if op, ok := g.Statistics["operation"].(string); ok && strings.TrimSpace(op) != "" {
			return normalizeCanonicalStatementKind(op)
		}
		if sk, ok := g.Statistics["statement_kind"].(string); ok && strings.TrimSpace(sk) != "" {
			return normalizeCanonicalStatementKind(sk)
		}
	}
	trimmed := strings.TrimSpace(g.RepresentativeShape)
	if trimmed != "" && !strings.ContainsAny(trimmed, " \t\n\r=") {
		return normalizeCanonicalStatementKind(trimmed)
	}
	return "", false
}

func normalizeCanonicalStatementKind(raw string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(raw))
	if k == "" || !canonicalStatementKinds[k] {
		return "", false
	}
	return k, true
}

// classifySkillOps implicitly classifies a SkillInjection into either a specific
// non-empty operation bucket set or pass-all (isAll=true). Classification is
// strictly conservative (fail-open): generic phrases, zero bucket hits, or
// cross-category/conflicting multi-bucket hits always return isAll=true.
func classifySkillOps(sk agent.SkillInjection) (map[string]bool, bool) {
	combined := strings.Join([]string{sk.Name, sk.FindingKey, sk.KnowledgeText}, "\n")
	lower := strings.ToLower(combined)

	for _, re := range genericRulePatterns {
		if re.MatchString(combined) {
			return nil, true
		}
	}
	for _, marker := range genericRuleChineseMarkers {
		if strings.Contains(lower, marker) {
			return nil, true
		}
	}

	sanitized := reNonVerbCompound.ReplaceAllString(combined, " ")
	matched := make(map[string]bool, 4)
	for op, re := range statementBucketPatterns {
		if re.MatchString(sanitized) {
			matched[op] = true
		}
	}
	for op, markers := range statementBucketChineseMarkers {
		if matched[op] {
			continue
		}
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				matched[op] = true
				break
			}
		}
	}

	if len(matched) == 0 {
		return nil, true
	}
	if matched["select"] && (reSelectCrossContext.MatchString(combined) || strings.Contains(combined, chineseViewMarker)) {
		return nil, true
	}
	if len(matched) == 1 {
		return matched, false
	}
	if isCompatibleOpSet(matched) {
		return matched, false
	}
	return nil, true
}

func isCompatibleOpSet(matched map[string]bool) bool {
	for _, group := range compatibleStatementOpGroups {
		if len(matched) > len(group) {
			continue
		}
		allIn := true
		for op := range matched {
			if !group[op] {
				allIn = false
				break
			}
		}
		if allIn {
			return true
		}
	}
	return false
}

// filterStageSkills applies the implicit conservative fast-filter to engine-matched
// skills for a stage. A skill is skipped only when batchOps is completely known,
// the skill maps unambiguously to a single bucket or compatible group, and the
// intersection with batchOps is empty.
func filterStageSkills(skills []agent.SkillInjection, groups []provider.AnonymizedGroup) []agent.SkillInjection {
	if len(skills) == 0 {
		return nil
	}
	batchOps, passAll := extractBatchStatementKinds(groups)
	if passAll {
		out := make([]agent.SkillInjection, len(skills))
		copy(out, skills)
		return out
	}
	out := make([]agent.SkillInjection, 0, len(skills))
	for _, sk := range skills {
		skillOps, isAll := classifySkillOps(sk)
		if isAll || opSetsIntersect(skillOps, batchOps) {
			out = append(out, sk)
		}
	}
	return out
}

func opSetsIntersect(a, b map[string]bool) bool {
	for op := range a {
		if b[op] {
			return true
		}
	}
	return false
}

// sortSkillsByDeterminism returns a deterministically ordered copy of skills so
// rule batch partitioning is stable across retries and execution modes.
func sortSkillsByDeterminism(skills []agent.SkillInjection) []agent.SkillInjection {
	if len(skills) == 0 {
		return nil
	}
	out := make([]agent.SkillInjection, len(skills))
	copy(out, skills)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].FindingKey != out[j].FindingKey {
			return out[i].FindingKey < out[j].FindingKey
		}
		if out[i].ToolID != out[j].ToolID {
			return out[i].ToolID < out[j].ToolID
		}
		return out[i].ConfigHash < out[j].ConfigHash
	})
	return out
}
