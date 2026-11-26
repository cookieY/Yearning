//go:build integration

package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
)

func TestQueryRequestConcurrentDecideCASRace(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	requesterID := uuid.NewString()
	reviewer1ID := uuid.NewString()
	reviewer2ID := uuid.NewString()
	for _, u := range []struct{ id, name string }{
		{requesterID, "q-cas-req"},
		{reviewer1ID, "q-cas-rev1"},
		{reviewer2ID, "q-cas-rev2"},
	} {
		if err := db.Exec(`insert into users (id, username, display_name, is_builtin_admin, version, created_at, updated_at)
			values (?, ?, ?, false, 1, now(), now())`, u.id, u.name, u.name).Error; err != nil {
			t.Fatal(err)
		}
	}

	dsID := uuid.NewString()
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, test_purpose, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'q-cas-ds', 'postgresql', 'postgresql', 'native', 'db.internal', 5432, 'app', '17.x', true, 'query', ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("aa", 32), requesterID).Error; err != nil {
		t.Fatal(err)
	}

	flowID := uuid.NewString()
	if err := db.Exec(`insert into flows (id, name, flow_type, status, version, created_by_user_id, created_at, updated_at)
		values (?, 'q-cas-flow', 'query_access', 'enabled', 1, ?, now(), now())`, flowID, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	stepID := uuid.NewString()
	if err := db.Exec(`insert into flow_approval_steps (id, flow_id, position) values (?, ?, 1)`, stepID, flowID).Error; err != nil {
		t.Fatal(err)
	}
	for _, rid := range []string{reviewer1ID, reviewer2ID} {
		if err := db.Exec(`insert into flow_approval_step_actors (step_id, user_id) values (?, ?)`, stepID, rid).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`insert into flow_query_capabilities (flow_id, datasource_id, can_query, can_export)
		values (?, ?, true, true)`, flowID, dsID).Error; err != nil {
		t.Fatal(err)
	}
	pgID := uuid.NewString()
	if err := db.Exec(`insert into permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
		values (?, 'q-cas-pg', true, 1, ?, now(), now())`, pgID, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_members (permission_group_id, user_id) values (?, ?)`, pgID, requesterID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into permission_group_flow_grants (permission_group_id, flow_id) values (?, ?)`, pgID, flowID).Error; err != nil {
		t.Fatal(err)
	}

	reqView, _, err := svc.CreateAccessRequest(ctx, CreateAccessRequestInput{
		Actor:          Actor{UserID: requesterID, Username: "q-cas-req"},
		FlowID:         flowID,
		DatasourceIDs:  []string{dsID},
		RequestedUntil: time.Now().Add(24 * time.Hour),
		Reason:         "cas decide race",
		IdempotencyKey: "q-cas-req-1",
	})
	if err != nil {
		t.Fatalf("CreateAccessRequest: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	views := make([]*AccessRequestView, 2)
	errs := make([]error, 2)

	go func() {
		defer wg.Done()
		<-start
		views[0], _, errs[0] = svc.Decide(ctx, DecideInput{
			Actor:          Actor{UserID: reviewer1ID, Username: "q-cas-rev1"},
			RequestID:      reqView.ID,
			Decision:       "approve",
			Comment:        "approve race",
			ExpectVersion:  reqView.Version,
			IdempotencyKey: "q-cas-approve-" + uuid.NewString()[:8],
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		views[1], _, errs[1] = svc.Decide(ctx, DecideInput{
			Actor:          Actor{UserID: reviewer2ID, Username: "q-cas-rev2"},
			RequestID:      reqView.ID,
			Decision:       "reject",
			Comment:        "reject race",
			ExpectVersion:  reqView.Version,
			IdempotencyKey: "q-cas-reject-" + uuid.NewString()[:8],
		})
	}()
	close(start)
	wg.Wait()

	successes := 0
	var winnerView *AccessRequestView
	var loserErr error
	for i := 0; i < 2; i++ {
		if errs[i] == nil && views[i] != nil {
			successes++
			winnerView = views[i]
		} else {
			loserErr = errs[i]
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 Decide winner, got %d (err0=%v err1=%v)", successes, errs[0], errs[1])
	}
	var derr *domain.Error
	if !errors.As(loserErr, &derr) || (derr.Code != domain.CodeConcurrent && derr.Code != domain.ErrIllegal().Code) {
		t.Fatalf("expected 1004 or 1010 for losing Decide caller, got %v", loserErr)
	}

	var decisionCount, grantCount int64
	db.Raw(`select count(*) from query_access_decisions where request_id = ?`, reqView.ID).Scan(&decisionCount)
	db.Raw(`select count(*) from query_grants where query_access_request_id = ?`, reqView.ID).Scan(&grantCount)
	if decisionCount != 1 {
		t.Fatalf("query_access_decisions count = %d, want 1", decisionCount)
	}
	wantGrants := int64(0)
	if winnerView.State == string(domain.RequestGrantActive) {
		wantGrants = 1
	}
	if grantCount != wantGrants {
		t.Fatalf("query_grants count = %d, want %d (winner state=%s)", grantCount, wantGrants, winnerView.State)
	}
}
