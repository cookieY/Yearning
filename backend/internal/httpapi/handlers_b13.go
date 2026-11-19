// handlers_b13.go settings domain HTTP handlers (OpenAPI admin/settings* operations).
// B12 delivered the application service without mounting serve routes; this package wires them
// following house style: RequireOperation guard + unified envelope + error catalog mapping.
package httpapi

import (
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/settings"
)

// B13Deps settings domain HTTP dependencies.
type B13Deps struct {
	Settings *settings.Service
}

// registerSettingsRoutes mounts the 5 settings operations, matching OpenAPI paths verbatim.
func registerSettingsRoutes(rg gin.IRouter, d *B13Deps, guard func(string) gin.HandlerFunc) {
	rg.GET("/admin/settings/:namespace", guard("getSettingsNamespace"), d.handleGet)
	rg.PUT("/admin/settings/:namespace", guard("replaceSettingsNamespace"), d.handleReplace)
	rg.POST("/admin/settings/:namespace/impact-assessments", guard("assessSettingsImpact"), d.handleAssess)
	rg.GET("/admin/settings/:namespace/schema", guard("getSettingsSchema"), d.handleSchema)
	rg.GET("/admin/settings/:namespace/revisions", guard("listSettingsRevisions"), d.handleRevisions)
}

// writeSettingsErr maps domain errors to the error catalog (settings_replace profile).
// ErrTokenInvalid maps to 1011 PRECONDITION_REQUIRED: frontend must re-assess rather than
// retrying silently (PRD 10 S4).
func writeSettingsErr(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, settings.ErrNotFound):
		WriteBusinessError(c, &BusinessError{Code: 1002, Name: "RESOURCE_NOT_FOUND", Message: "Resource not found"})
	case errors.Is(err, settings.ErrConflict):
		WriteBusinessError(c, &BusinessError{Code: 1003, Name: "VERSION_CONFLICT", Message: "Resource version conflict; please refresh and retry"})
	case errors.Is(err, settings.ErrValidation):
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
	case errors.Is(err, settings.ErrTokenInvalid):
		WriteBusinessError(c, &BusinessError{Code: 1011, Name: "PRECONDITION_REQUIRED", Message: "Impact confirmation token is invalid or expired; please re-evaluate"})
	default:
		WriteProblem(c, 500, "internal error")
	}
}

func settingsActor(c *gin.Context) (string, string) {
	id := session.FromContext(c.Request.Context())
	return id.UserID, id.Username
}

func (d *B13Deps) handleGet(c *gin.Context) {
	view, err := d.Settings.Get(c.Request.Context(), c.Param("namespace"))
	if err != nil {
		writeSettingsErr(c, err)
		return
	}
	WriteSuccess(c, view)
}

func (d *B13Deps) handleReplace(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req struct {
		Settings    json.RawMessage `json:"settings"`
		ImpactToken string          `json:"impact_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	actorID, actorName := settingsActor(c)
	view, err := d.Settings.Replace(c.Request.Context(), settings.ReplaceInput{
		Namespace:     c.Param("namespace"),
		Settings:      req.Settings,
		ExpectVersion: version,
		ActorID:       actorID,
		ActorUsername: actorName,
		ImpactToken:   req.ImpactToken,
	})
	if err != nil {
		writeSettingsErr(c, err)
		return
	}
	WriteSuccess(c, view)
}

func (d *B13Deps) handleAssess(c *gin.Context) {
	var req struct {
		Settings json.RawMessage `json:"settings"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	actorID, _ := settingsActor(c)
	view, err := d.Settings.Assess(c.Request.Context(), c.Param("namespace"), req.Settings, actorID)
	if err != nil {
		writeSettingsErr(c, err)
		return
	}
	WriteSuccess(c, view)
}

func (d *B13Deps) handleSchema(c *gin.Context) {
	view, err := d.Settings.SchemaView(c.Request.Context(), c.Param("namespace"))
	if err != nil {
		writeSettingsErr(c, err)
		return
	}
	WriteSuccess(c, view)
}

func (d *B13Deps) handleRevisions(c *gin.Context) {
	items, next, err := d.Settings.ListRevisions(c.Request.Context(),
		c.Param("namespace"), intQuery(c, "limit", 50), c.Query("after"))
	if err != nil {
		writeSettingsErr(c, err)
		return
	}
	writePage(c, items, next)
}
