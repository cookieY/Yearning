// templates_render.go holds templates domain helpers and operations.
package notification

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// DefaultPlaceholderTemplate returns the built-in default template with {{placeholder}} variables for an event.
func DefaultPlaceholderTemplate(eventType, locale string) string {
	key, ok := templateKey[eventType]
	if !ok {
		return ""
	}
	if locale != "en-US" {
		locale = "zh-CN"
	}
	return defaultPlaceholderTemplates[locale][key]
}

// ValidateMessageTemplate validates a custom notification rule template against length, control-character,
// balanced {{...}} syntax, allowlisted placeholder names, and forbidden SQL/secret literal patterns.
func ValidateMessageTemplate(tpl string) error {
	if tpl == "" {
		return nil
	}
	if utf8.RuneCountInString(tpl) > MaxMessageTemplateRunes {
		return ErrValidation
	}
	for _, r := range tpl {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return ErrValidation
		}
	}
	var staticBuf strings.Builder
	rest := tpl
	for len(rest) > 0 {
		openIdx := strings.Index(rest, "{{")
		closeIdx := strings.Index(rest, "}}")
		if closeIdx >= 0 && (openIdx < 0 || closeIdx < openIdx) {
			return ErrValidation // unmatched closing braces
		}
		if openIdx < 0 {
			staticBuf.WriteString(rest)
			break
		}
		staticBuf.WriteString(rest[:openIdx])
		afterOpen := rest[openIdx+2:]
		relClose := strings.Index(afterOpen, "}}")
		if relClose < 0 {
			return ErrValidation // unclosed {{
		}
		token := strings.TrimSpace(afterOpen[:relClose])
		if token == "" || strings.ContainsAny(token, "\r\n\t {}") || !allowlistedPlaceholderSet[token] {
			return ErrValidation
		}
		rest = afterOpen[relClose+2:]
	}
	lowerStatic := strings.ToLower(staticBuf.String())
	for _, pat := range forbiddenStaticPatterns {
		if strings.Contains(lowerStatic, pat) {
			return ErrValidation
		}
	}
	return nil
}

// RenderTemplateString replaces allowlisted {{placeholder}} tokens with bounded values from vars.
func RenderTemplateString(tpl string, vars map[string]string) string {
	if tpl == "" {
		return ""
	}
	var out strings.Builder
	rest := tpl
	for len(rest) > 0 {
		openIdx := strings.Index(rest, "{{")
		if openIdx < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:openIdx])
		afterOpen := rest[openIdx+2:]
		relClose := strings.Index(afterOpen, "}}")
		if relClose < 0 {
			out.WriteString(rest[openIdx:])
			break
		}
		token := strings.TrimSpace(afterOpen[:relClose])
		if allowlistedPlaceholderSet[token] {
			val := strings.TrimSpace(vars[token])
			if val == "" {
				val = "-"
			}
			out.WriteString(val)
		} else {
			out.WriteString("-")
		}
		rest = afterOpen[relClose+2:]
	}
	return out.String()
}

// boundedText strips control characters and truncates length (unified sanitizer for display fields).
func boundedText(s string, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// sanitizeOrderTitle sanitizes a change order title into a single-line bounded string (<= 120 runes)
// and redacts any SQL statement keywords or secret assignment patterns.
func sanitizeOrderTitle(s string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	runes := []rune(cleaned)
	if len(runes) > MaxOrderTitlePlaceholderRunes {
		cleaned = string(runes[:MaxOrderTitlePlaceholderRunes]) + "…"
	}
	lower := strings.ToLower(cleaned)
	for _, pat := range forbiddenStaticPatterns {
		if strings.Contains(lower, pat) {
			return "[REDACTED]"
		}
	}
	if cleaned == "" {
		return "-"
	}
	return cleaned
}

// boundedOperator sanitizes an operator name to the safe username character set.
func boundedOperator(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '.' || r == '-'
		if !ok {
			return -1
		}
		return r
	}, s)
}

func reasonOr(reason, locale, fallback string) string {
	if reason == "" {
		if locale == "en-US" {
			return fallback
		}
		switch fallback {
		case "reviewed":
			return "\u5df2\u5904\u7406"
		default:
			return "\u5931\u8d25"
		}
	}
	return reason
}

// validReason checks whether reason matches the bounded reason code format (never passing through free text).
func validReason(reason string) bool {
	if reason == "" || len(reason) > 64 {
		return false
	}
	for _, r := range reason {
		ok := r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// orderDisplayNumber resolves display_number from a change order ID (bounded read-only lookup; falls back to truncated ID).
func (w *Worker) orderDisplayNumber(ctx context.Context, orderID string) string {
	var dn string
	if w.db != nil {
		if err := w.db.WithContext(ctx).Raw(
			`select display_number from change_orders where id = ?`, orderID).Scan(&dn).Error; err == nil && dn != "" {
			return dn
		}
	}
	return shortRef(orderID)
}

func shortRef(id string) string {
	if len(id) > 8 {
		return "YR-" + id[:8]
	}
	if id == "" {
		return "YR-PREVIEW"
	}
	return "YR-" + id
}

// EventBody renders the complete notification body for an outbox event using the default template.
func (w *Worker) EventBody(ctx context.Context, ev *OutboxEventModel, reason string) string {
	return w.EventBodyWithTemplate(ctx, ev, reason, "")
}

// EventBodyWithTemplate renders the notification body for an outbox event, using customTemplate when
// non-empty and falling back to the built-in default template for ev.EventType.
func (w *Worker) EventBodyWithTemplate(ctx context.Context, ev *OutboxEventModel, reason, customTemplate string) string {
	if _, ok := templateKey[ev.EventType]; !ok {
		return ""
	}
	locale := w.getLocale()
	customTemplate = strings.TrimSpace(customTemplate)
	if customTemplate != "" && ValidateMessageTemplate(customTemplate) != nil {
		customTemplate = ""
	}
	tpl := customTemplate
	if tpl == "" {
		tpl = DefaultPlaceholderTemplate(ev.EventType, locale)
	}
	if tpl == "" {
		return ""
	}
	var vars map[string]string
	switch {
	case ev.EventType == "query_grant.revoked":
		vars = w.buildGrantRevokedTemplateVars(ctx, ev, reason, locale)
	case strings.HasPrefix(ev.EventType, "change_order.") || strings.HasPrefix(ev.EventType, "execution."):
		vars = w.buildOrderTemplateVars(ctx, ev, reason, locale)
	default:
		vars = w.buildAITemplateVars(ev, reason, locale)
	}
	body := RenderTemplateString(tpl, vars)
	if body == "" {
		return ""
	}
	// S007: append back-link to the system when general.public_base_url is configured and the template
	// did not already include {{order_url}} explicitly.
	if baseURL := w.getPublicBaseURL(); baseURL != "" && vars["order_id"] != "" && vars["order_id"] != "-" &&
		!strings.Contains(tpl, "{{order_url}}") &&
		(strings.HasPrefix(ev.EventType, "change_order.") || strings.HasPrefix(ev.EventType, "execution.")) {
		body += "\n" + strings.TrimRight(baseURL, "/") + "/changes/orders/" + vars["order_id"]
	}
	return body
}

func joinBoundedActors(users []string, maxCount int) string {
	cleaned := make([]string, 0, len(users))
	for _, u := range users {
		if c := boundedOperator(u); c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) == 0 {
		return "-"
	}
	if len(cleaned) > maxCount {
		cleaned = append(cleaned[:maxCount], "…")
	}
	return strings.Join(cleaned, ", ")
}

func nonEmptyOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// buildGrantRevokedTemplateVars resolves placeholders for query_grant.revoked events.
func (w *Worker) buildGrantRevokedTemplateVars(ctx context.Context, ev *OutboxEventModel, reason, locale string) map[string]string {
	var payload struct {
		GrantID    string `json:"grant_id"`
		RevokedBy  string `json:"revoked_by"`
		ReasonCode string `json:"reason_code"`
	}
	_ = json.Unmarshal(ev.Payload, &payload)
	grantID := payload.GrantID
	if grantID == "" {
		grantID = strings.TrimPrefix(ev.Subject, "query-grants/")
	}
	var row struct {
		RequestID string `gorm:"column:query_access_request_id"`
		Operator  string `gorm:"column:revoked_by_username_snapshot"`
	}
	if w.db != nil && grantID != "" {
		_ = w.db.WithContext(ctx).Raw(
			`select query_access_request_id, coalesce(revoked_by_username_snapshot, '')
			 from query_grants where id = ?`, grantID).Scan(&row).Error
	}
	operator := payload.RevokedBy
	if operator == "" {
		operator = row.Operator
	}
	var flowName string
	var datasources []string
	if w.db != nil && row.RequestID != "" {
		_ = w.db.WithContext(ctx).Raw(
			`select flow_name_snapshot from query_access_requests where id = ?`,
			row.RequestID).Scan(&flowName).Error
		_ = w.db.WithContext(ctx).Raw(
			`select datasource_name_snapshot from query_access_request_datasources
			 where request_id = ? order by datasource_name_snapshot limit 4`,
			row.RequestID).Scan(&datasources).Error
	}
	dsJoined := "-"
	if len(datasources) > 0 {
		shown := datasources
		if len(shown) > 3 {
			shown = append(shown[:3], "…")
		}
		dsJoined = strings.Join(shown, ", ")
	}
	if !validReason(reason) {
		reason = payload.ReasonCode
	}
	if !validReason(reason) {
		reason = "-"
	}
	occurredAt := "-"
	if !ev.OccurredAt.IsZero() {
		occurredAt = ev.OccurredAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return map[string]string{
		"event_type":        ev.EventType,
		"event_label":       nonEmptyOr(eventLabels[locale][ev.EventType], ev.EventType),
		"grant_flow_name":   nonEmptyOr(boundedText(flowName, 128), "-"),
		"grant_datasources": nonEmptyOr(boundedText(dsJoined, 384), "-"),
		"reason_code":       reason,
		"revoked_by":        nonEmptyOr(boundedOperator(operator), "-"),
		"revoked_at":        occurredAt,
		"occurred_at":       occurredAt,
	}
}

// buildAITemplateVars resolves placeholders for ai.* budget/failover events.
func (w *Worker) buildAITemplateVars(ev *OutboxEventModel, reason, locale string) map[string]string {
	if !validReason(reason) {
		reason = "-"
	}
	occurredAt := "-"
	if !ev.OccurredAt.IsZero() {
		occurredAt = ev.OccurredAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return map[string]string{
		"event_type":  ev.EventType,
		"event_label": nonEmptyOr(eventLabels[locale][ev.EventType], ev.EventType),
		"occurred_at": occurredAt,
		"reason_code": reason,
	}
}

// sampleTemplateVars returns deterministic preview placeholder values when no sample_order_id is supplied.
func sampleTemplateVars(locale, eventType string) map[string]string {
	if locale != "en-US" {
		locale = "zh-CN"
	}
	return map[string]string{
		"order_display_number":  "YR-PREVIEW",
		"order_no":              "YR-PREVIEW",
		"order_id":              "00000000-0000-4000-8000-000000000001",
		"order_title":           "orders_index_change",
		"event_type":            eventType,
		"event_label":           nonEmptyOr(eventLabels[locale][eventType], eventType),
		"order_state":           "stage_approval_active",
		"flow_name":             "prod-standard-flow",
		"stage_position":        "1",
		"stage_name":            "prod-primary",
		"datasource_name":       "prod-mysql-01",
		"datasource_engine":     "mysql",
		"submitter":             "alice",
		"submitter_username":    "alice",
		"submitted_at":          "2026-11-10T09:00:00Z",
		"approvers":             "bob, carol",
		"approved_by":           "bob",
		"reviewer":              "bob",
		"approval_decision":     "approve",
		"approved_at":           "2026-11-10T09:05:00Z",
		"reviewed_at":           "2026-11-10T09:05:00Z",
		"executors":             "dba_ops",
		"executed_by":           "dba_ops",
		"executor":              "dba_ops",
		"execution_started_at":  "2026-11-10T09:10:00Z",
		"executed_at":           "2026-11-10T09:10:03Z",
		"execution_finished_at": "2026-11-10T09:10:03Z",
		"reason_code":           "REVIEWED",
		"order_url":             "/changes/orders/00000000-0000-4000-8000-000000000001",
		"grant_flow_name":       "readonly-query-flow",
		"grant_datasources":     "prod-mysql-01",
		"revoked_by":            "admin",
		"revoked_at":            "2026-11-10T09:15:00Z",
		"occurred_at":           "2026-11-10T09:15:00Z",
	}
}
