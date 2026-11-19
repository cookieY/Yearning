// adapter.go Composite Adapter implementation: each instance binds one engine dialect and endpoint.
// Engine-specific differences (fixed metadata SQL, Explain format, cursor semantics) live in mysql.go/postgres.go.
package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// baseAdapter implements all orchestration logic for the Adapter interface.
type baseAdapter struct {
	ep   Endpoint
	opts AdapterOptions

	mu     sync.Mutex
	open   []*PurposePool // Pools opened via OpenPurposePool (closed together on Close)
	closed bool
}

// NewMySQLAdapter constructs a MySQL dialect Adapter.
func NewMySQLAdapter(ep Endpoint, opts AdapterOptions) Adapter {
	return &mysqlAdapter{baseAdapter{ep: ep, opts: opts}}
}

// NewPostgreSQLAdapter constructs a PostgreSQL dialect Adapter.
func NewPostgreSQLAdapter(ep Endpoint, opts AdapterOptions) Adapter {
	return &postgresAdapter{baseAdapter{ep: ep, opts: opts}}
}

func (a *baseAdapter) engine() string { return a.ep.CompatibilityMod }

// ---- Connector ----

func (a *baseAdapter) TestConnection(ctx context.Context, cred Credential, mat *TLSMaterial) (*ConnectionTestResult, error) {
	start := time.Now()
	identity, err := a.ReadServerIdentity(ctx, cred, mat)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		var cls Classified
		if errors.Is(err, ErrUnsupportedTarget) {
			cls = Classified{Class: ClassUnknown, FailureName: FailUnsupportedTarget}
		} else {
			cls = ClassifyError(a.engine(), err)
		}
		return &ConnectionTestResult{
			Engine:               engineNameOf(a.engine()),
			CompatibilityMode:    a.ep.CompatibilityMod,
			LatencyMS:            latency,
			SanitizedFailureName: cls.FailureName,
		}, errors.New(cls.SanitizedMessage())
	}
	result := &ConnectionTestResult{
		Engine:                    identity.EngineFamily,
		CompatibilityMode:         a.ep.CompatibilityMod,
		ServerVersion:             identity.ServerVersion,
		ServerIdentityFingerprint: a.fingerprint(*identity),
		Capabilities:              map[string]bool{},
		LatencyMS:                 latency,
	}
	if a.opts.CapabilityResolver == nil {
		result.SanitizedFailureName = FailUnsupportedTarget
		return result, errors.New("database: capability_target_unsupported")
	}
	_, caps, err := a.opts.CapabilityResolver(engineNameOf(a.engine()), a.ep.CompatibilityMod, identity.ServerVersion)
	if err != nil {
		result.SanitizedFailureName = FailUnsupportedTarget
		return result, errors.New("database: capability_target_unsupported")
	}
	result.Capabilities = caps
	return result, nil
}

func (a *baseAdapter) OpenPurposePool(ctx context.Context, purpose Purpose, cred Credential, mat *TLSMaterial) (*PurposePool, error) {
	pool, err := openPool(ctx, a.engine(), a.ep, purpose, cred, mat, a.opts)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = pool.Close()
		return nil, errors.New("database: adapter closed")
	}
	a.open = append(a.open, pool)
	a.mu.Unlock()
	return pool, nil
}

// Close closes the Adapter and all pools it opened (contract Connector.Close).
func (a *baseAdapter) Close() error {
	a.mu.Lock()
	a.closed = true
	pools := a.open
	a.open = nil
	a.mu.Unlock()
	var firstErr error
	for _, p := range pools {
		if err := p.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *baseAdapter) ReadServerIdentity(ctx context.Context, cred Credential, mat *TLSMaterial) (*ServerIdentity, error) {
	switch a.engine() {
	case "mysql":
		return readMySQLIdentity(ctx, a, cred, mat)
	case "postgresql":
		return readPostgresIdentity(ctx, a, cred, mat)
	default:
		return nil, fmt.Errorf("%w: engine %q", ErrUnsupportedTarget, a.engine())
	}
}

// explainPrefix returns the dialect Explain prefix (fixed text, without execution options).
func explainPrefix(engine string) string {
	if engine == "mysql" {
		return mysqlExplainPrefix()
	}
	return pgExplainPrefix()
}

// parseExplain parses raw Explain output for the dialect.
func parseExplain(engine string, columns []string, rows [][]string) (*ExplainFact, error) {
	if engine == "mysql" {
		return parseMySQLExplain(columns, rows)
	}
	return parsePostgresExplain(columns, rows)
}

func engineNameOf(mode string) string {
	if mode == "mysql" {
		return "mysql"
	}
	return "postgresql"
}

func (a *baseAdapter) fingerprint(identity ServerIdentity) string {
	if a.opts.Fingerprint != nil {
		return a.opts.Fingerprint(identity)
	}
	material := fmt.Sprintf("engine=%s\nhost=%s\nport=%d\ninstance=%s",
		identity.EngineFamily, a.ep.Host, a.ep.Port, identity.InstanceMarker)
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

// ---- IdentifierQuoter ----

func (a *baseAdapter) QuoteIdentifier(name string) (string, error) {
	if a.engine() == "mysql" {
		return quoteMySQLOpen(name)
	}
	return quotePostgresOpen(name)
}

// ---- PlanReader ----

func (a *baseAdapter) ExplainWithoutExecution(ctx context.Context, pool *PurposePool, schema, statementSQL string) (*ExplainFact, error) {
	candidateSQL, err := nonExecutingExplainSQL(a.engine(), statementSQL)
	if err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	var fact *ExplainFact
	err = pool.withExplainQueryer(ctx, schema, func(q queryer) error {
		rows, err := q.QueryContext(ctx, candidateSQL)
		if err != nil {
			return errors.New("database: explain rejected")
		}
		defer func() { _ = rows.Close() }()
		columns, rawRows, err := collectStringRows(rows)
		if err != nil {
			return errors.New("database: explain output unreadable")
		}
		fact, err = parseExplain(a.engine(), columns, rawRows)
		return err
	})
	if err != nil {
		return nil, err
	}
	return fact, nil
}

// collectStringRows reads the result set into a string matrix (generic collector for Explain output).
func collectStringRows(rows *sql.Rows) ([]string, [][]string, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]string
	for rows.Next() {
		scan := make([]sql.NullString, len(columns))
		ptrs := make([]any, len(columns))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(columns))
		for i, v := range scan {
			row[i] = v.String
		}
		out = append(out, row)
	}
	return columns, out, rows.Err()
}

// ---- MetadataReader ----

func (a *baseAdapter) ListSchemas(ctx context.Context, pool *PurposePool) ([]string, error) {
	var out []string
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		rows, err := q.QueryContext(ctx, listSchemasSQL(a.engine()))
		if err != nil {
			return errors.New("database: metadata query failed")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			out = append(out, name)
		}
		return rows.Err()
	})
	return out, err
}

func (a *baseAdapter) ListTables(ctx context.Context, pool *PurposePool, schema string) ([]string, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	var out []string
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		rows, err := q.QueryContext(ctx, listTablesSQL(a.engine()), schema)
		if err != nil {
			return errors.New("database: metadata query failed")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			out = append(out, name)
		}
		return rows.Err()
	})
	return out, err
}

func (a *baseAdapter) ListColumns(ctx context.Context, pool *PurposePool, schema, table string) ([]MetadataColumn, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return nil, err
	}
	var out []MetadataColumn
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		rows, err := q.QueryContext(ctx, listColumnsSQL(a.engine()), schema, table)
		if err != nil {
			return errors.New("database: metadata query failed")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var col MetadataColumn
			var nullable bool
			if err := rows.Scan(&col.ColumnName, &col.Ordinal, &col.DatabaseType, &nullable,
				&col.ColumnKey, &col.ColumnDeflt, &col.ColumnExtra, &col.ColumnRemark); err != nil {
				return err
			}
			col.SchemaName = schema
			col.TableName = table
			col.Nullable = nullable
			col.SensitivePolicy = a.sensitivePolicy(col.ColumnName)
			out = append(out, col)
		}
		return rows.Err()
	})
	return out, err
}

// TableExists reports whether schema.table exists (read-only against information_schema;
// existence fact for gh-ost residual inspection, etc.; identifiers are parameterized).
func (a *baseAdapter) TableExists(ctx context.Context, pool *PurposePool, schema, table string) (bool, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return false, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return false, err
	}
	query, args := mysqlTableExistenceFactsSQL, []any{schema, table, schema, table, schema}
	if a.engine() == "postgresql" {
		query, args = pgTableExistenceFactsSQL, []any{schema, table}
	}
	var facts tableExistenceObservation
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return errors.New("database: metadata query failed")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if a.engine() == "postgresql" {
				err = rows.Scan(&facts.tableCount, &facts.unsupportedRelationCount,
					&facts.schemaCount, &facts.metadataPrivilege)
			} else {
				err = rows.Scan(&facts.tableCount, &facts.unsupportedRelationCount,
					&facts.metadataPrivilege)
			}
			if err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		return false, err
	}
	return classifyTableExistence(a.engine(), facts)
}

type tableExistenceObservation struct {
	tableCount               int64
	unsupportedRelationCount int64
	schemaCount              int64
	metadataPrivilege        int64
}

// classifyTableExistence only permits an absent result when the catalog query
// proves that the empty result is not caused by metadata visibility filtering.
// A present catalog row remains an observed table even when later definition
// reads fail; callers must then surface that read failure instead of inventing
// an empty definition.
func classifyTableExistence(engine string, facts tableExistenceObservation) (bool, error) {
	if facts.tableCount > 0 {
		if engine == "postgresql" && facts.metadataPrivilege == 0 {
			return false, ErrMetadataVisibilityUnproven
		}
		return true, nil
	}
	if facts.unsupportedRelationCount > 0 {
		return false, ErrUnsupportedRelationType
	}
	if engine == "postgresql" {
		if facts.schemaCount == 0 {
			return false, nil
		}
		if facts.metadataPrivilege == 0 {
			return false, ErrMetadataVisibilityUnproven
		}
		return false, nil
	}
	if facts.metadataPrivilege == 0 {
		return false, ErrMetadataVisibilityUnproven
	}
	return false, nil
}

// EstimateTableRows returns a bounded metadata-only row estimate. The bool is
// capability support; a supported nil result means the engine has no reliable
// estimate and must remain unknown, never zero.
func (a *baseAdapter) EstimateTableRows(ctx context.Context, pool *PurposePool, schema, table string) (*int64, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return nil, err
	}
	query := mysqlTableRowEstimateSQL
	if a.engine() == "postgresql" {
		query = pgTableRowEstimateSQL
	}
	var estimate sql.NullString
	found := false
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		err := q.QueryRowContext(ctx, query, schema, table).Scan(&estimate)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return errors.New("database: metadata query failed")
		}
		found = true
		return nil
	})
	if err != nil || !found || !estimate.Valid {
		return nil, err
	}
	value, parseErr := strconv.ParseInt(estimate.String, 10, 64)
	if parseErr != nil || value < 0 {
		return nil, errors.New("database: metadata query failed")
	}
	return &value, nil
}

// TriggerCountOn returns the number of triggers on a table (gh-ost residual inspection; read-only).
func (a *baseAdapter) TriggerCountOn(ctx context.Context, pool *PurposePool, schema, table string) (int64, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return 0, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return 0, err
	}
	var n int64
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		rows, err := q.QueryContext(ctx,
			`select count(*) from information_schema.triggers where event_object_schema = ? and event_object_table = ?`,
			schema, table)
		if err != nil {
			return errors.New("database: metadata query failed")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if err := rows.Scan(&n); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	return n, err
}

// tableExistenceReader is the existence fact surface of a concrete dialect Adapter
// (implementation extension beyond the contract interface; shared by Review metadata and gh-ost residual inspection).
type tableExistenceReader interface {
	TableExists(ctx context.Context, pool *PurposePool, schema, table string) (bool, error)
}

// tableTriggerReader is the trigger count extension for gh-ost residual inspection.
type tableTriggerReader interface {
	TriggerCountOn(ctx context.Context, pool *PurposePool, schema, table string) (int64, error)
}

type tableRowEstimator interface {
	EstimateTableRows(ctx context.Context, pool *PurposePool, schema, table string) (*int64, error)
}

// TableExists queries table existence via the concrete Adapter (read-only against information_schema).
func TableExists(ctx context.Context, a Adapter, pool *PurposePool, schema, table string) (bool, error) {
	fr, ok := a.(tableExistenceReader)
	if !ok {
		return false, ErrUnsupportedTarget
	}
	return fr.TableExists(ctx, pool, schema, table)
}

// TableRowEstimate invokes the optional metadata-only row estimator. The
// capability boolean distinguishes an old adapter from an available estimator
// that currently has no reliable value.
func TableRowEstimate(ctx context.Context, a Adapter, pool *PurposePool, schema, table string) (*int64, bool, error) {
	estimator, ok := a.(tableRowEstimator)
	if !ok {
		return nil, false, nil
	}
	value, err := estimator.EstimateTableRows(ctx, pool, schema, table)
	if err != nil {
		return nil, true, err
	}
	return value, true, nil
}

// TriggerCountOn queries the table trigger count via the concrete Adapter (read-only).
func TriggerCountOn(ctx context.Context, a Adapter, pool *PurposePool, schema, table string) (int64, error) {
	fr, ok := a.(tableTriggerReader)
	if !ok {
		return 0, ErrUnsupportedTarget
	}
	return fr.TriggerCountOn(ctx, pool, schema, table)
}

func (a *baseAdapter) sensitivePolicy(column string) string {
	if a.opts.SensitiveColumns == nil {
		return "none"
	}
	if _, ok := a.opts.SensitiveColumns[strings.ToLower(column)]; ok {
		return "masked"
	}
	return "none"
}

func (a *baseAdapter) ListIndexes(ctx context.Context, pool *PurposePool, schema, table string) ([]MetadataIndex, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return nil, err
	}
	var flat []struct {
		IndexName  string
		NonUnique  bool
		Primary    bool
		Column     sql.NullString
		Seq        int
		Role       string
		Expression bool
		Prefix     sql.NullInt64
	}
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		rows, err := q.QueryContext(ctx, listIndexesSQL(a.engine()), schema, table)
		if err != nil {
			return errors.New("database: metadata query failed")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var item struct {
				IndexName  string
				NonUnique  bool
				Primary    bool
				Column     sql.NullString
				Seq        int
				Role       string
				Expression bool
				Prefix     sql.NullInt64
			}
			if err := rows.Scan(&item.IndexName, &item.NonUnique, &item.Primary, &item.Column,
				&item.Seq, &item.Role, &item.Expression, &item.Prefix); err != nil {
				return err
			}
			flat = append(flat, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	byName := map[string]*MetadataIndex{}
	var order []string
	for _, item := range flat {
		idx, ok := byName[item.IndexName]
		if !ok {
			idx = &MetadataIndex{SchemaName: schema, TableName: table, IndexName: item.IndexName,
				NonUnique: item.NonUnique, Primary: item.Primary}
			byName[item.IndexName] = idx
			order = append(order, item.IndexName)
		}
		if item.Expression {
			idx.ExpressionCount++
		} else if item.Column.Valid && item.Column.String != "" {
			if item.Role == "include" {
				idx.IncludedColumns = append(idx.IncludedColumns, item.Column.String)
			} else {
				idx.ColumnNames = append(idx.ColumnNames, item.Column.String)
			}
		}
		if item.Prefix.Valid && item.Prefix.Int64 > 0 {
			idx.PrefixLengths = append(idx.PrefixLengths, int(item.Prefix.Int64))
		}
	}
	out := make([]MetadataIndex, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out, nil
}

func (a *baseAdapter) ReadSchemaSignature(ctx context.Context, pool *PurposePool, schema string) (*SchemaSignature, error) {
	tables, err := a.ListTables(ctx, pool, schema)
	if err != nil {
		return nil, err
	}
	var canonical strings.Builder
	count := 0
	for _, t := range tables {
		cols, err := a.ListColumns(ctx, pool, schema, t)
		if err != nil {
			return nil, err
		}
		idx, err := a.ListIndexes(ctx, pool, schema, t)
		if err != nil {
			return nil, err
		}
		canonical.WriteString("table:")
		canonical.WriteString(t)
		canonical.WriteString("\n")
		for _, c := range cols {
			fmt.Fprintf(&canonical, "col:%s|%d|%s|%v\n", c.ColumnName, c.Ordinal, c.DatabaseType, c.Nullable)
			count++
		}
		for _, i := range idx {
			fmt.Fprintf(&canonical, "idx:%s|%v|keys:%s|includes:%s|expressions:%d|prefixes:",
				i.IndexName, i.NonUnique, strings.Join(i.ColumnNames, ","),
				strings.Join(i.IncludedColumns, ","), i.ExpressionCount)
			for n, length := range i.PrefixLengths {
				if n > 0 {
					canonical.WriteByte(',')
				}
				fmt.Fprintf(&canonical, "%d", length)
			}
			canonical.WriteByte('\n')
			count++
		}
	}
	return &SchemaSignature{
		SchemaName:   schema,
		Signature:    hexSHA256([]byte(canonical.String())),
		ElementCount: count,
	}, nil
}
