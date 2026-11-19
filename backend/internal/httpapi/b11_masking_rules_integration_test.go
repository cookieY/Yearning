//go:build integration

package httpapi

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
)

// TestB11MaskingRulesHTTPRoundTrip verifies management API, normalized persistence, authorization boundary,
// and proves via production Services (without injected MaskingSource) that queries and metadata read the real vocabulary.
func TestB11MaskingRulesHTTPRoundTrip(t *testing.T) {
	fx := newB11Fixture(t)
	base := "/api/v4/admin/flows/" + fx.qFlowID
	ruleURL := base + "/datasources/" + fx.qDsID + "/masking-rules"

	code, body, raw := fx.admin.do("PUT", ruleURL, map[string]any{
		"sensitive_columns": []string{" Secret ", "secret", "NAME"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		_, directErr := fx.q.ReplaceMaskingRule(t.Context(), queryapp.Actor{
			UserID: fx.adminUserID(t), Username: "admin", IsBuiltinAdmin: true,
		}, fx.qFlowID, fx.qDsID, []string{"secret", "name"})
		t.Fatalf("put masking rule: %d %s; direct error: %v", code, raw, directErr)
	}
	rule := body["data"].(map[string]any)
	if got := stringSlice(rule["sensitive_columns"]); !equalStrings(got, []string{"secret", "name"}) {
		t.Fatalf("normalized response = %#v", got)
	}
	if rule["version"].(float64) != 1 {
		t.Fatalf("initial version = %#v", rule["version"])
	}

	code, body, raw = fx.admin.do("GET", base+"/masking-rules", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("list masking rules: %d %s", code, raw)
	}
	items := itemsOf(body)
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	if got := stringSlice(items[0].(map[string]any)["sensitive_columns"]); !equalStrings(got, []string{"secret", "name"}) {
		t.Fatalf("listed normalized columns = %#v", got)
	}

	// Same (flow, datasource) is an atomic upsert; without relying on client If-Match, server transactions
	// serialize writes and increment version, avoiding a brief empty-vocabulary window from delete+insert.
	code, body, raw = fx.admin.do("PUT", ruleURL, map[string]any{
		"sensitive_columns": []string{"secret"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace masking rule: %d %s", code, raw)
	}
	if body["data"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("replacement version = %#v", body["data"])
	}
	persisted, err := fx.q.MaskingSource.Vocabulary(t.Context(), fx.db, fx.qFlowID, fx.qDsID)
	if err != nil || !equalStrings(persisted, []string{"secret"}) {
		t.Fatalf("persisted production vocabulary = %#v err=%v", persisted, err)
	}
	var audit struct {
		Count    int64
		Metadata string
	}
	if err := fx.db.Raw(`select count(*) as count, coalesce(max(metadata::text), '') as metadata
		from audit_events where event_type = 'flow_masking_rule.replaced'`).Scan(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.Count != 2 || strings.Contains(strings.ToLower(audit.Metadata), "secret") {
		t.Fatalf("masking audit must exclude vocabulary: %+v", audit)
	}

	// Write surface without If-Match: two concurrent transactions must serialize and each increment version once.
	adminActor := queryapp.Actor{UserID: fx.adminUserID(t), Username: "admin", IsBuiltinAdmin: true}
	versions := make(chan int64, 2)
	errs := make(chan error, 2)
	var writers sync.WaitGroup
	for _, columns := range [][]string{{"secret", "note"}, {"secret"}} {
		columns := columns
		writers.Add(1)
		go func() {
			defer writers.Done()
			updated, err := fx.q.ReplaceMaskingRule(t.Context(), adminActor, fx.qFlowID, fx.qDsID, columns)
			if err != nil {
				errs <- err
				return
			}
			versions <- updated.Version
		}()
	}
	writers.Wait()
	close(errs)
	close(versions)
	for err := range errs {
		t.Fatalf("concurrent replace: %v", err)
	}
	gotVersions := make([]int, 0, 2)
	for version := range versions {
		gotVersions = append(gotVersions, int(version))
	}
	sort.Ints(gotVersions)
	if len(gotVersions) != 2 || gotVersions[0] != 3 || gotVersions[1] != 4 {
		t.Fatalf("concurrent versions = %#v", gotVersions)
	}
	// Final vocabulary is determined by the last concurrent write, and both candidates include secret; since
	// subsequent production read assertions require a fixed final vocabulary, explicitly write secret-only once more.
	reset, err := fx.q.ReplaceMaskingRule(t.Context(), adminActor, fx.qFlowID, fx.qDsID, []string{"secret"})
	if err != nil || reset.Version != 5 {
		t.Fatalf("reset deterministic masking vocabulary: view=%#v err=%v", reset, err)
	}

	// Non-builtin admin is rejected for both reads and writes.
	if code, _, _ := fx.requester.do("GET", base+"/masking-rules", nil, false); code != http403 {
		t.Fatalf("non-admin GET status = %d", code)
	}
	if code, _, _ := fx.requester.do("PUT", ruleURL, map[string]any{
		"sensitive_columns": []string{"secret"},
	}, true); code != http403 {
		t.Fatalf("non-admin PUT status = %d", code)
	}

	// Datasource exists but is not in flow_query_capabilities; cannot create bypass rules.
	otherDS := fx.b6CreateDatasource(t, "b11-mask-outside-"+fx.qDsID[:8])
	outsideURL := base + "/datasources/" + otherDS + "/masking-rules"
	if code, body, _ := fx.admin.do("PUT", outsideURL, map[string]any{
		"sensitive_columns": []string{"secret"},
	}, true); code != 200 || errCode(body) != 1002 {
		t.Fatalf("outside-flow datasource = %d %#v", code, body)
	}

	// Production service reads from flow_masking_rules by default: aliased results and metadata share the same vocabulary.
	sessionID := fx.b11ActiveSessionHTTP(t)
	view, err := fx.q.ExecuteSelect(t.Context(), queryapp.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select secret as exposed, note from qtarget.accounts order by id",
	})
	if err != nil {
		t.Fatalf("execute with production vocabulary: %v", err)
	}
	for _, row := range view.Rows {
		if row[0] != queryapp.MaskedPlaceholder || row[1] == queryapp.MaskedPlaceholder {
			t.Fatalf("production vocabulary masking failed: columns=%#v row=%#v", view.Columns, row)
		}
	}
	cols, err := fx.q.ListColumns(t.Context(), fx.b11QActor(), sessionID, fx.qDsID, fx.qSchema, "accounts")
	if err != nil {
		t.Fatalf("list columns with production vocabulary: %v", err)
	}
	for _, col := range cols {
		if col.ColumnName == "secret" && !col.Masked {
			t.Fatalf("metadata did not use production vocabulary: %#v", cols)
		}
	}

	// Rule changes only affect new Query Runs; already open cursors keep the mask flags frozen at Run creation.
	if _, err := fx.q.ReplaceMaskingRule(t.Context(), adminActor, fx.qFlowID, fx.qDsID, []string{"secret"}); err != nil {
		t.Fatal(err)
	}
	frozen, err := fx.q.ExecuteSelect(t.Context(), queryapp.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select secret from qtarget.accounts order by id", PageSize: 1,
	})
	if err != nil || frozen.Page.NextCursor == nil || frozen.Rows[0][0] != queryapp.MaskedPlaceholder {
		t.Fatalf("create frozen masking cursor: view=%#v err=%v", frozen, err)
	}
	if _, err := fx.q.ReplaceMaskingRule(t.Context(), adminActor, fx.qFlowID, fx.qDsID, []string{}); err != nil {
		t.Fatal(err)
	}
	next, err := fx.q.FetchPage(t.Context(), queryapp.FetchPageInput{
		Actor: fx.b11QActor(), ExecutionID: frozen.ExecutionID,
		CursorToken: *frozen.Page.NextCursor, Purpose: "display",
	})
	if err != nil || next.Rows[0][0] != queryapp.MaskedPlaceholder {
		t.Fatalf("existing cursor did not retain mask plan: view=%#v err=%v", next, err)
	}
	fresh, err := fx.q.ExecuteSelect(t.Context(), queryapp.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select secret from qtarget.accounts order by id",
	})
	if err != nil || fresh.Rows[0][0] == queryapp.MaskedPlaceholder {
		t.Fatalf("new run did not observe replacement vocabulary: view=%#v err=%v", fresh, err)
	}

	// Vocabulary read failure must block new Query Runs and metadata rather than continuing with an empty vocabulary.
	fx.q.MaskingSource = failingVocabulary{}
	if _, err := fx.q.ExecuteSelect(t.Context(), queryapp.ExecuteSelectInput{
		Actor: fx.b11QActor(), SessionID: sessionID, DatasourceID: fx.qDsID,
		SchemaName: fx.qSchema, SQL: "select secret from qtarget.accounts",
	}); err == nil {
		t.Fatal("query continued after masking vocabulary read failure")
	}
	if _, err := fx.q.ListColumns(t.Context(), fx.b11QActor(), sessionID, fx.qDsID, fx.qSchema, "accounts"); err == nil {
		t.Fatal("metadata continued after masking vocabulary read failure")
	}
}

// TestB11MaskingRulesFollowFlowLifecycle verifies rules are datasource-scoped sub-aggregates of a query flow:
// template edits preserve rules for remaining datasources, clean up rules for removed capabilities, and physical
// flow deletion cleans up all rules. All three actions must execute within the same management API transaction.
func TestB11MaskingRulesFollowFlowLifecycle(t *testing.T) {
	fx := newB11Fixture(t)
	actor := queryapp.Actor{
		UserID: fx.adminUserID(t), Username: "admin", IsBuiltinAdmin: true,
	}
	flowID := fx.b11QueryFlow(t, fx.reviewer1ID, fx.reviewer2ID, fx.qDsID, true, false)
	if _, err := fx.q.ReplaceMaskingRule(t.Context(), actor, flowID, fx.qDsID, []string{"secret"}); err != nil {
		t.Fatalf("seed masking rule: %v", err)
	}

	// Capabilities that remain in the flow retain their independent rules and versions after full replacement.
	code, body, raw := fx.admin.doWithVersion(
		"PUT", "/api/v4/admin/flows/"+flowID,
		queryFlowReplacement("masking-lifecycle-retained", fx.reviewer1ID, fx.reviewer2ID, fx.qDsID),
		flowVersion(t, fx.db, flowID),
	)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace flow retaining datasource: %d %s", code, raw)
	}
	assertMaskingRuleCount(t, fx.db, flowID, fx.qDsID, 1)

	// When a capability is replaced, old datasource rules must be cleaned up within the flow transaction.
	replacementDS := fx.b6CreateDatasource(t, "masking-lifecycle-replacement")
	code, body, raw = fx.admin.doWithVersion(
		"PUT", "/api/v4/admin/flows/"+flowID,
		queryFlowReplacement("masking-lifecycle-replaced", fx.reviewer1ID, fx.reviewer2ID, replacementDS),
		flowVersion(t, fx.db, flowID),
	)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace flow changing datasource: %d %s", code, raw)
	}
	assertMaskingRuleCount(t, fx.db, flowID, fx.qDsID, 0)

	// New capability can configure rules; physical deletion of a flow with zero business references leaves no orphan rules.
	if _, err := fx.q.ReplaceMaskingRule(t.Context(), actor, flowID, replacementDS, []string{"secret"}); err != nil {
		t.Fatalf("seed replacement masking rule: %v", err)
	}
	code, body, raw = fx.admin.doWithVersion(
		"DELETE", "/api/v4/admin/flows/"+flowID, nil, flowVersion(t, fx.db, flowID),
	)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("delete flow: %d %s", code, raw)
	}
	assertMaskingRuleCount(t, fx.db, flowID, replacementDS, 0)
}

func queryFlowReplacement(name, reviewer1, reviewer2, datasourceID string) map[string]any {
	return map[string]any{
		"name": name, "flow_type": "query_access", "enabled": true,
		"approval_steps": []map[string]any{{
			"position": 1,
			"actors":   []map[string]any{{"user_id": reviewer1}, {"user_id": reviewer2}},
		}},
		"query_capabilities": []map[string]any{{
			"datasource_id": datasourceID, "can_query": true, "can_export": false,
		}},
	}
}

func assertMaskingRuleCount(t *testing.T, db *gorm.DB, flowID, datasourceID string, want int64) {
	t.Helper()
	var got int64
	if err := db.Table("flow_masking_rules").
		Where("flow_id = ? and datasource_id = ?", flowID, datasourceID).
		Count(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("masking rule count for flow=%s datasource=%s: got %d want %d", flowID, datasourceID, got, want)
	}
}

func TestB11MaskingRulesValidation(t *testing.T) {
	fx := newB11Fixture(t)
	ruleURL := "/api/v4/admin/flows/" + fx.qFlowID + "/datasources/" + fx.qDsID + "/masking-rules"
	// List and write surfaces share the same query_access boundary; a change_review flow cannot masquerade
	// as a valid query flow with no configured rules.
	changeFlowURL := "/api/v4/admin/flows/" + fx.lastFlowID + "/masking-rules"
	if code, body, _ := fx.admin.do("GET", changeFlowURL, nil, false); code != 200 || errCode(body) != 1002 {
		t.Fatalf("change-review masking list = %d %#v", code, body)
	}

	cases := []struct {
		name    string
		columns []string
	}{
		{name: "trimmed empty", columns: []string{"   "}},
		{name: "too long", columns: []string{strings.Repeat("x", 129)}},
		{name: "too many", columns: make([]string, 257)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "too many" {
				for i := range tc.columns {
					tc.columns[i] = "column-" + strings.Repeat("x", i%100)
				}
			}
			code, body, _ := fx.admin.do("PUT", ruleURL, map[string]any{
				"sensitive_columns": tc.columns,
			}, true)
			if code != 200 || errCode(body) != 1001 {
				t.Fatalf("status/body = %d %#v", code, body)
			}
		})
	}

	code, body, raw := fx.admin.do("PUT", ruleURL, map[string]any{
		"sensitive_columns": []string{" ÄBC ", "äbc"},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("unicode-normalized masking rule: %d %s", code, raw)
	}
	if got := stringSlice(body["data"].(map[string]any)["sensitive_columns"]); !equalStrings(got, []string{"äbc"}) {
		t.Fatalf("unicode-normalized columns = %#v", got)
	}
}

func stringSlice(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(string))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type failingVocabulary struct{}

func (failingVocabulary) Vocabulary(context.Context, *gorm.DB, string, string) ([]string, error) {
	return nil, errors.New("injected masking vocabulary failure")
}
