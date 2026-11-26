//go:build integration

package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func seedExecBeginFixture(
	t *testing.T, fx *reclaimFx, metadataJSON, sqlText, stmtKind, stmtOp, displayNumber string,
) (execUserID, orderID string) {
	t.Helper()
	for _, tbl := range []string{
		"async_tasks", "order_execution_actors", "order_sql_payloads",
		"sql_statements", "stage_review_snapshots", "review_runs",
		"datasources", "users",
	} {
		if err := fx.db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}

	execUserID = uuid.NewString()
	mustExec(t, fx.db, `
		insert into users (id, username, display_name, is_builtin_admin, version, created_at, updated_at)
		values (?, ?, 'Exec Race User', false, 1, now(), now())`,
		execUserID, "exec-race-"+execUserID[:8])

	dsID := uuid.NewString()
	dsFP := repeatHex("22")
	mustExec(t, fx.db, `
		insert into datasources (id, name, engine, compatibility_mode, deployment_kind,
			host, port, database_name, version_constraint, enabled, identity_fingerprint,
			version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'mysql', 'mysql', 'native', '127.0.0.1', 3306, 'shop', '8.0.x',
			true, ?, 1, ?, now(), now())`,
		dsID, "exec-race-ds-"+dsID[:8], dsFP, execUserID)
	if metadataJSON != "" {
		mustExec(t, fx.db, `update datasources set metadata = ?::jsonb where id = ?`, metadataJSON, dsID)
	}

	sqlBytes := []byte(sqlText)
	sqlHMAC, err := fx.svc.hmacSQL(sqlBytes)
	if err != nil {
		t.Fatal(err)
	}
	fpEngine, err := fingerprint.NewEngine(fingerprint.CryptoSigner{C: fx.svc.Crypter}, fingerprint.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fpRes, err := fpEngine.Analyze(fingerprint.Profile{Engine: "mysql", Mode: "mysql"}, sqlBytes, privacy.NewRunMapper())
	if err != nil {
		t.Fatal(err)
	}

	if stmtKind == "" {
		stmtKind = "dml"
	}
	if stmtOp == "" {
		stmtOp = "update"
	}

	runID := uuid.NewString()
	snapID := uuid.NewString()
	draftID := uuid.NewString()
	now := time.Now().UTC()
	mustExec(t, fx.db, `
		insert into review_runs (id, draft_id, draft_revision, state, flow_snapshot_hash,
			rule_config_hash, tool_config_hash, provider_plan_hash,
			fingerprint_algorithm_version, anonymization_algorithm_version,
			statement_count, fingerprint_group_count, budget_day, version, created_at, finished_at)
		values (?, ?, 1, 'ready', ?, ?, ?, ?, ?, 'v1', 1, 1, current_date, 1, ?, ?)`,
		runID, draftID, repeatHex("31"), repeatHex("32"), repeatHex("33"), repeatHex("34"),
		fingerprint.AlgorithmVersion, now, now)

	mustExec(t, fx.db, `
		insert into sql_statements (id, review_run_id, ordinal, statement_kind, operation,
			size_bytes, algorithm_version, dialect_profile, fingerprint_quality,
			fingerprint_key_id, canonical_fingerprint_hmac, dialect_fingerprint_hmac,
			execution_shape_hmac, typed_shape)
		values (?, ?, 1, ?, ?, ?, ?, 'mysql', 'exact', ?, ?, ?, ?, '{}'::jsonb)`,
		uuid.NewString(), runID, stmtKind, stmtOp, len(sqlBytes), fingerprint.AlgorithmVersion,
		fpRes.FingerprintKeyID, fpRes.CanonicalFingerprintHMAC, fpRes.DialectFingerprintHMAC,
		fpRes.ExecutionShapeHMAC)

	mustExec(t, fx.db, `
		insert into stage_review_snapshots (id, review_run_id, stage_position, datasource_id,
			datasource_identity_fingerprint, state, highest_severity, gate_passed,
			normalized_context_ciphertext, normalized_context_nonce,
			normalized_context_key_id, normalized_context_algorithm,
			snapshot_hash, created_at, finished_at)
		values (?, ?, 1, ?, ?, 'passed', 'none', true, ?, ?, 'pay-1', 'aes-256-gcm', ?, ?, ?)`,
		snapID, runID, dsID, dsFP, []byte{1}, []byte{2}, repeatHex("35"), now, now)

	orderID = uuid.NewString()
	stageID := uuid.NewString()
	mustExec(t, fx.db, `
		insert into change_orders (id, display_number, submitter_user_id_snapshot,
			submitter_username_snapshot, submitter_display_name_snapshot, title,
			description, state, current_stage_position, sql_hmac, snapshot_hash,
			version, submitted_at)
		values (?, ?, ?, 'sub1', 'Sub One', 'exec-cas-race', '',
			'stage_execution_pending', 1, ?, ?, 1, now())`,
		orderID, displayNumber, uuid.NewString(), sqlHMAC, repeatHex("36"))

	sqlEnv, err := fx.svc.Crypter.Seal(crypto.PurposePayload, sqlBytes, crypto.AAD{
		DeploymentID: fx.svc.Crypter.DeploymentID(),
		Table:        "order_sql_payloads",
		PrimaryKey:   orderID,
		Column:       "sql_ciphertext",
		Purpose:      crypto.PurposePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, fx.db, `
		insert into order_sql_payloads (order_id, sql_ciphertext, sql_nonce, sql_key_id,
			sql_algorithm, plaintext_hmac, size_bytes, statement_count, created_at)
		values (?, ?, ?, ?, ?, ?, ?, 1, now())`,
		orderID, sqlEnv.Ciphertext, sqlEnv.Nonce, sqlEnv.KeyID, sqlEnv.Algorithm, sqlHMAC, len(sqlBytes))

	targetEnv, err := orderdomain.SealTargetSnapshot(fx.svc.Crypter, stageID, orderdomain.TargetSnapshot{
		DatasourceID:        dsID,
		Engine:              "mysql",
		IdentityFingerprint: dsFP,
		TargetDatabase:      "shop",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, fx.db, `
		insert into order_stages (id, order_id, position, datasource_id,
			datasource_name_snapshot, datasource_engine_snapshot,
			datasource_identity_fingerprint, stage_review_snapshot_id,
			review_snapshot_hash, review_gate_passed, target_snapshot_ciphertext,
			target_snapshot_nonce, target_snapshot_key_id, target_snapshot_algorithm,
			state, version, activated_at)
		values (?, ?, 1, ?, 'exec-race-ds', 'mysql', ?, ?, ?, true, ?, ?, ?, ?, 'execution_pending', 1, now())`,
		stageID, orderID, dsID, dsFP, snapID, repeatHex("37"),
		targetEnv.Ciphertext, targetEnv.Nonce, targetEnv.KeyID, targetEnv.Algorithm)

	mustExec(t, fx.db, `
		insert into order_execution_actors (stage_id, executor_user_id_snapshot,
			username_snapshot, display_name_snapshot)
		values (?, ?, 'exec-race', 'Exec Race User')`,
		stageID, execUserID)

	return execUserID, orderID
}

func TestExecutionBeginConcurrentCASRace(t *testing.T) {
	fx := newReclaimFx(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	execUserID, orderID := seedExecBeginFixture(
		t, fx, "", "UPDATE shop.accounts SET balance = 10 WHERE id = 1", "dml", "update", "YR-20261125-900001",
	)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	views := make([]*AttemptView, 2)
	errs := make([]error, 2)
	actor := Actor{UserID: execUserID, Username: "exec-race"}

	for i := 0; i < 2; i++ {
		go func(slot int) {
			defer wg.Done()
			<-start
			views[slot], _, errs[slot] = fx.svc.Begin(ctx, BeginInput{
				OrderID:        orderID,
				Actor:          actor,
				ExpectVersion:  1,
				IdempotencyKey: "exec-cas-race-" + uuid.NewString()[:8],
			})
		}(i)
	}
	close(start)
	wg.Wait()

	successes := 0
	var loserErr error
	for i := 0; i < 2; i++ {
		if errs[i] == nil && views[i] != nil {
			successes++
		} else {
			loserErr = errs[i]
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 Begin winner, got %d (err0=%v err1=%v)", successes, errs[0], errs[1])
	}
	var derr *domain.Error
	if !errors.As(loserErr, &derr) || (derr.Code != domain.CodeConcurrent && derr.Code != domain.CodeAlreadyStarted && derr.Code != domain.ErrIllegal().Code) {
		t.Fatalf("expected 1004/3005/1010 for losing Begin caller, got %v", loserErr)
	}

	var attemptCount, taskCount int64
	fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attemptCount)
	fx.db.Raw(`select count(*) from async_tasks where kind = ?`, KindStageExecution).Scan(&taskCount)
	if attemptCount != 1 || taskCount != 1 {
		t.Fatalf("expected single execution attempt and task, got attempts=%d tasks=%d", attemptCount, taskCount)
	}
	if state := fx.orderState(t, orderID); state != "running" {
		t.Fatalf("order state = %q, want running", state)
	}
}

func TestExecutionBeginClassBOSCFailClosedIntegration(t *testing.T) {
	fx := newReclaimFx(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	execUserID, orderID := seedExecBeginFixture(
		t, fx, `{"detected_flavor":"tidb","verification_tier":"class_b"}`,
		"ALTER TABLE shop.accounts ADD COLUMN tag VARCHAR(16)", "ddl", "alter", "YR-20261125-900002",
	)

	view, replay, err := fx.svc.Begin(ctx, BeginInput{
		OrderID:          orderID,
		Actor:            Actor{UserID: execUserID, Username: "exec-race"},
		ExpectVersion:    1,
		DDLExecutionMode: "gh_ost",
		IdempotencyKey:   "exec-osc-failclosed-" + uuid.NewString()[:8],
	})
	if view != nil || replay {
		t.Fatalf("expected nil view and no replay on osc_unsupported_target, got view=%v replay=%v", view, replay)
	}
	var derr *domain.Error
	if !errors.As(err, &derr) || derr.Code != domain.CodePreflightFailed || derr.Name != "EXECUTION_PREFLIGHT_FAILED:osc_unsupported_target" {
		t.Fatalf("expected ErrPreflightFailed(osc_unsupported_target), got %v", err)
	}

	var attemptCount, taskCount int64
	fx.db.Raw(`select count(*) from execution_attempts where order_id = ?`, orderID).Scan(&attemptCount)
	fx.db.Raw(`select count(*) from async_tasks where kind = ?`, KindStageExecution).Scan(&taskCount)
	if attemptCount != 0 || taskCount != 0 {
		t.Fatalf("fail-closed preflight must persist 0 attempts/tasks, got attempts=%d tasks=%d", attemptCount, taskCount)
	}
	if state := fx.orderState(t, orderID); state != "stage_execution_pending" {
		t.Fatalf("order state = %q, want stage_execution_pending", state)
	}
}
