// service.go implements Database AI Assistant session management, message handling, encrypted persistence, and concurrent stream guards.
package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/help"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/assistant/tool"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

const timeFormatRFC3339 = "2006-01-02T15:04:05.000Z07:00"

type activeStreamEntry struct {
	sessionID string
	messageID string
	cancel    context.CancelFunc
}

// persistedMessagePayload is the internal JSON payload encrypted inside ai_assistant_messages.content_ciphertext.
type persistedMessagePayload struct {
	Content   string                `json:"content"`
	Citations []domain.CitationView `json:"citations,omitempty"`
}

// persistedDraftPayload is the internal JSON payload encrypted inside ai_assistant_sql_drafts.sql_ciphertext.
type persistedDraftPayload struct {
	SQLText          string   `json:"sql_text"`
	Assumptions      []string `json:"assumptions,omitempty"`
	NextSteps        []string `json:"next_steps,omitempty"`
	SemanticWarnings []string `json:"semantic_warnings,omitempty"`
	ReferencedTables []string `json:"referenced_tables,omitempty"`
}

// Services aggregates all application dependencies for the Database AI Assistant domain.
type Services struct {
	DB           *gorm.DB
	Crypter      *crypto.Crypter
	Review       *application.Services
	ReviewAgent  *application.AgentRunner
	Orders       *orderapp.Services
	Authorizer   *tool.Authorizer
	Synthesizer  Synthesizer
	BudgetConfig ContextBudgetConfig

	// Optional test hooks to override formal precheck or change order submission bridges.
	PrecheckRunner PrecheckRunner
	OrderSubmitter OrderSubmitter

	streamMu      sync.Mutex
	activeStreams map[string]*activeStreamEntry // userID -> activeStreamEntry
}

// NewServices constructs the Database AI Assistant application services.
func NewServices(
	db *gorm.DB,
	crypter *crypto.Crypter,
	reviewSvc *application.Services,
	reviewAgent *application.AgentRunner,
	orderSvc *orderapp.Services,
) (*Services, error) {
	if db == nil || crypter == nil {
		return nil, errors.New("assistant: nil db or crypter dependency")
	}
	catalog := &tool.AdapterCatalogReader{DB: db, Crypter: crypter}
	auth := &tool.Authorizer{
		DB:      db,
		Crypter: crypter,
		Catalog: catalog,
	}
	var fallbackSynth Synthesizer
	if reviewSvc == nil {
		fallbackSynth = &DeterministicSynthesizer{}
	}
	return &Services{
		DB:            db,
		Crypter:       crypter,
		Review:        reviewSvc,
		ReviewAgent:   reviewAgent,
		Orders:        orderSvc,
		Authorizer:    auth,
		Synthesizer:   NewProviderSynthesizer(db, crypter, fallbackSynth),
		BudgetConfig:  ContextBudgetConfig{}.withDefaults(),
		activeStreams: make(map[string]*activeStreamEntry),
	}, nil
}

// CreateSessionInput corresponds to CreateAssistantSessionRequest.
type CreateSessionInput struct {
	Title              string
	ActiveDatasourceID *string
	ActiveFlowID       *string
	ActiveSchemaName   *string
	OutputLocale       string
}

// CreateSession creates a new Database AI Assistant session (enforcing the per-user quota of 50 active sessions and 30-day default retention).
func (s *Services) CreateSession(ctx context.Context, actor domain.Actor, in CreateSessionInput) (*domain.SessionView, error) {
	now := time.Now().UTC()

	// Lazily archive expired active sessions for this user first.
	_ = s.DB.WithContext(ctx).Model(&persistence.AIAssistantSessionModel{}).
		Where("user_id = ? and state = 'active' and expires_at <= ?", actor.UserID, now).
		Updates(map[string]any{"state": string(domain.SessionStateExpired), "updated_at": now}).Error

	var activeCount int64
	if err := s.DB.WithContext(ctx).Model(&persistence.AIAssistantSessionModel{}).
		Where("user_id = ? and state = 'active' and expires_at > ?", actor.UserID, now).
		Count(&activeCount).Error; err != nil {
		return nil, err
	}
	if activeCount >= domain.MaxActiveSessionsPerUser {
		return nil, domain.ErrCapacityExhausted(fmt.Sprintf("At most %d active sessions are allowed per user; delete an older session before creating a new one", domain.MaxActiveSessionsPerUser))
	}

	title := strings.TrimSpace(in.Title)
	if title == "" {
		if isEnglishLocale(in.OutputLocale) {
			title = "New Chat"
		} else {
			title = "\u65b0\u5bf9\u8bdd"
		}
	}
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}

	sessionID := uuid.NewString()
	env, err := s.sealSessionTitle(sessionID, title)
	if err != nil {
		return nil, err
	}

	row := persistence.AIAssistantSessionModel{
		ID:                  sessionID,
		UserID:              actor.UserID,
		TitleCiphertext:     env.Ciphertext,
		TitleNonce:          env.Nonce,
		TitleKeyID:          env.KeyID,
		TitleAlgorithm:      env.Algorithm,
		State:               string(domain.SessionStateActive),
		ActiveDatasourceID:  normalizeOptionalString(in.ActiveDatasourceID),
		ActiveFlowID:        normalizeOptionalString(in.ActiveFlowID),
		ActiveSchemaName:    normalizeOptionalString(in.ActiveSchemaName),
		CompactedThroughSeq: 0,
		ExpiresAt:           now.Add(domain.DefaultSessionRetention),
		Version:             1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	view := s.sessionView(&row, title)
	return &view, nil
}

// ListSessions returns a paginated list of the current user's active, non-expired assistant sessions.
func (s *Services) ListSessions(ctx context.Context, actor domain.Actor, limit int, after string) ([]domain.SessionView, *string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	now := time.Now().UTC()
	q := s.DB.WithContext(ctx).Model(&persistence.AIAssistantSessionModel{}).
		Where("user_id = ? and state = 'active' and expires_at > ?", actor.UserID, now)

	if after != "" {
		parts := strings.SplitN(after, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, nil, domain.ErrValidation("Invalid pagination cursor")
		}
		q = q.Where("(updated_at, id) < (?, ?)", parts[0], parts[1])
	}

	var rows []persistence.AIAssistantSessionModel
	if err := q.Order("updated_at desc, id desc").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, nil, err
	}

	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		cur := fmt.Sprintf("%s|%s", last.UpdatedAt.UTC().Format(timeFormatRFC3339), last.ID)
		next = &cur
		rows = rows[:limit]
	}

	out := make([]domain.SessionView, 0, len(rows))
	for i := range rows {
		title, err := s.openSessionTitle(&rows[i])
		if err != nil {
			title = "Chat Session"
		}
		out = append(out, s.sessionView(&rows[i], title))
	}
	return out, next, nil
}

// GetSessionDetail returns the full message history and associated SQL drafts for a session owned by the current user (strictly forbidding cross-user access).
func (s *Services) GetSessionDetail(ctx context.Context, actor domain.Actor, sessionID string) (*domain.SessionDetailView, error) {
	sess, err := s.loadOwnedSession(ctx, s.DB, actor.UserID, sessionID)
	if err != nil {
		return nil, err
	}
	title, err := s.openSessionTitle(sess)
	if err != nil {
		return nil, err
	}

	var draftRows []persistence.AIAssistantSQLDraftModel
	if err := s.DB.WithContext(ctx).
		Where("session_id = ? and user_id = ?", sess.ID, actor.UserID).
		Order("revision asc, created_at asc").
		Find(&draftRows).Error; err != nil {
		return nil, err
	}

	dsNames := s.loadDatasourceNames(ctx, draftRows)
	draftByID := make(map[string]domain.SQLDraftView, len(draftRows))
	draftViews := make([]domain.SQLDraftView, 0, len(draftRows))
	for i := range draftRows {
		payload, oerr := s.openDraftPayload(&draftRows[i])
		if oerr != nil {
			return nil, oerr
		}
		var dsName *string
		if n, ok := dsNames[draftRows[i].DatasourceID]; ok {
			dsName = &n
		}
		dv := s.draftView(&draftRows[i], payload, dsName)
		draftByID[dv.ID] = dv
		draftViews = append(draftViews, dv)
	}

	var msgRows []persistence.AIAssistantMessageModel
	if err := s.DB.WithContext(ctx).
		Where("session_id = ? and user_id = ?", sess.ID, actor.UserID).
		Order("sequence asc").
		Find(&msgRows).Error; err != nil {
		return nil, err
	}

	msgViews := make([]domain.MessageView, 0, len(msgRows))
	for i := range msgRows {
		mp, oerr := s.openMessagePayload(&msgRows[i])
		if oerr != nil {
			return nil, oerr
		}
		var attachedDraft *domain.SQLDraftView
		if msgRows[i].SQLDraftID != nil {
			if dv, ok := draftByID[*msgRows[i].SQLDraftID]; ok {
				dvCopy := dv
				attachedDraft = &dvCopy
			}
		}
		msgViews = append(msgViews, s.messageView(&msgRows[i], mp, attachedDraft))
	}

	return &domain.SessionDetailView{
		Session:  s.sessionView(sess, title),
		Messages: msgViews,
		Drafts:   draftViews,
	}, nil
}

// DeleteSession soft-deletes session metadata and immediately purges all encrypted messages, SQL drafts, and context summary ciphertexts (PRD §5.7 A3).
func (s *Services) DeleteSession(ctx context.Context, actor domain.Actor, sessionID string, expectVersion int64) error {
	sess, err := s.loadOwnedSession(ctx, s.DB, actor.UserID, sessionID)
	if err != nil {
		return err
	}
	if sess.Version != expectVersion {
		return &domain.Error{Code: domain.CodeVersionConflict, Name: "VERSION_CONFLICT", Message: "Session version conflict; please refresh and retry"}
	}

	// If an active generation stream exists for this session, cancel it first.
	s.cancelActiveStreamForSession(actor.UserID, sess.ID)

	redactedEnv, err := s.sealSessionTitle(sess.ID, "[deleted]")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Physically delete associated encrypted summaries, messages, and SQL drafts immediately (clear draft references on messages/sessions before deleting drafts).
		if err := tx.Where("session_id = ?", sess.ID).Delete(&persistence.AIAssistantContextSummaryModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ? and user_id = ?", sess.ID, actor.UserID).Delete(&persistence.AIAssistantMessageModel{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&persistence.AIAssistantSessionModel{}).
			Where("id = ?", sess.ID).
			Update("selected_draft_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ? and user_id = ?", sess.ID, actor.UserID).Delete(&persistence.AIAssistantSQLDraftModel{}).Error; err != nil {
			return err
		}
		res := tx.Model(&persistence.AIAssistantSessionModel{}).
			Where("id = ? and user_id = ? and version = ?", sess.ID, actor.UserID, expectVersion).
			Updates(map[string]any{
				"title_ciphertext":  redactedEnv.Ciphertext,
				"title_nonce":       redactedEnv.Nonce,
				"title_key_id":      redactedEnv.KeyID,
				"title_algorithm":   redactedEnv.Algorithm,
				"state":             string(domain.SessionStateDeleted),
				"selected_draft_id": nil,
				"deleted_at":        now,
				"version":           expectVersion + 1,
				"updated_at":        now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return domain.ErrConcurrent()
		}
		return nil
	})
}

// CancelMessageStream cancels an in-flight streaming assistant response and marks the message state as cancelled.
func (s *Services) CancelMessageStream(ctx context.Context, actor domain.Actor, sessionID, messageID string) (*domain.MessageView, error) {
	sess, err := s.loadOwnedSession(ctx, s.DB, actor.UserID, sessionID)
	if err != nil {
		return nil, err
	}
	var msg persistence.AIAssistantMessageModel
	if err := s.DB.WithContext(ctx).
		Where("id = ? and session_id = ? and user_id = ?", messageID, sess.ID, actor.UserID).
		First(&msg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}

	s.cancelActiveStreamForSession(actor.UserID, sess.ID)

	if msg.State == string(domain.MessageStreaming) {
		now := time.Now().UTC()
		_ = s.DB.WithContext(ctx).Model(&persistence.AIAssistantMessageModel{}).
			Where("id = ?", msg.ID).
			Updates(map[string]any{
				"state":      string(domain.MessageCancelled),
				"version":    msg.Version + 1,
				"updated_at": now,
			}).Error
		msg.State = string(domain.MessageCancelled)
		msg.Version++
		msg.UpdatedAt = now
	}

	mp, err := s.openMessagePayload(&msg)
	if err != nil {
		mp = persistedMessagePayload{Content: "Generation stopped"}
	}
	view := s.messageView(&msg, mp, nil)
	return &view, nil
}

// StreamEvent represents a real-time SSE event pushed from the server to the client.
type StreamEvent struct {
	Event string
	Data  any
}

// SendMessageInput corresponds to SendAssistantMessageRequest.
type SendMessageInput struct {
	SessionID         string
	Content           string
	CapabilityHint    string
	OutputLocale      string
	DatasourceID      *string
	FlowID            *string
	SchemaName        *string
	TargetTable       *string
	SelectedDraftID   *string
	InputSQL          *string
	ExecutionPlanText *string
	OnStreamEvent     func(StreamEvent) `json:"-"`
}

func summarizeLatestSessionTitle(modelTitle, latestUserMessage, previousTitle, outputLocale string) string {
	cleanModel := strings.TrimSpace(strings.ReplaceAll(modelTitle, "\n", " "))
	if cleanModel != "" && uuid.Validate(cleanModel) != nil {
		runes := []rune(cleanModel)
		if len(runes) > 40 {
			cleanModel = string(runes[:40]) + "…"
		}
		return cleanModel
	}
	cleanMsg := strings.TrimSpace(strings.ReplaceAll(latestUserMessage, "\n", " "))
	cleanMsg = strings.Join(strings.Fields(cleanMsg), " ")
	if cleanMsg != "" && uuid.Validate(cleanMsg) != nil {
		runes := []rune(cleanMsg)
		if len(runes) > 28 {
			return string(runes[:28]) + "…"
		}
		return cleanMsg
	}
	if previousTitle != "" && uuid.Validate(previousTitle) != nil {
		return previousTitle
	}
	if isEnglishLocale(outputLocale) {
		return "New Chat"
	}
	return "\u65b0\u5bf9\u8bdd"
}

func (in SendMessageInput) emit(event string, data any) {
	if in.OnStreamEvent != nil {
		in.OnStreamEvent(StreamEvent{Event: event, Data: data})
	}
}

func isTokenBoundaryBlockedChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
		b == '_' || b == ':' || b == '<' || b == '>'
}

func makeSafeRestoringDeltaHandler(priv *privacy.TurnPrivacySession, emit func(string, any)) (func(string, string), func(string)) {
	onAnon, _, flush := makeTurnDeltaHandlers(priv, emit)
	return onAnon, flush
}

func makeTurnDeltaHandlers(priv *privacy.TurnPrivacySession, emit func(string, any)) (func(string, string), func(string, string), func(string)) {
	var (
		anonBuf    strings.Builder
		rawBuf     strings.Builder
		emittedLen int
	)
	emitIncremental := func(text string) {
		if len(text) > emittedLen {
			emit("delta", map[string]any{"delta": text[emittedLen:]})
			emittedLen = len(text)
		}
	}
	onAnonDelta := func(kind string, delta string) {
		if delta == "" {
			return
		}
		if kind == "reasoning" {
			emit("reasoning", map[string]any{"status": "running", "delta": priv.RestoreTextTokens(delta)})
			return
		}
		anonBuf.WriteString(delta)
		raw := anonBuf.String()
		safeEnd := len(raw)
		for safeEnd > 0 && isTokenBoundaryBlockedChar(raw[safeEnd-1]) {
			safeEnd--
		}
		if safeEnd == 0 {
			return
		}
		emitIncremental(priv.RestoreTextTokens(raw[:safeEnd]))
	}
	onRawDelta := func(kind string, delta string) {
		if delta == "" {
			return
		}
		if kind == "reasoning" {
			emit("reasoning", map[string]any{"status": "running", "delta": delta})
			return
		}
		rawBuf.WriteString(delta)
		emitIncremental(rawBuf.String())
	}
	flush := func(finalRestored string) {
		emitIncremental(finalRestored)
	}
	return onAnonDelta, onRawDelta, flush
}

func sanitizeSANComment(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	prevSpace := false
	for _, r := range trimmed {
		switch {
		case r < 0x20 || r == 0x7f:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case r == '<':
			b.WriteByte('(')
			prevSpace = false
		case r == '>':
			b.WriteByte(')')
			prevSpace = false
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	cleaned := strings.TrimSpace(b.String())
	runes := []rune(cleaned)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return cleaned
}

func restoreSANToken(priv *privacy.TurnPrivacySession, tok string) string {
	if priv != nil {
		if _, orig, ok := priv.LookupIdentifier(tok); ok && orig != "" {
			return orig
		}
	}
	return tok
}

func buildAssistantRawSQLScope(
	ctx context.Context,
	auth *tool.Authorizer,
	ds tool.DatasourceTarget,
	schemaName string,
	rawMeta []SANTableDescription,
) privacy.RawSQLScope {
	schKey := strings.ToLower(strings.TrimSpace(schemaName))
	allowed := make(map[string]map[string]map[string]struct{})
	if schKey != "" {
		tblMap := make(map[string]map[string]struct{}, len(rawMeta))
		for _, tbl := range rawMeta {
			tSch := strings.ToLower(strings.TrimSpace(tbl.Schema))
			if tSch == "" {
				tSch = schKey
			}
			if tSch != schKey {
				continue
			}
			tKey := strings.ToLower(strings.TrimSpace(tbl.Table))
			if tKey == "" {
				continue
			}
			colSet := make(map[string]struct{}, len(tbl.Columns))
			for _, col := range tbl.Columns {
				if cKey := strings.ToLower(strings.TrimSpace(col.Name)); cKey != "" {
					colSet[cKey] = struct{}{}
				}
			}
			tblMap[tKey] = colSet
		}
		allowed[schKey] = tblMap
	}
	var (
		listOnce   sync.Once
		schemaTbls []string
		listErr    error
	)
	return privacy.RawSQLScope{
		AuthorizedSchema:      schemaName,
		AllowedColumnsByTable: allowed,
		LookupTableColumns: func(reqSchema, reqTable string) ([]string, bool) {
			if auth == nil || auth.Catalog == nil || schKey == "" {
				return nil, false
			}
			if reqSchema != "" && !strings.EqualFold(reqSchema, schemaName) {
				return nil, false
			}
			listOnce.Do(func() {
				schemaTbls, listErr = auth.Catalog.ListTables(ctx, ds, schemaName)
			})
			if listErr != nil {
				return nil, false
			}
			matched := ""
			for _, candidate := range schemaTbls {
				if strings.EqualFold(candidate, reqTable) {
					matched = candidate
					break
				}
			}
			if matched == "" {
				return nil, false
			}
			rawCols, cerr := auth.Catalog.ListColumns(ctx, ds, schemaName, matched)
			if cerr != nil {
				return nil, false
			}
			out := make([]string, 0, len(rawCols))
			for _, rc := range rawCols {
				if name := strings.TrimSpace(rc.ColumnName); name != "" {
					out = append(out, name)
				}
			}
			return out, true
		},
	}
}

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

// SendMessage processes a single user turn in the Database AI Assistant:
//   - enforces per-user concurrent stream mutual exclusion (6007);
//   - creates a request-scoped in-memory TurnPrivacySession and registers defer ZeroAndRelease();
//   - performs capability routing, scope gating (6001), dual-track datasource authorization (6002),
//     controlled metadata loading, per-turn anonymization and zero-leak assertion (6003), token budget
//     and automatic summary compaction (6006), model synthesis/optimization, structured SQL restoration
//     verification (6004), and AEAD envelope persistence.
func (s *Services) SendMessage(ctx context.Context, actor domain.Actor, in SendMessageInput) (*domain.SendMessageResult, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" || len(content) > 16384 {
		return nil, domain.ErrValidation("Message content must be non-empty and at most 16384 bytes")
	}
	if in.CapabilityHint != "" && !domain.ValidCapability(in.CapabilityHint) {
		return nil, domain.ErrValidation("Unsupported capability hint")
	}

	sess, err := s.loadOwnedSession(ctx, s.DB, actor.UserID, in.SessionID)
	if err != nil {
		return nil, err
	}

	// Check per-session message quota (at most 100 messages, i.e., 50 Q&A turns).
	var msgCount int64
	if err := s.DB.WithContext(ctx).Model(&persistence.AIAssistantMessageModel{}).
		Where("session_id = ?", sess.ID).
		Count(&msgCount).Error; err != nil {
		return nil, err
	}
	if msgCount+2 > domain.MaxMessagesPerSession {
		return nil, domain.ErrCapacityExhausted(fmt.Sprintf("Each session supports at most %d messages; start a new session to continue", domain.MaxMessagesPerSession))
	}

	// Per-user concurrent stream lock (6007 ASSISTANT_STREAM_IN_PROGRESS).
	assistantMsgID := uuid.NewString()
	runCtx, cancelStream, acquired := s.acquireActiveStream(ctx, actor.UserID, sess.ID, assistantMsgID)
	if !acquired {
		return nil, domain.ErrStreamInProgress()
	}
	defer s.releaseActiveStream(actor.UserID, assistantMsgID, cancelStream)

	// Per-request in-memory anonymization session: always overwrite and wipe on exit (success, failure, or cancellation)!
	priv := privacy.NewTurnPrivacySession()
	defer priv.ZeroAndRelease()

	hasInputSQL := in.InputSQL != nil && strings.TrimSpace(*in.InputSQL) != ""
	capKind := help.ClassifyCapability(content, in.CapabilityHint, hasInputSQL)
	if capKind == domain.CapabilityOutOfScope {
		return nil, domain.ErrOutOfScope("The Database AI Assistant only supports Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization; non-database requests are out of scope")
	}

	inputSQL := ""
	if in.InputSQL != nil {
		inputSQL = strings.TrimSpace(*in.InputSQL)
	}
	planText := ""
	if in.ExecutionPlanText != nil {
		planText = strings.TrimSpace(*in.ExecutionPlanText)
	}
	if containsForbiddenStatementScope(content, inputSQL) {
		return nil, domain.ErrOutOfScope("High-risk statements such as TRUNCATE, DROP DATABASE/SCHEMA, and GRANT/REVOKE are not supported by the Database AI Assistant")
	}

	// If this is the first user message and the session title is still default, derive the session title from the first 36 characters (without credentials).
	currentTitle, err := s.openSessionTitle(sess)
	if err != nil {
		currentTitle = "New Chat"
	}
	if msgCount == 0 && (currentTitle == "\u65b0\u5bf9\u8bdd" || currentTitle == "New Chat" || currentTitle == "") {
		cand := strings.ReplaceAll(content, "\n", " ")
		if len([]rune(cand)) > 36 {
			cand = string([]rune(cand)[:36])
		}
		currentTitle = cand
	}

	in.emit("progress", map[string]any{"phase": "authorizing_and_anonymizing", "message_id": assistantMsgID})
	in.emit("tool", map[string]any{"tool": "privacy_session.anonymize_and_gate", "status": "running", "capability": string(capKind)})

	// Branch 1: Platform help consultation (invokes model via system prompt scope gate + controlled knowledge base to synthesize Markdown reply and citations).
	if capKind == domain.CapabilityPlatformHelp {
		anonPrompt, aerr := priv.AnonymizeNaturalLanguage(content)
		if aerr != nil {
			return nil, aerr
		}
		existingHistory, herr := s.loadSessionMessagesForContext(runCtx, actor.UserID, sess.ID)
		if herr != nil {
			return nil, herr
		}
		anonHistory, anonSummary, compactedNow, cerr := s.BuildAndCompactHistory(runCtx, sess, existingHistory, priv)
		if cerr != nil {
			return nil, cerr
		}
		in.emit("tool", map[string]any{"tool": "knowledge_corpus.retrieve_articles", "status": "running"})
		var historyCtxBuilder strings.Builder
		if anonSummary != "" {
			historyCtxBuilder.WriteString(anonSummary)
			historyCtxBuilder.WriteByte('\n')
		}
		for _, turn := range anonHistory {
			historyCtxBuilder.WriteString(turn.Content)
			historyCtxBuilder.WriteByte('\n')
		}
		visibleArticles := help.RetrieveRelevantArticles(actor, content, historyCtxBuilder.String(), 4)
		matchedIDs := help.PrimaryMatchedArticleIDs(visibleArticles)
		in.emit("tool", map[string]any{
			"tool":        "knowledge_corpus.retrieve_articles",
			"status":      "completed",
			"count":       len(matchedIDs),
			"matched_ids": matchedIDs,
		})
		synthIn := SynthesizeInput{
			Capability:        domain.CapabilityPlatformHelp,
			OutputLocale:      in.OutputLocale,
			AnonymizedPrompt:  anonPrompt,
			AnonymizedHistory: anonHistory,
			AnonymizedSummary: anonSummary,
			PlatformKnowledge: visibleArticles,
			Actor:             actor,
			RawPrompt:         content,
			RawHistory:        retainRawHistory(existingHistory, anonHistory),
			RawSummary:        anonSummary,
		}
		if lerr := scrubAndAssertSynthesizeInput(priv, &synthIn); lerr != nil {
			return nil, lerr
		}
		outboundBytes, merr := json.Marshal(synthIn)
		if merr != nil {
			return nil, domain.ErrAnonymizationFailed("marshal_synthesize_input_failed")
		}
		in.emit("tool", map[string]any{"tool": "privacy_session.anonymize_and_gate", "status": "completed", "capability": string(capKind)})
		reservedMinor, budgetDay, budgetCurrency, berr := s.reserveDailyBudget(runCtx, int64(len(outboundBytes))/3+256)
		if berr != nil {
			return nil, berr
		}
		in.emit("reasoning", map[string]any{"status": "running"})
		onDelta, onSANDelta, flushDelta := makeTurnDeltaHandlers(priv, in.emit)
		synthIn.OnDelta = onDelta
		synthIn.OnSANDelta = onSANDelta
		synthOut, serr := s.Synthesizer.Synthesize(runCtx, synthIn)
		s.releaseDailyBudget(runCtx, budgetDay, budgetCurrency, reservedMinor)
		if serr != nil {
			if errors.Is(serr, context.Canceled) {
				return nil, domain.ErrInvalidState("Generation request cancelled")
			}
			var ade *domain.Error
			if errors.As(serr, &ade) {
				return nil, ade
			}
			return nil, domain.ErrProviderFailed(serr.Error())
		}
		if synthOut.OutOfScope || synthOut.ResolvedCapability == string(domain.CapabilityOutOfScope) {
			msg := strings.TrimSpace(synthOut.AssistantReply)
			if msg == "" {
				msg = "The Database AI Assistant only supports Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization; non-database requests are out of scope"
			}
			return nil, domain.ErrOutOfScope(msg)
		}
		in.emit("reasoning", map[string]any{"status": "completed"})
		replyText := synthOut.AssistantReply
		rawTitle := synthOut.SessionTitle
		if !synthOut.UsedSANProfile {
			replyText = priv.RestoreTextTokens(replyText)
			rawTitle = priv.RestoreTextTokens(rawTitle)
		}
		currentTitle = summarizeLatestSessionTitle(rawTitle, content, currentTitle, in.OutputLocale)
		flushDelta(replyText)
		in.emit("progress", map[string]any{"phase": "validating_and_restoring"})
		citations := help.ResolveCitationsWithLocale(actor, synthOut.CitedArticleIDs, content, in.OutputLocale)
		return s.persistTurnOutcome(runCtx, actor, sess, currentTitle, content, capKind,
			persistedMessagePayload{Content: replyText, Citations: citations},
			nil, compactedNow, assistantMsgID)
	}

	// Branch 2 & 3: Natural-language SQL generation (sql_generate) or SQL analysis & optimization (sql_optimize).
	// 1. Resolve target datasource, flow, and schema.
	dsID := firstNonEmptyString(in.DatasourceID, sess.ActiveDatasourceID)
	flowID := firstNonEmptyString(in.FlowID, sess.ActiveFlowID)
	schemaName := firstNonEmptyString(in.SchemaName, sess.ActiveSchemaName)
	targetTable := ""
	if in.TargetTable != nil {
		targetTable = strings.TrimSpace(*in.TargetTable)
	}

	// If no datasource is selected yet, still run anonymization and system prompt scope gating (detecting 6003 credential leaks or 6001 out-of-scope requests, or returning Markdown clarification guidance).
	if dsID == "" {
		anonPrompt, aerr := priv.AnonymizeNaturalLanguage(content)
		if aerr != nil {
			return nil, aerr
		}
		synthIn := SynthesizeInput{
			Capability:        capKind,
			OutputLocale:      in.OutputLocale,
			AnonymizedPrompt:  anonPrompt,
			PlatformKnowledge: help.RetrieveRelevantArticles(actor, content, "", 2),
			Actor:             actor,
			RawPrompt:         content,
		}
		if _, merr := json.Marshal(synthIn); merr != nil {
			return nil, domain.ErrAnonymizationFailed("marshal_synthesize_input_failed")
		}
		if lerr := scrubAndAssertSynthesizeInput(priv, &synthIn); lerr != nil {
			return nil, lerr
		}
		in.emit("tool", map[string]any{"tool": "privacy_session.anonymize_and_gate", "status": "completed", "capability": string(capKind)})
		in.emit("reasoning", map[string]any{"status": "running"})
		onDelta, onSANDelta, flushDelta := makeTurnDeltaHandlers(priv, in.emit)
		synthIn.OnDelta = onDelta
		synthIn.OnSANDelta = onSANDelta
		synthOut, serr := s.Synthesizer.Synthesize(runCtx, synthIn)
		if serr != nil {
			return nil, domain.ErrProviderFailed(serr.Error())
		}
		if synthOut.OutOfScope || synthOut.ResolvedCapability == string(domain.CapabilityOutOfScope) {
			msg := strings.TrimSpace(synthOut.AssistantReply)
			if msg == "" {
				msg = "The Database AI Assistant only supports Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization; non-database requests are out of scope"
			}
			return nil, domain.ErrOutOfScope(msg)
		}
		in.emit("reasoning", map[string]any{"status": "completed"})
		rawClarify := synthOut.AssistantReply
		rawClarifyTitle := synthOut.SessionTitle
		if !synthOut.UsedSANProfile {
			rawClarify = priv.RestoreTextTokens(rawClarify)
			rawClarifyTitle = priv.RestoreTextTokens(rawClarifyTitle)
		}
		clarifyMsg := strings.TrimSpace(rawClarify)
		if clarifyMsg == "" {
			if isEnglishLocale(in.OutputLocale) {
				clarifyMsg = "### Please Select a Target Datasource\n\nPlease select a **Target Datasource** (along with schema and table) at the top of the panel so the assistant can inspect authorized table metadata."
			} else {
				clarifyMsg = "### \u8bf7\u5148\u9009\u62e9\u76ee\u6807\u6570\u636e\u6e90\n\n\u8bf7\u5148\u5728\u9762\u677f\u9876\u90e8\u9009\u62e9**\u76ee\u6807\u6570\u636e\u6e90**\uff08\u4ee5\u53ca\u76ee\u6807\u5e93/Schema \u548c\u8868\u540d\uff09\uff0c\u4ee5\u4fbf\u52a9\u624b\u5728\u60a8\u7684\u6388\u6743\u8303\u56f4\u5185\u8bfb\u53d6\u8868\u7ed3\u6784\u5e76\u751f\u6210\u6216\u5206\u6790 SQL\u3002"
			}
		}
		currentTitle = summarizeLatestSessionTitle(rawClarifyTitle, content, currentTitle, in.OutputLocale)
		flushDelta(clarifyMsg)
		in.emit("progress", map[string]any{"phase": "validating_and_restoring"})
		return s.persistTurnOutcome(runCtx, actor, sess, currentTitle, content, capKind,
			persistedMessagePayload{Content: clarifyMsg},
			nil, false, assistantMsgID)
	}

	// 2. Determine whether change order submission permission is required (DML / DDL / order submission requires change_review grant; read-only SELECT allows query grant).
	requireChangeGrant := requiresChangeFlowPermission(capKind, content, inputSQL)

	// 3. Server-side dual-track permission gate (immediately returns 6002 ASSISTANT_DATASOURCE_FORBIDDEN if unauthorized, before invoking any tool).
	dsTarget, err := s.Authorizer.AuthorizeDatasource(runCtx, actor.UserID, dsID, flowID, requireChangeGrant)
	if err != nil {
		_ = s.InvalidateSessionSummaries(runCtx, s.DB, sess.ID)
		return nil, err
	}
	if flowID == "" && dsTarget.ResolvedFlowID != "" {
		flowID = dsTarget.ResolvedFlowID
	}
	if schemaName == "" {
		schemaName = dsTarget.DefaultDatabase
		if schemaName == "" && dsTarget.CompatibilityMode == "postgresql" {
			schemaName = "public"
		}
	}

	profile, err := fingerprint.ParseProfile(dsTarget.Engine + "/" + dsTarget.CompatibilityMode)
	if err != nil {
		profile = fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
		if dsTarget.CompatibilityMode == "postgresql" {
			profile = fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
		}
	}

	// 4. Load column, type, nullability, default behavior, and index metadata for referenced tables via controlled read-only tools within the authorized scope.
	in.emit("tool", map[string]any{"tool": "catalog_inspector.load_schema_metadata", "status": "running", "schema": schemaName})
	tablesToDescribe := collectTargetTables(targetTable, inputSQL, content, profile)
	if len(tablesToDescribe) == 0 && s.Authorizer.Catalog != nil {
		// If the user did not explicitly pass target_table, try matching table names mentioned in the user message against the authorized schema's table list.
		if allTables, lerr := s.Authorizer.Catalog.ListTables(runCtx, dsTarget, schemaName); lerr == nil {
			lowContent := strings.ToLower(content)
			for _, tbl := range allTables {
				if tbl != "" && strings.Contains(lowContent, strings.ToLower(tbl)) {
					tablesToDescribe = append(tablesToDescribe, tbl)
				}
			}
			if len(tablesToDescribe) == 0 && len(allTables) == 1 {
				tablesToDescribe = append(tablesToDescribe, allTables[0])
			}
		}
	}

	var anonMetadata []*tool.AnonymizedTableDescription
	for _, tblName := range tablesToDescribe {
		desc, derr := s.Authorizer.DescribeAuthorizedTable(runCtx, priv, dsTarget, schemaName, tblName)
		if derr != nil {
			return nil, derr
		}
		anonMetadata = append(anonMetadata, desc)
	}
	in.emit("tool", map[string]any{"tool": "catalog_inspector.load_schema_metadata", "status": "completed", "schema": schemaName, "tables": len(anonMetadata)})

	metaBytes, _ := json.Marshal(anonMetadata)
	if err := s.CheckSingleInputBudget(content, inputSQL, planText, string(metaBytes)); err != nil {
		return nil, err
	}

	// 5. If an existing draft is selected and inputSQL is not explicitly provided in this turn, load the selected draft in optimization mode and re-anonymize it in the current turn.
	if inputSQL == "" && capKind == domain.CapabilitySQLOptimize {
		selectedID := firstNonEmptyString(in.SelectedDraftID, sess.SelectedDraftID)
		if selectedID != "" {
			if _, dp, derr := s.loadOwnedDraft(runCtx, s.DB, actor.UserID, sess.ID, selectedID); derr == nil {
				inputSQL = dp.SQLText
			}
		}
	}

	// 6. Anonymize the current input SQL, execution plan, and natural-language prompt uniformly using this turn's TurnPrivacySession.
	var anonInputSQL string
	if inputSQL != "" {
		anonInputSQL, err = priv.AnonymizeSQL(inputSQL, profile)
		if err != nil {
			return nil, err
		}
	}
	var anonPlan string
	if planText != "" {
		anonPlan, err = priv.AnonymizeNaturalLanguage(planText)
		if err != nil {
			return nil, err
		}
	}
	anonPrompt, err := priv.AnonymizeNaturalLanguage(content)
	if err != nil {
		return nil, err
	}

	// 7. Load session history, automatically compact earlier turns within budget, and anonymize history entering the model using this turn's TurnPrivacySession.
	existingHistory, err := s.loadSessionMessagesForContext(runCtx, actor.UserID, sess.ID)
	if err != nil {
		return nil, err
	}
	anonHistory, anonSummary, compactedNow, err := s.BuildAndCompactHistory(runCtx, sess, existingHistory, priv)
	if err != nil {
		return nil, err
	}

	// Collect bound value placeholders in order for generative draft binding.
	boundPhs := extractPlaceholdersInOrder(anonPrompt + " " + anonInputSQL)

	synthIn := SynthesizeInput{
		Capability:         capKind,
		EngineProfile:      profile.String(),
		OutputLocale:       in.OutputLocale,
		AnonymizedPrompt:   anonPrompt,
		AnonymizedInputSQL: anonInputSQL,
		AnonymizedPlanText: anonPlan,
		AnonymizedMetadata: anonMetadata,
		AnonymizedHistory:  anonHistory,
		AnonymizedSummary:  anonSummary,
		BoundPlaceholders:  boundPhs,
		PlatformKnowledge:  help.RetrieveRelevantArticles(actor, content, "", 2),
		Actor:              actor,
		RawPrompt:          content,
		RawInputSQL:        inputSQL,
		RawPlanText:        planText,
		RawMetadata:        restoreSANMetadata(priv, anonMetadata),
		RawHistory:         retainRawHistory(existingHistory, anonHistory),
		RawSummary:         anonSummary,
	}

	// 8. Enforce zero-leak assertion on the entire outbound payload before sending to any model (6003 Fail Closed).
	if err := scrubAndAssertSynthesizeInput(priv, &synthIn); err != nil {
		return nil, err
	}
	outboundBytes, err := json.Marshal(synthIn)
	if err != nil {
		return nil, domain.ErrAnonymizationFailed("marshal_synthesize_input_failed")
	}
	in.emit("tool", map[string]any{"tool": "privacy_session.anonymize_and_gate", "status": "completed", "capability": string(capKind)})

	// 9. Reserve global daily AI budget (shared ai_daily_budget_usage, returning 2010 when exhausted).
	reservedMinor, budgetDay, budgetCurrency, err := s.reserveDailyBudget(runCtx, int64(len(outboundBytes))/3+256)
	if err != nil {
		return nil, err
	}

	in.emit("reasoning", map[string]any{"status": "running"})
	onDelta, onSANDelta, flushDelta := makeTurnDeltaHandlers(priv, in.emit)
	synthIn.OnDelta = onDelta
	synthIn.OnSANDelta = onSANDelta
	synthOut, err := s.Synthesizer.Synthesize(runCtx, synthIn)
	if err != nil {
		s.releaseDailyBudget(runCtx, budgetDay, budgetCurrency, reservedMinor)
		if errors.Is(err, context.Canceled) {
			return nil, domain.ErrInvalidState("Generation request cancelled")
		}
		var ade *domain.Error
		if errors.As(err, &ade) {
			return nil, ade
		}
		return nil, domain.ErrProviderFailed(err.Error())
	}
	s.releaseDailyBudget(runCtx, budgetDay, budgetCurrency, reservedMinor)

	if synthOut.OutOfScope || synthOut.ResolvedCapability == string(domain.CapabilityOutOfScope) {
		msg := strings.TrimSpace(synthOut.AssistantReply)
		if msg == "" {
			msg = "The Database AI Assistant only supports Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization; non-database requests are out of scope"
		}
		return nil, domain.ErrOutOfScope(msg)
	}
	in.emit("reasoning", map[string]any{"status": "completed"})
	restoredReply := synthOut.AssistantReply
	rawSessionTitle := synthOut.SessionTitle
	if !synthOut.UsedSANProfile {
		restoredReply = priv.RestoreTextTokens(restoredReply)
		rawSessionTitle = priv.RestoreTextTokens(rawSessionTitle)
	}
	currentTitle = summarizeLatestSessionTitle(rawSessionTitle, content, currentTitle, in.OutputLocale)
	flushDelta(restoredReply)
	in.emit("progress", map[string]any{"phase": "validating_and_restoring"})

	// 10. If the model returned an SQL draft, run ValidateRawAssistantSQL (for SAN) or ValidateAndRestoreSQL (for default)
	// for dialect parsing, statement boundary, authorized object, and parameter binding verification before structured restoration (6004).
	var draftSpec *newDraftSpec
	if strings.TrimSpace(synthOut.AnonymizedSQL) != "" && !synthOut.NeedsClarify {
		in.emit("tool", map[string]any{"tool": "sql_validator.restore_and_verify_ast", "status": "running", "engine": dsTarget.Engine})
		var (
			restored *privacy.RestorationResult
			rerr     error
		)
		if synthOut.UsedSANProfile {
			rawScope := buildAssistantRawSQLScope(runCtx, s.Authorizer, dsTarget, schemaName, synthIn.RawMetadata)
			restored, rerr = privacy.ValidateRawAssistantSQL(synthOut.AnonymizedSQL, profile, rawScope)
		} else {
			restored, rerr = privacy.ValidateAndRestoreSQL(priv, synthOut.AnonymizedSQL, profile)
			if rerr != nil {
				// If an external provider returned anonymized SQL with minor token format drift, attempt fallback SQL draft synthesis via DeterministicSynthesizer.
				if _, isProvider := s.Synthesizer.(*ProviderSynthesizer); isProvider {
					fbIn := synthIn
					fbIn.OnDelta = nil
					fbIn.OnSANDelta = nil
					if fbOut, fberr := (&DeterministicSynthesizer{}).Synthesize(runCtx, fbIn); fberr == nil && fbOut != nil && strings.TrimSpace(fbOut.AnonymizedSQL) != "" {
						if fbRestored, fbrerr := privacy.ValidateAndRestoreSQL(priv, fbOut.AnonymizedSQL, profile); fbrerr == nil {
							restored = fbRestored
							rerr = nil
						}
					}
				}
			}
		}
		if rerr != nil {
			return nil, rerr
		}
		// If the restored statement is DML/DDL, double-check that the user holds a change_review grant.
		if restored.RiskCategory != domain.RiskReadOnly && !dsTarget.HasChangeGrant {
			return nil, domain.ErrDatasourceForbidden()
		}
		in.emit("tool", map[string]any{"tool": "sql_validator.restore_and_verify_ast", "status": "completed", "engine": dsTarget.Engine, "statement_kind": string(restored.StatementKind)})
		semanticWarningsIn := synthOut.SemanticWarnings
		assumptionsIn := synthOut.Assumptions
		nextStepsIn := synthOut.NextSteps
		if !synthOut.UsedSANProfile {
			semanticWarningsIn = priv.RestoreTextSlice(semanticWarningsIn)
			assumptionsIn = priv.RestoreTextSlice(assumptionsIn)
			nextStepsIn = priv.RestoreTextSlice(nextStepsIn)
		}
		warnings := append([]string{}, semanticWarningsIn...)
		warnings = append(warnings, restored.SemanticWarnings...)
		draftSpec = &newDraftSpec{
			DatasourceID:   dsTarget.DatasourceID,
			DatasourceName: dsTarget.DatasourceName,
			FlowID:         optionalStringPtr(flowID),
			Engine:         dsTarget.Engine,
			SchemaName:     schemaName,
			StatementKind:  string(restored.StatementKind),
			RiskCategory:   string(restored.RiskCategory),
			Payload: persistedDraftPayload{
				SQLText:          restored.RestoredSQL,
				Assumptions:      ensureStringSlice(assumptionsIn),
				NextSteps:        ensureStringSlice(nextStepsIn),
				SemanticWarnings: ensureStringSlice(warnings),
				ReferencedTables: ensureStringSlice(restored.ReferencedTables),
			},
		}
	}

	// Update active session context bindings.
	sess.ActiveDatasourceID = &dsTarget.DatasourceID
	if flowID != "" {
		sess.ActiveFlowID = &flowID
	}
	if schemaName != "" {
		sess.ActiveSchemaName = &schemaName
	}

	return s.persistTurnOutcome(runCtx, actor, sess, currentTitle, content, capKind,
		persistedMessagePayload{Content: restoredReply},
		draftSpec, compactedNow, assistantMsgID)
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

func scrubAndAssertSynthesizeInput(priv *privacy.TurnPrivacySession, in *SynthesizeInput) error {
	in.AnonymizedPrompt = priv.ScrubRegisteredTerms(in.AnonymizedPrompt)
	in.AnonymizedPlanText = priv.ScrubRegisteredTerms(in.AnonymizedPlanText)
	in.AnonymizedSummary = priv.ScrubRegisteredTerms(in.AnonymizedSummary)
	for i := range in.AnonymizedHistory {
		in.AnonymizedHistory[i].Content = priv.ScrubRegisteredTerms(in.AnonymizedHistory[i].Content)
	}
	for _, v := range []string{
		in.AnonymizedPrompt,
		in.AnonymizedInputSQL,
		in.AnonymizedPlanText,
		in.AnonymizedSummary,
	} {
		if v == "" {
			continue
		}
		if err := priv.AssertZeroLeak(v); err != nil {
			return err
		}
	}
	for _, t := range in.AnonymizedHistory {
		if t.Content == "" {
			continue
		}
		if err := priv.AssertZeroLeak(t.Content); err != nil {
			return err
		}
	}
	for _, m := range in.AnonymizedMetadata {
		if m == nil {
			continue
		}
		for _, tok := range []string{m.SchemaToken, m.TableToken} {
			if tok != "" {
				if err := priv.AssertZeroLeak(tok); err != nil {
					return err
				}
			}
		}
		for _, c := range m.Columns {
			if err := priv.AssertZeroLeak(c.ColumnToken); err != nil {
				return err
			}
		}
		for _, idx := range m.Indexes {
			if err := priv.AssertZeroLeak(idx.IndexToken); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Services) persistTurnOutcome(
	ctx context.Context,
	actor domain.Actor,
	sess *persistence.AIAssistantSessionModel,
	sessionTitle string,
	userContent string,
	capKind domain.Capability,
	assistantPayload persistedMessagePayload,
	draftSpec *newDraftSpec,
	compactedNow bool,
	assistantMsgID string,
) (*domain.SendMessageResult, error) {
	now := time.Now().UTC()
	userMsgID := uuid.NewString()

	userEnv, err := s.sealMessagePayload(userMsgID, persistedMessagePayload{Content: userContent})
	if err != nil {
		return nil, err
	}
	asstEnv, err := s.sealMessagePayload(assistantMsgID, assistantPayload)
	if err != nil {
		return nil, err
	}
	titleEnv, err := s.sealSessionTitle(sess.ID, sessionTitle)
	if err != nil {
		return nil, err
	}

	var (
		userMsgRow  persistence.AIAssistantMessageModel
		asstMsgRow  persistence.AIAssistantMessageModel
		draftRow    *persistence.AIAssistantSQLDraftModel
		draftViewPt *domain.SQLDraftView
	)

	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&persistence.AIAssistantMessageModel{}).
			Where("session_id = ?", sess.ID).
			Select("coalesce(max(sequence), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}

		userMsgRow = persistence.AIAssistantMessageModel{
			ID:                userMsgID,
			SessionID:         sess.ID,
			UserID:            actor.UserID,
			Sequence:          maxSeq + 1,
			Role:              string(domain.RoleUser),
			Capability:        string(capKind),
			State:             string(domain.MessageCompleted),
			ContentCiphertext: userEnv.Ciphertext,
			ContentNonce:      userEnv.Nonce,
			ContentKeyID:      userEnv.KeyID,
			ContentAlgorithm:  userEnv.Algorithm,
			Version:           1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := tx.Create(&userMsgRow).Error; err != nil {
			return err
		}

		// Create the assistant message row with sql_draft_id = nil first (satisfying FK ai_assistant_sql_drafts.message_id -> ai_assistant_messages.id).
		asstMsgRow = persistence.AIAssistantMessageModel{
			ID:                assistantMsgID,
			SessionID:         sess.ID,
			UserID:            actor.UserID,
			Sequence:          maxSeq + 2,
			Role:              string(domain.RoleAssistant),
			Capability:        string(capKind),
			State:             string(domain.MessageCompleted),
			ContentCiphertext: asstEnv.Ciphertext,
			ContentNonce:      asstEnv.Nonce,
			ContentKeyID:      asstEnv.KeyID,
			ContentAlgorithm:  asstEnv.Algorithm,
			Version:           1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := tx.Create(&asstMsgRow).Error; err != nil {
			return err
		}

		if draftSpec != nil {
			var maxRev int64
			if err := tx.Model(&persistence.AIAssistantSQLDraftModel{}).
				Where("session_id = ?", sess.ID).
				Select("coalesce(max(revision), 0)").
				Scan(&maxRev).Error; err != nil {
				return err
			}
			// Mark earlier unsubmitted drafts in this session as stale (preventing submission of outdated draft revisions).
			if maxRev > 0 {
				if err := tx.Model(&persistence.AIAssistantSQLDraftModel{}).
					Where("session_id = ? and submission_state != ?", sess.ID, string(domain.SubmissionOrderSubmitted)).
					Updates(map[string]any{"submission_state": string(domain.SubmissionStale), "updated_at": now}).Error; err != nil {
					return err
				}
			}

			dID := uuid.NewString()
			dEnv, serr := s.sealDraftPayload(dID, draftSpec.Payload)
			if serr != nil {
				return serr
			}
			macBytes, macKeyID, herr := s.computeSQLHMAC(draftSpec.Payload.SQLText)
			if herr != nil {
				return herr
			}
			dr := persistence.AIAssistantSQLDraftModel{
				ID:              dID,
				SessionID:       sess.ID,
				MessageID:       assistantMsgID,
				UserID:          actor.UserID,
				Revision:        maxRev + 1,
				DatasourceID:    draftSpec.DatasourceID,
				FlowID:          draftSpec.FlowID,
				Engine:          draftSpec.Engine,
				SchemaName:      draftSpec.SchemaName,
				StatementKind:   draftSpec.StatementKind,
				RiskCategory:    draftSpec.RiskCategory,
				SQLCiphertext:   dEnv.Ciphertext,
				SQLNonce:        dEnv.Nonce,
				SQLKeyID:        dEnv.KeyID,
				SQLAlgorithm:    dEnv.Algorithm,
				SQLHMAC:         macBytes,
				SQLHMACKeyID:    macKeyID,
				SubmissionState: string(domain.SubmissionDraft),
				Version:         1,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := tx.Create(&dr).Error; err != nil {
				return err
			}
			if err := tx.Model(&persistence.AIAssistantMessageModel{}).
				Where("id = ?", assistantMsgID).
				Update("sql_draft_id", dID).Error; err != nil {
				return err
			}
			asstMsgRow.SQLDraftID = &dID
			sess.SelectedDraftID = &dID
			draftRow = &dr
		}

		if err := tx.Model(&persistence.AIAssistantSessionModel{}).
			Where("id = ?", sess.ID).
			Updates(map[string]any{
				"title_ciphertext":     titleEnv.Ciphertext,
				"title_nonce":          titleEnv.Nonce,
				"title_key_id":         titleEnv.KeyID,
				"title_algorithm":      titleEnv.Algorithm,
				"active_datasource_id": sess.ActiveDatasourceID,
				"active_flow_id":       sess.ActiveFlowID,
				"active_schema_name":   sess.ActiveSchemaName,
				"selected_draft_id":    sess.SelectedDraftID,
				"version":              sess.Version + 1,
				"updated_at":           now,
			}).Error; err != nil {
			return err
		}
		sess.Version++
		sess.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}

	if draftRow != nil && draftSpec != nil {
		dsName := draftSpec.DatasourceName
		dv := s.draftView(draftRow, draftSpec.Payload, &dsName)
		draftViewPt = &dv
	}

	userView := s.messageView(&userMsgRow, persistedMessagePayload{Content: userContent}, nil)
	asstView := s.messageView(&asstMsgRow, assistantPayload, draftViewPt)
	sessView := s.sessionView(sess, sessionTitle)

	return &domain.SendMessageResult{
		Session:          sessView,
		UserMessage:      userView,
		AssistantMessage: asstView,
		SQLDraft:         draftViewPt,
		ContextCompacted: compactedNow,
	}, nil
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
	return s.Crypter.Seal(crypto.PurposePayload, []byte(title), crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_sessions",
		PrimaryKey:   sessionID,
		Column:       "title",
		Purpose:      crypto.PurposePayload,
	})
}

func (s *Services) openSessionTitle(row *persistence.AIAssistantSessionModel) (string, error) {
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.TitleCiphertext,
		Nonce:      row.TitleNonce,
		KeyID:      row.TitleKeyID,
		Algorithm:  row.TitleAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_sessions",
		PrimaryKey:   row.ID,
		Column:       "title",
		Purpose:      crypto.PurposePayload,
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
	return s.Crypter.Seal(crypto.PurposePayload, raw, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_messages",
		PrimaryKey:   messageID,
		Column:       "content",
		Purpose:      crypto.PurposePayload,
	})
}

func (s *Services) openMessagePayload(row *persistence.AIAssistantMessageModel) (persistedMessagePayload, error) {
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.ContentCiphertext,
		Nonce:      row.ContentNonce,
		KeyID:      row.ContentKeyID,
		Algorithm:  row.ContentAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_messages",
		PrimaryKey:   row.ID,
		Column:       "content",
		Purpose:      crypto.PurposePayload,
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
	return s.Crypter.Seal(crypto.PurposePayload, raw, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_sql_drafts",
		PrimaryKey:   draftID,
		Column:       "sql",
		Purpose:      crypto.PurposePayload,
	})
}

func (s *Services) openDraftPayload(row *persistence.AIAssistantSQLDraftModel) (persistedDraftPayload, error) {
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.SQLCiphertext,
		Nonce:      row.SQLNonce,
		KeyID:      row.SQLKeyID,
		Algorithm:  row.SQLAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_sql_drafts",
		PrimaryKey:   row.ID,
		Column:       "sql",
		Purpose:      crypto.PurposePayload,
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
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.SummaryCiphertext,
		Nonce:      row.SummaryNonce,
		KeyID:      row.SummaryKeyID,
		Algorithm:  row.SummaryAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "ai_assistant_context_summaries",
		PrimaryKey:   row.ID,
		Column:       "summary",
		Purpose:      crypto.PurposePayload,
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

func (s *Services) reserveDailyBudget(ctx context.Context, estTokens int64) (int64, string, string, error) {
	if s.Review == nil {
		return 0, "", "", nil
	}
	settings, err := s.Review.LoadBudgetSettings(ctx, s.DB)
	if err != nil || !settings.Enforced {
		return 0, "", "", nil
	}
	amountMinor := int64(1)
	if settings.InputPricePerMillionMinor > 0 {
		calc := (estTokens * settings.InputPricePerMillionMinor) / 1_000_000
		if calc > amountMinor {
			amountMinor = calc
		}
	}
	day := time.Now().UTC().Format("2006-01-02")
	if err := s.Review.Reserve(ctx, s.DB, day, settings.Currency, settings, amountMinor); err != nil {
		if errors.Is(err, application.ErrBudgetExhausted) {
			return 0, "", "", domain.ErrBudgetExhausted()
		}
		return 0, "", "", err
	}
	return amountMinor, day, settings.Currency, nil
}

func (s *Services) releaseDailyBudget(ctx context.Context, day, currency string, amountMinor int64) {
	if s.Review == nil || amountMinor <= 0 || day == "" {
		return
	}
	_ = s.Review.Release(ctx, s.DB, day, currency, amountMinor)
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

func requiresChangeFlowPermission(capKind domain.Capability, prompt, inputSQL string) bool {
	combined := strings.ToLower(prompt + " " + inputSQL)
	for _, kw := range []string{
		"insert", "update", "delete", "create", "alter", "drop", "truncate",
		"\u63d2\u5165", "\u65b0\u589e", "\u5199\u5165", "\u66f4\u65b0", "\u4fee\u6539", "\u5220\u9664", "\u5efa\u8868", "\u52a0\u5b57\u6bb5", "\u65b0\u589e\u5b57\u6bb5", "\u521b\u5efa\u7d22\u5f15", "\u63d0\u4ea4\u5de5\u5355",
	} {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	return false
}

func collectTargetTables(explicitTable, inputSQL, prompt string, profile fingerprint.Profile) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(t string) {
		clean := strings.Trim(strings.TrimSpace(t), "`\"'")
		if clean == "" {
			return
		}
		low := strings.ToLower(clean)
		if !seen[low] {
			seen[low] = true
			out = append(out, clean)
		}
	}
	if explicitTable != "" {
		add(explicitTable)
	}
	if inputSQL != "" {
		for _, t := range ExtractTablesFromSQL(inputSQL, profile) {
			add(t)
		}
	}
	if prompt != "" {
		for _, t := range ExtractTablesFromSQL(prompt, profile) {
			add(t)
		}
	}
	return out
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
