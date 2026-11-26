//go:build integration

package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

func TestWorkflowConcurrentReplaceAndDeleteCASRace(t *testing.T) {
	db := testkit.OpenPG(t)

	for _, tbl := range []string{
		"flow_masking_rules", "flow_query_capabilities",
		"flow_approval_step_actors", "flow_execution_actors",
		"flow_approval_steps", "flow_stages", "flows",
		"change_drafts", "query_access_requests", "permission_group_flow_grants",
		"datasources", "rule_sets", "users",
		"audit_events", "domain_events", "outbox_events",
	} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	crypter := testkit.NewCrypter(t)
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	wf, err := NewService(db, crypter, recorder)
	if err != nil {
		t.Fatal(err)
	}
	reviewSvc, err := reviewapp.NewServices(db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	adminID := uuid.NewString()
	if err := db.Exec(`insert into users (id, username, display_name, is_builtin_admin, role, created_at, updated_at)
		values (?, 'wf-cas-admin', 'Admin', false, 'admin', now(), now())`, adminID).Error; err != nil {
		t.Fatal(err)
	}
	ruleView, err := reviewSvc.CreateRuleSet(ctx, adminID, "wf-cas-rules", true, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	dsID := uuid.NewString()
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
		host, port, database_name, version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, 'wf-cas-ds', 'mysql', 'mysql', 'native', 'db.example.com', 3306, 'shop', '8.0.x', true, ?, 1, ?, now(), now())`,
		dsID, strings.Repeat("ab", 32), adminID).Error; err != nil {
		t.Fatal(err)
	}

	admin := AdminActor{UserID: adminID, Username: "wf-cas-admin"}
	makeWrite := func(name string) *FlowWrite {
		return &FlowWrite{
			Name: name, FlowType: "change_review", Enabled: true, RuleSetID: &ruleView.ID,
			Stages: []StageWrite{{
				Position: 1, DatasourceID: dsID,
				ApprovalSteps:   []ApprovalStepWrite{{Position: 1, Actors: []ActorRef{{UserID: adminID}}}},
				ExecutionActors: []ActorRef{{UserID: adminID}},
			}},
		}
	}

	flow, err := wf.CreateFlow(ctx, admin, makeWrite("wf-cas-v1"), RequestMeta{RequestID: uuid.NewString()})
	if err != nil {
		t.Fatalf("CreateFlow: %v", err)
	}

	// Part 1: Concurrent ReplaceFlow vs ReplaceFlow at expectVersion=1.
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	views := make([]*FlowView, 2)
	errs := make([]error, 2)
	names := []string{"wf-cas-v2-alpha", "wf-cas-v2-beta"}
	for i := 0; i < 2; i++ {
		go func(slot int) {
			defer wg.Done()
			<-start
			views[slot], errs[slot] = wf.ReplaceFlow(ctx, admin, flow.ID, flow.Version, makeWrite(names[slot]), RequestMeta{RequestID: uuid.NewString()})
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	var winner *FlowView
	var loserErr error
	for i := 0; i < 2; i++ {
		if errs[i] == nil && views[i] != nil {
			successes++
			winner = views[i]
		} else {
			loserErr = errs[i]
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 ReplaceFlow winner, got %d (err0=%v err1=%v)", successes, errs[0], errs[1])
	}
	if !errors.Is(loserErr, bizerr.ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict (1003) for losing ReplaceFlow, got %v", loserErr)
	}
	if winner.Version != 2 {
		t.Fatalf("winner version = %d, want 2", winner.Version)
	}
	var stageCount int64
	db.Raw(`select count(*) from flow_stages where flow_id = ?`, flow.ID).Scan(&stageCount)
	if stageCount != 1 {
		t.Fatalf("flow_stages count = %d, want 1 (no orphan stages)", stageCount)
	}

	// Part 2: Concurrent ReplaceFlow vs DeleteFlow at expectVersion=2.
	start2 := make(chan struct{})
	var wg2 sync.WaitGroup
	wg2.Add(2)
	var repErr, delErr error
	var repView *FlowView
	go func() {
		defer wg2.Done()
		<-start2
		repView, repErr = wf.ReplaceFlow(ctx, admin, flow.ID, winner.Version, makeWrite("wf-cas-v3"), RequestMeta{RequestID: uuid.NewString()})
	}()
	go func() {
		defer wg2.Done()
		<-start2
		delErr = wf.DeleteFlow(ctx, admin, flow.ID, winner.Version, RequestMeta{RequestID: uuid.NewString()})
	}()
	close(start2)
	wg2.Wait()

	part2Success := 0
	var part2LoserErr error
	if repErr == nil && repView != nil {
		part2Success++
	} else {
		part2LoserErr = repErr
	}
	if delErr == nil {
		part2Success++
	} else {
		part2LoserErr = delErr
	}
	if part2Success != 1 {
		t.Fatalf("expected exactly 1 winner in Replace vs Delete race, got %d (repErr=%v delErr=%v)", part2Success, repErr, delErr)
	}
	if !errors.Is(part2LoserErr, bizerr.ErrVersionConflict) &&
		!errors.Is(part2LoserErr, bizerr.ErrConcurrentModification) &&
		!errors.Is(part2LoserErr, bizerr.ErrNotFound) {
		t.Fatalf("expected 1003/1004/1002 for losing Replace/Delete caller, got %v", part2LoserErr)
	}
}
