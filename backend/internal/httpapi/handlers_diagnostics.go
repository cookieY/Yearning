// handlers_diagnostics.go SQL Performance Diagnostics (BE-B13-SQL-DIAGNOSTICS) HTTP handlers.
// Follows the OpenAPI /admin/diagnostics/* and /admin/datasources/{id}/diagnostics-config contracts;
// all responses carry Cache-Control: no-store; management-plane permissions are guarded by RequireOperation
// (builtin_admin or role='admin'), and advancing to change order precheck/submission additionally enforces
// FlowDatasourceGranted and the Submission Gate in the application layer.
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	diagapp "github.com/cookieY/YearningX/backend/internal/diagnostics/application"
	diagdomain "github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

// DiagnosticsDeps aggregates HTTP route dependencies for SQL Performance Diagnostics.
type DiagnosticsDeps struct {
	Diagnostics *diagapp.Services
	B3          *B3Deps
	B6          *B6Deps
	B9          *B9Deps
}

func buildDiagnosticsDeps(b3 *B3Deps, b6 *B6Deps, b9 *B9Deps) (*DiagnosticsDeps, error) {
	if b3 == nil || b3.DB == nil || b3.Crypter == nil {
		return nil, errors.New("diagnostics: missing b3 platform dependencies")
	}
	recorder := b3.Recorder
	if recorder == nil {
		recorder, _ = auditlog.NewRecorder(b3.DB)
	}
	var (
		revSvc   *reviewapp.Services
		revAgent *reviewapp.AgentRunner
		ordSvc   *orderapp.Services
	)
	if b6 != nil {
		revSvc = b6.Review
		revAgent = b6.AgentRunner()
	}
	if b9 != nil {
		ordSvc = b9.Orders
	}
	svc, err := diagapp.NewServices(b3.DB, b3.Crypter, recorder, revSvc, revAgent, ordSvc)
	if err != nil {
		return nil, err
	}
	return &DiagnosticsDeps{
		Diagnostics: svc,
		B3:          b3,
		B6:          b6,
		B9:          b9,
	}, nil
}

func diagnosticsActor(c *gin.Context) diagdomain.Actor {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		return diagdomain.Actor{}
	}
	return diagdomain.Actor{
		UserID:         id.UserID,
		Username:       id.Username,
		IsBuiltinAdmin: id.IsBuiltinAdmin,
		Role:           id.Role,
	}
}

func writeDiagnosticsErr(c *gin.Context, err error) bool {
	if err == nil {
		return true
	}
	var de *diagdomain.Error
	if errors.As(err, &de) {
		msg := de.Message
		if msg == "" {
			msg = "Business operation failed"
		}
		WriteBusinessError(c, &BusinessError{
			Code:    de.Code,
			Name:    de.Name,
			Message: msg,
			Meta:    de.Meta,
		})
		return true
	}
	return false
}

func parseTimeQuery(c *gin.Context, name string) (time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid timestamp format"})
		return time.Time{}, false
	}
	return t.UTC(), true
}

// HandleGetDatasourceDiagnosticsConfig GET /admin/datasources/:datasource_id/diagnostics-config.
func (d *DiagnosticsDeps) HandleGetDatasourceDiagnosticsConfig(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	view, err := d.Diagnostics.GetConfig(c.Request.Context(), datasourceID)
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, view)
}

// HandleUpdateDatasourceDiagnosticsConfig PUT /admin/datasources/:datasource_id/diagnostics-config.
func (d *DiagnosticsDeps) HandleUpdateDatasourceDiagnosticsConfig(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req struct {
		Enabled         *bool `json:"enabled"`
		IntervalSeconds *int  `json:"interval_seconds"`
		TopN            *int  `json:"top_n"`
		ResumeFromPause *bool `json:"resume_from_pause"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil || req.IntervalSeconds == nil || req.TopN == nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	resume := false
	if req.ResumeFromPause != nil {
		resume = *req.ResumeFromPause
	}
	view, err := d.Diagnostics.UpdateConfig(c.Request.Context(), diagnosticsActor(c), datasourceID, version, diagdomain.UpdateDiagnosticsConfigInput{
		Enabled:         *req.Enabled,
		IntervalSeconds: *req.IntervalSeconds,
		TopN:            *req.TopN,
		ResumeFromPause: resume,
	})
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, view)
}

// HandleListDiagnosticDatasources GET /admin/diagnostics/datasources.
func (d *DiagnosticsDeps) HandleListDiagnosticDatasources(c *gin.Context) {
	items, err := d.Diagnostics.ListDatasources(c.Request.Context())
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, gin.H{"items": items})
}

// HandleGetDiagnosticsOverview GET /admin/diagnostics/datasources/:datasource_id/overview.
func (d *DiagnosticsDeps) HandleGetDiagnosticsOverview(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	from, ok := parseTimeQuery(c, "from")
	if !ok {
		return
	}
	to, ok := parseTimeQuery(c, "to")
	if !ok {
		return
	}
	cmpFrom, ok := parseTimeQuery(c, "compare_from")
	if !ok {
		return
	}
	cmpTo, ok := parseTimeQuery(c, "compare_to")
	if !ok {
		return
	}
	var cmpFromPtr, cmpToPtr *time.Time
	if !cmpFrom.IsZero() && !cmpTo.IsZero() {
		cmpFromPtr = &cmpFrom
		cmpToPtr = &cmpTo
	}
	view, err := d.Diagnostics.GetOverview(c.Request.Context(), datasourceID, diagapp.OverviewQuery{
		From:        from,
		To:          to,
		CompareFrom: cmpFromPtr,
		CompareTo:   cmpToPtr,
		SortBy:      strings.TrimSpace(c.Query("sort_by")),
		Limit:       intQuery(c, "limit", 50),
	})
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, view)
}

// HandleTriggerManualDiagnosticsSnapshot POST /admin/diagnostics/datasources/:datasource_id/snapshots.
func (d *DiagnosticsDeps) HandleTriggerManualDiagnosticsSnapshot(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	summary, err := d.Diagnostics.TriggerManualSnapshot(c.Request.Context(), diagnosticsActor(c), datasourceID)
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, summary)
}

// HandleGetDiagnosticStatementDetail GET /admin/diagnostics/datasources/:datasource_id/statements/:statement_id.
func (d *DiagnosticsDeps) HandleGetDiagnosticStatementDetail(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	statementID, ok := uuidParam(c, "statement_id")
	if !ok {
		return
	}
	from, ok := parseTimeQuery(c, "from")
	if !ok {
		return
	}
	to, ok := parseTimeQuery(c, "to")
	if !ok {
		return
	}
	view, err := d.Diagnostics.GetStatementDetail(c.Request.Context(), datasourceID, statementID, from, to)
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, view)
}

// HandleConfirmDiagnosticOrderLink POST /admin/diagnostics/datasources/:datasource_id/statements/:statement_id/order-links.
func (d *DiagnosticsDeps) HandleConfirmDiagnosticOrderLink(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	statementID, ok := uuidParam(c, "statement_id")
	if !ok {
		return
	}
	var req struct {
		ChangeOrderID     string  `json:"change_order_id" binding:"required"`
		RelationKind      string  `json:"relation_kind" binding:"required"`
		TargetStatementID *string `json:"target_statement_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !isValidUUID(req.ChangeOrderID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if req.TargetStatementID != nil && *req.TargetStatementID != "" && !isValidUUID(*req.TargetStatementID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid target_statement_id"})
		return
	}
	view, err := d.Diagnostics.ConfirmOrderLink(c.Request.Context(), diagnosticsActor(c), datasourceID, statementID, req.ChangeOrderID, req.RelationKind, req.TargetStatementID)
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, view)
}

// HandleDiagnoseStatementWithAI POST /admin/diagnostics/datasources/:datasource_id/statements/:statement_id/ai-reports.
func (d *DiagnosticsDeps) HandleDiagnoseStatementWithAI(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	statementID, ok := uuidParam(c, "statement_id")
	if !ok {
		return
	}
	var req struct {
		From         *string `json:"from"`
		To           *string `json:"to"`
		OutputLocale *string `json:"output_locale"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
	}
	var from, to time.Time
	if req.From != nil && *req.From != "" {
		t, err := time.Parse(time.RFC3339, *req.From)
		if err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid from timestamp format"})
			return
		}
		from = t.UTC()
	}
	if req.To != nil && *req.To != "" {
		t, err := time.Parse(time.RFC3339, *req.To)
		if err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid to timestamp format"})
			return
		}
		to = t.UTC()
	}
	diagCtx := reviewapp.WithOutputLocale(c.Request.Context(), resolveRequestOutputLocale(c, req.OutputLocale))
	view, err := d.Diagnostics.DiagnoseStatementWithAI(diagCtx, diagnosticsActor(c), datasourceID, statementID, from, to)
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, view)
}

// HandlePrecheckDiagnosticOptimizationDraft POST /admin/diagnostics/datasources/:datasource_id/ai-reports/:report_id/prechecks.
func (d *DiagnosticsDeps) HandlePrecheckDiagnosticOptimizationDraft(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	reportID, ok := uuidParam(c, "report_id")
	if !ok {
		return
	}
	var req struct {
		FlowID       string  `json:"flow_id" binding:"required"`
		CandidateSQL *string `json:"candidate_sql"`
		OutputLocale *string `json:"output_locale"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !isValidUUID(req.FlowID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if d.B6 != nil {
		d.B6.EnsureWorker()
	}
	precheckCtx := reviewapp.WithOutputLocale(c.Request.Context(), resolveRequestOutputLocale(c, req.OutputLocale))
	res, err := d.Diagnostics.PrecheckOptimizationDraft(
		precheckCtx,
		diagnosticsActor(c),
		datasourceID,
		reportID,
		req.FlowID,
		req.CandidateSQL,
		strings.TrimSpace(c.GetHeader("Idempotency-Key")),
	)
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, res)
}

// HandleSubmitDiagnosticOptimizationOrder POST /admin/diagnostics/datasources/:datasource_id/ai-reports/:report_id/order-submissions.
func (d *DiagnosticsDeps) HandleSubmitDiagnosticOptimizationOrder(c *gin.Context) {
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	reportID, ok := uuidParam(c, "report_id")
	if !ok {
		return
	}
	var req struct {
		FlowID         string  `json:"flow_id" binding:"required"`
		EphemeralRunID string  `json:"ephemeral_run_id" binding:"required"`
		Title          string  `json:"title" binding:"required"`
		Remark         *string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !isValidUUID(req.FlowID) || !isValidUUID(req.EphemeralRunID) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	remark := ""
	if req.Remark != nil {
		remark = *req.Remark
	}
	res, err := d.Diagnostics.SubmitOptimizationOrder(
		c.Request.Context(),
		diagnosticsActor(c),
		datasourceID,
		reportID,
		req.FlowID,
		req.EphemeralRunID,
		req.Title,
		remark,
		strings.TrimSpace(c.GetHeader("Idempotency-Key")),
	)
	if err != nil {
		if !writeDiagnosticsErr(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, res)
}

// mountDiagnosticsRoutes mounts management-plane routes for SQL Performance Diagnostics (builtin-admin-management).
func mountDiagnosticsRoutes(engine *gin.Engine, deps ServeDeps) {
	diag := deps.Diagnostics
	guard := func(op string) gin.HandlerFunc {
		return RequireOperation(op, deps.B3.Sessions)
	}

	admin := engine.Group("/api/v4/admin")
	admin.GET("/datasources/:datasource_id/diagnostics-config", guard("getDatasourceDiagnosticsConfig"), diag.HandleGetDatasourceDiagnosticsConfig)
	admin.PUT("/datasources/:datasource_id/diagnostics-config", guard("updateDatasourceDiagnosticsConfig"), diag.HandleUpdateDatasourceDiagnosticsConfig)

	diagGroup := admin.Group("/diagnostics")
	diagGroup.GET("/datasources", guard("listDiagnosticDatasources"), diag.HandleListDiagnosticDatasources)
	diagGroup.GET("/datasources/:datasource_id/overview", guard("getDiagnosticsOverview"), diag.HandleGetDiagnosticsOverview)
	diagGroup.POST("/datasources/:datasource_id/snapshots", guard("triggerManualDiagnosticsSnapshot"), diag.HandleTriggerManualDiagnosticsSnapshot)
	diagGroup.GET("/datasources/:datasource_id/statements/:statement_id", guard("getDiagnosticStatementDetail"), diag.HandleGetDiagnosticStatementDetail)
	diagGroup.POST("/datasources/:datasource_id/statements/:statement_id/order-links", guard("confirmDiagnosticOrderLink"), diag.HandleConfirmDiagnosticOrderLink)
	diagGroup.POST("/datasources/:datasource_id/statements/:statement_id/ai-reports", guard("diagnoseStatementWithAi"), diag.HandleDiagnoseStatementWithAI)
	diagGroup.POST("/datasources/:datasource_id/ai-reports/:report_id/prechecks", guard("precheckDiagnosticOptimizationDraft"), diag.HandlePrecheckDiagnosticOptimizationDraft)
	diagGroup.POST("/datasources/:datasource_id/ai-reports/:report_id/order-submissions", guard("submitDiagnosticOptimizationOrder"), diag.HandleSubmitDiagnosticOptimizationOrder)
}
