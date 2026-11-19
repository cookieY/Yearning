package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func TestDatasourceIdentityExposesOnlyMatchedSafeFacts(t *testing.T) {
	target := frozenDatasourceTarget{
		SchemaVersion:        stageDatasourceTargetVersion,
		DatasourceID:         "datasource-secret-id",
		Engine:               "postgresql",
		CompatibilityMode:    "postgresql",
		Host:                 "db.private.internal",
		Port:                 5432,
		DatabaseName:         "private_database",
		ConfiguredIdentityFP: "configured-fingerprint",
		EngineFamily:         "postgresql",
		ServerVersion:        "17.2",
		InstanceMarker:       "pg:private-system-identifier",
		SystemIdentifier:     "private-system-identifier",
		DatabaseOID:          42,
		ConfiguredEndpointID: "db.private.internal:5432",
	}
	fact, err := (&factSource{target: target}).DatasourceIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fact["engine"] != "postgresql" ||
		fact["server_version"] != "17.2" ||
		fact["instance_identity_status"] != "matched" ||
		fact["database_identity_status"] != "matched" ||
		fact["metadata_complete"] != true {
		t.Fatalf("safe identity fact = %#v", fact)
	}
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		target.DatasourceID,
		target.Host,
		target.DatabaseName,
		target.ConfiguredIdentityFP,
		target.InstanceMarker,
		target.SystemIdentifier,
		target.ConfiguredEndpointID,
	} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("datasource identity fact leaked %q: %s", private, raw)
		}
	}
}

func TestEnsureFlowStagesIdentityGuardsAndBootstrap(t *testing.T) {
	// Guard branches on nil / custom DatasourceIdentityProbe / malformed UUIDs must be safe no-ops.
	var nilSvc *Services
	nilSvc.ensureFlowStagesIdentity(context.Background(), uuid.NewString(), uuid.NewString())
	nilSvc.ensureDraftFlowStagesIdentity(context.Background(), uuid.NewString(), uuid.NewString())

	probeSetSvc := &Services{
		DatasourceIdentityProbe: func(_ context.Context, _ *gorm.DB, _ *crypto.Crypter, _ datasourceTargetRow) (database.ServerIdentity, error) {
			return database.ServerIdentity{}, nil
		},
	}
	probeSetSvc.ensureFlowStagesIdentity(context.Background(), uuid.NewString(), uuid.NewString())
	probeSetSvc.ensureDraftFlowStagesIdentity(context.Background(), uuid.NewString(), uuid.NewString())

	dsn := strings.TrimSpace(os.Getenv("YEARNING_TEST_POSTGRES_DSN"))
	rawPort := strings.TrimSpace(os.Getenv("YEARNING_TEST_ADAPTER_PG_PORT"))
	if dsn == "" || rawPort == "" {
		return
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port <= 0 {
		return
	}
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	acquireSANPGTestLock(t)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect failed: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("postgres sql.DB failed: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	for _, tbl := range []string{
		"change_drafts", "flow_stages", "flows",
		"permission_group_flow_grants", "permission_group_members", "permission_groups",
		"datasource_tls_materials", "datasource_credentials", "datasources", "rule_sets", "users",
	} {
		if err := db.Exec("truncate table " + tbl + " cascade").Error; err != nil {
			_ = db.Exec("delete from " + tbl).Error
		}
	}

	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	keyringDoc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"rev-boot","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"id-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(1), makeKey(2), makeKey(3))
	if err := os.WriteFile(keyPath, []byte(keyringDoc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, _ := auditlog.NewRecorder(db)
	svc, err := NewServices(db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	actorID := uuid.NewString()
	otherID := uuid.NewString()
	for _, u := range []struct{ id, name string }{{actorID, "rev_actor"}, {otherID, "rev_other"}} {
		if err := db.Exec(`insert into users (id, username, display_name, is_builtin_admin, version, created_at, updated_at)
			values (?, ?, ?, false, 1, now(), now())`, u.id, u.name, u.name).Error; err != nil {
			t.Fatal(err)
		}
	}

	host := os.Getenv("YEARNING_TEST_ADAPTER_PG_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	user := os.Getenv("YEARNING_TEST_ADAPTER_PG_USER")
	if user == "" {
		user = "yr"
	}
	pass := os.Getenv("YEARNING_TEST_ADAPTER_PG_PASSWORD")

	dsID := uuid.NewString()
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, test_purpose, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'rev-live-pg', 'postgresql', 'postgresql', 'native', ?, ?, 'postgres', '17.x', true, 'review', null, 1, ?, now(), now())`,
		dsID, host, port, actorID).Error; err != nil {
		t.Fatal(err)
	}
	credID := uuid.NewString()
	credBytes, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	credEnv, err := crypter.Seal(crypto.PurposePayload, credBytes, crypto.AAD{
		DeploymentID: crypter.DeploymentID(), Table: "datasource_credentials",
		PrimaryKey: credID, Column: "credential", Purpose: "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into datasource_credentials (id, datasource_id, purpose, credential_ciphertext,
		credential_nonce, credential_key_id, credential_algorithm, enabled, created_at, updated_at)
		values (?, ?, 'review', ?, ?, ?, 'aes-256-gcm', true, now(), now())`,
		credID, dsID, credEnv.Ciphertext, credEnv.Nonce, credEnv.KeyID).Error; err != nil {
		t.Fatal(err)
	}

	ruleView, err := svc.CreateRuleSet(ctx, actorID, "rev-boot-rs-"+uuid.NewString()[:8], true, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	flowID := uuid.NewString()
	if err := db.Exec(`insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, 'rev-flow', 'change_review', 'enabled', ?, 1, ?, now(), now())`,
		flowID, ruleView.ID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	stageID := uuid.NewString()
	smEnv, err := crypter.Seal(crypto.PurposePayload, []byte(`[]`), crypto.AAD{
		DeploymentID: crypter.DeploymentID(), Table: "flow_stages",
		PrimaryKey: stageID, Column: "schema_mapping", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into flow_stages (id, flow_id, position, datasource_id,
		schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
		values (?, ?, 1, ?, ?, ?, ?, 'aes-256-gcm')`,
		stageID, flowID, dsID, smEnv.Ciphertext, smEnv.Nonce, smEnv.KeyID).Error; err != nil {
		t.Fatal(err)
	}
	pgID := uuid.NewString()
	if err := db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'rev-pg', true, 1, ?, now(), now())`, pgID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id)
		values (?, ?)`, pgID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id)
		values (?, ?)`, pgID, flowID).Error; err != nil {
		t.Fatal(err)
	}

	// Non-UUID and unauthorized user must leave identity_fingerprint NULL.
	svc.ensureFlowStagesIdentity(ctx, "not-a-uuid", flowID)
	svc.ensureFlowStagesIdentity(ctx, actorID, "not-a-uuid")
	svc.ensureFlowStagesIdentity(ctx, otherID, flowID)
	var beforeFP *string
	if err := db.Raw(`select identity_fingerprint from datasources where id = ?`, dsID).Scan(&beforeFP).Error; err != nil {
		t.Fatal(err)
	}
	if beforeFP != nil {
		t.Fatalf("expected NULL fingerprint for unauthorized user, got %q", *beforeFP)
	}

	// Authorized user via ensureFlowStagesIdentity populates 64-hex fingerprint.
	svc.ensureFlowStagesIdentity(ctx, actorID, flowID)
	var afterFP string
	if err := db.Raw(`select coalesce(identity_fingerprint, '') from datasources where id = ?`, dsID).Scan(&afterFP).Error; err != nil {
		t.Fatal(err)
	}
	if len(afterFP) != 64 {
		t.Fatalf("expected 64-hex fingerprint after ensureFlowStagesIdentity, got %q", afterFP)
	}

	// Also verify ensureDraftFlowStagesIdentity resets from NULL -> 64-hex.
	if err := db.Exec(`update datasources set identity_fingerprint = null where id = ?`, dsID).Error; err != nil {
		t.Fatal(err)
	}
	draftID := uuid.NewString()
	if err := db.Exec(`insert into change_drafts (id, owner_user_id, flow_id, flow_version_seen,
		title, description, state, revision, version, created_at, updated_at)
		values (?, ?, ?, 1, 'boot-draft', '', 'draft', 1, 1, now(), now())`,
		draftID, actorID, flowID).Error; err != nil {
		t.Fatal(err)
	}
	svc.ensureDraftFlowStagesIdentity(ctx, "not-a-uuid", draftID)
	svc.ensureDraftFlowStagesIdentity(ctx, actorID, "not-a-uuid")
	svc.ensureDraftFlowStagesIdentity(ctx, otherID, draftID)
	svc.ensureDraftFlowStagesIdentity(ctx, actorID, draftID)
	var draftFP string
	if err := db.Raw(`select coalesce(identity_fingerprint, '') from datasources where id = ?`, dsID).Scan(&draftFP).Error; err != nil {
		t.Fatal(err)
	}
	if draftFP != afterFP {
		t.Fatalf("expected ensureDraftFlowStagesIdentity to restore %q, got %q", afterFP, draftFP)
	}

	// Also verify StartRun invokes ensureDraftFlowStagesIdentity before freezing policy hash.
	if err := db.Exec(`update datasources set identity_fingerprint = null where id = ?`, dsID).Error; err != nil {
		t.Fatal(err)
	}
	_, _ = svc.StartRun(ctx, Actor{UserID: actorID, Username: "rev_actor"}, StartRunInput{
		DraftID:        draftID,
		ExpectVersion:  1,
		IdempotencyKey: "idem-startrun-boot-1",
	})
	var startRunFP string
	if err := db.Raw(`select coalesce(identity_fingerprint, '') from datasources where id = ?`, dsID).Scan(&startRunFP).Error; err != nil {
		t.Fatal(err)
	}
	if startRunFP != afterFP {
		t.Fatalf("expected StartRun to bootstrap stage identity %q, got %q", afterFP, startRunFP)
	}
}
