//go:build integration

// postgres_search_path_helpers_test.go provides package-local fixtures for the
// empty-mapping PostgreSQL search_path scenarios without changing shared test
// helpers owned by other work packages. After RCP-20261101-EPHEMERAL-PRECHECK,
// this file orchestrates only the ephemeral precheck two-step flow (POST /review-runs -> POST /change-orders):
// legacy async_tasks DB task chain drivers (searchPathAgentDeps/searchPathDrainWithFacts)
// were retired and removed along with StartRun.
package httpapi

import (
	"context"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// searchPathInjectFacts switches the fixture Agent's read-only fact source for
// the ephemeral precheck (equivalent to the facts parameter of legacy searchPathDrainWithFacts:
// injects fixed facts into the ephemeral review goroutine).
func searchPathInjectFacts(fx *b10Fixture, facts tool.FactSource) {
	fx.setAgentFacts(func(
		context.Context, string, application.FrozenDatasourceTarget, string,
	) (tool.FactSource, func(), error) {
		return facts, func() {}, nil
	})
}

// searchPathReadyDraftWithFacts creates draft + PUTs sql + injects facts + polls ephemeral precheck
// to ready terminal state. Returns (draftID, version, runID); version is the draft version after PUT sql
// (after RCP-20261101 Run no longer advances draft version/state).
func searchPathReadyDraftWithFacts(
	t *testing.T,
	fx *b10Fixture,
	flowID, statement string,
	respond func(provider.Request) ([]byte, int, error),
	facts tool.FactSource,
) (string, int64, string) {
	t.Helper()
	searchPathInjectFacts(fx, facts)
	return fx.b9ReadyDraft(t, flowID, statement, respond)
}

// readySearchPathDraftDiagnostic creates draft + PUTs sql + polls ephemeral precheck to terminal state,
// failing fatally with stage_results details from the run view if not ready (same diagnostic role as b9ReadyDraft).
// Returns (draftID, version, runID).
func readySearchPathDraftDiagnostic(
	t *testing.T, fx *b10Fixture, flowID, statement string,
	respond func(provider.Request) ([]byte, int, error),
) (string, int64, string) {
	t.Helper()
	code, body, raw := fx.owner.do("POST", "/api/v4/change-drafts", map[string]any{
		"flow_id": flowID, "title": "pg-path-diagnostic", "description": "pg-path",
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create draft: %d %s", code, raw)
	}
	draft := body["data"].(map[string]any)
	draftID := draft["id"].(string)
	version := int64(draft["version"].(float64))
	code, body, raw = fx.owner.doWithVersion("PUT",
		"/api/v4/change-drafts/"+draftID+"/sql",
		map[string]any{"sql": statement}, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("put SQL: %d %s", code, raw)
	}
	version++
	runID := fx.b9StartPrecheck(t, flowID, draftID, statement, respond)
	run := fx.b9WaitRunTerminal(t, runID)
	if run["state"] != "ready" {
		t.Fatalf("postgres search_path run not ready: state=%v failure=%v stages=%v",
			run["state"], run["failure_code"], run["stage_results"])
	}
	return draftID, version, runID
}

func equalStringList(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func containsSearchPathMember(members []string, want string) bool {
	for _, member := range members {
		if member == want {
			return true
		}
	}
	return false
}
