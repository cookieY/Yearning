//go:build integration

package application

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

func openQueryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testkit.OpenPG(t, testenv.WithDefaultPort(54332), testenv.WithDefaultDBName("yearning_b13_test"))
	sqlDB, err := db.DB()
	if err != nil || sqlDB.Ping() != nil {
		testenv.SkipOrFatal(t, "PostgreSQL test DB ping failed")
	}
	return db
}

func TestQueryApplicationServicesIntegration(t *testing.T) {
	db := openQueryTestDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	mk := func(seed byte) string {
		out := make([]byte, 32)
		for i := range out {
			out[i] = seed
		}
		return base64.StdEncoding.EncodeToString(out)
	}
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"query-cov-test","keys":[
	{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
	{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		mk(1), mk(4), mk(7))
	krPath := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(krPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(krPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(db, crypter, recorder)
	if err != nil {
		t.Fatalf("NewServices: %v", err)
	}
	svc.SetCursorIdleForTest(5 * time.Minute)
	svc.SetClockForTest(func() time.Time { return time.Now().UTC() })

	// Cryptographic & settings helpers
	if _, err := svc.AffinityHMAC([]byte("cursor-1")); err != nil {
		t.Fatalf("AffinityHMAC: %v", err)
	}
	if _, err := svc.hmacSQL([]byte("SELECT 1")); err != nil {
		t.Fatalf("hmacSQL: %v", err)
	}
	colRef := crypto.ColumnRef{Table: "query_executions", PrimaryKey: "id-1", Column: "query_payload"}
	env, err := svc.Crypter.SealColumn(crypto.PurposePayload, colRef, []byte("SELECT 1"))
	if err != nil {
		t.Fatalf("SealColumn: %v", err)
	}
	plain, err := svc.Crypter.OpenColumn(crypto.PurposePayload, colRef, env)
	if err != nil || string(plain) != "SELECT 1" {
		t.Fatalf("OpenColumn: plain=%q err=%v", string(plain), err)
	}
	if settings := svc.loadQuerySettings(ctx, db); settings.DefaultTimeoutMS <= 0 {
		t.Fatalf("loadQuerySettings expected positive DefaultTimeoutMS, got %+v", settings)
	}

	// ExpireGrants
	if err := svc.ExpireGrants(ctx); err != nil {
		t.Fatalf("ExpireGrants: %v", err)
	}

	// FlowDatasourceGranted returns false for nonexistent IDs
	if granted, err := svc.FlowDatasourceGranted(ctx, uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil || granted {
		t.Fatalf("FlowDatasourceGranted expected (false, nil), got (%v, %v)", granted, err)
	}

	// Masking rules & vocabulary
	nonAdmin := Actor{UserID: uuid.NewString(), Username: "user1", IsBuiltinAdmin: false}
	admin := Actor{UserID: uuid.NewString(), Username: "admin", IsBuiltinAdmin: true}
	flowID := uuid.NewString()
	dsID := uuid.NewString()

	if _, err := svc.ListMaskingRules(ctx, nonAdmin, flowID); err == nil {
		t.Fatal("expected error for non-admin ListMaskingRules")
	}
	if _, err := svc.ListMaskingRules(ctx, admin, flowID); err == nil {
		t.Fatal("expected error for non-existent flow ListMaskingRules")
	}
	if _, err := svc.ReplaceMaskingRule(ctx, nonAdmin, flowID, dsID, []string{"phone"}); err == nil {
		t.Fatal("expected error for non-admin ReplaceMaskingRule")
	}
	if _, err := svc.ReplaceMaskingRule(ctx, admin, flowID, dsID, []string{""}); err == nil {
		t.Fatal("expected error for invalid column in ReplaceMaskingRule")
	}
	if _, err := svc.ReplaceMaskingRule(ctx, admin, flowID, dsID, []string{"phone"}); err == nil {
		t.Fatal("expected error for non-existent flow in ReplaceMaskingRule")
	}

	// Seed a query_access flow and datasource + capability to test ReplaceMaskingRule & ListMaskingRules
	if err := db.Exec(`
		insert into datasources (
			id, name, engine, compatibility_mode, host, port,
			version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at
		) values (?, ?, 'mysql', 'mysql', '127.0.0.1', 3306, '8.0', true, ?, 1, ?, now(), now())`,
		dsID, "qcov-ds-"+dsID[:8], strings.Repeat("a", 64), admin.UserID,
	).Error; err != nil {
		t.Fatalf("insert datasource: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from flow_masking_rules where flow_id = ?`, flowID).Error
		_ = db.Exec(`delete from flow_query_capabilities where flow_id = ?`, flowID).Error
		_ = db.Exec(`delete from flows where id = ?`, flowID).Error
		_ = db.Exec(`delete from datasources where id = ?`, dsID).Error
	})
	if err := db.Exec(`
		insert into flows (id, name, flow_type, status, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'query_access', 'enabled', 1, ?, now(), now())`,
		flowID, "qcov-flow-"+flowID[:8], admin.UserID,
	).Error; err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	if err := db.Exec(`
		insert into flow_query_capabilities (flow_id, datasource_id, can_query, can_export)
		values (?, ?, true, false)`, flowID, dsID,
	).Error; err != nil {
		t.Fatalf("insert flow_query_capabilities: %v", err)
	}

	// Empty vocabulary before rule exists
	vocab, err := (databaseMaskingSource{}).Vocabulary(ctx, db, flowID, dsID)
	if err != nil || len(vocab) != 0 {
		t.Fatalf("Vocabulary empty: vocab=%v err=%v", vocab, err)
	}
	rules, err := svc.ListMaskingRules(ctx, admin, flowID)
	if err != nil || len(rules) != 0 {
		t.Fatalf("ListMaskingRules empty: rules=%v err=%v", rules, err)
	}

	// ReplaceMaskingRule upsert
	replaced, err := svc.ReplaceMaskingRule(ctx, admin, flowID, dsID, []string{"Phone", "email"})
	if err != nil || replaced == nil || len(replaced.SensitiveColumns) != 2 {
		t.Fatalf("ReplaceMaskingRule: replaced=%+v err=%v", replaced, err)
	}
	vocab, err = (databaseMaskingSource{}).Vocabulary(ctx, db, flowID, dsID)
	if err != nil || len(vocab) != 2 {
		t.Fatalf("Vocabulary after replace: vocab=%v err=%v", vocab, err)
	}
	rules, err = svc.ListMaskingRules(ctx, admin, flowID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("ListMaskingRules after replace: rules=%v err=%v", rules, err)
	}

	// CreateAccessRequest validation branches
	now := time.Now().UTC()
	for idx, in := range []CreateAccessRequestInput{
		{Actor: nonAdmin, FlowID: flowID, DatasourceIDs: []string{dsID}, RequestedUntil: now.Add(time.Hour), Reason: "", IdempotencyKey: "k1"},
		{Actor: nonAdmin, FlowID: flowID, DatasourceIDs: nil, RequestedUntil: now.Add(time.Hour), Reason: "ok", IdempotencyKey: "k2"},
		{Actor: nonAdmin, FlowID: flowID, DatasourceIDs: []string{dsID}, RequestedUntil: now.Add(-time.Hour), Reason: "ok", IdempotencyKey: "k3"},
		{Actor: nonAdmin, FlowID: flowID, DatasourceIDs: []string{dsID}, RequestedUntil: now.Add(90000 * time.Hour), Reason: "ok", IdempotencyKey: "k4"},
		{Actor: nonAdmin, FlowID: flowID, DatasourceIDs: []string{dsID}, RequestedUntil: now.Add(time.Hour), Reason: "ok", IdempotencyKey: "k5"},
	} {
		if _, _, err := svc.CreateAccessRequest(ctx, in); err == nil {
			t.Fatalf("CreateAccessRequest validation case %d expected error, got nil", idx)
		}
	}

	// CreateSession validation branches
	if _, _, err := svc.CreateSession(ctx, CreateSessionInput{
		Actor: nonAdmin, FlowID: flowID, GrantID: "", DatasourceIDs: []string{dsID},
	}); err == nil {
		t.Fatal("CreateSession without grant_id expected error")
	}
	if _, _, err := svc.CreateSession(ctx, CreateSessionInput{
		Actor: nonAdmin, FlowID: flowID, GrantID: uuid.NewString(), DatasourceIDs: []string{dsID}, IdempotencyKey: "sess-k1",
	}); err == nil {
		t.Fatal("CreateSession with nonexistent grant_id expected error")
	}

	// Seed users, approval step, and permission group grant to exercise full AccessRequest -> Grant -> Session lifecycle
	approver := Actor{UserID: uuid.NewString(), Username: "qcov-approver-" + flowID[:6], DisplayName: "Approver"}
	outsider := Actor{UserID: uuid.NewString(), Username: "qcov-outsider-" + flowID[:6], DisplayName: "Outsider"}
	nonAdmin.Username = "qcov-req-" + flowID[:6]
	nonAdmin.DisplayName = "Requester"

	for _, u := range []Actor{nonAdmin, approver, outsider} {
		if err := db.Exec(`
			insert into users (id, username, display_name, is_builtin_admin, version, created_at, updated_at)
			values (?, ?, ?, false, 1, now(), now())`,
			u.UserID, u.Username, u.DisplayName,
		).Error; err != nil {
			t.Fatalf("insert user %s: %v", u.Username, err)
		}
	}
	stepID := uuid.NewString()
	pgID := uuid.NewString()
	t.Cleanup(func() {
		_ = db.Exec(`delete from idempotency_records where requesting_user_id in (?, ?, ?)`, nonAdmin.UserID, approver.UserID, outsider.UserID).Error
		_ = db.Exec(`delete from query_session_capabilities where session_id in (select id from query_sessions where user_id_snapshot = ?)`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_sessions where user_id_snapshot = ?`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_grant_capabilities where grant_id in (select id from query_grants where requester_user_id_snapshot = ?)`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_grants where requester_user_id_snapshot = ?`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_access_decisions where request_id in (select id from query_access_requests where requester_user_id_snapshot = ?)`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_access_approval_actors where step_id in (select id from query_access_approval_steps where request_id in (select id from query_access_requests where requester_user_id_snapshot = ?))`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_access_approval_steps where request_id in (select id from query_access_requests where requester_user_id_snapshot = ?)`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_access_request_datasources where request_id in (select id from query_access_requests where requester_user_id_snapshot = ?)`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from query_access_requests where requester_user_id_snapshot = ?`, nonAdmin.UserID).Error
		_ = db.Exec(`delete from permission_group_flow_grants where permission_group_id = ?`, pgID).Error
		_ = db.Exec(`delete from permission_group_members where permission_group_id = ?`, pgID).Error
		_ = db.Exec(`delete from permission_groups where id = ?`, pgID).Error
		_ = db.Exec(`delete from flow_approval_step_actors where step_id = ?`, stepID).Error
		_ = db.Exec(`delete from flow_approval_steps where id = ?`, stepID).Error
		_ = db.Exec(`delete from users where id in (?, ?, ?)`, nonAdmin.UserID, approver.UserID, outsider.UserID).Error
	})

	if err := db.Exec(`insert into flow_approval_steps (id, flow_id, position) values (?, ?, 1)`, stepID, flowID).Error; err != nil {
		t.Fatalf("insert flow_approval_steps: %v", err)
	}
	if err := db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, stepID, approver.UserID).Error; err != nil {
		t.Fatalf("insert flow_approval_step_actors: %v", err)
	}
	if err := db.Exec(`
		insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, ?, true, 1, ?, now(), now())`, pgID, "qcov-pg-"+pgID[:8], admin.UserID).Error; err != nil {
		t.Fatalf("insert permission_groups: %v", err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`, pgID, nonAdmin.UserID).Error; err != nil {
		t.Fatalf("insert permission_group_members: %v", err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`, pgID, flowID).Error; err != nil {
		t.Fatalf("insert permission_group_flow_grants: %v", err)
	}

	// 1. CreateAccessRequest + replay + GetAccessRequest + ListAccessRequests
	reqView, replayed, err := svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          nonAdmin,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsID},
		RequestedUntil: now.Add(2 * time.Hour),
		Reason:         "Investigate production issue",
		IdempotencyKey: "req-ok-1",
	})
	if err != nil || replayed || reqView == nil {
		t.Fatalf("CreateAccessRequest: view=%+v replayed=%v err=%v", reqView, replayed, err)
	}
	_, replayed, err = svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          nonAdmin,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsID},
		RequestedUntil: now.Add(2 * time.Hour),
		Reason:         "Investigate production issue",
		IdempotencyKey: "req-ok-1",
	})
	if err != nil || !replayed {
		t.Fatalf("CreateAccessRequest replay: replayed=%v err=%v", replayed, err)
	}
	if _, err := svc.GetAccessRequest(ctx, nonAdmin, reqView.ID); err != nil {
		t.Fatalf("GetAccessRequest: %v", err)
	}
	if _, err := svc.GetAccessRequest(ctx, outsider, reqView.ID); err == nil {
		t.Fatal("expected error for outsider GetAccessRequest")
	}
	if list, _, _, err := svc.ListAccessRequests(ctx, nonAdmin, 10, "", "", ""); err != nil || len(list) == 0 {
		t.Fatalf("ListAccessRequests: len=%d err=%v", len(list), err)
	} else if list[0].CanDecide {
		t.Fatalf("requester non-approver must have CanDecide=false, got %+v", list[0])
	}
	if revList, _, _, err := svc.ListAccessRequests(ctx, nonAdmin, 10, "", "", "reviewer"); err != nil || len(revList) != 0 {
		t.Fatalf("requester non-approver scope=reviewer must be empty: len=%d err=%v", len(revList), err)
	}
	if approverList, _, _, err := svc.ListAccessRequests(ctx, approver, 10, "", "", "reviewer"); err != nil || len(approverList) != 1 || !approverList[0].CanDecide {
		t.Fatalf("active-step approver scope=reviewer must return CanDecide=true: list=%+v err=%v", approverList, err)
	}

	// 2. Decide (approve) -> issues Grant
	decidedView, replayed, err := svc.Decide(ctx, DecideInput{
		Actor:          approver,
		RequestID:      reqView.ID,
		Decision:       "approve",
		Comment:        "Approved for 2h",
		ExpectVersion:  reqView.Version,
		IdempotencyKey: "dec-ok-1",
	})
	if err != nil || replayed || decidedView == nil || decidedView.GrantID == nil {
		t.Fatalf("Decide approve: view=%+v replayed=%v err=%v", decidedView, replayed, err)
	}
	grantID := *decidedView.GrantID

	// 3. GetGrant + ListGrants
	gView, err := svc.GetGrant(ctx, nonAdmin, grantID)
	if err != nil || gView == nil {
		t.Fatalf("GetGrant: view=%+v err=%v", gView, err)
	}
	if _, err := svc.GetGrant(ctx, outsider, grantID); err == nil {
		t.Fatal("expected error for outsider GetGrant")
	}
	if gList, _, err := svc.ListGrants(ctx, nonAdmin, 10, ""); err != nil || len(gList) == 0 {
		t.Fatalf("ListGrants: len=%d err=%v", len(gList), err)
	}

	// 4. CreateSession + replay + GetSession + ListSessions + ListQueryOrders + CloseSession
	sessView, replayed, err := svc.CreateSession(ctx, CreateSessionInput{
		Actor:          nonAdmin,
		FlowID:         flowID,
		GrantID:        grantID,
		DatasourceIDs:  []string{dsID},
		IdempotencyKey: "sess-ok-1",
	})
	if err != nil || replayed || sessView == nil {
		t.Fatalf("CreateSession: view=%+v replayed=%v err=%v", sessView, replayed, err)
	}
	_, replayed, err = svc.CreateSession(ctx, CreateSessionInput{
		Actor:          nonAdmin,
		FlowID:         flowID,
		GrantID:        grantID,
		DatasourceIDs:  []string{dsID},
		IdempotencyKey: "sess-ok-1",
	})
	if err != nil || !replayed {
		t.Fatalf("CreateSession replay: replayed=%v err=%v", replayed, err)
	}
	if _, err := svc.GetSession(ctx, nonAdmin, sessView.ID); err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if _, err := svc.GetSession(ctx, outsider, sessView.ID); err == nil {
		t.Fatal("expected error for outsider GetSession")
	}
	if sList, _, err := svc.ListSessions(ctx, nonAdmin, 10, ""); err != nil || len(sList) == 0 {
		t.Fatalf("ListSessions: len=%d err=%v", len(sList), err)
	}
	if qoList, _, err := svc.ListQueryOrders(ctx, nonAdmin, 10, ""); err != nil || len(qoList) == 0 {
		t.Fatalf("ListQueryOrders: len=%d err=%v", len(qoList), err)
	}

	closedSess, replayed, err := svc.CloseSession(ctx, CloseSessionInput{
		Actor:          nonAdmin,
		SessionID:      sessView.ID,
		Reason:         "done querying",
		IdempotencyKey: "close-ok-1",
	})
	if err != nil || replayed || closedSess == nil {
		t.Fatalf("CloseSession: view=%+v replayed=%v err=%v", closedSess, replayed, err)
	}

	// 5. Revoke grant
	revGrant, replayed, err := svc.Revoke(ctx, RevokeInput{
		Actor:          approver,
		GrantID:        grantID,
		Reason:         "incident resolved",
		ExpectVersion:  gView.Version,
		IdempotencyKey: "rev-ok-1",
	})
	if err != nil || replayed || revGrant == nil {
		t.Fatalf("Revoke grant: view=%+v replayed=%v err=%v", revGrant, replayed, err)
	}

	// 6. Second request -> Withdraw
	req2, _, err := svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          nonAdmin,
		FlowID:         flowID,
		DatasourceIDs:  []string{dsID},
		RequestedUntil: now.Add(2 * time.Hour),
		Reason:         "Second request to withdraw",
		IdempotencyKey: "req-ok-2",
	})
	if err != nil || req2 == nil {
		t.Fatalf("CreateAccessRequest 2: %v", err)
	}
	withdrawn, replayed, err := svc.Withdraw(ctx, WithdrawInput{
		Actor:          nonAdmin,
		RequestID:      req2.ID,
		Reason:         "no longer needed",
		ExpectVersion:  req2.Version,
		IdempotencyKey: "wd-ok-1",
	})
	if err != nil || replayed || withdrawn == nil {
		t.Fatalf("Withdraw: view=%+v replayed=%v err=%v", withdrawn, replayed, err)
	}
}
