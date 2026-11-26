// begin.go begin_stage_execution command: frozen executor clicks execute -> Preflight ->
// create Attempt + Fence + statement ledger -> order -> running -> enqueue durable task.
// Atomic within the same transaction: Attempt / ledger / order / stage / event / Outbox / task
// (safe pre-send-boundary transaction; outbox insert failure rolls back the entire transaction = zero external sends).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/osc"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// KindStageExecution is the execution task kind (migration 000010 extends the CHECK enum).
const KindStageExecution = "stage_execution"

// AggregateTypeExecutionAttempt is the aggregate_type for stage_execution tasks;
// the getTask read path uses it to identify execution progress tasks and resolve Attempt executor relations
// (RCP-20261021-GETTASK-EXECUTOR-READ: single source of truth across read and write sides).
const AggregateTypeExecutionAttempt = "execution_attempt"

// TaskInput holds the input payload for a stage_execution task.
type TaskInput struct {
	AttemptID string `json:"attempt_id"`
	OrderID   string `json:"order_id"`
	StageID   string `json:"stage_id"`
}

// BeginInput holds the parameters when an executor initiates execution.
type BeginInput struct {
	OrderID        string
	Actor          Actor
	ExpectVersion  int64
	IdempotencyKey string
	// OscOverrides holds typed OSC tuning keys submitted by the executor (RCP-20260827;
	// merged with settings defaults before server-side argv assembly; unknown keys are rejected with 1001).
	OscOverrides map[string]any
	// DDLExecutionMode allows the executor to choose between "gh_ost" and "online_ddl" per attempt.
	DDLExecutionMode string
}

// orderRow is the locked read projection of change_orders.
type orderRow struct {
	ID                 string     `gorm:"column:id"`
	State              string     `gorm:"column:state"`
	CurrentStage       *int32     `gorm:"column:current_stage_position"`
	SQLHMAC            string     `gorm:"column:sql_hmac"`
	ManuallyVerif      bool       `gorm:"column:manually_verified"`
	Version            int64      `gorm:"column:version"`
	TerminalAt         *time.Time `gorm:"column:terminal_at"`
	TicketType         string     `gorm:"column:ticket_type"`
	SourceOrderID      *string    `gorm:"column:source_order_id"`
	RollbackEvidenceID *string    `gorm:"column:rollback_evidence_id"`
}

func (s *Services) lockOrder(ctx context.Context, tx *gorm.DB, orderID string) (*orderRow, error) {
	var o orderRow
	err := tx.WithContext(ctx).Raw(`
		select id::text, state, current_stage_position, sql_hmac, manually_verified,
		       version, terminal_at, ticket_type,
		       source_order_id::text as source_order_id,
		       rollback_evidence_id::text as rollback_evidence_id
		from change_orders where id = ? for update`, orderID).Scan(&o).Error
	if err != nil {
		return nil, err
	}
	if o.ID == "" {
		return nil, domain.ErrNotFound()
	}
	return &o, nil
}

// updateOrderVersionedRaw updates the work order with optimistic locking (WHERE id AND version).
func (s *Services) updateOrderVersionedRaw(ctx context.Context, tx *gorm.DB,
	orderID string, expectVersion int64, sets map[string]any) error {
	res := tx.WithContext(ctx).Exec(
		`update change_orders set `+setClause(sets)+` where id = ? and version = ?`,
		append(setValues(sets), orderID, expectVersion)...)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrConcurrent()
	}
	return nil
}

// touchStage updates the stage state with optimistic locking (RowsAffected must be checked).
func (s *Services) touchStage(ctx context.Context, tx *gorm.DB,
	stageID string, expectVersion int64, state string) error {
	res := tx.WithContext(ctx).Exec(
		`update order_stages set state = ?, version = version + 1 where id = ? and version = ?`,
		state, stageID, expectVersion)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrConcurrent()
	}
	return nil
}

// Begin initiates stage execution as a frozen executor (idempotent; returns Attempt view).
func (s *Services) Begin(ctx context.Context, in BeginInput) (view *AttemptView, replayed bool, err error) {
	route := "POST:/change-orders/{order_id}/execution-attempts"
	// osc_overrides participates in the request hash: same key with different overrides = different request
	// (1007 rather than silently replaying the first response).
	hashParts := []string{in.Actor.UserID, in.OrderID, canonicalOverrides(in.OscOverrides)}
	if in.DDLExecutionMode != "" {
		hashParts = append(hashParts, in.DDLExecutionMode)
	}
	reqHash := txutil.RequestHash(hashParts...)
	return txutil.RunIdempotent(ctx, s.idemStore(), in.Actor.UserID, route, in.OrderID, in.IdempotencyKey, reqHash, func() (*AttemptView, error) {
		// Expired lease reconciliation commits first in an independent transaction: reconciliation
		// progression (not_started -> failed / sent -> result_unknown + order transition) must not roll back
		// with a 1003 in begin (otherwise retriers would be stuck in a reconcile-conflict loop forever).
		if rerr := s.reconcileExpiredStage(ctx, in.OrderID); rerr != nil {
			return nil, rerr
		}
		var out *AttemptView
		var committedFailure error
		txErr := s.serializable(ctx, func(tx *gorm.DB) error {
			committedFailure = nil
			v, berr := s.beginOnce(ctx, tx, in)
			if berr != nil {
				if isPostgresSearchPathFailure(deName(berr)) {
					if ferr := s.failOrderAfterPostgresSearchPathPreflightTx(
						ctx, tx, in, deName(berr),
					); ferr != nil {
						return ferr
					}
					committedFailure = berr
					return nil
				}
				return berr
			}
			out = v
			return nil
		})
		if txErr != nil {
			var de *domain.Error
			if errors.As(txErr, &de) {
				// Previously, preflight failures left no trace (no Attempt, no audit), making diagnosis
				// impossible for both executors and admins (BUG-006); record an audit event here.
				if de.Code == domain.CodePreflightFailed {
					reason := de.Name
					if i := strings.Index(reason, ":"); i >= 0 {
						reason = reason[i+1:]
					}
					_ = s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
						EventType: "execution.preflight_failed", ActorKind: "user",
						ActorID: &in.Actor.UserID, ActorUsername: &in.Actor.Username,
						ResourceType: "change_order", ResourceID: &in.OrderID,
						Action: "begin_stage_execution", Outcome: "failed",
						Metadata: map[string]any{"reason": reason},
					})
				}
			}
			return nil, txErr
		}
		if committedFailure != nil {
			if de := domainError(committedFailure); de != nil &&
				de.Code == domain.CodePreflightFailed {
				_ = s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
					EventType: "execution.preflight_failed", ActorKind: "user",
					ActorID: &in.Actor.UserID, ActorUsername: &in.Actor.Username,
					ResourceType: "change_order", ResourceID: &in.OrderID,
					Action: "begin_stage_execution", Outcome: "failed",
					Metadata: map[string]any{
						"reason": postgresSearchPathFailureReason(de.Name),
					},
				})
			}
			return nil, committedFailure
		}
		// Post-commit in-process wakeup so idle stage_execution workers claim immediately without waiting for backoff.
		tasks.WakeKind(KindStageExecution)
		return out, nil
	})
}

func deName(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Name
	}
	return ""
}

func domainError(err error) *domain.Error {
	var de *domain.Error
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// beginOnce executes a single serializable attempt (expired lease reconciliation was already
// completed in an independent transaction by reconcileExpiredStage).
func (s *Services) beginOnce(ctx context.Context, tx *gorm.DB, in BeginInput) (*AttemptView, error) {
	order, err := s.lockOrder(ctx, tx, in.OrderID)
	if err != nil {
		return nil, err
	}
	if in.ExpectVersion != order.Version {
		return nil, domain.ErrConcurrent()
	}
	stage, err := s.loadOrderStage(ctx, tx, order.ID)
	if err != nil {
		return nil, err
	}
	// Frozen executor eligibility (W006; admins are not exempt).
	if !s.isFrozenExecutor(ctx, tx, stage.StageID, in.Actor.UserID) {
		return nil, domain.ErrNotFrozen()
	}
	// State and retry preconditions (E004): Attempts that crossed the send boundary take precedence
	// and are rejected with 3004 (result_unknown is both live and sent, so retry semantics must be RETRY_FORBIDDEN).
	live, sent, lerr := s.stageAttemptFacts(ctx, tx, stage.StageID)
	if lerr != nil {
		return nil, lerr
	}
	if sent > 0 {
		return nil, domain.ErrRetryForbidden()
	}
	if live > 0 {
		return nil, domain.ErrAlreadyStarted()
	}
	var nextOrderState string
	retry := false
	switch orderdomain.OrderState(order.State) {
	case orderdomain.StateStageExecution:
		next, terr := orderdomain.OrderTransition(orderdomain.StateStageExecution, orderdomain.ActionExecute)
		if terr != nil {
			return nil, domain.ErrIllegal()
		}
		nextOrderState = string(next)
	case orderdomain.StateRunning:
		// Re-clicking on the same order after a not_started failure: the order stays in running and only
		// a new Attempt is created (the order state machine has no running -> execution_pending edge; retrying does not transition order state).
		nextOrderState = order.State
		retry = true
	default:
		return nil, domain.ErrIllegal()
	}
	return s.beginCore(ctx, tx, order, stage, in, beginCoreOpts{
		nextOrderState: nextOrderState, retry: retry, reason: "execution_started",
	})
}

// beginCoreOpts holds shared execution core parameters (used by both immediate click and due-schedule claim paths).
type beginCoreOpts struct {
	nextOrderState string
	retry          bool
	reason         string
}

// beginCore Preflight -> Attempt + ledger + task creation -> order/stage progression
// (shared by immediate execution and due-schedule claim; safe pre-send-boundary transaction).
func (s *Services) beginCore(ctx context.Context, tx *gorm.DB, order *orderRow, stage *targetSnapshotRow,
	in BeginInput, opts beginCoreOpts) (*AttemptView, error) {
	// ---- Preflight (all read-only; failure = command fails without persisting an Attempt) ----
	if in.DDLExecutionMode != "" && in.DDLExecutionMode != "gh_ost" && in.DDLExecutionMode != "online_ddl" {
		return nil, domain.ErrValidation()
	}
	overrides, oerr := validateOscOverrides(in.OscOverrides)
	if oerr != nil {
		return nil, oerr
	}
	if werr := s.checkExecutionWindow(ctx, tx); werr != nil {
		return nil, werr
	}
	ds, err := s.loadDatasource(ctx, tx, stage.DatasourceID)
	if err != nil {
		return nil, err
	}
	if terr := s.checkTarget(ctx, tx, ds, stage.IdentityFingerprint); terr != nil {
		return nil, terr
	}
	plan, err := s.freezeStatementPlan(ctx, tx, order.ID, stage.StageID, ds)
	if err != nil {
		return nil, err
	}
	if in.DDLExecutionMode == "gh_ost" && !oscCapable(ds) {
		return nil, domain.ErrPreflightFailed("osc_unsupported_target")
	}
	if in.DDLExecutionMode == "online_ddl" && plan.Kind.IsOscKind() {
		plan.Kind = domain.KindDDL
	}
	snap, err := s.openTargetSnapshot(stage)
	if err != nil {
		return nil, err
	}
	schema, err := s.resolveStageSchema(ctx, tx, order.ID, stage, snap, ds, plan.Statements...)
	if err != nil {
		return nil, err
	}
	if schema.PostgresPath != nil {
		pool, err := s.openPool(ctx, ds)
		if err != nil {
			return nil, domain.ErrPreflightFailed("postgres_search_path_unavailable")
		}
		verifyErr := s.verifyResolvedStageSearchPath(ctx, pool, schema)
		closeErr := pool.Close()
		if verifyErr != nil {
			return nil, verifyErr
		}
		if closeErr != nil {
			return nil, domain.ErrPreflightFailed("postgres_search_path_unavailable")
		}
	}
	if plan.Kind.IsOscKind() && (s.Osc == nil || s.oscBinaryFor(plan.Kind) == nil) {
		// Policy requires OSC, but process Adapter or pinned binary is unavailable: fail closed
		// without falling back to direct DDL (fallback forbidden).
		return nil, domain.ErrGhOstUnsupported()
	}
	now := nowUTC()
	leaseExp := now.Add(LeaseDuration)
	attemptID := newID()
	attempt := &persistence.ExecutionAttemptModel{
		ID: attemptID, OrderID: order.ID, StageID: stage.StageID,
		InitiatedByUserID: in.Actor.UserID, InitiatedByUsername: in.Actor.Username,
		ExecutionKind: string(plan.Kind), State: string(domain.AttemptRunning),
		SendBoundary: string(domain.BoundaryNotStarted), FenceToken: 1,
		LeaseOwner: ptr.To(s.instanceOwner()), LeaseExpiresAt: &leaseExp,
		StartedAt: &now, Version: 1, CreatedAt: now,
	}
	if plan.Kind.IsOscKind() {
		if oerr := s.sealOscPlan(ctx, tx, attempt, plan, ds, schema.Schema, overrides); oerr != nil {
			return nil, oerr
		}
	}
	if err := tx.WithContext(ctx).Create(attempt).Error; err != nil {
		if txutil.IsUniqueViolation(err) {
			return nil, domain.ErrAlreadyStarted()
		}
		return nil, err
	}
	// Statement ledger (frozen plan: ordinal + kind; raw SQL is not persisted).
	for _, st := range plan.Statements {
		if err := tx.WithContext(ctx).Create(&persistence.ExecutionStatementModel{
			ID: newID(), AttemptID: attemptID, Ordinal: st.Ordinal,
			StatementKind: st.Kind, State: string(domain.StmtNotStarted),
		}).Error; err != nil {
			return nil, err
		}
	}
	// Stage -> running; advance order (retry only advances version).
	if uerr := s.touchStage(ctx, tx, stage.StageID, stage.Version, string(orderdomain.StageRunning)); uerr != nil {
		return nil, uerr
	}
	nextVersion := order.Version + 1
	sets := map[string]any{"version": nextVersion}
	if !opts.retry {
		sets["state"] = opts.nextOrderState
	}
	if uerr := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version, sets); uerr != nil {
		return nil, uerr
	}
	reason := opts.reason
	if eerr := s.emitOrderEvent(ctx, tx, EventExecStarted, order.ID, auditlog.UserActor(in.Actor.UserID, in.Actor.Username), StateChangedPayload{
		AggregateID: order.ID, From: ptr.To(order.State), To: opts.nextOrderState,
		ReasonCode: reason, StageID: ptr.To(stage.StageID), AggregateVersion: nextVersion,
	}); eerr != nil {
		return nil, eerr
	}
	// Durable Job (same transaction; recovered after crash by sweep adjudicating on the send boundary).
	input, err := json.Marshal(TaskInput{AttemptID: attemptID, OrderID: order.ID, StageID: stage.StageID})
	if err != nil {
		return nil, err
	}
	aggType := AggregateTypeExecutionAttempt
	if err := tasks.Enqueue(ctx, tx, &tasks.AsyncTaskModel{
		ID: newID(), Kind: KindStageExecution, State: "queued",
		AggregateType: &aggType, AggregateID: &attemptID,
		Input: input, ProgressUnit: "statements", ProgressTotal: int64(len(plan.Statements)),
		FenceToken: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		if errors.Is(err, tasks.ErrQueueFull) {
			return nil, domain.ErrSystemCapacity()
		}
		return nil, err
	}
	return s.buildAttemptView(ctx, tx, attemptID)
}

// sealOscPlan generates and seals the OSC plan (AEAD; contains no password; typed overrides
// are merged with settings defaults before entering the final argv; columns satisfy the execution_attempt_osc_fields CHECK constraint).
func (s *Services) sealOscPlan(ctx context.Context, tx *gorm.DB, at *persistence.ExecutionAttemptModel,
	plan *planResult, ds *datasource.DatasourceModel, schema string, overrides *osc.Overrides) error {
	stmt := plan.Statements[0]
	dbName, table, alter, err := osc.ParseAlterTable(string(stmt.SQL), schema)
	if err != nil {
		return domain.ErrPreflightFailed("osc_alter_unparseable")
	}
	user, err := s.execCredentialUser(ctx, tx, ds.ID)
	if err != nil {
		return err
	}
	dir := filepath.Join(oscAttemptRoot, at.ID)
	policy := s.loadOscPolicy(ctx, tx)
	effective := osc.Merge(policy.Defaults, overrides)
	var argv []string
	if domain.ExecutionKind(at.ExecutionKind) == domain.KindPtOsc {
		argv, err = osc.BuildPtOscArgv(osc.PlanInput{
			Database: dbName, Table: table, Alter: alter,
			Host: ds.Host, Port: ds.Port, User: user,
		}, effective)
	} else {
		argv, _, err = osc.BuildGhOstArgv(osc.PlanInput{
			Database: dbName, Table: table, Alter: alter,
			Host: ds.Host, Port: ds.Port, User: user,
		}, dir, effective)
	}
	if err != nil {
		return domain.ErrPreflightFailed("osc_plan_invalid")
	}
	planJSON, err := json.Marshal(map[string]any{"argv": argv, "dir": dir,
		"database": dbName, "table": table, "tool": at.ExecutionKind})
	if err != nil {
		return err
	}
	env, err := s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "execution_attempts",
		PrimaryKey: at.ID,
		Column:     "osc_plan_ciphertext",
	}, planJSON)
	if err != nil {
		return err
	}
	bin := s.oscBinaryFor(domain.ExecutionKind(at.ExecutionKind))
	if bin == nil {
		return domain.ErrGhOstUnsupported()
	}
	progress0, rows0 := 0, int64(0)
	at.OSCToolVersion = ptr.To(bin.Version)
	at.OSCBinarySHA256 = ptr.To(bin.SHA256)
	at.OSCPlanHash = ptr.To(osc.PlanHash(argv, dir))
	at.OSCPlanCiphertext = env.Ciphertext
	at.OSCPlanNonce = env.Nonce
	at.OSCPlanKeyID = ptr.To(env.KeyID)
	at.OSCPlanAlgorithm = ptr.To(env.Algorithm)
	at.OSCPhase = ptr.To(osc.PhasePlanned)
	at.OSCProgressBasisPoints = &progress0
	at.OSCRowsCopied = &rows0
	at.OSCResidualState = ptr.To(osc.ResidualNotChecked)
	return nil
}

// execCredentialUser decrypts the execution credential to extract only the username (the password
// is passed over stdin pipe only at spawn time and never enters the plan/argv/env).
func (s *Services) execCredentialUser(ctx context.Context, tx *gorm.DB, dsID string) (string, error) {
	var cred datasource.DatasourceCredentialModel
	if err := tx.WithContext(ctx).Where(
		"datasource_id = ? and purpose = 'execution' and enabled", dsID).First(&cred).Error; err != nil {
		return "", domain.ErrDatasourceDown()
	}
	plain, err := s.Crypter.OpenColumn(crypto.PurposeDatasourceCredential, crypto.ColumnRef{
		Table:      "datasource_credentials",
		PrimaryKey: cred.ID,
		Column:     "credential",
	}, crypto.Envelope{
		Ciphertext: cred.CredentialCiphertext, Nonce: cred.CredentialNonce,
		KeyID: cred.CredentialKeyID, Algorithm: cred.CredentialAlgorithm,
	})
	if err != nil {
		return "", domain.ErrDatasourceDown()
	}
	var payload struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil || payload.Username == "" {
		return "", domain.ErrDatasourceDown()
	}
	return payload.Username, nil
}

// oscAttemptRoot returns the per-Attempt isolated working directory root (0700 subdirectory created by Runner).
var oscAttemptRoot = filepath.Join(os.TempDir(), "yearning-osc")

// OscBinaryIdentity holds the pinned gh-ost binary coordinates (wired from the release manifest;
// tests inject a fake binary).
type OscBinaryIdentity struct {
	Version string
	SHA256  string
	Path    string
}

// oscBinaryFor returns the pinned binary coordinates for the given execution Kind (unwired = nil,
// failing closed during pre-spawn validation).
func (s *Services) oscBinaryFor(kind domain.ExecutionKind) *OscBinaryIdentity {
	tool := string(osc.ToolGhOst)
	if kind == domain.KindPtOsc {
		tool = string(osc.ToolPtOsc)
	}
	if s.OscBinaries == nil {
		return nil
	}
	return s.OscBinaries[tool]
}

// stageAttemptFacts returns (live Attempt count, send-boundary-crossed Attempt count).
func (s *Services) stageAttemptFacts(ctx context.Context, tx *gorm.DB, stageID string) (live, sent int64, err error) {
	var row struct {
		Live int64 `gorm:"column:live"`
		Sent int64 `gorm:"column:sent"`
	}
	if err = tx.WithContext(ctx).Raw(`
		select count(*) filter (where state in ('created','preflight','running','cancelling','result_unknown')) as live,
		       count(*) filter (where send_boundary <> 'not_started') as sent
		from execution_attempts where stage_id = ?`, stageID).Scan(&row).Error; err != nil {
		return
	}
	return row.Live, row.Sent, nil
}

// isFrozenExecutor reports whether userID is a member of the current stage's frozen executor snapshot.
func (s *Services) isFrozenExecutor(ctx context.Context, tx *gorm.DB, stageID, userID string) bool {
	var n int64
	if err := tx.WithContext(ctx).Raw(`
		select count(*) from order_execution_actors
		where stage_id = ? and executor_user_id_snapshot = ?`, stageID, userID).Scan(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// reconcileExpiredStage reconciles active Attempts with expired leases on the order's current stage
// (independent serializable transaction; outcome commits immediately):
// boundary=not_started -> failed (provably zero sends, allowing retry on the same order);
// otherwise -> result_unknown and advances the order (preventing another worker from reclaiming execution via the unique index).
// cancelling is not handled here (cancellation semantics belong uniformly to SweepCancelTimeouts / cancel watchdog).
func (s *Services) reconcileExpiredStage(ctx context.Context, orderID string) error {
	return s.serializable(ctx, func(tx *gorm.DB) error {
		order, err := s.lockOrder(ctx, tx, orderID)
		if err != nil {
			return err
		}
		stage, err := s.loadOrderStage(ctx, tx, order.ID)
		if err != nil {
			return err
		}
		var rows []persistence.ExecutionAttemptModel
		if err := tx.WithContext(ctx).Raw(`
			select id, order_id, stage_id, state, send_boundary, fence_token, version,
			       initiated_by_user_id_snapshot, initiated_by_username_snapshot
			from execution_attempts
			where stage_id = ? and state in ('preflight','running')
			  and lease_expires_at is not null and lease_expires_at < now()
			for update`, stage.StageID).Scan(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			at := &rows[i]
			now := nowUTC()
			if domain.SendBoundary(at.SendBoundary) == domain.BoundaryNotStarted {
				code := int(domain.CodeSystemCapacity)
				name := "WORKER_LOST_BEFORE_SEND"
				// Increment fence synchronously: once adjudicated dead, any fenced write from the old worker
				// (markBoundary / claim CAS / terminal write) immediately fails — "zero-send failure" likewise
				// seals off a second send by the old worker (fence_token_never_regresses).
				res := tx.WithContext(ctx).Exec(`
					update execution_attempts set state = ?, finished_at = ?,
					    business_err_code = ?, failure_name = ?,
					    lease_owner = null, lease_expires_at = null,
					    fence_token = fence_token + 1, version = version + 1
					where id = ? and version = ? and fence_token = ?`,
					string(domain.AttemptFailed), now, code, name, at.ID, at.Version, at.FenceToken)
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected != 1 {
					return domain.ErrConcurrent()
				}
				continue
			}
			// Lease lost after crossing send boundary: result_unknown; order -> result_unknown.
			actor := Actor{UserID: at.InitiatedByUserID, Username: at.InitiatedByUsername}
			if err := s.markResultUnknown(ctx, tx, at, "LEASE_LOST_AFTER_SEND", actor, order, stage); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReconcileExpiredAttempts performs global reconciliation of expired active Attempts (SEC-0022:
// called periodically by the scheduler, fixing the missing production caller defect). Clusters by
// work order and reuses reconcileExpiredStage (bounded batch, independent transaction per order);
// idempotent — already-terminal Attempts are skipped by in-row conditions, and errors are aggregated per order without blocking others.
func (s *Services) ReconcileExpiredAttempts(ctx context.Context) error {
	var orderIDs []string
	if err := s.DB.WithContext(ctx).Raw(`
		select distinct order_id::text from execution_attempts
		where state in ('preflight','running')
		  and lease_expires_at is not null and lease_expires_at < now()
		order by order_id
		limit ?`, sweepBatchLimit).Scan(&orderIDs).Error; err != nil {
		return err
	}
	var errs []error
	for _, orderID := range orderIDs {
		if err := s.reconcileExpiredStage(ctx, orderID); err != nil {
			errs = append(errs, fmt.Errorf("execution: reconcile order %s: %w", orderID, err))
		}
	}
	return errors.Join(errs...)
}

// canonicalOverrides canonicalizes override key-value pairs for idempotency hashing (sorted keys).
func canonicalOverrides(raw map[string]any) string {
	if len(raw) == 0 {
		return ""
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, raw[k]))
	}
	return strings.Join(parts, ";")
}

// setClause/setValues construct a deterministic SET clause (sorted keys; keys are controlled allowlisted column names).
func setClause(sets map[string]any) string {
	keys := make([]string, 0, len(sets))
	for k := range sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	clause := ""
	for _, k := range keys {
		if clause != "" {
			clause += ", "
		}
		clause += k + " = ?"
	}
	return clause
}

func setValues(sets map[string]any) []any {
	keys := make([]string, 0, len(sets))
	for k := range sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]any, 0, len(sets))
	for _, k := range keys {
		vals = append(vals, sets[k])
	}
	return vals
}
