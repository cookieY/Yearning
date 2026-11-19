package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// ColumnFact is the column descriptor shared by the contracted catalog and the live database catalog;
// only keys present in the contract file are compared.
type ColumnFact struct {
	OrdinalPosition    int     `json:"ordinal_position"`
	Column             string  `json:"column"`
	DatabaseType       string  `json:"database_type"`
	Nullable           bool    `json:"nullable"`
	DefaultExpression  *string `json:"default_expression"`
	IdentityKind       *string `json:"identity_kind"`
	GeneratedKind      *string `json:"generated_kind"`
	PrimaryKeyPosition int     `json:"primary_key_position"`
}

// TableFact holds the column set for a single table.
type TableFact struct {
	Table   string       `json:"table"`
	Columns []ColumnFact `json:"columns"`
}

// CatalogContract mirrors the structure of api/contracts/postgresql-column-catalog.json.
type CatalogContract struct {
	Source string      `json:"source"`
	Tables []TableFact `json:"tables"`
}

// LoadCatalogContract loads and parses the frozen PostgreSQL column catalog contract.
func LoadCatalogContract(path string) (*CatalogContract, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read column catalog: %w", err)
	}
	var c CatalogContract
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse column catalog: %w", err)
	}
	return &c, nil
}

// CatalogDiff holds the differences found by verify; all slices empty means the live schema matches the contract.
type CatalogDiff struct {
	MissingTables []string        `json:"missing_tables,omitempty"`
	ExtraTables   []string        `json:"extra_tables,omitempty"`
	TableDiffs    []TableDiffItem `json:"table_diffs,omitempty"`
}

// TableDiffItem describes column-level differences within a single table.
type TableDiffItem struct {
	Table          string       `json:"table"`
	MissingColumns []string     `json:"missing_columns,omitempty"`
	ExtraColumns   []string     `json:"extra_columns,omitempty"`
	ColumnDiffs    []ColumnDiff `json:"column_diffs,omitempty"`
}

// ColumnDiff describes attribute differences on a single column.
type ColumnDiff struct {
	Column string `json:"column"`
	Field  string `json:"field"`
	Expect any    `json:"expect"`
	Actual any    `json:"actual"`
}

// ForbiddenObjects counts database objects prohibited by contract (all counts must be 0).
type ForbiddenObjects struct {
	PhysicalForeignKeys int `json:"physical_foreign_keys"`
	UserTriggers        int `json:"user_triggers"`
	UserFunctions       int `json:"user_functions"`
	StoredProcedures    int `json:"stored_procedures"`
	AdvisoryLocksHeld   int `json:"advisory_locks_held"`
}

// VerifyReport is the output of `migrate verify`.
type VerifyReport struct {
	SchemaVersion  int64            `json:"schema_version"`
	ServerVersion  string           `json:"server_version"`
	CatalogMatched bool             `json:"catalog_matched"`
	TableCount     int              `json:"table_count"`
	ColumnCount    int              `json:"column_count"`
	Diff           CatalogDiff      `json:"diff"`
	Forbidden      ForbiddenObjects `json:"forbidden_objects"`
	Passed         bool             `json:"passed"`
}

// Verify compares the live pg_catalog against the frozen column catalog contract and counts any forbidden objects.
func (m *Migrator) Verify(ctx context.Context, contract *CatalogContract) (*VerifyReport, error) {
	report := &VerifyReport{}
	if err := m.Pool.QueryRow(ctx, "select current_setting('server_version')").Scan(&report.ServerVersion); err != nil {
		return nil, err
	}
	ledger, err := m.LoadLedger(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range ledger {
		if row.Version > report.SchemaVersion {
			report.SchemaVersion = row.Version
		}
	}
	live, err := m.queryCatalog(ctx)
	if err != nil {
		return nil, err
	}
	report.TableCount = len(live)
	for _, t := range live {
		report.ColumnCount += len(t.Columns)
	}
	report.Diff = diffCatalog(contract.Tables, live)
	report.CatalogMatched = len(report.Diff.MissingTables) == 0 &&
		len(report.Diff.ExtraTables) == 0 && len(report.Diff.TableDiffs) == 0
	if err := m.Pool.QueryRow(ctx, `
		select
			(select count(*) from pg_constraint where contype = 'f'),
			(select count(*) from pg_trigger where not tgisinternal),
			(select count(*) from pg_proc p join pg_namespace n on n.oid = p.pronamespace
			 where n.nspname = 'public' and p.prokind = 'f'),
			(select count(*) from pg_proc p join pg_namespace n on n.oid = p.pronamespace
			 where n.nspname = 'public' and p.prokind = 'p'),
			(select count(*) from pg_locks where locktype = 'advisory')`).
		Scan(&report.Forbidden.PhysicalForeignKeys, &report.Forbidden.UserTriggers,
			&report.Forbidden.UserFunctions, &report.Forbidden.StoredProcedures,
			&report.Forbidden.AdvisoryLocksHeld); err != nil {
		return nil, err
	}
	forbiddenZero := report.Forbidden.PhysicalForeignKeys == 0 &&
		report.Forbidden.UserTriggers == 0 && report.Forbidden.UserFunctions == 0 &&
		report.Forbidden.StoredProcedures == 0 && report.Forbidden.AdvisoryLocksHeld == 0
	report.Passed = report.CatalogMatched && forbiddenZero && report.SchemaVersion > 0
	return report, nil
}

// queryCatalog replicates the exact catalog query logic from scripts/contracts/export-postgresql-column-catalog.sql
// (format_type, pg_get_expr, and primary-key ordinal positions from pg_constraint).
func (m *Migrator) queryCatalog(ctx context.Context) ([]TableFact, error) {
	rows, err := m.Pool.Query(ctx, `
		with primary_key_columns as (
			select constraint_row.conrelid, key_column.attnum,
			       key_column.ordinality as primary_key_position
			from pg_constraint constraint_row
			cross join lateral unnest(constraint_row.conkey)
			    with ordinality as key_column(attnum, ordinality)
			where constraint_row.contype = 'p'
		)
		select table_row.relname as table_name,
		       attribute_row.attnum as ordinal_position,
		       attribute_row.attname as column_name,
		       pg_catalog.format_type(attribute_row.atttypid, attribute_row.atttypmod) as database_type,
		       not attribute_row.attnotnull as nullable,
		       pg_get_expr(default_row.adbin, default_row.adrelid) as default_expression,
		       coalesce(primary_key_columns.primary_key_position, 0) as primary_key_position
		from pg_class table_row
		join pg_namespace namespace_row on namespace_row.oid = table_row.relnamespace
		join pg_attribute attribute_row on attribute_row.attrelid = table_row.oid
		left join pg_attrdef default_row
		    on default_row.adrelid = table_row.oid and default_row.adnum = attribute_row.attnum
		left join primary_key_columns
		    on primary_key_columns.conrelid = table_row.oid and primary_key_columns.attnum = attribute_row.attnum
		where namespace_row.nspname = 'public'
		  and table_row.relkind = 'r'
		  and attribute_row.attnum > 0
		  and not attribute_row.attisdropped
		order by table_name, ordinal_position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTable := map[string]*TableFact{}
	var order []string
	for rows.Next() {
		var tableName string
		var col ColumnFact
		if err := rows.Scan(&tableName, &col.OrdinalPosition, &col.Column,
			&col.DatabaseType, &col.Nullable, &col.DefaultExpression,
			&col.PrimaryKeyPosition); err != nil {
			return nil, err
		}
		t, ok := byTable[tableName]
		if !ok {
			t = &TableFact{Table: tableName}
			byTable[tableName] = t
			order = append(order, tableName)
		}
		t.Columns = append(t.Columns, col)
	}
	out := make([]TableFact, 0, len(order))
	for _, name := range order {
		out = append(out, *byTable[name])
	}
	return out, rows.Err()
}

func diffCatalog(contract, live []TableFact) CatalogDiff {
	var diff CatalogDiff
	liveByName := make(map[string]TableFact, len(live))
	for _, t := range live {
		liveByName[t.Table] = t
	}
	contractNames := make(map[string]bool, len(contract))
	for _, ct := range contract {
		contractNames[ct.Table] = true
		lt, ok := liveByName[ct.Table]
		if !ok {
			diff.MissingTables = append(diff.MissingTables, ct.Table)
			continue
		}
		item := diffTable(ct, lt)
		if len(item.MissingColumns) > 0 || len(item.ExtraColumns) > 0 || len(item.ColumnDiffs) > 0 {
			diff.TableDiffs = append(diff.TableDiffs, item)
		}
	}
	for _, lt := range live {
		if !contractNames[lt.Table] {
			diff.ExtraTables = append(diff.ExtraTables, lt.Table)
		}
	}
	return diff
}

func diffTable(contract, live TableFact) TableDiffItem {
	item := TableDiffItem{Table: contract.Table}
	liveCols := make(map[string]ColumnFact, len(live.Columns))
	for _, c := range live.Columns {
		liveCols[c.Column] = c
	}
	seen := make(map[string]bool, len(contract.Columns))
	for _, cc := range contract.Columns {
		seen[cc.Column] = true
		lc, ok := liveCols[cc.Column]
		if !ok {
			item.MissingColumns = append(item.MissingColumns, cc.Column)
			continue
		}
		if cc.OrdinalPosition != lc.OrdinalPosition {
			item.ColumnDiffs = append(item.ColumnDiffs, ColumnDiff{cc.Column, "ordinal_position", cc.OrdinalPosition, lc.OrdinalPosition})
		}
		if cc.DatabaseType != lc.DatabaseType {
			item.ColumnDiffs = append(item.ColumnDiffs, ColumnDiff{cc.Column, "database_type", cc.DatabaseType, lc.DatabaseType})
		}
		if cc.Nullable != lc.Nullable {
			item.ColumnDiffs = append(item.ColumnDiffs, ColumnDiff{cc.Column, "nullable", cc.Nullable, lc.Nullable})
		}
		if cc.DefaultExpression == nil && lc.DefaultExpression != nil || cc.DefaultExpression != nil && lc.DefaultExpression == nil {
			item.ColumnDiffs = append(item.ColumnDiffs, ColumnDiff{cc.Column, "default_expression", strPtrValue(cc.DefaultExpression), strPtrValue(lc.DefaultExpression)})
		} else if cc.DefaultExpression != nil && *cc.DefaultExpression != *lc.DefaultExpression {
			item.ColumnDiffs = append(item.ColumnDiffs, ColumnDiff{cc.Column, "default_expression", *cc.DefaultExpression, *lc.DefaultExpression})
		}
		if cc.PrimaryKeyPosition != lc.PrimaryKeyPosition {
			item.ColumnDiffs = append(item.ColumnDiffs, ColumnDiff{cc.Column, "primary_key_position", cc.PrimaryKeyPosition, lc.PrimaryKeyPosition})
		}
	}
	for _, lc := range live.Columns {
		if !seen[lc.Column] {
			item.ExtraColumns = append(item.ExtraColumns, lc.Column)
		}
	}
	return item
}

func strPtrValue(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
