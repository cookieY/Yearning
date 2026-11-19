// rollback.go Rollback work order read surface and one-click rollback draft generation.
// Reuses the existing ChangeDraft / SQL precheck / ChangeOrder approval and execution pipeline.
package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/execution/rollback"
)

const idemRouteRollbackDraft = "POST /change-orders/{order_id}/rollback-drafts"

// RollbackDraftInput request parameters for one-click rollback draft generation.
type RollbackDraftInput struct {
	OrderID        string
	IdempotencyKey string
}

// populateOrderRollbackView populates rollback associations and evidence reasons on OrderView.
func (s *Services) populateOrderRollbackView(ctx context.Context, db *gorm.DB, o *persistence.ChangeOrderModel, view *OrderView) error {
	if o.SourceOrderID != nil && *o.SourceOrderID != "" {
		var srcDisplay string
		if err := db.WithContext(ctx).Raw(
			`select display_number from change_orders where id = ?`, *o.SourceOrderID,
		).Scan(&srcDisplay).Error; err != nil {
			return err
		}
		if srcDisplay != "" {
			view.SourceOrderDisplayNumber = &srcDisplay
		}
	}

	var ev struct {
		ID                string  `gorm:"column:id"`
		RollbackStatus    string  `gorm:"column:rollback_status"`
		UnsupportedReason *string `gorm:"column:unsupported_reason"`
		ConflictReason    *string `gorm:"column:conflict_reason"`
	}
	if err := db.WithContext(ctx).Raw(`
		select id::text as id, rollback_status, unsupported_reason, conflict_reason
		from order_rollback_evidences
		where order_id = ?
		order by created_at desc limit 1`, o.ID).Scan(&ev).Error; err != nil {
		return err
	}
	if ev.ID != "" {
		if view.RollbackEvidenceID == nil || *view.RollbackEvidenceID == "" {
			id := ev.ID
			view.RollbackEvidenceID = &id
		}
		view.RollbackUnsupportedReason = ev.UnsupportedReason
		view.RollbackConflictReason = ev.ConflictReason
	}

	var childRows []struct {
		ID            string    `gorm:"column:id"`
		DisplayNumber string    `gorm:"column:display_number"`
		State         string    `gorm:"column:state"`
		SubmittedAt   time.Time `gorm:"column:submitted_at"`
	}
	if err := db.WithContext(ctx).Raw(`
		select id::text as id, display_number, state, submitted_at
		from change_orders
		where source_order_id = ?
		order by submitted_at desc, id desc`, o.ID).Scan(&childRows).Error; err != nil {
		return err
	}
	for _, c := range childRows {
		view.RollbackOrders = append(view.RollbackOrders, RollbackOrderRef{
			ID:            c.ID,
			DisplayNumber: c.DisplayNumber,
			State:         c.State,
			SubmittedAt:   c.SubmittedAt.UTC().Format("2006-01-02T15:04:05.999999Z"),
		})
	}
	return nil
}

// assertRollbackSubmissionAllowed validates preconditions for submitting a rollback work order:
// 1. Source order exists, is completed, is not a rollback order, and has rollback_status in ('available', 'rollback_failed');
// 2. Bound evidence exists, has rollback_status in ('available', 'rollback_failed'), and rollback SQL HMAC matches evidence (tampering with rollback SQL is strictly forbidden);
// 3. Source order has no existing rollback order that is not in a terminal failed/withdrawn/rejected/voided state;
// 4. Marks both the source order and rollback evidence as rollback_in_progress within the same transaction upon passing validation.
func (s *Services) assertRollbackSubmissionAllowed(
	ctx context.Context,
	tx *gorm.DB,
	sourceOrderID string,
	evidenceIDPtr *string,
	submittedSQLHMAC string,
	submittedPlainSQL []byte,
	now time.Time,
) error {
	if sourceOrderID == "" || evidenceIDPtr == nil || *evidenceIDPtr == "" {
		return domain.ErrIllegal()
	}
	evidenceID := *evidenceIDPtr
	var src struct {
		ID             string
		State          string
		TicketType     string
		RollbackStatus string
	}
	if err := tx.WithContext(ctx).Raw(`
		select id::text as id, state, ticket_type, rollback_status
		from change_orders
		where id = ? for update`, sourceOrderID).Scan(&src).Error; err != nil {
		return err
	}
	if src.ID == "" {
		return domain.ErrNotFound()
	}
	if src.State != "completed" || src.TicketType == "rollback" ||
		(src.RollbackStatus != "available" && src.RollbackStatus != "rollback_failed") {
		return domain.ErrIllegal()
	}
	var ev struct {
		ID              string `gorm:"column:id"`
		RollbackStatus  string `gorm:"column:rollback_status"`
		RollbackSQLHMAC string `gorm:"column:rollback_sql_hmac"`
	}
	if err := tx.WithContext(ctx).Raw(`
		select id::text as id, rollback_status, rollback_sql_hmac
		from order_rollback_evidences
		where id = ? and order_id = ? for update`, evidenceID, sourceOrderID).Scan(&ev).Error; err != nil {
		return err
	}
	if ev.ID == "" || (ev.RollbackStatus != "available" && ev.RollbackStatus != "rollback_failed") {
		return domain.ErrIllegal()
	}
	if ev.RollbackSQLHMAC == "" {
		return domain.ErrIllegal()
	}
	hmacMatched := strings.EqualFold(ev.RollbackSQLHMAC, submittedSQLHMAC)
	if !hmacMatched && len(submittedPlainSQL) > 0 && s.Crypter != nil {
		trimmed := strings.TrimSpace(string(submittedPlainSQL))
		if macBytes, merr := s.Crypter.HMAC("sql_fingerprint", []byte(trimmed)); merr == nil {
			hmacMatched = strings.EqualFold(ev.RollbackSQLHMAC, hex.EncodeToString(macBytes))
		}
	}
	if !hmacMatched {
		return domain.ErrIllegal()
	}
	var activeCount int64
	if err := tx.WithContext(ctx).Raw(`
		select count(*) from change_orders
		where source_order_id = ?
		  and state not in ('rejected', 'withdrawn', 'voided', 'failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'missed_schedule')`, sourceOrderID).Scan(&activeCount).Error; err != nil {
		return err
	}
	if activeCount > 0 {
		return domain.ErrIllegal()
	}
	if err := tx.WithContext(ctx).Exec(`
		update change_orders
		set rollback_status = 'rollback_in_progress', version = version + 1
		where id = ? and rollback_status in ('available', 'rollback_failed')`,
		sourceOrderID).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		update order_rollback_evidences
		set rollback_status = 'rollback_in_progress', updated_at = ?
		where id = ? and rollback_status in ('available', 'rollback_failed')`,
		now, evidenceID).Error; err != nil {
		return err
	}
	return nil
}

// restoreSourceOrderRollbackStatusOnAbort restores the source order and rollback evidence from
// rollback_in_progress to available (or rollback_failed if a failed attempt already occurred)
// when a rollback work order is rejected, withdrawn, or voided.
func (s *Services) restoreSourceOrderRollbackStatusOnAbort(
	ctx context.Context,
	tx *gorm.DB,
	order *persistence.ChangeOrderModel,
	now time.Time,
) error {
	if order == nil || order.TicketType != "rollback" ||
		order.SourceOrderID == nil || *order.SourceOrderID == "" {
		return nil
	}
	sourceOrderID := *order.SourceOrderID
	var priorFailedCount int64
	if err := tx.WithContext(ctx).Raw(`
		select count(*) from change_orders
		where source_order_id = ?
		  and id <> ?
		  and state in ('failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'missed_schedule')`,
		sourceOrderID, order.ID).Scan(&priorFailedCount).Error; err != nil {
		return err
	}
	restoredStatus := "available"
	if priorFailedCount > 0 {
		restoredStatus = "rollback_failed"
	}
	if err := tx.WithContext(ctx).Exec(`
		update change_orders
		set rollback_status = ?, version = version + 1
		where id = ? and rollback_status = 'rollback_in_progress'`,
		restoredStatus, sourceOrderID).Error; err != nil {
		return err
	}
	if order.RollbackEvidenceID != nil && *order.RollbackEvidenceID != "" {
		if err := tx.WithContext(ctx).Exec(`
			update order_rollback_evidences
			set rollback_status = ?, updated_at = ?
			where id = ? and rollback_status = 'rollback_in_progress'`,
			restoredStatus, now, *order.RollbackEvidenceID).Error; err != nil {
			return err
		}
	} else {
		if err := tx.WithContext(ctx).Exec(`
			update order_rollback_evidences
			set rollback_status = ?, updated_at = ?
			where order_id = ? and rollback_status = 'rollback_in_progress'`,
			restoredStatus, now, sourceOrderID).Error; err != nil {
			return err
		}
	}
	return nil
}

// RevealOrderRollbackSQL decrypts the rollback SQL plaintext from a work order's rollback evidence (relation check; writes audit).
func (s *Services) RevealOrderRollbackSQL(ctx context.Context, actor Actor, meta RequestMeta, orderID, purpose string) (*SQLRevealResult, error) {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotFound()
	}
	var ev struct {
		ID                 string `gorm:"column:id"`
		EvidenceCiphertext []byte `gorm:"column:evidence_ciphertext"`
		EvidenceNonce      []byte `gorm:"column:evidence_nonce"`
		EvidenceKeyID      string `gorm:"column:evidence_key_id"`
		EvidenceAlgorithm  string `gorm:"column:evidence_algorithm"`
		RollbackSQLHMAC    string `gorm:"column:rollback_sql_hmac"`
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select id::text as id,
		       rollback_payload_ciphertext as evidence_ciphertext,
		       rollback_payload_nonce as evidence_nonce,
		       rollback_payload_key_id as evidence_key_id,
		       rollback_payload_algorithm as evidence_algorithm,
		       rollback_sql_hmac
		from order_rollback_evidences
		where order_id = ?
		order by created_at desc limit 1`, orderID).Scan(&ev).Error; err != nil {
		return nil, err
	}
	if ev.ID == "" || len(ev.EvidenceCiphertext) == 0 {
		return nil, domain.ErrNotFound()
	}
	bundle, err := rollback.OpenEvidenceBundle(
		s.Crypter,
		ev.ID,
		crypto.Envelope{
			Ciphertext: ev.EvidenceCiphertext,
			Nonce:      ev.EvidenceNonce,
			KeyID:      ev.EvidenceKeyID,
			Algorithm:  ev.EvidenceAlgorithm,
		},
		ev.RollbackSQLHMAC,
	)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(bundle.RollbackSQL) == "" {
		return nil, domain.ErrNotFound()
	}
	revealID := newID()
	now := nowUTC()
	res := &SQLRevealResult{
		RevealID:   revealID,
		SQL:        bundle.RollbackSQL,
		Watermark:  buildWatermark(actor.Username, now),
		ValidUntil: now.Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if purpose == "" {
		purpose = "rollback-order-sql"
	}
	if err := s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "workorder.sql_revealed", ActorKind: "user",
		ActorID: strPtrOrNil(actor.UserID), ActorUsername: strPtrOrNil(actor.Username),
		ResourceType: "change_order", ResourceID: strPtrOrNil(orderID),
		Action: "reveal", Outcome: "succeeded",
		RequestID: strPtrOrNil(meta.RequestID), SourceIP: strPtrOrNil(meta.SourceIP),
		UserAgent: strPtrOrNil(meta.UserAgent),
		Metadata:  map[string]any{"reveal_id": revealID, "purpose": purpose, "target": "rollback_sql"},
	}); err != nil {
		return nil, err
	}
	return res, nil
}

// CreateRollbackDraft creates a rollback draft in one click from verified rollback evidence of a completed work order.
func (s *Services) CreateRollbackDraft(ctx context.Context, actor Actor, meta RequestMeta, in RollbackDraftInput) (view *CopyView, replayed bool, err error) {
	reqHash := idemRequestHash(actor.UserID, in.OrderID, "rollback_draft")
	claimed, replay, err := s.idemClaim(ctx, actor.UserID, idemRouteRollbackDraft, in.OrderID, in.IdempotencyKey, reqHash)
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
	view, err = s.createRollbackDraftTx(ctx, actor, meta, in)
	if err != nil {
		_ = s.idemRelease(ctx, actor.UserID, idemRouteRollbackDraft, in.OrderID, in.IdempotencyKey)
		return nil, false, err
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return nil, false, err
	}
	if err := s.idemComplete(ctx, actor.UserID, idemRouteRollbackDraft, in.OrderID, in.IdempotencyKey, raw); err != nil {
		return nil, false, err
	}
	return view, false, nil
}

func (s *Services) createRollbackDraftTx(ctx context.Context, actor Actor, meta RequestMeta, in RollbackDraftInput) (*CopyView, error) {
	var view *CopyView
	err := s.serializable(ctx, func(tx *gorm.DB) error {
		v, err := s.createRollbackDraftOnce(ctx, tx, actor, meta, in)
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

func (s *Services) createRollbackDraftOnce(ctx context.Context, tx *gorm.DB, actor Actor, meta RequestMeta, in RollbackDraftInput) (*CopyView, error) {
	ok, err := s.CanRead(ctx, tx, in.OrderID, actor.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotFound()
	}
	var order struct {
		ID             string
		DisplayNumber  string
		Title          string
		State          string
		TicketType     string
		RollbackStatus string
	}
	if err := tx.WithContext(ctx).Raw(`
		select id::text as id, display_number, title, state, ticket_type, rollback_status
		from change_orders where id = ? for update`, in.OrderID).Scan(&order).Error; err != nil {
		return nil, err
	}
	if order.ID == "" {
		return nil, domain.ErrNotFound()
	}
	if order.State != "completed" || order.TicketType == "rollback" ||
		(order.RollbackStatus != "available" && order.RollbackStatus != "rollback_failed") {
		return nil, domain.ErrIllegal()
	}
	var activeCount int64
	if err := tx.WithContext(ctx).Raw(`
		select count(*) from change_orders
		where source_order_id = ?
		  and state not in ('rejected', 'withdrawn', 'voided', 'failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'missed_schedule')`, in.OrderID).Scan(&activeCount).Error; err != nil {
		return nil, err
	}
	if activeCount > 0 {
		return nil, domain.ErrIllegal()
	}

	var ev struct {
		ID                 string `gorm:"column:id"`
		RollbackStatus     string `gorm:"column:rollback_status"`
		EvidenceCiphertext []byte `gorm:"column:evidence_ciphertext"`
		EvidenceNonce      []byte `gorm:"column:evidence_nonce"`
		EvidenceKeyID      string `gorm:"column:evidence_key_id"`
		EvidenceAlgorithm  string `gorm:"column:evidence_algorithm"`
		RollbackSQLHMAC    string `gorm:"column:rollback_sql_hmac"`
	}
	if err := tx.WithContext(ctx).Raw(`
		select id::text as id, rollback_status,
		       rollback_payload_ciphertext as evidence_ciphertext,
		       rollback_payload_nonce as evidence_nonce,
		       rollback_payload_key_id as evidence_key_id,
		       rollback_payload_algorithm as evidence_algorithm,
		       rollback_sql_hmac
		from order_rollback_evidences
		where order_id = ?
		order by created_at desc limit 1`, in.OrderID).Scan(&ev).Error; err != nil {
		return nil, err
	}
	if ev.ID == "" || (ev.RollbackStatus != "available" && ev.RollbackStatus != "rollback_failed") || len(ev.EvidenceCiphertext) == 0 {
		return nil, domain.ErrIllegal()
	}
	bundle, err := rollback.OpenEvidenceBundle(
		s.Crypter,
		ev.ID,
		crypto.Envelope{
			Ciphertext: ev.EvidenceCiphertext,
			Nonce:      ev.EvidenceNonce,
			KeyID:      ev.EvidenceKeyID,
			Algorithm:  ev.EvidenceAlgorithm,
		},
		ev.RollbackSQLHMAC,
	)
	if err != nil {
		return nil, err
	}
	rollbackSQL := strings.TrimSpace(bundle.RollbackSQL)
	if rollbackSQL == "" {
		return nil, domain.ErrIllegal()
	}

	var flowSnapRow struct {
		Ciphertext []byte `gorm:"column:flow_snapshot_ciphertext"`
		Nonce      []byte `gorm:"column:flow_snapshot_nonce"`
		KeyID      string `gorm:"column:flow_snapshot_key_id"`
		Algorithm  string `gorm:"column:flow_snapshot_algorithm"`
	}
	if err := tx.WithContext(ctx).Raw(`
		select flow_snapshot_ciphertext, flow_snapshot_nonce, flow_snapshot_key_id, flow_snapshot_algorithm
		from order_flow_snapshots where order_id = ?`, in.OrderID).Scan(&flowSnapRow).Error; err != nil {
		return nil, err
	}
	if len(flowSnapRow.Ciphertext) == 0 {
		return nil, domain.ErrValidation()
	}
	flowPlain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: flowSnapRow.Ciphertext,
		Nonce:      flowSnapRow.Nonce,
		KeyID:      flowSnapRow.KeyID,
		Algorithm:  flowSnapRow.Algorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "order_flow_snapshots",
		PrimaryKey:   in.OrderID,
		Column:       "flow_snapshot_ciphertext",
		Purpose:      crypto.PurposePayload,
	})
	if err != nil {
		return nil, err
	}
	var flowSnap frozenFlowDocument
	if err := json.Unmarshal(flowPlain, &flowSnap); err != nil || flowSnap.FlowID == "" {
		return nil, domain.ErrValidation()
	}
	var flow struct {
		ID       string
		FlowType string
		Status   string
		Version  int64
	}
	if err := tx.WithContext(ctx).Raw(
		`select id::text as id, flow_type, status, version from flows where id = ?`, flowSnap.FlowID,
	).Scan(&flow).Error; err != nil {
		return nil, err
	}
	if flow.ID == "" || flow.FlowType != "change_review" || flow.Status != "enabled" {
		return nil, domain.ErrValidation()
	}
	granted, err := s.flowGranted(ctx, tx, actor.UserID, flow.ID)
	if err != nil {
		return nil, err
	}
	if !granted {
		return nil, domain.ErrFlowGrantRevoked()
	}

	title := "Rollback order: " + order.DisplayNumber
	if order.Title != "" {
		title = "Rollback: " + order.Title
	}
	for len(title) > 256 {
		_, size := utf8.DecodeLastRuneInString(title)
		if size <= 0 {
			title = title[:256]
			break
		}
		title = title[:len(title)-size]
	}
	desc := "Rollback order automatically generated from verified rollback evidence of completed order " + order.DisplayNumber + "."

	plainBytes := []byte(rollbackSQL)
	mac, err := s.Crypter.HMAC("sql_fingerprint", plainBytes)
	if err != nil {
		return nil, err
	}
	plainHMAC := hex.EncodeToString(mac)
	stmtCount := int32(len(bundle.RollbackStatements))
	if stmtCount <= 0 {
		stmtCount = int32(len(bundle.Statements))
	}
	if stmtCount <= 0 {
		stmtCount = 1
	}

	draftID := newID()
	now := time.Now().UTC()
	if err := tx.WithContext(ctx).Exec(`
		insert into change_drafts (
			id, owner_user_id, flow_id, flow_version_seen, title, description,
			rollback_enabled, ticket_type, source_order_id, rollback_evidence_id,
			revision, state, version, created_at, updated_at
		) values (?, ?, ?, ?, ?, ?, false, 'rollback', ?, ?, 1, 'draft', 1, ?, ?)`,
		draftID, actor.UserID, flow.ID, flow.Version, title, desc,
		order.ID, ev.ID, now, now).Error; err != nil {
		return nil, err
	}

	env, err := s.Crypter.Seal(crypto.PurposePayload, plainBytes, crypto.AAD{
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
		plainHMAC, len(plainBytes), stmtCount, now).Error; err != nil {
		return nil, err
	}

	if err := s.Recorder.Audit(ctx, tx, auditlog.AuditInput{
		EventType: "workorder.rollback_draft_created", ActorKind: "user",
		ActorID: strPtrOrNil(actor.UserID), ActorUsername: strPtrOrNil(actor.Username),
		ResourceType: "change_order", ResourceID: strPtrOrNil(order.ID),
		Action: "create_rollback_draft", Outcome: "succeeded",
		RequestID: strPtrOrNil(meta.RequestID), SourceIP: strPtrOrNil(meta.SourceIP),
		UserAgent: strPtrOrNil(meta.UserAgent),
		Metadata: map[string]any{
			"source_order_id":      order.ID,
			"rollback_evidence_id": ev.ID,
			"draft_id":             draftID,
		},
	}); err != nil {
		return nil, err
	}
	return &CopyView{DraftID: draftID}, nil
}
