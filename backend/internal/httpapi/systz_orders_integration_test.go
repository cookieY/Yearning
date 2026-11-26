//go:build integration

package httpapi

import (
	"fmt"
	"testing"
	"time"
)

// TestB9ListOrdersSystemTimezoneDayBoundary verifies D19 / DEF-15:
// submitted_from / submitted_to filter change orders by calendar day in general.system_timezone
// (default Asia/Shanghai, where 00:00-08:00 CST falls on the previous UTC day) and pick up
// general.system_timezone updates immediately on the very next request without restart.
func TestB9ListOrdersSystemTimezoneDayBoundary(t *testing.T) {
	fx := newB9Fixture(t)
	order := fx.b9ReadySubmit(t)
	orderID := order["id"].(string)

	// 2026-11-24T17:30:00Z == 2026-11-25 01:30:00 Asia/Shanghai (and 2026-11-24 UTC).
	earlyMorningCST := time.Date(2026, 11, 24, 17, 30, 0, 0, time.UTC)
	if err := fx.db.Exec(`update change_orders set submitted_at = ? where id = ?`, earlyMorningCST, orderID).Error; err != nil {
		t.Fatalf("update submitted_at: %v", err)
	}

	containsOrder := func(items []any, targetID string) bool {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok && m["id"] == targetID {
				return true
			}
		}
		return false
	}

	// Default Asia/Shanghai: 2026-11-25 includes 2026-11-24T17:30:00Z; 2026-11-24 excludes it.
	code, body, raw := fx.owner.do("GET", "/api/v4/change-orders?submitted_from=2026-11-25&submitted_to=2026-11-25", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list shanghai 2026-11-25: %d %s", code, raw)
	}
	if !containsOrder(dataMap(body)["items"].([]any), orderID) {
		t.Fatalf("expected Asia/Shanghai 01:30 order in 2026-11-25 filter, got %s", raw)
	}
	code, body, raw = fx.owner.do("GET", "/api/v4/change-orders?submitted_from=2026-11-24&submitted_to=2026-11-24", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list shanghai 2026-11-24: %d %s", code, raw)
	}
	if containsOrder(dataMap(body)["items"].([]any), orderID) {
		t.Fatalf("unexpected Asia/Shanghai 01:30 order in 2026-11-24 filter: %s", raw)
	}

	// Switch general.system_timezone -> UTC (impact: next_operation); cache invalidates immediately.
	code, getBody, raw := fx.admin.do("GET", "/api/v4/admin/settings/general", nil, false)
	if code != 200 || errCode(getBody) != 0 {
		t.Fatalf("get general settings: %d %s", code, raw)
	}
	ver := int64(dataMap(getBody)["version"].(float64))
	code, putBody, raw := fx.admin.putWithIfMatch("/api/v4/admin/settings/general",
		map[string]any{"settings": map[string]any{"system_timezone": "UTC"}},
		fmt.Sprintf("%d", ver),
	)
	if code != 200 || errCode(putBody) != 0 {
		t.Fatalf("replace system_timezone -> UTC: %d %s", code, raw)
	}

	// Immediate next request: UTC 2026-11-24 includes it; 2026-11-25 excludes it.
	code, body, raw = fx.owner.do("GET", "/api/v4/change-orders?submitted_from=2026-11-24&submitted_to=2026-11-24", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list UTC 2026-11-24: %d %s", code, raw)
	}
	if !containsOrder(dataMap(body)["items"].([]any), orderID) {
		t.Fatalf("expected UTC 17:30 order in 2026-11-24 filter immediately after settings update, got %s", raw)
	}
	code, body, raw = fx.owner.do("GET", "/api/v4/change-orders?submitted_from=2026-11-25&submitted_to=2026-11-25", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list UTC 2026-11-25: %d %s", code, raw)
	}
	if containsOrder(dataMap(body)["items"].([]any), orderID) {
		t.Fatalf("unexpected UTC 17:30 order in 2026-11-25 filter: %s", raw)
	}
}
