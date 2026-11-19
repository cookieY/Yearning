//go:build integration

// b6_helpers_test.go B6 HTTP test fixture helpers.
package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/workflow"
)

type ginEngine = gin.Engine

// OptionsAnonymous test session options.
func sessionOptionsB6() session.Options {
	return session.Options{CookieName: "yearning_session", Secure: false, AnonymousAllowed: true}
}

// newB3DepsForB6 constructs B3 dependencies (B6 test router needs admin endpoints to create users/datasources/flows).
func newB3DepsForB6(db *gorm.DB, identitySvc *identity.Service, sessions *identity.SessionStore,
	workflowSvc *workflow.Service, crypter *crypto.Crypter) *B3Deps {
	factory, err := datasource.NewFactory(crypter)
	if err != nil {
		panic(err)
	}
	dsRecorder, err := auditlog.NewRecorder(db)
	if err != nil {
		panic(err)
	}
	dsSvc, err := datasource.NewService(db, factory, crypter, dsRecorder)
	if err != nil {
		panic(err)
	}
	return &B3Deps{
		Identity: identitySvc, Sessions: sessions, Workflow: workflowSvc,
		Datasource: dsSvc, DB: db, Crypter: crypter, SecureCookie: false,
	}
}

func (fx *b6Fixture) adminCreateUserB6(t *testing.T, username, password string) string {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/users", map[string]any{
		"username": username, "display_name": username, "password": password,
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create user %s: %d %s", username, code, raw)
	}
	return body["data"].(map[string]any)["id"].(string)
}

// b6CreateDatasource creates a datasource and sets its identity fingerprint (required for review snapshots).
func (fx *b6Fixture) b6CreateDatasource(t *testing.T, name string) string {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/datasources", map[string]any{
		"name": name, "engine": "mysql", "compatibility_mode": "mysql",
		"deployment_kind": "native", "host": name + ".example.com", "port": 3306,
		// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: without stage mappings, MySQL
		// stage scope = datasource default database; fixture must provide database_name.
		"database_name":      "shop",
		"version_constraint": "8.0.x", "enabled": true,
		"credentials": []map[string]any{{
			"purpose": "execution", "username": "root",
			"password": map[string]any{"value": "not-a-real-secret"},
		}},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create datasource: %d %s", code, raw)
	}
	id := body["data"].(map[string]any)["id"].(string)
	if err := fx.db.Exec(`update datasources set identity_fingerprint = ? where id = ?`,
		strings.Repeat("ab", 32), id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// b6CreateFlow creates a change flow (single stage, same user as approver and executor).
func (fx *b6Fixture) b6CreateFlow(t *testing.T, name, ruleSetID, dsID, approverID string) string {
	t.Helper()
	payload := changeFlowPayload(name, ruleSetID, dsID, approverID)
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/flows", payload, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create flow %s: %d %s", name, code, raw)
	}
	return body["data"].(map[string]any)["id"].(string)
}

// b6CreateGrant grants a user permission to submit to the flow.
func (fx *b6Fixture) b6CreateGrant(t *testing.T, userID, flowID string) {
	t.Helper()
	code, body, raw := fx.admin.do("POST", "/api/v4/admin/permission-groups", map[string]any{
		"name": "b6-grant", "enabled": true,
		"member_user_ids":  []string{userID},
		"granted_flow_ids": []string{flowID},
	}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create grant: %d %s", code, raw)
	}
}

// insertRuleSet inserts a rule set directly (same as b3Fixture.insertRuleSet, for standalone fixtures).
func (fx *b6Fixture) insertRuleSet(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if err := fx.db.Exec(`insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, '{}'::jsonb, ?, 1, '00000000-0000-0000-0000-000000000000', now(), now())`,
		id, "rules-"+id[:8], fmt.Sprintf("%064x", 1)).Error; err != nil {
		t.Fatalf("insert rule set: %v", err)
	}
	return id
}
