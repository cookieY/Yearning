// service.go implements the workflow application service: Change/Query flow template CRUD,
// the replace_flow_template transactional command (repository-commands), permission groups,
// and the current user's flow catalog (permission group union restricted to enabled flows).
package workflow

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"gorm.io/gorm"
	"time"
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
// datasource catalog name, engine, compatibility_mode, and default database_name
// (catalog deletion is blocked by logical references, falling back to datasource_id if temporarily missing
// while still satisfying minLength 1).
type StageView struct {
	StageWrite                  `json:",inline"`
	DatasourceName              string  `json:"datasource_name"`
	DatasourceEngine            string  `json:"datasource_engine,omitempty"`
	DatasourceCompatibilityMode string  `json:"datasource_compatibility_mode,omitempty"`
	DatasourceDatabaseName      *string `json:"datasource_database_name,omitempty"`
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

// UserRow is a minimal table projection used strictly for cross-domain existence locking.
type UserRow struct{}

// DatasourceRow is a minimal table projection used strictly for cross-domain existence locking.
type DatasourceRow struct{}

// RuleSetRow is a minimal table projection used strictly for cross-domain existence locking.
type RuleSetRow struct{}

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

	// Join stage datasource catalog metadata in a single batch query (falling back to datasource_id if name missing).
	type stageDSMeta struct {
		ID                string  `gorm:"column:id"`
		Name              string  `gorm:"column:name"`
		Engine            string  `gorm:"column:engine"`
		CompatibilityMode string  `gorm:"column:compatibility_mode"`
		DatabaseName      *string `gorm:"column:database_name"`
	}
	stageDSByID := make(map[string]stageDSMeta, len(dsIDs))
	if len(dsIDs) > 0 {
		var metaRows []stageDSMeta
		if err := s.db.WithContext(ctx).Raw(
			`select id::text as id, name, engine, compatibility_mode, database_name from datasources where id in ?`, dsIDs).Scan(&metaRows).Error; err != nil {
			return nil, err
		}
		for _, r := range metaRows {
			stageDSByID[r.ID] = r
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
			if meta, ok := stageDSByID[st.DatasourceID]; ok && meta.Name != "" {
				stageView.DatasourceName = meta.Name
				stageView.DatasourceEngine = meta.Engine
				stageView.DatasourceCompatibilityMode = meta.CompatibilityMode
				stageView.DatasourceDatabaseName = meta.DatabaseName
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

// RequestMeta carries request context metadata.
type RequestMeta struct {
	RequestID string
}
