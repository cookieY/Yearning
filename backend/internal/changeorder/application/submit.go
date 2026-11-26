// submit.go Submit work order (submit_change_order command, serializable).
// All frozen surfaces are read and generated server-side from templates/Run snapshots,
// never trusting client-returned structures; non-Ready state, hash drift, gate failure,
// or missing template Actors all fail closed.
// The four-eyes principle (submitter cannot also be a frozen approver/executor) was
// repealed by RCP-20261031-SELF-ACTOR-ALLOWED — self-submit, self-approve, and self-execute are valid.
// See submit_v2.go for submission v2 (ephemeral Run materialization entry point).
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
)

// submitOnceInput holds internal submission command parameters passed by submitV2Tx.
type submitOnceInput struct {
	DraftID        string
	ExpectVersion  int64
	IdempotencyKey string
}

// acceptedRunRow is the frozen surface of an accepted (state=ready) Run.
type acceptedRunRow struct {
	ID               string
	DraftRevision    int64
	State            string
	FlowSnapshotHash string
	RuleConfigHash   string
	ToolConfigHash   string
	SQLPayloadHMAC   sql.NullString
}

// stageSnapRow is the submission binding surface of a single stage review snapshot.
type stageSnapRow struct {
	ID                  string
	DatasourceID        string
	State               string
	Severity            string
	GatePassed          bool
	Hash                string
	NormalizedContext   []byte
	NormalizedNonce     []byte
	NormalizedKeyID     string
	NormalizedAlgorithm string
}

// submitOnce command body: validate -> freeze -> activate -> emit events.
func (s *Services) submitOnce(ctx context.Context, tx *gorm.DB, actor Actor, in submitOnceInput) (*OrderView, error) {
	// 1. Draft row lock + ownership.
	var draft struct {
		ID              string
		OwnerUserID     string
		FlowID          string
		FlowVersionSeen int64
		Title           string
		Description     string
		// §18.56 Option B: submitter's expected execution time is frozen into the work order along with the snapshot.
		RequestedScheduledFor *time.Time
		RollbackEnabled       bool
		TargetDatabase        *string
		TicketType            string
		SourceOrderID         *string
		RollbackEvidenceID    *string
		Revision              int64
		State                 string
		Version               int64
	}
	if err := tx.WithContext(ctx).Raw(
		`select id::text as id, owner_user_id::text as owner_user_id, flow_id::text as flow_id,
		        flow_version_seen, title, description, requested_scheduled_for,
		        rollback_enabled, target_database, ticket_type, source_order_id::text as source_order_id,
		        rollback_evidence_id::text as rollback_evidence_id, revision, state, version
		 from change_drafts where id = ? for update`, in.DraftID).Scan(&draft).Error; err != nil {
		return nil, err
	}
	if draft.ID == "" {
		return nil, domain.ErrNotFound()
	}
	if draft.OwnerUserID != actor.UserID {
		return nil, domain.ErrNotFound()
	}
	// 2. State machine: only ready drafts can be submitted (draft side ready -> submitted).
	from := reviewdomain.DraftState(draft.State)
	next, err := reviewdomain.DraftTransition(from, reviewdomain.DraftActionSubmit)
	if err != nil {
		return nil, domain.ErrIllegal()
	}
	// 3. Version CAS precondition.
	if draft.Version != in.ExpectVersion {
		return nil, domain.ErrConcurrent()
	}
	// 4. Flow authorization is still valid (2014).
	granted, err := s.flowGranted(ctx, tx, actor.UserID, draft.FlowID)
	if err != nil {
		return nil, err
	}
	if !granted {
		return nil, domain.ErrFlowGrantRevoked()
	}
	// 5. Flow row and version drift check (2001).
	var flow struct {
		ID        string
		Name      string
		FlowType  string
		Status    string
		RuleSetID *string
		Version   int64
	}
	if err := tx.WithContext(ctx).Raw(
		`select id::text as id, name, flow_type, status, rule_set_id::text as rule_set_id, version
		 from flows where id = ?`, draft.FlowID).Scan(&flow).Error; err != nil {
		return nil, err
	}
	if flow.ID == "" {
		return nil, domain.ErrNotFound()
	}
	if flow.Status != "enabled" || flow.FlowType != "change_review" {
		return nil, domain.ErrSubmissionGate("flow_not_enabled")
	}
	if flow.Version != draft.FlowVersionSeen {
		return nil, domain.ErrReviewOutdated()
	}
	// 6. SQL payload exists and HMAC is valid.
	var payloadRow struct {
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
		from draft_sql_payloads where draft_id = ?`, in.DraftID).Scan(&payloadRow).Error; err != nil {
		return nil, err
	}
	if payloadRow.PlaintextHMAC == "" {
		return nil, domain.ErrSubmissionGate("sql_missing")
	}
	// 7. Latest accepted Run (state=ready).
	var run acceptedRunRow
	if err := tx.WithContext(ctx).Raw(`
		select r.id::text as id, r.draft_revision, r.state,
		       r.flow_snapshot_hash, r.rule_config_hash, r.tool_config_hash,
		       b.sql_payload_hmac as sql_payload_hmac
		from review_runs r
		left join review_run_sql_bindings b on b.review_run_id = r.id
		where r.draft_id = ? and r.state = 'ready'
		order by r.created_at desc, r.id desc limit 1`, in.DraftID).Scan(&run).Error; err != nil {
		return nil, err
	}
	if run.ID == "" {
		return nil, domain.ErrSubmissionGate("no_ready_run")
	}
	// 8. Four-way SQL hash consistency: Run binding == current draft payload.
	// Missing binding row (legacy Run prior to 000008) also fails closed: never submit unverified SQL.
	if !run.SQLPayloadHMAC.Valid || run.SQLPayloadHMAC.String == "" {
		return nil, domain.ErrSubmissionGate("sql_binding_missing")
	}
	if run.SQLPayloadHMAC.String != payloadRow.PlaintextHMAC {
		return nil, domain.ErrReviewOutdated()
	}
	// 9. Revision binding: the Revision at Run review time must equal the current draft Revision.
	if run.DraftRevision != draft.Revision {
		return nil, domain.ErrReviewOutdated()
	}
	// 10. Frozen input re-verification: flow structure / rule / combined hashes match the Run's frozen values.
	stages, err := s.loadFlowStageFacts(ctx, tx, draft.FlowID)
	if err != nil {
		return nil, err
	}
	if len(stages) == 0 {
		return nil, domain.ErrSubmissionGate("flow_stage_missing")
	}
	// 10.1 Engine consistency re-check (BUG-005 / Review F3): multi-stage change_review flows
	// must bind datasources of the exact same engine and compatibility mode across all stages.
	// Configuration-time Create/ReplaceFlow already fails closed, but two classes of legacy risk bypass configuration time:
	//   1. Mixed-engine flows existing prior to validation rollout;
	//   2. An admin subsequently changes a datasource's engine/compatibility_mode, silently turning
	//      an existing flow referencing it into a mixed-engine flow (flowHash matches after re-review, allowing submission).
	// Both cases would permanently stall at 3006 during second-stage execution preflight due to
	// dialect fingerprint mismatch, so re-check at submission freeze and fail closed with an explicit submission gate reason.
	if len(stages) >= 2 {
		first := stages[0]
		for _, st := range stages[1:] {
			if st.Engine != first.Engine || st.CompatibilityMode != first.CompatibilityMode {
				return nil, domain.ErrSubmissionGate("mixed_engine_flow")
			}
		}
	}
	stepsByPos, err := s.loadFlowApprovalSteps(ctx, tx, draft.FlowID)
	if err != nil {
		return nil, err
	}
	execByStage, err := s.loadFlowExecutionActors(ctx, tx, draft.FlowID)
	if err != nil {
		return nil, err
	}
	// Approval union is flattened and appended in (fs.position, st.position, user_id) order —
	// byte-identical to the frozenHashes reference implementation: duplicates are preserved when
	// the same approver appears in multiple steps of the same stage (deduplication would cause
	// flowHash drift and prevent valid flows from ever being submitted).
	approvalUnion := map[int][]string{}
	for pos, steps := range stepsByPos {
		for _, st := range steps {
			approvalUnion[pos] = append(approvalUnion[pos], st.Actors...)
		}
	}
	flowHash, err := computeFlowSnapshotHash(draft.FlowID, flow.Version, stages, approvalUnion)
	if err != nil {
		return nil, err
	}
	if flowHash != run.FlowSnapshotHash {
		return nil, domain.ErrReviewOutdated()
	}
	ruleHash, toolHash, err := s.computeRuleConfigHashes(ctx, tx, flow.RuleSetID)
	if err != nil {
		return nil, err
	}
	if ruleHash != run.RuleConfigHash || toolHash != run.ToolConfigHash {
		return nil, domain.ErrReviewOutdated()
	}
	// 11. Direct binding of each stage snapshot: state=passed, Gate passed, zero high/critical findings,
	// and datasource matches (Order Stage direct Review binding deliverable).
	snapshots := map[int]stageSnapRow{}
	var snapRows []struct {
		StagePosition       int
		ID                  string
		DatasourceID        string
		State               string
		Severity            string
		GatePassed          bool
		Hash                string
		NormalizedContext   []byte
		NormalizedNonce     []byte
		NormalizedKeyID     string
		NormalizedAlgorithm string
	}
	if err := tx.WithContext(ctx).Raw(`
		select stage_position, id::text as id, datasource_id::text as datasource_id,
		       state, highest_severity as severity, gate_passed, snapshot_hash as hash,
		       normalized_context_ciphertext as normalized_context,
		       normalized_context_nonce as normalized_nonce,
		       normalized_context_key_id as normalized_key_id,
		       normalized_context_algorithm as normalized_algorithm
		from stage_review_snapshots where review_run_id = ? order by stage_position`, run.ID).
		Scan(&snapRows).Error; err != nil {
		return nil, err
	}
	for _, r := range snapRows {
		snapshots[r.StagePosition] = stageSnapRow{
			ID: r.ID, DatasourceID: r.DatasourceID, State: r.State,
			Severity: r.Severity, GatePassed: r.GatePassed, Hash: r.Hash,
			NormalizedContext: r.NormalizedContext, NormalizedNonce: r.NormalizedNonce,
			NormalizedKeyID: r.NormalizedKeyID, NormalizedAlgorithm: r.NormalizedAlgorithm,
		}
	}
	for _, st := range stages {
		snap, ok := snapshots[st.Position]
		if !ok {
			return nil, domain.ErrSubmissionGate("stage_review_missing")
		}
		if snap.State != "passed" || !snap.GatePassed {
			return nil, domain.ErrSubmissionGate("stage_review_not_passed")
		}
		if snap.Severity == "high" || snap.Severity == "critical" {
			return nil, domain.ErrSubmissionGate("severity_blocked")
		}
		if snap.DatasourceID != st.DatasourceID {
			return nil, domain.ErrSubmissionGate("stage_datasource_mismatch")
		}
		if st.IdentityFP == nil || *st.IdentityFP == "" {
			return nil, domain.ErrSubmissionGate("datasource_identity_missing")
		}
		if err := s.verifySubmitPostgresSearchPath(ctx, run.ID, snap, st); err != nil {
			return nil, err
		}
		if _, ok := stepsByPos[st.Position]; !ok || len(stepsByPos[st.Position]) == 0 {
			return nil, domain.ErrSubmissionGate("approval_step_missing")
		}
		if len(execByStage[st.StageID]) == 0 {
			return nil, domain.ErrSubmissionGate("execution_actor_missing")
		}
	}
	// 12. All template Actors exist (all alive at freeze time; post-submission deletion follows the W008 path).
	if err := s.assertFlowActorsExist(ctx, tx, stepsByPos, execByStage); err != nil {
		return nil, err
	}
	// 12b. Separation of duties does not enforce a system-level four-eyes gate (RCP-20261031-SELF-ACTOR-ALLOWED,
	// superseding RCP-20261021-FOUR-EYES-PRINCIPLE): the submitter is allowed to serve as a frozen
	// approver/executor on the same work order — self-submit, self-approve, and self-execute are valid;
	// neither the approval side (decide frozen step member check) nor the execution side (begin frozen
	// executor check) excludes the submitter; dual-person review is governed by flow template design
	// (excluding the submitter from actor lists), while full-chain auditing records the operator as usual.
	// 13. Submitter snapshot.
	var submitter struct {
		Username    string
		DisplayName string
	}
	if err := tx.WithContext(ctx).Raw(
		`select username, display_name from users where id = ?`, actor.UserID).Scan(&submitter).Error; err != nil {
		return nil, err
	}
	if submitter.Username == "" {
		return nil, domain.ErrNotFound()
	}

	// ---- Freeze Writes ----
	plainSQL, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "draft_sql_payloads",
		PrimaryKey: in.DraftID,
		Column:     "sql_ciphertext",
	}, crypto.Envelope{
		Ciphertext: payloadRow.SQLCiphertext, Nonce: payloadRow.SQLNonce,
		KeyID: payloadRow.SQLKeyID, Algorithm: payloadRow.SQLAlgorithm,
	})
	if err != nil {
		return nil, err
	}

	now := nowUTC()
	ticketType := draft.TicketType
	if ticketType == "" {
		ticketType = "normal"
	}
	rollbackEnabled := draft.RollbackEnabled
	if ticketType == "rollback" {
		rollbackEnabled = false
		if draft.SourceOrderID == nil || *draft.SourceOrderID == "" {
			return nil, domain.ErrIllegal()
		}
		if err := s.assertRollbackSubmissionAllowed(
			ctx, tx, *draft.SourceOrderID, draft.RollbackEvidenceID,
			payloadRow.PlaintextHMAC, plainSQL, now,
		); err != nil {
			return nil, err
		}
	}
	rollbackStatus := "not_requested"
	if rollbackEnabled && ticketType == "normal" {
		rollbackStatus = "pending"
	}

	orderID := newID()
	displayNumber, err := s.nextDisplayNumber(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Create(&persistence.ChangeOrderModel{
		ID: orderID, DisplayNumber: displayNumber,
		SubmitterUserIDSnapshot: actor.UserID, SubmitterUsernameSnapshot: submitter.Username,
		SubmitterDisplayNameSnapshot: submitter.DisplayName,
		Title:                        draft.Title, Description: draft.Description,
		RequestedScheduledFor: draft.RequestedScheduledFor,
		RollbackEnabled:       rollbackEnabled,
		TicketType:            ticketType,
		SourceOrderID:         draft.SourceOrderID,
		RollbackEvidenceID:    draft.RollbackEvidenceID,
		RollbackStatus:        rollbackStatus,
		State:                 string(domain.StateStageApproval), CurrentStagePosition: int32Ptr(1),
		SQLHMAC: payloadRow.PlaintextHMAC, SnapshotHash: flowHash,
		Version: 1, SubmittedAt: now,
	}).Error; err != nil {
		return nil, err
	}
	// Complete flow snapshot (frozen template structure; AEAD bound to the order row).
	frozenFlow, err := s.buildFrozenFlow(draft.FlowID, flow.Name, flow.Version, flow.RuleSetID, stages, stepsByPos, execByStage)
	if err != nil {
		return nil, err
	}
	flowJSON, err := json.Marshal(frozenFlow)
	if err != nil {
		return nil, err
	}
	flowEnv, err := s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "order_flow_snapshots",
		PrimaryKey: orderID,
		Column:     "flow_snapshot_ciphertext",
	}, flowJSON)
	if err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Create(&persistence.OrderFlowSnapshotModel{
		OrderID: orderID, FlowSnapshotCiphertext: flowEnv.Ciphertext,
		FlowSnapshotNonce: flowEnv.Nonce, FlowSnapshotKeyID: flowEnv.KeyID,
		FlowSnapshotAlgorithm: flowEnv.Algorithm, SnapshotHash: flowHash, CreatedAt: now,
	}).Error; err != nil {
		return nil, err
	}
	// Re-seal SQL payload (AAD migrated to the order table; plaintext only passes through server memory).
	sqlEnv, err := s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "order_sql_payloads",
		PrimaryKey: orderID,
		Column:     "sql_ciphertext",
	}, plainSQL)
	if err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Create(&persistence.OrderSQLPayloadModel{
		OrderID: orderID, SQLCiphertext: sqlEnv.Ciphertext, SQLNonce: sqlEnv.Nonce,
		SQLKeyID: sqlEnv.KeyID, SQLAlgorithm: sqlEnv.Algorithm,
		PlaintextHMAC: payloadRow.PlaintextHMAC,
		SizeBytes:     payloadRow.SizeBytes, StatementCount: payloadRow.StatementCount,
		CreatedAt: now,
	}).Error; err != nil {
		return nil, err
	}
	// Stages + approval steps + frozen Actors.
	actorNames, err := s.userDisplayNames(ctx, tx, stepsByPos, execByStage)
	if err != nil {
		return nil, err
	}
	for _, st := range stages {
		snap := snapshots[st.Position]
		stageID := newID()
		// Target snapshot: datasource identity. RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS:
		// stages no longer carry schema mappings, so mapping ciphertexts are removed from the target snapshot;
		// execution-time object scope is determined by the datasource default database (MySQL) or
		// the PostgreSQL search_path context frozen during review.
		targetEnv, err := domain.SealTargetSnapshot(s.Crypter, stageID, domain.TargetSnapshot{
			DatasourceID:        st.DatasourceID,
			Engine:              st.Engine,
			IdentityFingerprint: ptr.Deref(st.IdentityFP),
			TargetDatabase:      ptr.Deref(draft.TargetDatabase),
		})
		if err != nil {
			return nil, err
		}
		stageState := domain.StagePending
		var activatedAt *time.Time
		if st.Position == 1 {
			stageState = domain.StageApproval
			t := now
			activatedAt = &t
		}
		if err := tx.WithContext(ctx).Create(&persistence.OrderStageModel{
			ID: stageID, OrderID: orderID, Position: int32(st.Position),
			DatasourceID: st.DatasourceID, DatasourceNameSnapshot: st.Name,
			DatasourceEngineSnapshot: st.Engine, DatasourceIdentityFingerprint: ptr.Deref(st.IdentityFP),
			StageReviewSnapshotID: snap.ID, ReviewSnapshotHash: snap.Hash, ReviewGatePassed: true,
			TargetSnapshotCiphertext: targetEnv.Ciphertext, TargetSnapshotNonce: targetEnv.Nonce,
			TargetSnapshotKeyID: targetEnv.KeyID, TargetSnapshotAlgorithm: targetEnv.Algorithm,
			State: string(stageState), Version: 1, ActivatedAt: activatedAt,
		}).Error; err != nil {
			return nil, err
		}
		for i, step := range stepsByPos[st.Position] {
			stepID := newID()
			stepState := domain.StepPending
			if st.Position == 1 && i == 0 {
				stepState = domain.StepActive
			}
			if err := tx.WithContext(ctx).Create(&persistence.OrderApprovalStepModel{
				ID: stepID, StageID: stageID, Position: int32(step.Position),
				State: string(stepState),
			}).Error; err != nil {
				return nil, err
			}
			for _, uid := range step.Actors {
				name := actorNames[uid]
				if err := tx.WithContext(ctx).Create(&persistence.OrderApprovalActorModel{
					StepID: stepID, ReviewerUserIDSnapshot: uid,
					UsernameSnapshot: name.Username, DisplayNameSnapshot: name.DisplayName,
				}).Error; err != nil {
					return nil, err
				}
			}
		}
		for _, uid := range execByStage[st.StageID] {
			name := actorNames[uid]
			if err := tx.WithContext(ctx).Create(&persistence.OrderExecutionActorModel{
				StageID: stageID, ExecutorUserIDSnapshot: uid,
				UsernameSnapshot: name.Username, DisplayNameSnapshot: name.DisplayName,
				IsBuiltinAdminSnapshot: name.IsBuiltinAdmin, UserVersionSnapshot: name.UserVersion,
				UserCreatedAtSnapshot: name.UserCreatedAt, UserUpdatedAtSnapshot: name.UserUpdatedAt,
			}).Error; err != nil {
				return nil, err
			}
		}
	}
	// 14. draft -> submitted (optimistic locking).
	res := tx.WithContext(ctx).Exec(`
		update change_drafts set state = ?, version = version + 1, updated_at = ?
		where id = ? and version = ?`, string(next), now, in.DraftID, in.ExpectVersion)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, domain.ErrConcurrent()
	}
	// 15. Domain event (submit_change_order.events only declares
	// change_order.submitted; flipping draft state does not emit a separate event — the routing table
	// has no corresponding internal event, and the command contract is authoritative).
	auditActor := auditlog.UserActor(actor.UserID, actor.Username)
	if err := s.emitOrderEvent(ctx, tx, EventOrderSubmitted, orderID, auditActor, OrderSubmittedPayload{
		OrderID: orderID, DisplayNumber: displayNumber, SubmitterUserID: actor.UserID,
		StageCount: len(stages), SnapshotHash: flowHash, AggregateVersion: 1,
	}); err != nil {
		return nil, err
	}
	return s.buildOrderView(ctx, tx, orderID)
}

// assertFlowActorsExist verifies that all users referenced by the template exist
// (all_template_actors_exist precondition).
func (s *Services) assertFlowActorsExist(ctx context.Context, tx *gorm.DB,
	stepsByPos map[int][]flowStepActors, execByStage map[string][]string) error {
	ids := map[string]bool{}
	for _, steps := range stepsByPos {
		for _, st := range steps {
			for _, uid := range st.Actors {
				ids[uid] = true
			}
		}
	}
	for _, actors := range execByStage {
		for _, uid := range actors {
			ids[uid] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	list := make([]string, 0, len(ids))
	for uid := range ids {
		list = append(list, uid)
	}
	var n int64
	if err := tx.WithContext(ctx).Raw(
		`select count(*) from users where id in ?`, list).Scan(&n).Error; err != nil {
		return err
	}
	if n != int64(len(list)) {
		return domain.ErrSubmissionGate("template_actor_missing")
	}
	return nil
}

// userDisplayNames batch-reads usernames and display names (for snapshot freezing).
type actorSnapshot struct {
	Username, DisplayName string
	IsBuiltinAdmin        bool
	UserVersion           int64
	UserCreatedAt         time.Time
	UserUpdatedAt         time.Time
}

func (s *Services) userDisplayNames(ctx context.Context, tx *gorm.DB,
	stepsByPos map[int][]flowStepActors, execByStage map[string][]string) (map[string]actorSnapshot, error) {
	ids := map[string]bool{}
	for _, steps := range stepsByPos {
		for _, st := range steps {
			for _, uid := range st.Actors {
				ids[uid] = true
			}
		}
	}
	for _, actors := range execByStage {
		for _, uid := range actors {
			ids[uid] = true
		}
	}
	out := map[string]actorSnapshot{}
	if len(ids) == 0 {
		return out, nil
	}
	list := make([]string, 0, len(ids))
	for uid := range ids {
		list = append(list, uid)
	}
	var rows []struct {
		ID            string
		Username      string
		DisplayName   string
		IsBuiltinAdm  bool
		UserVersion   int64
		UserCreatedAt time.Time
		UserUpdatedAt time.Time
	}
	if err := tx.WithContext(ctx).Raw(
		`select id::text as id, username, display_name,
		        is_builtin_admin as is_builtin_adm, version as user_version,
		        created_at as user_created_at, updated_at as user_updated_at
		 from users where id in ?`, list).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = actorSnapshot{r.Username, r.DisplayName, r.IsBuiltinAdm, r.UserVersion, r.UserCreatedAt, r.UserUpdatedAt}
	}
	return out, nil
}

// nextDisplayNumber generates display number YR-YYYYMMDD-NNNNNN
// (max+1 within serializable transaction; unique constraint backs concurrency).
func (s *Services) nextDisplayNumber(ctx context.Context, tx *gorm.DB, at time.Time) (string, error) {
	prefix := "YR-" + at.UTC().Format("20060102") + "-"
	var maxSeq sql.NullInt64
	if err := tx.WithContext(ctx).Raw(
		`select max(substring(display_number from 13 for 6)::int) from change_orders where display_number like ?`,
		prefix+"%").Scan(&maxSeq).Error; err != nil {
		return "", err
	}
	seq := int64(1)
	if maxSeq.Valid {
		seq = maxSeq.Int64 + 1
	}
	if seq > 999999 {
		return "", errors.New("changeorder: display number exhausted for day")
	}
	return fmt.Sprintf("%s%06d", prefix, seq), nil
}

// frozenFlowDocument is the complete frozen structure inside the order_flow_snapshots ciphertext
// (read-only display for admin/troubleshooting; execution reads column snapshots and does not rely on this document for authorization).
type frozenFlowDocument struct {
	FlowID      string            `json:"flow_id"`
	FlowName    string            `json:"flow_name,omitempty"`
	FlowVersion int64             `json:"flow_version"`
	RuleSetID   *string           `json:"rule_set_id"`
	Stages      []frozenFlowStage `json:"stages"`
}

type frozenFlowStage struct {
	Position        int              `json:"position"`
	Name            string           `json:"name,omitempty"`
	DatasourceID    string           `json:"datasource_id"`
	Engine          string           `json:"engine"`
	IdentityFP      string           `json:"identity_fingerprint"`
	ApprovalSteps   []frozenFlowStep `json:"approval_steps"`
	ExecutionActors []string         `json:"execution_actors"`
}

type frozenFlowStep struct {
	Position int      `json:"position"`
	Actors   []string `json:"actors"`
}

func (s *Services) buildFrozenFlow(flowID, flowName string, flowVersion int64, ruleSetID *string,
	stages []stageFact, stepsByPos map[int][]flowStepActors, execByStage map[string][]string) (*frozenFlowDocument, error) {
	doc := &frozenFlowDocument{FlowID: flowID, FlowName: flowName, FlowVersion: flowVersion, RuleSetID: ruleSetID}
	for _, st := range stages {
		fs := frozenFlowStage{
			Position: st.Position, Name: st.Name, DatasourceID: st.DatasourceID, Engine: st.Engine,
			IdentityFP: ptr.Deref(st.IdentityFP),
		}
		for _, step := range stepsByPos[st.Position] {
			fs.ApprovalSteps = append(fs.ApprovalSteps, frozenFlowStep{Position: step.Position, Actors: step.Actors})
		}
		fs.ExecutionActors = execByStage[st.StageID]
		doc.Stages = append(doc.Stages, fs)
	}
	return doc, nil
}

func int32Ptr(v int32) *int32 { return &v }
