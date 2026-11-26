// read_views.go holds read domain helpers and operations.
package application

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/watermark"
	"gorm.io/gorm"
	"strconv"
	"strings"
	"time"
)

// StageView maps to the OrderStage schema.
type StageView struct {
	ID              string      `json:"id"`
	Position        int32       `json:"position"`
	DatasourceName  string      `json:"datasource_name"`
	State           string      `json:"state"`
	ApprovalSteps   []StepView  `json:"approval_steps"`
	ExecutionActors []ActorView `json:"execution_actors"`
}

// StepView approval step (free-form object in schema; fields read directly from frozen tables).
type StepView struct {
	ID        string      `json:"id"`
	Position  int32       `json:"position"`
	State     string      `json:"state"`
	DecidedAt *string     `json:"decided_at,omitempty"`
	Actors    []ActorView `json:"actors"`
}

// ActorView frozen Actor snapshot. Approval step items are open objects (contract inline object),
// whereas executor items reference the full User shape — is_builtin_admin/version/created_at/updated_at
// are all snapshot columns (F12 #006) carried uniformly for both Actor types.
type ActorView struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	DisplayName    string `json:"display_name"`
	IsBuiltinAdmin bool   `json:"is_builtin_admin"`
	Version        int64  `json:"version"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// relationFilterSQL is the work order relation predicate (change-order-relevant-actor:
// submitter / frozen approver / frozen executor), ?1=orderID, ?2=actor.
const relationFilterSQL = `(
	o.submitter_user_id_snapshot = @actor
	or exists (select 1 from order_approval_actors aa
	           join order_approval_steps st on st.id = aa.step_id
	           join order_stages sg on sg.id = st.stage_id
	           where sg.order_id = o.id and aa.reviewer_user_id_snapshot = @actor)
	or exists (select 1 from order_execution_actors ea
	           join order_stages sg2 on sg2.id = ea.stage_id
	           where sg2.order_id = o.id and ea.executor_user_id_snapshot = @actor))`

// CanRead verifies the actor's relation to the work order (returns false on failure without leaking existence).
func (s *Services) CanRead(ctx context.Context, db *gorm.DB, orderID, userID string) (bool, error) {
	var n int64
	err := db.WithContext(ctx).Raw(
		`select count(*) from change_orders o where o.id = @order and `+relationFilterSQL,
		map[string]any{"order": orderID, "actor": userID}).Scan(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// buildOrderView assembles a single work order view by delegating to buildOrderViews.
func (s *Services) buildOrderView(ctx context.Context, db *gorm.DB, orderID string) (*OrderView, error) {
	var o persistence.ChangeOrderModel
	if err := db.WithContext(ctx).Where("id = ?", orderID).First(&o).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	views, err := s.buildOrderViews(ctx, db, []persistence.ChangeOrderModel{o})
	if err != nil {
		return nil, err
	}
	return views[0], nil
}

func (s *Services) loadApprovalActorsByStep(ctx context.Context, db *gorm.DB, stageIDs []string) (map[string][]persistence.OrderApprovalActorModel, error) {
	out := map[string][]persistence.OrderApprovalActorModel{}
	if len(stageIDs) == 0 {
		return out, nil
	}
	var rows []persistence.OrderApprovalActorModel
	if err := db.WithContext(ctx).Raw(`
		select aa.step_id::text as step_id, aa.reviewer_user_id_snapshot::text as reviewer_user_id_snapshot,
		       aa.username_snapshot, aa.display_name_snapshot
		from order_approval_actors aa
		join order_approval_steps st on st.id = aa.step_id
		where st.stage_id in ?
		order by aa.reviewer_user_id_snapshot`, stageIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.StepID] = append(out[r.StepID], r)
	}
	return out, nil
}

func (s *Services) loadExecutionActorsByStage(ctx context.Context, db *gorm.DB, stageIDs []string) (map[string][]persistence.OrderExecutionActorModel, error) {
	out := map[string][]persistence.OrderExecutionActorModel{}
	if len(stageIDs) == 0 {
		return out, nil
	}
	var rows []persistence.OrderExecutionActorModel
	if err := db.WithContext(ctx).Raw(`
		select stage_id::text as stage_id, executor_user_id_snapshot::text as executor_user_id_snapshot,
		       username_snapshot, display_name_snapshot,
		       is_builtin_admin_snapshot, user_version_snapshot,
		       user_created_at_snapshot, user_updated_at_snapshot
		from order_execution_actors where stage_id in ?
		order by executor_user_id_snapshot`, stageIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.StageID] = append(out[r.StageID], r)
	}
	return out, nil
}

// OrderListFilter carries the optional server-side filters specified by listChangeOrders
// (RCP-20260831-ORDER-LIST-FILTER). All filters narrow relationFilterSQL and never widen authorization.
type OrderListFilter struct {
	State                string
	Q                    string
	Datasource           string
	SubmittedFrom        *time.Time
	SubmittedToExclusive *time.Time
}

func escapeLikeLiteral(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListOrders performs relation filtering + optional server-side narrowing + cursor pagination (limit+1 prefetch; returns next page cursor).
func (s *Services) ListOrders(ctx context.Context, actor Actor, limit int, after string, filters ...OrderListFilter) ([]*OrderView, *string, error) {
	args := map[string]any{"actor": actor.UserID}
	where := relationFilterSQL
	if len(filters) > 0 {
		f := filters[0]
		if f.State != "" {
			where += ` and o.state = @state`
			args["state"] = f.State
		}
		if f.Q != "" {
			where += ` and (o.display_number ilike @q escape '\' or coalesce(o.title, '') ilike @q escape '\')`
			args["q"] = "%" + escapeLikeLiteral(f.Q) + "%"
		}
		if f.Datasource != "" {
			where += ` and exists (select 1 from order_stages sg_ds where sg_ds.order_id = o.id and sg_ds.datasource_name_snapshot = @datasource)`
			args["datasource"] = f.Datasource
		}
		if f.SubmittedFrom != nil {
			where += ` and o.submitted_at >= @submitted_from`
			args["submitted_from"] = f.SubmittedFrom.UTC()
		}
		if f.SubmittedToExclusive != nil {
			where += ` and o.submitted_at < @submitted_to_end`
			args["submitted_to_end"] = f.SubmittedToExclusive.UTC()
		}
	}
	if after != "" {
		ts, id, err := pagecursor.DecodeUUID(after)
		if err != nil {
			return nil, nil, domain.ErrValidation()
		}
		where += ` and (o.submitted_at, o.id) < (CAST(@ts AS timestamptz), CAST(@id AS uuid))`
		args["ts"], args["id"] = ts.Format(pagecursor.Format), id
	}
	var rows []persistence.ChangeOrderModel
	if err := s.DB.WithContext(ctx).Raw(`
		select o.* from change_orders o where `+where+`
		order by o.submitted_at desc, o.id desc limit `+strconv.Itoa(limit+1), args).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out, err := s.buildOrderViews(ctx, s.DB, rows)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		cur := pagecursor.Encode(last.SubmittedAt, last.ID)
		next = &cur
	}
	return out, next, nil
}

// GetOrder reads a single work order (relation check + invalid materialization to reflect Actor deletion facts).
func (s *Services) GetOrder(ctx context.Context, actor Actor, orderID string) (*OrderView, error) {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotFound()
	}
	// W008: materialize invalid on read (writes only when the frozen Actor set has been emptied).
	err = s.serializable(ctx, func(tx *gorm.DB) error {
		order, err := s.loadOrderForUpdate(ctx, tx, orderID)
		if err != nil {
			return err
		}
		return s.materializeInvalidIfNeeded(ctx, tx, order)
	})
	if err != nil {
		return nil, err
	}
	return s.buildOrderView(ctx, s.DB, orderID)
}

// FindingView maps to the ReviewFinding schema (read directly from frozen snapshots on the order side).
type FindingView struct {
	ID                 string   `json:"id"`
	StagePosition      int32    `json:"stage_position"`
	FingerprintGroupID *string  `json:"fingerprint_group_id"`
	Category           string   `json:"category"`
	Severity           string   `json:"severity"`
	Title              string   `json:"title"`
	Message            string   `json:"message"`
	Suggestion         *string  `json:"suggestion"`
	ModelConfidence    *float64 `json:"model_confidence"`
	EvidenceIDs        []string `json:"evidence_ids"`
}

// ListOrderFindings reads findings from frozen stage snapshots (R003 reuse; zero Provider calls).
func (s *Services) ListOrderFindings(ctx context.Context, actor Actor, orderID string, limit int, after string, meta ...RequestMeta) ([]*FindingView, *string, error) {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, domain.ErrNotFound()
	}
	q := `
		select f.id::text as id, snap.review_run_id::text as run_id, sg.position as stage_position,
		       f.fingerprint_group_id::text as fingerprint_group_id, f.category, f.severity,
		       f.title, f.message, f.suggestion, f.model_confidence, f.created_at
		from review_findings f
		join stage_review_snapshots snap on snap.id = f.stage_review_id
		join order_stages sg on sg.stage_review_snapshot_id = snap.id
		where sg.order_id = ?`
	args := []any{orderID}
	if after != "" {
		created, id, err := pagecursor.DecodeUUID(after)
		if err != nil {
			return nil, nil, domain.ErrValidation()
		}
		q += ` and (f.created_at, f.id) < (?, ?::uuid)`
		args = append(args, created, id)
	}
	q += ` order by f.created_at desc, f.id desc limit ?`
	args = append(args, limit+1)
	var rows []struct {
		RunID              string
		ID                 string
		StagePosition      int32
		FingerprintGroupID *string
		Category           string
		Severity           string
		Title              string
		Message            string
		Suggestion         *string
		ModelConfidence    *float64
		CreatedAt          time.Time
	}
	if err := s.DB.WithContext(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	evMap := map[string][]string{}
	if len(ids) > 0 {
		var evRows []struct {
			FindingID  string
			EvidenceID string
		}
		if err := s.DB.WithContext(ctx).Raw(`
			select finding_id::text as finding_id, evidence_fact_id::text as evidence_id
			from review_finding_evidence where finding_id in ? order by evidence_fact_id`, ids).Scan(&evRows).Error; err != nil {
			return nil, nil, err
		}
		for _, r := range evRows {
			evMap[r.FindingID] = append(evMap[r.FindingID], r.EvidenceID)
		}
	}
	restorer := newOrderIdentifierDisplay(s, actor, meta)
	out := make([]*FindingView, 0, len(rows))
	for _, r := range rows {
		ev := evMap[r.ID]
		if ev == nil {
			ev = []string{}
		}
		restored, err := restorer.restore(ctx, r.RunID, &FindingView{
			ID: r.ID, StagePosition: r.StagePosition, FingerprintGroupID: r.FingerprintGroupID,
			Category: r.Category, Severity: r.Severity, Title: r.Title, Message: r.Message,
			Suggestion: r.Suggestion, ModelConfidence: r.ModelConfidence,
			EvidenceIDs: ev,
		})
		if err != nil {
			return nil, nil, err
		}
		out = append(out, restored)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		cur := pagecursor.Encode(last.CreatedAt, last.ID)
		next = &cur
	}
	if err := restorer.audit(ctx); err != nil {
		return nil, nil, err
	}
	return out, next, nil
}

// SQLRevealResult plaintext Reveal response.
type SQLRevealResult struct {
	RevealID   string
	SQL        string
	Watermark  string
	ValidUntil string
}

// RevealOrderSQL decrypts work order SQL plaintext (relation check; no-store set by HTTP layer;
// writes an audit record on every Reveal). When purpose is rollback-order-sql, decrypts the rollback SQL from rollback evidence.
func (s *Services) RevealOrderSQL(ctx context.Context, actor Actor, meta RequestMeta, orderID, purpose string) (*SQLRevealResult, error) {
	if purpose == "rollback-order-sql" {
		return s.RevealOrderRollbackSQL(ctx, actor, meta, orderID, purpose)
	}
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotFound()
	}
	var payload persistence.OrderSQLPayloadModel
	if err := s.DB.WithContext(ctx).Where("order_id = ?", orderID).First(&payload).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, domain.ErrNotFound()
		}
		return nil, err
	}
	plaintext, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "order_sql_payloads",
		PrimaryKey: orderID,
		Column:     "sql_ciphertext",
	}, crypto.Envelope{
		Ciphertext: payload.SQLCiphertext, Nonce: payload.SQLNonce,
		KeyID: payload.SQLKeyID, Algorithm: payload.SQLAlgorithm,
	})
	if err != nil {
		return nil, err
	}
	mac, err := s.Crypter.HMAC("sql_fingerprint", plaintext)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hex.EncodeToString(mac), payload.PlaintextHMAC) {
		return nil, domain.ErrSubmissionGate("sql_hmac_mismatch")
	}
	revealID := newID()
	now := nowUTC()
	res := &SQLRevealResult{
		RevealID:   revealID,
		SQL:        string(plaintext),
		Watermark:  watermark.SQLViewer(actor.Username, now),
		ValidUntil: now.Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if err := s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "workorder.sql_revealed", ActorKind: "user",
		ActorID: ptr.NonZero(actor.UserID), ActorUsername: ptr.NonZero(actor.Username),
		ResourceType: "change_order", ResourceID: ptr.NonZero(orderID),
		Action: "reveal", Outcome: "succeeded",
		RequestID: ptr.NonZero(meta.RequestID), SourceIP: ptr.NonZero(meta.SourceIP),
		UserAgent: ptr.NonZero(meta.UserAgent),
		Metadata:  map[string]any{"reveal_id": revealID, "purpose": purpose},
	}); err != nil {
		return nil, err
	}
	return res, nil
}

// RecordSQLCopy records a copy event (without storing copied content; binds the reveal audit ID).
// SEC-0035: source reveal ownership check — the referenced reveal must be a Reveal audit record
// issued to the current user for this work order, preventing arbitrary reveal_id cross-linking in the audit chain.
func (s *Services) RecordSQLCopy(ctx context.Context, actor Actor, meta RequestMeta, orderID, revealID string) error {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrNotFound()
	}
	if err := s.verifyRevealOwnership(ctx, actor.UserID, orderID, revealID); err != nil {
		return err
	}
	return s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "workorder.sql_copied", ActorKind: "user",
		ActorID: ptr.NonZero(actor.UserID), ActorUsername: ptr.NonZero(actor.Username),
		ResourceType: "change_order", ResourceID: ptr.NonZero(orderID),
		Action: "copy", Outcome: "succeeded",
		RequestID: ptr.NonZero(meta.RequestID), SourceIP: ptr.NonZero(meta.SourceIP),
		UserAgent: ptr.NonZero(meta.UserAgent),
		Metadata:  map[string]any{"reveal_id": revealID},
	})
}

// verifyRevealOwnership verifies that revealID was issued by a sql_revealed audit record
// for this user and work order (non-existent / foreign user / foreign order -> RESOURCE_NOT_FOUND without leaking existence).
func (s *Services) verifyRevealOwnership(ctx context.Context, userID, orderID, revealID string) error {
	if revealID == "" {
		return domain.ErrNotFound()
	}
	var n int64
	if err := s.DB.WithContext(ctx).Raw(`
		select count(*) from audit_events
		where event_type = 'workorder.sql_revealed'
		  and actor_kind = 'user' and actor_id = ?
		  and resource_type = 'change_order' and resource_id = ?
		  and metadata->>'reveal_id' = ?`,
		userID, orderID, revealID).Scan(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound()
	}
	return nil
}
