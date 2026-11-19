// postgres.go PostgreSQL dialect: fixed pg_catalog SQL (parameterized), instance identity
// (with availability fallback for system_identifier), and EXPLAIN (FORMAT JSON) parsing.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
)

type postgresAdapter struct {
	baseAdapter
}

const pgListSchemasSQL = `SELECT nspname FROM pg_namespace
 WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
 ORDER BY nspname`

const pgListTablesSQL = `SELECT c.relname FROM pg_class c
 JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
 ORDER BY c.relname`

const pgListColumnsSQL = `SELECT a.attname, a.attnum,
 format_type(a.atttypid, a.atttypmod) || repeat('[]', GREATEST(a.attndims - 1, 0)),
 NOT a.attnotnull,
 '', COALESCE(pg_get_expr(ad.adbin, ad.adrelid), ''),
 CASE WHEN a.attidentity <> '' THEN 'auto_increment' WHEN a.attgenerated <> '' THEN 'generated' ELSE '' END,
 COALESCE(col_description(a.attrelid, a.attnum), '')
 FROM pg_attribute a
 LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
 WHERE a.attrelid = (
     SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = $1 AND c.relname = $2
 ) AND a.attnum > 0 AND NOT a.attisdropped
 ORDER BY a.attnum`

const pgListIndexesSQL = `SELECT ic.relname, NOT i.indisunique, i.indisprimary,
 COALESCE(pa.attname, ''), k.ord::int,
 CASE WHEN k.ord <= i.indnkeyatts THEN 'key' ELSE 'include' END,
 k.attnum = 0, NULL::int
 FROM pg_index i
 JOIN pg_class tc ON tc.oid = i.indrelid
 JOIN pg_namespace n ON n.oid = tc.relnamespace
 JOIN pg_class ic ON ic.oid = i.indexrelid
 CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
 LEFT JOIN pg_attribute pa ON pa.attrelid = tc.oid AND pa.attnum = k.attnum
 WHERE n.nspname = $1 AND tc.relname = $2
 ORDER BY ic.relname, k.ord`

const pgTableExistenceFactsSQL = `SELECT
 (SELECT COUNT(*) FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p')) AS table_count,
 (SELECT COUNT(*) FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind NOT IN ('r', 'p')) AS unsupported_relation_count,
 (SELECT COUNT(*) FROM pg_catalog.pg_namespace n
  WHERE n.nspname = $1) AS schema_count,
 CASE WHEN EXISTS (
  SELECT 1 FROM pg_catalog.pg_namespace n
  WHERE n.nspname = $1
    AND pg_catalog.has_schema_privilege(n.oid, 'USAGE')
 ) THEN 1 ELSE 0 END AS schema_usage`

const pgTableRowEstimateSQL = `SELECT CASE
     WHEN c.reltuples < 0 THEN NULL
     ELSE round(c.reltuples)::bigint
   END
 FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p')`

const pgIdentitySQL = `SELECT current_setting('server_version_num')::int, current_setting('server_version'),
 (SELECT oid::int FROM pg_database WHERE datname = current_database()), current_database()`

const pgSystemIdentifierSQL = `SELECT system_identifier::text FROM pg_control_system()`

// readPostgresIdentity reads the PG instance identity; system_identifier is provided "when available"
// per contract (empty when privileges are insufficient, not treated as a failure).
func readPostgresIdentity(ctx context.Context, a *baseAdapter, cred Credential, mat *TLSMaterial) (*ServerIdentity, error) {
	ccfg, err := BuildPostgresConfig(ctx, a.ep, cred, mat, ConnectTimeout)
	if err != nil {
		return nil, err
	}
	conn, err := connectPostgres(ctx, ccfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(ctx) }()
	var versionNum int
	var version, dbName string
	var dbOID int
	if err := conn.QueryRow(ctx, pgIdentitySQL).Scan(&versionNum, &version, &dbOID, &dbName); err != nil {
		return nil, errors.New("database: postgresql probe failed")
	}
	// Instance identity v1: server_version_num + database_oid + endpoint.
	// Fallback remains stable when system_identifier is unavailable (invariant across credential rotation).
	marker := fmt.Sprintf("pg:%d:%d", versionNum, dbOID)
	identity := &ServerIdentity{
		EngineFamily:         "postgresql",
		ServerVersion:        normalizeVersionString(version),
		InstanceMarker:       marker,
		DatabaseOID:          uint32(dbOID),
		DatabaseName:         dbName,
		ConfiguredEndpointID: fmt.Sprintf("%s:%d", a.ep.Host, a.ep.Port),
	}
	var sysID string
	if err := conn.QueryRow(ctx, pgSystemIdentifierSQL).Scan(&sysID); err == nil && sysID != "" {
		identity.SystemIdentifier = sysID
		identity.InstanceMarker = "pg:" + sysID
	}
	return identity, nil
}

// connectPostgres opens a one-shot native pgx connection (used only for probing / identity reads).
func connectPostgres(ctx context.Context, ccfg *pgx.ConnConfig) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, ccfg)
	if err != nil {
		// §18.76 supplement (owner ruling: probe root cause reaches the operator directly): pgx error strings
		// explicitly redact passwords, so surfacing dial target / authentication reasons is safe.
		return nil, fmt.Errorf("database: postgresql dial failed: %w", err)
	}
	return conn, nil
}

// pgExplainPrefix PG: EXPLAIN (FORMAT JSON), never with ANALYZE.
func pgExplainPrefix() string { return "EXPLAIN (FORMAT JSON) " }

// parsePostgresExplain parses the root plan node of the PG EXPLAIN (FORMAT JSON) array.
func parsePostgresExplain(columns []string, rows [][]string) (*ExplainFact, error) {
	var joined strings.Builder
	for _, row := range rows {
		joined.WriteString(strings.Join(row, ""))
	}
	raw := []byte(joined.String())
	var docs []map[string]any
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, errors.New("database: explain output unparsable")
	}
	fact := &ExplainFact{RawEvidenceReference: fmt.Sprintf("sha256:%s:%d", hexSHA256(raw), len(raw))}
	if len(docs) == 0 {
		return nil, errors.New("database: explain output empty")
	}
	plan, _ := docs[0]["Plan"].(map[string]any)
	if plan == nil {
		return nil, errors.New("database: explain output missing plan")
	}
	if nt, ok := plan["Node Type"].(string); ok {
		fact.AccessMethod = nt
	}
	if rowsN, ok := plan["Plan Rows"].(float64); ok {
		fact.EstimatedRows = int64(math.Round(rowsN))
	}
	if cost, ok := plan["Total Cost"].(float64); ok {
		fact.EstimatedCost = cost
	}
	if idx, ok := plan["Index Name"].(string); ok && idx != "" {
		fact.IndexNames = append(fact.IndexNames, idx)
	}
	return fact, nil
}
