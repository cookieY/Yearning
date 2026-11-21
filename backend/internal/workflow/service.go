// service.go implements the workflow application service: Change/Query flow template CRUD,
// the replace_flow_template transactional command (repository-commands), permission groups,
// and the current user's flow catalog (permission group union restricted to enabled flows).
package workflow

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

// AdminActor describes the initiator of an administrative operation (control-plane caller, not a flow node participant).
type AdminActor struct {
	UserID   string
	Username string
}

// ActorRef corresponds to OpenAPI ActorRef: a specific user assigned to a flow node.
type ActorRef struct {
	UserID string `json:"user_id"`
}

// ApprovalStepWrite corresponds to OpenAPI ApprovalStepWrite.
type ApprovalStepWrite struct {
	Position int        `json:"position"`
	Actors   []ActorRef `json:"actors"`
}

// StageWrite corresponds to OpenAPI FlowStageWrite. Stages no longer carry logical-to-physical
// schema mappings (RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS): each stage binds a single datasource,
// and object scope is determined by the mapping-free branch of review/execution (frozen search_path
// binding for PostgreSQL; datasource default database as the sole allowed schema for MySQL).
type StageWrite struct {
	Position        int                 `json:"position"`
	DatasourceID    string              `json:"datasource_id"`
	ApprovalSteps   []ApprovalStepWrite `json:"approval_steps"`
	ExecutionActors []ActorRef          `json:"execution_actors"`
}

// QueryCapabilityWrite corresponds to OpenAPI QueryCapabilityWrite.
type QueryCapabilityWrite struct {
	DatasourceID string `json:"datasource_id"`
	CanQuery     bool   `json:"can_query"`
	CanExport    bool   `json:"can_export"`
}

// QueryCapabilityView corresponds to OpenAPI QueryCapabilityView (read shape enriched with datasource name,
// RCP-20260901-FLOW-CATALOG-DATASOURCE-NAMES; joins the datasource catalog for the current name
// without introducing stale snapshots -- listDatasources remains admin-only).
type QueryCapabilityView struct {
	DatasourceID   string `json:"datasource_id"`
	DatasourceName string `json:"datasource_name"`
	CanQuery       bool   `json:"can_query"`
	CanExport      bool   `json:"can_export"`
}

// FlowWrite corresponds to OpenAPI FlowWrite.
type FlowWrite struct {
	Name              string                 `json:"name"`
	FlowType          string                 `json:"flow_type"`
	Enabled           bool                   `json:"enabled"`
	RuleSetID         *string                `json:"rule_set_id"`
	Stages            []StageWrite           `json:"stages"`
	ApprovalSteps     []ApprovalStepWrite    `json:"approval_steps"`
	QueryCapabilities []QueryCapabilityWrite `json:"query_capabilities"`
}

// FlowView is the flow response view (including decrypted schema mappings visible on the control plane).
// StageView is the read-side single stage view (FlowStageView): all StageWrite fields plus the joined
// datasource catalog name (RCP-20260904-FLOW-STAGE-DATASOURCE-NAME; catalog deletion is blocked by
// logical references, falling back to datasource_id if temporarily missing while still satisfying minLength 1).
type StageView struct {
	StageWrite     `json:",inline"`
	DatasourceName string `json:"datasource_name"`
}

// FlowView is the read-side flow view (FlowView contract): stages and query_capabilities shadow
// the write-shape fields of the same name on FlowWrite (following the existing QueryCapabilities precedent); F12 No.004:
// all three arrays have minItems 1 and are optional -- empty slices are omitted via omitempty since both null and [] violate the schema.
type FlowView struct {
	ID        string `json:"id"`
	FlowWrite `json:",inline"`
	// stages and approval_steps on the read surface carry minItems 1 and are optional in the contract: query_access
	// flows have no stages and change_review flows keep all steps inside stages -- omit the key when empty rather than
	// serializing an empty array (F12 #004 resolution: omission is valid whereas both null and [] violate minItems).
	Stages            []StageView           `json:"stages,omitempty"`
	ApprovalSteps     []ApprovalStepWrite   `json:"approval_steps,omitempty"`
	QueryCapabilities []QueryCapabilityView `json:"query_capabilities,omitempty"`
	Status            string                `json:"status"`
	Version           int64                 `json:"version"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
}

// Service is the workflow application service.
type Service struct {
	db      *gorm.DB
	crypter *crypto.Crypter
	audit   *auditlog.Recorder
}

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

// validateFlowWrite performs structural validation (enforcing the distinct rule sets for change and query flows).
func validateFlowWrite(w *FlowWrite) error {
	if w.Name == "" || len(w.Name) > 128 {
		return bizerr.ErrValidationFailed
	}
	switch w.FlowType {
	case "change_review":
		if w.RuleSetID == nil || *w.RuleSetID == "" {
			return bizerr.ErrValidationFailed
		}
		if len(w.Stages) < 1 {
			return bizerr.ErrValidationFailed
		}
		if len(w.ApprovalSteps) > 0 || len(w.QueryCapabilities) > 0 {
			return bizerr.ErrValidationFailed
		}
	case "query_access":
		if w.RuleSetID != nil {
			return bizerr.ErrValidationFailed
		}
		if len(w.ApprovalSteps) < 1 || len(w.ApprovalSteps) > 10 {
			return bizerr.ErrValidationFailed
		}
		if len(w.QueryCapabilities) < 1 {
			return bizerr.ErrValidationFailed
		}
		if len(w.Stages) > 0 {
			return bizerr.ErrValidationFailed
		}
	default:
		return bizerr.ErrValidationFailed
	}
	positions := map[int]bool{}
	for i := range w.Stages {
		st := &w.Stages[i]
		if st.Position < 1 || positions[st.Position] {
			return bizerr.ErrValidationFailed
		}
		positions[st.Position] = true
		if st.DatasourceID == "" {
			return bizerr.ErrValidationFailed
		}
		if len(st.ApprovalSteps) < 1 || len(st.ApprovalSteps) > 10 {
			return bizerr.ErrValidationFailed
		}
		if len(st.ExecutionActors) < 1 {
			return bizerr.ErrValidationFailed
		}
		stepPos := map[int]bool{}
		for _, s := range st.ApprovalSteps {
			if s.Position < 1 || s.Position > 10 || stepPos[s.Position] {
				return bizerr.ErrValidationFailed
			}
			stepPos[s.Position] = true
			if len(s.Actors) < 1 {
				return bizerr.ErrValidationFailed
			}
			seen := map[string]bool{}
			for _, a := range s.Actors {
				if a.UserID == "" || seen[a.UserID] {
					return bizerr.ErrValidationFailed
				}
				seen[a.UserID] = true
			}
		}
	}
	flowStepPos := map[int]bool{}
	for _, s := range w.ApprovalSteps {
		if s.Position < 1 || s.Position > 10 || flowStepPos[s.Position] {
			return bizerr.ErrValidationFailed
		}
		flowStepPos[s.Position] = true
		if len(s.Actors) < 1 {
			return bizerr.ErrValidationFailed
		}
		seen := map[string]bool{}
		for _, a := range s.Actors {
			if a.UserID == "" || seen[a.UserID] {
				return bizerr.ErrValidationFailed
			}
			seen[a.UserID] = true
		}
	}
	dsSeen := map[string]bool{}
	for _, q := range w.QueryCapabilities {
		if q.DatasourceID == "" || dsSeen[q.DatasourceID] || !q.CanQuery {
			return bizerr.ErrValidationFailed
		}
		dsSeen[q.DatasourceID] = true
	}
	return nil
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

// UserRow is a minimal table projection used strictly for cross-domain existence locking.
type UserRow struct{}

func (UserRow) TableName() string { return "users" }

// DatasourceRow is a minimal table projection used strictly for cross-domain existence locking.
type DatasourceRow struct{}

func (DatasourceRow) TableName() string { return "datasources" }

// RuleSetRow is a minimal table projection used strictly for cross-domain existence locking.
type RuleSetRow struct{}

func (RuleSetRow) TableName() string { return "rule_sets" }

// sealSchemaMapping seals a placeholder envelope for stage schema mappings. Per RCP-20261031-REMOVE-
// FLOW-SCHEMA-MAPPINGS: flow stages no longer configure logical-to-physical schema mappings and new writes
// always seal an empty array; the schema_mapping_* encrypted columns and AAD structure are retained (zero migration)
// so historical envelopes on existing rows can still be verified under the original AAD, while readers no longer
// consume mapping contents as active configuration (both review and execution follow the unmapped branch).
func (s *Service) sealSchemaMapping(flowStageID string) (ct, nonce []byte, keyID string, err error) {
	env, err := s.crypter.Seal(crypto.PurposePayload, []byte("[]"), crypto.AAD{
		DeploymentID: s.crypter.DeploymentID(),
		Table:        "flow_stages", PrimaryKey: flowStageID,
		Column: "schema_mapping", Purpose: "flow_schema_mapping",
	})
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
			if isUniqueErr(err) {
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
		RequestID: strPtr(meta.RequestID),
		Metadata:  map[string]any{"flow_type": w.FlowType, "stage_count": len(w.Stages)},
	})
	_ = s.audit.Emit(ctx, nil, "flow.created", "flow:"+flowID,
		auditlog.Actor{Kind: "user", UserID: actor.UserID, Username: actor.Username},
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
			auditlog.Actor{Kind: "user", UserID: actor.UserID, Username: actor.Username},
			map[string]any{"flow_id": flowID})
	})
	if err != nil {
		return nil, err
	}
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "flow.updated", ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "flow", ResourceID: &flowID, Action: "replace_flow", Outcome: "succeeded",
		RequestID: strPtr(meta.RequestID),
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
			RequestID: strPtr(meta.RequestID),
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

func (s *Service) viewsOf(ctx context.Context, rows []FlowModel) ([]FlowView, error) {
	if len(rows) == 0 {
		return []FlowView{}, nil
	}
	flowIDs := make([]string, len(rows))
	for i := range rows {
		flowIDs[i] = rows[i].ID
	}

	var stages []FlowStageModel
	if err := s.db.WithContext(ctx).Where("flow_id in ?", flowIDs).
		Order("position asc").Find(&stages).Error; err != nil {
		return nil, err
	}

	stageIDs := make([]string, 0, len(stages))
	dsIDSet := make(map[string]bool, len(stages))
	dsIDs := make([]string, 0, len(stages))
	stagesByFlow := make(map[string][]FlowStageModel, len(rows))
	for i := range stages {
		st := stages[i]
		stageIDs = append(stageIDs, st.ID)
		if !dsIDSet[st.DatasourceID] {
			dsIDSet[st.DatasourceID] = true
			dsIDs = append(dsIDs, st.DatasourceID)
		}
		stagesByFlow[st.FlowID] = append(stagesByFlow[st.FlowID], st)
	}

	// Join stage datasource catalog names in a single batch query (falling back to datasource_id if missing).
	stageDSNames := make(map[string]string, len(dsIDs))
	if len(dsIDs) > 0 {
		var nameRows []struct {
			ID   string `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		if err := s.db.WithContext(ctx).Raw(
			`select id::text as id, name from datasources where id in ?`, dsIDs).Scan(&nameRows).Error; err != nil {
			return nil, err
		}
		for _, r := range nameRows {
			stageDSNames[r.ID] = r.Name
		}
	}

	var steps []FlowApprovalStepModel
	if err := s.db.WithContext(ctx).Where("flow_id in ?", flowIDs).
		Order("position asc").Find(&steps).Error; err != nil {
		return nil, err
	}

	stepIDs := make([]string, len(steps))
	stepsByStage := make(map[string][]FlowApprovalStepModel, len(stages))
	flowStepsByFlow := make(map[string][]FlowApprovalStepModel, len(rows))
	for i := range steps {
		sp := steps[i]
		stepIDs[i] = sp.ID
		if sp.StageID != nil {
			stepsByStage[*sp.StageID] = append(stepsByStage[*sp.StageID], sp)
		} else {
			flowStepsByFlow[sp.FlowID] = append(flowStepsByFlow[sp.FlowID], sp)
		}
	}

	stepActors := make(map[string][]string, len(steps))
	if len(stepIDs) > 0 {
		var actorRows []FlowApprovalStepActorModel
		if err := s.db.WithContext(ctx).Where("step_id in ?", stepIDs).
			Order("step_id asc, user_id asc").Find(&actorRows).Error; err != nil {
			return nil, err
		}
		for _, a := range actorRows {
			stepActors[a.StepID] = append(stepActors[a.StepID], a.UserID)
		}
	}

	stageExecActors := make(map[string][]ActorRef, len(stages))
	if len(stageIDs) > 0 {
		var execRows []FlowExecutionActorModel
		if err := s.db.WithContext(ctx).Where("stage_id in ?", stageIDs).
			Order("stage_id asc, user_id asc").Find(&execRows).Error; err != nil {
			return nil, err
		}
		for _, a := range execRows {
			stageExecActors[a.StageID] = append(stageExecActors[a.StageID], ActorRef{UserID: a.UserID})
		}
	}

	var caps []struct {
		FlowID         string `gorm:"column:flow_id"`
		DatasourceID   string `gorm:"column:datasource_id"`
		DatasourceName string `gorm:"column:datasource_name"`
		CanQuery       bool   `gorm:"column:can_query"`
		CanExport      bool   `gorm:"column:can_export"`
	}
	if err := s.db.WithContext(ctx).Raw(`
		select c.flow_id::text as flow_id, c.datasource_id::text as datasource_id,
		       d.name as datasource_name, c.can_query, c.can_export
		from flow_query_capabilities c
		join datasources d on d.id = c.datasource_id
		where c.flow_id in ?
		order by c.datasource_id`, flowIDs).Scan(&caps).Error; err != nil {
		return nil, err
	}
	capsByFlow := make(map[string][]QueryCapabilityView, len(rows))
	for _, q := range caps {
		capsByFlow[q.FlowID] = append(capsByFlow[q.FlowID], QueryCapabilityView{
			DatasourceID: q.DatasourceID, DatasourceName: q.DatasourceName,
			CanQuery: q.CanQuery, CanExport: q.CanExport,
		})
	}

	views := make([]FlowView, len(rows))
	for i := range rows {
		row := &rows[i]
		view := FlowView{
			ID: row.ID, FlowWrite: FlowWrite{
				Name: row.Name, FlowType: row.FlowType, Enabled: row.Status == "enabled",
				RuleSetID: row.RuleSetID,
			},
			// F12 No.004: minItems 1 arrays (stages, approval_steps,
			// query_capabilities) always omit the key when empty -- serializing [] violates minItems,
			// and omitempty already handles omission.
			Status: row.Status, Version: row.Version,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
		for _, st := range stagesByFlow[row.ID] {
			stageView := StageView{
				StageWrite: StageWrite{
					Position: st.Position, DatasourceID: st.DatasourceID,
					// An invalidated flow may have an empty execution actor set; serialize as [] rather than null per OpenAPI required array.
					ApprovalSteps:   []ApprovalStepWrite{},
					ExecutionActors: []ActorRef{},
				},
			}
			if name, ok := stageDSNames[st.DatasourceID]; ok && name != "" {
				stageView.DatasourceName = name
			} else {
				stageView.DatasourceName = st.DatasourceID
			}
			for _, sp := range stepsByStage[st.ID] {
				stageView.ApprovalSteps = append(stageView.ApprovalSteps, ApprovalStepWrite{
					Position: sp.Position, Actors: toActorRefs(stepActors[sp.ID]),
				})
			}
			if ea := stageExecActors[st.ID]; len(ea) > 0 {
				stageView.ExecutionActors = ea
			}
			view.Stages = append(view.Stages, stageView)
		}
		for _, sp := range flowStepsByFlow[row.ID] {
			view.ApprovalSteps = append(view.ApprovalSteps, ApprovalStepWrite{
				Position: sp.Position, Actors: toActorRefs(stepActors[sp.ID]),
			})
		}
		if qc := capsByFlow[row.ID]; len(qc) > 0 {
			view.QueryCapabilities = qc
		}
		views[i] = view
	}
	return views, nil
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

// RequestMeta carries request context metadata.
type RequestMeta struct {
	RequestID string
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// toActorRefs converts a slice of user IDs into ActorRef views.
func toActorRefs(ids []string) []ActorRef {
	out := make([]ActorRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, ActorRef{UserID: id})
	}
	return out
}

func isUniqueErr(err error) bool { return txutil.IsUniqueViolation(err) }
