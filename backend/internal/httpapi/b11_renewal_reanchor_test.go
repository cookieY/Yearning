//go:build integration

// b11_renewal_reanchor_test.go OBS-009 renewal re-anchor parity: HandleRenewQueryGrant
// uses the same semantics as creation (requested_until = system timezone wall clock -> UTC instant),
// closing the historical review F8 inconsistency where renewal passed through the raw instant (2026-11-02).
package httpapi

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/platform/systz"
)

// TestB11RenewalReanchorsRequestedUntil re-anchors renewal requested_until by
// general.system_timezone wall clock (wall clock digits -8h under Asia/Shanghai),
// rather than passing through the raw UTC instant.
func TestB11RenewalReanchorsRequestedUntil(t *testing.T) {
	fx := newB11Fixture(t)
	// Explicitly persist Asia/Shanghai (also the default when row is missing; explicit seed makes the assertion
	// independent of any existing general settings in the environment).
	if err := fx.db.Exec(`
		insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('general', 1, jsonb_build_object('system_timezone', 'Asia/Shanghai'), 1, '00000000-0000-0000-0000-000000000000'::uuid, now(), now())
		on conflict (namespace) do update set settings = system_settings.settings || jsonb_build_object('system_timezone', 'Asia/Shanghai')`).Error; err != nil {
		t.Fatal(err)
	}
	systz.InvalidateDB(fx.db)
	// Request -> approval -> Grant (setup uses creation surface; after +24h wall-clock re-anchoring it remains
	// strictly in the future and within the 30-day limit).
	code, body, raw := fx.requester.doWithHeaders("POST", "/api/v4/query-access-requests", map[string]any{
		"flow_id":         fx.qFlowID,
		"datasource_ids":  []string{fx.qDsID},
		"requested_until": time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z"),
		"reason":          "b11 renewal reanchor setup",
	}, map[string]string{
		"Idempotency-Key": "b11-rra-" + uuid.NewString()[:12],
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create access request: %d %s", code, raw)
	}
	reqView := body["data"].(map[string]any)
	reqID := reqView["id"].(string)
	version := int64(reqView["version"].(float64))
	if code, body, raw = fx.reviewer1.doWithHeaders("POST", "/api/v4/query-access-requests/"+reqID+"/decisions",
		map[string]any{"decision": "approve", "comment": "ok"}, map[string]string{
			"Idempotency-Key": "b11-rra-dec-" + reqID[:8],
			"If-Match":        fmt.Sprintf(`"%d"`, version),
			"X-CSRF-Token":    fx.reviewer1.cookies["yearning_csrf"],
		}); code != 200 || errCode(body) != 0 {
		t.Fatalf("decide: %d %s", code, raw)
	}
	grantID := body["data"].(map[string]any)["grant_id"].(string)

	// Renewal wall clock: +48h wall-clock digits (with Z suffix). Expected = same digits interpreted
	// under Asia/Shanghai into UTC (8 hours earlier than the raw UTC instant).
	wall := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Minute)
	wallStr := wall.Format("2006-01-02T15:04:05Z")
	if code, body, raw = fx.requester.doWithHeaders("POST", "/api/v4/query-grants/"+grantID+"/renewal-requests",
		map[string]any{"requested_until": wallStr, "reason": "b11 renewal reanchor"}, map[string]string{
			"Idempotency-Key": "b11-rra-renew-" + uuid.NewString()[:12],
			"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
		}); code != 200 || errCode(body) != 0 {
		t.Fatalf("renew: %d %s", code, raw)
	}
	renewed := body["data"].(map[string]any)
	if renewed["requested_until"] == nil {
		t.Fatalf("renewal view carries no requested_until: %v", renewed)
	}
	got, err := time.Parse(time.RFC3339Nano, renewed["requested_until"].(string))
	if err != nil {
		t.Fatalf("renewal requested_until not RFC3339: %v", err)
	}
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(wall.UTC().Year(), wall.UTC().Month(), wall.UTC().Day(),
		wall.UTC().Hour(), wall.UTC().Minute(), 0, 0, sh).UTC()
	if !got.Equal(want) {
		t.Fatalf("renewal requested_until not reanchored: got %s want %s (wall %s)",
			got.UTC().Format(time.RFC3339Nano), want.Format(time.RFC3339Nano), wallStr)
	}
}
