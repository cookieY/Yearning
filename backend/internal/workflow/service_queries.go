// service_queries.go holds service domain helpers and operations.
package workflow

import (
	"context"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
	"time"
)

// NewService constructs a workflow Service.
func NewService(db *gorm.DB, crypter *crypto.Crypter, audit *auditlog.Recorder) (*Service, error) {
	if db == nil || crypter == nil || audit == nil {
		return nil, errors.New("workflow: nil dependency")
	}
	return &Service{db: db, crypter: crypter, audit: audit}, nil
}

// Recorder returns the attached auditlog.Recorder.
func (s *Service) Recorder() *auditlog.Recorder {
	if s == nil {
		return nil
	}
	return s.audit
}

// referencedIDs collects all user, datasource, and rule set IDs referenced by the write payload.
func (w *FlowWrite) referencedIDs() (users, datasources []string, ruleSet string) {
	seenU, seenD := map[string]bool{}, map[string]bool{}
	addU := func(id string) {
		if id != "" && !seenU[id] {
			seenU[id] = true
			users = append(users, id)
		}
	}
	for i := range w.Stages {
		st := &w.Stages[i]
		if !seenD[st.DatasourceID] {
			seenD[st.DatasourceID] = true
			datasources = append(datasources, st.DatasourceID)
		}
		for _, s := range st.ApprovalSteps {
			for _, a := range s.Actors {
				addU(a.UserID)
			}
		}
		for _, a := range st.ExecutionActors {
			addU(a.UserID)
		}
	}
	for _, s := range w.ApprovalSteps {
		for _, a := range s.Actors {
			addU(a.UserID)
		}
	}
	for _, q := range w.QueryCapabilities {
		if !seenD[q.DatasourceID] {
			seenD[q.DatasourceID] = true
			datasources = append(datasources, q.DatasourceID)
		}
	}
	if w.RuleSetID != nil {
		ruleSet = *w.RuleSetID
	}
	// Lock order: resource_type ascending + UUID lexicographical order (per repository-commands lock_order).
	sort.Strings(users)
	sort.Strings(datasources)
	return users, datasources, ruleSet
}

// lockReferences acquires FOR KEY SHARE locks on referenced rows within the transaction and verifies existence and enabled status.
// PostgreSQL does not allow combining FOR KEY SHARE with aggregate queries, so we select row IDs to check existence.
func lockReferences(ctx context.Context, tx *gorm.DB, users, datasources []string, ruleSet string) error {
	for _, uid := range users {
		var ids []string
		if err := tx.WithContext(ctx).Model(&UserRow{}).
			Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Where("id = ?", uid).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) != 1 {
			return bizerr.ErrValidationFailed
		}
	}
	for _, dsID := range datasources {
		var ids []string
		if err := tx.WithContext(ctx).Model(&DatasourceRow{}).
			Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Where("id = ? and enabled = true", dsID).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) != 1 {
			return bizerr.ErrValidationFailed
		}
	}
	if ruleSet != "" {
		var ids []string
		if err := tx.WithContext(ctx).Model(&RuleSetRow{}).
			Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Where("id = ?", ruleSet).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) != 1 {
			return bizerr.ErrValidationFailed
		}
	}
	return nil
}

func (UserRow) TableName() string { return "users" }

func (DatasourceRow) TableName() string { return "datasources" }

func (RuleSetRow) TableName() string { return "rule_sets" }

// sealSchemaMapping seals a placeholder envelope for stage schema mappings. Per RCP-20261031-REMOVE-
// FLOW-SCHEMA-MAPPINGS: flow stages no longer configure logical-to-physical schema mappings and new writes
// always seal an empty array; the schema_mapping_* encrypted columns and AAD structure are retained (zero migration)
// so historical envelopes on existing rows can still be verified under the original AAD, while readers no longer
// consume mapping contents as active configuration (both review and execution follow the unmapped branch).
func (s *Service) sealSchemaMapping(flowStageID string) (ct, nonce []byte, keyID string, err error) {
	env, err := s.crypter.SealColumn(crypto.PurposeFlowSchemaMapping, crypto.ColumnRef{
		Table:      "flow_stages",
		PrimaryKey: flowStageID,
		Column:     "schema_mapping",
	}, []byte("[]"))
	if err != nil {
		return nil, nil, "", err
	}
	return env.Ciphertext, env.Nonce, env.KeyID, nil
}

// writeFlowGraph writes stages, approval steps, actors, and query capabilities within a transaction (full rebuild).
func (s *Service) writeFlowGraph(ctx context.Context, tx *gorm.DB, flowID string, w *FlowWrite) error {
	for i := range w.Stages {
		st := &w.Stages[i]
		stageID := uuid.NewString()
		ct, nonce, keyID, err := s.sealSchemaMapping(stageID)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(&FlowStageModel{
			ID: stageID, FlowID: flowID, Position: st.Position,
			DatasourceID:            st.DatasourceID,
			SchemaMappingCiphertext: ct, SchemaMappingNonce: nonce,
			SchemaMappingKeyID: keyID, SchemaMappingAlgorithm: crypto.EnvelopeAlgorithm,
		}).Error; err != nil {
			return err
		}
		if err := s.writeSteps(ctx, tx, flowID, &stageID, st.ApprovalSteps); err != nil {
			return err
		}
		for _, actor := range st.ExecutionActors {
			if err := tx.WithContext(ctx).Create(&FlowExecutionActorModel{
				StageID: stageID, UserID: actor.UserID,
			}).Error; err != nil {
				return err
			}
		}
	}
	if err := s.writeSteps(ctx, tx, flowID, nil, w.ApprovalSteps); err != nil {
		return err
	}
	for _, q := range w.QueryCapabilities {
		if err := tx.WithContext(ctx).Create(&FlowQueryCapabilityModel{
			FlowID: flowID, DatasourceID: q.DatasourceID,
			CanQuery: true, CanExport: q.CanExport,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) writeSteps(ctx context.Context, tx *gorm.DB, flowID string, stageID *string, steps []ApprovalStepWrite) error {
	for _, st := range steps {
		stepID := uuid.NewString()
		if err := tx.WithContext(ctx).Create(&FlowApprovalStepModel{
			ID: stepID, FlowID: flowID, StageID: stageID, Position: st.Position,
		}).Error; err != nil {
			return err
		}
		for _, actor := range st.Actors {
			if err := tx.WithContext(ctx).Create(&FlowApprovalStepActorModel{
				StepID: stepID, UserID: actor.UserID,
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// validateChangeFlowSingleEngine fixes BUG-005 (Option A): multi-stage change_review flows
// must bind datasources of the same engine and compatibility mode -- execution preflight recomputes
// statement dialect fingerprints against each stage datasource's engine, whereas review freezes a single dialect
// per Run, which would cause cross-engine stage 2 preflight to permanently fail with 3006. Fail closed at configuration time.
func (s *Service) validateChangeFlowSingleEngine(ctx context.Context, tx *gorm.DB, w *FlowWrite) error {
	if w.FlowType != "change_review" || len(w.Stages) < 2 {
		return nil
	}
	// Multiple stages may reference the same datasource (common in serial multi-stage flows): query against
	// the deduplicated ID set and compare row counts, otherwise SQL IN's natural deduplication would
	// misclassify "two stages sharing one datasource" as a missing datasource (1001).
	seen := make(map[string]struct{}, len(w.Stages))
	ids := make([]string, 0, len(w.Stages))
	for _, st := range w.Stages {
		if _, dup := seen[st.DatasourceID]; dup {
			continue
		}
		seen[st.DatasourceID] = struct{}{}
		ids = append(ids, st.DatasourceID)
	}
	var rows []struct {
		Engine            string
		CompatibilityMode string
	}
	if err := tx.Raw(`select engine, compatibility_mode from datasources where id in ?`, ids).
		Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return bizerr.ErrValidationFailed
	}
	first := rows[0]
	for _, r := range rows[1:] {
		if r != first {
			return bizerr.ErrValidationFailed
		}
	}
	return nil
}

// CreateFlow creates a flow template (shared entry point for change and query flows; flow_type is immutable once saved).
func (s *Service) CreateFlow(ctx context.Context, actor AdminActor, w *FlowWrite, meta RequestMeta) (*FlowView, error) {
	if err := validateFlowWrite(w); err != nil {
		return nil, err
	}
	users, datasources, ruleSet := w.referencedIDs()
	if err := s.validateChangeFlowSingleEngine(ctx, s.db, w); err != nil {
		return nil, err
	}
	flowID := uuid.NewString()
	now := time.Now().UTC()
	err := txutil.Serializable(ctx, s.db, func(tx *gorm.DB) error {
		if err := lockReferences(ctx, tx, users, datasources, ruleSet); err != nil {
			return err
		}
		status := "disabled"
		if w.Enabled {
			status = "enabled"
		}
		row := FlowModel{
			ID: flowID, Name: w.Name, FlowType: w.FlowType,
			Status: status, RuleSetID: w.RuleSetID,
			Version: 1, CreatedByUserID: actor.UserID, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			if txutil.IsUniqueViolation(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
		return s.writeFlowGraph(ctx, tx, flowID, w)
	})
	if err != nil {
		return nil, err
	}
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "flow.created", ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "flow", ResourceID: &flowID, Action: "create_flow", Outcome: "succeeded",
		RequestID: ptr.NonZero(meta.RequestID),
		Metadata:  map[string]any{"flow_type": w.FlowType, "stage_count": len(w.Stages)},
	})
	_ = s.audit.Emit(ctx, nil, "flow.created", "flow:"+flowID,
		auditlog.UserActor(actor.UserID, actor.Username),
		map[string]any{"flow_id": flowID, "flow_type": w.FlowType})
	return s.Get(ctx, flowID)
}

// ReplaceFlow executes the replace_flow_template command:
// atomically replaces the entire template structure (replace_membership_exact semantics) without affecting already frozen instances.
func (s *Service) ReplaceFlow(ctx context.Context, actor AdminActor, flowID string, expectVersion int64, w *FlowWrite, meta RequestMeta) (*FlowView, error) {
	if err := validateFlowWrite(w); err != nil {
		return nil, err
	}
	users, datasources, ruleSet := w.referencedIDs()
	if err := s.validateChangeFlowSingleEngine(ctx, s.db, w); err != nil {
		return nil, err
	}
	err := txutil.Serializable(ctx, s.db, func(tx *gorm.DB) error {
		var row FlowModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", flowID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			return bizerr.ErrVersionConflict
		}
		// Flow type is immutable; rule set type constraints mirror the database check constraints.
		if row.FlowType != w.FlowType {
			return bizerr.ErrValidationFailed
		}
		if err := lockReferences(ctx, tx, users, datasources, ruleSet); err != nil {
			return err
		}
		// Delete the old graph in full and rebuild; changes only affect tickets submitted afterward.
		if err := deleteFlowGraph(ctx, tx, flowID); err != nil {
			return err
		}
		if err := s.writeFlowGraph(ctx, tx, flowID, w); err != nil {
			return err
		}
		// Masking rules are managed independently of the template write payload: retain rules for datasources
		// that remain in query capabilities and delete only capabilities removed by this replacement to avoid leaving unmanageable orphan policies.
		if err := deleteStaleFlowMaskingRules(ctx, tx, flowID); err != nil {
			return err
		}
		// Editing a template restores an invalid flow to enabled or disabled as specified by the payload.
		status := "disabled"
		if w.Enabled {
			status = "enabled"
		}
		res := tx.Model(&FlowModel{}).
			Where("id = ? and version = ?", flowID, expectVersion).
			Updates(map[string]any{
				"name": w.Name, "status": status, "rule_set_id": w.RuleSetID,
				"version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return bizerr.ErrVersionConflict
		}
		// B8 (R006): any template modification (structure, rule set, enabled flag, or version) immediately
		// invalidates uncommitted Reviews referencing this flow; active Runs converge lazily via frozen hash
		// re-verification. Snapshots of already submitted tickets are unaffected.
		if err := reviewapp.InvalidateRunsForFlow(ctx, tx, flowID); err != nil {
			return err
		}
		return s.audit.Emit(ctx, tx, "flow.updated", "flow:"+flowID,
			auditlog.UserActor(actor.UserID, actor.Username),
			map[string]any{"flow_id": flowID})
	})
	if err != nil {
		return nil, err
	}
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "flow.updated", ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "flow", ResourceID: &flowID, Action: "replace_flow", Outcome: "succeeded",
		RequestID: ptr.NonZero(meta.RequestID),
	})
	return s.Get(ctx, flowID)
}

func deleteFlowGraph(ctx context.Context, tx *gorm.DB, flowID string) error {
	if err := tx.WithContext(ctx).Exec(`delete from flow_approval_step_actors where step_id in
		(select id from flow_approval_steps where flow_id = ?)`, flowID).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Where("flow_id = ?", flowID).Delete(&FlowApprovalStepModel{}).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`delete from flow_execution_actors where stage_id in
		(select id from flow_stages where flow_id = ?)`, flowID).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Where("flow_id = ?", flowID).Delete(&FlowStageModel{}).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Where("flow_id = ?", flowID).Delete(&FlowQueryCapabilityModel{}).Error; err != nil {
		return err
	}
	return nil
}

func deleteStaleFlowMaskingRules(ctx context.Context, tx *gorm.DB, flowID string) error {
	return tx.WithContext(ctx).Exec(`delete from flow_masking_rules as rule
		where rule.flow_id = ?
		and not exists (
			select 1 from flow_query_capabilities as capability
			where capability.flow_id = rule.flow_id
			and capability.datasource_id = rule.datasource_id
		)`, flowID).Error
}

func deleteAllFlowMaskingRules(ctx context.Context, tx *gorm.DB, flowID string) error {
	return tx.WithContext(ctx).
		Exec(`delete from flow_masking_rules where flow_id = ?`, flowID).Error
}

// DeleteFlow performs physical deletion only when there are zero references; returns 1106 if referenced by
// drafts, query access requests, or permission group grants (in which case the flow can only be disabled).
func (s *Service) DeleteFlow(ctx context.Context, actor AdminActor, flowID string, expectVersion int64, meta RequestMeta) error {
	err := txutil.Serializable(ctx, s.db, func(tx *gorm.DB) error {
		var row FlowModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", flowID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			return bizerr.ErrConcurrentModification
		}
		var drafts, requests, grants int64
		if err := tx.WithContext(ctx).Table("change_drafts").Where("flow_id = ?", flowID).Count(&drafts).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Table("query_access_requests").Where("flow_id_snapshot = ?", flowID).Count(&requests).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Table("permission_group_flow_grants").Where("flow_id = ?", flowID).Count(&grants).Error; err != nil {
			return err
		}
		if drafts+requests+grants > 0 {
			return bizerr.ErrReferenced
		}
		if err := deleteFlowGraph(ctx, tx, flowID); err != nil {
			return err
		}
		if err := deleteAllFlowMaskingRules(ctx, tx, flowID); err != nil {
			return err
		}
		if err := tx.Where("id = ?", flowID).Delete(&FlowModel{}).Error; err != nil {
			return err
		}
		return s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "flow.deleted", ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
			ResourceType: "flow", ResourceID: &flowID, Action: "delete_flow", Outcome: "succeeded",
			RequestID: ptr.NonZero(meta.RequestID),
		})
	})
	return err
}

// Get and List return control-plane flow views.
func (s *Service) Get(ctx context.Context, flowID string) (*FlowView, error) {
	var row FlowModel
	if err := s.db.WithContext(ctx).Where("id = ?", flowID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizerr.ErrNotFound
		}
		return nil, err
	}
	return s.viewOf(ctx, &row)
}

// List returns a paginated list of control-plane flow views.
// search filters by name substring (§18.73 server-side q); flowType and enabled are column-level
// narrowing filters (RCP-20261105-ADMIN-LIST-FIELD-FILTERS) where zero values mean unset and combine with AND.
func (s *Service) List(ctx context.Context, limit int, after, search, flowType string, enabled *bool) ([]FlowView, *string, error) {
	if limit < 1 || limit > 200 {
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch flowType {
	case "", "change_review", "query_access":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	q := s.db.WithContext(ctx).Model(&FlowModel{}).
		Order("created_at asc, id asc").Limit(limit + 1)
	if after != "" {
		var cursor FlowModel
		if err := s.db.WithContext(ctx).Select("created_at", "id").
			Where("id = ?", after).First(&cursor).Error; err != nil {
			return nil, nil, bizerr.ErrNotFound
		}
		q = q.Where("(created_at, id) > (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	if flowType != "" {
		q = q.Where("flow_type = ?", flowType)
	}
	if enabled != nil {
		if *enabled {
			q = q.Where("status = ?", "enabled")
		} else {
			q = q.Where("status <> ?", "enabled")
		}
	}
	if search != "" {
		q = q.Where("name ILIKE ?", "%"+search+"%")
	}
	var rows []FlowModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	views, err := s.viewsOf(ctx, rows)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return views, next, nil
}

func (s *Service) viewOf(ctx context.Context, row *FlowModel) (*FlowView, error) {
	views, err := s.viewsOf(ctx, []FlowModel{*row})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// CurrentUserFlows returns currently enabled flows authorized for the user via permission groups
// (taking the union of active grants; disabled flows are excluded from available options).
func (s *Service) CurrentUserFlows(ctx context.Context, userID, flowType string, limit int, after string) ([]FlowView, *string, error) {
	if flowType != "change_review" && flowType != "query_access" {
		return nil, nil, bizerr.ErrValidationFailed
	}
	if limit < 1 || limit > 200 {
		return nil, nil, bizerr.ErrValidationFailed
	}
	q := s.db.WithContext(ctx).Model(&FlowModel{}).
		Joins("join permission_group_flow_grants g on g.flow_id = flows.id").
		Joins("join permission_group_members m on m.permission_group_id = g.permission_group_id").
		Joins("join permission_groups pg on pg.id = g.permission_group_id and pg.enabled = true").
		Where("m.user_id = ? and flows.flow_type = ? and flows.status = 'enabled'", userID, flowType).
		Order("flows.created_at asc, flows.id asc").
		Distinct("flows.*").
		Limit(limit + 1)
	if after != "" {
		var cursor FlowModel
		if err := s.db.WithContext(ctx).Select("created_at", "id").
			Where("id = ?", after).First(&cursor).Error; err != nil {
			return nil, nil, bizerr.ErrNotFound
		}
		q = q.Where("(flows.created_at, flows.id) > (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	var rows []FlowModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	views, err := s.viewsOf(ctx, rows)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return views, next, nil
}

// toActorRefs converts a slice of user IDs into ActorRef views.
func toActorRefs(ids []string) []ActorRef {
	out := make([]ActorRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, ActorRef{UserID: id})
	}
	return out
}
