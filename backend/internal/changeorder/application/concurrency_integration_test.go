//go:build integration

package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

func TestChangeOrderRealPGSerializable40001Retry(t *testing.T) {
	fx := newSubFx(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	submitter := fx.seedUser(t, "co-ser-sub")
	approver := fx.seedUser(t, "co-ser-app")
	executor := fx.seedUser(t, "co-ser-exec")
	flowID := fx.seedFlow(t, []subStageSpec{{
		ApproverSteps: [][]string{{approver}},
		Executors:     []string{executor},
	}})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	order, err := fx.submitReadyDraftAs(t, draftID, flowID, submitter, "co-ser-sub")
	if err != nil {
		t.Fatalf("submitReadyDraftAs: %v", err)
	}

	var barrierWG sync.WaitGroup
	barrierWG.Add(2)
	barrierDone := make(chan struct{})
	go func() {
		barrierWG.Wait()
		close(barrierDone)
	}()

	var totalAttempts atomic.Int32
	var sawSerializationErr atomic.Bool
	errs := make([]error, 2)
	var workers sync.WaitGroup
	workers.Add(2)

	for i := 0; i < 2; i++ {
		go func() {
			defer workers.Done()
			var localAttempt int
			errs[i] = txutil.Serializable(ctx, fx.db, func(tx *gorm.DB) error {
				localAttempt++
				totalAttempts.Add(1)

				var curVersion int64
				if qerr := tx.WithContext(ctx).Raw(
					`select version from change_orders where id = ?`, order.ID,
				).Scan(&curVersion).Error; qerr != nil {
					return qerr
				}

				if localAttempt == 1 {
					// Both transactions read the same snapshot before either writes, forcing a
					// real PostgreSQL SQLSTATE 40001 serialization failure on the second committer.
					barrierWG.Done()
					select {
					case <-barrierDone:
					case <-ctx.Done():
						return ctx.Err()
					}
				} else {
					sawSerializationErr.Store(true)
				}

				res := tx.WithContext(ctx).Exec(
					`update change_orders set version = ? where id = ? and version = ?`,
					curVersion+1, order.ID, curVersion,
				)
				if res.Error != nil {
					if txutil.IsSerializationFailure(res.Error) {
						sawSerializationErr.Store(true)
					}
					return res.Error
				}
				if res.RowsAffected != 1 {
					return domain.ErrConcurrent()
				}
				return nil
			})
		}()
	}
	workers.Wait()

	for idx, werr := range errs {
		if werr != nil {
			t.Fatalf("worker %d failed: %v", idx, werr)
		}
	}
	if totalAttempts.Load() < 3 || !sawSerializationErr.Load() {
		t.Fatalf("expected real PostgreSQL 40001 retry (attempts >= 3 and sawSerializationErr=true), got attempts=%d saw=%v",
			totalAttempts.Load(), sawSerializationErr.Load())
	}

	var finalVersion int64
	if err := fx.db.Raw(`select version from change_orders where id = ?`, order.ID).Scan(&finalVersion).Error; err != nil {
		t.Fatal(err)
	}
	if finalVersion != order.Version+2 {
		t.Fatalf("final version = %d, want %d (no lost update across 40001 retry)", finalVersion, order.Version+2)
	}
}

func TestChangeOrderConcurrentDecideAndWithdrawCAS(t *testing.T) {
	fx := newSubFx(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	submitter := fx.seedUser(t, "co-cas-sub")
	approver := fx.seedUser(t, "co-cas-app")
	executor := fx.seedUser(t, "co-cas-exec")
	flowID := fx.seedFlow(t, []subStageSpec{{
		ApproverSteps: [][]string{{approver}},
		Executors:     []string{executor},
	}})
	fx.seedGrant(t, submitter, flowID)

	// Part 1: Concurrent Decide vs Withdraw with distinct idempotency keys at same ExpectVersion.
	draft1 := fx.seedReadyDraft(t, flowID, submitter)
	order1, err := fx.submitReadyDraftAs(t, draft1, flowID, submitter, "co-cas-sub")
	if err != nil {
		t.Fatalf("submitReadyDraftAs(1): %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var decErr, wdErr error
	var decView, wdView *OrderView

	go func() {
		defer wg.Done()
		<-start
		decView, _, decErr = fx.svc.Decide(ctx, Actor{UserID: approver, Username: "co-cas-app"}, RequestMeta{RequestID: uuid.NewString()}, DecideInput{
			OrderID:        order1.ID,
			ExpectVersion:  order1.Version,
			Decision:       "approve",
			Comment:        "looks good",
			IdempotencyKey: "cas-decide-1",
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		wdView, _, wdErr = fx.svc.Withdraw(ctx, Actor{UserID: submitter, Username: "co-cas-sub"}, RequestMeta{RequestID: uuid.NewString()}, LifecycleInput{
			OrderID:        order1.ID,
			ExpectVersion:  order1.Version,
			Reason:         "cancel race",
			IdempotencyKey: "cas-withdraw-1",
		})
	}()
	close(start)
	wg.Wait()

	successes := 0
	var loserErr error
	var winnerState string
	if decErr == nil && decView != nil {
		successes++
		winnerState = decView.State
	} else {
		loserErr = decErr
	}
	if wdErr == nil && wdView != nil {
		successes++
		winnerState = wdView.State
	} else {
		loserErr = wdErr
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 winner in Decide vs Withdraw CAS race, got %d (decErr=%v wdErr=%v)", successes, decErr, wdErr)
	}
	var derr *domain.Error
	if !errors.As(loserErr, &derr) || (derr.Code != domain.CodeConcurrent && derr.Code != domain.ErrIllegal().Code) {
		t.Fatalf("expected 1004 or 1010 for loser, got %v", loserErr)
	}

	var dbState string
	var dbVersion int64
	if err := fx.db.Raw(`select state, version from change_orders where id = ?`, order1.ID).Row().Scan(&dbState, &dbVersion); err != nil {
		t.Fatal(err)
	}
	if dbState != winnerState || dbVersion != order1.Version+1 {
		t.Fatalf("DB state/version mismatch: got (%s, %d), want (%s, %d)", dbState, dbVersion, winnerState, order1.Version+1)
	}

	var decisionCount, withdrawAuditCount int64
	if err := fx.db.Raw(`select count(*) from approval_decisions where order_id = ?`, order1.ID).Scan(&decisionCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Raw(`select count(*) from audit_events where resource_id = ? and event_type = 'change_order.withdraw'`, order1.ID).Scan(&withdrawAuditCount).Error; err != nil {
		t.Fatal(err)
	}
	if decisionCount+withdrawAuditCount != 1 {
		t.Fatalf("expected exactly 1 persisted state-transition side effect, got decisions=%d withdraws=%d (winnerState=%s)", decisionCount, withdrawAuditCount, winnerState)
	}

	// Part 2: Concurrent Decide calls with identical IdempotencyKey produce a single approval decision.
	draft2 := fx.seedReadyDraft(t, flowID, submitter)
	order2, err := fx.submitReadyDraftAs(t, draft2, flowID, submitter, "co-cas-sub")
	if err != nil {
		t.Fatalf("submitReadyDraftAs(2): %v", err)
	}

	start2 := make(chan struct{})
	var wg2 sync.WaitGroup
	wg2.Add(2)
	var vA, vB *OrderView
	var eA, eB error
	idemKey := fmt.Sprintf("same-idem-%s", order2.ID[:8])
	for idx := 0; idx < 2; idx++ {
		go func(slot int) {
			defer wg2.Done()
			<-start2
			v, _, e := fx.svc.Decide(ctx, Actor{UserID: approver, Username: "co-cas-app"}, RequestMeta{RequestID: "r-idem"}, DecideInput{
				OrderID:        order2.ID,
				ExpectVersion:  order2.Version,
				Decision:       "approve",
				Comment:        "idempotent race",
				IdempotencyKey: idemKey,
			})
			if slot == 0 {
				vA, eA = v, e
			} else {
				vB, eB = v, e
			}
		}(idx)
	}
	close(start2)
	wg2.Wait()

	if eA != nil && eB != nil {
		t.Fatalf("at least one idempotent caller must succeed: eA=%v eB=%v", eA, eB)
	}
	if eA == nil && vA.State != string(domain.StateStageExecution) {
		t.Fatalf("unexpected state for vA: %s", vA.State)
	}
	if eB == nil && vB.State != string(domain.StateStageExecution) {
		t.Fatalf("unexpected state for vB: %s", vB.State)
	}
	var dec2Count int64
	fx.db.Raw(`select count(*) from approval_decisions where order_id = ?`, order2.ID).Scan(&dec2Count)
	if dec2Count != 1 {
		t.Fatalf("expected exactly 1 approval_decisions row for shared idempotency key, got %d", dec2Count)
	}
}
