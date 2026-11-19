// factsource.go B4 Adapter wiring for the built-in tool evidence collection port.
//
// Package datasource is outside B7 allowed_paths, so review/application implements equivalent decryption
// here using the same Crypter, the same AAD recipe (datasource_credential/datasource_tls purposes and
// table/column bindings), and the same credential plaintext struct, constructing a review-purpose
// connection pool via the Adapter exported by package database.
// Drift risk: if datasource.Factory adjusts its envelope recipe, this file must be updated in lockstep
// (covered by integration tests using real encrypted rows).
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// ErrFactSourceUnavailable indicates the evidence collection channel is unavailable (fail closed -> 2012).
var (
	ErrFactSourceUnavailable     = errors.New("review: fact source unavailable")
	ErrFactSourceIdentityChanged = errors.New("review: fact source identity changed")
)

type factSource struct {
	db      *gorm.DB
	crypter *crypto.Crypter
	runID   string
	adapter database.Adapter
	pool    *database.PurposePool
	target  frozenDatasourceTarget
}

type representativeSQLResolverKey struct{}

// RepresentativeSQLResolver resolves the representative statement SQL for (runID, groupID) from memory when an ephemeral run is active.
type RepresentativeSQLResolver func(runID, groupID string) (string, bool, error)

func withRepresentativeSQLResolver(ctx context.Context, fn RepresentativeSQLResolver) context.Context {
	return context.WithValue(ctx, representativeSQLResolverKey{}, fn)
}

func representativeSQLResolverFromContext(ctx context.Context) RepresentativeSQLResolver {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(representativeSQLResolverKey{}).(RepresentativeSQLResolver)
	return fn
}

type postgresSearchPathPlanReader interface {
	ExplainWithPostgresSearchPath(
		context.Context,
		*database.PurposePool,
		database.PostgresSearchPath,
		string,
	) (*database.ExplainFact, error)
}

// AdapterFactSourceFor constructs the FactSourceFor wiring function (opens a pool per datasource row;
// carries runID for safe_explain to resolve representative statements).
func AdapterFactSourceFor(db *gorm.DB, crypter *crypto.Crypter) func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
	return func(ctx context.Context, datasourceID string, target frozenDatasourceTarget, runID string) (tool.FactSource, func(), error) {
		if err := target.validate(); err != nil || target.DatasourceID != datasourceID {
			return nil, nil, ErrFactSourceIdentityChanged
		}
		row, err := loadDatasourceTargetRow(ctx, db, datasourceID)
		if err != nil {
			return nil, nil, err
		}
		if !target.matchesConfig(row) {
			return nil, nil, ErrFactSourceIdentityChanged
		}
		adapter, cred, mat, identity, err := probeDatasourceConnection(ctx, db, crypter, row)
		if err != nil {
			return nil, nil, err
		}
		if !target.matchesIdentity(*identity) {
			_ = adapter.Close()
			return nil, nil, ErrFactSourceIdentityChanged
		}
		// The configured row is checked again after the live probe. This closes
		// the window in which another request could replace the target while
		// the identity query is in flight.
		after, err := loadDatasourceTargetRow(ctx, db, datasourceID)
		if err != nil || !target.matchesConfig(after) {
			_ = adapter.Close()
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, ErrFactSourceIdentityChanged
		}
		pool, err := adapter.OpenPurposePool(ctx, database.PurposeReview, cred, mat)
		if err != nil {
			_ = adapter.Close()
			return nil, nil, err
		}
		fs := &factSource{
			db: db, crypter: crypter, runID: runID,
			adapter: adapter, pool: pool, target: target,
		}
		return fs, func() {
			_ = pool.Close()
			_ = adapter.Close()
		}, nil
	}
}

func probeDatasourceConnection(
	ctx context.Context, db *gorm.DB, crypter *crypto.Crypter, row datasourceTargetRow,
) (database.Adapter, database.Credential, *database.TLSMaterial, *database.ServerIdentity, error) {
	return probeDatasourceConnectionForPurpose(
		ctx, db, crypter, row, "review",
	)
}

func probeDatasourceConnectionForPurpose(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	row datasourceTargetRow,
	credentialPurpose string,
) (database.Adapter, database.Credential, *database.TLSMaterial, *database.ServerIdentity, error) {
	if !row.Enabled || row.Host == "" || row.Port <= 0 {
		return nil, database.Credential{}, nil, nil, ErrFactSourceUnavailable
	}
	var (
		cred database.Credential
		err  error
	)
	if credentialPurpose == "review" {
		cred, err = openCredential(ctx, db, crypter, row.ID)
	} else {
		cred, err = openCredentialForPurpose(ctx, db, crypter, row.ID, credentialPurpose)
	}
	if err != nil {
		return nil, database.Credential{}, nil, nil, err
	}
	mat, err := openTLS(ctx, db, crypter, row.ID)
	if err != nil {
		return nil, database.Credential{}, nil, nil, err
	}
	databaseName := ""
	if row.DatabaseName != nil {
		databaseName = *row.DatabaseName
	}
	ep := database.Endpoint{
		Host: row.Host, Port: row.Port,
		DatabaseName: databaseName, CompatibilityMod: row.CompatibilityMode,
	}
	var adapter database.Adapter
	switch row.CompatibilityMode {
	case "mysql":
		adapter = database.NewMySQLAdapter(ep, database.AdapterOptions{})
	case "postgresql":
		adapter = database.NewPostgreSQLAdapter(ep, database.AdapterOptions{})
	default:
		return nil, database.Credential{}, nil, nil, ErrFactSourceUnavailable
	}
	identity, err := adapter.ReadServerIdentity(ctx, cred, mat)
	if err != nil {
		_ = adapter.Close()
		return nil, database.Credential{}, nil, nil, err
	}
	return adapter, cred, mat, identity, nil
}

func probeDatasourceIdentity(
	ctx context.Context, db *gorm.DB, crypter *crypto.Crypter, row datasourceTargetRow,
) (database.ServerIdentity, error) {
	adapter, _, _, identity, err := probeDatasourceConnection(ctx, db, crypter, row)
	if err != nil {
		return database.ServerIdentity{}, err
	}
	defer func() { _ = adapter.Close() }()
	if identity == nil {
		return database.ServerIdentity{}, ErrFactSourceUnavailable
	}
	return *identity, nil
}

// openCredential decrypts an enabled read-only credential (preferring review, falling back to query;
// never falling back to execution).
func openCredential(ctx context.Context, db *gorm.DB, crypter *crypto.Crypter, datasourceID string) (database.Credential, error) {
	cred, err := openCredentialForPurpose(ctx, db, crypter, datasourceID, "review")
	if err == nil || !errors.Is(err, ErrFactSourceUnavailable) {
		return cred, err
	}
	return openCredentialForPurpose(ctx, db, crypter, datasourceID, "query")
}

func openCredentialForPurpose(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	datasourceID string,
	credentialPurpose string,
) (database.Credential, error) {
	if credentialPurpose != "review" && credentialPurpose != "query" && credentialPurpose != "execution" {
		return database.Credential{}, ErrFactSourceUnavailable
	}
	var row struct {
		ID         string
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		Purpose    string
		Enabled    bool
	}
	err := db.WithContext(ctx).Raw(`
		select id::text as id, credential_ciphertext as ciphertext, credential_nonce as nonce,
		       credential_key_id as key_id, purpose, enabled
		from datasource_credentials
		where datasource_id = ? and purpose = ? and enabled limit 1`,
		datasourceID, credentialPurpose).Scan(&row).Error
	if err != nil {
		return database.Credential{}, err
	}
	if row.ID == "" {
		return database.Credential{}, ErrFactSourceUnavailable
	}
	plain, err := crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce, KeyID: row.KeyID, Algorithm: "aes-256-gcm",
	}, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_credentials", PrimaryKey: row.ID,
		Column: "credential", Purpose: "datasource_credential",
	})
	if err != nil {
		return database.Credential{}, err
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		return database.Credential{}, fmt.Errorf("review: credential payload malformed")
	}
	return database.Credential{Username: payload.Username, Password: payload.Password}, nil
}

// openTLS decrypts TLS material and splits PEM blocks (same semantics as datasource.splitTLSMaterial:
// when a private key is present => last certificate is client cert and the rest form the CA chain;
// when no private key is present => all certificates form the CA chain).
func openTLS(ctx context.Context, db *gorm.DB, crypter *crypto.Crypter, datasourceID string) (*database.TLSMaterial, error) {
	var row struct {
		ID         string
		Ciphertext []byte
		Nonce      []byte
		KeyID      *string
		Algorithm  *string
		Verify     bool
		ServerName *string
	}
	err := db.WithContext(ctx).Raw(`
		select id::text as id, material_ciphertext as ciphertext, material_nonce as nonce,
		       material_key_id as key_id, material_algorithm as algorithm,
		       verify_server as verify, server_name
		from datasource_tls_materials where datasource_id = ? limit 1`, datasourceID).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" || row.KeyID == nil {
		return nil, nil
	}
	plain, err := crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce, KeyID: *row.KeyID, Algorithm: derefString(row.Algorithm),
	}, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_tls_materials", PrimaryKey: row.ID,
		Column: "material", Purpose: "datasource_tls",
	})
	if err != nil {
		return nil, err
	}
	mat := &database.TLSMaterial{VerifyServer: row.Verify}
	if row.ServerName != nil {
		mat.ServerName = *row.ServerName
	}
	var certs [][]byte
	rest := plain
	var lastKey []byte
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE":
			certs = append(certs, pem.EncodeToMemory(block))
		case strings.HasSuffix(block.Type, "PRIVATE KEY"):
			lastKey = pem.EncodeToMemory(block)
		}
	}
	if lastKey != nil && len(certs) > 0 {
		mat.CertPEM = certs[len(certs)-1]
		mat.KeyPEM = lastKey
		certs = certs[:len(certs)-1]
	}
	for _, c := range certs {
		mat.CAPEM = append(mat.CAPEM, c...)
	}
	return mat, nil
}

// ---- Evidence collection implementations for the five built-in tools (database.Port semantics) ----

func (f *factSource) DatasourceIdentity(ctx context.Context) (map[string]any, error) {
	return map[string]any{
		"engine":                   f.target.CompatibilityMode,
		"engine_family":            f.target.EngineFamily,
		"server_version":           f.target.ServerVersion,
		"compatibility_mode":       f.target.CompatibilityMode,
		"instance_identity_status": "matched",
		"database_identity_status": "matched",
		"metadata_complete":        true,
		"truncated":                false,
	}, nil
}

func (f *factSource) SchemaMetadata(ctx context.Context, schema string) (map[string]any, error) {
	tables, err := f.adapter.ListTables(ctx, f.pool, schema)
	if err != nil {
		return nil, err
	}
	bounded := boundedMetadataTables(tables)
	complete := len(bounded) == len(tables)
	return map[string]any{
		"schema":               schema,
		"table_count":          len(tables),
		"returned_table_count": len(bounded),
		"tables":               bounded,
		"metadata_complete":    complete,
		"truncated":            !complete,
	}, nil
}

func (f *factSource) TableMetadata(ctx context.Context, schema, table string) (map[string]any, error) {
	exists, err := database.TableExists(ctx, f.adapter, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	if !exists {
		return absentTableMetadataFact(schema, table), nil
	}
	columns, err := f.adapter.ListColumns(ctx, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		// PostgreSQL permits a zero-column table. Re-observe before
		// interpreting an empty profile so a drop racing the two metadata
		// reads is not reported as a present table.
		exists, err = database.TableExists(ctx, f.adapter, f.pool, schema, table)
		if err != nil {
			return nil, err
		}
		if !exists {
			return absentTableMetadataFact(schema, table), nil
		}
	}
	projected, total, complete := metadataColumnFacts(columns)
	truncated := len(columns) > maxTableMetadataColumns
	return map[string]any{
		"schema":                schema,
		"table":                 table,
		"existence":             "present",
		"column_count":          total,
		"returned_column_count": len(projected),
		"columns":               projected,
		"metadata_complete":     complete,
		"truncated":             truncated,
		"estimate":              false,
	}, nil
}

func (f *factSource) TableStatistics(ctx context.Context, schema, table string) (map[string]any, error) {
	exists, err := database.TableExists(ctx, f.adapter, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	if !exists {
		return absentTableStatisticsFact(schema, table), nil
	}
	cols, err := f.adapter.ListColumns(ctx, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	idx, err := f.adapter.ListIndexes(ctx, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	if len(idx) == 0 {
		exists, err = database.TableExists(ctx, f.adapter, f.pool, schema, table)
		if err != nil {
			return nil, err
		}
		if !exists {
			return absentTableStatisticsFact(schema, table), nil
		}
	}
	hasPK := false
	for _, ix := range idx {
		if ix.Primary {
			hasPK = true
			break
		}
	}
	rows, supported, err := database.TableRowEstimate(ctx, f.adapter, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	rowStatus := "unsupported"
	if supported {
		rowStatus = "unknown"
		if rows != nil {
			rowStatus = "observed"
		}
	}
	// Metadata-derived statistics: all marked as estimate (PRD 9.4 — neither frontend nor model may
	// describe them as actual execution results).
	fact := map[string]any{
		"schema": schema, "table": table,
		"existence":           "present",
		"column_count":        len(cols),
		"index_count":         len(idx),
		"has_primary_key":     hasPK,
		"row_estimate_status": rowStatus,
		"metadata_complete":   true,
		"truncated":           false,
		"estimate":            true,
	}
	if rows != nil {
		fact["estimated_rows"] = *rows
	}
	return fact, nil
}

func (f *factSource) IndexMetadata(ctx context.Context, schema, table string) (map[string]any, error) {
	exists, err := database.TableExists(ctx, f.adapter, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	if !exists {
		return absentIndexMetadataFact(schema, table), nil
	}
	idx, err := f.adapter.ListIndexes(ctx, f.pool, schema, table)
	if err != nil {
		return nil, err
	}
	limit := len(idx)
	if limit > maxIndexMetadataEntries {
		limit = maxIndexMetadataEntries
	}
	out := make([]map[string]any, 0, limit)
	remainingColumns := maxIndexColumnEntries
	columnsTruncated := false
	for _, ix := range idx[:limit] {
		keyLimit := len(ix.ColumnNames)
		if keyLimit > remainingColumns {
			keyLimit = remainingColumns
			columnsTruncated = true
		}
		columns := append([]string(nil), ix.ColumnNames[:keyLimit]...)
		remainingColumns -= keyLimit
		includeLimit := len(ix.IncludedColumns)
		if includeLimit > remainingColumns {
			includeLimit = remainingColumns
			columnsTruncated = true
		}
		included := append([]string(nil), ix.IncludedColumns[:includeLimit]...)
		remainingColumns -= includeLimit
		expressionCount := ix.ExpressionCount
		if expressionCount > remainingColumns {
			expressionCount = remainingColumns
			columnsTruncated = true
		}
		remainingColumns -= expressionCount
		indexFact := map[string]any{
			"index_name":          ix.IndexName,
			"non_unique":          ix.NonUnique,
			"primary":             ix.Primary,
			"columns":             columns,
			"included_columns":    included,
			"expression_count":    expressionCount,
			"has_expression":      ix.ExpressionCount > 0,
			"prefix_column_count": len(ix.PrefixLengths),
			"has_prefix":          len(ix.PrefixLengths) > 0,
		}
		if len(ix.PrefixLengths) > 0 {
			prefixLimit := len(ix.PrefixLengths)
			if prefixLimit > maxIndexColumnEntries {
				prefixLimit = maxIndexColumnEntries
				columnsTruncated = true
			}
			indexFact["prefix_lengths"] = append([]int(nil), ix.PrefixLengths[:prefixLimit]...)
		}
		out = append(out, indexFact)
	}
	// Expression definitions and prefix-to-column ordering are deliberately not
	// copied into the Provider channel. Their presence therefore leaves the
	// structural profile explicitly incomplete instead of claiming the bounded
	// key/include lists describe the whole index.
	complete := len(out) == len(idx) && !columnsTruncated &&
		allIndexStructuresKnown(idx)
	return map[string]any{
		"schema":            schema,
		"table":             table,
		"existence":         "present",
		"index_count":       len(idx),
		"indexes":           out,
		"metadata_complete": complete,
		"truncated":         !complete,
	}, nil
}

func allIndexStructuresKnown(indexes []database.MetadataIndex) bool {
	for _, index := range indexes {
		if index.ExpressionCount > 0 || len(index.PrefixLengths) > 0 {
			return false
		}
	}
	return true
}

func (f *factSource) SafeExplain(ctx context.Context, schema, groupID string) (map[string]any, error) {
	return f.safeExplain(ctx, schema, groupID)
}

func (f *factSource) SafeExplainWithPostgresSearchPath(
	ctx context.Context,
	schema string,
	groupID string,
	path FrozenPostgresSearchPath,
) (map[string]any, error) {
	if f == nil || f.adapter == nil || f.pool == nil ||
		f.target.CompatibilityMode != "postgresql" ||
		path.Validate() != nil || path.HasTemporarySchema() ||
		database.ValidateIdentifier(schema) != nil {
		return nil, ErrFactSourceUnavailable
	}
	reader, ok := f.adapter.(postgresSearchPathPlanReader)
	if !ok {
		return nil, ErrFactSourceUnavailable
	}
	sqlText, err := f.representativeStatementSQL(ctx, groupID)
	if err != nil {
		return nil, err
	}
	fact, err := reader.ExplainWithPostgresSearchPath(
		ctx, f.pool, databasePostgresPath(path), sqlText,
	)
	if err != nil {
		return nil, err
	}
	return normalizedExplainFact(fact), nil
}

func (f *factSource) safeExplain(
	ctx context.Context, schema, groupID string,
) (map[string]any, error) {
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: the logical-alias rewrite channel was removed along with
	// flow-stage Schema mappings — raw SQL is sent to EXPLAIN verbatim, and schema is resolved by the caller
	// (stageSchemaBinding) to an allowed physical name.
	sqlText, err := f.representativeStatementSQL(ctx, groupID)
	if err != nil {
		return nil, err
	}
	fact, err := f.adapter.ExplainWithoutExecution(ctx, f.pool, schema, sqlText)
	if err != nil {
		return nil, err
	}
	return normalizedExplainFact(fact), nil
}

func normalizedExplainFact(fact *database.ExplainFact) map[string]any {
	if fact == nil {
		return nil
	}
	return map[string]any{
		"access_method":  fact.AccessMethod,
		"estimated_rows": fact.EstimatedRows,
		"estimated_cost": fact.EstimatedCost,
		"index_names":    fact.IndexNames,
		"warnings":       fact.Warnings,
		"estimate":       true,
	}
}

// representativeStatementSQL resolves the real SQL of a group's representative statement:
// sql_statements.ordinal -> draft payload decryption -> streaming statement splitting to take the matching ordinal.
// Plaintext resides in memory only during this call, never persisted to DB or entering default model context.
func (f *factSource) representativeStatementSQL(ctx context.Context, groupID string) (string, error) {
	return resolveRepresentativeStatementSQL(ctx, f.db, f.crypter, f.runID, groupID, f.runProfile(ctx))
}

// resolveRepresentativeStatementSQL resolves the raw SQL of a group's representative statement from either
// an active ephemeral resolver in ctx or decrypted draft_sql_payloads for persistent review runs.
func resolveRepresentativeStatementSQL(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	runID string,
	groupID string,
	profileHint string,
) (string, error) {
	if resolve := representativeSQLResolverFromContext(ctx); resolve != nil {
		if sqlText, ok, err := resolve(runID, groupID); ok {
			return sqlText, err
		}
	}
	if db == nil || crypter == nil || runID == "" || groupID == "" {
		return "", ErrFactSourceUnavailable
	}
	var row struct {
		DraftID string
		Ordinal int32
	}
	if err := db.WithContext(ctx).Raw(`
		select p.draft_id::text as draft_id, ss.ordinal
		from sql_fingerprint_groups g
		join sql_statements ss on ss.id = g.representative_statement_id
		join review_runs r on r.id = g.review_run_id
		join draft_sql_payloads p on p.draft_id = r.draft_id
		where g.id = ? and g.review_run_id = ?`, groupID, runID).Scan(&row).Error; err != nil {
		return "", err
	}
	if row.DraftID == "" {
		return "", ErrFactSourceUnavailable
	}
	var payload struct {
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		HMAC       string
	}
	if err := db.WithContext(ctx).Raw(`
		select sql_ciphertext as ciphertext, sql_nonce as nonce, sql_key_id as key_id, plaintext_hmac as hmac
		from draft_sql_payloads where draft_id = ?`, row.DraftID).Scan(&payload).Error; err != nil {
		return "", err
	}
	plain, err := crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: payload.Ciphertext, Nonce: payload.Nonce, KeyID: payload.KeyID, Algorithm: "aes-256-gcm",
	}, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "draft_sql_payloads", PrimaryKey: row.DraftID,
		Column: "sql_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		return "", err
	}
	if profileHint == "" {
		profileHint = runProfileForDB(ctx, db, runID)
	}
	profile, err := fingerprint.ParseProfile(profileHint)
	if err != nil {
		return "", ErrFactSourceUnavailable
	}
	splitter := changeset.NewSplitter(bytes.NewReader(plain), profile, 524288)
	for {
		stmt, done, serr := splitter.Next()
		if serr != nil {
			return "", ErrFactSourceUnavailable
		}
		if done {
			return "", ErrFactSourceUnavailable
		}
		if int32(stmt.Index) == row.Ordinal {
			return string(stmt.SQL), nil
		}
	}
}

// runProfile resolves the dialect profile of the flow's first stage (statement splitter input).
func (f *factSource) runProfile(ctx context.Context) string {
	return runProfileForDB(ctx, f.db, f.runID)
}

func runProfileForDB(ctx context.Context, db *gorm.DB, runID string) string {
	if db == nil || runID == "" {
		return "mysql/mysql"
	}
	var name string
	if err := db.WithContext(ctx).Raw(`
		select d.engine || '/' || d.compatibility_mode as name
		from review_runs r
		join change_drafts cd on cd.id = r.draft_id
		join flow_stages fs on fs.flow_id = cd.flow_id
		join datasources d on d.id = fs.datasource_id
		where r.id = ?
		order by fs.position limit 1`, runID).Scan(&name).Error; err != nil {
		return "mysql/mysql"
	}
	if name == "" {
		return "mysql/mysql"
	}
	return name
}

// derefString is a local string dereference helper (kept local to avoid duplicate registration outside service package).
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
