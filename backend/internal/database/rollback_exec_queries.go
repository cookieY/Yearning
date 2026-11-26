// rollback_exec_queries.go holds rollback_exec domain helpers and operations.
package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RollbackCell represents a single column value captured for rollback forensics, preserving exact
// string/hex bytes and database types to prevent JSON serialization from degrading 64-bit integers or high-precision DECIMALs to float64.
type RollbackCell struct {
	Column       string `json:"column"`
	DatabaseType string `json:"database_type"`
	IsNull       bool   `json:"is_null"`
	Text         string `json:"text,omitempty"`
	HexBytes     string `json:"hex_bytes,omitempty"`
}

// Equal reports whether two cell values are semantically equal (used for pre-rollback conflict detection).
func (c RollbackCell) Equal(other RollbackCell) bool {
	if c.IsNull != other.IsNull {
		return false
	}
	if c.IsNull {
		return true
	}
	if c.HexBytes != "" || other.HexBytes != "" {
		return strings.EqualFold(c.HexBytes, other.HexBytes)
	}
	if c.Text == other.Text {
		return true
	}
	// Normalized boolean comparison (MySQL 1/0 vs true/false, PG t/f vs true/false).
	if b1, ok1 := parseBoolLike(c.Text, c.DatabaseType); ok1 {
		if b2, ok2 := parseBoolLike(other.Text, other.DatabaseType); ok2 {
			return b1 == b2
		}
	}
	// Normalized numeric trailing-zero comparison (e.g., DECIMAL 10.50 vs 10.5).
	if isNumericType(c.DatabaseType) || isNumericType(other.DatabaseType) {
		if normalizeDecimalString(c.Text) == normalizeDecimalString(other.Text) {
			return true
		}
	}
	return false
}

func parseBoolLike(val, dbType string) (bool, bool) {
	upper := strings.ToUpper(dbType)
	if !strings.Contains(upper, "BOOL") && upper != "TINYINT(1)" {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "t", "1":
		return true, true
	case "false", "f", "0":
		return false, true
	default:
		return false, false
	}
}

func isNumericType(dbType string) bool {
	upper := strings.ToUpper(dbType)
	for _, token := range []string{"INT", "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL"} {
		if strings.Contains(upper, token) {
			return true
		}
	}
	return false
}

func normalizeDecimalString(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// RollbackRow is a slice of cells ordered by column position for a single row.
type RollbackRow struct {
	Cells []RollbackCell `json:"cells"`
}

// Get looks up a cell by column name (case-insensitive).
func (r RollbackRow) Get(col string) (RollbackCell, bool) {
	for _, c := range r.Cells {
		if strings.EqualFold(c.Column, col) {
			return c, true
		}
	}
	return RollbackCell{}, false
}

// RollbackColumnMeta describes rollback-relevant metadata for a single column of a target table.
type RollbackColumnMeta struct {
	Name               string  `json:"name"`
	Ordinal            int     `json:"ordinal"`
	DataType           string  `json:"data_type"`
	ColumnType         string  `json:"column_type"`
	Nullable           bool    `json:"nullable"`
	DefaultExpr        *string `json:"default_expr,omitempty"`
	IsPrimaryKey       bool    `json:"is_primary_key"`
	PKOrdinal          int     `json:"pk_ordinal"`
	IsAutoIncrement    bool    `json:"is_auto_increment"`
	IsIdentity         bool    `json:"is_identity"`
	IdentityGeneration string  `json:"identity_generation,omitempty"` // ALWAYS | BY DEFAULT
	IsGenerated        bool    `json:"is_generated"`
	GenerationExpr     string  `json:"generation_expr,omitempty"`
	Comment            string  `json:"comment,omitempty"`
}

// RollbackIndexMeta describes index metadata of a target table.
type RollbackIndexMeta struct {
	Name      string   `json:"name"`
	Unique    bool     `json:"unique"`
	Primary   bool     `json:"primary"`
	Columns   []string `json:"columns"`
	IndexType string   `json:"index_type,omitempty"`
	CreateDDL string   `json:"create_ddl,omitempty"`
}

// RollbackConstraintMeta describes table-level constraint metadata (PRIMARY KEY / UNIQUE / CHECK / FOREIGN KEY).
type RollbackConstraintMeta struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"` // PRIMARY KEY | UNIQUE | CHECK | FOREIGN KEY
	Columns    []string `json:"columns,omitempty"`
	Definition string   `json:"definition,omitempty"`
}

// RollbackForeignKeyMeta describes child-table foreign key cascade rules referencing the current table.
type RollbackForeignKeyMeta struct {
	ChildSchema    string `json:"child_schema"`
	ChildTable     string `json:"child_table"`
	ConstraintName string `json:"constraint_name"`
	UpdateRule     string `json:"update_rule"`
	DeleteRule     string `json:"delete_rule"`
}

// TableRollbackMetadata aggregates the complete target table snapshot required for DML/DDL rollback.
type TableRollbackMetadata struct {
	Engine                string                   `json:"engine"`
	ServerVersion         string                   `json:"server_version"`
	ServerVersionNum      int                      `json:"server_version_num"`
	SchemaName            string                   `json:"schema_name"`
	TableName             string                   `json:"table_name"`
	Exists                bool                     `json:"exists"`
	IsPartitioned         bool                     `json:"is_partitioned"`
	TriggerCount          int                      `json:"trigger_count"`
	HasExplicitPrimaryKey bool                     `json:"has_explicit_primary_key"`
	IdentityKeyColumns    []string                 `json:"identity_key_columns"`
	Columns               []RollbackColumnMeta     `json:"columns"`
	Indexes               []RollbackIndexMeta      `json:"indexes"`
	Constraints           []RollbackConstraintMeta `json:"constraints"`
	IncomingForeignKeys   []RollbackForeignKeyMeta `json:"incoming_foreign_keys,omitempty"`
	CreateTableDDL        string                   `json:"create_table_ddl,omitempty"`
	EstimatedRows         int64                    `json:"estimated_rows"`
}

// ColumnByName looks up column metadata by name.
func (m *TableRollbackMetadata) ColumnByName(name string) (RollbackColumnMeta, bool) {
	if m == nil {
		return RollbackColumnMeta{}, false
	}
	for _, col := range m.Columns {
		if strings.EqualFold(col.Name, name) {
			return col, true
		}
	}
	return RollbackColumnMeta{}, false
}

// IndexByName looks up index metadata by name.
func (m *TableRollbackMetadata) IndexByName(name string) (RollbackIndexMeta, bool) {
	if m == nil {
		return RollbackIndexMeta{}, false
	}
	for _, idx := range m.Indexes {
		if strings.EqualFold(idx.Name, name) {
			return idx, true
		}
	}
	return RollbackIndexMeta{}, false
}

// ConstraintByName looks up constraint metadata by name.
func (m *TableRollbackMetadata) ConstraintByName(name string) (RollbackConstraintMeta, bool) {
	if m == nil {
		return RollbackConstraintMeta{}, false
	}
	for _, con := range m.Constraints {
		if strings.EqualFold(con.Name, name) {
			return con, true
		}
	}
	return RollbackConstraintMeta{}, false
}

// HasUnsafeIncomingCascade reports whether any child-table foreign key has ON UPDATE/DELETE CASCADE or SET NULL/DEFAULT.
func (m *TableRollbackMetadata) HasUnsafeIncomingCascade(forUpdate, forDelete bool) (bool, string) {
	if m == nil {
		return false, ""
	}
	for _, fk := range m.IncomingForeignKeys {
		if forUpdate {
			rule := strings.ToUpper(fk.UpdateRule)
			if rule == "CASCADE" || rule == "SET NULL" || rule == "SET DEFAULT" {
				return true, fmt.Sprintf("child table %s.%s foreign key %s has ON UPDATE %s", fk.ChildSchema, fk.ChildTable, fk.ConstraintName, rule)
			}
		}
		if forDelete {
			rule := strings.ToUpper(fk.DeleteRule)
			if rule == "CASCADE" || rule == "SET NULL" || rule == "SET DEFAULT" {
				return true, fmt.Sprintf("child table %s.%s foreign key %s has ON DELETE %s", fk.ChildSchema, fk.ChildTable, fk.ConstraintName, rule)
			}
		}
	}
	return false, ""
}

// RollbackCapableTx allows unit-test doubles to implement rollback transaction hooks without starting a real connection pool.
type RollbackCapableTx interface {
	DMLTransaction
	QueryRollbackRows(ctx context.Context, query string, args ...any) ([]string, []RollbackRow, error)
	ExecDMLWithLastInsertID(ctx context.Context, sqlText string) (*StatementResult, int64, error)
	InspectTableMetadata(ctx context.Context, schemaName, tableName string) (*TableRollbackMetadata, error)
}

type sqlQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// QueryTxRollbackRows executes a query within an open DML transaction and scans the result into a RollbackRow slice.
func QueryTxRollbackRows(ctx context.Context, tx DMLTransaction, query string, args ...any) ([]string, []RollbackRow, error) {
	if mock, ok := tx.(RollbackCapableTx); ok {
		return mock.QueryRollbackRows(ctx, query, args...)
	}
	dtx, ok := tx.(*dmlTx)
	if !ok || !dtx.open || dtx.tx == nil {
		return nil, nil, ErrTransactionRequired
	}
	rows, err := dtx.tx.QueryContext(dtx.execCtx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanRollbackRows(rows)
}

// ExecuteDMLWithLastInsertID executes a single DML statement within an open DML transaction and returns both StatementResult and LastInsertId (for MySQL auto-increment primary key capture).
func ExecuteDMLWithLastInsertID(ctx context.Context, a Adapter, tx DMLTransaction, sqlText string) (*StatementResult, int64, error) {
	if mock, ok := tx.(RollbackCapableTx); ok {
		return mock.ExecDMLWithLastInsertID(ctx, sqlText)
	}
	t, ok := tx.(*dmlTx)
	if !ok || !t.open || t.tx == nil {
		return nil, 0, ErrTransactionRequired
	}
	if sqlText == "" {
		return nil, 0, ErrStatementRequired
	}
	started := time.Now().UTC()
	sqlRes, err := t.tx.ExecContext(t.execCtx, sqlText)
	if err == nil {
		affected := int64(0)
		var lastID int64
		if sqlRes != nil {
			if n, rerr := sqlRes.RowsAffected(); rerr == nil {
				affected = n
			}
			if t.pool.engine == "mysql" {
				if id, lerr := sqlRes.LastInsertId(); lerr == nil {
					lastID = id
				}
			}
		}
		t.statements++
		return &StatementResult{
			StartedAt:       started,
			FinishedAt:      time.Now().UTC(),
			AffectedRows:    affected,
			EngineCodeClass: ClassSuccess,
			Outcome:         OutcomeSuccess,
			SendBoundary:    SendBoundarySent,
		}, lastID, nil
	}
	result := statementFailure(t.pool.engine, started, err)
	if result.Outcome == OutcomeResultUnknown {
		t.sealUnknown()
	}
	return result, 0, nil
}

const rollbackMetadataTimeout = 15 * time.Second

type (
	rollbackSearchPathContextKey          struct{}
	rollbackAllowSearchPathFallbackCtxKey struct{}
)

// WithRollbackSearchPath attaches an ordered PostgreSQL search_path to ctx for pool-backed
// rollback metadata/index queries (tx == nil) so unqualified identifier visibility matches execution.
func WithRollbackSearchPath(ctx context.Context, searchPath []string) context.Context {
	if len(searchPath) == 0 {
		return ctx
	}
	copied := append([]string(nil), searchPath...)
	return context.WithValue(ctx, rollbackSearchPathContextKey{}, copied)
}

func rollbackSearchPathFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	sp, _ := ctx.Value(rollbackSearchPathContextKey{}).([]string)
	return sp
}

// WithRollbackAllowSearchPathFallback controls whether PostgreSQL metadata inspection may fall back
// to pg_table_is_visible(c.oid) when an unqualified target is not in the initial default schema.
// Defaults to false so schema-qualified targets, CREATE TABLE targets, and RENAME TABLE destinations
// never resolve onto another schema in search_path.
func WithRollbackAllowSearchPathFallback(ctx context.Context, allow bool) context.Context {
	return context.WithValue(ctx, rollbackAllowSearchPathFallbackCtxKey{}, allow)
}

func rollbackAllowSearchPathFallbackFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	allow, _ := ctx.Value(rollbackAllowSearchPathFallbackCtxKey{}).(bool)
	return allow
}

// QueryPoolRollbackRows borrows an independent connection from the pool (optionally pinning schema) to execute read-only metadata/snapshot queries.
func QueryPoolRollbackRows(ctx context.Context, pool *PurposePool, schemaName string, query string, args ...any) ([]string, []RollbackRow, error) {
	if pool == nil || pool.db == nil {
		return nil, nil, errors.New("database: pool is nil")
	}
	qctx, cancel := context.WithTimeout(ctx, rollbackMetadataTimeout)
	defer cancel()
	conn, err := pool.db.Conn(qctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close() }()
	if pool.engine == "postgresql" && schemaName == "" {
		if sp := rollbackSearchPathFromContext(ctx); len(sp) > 0 {
			reset, err := applyConnRollbackSearchPath(qctx, conn, sp)
			if err != nil {
				return nil, nil, err
			}
			defer reset()
		}
	} else if schemaName != "" {
		if err := ValidateIdentifier(schemaName); err != nil {
			return nil, nil, err
		}
		reset, err := applyConnRollbackSchema(qctx, pool.engine, conn, schemaName)
		if err != nil {
			return nil, nil, err
		}
		defer reset()
	}
	rows, err := conn.QueryContext(qctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanRollbackRows(rows)
}

func scanRollbackRows(rows *sql.Rows) ([]string, []RollbackRow, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, err
	}
	var out []RollbackRow
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		cells := make([]RollbackCell, len(cols))
		for i, raw := range vals {
			dbType := ""
			if i < len(colTypes) && colTypes[i] != nil {
				dbType = colTypes[i].DatabaseTypeName()
			}
			cells[i] = normalizeRollbackCell(cols[i], dbType, raw)
		}
		out = append(out, RollbackRow{Cells: cells})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return cols, out, nil
}

func normalizeRollbackCell(colName, dbType string, raw any) RollbackCell {
	cell := RollbackCell{
		Column:       colName,
		DatabaseType: dbType,
	}
	if raw == nil {
		cell.IsNull = true
		return cell
	}
	switch v := raw.(type) {
	case bool:
		cell.Text = strconv.FormatBool(v)
	case int64:
		cell.Text = strconv.FormatInt(v, 10)
	case int32:
		cell.Text = strconv.FormatInt(int64(v), 10)
	case int:
		cell.Text = strconv.Itoa(v)
	case uint64:
		cell.Text = strconv.FormatUint(v, 10)
	case float64:
		cell.Text = strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		cell.Text = strconv.FormatFloat(float64(v), 'f', -1, 32)
	case string:
		cell.Text = v
	case []byte:
		if isBinaryDatabaseType(dbType) {
			cell.HexBytes = hex.EncodeToString(v)
		} else {
			cell.Text = string(v)
		}
	case time.Time:
		upper := strings.ToUpper(dbType)
		switch {
		case upper == "DATE":
			cell.Text = v.Format("2006-01-02")
		case upper == "TIME" || upper == "TIMETZ":
			cell.Text = v.Format("15:04:05.999999")
		case strings.Contains(upper, "TZ") || strings.Contains(upper, "TIME ZONE"):
			cell.Text = v.UTC().Format("2006-01-02 15:04:05.999999-07:00")
		default:
			cell.Text = v.UTC().Format("2006-01-02 15:04:05.999999")
		}
	default:
		cell.Text = fmt.Sprint(v)
	}
	return cell
}

func isBinaryDatabaseType(dbType string) bool {
	upper := strings.ToUpper(strings.TrimSpace(dbType))
	switch upper {
	case "BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BYTEA", "BIT":
		return true
	default:
		return false
	}
}

// InspectTableRollbackMetadata reads columns, primary/unique keys, indexes, constraints, triggers,
// foreign key cascades, and table creation DDL for a target table.
// When tx is non-nil, reads within the same DML transaction (guaranteeing the same transaction snapshot and metadata lock view as the subsequent DML);
// when tx is nil, borrows an independent connection from the pool (used for before/after schema snapshots around DDL execution).
func InspectTableRollbackMetadata(
	ctx context.Context,
	pool *PurposePool,
	tx DMLTransaction,
	schemaName, tableName string,
) (*TableRollbackMetadata, error) {
	allowFallback := rollbackAllowSearchPathFallbackFromContext(ctx)
	if mock, ok := tx.(RollbackCapableTx); ok {
		return mock.InspectTableMetadata(ctx, schemaName, tableName)
	}
	if tx != nil {
		dtx, ok := tx.(*dmlTx)
		if !ok || !dtx.open || dtx.tx == nil {
			return nil, ErrTransactionRequired
		}
		if dtx.pool.engine == "mysql" {
			return inspectMySQLTableRollbackMetadata(dtx.execCtx, dtx.tx, schemaName, tableName)
		}
		return inspectPostgresTableRollbackMetadata(dtx.execCtx, dtx.tx, schemaName, tableName, allowFallback)
	}
	if pool == nil || pool.db == nil {
		return nil, errors.New("database: pool is nil")
	}
	qctx, cancel := context.WithTimeout(ctx, rollbackMetadataTimeout)
	defer cancel()
	conn, err := pool.db.Conn(qctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if pool.engine == "postgresql" {
		if sp := rollbackSearchPathFromContext(ctx); len(sp) > 0 {
			reset, err := applyConnRollbackSearchPath(qctx, conn, sp)
			if err != nil {
				return nil, err
			}
			defer reset()
		} else if schemaName != "" {
			if err := ValidateIdentifier(schemaName); err != nil {
				return nil, err
			}
			reset, err := applyConnRollbackSchema(qctx, pool.engine, conn, schemaName)
			if err != nil {
				return nil, err
			}
			defer reset()
		}
		return inspectPostgresTableRollbackMetadata(qctx, conn, schemaName, tableName, allowFallback)
	}
	if schemaName != "" {
		if err := ValidateIdentifier(schemaName); err != nil {
			return nil, err
		}
		reset, err := applyConnRollbackSchema(qctx, pool.engine, conn, schemaName)
		if err != nil {
			return nil, err
		}
		defer reset()
	}
	return inspectMySQLTableRollbackMetadata(qctx, conn, schemaName, tableName)
}

func mapPGFKAction(code string) string {
	switch code {
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	case "r":
		return "RESTRICT"
	default:
		return "NO ACTION"
	}
}

func buildPostgresCreateTableDDL(meta *TableRollbackMetadata) string {
	if meta == nil || !meta.Exists {
		return ""
	}
	qSchema, err1 := quotePostgresOpen(meta.SchemaName)
	qTable, err2 := quotePostgresOpen(meta.TableName)
	if err1 != nil || err2 != nil {
		return ""
	}
	lines := make([]string, 0, len(meta.Columns)+len(meta.Constraints))
	for _, col := range meta.Columns {
		qCol, err := quotePostgresOpen(col.Name)
		if err != nil {
			continue
		}
		var part strings.Builder
		part.WriteString("    ")
		part.WriteString(qCol)
		part.WriteString(" ")
		isNextvalDefault := !col.IsIdentity && !col.IsGenerated && col.DefaultExpr != nil &&
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(*col.DefaultExpr)), "nextval(")
		if isNextvalDefault {
			switch strings.ToLower(strings.TrimSpace(col.ColumnType)) {
			case "bigint", "int8":
				part.WriteString("BIGSERIAL")
			case "smallint", "int2":
				part.WriteString("SMALLSERIAL")
			default:
				part.WriteString("SERIAL")
			}
		} else {
			part.WriteString(col.ColumnType)
		}
		if col.IsIdentity {
			gen := col.IdentityGeneration
			if gen == "" {
				gen = "BY DEFAULT"
			}
			part.WriteString(" GENERATED ")
			part.WriteString(gen)
			part.WriteString(" AS IDENTITY")
		} else if col.IsGenerated && col.GenerationExpr != "" {
			part.WriteString(" GENERATED ALWAYS AS (")
			part.WriteString(col.GenerationExpr)
			part.WriteString(") STORED")
		} else if !isNextvalDefault && col.DefaultExpr != nil && *col.DefaultExpr != "" {
			part.WriteString(" DEFAULT ")
			part.WriteString(*col.DefaultExpr)
		}
		if !col.Nullable {
			part.WriteString(" NOT NULL")
		}
		lines = append(lines, part.String())
	}
	for _, con := range meta.Constraints {
		qCon, err := quotePostgresOpen(con.Name)
		if err != nil || con.Definition == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("    CONSTRAINT %s %s", qCon, con.Definition))
	}
	return fmt.Sprintf("CREATE TABLE %s.%s (\n%s\n);", qSchema, qTable, strings.Join(lines, ",\n"))
}

func parseMySQLVersionNum(ver string) int {
	clean := strings.SplitN(strings.TrimSpace(ver), "-", 2)[0]
	parts := strings.Split(clean, ".")
	if len(parts) < 2 {
		return 0
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch := 0
	if len(parts) >= 3 {
		patch, _ = strconv.Atoi(parts[2])
	}
	return major*10000 + minor*100 + patch
}
