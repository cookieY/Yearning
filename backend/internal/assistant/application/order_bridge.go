// order_bridge.go implements formal AI precheck triggering (precheckAssistantSqlDraft) and
// idempotent change order submission (submitAssistantSqlDraftOrder) for SQL drafts,
// enforcing revision/HMAC bindings and platform submission gates (PRD §5.4 / §5.9).
package application

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
)

// PrecheckDraftInput corresponds to PrecheckAssistantSqlDraftRequest.
type PrecheckDraftInput struct {
	SessionID      string
	DraftID        string
	FlowID         string
	Revision       int64
	SQLHMAC        string
	OutputLocale   string
	IdempotencyKey string
}

// SubmitDraftOrderInput corresponds to SubmitAssistantSqlDraftOrderRequest.
type SubmitDraftOrderInput struct {
	SessionID             string
	DraftID               string
	FlowID                string
	RunID                 string
	Revision              int64
	SQLHMAC               string
	Title                 string
	Description           *string
	RequestedScheduledFor *time.Time
	RollbackEnabled       *bool
	IdempotencyKey        string
}

// PrecheckRunner abstracts the formal ephemeral AI review starter (delegating to review.Services.StartEphemeralRun by default).
type PrecheckRunner func(ctx context.Context, actor domain.Actor, flowID string, sql []byte, locale, idemKey string) (runID string, runState string, err error)

// OrderSubmitter abstracts formal change order submission (delegating to changeorder.Services.SubmitOrderV2 by default).
type OrderSubmitter func(ctx context.Context, actor domain.Actor, in orderapp.SubmitOrderV2Input) (*orderapp.OrderView, error)

// PrecheckDraft triggers a formal platform AI precheck for a restored SQL draft without bypassing any platform gate.
func (s *Services) PrecheckDraft(ctx context.Context, actor domain.Actor, in PrecheckDraftInput) (*domain.PrecheckDraftResult, error) {
	sess, err := s.loadOwnedSession(ctx, s.DB, actor.UserID, in.SessionID)
	if err != nil {
		return nil, err
	}
	draft, payload, err := s.loadOwnedDraft(ctx, s.DB, actor.UserID, sess.ID, in.DraftID)
	if err != nil {
		return nil, err
	}
	if draft.RiskCategory == string(domain.RiskReadOnly) {
		return nil, domain.ErrValidation("Read-only SELECT drafts do not require a change order; run them in Query Workspace instead")
	}
	if draft.SubmissionState == string(domain.SubmissionOrderSubmitted) {
		return nil, domain.ErrInvalidState("Draft has already been submitted as a formal change order and cannot be prechecked again")
	}

	// 1. Verify Revision and SQL HMAC to prevent prechecking stale drafts (6005).
	storedHMAC := hex.EncodeToString(draft.SQLHMAC)
	if in.Revision != draft.Revision || !strings.EqualFold(strings.TrimSpace(in.SQLHMAC), storedHMAC) {
		return nil, domain.ErrDraftRevisionStale("draft_revision_or_hmac_mismatch")
	}
	recomputedHMAC, _, err := s.computeSQLHMAC(payload.SQLText)
	if err != nil || !strings.EqualFold(hex.EncodeToString(recomputedHMAC), storedHMAC) {
		return nil, domain.ErrDraftRevisionStale("draft_sql_hmac_verification_failed")
	}

	// 2. Re-verify current user submission permission on the target datasource and change flow (6002).
	flowID := strings.TrimSpace(in.FlowID)
	if flowID == "" && draft.FlowID != nil {
		flowID = *draft.FlowID
	}
	dsTarget, err := s.Authorizer.AuthorizeDatasource(ctx, actor.UserID, draft.DatasourceID, flowID, true)
	if err != nil {
		// Invalidate session summaries when permissions change.
		_ = s.InvalidateSessionSummaries(ctx, s.DB, sess.ID)
		return nil, err
	}
	resolvedFlowID := dsTarget.ResolvedFlowID
	if resolvedFlowID == "" {
		resolvedFlowID = flowID
	}
	if resolvedFlowID == "" {
		return nil, domain.ErrDatasourceForbidden()
	}

	// 3. Invoke formal platform ephemeral precheck.
	runID, runState, err := s.runFormalPrecheck(ctx, actor, resolvedFlowID, []byte(payload.SQLText), in.OutputLocale, in.IdempotencyKey)
	if err != nil {
		return nil, mapBridgeError(err)
	}

	subState := string(domain.SubmissionPrecheckReady)
	switch runState {
	case string(reviewdomain.RunStateBlocked), string(reviewdomain.RunStateFailed), string(reviewdomain.RunStatePartial):
		subState = string(domain.SubmissionPrecheckBlocked)
	case "queued", "pending", string(reviewdomain.RunStateRunning):
		subState = string(domain.SubmissionPrecheckRunning)
	}

	now := time.Now().UTC()
	res := s.DB.WithContext(ctx).Model(&persistence.AIAssistantSQLDraftModel{}).
		Where("id = ? and version = ?", draft.ID, draft.Version).
		Updates(map[string]any{
			"flow_id":          resolvedFlowID,
			"ephemeral_run_id": runID,
			"submission_state": subState,
			"version":          draft.Version + 1,
			"updated_at":       now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, domain.ErrConcurrent()
	}
	draft.FlowID = &resolvedFlowID
	draft.EphemeralRunID = &runID
	draft.SubmissionState = subState
	draft.Version++
	draft.UpdatedAt = now

	view := s.draftView(draft, payload, &dsTarget.DatasourceName)
	return &domain.PrecheckDraftResult{
		Draft:          view,
		EphemeralRunID: runID,
		ResultRef:      "/review-runs/" + runID,
	}, nil
}

// SubmitDraftOrder submits a change order idempotently via SubmitOrderV2 after user confirmation and formal AI precheck completion.
// Only updates draft state and returns OrderView after the platform returns a committed order number (PRD §5.4-6/7).
func (s *Services) SubmitDraftOrder(ctx context.Context, actor domain.Actor, in SubmitDraftOrderInput) (*orderapp.OrderView, error) {
	if strings.TrimSpace(in.IdempotencyKey) == "" || len(in.IdempotencyKey) > 128 {
		return nil, domain.ErrValidation("Idempotency-Key must be non-empty and at most 128 characters")
	}
	if strings.TrimSpace(in.Title) == "" || len(in.Title) > 256 {
		return nil, domain.ErrValidation("Change order title must be non-empty and at most 256 characters")
	}

	sess, err := s.loadOwnedSession(ctx, s.DB, actor.UserID, in.SessionID)
	if err != nil {
		return nil, err
	}
	draft, payload, err := s.loadOwnedDraft(ctx, s.DB, actor.UserID, sess.ID, in.DraftID)
	if err != nil {
		return nil, err
	}
	if draft.RiskCategory == string(domain.RiskReadOnly) {
		return nil, domain.ErrValidation("Read-only SELECT drafts cannot be submitted as change orders")
	}

	// 1. Verify Revision, SQL HMAC, bound EphemeralRunID, and submission state (6005).
	storedHMAC := hex.EncodeToString(draft.SQLHMAC)
	if in.Revision != draft.Revision || !strings.EqualFold(strings.TrimSpace(in.SQLHMAC), storedHMAC) {
		return nil, domain.ErrDraftRevisionStale("draft_revision_or_hmac_mismatch")
	}
	if draft.SubmissionState == string(domain.SubmissionStale) {
		return nil, domain.ErrDraftRevisionStale("draft_is_stale")
	}
	if draft.EphemeralRunID == nil || *draft.EphemeralRunID != in.RunID {
		return nil, domain.ErrDraftRevisionStale("ephemeral_run_id_not_bound_to_draft")
	}
	if draft.FlowID != nil && *draft.FlowID != "" && *draft.FlowID != in.FlowID {
		return nil, domain.ErrDraftRevisionStale("flow_id_changed_since_precheck")
	}

	// 2. Re-verify current user submission permission on the target datasource and change flow (6002).
	dsTarget, err := s.Authorizer.AuthorizeDatasource(ctx, actor.UserID, draft.DatasourceID, in.FlowID, true)
	if err != nil {
		_ = s.InvalidateSessionSummaries(ctx, s.DB, sess.ID)
		return nil, err
	}
	_ = dsTarget

	// 3. Invoke the canonical platform order submission entry point (including idempotency key, Submission Gate, and multi-stage checks).
	title := strings.TrimSpace(in.Title)
	orderView, err := s.runFormalOrderSubmit(ctx, actor, orderapp.SubmitOrderV2Input{
		FlowID:                in.FlowID,
		SQL:                   []byte(payload.SQLText),
		RunID:                 in.RunID,
		Title:                 &title,
		Description:           in.Description,
		RequestedScheduledFor: in.RequestedScheduledFor,
		RollbackEnabled:       in.RollbackEnabled,
		IdempotencyKey:        strings.TrimSpace(in.IdempotencyKey),
	})
	if err != nil {
		return nil, mapBridgeError(err)
	}

	// 4. Update draft state to order_submitted after the platform returns the committed order number.
	now := time.Now().UTC()
	_ = s.DB.WithContext(ctx).Model(&persistence.AIAssistantSQLDraftModel{}).
		Where("id = ?", draft.ID).
		Updates(map[string]any{
			"submission_state":           string(domain.SubmissionOrderSubmitted),
			"submitted_order_id":         orderView.ID,
			"submitted_order_display_no": orderView.DisplayNumber,
			"version":                    gorm.Expr("version + 1"),
			"updated_at":                 now,
		}).Error

	return orderView, nil
}

func (s *Services) runFormalPrecheck(
	ctx context.Context,
	actor domain.Actor,
	flowID string,
	sqlBytes []byte,
	locale string,
	idemKey string,
) (string, string, error) {
	if s.PrecheckRunner != nil {
		return s.PrecheckRunner(ctx, actor, flowID, sqlBytes, locale, idemKey)
	}
	if s.Review == nil || s.ReviewAgent == nil {
		return "", "", domain.ErrProviderFailed("AI precheck service is not configured")
	}
	if locale == "" {
		locale = "zh-CN"
	}
	runID, err := s.Review.StartEphemeralRun(ctx, reviewapp.Actor{
		UserID:   actor.UserID,
		Username: actor.Username,
	}, s.ReviewAgent, reviewapp.EphemeralStartInput{
		FlowID:         flowID,
		SQL:            sqlBytes,
		OutputLocale:   locale,
		IdempotencyKey: idemKey,
	})
	if err != nil {
		return "", "", err
	}
	state := string(reviewdomain.RunStateRunning)
	if er := s.Review.LookupEphemeralRun(runID, actor.UserID); er != nil {
		state = string(er.State())
	}
	return runID, state, nil
}

func (s *Services) runFormalOrderSubmit(
	ctx context.Context,
	actor domain.Actor,
	in orderapp.SubmitOrderV2Input,
) (*orderapp.OrderView, error) {
	if s.OrderSubmitter != nil {
		return s.OrderSubmitter(ctx, actor, in)
	}
	if s.Orders == nil || s.Review == nil {
		return nil, domain.ErrInvalidState("Change order service is not configured")
	}
	view, _, err := s.Orders.SubmitOrderV2(ctx, orderapp.Actor{
		UserID:   actor.UserID,
		Username: actor.Username,
	}, s.Review, in)
	return view, err
}

func mapBridgeError(err error) error {
	if err == nil {
		return nil
	}
	var ade *domain.Error
	if errors.As(err, &ade) {
		return ade
	}
	var rde *reviewdomain.Error
	if errors.As(err, &rde) {
		meta := map[string]any{}
		if rde.Detail != "" {
			meta["detail"] = rde.Detail
		}
		return &domain.Error{Code: int(rde.Code), Name: rde.Name, Message: "AI precheck validation failed", Meta: meta}
	}
	var ode *orderdomain.Error
	if errors.As(err, &ode) {
		name := ode.Name
		meta := map[string]any{}
		if i := strings.Index(name, ":"); i >= 0 {
			meta["reason"] = name[i+1:]
			name = name[:i]
		}
		// Forward canonical changeorder business error code and metadata.
		return &domain.Error{Code: int(ode.Code), Name: name, Message: "Submission gate check failed", Meta: meta}
	}
	return err
}
