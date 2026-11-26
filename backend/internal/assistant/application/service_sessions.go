// service_sessions.go holds service domain helpers and operations.
package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/assistant/tool"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/platform/pagination"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"strings"
	"sync"
	"time"
)

const (
	timeFormatRFC3339 = "2006-01-02T15:04:05.000Z07:00"

	defaultSessionTitleEN = "New Chat"
	// Localized zh-CN equivalent of "New Chat" (kept Unicode-escaped per Go pure-English rule;
	// matches frontend i18n key assistant.newSessionDefaultTitle).
	defaultSessionTitleZH = "\u65b0\u5bf9\u8bdd"

	missingDatasourceClarifyEN = "### Please Select a Target Datasource\n\nPlease select a **Target Datasource** (along with schema and table) at the top of the panel so the assistant can inspect authorized table metadata."
	// Localized zh-CN equivalent prompting the user to select a Target Datasource and schema/table first.
	missingDatasourceClarifyZH = "### \u8bf7\u5148\u9009\u62e9\u76ee\u6807\u6570\u636e\u6e90\n\n\u8bf7\u5148\u5728\u9762\u677f\u9876\u90e8\u9009\u62e9**\u76ee\u6807\u6570\u636e\u6e90**\uff08\u4ee5\u53ca\u76ee\u6807\u5e93/Schema \u548c\u8868\u540d\uff09\uff0c\u4ee5\u4fbf\u52a9\u624b\u5728\u60a8\u7684\u6388\u6743\u8303\u56f4\u5185\u8bfb\u53d6\u8868\u7ed3\u6784\u5e76\u751f\u6210\u6216\u5206\u6790 SQL\u3002"
)

// zhChangeActionKeywords holds Chinese DML/DDL intent verbs ("insert", "add", "write", "update",
// "modify", "delete", "create table", "add column", "add field", "create index", "submit order")
// kept Unicode-escaped to preserve pure-English Go source files.
var zhChangeActionKeywords = []string{
	"\u63d2\u5165",
	"\u65b0\u589e",
	"\u5199\u5165",
	"\u66f4\u65b0",
	"\u4fee\u6539",
	"\u5220\u9664",
	"\u5efa\u8868",
	"\u52a0\u5b57\u6bb5",
	"\u65b0\u589e\u5b57\u6bb5",
	"\u521b\u5efa\u7d22\u5f15",
	"\u63d0\u4ea4\u5de5\u5355",
}

func defaultSessionTitle(outputLocale string) string {
	if isEnglishLocale(outputLocale) {
		return defaultSessionTitleEN
	}
	return defaultSessionTitleZH
}

func isDefaultSessionTitle(title string) bool {
	return title == defaultSessionTitleZH || title == defaultSessionTitleEN || title == ""
}

func missingDatasourceClarifyMarkdown(outputLocale string) string {
	if isEnglishLocale(outputLocale) {
		return missingDatasourceClarifyEN
	}
	return missingDatasourceClarifyZH
}

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
	return &Services{
		DB:            db,
		Crypter:       crypter,
		Review:        reviewSvc,
		ReviewAgent:   reviewAgent,
		Orders:        orderSvc,
		Authorizer:    auth,
		Synthesizer:   NewProviderSynthesizer(db, crypter, nil),
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
		title = defaultSessionTitle(in.OutputLocale)
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
	limit = pagination.ClampLimit(limit, 50, 200)
	now := time.Now().UTC()
	q := s.DB.WithContext(ctx).Model(&persistence.AIAssistantSessionModel{}).
		Where("user_id = ? and state = 'active' and expires_at > ?", actor.UserID, now)

	if after != "" {
		ts, id, err := pagecursor.DecodeUUID(after)
		if err != nil {
			return nil, nil, domain.ErrValidation("Invalid pagination cursor")
		}
		q = q.Where("(updated_at, id) < (?, ?)", ts, id)
	}

	var rows []persistence.AIAssistantSessionModel
	if err := q.Order("updated_at desc, id desc").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, nil, err
	}

	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		cur := pagecursor.Encode(last.UpdatedAt, last.ID)
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
	return defaultSessionTitle(outputLocale)
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
