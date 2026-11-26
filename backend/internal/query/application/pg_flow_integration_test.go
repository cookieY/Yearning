//go:build integration

package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
)

func openQueryPGIntegrationDB(t *testing.T, primaryEnv string) *gorm.DB {
	t.Helper()
	return testkit.OpenPG(t, testenv.WithExtraDSNEnv(primaryEnv), testenv.WithDefaultPort(5432))
}

func TestIdentityBootstrapAndFallback_PGFlow(t *testing.T) {
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	db := openQueryPGIntegrationDB(t, "YEARNING_TEST_POSTGRES_DSN")

	for _, tbl := range []string{
		"query_access_decisions", "query_access_approval_actors", "query_access_approval_steps",
		"query_access_request_datasources", "query_access_requests",
		"query_session_capabilities", "query_sessions", "query_grant_capabilities", "query_grants",
		"idempotency_records", "domain_events", "outbox_events", "audit_events",
		"flow_query_capabilities", "flow_approval_step_actors", "flow_approval_steps", "flows",
		"permission_group_flow_grants", "permission_group_members", "permission_groups",
		"datasource_tls_materials", "datasource_credentials", "datasources", "system_settings", "users",
	} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	crypter := newTestCrypterForQueryBootstrap(t)
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	requesterID := uuid.NewString()
	reviewerID := uuid.NewString()
	ungrantedID := uuid.NewString()
	for _, u := range []struct{ id, name string }{
		{requesterID, "req_user"},
		{reviewerID, "rev_user"},
		{ungrantedID, "other_user"},
	} {
		if err := db.Exec(`insert into users (id, username, display_name, is_builtin_admin, version, created_at, updated_at)
			values (?, ?, ?, false, 1, now(), now())`, u.id, u.name, u.name).Error; err != nil {
			t.Fatal(err)
		}
	}

	sealCred := func(dsID, purpose string) {
		credID := uuid.NewString()
		raw, _ := json.Marshal(map[string]string{"username": "ro", "password": "secret"})
		env, serr := crypter.Seal(crypto.PurposePayload, raw, crypto.AAD{
			DeploymentID: crypter.DeploymentID(), Table: "datasource_credentials",
			PrimaryKey: credID, Column: "credential", Purpose: "datasource_credential",
		})
		if serr != nil {
			t.Fatal(serr)
		}
		if err := db.Exec(`insert into datasource_credentials (id, datasource_id, purpose, credential_ciphertext,
			credential_nonce, credential_key_id, credential_algorithm, enabled, created_at, updated_at)
			values (?, ?, ?, ?, ?, ?, 'aes-256-gcm', true, now(), now())`,
			credID, dsID, purpose, env.Ciphertext, env.Nonce, env.KeyID).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Create 3 datasources with identity_fingerprint = NULL:
	// dsReachable (review credential fallback via test_purpose), dsUnreachable, dsDisabled.
	dsReachable := uuid.NewString()
	dsUnreachable := uuid.NewString()
	dsDisabled := uuid.NewString()
	dsReal := uuid.NewString()
	for _, d := range []struct {
		id      string
		name    string
		enabled bool
		purpose string
	}{
		{dsReachable, "ds-reachable", true, "review"},
		{dsUnreachable, "ds-unreachable", true, "query"},
		{dsDisabled, "ds-disabled", false, "query"},
	} {
		if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
			host, port, database_name, version_constraint, enabled, test_purpose, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
			values (?, ?, 'postgresql', 'postgresql', 'native', 'db.internal', 5432, 'app', '17.x', ?, ?, null, 1, ?, now(), now())`,
			d.id, d.name, d.enabled, d.purpose, requesterID).Error; err != nil {
			t.Fatal(err)
		}
		sealCred(d.id, d.purpose)
	}

	// Flow + approval step + capabilities + grant to requesterID (not ungrantedID).
	flowID := uuid.NewString()
	if err := db.Exec(`insert into flows (id, name, flow_type, status, version, created_by_user_id, created_at, updated_at)
		values (?, 'q-flow', 'query_access', 'enabled', 1, ?, now(), now())`, flowID, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	stepID := uuid.NewString()
	if err := db.Exec(`insert into flow_approval_steps (id, flow_id, position) values (?, ?, 1)`, stepID, flowID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, stepID, reviewerID).Error; err != nil {
		t.Fatal(err)
	}
	for _, dsID := range []string{dsReachable, dsUnreachable, dsDisabled} {
		if err := db.Exec(`insert into flow_query_capabilities (flow_id, datasource_id, can_query, can_export)
			values (?, ?, true, true)`, flowID, dsID).Error; err != nil {
			t.Fatal(err)
		}
	}
	pgID := uuid.NewString()
	if err := db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'pg-1', true, 1, ?, now(), now())`, pgID, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id)
		values (?, ?)`, pgID, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id)
		values (?, ?)`, pgID, flowID).Error; err != nil {
		t.Fatal(err)
	}

	probeCalls := 0
	svc.IdentityProbe = func(_ context.Context, ds *datasource.DatasourceModel, _ database.Credential, _ *database.TLSMaterial) (*database.ServerIdentity, error) {
		probeCalls++
		if ds.ID == dsUnreachable {
			return nil, errors.New("dial tcp timeout")
		}
		return &database.ServerIdentity{
			EngineFamily:   "postgresql",
			ServerVersion:  "17.2",
			InstanceMarker: "pg:inst-" + ds.ID,
		}, nil
	}

	// Guard: invalid UUIDs or unauthorized user must trigger 0 probes.
	svc.ensureDatasourcesIdentity(ctx, requesterID, "not-a-uuid", nil)
	svc.ensureDatasourcesIdentity(ctx, requesterID, flowID, []string{"not-a-uuid"})
	svc.ensureDatasourcesIdentity(ctx, ungrantedID, flowID, []string{dsReachable})
	if probeCalls != 0 {
		t.Fatalf("unauthorized / invalid inputs triggered %d probes, want 0", probeCalls)
	}

	// Unreachable datasource -> 3010 DATASOURCE_UNAVAILABLE and zero orphaned idempotency rows.
	actor := Actor{UserID: requesterID, Username: "req_user"}
	_, _, err = svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          actor,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsUnreachable},
		RequestedUntil: time.Now().Add(24 * time.Hour),
		Reason:         "test unreachable",
		IdempotencyKey: "idem-unreach-1",
	})
	var derr *domain.Error
	if !errors.As(err, &derr) || derr.Code != 3010 {
		t.Fatalf("expected 3010 on unreachable datasource, got %v", err)
	}

	// Disabled datasource -> 3010 DATASOURCE_UNAVAILABLE.
	_, _, err = svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          actor,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsDisabled},
		RequestedUntil: time.Now().Add(24 * time.Hour),
		Reason:         "test disabled",
		IdempotencyKey: "idem-disabled-1",
	})
	if !errors.As(err, &derr) || derr.Code != 3010 {
		t.Fatalf("expected 3010 on disabled datasource, got %v", err)
	}

	// Corrupt TLS row on dsReachable must fail closed (3010) rather than downgrading to plaintext.
	badKeyID := "pay-1"
	badAlg := "aes-256-gcm"
	if err := db.Exec(`insert into datasource_tls_materials (id, datasource_id, purpose, verify_server,
		material_ciphertext, material_nonce, material_key_id, material_algorithm, created_at, updated_at)
		values (?, ?, 'review', true, ?, ?, ?, ?, now(), now())`,
		uuid.NewString(), dsReachable, []byte("corrupt"), []byte("123456789012"), badKeyID, badAlg).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          actor,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsReachable},
		RequestedUntil: time.Now().Add(24 * time.Hour),
		Reason:         "corrupt tls fail closed",
		IdempotencyKey: "idem-bad-tls-1",
	})
	if !errors.As(err, &derr) || derr.Code != 3010 {
		t.Fatalf("expected 3010 on corrupt TLS material, got %v", err)
	}
	if err := db.Exec(`delete from datasource_tls_materials where datasource_id = ?`, dsReachable).Error; err != nil {
		t.Fatal(err)
	}

	// Valid reachable datasource -> bootstraps fingerprint via review fallback credential and creates request.
	view, _, err := svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          actor,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsReachable},
		RequestedUntil: time.Now().Add(24 * time.Hour),
		Reason:         "bootstrap ok",
		IdempotencyKey: "idem-ok-1",
	})
	if err != nil || view == nil || view.State != "access_pending" {
		t.Fatalf("CreateAccessRequest after bootstrap failed: view=%+v err=%v", view, err)
	}

	// Approval-free CreateSession (with IdempotencyKey) on unprobed datasource + non-granted datasource + unreachable datasource.
	if err := db.Exec(`insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('query', 1, '{"approval_enabled":false,"default_timeout_ms":30000}'::jsonb, 1, ?, now(), now())
		on conflict (namespace) do update set settings = excluded.settings, updated_at = now()`, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from system_settings where namespace = 'query'`).Error
	})
	if err := db.Exec(`update datasources set identity_fingerprint = null where id = ?`, dsReachable).Error; err != nil {
		t.Fatal(err)
	}
	sessView, _, err := svc.CreateSession(ctx, CreateSessionInput{
		Actor:          actor,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsReachable},
		IdempotencyKey: "idem-sess-ok-1",
	})
	if err != nil || sessView == nil || sessView.State != "active" {
		t.Fatalf("approval-free CreateSession bootstrap failed: view=%+v err=%v", sessView, err)
	}
	_, _, err = svc.CreateSession(ctx, CreateSessionInput{
		Actor:          actor,
		FlowID:         flowID,
		DatasourceIDs:  []string{uuid.NewString()},
		IdempotencyKey: "idem-sess-not-granted",
	})
	if !errors.As(err, &derr) || derr.Code != domain.CodeDsNotGranted {
		t.Fatalf("expected 4002 on datasource outside flow, got %v", err)
	}
	_, _, err = svc.CreateSession(ctx, CreateSessionInput{
		Actor:          actor,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsUnreachable},
		IdempotencyKey: "idem-sess-unreach",
	})
	if !errors.As(err, &derr) || derr.Code != 3010 {
		t.Fatalf("expected 3010 on approval-free unreachable datasource, got %v", err)
	}

	// Empty-IdempotencyKey CreateSession branch also bootstraps unprobed datasource cleanly.
	if err := db.Exec(`update datasources set identity_fingerprint = null where id = ?`, dsReachable).Error; err != nil {
		t.Fatal(err)
	}
	noKeySess, _, err := svc.CreateSession(ctx, CreateSessionInput{
		Actor:         actor,
		FlowID:        flowID,
		DatasourceIDs: []string{dsReachable},
	})
	if err != nil || noKeySess == nil || noKeySess.State != "active" {
		t.Fatalf("empty-key CreateSession bootstrap failed: view=%+v err=%v", noKeySess, err)
	}

	// Rollback idempotency claim release across Decide, Withdraw, Revoke, Relinquish, and CloseSession.
	missingID := uuid.NewString()
	_, _, _ = svc.Decide(ctx, DecideInput{Actor: actor, RequestID: missingID, Decision: "approved", ExpectVersion: 1, IdempotencyKey: "idem-dec-miss"})
	_, _, _ = svc.Withdraw(ctx, WithdrawInput{Actor: actor, RequestID: missingID, Reason: "cancel", ExpectVersion: 1, IdempotencyKey: "idem-wd-miss"})
	_, _, _ = svc.Revoke(ctx, RevokeInput{Actor: Actor{UserID: requesterID, Username: "req_user", IsBuiltinAdmin: true}, GrantID: missingID, Reason: "rev", ExpectVersion: 1, IdempotencyKey: "idem-rev-miss"})
	_, _, _ = svc.Relinquish(ctx, RelinquishInput{Actor: actor, GrantID: missingID, Reason: "rel", ExpectVersion: 1, IdempotencyKey: "idem-rel-miss"})
	_, _, _ = svc.CloseSession(ctx, CloseSessionInput{Actor: actor, SessionID: missingID, Reason: "cls", IdempotencyKey: "idem-cls-miss"})
	var stuckCount int64
	if err := db.Raw(`select count(1) from idempotency_records where state = 'in_progress'`).Scan(&stuckCount).Error; err != nil {
		t.Fatal(err)
	}
	if stuckCount != 0 {
		t.Fatalf("expected 0 stuck in_progress idempotency claims, got %d", stuckCount)
	}

	// Also exercise production adapter dial branch when target PG is configured.
	targetPG, targetOK := testenv.ResolveTargetPG(
		testenv.WithDefaultUser("yr"),
		testenv.WithDefaultDBName("postgres"),
	)
	if !targetOK && testenv.RequireIntegration() {
		targetPG = testenv.RequireTargetPG(t)
		targetOK = true
	}
	if targetOK {
		host := targetPG.Host
		port := targetPG.Port
		user := targetPG.User
		pass := targetPG.Password
		dbName := targetPG.DBName
		if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
				host, port, database_name, version_constraint, enabled, test_purpose, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
				values (?, 'ds-live-pg', 'postgresql', 'postgresql', 'native', ?, ?, ?, '17.x', true, 'query', null, 1, ?, now(), now())`,
			dsReal, host, port, dbName, requesterID).Error; err != nil {
			t.Fatal(err)
		}
		credID := uuid.NewString()
		raw, _ := json.Marshal(map[string]string{"username": user, "password": pass})
		env, serr := crypter.Seal(crypto.PurposePayload, raw, crypto.AAD{
			DeploymentID: crypter.DeploymentID(), Table: "datasource_credentials",
			PrimaryKey: credID, Column: "credential", Purpose: "datasource_credential",
		})
		if serr != nil {
			t.Fatal(serr)
		}
		if err := db.Exec(`insert into datasource_credentials (id, datasource_id, purpose, credential_ciphertext,
				credential_nonce, credential_key_id, credential_algorithm, enabled, created_at, updated_at)
				values (?, ?, 'query', ?, ?, ?, 'aes-256-gcm', true, now(), now())`,
			credID, dsReal, env.Ciphertext, env.Nonce, env.KeyID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`insert into flow_query_capabilities (flow_id, datasource_id, can_query, can_export)
				values (?, ?, true, false)`, flowID, dsReal).Error; err != nil {
			t.Fatal(err)
		}
		svc.IdentityProbe = nil
		svc.ensureDatasourcesIdentity(ctx, requesterID, flowID, []string{dsReal})
		var liveFP string
		if err := db.Raw(`select coalesce(identity_fingerprint, '') from datasources where id = ?`, dsReal).Scan(&liveFP).Error; err != nil {
			t.Fatal(err)
		}
		if !isValidIdentityFingerprint(liveFP) {
			t.Fatalf("expected live PG adapter bootstrap to persist 64-hex fingerprint, got %q", liveFP)
		}
	}

	// Verify loadReadOnlyQueryCredential falls back from query to review and never uses execution.
	if cred, resolved, cerr := svc.loadReadOnlyQueryCredential(ctx, dsReachable); cerr != nil || cred == nil || resolved != "review" {
		t.Fatalf("expected review fallback for query credential, got cred=%v resolved=%q err=%v", cred, resolved, cerr)
	}
	dsExecOnly := uuid.NewString()
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, test_purpose, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'ds-exec-only', 'postgresql', 'postgresql', 'native', 'db.internal', 5432, 'app', '17.x', true, 'execution', null, 1, ?, now(), now())`,
		dsExecOnly, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	sealCred(dsExecOnly, "execution")
	if _, _, cerr := svc.loadReadOnlyQueryCredential(ctx, dsExecOnly); !errors.Is(cerr, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound for execution-only datasource, got %v", cerr)
	}
}

func TestMetadataCacheNeverBypassesSessionOrAuthGuards(t *testing.T) {
	db := openQueryPGIntegrationDB(t, "YEARNING_TEST_DATABASE_DSN")

	crypter := newTestCrypterForQueryBootstrap(t)
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ownerID := uuid.NewString()
	otherID := uuid.NewString()
	flowID := uuid.NewString()
	dsID := uuid.NewString()
	pgID := uuid.NewString()
	grantID := uuid.NewString()
	reqID := uuid.NewString()
	sessionID := uuid.NewString()
	ownerName := "mc_owner_" + ownerID[:8]
	flowName := "mc-flow-" + flowID[:8]
	fp := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for _, u := range []struct{ id, name string }{
		{ownerID, ownerName},
		{otherID, "mc_other_" + otherID[:8]},
	} {
		if err := db.Exec(`insert into users (id, username, display_name, is_builtin_admin, version, created_at, updated_at)
			values (?, ?, ?, false, 1, now(), now())`, u.id, u.name, u.name).Error; err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_ = db.Exec(`delete from query_session_capabilities where session_id = ?`, sessionID).Error
		_ = db.Exec(`delete from query_sessions where id = ?`, sessionID).Error
		_ = db.Exec(`delete from query_grants where id = ?`, grantID).Error
		_ = db.Exec(`delete from permission_group_flow_grants where permission_group_id = ?`, pgID).Error
		_ = db.Exec(`delete from permission_group_members where permission_group_id = ?`, pgID).Error
		_ = db.Exec(`delete from permission_groups where id = ?`, pgID).Error
		_ = db.Exec(`delete from flow_query_capabilities where flow_id = ?`, flowID).Error
		_ = db.Exec(`delete from flows where id = ?`, flowID).Error
		_ = db.Exec(`delete from datasources where id = ?`, dsID).Error
		_ = db.Exec(`delete from users where id in (?, ?)`, ownerID, otherID).Error
	}()

	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, test_purpose, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'postgresql', 'postgresql', 'native', 'db.internal', 5432, 'app', '17.x', true, 'query', ?, 1, ?, now(), now())`,
		dsID, "mc-ds-"+dsID[:8], fp, ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into flows (id, name, flow_type, status, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'query_access', 'enabled', 1, ?, now(), now())`, flowID, flowName, ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into flow_query_capabilities (flow_id, datasource_id, can_query, can_export)
		values (?, ?, true, false)`, flowID, dsID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, 1, ?, now(), now())`, pgID, "mc-pg-"+pgID[:8], ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`, pgID, ownerID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`, pgID, flowID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into query_grants (id, query_access_request_id, requester_user_id_snapshot, requester_username_snapshot, state, expires_at, version, created_at)
		values (?, ?, ?, ?, 'active', now() + interval '1 hour', 1, now())`,
		grantID, reqID, ownerID, ownerName).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into query_sessions (id, user_id_snapshot, username_snapshot, flow_id_snapshot, flow_name_snapshot, grant_id, approval_required_snapshot, state, version, created_at, last_seen_at)
		values (?, ?, ?, ?, ?, ?, true, 'active', 1, now(), now())`,
		sessionID, ownerID, ownerName, flowID, flowName, grantID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into query_session_capabilities (session_id, datasource_id, datasource_name_snapshot, datasource_identity_fingerprint, state, can_query, can_export)
		values (?, ?, 'mc-ds', ?, 'active', true, false)`, sessionID, dsID, fp).Error; err != nil {
		t.Fatal(err)
	}

	adapter := &fakeMetadataAdapter{
		schemas: []string{"app"},
		tables:  map[string][]string{"app": {"orders"}},
		columns: map[string][]database.MetadataColumn{
			"app.orders": {{SchemaName: "app", TableName: "orders", ColumnName: "id", Ordinal: 1, DatabaseType: "bigint"}},
		},
	}
	svc.PoolFactory = func(_ context.Context, _ *datasource.DatasourceModel, _ int) (database.Adapter, *database.PurposePool, error) {
		return adapter, &database.PurposePool{}, nil
	}

	owner := Actor{UserID: ownerID, Username: "owner"}
	other := Actor{UserID: otherID, Username: "other"}

	// Warm the cache as the rightful owner.
	if _, err := svc.ListSchemas(ctx, owner, sessionID, dsID); err != nil {
		t.Fatalf("owner ListSchemas failed: %v", err)
	}
	if _, err := svc.ListSchemas(ctx, owner, sessionID, dsID); err != nil {
		t.Fatalf("owner cached ListSchemas failed: %v", err)
	}
	if adapter.schemaCalls.Load() != 1 {
		t.Fatalf("expected 1 upstream schema call, got %d", adapter.schemaCalls.Load())
	}

	// Wrong actor is rejected even though cache is warm.
	var derr *domain.Error
	if _, err := svc.ListSchemas(ctx, other, sessionID, dsID); !errors.As(err, &derr) || derr.Code != domain.CodeNotFound {
		t.Fatalf("expected CodeNotFound for non-owner actor, got %v", err)
	}

	// Disabling the datasource blocks metadata reads even while cached.
	if err := db.Exec(`update datasources set enabled = false where id = ?`, dsID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListSchemas(ctx, owner, sessionID, dsID); err == nil {
		t.Fatal("expected disabled datasource to reject ListSchemas despite warm cache")
	}
	if err := db.Exec(`update datasources set enabled = true where id = ?`, dsID).Error; err != nil {
		t.Fatal(err)
	}

	// Closing the session blocks metadata reads even while cached.
	if err := db.Exec(`update query_sessions set state = 'revoked', closed_at = now(), close_reason = 'revoked' where id = ?`, sessionID).Error; err != nil {
		t.Fatal(err)
	}
	derr = nil
	if _, err := svc.ListSchemas(ctx, owner, sessionID, dsID); !errors.As(err, &derr) || derr.Code != domain.CodeGrantRevoked {
		t.Fatalf("expected CodeGrantRevoked for revoked session despite warm cache, got %v", err)
	}
}
