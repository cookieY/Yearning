package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
)

const maxSensitiveColumns = 256

// FlowMaskingRuleView is the normalized masking rule view returned by the admin API.
type FlowMaskingRuleView struct {
	DatasourceID     string   `json:"datasource_id"`
	SensitiveColumns []string `json:"sensitive_columns"`
	Version          int64    `json:"version"`
}

// databaseMaskingSource is the default production vocabulary source. No configured row means an empty vocabulary;
// query failures, corrupted JSON, or constraint violations return an error so execution and metadata paths fail closed.
type databaseMaskingSource struct{}

func (databaseMaskingSource) Vocabulary(ctx context.Context, db *gorm.DB, flowID, datasourceID string) ([]string, error) {
	var row persistence.FlowMaskingRuleModel
	err := db.WithContext(ctx).
		Where("flow_id = ? and datasource_id = ?", flowID, datasourceID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw []string
	if err := json.Unmarshal(row.SensitiveColumns, &raw); err != nil {
		return nil, err
	}
	normalized, err := normalizeSensitiveColumns(raw)
	if err != nil || !sameStrings(raw, normalized) {
		return nil, errors.New("query: invalid persisted masking vocabulary")
	}
	return normalized, nil
}

// ListMaskingRules returns existing masking rule rows for a flow without synthesizing empty rows for unconfigured datasources.
func (s *Services) ListMaskingRules(ctx context.Context, actor Actor, flowID string) ([]FlowMaskingRuleView, error) {
	if !actor.IsBuiltinAdmin {
		return nil, domain.ErrNotFound()
	}
	var exists bool
	if err := s.DB.WithContext(ctx).Raw(
		`select exists(select 1 from flows where id = ? and flow_type = 'query_access')`, flowID).
		Scan(&exists).Error; err != nil {
		return nil, err
	}
	if !exists {
		return nil, domain.ErrNotFound()
	}
	var rows []persistence.FlowMaskingRuleModel
	if err := s.DB.WithContext(ctx).Where("flow_id = ?", flowID).
		Order("datasource_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]FlowMaskingRuleView, 0, len(rows))
	for _, row := range rows {
		var raw []string
		if err := json.Unmarshal(row.SensitiveColumns, &raw); err != nil {
			return nil, err
		}
		normalized, err := normalizeSensitiveColumns(raw)
		if err != nil || !sameStrings(raw, normalized) {
			return nil, errors.New("query: invalid persisted masking vocabulary")
		}
		out = append(out, FlowMaskingRuleView{
			DatasourceID: row.DatasourceID, SensitiveColumns: normalized, Version: row.Version,
		})
	}
	return out, nil
}

// ReplaceMaskingRule performs an atomic upsert in a serializable transaction. Because OpenAPI does not define If-Match
// on this endpoint, concurrent writes rely on database serialization + conflict retry, strictly incrementing version on success.
func (s *Services) ReplaceMaskingRule(ctx context.Context, actor Actor, flowID, datasourceID string,
	columns []string) (*FlowMaskingRuleView, error) {
	if !actor.IsBuiltinAdmin {
		return nil, domain.ErrNotFound()
	}
	normalized, err := normalizeSensitiveColumns(columns)
	if err != nil {
		return nil, domain.ErrValidation()
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	var saved persistence.FlowMaskingRuleModel
	err = s.serializable(ctx, func(tx *gorm.DB) error {
		// Lock order matches repository-commands: flow -> capability -> rule.
		var lockedFlowID string
		flowResult := tx.WithContext(ctx).Raw(
			`select id::text from flows where id = ? and flow_type = 'query_access' for update`, flowID).
			Scan(&lockedFlowID)
		if flowResult.Error != nil {
			return flowResult.Error
		}
		if flowResult.RowsAffected != 1 {
			return domain.ErrNotFound()
		}
		var lockedCapabilityFlowID string
		capResult := tx.WithContext(ctx).Raw(`
			select flow_id::text from flow_query_capabilities
			where flow_id = ? and datasource_id = ? for update`, flowID, datasourceID).
			Scan(&lockedCapabilityFlowID)
		if capResult.Error != nil {
			return capResult.Error
		}
		if capResult.RowsAffected != 1 {
			return domain.ErrNotFound()
		}
		var priorVersion int64
		if result := tx.WithContext(ctx).Raw(`
			select version from flow_masking_rules
			where flow_id = ? and datasource_id = ? for update`, flowID, datasourceID).
			Scan(&priorVersion); result.Error != nil {
			return result.Error
		}
		result := tx.WithContext(ctx).Raw(`
			insert into flow_masking_rules
				(flow_id, datasource_id, sensitive_columns, version, created_at, updated_at)
			values (?, ?, ?::jsonb, 1, now(), now())
			on conflict (flow_id, datasource_id) do update set
				sensitive_columns = excluded.sensitive_columns,
				version = flow_masking_rules.version + 1,
				updated_at = excluded.updated_at
			returning flow_id, datasource_id, sensitive_columns, version, created_at, updated_at`,
			flowID, datasourceID, string(raw)).Scan(&saved)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || saved.Version != priorVersion+1 {
			return domain.ErrConcurrent()
		}
		audit := auditInput(actor, "flow_masking_rule.replaced", "flow_masking_rules",
			flowID, "replace", "succeeded")
		audit.Metadata = map[string]any{
			"flow_id": flowID, "datasource_id": datasourceID, "version": saved.Version,
		}
		if err := s.Recorder.Audit(ctx, tx, audit); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &FlowMaskingRuleView{
		DatasourceID: saved.DatasourceID, SensitiveColumns: normalized, Version: saved.Version,
	}, nil
}

func normalizeSensitiveColumns(columns []string) ([]string, error) {
	if len(columns) > maxSensitiveColumns {
		return nil, domain.ErrValidation()
	}
	out := make([]string, 0, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if n := utf8.RuneCountInString(column); n < 1 || n > 128 {
			return nil, domain.ErrValidation()
		}
		column = domain.FoldIdentifier(column)
		// Unicode full case folding can expand characters (e.g. ß -> ss); the normalized key persisted
		// after folding must still satisfy the OpenAPI 1..128 character length bound per item.
		if n := utf8.RuneCountInString(column); n < 1 || n > 128 {
			return nil, domain.ErrValidation()
		}
		if _, ok := seen[column]; ok {
			continue
		}
		seen[column] = struct{}{}
		out = append(out, column)
	}
	return out, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
