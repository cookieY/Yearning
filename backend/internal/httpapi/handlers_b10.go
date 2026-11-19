// handlers_b10.go B10 execution domain HTTP handlers: start execution, Attempt/statement read surfaces,
// cancellation, and manual verification. Write operations are restricted to current stage frozen executors (W006, admin not exempted);
// read surface = order stakeholders; non-stakeholders receive 403 per PRD 04 S9.
package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// B10Deps aggregates execution domain dependencies.
type B10Deps struct {
	Exec     *execapp.Services
	Runner   *execapp.Runner
	Orders   *orderapp.Services
	Sessions *identity.SessionStore
	DB       *gorm.DB
}

// buildB10FromB9 constructs B10 from B9 dependencies (cmd wiring is outside B10 paths, following derivation pattern).
func buildB10FromB9(b9 *B9Deps) (*B10Deps, error) {
	svc, err := execapp.NewServices(b9.DB, b9.Review.Crypter, b9.Review.Recorder)
	if err != nil {
		return nil, err
	}
	return &B10Deps{Exec: svc, Runner: execapp.NewRunner(svc), Orders: b9.Orders, DB: b9.DB}, nil
}

// b10Actor extracts the current user.
func b10Actor(c *gin.Context) execapp.Actor {
	id := session.FromContext(c.Request.Context())
	return execapp.Actor{UserID: id.UserID, Username: id.Username}
}

// currentUserID extracts the user ID from the session.
func currentUserID(c *gin.Context) string {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		return ""
	}
	return id.UserID
}

// writeExecErr maps execution domain errors (splitting colon-suffixed reason into Meta).
func writeExecErr(c *gin.Context, err error) bool {
	var de *domain.Error
	switch {
	case err == nil:
		return true
	case errors.As(err, &de):
		name := de.Name
		var meta map[string]any
		if i := strings.Index(name, ":"); i >= 0 {
			reason := name[i+1:]
			// detail equals reason: frontend S18.75-A uniformly renders diagnostic text from meta.detail
			// (BUG-006 -- previously executors could not see preflight rejection reasons).
			meta = map[string]any{"reason": reason, "detail": reason}
			name = name[:i]
		}
		WriteBusinessError(c, &BusinessError{Code: int(de.Code), Name: name,
			Message: "Execution request rejected", Meta: meta})
	default:
		return false
	}
	return true
}

// writeForbidden403 returns uniform 403 for non-stakeholders (without leaking existence).
func writeForbidden403(c *gin.Context) {
	WriteProblem(c, http.StatusForbidden, "permission denied")
}

// HandleCreateExecutionAttempt POST /change-orders/{order_id}/execution-attempts.
// Optional body carries typed osc_overrides (RCP-20260827; unknown key / out-of-range value -> 1001).
func (d *B10Deps) HandleCreateExecutionAttempt(c *gin.Context) {
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
		DDLExecutionMode string         `json:"ddl_execution_mode"`
		OscOverrides     map[string]any `json:"osc_overrides"`
	}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed,
				Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
	}
	view, _, err := d.Exec.Begin(c.Request.Context(), execapp.BeginInput{
		OrderID: orderID, Actor: b10Actor(c), ExpectVersion: version, IdempotencyKey: key,
		OscOverrides:     req.OscOverrides,
		DDLExecutionMode: req.DDLExecutionMode,
	})
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Code == domain.CodeNotFrozenActor {
			writeForbidden403(c)
			return
		}
		if !writeExecErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleGetExecutionAttempt GET /execution-attempts/{attempt_id}.
func (d *B10Deps) HandleGetExecutionAttempt(c *gin.Context) {
	attemptID, ok := uuidParam(c, "attempt_id")
	if !ok {
		return
	}
	allowed, err := d.Exec.CanReadAttempt(c.Request.Context(), attemptID, currentUserID(c))
	if err != nil {
		if !writeExecErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	if !allowed {
		writeForbidden403(c)
		return
	}
	view, err := d.Exec.GetAttempt(c.Request.Context(), attemptID)
	if err != nil {
		if !writeExecErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleListExecutionStatements GET /execution-attempts/{attempt_id}/statements.
func (d *B10Deps) HandleListExecutionStatements(c *gin.Context) {
	attemptID, ok := uuidParam(c, "attempt_id")
	if !ok {
		return
	}
	after := strings.TrimSpace(c.Query("after"))
	if after != "" {
		if _, aerr := strconv.Atoi(after); aerr != nil || strings.HasPrefix(after, "-") {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed,
				Name: "VALIDATION_FAILED", Message: "Invalid pagination cursor"})
			return
		}
	}
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		v, lerr := strconv.Atoi(raw)
		if lerr != nil || v < 1 || v > 200 {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed,
				Name: "VALIDATION_FAILED", Message: "Invalid limit parameter"})
			return
		}
		limit = v
	}
	allowed, err := d.Exec.CanReadAttempt(c.Request.Context(), attemptID, currentUserID(c))
	if err != nil {
		if !writeExecErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	if !allowed {
		writeForbidden403(c)
		return
	}
	items, next, hasMore, err := d.Exec.ListStatements(c.Request.Context(), attemptID, after, limit)
	if err != nil {
		if !writeExecErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	if hasMore && next != "" {
		writePage(c, items, &next)
		return
	}
	writePage(c, items, nil)
}

// HandleCancelExecutionAttempt POST /execution-attempts/{attempt_id}/cancellation.
func (d *B10Deps) HandleCancelExecutionAttempt(c *gin.Context) {
	attemptID, ok := uuidParam(c, "attempt_id")
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
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed,
			Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	var orderID string
	if err := d.DB.WithContext(c.Request.Context()).Raw(
		`select order_id::text from execution_attempts where id = ?`, attemptID).
		Scan(&orderID).Error; err != nil || orderID == "" {
		writeForbidden403(c)
		return
	}
	view, _, err := d.Exec.Cancel(c.Request.Context(), execapp.CancelInput{
		AttemptID: attemptID, OrderID: orderID, Actor: b10Actor(c),
		ExpectVersion: version, IdempotencyKey: key, Reason: req.Reason,
	}, d.Runner.CancelActive)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Code == domain.CodeNotFrozenActor {
			writeForbidden403(c)
			return
		}
		if !writeExecErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleCreateExecutionVerification POST /execution-attempts/{attempt_id}/verifications.
func (d *B10Deps) HandleCreateExecutionVerification(c *gin.Context) {
	attemptID, ok := uuidParam(c, "attempt_id")
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
		Result   string `json:"result" binding:"required"`
		Reason   string `json:"reason" binding:"required,max=4096"`
		Evidence []struct {
			Kind    string `json:"kind" binding:"required"`
			Content string `json:"content" binding:"required,max=8192"`
		} `json:"evidence" binding:"required,dive"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed,
			Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	in := execapp.VerifyInput{
		AttemptID: attemptID, Actor: b10Actor(c), ExpectVersion: version,
		Result: req.Result, Reason: req.Reason, IdempotencyKey: key,
	}
	for _, e := range req.Evidence {
		in.Evidence = append(in.Evidence, execapp.EvidenceItem{Kind: e.Kind, Content: e.Content})
	}
	orderID, _, err := d.Exec.Verify(c.Request.Context(), in)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) && de.Code == domain.CodeNotFrozenActor {
			writeForbidden403(c)
			return
		}
		if !writeExecErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	// Response = updated change order view after verification (OpenAPI createExecutionVerification 200
	// data=ChangeOrder).
	actor := b10Actor(c)
	view, verr := d.Orders.GetOrder(c.Request.Context(), orderapp.Actor{
		UserID: actor.UserID, Username: actor.Username,
	}, orderID)
	if verr != nil {
		if !writeExecErr(c, verr) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}
