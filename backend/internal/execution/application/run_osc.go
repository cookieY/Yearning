// run_osc.go holds gh-ost and pt-osc online schema change execution.
package application

import (
	"context"
	"encoding/json"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/osc"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"time"
)

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
	plain, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "execution_attempts",
		PrimaryKey: at.ID,
		Column:     "osc_plan_ciphertext",
	}, crypto.Envelope{
		Ciphertext: at.OSCPlanCiphertext, Nonce: at.OSCPlanNonce,
		KeyID: ptr.Deref(at.OSCPlanKeyID), Algorithm: ptr.Deref(at.OSCPlanAlgorithm),
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
		BinaryPath: bin.Path, BinarySHA256: ptr.Deref(at.OSCBinarySHA256),
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
	facts, stmtRow := buildOscOutcomeFacts(outcome, &startedAt, &finishedAt)
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
		sets["failure_name"] = ptr.Deref(f.FailureName)
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

func buildOscOutcomeFacts(outcome *osc.Outcome, startedAt, finishedAt *time.Time) (resultFacts, stmtFact) {
	facts := resultFacts{
		Boundary: domain.BoundarySent,
		Osc: &oscFact{
			Phase: outcome.Phase, ProgressBps: outcome.ProgressBps,
			RowsCopied: outcome.RowsCopied, Residual: outcome.ResidualState,
		},
	}
	stmtRow := stmtFact{
		Ordinal: 1, StartedAt: startedAt, FinishedAt: finishedAt,
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
	return facts, stmtRow
}
