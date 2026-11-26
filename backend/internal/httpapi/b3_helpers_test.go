//go:build integration

// Fixture helpers for gate tests: creating rule sets / datasources / flows / permission groups / users.
package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/identity"
)

type gormDB = *gorm.DB

func newJSONRequest(method, path string, raw []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

const http401 = http.StatusUnauthorized
const http403 = http.StatusForbidden

func errCode(body map[string]any) int {
	if body == nil {
		return -1
	}
	if v, ok := body["err_code"].(float64); ok {
		return int(v)
	}
	return -1
}

func itemsOf(body map[string]any) []any {
	data, _ := body["data"].(map[string]any)
	if data == nil {
		return nil
	}
	items, _ := data["items"].([]any)
	return items
}

func identityRequestMeta() identity.RequestMeta {
	return identity.RequestMeta{}
}

func scanStrings(t *testing.T, db gormDB, query string) []string {
	t.Helper()
	var out []string
	if err := db.Raw(query).Scan(&out).Error; err != nil {
		t.Fatalf("scan %s: %v", query, err)
	}
	return out
}

func (fx *b3Fixture) insertRuleSet(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if err := fx.db.Exec(`insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, '{}'::jsonb, ?, 1, '00000000-0000-0000-0000-000000000000', now(), now())`,
		id, "rules-"+id[:8], fmt.Sprintf("%064x", 1)).Error; err != nil {
		t.Fatalf("insert rule set: %v", err)
	}
	return id
}

func (fx *b3Fixture) adminCreateDatasource(t *testing.T, name string) string {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", map[string]any{
		"name": name, "engine": "mysql", "compatibility_mode": "mysql",
		"deployment_kind": "native", "host": name + ".invalid", "port": 3306,
		"version_constraint": "8.0.x", "enabled": true,
		"credentials": []map[string]any{{
			"purpose": "execution", "username": "root",
			"password": map[string]any{"value": "not-a-real-secret"},
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create datasource: %d %s", code, raw)
	}
	id, _ := body["data"].(map[string]any)["id"].(string)
	return id
}

func (fx *b3Fixture) adminCreateUser(t *testing.T, username, password string) string {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/users", map[string]any{
		"username": username, "display_name": username, "password": password,
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create user %s: %d %s", username, code, raw)
	}
	id, _ := body["data"].(map[string]any)["id"].(string)
	return id
}

func (fx *b3Fixture) adminDeleteUser(t *testing.T, userID string) {
	t.Helper()
	version := userVersion(t, fx.db, userID)
	code, body, raw := fx.admin.doWithVersion("DELETE", "/api/v4/admin/users/"+userID, nil, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("delete user: %d %s", code, raw)
	}
}

func (fx *b3Fixture) adminCreateChangeFlow(t *testing.T, name, ruleSetID, dsID, approverID string) string {
	t.Helper()
	payload := changeFlowPayload(name, ruleSetID, dsID, approverID)
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/flows", payload, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create flow %s: %d %s", name, code, raw)
	}
	id, _ := body["data"].(map[string]any)["id"].(string)
	return id
}

func changeFlowPayload(name, ruleSetID, dsID, approverID string) map[string]any {
	return map[string]any{
		"name": name, "flow_type": "change_review", "enabled": true, "rule_set_id": ruleSetID,
		"stages": []map[string]any{{
			"position": 1, "datasource_id": dsID,
			"approval_steps": []map[string]any{{
				"position": 1, "actors": []map[string]any{{"user_id": approverID}},
			}},
			"execution_actors": []map[string]any{{"user_id": approverID}},
		}},
	}
}

func (fx *b3Fixture) adminCreateGroup(t *testing.T, name string, members, flows []string) string {
	t.Helper()
	if members == nil {
		members = []string{}
	}
	if flows == nil {
		flows = []string{}
	}
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/permission-groups", map[string]any{
		"name": name, "enabled": true, "member_user_ids": members, "granted_flow_ids": flows,
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create group %s: %d %s", name, code, raw)
	}
	id, _ := body["data"].(map[string]any)["id"].(string)
	return id
}

func (fx *b3Fixture) adminDeleteGroup(t *testing.T, groupID string) {
	t.Helper()
	var version int64
	_ = fx.db.Raw(`select version from permission_groups where id = ?`, groupID).Scan(&version).Error
	code, body, raw := fx.admin.doWithVersion("DELETE", "/api/v4/admin/permission-groups/"+groupID, nil, version)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("delete group: %d %s", code, raw)
	}
}

func (fx *b3Fixture) mustAdminID(t *testing.T) string {
	t.Helper()
	var id string
	if err := fx.db.Raw(`select id from users where username = 'admin'`).Scan(&id).Error; err != nil || id == "" {
		t.Fatalf("admin id: %v", err)
	}
	return id
}

func flowVersion(t *testing.T, db gormDB, flowID string) int64 {
	t.Helper()
	var version int64
	if err := db.Raw(`select version from flows where id = ?`, flowID).Scan(&version).Error; err != nil {
		t.Fatalf("flow version: %v", err)
	}
	return version
}

func userVersion(t *testing.T, db gormDB, userID string) int64 {
	t.Helper()
	var version int64
	if err := db.Raw(`select version from users where id = ?`, userID).Scan(&version).Error; err != nil {
		t.Fatalf("user version: %v", err)
	}
	return version
}

func dsVersion(t *testing.T, db gormDB, dsID string) int64 {
	t.Helper()
	var version int64
	if err := db.Raw(`select version from datasources where id = ?`, dsID).Scan(&version).Error; err != nil {
		t.Fatalf("ds version: %v", err)
	}
	return version
}

// doWithVersion executes a request with the If-Match version header.
func (cl *testClient) doWithVersion(method, path string, body any, version int64) (int, map[string]any, string) {
	return cl.doWithHeaders(method, path, body, map[string]string{
		"If-Match":     fmt.Sprintf(`"%d"`, version),
		"X-CSRF-Token": cl.cookies["yearning_csrf"],
	})
}

func (cl *testClient) doWithHeaders(method, path string, body any, headers map[string]string) (int, map[string]any, string) {
	cl.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			cl.t.Fatal(err)
		}
	}
	req := newJSONRequest(method, path, raw)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if len(cl.cookies) > 0 {
		var parts []string
		for k, v := range cl.cookies {
			parts = append(parts, k+"="+v)
		}
		req.Header.Set("Cookie", joinSemis(parts))
	}
	rec := newRecorder()
	cl.engine.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Value == "" || c.MaxAge < 0 {
			delete(cl.cookies, c.Name)
		} else {
			cl.cookies[c.Name] = c.Value
		}
	}
	bodyText := rec.Body.String()
	var parsed map[string]any
	_ = json.Unmarshal([]byte(bodyText), &parsed)
	return rec.Code, parsed, bodyText
}

func joinSemis(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}
