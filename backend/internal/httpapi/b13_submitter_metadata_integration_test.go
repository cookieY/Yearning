//go:build integration

// b13_submitter_metadata_integration_test.go submitter-side metadata projection
// (RCP-20260904-SUBMITTER-METADATA-PROJECTION):
// authorization scope = flow granted to session user via permission group (enabled change_review) and
// datasource belongs to one of its stages; unauthorized returns 2014 (non-enumerable); read-only,
// column metadata omits sample values (masked evaluated live against flow vocabulary).
package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (fx *b11Fixture) b13SeedChangeFlowWithStage(t *testing.T, requesterID, dsID string) (string, string) {
	t.Helper()
	ruleSetID := uuid.NewString()
	if err := fx.db.Exec(`insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, '{}'::jsonb, ?, 1, '00000000-0000-0000-0000-000000000000', now(), now())`,
		ruleSetID, "rules-"+ruleSetID[:8], fmt.Sprintf("%064x", 1)).Error; err != nil {
		t.Fatalf("insert rule set: %v", err)
	}
	payload := map[string]any{
		"name": fmt.Sprintf("b13-submit-flow-%s", uuid.NewString()[:8]), "flow_type": "change_review",
		"enabled": true, "rule_set_id": ruleSetID,
		"stages": []map[string]any{{
			"position":         1,
			"datasource_id":    dsID,
			"approval_steps":   []map[string]any{{"position": 1, "actors": []map[string]any{{"user_id": fx.adminUserID(t)}}}},
			"execution_actors": []map[string]any{{"user_id": fx.adminUserID(t)}},
		}},
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/flows", payload, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create change flow: %d %s", code, raw)
	}
	// Flow ID is generated server-side (handler ignores client ID); read real value from response.
	flowID := body["data"].(map[string]any)["id"].(string)
	groupName := "b13-grant-" + flowID[:8]
	if code, body, raw := fx.admin.do("POST", "/api/v4/admin/permission-groups", map[string]any{
		"name": groupName, "enabled": true,
		"member_user_ids": []string{requesterID}, "granted_flow_ids": []string{flowID},
	}, true); code != 200 || errCode(body) != 0 {
		t.Fatalf("create grant group: %d %s", code, raw)
	}
	return flowID, ruleSetID
}

// b13CreateSubmitter creates a submitter user and logs in (following b3 gates userClient pattern).
func (fx *b11Fixture) b13CreateSubmitter(t *testing.T, username string) (testClient, string) {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/users", map[string]any{
		"username": username, "display_name": username, "password": adminPassword,
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create submitter user: %d %s", code, raw)
	}
	id := body["data"].(map[string]any)["id"].(string)
	client := testClient{t: t, engine: fx.engine, cookies: map[string]string{}}
	if _, b, _ := client.login(username, adminPassword); errCode(b) != 0 {
		t.Fatalf("submitter login %s: %v", username, b)
	}
	return client, id
}

func TestSubmitterMetadataProjection(t *testing.T) {
	// newB11Fixture bootstraps a real PG target DB and query target datasource (fx.qDsID, query-purpose
	// credentials pointing to qtarget DB); this test reuses the same target without creating another DB.
	fx := newB11Fixture(t)
	dsID := fx.qDsID

	// Authorization chain: change_review flow (stage = query target datasource) + permission group granting submitter.
	submitter, submitterID := fx.b13CreateSubmitter(t, "b13-sub-"+uuid.NewString()[:8])
	flowID, _ := fx.b13SeedChangeFlowWithStage(t, submitterID, dsID)

	base := "/api/v4/users/me/flows/" + flowID + "/datasources/" + dsID + "/metadata"

	// Unauthorized user (reviewer1 only has query domain identity) must get 2014 and see no schemas.
	code, body, _ := fx.reviewer1.do("GET", base+"/schemas", nil, false)
	if code != 200 || errCode(body) != 2014 {
		t.Fatalf("ungrant user must get 2014, got %d %s", code, body)
	}

	// Authorized submitter: schema list includes physical schema qtarget.
	code, body, _ = submitter.do("GET", base+"/schemas", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("schemas: %d %s", code, body)
	}
	schemas := itemsOf(body)
	found := false
	for _, s := range schemas {
		if m, ok := s.(map[string]any); ok && m["name"] == "qtarget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("qtarget schema missing: %s", body)
	}

	// Table list includes accounts/pages; column list has no sample values and evaluates masked live from flow_masking_rules.
	if err := fx.db.Exec(`insert into flow_masking_rules (flow_id, datasource_id, sensitive_columns, version, created_at, updated_at)
		values (?, ?, ?, 1, now(), now())`,
		flowID, dsID, `["secret"]`).Error; err != nil {
		t.Fatalf("seed masking rule: %v", err)
	}
	code, body, _ = submitter.do("GET", base+"/tables?schema_name=qtarget", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("tables: %d %s", code, body)
	}
	tables := itemsOf(body)
	names := map[string]bool{}
	for _, tb := range tables {
		if m, ok := tb.(map[string]any); ok {
			names[m["table_name"].(string)] = true
		}
	}
	if !names["accounts"] || !names["pages"] {
		t.Fatalf("tables missing: %s", body)
	}

	code, body, _ = submitter.do("GET", base+"/columns?schema_name=qtarget&table_name=accounts", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("columns: %d %s", code, body)
	}
	raw := fmt.Sprintf("%v", body)
	if strings.Contains(raw, "s-crumb") {
		t.Fatalf("column metadata leaked sample values: %s", raw)
	}
	masked := map[string]bool{}
	for _, c := range itemsOf(body) {
		if m, ok := c.(map[string]any); ok {
			masked[m["column_name"].(string)] = m["masked"].(bool)
		}
	}
	if !masked["secret"] {
		t.Fatalf("masked vocabulary not applied: %s", body)
	}

	// Immediately invalidated once flow is disabled (2014).
	if err := fx.db.Exec(`update flows set status = 'disabled' where id = ?`, flowID).Error; err != nil {
		t.Fatalf("disable flow: %v", err)
	}
	subCode, subBody, _ := submitter.do("GET", base+"/schemas", nil, false)
	if subCode != 200 || errCode(subBody) != 2014 {
		t.Fatalf("disabled flow must yield 2014, got %d %s", subCode, subBody)
	}

	// Disabled datasource: all three endpoints return unavailable (3010); columns shares the same
	// guard surface as schemas/tables (added assertion from Review R2).
	flowID2, _ := fx.b13SeedChangeFlowWithStage(t, submitterID, dsID)
	if err := fx.db.Exec(`update datasources set enabled = false where id = ?`, dsID).Error; err != nil {
		t.Fatalf("disable datasource: %v", err)
	}
	base2 := "/api/v4/users/me/flows/" + flowID2 + "/datasources/" + dsID + "/metadata"
	for _, face := range []string{"schemas", "tables?schema_name=qtarget", "columns?schema_name=qtarget&table_name=accounts"} {
		dc, db2, _ := submitter.do("GET", base2+"/"+face, nil, false)
		if dc != 200 || errCode(db2) != 3010 {
			t.Fatalf("disabled datasource %s must yield 3010, got %d %s", face, dc, db2)
		}
	}
	if err := fx.db.Exec(`update datasources set enabled = true where id = ?`, dsID).Error; err != nil {
		t.Fatalf("re-enable datasource: %v", err)
	}
}
