// run.go Execution Runner (stage_execution task Handler): single-tx DML (E001), serial DDL (E002), gh-ost delegation.
// Durably advances send_boundary (not_started -> sending -> sent) and verifies Attempt fence; ambiguous -> result_unknown.
// Row business_err_code mapping: 3010 DATASOURCE_UNAVAILABLE (conn/worker loss), 2003 SQL_PARSE_FAILED (statement reject),
// 3005 EXECUTION_RESULT_UNKNOWN (result_unknown), 1010 EXECUTION_CANCELLED (user cancel).
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
	"strings"
)

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
					searchPathErr = s.verifyPostgresSearchPathForBinding(
						beforeSendCtx, target.Pool, *target.FrozenPath, target.Binding,
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
