// run_lifecycle.go holds Runner attempt lifecycle, lease, heartbeat, and reclaim logic.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/tasks"
	"gorm.io/gorm"
	"sync"
	"time"
)

// FaultHooks holds fault-injection switches for test builds (always nil in production;
// execution-failure-injection harness: fault_switches_are_test_build_only).
// A hook returning an error aborts RunAttempt with process-death semantics (Attempt stays
// running with its lease, leaving adjudication to expired-lease reconciliation).
type FaultHooks struct {
	// AfterAttemptPersist observation point after begin commits and before opening the target connection.
	AfterAttemptPersist func(attemptID string)
	// AfterTargetResolved observation point before reading the target connection, frozen path, and statement plan.
	// Tests can induce path drift after early probes pass to verify re-checking prior to send.
	AfterTargetResolved func(attemptID string)
	// AfterStatementPersist after persisting the Nth statement fact; returning an error simulates
	// process death (pod_kill_between_ddl_statements).
	AfterStatementPersist func(attemptID string, ordinal int32) error
	// AfterCommitConfirm after DML commit confirmation and before terminal state persistence;
	// returning an error simulates process death (dml_commit_confirmed_then_local_persist_fails).
	AfterCommitConfirm func(attemptID string) error
}

// Runner executes stage_execution tasks.
type Runner struct {
	svc   *Services
	hooks *FaultHooks

	mu      sync.Mutex
	actives map[string]*activeRun
}

// activeRun is the in-process running Attempt handle (for cancellation addressing).
type activeRun struct {
	Dir     string // gh-ost attempt directory (empty = direct connection execution)
	Pool    *database.PurposePool
	Adapter database.Adapter
	Cancel  context.CancelFunc
}

// NewRunner constructs a Runner.
func NewRunner(svc *Services) *Runner {
	return &Runner{svc: svc, actives: map[string]*activeRun{}}
}

// SetFaultHooks injects fault switches for testing.
func (r *Runner) SetFaultHooks(h *FaultHooks) { r.hooks = h }

// Execute implements the tasks.Handler-compatible signature (input is TaskInput JSON).
// SEC-0001: respects the task-level fenceGuard — if the task fence is lost (another worker
// reclaimed the async_tasks row), this worker is a zombie and must not touch the Attempt;
// beat reports statement progress back to async_tasks (progress_completed/total).
func (r *Runner) Execute(ctx context.Context, db *gorm.DB, raw json.RawMessage,
	fenceGuard func() error, beat func(completed, total int64)) error {
	var in TaskInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	if fenceGuard != nil {
		if err := fenceGuard(); err != nil {
			return err
		}
	}
	err := r.runAttempt(ctx, in, fenceGuard, beat)
	// domain.ErrConcurrent() constructs a new *domain.Error pointer each time without Is/Unwrap,
	// so errors.Is across two independently constructed pointers is always false — must check via
	// errors.As + Code (matching the existing pattern in schedule.go).
	var de *domain.Error
	if err != nil && errors.As(err, &de) && de.Code == domain.CodeConcurrent {
		// Claim/fence contention (Attempt lease is still held by an active worker or adjudicated dead
		// by reconciliation): propagate with crash semantics — the task does not enter a terminal state
		// and converges via reclaim after lease expiry; calling FailBusiness here would misclassify
		// concurrency contention as a task failure and block subsequent reconciliation reclaim
		// (worker.RunOnce checks ErrWorkerCrashed before FailBusiness).
		return fmt.Errorf("%w: attempt claim contested: %v", tasks.ErrWorkerCrashed, err)
	}
	return err
}

// RunAttempt executes a single Attempt to its terminal state (idempotent: returns immediately if already terminal).
func (r *Runner) RunAttempt(ctx context.Context, in TaskInput) error {
	return r.runAttempt(ctx, in, nil, nil)
}

// reclaimAction is the adjudication outcome for a task reclaim (SEC-0001 core invariant).
type reclaimAction int

const (
	// reclaimExecute: send boundary not crossed and not cancelling — claim and execute normally.
	reclaimExecute reclaimAction = iota
	// reclaimResultUnknown: send boundary already crossed — never replay from ordinal 1;
	// adjudicate directly as result_unknown (manual verification path).
	reclaimResultUnknown
	// reclaimCancelFinalize: cancellation request arrived before any send and worker was lost —
	// provably zero sends, converge to cancelled terminal state.
	reclaimCancelFinalize
)

// adjudicateReclaim adjudicates reclaim handling from persisted facts: any Attempt with a
// boundary other than not_started that is driven again (worker died after send / new worker
// claimed after lease expiry / duplicate drive) uniformly becomes result_unknown, freezing the
// target DB evidence surface (pure function; covered by table-driven unit tests).
func adjudicateReclaim(state domain.AttemptState, boundary domain.SendBoundary) reclaimAction {
	if boundary != domain.BoundaryNotStarted {
		return reclaimResultUnknown
	}
	if state == domain.AttemptCancelling {
		return reclaimCancelFinalize
	}
	return reclaimExecute
}

// runAttempt execution core (shared by tasks.Execute and direct invocation; taskFence/beat may be nil).
func (r *Runner) runAttempt(ctx context.Context, in TaskInput,
	taskFence func() error, beat func(completed, total int64)) error {
	s := r.svc
	var at persistence.ExecutionAttemptModel
	if err := s.DB.WithContext(ctx).Where("id = ?", in.AttemptID).First(&at).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if domain.AttemptState(at.State).Terminal() {
		return nil
	}
	// SEC-0060: task payload must match the Attempt row binding (Attempt row and task row are
	// created in the same transaction by begin; mismatch = async_tasks.input was tampered with or
	// mislinked, which could execute order A's statements against order B's datasource). Fail closed:
	// do not claim, do not send, and write a forensic audit record.
	if in.OrderID != at.OrderID || in.StageID != at.StageID {
		s.auditTaskBindingMismatch(ctx, &at, in)
		return domain.ErrIllegal()
	}
	actor := Actor{UserID: at.InitiatedByUserID, Username: at.InitiatedByUsername}
	// SEC-0001(a): adjudicate by persisted send_boundary before claiming — never replay a reclaim
	// that has crossed the send boundary (a second external send = unrecoverable integrity violation).
	switch adjudicateReclaim(domain.AttemptState(at.State), domain.SendBoundary(at.SendBoundary)) {
	case reclaimResultUnknown:
		fence, cerr := s.claimAttempt(ctx, &at)
		if cerr != nil {
			return cerr
		}
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptResultUnknown, Boundary: domain.SendBoundary(at.SendBoundary),
			ReasonCode: int(domain.CodeResultUnknown), FailureName: "RECLAIM_AFTER_SEND",
		}, actor)
	case reclaimCancelFinalize:
		// Cancellation request arrived and zero sends are provable: converge directly to the
		// cancelled terminal state without opening a target connection (same semantics as the
		// not_started branch of SweepCancelTimeouts).
		fence, cerr := s.claimAttempt(ctx, &at)
		if cerr != nil {
			return cerr
		}
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptCancelled, Boundary: domain.BoundaryNotStarted,
			ReasonCode: int(domain.CodeIllegalTransition), FailureName: "EXECUTION_CANCELLED",
		}, actor)
	}
	fence, err := s.claimAttempt(ctx, &at)
	if err != nil {
		return err
	}
	// Lease heartbeat loop (stops when ctx ends; does not block return).
	hbCtx, stopHB := context.WithCancel(context.WithoutCancel(ctx))
	defer stopHB()
	go s.heartbeatLoop(hbCtx, at.ID, fence)
	defer func() {
		r.mu.Lock()
		delete(r.actives, at.ID)
		r.mu.Unlock()
	}()
	if r.hooks != nil && r.hooks.AfterAttemptPersist != nil {
		r.hooks.AfterAttemptPersist(at.ID)
	}
	switch domain.ExecutionKind(at.ExecutionKind) {
	case domain.KindDML:
		return r.runDML(ctx, in, &at, fence, actor, taskFence, beat)
	case domain.KindDDL:
		return r.runDDL(ctx, in, &at, fence, actor, taskFence, beat)
	case domain.KindGhOst, domain.KindPtOsc:
		return r.runOsc(ctx, in, &at, fence, actor, taskFence)
	default:
		return domain.ErrIllegal()
	}
}

// auditTaskBindingMismatch records a task payload binding mismatch audit event (fail-closed
// forensic surface; contains no SQL or secrets).
func (s *Services) auditTaskBindingMismatch(ctx context.Context, at *persistence.ExecutionAttemptModel, in TaskInput) {
	_ = s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "execution.task_binding_mismatch", ActorKind: "worker",
		ActorUsername: ptr.To(s.instanceOwner()),
		ResourceType:  "execution_attempt", ResourceID: ptr.To(at.ID),
		Action: "run_stage_execution", Outcome: "denied",
		Metadata: map[string]any{
			"task_order_id": in.OrderID, "task_stage_id": in.StageID,
			"attempt_order_id": at.OrderID, "attempt_stage_id": at.StageID,
		},
	})
}

// claimAttempt claims the Attempt via CAS: fence+1 + lease. SEC-0001(b) precondition: claimable
// only if "never claimed (fence=1, handoff from begin)", "held by this process", or "old lease
// expired" — task reclaim must never steal an Attempt lease still held by an active worker
// (CAS on the attempt's own fence alone is insufficient to prove task-level ownership).
// 0 rows = fence mismatch or precondition not met; abort execution.
func (s *Services) claimAttempt(ctx context.Context, at *persistence.ExecutionAttemptModel) (int64, error) {
	exp := nowUTC().Add(LeaseDuration)
	res := s.DB.WithContext(ctx).Exec(`
		update execution_attempts set fence_token = fence_token + 1,
		    lease_owner = ?, lease_expires_at = ?, version = version + 1
		where id = ? and fence_token = ?
		  and (fence_token = 1 or lease_owner = ? or lease_expires_at < ?)`,
		s.instanceOwner(), exp, at.ID, at.FenceToken, s.instanceOwner(), nowUTC())
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected != 1 {
		return 0, domain.ErrConcurrent()
	}
	at.FenceToken++
	at.Version++
	at.LeaseOwner = ptr.To(s.instanceOwner())
	return at.FenceToken, nil
}

// heartbeatLoop renews the lease (proving worker liveness within the lease window). Does not
// renew cancelling rows: when entering the cancellation channel, the cancellation initiator
// writes a bounded lease (withdraw linkage = now()+30s) that SweepCancelTimeouts adjudicates
// upon expiry — if cancellation is ineffective (missing target DB KILL privilege / unresponsive
// gh-ost / hung execution goroutine) while the heartbeat goroutine is alive, the Attempt must
// not stay stuck in cancelling indefinitely via lease renewal.
func (s *Services) heartbeatLoop(ctx context.Context, attemptID string, fence int64) {
	ticker := time.NewTicker(LeaseDuration / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.renewLease(ctx, attemptID, fence)
		}
	}
}

// renewLease performs a single lease renewal (called inline on each heartbeat tick). Hits 0 rows
// and does not renew for cancelling rows (see heartbeatLoop); dual fence+owner conditions on
// running rows prevent cross-worker false renewal.
func (s *Services) renewLease(ctx context.Context, attemptID string, fence int64) {
	_ = s.DB.WithContext(ctx).Exec(`
		update execution_attempts set lease_expires_at = ?
		where id = ? and fence_token = ? and lease_owner = ?
		  and state <> 'cancelling'`,
		nowUTC().Add(LeaseDuration), attemptID, fence, s.instanceOwner()).Error
}

// stageTarget holds the resolved stage execution target.
type stageTarget struct {
	Pool       *database.PurposePool
	Adapter    database.Adapter
	Schema     string
	SearchPath []string
	FrozenPath *reviewapp.FrozenPostgresSearchPath
	Binding    *reviewapp.FrozenPostgresPathBinding
	DS         *datasource.DatasourceModel
}

// openStageTarget resolves and opens the stage target (datasource row -> execution pool -> Schema).
// Locates the stage by the stageID carried in the task (determining the current stage of a
// multi-stage order is outside the Runner's responsibility).
func (s *Services) openStageTarget(ctx context.Context, in TaskInput) (*stageTarget, func(), error) {
	stage, err := s.loadStageByID(ctx, s.DB, in.StageID)
	if err != nil {
		return nil, nil, err
	}
	ds, err := s.loadDatasource(ctx, s.DB, stage.DatasourceID)
	if err != nil {
		return nil, nil, err
	}
	if !ds.Enabled {
		return nil, nil, domain.ErrDatasourceDown()
	}
	if ds.IdentityFingerprint == nil || *ds.IdentityFingerprint != stage.IdentityFingerprint {
		return nil, nil, domain.ErrPreflightFailed("datasource_identity_changed")
	}
	pool, err := s.openPool(ctx, ds)
	if err != nil {
		return nil, nil, err
	}
	snap, err := s.openTargetSnapshot(stage)
	if err != nil {
		_ = pool.Close()
		return nil, nil, err
	}
	resolution, err := s.resolveStageSchema(ctx, s.DB, in.OrderID, stage, snap, ds)
	if err != nil {
		_ = pool.Close()
		return nil, nil, err
	}
	if err := s.verifyResolvedStageSearchPath(ctx, pool, resolution); err != nil {
		_ = pool.Close()
		return nil, nil, err
	}
	var adapter database.Adapter
	ep := database.Endpoint{
		Host: ds.Host, Port: ds.Port,
		DatabaseName: ptr.Deref(ds.DatabaseName), CompatibilityMod: ds.CompatibilityMode,
	}
	if ds.CompatibilityMode == "mysql" {
		adapter = database.NewMySQLAdapter(ep, database.AdapterOptions{})
	} else {
		adapter = database.NewPostgreSQLAdapter(ep, database.AdapterOptions{})
	}
	cleanup := func() { _ = pool.Close() }
	return &stageTarget{
		Pool: pool, Adapter: adapter,
		Schema: resolution.Schema, SearchPath: resolution.SearchPath,
		FrozenPath: resolution.PostgresPath, Binding: resolution.Binding, DS: ds,
	}, cleanup, nil
}

func (s *Services) verifyResolvedStageSearchPath(
	ctx context.Context,
	pool *database.PurposePool,
	resolution stageSchemaResolution,
) error {
	if resolution.PostgresPath == nil {
		return nil
	}
	return s.verifyPostgresReviewContext(
		ctx, pool, *resolution.PostgresPath, resolution.Binding, 0,
	)
}

// markBoundary durably advances the send boundary: read-compare-advance (target >= current is
// an idempotent no-op; fence mismatch or 0-row CAS contention yields ErrConcurrent — intercepting
// stale workers adjudicated dead by reconciliation or a new worker, preventing a second send).
func (s *Services) markBoundary(ctx context.Context, attemptID string, fence int64, b domain.SendBoundary) error {
	var current string
	err := s.DB.WithContext(ctx).Raw(
		`select send_boundary from execution_attempts where id = ? and fence_token = ?`,
		attemptID, fence).Scan(&current).Error
	if err != nil || current == "" {
		return domain.ErrConcurrent()
	}
	if domain.SendBoundary(current).AtLeast(b) {
		// Idempotent no-op must likewise prove fence ownership: a stale worker adjudicated dead
		// after crossing the boundary (lease cleared by reconciliation or owner changed) must not
		// continue sending trailing statements — target DB state is frozen after result_unknown so
		// the manual verification evidence surface cannot drift.
		return s.checkFenceHeld(ctx, attemptID, fence)
	}
	res := s.DB.WithContext(ctx).Exec(`
		update execution_attempts set send_boundary = ?
		where id = ? and fence_token = ? and send_boundary = ?`,
		string(b), attemptID, fence, current)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrConcurrent()
	}
	return nil
}

// loadPlan re-derives the frozen statement texts (aligned by ordinal with the ledger).
func (s *Services) loadPlan(ctx context.Context, in TaskInput, ds *datasource.DatasourceModel) ([]PlanStatement, error) {
	plan, err := s.freezeStatementPlan(ctx, s.DB, in.OrderID, in.StageID, ds)
	if err != nil {
		return nil, err
	}
	var ledgerCount int64
	if err := s.DB.WithContext(ctx).Raw(
		`select count(*) from execution_statements where attempt_id = ?`, in.AttemptID).
		Scan(&ledgerCount).Error; err != nil {
		return nil, err
	}
	if int32(len(plan.Statements)) != int32(ledgerCount) {
		return nil, domain.ErrPreflightFailed("statement_count_mismatch")
	}
	return plan.Statements, nil
}

// checkFenceHeld verifies that the fence is still held by this instance (owner matches + lease valid);
// fails immediately for a stale worker.
func (s *Services) checkFenceHeld(ctx context.Context, attemptID string, fence int64) error {
	var owner string
	err := s.DB.WithContext(ctx).Raw(
		`select coalesce(lease_owner,'') from execution_attempts
		 where id = ? and fence_token = ? and lease_expires_at > now()`,
		attemptID, fence).Scan(&owner).Error
	if err != nil || owner != s.instanceOwner() {
		return domain.ErrConcurrent()
	}
	return nil
}

// sendGate external send gate (SEC-0001: dual gating on both task-level and Attempt-level fences) —
// losing the task fence (async_tasks reclaimed) makes this worker a zombie, which just like losing
// the Attempt lease must never send another byte.
func (r *Runner) sendGate(ctx context.Context, attemptID string, fence int64, taskFence func() error) error {
	if taskFence != nil {
		if err := taskFence(); err != nil {
			return err
		}
	}
	return r.svc.checkFenceHeld(ctx, attemptID, fence)
}

// reportBeat reports statement progress back to async_tasks (beat may be nil; direct invocation path has no task handle).
func reportBeat(beat func(completed, total int64), completed, total int) {
	if beat != nil {
		beat(int64(completed), int64(total))
	}
}

// loadLedgerStates reads the current states of the statement ledger (by ordinal; persisted
// fact source for SEC-0001(d) idempotent continuation).
func (s *Services) loadLedgerStates(ctx context.Context, attemptID string) (map[int32]domain.StatementState, error) {
	var rows []struct {
		Ordinal int32  `gorm:"column:ordinal"`
		State   string `gorm:"column:state"`
	}
	if err := s.DB.WithContext(ctx).Raw(
		`select ordinal, state from execution_statements where attempt_id = ?`, attemptID).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[int32]domain.StatementState, len(rows))
	for _, row := range rows {
		out[row.Ordinal] = domain.StatementState(row.State)
	}
	return out, nil
}

// attemptCancelling reports whether the Attempt has been set to cancelling
// (written by cancel command / watchdog; polled between statements).
func (s *Services) attemptCancelling(ctx context.Context, attemptID string) bool {
	var state string
	if err := s.DB.WithContext(ctx).Raw(
		`select state from execution_attempts where id = ?`, attemptID).
		Scan(&state).Error; err != nil {
		return false
	}
	return state == string(domain.AttemptCancelling)
}

// poolOpenFacts converts a target pool open failure into failed/not_started facts (provably zero sends).
func poolOpenFacts(err error) resultFacts {
	var de *domain.Error
	if errors.As(err, &de) {
		return resultFacts{
			Terminal: domain.AttemptFailed, Boundary: domain.BoundaryNotStarted,
			ReasonCode: int(de.Code), FailureName: de.Name,
		}
	}
	return resultFacts{
		Terminal: domain.AttemptFailed, Boundary: domain.BoundaryNotStarted,
		ReasonCode: int(domain.CodeDatasourceDown), FailureName: "pool_unreachable",
	}
}

// stmtFactOf converts an Adapter statement result into a ledger fact.
func stmtFactOf(ordinal int32, res *database.StatementResult) stmtFact {
	f := stmtFact{
		Ordinal:   ordinal,
		StartedAt: &res.StartedAt, FinishedAt: &res.FinishedAt,
	}
	switch res.Outcome {
	case database.OutcomeSuccess:
		f.State = domain.StmtSucceeded
		affected := res.AffectedRows
		f.AffectedRows = &affected
	case database.OutcomeFailed:
		f.State = domain.StmtFailed
		code := int(domain.CodeStatementRejected)
		name := res.SanitizedFailureName
		f.ErrCode = &code
		f.FailureName = &name
		// Ciphertext detail = classification summary (sanitized class name + engine category; raw driver text never leaves the adapter).
		f.Detail = []byte(`{"engine_code_class":"` + string(res.EngineCodeClass) +
			`","failure_name":"` + res.SanitizedFailureName + `"}`)
	case database.OutcomeCancelled:
		f.State = domain.StmtCancelled
	case database.OutcomeResultUnknown:
		f.State = domain.StmtUnknown
		code := int(domain.CodeResultUnknown)
		name := res.SanitizedFailureName
		f.ErrCode = &code
		f.FailureName = &name
		f.Detail = []byte(`{"engine_code_class":"` + string(res.EngineCodeClass) +
			`","failure_name":"` + res.SanitizedFailureName + `"}`)
	}
	return f
}

func rollbackConfirmed(rbRes *database.StatementResult, rerr error) bool {
	return rerr == nil && rbRes != nil && rbRes.Outcome != database.OutcomeResultUnknown
}

func classifyDMLFailureRollback(rbRes *database.StatementResult, rerr error) (domain.AttemptState, int, string) {
	if rollbackConfirmed(rbRes, rerr) {
		return domain.AttemptFailed, int(domain.CodeStatementRejected), "DML_STATEMENT_FAILED"
	}
	return domain.AttemptResultUnknown, int(domain.CodeResultUnknown), "ROLLBACK_AMBIGUOUS"
}

func classifyDMLCancelRollback(rbRes *database.StatementResult, rerr error) (domain.AttemptState, int, string) {
	if rollbackConfirmed(rbRes, rerr) {
		return domain.AttemptCancelled, int(domain.CodeIllegalTransition), "EXECUTION_CANCELLED"
	}
	return domain.AttemptResultUnknown, int(domain.CodeResultUnknown), "CANCEL_NOT_CONFIRMED"
}

func classifyDMLSearchPathFailureRollback(
	rbRes *database.StatementResult,
	rerr error,
	priorFacts int,
) (domain.AttemptState, domain.SendBoundary, int, string) {
	if !rollbackConfirmed(rbRes, rerr) {
		return domain.AttemptResultUnknown, domain.BoundarySent, int(domain.CodeResultUnknown), "ROLLBACK_AMBIGUOUS"
	}
	if priorFacts == 0 {
		return domain.AttemptFailed, domain.BoundaryNotStarted, int(domain.CodePreflightFailed), "postgres_search_path_changed"
	}
	return domain.AttemptFailed, domain.BoundarySent, int(domain.CodeStatementRejected), "postgres_search_path_changed"
}

func classifyDMLRollbackCapabilityOrConflict(
	rbRes *database.StatementResult,
	rerr error,
	code int,
	name string,
) (domain.AttemptState, domain.SendBoundary, int, string) {
	if !rollbackConfirmed(rbRes, rerr) {
		return domain.AttemptResultUnknown, domain.BoundarySent, int(domain.CodeResultUnknown), "ROLLBACK_AMBIGUOUS"
	}
	return domain.AttemptFailed, domain.BoundarySent, code, name
}

func buildDMLPreCommitAbortFacts(
	s *Services,
	rbRes *database.StatementResult,
	rerr error,
	lastOrdinal int32,
	reason string,
	facts []stmtFact,
) resultFacts {
	if !rollbackConfirmed(rbRes, rerr) {
		return resultFacts{
			Terminal:    domain.AttemptResultUnknown,
			Boundary:    domain.BoundarySent,
			ReasonCode:  int(domain.CodeResultUnknown),
			FailureName: "ROLLBACK_AMBIGUOUS",
			Statements:  facts,
		}
	}
	return s.rollbackUnsupportedFacts(lastOrdinal, domain.BoundarySent, reason, facts)
}

func (s *Services) abortDMLOnPreCommitRollbackError(
	ctx context.Context,
	execCtx context.Context,
	target *stageTarget,
	tx database.DMLTransaction,
	rbCtx *orderRollbackContext,
	attemptID string,
	fence int64,
	lastOrdinal int32,
	reason string,
	facts []stmtFact,
	actor Actor,
) error {
	rbRes, rerr := target.Adapter.RollbackDMLTransaction(execCtx, tx)
	_ = s.persistUnsupportedRollbackEvidence(ctx, rbCtx, "dml", reason)
	return s.applyResult(ctx, attemptID, fence, buildDMLPreCommitAbortFacts(s, rbRes, rerr, lastOrdinal, reason, facts), actor)
}

func (s *Services) ensureDMLTxSearchPath(
	execCtx context.Context,
	target *stageTarget,
	tx database.DMLTransaction,
	ordinal int32,
) error {
	if target.FrozenPath == nil {
		return nil
	}
	// A user-defined function or operator may mutate the transaction's
	// session setting. Reapply the frozen path and re-resolve this
	// statement's reviewed relations before every send.
	if err := database.ApplyTxSearchPath(execCtx, target.Pool, tx, target.SearchPath); err != nil {
		return err
	}
	return s.verifyPostgresReviewContextOnDMLTransaction(
		execCtx, target.Pool, tx, *target.FrozenPath, target.Binding, ordinal,
	)
}

func ddlBeatCompleted(priorSucceeded, factsCount int) int {
	return priorSucceeded + factsCount
}

func ddlNotSentFacts(
	previous domain.SendBoundary,
	err error,
) (domain.AttemptState, domain.SendBoundary, int, string, bool) {
	var notSent *database.StatementNotSentError
	if !errors.As(err, &notSent) {
		return "", "", 0, "", false
	}
	if previous == domain.BoundarySent {
		return domain.AttemptPartialFailed, domain.BoundarySent,
			int(domain.CodeStatementRejected), notSent.FailureName, true
	}
	return domain.AttemptFailed, domain.BoundaryNotStarted,
		int(domain.CodePreflightFailed), notSent.FailureName, true
}
