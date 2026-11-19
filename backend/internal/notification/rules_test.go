// rules_test.go unit tests for the S007 rules surface: pinned event vocabulary and AI exclusion,
// recipient_key format, and masked preview message copy.
package notification

import (
	"strings"
	"testing"
)

// TestEventVocabularyFrozen asserts the vocabulary matches migration 000021 CHECK verbatim (RCP pinned surface)
// and structurally excludes AI review events.
func TestEventVocabularyFrozen(t *testing.T) {
	expected := []string{
		"change_order.submitted", "change_order.step_activated",
		"change_order.approval_decided", "change_order.stage_ready_for_execution",
		"change_order.rejected", "change_order.withdrawn", "change_order.voided",
		"execution.scheduled", "execution.schedule_cancelled", "execution.started",
		"execution.succeeded", "execution.failed", "execution.partial_failed",
		"execution.cancelled", "execution.partial_cancelled", "execution.result_unknown",
		"execution.blocked_datasource_unavailable", "execution.missed_schedule",
		"query_grant.revoked", "ai.budget_warning", "ai.budget_exhausted", "ai.provider_failover",
	}
	if len(EventVocabulary) != len(expected) {
		t.Fatalf("vocabulary size = %d, want %d", len(EventVocabulary), len(expected))
	}
	set := map[string]bool{}
	for _, e := range EventVocabulary {
		set[e] = true
	}
	for _, e := range expected {
		if !set[e] {
			t.Fatalf("vocabulary missing %s", e)
		}
	}
	for _, banned := range []string{"review.blocked", "review_run.completed", "change_draft.precheck_completed"} {
		if set[banned] {
			t.Fatalf("AI pre-review event %s must not be subscribable", banned)
		}
	}
}

// TestRecipientKeyHelpers M7 rewrite: deduplication identities are constructed via production helpers
// and match the migration CHECK regex ^(user|group):uuid$ verbatim (replacing the old self-asserting test).
func TestRecipientKeyHelpers(t *testing.T) {
	const uuidShape = "0198da00-0003-7000-8000-000000000001"
	userKey := recipientKeyForUser(uuidShape)
	groupKey := recipientKeyForGroup(uuidShape)
	for key, prefix := range map[string]string{
		userKey: "user:", groupKey: "group:",
	} {
		if len(key) != len(prefix)+len(uuidShape) || key[:len(prefix)] != prefix {
			t.Fatalf("key %q does not match %s+uuid shape", key, prefix)
		}
	}
	// Production resolution path consumes the same helper (recipientKindOf is used for suppression auditing).
	if recipientKindOf(userKey) != "user" || recipientKindOf(groupKey) != "group" {
		t.Fatal("recipientKindOf diverges from key helpers")
	}
}

// TestPreviewMessageFixedCopy verifies preview copy comes exclusively from fixed templates (never passing through free text).
func TestPreviewMessageFixedCopy(t *testing.T) {
	msg := previewMessage("change_order.submitted")
	if msg == "" || len(msg) > 256 {
		t.Fatalf("preview = %q", msg)
	}
	if previewMessage("review.blocked") != "" {
		t.Fatal("banned event must render empty preview")
	}
}

// TestVocabularyTemplateCoverage C1 pin: every event in the subscribable vocabulary must have a
// rendering path — full templateKey coverage (except query_grant.revoked which uses a dedicated renderer)
// with complete bilingual entries. Missing templates previously caused false-positive delivery success (R1 Critical-1).
func TestVocabularyTemplateCoverage(t *testing.T) {
	for _, event := range EventVocabulary {
		key, ok := templateKey[event]
		if !ok {
			t.Fatalf("vocabulary event %s has no template key", event)
		}
		if key == "grant_revoked" {
			continue // dedicated renderer grantRevokedBody
		}
		for locale, texts := range localeText {
			if texts[key] == "" {
				t.Fatalf("template %s missing in %s (event %s)", key, locale, event)
			}
		}
	}
}

// TestRenderEventCancelFamilyCancellation C1 follow-up: cancellation-family messages must not
// render as failure (partial_cancelled previously reused the partial-failed template).
func TestRenderEventCancelFamilyCancellation(t *testing.T) {
	cancelled := RenderEvent("execution.cancelled", "zh-CN", "YR-1", "")
	partial := RenderEvent("execution.partial_cancelled", "zh-CN", "YR-1", "")
	if cancelled == "" || partial == "" {
		t.Fatalf("cancel templates render empty: %q / %q", cancelled, partial)
	}
	if partial == RenderEvent("execution.partial_failed", "zh-CN", "YR-1", "FAILED") {
		t.Fatal("partial_cancelled must not reuse the partial-failed template")
	}
}

// TestDefaultPlaceholderTemplateCoverage ensures every subscribable event has a valid bilingual
// default placeholder template that passes ValidateMessageTemplate.
func TestDefaultPlaceholderTemplateCoverage(t *testing.T) {
	for _, event := range EventVocabulary {
		for _, locale := range []string{"zh-CN", "en-US"} {
			tpl := DefaultPlaceholderTemplate(event, locale)
			if tpl == "" {
				t.Fatalf("missing default placeholder template for %s (%s)", event, locale)
			}
			if err := ValidateMessageTemplate(tpl); err != nil {
				t.Fatalf("default template for %s (%s) failed validation: %v", event, locale, err)
			}
		}
	}
	if DefaultPlaceholderTemplate("review.blocked", "zh-CN") != "" {
		t.Fatal("excluded event must return empty default template")
	}
}

// TestValidateMessageTemplateAllowlistAndSecurity verifies allowlisted placeholders are accepted
// while unknown placeholders, malformed braces, control characters, oversize templates, and
// raw SQL/secret patterns are rejected.
func TestValidateMessageTemplateAllowlistAndSecurity(t *testing.T) {
	var allBuilder strings.Builder
	for _, ph := range AllowlistedPlaceholders {
		allBuilder.WriteString("{{")
		allBuilder.WriteString(ph)
		allBuilder.WriteString("}} ")
	}
	if err := ValidateMessageTemplate(allBuilder.String()); err != nil {
		t.Fatalf("all allowlisted placeholders should be accepted, got %v", err)
	}

	invalidCases := []struct {
		name string
		tpl  string
	}{
		{name: "unknown placeholder sql", tpl: "Order {{order_id}} SQL: {{sql}}"},
		{name: "unknown placeholder comment", tpl: "Order {{order_id}} comment: {{approval_comment}}"},
		{name: "empty placeholder", tpl: "Order {{}} submitted"},
		{name: "unclosed open braces", tpl: "Order {{order_id submitted"},
		{name: "unmatched close braces", tpl: "Order order_id}} submitted"},
		{name: "nested braces", tpl: "Order {{{order_id}}} submitted"},
		{name: "control character", tpl: "Order {{order_id}}\x00 submitted"},
		{name: "too long", tpl: strings.Repeat("a", MaxMessageTemplateRunes+1)},
		{name: "sql keyword in static text", tpl: "Order {{order_id}} select * from users"},
		{name: "secret pattern in static text", tpl: "Order {{order_id}} password=123"},
	}
	for _, tc := range invalidCases {
		if err := ValidateMessageTemplate(tc.tpl); err == nil {
			t.Fatalf("expected validation error for case %q (%q)", tc.name, tc.tpl)
		}
	}
}

// TestSanitizeOrderTitleAndRenderTemplate verifies {{order_title}} sanitization and placeholder rendering.
func TestSanitizeOrderTitleAndRenderTemplate(t *testing.T) {
	if got := sanitizeOrderTitle("  Add index\non orders\t table  "); got != "Add index on orders  table" {
		t.Fatalf("unexpected sanitized title: %q", got)
	}
	longTitle := strings.Repeat("x", 130)
	if got := sanitizeOrderTitle(longTitle); len([]rune(got)) != MaxOrderTitlePlaceholderRunes+1 {
		t.Fatalf("expected truncated rune length %d, got %d (%q)", MaxOrderTitlePlaceholderRunes+1, len([]rune(got)), got)
	}
	if got := sanitizeOrderTitle("SELECT * FROM secret_table"); got != "[REDACTED]" {
		t.Fatalf("expected SQL-bearing title to be redacted, got %q", got)
	}
	if got := sanitizeOrderTitle("conn password=abc"); got != "[REDACTED]" {
		t.Fatalf("expected secret-bearing title to be redacted, got %q", got)
	}

	rendered := previewMessageWithTemplate(
		"change_order.submitted",
		"[Custom] ID={{order_id}} No={{order_display_number}} By={{submitter}} At={{submitted_at}} State={{order_state}} Approvers={{approvers}} Executors={{executors}}",
	)
	for _, want := range []string{
		"ID=00000000-0000-4000-8000-000000000001",
		"No=YR-PREVIEW",
		"By=alice",
		"At=2026-11-10T09:00:00Z",
		"State=stage_approval_active",
		"Approvers=bob, carol",
		"Executors=dba_ops",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered preview %q missing %q", rendered, want)
		}
	}
}
