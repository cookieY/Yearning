// handlers_workflow.go workflow domain HTTP handlers: current user flow catalog,
// approval flows CRUD, and permission groups CRUD.
package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/workflow"
)

func (d *B3Deps) HandleCurrentUserFlows(c *gin.Context) {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		WriteProblem(c, http.StatusUnauthorized, "authentication required")
		return
	}
	items, next, err := d.Workflow.CurrentUserFlows(c.Request.Context(), id.UserID, c.Query("flow_type"), intQuery(c, "limit", 50), c.Query("after"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

// ---- Management surface: Flows ----

func bindFlowWrite(c *gin.Context) (*workflow.FlowWrite, bool) {
	var w workflow.FlowWrite
	if err := c.ShouldBindJSON(&w); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return nil, false
	}
	return &w, true
}

func (d *B3Deps) HandleListFlows(c *gin.Context) {
	flowType, ok := filterEnumQuery(c, "flow_type", "change_review", "query_access")
	if !ok {
		return
	}
	enabled, ok := filterBoolQuery(c, "enabled")
	if !ok {
		return
	}
	items, next, err := d.Workflow.List(c.Request.Context(), intQuery(c, "limit", 50), c.Query("after"), c.Query("q"), flowType, enabled)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B3Deps) HandleCreateFlow(c *gin.Context) {
	w, ok := bindFlowWrite(c)
	if !ok {
		return
	}
	view, err := d.Workflow.CreateFlow(c.Request.Context(), workflowActorOf(c), w, workflowMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleGetFlow(c *gin.Context) {
	view, err := d.Workflow.Get(c.Request.Context(), c.Param("flow_id"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleReplaceFlow(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	w, ok := bindFlowWrite(c)
	if !ok {
		return
	}
	view, err := d.Workflow.ReplaceFlow(c.Request.Context(), workflowActorOf(c), c.Param("flow_id"), version, w, workflowMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleDeleteFlow(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	err := d.Workflow.DeleteFlow(c.Request.Context(), workflowActorOf(c), c.Param("flow_id"), version, workflowMeta(c))
	if err != nil {
		if !writeDomainError(c, err, bizerr.CodeFlowReferenced) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// ---- Management surface: Permission groups ----

func (d *B3Deps) HandleListPermissionGroups(c *gin.Context) {
	enabled, ok := filterBoolQuery(c, "enabled")
	if !ok {
		return
	}
	memberUserID, ok := filterUUIDQuery(c, "member_user_id")
	if !ok {
		return
	}
	flowID, ok := filterUUIDQuery(c, "flow_id")
	if !ok {
		return
	}
	items, next, err := d.Workflow.ListGroups(c.Request.Context(), intQuery(c, "limit", 50), c.Query("after"), c.Query("q"), enabled, memberUserID, flowID)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B3Deps) HandleCreatePermissionGroup(c *gin.Context) {
	var w workflow.GroupWrite
	if err := c.ShouldBindJSON(&w); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Workflow.CreateGroup(c.Request.Context(), workflowActorOf(c), &w, workflowMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleGetPermissionGroup(c *gin.Context) {
	view, err := d.Workflow.GetGroup(c.Request.Context(), c.Param("group_id"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleReplacePermissionGroup(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var w workflow.GroupWrite
	if err := c.ShouldBindJSON(&w); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Workflow.ReplaceGroup(c.Request.Context(), workflowActorOf(c), c.Param("group_id"), version, &w, workflowMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleDeletePermissionGroup(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	err := d.Workflow.DeleteGroup(c.Request.Context(), workflowActorOf(c), c.Param("group_id"), version, workflowMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

func workflowActorOf(c *gin.Context) workflow.AdminActor {
	id := session.FromContext(c.Request.Context())
	return workflow.AdminActor{UserID: id.UserID, Username: id.Username}
}

func workflowMeta(c *gin.Context) workflow.RequestMeta {
	return workflow.RequestMeta{RequestID: RequestID(c)}
}
