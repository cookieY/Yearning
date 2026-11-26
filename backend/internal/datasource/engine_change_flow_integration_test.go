//go:build integration

// engine_change_flow_integration_test.go integration tests for datasource engine change reference consistency
// (BUG-005 Option A / independent review F3 second fallback path).
//
// Scenario: an administrator changes a datasource's engine/compatibility_mode, silently turning an existing
// single-engine change_review flow that references it into a mixed-engine flow; during stage 2 execution
// preflight, dialect fingerprints are recomputed under the new engine, mismatching the dialect frozen at
// review time and permanently wedging with error 3006.
//
// This test directly verifies engineChangeConflictsFlow (the guard condition in datasource.Replace)
// against real PostgreSQL across four cases:
//   - Other stages in a multi-stage flow differ from the new engine -> conflict (change must be rejected)
//   - Other stages in a multi-stage flow match the new engine -> no conflict (change allowed)
//   - Single-stage flow -> no conflict (no cross-engine issue exists)
//   - query_access flow -> no conflict (not governed by this rule)
package datasource

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

// openEngineTestDB connects to the integration test database under the shared flock before truncating tables.
func openEngineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testkit.OpenPG(t)
	for _, tbl := range []string{
		"flow_stages", "flows", "datasources",
	} {
		if err := db.Exec("truncate table " + tbl + " cascade").Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	return db
}

// newEngineTestService constructs a real Service (Replace requires both crypter and audit recorder).
func newEngineTestService(t *testing.T, db *gorm.DB) *Service {
	t.Helper()
	crypter := migRuntimeCrypter(t)
	factory, err := NewFactory(crypter)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(db, factory, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// engineWriteInput builds a valid full-replacement payload (with engine/mode specified by the caller).
func engineWriteInput(name, engine, mode string) *WriteInput {
	password := "itest-secret"
	return &WriteInput{
		Name: name, Engine: engine, CompatibilityMode: mode,
		DeploymentKind: "native", Host: "127.0.0.1", Port: 3306,
		VersionConstraint: "8.0", Enabled: true,
		Credentials: []CredentialInput{{
			Purpose:  "review",
			Username: "u",
			Password: &struct {
				Value string `json:"value"`
			}{Value: password},
		}},
	}
}

// seedDS creates a datasource and returns its ID.
func seedDS(t *testing.T, db *gorm.DB, engine, mode string) string {
	t.Helper()
	id := uuid.NewString()
	if err := db.Exec(`
		insert into datasources (id, name, engine, compatibility_mode, host, port,
			version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, ?, ?, ?, '127.0.0.1', 3306, '8.0', true, ?, 1, ?, now(), now())`,
		id, "ec-ds-"+uuid.NewString()[:8], engine, mode,
		"2222222222222222222222222222222222222222222222222222222222222222",
		uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// seedFlowWithStages creates a flow and attaches stages (one datasource per stage).
// Note the flow_rule_type constraint: change_review requires a non-null rule_set_id,
// whereas query_access requires rule_set_id to be null.
func seedFlowWithStages(t *testing.T, db *gorm.DB, flowType string, dsIDs []string) string {
	t.Helper()
	var ruleSetID *string
	if flowType == "change_review" {
		id := uuid.NewString()
		if err := db.Exec(`
			insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
			values (?, ?, true, '{}'::jsonb, ?, 1, ?, now(), now())`,
			id, "ec-rs-"+uuid.NewString()[:8],
			"1111111111111111111111111111111111111111111111111111111111111111",
			uuid.NewString()).Error; err != nil {
			t.Fatal(err)
		}
		ruleSetID = &id
	}
	flowID := uuid.NewString()
	if err := db.Exec(`
		insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, ?, ?, 'enabled', ?, 1, ?, now(), now())`,
		flowID, "ec-flow-"+uuid.NewString()[:8], flowType, ruleSetID,
		uuid.NewString()).Error; err != nil {
		t.Fatal(err)
	}
	for i, dsID := range dsIDs {
		if err := db.Exec(`
			insert into flow_stages (id, flow_id, position, datasource_id,
				schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
			values (?, ?, ?, ?, ?, ?, 'k1', 'aes-256-gcm')`,
			uuid.NewString(), flowID, i+1, dsID, []byte{1}, []byte{2}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return flowID
}

// TestEngineChangeConflictsFlow verifies all four cases of the predicate function.
//
// Note: this function is called only when Replace detects that engine/compatibility_mode actually changed,
// so "changing to the same value as current" is not a scenario it needs to handle (that call never happens).
// Each case uses an isolated flow to avoid cross-test interference.
func TestEngineChangeConflictsFlow(t *testing.T) {
	db := openEngineTestDB(t)
	ctx := context.Background()

	// Case 1: the other stage of a multi-stage flow is postgresql; changing the mysql datasource
	// to postgresql matches the other stage -> no conflict (change allowed).
	dsMySQL := seedDS(t, db, "mysql", "mysql")
	dsPG := seedDS(t, db, "postgresql", "postgresql")
	seedFlowWithStages(t, db, "change_review", []string{dsMySQL, dsPG})
	conflict, err := engineChangeConflictsFlow(ctx, db, dsMySQL, "postgresql", "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("changing to the sibling stage's engine must not conflict")
	}

	// Case 2: the other stage of a multi-stage flow is mysql; changing this datasource to postgresql
	// disagrees with the other stage in the same flow -> conflict (change must be rejected).
	dsA := seedDS(t, db, "mysql", "mysql")
	dsB := seedDS(t, db, "mysql", "mysql")
	seedFlowWithStages(t, db, "change_review", []string{dsA, dsB})
	conflict, err = engineChangeConflictsFlow(ctx, db, dsA, "postgresql", "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("changing to a different engine than sibling stage must conflict")
	}

	// Case 3: single-stage flow -> never conflicts regardless of target engine (no cross-engine issue).
	dsSingle := seedDS(t, db, "mysql", "mysql")
	seedFlowWithStages(t, db, "change_review", []string{dsSingle})
	conflict, err = engineChangeConflictsFlow(ctx, db, dsSingle, "postgresql", "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("single-stage flow must never conflict")
	}

	// Case 4: query_access flow -> not governed by change_review single-engine rules.
	dsQuery := seedDS(t, db, "mysql", "mysql")
	dsQueryOther := seedDS(t, db, "mysql", "mysql")
	seedFlowWithStages(t, db, "query_access", []string{dsQuery, dsQueryOther})
	conflict, err = engineChangeConflictsFlow(ctx, db, dsQuery, "postgresql", "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("query_access flow must not be constrained by change_review rule")
	}
}

// TestEngineChangeConflictsFlowUnreferenced verifies that changing the engine of a datasource
// not referenced by any flow is unrestricted.
func TestEngineChangeConflictsFlowUnreferenced(t *testing.T) {
	db := openEngineTestDB(t)
	ctx := context.Background()
	ds := seedDS(t, db, "mysql", "mysql")
	conflict, err := engineChangeConflictsFlow(ctx, db, ds, "postgresql", "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("unreferenced datasource must not conflict")
	}
}

// TestReplaceRejectsEngineChangeConflictingFlow exercises the full Service.Replace path to assert
// that the guard takes effect (review C1): in addition to the predicate returning true, verify that:
//  1. Replace returns 1001 (bizerr.ErrValidationFailed);
//  2. The change is rolled back — engine/compatibility_mode/version remain unchanged.
//
// If the call site in Replace is removed or its condition is inverted, this test will fail.
func TestReplaceRejectsEngineChangeConflictingFlow(t *testing.T) {
	db := openEngineTestDB(t)
	ctx := context.Background()
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	svc := newEngineTestService(t, db)
	actor := ActorRef{UserID: uuid.NewString(), Username: "itest-admin"}

	// Two-stage change_review flow: two mysql datasources.
	dsA := seedDS(t, db, "mysql", "mysql")
	dsB := seedDS(t, db, "mysql", "mysql")
	seedFlowWithStages(t, db, "change_review", []string{dsA, dsB})

	var before DatasourceModel
	if err := db.Where("id = ?", dsA).First(&before).Error; err != nil {
		t.Fatal(err)
	}

	// Change dsA to postgresql -> mixes engines with dsB (mysql) -> must be rejected.
	_, err := svc.Replace(ctx, actor, dsA, before.Version,
		engineWriteInput("ec-rename", "postgresql", "postgresql"), auditMeta{})
	if err == nil {
		t.Fatal("Replace must reject engine change that would create a mixed-engine flow")
	}
	if !errors.Is(err, bizerr.ErrValidationFailed) {
		t.Fatalf("want 1001 VALIDATION_FAILED, got %v", err)
	}

	// The change must roll back completely: engine/mode/version all remain unchanged.
	var after DatasourceModel
	if err := db.Where("id = ?", dsA).First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Engine != "mysql" || after.CompatibilityMode != "mysql" {
		t.Fatalf("engine change must roll back, got engine=%s mode=%s",
			after.Engine, after.CompatibilityMode)
	}
	if after.Version != before.Version {
		t.Fatalf("version must not bump on rejected replace: before=%d after=%d",
			before.Version, after.Version)
	}
	if after.Name != before.Name {
		t.Fatalf("name must not change on rejected replace: before=%q after=%q",
			before.Name, after.Name)
	}
}

// TestReplaceAllowsEngineChangeConsistentFlow verifies that compatible engine changes still succeed
// (confirming the guard condition was not inverted into rejecting every engine change).
func TestReplaceAllowsEngineChangeConsistentFlow(t *testing.T) {
	db := openEngineTestDB(t)
	ctx := context.Background()
	restore := addrguard.SetLoopbackBypassForTest(true)
	t.Cleanup(restore)

	svc := newEngineTestService(t, db)
	actor := ActorRef{UserID: uuid.NewString(), Username: "itest-admin"}

	// Single-stage change_review flow: no mixed-engine issue exists, so changing the engine must succeed.
	ds := seedDS(t, db, "mysql", "mysql")
	seedFlowWithStages(t, db, "change_review", []string{ds})

	var before DatasourceModel
	if err := db.Where("id = ?", ds).First(&before).Error; err != nil {
		t.Fatal(err)
	}
	view, err := svc.Replace(ctx, actor, ds, before.Version,
		engineWriteInput("ec-single", "postgresql", "postgresql"), auditMeta{})
	if err != nil {
		t.Fatalf("single-stage flow must allow engine change: %v", err)
	}
	if view.Engine != "postgresql" {
		t.Fatalf("want engine postgresql, got %s", view.Engine)
	}
}
