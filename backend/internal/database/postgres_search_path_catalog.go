package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	maxPostgresRelationLookups       = 50000
	postgresSearchPathExplainTimeout = 10 * time.Second
	postgresCatalogSearchPath        = "SET LOCAL search_path = pg_catalog"
)

const postgresRelationBatchLookupSQL = `
	WITH requested AS (
		SELECT pg_catalog.jsonb_extract_path(
		           $1::pg_catalog.jsonb, 'relations') AS relations,
		       pg_catalog.jsonb_extract_path(
		           $1::pg_catalog.jsonb, 'schemas') AS schemas,
		       pg_catalog.jsonb_extract_path_text(
		           $1::pg_catalog.jsonb, 'role') AS role
	),
	relation_names AS (
		SELECT value AS name, ordinality::pg_catalog.int4 AS ord
		  FROM requested,
		       LATERAL pg_catalog.jsonb_array_elements_text(relations)
		       WITH ORDINALITY AS r(value, ordinality)
	),
	schema_names AS (
		SELECT value AS name, ordinality::pg_catalog.int4 AS ord
		  FROM requested,
		       LATERAL pg_catalog.jsonb_array_elements_text(schemas)
		       WITH ORDINALITY AS s(value, ordinality)
	),
	resolved AS (
		SELECT r.name AS relation,
		       r.ord AS relation_ord,
		       s.name AS schema,
		       n.oid IS NOT NULL AS schema_exists,
		       CASE WHEN n.oid IS NULL THEN false
		            ELSE COALESCE(pg_catalog.has_schema_privilege(
		                 requested.role::pg_catalog.name, n.oid, 'USAGE'), false)
		       END AS usage,
		       c.relkind::pg_catalog.text AS relkind,
		       c.oid::pg_catalog.int8 AS relation_oid
		  FROM relation_names r
		  CROSS JOIN schema_names s
		  CROSS JOIN requested
		  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = s.name
		  LEFT JOIN pg_catalog.pg_class c
		         ON c.relnamespace = n.oid AND c.relname = r.name
	)
	SELECT relation,
	       pg_catalog.bool_and(schema_exists) AS all_schemas_exist,
	       pg_catalog.bool_and(usage) AS all_schemas_visible,
	       COALESCE(
	           pg_catalog.jsonb_object_agg(
	               schema,
	               pg_catalog.jsonb_build_object(
	                   'kind', relkind, 'oid', relation_oid
	               )
	           )
	           FILTER (WHERE relkind IS NOT NULL),
	           '{}'::pg_catalog.jsonb
	       ) AS present_by_schema
	  FROM resolved
	 GROUP BY relation, relation_ord
	 ORDER BY relation_ord`

const postgresSchemasVisibleSQL = `
	WITH requested AS (
		SELECT pg_catalog.jsonb_extract_path(
		           $1::pg_catalog.jsonb, 'schemas') AS schemas,
		       pg_catalog.jsonb_extract_path_text(
		           $1::pg_catalog.jsonb, 'role') AS role
	),
	schema_names AS (
		SELECT value AS name
		  FROM requested,
		       LATERAL pg_catalog.jsonb_array_elements_text(schemas) AS s(value)
	),
	resolved AS (
		SELECT n.oid IS NOT NULL AS schema_exists,
		       CASE WHEN n.oid IS NULL THEN false
		            ELSE COALESCE(pg_catalog.has_schema_privilege(
		                 requested.role::pg_catalog.name, n.oid, 'USAGE'), false)
		       END AS usage
		  FROM schema_names s
		  CROSS JOIN requested
		  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = s.name
	)
	SELECT COALESCE(pg_catalog.bool_and(schema_exists), false),
	       COALESCE(pg_catalog.bool_and(usage), false)
	  FROM resolved`

// IsDisallowedPostgresSystemSchema reports whether schema is empty or names a built-in
// PostgreSQL catalog, toast, or temporary schema disallowed for explicit overrides
// and SQL-derived scope expansion.
func IsDisallowedPostgresSystemSchema(schema string) bool {
	trimmed := strings.TrimSpace(schema)
	low := strings.ToLower(trimmed)
	return trimmed == "" ||
		postgresTemporarySchema(trimmed) ||
		postgresTemporarySchema(low) ||
		low == "pg_catalog" ||
		low == "information_schema" ||
		low == "pg_toast" ||
		strings.HasPrefix(low, "pg_toast_")
}

func mergePostgresLookupSchemas(path PostgresSearchPath, extraSchemas []string) ([]string, error) {
	out := make([]string, 0, len(path.Effective)+len(extraSchemas))
	seen := make(map[string]bool, len(path.Effective)+len(extraSchemas))
	for _, schema := range path.Effective {
		if !seen[schema] {
			seen[schema] = true
			out = append(out, schema)
		}
	}
	for _, schema := range extraSchemas {
		if ValidateIdentifier(schema) != nil || IsDisallowedPostgresSystemSchema(schema) {
			return nil, ErrPostgresRelationUnresolved
		}
		if seen[schema] {
			continue
		}
		seen[schema] = true
		out = append(out, schema)
	}
	if len(out) > maxPostgresSearchPathSchemas+3 {
		return nil, ErrPostgresRelationUnresolved
	}
	return out, nil
}

// PostgresRelationLookup is a complete lookup for one exact relation name
// across the frozen effective path. An absent map member proves that the name
// is absent in that schema; any unreadable schema aborts the whole lookup.
type PostgresRelationLookup struct {
	Relation        string
	PresentBySchema map[string]PostgresRelationKind
	// OIDBySchema binds a present relation to its catalog identity. It detects
	// a drop/recreate that preserves schema and name but changes the target.
	OIDBySchema map[string]uint32
}

type postgresBatchLookupInput struct {
	Relations []string `json:"relations"`
	Schemas   []string `json:"schemas"`
	Role      string   `json:"role"`
}

type searchPathEngineAdapter interface {
	engine() string
}

// ExplainWithPostgresSearchPath is the trusted Adapter capability used by the
// review fact source. The canonical Adapter interface remains unchanged.
func (a *baseAdapter) ExplainWithPostgresSearchPath(
	ctx context.Context,
	pool *PurposePool,
	path PostgresSearchPath,
	statementSQL string,
) (*ExplainFact, error) {
	if err := path.Validate(); err != nil {
		return nil, ErrPostgresSearchPathUnavailable
	}
	return ExplainPostgresWithSearchPath(
		ctx, a, pool, path.Explicit, statementSQL,
	)
}

func validatePostgresCatalogAccess(
	pool *PurposePool,
	path PostgresSearchPath,
	extraSchemas []string,
) ([]string, error) {
	if pool == nil || pool.engine != "postgresql" ||
		(pool.purpose != PurposeReview && pool.purpose != PurposeExecution) {
		return nil, ErrPostgresSearchPathUnavailable
	}
	if err := path.Validate(); err != nil {
		return nil, ErrPostgresSearchPathUnavailable
	}
	return mergePostgresLookupSchemas(path, extraSchemas)
}

func withPostgresCatalogTx(
	ctx context.Context,
	pool *PurposePool,
	fn func(ctx context.Context, tx *sql.Tx) error,
) error {
	lookupCtx, cancel := context.WithTimeout(ctx, postgresRelationResolutionTimeout)
	defer cancel()
	return pool.withReadQueryer(lookupCtx, true, func(q queryer) error {
		tx, ok := q.(*sql.Tx)
		if !ok {
			return ErrPostgresSearchPathUnavailable
		}
		if _, err := tx.ExecContext(lookupCtx, postgresCatalogSearchPath); err != nil {
			return ErrPostgresSearchPathUnavailable
		}
		return fn(lookupCtx, tx)
	})
}

func preparePostgresRelationBatchLookup(
	relations []string,
	allowedSchemas []string,
	role string,
) (
	uniqueRelations []string,
	requestJSON string,
	lookups []PostgresRelationLookup,
	lookupByName map[string]*PostgresRelationLookup,
	err error,
) {
	uniqueRelations = make([]string, 0, len(relations))
	seenRequested := make(map[string]bool, len(relations))
	for _, relation := range relations {
		if err := ValidateIdentifier(relation); err != nil {
			return nil, "", nil, nil, ErrPostgresRelationUnresolved
		}
		if seenRequested[relation] {
			continue
		}
		seenRequested[relation] = true
		uniqueRelations = append(uniqueRelations, relation)
	}
	rawJSON, err := json.Marshal(postgresBatchLookupInput{
		Relations: uniqueRelations,
		Schemas:   allowedSchemas,
		Role:      role,
	})
	if err != nil {
		return nil, "", nil, nil, ErrPostgresRelationUnresolved
	}
	lookups = make([]PostgresRelationLookup, len(uniqueRelations))
	lookupByName = make(map[string]*PostgresRelationLookup, len(uniqueRelations))
	for i, relation := range uniqueRelations {
		lookups[i] = PostgresRelationLookup{
			Relation: relation, PresentBySchema: map[string]PostgresRelationKind{},
		}
		lookupByName[relation] = &lookups[i]
	}
	return uniqueRelations, string(rawJSON), lookups, lookupByName, nil
}

// LookupPostgresRelations performs one fixed catalog query for all requested
// names across the frozen effective path and any explicitly qualified non-system
// schemas. It runs on the review-purpose connection with pg_catalog as its own
// search path, so target-role functions, operators and types cannot intercept the catalog read.
func LookupPostgresRelations(
	ctx context.Context,
	pool *PurposePool,
	path PostgresSearchPath,
	relations []string,
	extraSchemas ...string,
) ([]PostgresRelationLookup, error) {
	allowedSchemas, err := validatePostgresCatalogAccess(pool, path, extraSchemas)
	if err != nil {
		return nil, err
	}
	if len(relations) == 0 || len(relations) > maxPostgresRelationLookups {
		return nil, ErrPostgresRelationUnresolved
	}
	uniqueRelations, requestJSON, lookups, lookupByName, err := preparePostgresRelationBatchLookup(
		relations, allowedSchemas, path.CurrentUser,
	)
	if err != nil {
		return nil, err
	}
	seenRows := make(map[string]bool, len(uniqueRelations))
	if err := withPostgresCatalogTx(ctx, pool, func(lookupCtx context.Context, tx *sql.Tx) error {
		return scanPostgresRelationLookups(
			lookupCtx, tx, allowedSchemas, requestJSON,
			lookups, lookupByName, seenRows, len(uniqueRelations),
		)
	}); err != nil {
		return nil, err
	}
	return lookups, nil
}

// VerifyPostgresSchemasVisible verifies that every schema in path.Effective and
// any extraSchemas exists and grants USAGE to path.CurrentUser.
func VerifyPostgresSchemasVisible(
	ctx context.Context,
	pool *PurposePool,
	path PostgresSearchPath,
	extraSchemas ...string,
) error {
	allowedSchemas, err := validatePostgresCatalogAccess(pool, path, extraSchemas)
	if err != nil {
		return err
	}
	return withPostgresCatalogTx(ctx, pool, func(lookupCtx context.Context, tx *sql.Tx) error {
		return verifyPostgresSchemasVisibleOnQueryer(lookupCtx, tx, path.CurrentUser, allowedSchemas)
	})
}

func verifyPostgresSchemasVisibleOnQueryer(
	ctx context.Context,
	q queryer,
	role string,
	schemas []string,
) error {
	requestJSON, err := json.Marshal(postgresBatchLookupInput{
		Schemas: schemas,
		Role:    role,
	})
	if err != nil {
		return ErrPostgresRelationUnresolved
	}
	var schemasExist, schemasVisible bool
	if err := q.QueryRowContext(ctx, postgresSchemasVisibleSQL, string(requestJSON)).Scan(
		&schemasExist, &schemasVisible,
	); err != nil {
		return ErrPostgresSearchPathUnavailable
	}
	if !schemasExist {
		return fmt.Errorf("%w: %w", ErrPostgresSearchPathUnavailable, ErrPolicyCatalogSchemaUnavailable)
	}
	if !schemasVisible {
		return fmt.Errorf("%w: %w", ErrPostgresSearchPathUnavailable, ErrMetadataVisibilityUnproven)
	}
	return nil
}

func scanPostgresRelationLookups(
	ctx context.Context,
	q queryer,
	allowedSchemas []string,
	requestJSON string,
	lookups []PostgresRelationLookup,
	lookupByName map[string]*PostgresRelationLookup,
	seenRows map[string]bool,
	expectedRows int,
) error {
	rows, err := q.QueryContext(ctx, postgresRelationBatchLookupSQL, requestJSON)
	if err != nil {
		return ErrPostgresSearchPathUnavailable
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			relation        string
			schemasExist    bool
			schemasVisible  bool
			presentBySchema []byte
		)
		if err := rows.Scan(
			&relation, &schemasExist, &schemasVisible, &presentBySchema,
		); err != nil {
			return ErrPostgresSearchPathUnavailable
		}
		lookup := lookupByName[relation]
		if lookup == nil || seenRows[relation] {
			return ErrPostgresRelationUnresolved
		}
		seenRows[relation] = true
		if !schemasExist {
			return fmt.Errorf("%w: %w", ErrPostgresSearchPathUnavailable, ErrPolicyCatalogSchemaUnavailable)
		}
		if !schemasVisible {
			return fmt.Errorf("%w: %w", ErrPostgresSearchPathUnavailable, ErrMetadataVisibilityUnproven)
		}
		rawEntries := map[string]json.RawMessage{}
		if err := json.Unmarshal(presentBySchema, &rawEntries); err != nil {
			return ErrPostgresRelationUnresolved
		}
		for schema, raw := range rawEntries {
			var identity struct {
				Kind string `json:"kind"`
				OID  uint32 `json:"oid"`
			}
			if err := json.Unmarshal(raw, &identity); err != nil ||
				identity.Kind == "" || identity.OID == 0 ||
				!slices.Contains(allowedSchemas, schema) {
				return ErrPostgresRelationUnresolved
			}
			lookup.PresentBySchema[schema] = postgresRelationKind(identity.Kind)
			if lookup.OIDBySchema == nil {
				lookup.OIDBySchema = map[string]uint32{}
			}
			lookup.OIDBySchema[schema] = identity.OID
		}
	}
	if err := rows.Err(); err != nil {
		return ErrPostgresSearchPathUnavailable
	}
	if len(seenRows) != expectedRows {
		return ErrPostgresRelationUnresolved
	}
	return nil
}

// InspectPostgresDMLTransaction validates the exact transaction that is about
// to execute a reviewed statement. The read is performed on that transaction,
// so uncommitted and temporary objects created by earlier reviewed statements
// participate in relation resolution.
func InspectPostgresDMLTransaction(
	ctx context.Context,
	pool *PurposePool,
	tx DMLTransaction,
	path PostgresSearchPath,
	relations []string,
	callables []PostgresCallableRequest,
	extraSchemas ...string,
) (PostgresSearchPath, []PostgresRelationLookup, []PostgresCallableResolution, error) {
	concrete, ok := tx.(*dmlTx)
	if !ok || !concrete.open || pool == nil || concrete.pool != pool ||
		pool.engine != "postgresql" {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	return inspectPostgresQueryer(ctx, concrete.tx, path, relations, callables, extraSchemas...)
}

// InspectPostgresDDLConnection performs the same inspection on the dedicated
// connection used for an ordered DDL statement. A short local transaction
// isolates SET LOCAL and leaves the connection's frozen session path intact
// for the statement that follows.
func InspectPostgresDDLConnection(
	ctx context.Context,
	conn *sql.Conn,
	path PostgresSearchPath,
	relations []string,
	callables []PostgresCallableRequest,
	extraSchemas ...string,
) (PostgresSearchPath, []PostgresRelationLookup, []PostgresCallableResolution, error) {
	if conn == nil {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	snapshot, lookups, resolutions, inspectErr := inspectPostgresQueryer(
		ctx, tx, path, relations, callables, extraSchemas...,
	)
	if err := tx.Rollback(); err != nil && inspectErr == nil {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	return snapshot, lookups, resolutions, inspectErr
}

func inspectPostgresQueryer(
	ctx context.Context,
	q queryer,
	path PostgresSearchPath,
	relations []string,
	callables []PostgresCallableRequest,
	extraSchemas ...string,
) (PostgresSearchPath, []PostgresRelationLookup, []PostgresCallableResolution, error) {
	if q == nil || path.Validate() != nil {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	allowedSchemas, err := mergePostgresLookupSchemas(path, extraSchemas)
	if err != nil {
		return PostgresSearchPath{}, nil, nil, err
	}
	snapshot, err := probePostgresQueryerSearchPath(ctx, q)
	if err != nil || !path.Equal(snapshot) {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	if len(relations) == 0 && len(callables) == 0 && len(extraSchemas) == 0 {
		return snapshot, []PostgresRelationLookup{}, nil, nil
	}
	normalizedCallables, err := NormalizePostgresCallableRequests(callables)
	if err != nil {
		return PostgresSearchPath{}, nil, nil, err
	}
	if len(relations) > maxPostgresRelationLookups {
		return PostgresSearchPath{}, nil, nil, ErrPostgresRelationUnresolved
	}
	unique, requestJSON, lookups, lookupByName, err := preparePostgresRelationBatchLookup(
		relations, allowedSchemas, path.CurrentUser,
	)
	if err != nil {
		return PostgresSearchPath{}, nil, nil, err
	}
	executor, ok := q.(execer)
	if !ok {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	if _, err := executor.ExecContext(ctx, postgresCatalogSearchPath); err != nil {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	if len(unique) != 0 {
		if err := scanPostgresRelationLookups(
			ctx, q, allowedSchemas, requestJSON, lookups, lookupByName,
			map[string]bool{}, len(unique),
		); err != nil {
			return PostgresSearchPath{}, nil, nil, err
		}
	} else if len(extraSchemas) != 0 {
		if err := verifyPostgresSchemasVisibleOnQueryer(ctx, q, path.CurrentUser, allowedSchemas); err != nil {
			return PostgresSearchPath{}, nil, nil, err
		}
	}
	var resolutions []PostgresCallableResolution
	if len(normalizedCallables) != 0 {
		payload, err := postgresCallablePayload(path, normalizedCallables)
		if err != nil {
			return PostgresSearchPath{}, nil, nil, err
		}
		resolutions, err = resolvePostgresCallablesOnQueryer(
			ctx, q, path, normalizedCallables, payload,
		)
		if err != nil {
			return PostgresSearchPath{}, nil, nil, err
		}
	}
	restoreSQL, err := postgresSearchPathSQL(path.Explicit, true)
	if err != nil {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	if _, err := executor.ExecContext(ctx, restoreSQL); err != nil {
		return PostgresSearchPath{}, nil, nil, ErrPostgresSearchPathUnavailable
	}
	return snapshot, lookups, resolutions, nil
}

// ExplainPostgresWithSearchPath applies a frozen expanded explicit path on a
// clean review connection and executes only a fixed non-ANALYZE EXPLAIN. The
// effective lookup path is never installed because pg_catalog's implicit
// position belongs to lookup semantics rather than connection replay.
func ExplainPostgresWithSearchPath(
	ctx context.Context,
	adapter Adapter,
	pool *PurposePool,
	searchPath []string,
	statementSQL string,
) (*ExplainFact, error) {
	engineAdapter, ok := adapter.(searchPathEngineAdapter)
	if !ok || engineAdapter.engine() != "postgresql" ||
		pool == nil || pool.engine != "postgresql" || pool.purpose != PurposeReview {
		return nil, ErrUnsupportedTarget
	}
	pathSQL, err := postgresSearchPathSQL(searchPath, true)
	if err != nil {
		return nil, err
	}
	candidateSQL, err := nonExecutingExplainSQL("postgresql", statementSQL)
	if err != nil {
		return nil, err
	}
	explainCtx, cancel := context.WithTimeout(ctx, postgresSearchPathExplainTimeout)
	defer cancel()
	var fact *ExplainFact
	err = pool.withReadQueryer(explainCtx, false, func(q queryer) error {
		tx, ok := q.(*sql.Tx)
		if !ok {
			return ErrUnsupportedTarget
		}
		if _, err := tx.ExecContext(explainCtx, pathSQL); err != nil {
			return errors.New("database: schema context apply failed")
		}
		rows, err := tx.QueryContext(explainCtx, candidateSQL)
		if err != nil {
			return errors.New("database: explain rejected")
		}
		defer func() { _ = rows.Close() }()
		columns, rawRows, err := collectStringRows(rows)
		if err != nil {
			return errors.New("database: explain output unreadable")
		}
		fact, err = parsePostgresExplain(columns, rawRows)
		return err
	})
	if err != nil {
		return nil, err
	}
	return fact, nil
}
