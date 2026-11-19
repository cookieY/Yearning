package legacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourceTables lists the 16 legacy tables covered by the field mapping contract, ordered by dependency.
var SourceTables = []string{
	"core_accounts",
	"core_global_configurations",
	"core_data_sources",
	"core_role_groups",
	"core_graineds",
	"core_workflow_tpls",
	"core_rules",
	"core_sql_orders",
	"core_sql_records",
	"core_rollbacks",
	"core_workflow_details",
	"core_order_comments",
	"core_query_orders",
	"core_query_records",
	"core_auto_tasks",
	"core_total_tickets",
}

// Row represents a single legacy database row keyed by legacy column name (GORM default snake_case).
type Row map[string]any

// Source is the legacy database reader interface. Implementations must stream rows in ascending primary key order.
type Source interface {
	Close() error
	Ping(ctx context.Context) error
	// ServerVersion returns the legacy database version string (recorded in Manifest.source.release).
	ServerVersion(ctx context.Context) (string, error)
	// TableSchema returns the ordered list of column names and types used to compute the schema fingerprint.
	TableSchema(ctx context.Context, table string) ([]ColumnSpec, error)
	// StreamRows streams rows in batches ordered by ascending primary key.
	StreamRows(ctx context.Context, table string, batch int, fn func([]Row) error) error
	// CountRows returns the total row count of a table (used by inspect and reconciliation).
	CountRows(ctx context.Context, table string) (int64, error)
}

// ColumnSpec describes a column in the legacy database.
type ColumnSpec struct {
	Name     string
	DataType string
	Nullable bool
}

// MySQLSource reads from a live legacy MySQL database via go-sql-driver/mysql.
type MySQLSource struct {
	db *sql.DB
}

// OpenMySQL opens a connection to the legacy MySQL database with the same TLS semantics as the migration target.
func OpenMySQL(ep Endpoint) (*MySQLSource, error) {
	password, err := ep.Password.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve source password: %w", err)
	}
	tlsCfg, err := buildMySQLTLS(ep.TLS)
	if err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&charset=utf8mb4",
		ep.Username, string(password), ep.Host, ep.Port, ep.Database)
	if tlsCfg != nil {
		if err := registerMySQLTLS("yearning-migration", tlsCfg); err != nil {
			return nil, err
		}
		dsn += "&tls=yearning-migration"
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(2)
	return &MySQLSource{db: db}, nil
}

func (s *MySQLSource) Close() error { return s.db.Close() }

func (s *MySQLSource) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *MySQLSource) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := s.db.QueryRowContext(ctx, "select version()").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

func (s *MySQLSource) TableSchema(ctx context.Context, table string) ([]ColumnSpec, error) {
	rows, err := s.db.QueryContext(ctx, `
		select column_name, column_type, is_nullable
		from information_schema.columns
		where table_schema = database() and table_name = ?
		order by ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnSpec
	for rows.Next() {
		var c ColumnSpec
		var nullable string
		if err := rows.Scan(&c.Name, &c.DataType, &nullable); err != nil {
			return nil, err
		}
		c.Nullable = nullable == "YES"
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *MySQLSource) StreamRows(ctx context.Context, table string, batch int, fn func([]Row) error) error {
	if !validLegacyTable(table) {
		return fmt.Errorf("table %s not in legacy contract", table)
	}
	query := strings.Join([]string{"select * from", table, "order by id"}, " ")
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	buffered := make([]Row, 0, batch)
	flush := func() error {
		if len(buffered) == 0 {
			return nil
		}
		if err := fn(buffered); err != nil {
			return err
		}
		buffered = buffered[:0]
		return nil
	}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make(Row, len(cols))
		for i, c := range cols {
			row[c] = normalizeMySQLValue(values[i])
		}
		buffered = append(buffered, row)
		if len(buffered) >= batch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

func (s *MySQLSource) CountRows(ctx context.Context, table string) (int64, error) {
	if !validLegacyTable(table) {
		return 0, fmt.Errorf("table %s not in legacy contract", table)
	}
	var n int64
	query := strings.Join([]string{"select count(*) from", table}, " ")
	if err := s.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func validLegacyTable(table string) bool {
	for _, t := range SourceTables {
		if t == table {
			return true
		}
	}
	return false
}

// SnapshotSource reads synthetic/sanitized data from JSON snapshots under testdata/migration.
// Production always uses MySQLSource; SnapshotSource serves tests and CI only (PRD §8-7: CI uses sanitized/synthetic snapshots).
type SnapshotSource struct {
	dir string
	// schema describes the expected structure of a snapshot (derived from frozen legacy model.go behavior).
	schemas map[string][]ColumnSpec
}

// OpenSnapshot opens a snapshot directory containing one <table>.jsonl file per table plus _schema.json.
func OpenSnapshot(dir string) (*SnapshotSource, error) {
	schemaPath := filepath.Join(dir, "_schema.json")
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read snapshot schema: %w", err)
	}
	var schemaMap map[string][]ColumnSpec
	if err := json.Unmarshal(raw, &schemaMap); err != nil {
		return nil, fmt.Errorf("parse snapshot schema: %w", err)
	}
	return &SnapshotSource{dir: dir, schemas: schemaMap}, nil
}

func (s *SnapshotSource) Close() error { return nil }

func (s *SnapshotSource) Ping(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(s.dir, "_schema.json")); err != nil {
		return err
	}
	return nil
}

func (s *SnapshotSource) ServerVersion(ctx context.Context) (string, error) {
	return "synthetic-snapshot", nil
}

func (s *SnapshotSource) TableSchema(ctx context.Context, table string) ([]ColumnSpec, error) {
	cols, ok := s.schemas[table]
	if !ok {
		return nil, fmt.Errorf("snapshot schema missing table %s", table)
	}
	return cols, nil
}

// SnapshotRow preserves original JSON types when decoding .jsonl snapshot lines.
type snapshotFileMeta struct {
	Rows int `json:"rows"`
}

func (s *SnapshotSource) StreamRows(ctx context.Context, table string, batch int, fn func([]Row) error) error {
	if !validLegacyTable(table) {
		return fmt.Errorf("table %s not in legacy contract", table)
	}
	if _, ok := s.schemas[table]; !ok {
		return fmt.Errorf("snapshot schema missing table %s", table)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, table+".jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Contracted tables without a .jsonl data file are treated as having 0 rows
		}
		return err
	}
	buffered := make([]Row, 0, batch)
	flush := func() error {
		if len(buffered) == 0 {
			return nil
		}
		if err := fn(buffered); err != nil {
			return err
		}
		buffered = buffered[:0]
		return nil
	}
	var lastID int64 = -1
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row Row
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&row); err != nil {
			return fmt.Errorf("parse snapshot row in %s: %w", table, err)
		}
		id := rowInt(row["id"])
		if lastID >= 0 && id <= lastID {
			return fmt.Errorf("snapshot %s rows must be ordered by ascending id", table)
		}
		lastID = id
		buffered = append(buffered, normalizeSnapshotRow(row))
		if len(buffered) >= batch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func (s *SnapshotSource) CountRows(ctx context.Context, table string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, table+".jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var n int64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}

// normalizeSnapshotRow normalizes json.Number and []any into int64/[]any
// to ensure deterministic types for subsequent JCS hashing.
func normalizeSnapshotRow(row Row) Row {
	out := make(Row, len(row))
	for k, v := range row {
		out[k] = normalizeSnapshotValue(v)
	}
	return out
}

func normalizeSnapshotValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		return t.String() // Snapshots forbid non-integer numeric fields
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = normalizeSnapshotValue(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalizeSnapshotValue(item)
		}
		return out
	default:
		return v
	}
}

// SchemaFingerprint computes a canonical hash over column names, types, and nullability.
func SchemaFingerprint(src Source, ctx context.Context) (string, error) {
	fingerprint := map[string]any{}
	for _, table := range SourceTables {
		cols, err := src.TableSchema(ctx, table)
		if err != nil {
			return "", fmt.Errorf("schema %s: %w", table, err)
		}
		list := make([]any, 0, len(cols))
		for _, c := range cols {
			list = append(list, map[string]any{
				"name": c.Name, "type": c.DataType, "nullable": c.Nullable,
			})
		}
		fingerprint[table] = list
	}
	return JCSHash(fingerprint)
}

// TableNamesSorted returns a sorted copy of the contracted source table names.
func TableNamesSorted() []string {
	out := append([]string{}, SourceTables...)
	sort.Strings(out)
	return out
}

func rowInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case json.Number:
		i, _ := t.Int64()
		return i
	default:
		return -1
	}
}
