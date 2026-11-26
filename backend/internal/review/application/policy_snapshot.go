package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
)

const reviewPolicySnapshotSchemaVersion = "review-policy-v1"

// ErrPolicySnapshotInvalid covers missing, malformed, tampered, unsupported
// or mismatched snapshots. Callers should fail closed and mark the Run
// outdated rather than retrying with the current administrative policy.
var ErrPolicySnapshotInvalid = errors.New("review: invalid frozen policy snapshot")

// FrozenReviewPolicy is the immutable policy material consumed by a Run.
// It contains the complete effective candidate set; per-stage matching may
// narrow the knowledge entries but never re-reads current administrative rows.
type FrozenReviewPolicy struct {
	SchemaVersion    string                 `json:"schema_version"`
	Flow             flowSnapshot           `json:"flow"`
	RuleSet          FrozenReviewRuleSet    `json:"rule_set"`
	PromptTools      []FrozenPromptTool     `json:"prompt_tools"`
	KnowledgeEntries []FrozenKnowledgeEntry `json:"knowledge_entries"`
}

// FrozenReviewRuleSet records the rule-set identity and canonical membership.
type FrozenReviewRuleSet struct {
	ID         string   `json:"id"`
	ConfigHash string   `json:"config_hash"`
	Enabled    bool     `json:"enabled"`
	MemberIDs  []string `json:"member_ids"`
}

// FrozenPromptTool contains the exact enabled tool definition used by the Run.
type FrozenPromptTool struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Engine     string               `json:"engine"`
	Parameters map[string]any       `json:"parameters"`
	Definition PromptToolDefinition `json:"definition"`
	ConfigHash string               `json:"config_hash"`
	FindingKey string               `json:"finding_key"`
	Version    int64                `json:"version"`
}

// FrozenKnowledgeEntry contains an enabled knowledge candidate. Workers filter
// these frozen candidates against frozen object identities.
type FrozenKnowledgeEntry struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Purpose      string               `json:"purpose"`
	ScopeType    knowledge.ScopeType  `json:"scope_type"`
	DatasourceID string               `json:"datasource_id,omitempty"`
	DatabaseName string               `json:"database_name,omitempty"`
	TableName    string               `json:"table_name,omitempty"`
	Definition   knowledge.Definition `json:"definition"`
	ConfigHash   string               `json:"config_hash"`
	FindingKey   string               `json:"finding_key"`
	Version      int64                `json:"version"`
}

// canonicalReviewPolicyIdentity returns the canonical snapshot hash and bytes.
func canonicalReviewPolicyIdentity(policy FrozenReviewPolicy) (string, []byte, error) {
	canonical, err := canonicalReviewPolicy(policy)
	if err != nil {
		return "", nil, err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", nil, err
	}
	return hashutil.SHA256Hex(raw), raw, nil
}

func canonicalReviewPolicy(policy FrozenReviewPolicy) (FrozenReviewPolicy, error) {
	if policy.SchemaVersion != reviewPolicySnapshotSchemaVersion {
		return FrozenReviewPolicy{}, errors.New("unsupported policy snapshot schema")
	}
	if policy.Flow.FlowID == "" || policy.Flow.FlowVersion < 1 {
		return FrozenReviewPolicy{}, errors.New("flow identity missing")
	}
	if !validSHA256(policy.RuleSet.ConfigHash) {
		return FrozenReviewPolicy{}, errors.New("rule set hash missing")
	}
	out := policy
	out.Flow = cloneFlowSnapshot(policy.Flow)
	sort.Slice(out.Flow.Stages, func(i, j int) bool {
		return out.Flow.Stages[i].Position < out.Flow.Stages[j].Position
	})
	for _, stage := range out.Flow.Stages {
		if stage.Position < 1 || stage.DatasourceID == "" {
			return FrozenReviewPolicy{}, errors.New("stage identity missing")
		}
	}

	out.RuleSet.MemberIDs = append([]string(nil), policy.RuleSet.MemberIDs...)
	sort.Strings(out.RuleSet.MemberIDs)
	if out.RuleSet.ID == "" && len(out.RuleSet.MemberIDs) != 0 {
		return FrozenReviewPolicy{}, errors.New("rule membership without rule set")
	}

	out.PromptTools = append([]FrozenPromptTool(nil), policy.PromptTools...)
	for i := range out.PromptTools {
		if out.PromptTools[i].ID == "" ||
			out.PromptTools[i].Name == "" ||
			out.PromptTools[i].Engine == "" ||
			!validSHA256(out.PromptTools[i].ConfigHash) {
			return FrozenReviewPolicy{}, errors.New("prompt tool identity missing")
		}
		if err := ValidatePromptToolDefinition(out.PromptTools[i].Definition); err != nil {
			return FrozenReviewPolicy{}, err
		}
		out.PromptTools[i].Parameters = deepCopyMap(out.PromptTools[i].Parameters)
	}
	sort.Slice(out.PromptTools, func(i, j int) bool {
		return out.PromptTools[i].ID < out.PromptTools[j].ID
	})

	out.KnowledgeEntries = append([]FrozenKnowledgeEntry(nil), policy.KnowledgeEntries...)
	for i := range out.KnowledgeEntries {
		entry := out.KnowledgeEntries[i]
		if entry.ID == "" || entry.Name == "" || !validSHA256(entry.ConfigHash) {
			return FrozenReviewPolicy{}, errors.New("knowledge identity missing")
		}
		if err := entry.Definition.ValidatePersisted(); err != nil {
			return FrozenReviewPolicy{}, err
		}
		switch entry.ScopeType {
		case knowledge.ScopeGlobal:
			if entry.DatasourceID != "" || entry.DatabaseName != "" || entry.TableName != "" {
				return FrozenReviewPolicy{}, errors.New("invalid global knowledge scope")
			}
		case knowledge.ScopeDatasource:
			if entry.DatasourceID == "" || entry.DatabaseName == "" || entry.TableName != "" {
				return FrozenReviewPolicy{}, errors.New("invalid datasource knowledge scope")
			}
		case knowledge.ScopeTable:
			if entry.DatasourceID == "" || entry.DatabaseName == "" || entry.TableName == "" {
				return FrozenReviewPolicy{}, errors.New("invalid table knowledge scope")
			}
		default:
			return FrozenReviewPolicy{}, errors.New("unknown knowledge scope")
		}
	}
	sort.Slice(out.KnowledgeEntries, func(i, j int) bool {
		return out.KnowledgeEntries[i].ID < out.KnowledgeEntries[j].ID
	})

	if err := rejectDuplicatePositions(out.Flow.Stages); err != nil {
		return FrozenReviewPolicy{}, err
	}
	if err := rejectDuplicateStrings(
		stringIDs(out.PromptTools),
		"duplicate prompt tool identity",
	); err != nil {
		return FrozenReviewPolicy{}, err
	}
	if err := rejectDuplicateStrings(
		knowledgeIDs(out.KnowledgeEntries),
		"duplicate knowledge identity",
	); err != nil {
		return FrozenReviewPolicy{}, err
	}
	if err := rejectDuplicateStrings(
		out.RuleSet.MemberIDs,
		"duplicate rule set membership",
	); err != nil {
		return FrozenReviewPolicy{}, err
	}
	return out, nil
}

func policyIdentityParts(policy FrozenReviewPolicy) (string, string, string, string, error) {
	canonical, err := canonicalReviewPolicy(policy)
	if err != nil {
		return "", "", "", "", err
	}
	canonicalRaw, err := json.Marshal(canonical)
	if err != nil {
		return "", "", "", "", err
	}
	flowRaw, err := json.Marshal(canonical.Flow)
	if err != nil {
		return "", "", "", "", err
	}
	policyHash := hashutil.SHA256Hex(canonicalRaw)
	flowHash := hashutil.SHA256Hex(flowRaw)
	ruleHash := canonical.RuleSet.ConfigHash
	toolHash := effectiveFrozenPolicyToolHash(canonical)
	return policyHash, flowHash, ruleHash, toolHash, nil
}

func effectiveFrozenPolicyToolHash(policy FrozenReviewPolicy) string {
	hashes := make([]string, 0, len(policy.PromptTools)+len(policy.KnowledgeEntries))
	for _, tool := range policy.PromptTools {
		hashes = append(hashes, tool.ConfigHash)
	}
	for _, entry := range policy.KnowledgeEntries {
		hashes = append(hashes, "knowledge_entry:"+entry.ID+":"+entry.ConfigHash)
	}
	return domain.EffectiveToolConfigHash(hashes)
}

// freezeReviewPolicy builds the snapshot and all three existing Run identity
// columns from one locked policy read inside the StartRun transaction.
func (s *Services) freezeReviewPolicy(
	ctx context.Context,
	db *gorm.DB,
	flowID string,
	flow flowRow,
) (*FrozenReviewPolicy, string, string, string, error) {
	stages, err := s.loadFlowStages(ctx, db, flowID)
	if err != nil {
		return nil, "", "", "", err
	}
	approval, err := s.loadApprovalActors(ctx, db, flowID)
	if err != nil {
		return nil, "", "", "", err
	}
	flowSnap := flowSnapshot{FlowID: flowID, FlowVersion: flow.Version}
	for i := range stages {
		stage := stages[i]
		identity := ""
		if stage.IdentityFP != nil {
			identity = *stage.IdentityFP
		}
		databaseName := ""
		if stage.DatabaseName != nil {
			databaseName = *stage.DatabaseName
		}
		flowSnap.Stages = append(flowSnap.Stages, flowStageSummary{
			Position:               stage.Position,
			DatasourceID:           stage.DatasourceID,
			DatasourceEngine:       stage.Engine,
			DatasourceMode:         stage.CompatibilityMode,
			DatasourceHost:         stage.Host,
			DatasourcePort:         stage.Port,
			DatasourceDatabaseName: databaseName,
			DatasourceIdentityFP:   identity,
			ApprovalStepActors:     approval[stage.Position],
		})
	}
	ruleSet, tools, entries, err := loadFrozenPolicyRows(ctx, db, flow.RuleSetID)
	if err != nil {
		return nil, "", "", "", err
	}
	policy := &FrozenReviewPolicy{
		SchemaVersion:    reviewPolicySnapshotSchemaVersion,
		Flow:             flowSnap,
		RuleSet:          ruleSet,
		PromptTools:      tools,
		KnowledgeEntries: entries,
	}
	_, flowHash, ruleHash, toolHash, err := policyIdentityParts(*policy)
	if err != nil {
		return nil, "", "", "", err
	}
	return policy, flowHash, ruleHash, toolHash, nil
}

// frozenHashes preserves the existing drift-check call surface while deriving
// all three values from the complete frozen-policy implementation.
func (s *Services) frozenHashes(
	ctx context.Context,
	db *gorm.DB,
	flowID string,
	flow flowRow,
) (string, string, string, error) {
	_, flowHash, ruleHash, toolHash, err := s.freezeReviewPolicy(ctx, db, flowID, flow)
	return flowHash, ruleHash, toolHash, err
}

// ruleConfigHashes preserves the policy-only helper used by existing unit
// fixtures. Production StartRun always freezes the complete Run snapshot.
func (s *Services) ruleConfigHashes(
	ctx context.Context,
	db *gorm.DB,
	ruleSetID *string,
) (string, string, error) {
	ruleSet, tools, entries, err := loadFrozenPolicyRows(ctx, db, ruleSetID)
	if err != nil {
		return "", "", err
	}
	policy := FrozenReviewPolicy{
		PromptTools:      tools,
		KnowledgeEntries: entries,
	}
	return ruleSet.ConfigHash, effectiveFrozenPolicyToolHash(policy), nil
}

func loadFrozenPolicyRows(
	ctx context.Context,
	db *gorm.DB,
	ruleSetID *string,
) (FrozenReviewRuleSet, []FrozenPromptTool, []FrozenKnowledgeEntry, error) {
	ruleSet := FrozenReviewRuleSet{ConfigHash: hashutil.SHA256Hex([]byte("none"))}
	var tools []FrozenPromptTool
	if ruleSetID != nil && *ruleSetID != "" {
		var row struct {
			ID         string
			ConfigHash string
			Enabled    bool
		}
		if err := db.WithContext(ctx).Raw(`
			select id::text as id, config_hash, enabled
			from rule_sets where id = ? for update`, *ruleSetID).
			Scan(&row).Error; err != nil {
			return FrozenReviewRuleSet{}, nil, nil, err
		}
		if row.ID == "" || !validSHA256(row.ConfigHash) {
			return FrozenReviewRuleSet{}, nil, nil, ErrNotFound
		}
		var memberIDs []string
		if err := db.WithContext(ctx).Raw(`
			select prompt_tool_id::text
			from rule_set_prompt_tools
			where rule_set_id = ?
			order by prompt_tool_id`, *ruleSetID).Scan(&memberIDs).Error; err != nil {
			return FrozenReviewRuleSet{}, nil, nil, err
		}
		sort.Strings(memberIDs)
		ruleSet = FrozenReviewRuleSet{
			ID:         row.ID,
			ConfigHash: row.ConfigHash,
			Enabled:    row.Enabled,
			MemberIDs:  memberIDs,
		}
		if row.Enabled {
			var rows []struct {
				ID         string
				Name       string
				Engine     string
				Parameters []byte
				Definition []byte
				ConfigHash string
				FindingKey string
				Version    int64
			}
			if err := db.WithContext(ctx).Raw(`
				select t.id::text as id, t.name, coalesce(t.engine, 'all') as engine,
				       t.parameters, t.definition, t.config_hash, t.finding_key, t.version
				from rule_set_prompt_tools rt
				join prompt_tools t on t.id = rt.prompt_tool_id
				where rt.rule_set_id = ? and t.state = 'enabled'
				order by t.id for update of t`, *ruleSetID).Scan(&rows).Error; err != nil {
				return FrozenReviewRuleSet{}, nil, nil, err
			}
			tools = make([]FrozenPromptTool, 0, len(rows))
			for _, raw := range rows {
				var def PromptToolDefinition
				if err := json.Unmarshal(raw.Definition, &def); err != nil {
					return FrozenReviewRuleSet{}, nil, nil, ErrPolicySnapshotInvalid
				}
				params := map[string]any{}
				if len(raw.Parameters) > 0 {
					if err := json.Unmarshal(raw.Parameters, &params); err != nil {
						return FrozenReviewRuleSet{}, nil, nil, ErrPolicySnapshotInvalid
					}
				}
				tools = append(tools, FrozenPromptTool{
					ID: raw.ID, Name: raw.Name, Engine: raw.Engine,
					Parameters: normalizeParams(params), Definition: def,
					ConfigHash: raw.ConfigHash, FindingKey: raw.FindingKey,
					Version: raw.Version,
				})
			}
		}
	}

	var knowledgeRows []struct {
		ID           string
		Name         string
		Purpose      string
		ScopeType    string
		DatasourceID *string
		DatabaseName *string
		TableName    *string
		Definition   []byte
		ConfigHash   string
		FindingKey   string
		Version      int64
	}
	if err := db.WithContext(ctx).Raw(`
		select id::text as id, name, purpose, scope_type,
		       datasource_id::text as datasource_id, database_name,
		       table_name, definition, config_hash, finding_key, version
		from knowledge_entries
		where state = 'enabled'
		  and scope_type in ('datasource', 'table')
		order by id
		for update`).Scan(&knowledgeRows).Error; err != nil {
		return FrozenReviewRuleSet{}, nil, nil, err
	}
	entries := make([]FrozenKnowledgeEntry, 0, len(knowledgeRows))
	for _, raw := range knowledgeRows {
		var def knowledge.Definition
		if err := json.Unmarshal(raw.Definition, &def); err != nil {
			return FrozenReviewRuleSet{}, nil, nil, ErrPolicySnapshotInvalid
		}
		entries = append(entries, FrozenKnowledgeEntry{
			ID: raw.ID, Name: raw.Name, Purpose: raw.Purpose,
			ScopeType:    knowledge.ScopeType(raw.ScopeType),
			DatasourceID: stringValue(raw.DatasourceID),
			DatabaseName: stringValue(raw.DatabaseName),
			TableName:    stringValue(raw.TableName),
			Definition:   def, ConfigHash: raw.ConfigHash,
			FindingKey: raw.FindingKey, Version: raw.Version,
		})
	}
	return ruleSet, tools, entries, nil
}

func cloneFlowSnapshot(flow flowSnapshot) flowSnapshot {
	out := flow
	out.Stages = append([]flowStageSummary(nil), flow.Stages...)
	for i := range out.Stages {
		out.Stages[i].ApprovalStepActors = append(
			[]string(nil),
			flow.Stages[i].ApprovalStepActors...,
		)
		out.Stages[i].ExecutionActors = append(
			[]string(nil),
			flow.Stages[i].ExecutionActors...,
		)
	}
	return out
}

func deepCopyMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		switch v := value.(type) {
		case float64:
			if v == float64(int64(v)) {
				out[key] = int64(v)
			} else {
				out[key] = v
			}
		case json.Number:
			if n, err := v.Int64(); err == nil {
				out[key] = int64(n)
			} else {
				out[key] = v
			}
		case []string:
			out[key] = append([]string(nil), v...)
		case []any:
			out[key] = append([]any(nil), v...)
		default:
			out[key] = v
		}
	}
	return out
}

func rejectDuplicatePositions(stages []flowStageSummary) error {
	for i := 1; i < len(stages); i++ {
		if stages[i-1].Position == stages[i].Position {
			return errors.New("duplicate stage position")
		}
	}
	return nil
}

func rejectDuplicateStrings(values []string, message string) error {
	for i := 1; i < len(values); i++ {
		if values[i-1] == values[i] {
			return errors.New(message)
		}
	}
	return nil
}

func stringIDs(tools []FrozenPromptTool) []string {
	out := make([]string, len(tools))
	for i := range tools {
		out[i] = tools[i].ID
	}
	return out
}

func knowledgeIDs(entries []FrozenKnowledgeEntry) []string {
	out := make([]string, len(entries))
	for i := range entries {
		out[i] = entries[i].ID
	}
	return out
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
