//go:build integration

// fux003_http_detail_integration_test.go closure evidence for F-UX-003 review C-1:
// 1001 envelope when starting AI precheck without SQL must carry meta.detail at the HTTP layer --
// writeReviewErr errors.As branch previously dropped Detail, leaving the fix in the application layer
// (StartRun domain.Error) without reaching the HTTP boundary.
package httpapi

import (
	"strings"
	"testing"
)

func TestB6RunReviewWithoutSQLCarriesDetail(t *testing.T) {
	fx := newB6Fixture(t)
	// RCP-20261101: ephemeral precheck validates non-empty SQL -- 1001 envelope for empty SQL must carry
	// meta.detail at the HTTP layer (F-UX-003: UI renders the specific reason rather than a generic message).
	code, body, raw := fx.owner.doWithHeaders("POST", "/api/v4/review-runs", map[string]any{
		"flow_id": fx.flowID, "sql": "",
	}, map[string]string{
		"X-CSRF-Token": fx.owner.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 1001 {
		t.Fatalf("empty-sql run want 1001 got %d %s", code, raw)
	}
	meta, _ := body["meta"].(map[string]any)
	detail, _ := meta["detail"].(string)
	if !strings.Contains(detail, "SQL") {
		t.Fatalf("meta.detail missing cause: %q (raw=%s)", detail, raw)
	}
}

func TestB6RunReviewSQLParseFailedCarriesLocationDetail(t *testing.T) {
	fx := newB6Fixture(t)
	code, body, raw := fx.owner.doWithHeaders("POST", "/api/v4/review-runs", map[string]any{
		"flow_id": fx.flowID,
		"sql":     "SELECT 1;\nSELECT 'unterminated",
	}, map[string]string{
		"X-CSRF-Token": fx.owner.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 2003 {
		t.Fatalf("parse-failed run want 2003 got %d %s", code, raw)
	}
	meta, _ := body["meta"].(map[string]any)
	detail, _ := meta["detail"].(string)
	for _, want := range []string{"line 2", "statement #2", "unterminated"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("meta.detail missing %q: %q (raw=%s)", want, detail, raw)
		}
	}
}
