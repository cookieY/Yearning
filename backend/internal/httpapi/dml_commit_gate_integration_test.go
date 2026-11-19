//go:build integration

// dml_commit_gate_integration_test.go NEW-EXEC-01 regression: cancel/fence recheck in the DML
// "final statement succeeded -> COMMIT" window. When withdrawal (setting cancelling in same transaction)
// or watchdog finalization lands in that window, the transaction must roll back as cancelled/sent instead
// of committing -- injected via FaultHooks.AfterStatementPersist (after final statement fact persists,
// before commit decision) by setting Attempt to cancelling to simulate the race; control case proves commit
// succeeds normally when uncancelled.
//
// Requires real PostgreSQL (YEARNING_TEST_DATABASE_*; shares DB with httpapi integration fixtures,
// serialized via the same file lock). Reuses b10 full-chain fixture.
package httpapi

import (
	"testing"

	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
)

func TestDMLCommitWindowCancelRollsBack(t *testing.T) {
	fx := newB10Fixture(t)
	target := mustOpenTargetPG(t, fx)
	defer target.Close()
	execTarget(t, target, `drop table if exists exec_target.b10_commit_gate`)
	execTarget(t, target, `create table exec_target.b10_commit_gate(id int primary key, amount int)`)
	execTarget(t, target, `insert into exec_target.b10_commit_gate values (1, 100)`)
	dsID := fx.b10PGDatasource(t, "b10-gate", 54330)
	flowID := fx.b10ExecFlow(t, "b10-gate-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update b10_commit_gate set amount = amount + 1 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	fx.runner.SetFaultHooks(&execapp.FaultHooks{
		AfterStatementPersist: func(attemptID string, ordinal int32) error {
			// Withdrawal/watchdog hits between final statement success and COMMIT.
			return fx.db.Exec(`update execution_attempts set state = 'cancelling'
				where id = ?`, attemptID).Error
		},
	})
	defer fx.runner.SetFaultHooks(nil)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	state, _, _, _ := fx.b10AttemptRow(t, attempt)
	if state != "cancelled" {
		t.Fatalf("attempt=%s want cancelled (commit must not win the race)", state)
	}
	rows := fx.b10TargetQueryPG(t,
		`select amount from exec_target.b10_commit_gate where id = 1`)
	if len(rows) != 1 || rows[0][0] != "100" {
		t.Fatalf("commit landed after cancel in commit window: %v", rows)
	}
}

func TestDMLCommitWindowNoCancelStillCommits(t *testing.T) {
	fx := newB10Fixture(t)
	target := mustOpenTargetPG(t, fx)
	defer target.Close()
	execTarget(t, target, `drop table if exists exec_target.b10_commit_ok`)
	execTarget(t, target, `create table exec_target.b10_commit_ok(id int primary key, amount int)`)
	execTarget(t, target, `insert into exec_target.b10_commit_ok values (1, 100)`)
	dsID := fx.b10PGDatasource(t, "b10-gate-ok", 54330)
	flowID := fx.b10ExecFlow(t, "b10-gate-ok-flow", "exec_target", dsID)
	orderID, version := fx.b10OrderToExecutionPending(t, flowID,
		"update b10_commit_ok set amount = amount + 1 where id = 1;")
	attempt := fx.b10Begin(t, orderID, version)
	if err := fx.b10Drive(t, attempt); err != nil {
		t.Fatalf("drive: %v", err)
	}
	state, _, _, _ := fx.b10AttemptRow(t, attempt)
	if state != "succeeded" {
		t.Fatalf("attempt=%s want succeeded", state)
	}
	rows := fx.b10TargetQueryPG(t,
		`select amount from exec_target.b10_commit_ok where id = 1`)
	if len(rows) != 1 || rows[0][0] != "101" {
		t.Fatalf("normal commit broken by gate: %v", rows)
	}
}
