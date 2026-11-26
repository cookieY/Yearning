// handlers_datasource.go datasource domain HTTP handlers: datasource CRUD, connection test tasks,
// capability probes, and async connection task status views. Secrets are write-only.
package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// ---- Management surface: Datasources ----

func (d *B3Deps) HandleListDatasources(c *gin.Context) {
	engine, ok := filterEnumQuery(c, "engine", "mysql", "postgresql", "tidb", "oceanbase", "polardb")
	if !ok {
		return
	}
	compatibilityMode, ok := filterEnumQuery(c, "compatibility_mode", "mysql", "postgresql")
	if !ok {
		return
	}
	credentialMode, ok := filterEnumQuery(c, "credential_mode", "read_write", "read_only")
	if !ok {
		return
	}
	referenced, ok := filterBoolQuery(c, "referenced")
	if !ok {
		return
	}
	items, next, err := d.Datasource.List(c.Request.Context(), intQuery(c, "limit", 50), c.Query("after"), c.Query("q"), datasource.ListFilters{
		Engine: engine, CompatibilityMode: compatibilityMode, CredentialMode: credentialMode, Referenced: referenced,
	})
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B3Deps) HandleCreateDatasource(c *gin.Context) {
	var in datasource.WriteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Datasource.Create(c.Request.Context(), datasourceActorOf(c), &in, datasourceMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleGetDatasource(c *gin.Context) {
	view, err := d.Datasource.Get(c.Request.Context(), c.Param("datasource_id"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleReplaceDatasource(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var in datasource.WriteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Datasource.Replace(c.Request.Context(), datasourceActorOf(c), c.Param("datasource_id"), version, &in, datasourceMeta(c))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B3Deps) HandleDeleteDatasource(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	err := d.Datasource.Delete(c.Request.Context(), datasourceActorOf(c), c.Param("datasource_id"), version, datasourceMeta(c))
	if err != nil {
		if !writeDomainError(c, err, bizerr.CodeDatasourceReferenced) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

func (d *B3Deps) HandleTestDatasourceConnection(c *gin.Context) {
	var req struct {
		Purpose string `json:"purpose" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	_, task, err := d.Datasource.TestAndCapabilities(c.Request.Context(), c.Param("datasource_id"), req.Purpose)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, taskView(task))
}

func (d *B3Deps) HandleDatasourceCapabilities(c *gin.Context) {
	// S18.73: capabilities endpoint selects probe credentials using the datasource persisted connection test purpose
	// (S18.61 test_purpose; previously hardcoded to review, inconsistent with the stored-purpose-driven
	// semantics of the list "Test Connection" button).
	purpose, err := d.Datasource.StoredTestPurpose(c.Request.Context(), c.Param("datasource_id"))
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	caps, err := d.Datasource.Capabilities(c.Request.Context(), c.Param("datasource_id"), purpose)
	if err != nil {
		if !writeDomainError(c, err, 0) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, caps)
}

// ---- Tasks ----

// HandleGetTask reads a connection test task (all B3 tasks are created by admin,
// satisfying task-reader rule via management guard; subsequent packages extend actor relationships for business tasks).
func (d *B3Deps) HandleGetTask(c *gin.Context) {
	var task datasource.AsyncTaskModel
	if err := d.DB.WithContext(c.Request.Context()).
		Where("id = ?", c.Param("task_id")).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeResourceNotFound, Name: "RESOURCE_NOT_FOUND", Message: "Resource not found"})
			return
		}
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	WriteSuccess(c, taskView(&task))
}

// taskView exposes allowlisted Task fields. S18.76 fix (owner decision following Provider rejection
// reason surfacing path): failed tasks attach a Problem-shaped error (detail=failure_name,
// e.g. capability_unresolved/connection_failed) so the inline UI displays the specific failure category.
func taskView(t *datasource.AsyncTaskModel) gin.H {
	view := gin.H{
		"id":    t.ID,
		"kind":  t.Kind,
		"state": t.State,
		"progress": gin.H{
			"completed": t.ProgressCompleted,
			"total":     t.ProgressTotal,
			"unit":      t.ProgressUnit,
		},
		"created_at": t.CreatedAt,
		"updated_at": t.UpdatedAt,
	}
	if t.State == "failed" {
		failure := "unknown"
		if t.FailureName != nil && *t.FailureName != "" {
			failure = *t.FailureName
		}
		code := 0
		if t.BusinessErrCode != nil {
			code = *t.BusinessErrCode
		}
		view["error"] = gin.H{
			"type":       "about:blank",
			"title":      failure,
			"status":     code,
			"detail":     failure,
			"request_id": "",
		}
	}
	return view
}

func datasourceActorOf(c *gin.Context) datasource.ActorRef {
	id := session.FromContext(c.Request.Context())
	return datasource.ActorRef{UserID: id.UserID, Username: id.Username}
}

func datasourceMeta(c *gin.Context) struct{ RequestID string } {
	return struct{ RequestID string }{RequestID: RequestID(c)}
}
