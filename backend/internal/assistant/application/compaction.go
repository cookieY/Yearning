// compaction.go implements session context token budget accounting, over-limit rejection (6006),
// and automatic desensitized summarization of earlier history turns (PRD §5.8 A4).
package application

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// Default context budget thresholds (PRD §5.8 A4).
const (
	DefaultMaxContextTokens        = 16384
	DefaultReservedOutputTokens    = 4096
	DefaultCompactionTurnThreshold = 12 // Trigger automatic summary compaction when history exceeds 12 messages
	DefaultKeepRecentMessages      = 8  // Keep the most recent 4 turns (8 messages) verbatim for per-turn re-anonymization
	DefaultMaxSingleInputTokens    = 8192
)

// ContextBudgetConfig controls per-turn context token budgets and automatic compaction thresholds.
type ContextBudgetConfig struct {
	MaxContextTokens        int64
	ReservedOutputTokens    int64
	CompactionTurnThreshold int
	KeepRecentMessages      int
	MaxSingleInputTokens    int64
}

func (c ContextBudgetConfig) withDefaults() ContextBudgetConfig {
	if c.MaxContextTokens <= 0 {
		c.MaxContextTokens = DefaultMaxContextTokens
	}
	if c.ReservedOutputTokens <= 0 {
		c.ReservedOutputTokens = DefaultReservedOutputTokens
	}
	if c.CompactionTurnThreshold <= 0 {
		c.CompactionTurnThreshold = DefaultCompactionTurnThreshold
	}
	if c.KeepRecentMessages <= 0 {
		c.KeepRecentMessages = DefaultKeepRecentMessages
	}
	if c.MaxSingleInputTokens <= 0 {
		c.MaxSingleInputTokens = DefaultMaxSingleInputTokens
	}
	return c
}

// CheckSingleInputBudget verifies whether a single user prompt, input SQL, and anonymized metadata exceed the single-turn token budget.
// Returns 6006 ASSISTANT_CONTEXT_BUDGET_EXCEEDED immediately rather than silently truncating SQL or schema context (PRD §5.8).
func (s *Services) CheckSingleInputBudget(prompt, inputSQL, planText, metadataJSON string) error {
	cfg := s.BudgetConfig.withDefaults()
	singleTokens := estimateTokenCount(prompt) + estimateTokenCount(inputSQL) +
		estimateTokenCount(planText) + estimateTokenCount(metadataJSON)
	if singleTokens > cfg.MaxSingleInputTokens {
		return domain.ErrContextBudgetExceeded("Single SQL statement, execution plan, or table metadata exceeds the model context budget; narrow the table scope or split the query and retry")
	}
	return nil
}

// BuildAndCompactHistory loads session history, compacts earlier turns into an encrypted desensitized summary when thresholds are exceeded,
// and re-anonymizes retained recent messages using the current turn's TurnPrivacySession.
func (s *Services) BuildAndCompactHistory(
	ctx context.Context,
	sess *persistence.AIAssistantSessionModel,
	history []domain.MessageView,
	priv *privacy.TurnPrivacySession,
) ([]AnonymizedTurn, string, bool, error) {
	cfg := s.BudgetConfig.withDefaults()
	remainingInputBudget := cfg.MaxContextTokens - cfg.ReservedOutputTokens
	triggerTokenThreshold := (remainingInputBudget * 75) / 100

	var totalHistTokens int64
	for _, m := range history {
		totalHistTokens += estimateTokenCount(m.Content)
	}

	needCompaction := len(history) > cfg.CompactionTurnThreshold || totalHistTokens >= triggerTokenThreshold
	compactedNow := false
	var summaryText string

	// Load the latest valid summary if one exists and is not invalidated.
	var existingSummary persistence.AIAssistantContextSummaryModel
	hasExistingSummary := false
	if err := s.DB.WithContext(ctx).
		Where("session_id = ? and invalidated = false", sess.ID).
		Order("covered_through_seq desc, created_at desc").
		First(&existingSummary).Error; err == nil {
		if plain, oerr := s.openSummaryContent(&existingSummary); oerr == nil {
			summaryText = plain
			hasExistingSummary = true
		}
	}

	recentStart := 0
	if needCompaction && len(history) > cfg.KeepRecentMessages {
		recentStart = len(history) - cfg.KeepRecentMessages
		earlier := history[:recentStart]
		lastEarlierSeq := earlier[len(earlier)-1].Sequence

		if !hasExistingSummary || existingSummary.CoveredThroughSeq < lastEarlierSeq {
			anonEarlier := make([]AnonymizedTurn, 0, len(earlier))
			for _, em := range earlier {
				anonContent, err := anonymizeHistoryTurn(priv, em.Role, em.Content)
				if err != nil {
					// Never send raw text when anonymization fails; skip earlier text that cannot be safely desensitized.
					continue
				}
				anonEarlier = append(anonEarlier, AnonymizedTurn{
					Sequence:   em.Sequence,
					Role:       em.Role,
					Capability: em.Capability,
					Content:    anonContent,
				})
			}
			for i := range anonEarlier {
				anonEarlier[i].Content = priv.ScrubRegisteredTerms(anonEarlier[i].Content)
			}
			if len(anonEarlier) > 0 {
				genSummary, _, _, serr := s.Synthesizer.Summarize(ctx, anonEarlier)
				if serr == nil && strings.TrimSpace(genSummary) != "" && priv.AssertZeroLeak(genSummary) == nil {
					if saveErr := s.saveContextSummary(ctx, sess, earlier[0].Sequence, lastEarlierSeq, genSummary); saveErr == nil {
						summaryText = genSummary
						compactedNow = true
					}
				}
			}
		}
	}

	// Re-anonymize each retained recent message using the current turn's TurnPrivacySession.
	recentSlice := history[recentStart:]
	anonRecent := make([]AnonymizedTurn, 0, len(recentSlice))
	for _, rm := range recentSlice {
		anonContent, err := anonymizeHistoryTurn(priv, rm.Role, rm.Content)
		if err != nil {
			return nil, "", false, err
		}
		anonRecent = append(anonRecent, AnonymizedTurn{
			Sequence:   rm.Sequence,
			Role:       rm.Role,
			Capability: rm.Capability,
			Content:    anonContent,
		})
	}
	for i := range anonRecent {
		anonRecent[i].Content = priv.ScrubRegisteredTerms(anonRecent[i].Content)
	}

	// Verify zero-leak on any existing summary with the current turn's TurnPrivacySession before passing to the model.
	if summaryText != "" {
		anonSum, err := priv.AnonymizeAssistantHistory(summaryText)
		if err != nil {
			summaryText = ""
		} else {
			summaryText = priv.ScrubRegisteredTerms(anonSum)
		}
	}

	return anonRecent, summaryText, compactedNow, nil
}

func anonymizeHistoryTurn(priv *privacy.TurnPrivacySession, role, content string) (string, error) {
	if role == "assistant" {
		return priv.AnonymizeAssistantHistory(content)
	}
	return priv.AnonymizeNaturalLanguage(content)
}

func (s *Services) saveContextSummary(
	ctx context.Context,
	sess *persistence.AIAssistantSessionModel,
	fromSeq, throughSeq int64,
	summaryPlain string,
) error {
	sumID := uuid.NewString()
	env, err := s.Crypter.Seal(crypto.PurposePayload, []byte(summaryPlain), crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_context_summaries",
		PrimaryKey:   sumID,
		Column:       "summary",
		Purpose:      crypto.PurposePayload,
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Mark prior summaries as invalidated.
		if err := tx.Model(&persistence.AIAssistantContextSummaryModel{}).
			Where("session_id = ? and invalidated = false", sess.ID).
			Update("invalidated", true).Error; err != nil {
			return err
		}
		row := persistence.AIAssistantContextSummaryModel{
			ID:                sumID,
			SessionID:         sess.ID,
			CoveredFromSeq:    fromSeq,
			CoveredThroughSeq: throughSeq,
			SourceVersion:     sess.Version,
			SummaryCiphertext: env.Ciphertext,
			SummaryNonce:      env.Nonce,
			SummaryKeyID:      env.KeyID,
			SummaryAlgorithm:  env.Algorithm,
			Invalidated:       false,
			CreatedAt:         now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Model(&persistence.AIAssistantSessionModel{}).
			Where("id = ?", sess.ID).
			Updates(map[string]any{
				"compacted_through_seq": throughSeq,
				"updated_at":            now,
			}).Error; err != nil {
			return err
		}
		sess.CompactedThroughSeq = throughSeq
		return nil
	})
}

// InvalidateSessionSummaries marks active context summaries as invalidated when session messages, deletions, or datasource permissions change (PRD §5.8).
func (s *Services) InvalidateSessionSummaries(ctx context.Context, tx *gorm.DB, sessionID string) error {
	return tx.WithContext(ctx).Model(&persistence.AIAssistantContextSummaryModel{}).
		Where("session_id = ? and invalidated = false", sessionID).
		Update("invalidated", true).Error
}
