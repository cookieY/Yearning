// schema_binding.go SEC-0041 fix: bind AI tool call schema/table arguments to the stage's frozen
// object scope (data minimization; CWE-863 authorization boundary).
//
// Basis: security-review SEC-0041 — built-in evidence tools previously validated only argument names
// (tool/registry.go validateArguments), allowing the model to request schemas outside the frozen scope
// and exfiltrate table/column/index metadata from other schemas.
//
// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: flow stages no longer configure logical-to-physical Schema
// mappings (the workflow write surface always seals an empty placeholder envelope, preserving
// schema_mapping_* encrypted columns and AAD structure with zero migration; read surfaces no longer
// consume mapping content as active configuration). Each stage's object scope falls back to the
// unmapped branch (same semantics as execution resolveStageSchema):
//   - PostgreSQL: execution-identity search_path frozen binding (frozenPostgresBindingFromContext,
//     from the stage snapshot normalized_context envelope; binding v3 delivered in B7 includes
//     callables catalog OID proof).
//   - MySQL / other engines: the datasource connection default database (datasources.database_name)
//     is the sole allowed schema.
//
// During review, the Run's frozen hash (frozenHashes -> review_runs.flow_snapshot_hash, whose
// composition no longer includes the mapping ciphertext component) re-verifies that the flow row has
// not drifted; on drift or unavailable binding, that stage fails closed (tools carrying a schema
// argument are uniformly rejected with 2012 without touching the DB). Explicitly qualified objects must
// use allowed physical schema names directly, and SQL qualifiers are never rewritten (the execution
// contract never performs logical alias substitution). The table argument is bounded within the frozen
// scope by its accompanying schema argument, so this layer performs pre-DB identifier validity checks
// (the adapter also validates before quoting). Within the model-visible channel, both schema and table
// are Run-scoped anonymous tokens (SEC-0002), and validation runs after detokenizeToolArgs and before any FactSource call.
package application

import (
	"context"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// stageSchemaBinding holds the frozen Schema object set for a single stage.
// defensible=false indicates the frozen scope is unavailable (flow hash drift / missing row / missing
// snapshot or incomplete binding) — fail closed: any tool call carrying a schema argument is treated as out of bounds.
type stageSchemaBinding struct {
	defensible bool
	// schemas is the frozen set of allowed physical schema names
	// (PostgreSQL: the execution-identity explicit search_path members;
	// MySQL/other engines: the datasource default database alone). These
	// names authorize tool scope; physical names never reach the model
	// channel untokenized.
	schemas map[string]bool
	// postgres is present only for a frozen PostgreSQL environment whose
	// execution-identity search_path is bound from the stage snapshot.
	// MySQL and other engines use schemas (single default database).
	postgres *frozenPostgresPathBinding
}

// frozenStageSchemas constructs the frozen object scope for all stage snapshots of a Run
// (key = stage_position). Any unavailable condition retains the fail-closed default binding for that
// stage without returning an error — an unavailable binding is a tool-level rejection condition (2012)
// and does not alter Run lifecycle semantics.
func (r *AgentRunner) frozenStageSchemas(ctx context.Context, db *gorm.DB,
	run *persistence.ReviewRunModel, snapshots []persistence.StageReviewSnapshotModel) map[int32]*stageSchemaBinding {
	// DB path: the Run row does not store flow_id, resolving it via the draft row (ephemeral Runs have no
	// draft row — RCP-20261101: callers use frozenStageSchemasForFlow to pass the flow directly).
	var flowID string
	if err := db.WithContext(ctx).Raw(
		`select flow_id::text from change_drafts where id = ?`, run.DraftID).Scan(&flowID).Error; err != nil || flowID == "" {
		out := make(map[int32]*stageSchemaBinding, len(snapshots))
		for i := range snapshots {
			out[snapshots[i].StagePosition] = &stageSchemaBinding{}
		}
		return out
	}
	return r.frozenStageSchemasForFlow(ctx, db, flowID, run, snapshots)
}

// frozenStageSchemasForFlow constructs per-stage schema bindings for an explicit flow (reused by the DB
// path after resolving flow_id from the draft; ephemeral Runs pass er.FlowID directly — no draft row to query).
func (r *AgentRunner) frozenStageSchemasForFlow(ctx context.Context, db *gorm.DB, flowID string,
	run *persistence.ReviewRunModel, snapshots []persistence.StageReviewSnapshotModel) map[int32]*stageSchemaBinding {
	out := make(map[int32]*stageSchemaBinding, len(snapshots))
	for i := range snapshots {
		out[snapshots[i].StagePosition] = &stageSchemaBinding{}
	}
	// Submission-time freeze re-verification: the Run's frozen hash must equal the hash recomputed from
	// the current flow row; drift means the flow row no longer matches the stage configuration at review
	// time, keeping all stages fail-closed.
	if flowID == "" {
		return out
	}
	var flow flowRow
	if err := db.WithContext(ctx).Raw(
		`select id, flow_type, status, rule_set_id, version from flows where id = ?`, flowID).Scan(&flow).Error; err != nil {
		return out
	}
	flowHash, _, _, err := r.svc.frozenHashes(ctx, db, flowID, flow)
	if err != nil || flowHash == "" || flowHash != run.FlowSnapshotHash {
		return out
	}
	var rows []struct {
		Position          int32
		CompatibilityMode string
		DatabaseName      *string
	}
	if err := db.WithContext(ctx).Raw(`
		select fs.position, d.compatibility_mode, d.database_name
		from flow_stages fs
		join datasources d on d.id = fs.datasource_id
		where fs.flow_id = ? order by fs.position`, flowID).Scan(&rows).Error; err != nil {
		return out
	}
	snapshotByPosition := make(map[int32]*persistence.StageReviewSnapshotModel, len(snapshots))
	for i := range snapshots {
		snapshotByPosition[snapshots[i].StagePosition] = &snapshots[i]
	}
	for i := range rows {
		row := &rows[i]
		if row.CompatibilityMode == "postgresql" {
			// PostgreSQL stage: execution-identity search_path binding comes from the stage snapshot
			// normalized_context envelope (frozen at review time, never reading template rows).
			snap := snapshotByPosition[row.Position]
			if snap == nil {
				continue
			}
			binding, ok := frozenPostgresBindingFromContext(
				r.svc.Crypter, run.ID, row.Position, crypto.Envelope{
					Ciphertext: snap.NormalizedContextCiphertext,
					Nonce:      snap.NormalizedContextNonce,
					KeyID:      snap.NormalizedContextKeyID,
					Algorithm:  snap.NormalizedContextAlgorithm,
				},
			)
			if !ok {
				continue
			}
			if !binding.Complete || !binding.CallablesSafe {
				continue
			}
			pathSet := make(map[string]bool, len(binding.Path.Explicit))
			for _, schema := range binding.Path.Explicit {
				pathSet[schema] = true
			}
			out[row.Position] = &stageSchemaBinding{
				defensible: true,
				schemas:    pathSet,
				postgres:   binding,
			}
			continue
		}
		// MySQL / other engines: the datasource connection default database is the sole allowed schema.
		if row.DatabaseName == nil {
			continue
		}
		dn := *row.DatabaseName
		if dn == "" || database.ValidateIdentifier(dn) != nil {
			continue
		}
		out[row.Position] = &stageSchemaBinding{
			defensible: true,
			schemas:    map[string]bool{dn: true},
		}
	}
	return out
}

// buildTokenLegend constructs a controlled anonymous token legend from the stage's frozen Schema
// binding set and statement-touched tables (NEW-AI-03 bootstrap): at Run startup / stage start, it
// pre-tokenizes schema identifiers in the binding set (and statement-touched tables when present) so
// the model can invoke schema/table tools using legend tokens on turn 1, eliminating the circular
// dependency where tokens only exist after a tool returns. The legend contains only tokens and semantic
// roles, never raw identifiers. If tokenizing an individual identifier fails (e.g. a malformed name
// containing mapping delimiter characters), that entry is skipped — it cannot validly appear in token
// form on the model channel, and the real name remains governed by stageBindingAllowsTool frozen-set validation (never silently allowed).
func buildTokenLegend(mapper *privacy.RunMapper, binding *stageSchemaBinding, touchedTables []string) []agent.TokenLegendEntry {
	if mapper == nil {
		return nil
	}
	out := []agent.TokenLegendEntry{}
	seen := map[string]bool{}
	add := func(kind privacy.IdentifierKind, name string) {
		if name == "" {
			return
		}
		tok := mapper.LookupScoped(kind, privacy.ScopeCaseSensitive, name)
		if tok == "" {
			tok = mapper.Lookup(kind, name)
		}
		if tok == "" {
			var err error
			tok, err = mapper.Token(kind, name)
			if err != nil {
				return // Malformed name cannot be tokenized: omit from legend (see comment above)
			}
		}
		if seen[tok] {
			return // Each token (e.g. duplicate table names) appears at most once in the legend
		}
		seen[tok] = true
		out = append(out, agent.TokenLegendEntry{Token: tok, Kind: string(kind)})
	}
	if binding != nil {
		pgSchemas := binding.allPostgresSchemas()
		var schemas []string
		for s := range binding.schemas {
			schemas = append(schemas, s)
		}
		schemas = append(schemas, pgSchemas...)
		sort.Strings(schemas) // Deterministic legend order (map iteration is unordered)
		for _, s := range schemas {
			add(privacy.KindSchema, s)
		}
	}
	tables := append([]string{}, touchedTables...)
	sort.Strings(tables)
	for _, tbl := range tables {
		add(privacy.KindTable, tbl)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// defaultSchema returns the single default schema name for a stage: for MySQL / other engines = the
// datasource connection default database (when the allowed set has exactly one element); PostgreSQL
// bindings do not provide a default schema for unqualified objects (search_path binding is handled
// separately by the postgres branch). Under the unmapped branch this name is the allowed set itself, where physical and logical names are identical.
func (b *stageSchemaBinding) defaultSchema() string {
	if b == nil || !b.defensible || b.postgres != nil || len(b.schemas) != 1 {
		return ""
	}
	for schema := range b.schemas {
		return schema
	}
	return ""
}

func (b *stageSchemaBinding) physicalSchema(schema string) (string, bool) {
	if b == nil || !b.defensible {
		return "", false
	}
	if b.allowsPhysicalSchema(schema) {
		return schema, true
	}
	if b.allowsPostgresLookupSchema(schema) {
		return schema, true
	}
	return "", false
}

// explicitPhysicalSchema applies the stricter identity rule for a schema
// explicitly qualified in SQL. Execution never rewrites qualifiers, so only
// an allowed physical schema name is valid here.
func (b *stageSchemaBinding) explicitPhysicalSchema(schema string) (string, bool) {
	if b == nil || !b.defensible || !b.allowsPhysicalSchema(schema) {
		return "", false
	}
	return schema, true
}

func (b *stageSchemaBinding) allowsPhysicalSchema(schema string) bool {
	if b.allowsPostgresLookupSchema(schema) {
		return true
	}
	return b.schemas[schema]
}

func (b *stageSchemaBinding) allowsPostgresLookupSchema(schema string) bool {
	if b == nil || b.postgres == nil || schema == "" ||
		schema == "pg_catalog" || strings.HasPrefix(schema, "pg_temp_") {
		return false
	}
	for _, candidate := range b.postgres.Path.Effective {
		if candidate == schema {
			return true
		}
	}
	return false
}

// stageBindingAllowsTool validates whether detokenized tool arguments fall within the stage's frozen
// object scope (SEC-0041; executed before registry.Execute and before any FactSource call).
//   - When the schema argument is non-empty, it must resolve to an allowed physical schema; unavailable
//     binding or out-of-scope schema is rejected immediately.
//   - An empty schema argument is delegated to registry required-argument validation for rejection
//     (likewise booked as 2012 without touching the DB).
//   - The table argument is bounded within the frozen scope by its schema argument (the scope does not
//     enumerate tables); pre-DB identifier validity checking runs here so malformed identifiers are rejected without touching the DB.
func stageBindingAllowsTool(b *stageSchemaBinding, args map[string]any) bool {
	_, ok := resolveStageBindingToolArgs(b, args)
	return ok
}

// resolveStageBindingToolArgs validates an explicitly referenced allowed
// physical schema and returns a copy whose schema argument is the frozen
// physical schema. The original argument map remains the audit
// representation of what the Provider actually requested.
func resolveStageBindingToolArgs(b *stageSchemaBinding, args map[string]any) (map[string]any, bool) {
	if b == nil {
		return nil, false // Unknown boundary (missing binding) fails closed
	}
	out := make(map[string]any, len(args)+1)
	for key, value := range args {
		out[key] = value
	}
	schema, _ := args["schema"].(string)
	if schema != "" {
		physical, ok := b.physicalSchema(schema)
		if !ok {
			return nil, false
		}
		out["schema"] = physical
	}
	if table, ok := args["table"].(string); ok && table != "" {
		if database.ValidateIdentifier(table) != nil {
			return nil, false
		}
	}
	return out, true
}

// schemaBoundFactSource exposes physical schema names only to the trusted
// FactSource. It deliberately does not pass a logical-to-physical SQL rewrite
// map: execution applies the physical schema only as the connection default
// and never rewrites explicit qualifiers.
type schemaBoundFactSource struct {
	tool.FactSource
	binding *stageSchemaBinding
}

func bindSchemaFactSource(source tool.FactSource, binding *stageSchemaBinding) tool.FactSource {
	if source == nil || binding == nil || !binding.defensible ||
		(len(binding.schemas) == 0 && binding.postgres == nil) {
		return source
	}
	return &schemaBoundFactSource{FactSource: source, binding: binding}
}

func (s *schemaBoundFactSource) SafeExplain(ctx context.Context, schema, groupID string) (map[string]any, error) {
	if s.binding != nil && s.binding.postgres != nil {
		path := s.binding.postgres.Path
		if path.Validate() != nil || path.HasTemporarySchema() ||
			database.ValidateIdentifier(schema) != nil {
			return nil, tool.ErrToolFailed
		}
		explainer, ok := s.FactSource.(postgresSearchPathExplainSource)
		if !ok {
			return nil, tool.ErrToolFailed
		}
		return explainer.SafeExplainWithPostgresSearchPath(
			ctx, schema, groupID, path,
		)
	}
	return s.FactSource.SafeExplain(ctx, schema, groupID)
}

type postgresSearchPathExplainSource interface {
	SafeExplainWithPostgresSearchPath(
		context.Context,
		string,
		string,
		FrozenPostgresSearchPath,
	) (map[string]any, error)
}

func (s *schemaBoundFactSource) TableMetadata(ctx context.Context, schema, table string) (map[string]any, error) {
	scoped, ok := s.FactSource.(tool.TableMetadataSource)
	if !ok {
		return nil, tool.ErrToolFailed
	}
	return scoped.TableMetadata(ctx, schema, table)
}
