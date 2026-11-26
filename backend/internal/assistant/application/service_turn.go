// service_turn.go holds service domain helpers and operations.
package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"gorm.io/gorm"
	"regexp"
	"strings"
	"time"
)

func restoreSANMetadata(priv *privacy.TurnPrivacySession, anon []*tool.AnonymizedTableDescription) []SANTableDescription {
	if len(anon) == 0 {
		return nil
	}
	out := make([]SANTableDescription, 0, len(anon))
	for _, tbl := range anon {
		if tbl == nil {
			continue
		}
		cols := make([]SANColumnFact, 0, len(tbl.Columns))
		for _, c := range tbl.Columns {
			cols = append(cols, SANColumnFact{
				Name:            restoreSANToken(priv, c.ColumnToken),
				Ordinal:         c.Ordinal,
				DataType:        c.DataType,
				Nullable:        c.Nullable,
				IsPrimaryKey:    c.IsPrimaryKey,
				IsUnique:        c.IsUnique,
				DefaultBehavior: string(c.DefaultBehavior),
				Comment:         sanitizeSANComment(c.RawComment),
			})
		}
		idxs := make([]SANIndexFact, 0, len(tbl.Indexes))
		for _, idx := range tbl.Indexes {
			idxCols := make([]string, 0, len(idx.ColumnTokens))
			for _, ct := range idx.ColumnTokens {
				idxCols = append(idxCols, restoreSANToken(priv, ct))
			}
			incCols := make([]string, 0, len(idx.IncludedTokens))
			for _, it := range idx.IncludedTokens {
				incCols = append(incCols, restoreSANToken(priv, it))
			}
			idxs = append(idxs, SANIndexFact{
				Name:            restoreSANToken(priv, idx.IndexToken),
				Primary:         idx.Primary,
				Unique:          idx.Unique,
				Columns:         idxCols,
				IncludedColumns: incCols,
				ExpressionCount: idx.ExpressionCount,
			})
		}
		out = append(out, SANTableDescription{
			Schema:  restoreSANToken(priv, tbl.SchemaToken),
			Table:   restoreSANToken(priv, tbl.TableToken),
			Columns: cols,
			Indexes: idxs,
		})
	}
	return out
}

func retainRawHistory(existing []domain.MessageView, anon []AnonymizedTurn) []AnonymizedTurn {
	if len(anon) == 0 {
		return nil
	}
	bySeq := make(map[int64]string, len(existing))
	for _, m := range existing {
		bySeq[m.Sequence] = m.Content
	}
	out := make([]AnonymizedTurn, len(anon))
	for i, turn := range anon {
		rawContent := turn.Content
		if orig, ok := bySeq[turn.Sequence]; ok && orig != "" {
			rawContent = orig
		}
		out[i] = AnonymizedTurn{
			Sequence:   turn.Sequence,
			Role:       turn.Role,
			Capability: turn.Capability,
			Content:    rawContent,
		}
	}
	return out
}

type executedSynthesizeTurn struct {
	Out           *SynthesizeOutput
	RestoredReply string
	NextTitle     string
}

// executeSynthesizeTurn runs the shared outbound zero-leak check, daily AI budget reserve/settle/release,
// stream delta handling, out-of-scope gate, and session-local token restoration for all SendMessage branches.
func (s *Services) executeSynthesizeTurn(
	runCtx context.Context,
	priv *privacy.TurnPrivacySession,
	in SendMessageInput,
	assistantMsgID string,
	capKind domain.Capability,
	userContent string,
	currentTitle string,
	synthIn *SynthesizeInput,
	fallbackEmptyReply string,
) (*executedSynthesizeTurn, error) {
	if err := scrubAndAssertSynthesizeInput(priv, synthIn); err != nil {
		return nil, err
	}
	outboundBytes, err := json.Marshal(synthIn)
	if err != nil {
		return nil, domain.ErrAnonymizationFailed("marshal_synthesize_input_failed")
	}
	in.emit("tool", map[string]any{"tool": "privacy_session.anonymize_and_gate", "status": "completed", "capability": string(capKind)})
	lease, err := s.acquireDailyBudgetLease(runCtx, int64(len(outboundBytes))/3+256)
	if err != nil {
		return nil, err
	}
	in.emit("reasoning", map[string]any{"status": "running"})
	onDelta, onSANDelta, flushDelta := makeTurnDeltaHandlers(priv, in.emit)
	synthIn.OnDelta = onDelta
	synthIn.OnSANDelta = onSANDelta
	synthOut, err := s.Synthesizer.Synthesize(runCtx, *synthIn)
	if err != nil {
		_ = lease.Release(context.WithoutCancel(runCtx))
		if errors.Is(err, context.Canceled) {
			return nil, domain.ErrInvalidState("Generation request cancelled")
		}
		var ade *domain.Error
		if errors.As(err, &ade) {
			return nil, ade
		}
		return nil, domain.ErrProviderFailed(err.Error())
	}
	var providerID, modelName string
	var inTokens, outTokens int64
	if synthOut != nil {
		providerID, modelName = synthOut.ProviderID, synthOut.ModelName
		inTokens, outTokens = synthOut.InputTokens, synthOut.OutputTokens
	}
	if bserr := lease.Settle(runCtx, assistantMsgID, providerID, modelName, "assistant", inTokens, outTokens, "succeeded"); bserr != nil {
		return nil, bserr
	}
	if synthOut.OutOfScope || synthOut.ResolvedCapability == string(domain.CapabilityOutOfScope) {
		msg := strings.TrimSpace(synthOut.AssistantReply)
		if msg == "" {
			msg = "The Database AI Assistant only supports Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization; non-database requests are out of scope"
		}
		return nil, domain.ErrOutOfScope(msg)
	}
	in.emit("reasoning", map[string]any{"status": "completed"})
	reply := synthOut.AssistantReply
	rawTitle := synthOut.SessionTitle
	if !synthOut.UsedSANProfile {
		reply = priv.RestoreTextTokens(reply)
		rawTitle = priv.RestoreTextTokens(rawTitle)
	}
	if fallbackEmptyReply != "" {
		if trimmed := strings.TrimSpace(reply); trimmed != "" {
			reply = trimmed
		} else {
			reply = fallbackEmptyReply
		}
	}
	nextTitle := summarizeLatestSessionTitle(rawTitle, userContent, currentTitle, in.OutputLocale)
	flushDelta(reply)
	in.emit("progress", map[string]any{"phase": "validating_and_restoring"})
	return &executedSynthesizeTurn{
		Out:           synthOut,
		RestoredReply: reply,
		NextTitle:     nextTitle,
	}, nil
}

type newDraftSpec struct {
	DatasourceID   string
	DatasourceName string
	FlowID         *string
	Engine         string
	SchemaName     string
	StatementKind  string
	RiskCategory   string
	Payload        persistedDraftPayload
}

// ---- Helper & encryption methods ----

func (s *Services) acquireActiveStream(parent context.Context, userID, sessionID, messageID string) (context.Context, context.CancelFunc, bool) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if _, busy := s.activeStreams[userID]; busy {
		return nil, nil, false
	}
	runCtx, cancel := context.WithCancel(parent)
	s.activeStreams[userID] = &activeStreamEntry{
		sessionID: sessionID,
		messageID: messageID,
		cancel:    cancel,
	}
	return runCtx, cancel, true
}

func (s *Services) releaseActiveStream(userID, messageID string, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if cur, ok := s.activeStreams[userID]; ok && cur.messageID == messageID {
		delete(s.activeStreams, userID)
	}
}

func (s *Services) cancelActiveStreamForSession(userID, sessionID string) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if cur, ok := s.activeStreams[userID]; ok && cur.sessionID == sessionID {
		cur.cancel()
		delete(s.activeStreams, userID)
	}
}

func (s *Services) loadOwnedSession(ctx context.Context, db *gorm.DB, userID, sessionID string) (*persistence.AIAssistantSessionModel, error) {
	var row persistence.AIAssistantSessionModel
	if err := db.WithContext(ctx).Where("id = ?", sessionID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	if row.UserID != userID || row.State != string(domain.SessionStateActive) || !row.ExpiresAt.After(time.Now().UTC()) {
		return nil, domain.ErrNotFound()
	}
	return &row, nil
}

func (s *Services) loadOwnedDraft(
	ctx context.Context,
	db *gorm.DB,
	userID, sessionID, draftID string,
) (*persistence.AIAssistantSQLDraftModel, persistedDraftPayload, error) {
	var row persistence.AIAssistantSQLDraftModel
	if err := db.WithContext(ctx).
		Where("id = ? and session_id = ? and user_id = ?", draftID, sessionID, userID).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, persistedDraftPayload{}, domain.ErrNotFound()
		}
		return nil, persistedDraftPayload{}, err
	}
	payload, err := s.openDraftPayload(&row)
	if err != nil {
		return nil, persistedDraftPayload{}, err
	}
	return &row, payload, nil
}

func (s *Services) loadSessionMessagesForContext(ctx context.Context, userID, sessionID string) ([]domain.MessageView, error) {
	var rows []persistence.AIAssistantMessageModel
	if err := s.DB.WithContext(ctx).
		Where("session_id = ? and user_id = ?", sessionID, userID).
		Order("sequence asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.MessageView, 0, len(rows))
	for i := range rows {
		mp, err := s.openMessagePayload(&rows[i])
		if err != nil {
			continue
		}
		out = append(out, s.messageView(&rows[i], mp, nil))
	}
	return out, nil
}

func (s *Services) loadDatasourceNames(ctx context.Context, drafts []persistence.AIAssistantSQLDraftModel) map[string]string {
	out := make(map[string]string)
	if len(drafts) == 0 {
		return out
	}
	ids := make([]string, 0, len(drafts))
	seen := make(map[string]bool)
	for _, d := range drafts {
		if d.DatasourceID != "" && !seen[d.DatasourceID] {
			seen[d.DatasourceID] = true
			ids = append(ids, d.DatasourceID)
		}
	}
	var rows []struct {
		ID   string `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	_ = s.DB.WithContext(ctx).Raw(`select id::text as id, name from datasources where id in ?`, ids).Scan(&rows).Error
	for _, r := range rows {
		out[r.ID] = r.Name
	}
	return out
}

func (s *Services) sealSessionTitle(sessionID, title string) (crypto.Envelope, error) {
	return s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "ai_assistant_sessions",
		PrimaryKey: sessionID,
		Column:     "title",
	}, []byte(title))
}

func (s *Services) openSessionTitle(row *persistence.AIAssistantSessionModel) (string, error) {
	plain, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "ai_assistant_sessions",
		PrimaryKey: row.ID,
		Column:     "title",
	}, crypto.Envelope{
		Ciphertext: row.TitleCiphertext,
		Nonce:      row.TitleNonce,
		KeyID:      row.TitleKeyID,
		Algorithm:  row.TitleAlgorithm,
	})
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Services) sealMessagePayload(messageID string, payload persistedMessagePayload) (crypto.Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return crypto.Envelope{}, err
	}
	return s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "ai_assistant_messages",
		PrimaryKey: messageID,
		Column:     "content",
	}, raw)
}

func (s *Services) openMessagePayload(row *persistence.AIAssistantMessageModel) (persistedMessagePayload, error) {
	plain, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "ai_assistant_messages",
		PrimaryKey: row.ID,
		Column:     "content",
	}, crypto.Envelope{
		Ciphertext: row.ContentCiphertext,
		Nonce:      row.ContentNonce,
		KeyID:      row.ContentKeyID,
		Algorithm:  row.ContentAlgorithm,
	})
	if err != nil {
		return persistedMessagePayload{}, err
	}
	var out persistedMessagePayload
	if err := json.Unmarshal(plain, &out); err != nil {
		return persistedMessagePayload{Content: UnwrapAssistantReplyJSON(string(plain))}, nil
	}
	out.Content = UnwrapAssistantReplyJSON(out.Content)
	return out, nil
}

func (s *Services) sealDraftPayload(draftID string, payload persistedDraftPayload) (crypto.Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return crypto.Envelope{}, err
	}
	return s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "ai_assistant_sql_drafts",
		PrimaryKey: draftID,
		Column:     "sql",
	}, raw)
}

func (s *Services) openDraftPayload(row *persistence.AIAssistantSQLDraftModel) (persistedDraftPayload, error) {
	plain, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "ai_assistant_sql_drafts",
		PrimaryKey: row.ID,
		Column:     "sql",
	}, crypto.Envelope{
		Ciphertext: row.SQLCiphertext,
		Nonce:      row.SQLNonce,
		KeyID:      row.SQLKeyID,
		Algorithm:  row.SQLAlgorithm,
	})
	if err != nil {
		return persistedDraftPayload{}, err
	}
	var out persistedDraftPayload
	if err := json.Unmarshal(plain, &out); err != nil || out.SQLText == "" {
		return persistedDraftPayload{SQLText: string(plain)}, nil
	}
	return out, nil
}

func (s *Services) openSummaryContent(row *persistence.AIAssistantContextSummaryModel) (string, error) {
	plain, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "ai_assistant_context_summaries",
		PrimaryKey: row.ID,
		Column:     "summary",
	}, crypto.Envelope{
		Ciphertext: row.SummaryCiphertext,
		Nonce:      row.SummaryNonce,
		KeyID:      row.SummaryKeyID,
		Algorithm:  row.SummaryAlgorithm,
	})
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Services) computeSQLHMAC(sqlText string) ([]byte, string, error) {
	keyID, err := s.Crypter.HMACKeyID("sql_fingerprint")
	if err != nil {
		return nil, "", err
	}
	mac, err := s.Crypter.HMAC("sql_fingerprint", []byte(sqlText))
	if err != nil {
		return nil, "", err
	}
	return mac, keyID, nil
}

// acquireDailyBudgetLease acquires a shared daily AI budget lease via review/application.
// Fails closed with ErrBudgetServiceUnavailable when s.Review or s.DB is unconfigured (D14).
func (s *Services) acquireDailyBudgetLease(ctx context.Context, estTokens int64) (*application.BudgetLease, error) {
	if s == nil || s.Review == nil {
		return nil, application.ErrBudgetServiceUnavailable
	}
	lease, err := s.Review.AcquireBudgetLease(ctx, s.DB, estTokens)
	if err != nil {
		if errors.Is(err, application.ErrBudgetExhausted) {
			return nil, domain.ErrBudgetExhausted()
		}
		return nil, err
	}
	return lease, nil
}

func (s *Services) sessionView(row *persistence.AIAssistantSessionModel, title string) domain.SessionView {
	return domain.SessionView{
		ID:                  row.ID,
		UserID:              row.UserID,
		Title:               title,
		State:               row.State,
		ActiveDatasourceID:  row.ActiveDatasourceID,
		ActiveFlowID:        row.ActiveFlowID,
		ActiveSchemaName:    row.ActiveSchemaName,
		SelectedDraftID:     row.SelectedDraftID,
		CompactedThroughSeq: row.CompactedThroughSeq,
		ExpiresAt:           row.ExpiresAt.UTC().Format(timeFormatRFC3339),
		Version:             row.Version,
		CreatedAt:           row.CreatedAt.UTC().Format(timeFormatRFC3339),
		UpdatedAt:           row.UpdatedAt.UTC().Format(timeFormatRFC3339),
	}
}

func (s *Services) messageView(
	row *persistence.AIAssistantMessageModel,
	mp persistedMessagePayload,
	draft *domain.SQLDraftView,
) domain.MessageView {
	citations := mp.Citations
	if citations == nil {
		citations = []domain.CitationView{}
	}
	return domain.MessageView{
		ID:          row.ID,
		SessionID:   row.SessionID,
		Sequence:    row.Sequence,
		Role:        row.Role,
		Capability:  row.Capability,
		State:       row.State,
		Content:     mp.Content,
		Citations:   citations,
		SQLDraft:    draft,
		FailureCode: row.FailureCode,
		CreatedAt:   row.CreatedAt.UTC().Format(timeFormatRFC3339),
	}
}

func (s *Services) draftView(
	row *persistence.AIAssistantSQLDraftModel,
	dp persistedDraftPayload,
	dsName *string,
) domain.SQLDraftView {
	return domain.SQLDraftView{
		ID:                      row.ID,
		SessionID:               row.SessionID,
		MessageID:               row.MessageID,
		Revision:                row.Revision,
		DatasourceID:            row.DatasourceID,
		DatasourceName:          dsName,
		FlowID:                  row.FlowID,
		Engine:                  row.Engine,
		SchemaName:              row.SchemaName,
		StatementKind:           row.StatementKind,
		RiskCategory:            row.RiskCategory,
		SQLText:                 dp.SQLText,
		SQLHMAC:                 hex.EncodeToString(row.SQLHMAC),
		Assumptions:             ensureStringSlice(dp.Assumptions),
		NextSteps:               ensureStringSlice(dp.NextSteps),
		SemanticWarnings:        ensureStringSlice(dp.SemanticWarnings),
		ReferencedTables:        ensureStringSlice(dp.ReferencedTables),
		SubmissionState:         row.SubmissionState,
		EphemeralRunID:          row.EphemeralRunID,
		SubmittedOrderID:        row.SubmittedOrderID,
		SubmittedOrderDisplayNo: row.SubmittedOrderDisplayNo,
		Version:                 row.Version,
		CreatedAt:               row.CreatedAt.UTC().Format(timeFormatRFC3339),
		UpdatedAt:               row.UpdatedAt.UTC().Format(timeFormatRFC3339),
	}
}

var enChangeActionKeywords = []string{
	"insert", "update", "delete", "create", "alter", "drop", "truncate",
}

func requiresChangeFlowPermission(capKind domain.Capability, prompt, inputSQL string) bool {
	combined := strings.ToLower(prompt + " " + inputSQL)
	for _, kw := range enChangeActionKeywords {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	for _, kw := range zhChangeActionKeywords {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	return false
}

var (
	boundPhTokenRe       = regexp.MustCompile(`(?i):val_(?:str|int|num|bool|ts|uuid|json|hex)_[0-9]+`)
	forbiddenStmtScopeRe = regexp.MustCompile(`(?i)\b(?:truncate\s+table|truncate\b|drop\s+database\b|drop\s+schema\b|grant\s+|revoke\s+)`)
)

func containsForbiddenStatementScope(content, inputSQL string) bool {
	return forbiddenStmtScopeRe.MatchString(content) || forbiddenStmtScopeRe.MatchString(inputSQL)
}

func extractPlaceholdersInOrder(text string) []string {
	matches := boundPhTokenRe.FindAllString(text, -1)
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		low := strings.ToLower(m)
		if !seen[low] {
			seen[low] = true
			out = append(out, low)
		}
	}
	return out
}

func normalizeOptionalString(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func optionalStringPtr(s string) *string {
	v := strings.TrimSpace(s)
	if v == "" {
		return nil
	}
	return &v
}

func firstNonEmptyString(a, b *string) string {
	if a != nil && strings.TrimSpace(*a) != "" {
		return strings.TrimSpace(*a)
	}
	if b != nil && strings.TrimSpace(*b) != "" {
		return strings.TrimSpace(*b)
	}
	return ""
}

func ensureStringSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
