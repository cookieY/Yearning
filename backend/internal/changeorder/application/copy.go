// copy.go Copies a terminal work order into a new draft (withdraw/void loop closure).
// Only copies SQL, title/description, and target flow reference; does not copy Review,
// approval decisions, execution results, or frozen flow instances; the new draft must
// undergo precheck again (PRD §5/§77).
package application

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// CopyInput parameters for copying a work order.
type CopyInput struct {
	OrderID        string
	TargetFlowID   string
	Title          string
	Description    string
	IdempotencyKey string
}

// CopyView copy result (new draft ID; ChangeDraft view is rendered by the B6 read surface).
type CopyView struct {
	DraftID string `json:"draft_id"`
}

const idemRouteCopy = "POST /change-orders/{order_id}/draft-copies"

// CopyToDraft copies a terminal work order into a new draft (submitter only).
func (s *Services) CopyToDraft(ctx context.Context, actor Actor, meta RequestMeta, in CopyInput) (view *CopyView, replayed bool, err error) {
	if in.Title == "" || len(in.Title) > 256 {
		return nil, false, domain.ErrValidation()
	}
	if len(in.Description) > 4096 {
		return nil, false, domain.ErrValidation()
	}
	reqHash := idemRequestHash(actor.UserID, in.OrderID, in.TargetFlowID, in.Title)
	claimed, replay, err := s.idemClaim(ctx, actor.UserID, idemRouteCopy, in.OrderID, in.IdempotencyKey, reqHash)
	if err != nil {
		return nil, false, err
	}
	if claimed == nil {
		var v CopyView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	view, err = s.copyTx(ctx, actor, in)
	if err != nil {
		_ = s.idemRelease(ctx, actor.UserID, idemRouteCopy, in.OrderID, in.IdempotencyKey)
		return nil, false, err
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return nil, false, err
	}
	if err := s.idemComplete(ctx, actor.UserID, idemRouteCopy, in.OrderID, in.IdempotencyKey, raw); err != nil {
		return nil, false, err
	}
	return view, false, nil
}

func (s *Services) copyTx(ctx context.Context, actor Actor, in CopyInput) (*CopyView, error) {
	var view *CopyView
	err := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.copyOnce(ctx, tx, actor, in)
		if err != nil {
			return err
		}
		view = v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

func (s *Services) copyOnce(ctx context.Context, tx *gorm.DB, actor Actor, in CopyInput) (*CopyView, error) {
	// Source work order: submitter only, must be in a terminal state (rejected/withdrawn/voided/failed, etc.).
	var order struct {
		ID                      string
		SubmitterUserIDSnapshot string
		State                   string
	}
	if err := tx.WithContext(ctx).Raw(
		`select id::text as id, submitter_user_id_snapshot::text as submitter_user_id_snapshot, state
		 from change_orders where id = ? for update`, in.OrderID).Scan(&order).Error; err != nil {
		return nil, err
	}
	if order.ID == "" {
		return nil, domain.ErrNotFound()
	}
	if order.SubmitterUserIDSnapshot != actor.UserID {
		return nil, domain.ErrNotFound()
	}
	if !domain.OrderTerminal(domain.OrderState(order.State)) {
		return nil, domain.ErrIllegal()
	}
	// Target flow: change_review and enabled; uses the current version (the new draft must
	// load the latest template and re-run precheck).
	var flow struct {
		ID       string
		FlowType string
		Status   string
		Version  int64
	}
	if err := tx.WithContext(ctx).Raw(
		`select id::text as id, flow_type, status, version from flows where id = ?`, in.TargetFlowID).
		Scan(&flow).Error; err != nil {
		return nil, err
	}
	if flow.ID == "" || flow.FlowType != "change_review" || flow.Status != "enabled" {
		return nil, domain.ErrValidation()
	}
	// Target flow authorization (consistent with CreateDraft: unauthorized flows cannot be draft targets).
	granted, err := s.flowGranted(ctx, tx, actor.UserID, in.TargetFlowID)
	if err != nil {
		return nil, err
	}
	if !granted {
		return nil, domain.ErrFlowGrantRevoked()
	}
	// Decrypt the source SQL envelope and re-seal it with the new draft's AAD.
	var payload struct {
		SQLCiphertext  []byte
		SQLNonce       []byte
		SQLKeyID       string
		SQLAlgorithm   string
		PlaintextHMAC  string
		SizeBytes      int32
		StatementCount int32
	}
	if err := tx.WithContext(ctx).Raw(`
		select sql_ciphertext, sql_nonce, sql_key_id, sql_algorithm,
		       plaintext_hmac, size_bytes, statement_count
		from order_sql_payloads where order_id = ?`, in.OrderID).Scan(&payload).Error; err != nil {
		return nil, err
	}
	if payload.PlaintextHMAC == "" {
		return nil, domain.ErrNotFound()
	}
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: payload.SQLCiphertext, Nonce: payload.SQLNonce,
		KeyID: payload.SQLKeyID, Algorithm: payload.SQLAlgorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "order_sql_payloads",
		PrimaryKey: in.OrderID, Column: "sql_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return nil, err
	}
	draftID := newID()
	now := time.Now().UTC()
	if err := tx.WithContext(ctx).Exec(`
		insert into change_drafts (id, owner_user_id, flow_id, flow_version_seen, title, description,
		    revision, state, version, created_at, updated_at)
		values (?, ?, ?, ?, ?, ?, 1, 'draft', 1, ?, ?)`,
		draftID, actor.UserID, in.TargetFlowID, flow.Version, in.Title, in.Description, now, now).Error; err != nil {
		return nil, err
	}
	env, err := s.Crypter.Seal(crypto.PurposePayload, plain, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(), Table: "draft_sql_payloads",
		PrimaryKey: draftID, Column: "sql_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Exec(`
		insert into draft_sql_payloads (draft_id, sql_ciphertext, sql_nonce, sql_key_id, sql_algorithm,
		    plaintext_hmac, size_bytes, statement_count, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		draftID, env.Ciphertext, env.Nonce, env.KeyID, env.Algorithm,
		payload.PlaintextHMAC, payload.SizeBytes, payload.StatementCount, now).Error; err != nil {
		return nil, err
	}
	return &CopyView{DraftID: draftID}, nil
}
