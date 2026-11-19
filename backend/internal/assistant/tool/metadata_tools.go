// Package tool implements the 4 read-only metadata tools and dual-track server-side authorization gate for the Database AI Assistant.
//
// Security Invariants (PRD §5.4 / §5.5 / §5.9):
//  1. Only assistant_list_schemas, assistant_list_tables, assistant_describe_table, and assistant_list_indexes
//     are exposed as read-only catalog operations; executing user/model SQL, running EXPLAIN, or sampling business rows is strictly forbidden.
//  2. Every tool invocation verifies target datasource permissions for the authenticated actor first:
//     - Change orders and DML/DDL scenarios require change_review flow submission permission (FlowDatasourceGranted);
//     - Read-only SELECT generation and optimization allow FlowDatasourceGranted or active query permission (QueryMetadataGranted);
//     - Unauthorized access immediately returns 6002 ASSISTANT_DATASOURCE_FORBIDDEN without calling the underlying adapter or disclosing object existence.
//  3. Before metadata reaches the model, free-text column remarks (ColumnRemark) and raw default literals (ColumnDeflt)
//     are stripped (normalized into the default_behavior enum: none | server_default | auto_increment | sequence | generated)
//     and mapped to anonymous tokens via TurnPrivacySession.
package tool

import (
	"context"
	"encoding/json"
	"strings"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
)

// Controlled read-only metadata tool identifiers.
const (
	ToolListSchemas   = "assistant_list_schemas"
	ToolListTables    = "assistant_list_tables"
	ToolDescribeTable = "assistant_describe_table"
	ToolListIndexes   = "assistant_list_indexes"
)

// DefaultBehavior enumerates sanitized column default behaviors (raw default literals are never exposed to the model).
type DefaultBehavior string

const (
	DefaultNone          DefaultBehavior = "none"
	DefaultServerDefault DefaultBehavior = "server_default"
	DefaultAutoIncrement DefaultBehavior = "auto_increment"
	DefaultSequence      DefaultBehavior = "sequence"
	DefaultGenerated     DefaultBehavior = "generated"
)

// ClassifyDefaultBehavior normalizes MetadataColumn.ColumnDeflt and ColumnExtra into a sanitized DefaultBehavior enum.
func ClassifyDefaultBehavior(columnDefault, columnExtra string) DefaultBehavior {
	extra := strings.ToLower(strings.TrimSpace(columnExtra))
	def := strings.ToLower(strings.TrimSpace(columnDefault))
	switch {
	case strings.Contains(extra, "auto_increment") || strings.Contains(extra, "identity"):
		return DefaultAutoIncrement
	case strings.Contains(extra, "generated") || strings.Contains(extra, "virtual") || strings.Contains(extra, "stored"):
		return DefaultGenerated
	case strings.Contains(def, "nextval("):
		return DefaultSequence
	case def != "" && def != "null":
		return DefaultServerDefault
	default:
		return DefaultNone
	}
}

// AnonymizedColumnFact represents the desensitized structural fact for a single column (excluding ColumnRemark and raw ColumnDeflt on wire).
// RawComment is marked json:"-" and held strictly in memory for explicit SAN ("san") endpoints.
type AnonymizedColumnFact struct {
	ColumnToken     string          `json:"column_token"`
	Ordinal         int             `json:"ordinal"`
	DataType        string          `json:"data_type"`
	Nullable        bool            `json:"nullable"`
	IsPrimaryKey    bool            `json:"is_primary_key"`
	IsUnique        bool            `json:"is_unique"`
	DefaultBehavior DefaultBehavior `json:"default_behavior"`
	RawComment      string          `json:"-"`
}

// AnonymizedIndexFact represents the desensitized structural fact for an index.
type AnonymizedIndexFact struct {
	IndexToken      string   `json:"index_token"`
	Primary         bool     `json:"primary"`
	Unique          bool     `json:"unique"`
	ColumnTokens    []string `json:"column_tokens"`
	IncludedTokens  []string `json:"included_tokens,omitempty"`
	ExpressionCount int      `json:"expression_count,omitempty"`
}

// AnonymizedTableDescription represents the complete desensitized metadata for a single table.
type AnonymizedTableDescription struct {
	SchemaToken string                 `json:"schema_token"`
	TableToken  string                 `json:"table_token"`
	Columns     []AnonymizedColumnFact `json:"columns"`
	Indexes     []AnonymizedIndexFact  `json:"indexes,omitempty"`
}

// DatasourceTarget identifies a target datasource after server-side permission verification.
type DatasourceTarget struct {
	DatasourceID      string
	DatasourceName    string
	Engine            string
	CompatibilityMode string
	DefaultDatabase   string
	ResolvedFlowID    string
	HasChangeGrant    bool
	HasQueryGrant     bool
}

// CatalogReader abstracts read-only catalog inspection (backed by database.Adapter in production and deterministic stubs in tests).
type CatalogReader interface {
	ListSchemas(ctx context.Context, ds DatasourceTarget) ([]string, error)
	ListTables(ctx context.Context, ds DatasourceTarget, schema string) ([]string, error)
	ListColumns(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataColumn, error)
	ListIndexes(ctx context.Context, ds DatasourceTarget, schema, table string) ([]database.MetadataIndex, error)
}

// Authorizer verifies user permissions for change order submission and query metadata access on a target datasource.
type Authorizer struct {
	DB      *gorm.DB
	Crypter *crypto.Crypter
	Catalog CatalogReader
}

// AuthorizeDatasource verifies whether the current user is authorized to access the target datasource (PRD §5.4 / §5.5 A1):
//   - requireChangeGrant=true (change order / DML / DDL): requires FlowDatasourceGranted;
//   - requireChangeGrant=false (read-only SELECT generation / optimization): requires FlowDatasourceGranted or QueryMetadataGranted;
//   - returns 6002 ASSISTANT_DATASOURCE_FORBIDDEN if the datasource does not exist, is disabled, or is unauthorized.
func (a *Authorizer) AuthorizeDatasource(
	ctx context.Context,
	userID string,
	datasourceID string,
	flowID string,
	requireChangeGrant bool,
) (DatasourceTarget, error) {
	if a == nil || a.DB == nil || strings.TrimSpace(userID) == "" || strings.TrimSpace(datasourceID) == "" {
		return DatasourceTarget{}, domain.ErrDatasourceForbidden()
	}

	var dsRow struct {
		ID                string  `gorm:"column:id"`
		Name              string  `gorm:"column:name"`
		Engine            string  `gorm:"column:engine"`
		CompatibilityMode string  `gorm:"column:compatibility_mode"`
		DatabaseName      *string `gorm:"column:database_name"`
		Enabled           bool    `gorm:"column:enabled"`
	}
	err := a.DB.WithContext(ctx).Raw(`
		select id::text as id, name, engine, compatibility_mode, database_name, enabled
		from datasources where id = ?`, datasourceID).Scan(&dsRow).Error
	if err != nil || dsRow.ID == "" || !dsRow.Enabled {
		return DatasourceTarget{}, domain.ErrDatasourceForbidden()
	}

	resolvedFlowID, hasChange, err := a.checkFlowDatasourceGranted(ctx, userID, datasourceID, flowID)
	if err != nil {
		return DatasourceTarget{}, err
	}

	hasQuery := false
	if !requireChangeGrant && !hasChange {
		hasQuery, err = a.checkQueryMetadataGranted(ctx, userID, datasourceID)
		if err != nil {
			return DatasourceTarget{}, err
		}
	}

	if requireChangeGrant && !hasChange {
		return DatasourceTarget{}, domain.ErrDatasourceForbidden()
	}
	if !requireChangeGrant && !hasChange && !hasQuery {
		return DatasourceTarget{}, domain.ErrDatasourceForbidden()
	}

	defaultDB := ""
	if dsRow.DatabaseName != nil {
		defaultDB = *dsRow.DatabaseName
	}
	return DatasourceTarget{
		DatasourceID:      dsRow.ID,
		DatasourceName:    dsRow.Name,
		Engine:            dsRow.Engine,
		CompatibilityMode: dsRow.CompatibilityMode,
		DefaultDatabase:   defaultDB,
		ResolvedFlowID:    resolvedFlowID,
		HasChangeGrant:    hasChange,
		HasQueryGrant:     hasQuery,
	}, nil
}

func (a *Authorizer) checkFlowDatasourceGranted(ctx context.Context, userID, datasourceID, flowID string) (string, bool, error) {
	var matchedFlowID string
	if strings.TrimSpace(flowID) != "" {
		err := a.DB.WithContext(ctx).Raw(`
			select f.id::text
			from flow_stages fs
			join flows f on f.id = fs.flow_id
			join permission_group_flow_grants g on g.flow_id = f.id
			join permission_group_members m on m.permission_group_id = g.permission_group_id
			join permission_groups pg on pg.id = g.permission_group_id and pg.enabled = true
			where m.user_id = ? and f.flow_type = 'change_review' and f.status = 'enabled'
			  and f.id = ? and fs.datasource_id = ?
			limit 1`, userID, flowID, datasourceID).Scan(&matchedFlowID).Error
		if err != nil {
			return "", false, err
		}
		return matchedFlowID, matchedFlowID != "", nil
	}

	err := a.DB.WithContext(ctx).Raw(`
		select f.id::text
		from flow_stages fs
		join flows f on f.id = fs.flow_id
		join permission_group_flow_grants g on g.flow_id = f.id
		join permission_group_members m on m.permission_group_id = g.permission_group_id
		join permission_groups pg on pg.id = g.permission_group_id and pg.enabled = true
		where m.user_id = ? and f.flow_type = 'change_review' and f.status = 'enabled'
		  and fs.datasource_id = ?
		order by f.created_at asc, f.id asc
		limit 1`, userID, datasourceID).Scan(&matchedFlowID).Error
	if err != nil {
		return "", false, err
	}
	return matchedFlowID, matchedFlowID != "", nil
}

func (a *Authorizer) checkQueryMetadataGranted(ctx context.Context, userID, datasourceID string) (bool, error) {
	// 1. Check whether an active, non-expired query_grants record authorizes this datasource (can_query = true).
	var grantCount int64
	err := a.DB.WithContext(ctx).Raw(`
		select count(1)
		from query_grants qg
		join query_grant_capabilities qgc on qgc.grant_id = qg.id
		where qg.requester_user_id_snapshot = ?
		  and qg.state = 'active'
		  and qg.expires_at > now()
		  and qgc.datasource_id = ?
		  and qgc.can_query = true`, userID, datasourceID).Scan(&grantCount).Error
	if err != nil {
		return false, err
	}
	if grantCount > 0 {
		return true, nil
	}

	// 2. Check whether an active query_sessions capability row authorizes this datasource.
	var sessionCount int64
	err = a.DB.WithContext(ctx).Raw(`
		select count(1)
		from query_sessions qs
		join query_session_capabilities qsc on qsc.session_id = qs.id
		where qs.user_id_snapshot = ?
		  and qs.state = 'active'
		  and qsc.datasource_id = ?
		  and qsc.state = 'active'
		  and qsc.can_query = true`, userID, datasourceID).Scan(&sessionCount).Error
	if err != nil {
		return false, err
	}
	if sessionCount > 0 {
		return true, nil
	}

	// 3. When query.approval_enabled = false, check whether the user is granted an enabled query_access flow covering this datasource.
	approvalEnabled := true
	var rawSettings *string
	if err := a.DB.WithContext(ctx).Raw(`select settings::text from system_settings where namespace = 'query'`).Scan(&rawSettings).Error; err == nil && rawSettings != nil && *rawSettings != "" {
		var doc struct {
			ApprovalEnabled *bool `json:"approval_enabled"`
		}
		if json.Unmarshal([]byte(*rawSettings), &doc) == nil && doc.ApprovalEnabled != nil {
			approvalEnabled = *doc.ApprovalEnabled
		}
	}
	if !approvalEnabled {
		var qflowCount int64
		err = a.DB.WithContext(ctx).Raw(`
			select count(1)
			from flow_query_datasource_capabilities fq
			join flows f on f.id = fq.flow_id
			join permission_group_flow_grants g on g.flow_id = f.id
			join permission_group_members m on m.permission_group_id = g.permission_group_id
			join permission_groups pg on pg.id = g.permission_group_id and pg.enabled = true
			where m.user_id = ? and f.flow_type = 'query_access' and f.status = 'enabled'
			  and fq.datasource_id = ? and fq.can_query = true`, userID, datasourceID).Scan(&qflowCount).Error
		if err != nil {
			return false, err
		}
		return qflowCount > 0, nil
	}
	return false, nil
}

// DescribeAuthorizedTable reads column and index metadata for an authorized table, strips remarks and raw defaults, and registers anonymous tokens in TurnPrivacySession.
func (a *Authorizer) DescribeAuthorizedTable(
	ctx context.Context,
	priv *privacy.TurnPrivacySession,
	ds DatasourceTarget,
	schema string,
	table string,
) (*AnonymizedTableDescription, error) {
	if a == nil || a.Catalog == nil || priv == nil {
		return nil, domain.ErrDatasourceForbidden()
	}
	if schema == "" {
		schema = ds.DefaultDatabase
		if schema == "" && ds.CompatibilityMode == "postgresql" {
			schema = "public"
		}
	}
	schemaTok, err := priv.RegisterSchema(schema)
	if err != nil {
		return nil, err
	}
	tableTok, err := priv.RegisterTable(schema, table)
	if err != nil {
		return nil, err
	}

	rawCols, err := a.Catalog.ListColumns(ctx, ds, schema, table)
	if err != nil {
		return nil, err
	}
	cols := make([]AnonymizedColumnFact, 0, len(rawCols))
	for _, rc := range rawCols {
		colTok, err := priv.RegisterColumn(schema, table, rc.ColumnName)
		if err != nil {
			return nil, err
		}
		keyUpper := strings.ToUpper(strings.TrimSpace(rc.ColumnKey))
		cols = append(cols, AnonymizedColumnFact{
			ColumnToken:     colTok,
			Ordinal:         rc.Ordinal,
			DataType:        rc.DatabaseType,
			Nullable:        rc.Nullable,
			IsPrimaryKey:    keyUpper == "PRI" || keyUpper == "PK",
			IsUnique:        keyUpper == "PRI" || keyUpper == "PK" || keyUpper == "UNI",
			DefaultBehavior: ClassifyDefaultBehavior(rc.ColumnDeflt, rc.ColumnExtra),
			RawComment:      strings.TrimSpace(rc.ColumnRemark),
		})
	}

	rawIdx, _ := a.Catalog.ListIndexes(ctx, ds, schema, table)
	idxs := make([]AnonymizedIndexFact, 0, len(rawIdx))
	for _, ri := range rawIdx {
		idxTok, err := priv.RegisterIndex(schema, table, ri.IndexName)
		if err != nil {
			return nil, err
		}
		var colToks []string
		for _, cn := range ri.ColumnNames {
			ct, err := priv.RegisterColumn(schema, table, cn)
			if err != nil {
				return nil, err
			}
			colToks = append(colToks, ct)
		}
		var incToks []string
		for _, in := range ri.IncludedColumns {
			it, err := priv.RegisterColumn(schema, table, in)
			if err != nil {
				return nil, err
			}
			incToks = append(incToks, it)
		}
		idxs = append(idxs, AnonymizedIndexFact{
			IndexToken:      idxTok,
			Primary:         ri.Primary,
			Unique:          !ri.NonUnique,
			ColumnTokens:    colToks,
			IncludedTokens:  incToks,
			ExpressionCount: ri.ExpressionCount,
		})
	}

	desc := &AnonymizedTableDescription{
		SchemaToken: schemaTok,
		TableToken:  tableTok,
		Columns:     cols,
		Indexes:     idxs,
	}
	rawJSON, err := json.Marshal(desc)
	if err != nil {
		return nil, domain.ErrAnonymizationFailed("marshal_table_description_failed")
	}
	if err := priv.AssertZeroLeak(string(rawJSON)); err != nil {
		return nil, err
	}
	return desc, nil
}
