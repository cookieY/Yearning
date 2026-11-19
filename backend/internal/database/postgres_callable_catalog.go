package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// maxPostgresCallableLookups bounds one callable identity batch. It matches the
// relation bound so a single statement set cannot inflate the catalog read.
const maxPostgresCallableLookups = 50000

// ErrPostgresCallableUnresolved reports that callable identity could not be
// proven from the catalog. Callers must treat it as a fail-closed condition.
var ErrPostgresCallableUnresolved = errors.New("database: postgres callable unresolved")

// PostgresCallableKind is the catalog family of an unqualified callable
// reference: a routine name or an operator spelling.
type PostgresCallableKind string

const (
	// PostgresCallableFunction is an unqualified routine call such as lower(x).
	PostgresCallableFunction PostgresCallableKind = "function"
	// PostgresCallableOperator is an operator spelling such as @ or ||.
	PostgresCallableOperator PostgresCallableKind = "operator"
)

// PostgresCallableRequest asks for the catalog identity of one unqualified
// callable name on the frozen effective path.
type PostgresCallableRequest struct {
	Kind PostgresCallableKind
	Name string
}

// PostgresCallableWinner is the callable a signature actually resolves to on
// the frozen effective path. Identity records the owning schema, the resolved
// signature (routine input OID list or operator left,right type pair), the
// result type, the catalog OID and whether that schema is pg_catalog.
type PostgresCallableWinner struct {
	Schema     string
	Signature  string
	ResultType string
	OID        uint32
	PgCatalog  bool
}

// PostgresCallableResolution is the catalog fact for one requested name. A
// signature is the routine's input OID list or the operator's left,right type
// pair; exactly one visible winner exists per signature (PostgreSQL resolves a
// name plus argument types to a single routine in path order).
type PostgresCallableResolution struct {
	Request PostgresCallableRequest
	Winners []PostgresCallableWinner
}

// Unsafe reports whether any visible signature resolves outside pg_catalog.
func (r PostgresCallableResolution) Unsafe() bool {
	for _, winner := range r.Winners {
		if !winner.PgCatalog {
			return true
		}
	}
	return false
}

// Comparable reports whether the resolution is usable evidence: at least one
// signature is visible, so silence from the catalog is never read as safety.
func (r PostgresCallableResolution) Comparable() bool {
	return len(r.Winners) != 0
}

type postgresCallableBatchInput struct {
	Functions []string `json:"functions"`
	Operators []string `json:"operators"`
	Schemas   []string `json:"schemas"`
	Role      string   `json:"role"`
}

type postgresCallableBatchWinner struct {
	Schema     string `json:"schema"`
	Signature  string `json:"signature"`
	OID        uint32 `json:"oid"`
	ResultType string `json:"result_type"`
	PgCatalog  bool   `json:"pg_catalog"`
}

type postgresCallableBatchRow struct {
	Kind    string                        `json:"kind"`
	Name    string                        `json:"name"`
	Winners []postgresCallableBatchWinner `json:"winners"`
}

// postgresCallableBatchLookupSQL resolves every requested callable name on the
// frozen effective path. It runs with search_path = pg_catalog, takes the
// frozen path as data, and returns only winner rows so the operator's own
// lookup cannot be intercepted by a user-defined name.
const postgresCallableBatchLookupSQL = `
	WITH requested AS (
		SELECT pg_catalog.jsonb_extract_path(
		           $1::pg_catalog.jsonb, 'functions') AS functions,
		       pg_catalog.jsonb_extract_path(
		           $1::pg_catalog.jsonb, 'operators') AS operators,
		       pg_catalog.jsonb_extract_path(
		           $1::pg_catalog.jsonb, 'schemas') AS schemas,
		       pg_catalog.jsonb_extract_path_text(
		           $1::pg_catalog.jsonb, 'role') AS role
	),
	function_names AS (
		SELECT DISTINCT value AS name
		  FROM requested,
		       LATERAL pg_catalog.jsonb_array_elements_text(functions) AS f(value)
	),
	operator_names AS (
		SELECT DISTINCT value AS name
		  FROM requested,
		       LATERAL pg_catalog.jsonb_array_elements_text(operators) AS o(value)
	),
	schema_names AS (
		SELECT value AS name, ordinality::pg_catalog.int4 AS ord
		  FROM requested,
		       LATERAL pg_catalog.jsonb_array_elements_text(schemas)
		       WITH ORDINALITY AS s(value, ordinality)
	),
	functions AS (
		SELECT 'function'::pg_catalog.text AS kind,
		       fn.name::pg_catalog.text AS name,
		       n.nspname::pg_catalog.text AS schema,
		       (CASE WHEN p.proargtypes::pg_catalog.text = ''
		             THEN '()'
		             ELSE p.proargtypes::pg_catalog.text
		        END)::pg_catalog.text AS signature,
		       p.prorettype::pg_catalog.regtype::pg_catalog.text AS result_type,
		       p.oid::pg_catalog.int8 AS oid,
		       CASE WHEN s.name IS NOT NULL THEN s.ord ELSE 0 END AS position
		  FROM function_names fn
		  CROSS JOIN requested r
		  JOIN pg_catalog.pg_proc p ON p.proname = fn.name
		  JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		  LEFT JOIN schema_names s ON s.name = n.nspname
		 WHERE (n.nspname = 'pg_catalog' OR s.name IS NOT NULL)
		   AND (COALESCE(r.role, '') = ''
		        OR pg_catalog.has_schema_privilege(
		             r.role::pg_catalog.name, n.oid, 'USAGE'))
	),
	operators AS (
		SELECT 'operator'::pg_catalog.text AS kind,
		       onm.name::pg_catalog.text AS name,
		       n.nspname::pg_catalog.text AS schema,
		       (CASE WHEN o.oprleft = 0 THEN '-'
		             ELSE o.oprleft::pg_catalog.regtype::pg_catalog.text END
		        || ',' ||
		        CASE WHEN o.oprright = 0 THEN '-'
		             ELSE o.oprright::pg_catalog.regtype::pg_catalog.text END
		       )::pg_catalog.text AS signature,
		       o.oprresult::pg_catalog.regtype::pg_catalog.text AS result_type,
		       o.oid::pg_catalog.int8 AS oid,
		       CASE WHEN s.name IS NOT NULL THEN s.ord ELSE 0 END AS position
		  FROM operator_names onm
		  CROSS JOIN requested r
		  JOIN pg_catalog.pg_operator o ON o.oprname = onm.name
		  JOIN pg_catalog.pg_namespace n ON n.oid = o.oprnamespace
		  LEFT JOIN schema_names s ON s.name = n.nspname
		 WHERE (n.nspname = 'pg_catalog' OR s.name IS NOT NULL)
		   AND (COALESCE(r.role, '') = ''
		        OR pg_catalog.has_schema_privilege(
		             r.role::pg_catalog.name, n.oid, 'USAGE'))
	),
	all_callables AS (
		SELECT * FROM functions UNION ALL SELECT * FROM operators
	),
	ranked AS (
		SELECT c.*,
		       pg_catalog.row_number() OVER (
		           PARTITION BY c.kind, c.name, c.signature
		           ORDER BY c.position, c.oid
		       )::pg_catalog.int4 AS rank
		  FROM all_callables c
	),
	summary AS (
		SELECT kind, name,
		       pg_catalog.jsonb_agg(
		           pg_catalog.jsonb_build_object(
		               'schema', schema, 'signature', signature,
		               'oid', oid, 'result_type', result_type,
		               'pg_catalog', schema = 'pg_catalog'
		           ) ORDER BY signature, oid
		       ) AS winners
		  FROM ranked
		 WHERE rank = 1
		 GROUP BY kind, name
	)
	SELECT COALESCE(
	           pg_catalog.jsonb_agg(
	               pg_catalog.jsonb_build_object(
	                   'kind', kind, 'name', name,
	                   'winners', winners
	               ) ORDER BY kind, name
	           ),
	           '[]'::pg_catalog.jsonb
	       )::pg_catalog.text
	  FROM summary`

// ResolvePostgresCallables resolves callable identity for the requested names
// on one review-purpose connection whose own search path is pg_catalog. The
// frozen effective path is passed as data, never installed on the session.
func ResolvePostgresCallables(
	ctx context.Context,
	pool *PurposePool,
	path PostgresSearchPath,
	requests []PostgresCallableRequest,
) ([]PostgresCallableResolution, error) {
	if pool == nil || pool.engine != "postgresql" ||
		(pool.purpose != PurposeReview && pool.purpose != PurposeExecution) {
		return nil, ErrPostgresSearchPathUnavailable
	}
	if err := path.Validate(); err != nil {
		return nil, ErrPostgresSearchPathUnavailable
	}
	normalized, err := NormalizePostgresCallableRequests(requests)
	if err != nil {
		return nil, err
	}
	payload, err := postgresCallablePayload(path, normalized)
	if err != nil {
		return nil, err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, postgresRelationResolutionTimeout)
	defer cancel()
	var resolutions []PostgresCallableResolution
	err = pool.withReadQueryer(lookupCtx, true, func(q queryer) error {
		tx, ok := q.(*sql.Tx)
		if !ok {
			return ErrPostgresSearchPathUnavailable
		}
		if _, execErr := tx.ExecContext(lookupCtx, postgresCatalogSearchPath); execErr != nil {
			return ErrPostgresSearchPathUnavailable
		}
		resolutions, err = resolvePostgresCallablesOnQueryer(
			lookupCtx, tx, path, normalized, payload,
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resolutions, nil
}

// NormalizePostgresCallableRequests deduplicates and orders requests so the
// frozen binding and the send-time re-resolution ask exactly the same
// questions in the same order.
func NormalizePostgresCallableRequests(
	requests []PostgresCallableRequest,
) ([]PostgresCallableRequest, error) {
	if len(requests) > maxPostgresCallableLookups {
		return nil, ErrPostgresCallableUnresolved
	}
	unique := make([]PostgresCallableRequest, 0, len(requests))
	seen := make(map[string]bool, len(requests))
	for _, request := range requests {
		if request.Kind != PostgresCallableFunction &&
			request.Kind != PostgresCallableOperator {
			return nil, ErrPostgresCallableUnresolved
		}
		if err := ValidatePostgresCallableName(request.Kind, request.Name); err != nil {
			return nil, ErrPostgresCallableUnresolved
		}
		key := string(request.Kind) + ":" + request.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, request)
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Kind != unique[j].Kind {
			return unique[i].Kind < unique[j].Kind
		}
		return unique[i].Name < unique[j].Name
	})
	return unique, nil
}

// postgresOperatorChars is the operator spelling character set. PostgreSQL
// operator names are built from exactly these characters, and a single one of
// them (for example @) is a valid custom operator.
const postgresOperatorChars = "+-*/<>=~!@#%^&|`?"

// ValidatePostgresCallableName validates a callable name for its kind. Routine
// names are identifiers; operator names are spellings such as @, || or ->>.
func ValidatePostgresCallableName(kind PostgresCallableKind, name string) error {
	switch kind {
	case PostgresCallableFunction:
		return ValidateIdentifier(name)
	case PostgresCallableOperator:
		if name == "" || len(name) > 63 {
			return ErrPostgresCallableUnresolved
		}
		for _, r := range name {
			if !strings.ContainsRune(postgresOperatorChars, r) {
				return ErrPostgresCallableUnresolved
			}
		}
		return nil
	default:
		return ErrPostgresCallableUnresolved
	}
}

// PostgresCallablePayloadKey renders the stable binding key of one request.
func PostgresCallablePayloadKey(kind PostgresCallableKind, name string) string {
	return string(kind) + ":" + name
}

func postgresCallablePayload(
	path PostgresSearchPath,
	requests []PostgresCallableRequest,
) (string, error) {
	input := postgresCallableBatchInput{
		Schemas:   path.Effective,
		Role:      path.CurrentUser,
		Functions: []string{},
		Operators: []string{},
	}
	for _, request := range requests {
		switch request.Kind {
		case PostgresCallableFunction:
			input.Functions = append(input.Functions, request.Name)
		case PostgresCallableOperator:
			input.Operators = append(input.Operators, request.Name)
		default:
			return "", ErrPostgresCallableUnresolved
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", ErrPostgresCallableUnresolved
	}
	return string(raw), nil
}

func resolvePostgresCallablesOnQueryer(
	ctx context.Context,
	q queryer,
	path PostgresSearchPath,
	requests []PostgresCallableRequest,
	payload string,
) ([]PostgresCallableResolution, error) {
	if q == nil || len(requests) == 0 {
		return nil, ErrPostgresCallableUnresolved
	}
	rows, err := q.QueryContext(ctx, postgresCallableBatchLookupSQL, payload)
	if err != nil {
		return nil, ErrPostgresSearchPathUnavailable
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, ErrPostgresSearchPathUnavailable
		}
		return nil, ErrPostgresCallableUnresolved
	}
	var raw string
	if err := rows.Scan(&raw); err != nil {
		return nil, ErrPostgresCallableUnresolved
	}
	if rows.Next() {
		return nil, ErrPostgresCallableUnresolved
	}
	if err := rows.Err(); err != nil {
		return nil, ErrPostgresSearchPathUnavailable
	}
	var values []postgresCallableBatchRow
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, ErrPostgresCallableUnresolved
	}
	return buildPostgresCallableResolutions(path, requests, values)
}

func buildPostgresCallableResolutions(
	path PostgresSearchPath,
	requests []PostgresCallableRequest,
	values []postgresCallableBatchRow,
) ([]PostgresCallableResolution, error) {
	byKey := make(map[string]postgresCallableBatchRow, len(values))
	for _, value := range values {
		kind := PostgresCallableKind(value.Kind)
		if kind != PostgresCallableFunction && kind != PostgresCallableOperator {
			return nil, ErrPostgresCallableUnresolved
		}
		if ValidatePostgresCallableName(kind, value.Name) != nil {
			return nil, ErrPostgresCallableUnresolved
		}
		key := PostgresCallablePayloadKey(kind, value.Name)
		if _, duplicate := byKey[key]; duplicate {
			return nil, ErrPostgresCallableUnresolved
		}
		byKey[key] = value
	}
	out := make([]PostgresCallableResolution, 0, len(requests))
	for _, request := range requests {
		key := PostgresCallablePayloadKey(request.Kind, request.Name)
		value, found := byKey[key]
		resolution := PostgresCallableResolution{Request: request}
		if found {
			delete(byKey, key)
			seen := make(map[string]bool, len(value.Winners))
			for _, winner := range value.Winners {
				if ValidateIdentifier(winner.Schema) != nil ||
					winner.Signature == "" || winner.ResultType == "" || winner.OID == 0 ||
					winner.PgCatalog != (winner.Schema == "pg_catalog") ||
					!containsString(path.Effective, winner.Schema) {
					return nil, ErrPostgresCallableUnresolved
				}
				if seen[winner.Signature] {
					return nil, ErrPostgresCallableUnresolved
				}
				seen[winner.Signature] = true
				resolution.Winners = append(resolution.Winners, PostgresCallableWinner{
					Schema:     winner.Schema,
					Signature:  winner.Signature,
					ResultType: winner.ResultType,
					OID:        winner.OID,
					PgCatalog:  winner.PgCatalog,
				})
			}
			sort.Slice(resolution.Winners, func(i, j int) bool {
				if resolution.Winners[i].Signature != resolution.Winners[j].Signature {
					return resolution.Winners[i].Signature < resolution.Winners[j].Signature
				}
				return resolution.Winners[i].OID < resolution.Winners[j].OID
			})
			// Zero winners means no schema on the frozen path defines this
			// name at all. The request is still frozen with empty evidence so
			// send time can detect a definition appearing later.

		}
		out = append(out, resolution)
	}
	if len(byKey) != 0 {
		return nil, ErrPostgresCallableUnresolved
	}
	return out, nil
}
