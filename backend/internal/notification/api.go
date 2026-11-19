// api.go HTTP surface for the notification domain (OpenAPI notification-channels* operations;
// mounted via RegisterRoutes by the wiring package — httpapi is outside B12 allowed_paths).
package notification

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// Deps holds HTTP dependencies for the notification domain. Guard is injected by the wiring package
// (RequireOperation: policy decision + mutating CSRF double-submit); when nil, routes mount directly
// (in-domain fallback guard for unit tests).
type Deps struct {
	Service *Service
	Guard   func(op string) gin.HandlerFunc
}

// RegisterRoutes mounts notification operations with paths aligned verbatim to OpenAPI /admin/notification-*;
// each operationId maps 1:1 to authorization-policy builtin-admin-management and is enforced by the injected Guard.
// S007 adds 6 notification-rules operations.
func RegisterRoutes(rg gin.IRouter, d *Deps) {
	h := &handler{d: d}
	g := d.guard
	rg.GET("/admin/notification-channels", g("listNotificationChannels"), h.list)
	rg.POST("/admin/notification-channels", g("createNotificationChannel"), h.create)
	rg.GET("/admin/notification-channels/:channel_id", g("getNotificationChannel"), h.get)
	rg.PUT("/admin/notification-channels/:channel_id", g("replaceNotificationChannel"), h.replace)
	rg.DELETE("/admin/notification-channels/:channel_id", g("deleteNotificationChannel"), h.delete)
	rg.POST("/admin/notification-channels/:channel_id/test-deliveries", g("createNotificationTestDelivery"), h.testDelivery)
	rg.GET("/admin/notification-deliveries", g("listNotificationDeliveries"), h.listDeliveries)
	// S007 rules surface.
	rg.GET("/admin/notification-rules", g("listNotificationRules"), h.listRules)
	rg.POST("/admin/notification-rules", g("createNotificationRule"), h.createRule)
	rg.POST("/admin/notification-rules/preview", g("previewNotificationRule"), h.previewRule)
	rg.GET("/admin/notification-rules/:rule_id", g("getNotificationRule"), h.getRule)
	rg.PUT("/admin/notification-rules/:rule_id", g("replaceNotificationRule"), h.replaceRule)
	rg.DELETE("/admin/notification-rules/:rule_id", g("deleteNotificationRule"), h.deleteRule)
}

// guard returns the operation guard, or a pass-through when none was injected (in-domain fallback).
func (d *Deps) guard(op string) gin.HandlerFunc {
	if d.Guard == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return d.Guard(op)
}

type handler struct{ d *Deps }

func currentAdmin(c *gin.Context) (string, bool) {
	id := session.FromContext(c.Request.Context())
	if id == nil || !id.HasAdminAccess() {
		return "", false
	}
	return id.UserID, true
}

func writeErr(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrNotFound):
		bizErr(c, 1002, "Resource not found")
	case errors.Is(err, ErrConflict):
		bizErr(c, 1003, "Resource version conflict; please refresh and retry")
	case errors.Is(err, ErrChannelReferenced):
		bizErr(c, 1006, "Channel is still referenced by notification rules; update or delete those rules first")
	case errors.Is(err, ErrValidation):
		bizErr(c, 1001, "Invalid request parameters")
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": "internal error"})
	}
}

// writeDeleteErr dedicated error mapping for deleteNotificationChannel: the admin_delete profile
// declares only CONCURRENT_MODIFICATION (no VERSION_CONFLICT; profile is exhaustive), so If-Match
// mismatches and delete races surface as 1004, whereas replace (admin_replace declares both codes)
// continues to use 1003 via writeErr. Pre-aligned for unmounted route (FE-F7 upgrade check, §14.3).
func writeDeleteErr(c *gin.Context, err error) {
	if errors.Is(err, ErrConflict) {
		bizErr(c, 1004, "Concurrent modification conflict; please retry")
		return
	}
	writeErr(c, err)
}

func okData(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"err_code": 0, "message": "ok", "data": data, "request_id": c.GetString("request_id")})
}

func bizErr(c *gin.Context, code int, message string) {
	// BusinessErrorResponse has additionalProperties:false: request_id and
	// retryable are required (business errors are never retryable).
	c.JSON(http.StatusOK, gin.H{"err_code": code, "message": message, "data": nil,
		"request_id": c.GetString("request_id"), "retryable": false})
}

func (h *handler) list(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	items, err := h.d.Service.ListChannels(c.Request.Context())
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, gin.H{"items": items, "page": gin.H{"next_cursor": nil, "has_more": false}})
}

func (h *handler) create(c *gin.Context) {
	actorID, ok := currentAdmin(c)
	if !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	var in WriteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeErr(c, ErrValidation)
		return
	}
	view, err := h.d.Service.CreateChannel(c.Request.Context(), actorID, &in)
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, view)
}

func (h *handler) get(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	view, err := h.d.Service.GetChannel(c.Request.Context(), c.Param("channel_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, view)
}

func (h *handler) replace(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	var in WriteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeErr(c, ErrValidation)
		return
	}
	version := parseVersion(c.GetHeader("If-Match"))
	view, err := h.d.Service.ReplaceChannel(c.Request.Context(), c.Param("channel_id"), version, &in)
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, view)
}

func (h *handler) delete(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	version := parseVersion(c.GetHeader("If-Match"))
	if err := h.d.Service.DeleteChannel(c.Request.Context(), c.Param("channel_id"), version); err != nil {
		writeDeleteErr(c, err)
		return
	}
	okData(c, nil)
}

func (h *handler) testDelivery(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	ch, err := h.d.Service.GetChannel(c.Request.Context(), c.Param("channel_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	taskID, err := h.d.Service.CreateTestDelivery(c.Request.Context(), ch.ID)
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, gin.H{"task_id": taskID, "channel_id": ch.ID})
}

// DeliveryView conforms to OpenAPI NotificationDelivery (additionalProperties:
// false — error key = last_error_code, projecting last_failure_name; address envelopes are never echoed).
// R1 M3: event reference, channel, and masked target are populated via read-side joins (PRD §3.5
// delivery record information surface), rendering address decryption failures as "<undecryptable>".
type DeliveryView struct {
	ID           string  `json:"id"`
	DomainEvent  string  `json:"domain_event_id"`
	EventType    string  `json:"event_type"`
	Channel      string  `json:"notification_channel_id"`
	ChannelKind  string  `json:"channel_kind"`
	ChannelName  string  `json:"channel_name"`
	Recipient    *string `json:"recipient_user_id"`
	MaskedTarget string  `json:"masked_target"`
	State        string  `json:"state"`
	Attempts     int     `json:"delivery_attempt_count"`
	NextAttempt  *string `json:"next_attempt_at"`
	LastErrCode  *string `json:"last_error_code"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

func (h *handler) listDeliveries(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	var rows []NotificationDeliveryModel
	q := h.d.Service.DB.WithContext(c.Request.Context()).Model(&NotificationDeliveryModel{})
	// state filter is an OpenAPI-declared parameter (NotificationDeliveryState enum).
	if v := c.Query("state"); v != "" {
		q = q.Where("state = ?", v)
	}
	if v := c.Query("after"); v != "" && uuid.Validate(v) == nil {
		q = q.Where("(created_at, id) < ((select created_at from notification_deliveries where id = ?), ?::uuid)", v, v)
	}
	if err := q.Order("created_at desc, id desc").Limit(50).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "internal error"})
		return
	}
	// R1 M3: read-side join populates event type and channel name (masked target decryption happens inside the loop).
	// R2 N1: narrow event types to the current page's domain_event_id set (IN clause), forbidding unbounded full-table reads.
	eventTypeByID := make(map[string]string, len(rows))
	if len(rows) > 0 {
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.DomainEventID)
		}
		var events []struct {
			ID        string `gorm:"column:id"`
			EventType string `gorm:"column:event_type"`
		}
		_ = h.d.Service.DB.WithContext(c.Request.Context()).
			Raw(`select id, event_type from outbox_events where id in ?`, ids).
			Scan(&events).Error
		for _, e := range events {
			eventTypeByID[e.ID] = e.EventType
		}
	}
	channels, _ := h.d.Service.ListChannels(c.Request.Context())
	channelByID := make(map[string]*ChannelView, len(channels))
	for i := range channels {
		channelByID[channels[i].ID] = &channels[i]
	}
	items := make([]DeliveryView, 0, len(rows))
	for _, r := range rows {
		v := DeliveryView{
			ID: r.ID, DomainEvent: r.DomainEventID,
			EventType: eventTypeByID[r.DomainEventID],
			Channel:   r.NotificationChannel,
			Recipient: r.RecipientUserID, State: r.State, Attempts: r.AttemptCount,
			LastErrCode: r.LastFailureName,
			CreatedAt:   r.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			UpdatedAt:   r.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		}
		if ch := channelByID[r.NotificationChannel]; ch != nil {
			v.ChannelKind = ch.Kind
			v.ChannelName = ch.Name
		}
		// Masked target: after decryption, retain at most 2 characters of the local part + domain (plaintext never leaves the read surface).
		if plain, err := h.d.Service.openDeliveryAddress(c.Request.Context(), &r); err == nil {
			v.MaskedTarget = maskAddress(plain)
		} else {
			v.MaskedTarget = "<undecryptable>"
		}
		if r.NextAttemptAt != nil {
			s := r.NextAttemptAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			v.NextAttempt = &s
		}
		items = append(items, v)
	}
	var nextCursor any
	if len(items) >= 50 && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	okData(c, gin.H{"items": items, "page": gin.H{"next_cursor": nextCursor, "has_more": len(items) >= 50}})
}

func parseVersion(raw string) int64 {
	var v int64
	_, _ = fmt.Sscanf(strings.Trim(raw, `"`), "%d", &v)
	return v
}

// ---- S007 rules surface handlers ----

func (h *handler) listRules(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	items, err := h.d.Service.ListRules(c.Request.Context())
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, gin.H{"items": items, "page": gin.H{"next_cursor": nil, "has_more": false}})
}

func (h *handler) createRule(c *gin.Context) {
	actorID, ok := currentAdmin(c)
	if !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	var in RuleWrite
	if err := c.ShouldBindJSON(&in); err != nil {
		writeErr(c, ErrValidation)
		return
	}
	view, err := h.d.Service.CreateRule(c.Request.Context(), actorID, &in)
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, view)
}

func (h *handler) getRule(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	view, err := h.d.Service.GetRule(c.Request.Context(), c.Param("rule_id"))
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, view)
}

func (h *handler) replaceRule(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	var in RuleWrite
	if err := c.ShouldBindJSON(&in); err != nil {
		writeErr(c, ErrValidation)
		return
	}
	version := parseVersion(c.GetHeader("If-Match"))
	view, err := h.d.Service.ReplaceRule(c.Request.Context(), c.Param("rule_id"), version, &in)
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, view)
}

func (h *handler) deleteRule(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	version := parseVersion(c.GetHeader("If-Match"))
	if err := h.d.Service.DeleteRule(c.Request.Context(), c.Param("rule_id"), version); err != nil {
		writeDeleteErr(c, err)
		return
	}
	okData(c, nil)
}

func (h *handler) previewRule(c *gin.Context) {
	if _, ok := currentAdmin(c); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	var in RulePreviewInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeErr(c, ErrValidation)
		return
	}
	view, err := h.d.Service.PreviewRule(c.Request.Context(), &in)
	if err != nil {
		writeErr(c, err)
		return
	}
	okData(c, view)
}
