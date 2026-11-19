// verify.go resolve_unknown_execution: only a frozen executor may append a manual verification
// to a result_unknown Attempt (E005). Results are restricted to four values; reason + Evidence
// are required (AEAD-sealed); verification facts are append-only; confirmed_succeeded advances
// to completed / next stage and permanently sets manually_verified=true; still_unknown keeps
// the order blocked.
// The original Execution Run is permanently forbidden from re-executing (backed by unique index + 3004).
package application

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	"github.com/cookieY/YearningX/backend/internal/execution/persistence"
)

// EvidenceItem represents a single database-side evidence item (kind: text|database_fact|external_reference).
type EvidenceItem struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// VerifyInput holds parameters for manual verification.
type VerifyInput struct {
	AttemptID      string
	Actor          Actor
	ExpectVersion  int64
	Result         string
	Reason         string
	Evidence       []EvidenceItem
	IdempotencyKey string
}

// Verify persists a manual verification (idempotent). Returns the work order ID (ChangeOrder
// view is assembled by httpapi via the changeorder read surface — the replay path likewise reads only the current order view).
func (s *Services) Verify(ctx context.Context, in VerifyInput) (orderID string, replayed bool, err error) {
	route := "POST:/execution-attempts/{attempt_id}/verifications"
	reqHash := idemRequestHash(in.Actor.UserID, in.AttemptID, in.Result, in.Reason)
	claimed, replay, cerr := s.idemClaim(ctx, in.Actor.UserID, route, in.AttemptID, in.IdempotencyKey, reqHash)
	if cerr != nil {
		return "", false, cerr
	}
	if !claimed {
		var rid struct {
			OrderID string `json:"order_id"`
		}
		if jerr := json.Unmarshal(replay, &rid); jerr != nil || rid.OrderID == "" {
			return "", false, domain.ErrIdemReused()
		}
		return rid.OrderID, true, nil
	}
	var order string
	var out []byte
	txErr := s.serializable(ctx, func(tx *gorm.DB) error {
		oid, j, verr := s.verifyOnce(ctx, tx, in)
		if verr != nil {
			return verr
		}
		order = oid
		out = j
		return nil
	})
	if txErr != nil {
		var de *domain.Error
		if errors.As(txErr, &de) {
			_ = s.idemRelease(ctx, in.Actor.UserID, route, in.AttemptID, in.IdempotencyKey)
		}
		return "", false, txErr
	}
	if cerr := s.idemComplete(ctx, in.Actor.UserID, route, in.AttemptID, in.IdempotencyKey, out); cerr != nil {
		return "", false, cerr
	}
	return order, false, nil
}

// verifyOnce executes a single verification transaction.
func (s *Services) verifyOnce(ctx context.Context, tx *gorm.DB, in VerifyInput) (string, []byte, error) {
	// Validate result and evidence shape (3012 when Evidence is missing).
	result := domain.VerificationResult(in.Result)
	if !domain.ValidVerificationResult(result) {
		return "", nil, domain.ErrValidation()
	}
	if len([]rune(in.Reason)) < 1 || len([]rune(in.Reason)) > 4096 {
		return "", nil, domain.ErrValidation()
	}
	if len(in.Evidence) < 1 {
		return "", nil, domain.ErrEvidenceRequired()
	}
	for _, e := range in.Evidence {
		switch e.Kind {
		case "text", "database_fact", "external_reference":
		default:
			return "", nil, domain.ErrValidation()
		}
		if len([]rune(e.Content)) < 1 || len([]rune(e.Content)) > 8192 {
			return "", nil, domain.ErrValidation()
		}
	}
	at, err := s.loadAttemptForUpdate(ctx, tx, in.AttemptID)
	if err != nil {
		return "", nil, err
	}
	if !s.isFrozenExecutor(ctx, tx, at.StageID, in.Actor.UserID) {
		return "", nil, domain.ErrNotFrozen()
	}
	if at.State != string(domain.AttemptResultUnknown) {
		return "", nil, domain.ErrIllegal()
	}
	if in.ExpectVersion != at.Version {
		return "", nil, domain.ErrConcurrent()
	}
	order, err := s.lockOrder(ctx, tx, at.OrderID)
	if err != nil {
		return "", nil, err
	}
	// Order-side guard: the sole valid source state for verify_* is result_unknown. After the
	// first non-still_unknown verification, the work order is already terminalized — the Attempt
	// stays in result_unknown, but the work order must never be flipped by a second verification
	// (static edge table lookup does not check the actual source state, so it must be explicitly intercepted here).
	if orderdomain.OrderState(order.State) != orderdomain.StateResultUnknown {
		return "", nil, domain.ErrIllegal()
	}
	stage, err := s.loadOrderStage(ctx, tx, at.OrderID)
	if err != nil {
		return "", nil, err
	}
	now := nowUTC()
	// Verification fact (append-only; reason and Evidence are AEAD-sealed).
	reasonJSON, _ := json.Marshal(in.Reason)
	reasonEnv, err := s.Crypter.Seal(crypto.PurposePayload, reasonJSON, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "execution_verifications",
		PrimaryKey: at.ID + "#reason", Column: "reason", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return "", nil, err
	}
	evJSON, err := json.Marshal(in.Evidence)
	if err != nil {
		return "", nil, err
	}
	evEnv, err := s.Crypter.Seal(crypto.PurposePayload, evJSON, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "execution_verifications",
		PrimaryKey: at.ID + "#evidence", Column: "evidence_bundle", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return "", nil, err
	}
	ver := &persistence.ExecutionVerificationModel{
		ID: newID(), AttemptID: at.ID,
		VerifiedByUserID: in.Actor.UserID, VerifiedByUsername: in.Actor.Username,
		Result:           in.Result,
		ReasonCiphertext: reasonEnv.Ciphertext, ReasonNonce: reasonEnv.Nonce,
		ReasonKeyID: reasonEnv.KeyID, ReasonAlgorithm: reasonEnv.Algorithm,
		EvidenceBundleCiphertext: evEnv.Ciphertext, EvidenceNonce: evEnv.Nonce,
		EvidenceKeyID: evEnv.KeyID, EvidenceAlgorithm: evEnv.Algorithm,
		OccurredAt: now,
	}
	if err := tx.WithContext(ctx).Create(ver).Error; err != nil {
		if isUniqueViolation(err) {
			return "", nil, domain.ErrIllegal()
		}
		return "", nil, err
	}
	// Advance work order according to the verification result (from result_unknown source; still_unknown self-loop only records the fact).
	nextVersion := order.Version + 1
	sets := map[string]any{"version": nextVersion}
	var trigger orderdomain.OrderTrigger
	switch result {
	case domain.VerifiedSucceeded:
		last, lerr := s.isLastStage(ctx, tx, order.ID, stage.Position)
		if lerr != nil {
			return "", nil, lerr
		}
		if last {
			trigger = orderdomain.ActionVerifySucceededFinal
		} else {
			trigger = orderdomain.ActionVerifySucceededNonFinal
		}
		sets["manually_verified"] = true
	case domain.VerifiedFailed:
		trigger = orderdomain.ActionVerifyFailed
	case domain.VerifiedPartial:
		trigger = orderdomain.ActionVerifyPartial
	case domain.StillUnknown:
		// Keep blocking all subsequent stages; only record the verification fact and advance version.
		if err := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version, sets); err != nil {
			return "", nil, err
		}
		out, eerr := s.emitVerificationEvent(ctx, tx, order, stage, at, in, nextVersion, result)
		if eerr != nil {
			return "", nil, eerr
		}
		return order.ID, out, nil
	}
	next, terr := orderdomain.OrderTransition(orderdomain.StateResultUnknown, trigger)
	if terr != nil {
		return "", nil, domain.ErrIllegal()
	}
	sets["state"] = string(next)
	if orderdomain.OrderTerminal(next) {
		sets["terminal_at"] = now
	}
	if err := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version, sets); err != nil {
		return "", nil, err
	}
	if err := s.syncSourceOrderRollbackOnOrderTransitionTx(ctx, tx, order, next, now); err != nil {
		return "", nil, err
	}
	// Advance stage: succeeded -> stage succeeded (+ next stage activated); failed family -> stage terminal state.
	if result == domain.VerifiedSucceeded {
		if err := s.touchStageLocked(ctx, tx, stage.StageID, string(orderdomain.StageSucceeded)); err != nil {
			return "", nil, err
		}
		if next == orderdomain.StateStageApproval {
			if err := s.activateNextStage(ctx, tx, order.ID, stage.Position, nextVersion, in.Actor); err != nil {
				return "", nil, err
			}
		}
	} else {
		if err := s.touchStageLocked(ctx, tx, stage.StageID, string(orderdomain.StageFailed)); err != nil {
			return "", nil, err
		}
	}
	out, eerr := s.emitVerificationEvent(ctx, tx, order, stage, at, in, nextVersion, result)
	if eerr != nil {
		return "", nil, eerr
	}
	return order.ID, out, nil
}

// emitVerificationEvent emits unknown_resolved (emit_all: dual verification_recorded
// + state_changed events). Returns the state summary JSON (idempotency response body).
func (s *Services) emitVerificationEvent(ctx context.Context, tx *gorm.DB, order *orderRow,
	stage *targetSnapshotRow, at *persistence.ExecutionAttemptModel, in VerifyInput,
	version int64, result domain.VerificationResult) ([]byte, error) {
	toState := order.State
	if result != domain.StillUnknown {
		// The post-transition state in sets is known from the caller path; reconstruct from/to
		// semantics here using the pre-transition order snapshot + result (stateChanged.from = pre-transition result_unknown).
		var cur string
		if err := tx.WithContext(ctx).Raw(
			`select state from change_orders where id = ?`, order.ID).Scan(&cur).Error; err != nil {
			return nil, err
		}
		toState = cur
	}
	if err := s.emitOrderEvent(ctx, tx, EventUnknownResolv, order.ID, auditActor(in.Actor),
		VerificationRecordedPayload{
			OrderID: order.ID, AttemptID: at.ID, VerifiedByUserID: in.Actor.UserID,
			Result: in.Result, EvidenceCount: len(in.Evidence), AggregateVersion: version,
		}); err != nil {
		return nil, err
	}
	if err := s.emitOrderEvent(ctx, tx, EventUnknownResolv, order.ID, auditActor(in.Actor),
		StateChangedPayload{
			AggregateID: order.ID, From: strPtr(order.State), To: toState,
			ReasonCode: "unknown_resolved:" + in.Result, StageID: strPtr(stage.StageID),
			AggregateVersion: version,
		}); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"order_id": order.ID, "attempt_id": at.ID, "result": in.Result,
		"aggregate_version": version,
	})
}
