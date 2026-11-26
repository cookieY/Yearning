// handlers_schedule.go execution schedule domain HTTP handlers (E007): createExecutionSchedule
// and cancelExecutionSchedule restricted to frozen executors.
package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/execution/application"
	execdomain "github.com/cookieY/YearningX/backend/internal/execution/domain"
)

// ---- Scheduling (E007) ----

func (d *B11Deps) HandleCreateExecutionSchedule(c *gin.Context) {
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
		ScheduledFor string `json:"scheduled_for"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	at, err := time.Parse(time.RFC3339Nano, req.ScheduledFor)
	if err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if devViewAsBlocksExecution(c) {
		writeForbidden403(c)
		return
	}
	view, _, err := d.Exec.CreateSchedule(c.Request.Context(), application.CreateScheduleInput{
		OrderID: orderID, Actor: application.Actor{
			UserID: b11Actor(c).UserID, Username: b11Actor(c).Username,
		}, ScheduledFor: at, ExpectVersion: version, IdempotencyKey: key,
	})
	if err != nil {
		var de *execdomain.Error
		if errors.As(err, &de) && de.Code == execdomain.CodeNotFrozenActor {
			writeForbidden403(c)
			return
		}
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleCancelExecutionSchedule POST
// /change-orders/{order_id}/execution-schedules/cancellation (owner decision
// 2026-10-30): any frozen executor cancels a pending schedule, returning order to stage_execution_pending.
// Mutually exclusive with due claim via schedule row CAS (first committer wins, loser receives 1004/1010).
func (d *B11Deps) HandleCancelExecutionSchedule(c *gin.Context) {
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
	if devViewAsBlocksExecution(c) {
		writeForbidden403(c)
		return
	}
	view, _, err := d.Exec.CancelSchedule(c.Request.Context(), application.CancelScheduleInput{
		OrderID: orderID, Actor: application.Actor{
			UserID: b11Actor(c).UserID, Username: b11Actor(c).Username,
		}, ExpectVersion: version, IdempotencyKey: key,
	})
	if err != nil {
		var de *execdomain.Error
		if errors.As(err, &de) && de.Code == execdomain.CodeNotFrozenActor {
			writeForbidden403(c)
			return
		}
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}
