// handlers_orders.go change order domain HTTP handlers: submission, approval, withdrawal/voidance, and read surfaces.
// Responses follow OpenAPI envelopes; actor relationship failures return 1002 (without leaking existence);
// withdrawal/voidance by non-submitter returns 403 per PRD S9 (admin not exempted).
package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/platform/systz"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

// B9Deps aggregates B9 domain dependencies.
type B9Deps struct {
	Orders   *application.Services
	Review   *reviewapp.Services
	Sessions *identity.SessionStore
	DB       *gorm.DB
}

// buildB9FromB6 constructs B9 from B6 dependencies (cmd wiring is outside B9 paths, following derivation pattern).
func buildB9FromB6(b6 *B6Deps) (*B9Deps, error) {
	svc, err := application.NewServices(b6.DB, b6.Review.Crypter, b6.Review.Recorder)
	if err != nil {
		return nil, err
	}
	return &B9Deps{Orders: svc, Review: b6.Review, Sessions: b6.Sessions, DB: b6.DB}, nil
}

// b9Actor extracts the current user.
func b9Actor(c *gin.Context) application.Actor {
	id := session.FromContext(c.Request.Context())
	return application.Actor{UserID: id.UserID, Username: id.Username}
}

func b9Meta(c *gin.Context) application.RequestMeta {
	return application.RequestMeta{
		RequestID: RequestID(c), SourceIP: c.ClientIP(), UserAgent: c.GetHeader("User-Agent"),
	}
}

// requireCursor validates "ts|id" cursor shape via pagecursor.Decode (invalid cursor returns 1001).
func requireCursor(c *gin.Context) (string, bool) {
	raw := c.Query("after")
	if raw != "" {
		if _, _, err := pagecursor.Decode(raw); err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid pagination cursor"})
			return "", false
		}
	}
	return raw, true
}

// requireSeqCursor validates numeric sequence cursor for timeline.
func requireSeqCursor(c *gin.Context) (string, bool) {
	raw := strings.TrimSpace(c.Query("after"))
	if raw == "" {
		return raw, true
	}
	if _, err := strconv.ParseInt(raw, 10, 64); err != nil || raw[0] == '-' {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid pagination cursor"})
		return "", false
	}
	return raw, true
}

func idemKey(c *gin.Context) (string, bool) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid Idempotency-Key"})
		return "", false
	}
	return key, true
}

// HandleSubmitOrderV2 POST /change-orders (RCP-20261101-EPHEMERAL-PRECHECK).
// Submission carries {flow_id, current SQL, ephemeral run_id, metadata}; server verifies SQL against
// reviewed SQL, materializes Run, and freezes change order in a single transaction. Carries draft_id in draft mode, omitted in direct submit.
func (d *B9Deps) HandleSubmitOrderV2(c *gin.Context) {
	var body struct {
		FlowID            string          `json:"flow_id"`
		DraftID           string          `json:"draft_id"`
		SQL               string          `json:"sql"`
		RunID             string          `json:"run_id"`
		TargetDatabaseRaw json.RawMessage `json:"target_database"`
		RollbackEnabled   *bool           `json:"rollback_enabled"`
		Meta              *struct {
			Title                 *string `json:"title"`
			Description           *string `json:"description"`
			RequestedScheduledFor *string `json:"requested_scheduled_for"`
			RollbackEnabled       *bool   `json:"rollback_enabled"`
		} `json:"metadata"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request body"})
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	targetDBSet, targetDB, ok := parseOptionalTargetDatabaseRaw(c, body.TargetDatabaseRaw)
	if !ok {
		return
	}
	var title, description *string
	var requested *time.Time
	rollbackEnabled := body.RollbackEnabled
	if body.Meta != nil {
		title = body.Meta.Title
		description = body.Meta.Description
		if body.Meta.RollbackEnabled != nil {
			rollbackEnabled = body.Meta.RollbackEnabled
		}
		if body.Meta.RequestedScheduledFor != nil && *body.Meta.RequestedScheduledFor != "" {
			t, perr := time.Parse(time.RFC3339, *body.Meta.RequestedScheduledFor)
			if perr != nil {
				WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid requested_scheduled_for"})
				return
			}
			requested = &t
		}
	}
	view, _, err := d.Orders.SubmitOrderV2(c.Request.Context(), b9Actor(c), d.Review,
		application.SubmitOrderV2Input{
			FlowID: body.FlowID, DraftID: body.DraftID,
			SQL: []byte(body.SQL), RunID: body.RunID,
			TargetDatabase: targetDB, TargetDatabaseSet: targetDBSet,
			Title: title, Description: description, RequestedScheduledFor: requested,
			RollbackEnabled: rollbackEnabled,
			IdempotencyKey:  key,
		})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, projectOrderForDevViewAs(c, view))
}

// HandleListOrders GET /change-orders.
func (d *B9Deps) HandleListOrders(c *gin.Context) {
	after, ok := requireCursor(c)
	if !ok {
		return
	}
	var filter application.OrderListFilter
	if rawState := strings.TrimSpace(c.Query("state")); rawState != "" {
		if !domain.ValidOrderState(domain.OrderState(rawState)) {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid order state filter"})
			return
		}
		filter.State = rawState
	}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		if utf8.RuneCountInString(q) > 128 {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid search keyword filter"})
			return
		}
		filter.Q = q
	}
	if ds := strings.TrimSpace(c.Query("datasource")); ds != "" {
		if utf8.RuneCountInString(ds) > 128 {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid datasource filter"})
			return
		}
		filter.Datasource = ds
	}
	rawFrom := strings.TrimSpace(c.Query("submitted_from"))
	rawTo := strings.TrimSpace(c.Query("submitted_to"))
	if rawFrom != "" || rawTo != "" {
		tzLoc := systz.Location(c.Request.Context(), d.DB)
		if rawFrom != "" {
			startUTC, _, err := systz.DayRange(rawFrom, tzLoc)
			if err != nil {
				WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid submitted_from date filter"})
				return
			}
			filter.SubmittedFrom = &startUTC
		}
		if rawTo != "" {
			_, endExclusiveUTC, err := systz.DayRange(rawTo, tzLoc)
			if err != nil {
				WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid submitted_to date filter"})
				return
			}
			filter.SubmittedToExclusive = &endExclusiveUTC
		}
	}
	items, next, err := d.Orders.ListOrders(c.Request.Context(), b9Actor(c),
		intQuery(c, "limit", 50), after, filter)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	items = filterOrdersForDevViewAs(c, items)
	writePage(c, items, next)
}

// HandleGetOrder GET /change-orders/{order_id}.
func (d *B9Deps) HandleGetOrder(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	view, err := d.Orders.GetOrder(c.Request.Context(), b9Actor(c), orderID)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, projectOrderForDevViewAs(c, view))
}

// HandleDecideOrder POST /change-orders/{order_id}/approval-decisions.
func (d *B9Deps) HandleDecideOrder(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Decision string `json:"decision" binding:"required,oneof=approve reject"`
		Comment  string `json:"comment" binding:"max=4096"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if devViewAsBlocksApproval(c) {
		writeCodedError(c, domain.ErrNotFrozenActor())
		return
	}
	view, _, err := d.Orders.Decide(c.Request.Context(), b9Actor(c), b9Meta(c),
		application.DecideInput{OrderID: orderID, ExpectVersion: version,
			Decision: req.Decision, Comment: req.Comment, IdempotencyKey: key})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, projectOrderForDevViewAs(c, view))
}

// requireSubmitter withdrawal/voidance pre-check: submitter only (PRD S9 non-submitter gets 403,
// admin not exempted).
func (d *B9Deps) requireSubmitter(c *gin.Context, orderID string) bool {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		WriteProblem(c, http.StatusUnauthorized, "authentication required")
		return false
	}
	var submitter string
	if err := d.DB.WithContext(c.Request.Context()).Raw(
		`select submitter_user_id_snapshot::text from change_orders where id = ?`, orderID).
		Scan(&submitter).Error; err != nil || submitter == "" {
		// Non-existent order also returns 403 externally (does not leak existence; business layer has 1002 as second line of defense).
		WriteProblem(c, http.StatusForbidden, "permission denied")
		return false
	}
	if submitter != id.UserID {
		WriteProblem(c, http.StatusForbidden, "permission denied")
		return false
	}
	return true
}

// HandleWithdrawOrder POST /change-orders/{order_id}/withdrawal.
func (d *B9Deps) HandleWithdrawOrder(c *gin.Context) {
	d.handleLifecycle(c, "withdrawal")
}

// HandleVoidOrder POST /change-orders/{order_id}/voidance.
func (d *B9Deps) HandleVoidOrder(c *gin.Context) {
	d.handleLifecycle(c, "voidance")
}

func (d *B9Deps) handleLifecycle(c *gin.Context, kind string) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason" binding:"required,min=1,max=4096"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if !d.requireSubmitter(c, orderID) {
		return
	}
	in := application.LifecycleInput{OrderID: orderID, ExpectVersion: version,
		Reason: req.Reason, IdempotencyKey: key}
	var view *application.OrderView
	var err error
	if kind == "withdrawal" {
		view, _, err = d.Orders.Withdraw(c.Request.Context(), b9Actor(c), b9Meta(c), in)
	} else {
		view, _, err = d.Orders.Void(c.Request.Context(), b9Actor(c), b9Meta(c), in)
	}
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, projectOrderForDevViewAs(c, view))
}

// HandleListTimeline GET /change-orders/{order_id}/timeline.
func (d *B9Deps) HandleListTimeline(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	after, ok := requireSeqCursor(c)
	if !ok {
		return
	}
	items, next, err := d.Orders.ListTimeline(c.Request.Context(), b9Actor(c), orderID,
		intQuery(c, "limit", 50), after)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

// HandleListComments GET /change-orders/{order_id}/comments.
func (d *B9Deps) HandleListComments(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	after, ok := requireCursor(c)
	if !ok {
		return
	}
	items, next, err := d.Orders.ListComments(c.Request.Context(), b9Actor(c), orderID,
		intQuery(c, "limit", 50), after)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

// HandleCreateComment POST /change-orders/{order_id}/comments.
func (d *B9Deps) HandleCreateComment(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Content string `json:"content" binding:"required,min=1,max=4096"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, _, err := d.Orders.CreateComment(c.Request.Context(), b9Actor(c), b9Meta(c),
		orderID, req.Content, key)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleCopyToDraft POST /change-orders/{order_id}/draft-copies.
func (d *B9Deps) HandleCopyToDraft(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		TargetFlowID string `json:"target_flow_id" binding:"required,uuid"`
		Title        string `json:"title" binding:"required,min=1,max=256"`
		Description  string `json:"description" binding:"max=4096"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	// Copy belongs to submitter-lifecycle (PRD: only submitter can copy).
	if !d.requireSubmitter(c, orderID) {
		return
	}
	view, _, err := d.Orders.CopyToDraft(c.Request.Context(), b9Actor(c), b9Meta(c),
		application.CopyInput{OrderID: orderID, TargetFlowID: req.TargetFlowID,
			Title: req.Title, Description: req.Description, IdempotencyKey: key})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	// Response is a full ChangeDraft view (OpenAPI data: ChangeDraft).
	draft, err := d.Review.GetDraft(c.Request.Context(), reviewapp.Actor{
		UserID: b9Actor(c).UserID, Username: b9Actor(c).Username,
	}, view.DraftID)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, draft)
}

// HandleCreateRollbackDraft POST /change-orders/{order_id}/rollback-drafts.
func (d *B9Deps) HandleCreateRollbackDraft(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	view, _, err := d.Orders.CreateRollbackDraft(c.Request.Context(), b9Actor(c), b9Meta(c),
		application.RollbackDraftInput{OrderID: orderID, IdempotencyKey: key})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	draft, err := d.Review.GetDraft(c.Request.Context(), reviewapp.Actor{
		UserID: b9Actor(c).UserID, Username: b9Actor(c).Username,
	}, view.DraftID)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, draft)
}

// HandleRevealOrderSql POST /change-orders/{order_id}/sql-reveals.
func (d *B9Deps) HandleRevealOrderSql(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	var req revealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	res, err := d.Orders.RevealOrderSQL(c.Request.Context(), b9Actor(c), b9Meta(c), orderID, req.Purpose)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, gin.H{
		"reveal_id": res.RevealID, "sql": res.SQL,
		"watermark": res.Watermark, "valid_until": res.ValidUntil,
	})
}

// HandleRecordSqlCopy POST /change-orders/{order_id}/sql-copy-events.
func (d *B9Deps) HandleRecordSqlCopy(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	var req struct {
		RevealID string `json:"source_reveal_id" binding:"required,uuid"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if err := d.Orders.RecordSQLCopy(c.Request.Context(), b9Actor(c), b9Meta(c), orderID, req.RevealID); err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// HandleListOrderFindings GET /change-orders/{order_id}/review-findings.
func (d *B9Deps) HandleListOrderFindings(c *gin.Context) {
	orderID, ok := uuidParam(c, "order_id")
	if !ok {
		return
	}
	after, ok := requireCursor(c)
	if !ok {
		return
	}
	items, next, err := d.Orders.ListOrderFindings(c.Request.Context(), b9Actor(c), orderID,
		intQuery(c, "limit", 50), after, b9Meta(c))
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}
