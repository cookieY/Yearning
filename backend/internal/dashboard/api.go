// api.go HTTP surface for the dashboard domain (OpenAPI dashboard*/announcements*/audit-events
// operations; mounted by the wiring package). Audit read is a read-only projection (S004;
// audit_events itself has a 90-day retention).
package dashboard

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/httpenvelope"
)

// Deps holds HTTP dependencies for the dashboard domain. Guard is injected by the wiring package
// (RequireOperation: policy decision + mutating CSRF double-submit) and is mandatory — a nil Guard
// fails closed with HTTP 500 Problem Details rather than allowing unauthenticated pass-through.
// ReleaseFeedURL is the upstream releases/latest URL (RCP-20261020): empty disables the refresh loop.
type Deps struct {
	Service        *Service
	Guard          func(op string) gin.HandlerFunc
	ReleaseFeedURL string
}

// RegisterRoutes mounts dashboard/announcements/audit-read operations with paths aligned
// verbatim to frozen OpenAPI values (/dashboard/me, /announcements/current, /releases/latest,
// /admin/dashboard/operations, and /admin/dashboard/system-health are authenticated surfaces —
// §18.71 platform operations stats and §18.83 system health are open to all authenticated sessions,
// as is the release snapshot; remaining endpoints are admin-only).
// Each operationId maps 1:1 to an authorization-policy rule enforced by the injected Guard.
func RegisterRoutes(rg gin.IRouter, d *Deps) {
	h := &handler{d: d}
	g := d.guard
	rg.GET("/dashboard/me", g("getMyDashboard"), h.myDashboard)
	rg.GET("/admin/dashboard/operations", g("getOperationsDashboard"), h.operations)
	rg.GET("/admin/dashboard/system-health", g("getSystemHealthDashboard"), h.systemHealth)
	rg.GET("/announcements/current", g("getCurrentAnnouncement"), h.currentAnnouncement)
	rg.GET("/releases/latest", g("getLatestRelease"), h.latestRelease)
	rg.GET("/admin/announcement-revisions", g("listAnnouncementRevisions"), h.listAnnouncementRevisions)
	rg.POST("/admin/announcement-revisions", g("createAnnouncementRevision"), h.createAnnouncementRevision)
	rg.PUT("/admin/announcement-publication", g("publishAnnouncementRevision"), h.publishAnnouncement)
	rg.GET("/admin/audit-events", g("listAuditEvents"), h.listAuditEvents)
}

// guard returns the injected operation guard, failing closed with HTTP 500 Problem Details when nil.
func (d *Deps) guard(op string) gin.HandlerFunc {
	if d == nil || d.Guard == nil {
		return func(c *gin.Context) {
			httpenvelope.WriteProblem(c, http.StatusInternalServerError, "authorization guard not configured")
			c.Abort()
		}
	}
	return d.Guard(op)
}

type handler struct{ d *Deps }

func requireUser(c *gin.Context) (*session.Identity, bool) {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		httpenvelope.WriteProblem(c, http.StatusUnauthorized, "authentication required")
		c.Abort()
		return nil, false
	}
	return id, true
}

func adminGuard(c *gin.Context) bool {
	id := session.FromContext(c.Request.Context())
	if id == nil || !id.HasAdminAccess() {
		httpenvelope.WriteProblem(c, http.StatusForbidden, "permission denied")
		c.Abort()
		return false
	}
	return true
}

func windowDays(c *gin.Context) int {
	n, _ := strconv.Atoi(c.DefaultQuery("window_days", "30"))
	return n
}

func (h *handler) myDashboard(c *gin.Context) {
	id, ok := requireUser(c)
	if !ok {
		return
	}
	view, err := h.d.Service.MyDashboard(c.Request.Context(), id.UserID)
	if err != nil {
		httpenvelope.WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	httpenvelope.WriteSuccess(c, view)
}

func (h *handler) operations(c *gin.Context) {
	// §18.71: authenticated sessions can read operations stats, scoped by role:
	// admins receive platform-wide aggregates while non-admin users receive aggregates
	// scoped strictly to change orders and query executions tied to their own user ID.
	id, ok := requireUser(c)
	if !ok {
		return
	}
	var (
		view *OperationsView
		err  error
	)
	if id.HasAdminAccess() {
		view, err = h.d.Service.OperationsDashboard(c.Request.Context(), windowDays(c))
	} else {
		view, err = h.d.Service.OperationsDashboardForUser(c.Request.Context(), id.UserID, windowDays(c))
	}
	if err != nil {
		httpenvelope.WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	httpenvelope.WriteSuccess(c, view)
}

func (h *handler) systemHealth(c *gin.Context) {
	// §18.83 (owner 2026-10-23 directive): system health read surface is open to all authenticated sessions
	// (authorization-policy authenticated-session rule) for the home page announcement card health indicator.
	if _, ok := requireUser(c); !ok {
		return
	}
	view, err := h.d.Service.SystemHealth(c.Request.Context())
	if err != nil {
		httpenvelope.WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	httpenvelope.WriteSuccess(c, view)
}

func (h *handler) currentAnnouncement(c *gin.Context) {
	// OpenAPI global security=sessionCookie with no anonymous override on this operation: authenticated surface.
	if _, ok := requireUser(c); !ok {
		return
	}
	view, err := h.d.Service.Current(c.Request.Context())
	if err != nil {
		httpenvelope.WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	httpenvelope.WriteSuccess(c, view)
}

// latestRelease authenticated surface (same as getCurrentAnnouncement): reads the release snapshot,
// returning data:null when no snapshot exists yet (frontend renders a min-height placeholder instead of hiding the card).
func (h *handler) latestRelease(c *gin.Context) {
	if _, ok := requireUser(c); !ok {
		return
	}
	view, err := h.d.Service.LatestRelease(c.Request.Context())
	if err != nil {
		httpenvelope.WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	httpenvelope.WriteSuccess(c, view)
}

func (h *handler) listAnnouncementRevisions(c *gin.Context) {
	if !adminGuard(c) {
		return
	}
	items, err := h.d.Service.ListRevisions(c.Request.Context(), 50)
	if err != nil {
		httpenvelope.WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	httpenvelope.WriteSuccess(c, gin.H{"items": items, "page": gin.H{"next_cursor": nil, "has_more": false}})
}

func (h *handler) createAnnouncementRevision(c *gin.Context) {
	if !adminGuard(c) {
		return
	}
	id := session.FromContext(c.Request.Context())
	var req struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown_source"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
		return
	}
	view, err := h.d.Service.CreateRevision(c.Request.Context(), CreateRevisionInput{
		Title: req.Title, Markdown: req.Markdown, ActorID: id.UserID, ActorUsername: id.Username,
	})
	if err != nil {
		httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid announcement content", nil)
		return
	}
	httpenvelope.WriteSuccess(c, view)
}

func (h *handler) publishAnnouncement(c *gin.Context) {
	if !adminGuard(c) {
		return
	}
	id := session.FromContext(c.Request.Context())
	var req struct {
		AnnouncementRevisionID string `json:"announcement_revision_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || uuid.Validate(req.AnnouncementRevisionID) != nil {
		httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
		return
	}
	version, ok := httpenvelope.ParseIfMatch(c)
	if !ok {
		return
	}
	view, pubVersion, err := h.d.Service.Publish(c.Request.Context(), req.AnnouncementRevisionID,
		id.UserID, id.Username, version)
	if err != nil {
		if err == ErrConflict {
			httpenvelope.WriteCodedError(c, bizerr.CodeVersionConflict, "VERSION_CONFLICT", "Resource version conflict; please refresh and retry", nil)
			return
		}
		httpenvelope.WriteCodedError(c, bizerr.CodeResourceNotFound, "RESOURCE_NOT_FOUND", "Resource not found", nil)
		return
	}
	// Response version equals the persisted pointer version truth (R2-F12), consistent with Current().
	httpenvelope.WriteSuccess(c, gin.H{"revision": view, "published_by_username": id.Username,
		"published_at": h.d.Service.Now().Format("2006-01-02T15:04:05.999999999Z07:00"), "version": pubVersion})
}

// AuditEventView conforms to OpenAPI AuditEvent (read-only projection; metadata clipped to a
// bounded allowlist — never passing through arbitrary nested structures).
type AuditEventView struct {
	ID           string         `json:"id"`
	EventType    string         `json:"event_type"`
	ActorKind    string         `json:"actor_kind"`
	ActorID      *string        `json:"actor_id"`
	ActorName    *string        `json:"actor_username_snapshot"`
	ResourceType string         `json:"resource_type"`
	ResourceID   *string        `json:"resource_id"`
	Action       string         `json:"action"`
	Outcome      string         `json:"outcome"`
	RequestID    *string        `json:"request_id"`
	Metadata     map[string]any `json:"metadata"`
	OccurredAt   string         `json:"occurred_at"`
	ExpiresAt    string         `json:"expires_at"`
}

// Audit date filter predicates (UTC day boundaries; anchored via `at time zone 'UTC'` so semantics
// are independent of the PG session timezone — R1 Medium-1). from includes 00:00 of the start day,
// and to includes the full end day (< 00:00 of the next day).
const (
	auditOccurredFromPred = "occurred_at >= ((?::date)::timestamp at time zone 'UTC')"
	auditOccurredToPred   = "occurred_at < (((?::date)::timestamp at time zone 'UTC') + interval '1 day')"
)

func (h *handler) listAuditEvents(c *gin.Context) {
	if !adminGuard(c) {
		return
	}
	var rows []struct {
		ID           string  `gorm:"column:id"`
		EventType    string  `gorm:"column:event_type"`
		ActorKind    string  `gorm:"column:actor_kind"`
		ActorID      *string `gorm:"column:actor_id"`
		ActorName    *string `gorm:"column:actor_username_snapshot"`
		ResourceType string  `gorm:"column:resource_type"`
		ResourceID   *string `gorm:"column:resource_id"`
		Action       string  `gorm:"column:action"`
		Outcome      string  `gorm:"column:outcome"`
		RequestID    *string `gorm:"column:request_id"`
		Metadata     []byte  `gorm:"column:metadata"`
		OccurredAt   string  `gorm:"column:occurred_at"`
		ExpiresAt    string  `gorm:"column:expires_at"`
	}
	limit := 50
	if v, err := strconv.Atoi(c.DefaultQuery("limit", "50")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	args := []any{}
	q := `select id::text, event_type, actor_kind, actor_id::text, actor_username_snapshot,
	      resource_type, resource_id::text, action, outcome, request_id::text, metadata,
	      to_char(occurred_at, 'YYYY-MM-DD"T"HH24:MI:SS.USZ') as occurred_at,
	      to_char(expires_at, 'YYYY-MM-DD"T"HH24:MI:SS.USZ') as expires_at
	      from audit_events`
	// Server-side filters (RCP-20260901-AUDIT-LIST-FILTERS): all are exact filters on existing
	// non-sensitive dimensions that narrow the admin audit scope and compose with after-cursor pagination.
	// Invalid values (non-UUID, non-enum, non-date) return 1001 rather than silently widening the query.
	var conds []string
	if v := c.Query("actor_id"); v != "" {
		if uuid.Validate(v) != nil {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, "actor_id = ?::uuid")
		args = append(args, v)
	}
	if v := c.Query("action"); v != "" {
		if len(v) > 128 {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, "action = ?")
		args = append(args, v)
	}
	if v := c.Query("resource_type"); v != "" {
		if len(v) > 128 {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, "resource_type = ?")
		args = append(args, v)
	}
	if v := c.Query("outcome"); v != "" {
		if v != "succeeded" && v != "denied" && v != "failed" {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, "outcome = ?")
		args = append(args, v)
	}
	if v := c.Query("request_id"); v != "" {
		if uuid.Validate(v) != nil {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, "request_id = ?::uuid")
		args = append(args, v)
	}
	// occurred_from/to form a closed UTC day interval (per RCP specification): comparing date against
	// timestamptz defaults to midnight in the session TimeZone, which would shift boundaries when
	// deployment PGTZ is non-UTC — anchor explicitly with `at time zone 'UTC'` regardless of session
	// timezone (R1 Medium-1); from = 00:00 UTC on start day, to = end of day UTC inclusive.
	if v := c.Query("occurred_from"); v != "" {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, auditOccurredFromPred)
		args = append(args, v)
	}
	if v := c.Query("occurred_to"); v != "" {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, auditOccurredToPred)
		args = append(args, v)
	}
	if after := c.Query("after"); after != "" {
		if uuid.Validate(after) != nil {
			httpenvelope.WriteCodedError(c, bizerr.CodeValidationFailed, "VALIDATION_FAILED", "Invalid request parameters", nil)
			return
		}
		conds = append(conds, "(occurred_at, id) < ((select occurred_at from audit_events where id = ?), ?::uuid)")
		args = append(args, after, after)
	}
	if len(conds) > 0 {
		q += " where " + strings.Join(conds, " and ")
	}
	q += " order by occurred_at desc, id desc limit " + strconv.Itoa(limit)
	if err := h.d.Service.DB.WithContext(c.Request.Context()).Raw(q, args...).Scan(&rows).Error; err != nil {
		httpenvelope.WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	items := make([]AuditEventView, 0, len(rows))
	for _, r := range rows {
		meta := clipAuditMetadata(r.Metadata)
		items = append(items, AuditEventView{
			ID: r.ID, EventType: r.EventType, ActorKind: r.ActorKind,
			ActorID: r.ActorID, ActorName: r.ActorName, ResourceType: r.ResourceType,
			ResourceID: r.ResourceID, Action: r.Action, Outcome: r.Outcome,
			RequestID: r.RequestID, Metadata: meta, OccurredAt: r.OccurredAt,
			ExpiresAt: r.ExpiresAt,
		})
	}
	var nextCursor any
	if len(items) >= limit && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	httpenvelope.WriteSuccess(c, gin.H{"items": items, "page": gin.H{"next_cursor": nextCursor, "has_more": len(items) >= limit}})
}
