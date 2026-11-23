// run.go Execution Runner (stage_execution task Handler): executes against the real target
// database according to the frozen statement plan — single transaction for DML (E001), non-transactional
// serial execution for DDL (E002), and process delegation for gh-ost. Durably advances the send
// boundary (not_started -> sending -> sent); verifies the Attempt fence before writing terminal state;
// any ambiguous boundary -> result_unknown and is never replayed.
//
// Row-level business_err_code mapping (documented choices within the frozen error-codes.json vocabulary,
// as the vocabulary has no dedicated "statement engine failure/cancelled" code; see implementation-notes):
//   - Connection failure / Pool open failure / worker loss -> 3010 DATASOURCE_UNAVAILABLE
//   - Deterministic statement rejection (statement & Attempt rows) -> 2003 SQL_PARSE_FAILED
//   - result_unknown                                       -> 3005 EXECUTION_RESULT_UNKNOWN
//   - User cancellation                                    -> 1010 + EXECUTION_CANCELLED
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
	"github.com/cookieY/YearningX/backend/internal/osc"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/tasks"
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
		ActorUsername: strPtr(s.instanceOwner()),
		ResourceType:  "execution_attempt", ResourceID: strPtr(at.ID),
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
	at.LeaseOwner = strPtr(s.instanceOwner())
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
		DatabaseName: derefStr(ds.DatabaseName), CompatibilityMod: ds.CompatibilityMode,
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

// runDML E001: stage = single complete transaction; deterministic failure -> confirmed rollback -> failed;
// ambiguous commit -> result_unknown; cancellation -> rollback -> cancelled.
func (r *Runner) runDML(ctx context.Context, in TaskInput, at *persistence.ExecutionAttemptModel,
	fence int64, actor Actor, taskFence func() error, beat func(completed, total int64)) error {
	s := r.svc
	target, cleanup, err := s.openStageTarget(ctx, in)
	if err != nil {
		// Pre-connection failure: boundary=not_started (provably zero sends -> allows retry on the same order).
		return s.applyResult(ctx, in.AttemptID, fence, poolOpenFacts(err), actor)
	}
	defer cleanup()
	if r.hooks != nil && r.hooks.AfterTargetResolved != nil {
		r.hooks.AfterTargetResolved(at.ID)
	}
	// Cancellation observation: register active handle; cancel command triggers pool.CancelExecution.
	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	r.registerActive(at.ID, &activeRun{Pool: target.Pool, Adapter: target.Adapter, Cancel: cancelExec})
	defer r.unregisterActive(at.ID)
	cancelWatch := s.watchCancel(execCtx, at.ID, func() {
		_ = database.CancelInflightStatements(execCtx, target.Adapter, target.Pool)
	})
	defer cancelWatch()

	plan, err := s.loadPlan(ctx, in, target.DS)
	if err != nil {
		return err
	}
	rbCtx, err := s.loadOrderRollbackContext(ctx, in, target)
	if err != nil {
		return err
	}
	if rejFacts, err := s.precheckNormalOrderRollbackCapability(ctx, rbCtx, plan); err != nil {
		return err
	} else if rejFacts != nil {
		return s.applyResult(ctx, in.AttemptID, fence, *rejFacts, actor)
	}
	if rejFacts, err := s.prepareRollbackTicketExecution(ctx, rbCtx); err != nil {
		return err
	} else if rejFacts != nil {
		return s.applyResult(ctx, in.AttemptID, fence, *rejFacts, actor)
	}
	// SEC-0001(d): existing terminal statements in the ledger indicate a prior transaction with
	// ambiguous outcome — single-transaction DML semantics forbid partial continuation, so fail closed
	// as unknown without sending.
	ledger, err := s.loadLedgerStates(ctx, in.AttemptID)
	if err != nil {
		return err
	}
	tx, err := target.Adapter.BeginDMLTransaction(execCtx, target.Pool)
	if err != nil {
		// Connection timeout class: boundary=not_started (fault: connect_timeout_before_send).
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptFailed, Boundary: domain.BoundaryNotStarted,
			ReasonCode: int(domain.CodeDatasourceDown), FailureName: "connection_open_failed",
		}, actor)
	}
	if err := database.ApplyTxSearchPath(
		execCtx, target.Pool, tx, target.SearchPath,
	); err != nil {
		if _, rerr := tx.Rollback(execCtx); rerr != nil {
			_ = rerr
		}
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptFailed, Boundary: domain.BoundaryNotStarted,
			ReasonCode: int(domain.CodeDatasourceDown), FailureName: "schema_context_failed",
		}, actor)
	}
	// openStageTarget and begin/submit have already revalidated the path. Probe
	// again after plan loading and transaction setup so role/path drift during
	// those steps cannot cross the first change-statement send.
	if err := s.verifyResolvedStageSearchPath(ctx, target.Pool, stageSchemaResolution{
		PostgresPath: target.FrozenPath, Binding: target.Binding,
	}); err != nil {
		if _, rerr := tx.Rollback(execCtx); rerr != nil {
			_ = rerr
		}
		return s.applyResult(ctx, in.AttemptID, fence, poolOpenFacts(err), actor)
	}
	// Durable boundary advance: about to perform the first send.
	if err := s.markBoundary(ctx, in.AttemptID, fence, domain.BoundarySending); err != nil {
		return err
	}
	facts := []stmtFact{}
	capturedRollbackStmts := []rollback.StatementEvidence{}
	var totalRollbackRows int64
	terminal := domain.AttemptSucceeded
	boundary := domain.BoundarySending
	reasonCode, failureName := 0, ""
	skipSuffix := false
	for _, st := range plan {
		// Pre-send fence ownership check (dual task + Attempt gate; aborts immediately if the lease
		// was adjudicated dead by reconciliation or the task was reclaimed — DML has no per-statement
		// markBoundary gate, so this serves as the send gate).
		if ferr := r.sendGate(ctx, in.AttemptID, fence, taskFence); ferr != nil {
			return ferr
		}
		// Terminal statement in ledger (residual from a prior transaction): do not send, adjudicate as ambiguous.
		if ledger[st.Ordinal] != domain.StmtNotStarted {
			return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
				Terminal: domain.AttemptResultUnknown, Boundary: boundary,
				ReasonCode: int(domain.CodeResultUnknown), FailureName: "LEDGER_INCONSISTENT",
				Statements: facts,
			}, actor)
		}
		// Inter-statement cancellation check (PRD §5: stop sending subsequent statements; already-executed statements roll back with the transaction).
		if s.attemptCancelling(ctx, in.AttemptID) {
			rbRes, rerr := target.Adapter.RollbackDMLTransaction(execCtx, tx)
			terminal, reasonCode, failureName = classifyDMLCancelRollback(rbRes, rerr)
			boundary = domain.BoundarySent
			break
		}
		if err := s.ensureDMLTxSearchPath(execCtx, target, tx, st.Ordinal); err != nil {
			rbRes, rerr := target.Adapter.RollbackDMLTransaction(execCtx, tx)
			terminal, boundary, reasonCode, failureName = classifyDMLSearchPathFailureRollback(rbRes, rerr, len(facts))
			break
		}
		stmtEv, res, eerr := s.executeDMLStatementWithRollback(execCtx, target, tx, rbCtx, st)
		if eerr != nil {
			if isCap, isConf, reason := classifyRollbackDMLError(eerr); isCap || isConf {
				rbRes, rerr := target.Adapter.RollbackDMLTransaction(execCtx, tx)
				now := nowUTC()
				code := int(domain.CodeStatementRejected)
				name := failureRollbackUnsupported
				detailKey := "unsupported_reason"
				classKey := "rollback_capability_rejected"
				if isConf {
					name = failureRollbackConflict
					detailKey = "conflict_reason"
					classKey = "rollback_conflict_detected"
					_ = s.markSourceOrderRollbackConflict(ctx, rbCtx, reason)
				} else {
					_ = s.persistUnsupportedRollbackEvidence(ctx, rbCtx, "dml", reason)
				}
				detail, _ := json.Marshal(map[string]any{
					"engine_code_class": classKey,
					"failure_name":      name,
					detailKey:           reason,
				})
				fact := stmtFact{
					Ordinal:     st.Ordinal,
					State:       domain.StmtFailed,
					ErrCode:     &code,
					FailureName: &name,
					Detail:      detail,
					StartedAt:   &now,
					FinishedAt:  &now,
				}
				facts = append(facts, fact)
				_ = s.persistStatementFact(ctx, in.AttemptID, fence, fact)
				terminal, boundary, reasonCode, failureName = classifyDMLRollbackCapabilityOrConflict(rbRes, rerr, code, name)
				skipSuffix = true
				break
			}
			// Adapter error (non-statement outcome): treat as ambiguous.
			terminal, boundary = domain.AttemptResultUnknown, domain.BoundarySent
			reasonCode, failureName = int(domain.CodeResultUnknown), "DML_EXECUTION_AMBIGUOUS"
			break
		}
		if stmtEv != nil {
			capturedRollbackStmts = append(capturedRollbackStmts, *stmtEv)
			if res != nil {
				totalRollbackRows += res.AffectedRows
			}
		}
		fact := stmtFactOf(st.Ordinal, res)
		facts = append(facts, fact)
		_ = s.persistStatementFact(ctx, in.AttemptID, fence, fact)
		reportBeat(beat, len(facts), len(plan))
		if r.hooks != nil && r.hooks.AfterStatementPersist != nil {
			if herr := r.hooks.AfterStatementPersist(in.AttemptID, st.Ordinal); herr != nil {
				return herr
			}
		}
		switch res.Outcome {
		case database.OutcomeSuccess:
			// First response arrived: at least one statement has been handed to the target database.
			_ = s.markBoundary(ctx, in.AttemptID, fence, domain.BoundarySent)
			boundary = domain.BoundarySent
		case database.OutcomeFailed:
			// Deterministic failure: roll back and confirm (E001: whole-stage transaction rollback -> order failed).
			rbRes, rerr := target.Adapter.RollbackDMLTransaction(execCtx, tx)
			terminal, reasonCode, failureName = classifyDMLFailureRollback(rbRes, rerr)
			boundary = domain.BoundarySent
		case database.OutcomeCancelled:
			// Cancellation confirmed: roll back current transaction (previously committed stages are not rolled back).
			rbRes, rerr := target.Adapter.RollbackDMLTransaction(execCtx, tx)
			terminal, reasonCode, failureName = classifyDMLCancelRollback(rbRes, rerr)
			boundary = domain.BoundarySent
		case database.OutcomeResultUnknown:
			terminal, boundary = domain.AttemptResultUnknown, domain.BoundarySent
			reasonCode, failureName = int(domain.CodeResultUnknown), res.SanitizedFailureName
		}
		if terminal != domain.AttemptSucceeded {
			break
		}
	}
	if terminal == domain.AttemptSucceeded {
		// NEW-EXEC-01: re-check gates immediately before COMMIT, clamping the "final statement succeeded -> COMMIT" window.
		// Losing sendGate has the same semantics as between statements: abort immediately (the transaction
		// rolls back when the connection is reclaimed, leaving adjudication to reconciliation without local
		// terminalization); hitting cancellation (withdraw / watchdog terminalization landing inside this
		// window) has the same semantics as inter-statement cancellation: explicit rollback -> cancelled/sent,
		// or unconfirmed rollback -> result_unknown. Together they guarantee COMMIT never races ahead of cancellation.
		if ferr := r.sendGate(ctx, in.AttemptID, fence, taskFence); ferr != nil {
			return ferr
		}
		if s.attemptCancelling(ctx, in.AttemptID) {
			rbRes, rerr := target.Adapter.RollbackDMLTransaction(execCtx, tx)
			terminal, reasonCode, failureName = classifyDMLCancelRollback(rbRes, rerr)
			boundary = domain.BoundarySent
			_ = s.finalizeSourceOrderRollbackStatus(ctx, rbCtx, terminal)
			return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
				Terminal: terminal, Boundary: boundary,
				ReasonCode: reasonCode, FailureName: failureName,
				Statements: facts,
			}, actor)
		}
		// When rollback protection is enabled, pre-assemble and validate the rollback evidence bundle before committing the target DB.
		var preSealedEvID string
		var preSealedEnv crypto.Envelope
		var preSealedHMAC string
		if rbCtx != nil && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
			bundle := rollback.AssembleOrderEvidenceBundle("dml", capturedRollbackStmts)
			if strings.TrimSpace(bundle.RollbackSQL) == "" && len(capturedRollbackStmts) > 0 && totalRollbackRows > 0 {
				return s.abortDMLOnPreCommitRollbackError(
					ctx, execCtx, target, tx, rbCtx, in.AttemptID, fence,
					plan[len(plan)-1].Ordinal, "failed to generate deterministic rollback SQL for captured rows", facts, actor,
				)
			}
			preSealedEvID = newID()
			env, mac, serr := rollback.SealEvidenceBundle(s.Crypter, preSealedEvID, bundle)
			if serr != nil {
				return s.abortDMLOnPreCommitRollbackError(
					ctx, execCtx, target, tx, rbCtx, in.AttemptID, fence,
					plan[len(plan)-1].Ordinal, fmt.Sprintf("failed to seal rollback evidence before commit: %v", serr), facts, actor,
				)
			}
			preSealedEnv = env
			preSealedHMAC = mac
		}
		// All DML statements succeeded -> commit; ambiguous commit = classic result_unknown.
		cRes, cerr := target.Adapter.CommitDMLTransaction(execCtx, tx)
		if cerr != nil {
			if rbCtx != nil && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
				_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "dml", "target transaction commit outcome unknown")
			}
			_ = s.finalizeSourceOrderRollbackStatus(ctx, rbCtx, domain.AttemptResultUnknown)
			return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
				Terminal: domain.AttemptResultUnknown, Boundary: domain.BoundarySent,
				ReasonCode: int(domain.CodeResultUnknown), FailureName: "COMMIT_ACK_LOST",
				Statements: facts,
			}, actor)
		}
		if cRes.Outcome == database.OutcomeSuccess {
			if rbCtx != nil && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
				if perr := s.persistAvailableRollbackEvidence(
					ctx, rbCtx, preSealedEvID, "dml", preSealedEnv, preSealedHMAC, totalRollbackRows,
				); perr != nil {
					_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "dml", perr.Error())
				}
			}
			_ = s.finalizeSourceOrderRollbackStatus(ctx, rbCtx, domain.AttemptSucceeded)
			if r.hooks != nil && r.hooks.AfterCommitConfirm != nil {
				if herr := r.hooks.AfterCommitConfirm(in.AttemptID); herr != nil {
					return herr
				}
			}
			return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
				Terminal: domain.AttemptSucceeded, Boundary: domain.BoundarySent,
				Statements: facts,
			}, actor)
		}
		terminal, boundary = domain.AttemptResultUnknown, domain.BoundarySent
		reasonCode, failureName = int(domain.CodeResultUnknown), "COMMIT_ACK_LOST"
	}
	_ = s.finalizeSourceOrderRollbackStatus(ctx, rbCtx, terminal)
	return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
		Terminal: terminal, Boundary: boundary,
		ReasonCode: reasonCode, FailureName: failureName,
		Statements: facts, SkipSuffix: skipSuffix,
	}, actor)
}

// runDDL E002: non-transactional serial execution; first error stops trailing statements (skipped)
// while preserving prior successes -> partial_failed; cancellation maps to cancelled/partial_cancelled
// depending on whether any statement succeeded.
// SEC-0001(d): statements already marked succeeded in the ledger are idempotently skipped (never resent),
// while any other terminal ledger state fails closed as unknown due to fact contradiction.
func (r *Runner) runDDL(ctx context.Context, in TaskInput, at *persistence.ExecutionAttemptModel,
	fence int64, actor Actor, taskFence func() error, beat func(completed, total int64)) error {
	s := r.svc
	target, cleanup, err := s.openStageTarget(ctx, in)
	if err != nil {
		return s.applyResult(ctx, in.AttemptID, fence, poolOpenFacts(err), actor)
	}
	defer cleanup()
	if r.hooks != nil && r.hooks.AfterTargetResolved != nil {
		r.hooks.AfterTargetResolved(at.ID)
	}
	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	r.registerActive(at.ID, &activeRun{Pool: target.Pool, Adapter: target.Adapter, Cancel: cancelExec})
	defer r.unregisterActive(at.ID)
	cancelWatch := s.watchCancel(execCtx, at.ID, func() {
		_ = database.CancelInflightStatements(execCtx, target.Adapter, target.Pool)
	})
	defer cancelWatch()

	plan, err := s.loadPlan(ctx, in, target.DS)
	if err != nil {
		return err
	}
	rbCtx, err := s.loadOrderRollbackContext(ctx, in, target)
	if err != nil {
		return err
	}
	if rejFacts, err := s.precheckNormalOrderRollbackCapability(ctx, rbCtx, plan); err != nil {
		return err
	} else if rejFacts != nil {
		return s.applyResult(ctx, in.AttemptID, fence, *rejFacts, actor)
	}
	if rejFacts, err := s.prepareRollbackTicketExecution(ctx, rbCtx); err != nil {
		return err
	} else if rejFacts != nil {
		return s.applyResult(ctx, in.AttemptID, fence, *rejFacts, actor)
	}
	ledger, err := s.loadLedgerStates(ctx, in.AttemptID)
	if err != nil {
		return err
	}
	facts := []stmtFact{}
	capturedDDLStmts := []rollback.StatementEvidence{}
	var ddlPostExecErr error
	priorSucceeded := 0
	succeeded := 0
	terminal := domain.AttemptSucceeded
	boundary := domain.BoundaryNotStarted
	reasonCode, failureName := 0, ""
	stop := false
	searchPathRejected := false
	for _, st := range plan {
		if stop {
			break
		}
		switch ledger[st.Ordinal] {
		case domain.StmtSucceeded:
			// Statement already succeeded: fact confirmed by a prior run, skip idempotently without resending.
			priorSucceeded++
			succeeded++
			continue
		case domain.StmtNotStarted, "":
			// Normal send path.
		default:
			// Ledger is terminal while Attempt is non-terminal: fact contradiction, fail closed as unknown.
			return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
				Terminal: domain.AttemptResultUnknown, Boundary: boundary,
				ReasonCode: int(domain.CodeResultUnknown), FailureName: "LEDGER_INCONSISTENT",
				Statements: facts,
			}, actor)
		}
		// Pre-send fence ownership check (dual task + Attempt gate; while markBoundary's no-op
		// path already checks ownership, this explicitly gates statements before reaching the send boundary as well).
		if ferr := r.sendGate(ctx, in.AttemptID, fence, taskFence); ferr != nil {
			return ferr
		}
		// Inter-statement cancellation check (PRD §5: stop sending subsequent statements once a cancellation request is received).
		if s.attemptCancelling(ctx, in.AttemptID) {
			if succeeded > 0 {
				terminal = domain.AttemptPartialCancel
			} else {
				terminal = domain.AttemptCancelled
			}
			reasonCode, failureName = int(domain.CodeIllegalTransition), "EXECUTION_CANCELLED"
			stop = true
			break
		}
		if rbCtx != nil && rbCtx.TicketType == "rollback" {
			if stmtEv, ok := rbCtx.inverseTriggerByOrdinal[st.Ordinal]; ok {
				rbVerifyCtx := database.WithRollbackSearchPath(execCtx, target.SearchPath)
				if cerr := rollback.VerifyDDLStatementPrecondition(
					rbVerifyCtx, target.Pool, rbCtx.Engine, stmtEv,
				); cerr != nil {
					_, _, reason := classifyRollbackDMLError(cerr)
					if reason == "" {
						reason = cerr.Error()
					}
					_ = s.markSourceOrderRollbackConflict(ctx, rbCtx, reason)
					now := nowUTC()
					code := int(domain.CodeStatementRejected)
					name := failureRollbackConflict
					detail, _ := json.Marshal(map[string]any{
						"engine_code_class": "rollback_conflict_detected",
						"failure_name":      failureRollbackConflict,
						"conflict_reason":   reason,
					})
					fact := stmtFact{
						Ordinal:     st.Ordinal,
						State:       domain.StmtFailed,
						ErrCode:     &code,
						FailureName: &name,
						Detail:      detail,
						StartedAt:   &now,
						FinishedAt:  &now,
					}
					facts = append(facts, fact)
					_ = s.persistStatementFact(ctx, in.AttemptID, fence, fact)
					if succeeded > 0 {
						terminal, boundary = domain.AttemptPartialFailed, domain.BoundarySent
					} else {
						terminal, boundary = domain.AttemptFailed, domain.BoundaryNotStarted
					}
					reasonCode, failureName = code, name
					stop = true
					break
				}
			}
		}
		var ddlPreSnap *database.TableRollbackMetadata
		var ddlParsed *rollback.ParsedStatement
		if rbCtx != nil && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
			ddlParsed = rbCtx.parsedByOrdinal[st.Ordinal]
			if ddlParsed == nil {
				var perr error
				ddlParsed, perr = rollback.AnalyzeStatement(rbCtx.Engine, rbCtx.SchemaName, string(st.SQL))
				if perr != nil {
					_, _, reason := classifyRollbackDMLError(perr)
					if reason == "" {
						reason = perr.Error()
					}
					_ = s.persistUnsupportedRollbackEvidence(ctx, rbCtx, "ddl", reason)
					return s.applyResult(ctx, in.AttemptID, fence,
						s.rollbackUnsupportedFacts(st.Ordinal, boundary, reason, facts), actor)
				}
			}
			ddlSnapCtx := database.WithRollbackSearchPath(execCtx, target.SearchPath)
			preSnap, perr := rollback.PrepareDDLPreExecution(
				ddlSnapCtx, target.Pool, rbCtx.Engine, ddlParsed,
			)
			if preSnap != nil && preSnap.ServerVersion != "" {
				rbCtx.DatabaseVersion = preSnap.ServerVersion
				rbCtx.PGVersionNum = preSnap.ServerVersionNum
			}
			if perr != nil {
				_, _, reason := classifyRollbackDMLError(perr)
				if reason == "" {
					reason = perr.Error()
				}
				if succeeded == 0 {
					_ = s.persistUnsupportedRollbackEvidence(ctx, rbCtx, "ddl", reason)
				} else {
					_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", reason)
				}
				now := nowUTC()
				code := int(domain.CodeStatementRejected)
				name := failureRollbackUnsupported
				detail, _ := json.Marshal(map[string]any{
					"engine_code_class":  "rollback_capability_rejected",
					"failure_name":       failureRollbackUnsupported,
					"unsupported_reason": reason,
				})
				fact := stmtFact{
					Ordinal:     st.Ordinal,
					State:       domain.StmtFailed,
					ErrCode:     &code,
					FailureName: &name,
					Detail:      detail,
					StartedAt:   &now,
					FinishedAt:  &now,
				}
				facts = append(facts, fact)
				_ = s.persistStatementFact(ctx, in.AttemptID, fence, fact)
				if succeeded > 0 {
					terminal, boundary = domain.AttemptPartialFailed, domain.BoundarySent
				} else {
					terminal, boundary = domain.AttemptFailed, domain.BoundaryNotStarted
				}
				reasonCode, failureName = code, name
				stop = true
				break
			}
			ddlPreSnap = preSnap
		}
		var beforeSendErr, searchPathErr error
		res, eerr := database.ExecuteDDLWithSearchPathInspectedBeforeSend(
			execCtx, target.Adapter, target.Pool, target.SearchPath, string(st.SQL),
			func(beforeSendCtx context.Context, conn *sql.Conn) error {
				// Connection and SET search_path have succeeded. Revalidate the
				// exact connection immediately before the durable boundary
				// advances and SQL is handed to the driver.
				if target.FrozenPath != nil {
					// The dedicated connection has an explicit frozen path.
					// Probe the execution role's current default as well so a
					// role-level change after target resolution cannot be hidden
					// by that explicit SET.
					searchPathErr = s.verifyPostgresSearchPath(
						beforeSendCtx, target.Pool, *target.FrozenPath,
					)
					if searchPathErr != nil {
						return &database.StatementNotSentError{
							FailureName: "postgres_search_path_changed",
						}
					}
					searchPathErr = s.verifyPostgresReviewContextOnDDLConnection(
						beforeSendCtx, conn, *target.FrozenPath,
						target.Binding, st.Ordinal,
					)
				}
				if searchPathErr != nil {
					return &database.StatementNotSentError{
						FailureName: "postgres_search_path_changed",
					}
				}
				beforeSendErr = s.markBoundary(
					ctx, in.AttemptID, fence, domain.BoundarySending,
				)
				if beforeSendErr == nil && boundary == domain.BoundaryNotStarted {
					boundary = domain.BoundarySending
				}
				return beforeSendErr
			},
		)
		if beforeSendErr != nil {
			return beforeSendErr
		}
		if searchPathErr != nil {
			searchPathRejected = true
			notSent := &database.StatementNotSentError{
				FailureName: "postgres_search_path_changed",
			}
			terminal, boundary, reasonCode, failureName, _ =
				ddlNotSentFacts(boundary, notSent)
			stop = true
			break
		}
		if eerr != nil {
			if notSentTerminal, notSentBoundary, notSentReason,
				notSentName, ok := ddlNotSentFacts(boundary, eerr); ok {
				// The helper proves statementSQL was never handed to the
				// driver. With no prior success the stage remains retryable;
				// after a prior DDL success it is partial_failed and frozen.
				terminal, boundary = notSentTerminal, notSentBoundary
				reasonCode, failureName = notSentReason, notSentName
				stop = true
				break
			}
			terminal, boundary = domain.AttemptResultUnknown, domain.BoundarySent
			reasonCode, failureName = int(domain.CodeResultUnknown), "DDL_EXECUTION_AMBIGUOUS"
			break
		}
		fact := stmtFactOf(st.Ordinal, res)
		facts = append(facts, fact)
		_ = s.persistStatementFact(ctx, in.AttemptID, fence, fact)
		reportBeat(beat, ddlBeatCompleted(priorSucceeded, len(facts)), len(plan))
		if r.hooks != nil && r.hooks.AfterStatementPersist != nil {
			if herr := r.hooks.AfterStatementPersist(in.AttemptID, st.Ordinal); herr != nil {
				return herr
			}
		}
		switch res.Outcome {
		case database.OutcomeSuccess:
			succeeded++
			_ = s.markBoundary(ctx, in.AttemptID, fence, domain.BoundarySent)
			boundary = domain.BoundarySent
			if rbCtx != nil && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
				ddlSnapCtx := database.WithRollbackSearchPath(execCtx, target.SearchPath)
				ddlEv, ferr := rollback.FinalizeDDLPostExecution(
					ddlSnapCtx, target.Pool, rbCtx.Engine, int(st.Ordinal), ddlParsed, ddlPreSnap,
				)
				if ferr != nil {
					ddlPostExecErr = ferr
				} else if ddlEv != nil {
					capturedDDLStmts = append(capturedDDLStmts, *ddlEv)
				}
			}
		case database.OutcomeFailed:
			// First deterministic failure: stop trailing statements and preserve prior successes.
			terminal, boundary = domain.AttemptPartialFailed, domain.BoundarySent
			reasonCode = int(domain.CodeStatementRejected)
			failureName = "DDL_STATEMENT_FAILED"
			stop = true
		case database.OutcomeCancelled:
			if succeeded > 0 {
				terminal = domain.AttemptPartialCancel
			} else {
				terminal = domain.AttemptCancelled
			}
			boundary = domain.BoundarySent
			reasonCode = int(domain.CodeIllegalTransition)
			failureName = "EXECUTION_CANCELLED"
			stop = true
		case database.OutcomeResultUnknown:
			terminal, boundary = domain.AttemptResultUnknown, domain.BoundarySent
			reasonCode, failureName = int(domain.CodeResultUnknown), res.SanitizedFailureName
			stop = true
		}
	}
	if rbCtx != nil && rbCtx.RollbackEnabled && rbCtx.TicketType == "normal" {
		if terminal == domain.AttemptSucceeded && ddlPostExecErr == nil && len(capturedDDLStmts) == len(plan) {
			bundle := rollback.AssembleOrderEvidenceBundle("ddl", capturedDDLStmts)
			evID := newID()
			env, mac, serr := rollback.SealEvidenceBundle(s.Crypter, evID, bundle)
			if serr != nil {
				_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", serr.Error())
			} else if perr := s.persistAvailableRollbackEvidence(ctx, rbCtx, evID, "ddl", env, mac, 0); perr != nil {
				_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", perr.Error())
			}
		} else if ddlPostExecErr != nil {
			_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", ddlPostExecErr.Error())
		} else if succeeded > 0 && terminal != domain.AttemptSucceeded {
			_ = s.persistIncompleteRollbackEvidence(ctx, rbCtx, "ddl", "DDL execution did not complete all statements")
		}
	}
	_ = s.finalizeSourceOrderRollbackStatus(ctx, rbCtx, terminal)
	skipSuffix := stop && terminal != domain.AttemptResultUnknown
	if searchPathRejected && boundary == domain.BoundaryNotStarted {
		// The rejected statement and all later statements were never sent;
		// preserve them as not_started so the zero-send failure remains
		// distinguishable from a deterministic or cancelled suffix.
		skipSuffix = false
	}
	return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
		Terminal: terminal, Boundary: boundary,
		ReasonCode: reasonCode, FailureName: failureName,
		Statements: facts, SkipSuffix: skipSuffix,
	}, actor)
}

// runOsc gh-ost process delegation: unseal plan -> spawn -> persist heartbeat/progress ->
// classify exit (success / failure / cancelled / process lost -> result_unknown) + inspect residuals.
func (r *Runner) runOsc(ctx context.Context, in TaskInput, at *persistence.ExecutionAttemptModel,
	fence int64, actor Actor, taskFence func() error) error {
	s := r.svc
	kind := domain.ExecutionKind(at.ExecutionKind)
	tool := osc.ToolGhOst
	if kind == domain.KindPtOsc {
		tool = osc.ToolPtOsc
	}
	bin := s.oscBinaryFor(kind)
	if s.Osc == nil || bin == nil || bin.Path == "" {
		// Process Adapter unwired or binary coordinates unpinned: fail closed without spawning or downgrading.
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptFailed, Boundary: domain.BoundaryNotStarted,
			ReasonCode: int(domain.CodeGhOstUnsupported), FailureName: "OSC_BINARY_UNPINNED",
		}, actor)
	}
	// Unseal plan (AEAD; contains no password).
	var planDoc struct {
		Argv     []string `json:"argv"`
		Dir      string   `json:"dir"`
		Database string   `json:"database"`
		Table    string   `json:"table"`
	}
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: at.OSCPlanCiphertext, Nonce: at.OSCPlanNonce,
		KeyID: derefStr(at.OSCPlanKeyID), Algorithm: derefStr(at.OSCPlanAlgorithm),
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "execution_attempts",
		PrimaryKey: at.ID, Column: "osc_plan_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptFailed, Boundary: domain.BoundaryNotStarted,
			ReasonCode: int(domain.CodePreflightFailed), FailureName: "OSC_PLAN_UNREADABLE",
		}, actor)
	}
	if err := json.Unmarshal(plain, &planDoc); err != nil {
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptFailed, Boundary: domain.BoundaryNotStarted,
			ReasonCode: int(domain.CodePreflightFailed), FailureName: "OSC_PLAN_UNREADABLE",
		}, actor)
	}
	// Pre-spawn SSRF & replication topology guard (runs before rollback/send-boundary checks so
	// forbidden targets or unregistered replica primaries fail cleanly with BoundaryNotStarted).
	pinnedIP, assumeMaster, topoFailFacts, topoErr := s.preflightOscTopologyAndPin(ctx, in, at, tool, actor)
	if topoErr != nil {
		return topoErr
	}
	if topoFailFacts != nil {
		return s.applyResult(ctx, in.AttemptID, fence, *topoFailFacts, actor)
	}
	// Pre-spawn rollback capability / precondition check (runs before entering the sending boundary).
	rbCtx, ddlParsed, ddlPreMeta, rbFailFacts, rbErr := s.prepareOscRollbackBeforeSpawn(ctx, in, planDoc.Database)
	if rbErr != nil {
		return rbErr
	}
	if rbFailFacts != nil {
		return s.applyResult(ctx, in.AttemptID, fence, *rbFailFacts, actor)
	}
	// Pre-spawn task fence check (SEC-0001: spawning a process carrying a copy of the SQL = send boundary;
	// a zombie worker that lost its task fence must never spawn).
	if taskFence != nil {
		if err := taskFence(); err != nil {
			return err
		}
	}
	// Pre-spawn durable boundary: the process is about to start carrying a copy of the SQL.
	if err := s.markBoundary(ctx, in.AttemptID, fence, domain.BoundarySending); err != nil {
		return err
	}
	startedAt := nowUTC()
	_ = s.persistStatementFact(ctx, in.AttemptID, fence, stmtFact{
		Ordinal: 1, State: domain.StmtSent, StartedAt: &startedAt,
	})
	r.registerActive(at.ID, &activeRun{Dir: planDoc.Dir})
	defer r.unregisterActive(at.ID)
	// Decrypt the password only at this instant and discard it immediately after passing over the stdin pipe.
	password, err := s.execCredentialPassword(ctx, at)
	if err != nil {
		return err
	}
	inspector, poolCleanup := s.oscResidualInspector(ctx, in)
	if poolCleanup != nil {
		defer poolCleanup()
	}
	job := &osc.Job{
		BinaryPath: bin.Path, BinarySHA256: derefStr(at.OSCBinarySHA256),
		Tool: tool, Argv: rewriteArgvHost(planDoc.Argv, pinnedIP),
		PinnedHost: pinnedIP, AssumeMasterHostname: assumeMaster,
		Dir:      planDoc.Dir,
		Password: password, Inspector: inspector,
		Database: planDoc.Database, Table: planDoc.Table,
	}
	hooks := osc.Hooks{
		OnHeartbeat: func(t time.Time) { s.persistOscHeartbeat(ctx, at.ID, fence, t) },
		OnProgress: func(phase string, bps int, rows int64) {
			s.persistOscProgress(ctx, at.ID, fence, phase, bps, rows)
		},
	}
	outcome, rerr := s.Osc.Run(ctx, job, hooks)
	finishedAt := nowUTC()
	if rerr != nil {
		// Spawn failure (hash mismatch / unreadable binary): boundary stays at sending (already durably
		// entered the send phase, never regresses); OSC failure advances the order to the failed terminal
		// state — retry on the same order is forbidden (osc lifecycle.retry_or_resume_same_order=forbidden).
		s.finalizeOscRollbackAfterRun(ctx, in, rbCtx, ddlParsed, ddlPreMeta, domain.AttemptFailed)
		code := int(domain.CodeGhOstUnsupported)
		fname := "OSC_SPAWN_FAILED"
		return s.applyResult(ctx, in.AttemptID, fence, resultFacts{
			Terminal: domain.AttemptFailed, Boundary: domain.BoundarySending,
			ReasonCode: code, FailureName: fname,
			Statements: []stmtFact{{
				Ordinal: 1, State: domain.StmtFailed,
				StartedAt: &startedAt, FinishedAt: &finishedAt,
				ErrCode: &code, FailureName: &fname,
			}},
		}, actor)
	}
	facts := resultFacts{
		Boundary: domain.BoundarySent,
		Osc: &oscFact{
			Phase: outcome.Phase, ProgressBps: outcome.ProgressBps,
			RowsCopied: outcome.RowsCopied, Residual: outcome.ResidualState,
		},
	}
	stmtRow := stmtFact{
		Ordinal: 1, StartedAt: &startedAt, FinishedAt: &finishedAt,
		AffectedRows: &outcome.RowsCopied,
	}
	switch {
	case outcome.Phase == osc.PhaseSucceeded && outcome.ExitCode == 0 && !outcome.ProcessLost:
		facts.Terminal = domain.AttemptSucceeded
		stmtRow.State = domain.StmtSucceeded
	case outcome.Phase == osc.PhaseCancelled:
		facts.Terminal = domain.AttemptCancelled
		facts.ReasonCode = int(domain.CodeIllegalTransition)
		facts.FailureName = "EXECUTION_CANCELLED"
		stmtRow.State = domain.StmtCancelled
	case outcome.ProcessLost || outcome.Phase == osc.PhaseResultUnknown:
		facts.Terminal = domain.AttemptResultUnknown
		facts.ReasonCode = int(domain.CodeResultUnknown)
		facts.FailureName = "OSC_PROCESS_LOST"
		stmtRow.State = domain.StmtUnknown
		stmtRow.ErrCode = &facts.ReasonCode
		stmtRow.FailureName = &facts.FailureName
	default:
		// Non-zero exit (terminal failure regardless of whether cutover was observed complete;
		// under single-statement semantics, attributes failed/partial_failed based on observed cutover per contract).
		if outcome.Phase == osc.PhaseCutover || outcome.Phase == osc.PhaseCleanup {
			facts.Terminal = domain.AttemptPartialFailed
		} else {
			facts.Terminal = domain.AttemptFailed
		}
		facts.ReasonCode = int(domain.CodeGhOstUnsupported)
		facts.FailureName = outcome.FailureName
		stmtRow.State = domain.StmtFailed
		stmtRow.ErrCode = &facts.ReasonCode
		stmtRow.FailureName = &facts.FailureName
	}
	facts.Statements = []stmtFact{stmtRow}
	s.finalizeOscRollbackAfterRun(ctx, in, rbCtx, ddlParsed, ddlPreMeta, facts.Terminal)
	return s.applyResult(ctx, in.AttemptID, fence, facts, actor)
}

// execCredentialPassword decrypts the execution credential password (only at the instant of OSC spawn).
func (s *Services) execCredentialPassword(ctx context.Context, at *persistence.ExecutionAttemptModel) (string, error) {
	stage, err := s.loadStageByID(ctx, s.DB, at.StageID)
	if err != nil {
		return "", err
	}
	password, err := s.openExecPassword(ctx, s.DB, stage.DatasourceID)
	if err != nil {
		return "", err
	}
	return password, nil
}

// registerActive/unregisterActive registers or removes an active execution handle (for cancellation addressing).
func (r *Runner) registerActive(id string, run *activeRun) {
	r.mu.Lock()
	r.actives[id] = run
	r.mu.Unlock()
}

func (r *Runner) unregisterActive(id string) {
	r.mu.Lock()
	delete(r.actives, id)
	r.mu.Unlock()
}

// CancelActive cancels an Attempt running in this process: gh-ost uses panic flag + process cancellation;
// direct execution uses server-side targeted statement cancellation (contract cancel semantics: does not
// kill the connection so rollback can be confirmed; forcibly killing the connection would push the outcome into result_unknown).
func (r *Runner) CancelActive(attemptID string) bool {
	r.mu.Lock()
	run := r.actives[attemptID]
	r.mu.Unlock()
	if run == nil {
		return false
	}
	if run.Dir != "" && r.svc.Osc != nil {
		r.svc.Osc.Cancel(run.Dir)
		return true
	}
	if run.Pool != nil && run.Adapter != nil {
		_ = database.CancelInflightStatements(context.Background(), run.Adapter, run.Pool)
	}
	return true
}

// watchCancel polls whether the Attempt is set to cancelling -> triggers the cancellation callback
// (cross-instance cancellation is backed by the lease-timeout sweep; in-process cancellation responds
// immediately). Exits as soon as the Attempt enters a terminal state to prevent goroutine and DB
// polling leaks (execute ctx in direct-invocation scenarios may never be cancelled).
func (s *Services) watchCancel(ctx context.Context, attemptID string, trigger func()) func() {
	wctx, stop := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-ticker.C:
				var state string
				if err := s.DB.WithContext(wctx).Raw(
					`select state from execution_attempts where id = ?`, attemptID).
					Scan(&state).Error; err != nil {
					continue
				}
				switch domain.AttemptState(state) {
				case domain.AttemptCancelling:
					trigger()
					return
				case domain.AttemptSucceeded, domain.AttemptFailed,
					domain.AttemptPartialFailed, domain.AttemptCancelled,
					domain.AttemptPartialCancel, domain.AttemptResultUnknown:
					return
				}
			}
		}
	}()
	return stop
}

// persistStatementFact persists a single statement fact immediately (crash-observable;
// idempotently rewritten in full batch during terminal state persistence).
func (s *Services) persistStatementFact(ctx context.Context, attemptID string, fence int64, f stmtFact) error {
	sets := map[string]any{
		"state": string(f.State), "started_at": f.StartedAt, "finished_at": f.FinishedAt,
		"affected_row_count": f.AffectedRows,
	}
	if f.ErrCode != nil {
		sets["business_err_code"] = *f.ErrCode
		sets["failure_name"] = derefStr(f.FailureName)
	} else {
		sets["business_err_code"] = nil
		sets["failure_name"] = nil
	}
	res := s.DB.WithContext(ctx).Exec(
		`update execution_statements set `+setClause(sets)+
			` where attempt_id = ? and ordinal = ? and state in ('not_started','sent')
			  and exists (select 1 from execution_attempts a
			              where a.id = ? and a.fence_token = ? and a.lease_expires_at > now())`,
		append(setValues(sets), attemptID, f.Ordinal, attemptID, fence)...)
	return res.Error
}

// persistOscHeartbeat persists OSC heartbeat (throttled to <=10s).
func (s *Services) persistOscHeartbeat(ctx context.Context, attemptID string, fence int64, t time.Time) {
	_ = s.DB.WithContext(ctx).Exec(`
		update execution_attempts set osc_last_heartbeat_at = ?
		where id = ? and fence_token = ?`, t, attemptID, fence).Error
}

// persistOscProgress persists OSC progress (phase / basis points / row counts).
func (s *Services) persistOscProgress(ctx context.Context, attemptID string, fence int64,
	phase string, bps int, rows int64) {
	_ = s.DB.WithContext(ctx).Exec(`
		update execution_attempts set osc_phase = ?, osc_progress_basis_points = ?, osc_rows_copied = ?
		where id = ? and fence_token = ?`, phase, bps, rows, attemptID, fence).Error
}

// oscResidualInspector constructs a residual inspector (reusing read-only queries on the execution pool;
// returns nil = not_checked if the pool cannot be opened).
func (s *Services) oscResidualInspector(ctx context.Context, in TaskInput) (osc.ResidualInspector, func()) {
	target, cleanup, err := s.openStageTarget(ctx, in)
	if err != nil {
		return nil, func() {}
	}
	return &poolResidualInspector{target: target}, cleanup
}

// poolResidualInspector inspects gh-ost residuals via the target Adapter metadata surface
// (_gho/_ghc table existence + target table trigger count; allowlisted parameterized read-only identifiers).
type poolResidualInspector struct {
	target *stageTarget
}

func (p *poolResidualInspector) Inspect(ctx context.Context, dbName, table string) (ghost, changelog, triggers bool, socketExists bool, err error) {
	// gh-ost residuals: _gho/_ghc; pt-osc residual: _new shadow table (the same probe surface
	// covers both tools by naming convention).
	ghost, err = database.TableExists(ctx, p.target.Adapter, p.target.Pool, dbName, "_"+table+"_gho")
	if err != nil {
		return false, false, false, false, err
	}
	changelog, err = database.TableExists(ctx, p.target.Adapter, p.target.Pool, dbName, "_"+table+"_ghc")
	if err != nil {
		return false, false, false, false, err
	}
	shadow, err := database.TableExists(ctx, p.target.Adapter, p.target.Pool, dbName, "_"+table+"_new")
	if err != nil {
		return false, false, false, false, err
	}
	n, err := database.TriggerCountOn(ctx, p.target.Adapter, p.target.Pool, dbName, table)
	if err != nil {
		return false, false, false, false, err
	}
	return ghost || shadow, changelog, n > 0, false, nil
}
