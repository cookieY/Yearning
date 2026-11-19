//go:build integration

package application

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
)

func TestChangeOrderCommentsAndRollbackIntegration(t *testing.T) {
	fx := newSubFx(t)
	ctx := context.Background()

	submitter := fx.seedUser(t, "co-cov-sub")
	approver := fx.seedUser(t, "co-cov-app")
	executor := fx.seedUser(t, "co-cov-exec")
	outsider := fx.seedUser(t, "co-cov-out")

	flowID := fx.seedFlow(t, []subStageSpec{
		{ApproverSteps: [][]string{{approver}}, Executors: []string{executor}},
	})
	fx.seedGrant(t, submitter, flowID)
	draftID := fx.seedReadyDraft(t, flowID, submitter)
	fx.seedReadyRun(t, draftID, flowID)

	view, err := fx.submitAs(t, draftID, submitter, "co-cov-sub")
	if err != nil {
		t.Fatalf("submitAs: %v", err)
	}
	subActor := Actor{UserID: submitter, Username: "co-cov-sub"}
	outActor := Actor{UserID: outsider, Username: "co-cov-out"}

	// 1. Comments validation & authorization
	if _, _, err := fx.svc.CreateComment(ctx, subActor, RequestMeta{}, view.ID, "", "idem-empty"); err == nil {
		t.Fatal("expected error for empty comment")
	}
	if _, _, err := fx.svc.CreateComment(ctx, subActor, RequestMeta{}, view.ID, strings.Repeat("x", 4097), "idem-long"); err == nil {
		t.Fatal("expected error for oversized comment")
	}
	if _, _, err := fx.svc.CreateComment(ctx, outActor, RequestMeta{}, view.ID, "outsider note", "idem-out"); err == nil {
		t.Fatal("expected error for outsider comment")
	}
	if _, _, err := fx.svc.ListComments(ctx, outActor, view.ID, 10, ""); err == nil {
		t.Fatal("expected error for outsider ListComments")
	}

	// Create 3 comments and verify idempotency replay + cursor pagination
	c1, replayed, err := fx.svc.CreateComment(ctx, subActor, RequestMeta{RequestID: "r1"}, view.ID, "first comment", "idem-c1")
	if err != nil || replayed || c1 == nil {
		t.Fatalf("CreateComment c1: view=%v replayed=%v err=%v", c1, replayed, err)
	}
	c1Replay, replayed, err := fx.svc.CreateComment(ctx, subActor, RequestMeta{RequestID: "r1"}, view.ID, "first comment", "idem-c1")
	if err != nil || !replayed || c1Replay.ID != c1.ID {
		t.Fatalf("CreateComment c1 replay: view=%v replayed=%v err=%v", c1Replay, replayed, err)
	}
	_, _, err = fx.svc.CreateComment(ctx, subActor, RequestMeta{}, view.ID, "second comment", "idem-c2")
	if err != nil {
		t.Fatalf("CreateComment c2: %v", err)
	}
	_, _, err = fx.svc.CreateComment(ctx, subActor, RequestMeta{}, view.ID, "third comment", "idem-c3")
	if err != nil {
		t.Fatalf("CreateComment c3: %v", err)
	}

	page1, nextCur, err := fx.svc.ListComments(ctx, subActor, view.ID, 2, "")
	if err != nil || len(page1) != 2 || nextCur == nil {
		t.Fatalf("ListComments page1: len=%d next=%v err=%v", len(page1), nextCur, err)
	}
	page2, nextCur2, err := fx.svc.ListComments(ctx, subActor, view.ID, 2, *nextCur)
	if err != nil || len(page2) != 1 || nextCur2 != nil {
		t.Fatalf("ListComments page2: len=%d next=%v err=%v", len(page2), nextCur2, err)
	}

	// 2. Rollback evidence, RevealOrderRollbackSQL, CreateRollbackDraft, and status transitions
	if _, err := fx.svc.RevealOrderRollbackSQL(ctx, outActor, RequestMeta{}, view.ID, ""); err == nil {
		t.Fatal("expected error for outsider RevealOrderRollbackSQL")
	}
	if _, err := fx.svc.RevealOrderRollbackSQL(ctx, subActor, RequestMeta{}, view.ID, ""); err == nil {
		t.Fatal("expected error when no rollback evidence exists")
	}
	if _, _, err := fx.svc.CreateRollbackDraft(ctx, subActor, RequestMeta{}, RollbackDraftInput{
		OrderID: view.ID, IdempotencyKey: "idem-rb-early",
	}); err == nil {
		t.Fatal("expected error when order is not completed")
	}

	// Look up datasource_id of stage 0
	var dsID string
	if err := fx.db.Raw(`select datasource_id::text from order_stages where id = ?`, view.Stages[0].ID).Scan(&dsID).Error; err != nil {
		t.Fatalf("select datasource_id: %v", err)
	}
	now := time.Now().UTC()
	attemptID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into execution_attempts (
			id, order_id, stage_id, initiated_by_user_id_snapshot, initiated_by_username_snapshot,
			execution_kind, state, send_boundary, fence_token, started_at, finished_at, version, created_at
		) values (?, ?, ?, ?, 'co-cov-exec', 'dml', 'succeeded', 'sent', 1, ?, ?, 1, ?)`,
		attemptID, view.ID, view.Stages[0].ID, executor, now, now, now,
	).Error; err != nil {
		t.Fatalf("insert execution_attempts: %v", err)
	}

	// Seal a valid rollback EvidenceBundle and mark order completed + available
	evID := uuid.NewString()
	rbSQL := "DELETE FROM public.accounts WHERE id = 42;"
	bundle := &rollback.EvidenceBundle{
		Engine:             "postgresql",
		ServerVersion:      "16.0",
		ServerVersionNum:   160000,
		SchemaName:         "public",
		OperationType:      "dml",
		Strategy:           rollback.StrategyPGDMLTxReturning,
		OriginalStatements: []string{"INSERT INTO public.accounts(id) VALUES (42);"},
		RollbackStatements: []string{rbSQL},
		RollbackSQL:        rbSQL,
		AffectedRows:       1,
	}
	env, sqlHMAC, err := rollback.SealEvidenceBundle(fx.crypter, evID, bundle)
	if err != nil {
		t.Fatalf("SealEvidenceBundle: %v", err)
	}
	if err := fx.db.Exec(`
		insert into order_rollback_evidences (
			id, order_id, stage_id, attempt_id, datasource_id,
			database_engine, database_version, schema_name, operation_type, rollback_status,
			affected_rows,
			rollback_payload_ciphertext, rollback_payload_nonce, rollback_payload_key_id, rollback_payload_algorithm,
			rollback_sql_hmac, created_at, updated_at
		) values (?, ?, ?, ?, ?, 'postgresql', '16.0', 'public', 'dml', 'available', 1, ?, ?, ?, ?, ?, ?, ?)`,
		evID, view.ID, view.Stages[0].ID, attemptID, dsID,
		env.Ciphertext, env.Nonce, env.KeyID, env.Algorithm,
		sqlHMAC, now, now,
	).Error; err != nil {
		t.Fatalf("insert order_rollback_evidences: %v", err)
	}
	if err := fx.db.Exec(`
		update change_orders
		set state = 'completed', current_stage_position = null, terminal_at = ?, rollback_status = 'available', rollback_evidence_id = ?
		where id = ?`, now, evID, view.ID).Error; err != nil {
		t.Fatalf("update change_orders completed: %v", err)
	}

	// RevealOrderRollbackSQL succeeds and writes audit event
	reqID := uuid.NewString()
	rev, err := fx.svc.RevealOrderRollbackSQL(ctx, subActor, RequestMeta{RequestID: reqID}, view.ID, "")
	if err != nil || rev == nil || rev.SQL != rbSQL {
		t.Fatalf("RevealOrderRollbackSQL: rev=%+v err=%v", rev, err)
	}

	// CreateRollbackDraft succeeds and replays idempotently
	rbDraft, replayed, err := fx.svc.CreateRollbackDraft(ctx, subActor, RequestMeta{RequestID: reqID}, RollbackDraftInput{
		OrderID: view.ID, IdempotencyKey: "idem-rb-ok",
	})
	if err != nil || replayed || rbDraft == nil || rbDraft.DraftID == "" {
		t.Fatalf("CreateRollbackDraft: draft=%+v replayed=%v err=%v", rbDraft, replayed, err)
	}
	rbDraftReplay, replayed, err := fx.svc.CreateRollbackDraft(ctx, subActor, RequestMeta{RequestID: reqID}, RollbackDraftInput{
		OrderID: view.ID, IdempotencyKey: "idem-rb-ok",
	})
	if err != nil || !replayed || rbDraftReplay.DraftID != rbDraft.DraftID {
		t.Fatalf("CreateRollbackDraft replay: draft=%+v replayed=%v err=%v", rbDraftReplay, replayed, err)
	}

	// Exercise assertRollbackSubmissionAllowed & restoreSourceOrderRollbackStatusOnAbort
	if err := fx.svc.assertRollbackSubmissionAllowed(ctx, fx.db, "", nil, "", nil, now); err == nil {
		t.Fatal("expected error on empty sourceOrderID")
	}
	if err := fx.svc.assertRollbackSubmissionAllowed(ctx, fx.db, view.ID, &evID, "wrong-hmac", []byte("SELECT 1"), now); err == nil {
		t.Fatal("expected error on mismatched HMAC")
	}
	if err := fx.svc.assertRollbackSubmissionAllowed(ctx, fx.db, view.ID, &evID, "", []byte(rbSQL), now); err != nil {
		t.Fatalf("assertRollbackSubmissionAllowed valid: %v", err)
	}

	// Insert a synthetic child rollback order so populateOrderRollbackView & restoreSourceOrderRollbackStatusOnAbort cover child order paths
	childOrderID := uuid.NewString()
	if err := fx.db.Exec(`
		insert into change_orders (
			id, display_number, submitter_user_id_snapshot, submitter_username_snapshot, submitter_display_name_snapshot,
			title, description, state, sql_hmac, snapshot_hash, manually_verified,
			rollback_enabled, ticket_type, source_order_id, rollback_evidence_id,
			version, submitted_at, terminal_at
		) values (?, 'YR-20260102-000099', ?, 'co-cov-sub', 'co-cov-sub', 'rb order', 'desc', 'rejected', ?, ?, false, false, 'rollback', ?, ?, 1, ?, ?)`,
		childOrderID, submitter, strings.Repeat("a", 64), strings.Repeat("b", 64), view.ID, evID, now, now,
	).Error; err != nil {
		t.Fatalf("insert child rollback order: %v", err)
	}

	// Restore source order rollback status on abort
	childModel := &persistence.ChangeOrderModel{
		ID:                 childOrderID,
		TicketType:         "rollback",
		SourceOrderID:      &view.ID,
		RollbackEvidenceID: &evID,
	}
	if err := fx.svc.restoreSourceOrderRollbackStatusOnAbort(ctx, fx.db, childModel, now); err != nil {
		t.Fatalf("restoreSourceOrderRollbackStatusOnAbort: %v", err)
	}

	// Read both source order and child order via GetOrder / buildOrderView to exercise populateOrderRollbackView
	srcOrderView, err := fx.svc.GetOrder(ctx, subActor, view.ID)
	if err != nil || len(srcOrderView.RollbackOrders) == 0 {
		t.Fatalf("GetOrder source order: view=%+v err=%v", srcOrderView, err)
	}
	if _, err := fx.svc.GetOrder(ctx, outActor, view.ID); err == nil {
		t.Fatal("expected error for outsider GetOrder")
	}
	var srcModel persistence.ChangeOrderModel
	_ = fx.db.First(&srcModel, "id = ?", childOrderID).Error
	childView := &OrderView{}
	if err := fx.svc.populateOrderRollbackView(ctx, fx.db, &srcModel, childView); err != nil || childView.SourceOrderDisplayNumber == nil {
		t.Fatalf("populateOrderRollbackView child: view=%+v err=%v", childView, err)
	}

	// Exercise RevealOrderSQL (mismatch branch + rollback-order-sql branch), ListOrderFindings, and CopyToDraft
	_, _ = fx.svc.RevealOrderSQL(ctx, subActor, RequestMeta{RequestID: reqID}, view.ID, "review")
	sqlRev, err := fx.svc.RevealOrderSQL(ctx, subActor, RequestMeta{RequestID: reqID}, view.ID, "rollback-order-sql")
	if err != nil || sqlRev == nil {
		t.Fatalf("RevealOrderSQL rollback-order-sql: rev=%+v err=%v", sqlRev, err)
	}
	if _, _, err := fx.svc.ListOrderFindings(ctx, subActor, view.ID, 10, "", RequestMeta{RequestID: reqID}); err != nil {
		t.Fatalf("ListOrderFindings: %v", err)
	}
	cpView, replayed, err := fx.svc.CopyToDraft(ctx, subActor, RequestMeta{RequestID: reqID}, CopyInput{
		OrderID:        view.ID,
		TargetFlowID:   flowID,
		Title:          "Copied Order",
		Description:    "Copied description",
		IdempotencyKey: "idem-copy-1",
	})
	if err != nil || replayed || cpView == nil || cpView.DraftID == "" {
		t.Fatalf("CopyToDraft: view=%+v replayed=%v err=%v", cpView, replayed, err)
	}
	_, replayed, err = fx.svc.CopyToDraft(ctx, subActor, RequestMeta{RequestID: reqID}, CopyInput{
		OrderID:        view.ID,
		TargetFlowID:   flowID,
		Title:          "Copied Order",
		Description:    "Copied description",
		IdempotencyKey: "idem-copy-1",
	})
	if err != nil || !replayed {
		t.Fatalf("CopyToDraft replay: replayed=%v err=%v", replayed, err)
	}
}
