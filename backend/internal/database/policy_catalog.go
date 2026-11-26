package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxPolicyCatalogSchemas       = 64
	maxPolicyCatalogIdentifiers   = 50000
	policyCatalogStatementTimeout = 5 * time.Second
)

// PolicyCatalogKind is the closed object-kind vocabulary used by policy text
// privacy. It is internal application data and is never sent to a Provider.
type PolicyCatalogKind string

const (
	PolicyCatalogSchema     PolicyCatalogKind = "schema"
	PolicyCatalogTable      PolicyCatalogKind = "table"
	PolicyCatalogView       PolicyCatalogKind = "view"
	PolicyCatalogColumn     PolicyCatalogKind = "column"
	PolicyCatalogIndex      PolicyCatalogKind = "index"
	PolicyCatalogConstraint PolicyCatalogKind = "constraint"
)

const policyCatalogStatusKind = "_schema_status"

var (
	ErrPolicyCatalogUnsupported       = errors.New("database: policy catalog unsupported")
	ErrPolicyCatalogUnavailable       = errors.New("database: policy catalog unavailable")
	ErrPolicyCatalogMalformed         = errors.New("database: policy catalog malformed")
	ErrPolicyCatalogTooLarge          = errors.New("database: policy catalog too large")
	ErrPolicyCatalogSchemaUnavailable = errors.New("database: policy catalog schema unavailable")
)

// PolicyCatalogIdentifier carries only a catalog identity. The owning
// application is responsible for keeping it in memory and never publishing it
// through a review evidence or Provider tool channel.
type PolicyCatalogIdentifier struct {
	Kind     PolicyCatalogKind
	Schema   string
	Relation string
	Name     string
}

// PolicyCatalog is a complete, bounded snapshot for the requested authorized
// physical schemas. ReadPolicyCatalog never returns a partial snapshot.
type PolicyCatalog struct {
	Identifiers []PolicyCatalogIdentifier
	SchemaCount int
}

type policyCatalogRow struct {
	Kind     string
	Schema   string
	Relation string
	Name     string
	Status   string
}

type policyCatalogReader interface {
	readPolicyCatalog(context.Context, *PurposePool, []string) ([]policyCatalogRow, error)
}

// ReadPolicyCatalog obtains one complete catalog snapshot without extending the
// Adapter interface. Adapters that do not implement the private reader fail
// closed, preserving existing test doubles and external implementations.
func ReadPolicyCatalog(
	ctx context.Context,
	adapter Adapter,
	pool *PurposePool,
	schemas []string,
) (PolicyCatalog, error) {
	if adapter == nil || pool == nil {
		return PolicyCatalog{}, ErrPolicyCatalogUnavailable
	}
	reader, ok := adapter.(policyCatalogReader)
	if !ok {
		return PolicyCatalog{}, ErrPolicyCatalogUnsupported
	}
	if pool.Purpose() != PurposeReview {
		return PolicyCatalog{}, fmt.Errorf("%w: review pool required", ErrPolicyCatalogUnsupported)
	}
	validated, err := validatePolicyCatalogSchemas(schemas)
	if err != nil {
		return PolicyCatalog{}, err
	}
	rows, err := reader.readPolicyCatalog(ctx, pool, validated)
	if err != nil {
		return PolicyCatalog{}, err
	}
	return normalizePolicyCatalogRows(rows, validated)
}

func validatePolicyCatalogSchemas(schemas []string) ([]string, error) {
	if len(schemas) == 0 {
		return nil, fmt.Errorf("%w: no schemas", ErrPolicyCatalogMalformed)
	}
	if len(schemas) > maxPolicyCatalogSchemas {
		return nil, ErrPolicyCatalogTooLarge
	}
	seen := make(map[string]struct{}, len(schemas))
	out := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		if err := ValidateIdentifier(schema); err != nil {
			return nil, fmt.Errorf("%w: schema identifier", ErrPolicyCatalogMalformed)
		}
		if _, exists := seen[schema]; exists {
			return nil, fmt.Errorf("%w: duplicate schema", ErrPolicyCatalogMalformed)
		}
		seen[schema] = struct{}{}
		out = append(out, schema)
	}
	return out, nil
}

func normalizePolicyCatalogRows(rows []policyCatalogRow, schemas []string) (PolicyCatalog, error) {
	requested := make(map[string]struct{}, len(schemas))
	for _, schema := range schemas {
		requested[schema] = struct{}{}
	}
	visible := make(map[string]struct{}, len(schemas))
	seen := make(map[PolicyCatalogIdentifier]struct{})
	identifiers := make([]PolicyCatalogIdentifier, 0, len(rows))
	for _, row := range rows {
		if row.Kind == policyCatalogStatusKind {
			if _, ok := requested[row.Schema]; !ok || row.Status == "" {
				return PolicyCatalog{}, ErrPolicyCatalogMalformed
			}
			if _, duplicate := visible[row.Schema]; duplicate {
				return PolicyCatalog{}, ErrPolicyCatalogMalformed
			}
			switch row.Status {
			case "visible":
				visible[row.Schema] = struct{}{}
			case "unprivileged":
				return PolicyCatalog{}, ErrMetadataVisibilityUnproven
			case "missing":
				return PolicyCatalog{}, fmt.Errorf("%w: schema", ErrPolicyCatalogSchemaUnavailable)
			default:
				return PolicyCatalog{}, ErrPolicyCatalogMalformed
			}
			continue
		}
		identifier := PolicyCatalogIdentifier{
			Kind: PolicyCatalogKind(row.Kind), Schema: row.Schema,
			Relation: row.Relation, Name: row.Name,
		}
		if err := validatePolicyCatalogIdentifier(identifier); err != nil {
			return PolicyCatalog{}, err
		}
		if _, exists := requested[identifier.Schema]; !exists {
			return PolicyCatalog{}, ErrPolicyCatalogMalformed
		}
		if _, duplicate := seen[identifier]; duplicate {
			continue
		}
		seen[identifier] = struct{}{}
		identifiers = append(identifiers, identifier)
	}
	if len(visible) != len(requested) {
		return PolicyCatalog{}, ErrMetadataVisibilityUnproven
	}
	if len(identifiers) > maxPolicyCatalogIdentifiers {
		return PolicyCatalog{}, ErrPolicyCatalogTooLarge
	}
	sort.Slice(identifiers, func(i, j int) bool {
		a, b := identifiers[i], identifiers[j]
		if a.Schema != b.Schema {
			return a.Schema < b.Schema
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Relation != b.Relation {
			return a.Relation < b.Relation
		}
		return a.Name < b.Name
	})
	return PolicyCatalog{Identifiers: identifiers, SchemaCount: len(requested)}, nil
}

func validatePolicyCatalogIdentifier(identifier PolicyCatalogIdentifier) error {
	switch identifier.Kind {
	case PolicyCatalogSchema:
		if identifier.Relation != "" || identifier.Name != identifier.Schema {
			return ErrPolicyCatalogMalformed
		}
	case PolicyCatalogTable, PolicyCatalogView:
		if identifier.Relation != "" || identifier.Name == "" {
			return ErrPolicyCatalogMalformed
		}
	case PolicyCatalogColumn, PolicyCatalogIndex, PolicyCatalogConstraint:
		if identifier.Relation == "" || identifier.Name == "" {
			return ErrPolicyCatalogMalformed
		}
	default:
		return ErrPolicyCatalogMalformed
	}
	for _, name := range []string{identifier.Schema, identifier.Relation, identifier.Name} {
		if name == "" {
			continue
		}
		if len(name) > 256 || !utf8.ValidString(name) {
			return ErrPolicyCatalogMalformed
		}
		for _, r := range name {
			if unicode.IsControl(r) || r == 0 {
				return ErrPolicyCatalogMalformed
			}
		}
	}
	return nil
}

func (a *baseAdapter) readPolicyCatalog(
	ctx context.Context,
	pool *PurposePool,
	schemas []string,
) ([]policyCatalogRow, error) {
	if a == nil || pool == nil || pool.Purpose() != PurposeReview || pool.Engine() != a.engine() {
		return nil, ErrPolicyCatalogUnsupported
	}
	query, args, err := policyCatalogQuery(a.engine(), len(schemas))
	if err != nil {
		return nil, err
	}
	repetitions := 1
	if a.engine() == "mysql" {
		// status requested set + schema visibility + (table/column/index/constraint: visible subquery + direct WHERE schema pruning).
		repetitions = 10
	}
	for i := 0; i < repetitions; i++ {
		for _, schema := range schemas {
			args = append(args, schema)
		}
	}
	args = append(args, maxPolicyCatalogIdentifiers+len(schemas)+1)

	queryCtx, cancel := context.WithTimeout(ctx, policyCatalogStatementTimeout)
	defer cancel()
	var rows []policyCatalogRow
	err = pool.withReadQueryer(queryCtx, true, func(q queryer) error {
		result, err := q.QueryContext(queryCtx, query, args...)
		if err != nil {
			return ErrPolicyCatalogUnavailable
		}
		defer func() { _ = result.Close() }()
		for result.Next() {
			var row policyCatalogRow
			var rowKind, kind, schema, relation, name, status sql.NullString
			if err := result.Scan(
				&rowKind, &kind, &schema, &relation, &name, &status,
			); err != nil {
				return ErrPolicyCatalogMalformed
			}
			row.Kind = rowKind.String
			if row.Kind != policyCatalogStatusKind {
				row.Kind = kind.String
			}
			row.Schema = schema.String
			row.Relation = relation.String
			row.Name = name.String
			row.Status = status.String
			rows = append(rows, row)
			if len(rows) > maxPolicyCatalogIdentifiers+len(schemas)+1 {
				return ErrPolicyCatalogTooLarge
			}
		}
		if err := result.Err(); err != nil {
			return ErrPolicyCatalogUnavailable
		}
		return nil
	})
	if err != nil {
		for _, sentinel := range []error{
			ErrPolicyCatalogUnavailable,
			ErrPolicyCatalogMalformed,
			ErrPolicyCatalogTooLarge,
			ErrPolicyCatalogUnsupported,
		} {
			if errors.Is(err, sentinel) {
				return nil, err
			}
		}
		// Pool checkout and driver errors are catalog-read failures. Do not
		// pass an implementation-specific database error to the privacy gate.
		return nil, ErrPolicyCatalogUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrPolicyCatalogUnavailable
	}
	if err := queryCtx.Err(); err != nil {
		return nil, ErrPolicyCatalogUnavailable
	}
	return rows, nil
}

func policyCatalogQuery(engine string, schemaCount int) (string, []any, error) {
	if schemaCount <= 0 || schemaCount > maxPolicyCatalogSchemas {
		return "", nil, ErrPolicyCatalogMalformed
	}
	switch engine {
	case "postgresql":
		return postgresPolicyCatalogSQL(schemaCount), nil, nil
	case "mysql":
		return mysqlPolicyCatalogSQL(schemaCount), nil, nil
	default:
		return "", nil, ErrPolicyCatalogUnsupported
	}
}

func policyPlaceholders(start, count int, castText bool) string {
	values := make([]string, 0, count)
	for i := 0; i < count; i++ {
		value := fmt.Sprintf("$%d", start+i)
		if castText {
			value += "::text"
		}
		values = append(values, "("+value+")")
	}
	return strings.Join(values, ",")
}

func postgresPolicyCatalogSQL(schemaCount int) string {
	values := policyPlaceholders(1, schemaCount, true)
	limitArg := schemaCount + 1
	return `
WITH requested(schema_name) AS (VALUES ` + values + `),
schema_state AS (
  SELECT r.schema_name,
    CASE
      WHEN n.oid IS NULL THEN 'missing'
      WHEN pg_catalog.has_schema_privilege(n.oid, 'USAGE') THEN 'visible'
      ELSE 'unprivileged'
    END AS schema_status
  FROM requested r
  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = r.schema_name
),
catalog AS (
  SELECT 'identifier'::text AS row_kind, 'schema'::text AS kind,
    ss.schema_name, ''::text AS relation_name, ss.schema_name AS object_name,
    ''::text AS schema_status
  FROM schema_state ss WHERE ss.schema_status = 'visible'
  UNION
  SELECT 'identifier', CASE WHEN c.relkind IN ('v','m') THEN 'view' ELSE 'table' END,
    n.nspname, '', c.relname, ''
  FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN schema_state ss ON ss.schema_name = n.nspname
  WHERE ss.schema_status = 'visible' AND c.relkind IN ('r','p','v','m','f')
  UNION
  SELECT 'identifier', 'column', n.nspname, c.relname, a.attname, ''
  FROM pg_catalog.pg_attribute a
  JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  JOIN schema_state ss ON ss.schema_name = n.nspname
  WHERE ss.schema_status = 'visible' AND c.relkind IN ('r','p','v','m','f')
    AND a.attnum > 0 AND NOT a.attisdropped
  UNION
  SELECT 'identifier', 'index', n.nspname, tc.relname, ic.relname, ''
  FROM pg_catalog.pg_index i
  JOIN pg_catalog.pg_class tc ON tc.oid = i.indrelid
  JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = tc.relnamespace
  JOIN schema_state ss ON ss.schema_name = n.nspname
  WHERE ss.schema_status = 'visible'
  UNION
  SELECT 'identifier', 'constraint', n.nspname, tc.relname, co.conname, ''
  FROM pg_catalog.pg_constraint co
  JOIN pg_catalog.pg_class tc ON tc.oid = co.conrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = tc.relnamespace
  JOIN schema_state ss ON ss.schema_name = n.nspname
  WHERE ss.schema_status = 'visible'
)
SELECT row_kind, COALESCE(kind, ''), COALESCE(schema_name, ''),
  COALESCE(relation_name, ''), COALESCE(object_name, ''),
  NULLIF(schema_status, '')
FROM (
  SELECT '` + policyCatalogStatusKind + `'::text AS row_kind, ''::text AS kind, schema_name,
    ''::text AS relation_name, ''::text AS object_name, schema_status
  FROM schema_state
  UNION ALL
  SELECT row_kind, kind, schema_name, relation_name, object_name, ''
  FROM catalog
) catalog_rows
ORDER BY row_kind DESC, schema_name, kind, relation_name, object_name
LIMIT $` + fmt.Sprint(limitArg)
}

func mysqlPolicyCatalogSQL(schemaCount int) string {
	requested := mysqlRequestedSchemasSQL(schemaCount)
	plainPlaceholders := mysqlPlainSchemaPlaceholders(schemaCount)
	visible := func(start int) string {
		return `SELECT s.schema_name
  FROM information_schema.schemata s
  WHERE BINARY s.schema_name IN (` + mysqlBinarySchemaPlaceholders(start, schemaCount) + `)
    AND (
      EXISTS (SELECT 1 FROM information_schema.user_privileges p
        WHERE replace(p.grantee, '''', '') = current_user()
          AND p.privilege_type = 'SELECT')
      OR EXISTS (SELECT 1 FROM information_schema.schema_privileges p
        WHERE replace(p.grantee, '''', '') = current_user()
          AND (BINARY p.table_schema = BINARY s.schema_name OR BINARY replace(p.table_schema, '\\_', '_') = BINARY s.schema_name)
          AND p.privilege_type = 'SELECT')
    )`
	}
	// MySQL 5.7 has no CTEs. Repeat the bounded, parameterized visible-schema
	// derived table in the single UNION statement, and include direct
	// table_schema IN (...) predicates on each information_schema branch so
	// MySQL 5.7 prunes unrelated schemas instead of scanning the full instance.
	s1 := schemaCount + 1
	s2 := s1 + schemaCount
	s3 := s2 + 2*schemaCount
	s4 := s3 + 2*schemaCount
	s5 := s4 + 2*schemaCount
	_ = s5
	return `
SELECT row_kind, kind, schema_name, relation_name, object_name, NULLIF(schema_status, '')
FROM (
  SELECT '` + policyCatalogStatusKind + `' AS row_kind, '' AS kind, r.schema_name, '' AS relation_name,
    '' AS object_name,
    CASE
      WHEN s.schema_name IS NULL THEN 'missing'
      WHEN (
        EXISTS (SELECT 1 FROM information_schema.user_privileges p
          WHERE replace(p.grantee, '''', '') = current_user()
            AND p.privilege_type = 'SELECT')
        OR EXISTS (SELECT 1 FROM information_schema.schema_privileges p
          WHERE replace(p.grantee, '''', '') = current_user()
            AND (BINARY p.table_schema = BINARY r.schema_name OR BINARY replace(p.table_schema, '\\_', '_') = BINARY r.schema_name)
            AND p.privilege_type = 'SELECT')
      ) THEN 'visible'
      ELSE 'unprivileged'
    END AS schema_status
  FROM (` + requested + `) r
  LEFT JOIN information_schema.schemata s ON BINARY s.schema_name = BINARY r.schema_name
  UNION ALL
  SELECT 'identifier', 'schema', schema_name, '', schema_name, ''
  FROM (` + visible(s1) + `) visible_schema
  UNION ALL
  SELECT 'identifier',
    CASE WHEN t.table_type = 'VIEW' THEN 'view' ELSE 'table' END,
    t.table_schema, '', t.table_name, ''
  FROM information_schema.tables t
  JOIN (` + visible(s2) + `) visible_schema ON BINARY visible_schema.schema_name = BINARY t.table_schema
  WHERE t.table_schema IN (` + plainPlaceholders + `)
    AND t.table_type IN ('BASE TABLE', 'VIEW', 'SYSTEM VIEW')
  UNION ALL
  SELECT 'identifier', 'column', c.table_schema, c.table_name, c.column_name, ''
  FROM information_schema.columns c
  JOIN (` + visible(s3) + `) visible_schema ON BINARY visible_schema.schema_name = BINARY c.table_schema
  WHERE c.table_schema IN (` + plainPlaceholders + `)
  UNION ALL
  SELECT 'identifier', 'index', st.table_schema, st.table_name, st.index_name, ''
  FROM information_schema.statistics st
  JOIN (` + visible(s4) + `) visible_schema ON BINARY visible_schema.schema_name = BINARY st.table_schema
  WHERE st.table_schema IN (` + plainPlaceholders + `)
  GROUP BY st.table_schema, st.table_name, st.index_name
  UNION ALL
  SELECT 'identifier', 'constraint', tc.table_schema, tc.table_name, tc.constraint_name, ''
  FROM information_schema.table_constraints tc
  JOIN (` + visible(s5) + `) visible_schema ON BINARY visible_schema.schema_name = BINARY tc.table_schema
  WHERE tc.table_schema IN (` + plainPlaceholders + `)
) catalog_rows
ORDER BY row_kind DESC, schema_name, kind, relation_name, object_name
LIMIT ?`
}

func mysqlRequestedSchemasSQL(schemaCount int) string {
	parts := make([]string, schemaCount)
	for i := range parts {
		parts[i] = "SELECT ? AS schema_name"
	}
	return strings.Join(parts, " UNION ALL ")
}

func mysqlBinarySchemaPlaceholders(start, count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = "BINARY ?"
	}
	return strings.Join(values, ",")
}

func mysqlPlainSchemaPlaceholders(count int) string {
	values := make([]string, count)
	for i := range values {
		values[i] = "?"
	}
	return strings.Join(values, ",")
}
