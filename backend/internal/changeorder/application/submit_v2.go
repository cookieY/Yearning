// submit_v2.go Submit v2 (RCP-20261101-EPHEMERAL-PRECHECK).
//
// Owner 2026-11-01 invariant: Save & Return = sole write entry point for drafts/SQL; Submit =
// sole persistence point for review artifacts. This command carries {flow, current SQL, ephemeral
// Run reference, metadata} and completes within a single serializable transaction: consume ephemeral
// Run -> persist SQL payload and verify against reviewed HMAC (preventing review-A-submit-B) ->
// validate/backfill draft -> materialize Run -> reuse submitOnce to freeze the work order.
// The precheck path never touches drafts.
package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
)

// idemRouteSubmitV2 idempotency route for submit v2 (matches OpenAPI path).
const idemRouteSubmitV2 = "POST /change-orders"

// SubmitOrderV2Input parameters for submit v2.
type SubmitOrderV2Input struct {
	FlowID string
	// DraftID optional: present = submit existing draft; omitted = direct submit in new-order mode
	// (backfills a shell draft row within the transaction without producing an editable draft —
	// the state machine immediately transitions it to submitted).
	DraftID string
	SQL     []byte
	RunID   string
	// Metadata (title is required for direct submit in new-order mode; draft mode uses the draft's metadata and ignores this).
	Title                 *string
	Description           *string
	RequestedScheduledFor *time.Time
	RollbackEnabled       *bool
	IdempotencyKey        string
}

// SubmitOrderV2 submits an ephemeral precheck result as an immutable work order.
func (s *Services) SubmitOrderV2(ctx context.Context, actor Actor, reviewSvc *reviewapp.Services, in SubmitOrderV2Input) (view *OrderView, replayed bool, err error) {
	reqHash := idemRequestHash(actor.UserID, in.FlowID, in.RunID)
	claimed, replay, err := s.idemClaim(ctx, actor.UserID, idemRouteSubmitV2, in.RunID, in.IdempotencyKey, reqHash)
	if err != nil {
		return nil, false, err
	}
	if claimed == nil {
		var v OrderView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	view, err = s.submitV2Tx(ctx, actor, reviewSvc, in)
	if err != nil {
		_ = s.idemRelease(ctx, actor.UserID, idemRouteSubmitV2, in.RunID, in.IdempotencyKey)
		return nil, false, err
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return nil, false, err
	}
	if err := s.idemComplete(ctx, actor.UserID, idemRouteSubmitV2, in.RunID, in.IdempotencyKey, raw); err != nil {
		return nil, false, err
	}
	return view, false, nil
}

func (s *Services) submitV2Tx(ctx context.Context, actor Actor, reviewSvc *reviewapp.Services, in SubmitOrderV2Input) (*OrderView, error) {
	var view *OrderView
	// Review agent H1 (closure round revision): Take is performed only once outside the serializable closure.
	// SSI conflicts are typically detected at COMMIT — serializable re-enters the closure, and any
	// combination of Take/compensate inside the closure leaves a window where "retry cannot recover the Run".
	// Taking once outside the closure and reusing the same pointer across retries, compensating only if
	// serializable ultimately fails, ensures transient conflicts never destroy the precheck result the
	// user is viewing; on success, the materialized rows become the authoritative surface.
	er := reviewSvc.EphemeralTake(in.RunID, actor.UserID)
	if er == nil {
		return nil, domain.ErrReviewOutdated()
	}
	err := s.serializable(ctx, func(tx *gorm.DB) error {
		if er.State() != reviewdomain.RunStateReady {
			// Non-ready Runs (blocked/partial/failed/running) uniformly require re-running precheck.
			return domain.ErrReviewOutdated()
		}
		if er.FlowID != in.FlowID {
			return domain.ErrValidation()
		}
		// Validate/backfill draft (including consistency of flow version with precheck time).
		draftID, expectVersion, draftRevision, derr := s.ensureDraftForSubmitV2(ctx, tx, actor, in, er)
		if derr != nil {
			return derr
		}
		// Persist SQL payload (submission write entry point) and compare against reviewed HMAC (preventing review-A-submit-B).
		mac, perr := reviewSvc.PersistDraftSQLPayloadTx(ctx, tx, draftID, in.SQL, er.StatementCount())
		if perr != nil {
			return perr
		}
		if !strings.EqualFold(mac, er.SQLHMAC) {
			return domain.ErrReviewOutdated()
		}
		// Materialize Run (Submit = sole persistence point for review artifacts).
		if merr := reviewSvc.MaterializeEphemeralRunTx(ctx, tx, er, draftID, draftRevision); merr != nil {
			return merr
		}
		// Reuse existing submit command body (gate re-verification / freeze / events).
		v, serr := s.submitOnce(ctx, tx, actor, SubmitInput{
			DraftID:        draftID,
			ExpectVersion:  expectVersion,
			IdempotencyKey: in.IdempotencyKey,
		})
		if serr != nil {
			return serr
		}
		view = v
		return nil
	})
	if err != nil {
		// Final failure (including retry exhaustion): restore in-memory Run via compensation so the user can view and resubmit.
		reviewSvc.EphemeralPutBack(er)
		return nil, err
	}
	return view, nil
}

// ensureDraftForSubmitV2 validates an existing draft (ownership / flow / version) and returns
// (draftID, expectVersion, revision); backfills a shell row for direct submit in new-order mode.
func (s *Services) ensureDraftForSubmitV2(ctx context.Context, tx *gorm.DB, actor Actor, in SubmitOrderV2Input, er *reviewapp.EphemeralRun) (string, int64, int64, error) {
	if in.DraftID != "" {
		var draft struct {
			ID              string
			OwnerUserID     string
			FlowID          string
			FlowVersionSeen int64
			TicketType      string
			RollbackEnabled bool
			State           string
			Version         int64
			Revision        int64
		}
		if err := tx.WithContext(ctx).Raw(
			`select id::text as id, owner_user_id::text as owner_user_id, flow_id::text as flow_id,
			        flow_version_seen, ticket_type, rollback_enabled, state, version, revision
			 from change_drafts where id = ? for update`, in.DraftID).Scan(&draft).Error; err != nil {
			return "", 0, 0, err
		}
		if draft.ID == "" || draft.OwnerUserID != actor.UserID {
			return "", 0, 0, domain.ErrNotFound()
		}
		if draft.FlowID != in.FlowID {
			return "", 0, 0, domain.ErrValidation()
		}
		var flowVersion int64
		if err := tx.WithContext(ctx).Raw(
			`select version from flows where id = ?`, in.FlowID).Scan(&flowVersion).Error; err != nil {
			return "", 0, 0, err
		}
		// Flow drift (relative to draft confirmation time or precheck time) requires re-running precheck.
		if flowVersion != draft.FlowVersionSeen || flowVersion != er.FlowVersion {
			return "", 0, 0, domain.ErrReviewOutdated()
		}
		if draft.TicketType == "rollback" {
			var expectedHMAC string
			if err := tx.WithContext(ctx).Raw(
				`select plaintext_hmac from draft_sql_payloads where draft_id = ?`, draft.ID,
			).Scan(&expectedHMAC).Error; err != nil {
				return "", 0, 0, err
			}
			if expectedHMAC != "" && s.Crypter != nil {
				rawMAC, merr := s.Crypter.HMAC("sql_fingerprint", in.SQL)
				if merr != nil {
					return "", 0, 0, merr
				}
				matched := strings.EqualFold(expectedHMAC, hex.EncodeToString(rawMAC))
				if !matched {
					trimmedMAC, terr := s.Crypter.HMAC("sql_fingerprint", []byte(strings.TrimSpace(string(in.SQL))))
					if terr != nil {
						return "", 0, 0, terr
					}
					matched = strings.EqualFold(expectedHMAC, hex.EncodeToString(trimmedMAC))
				}
				if !matched {
					return "", 0, 0, domain.ErrIllegal()
				}
			}
		}
		if in.RollbackEnabled != nil && draft.TicketType != "rollback" && *in.RollbackEnabled != draft.RollbackEnabled {
			if err := tx.WithContext(ctx).Exec(
				`update change_drafts set rollback_enabled = ? where id = ?`,
				*in.RollbackEnabled, draft.ID).Error; err != nil {
				return "", 0, 0, err
			}
		}
		return draft.ID, draft.Version, draft.Revision, nil
	}
	// Direct submit in new-order mode: title is required.
	if in.Title == nil || strings.TrimSpace(*in.Title) == "" {
		return "", 0, 0, domain.ErrValidation()
	}
	draftID := newID()
	now := time.Now().UTC()
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	rollbackEnabled := true
	if in.RollbackEnabled != nil {
		rollbackEnabled = *in.RollbackEnabled
	}
	if err := tx.WithContext(ctx).Exec(`
		insert into change_drafts (id, owner_user_id, flow_id, flow_version_seen,
			title, description, requested_scheduled_for, rollback_enabled, ticket_type,
			state, revision, version, created_at, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, 'normal', 'draft', 1, 1, ?, ?)`,
		draftID, actor.UserID, in.FlowID, er.FlowVersion,
		*in.Title, description, in.RequestedScheduledFor, rollbackEnabled, now, now).Error; err != nil {
		return "", 0, 0, err
	}
	return draftID, 1, 1, nil
}
