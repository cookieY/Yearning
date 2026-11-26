// handlers_assistant.go implements the HTTP handlers for the Database AI Assistant (BE-B13-AI-ASSISTANT).
// Follows the OpenAPI /ai-assistant/sessions* contract and standard 3-field response envelope;
// all sensitive responses carry Cache-Control: no-store; cross-user resource lookups return 1002,
// and unauthorized datasource access returns 6002 without disclosing restricted object existence.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	assistantapp "github.com/cookieY/YearningX/backend/internal/assistant/application"
	assistantdomain "github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/authorization"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

func resolveRequestOutputLocale(c *gin.Context, explicit *string) string {
	if explicit != nil && strings.TrimSpace(*explicit) != "" {
		return reviewapp.ResolveOutputLocale(*explicit)
	}
	return reviewapp.ResolveOutputLocale(c.GetHeader("Accept-Language"))
}

// AssistantDeps aggregates HTTP route dependencies for the Database AI Assistant.
type AssistantDeps struct {
	Assistant *assistantapp.Services
	B6        *B6Deps
	B9        *B9Deps
}

func buildAssistantDeps(b3 *B3Deps, b6 *B6Deps, b9 *B9Deps) (*AssistantDeps, error) {
	if b3 == nil || b3.DB == nil || b3.Crypter == nil || b6 == nil {
		return nil, errors.New("assistant: missing platform dependencies")
	}
	var b9Orders = b9.Orders
	svc, err := assistantapp.NewServices(b3.DB, b3.Crypter, b6.Review, b6.AgentRunner(), b9Orders)
	if err != nil {
		return nil, err
	}
	return &AssistantDeps{
		Assistant: svc,
		B6:        b6,
		B9:        b9,
	}, nil
}

func assistantActor(c *gin.Context) assistantdomain.Actor {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		return assistantdomain.Actor{}
	}
	return assistantdomain.Actor{
		UserID:         id.UserID,
		Username:       id.Username,
		IsBuiltinAdmin: authorization.Classify(id) == authorization.ActorBuiltinAdmin,
	}
}

// HandleListAssistantSessions handles GET /ai-assistant/sessions.
func (d *AssistantDeps) HandleListAssistantSessions(c *gin.Context) {
	after, ok := requireCursor(c)
	if !ok {
		return
	}
	items, next, err := d.Assistant.ListSessions(c.Request.Context(), assistantActor(c), intQuery(c, "limit", 50), after)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

// HandleCreateAssistantSession handles POST /ai-assistant/sessions.
func (d *AssistantDeps) HandleCreateAssistantSession(c *gin.Context) {
	var body struct {
		Title              *string `json:"title"`
		ActiveDatasourceID *string `json:"active_datasource_id"`
		ActiveFlowID       *string `json:"active_flow_id"`
		ActiveSchemaName   *string `json:"active_schema_name"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
	}
	if body.ActiveDatasourceID != nil && *body.ActiveDatasourceID != "" && !isValidUUID(*body.ActiveDatasourceID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid active_datasource_id"})
		return
	}
	if body.ActiveFlowID != nil && *body.ActiveFlowID != "" && !isValidUUID(*body.ActiveFlowID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid active_flow_id"})
		return
	}
	title := ""
	if body.Title != nil {
		title = *body.Title
	}
	view, err := d.Assistant.CreateSession(c.Request.Context(), assistantActor(c), assistantapp.CreateSessionInput{
		Title:              title,
		ActiveDatasourceID: body.ActiveDatasourceID,
		ActiveFlowID:       body.ActiveFlowID,
		ActiveSchemaName:   body.ActiveSchemaName,
		OutputLocale:       resolveRequestOutputLocale(c, nil),
	})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleGetAssistantSession handles GET /ai-assistant/sessions/{session_id}.
func (d *AssistantDeps) HandleGetAssistantSession(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	view, err := d.Assistant.GetSessionDetail(c.Request.Context(), assistantActor(c), sessionID)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, view)
}

// HandleDeleteAssistantSession handles DELETE /ai-assistant/sessions/{session_id}.
func (d *AssistantDeps) HandleDeleteAssistantSession(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	if err := d.Assistant.DeleteSession(c.Request.Context(), assistantActor(c), sessionID, version); err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// HandleSendAssistantMessage handles POST /ai-assistant/sessions/{session_id}/messages.
func (d *AssistantDeps) HandleSendAssistantMessage(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	var body struct {
		Content           string  `json:"content"`
		CapabilityHint    *string `json:"capability_hint"`
		OutputLocale      *string `json:"output_locale"`
		DatasourceID      *string `json:"datasource_id"`
		FlowID            *string `json:"flow_id"`
		SchemaName        *string `json:"schema_name"`
		TargetTable       *string `json:"target_table"`
		SelectedDraftID   *string `json:"selected_draft_id"`
		InputSQL          *string `json:"input_sql"`
		ExecutionPlanText *string `json:"execution_plan_text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request body"})
		return
	}
	if body.DatasourceID != nil && *body.DatasourceID != "" && !isValidUUID(*body.DatasourceID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid datasource_id"})
		return
	}
	if body.FlowID != nil && *body.FlowID != "" && !isValidUUID(*body.FlowID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid flow_id"})
		return
	}
	if body.SelectedDraftID != nil && *body.SelectedDraftID != "" && !isValidUUID(*body.SelectedDraftID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid selected_draft_id"})
		return
	}
	hint := ""
	if body.CapabilityHint != nil {
		hint = *body.CapabilityHint
	}
	wantsSSE := strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream")
	sseHeadersSent := false
	writeSSEFrame := func(event string, data any) {
		if !sseHeadersSent {
			c.Header("Cache-Control", "no-store")
			c.Header("Content-Type", "text/event-stream; charset=utf-8")
			c.Header("Connection", "keep-alive")
			c.Header("X-Accel-Buffering", "no")
			c.Status(http.StatusOK)
			sseHeadersSent = true
		}
		payload, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, string(payload))
		c.Writer.Flush()
	}

	var onStream func(assistantapp.StreamEvent)
	if wantsSSE {
		onStream = func(ev assistantapp.StreamEvent) {
			writeSSEFrame(ev.Event, ev.Data)
		}
	}

	res, err := d.Assistant.SendMessage(c.Request.Context(), assistantActor(c), assistantapp.SendMessageInput{
		SessionID:         sessionID,
		Content:           body.Content,
		CapabilityHint:    hint,
		OutputLocale:      resolveRequestOutputLocale(c, body.OutputLocale),
		DatasourceID:      body.DatasourceID,
		FlowID:            body.FlowID,
		SchemaName:        body.SchemaName,
		TargetTable:       body.TargetTable,
		SelectedDraftID:   body.SelectedDraftID,
		InputSQL:          body.InputSQL,
		ExecutionPlanText: body.ExecutionPlanText,
		OnStreamEvent:     onStream,
	})
	if err != nil {
		if wantsSSE && sseHeadersSent {
			var de *assistantdomain.Error
			if errors.As(err, &de) {
				msg := de.Message
				if msg == "" {
					msg = "Business operation failed"
				}
				writeSSEFrame("error", map[string]any{
					"err_code": de.Code,
					"err_name": de.Name,
					"message":  msg,
					"meta":     de.Meta,
				})
			} else {
				writeSSEFrame("error", map[string]any{
					"err_code": 5000,
					"err_name": "INTERNAL_ERROR",
					"message":  "Service temporarily unavailable; please try again later",
				})
			}
			return
		}
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	if wantsSSE {
		writeSSEFrame("done", res)
		return
	}
	WriteSuccess(c, res)
}

// HandleCancelAssistantMessageStream POST /ai-assistant/sessions/{session_id}/messages/{message_id}/cancellation.
func (d *AssistantDeps) HandleCancelAssistantMessageStream(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	messageID, ok := uuidParam(c, "message_id")
	if !ok {
		return
	}
	view, err := d.Assistant.CancelMessageStream(c.Request.Context(), assistantActor(c), sessionID, messageID)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandlePrecheckAssistantSqlDraft POST /ai-assistant/sessions/{session_id}/drafts/{draft_id}/prechecks.
func (d *AssistantDeps) HandlePrecheckAssistantSqlDraft(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	draftID, ok := uuidParam(c, "draft_id")
	if !ok {
		return
	}
	var body struct {
		FlowID       string  `json:"flow_id"`
		Revision     int64   `json:"revision"`
		SQLHMAC      string  `json:"sql_hmac"`
		OutputLocale *string `json:"output_locale"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Revision < 1 || strings.TrimSpace(body.SQLHMAC) == "" || !isValidUUID(body.FlowID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	locale := resolveRequestOutputLocale(c, body.OutputLocale)
	res, err := d.Assistant.PrecheckDraft(c.Request.Context(), assistantActor(c), assistantapp.PrecheckDraftInput{
		SessionID:      sessionID,
		DraftID:        draftID,
		FlowID:         body.FlowID,
		Revision:       body.Revision,
		SQLHMAC:        body.SQLHMAC,
		OutputLocale:   locale,
		IdempotencyKey: strings.TrimSpace(c.GetHeader("Idempotency-Key")),
	})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, res)
}

// HandleSubmitAssistantSqlDraftOrder POST /ai-assistant/sessions/{session_id}/drafts/{draft_id}/order-submissions.
func (d *AssistantDeps) HandleSubmitAssistantSqlDraftOrder(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	draftID, ok := uuidParam(c, "draft_id")
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var body struct {
		FlowID                string  `json:"flow_id"`
		RunID                 string  `json:"run_id"`
		Revision              int64   `json:"revision"`
		SQLHMAC               string  `json:"sql_hmac"`
		Title                 string  `json:"title"`
		Description           *string `json:"description"`
		RequestedScheduledFor *string `json:"requested_scheduled_for"`
		RollbackEnabled       *bool   `json:"rollback_enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Revision < 1 || !isValidUUID(body.FlowID) || !isValidUUID(body.RunID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	var requested *time.Time
	if body.RequestedScheduledFor != nil && *body.RequestedScheduledFor != "" {
		t, perr := time.Parse(time.RFC3339, *body.RequestedScheduledFor)
		if perr != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid requested_scheduled_for format"})
			return
		}
		requested = &t
	}
	orderView, err := d.Assistant.SubmitDraftOrder(c.Request.Context(), assistantActor(c), assistantapp.SubmitDraftOrderInput{
		SessionID:             sessionID,
		DraftID:               draftID,
		FlowID:                body.FlowID,
		RunID:                 body.RunID,
		Revision:              body.Revision,
		SQLHMAC:               body.SQLHMAC,
		Title:                 body.Title,
		Description:           body.Description,
		RequestedScheduledFor: requested,
		RollbackEnabled:       body.RollbackEnabled,
		IdempotencyKey:        key,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, orderView)
}

func isValidUUID(raw string) bool {
	if len(raw) != 36 {
		return false
	}
	for i, ch := range raw {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}

func mountAssistantRoutes(engine *gin.Engine, deps ServeDeps) {
	if deps.Assistant == nil || deps.B6 == nil {
		return
	}
	guard := func(op string) gin.HandlerFunc {
		return RequireBusinessOperation(op, businessSessions(deps))
	}
	api := engine.Group("/api/v4/ai-assistant/sessions")
	api.GET("", guard("listAssistantSessions"), deps.Assistant.HandleListAssistantSessions)
	api.POST("", guard("createAssistantSession"), deps.Assistant.HandleCreateAssistantSession)
	api.GET("/:session_id", guard("getAssistantSession"), deps.Assistant.HandleGetAssistantSession)
	api.DELETE("/:session_id", guard("deleteAssistantSession"), deps.Assistant.HandleDeleteAssistantSession)
	api.POST("/:session_id/messages", guard("sendAssistantMessage"), deps.Assistant.HandleSendAssistantMessage)
	api.POST("/:session_id/messages/:message_id/cancellation", guard("cancelAssistantMessageStream"), deps.Assistant.HandleCancelAssistantMessageStream)
	api.POST("/:session_id/drafts/:draft_id/prechecks", guard("precheckAssistantSqlDraft"), deps.Assistant.HandlePrecheckAssistantSqlDraft)
	api.POST("/:session_id/drafts/:draft_id/order-submissions", guard("submitAssistantSqlDraftOrder"), deps.Assistant.HandleSubmitAssistantSqlDraftOrder)
}
