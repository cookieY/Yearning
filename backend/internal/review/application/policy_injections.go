package application

import (
	"context"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"gorm.io/gorm"
)

// freezeInjections consumes only the authenticated StartRun policy. Current
// administrator rows are never a substitute for missing frozen inputs.
func (r *AgentRunner) freezeInjections(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel, policy *FrozenReviewPolicy) (*injections, error) {
	if policy == nil {
		return nil, ErrPolicySnapshotInvalid
	}
	inj := &injections{
		SkillsByStage:          map[string][]agent.SkillInjection{},
		KnowledgeByStage:       map[string][]*knowledge.Entry{},
		mandatoryKeysByStage:   map[string]map[string]bool{},
		touchedTablesByStage:   map[string][]string{},
		knowledgeScopeComplete: map[string]bool{},
	}
	for _, raw := range policy.PromptTools {
		inj.Skills = append(inj.Skills, agent.SkillInjection{ToolID: raw.ID, Name: raw.Name, Engine: raw.Engine, Parameters: deepCopyMap(raw.Parameters), KnowledgeText: raw.Definition.KnowledgeText, ConfigHash: raw.ConfigHash, FindingKey: raw.FindingKey, MaxSeverity: raw.Definition.MaxSeverity})
	}
	snapshots, err := r.svc.loadStageSnapshots(ctx, db, run.ID)
	if err != nil {
		return nil, err
	}
	bindings := r.frozenStageSchemas(ctx, db, run, snapshots)
	mapper := privacy.NewRunMapper()
	if err := r.rehydrateIdentifierMapping(ctx, db, run.ID, mapper); err != nil {
		return nil, err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range snapshots {
			snap := &snapshots[i]
			target, err := frozenDatasourceTargetFromContext(r.svc.Crypter, run.ID, snap.StagePosition, crypto.Envelope{Ciphertext: snap.NormalizedContextCiphertext, Nonce: snap.NormalizedContextNonce, KeyID: snap.NormalizedContextKeyID, Algorithm: snap.NormalizedContextAlgorithm})
			if err != nil {
				return err
			}
			groups, err := r.stageGroups(ctx, tx, run.ID, snap.DatasourceID)
			if err != nil {
				return err
			}
			engineSkills := make([]agent.SkillInjection, 0, len(inj.Skills))
			for _, sk := range inj.Skills {
				if sk.MatchesEngine(target.Engine, target.CompatibilityMode) {
					engineSkills = append(engineSkills, sk)
				}
			}
			stageSkills := sortSkillsByDeterminism(filterStageSkills(engineSkills, groups))
			inj.SkillsByStage[snap.ID] = stageSkills
			mandatory := map[string]bool{}
			for _, sk := range stageSkills {
				addMandatoryFindingKey(mandatory, sk.FindingKey)
				if err := insertToolCallRef(ctx, tx, run.ID, snap.ID, "prompt_tool", sk.Name, sk.ToolID, sk.ConfigHash); err != nil {
					return err
				}
			}
			scope, complete, err := r.knowledgeScopeForStage(ctx, tx, run, snap, bindings[snap.StagePosition], mapper)
			if err != nil {
				return err
			}
			inj.knowledgeScopeComplete[snap.ID] = complete
			inj.touchedTablesByStage[snap.ID] = append([]string(nil), scope.Tables...)
			entries := []*knowledge.Entry{}
			for _, raw := range policy.KnowledgeEntries {
				entry := &knowledge.Entry{ID: raw.ID, Name: raw.Name, Purpose: raw.Purpose, State: knowledge.StateEnabled, ScopeType: raw.ScopeType, DatasourceID: raw.DatasourceID, DatabaseName: raw.DatabaseName, TableName: raw.TableName, Definition: raw.Definition, ConfigHash: raw.ConfigHash, FindingKey: raw.FindingKey, Version: raw.Version}
				if !entry.Matched(scope) {
					continue
				}
				entries = append(entries, entry)
			}
			knowledge.SortByDeterminism(entries)
			inj.KnowledgeByStage[snap.ID] = entries
			for _, entry := range entries {
				addMandatoryFindingKey(mandatory, entry.FindingKey)
				if err := insertToolCallRef(ctx, tx, run.ID, snap.ID, "knowledge_entry", entry.Name, entry.ID, entry.ConfigHash); err != nil {
					return err
				}
			}
			inj.mandatoryKeysByStage[snap.ID] = mandatory
		}
		return nil
	})
	return inj, err
}

func addMandatoryFindingKey(dst map[string]bool, findingKey string) {
	if findingKey == "" {
		return
	}
	dst[findingKey] = true
}

// insertToolCallRef writes an injection reference, skipping if already present.
func insertToolCallRef(ctx context.Context, db *gorm.DB, runID, stageReviewID, kind, name, refID, hash string) error {
	var n int64
	if err := db.WithContext(ctx).Raw(
		`select count(*) from review_tool_calls where review_run_id = ? and stage_review_id = ? and tool_kind = ? and input_ref_id = ?`,
		runID, stageReviewID, kind, refID).Scan(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := nowUTC()
	res := db.WithContext(ctx).Exec(`
		insert into review_tool_calls (id, review_run_id, stage_review_id, tool_kind, tool_name,
			input_ref_id, config_hash, success, started_at, finished_at)
		values (?, ?, ?, ?, ?, ?, ?, true, ?, ?)`,
		newID(), runID, stageReviewID, kind, name, refID, hash, now, now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConcurrent
	}
	return nil
}
