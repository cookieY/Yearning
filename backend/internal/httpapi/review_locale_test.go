//go:build integration

// review_locale_test.go output locale freezing (RCP-20261101 ephemeral precheck semantics):
// output_locale on POST /review-runs is frozen onto the ephemeral Run; same-key replay within
// the in-memory idempotency window reuses the same Run. Legacy DB task chain (output_locale in
// sql_grouping task input) was retired with StartRun.
package httpapi

import (
	"net/http"
	"testing"
)

func TestRunDraftReviewFreezesOutputLocale(t *testing.T) {
	tests := []struct {
		name       string
		locale     string
		wantLocale string
		idemKey    string
	}{
		{name: "English", locale: "en-US", wantLocale: "en-US", idemKey: "review-locale-en-US"},
		{name: "missing defaults to Chinese", wantLocale: "zh-CN", idemKey: "review-locale-default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newB9Fixture(t)
			sql := "update orders set amount = amount + 1 where id = 1;"
			runID := fx.b9StartPrecheckLocale(t, fx.lastFlowID, sql, tt.locale, tt.idemKey)

			// Semantic assertion: ephemeral Run freezes the resolved output locale.
			er := fx.svc.EphemeralGetForTest(runID, fx.ownerID)
			if er == nil {
				t.Fatalf("ephemeral run %s missing", runID)
			}
			if got := er.OutputLocale; got != tt.wantLocale {
				t.Fatalf("run output_locale = %q, want %q", got, tt.wantLocale)
			}

			// Same-key replay: in-memory idempotency window returns the same run without re-executing.
			replayed := fx.b9StartPrecheckLocale(t, fx.lastFlowID, sql, tt.locale, tt.idemKey)
			if replayed != runID {
				t.Fatalf("idempotent replay run = %q, want %q", replayed, runID)
			}
		})
	}
}

// b9StartPrecheckLocale starts an ephemeral precheck with output_locale and returns run_id.
func (fx *b9Fixture) b9StartPrecheckLocale(t *testing.T, flowID, sql, locale, idemKey string) string {
	t.Helper()
	fx.setAgentDial(fx.b9CleanDial())
	payload := map[string]any{"flow_id": flowID, "sql": sql}
	if locale != "" {
		payload["output_locale"] = locale
	}
	headers := map[string]string{
		"Idempotency-Key": idemKey,
		"X-CSRF-Token":    fx.owner.cookies["yearning_csrf"],
	}
	code, body, raw := fx.owner.doWithHeaders("POST", "/api/v4/review-runs", payload, headers)
	if code != http.StatusOK || errCode(body) != 0 {
		t.Fatalf("start ephemeral review: %d %s", code, raw)
	}
	runID := body["data"].(map[string]any)["run_id"].(string)
	if runID == "" {
		t.Fatalf("run_id missing: %s", raw)
	}
	return runID
}
