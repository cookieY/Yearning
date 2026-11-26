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
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// stageSchemaBinding holds the frozen Schema object set for a single stage.
// defensible=false indicates the frozen scope is unavailable (flow hash drift / missing row / missing
// snapshot or incomplete binding) — fail closed: any tool call carrying a schema argument is treated as out of bounds.
type stageSchemaBinding struct {
	defensible bool
	// schemas is the frozen set of allowed physical schema names
	// (PostgreSQL: the execution-identity explicit search_path members;
	// MySQL/other engines: optional default database plus valid non-system SQL schema qualifiers). These
	// names authorize tool scope; physical names never reach the model
	// channel untokenized.
	schemas map[string]bool
	// defaultSchemaName stores the MySQL connection or explicit target_database default schema when
	// defaultSchemaSet is true (empty when only explicit SQL qualifiers are present).
	defaultSchemaName    string
	defaultSchemaSet     bool
	hasSQLDerivedSchemas bool
	// postgres is present only for a frozen PostgreSQL environment whose
	// execution-identity search_path is bound from the stage snapshot.
	// MySQL and other engines use schemas.
	postgres *frozenPostgresPathBinding
	// unavailableReason records a privacy-safe machine code explaining why defensible=false.
	unavailableReason string
}

type frozenFlowStageRow struct {
	Position          int32
	CompatibilityMode string
	DatabaseName      *string
}

func isValidPostgresExplicitSchema(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed != "" &&
		len(trimmed) <= 128 &&
		database.ValidateIdentifier(trimmed) == nil &&
		!database.IsDisallowedPostgresSystemSchema(trimmed)
}

// ValidateTargetDatabase validates an explicit target_database (or PostgreSQL target schema) override:
// non-empty identifier <= 128 chars, never targeting built-in MySQL or PostgreSQL system schemas.
func ValidateTargetDatabase(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || len(trimmed) > 128 || database.ValidateIdentifier(trimmed) != nil {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "Invalid target_database"}
	}
	if database.IsDisallowedPostgresSystemSchema(trimmed) {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "Invalid target_database"}
	}
	switch strings.ToLower(trimmed) {
	case "mysql", "sys", "information_schema", "performance_schema":
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "Invalid target_database"}
	}
	return nil
}

// resolveEffectiveMySQLDatabase returns the effective default schema for a MySQL-compatible stage
// from the explicit target_database override (when supplied) or datasources.database_name.
func resolveEffectiveMySQLDatabase(row frozenFlowStageRow, targetDatabase ...string) string {
	if len(targetDatabase) > 0 {
		if explicit := strings.TrimSpace(targetDatabase[0]); explicit != "" {
			return explicit
		}
	}
	if row.DatabaseName != nil {
		return strings.TrimSpace(*row.DatabaseName)
	}
	return ""
}

// ExplicitMySQLSchemaCheck summarizes explicit schema qualification checks shared by AI review
// schema binding and execution preflight statement verification.
type ExplicitMySQLSchemaCheck struct {
	ValidSchemas      []string
	TableCount        int
	HasInvalidSchema  bool
	HasUnqualified    bool
	HasUnsupportedRef bool
}

// AllExplicitlyQualified reports whether the inspected SQL contains at least one physical table
// reference and every table/physical object carries a valid non-system schema qualifier.
func (c ExplicitMySQLSchemaCheck) AllExplicitlyQualified() bool {
	return c.TableCount > 0 && !c.HasInvalidSchema && !c.HasUnqualified && !c.HasUnsupportedRef
}

func inspectMySQLObjectSchema(
	kind, token, schemaToken, relation string,
	mapper *privacy.RunMapper,
	seen map[string]bool,
	out *ExplicitMySQLSchemaCheck,
) {
	if kind == "alias" || kind == "cte" {
		return
	}
	if kind == "schema" {
		tok := schemaToken
		if tok == "" {
			tok = token
		}
		if relation != "reference" {
			out.HasUnsupportedRef = true
		}
		origKind, original, found := mapper.Original(tok)
		if !found || origKind != privacy.KindSchema || ValidateTargetDatabase(original) != nil {
			out.HasInvalidSchema = true
			return
		}
		if !seen[original] {
			seen[original] = true
			out.ValidSchemas = append(out.ValidSchemas, original)
		}
		return
	}
	if kind == "column" && schemaToken == "" {
		return
	}
	if kind == "table" {
		out.TableCount++
	}
	if schemaToken == "" {
		out.HasUnqualified = true
		return
	}
	origKind, original, found := mapper.Original(schemaToken)
	if !found || origKind != privacy.KindSchema || ValidateTargetDatabase(original) != nil {
		out.HasInvalidSchema = true
		return
	}
	if !seen[original] {
		seen[original] = true
		out.ValidSchemas = append(out.ValidSchemas, original)
	}
}

// InspectMySQLFingerprintObjects validates explicit schema qualifiers emitted by fingerprint analysis
// for a single SQL statement.
func InspectMySQLFingerprintObjects(
	objects []fingerprint.ObjectReference,
	metadataComplete bool,
	mapper *privacy.RunMapper,
) ExplicitMySQLSchemaCheck {
	var out ExplicitMySQLSchemaCheck
	if !metadataComplete || mapper == nil {
		out.HasUnsupportedRef = true
		return out
	}
	seen := map[string]bool{}
	for _, obj := range objects {
		inspectMySQLObjectSchema(obj.Kind, obj.Token, obj.SchemaToken, string(obj.Relation), mapper, seen, &out)
	}
	sort.Strings(out.ValidSchemas)
	return out
}

func inspectMySQLGroupObjects(groups []provider.AnonymizedGroup, mapper *privacy.RunMapper) ExplicitMySQLSchemaCheck {
	var out ExplicitMySQLSchemaCheck
	if mapper == nil {
		out.HasUnsupportedRef = true
		return out
	}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, obj := range group.Objects {
			inspectMySQLObjectSchema(obj.Kind, obj.Token, obj.SchemaToken, obj.Relation, mapper, seen, &out)
		}
	}
	sort.Strings(out.ValidSchemas)
	return out
}

func resolvePostgresStageBinding(
	crypter *crypto.Crypter,
	runID string,
	row frozenFlowStageRow,
	snap *persistence.StageReviewSnapshotModel,
) *stageSchemaBinding {
	// PostgreSQL stage: execution-identity search_path binding comes from the stage snapshot
	// normalized_context envelope (frozen at review time, never reading template rows).
	if snap == nil || crypter == nil {
		return &stageSchemaBinding{unavailableReason: "postgres_search_path_unavailable"}
	}
	env := crypto.Envelope{
		Ciphertext: snap.NormalizedContextCiphertext,
		Nonce:      snap.NormalizedContextNonce,
		KeyID:      snap.NormalizedContextKeyID,
		Algorithm:  snap.NormalizedContextAlgorithm,
	}
	binding, reason := openFrozenPostgresBindingFromContext(crypter, runID, row.Position, env)
	if binding == nil || !binding.Complete || !binding.CallablesSafe {
		return &stageSchemaBinding{
			unavailableReason: reason,
		}
	}
	pathSet := make(map[string]bool)
	for _, schema := range binding.Path.Explicit {
		pathSet[schema] = true
	}
	for _, schema := range binding.ExplicitSchemas {
		if isValidPostgresExplicitSchema(schema) {
			pathSet[schema] = true
		}
	}
	return &stageSchemaBinding{
		defensible:           true,
		schemas:              pathSet,
		hasSQLDerivedSchemas: len(binding.ExplicitSchemas) > 0 || binding.TargetSchema != "",
		postgres:             binding,
	}
}

func resolveMySQLStageBinding(
	row frozenFlowStageRow,
	targetDatabase string,
	groups []provider.AnonymizedGroup,
	mapper *privacy.RunMapper,
) *stageSchemaBinding {
	defaultDB := resolveEffectiveMySQLDatabase(row, targetDatabase)
	hasExplicitTarget := strings.TrimSpace(targetDatabase) != ""
	if defaultDB != "" && ValidateTargetDatabase(defaultDB) != nil {
		return &stageSchemaBinding{unavailableReason: "datasource_database_required"}
	}
	check := inspectMySQLGroupObjects(groups, mapper)
	schemas := make(map[string]bool)
	if defaultDB != "" {
		schemas[defaultDB] = true
	}
	hasSQLDerived := false
	for _, schema := range check.ValidSchemas {
		schemas[schema] = true
		if schema != defaultDB {
			hasSQLDerived = true
		}
	}
	if len(schemas) == 0 {
		if check.HasInvalidSchema {
			return &stageSchemaBinding{unavailableReason: "sql_schema_out_of_scope"}
		}
		return &stageSchemaBinding{unavailableReason: "datasource_database_required"}
	}
	return &stageSchemaBinding{
		defensible:           true,
		schemas:              schemas,
		defaultSchemaName:    defaultDB,
		defaultSchemaSet:     true,
		hasSQLDerivedSchemas: hasSQLDerived || hasExplicitTarget,
	}
}

// frozenStageSchemasForFlow constructs per-stage schema bindings for an explicit flow (reused across
// ephemeral Runs by passing er.FlowID directly — no draft row to query).
func (r *AgentRunner) frozenStageSchemasForFlow(
	ctx context.Context,
	db *gorm.DB,
	flowID string,
	run *persistence.ReviewRunModel,
	snapshots []persistence.StageReviewSnapshotModel,
	groupsForStage func(datasourceID string) []provider.AnonymizedGroup,
	mapper *privacy.RunMapper,
	targetDatabase ...string,
) map[int32]*stageSchemaBinding {
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
	var rows []frozenFlowStageRow
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
	var crypter *crypto.Crypter
	if r != nil && r.svc != nil {
		crypter = r.svc.Crypter
	}
	var runID string
	if run != nil {
		runID = run.ID
	}
	var targetDB string
	if len(targetDatabase) > 0 {
		targetDB = strings.TrimSpace(targetDatabase[0])
	}
	for i := range rows {
		row := rows[i]
		snap := snapshotByPosition[row.Position]
		if row.CompatibilityMode == "postgresql" {
			out[row.Position] = resolvePostgresStageBinding(crypter, runID, row, snap)
			continue
		}
		var stageGroups []provider.AnonymizedGroup
		if snap != nil && groupsForStage != nil {
			stageGroups = groupsForStage(snap.DatasourceID)
		}
		out[row.Position] = resolveMySQLStageBinding(row, targetDB, stageGroups, mapper)
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

// defaultSchema returns the default schema name for a stage when unqualified table references are
// resolved (empty when only explicit SQL qualifiers are authorized or on PostgreSQL stages).
func (b *stageSchemaBinding) defaultSchema() string {
	if b == nil || !b.defensible || b.postgres != nil {
		return ""
	}
	if b.defaultSchemaSet {
		return b.defaultSchemaName
	}
	if len(b.schemas) != 1 {
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
	if b == nil || b.postgres == nil || database.IsDisallowedPostgresSystemSchema(schema) {
		return false
	}
	return b.postgres.containsAllowedSchema(schema)
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
