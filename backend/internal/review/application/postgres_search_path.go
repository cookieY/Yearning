package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

const (
	stagePostgresSearchPathKey          = "postgres_search_path"
	stagePostgresObjectBindingKey       = "postgres_object_binding"
	stagePostgresSearchPathStatusKey    = "postgres_search_path_status"
	postgresSearchPathUnavailable       = "unavailable"
	postgresSearchPathSnapshotVersion   = 1
	postgresObjectResolutionVersion     = 3
	postgresSearchPathMaxSchemas        = 64
	postgresSearchPathMaxResolvedTables = 50000
)

var errPostgresSearchPathInvalid = errors.New("review: postgres search_path snapshot invalid")

// PostgresSearchTarget is the trusted target projection passed to the
// application test seams. Credentials and TLS material are intentionally
// absent: the production resolver reloads them from the encrypted datasource
// rows using the same trusted configuration-drift checks as the review tools.
type PostgresSearchTarget struct {
	DatasourceID      string
	CompatibilityMode string
	Host              string
	Port              int
	DatabaseName      *string
}

// PostgresRelationRequest identifies one original relation occurrence. The
// application creates one request for every statement-ordinal/token pair.
type PostgresRelationRequest struct {
	Ordinal    int32
	TableToken string
	TableName  string
}

// PostgresRelationLookup is a complete baseline for every schema in the frozen
// effective path. PresentBySchema values are table, view, sequence, index,
// foreign_table, or other. A Complete lookup proves that every searched schema
// exists and is visible to the execution role, including absence entries.
type PostgresRelationLookup struct {
	Ordinal         int32
	TableToken      string
	TableName       string
	PresentBySchema map[string]string
	OIDBySchema     map[string]uint32
	Complete        bool
}

// PostgresRelationBaseline is one exact-name result from a single bounded
// catalog snapshot. The application replays batch effects locally and assigns
// the same baseline to every statement occurrence of that exact name.
type PostgresRelationBaseline struct {
	TableName       string
	PresentBySchema map[string]string
	OIDBySchema     map[string]uint32
}

// PostgresPathProbe and PostgresRelationResolver are test seams for
// application integration fixtures whose execution target is intentionally
// unreachable. Production leaves them nil and uses the target database
// adapter with execution/review purpose pools.
type PostgresPathProbe func(context.Context, PostgresSearchTarget) (FrozenPostgresSearchPath, error)
type PostgresRelationResolver func(
	context.Context,
	PostgresSearchTarget,
	FrozenPostgresSearchPath,
	PostgresRelationRequest,
) (PostgresRelationLookup, error)
type PostgresRelationBatchResolver func(
	context.Context,
	PostgresSearchTarget,
	FrozenPostgresSearchPath,
	[]string,
) ([]PostgresRelationBaseline, error)
type PostgresCallableResolver func(
	context.Context,
	PostgresSearchTarget,
	FrozenPostgresSearchPath,
	[]database.PostgresCallableRequest,
) ([]database.PostgresCallableResolution, error)

// FrozenPostgresSearchPath is the encrypted review-stage snapshot used by
// review, change-order submission and execution. Explicit is the list applied
// with SET search_path; Effective is the fully expanded lookup order returned
// by current_schemas(true). CreationSchema is current_schema().
type FrozenPostgresSearchPath struct {
	Version        int      `json:"version"`
	CurrentUser    string   `json:"current_user"`
	Configured     string   `json:"configured"`
	Explicit       []string `json:"explicit"`
	Effective      []string `json:"effective"`
	CreationSchema string   `json:"creation_schema"`
}

func (p FrozenPostgresSearchPath) Validate() error {
	if p.Version != postgresSearchPathSnapshotVersion ||
		p.CurrentUser == "" ||
		p.CreationSchema == "" ||
		len(p.Explicit) == 0 ||
		len(p.Effective) == 0 ||
		len(p.Explicit) > postgresSearchPathMaxSchemas ||
		len(p.Effective) > postgresSearchPathMaxSchemas+3 {
		return errPostgresSearchPathInvalid
	}
	if database.ValidateIdentifier(p.CreationSchema) != nil {
		return errPostgresSearchPathInvalid
	}
	seenExplicit := map[string]bool{}
	for _, schema := range p.Explicit {
		if database.ValidateIdentifier(schema) != nil || seenExplicit[schema] {
			return errPostgresSearchPathInvalid
		}
		seenExplicit[schema] = true
	}
	explicit := map[string]bool{}
	for _, schema := range p.Explicit {
		explicit[schema] = true
	}
	seenEffective := map[string]bool{}
	for _, schema := range p.Effective {
		if schema == "" || len(schema) > 256 || seenEffective[schema] {
			return errPostgresSearchPathInvalid
		}
		// current_schemas(true) may contain implicit pg_catalog and pg_temp_*.
		// Other implicit names are not valid PostgreSQL schemas and must fail.
		if !explicit[schema] && schema != "pg_catalog" &&
			!strings.HasPrefix(schema, "pg_temp_") {
			return errPostgresSearchPathInvalid
		}
		seenEffective[schema] = true
	}
	if p.CreationSchema != p.Explicit[0] {
		return errPostgresSearchPathInvalid
	}
	expectedEffective := p.Explicit
	if !containsPostgresSchema(p.Explicit, "pg_catalog") {
		expectedEffective = append([]string{"pg_catalog"}, p.Explicit...)
	}
	effectiveWithoutTemporary := make([]string, 0, len(p.Effective))
	for _, schema := range p.Effective {
		if !strings.HasPrefix(schema, "pg_temp_") {
			effectiveWithoutTemporary = append(effectiveWithoutTemporary, schema)
		}
	}
	if !equalStrings(effectiveWithoutTemporary, expectedEffective) {
		return errPostgresSearchPathInvalid
	}
	return nil
}

func (p FrozenPostgresSearchPath) Equal(other FrozenPostgresSearchPath) bool {
	return p.Version == other.Version &&
		p.CurrentUser == other.CurrentUser &&
		p.CreationSchema == other.CreationSchema &&
		equalStrings(p.Explicit, other.Explicit) &&
		equalStrings(p.Effective, other.Effective)
}

func (p FrozenPostgresSearchPath) HasTemporarySchema() bool {
	for _, schema := range p.Effective {
		if strings.HasPrefix(schema, "pg_temp_") {
			return true
		}
	}
	return false
}

// CallableLookupSafe reports whether unqualified functions and operators in
// pg_catalog cannot be shadowed by an earlier user schema. PostgreSQL inserts
// pg_catalog implicitly before search_path unless it is named explicitly, so
// an explicit path such as app,pg_catalog is not safe for callable review.
func (p FrozenPostgresSearchPath) CallableLookupSafe() bool {
	for _, schema := range p.Effective {
		if strings.HasPrefix(schema, "pg_temp_") {
			continue
		}
		return schema == "pg_catalog"
	}
	return false
}

func containsPostgresSchema(schemas []string, want string) bool {
	for _, schema := range schemas {
		if schema == want {
			return true
		}
	}
	return false
}

func postgresSearchTargetOf(target frozenDatasourceTarget) PostgresSearchTarget {
	var databaseName *string
	if target.DatabaseName != "" {
		name := target.DatabaseName
		databaseName = &name
	}
	return PostgresSearchTarget{
		DatasourceID:      target.DatasourceID,
		CompatibilityMode: target.CompatibilityMode,
		Host:              target.Host,
		Port:              target.Port,
		DatabaseName:      databaseName,
	}
}

type postgresRelationRequest struct {
	Ordinal    int32
	TableToken string
	TableName  string
}

// postgresRelationLookup contains trusted, local-only catalog information.
// PresentBySchema is deliberately complete for every schema in Effective so
// batch simulation can distinguish missing, shadowed and non-table relations.
type postgresRelationLookup struct {
	Ordinal         int32
	TableToken      string
	TableName       string
	PresentBySchema map[string]string // schema -> table|view|foreign_table|other
	OIDBySchema     map[string]uint32
	Complete        bool
}

// ResolvePostgresRelations is implemented at the database boundary. The
// injected service seam returns the execution-role environment from a clean
// connection and a complete relation lookup for each unqualified table token.
type ResolvePostgresRelations func(
	ordinal int32,
	sourceSQL string,
	objectToken string,
	objectName string,
	operation fingerprint.ValidityOperation,
	path FrozenPostgresSearchPath,
) (postgresRelationLookup, error)

type frozenPostgresObjectBinding struct {
	TableName        string `json:"table_name"`
	Schema           string `json:"schema,omitempty"`
	Kind             string `json:"kind,omitempty"`
	OID              uint32 `json:"oid,omitempty"`
	CreatedByOrdinal int32  `json:"created_by_ordinal,omitempty"`
	Present          bool   `json:"present"`
	Explicit         bool   `json:"explicit"`
	Supported        bool   `json:"supported"`
}

type frozenPostgresBaselineRelation struct {
	PresentBySchema map[string]string `json:"present_by_schema"`
	OIDBySchema     map[string]uint32 `json:"oid_by_schema"`
}

// frozenPostgresCallableWinner is the callable one signature resolves to on the
// frozen effective path. It records the owning schema, the resolved signature
// (routine argument OID list or operator left,right type pair), the result
// type, the catalog OID and whether that schema is pg_catalog, so send time can
// detect any winner being replaced, not only a shadowing one.
type frozenPostgresCallableWinner struct {
	Schema     string `json:"schema"`
	Signature  string `json:"signature"`
	ResultType string `json:"result_type"`
	OID        uint32 `json:"oid"`
	PgCatalog  bool   `json:"pg_catalog"`
}

// frozenPostgresCallable is the catalog identity of one unqualified callable
// name: exactly which callable every reachable signature resolves to.
type frozenPostgresCallable struct {
	Kind    string                         `json:"kind"`
	Name    string                         `json:"name"`
	Winners []frozenPostgresCallableWinner `json:"winners"`
}

type frozenPostgresPathBinding struct {
	Version            int                                       `json:"version"`
	Path               FrozenPostgresSearchPath                  `json:"path"`
	Complete           bool                                      `json:"complete"`
	CallablesSafe      bool                                      `json:"callables_safe"`
	CallablesChecked   bool                                      `json:"callables_checked"`
	CallablesRequested []string                                  `json:"callables_requested,omitempty"`
	Callables          map[string]frozenPostgresCallable         `json:"callables,omitempty"`
	Baseline           map[string]frozenPostgresBaselineRelation `json:"baseline"`
	Relations          map[string]frozenPostgresObjectBinding    `json:"relations"`
	UniformRelations   map[string]frozenPostgresObjectBinding    `json:"uniform_relations,omitempty"`
}

// Exported aliases define the encrypted review/execution contract while
// keeping the implementation names local to this package.
type FrozenPostgresObjectBinding = frozenPostgresObjectBinding
type FrozenPostgresBaselineRelation = frozenPostgresBaselineRelation
type FrozenPostgresPathBinding = frozenPostgresPathBinding

func postgresObjectBindingKey(ordinal int32, token string) string {
	return fmt.Sprintf("%d:%s", ordinal, token)
}

func (b *stageSchemaBinding) postgresObjectSchema(ordinal int32, token string) (string, bool, bool) {
	if b == nil || b.postgres == nil || !b.postgres.Complete || token == "" {
		return "", false, false
	}
	if resolution, ok := b.postgres.UniformRelations[token]; ok {
		if !resolution.Supported {
			return "", false, false
		}
		if !resolution.Present {
			return b.postgres.Path.CreationSchema, false, true
		}
		if database.ValidateIdentifier(resolution.Schema) != nil {
			return "", false, false
		}
		return resolution.Schema, true, true
	}
	resolution, ok := b.postgres.Relations[postgresObjectBindingKey(ordinal, token)]
	if !ok || !resolution.Supported {
		return "", false, false
	}
	if !resolution.Present {
		return b.postgres.Path.CreationSchema, false, true
	}
	if database.ValidateIdentifier(resolution.Schema) != nil {
		return "", false, false
	}
	return resolution.Schema, true, true
}

func (b *stageSchemaBinding) postgresObjectSchemas(token string) []string {
	if b == nil || b.postgres == nil || token == "" {
		return nil
	}
	schemas := map[string]bool{}
	if resolution, ok := b.postgres.UniformRelations[token]; ok {
		if resolution.Supported && resolution.Present {
			schemas[resolution.Schema] = true
		}
	}
	for key, resolution := range b.postgres.Relations {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == token && resolution.Supported && resolution.Present {
			schemas[resolution.Schema] = true
		}
	}
	out := make([]string, 0, len(schemas))
	for schema := range schemas {
		out = append(out, schema)
	}
	sort.Strings(out)
	return out
}

// postgresObjectSchemaAt resolves one table token for one exact statement
// ordinal. Grouped Provider objects can span several occurrences, so callers
// may only authorize the object when every occurrence resolves to the same
// physical schema.
func (b *stageSchemaBinding) postgresObjectSchemaAt(
	ordinal int32,
	token string,
) (string, bool, bool) {
	if ordinal <= 0 {
		return "", false, false
	}
	return b.postgresObjectSchema(ordinal, token)
}

// postgresRelationsUniformSchema verifies every frozen relation entry in
// b.postgres.Relations for token agrees on Supported, valid schema identity,
// and (expectedSchema, expectedPresent). Used when OrdinalRangesTruncated
// prevents scanning every statement ordinal from StatementOrdinalRanges alone.
func (b *stageSchemaBinding) postgresRelationsUniformSchema(
	token string,
	expectedSchema string,
	expectedPresent bool,
) bool {
	if b == nil || b.postgres == nil || !b.postgres.Complete || token == "" || expectedSchema == "" {
		return false
	}
	matched := 0
	for key, resolution := range b.postgres.Relations {
		_, tok, ok := strings.Cut(key, ":")
		if !ok || tok != token {
			continue
		}
		if !resolution.Supported {
			return false
		}
		var schema string
		if !resolution.Present {
			schema = b.postgres.Path.CreationSchema
		} else {
			schema = resolution.Schema
		}
		if schema == "" || database.ValidateIdentifier(schema) != nil {
			return false
		}
		if schema != expectedSchema || resolution.Present != expectedPresent {
			return false
		}
		matched++
	}
	return matched > 0
}

// postgresObjectSchemaForReference applies postgresObjectSchemaAt to every
// statement occurrence represented by an anonymized object reference.
func (b *stageSchemaBinding) postgresObjectSchemaForReference(
	object provider.AnonymizedObjectReference,
) (string, bool) {
	if b == nil || b.postgres == nil || !b.postgres.Complete || object.Token == "" {
		return "", false
	}
	if resolution, ok := b.postgres.UniformRelations[object.Token]; ok {
		if len(object.StatementOrdinalRanges) == 0 {
			if object.StatementOrdinal <= 0 {
				return "", false
			}
		} else {
			for _, span := range object.StatementOrdinalRanges {
				if span.Start <= 0 || span.End < span.Start {
					return "", false
				}
			}
		}
		if !resolution.Supported {
			return "", false
		}
		if !resolution.Present {
			if database.ValidateIdentifier(b.postgres.Path.CreationSchema) != nil {
				return "", false
			}
			return b.postgres.Path.CreationSchema, true
		}
		if database.ValidateIdentifier(resolution.Schema) != nil {
			return "", false
		}
		return resolution.Schema, true
	}
	schema := ""
	present := false
	seen := false
	if len(object.StatementOrdinalRanges) == 0 {
		if object.StatementOrdinal <= 0 {
			return "", false
		}
		next, nextPresent, complete := b.postgresObjectSchemaAt(object.StatementOrdinal, object.Token)
		if !complete || next == "" {
			return "", false
		}
		schema, present, seen = next, nextPresent, true
	} else {
		for _, span := range object.StatementOrdinalRanges {
			if span.Start <= 0 || span.End < span.Start {
				return "", false
			}
			for ordinal := span.Start; ordinal <= span.End; ordinal++ {
				next, nextPresent, complete := b.postgresObjectSchemaAt(ordinal, object.Token)
				if !complete || next == "" || (seen && (next != schema || nextPresent != present)) {
					return "", false
				}
				schema, present, seen = next, nextPresent, true
			}
		}
	}
	if !seen {
		return "", false
	}
	if object.OrdinalRangesTruncated && !b.postgresRelationsUniformSchema(object.Token, schema, present) {
		return "", false
	}
	return schema, true
}

func (b *stageSchemaBinding) postgresAllowsSchemaToken(mapper *privacy.RunMapper, token string) bool {
	if b == nil || b.postgres == nil || mapper == nil || token == "" {
		return false
	}
	kind, original, found := mapper.Original(token)
	if !found || kind != privacy.KindSchema || original == "" || database.ValidateIdentifier(original) != nil {
		return false
	}
	if strings.HasPrefix(original, "pg_temp_") {
		return false
	}
	for _, candidate := range b.postgres.Path.Effective {
		if candidate == original {
			return true
		}
	}
	return false
}

func postgresReferenceOrdinals(object provider.AnonymizedObjectReference) []int32 {
	seen := map[int32]bool{}
	out := make([]int32, 0)
	add := func(ordinal int32) {
		if ordinal > 0 && !seen[ordinal] {
			seen[ordinal] = true
			out = append(out, ordinal)
		}
	}
	if len(object.StatementOrdinalRanges) == 0 {
		add(object.StatementOrdinal)
	}
	for _, span := range object.StatementOrdinalRanges {
		if span.Start <= 0 || span.End < span.Start {
			return nil
		}
		for ordinal := span.Start; ordinal <= span.End && len(out) <= 16384; ordinal++ {
			add(ordinal)
		}
		if len(out) > 16384 {
			return nil
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (b *stageSchemaBinding) allPostgresSchemas() []string {
	if b == nil || b.postgres == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, schema := range b.postgres.Path.Explicit {
		if schema == "pg_catalog" || strings.HasPrefix(schema, "pg_temp_") || seen[schema] {
			continue
		}
		seen[schema] = true
		out = append(out, schema)
	}
	return out
}

// preparePostgresObjectBindings is a pure batch binder once the database
// adapter has supplied complete per-schema relation presence. It applies only
// parser-confirmed create/drop effects. Rename and parser-incomplete
// statements remain incomplete rather than being reduced to keyword guesses.
func preparePostgresObjectBindings(
	path FrozenPostgresSearchPath,
	statements []StatementValidityAtOrdinal,
	lookups []postgresRelationLookup,
	originalName func(token string) (string, bool),
	unsafeStatements ...map[int32]bool,
) (frozenPostgresPathBinding, bool) {
	return preparePostgresObjectBindingsWithCallables(
		path, statements, lookups, nil, false, originalName, unsafeStatements...,
	)
}

// preparePostgresObjectBindingsWithCallables binds relation identity and
// callable identity into a single frozen snapshot. Callable safety is derived
// from catalog facts and catalog-first frozen path order; when no
// such facts are supplied the snapshot stays partial instead of guessing from
// the schema name strings.
func preparePostgresObjectBindingsWithCallables(
	path FrozenPostgresSearchPath,
	statements []StatementValidityAtOrdinal,
	lookups []postgresRelationLookup,
	callables []database.PostgresCallableResolution,
	callablesChecked bool,
	originalName func(token string) (string, bool),
	unsafeStatements ...map[int32]bool,
) (frozenPostgresPathBinding, bool) {
	pathComplete := path.Validate() == nil && !path.HasTemporarySchema()
	out := frozenPostgresPathBinding{
		Version:       postgresObjectResolutionVersion,
		Path:          path,
		Complete:      pathComplete,
		CallablesSafe: postgresCallablesSafe(path, callables, callablesChecked),
		Baseline:      map[string]frozenPostgresBaselineRelation{},
		Relations:     map[string]frozenPostgresObjectBinding{},
	}
	if callablesChecked || len(callables) != 0 {
		out.CallablesChecked = callablesChecked
		out.Callables = make(map[string]frozenPostgresCallable, len(callables))
		out.CallablesRequested = make([]string, 0, len(callables))
		for _, resolution := range callables {
			key := database.PostgresCallablePayloadKey(
				resolution.Request.Kind, resolution.Request.Name,
			)
			entry := frozenPostgresCallable{
				Kind:    string(resolution.Request.Kind),
				Name:    resolution.Request.Name,
				Winners: make([]frozenPostgresCallableWinner, 0, len(resolution.Winners)),
			}
			for _, winner := range resolution.Winners {
				entry.Winners = append(entry.Winners, frozenPostgresCallableWinner{
					Schema:     winner.Schema,
					Signature:  winner.Signature,
					OID:        winner.OID,
					ResultType: winner.ResultType,
					PgCatalog:  winner.PgCatalog,
				})
			}
			out.Callables[key] = entry
			out.CallablesRequested = append(out.CallablesRequested, key)
		}
		sort.Strings(out.CallablesRequested)
	}
	if !out.Complete || len(statements) == 0 || len(statements) > 100000 {
		if len(statements) > 100000 {
			out.Complete = false
		}
		return out, false
	}
	pathSchemas := map[string]bool{}
	for _, schema := range path.Effective {
		pathSchemas[schema] = true
	}
	type ordinalTokenKey struct {
		ordinal int32
		token   string
	}
	lookupByKey := make(map[ordinalTokenKey]postgresRelationLookup, len(lookups))
	lookupsByOrdinal := make(map[int32][]postgresRelationLookup, len(statements))
	initialState := map[string]map[string]string{}
	initialOIDs := map[string]map[string]uint32{}
	checkedTokens := map[string]string{}
	for _, lookup := range lookups {
		if lookup.Ordinal <= 0 || lookup.TableToken == "" || !lookup.Complete ||
			lookup.TableName == "" || len(lookup.PresentBySchema) > postgresSearchPathMaxSchemas+3 {
			out.Complete = false
			return out, false
		}
		if originalName != nil {
			if cachedName, cached := checkedTokens[lookup.TableToken]; cached {
				if cachedName != lookup.TableName {
					out.Complete = false
					return out, false
				}
			} else {
				name, ok := originalName(lookup.TableToken)
				if !ok || name != lookup.TableName {
					out.Complete = false
					return out, false
				}
				checkedTokens[lookup.TableToken] = name
			}
		}
		key := ordinalTokenKey{ordinal: lookup.Ordinal, token: lookup.TableToken}
		if _, duplicate := lookupByKey[key]; duplicate {
			out.Complete = false
			return out, false
		}
		lookupByKey[key] = lookup
		lookupsByOrdinal[lookup.Ordinal] = append(lookupsByOrdinal[lookup.Ordinal], lookup)
		if prior, seen := initialState[lookup.TableName]; seen {
			priorOIDs := initialOIDs[lookup.TableName]
			if len(lookup.PresentBySchema) != len(prior) {
				out.Complete = false
				return out, false
			}
			for schema, kind := range lookup.PresentBySchema {
				if !pathSchemas[schema] || kind == "" || prior[schema] != kind {
					out.Complete = false
					return out, false
				}
				oid := lookup.OIDBySchema[schema]
				if oid == 0 || priorOIDs[schema] != oid {
					out.Complete = false
					return out, false
				}
			}
			continue
		}
		if len(initialState) >= postgresSearchPathMaxResolvedTables {
			out.Complete = false
			return out, false
		}
		presence := make(map[string]string, len(lookup.PresentBySchema))
		oids := make(map[string]uint32, len(lookup.PresentBySchema))
		for schema, kind := range lookup.PresentBySchema {
			if !pathSchemas[schema] || kind == "" {
				out.Complete = false
				return out, false
			}
			presence[schema] = kind
			oid := lookup.OIDBySchema[schema]
			if oid == 0 {
				out.Complete = false
				return out, false
			}
			oids[schema] = oid
		}
		initialState[lookup.TableName] = presence
		initialOIDs[lookup.TableName] = oids
	}
	for tableName, presence := range initialState {
		out.Baseline[tableName] = frozenPostgresBaselineRelation{
			PresentBySchema: clonePostgresPresence(presence),
			OIDBySchema:     clonePostgresOIDs(initialOIDs[tableName]),
		}
	}

	// table name -> schema -> relation kind after proven prior effects. State
	// is keyed by exact original name rather than Provider token because one
	// physical table can have several mapper tokens across scopes.
	virtualKind := map[string]map[string]string{}
	virtualOID := map[string]map[string]uint32{}
	virtualCreatedBy := map[string]map[string]int32{}
	for name, presence := range initialState {
		virtualKind[name] = clonePostgresPresence(presence)
		virtualOID[name] = clonePostgresOIDs(initialOIDs[name])
		virtualCreatedBy[name] = map[string]int32{}
	}
	useUniform := len(lookups) > 1024
	var (
		uniformCandidate     map[string]frozenPostgresObjectBinding
		uniformOrdinals      map[string][]int32
		nonUniformTokens     map[string]bool
		mutatedTables        map[string]bool
		uniformTokensByTable map[string][]string
	)
	if useUniform {
		uniformCandidate = make(map[string]frozenPostgresObjectBinding, len(initialState))
		uniformOrdinals = make(map[string][]int32, len(initialState))
		nonUniformTokens = make(map[string]bool)
		mutatedTables = make(map[string]bool)
		uniformTokensByTable = make(map[string][]string, len(initialState))
	}
	markTokenNonUniform := func(token string) {
		if !useUniform || token == "" || nonUniformTokens[token] {
			return
		}
		nonUniformTokens[token] = true
		cand, seen := uniformCandidate[token]
		if !seen {
			return
		}
		prevOrdinals := uniformOrdinals[token]
		delete(uniformOrdinals, token)
		delete(uniformCandidate, token)
		if len(out.Relations)+len(prevOrdinals) > postgresSearchPathMaxResolvedTables {
			out.Complete = false
			return
		}
		for _, prevOrd := range prevOrdinals {
			out.Relations[postgresObjectBindingKey(prevOrd, token)] = cand
		}
	}
	// Any table whose presence changes via CREATE/DROP (or RENAME) cannot remain
	// in UniformRelations even if never referenced again later in the batch,
	// because execution checks every UniformRelations entry at every DDL ordinal.
	markTableNonUniform := func(tableName string, triggerToken string) {
		if !useUniform {
			return
		}
		if tableName != "" {
			mutatedTables[tableName] = true
			for _, tok := range uniformTokensByTable[tableName] {
				markTokenNonUniform(tok)
			}
			delete(uniformTokensByTable, tableName)
		}
		markTokenNonUniform(triggerToken)
	}
	recordRelation := func(ordinal int32, token string, resolution frozenPostgresObjectBinding) {
		if !useUniform {
			out.Relations[postgresObjectBindingKey(ordinal, token)] = resolution
			return
		}
		if nonUniformTokens[token] || mutatedTables[resolution.TableName] {
			markTokenNonUniform(token)
			if !out.Complete || len(out.Relations) >= postgresSearchPathMaxResolvedTables {
				out.Complete = false
				return
			}
			out.Relations[postgresObjectBindingKey(ordinal, token)] = resolution
			return
		}
		cand, seen := uniformCandidate[token]
		if !seen {
			if resolution.CreatedByOrdinal == 0 {
				uniformCandidate[token] = resolution
				uniformOrdinals[token] = append(uniformOrdinals[token], ordinal)
				uniformTokensByTable[resolution.TableName] = append(uniformTokensByTable[resolution.TableName], token)
				return
			}
			nonUniformTokens[token] = true
			if len(out.Relations) >= postgresSearchPathMaxResolvedTables {
				out.Complete = false
				return
			}
			out.Relations[postgresObjectBindingKey(ordinal, token)] = resolution
			return
		}
		if resolution == cand {
			uniformOrdinals[token] = append(uniformOrdinals[token], ordinal)
			return
		}
		markTokenNonUniform(token)
		if !out.Complete || len(out.Relations) >= postgresSearchPathMaxResolvedTables {
			out.Complete = false
			return
		}
		out.Relations[postgresObjectBindingKey(ordinal, token)] = resolution
	}
	parserComplete := true
	unsafeByOrdinal := map[int32]bool{}
	if len(unsafeStatements) > 0 && unsafeStatements[0] != nil {
		unsafeByOrdinal = unsafeStatements[0]
	}
	for _, statement := range statements {
		facts := statement.Facts
		if !facts.Complete || facts.Unsupported != "" {
			parserComplete = false
		}
		pathUnsafe := unsafeByOrdinal[statement.Ordinal]
		if pathUnsafe {
			parserComplete = false
		}
		for _, lookup := range lookupsByOrdinal[statement.Ordinal] {
			presence := virtualKind[lookup.TableName]
			if presence == nil {
				// A replacement token may first appear in a rename. Rename
				// semantics remain intentionally partial until a complete
				// old/new identity transition is available.
				presence = clonePostgresPresence(lookup.PresentBySchema)
				virtualKind[lookup.TableName] = presence
				virtualOID[lookup.TableName] = clonePostgresOIDs(lookup.OIDBySchema)
				virtualCreatedBy[lookup.TableName] = map[string]int32{}
			}
			resolution := frozenPostgresObjectBinding{
				TableName: lookup.TableName,
				Supported: true,
			}
			if pathUnsafe {
				resolution.Supported = false
				recordRelation(lookup.Ordinal, lookup.TableToken, resolution)
				continue
			}
			targetSchema, explicit, targetKnown := statementTableTargetSchema(
				facts, lookup.TableToken, originalName,
			)
			if !targetKnown {
				resolution.Supported = false
				parserComplete = false
			} else if statementCreatesTable(facts, lookup.TableToken) {
				resolution.Explicit = explicit
				if !explicit {
					targetSchema = path.CreationSchema
				}
				if !pathSchemas[targetSchema] {
					resolution.Supported = false
					parserComplete = false
				} else {
					kind := presence[targetSchema]
					resolution.Schema = targetSchema
					resolution.Present = kind != ""
					resolution.Supported = kind == "" || kind == "table"
					resolution.Kind = kind
					resolution.OID = virtualOID[lookup.TableName][targetSchema]
					resolution.CreatedByOrdinal =
						virtualCreatedBy[lookup.TableName][targetSchema]
					if !resolution.Supported {
						out.Complete = false
					}
				}
			} else if explicit {
				resolution.Explicit = true
				if !pathSchemas[targetSchema] {
					resolution.Supported = false
					parserComplete = false
				} else {
					kind := presence[targetSchema]
					resolution.Schema = targetSchema
					resolution.Present = kind != ""
					resolution.Supported = kind == "" || kind == "table"
					resolution.Kind = kind
					resolution.OID = virtualOID[lookup.TableName][targetSchema]
					resolution.CreatedByOrdinal =
						virtualCreatedBy[lookup.TableName][targetSchema]
					if !resolution.Supported {
						out.Complete = false
					}
				}
			} else {
				for _, schema := range path.Effective {
					kind := presence[schema]
					if kind == "" {
						continue
					}
					resolution.Schema = schema
					resolution.Present = true
					resolution.Supported = kind == "table"
					resolution.Kind = kind
					resolution.OID = virtualOID[lookup.TableName][schema]
					resolution.CreatedByOrdinal =
						virtualCreatedBy[lookup.TableName][schema]
					if !resolution.Supported {
						out.Complete = false
					}
					break
				}
				if !resolution.Present {
					resolution.Schema = path.CreationSchema
				}
			}
			recordRelation(lookup.Ordinal, lookup.TableToken, resolution)
		}

		if pathUnsafe {
			continue
		}
		for _, effect := range facts.Effects {
			if effect.Object.Kind != fingerprint.ValidityObjectTable {
				if effect.Replacement != nil && effect.Replacement.Kind == fingerprint.ValidityObjectTable {
					parserComplete = false
				}
				continue
			}
			lookup, found := lookupByKey[ordinalTokenKey{
				ordinal: statement.Ordinal, token: effect.Object.Token,
			}]
			if !found {
				parserComplete = false
				continue
			}
			presence := virtualKind[lookup.TableName]
			if presence == nil {
				presence = clonePostgresPresence(lookup.PresentBySchema)
				virtualKind[lookup.TableName] = presence
				virtualOID[lookup.TableName] = clonePostgresOIDs(lookup.OIDBySchema)
				virtualCreatedBy[lookup.TableName] = map[string]int32{}
			}
			targetSchema, explicit, targetKnown := statementTableTargetSchema(
				facts, effect.Object.Token, originalName,
			)
			if !targetKnown {
				parserComplete = false
				continue
			}
			switch effect.Action {
			case fingerprint.ValidityEffectCreate:
				if !explicit {
					targetSchema = path.CreationSchema
				}
				if !pathSchemas[targetSchema] {
					parserComplete = false
					continue
				}
				kind := presence[targetSchema]
				if kind == "" {
					presence[targetSchema] = "table"
					delete(virtualOID[lookup.TableName], targetSchema)
					virtualCreatedBy[lookup.TableName][targetSchema] = statement.Ordinal
					markTableNonUniform(lookup.TableName, effect.Object.Token)
				}
				// CREATE TABLE IF NOT EXISTS is a no-op for an existing
				// relation. An existing non-table relation remains visible as
				// unsupported rather than being promoted to a table.
			case fingerprint.ValidityEffectDrop:
				if !explicit {
					targetSchema = path.CreationSchema
					for _, schema := range path.Effective {
						if presence[schema] != "" {
							targetSchema = schema
							break
						}
					}
				}
				if presence[targetSchema] == "table" {
					delete(presence, targetSchema)
					delete(virtualOID[lookup.TableName], targetSchema)
					delete(virtualCreatedBy[lookup.TableName], targetSchema)
					markTableNonUniform(lookup.TableName, effect.Object.Token)
				}
			case fingerprint.ValidityEffectRename:
				// A rename changes the future name-to-schema identity. The
				// current token graph does not carry an executable rename
				// closure for every relation kind, so later binding is
				// intentionally partial.
				markTableNonUniform(lookup.TableName, effect.Object.Token)
				parserComplete = false
			}
			virtualKind[lookup.TableName] = presence
		}
	}
	if useUniform {
		if len(uniformCandidate) > postgresSearchPathMaxResolvedTables ||
			len(out.Relations) > postgresSearchPathMaxResolvedTables {
			out.Complete = false
		} else if len(uniformCandidate) > 0 {
			out.UniformRelations = make(map[string]frozenPostgresObjectBinding, len(uniformCandidate))
			for token, cand := range uniformCandidate {
				out.UniformRelations[token] = cand
			}
		}
	}
	out.Complete = parserComplete && out.Complete
	return out, out.Complete
}

// postgresCallablesSafe requires catalog-first path order and catalog proof. With
// facts it requires every reachable signature to be won by pg_catalog. Without
// facts the only provable case is an effective path that consists solely of
// pg_catalog, because such a path cannot route an unqualified call elsewhere.
func postgresCallablesSafe(
	path FrozenPostgresSearchPath,
	callables []database.PostgresCallableResolution,
	checked bool,
) bool {
	if path.Validate() != nil || path.HasTemporarySchema() || !path.CallableLookupSafe() {
		return false
	}
	if !checked {
		return postgresPathCatalogOnly(path)
	}
	for _, resolution := range callables {
		// A name with no visible winner cannot be reached by the statement at
		// all; that absence is still frozen, and send time re-resolves the same
		// name, so a post-freeze definition surfaces as drift instead of a
		// silent pass. Only a winner outside pg_catalog is unsafe here.
		if resolution.Unsafe() {
			return false
		}
	}
	return true
}

// postgresPathCatalogOnly reports whether every non-temporary schema on the
// effective path is pg_catalog.
func postgresPathCatalogOnly(path FrozenPostgresSearchPath) bool {
	for _, schema := range path.Effective {
		if strings.HasPrefix(schema, "pg_temp_") {
			continue
		}
		if schema != "pg_catalog" {
			return false
		}
	}
	return true
}

func statementTableTargetSchema(
	facts fingerprint.StatementValidity,
	token string,
	originalName func(token string) (string, bool),
) (schema string, explicit, complete bool) {
	complete = true
	seenSchema := false
	sawUnqualified := false
	consider := func(object fingerprint.ValidityObject) {
		if object.Kind != fingerprint.ValidityObjectTable || object.Token != token {
			return
		}
		if object.SchemaToken == "" {
			sawUnqualified = true
			if seenSchema {
				complete = false
			}
			return
		}
		explicit = true
		if sawUnqualified {
			complete = false
		}
		if originalName == nil {
			complete = false
			return
		}
		name, ok := originalName(object.SchemaToken)
		if !ok || database.ValidateIdentifier(name) != nil {
			complete = false
			return
		}
		if seenSchema && schema != name {
			complete = false
			return
		}
		schema = name
		seenSchema = true
	}
	for _, requirement := range facts.Requirements {
		consider(requirement.Object)
	}
	for _, effect := range facts.Effects {
		consider(effect.Object)
		if effect.Replacement != nil {
			consider(*effect.Replacement)
		}
	}
	return schema, explicit, complete
}

func equalPostgresPresence(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for schema, kind := range left {
		if right[schema] != kind {
			return false
		}
	}
	return true
}

func equalPostgresOIDs(left, right map[string]uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for schema, oid := range left {
		if right[schema] != oid {
			return false
		}
	}
	return true
}

func clonePostgresPresence(source map[string]string) map[string]string {
	out := make(map[string]string, len(source))
	for schema, kind := range source {
		out[schema] = kind
	}
	return out
}

func clonePostgresOIDs(source map[string]uint32) map[string]uint32 {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]uint32, len(source))
	for schema, oid := range source {
		out[schema] = oid
	}
	return out
}

func statementCreatesTable(facts fingerprint.StatementValidity, token string) bool {
	for _, effect := range facts.Effects {
		if effect.Object.Kind == fingerprint.ValidityObjectTable &&
			effect.Object.Token == token &&
			effect.Action == fingerprint.ValidityEffectCreate {
			return true
		}
	}
	return false
}

func decodeFrozenPostgresPath(value any) (FrozenPostgresSearchPath, bool) {
	if value == nil {
		return FrozenPostgresSearchPath{}, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return FrozenPostgresSearchPath{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var path FrozenPostgresSearchPath
	if err := decoder.Decode(&path); err != nil || path.Validate() != nil {
		return FrozenPostgresSearchPath{}, false
	}
	return path, true
}

func decodeFrozenPostgresPathBinding(value any) (*frozenPostgresPathBinding, bool) {
	if value == nil {
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var binding frozenPostgresPathBinding
	if err := decoder.Decode(&binding); err != nil ||
		binding.Version != postgresObjectResolutionVersion ||
		binding.Path.Validate() != nil ||
		binding.Baseline == nil ||
		len(binding.Relations) > postgresSearchPathMaxResolvedTables ||
		len(binding.UniformRelations) > postgresSearchPathMaxResolvedTables ||
		!validFrozenPostgresCallables(binding) {
		return nil, false
	}
	pathSchemas := map[string]bool{}
	for _, schema := range binding.Path.Effective {
		pathSchemas[schema] = true
	}
	for tableName, baseline := range binding.Baseline {
		if database.ValidateIdentifier(tableName) != nil ||
			baseline.PresentBySchema == nil || !validPostgresBaseline(binding.Path, baseline) {
			return nil, false
		}
	}
	for token, relation := range binding.UniformRelations {
		if token == "" ||
			relation.CreatedByOrdinal != 0 ||
			database.ValidateIdentifier(relation.TableName) != nil ||
			(relation.Present && (database.ValidateIdentifier(relation.Schema) != nil ||
				!validPostgresRelationKind(relation.Kind) ||
				!pathSchemas[relation.Schema] ||
				relation.OID == 0)) ||
			(!relation.Present && database.ValidateIdentifier(relation.Schema) != nil) {
			return nil, false
		}
	}
	for key, relation := range binding.Relations {
		ordinalText, token, ok := strings.Cut(key, ":")
		ordinal, parseErr := strconv.ParseInt(ordinalText, 10, 32)
		if !ok || token == "" || parseErr != nil || ordinal <= 0 ||
			database.ValidateIdentifier(relation.TableName) != nil ||
			(relation.Present && (database.ValidateIdentifier(relation.Schema) != nil ||
				!validPostgresRelationKind(relation.Kind) ||
				!pathSchemas[relation.Schema] ||
				(relation.OID == 0 &&
					(relation.CreatedByOrdinal <= 0 ||
						relation.CreatedByOrdinal >= int32(ordinal))) ||
				(relation.OID != 0 && relation.CreatedByOrdinal != 0))) ||
			(!relation.Present && database.ValidateIdentifier(relation.Schema) != nil) {
			return nil, false
		}
		if _, overlap := binding.UniformRelations[token]; overlap {
			return nil, false
		}
	}
	return &binding, true
}

// validFrozenPostgresCallables re-derives callable safety from the stored
// catalog facts so a tampered or stale snapshot cannot claim safety it never
// proved.
func validFrozenPostgresCallables(binding frozenPostgresPathBinding) bool {
	if binding.CallablesSafe && !binding.CallablesChecked {
		return false
	}
	if !binding.CallablesChecked && len(binding.Callables) != 0 {
		return false
	}
	if len(binding.CallablesRequested) != len(binding.Callables) {
		return false
	}
	pathSchemas := map[string]bool{}
	for _, schema := range binding.Path.Effective {
		pathSchemas[schema] = true
	}
	requested := map[string]bool{}
	for _, key := range binding.CallablesRequested {
		if requested[key] {
			return false
		}
		requested[key] = true
	}
	safe := binding.CallablesChecked && !binding.Path.HasTemporarySchema() && binding.Path.CallableLookupSafe()
	for key, callable := range binding.Callables {
		kind := database.PostgresCallableKind(callable.Kind)
		if !requested[key] ||
			key != database.PostgresCallablePayloadKey(kind, callable.Name) ||
			(kind != database.PostgresCallableFunction &&
				kind != database.PostgresCallableOperator) ||
			database.ValidatePostgresCallableName(kind, callable.Name) != nil {
			return false
		}
		signatures := map[string]bool{}
		for _, winner := range callable.Winners {
			if database.ValidateIdentifier(winner.Schema) != nil ||
				!pathSchemas[winner.Schema] ||
				winner.PgCatalog != (winner.Schema == "pg_catalog") ||
				winner.Signature == "" || winner.ResultType == "" || winner.OID == 0 {
				return false
			}
			if signatures[winner.Signature] {
				return false
			}
			signatures[winner.Signature] = true
			if !winner.PgCatalog {
				safe = false
			}
		}
	}
	return safe == binding.CallablesSafe
}

func validPostgresBaseline(
	path FrozenPostgresSearchPath,
	baseline frozenPostgresBaselineRelation,
) bool {
	for schema, kind := range baseline.PresentBySchema {
		if !containsPostgresSchema(path.Effective, schema) ||
			!validPostgresRelationKind(kind) || baseline.OIDBySchema[schema] == 0 {
			return false
		}
	}
	if len(baseline.OIDBySchema) != len(baseline.PresentBySchema) {
		return false
	}
	for schema, oid := range baseline.OIDBySchema {
		if oid == 0 || baseline.PresentBySchema[schema] == "" {
			return false
		}
	}
	return true
}

// FrozenPostgresReviewContext is the immutable environment shared by the
// accepted review, submission and execution boundaries.
type FrozenPostgresReviewContext struct {
	Path    FrozenPostgresSearchPath
	Binding FrozenPostgresPathBinding
}

// DecodeFrozenPostgresReviewContext performs strict decoding of the two
// versioned values embedded in the encrypted review context.
func DecodeFrozenPostgresReviewContext(
	plain []byte,
) (FrozenPostgresReviewContext, bool) {
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()
	value := map[string]any{}
	if err := decoder.Decode(&value); err != nil {
		return FrozenPostgresReviewContext{}, false
	}
	path, pathOK := decodeFrozenPostgresPath(value[stagePostgresSearchPathKey])
	binding, bindingOK := decodeFrozenPostgresPathBinding(
		value[stagePostgresObjectBindingKey],
	)
	if !pathOK || !bindingOK || !path.Equal(binding.Path) {
		return FrozenPostgresReviewContext{}, false
	}
	return FrozenPostgresReviewContext{
		Path: path, Binding: *binding,
	}, true
}

func withFrozenPostgresBinding(
	value map[string]any,
	binding frozenPostgresPathBinding,
) (map[string]any, error) {
	if binding.Path.Validate() != nil ||
		binding.Version != postgresObjectResolutionVersion {
		return nil, errPostgresSearchPathInvalid
	}
	if _, ok := decodeFrozenPostgresPathBinding(binding); !ok {
		return nil, errPostgresSearchPathInvalid
	}
	pathRaw, err := json.Marshal(binding.Path)
	if err != nil {
		return nil, err
	}
	bindingRaw, err := json.Marshal(binding)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(value)+2)
	for key, item := range value {
		out[key] = item
	}
	out[stagePostgresSearchPathKey] = json.RawMessage(pathRaw)
	out[stagePostgresObjectBindingKey] = json.RawMessage(bindingRaw)
	return out, nil
}

func frozenPostgresBindingFromContext(
	c *crypto.Crypter,
	runID string,
	stagePosition int32,
	env crypto.Envelope,
) (*frozenPostgresPathBinding, bool) {
	value, err := openStageReviewContext(c, runID, stagePosition, env)
	if err != nil {
		return nil, false
	}
	path, pathOK := decodeFrozenPostgresPath(value[stagePostgresSearchPathKey])
	binding, bindingOK := decodeFrozenPostgresPathBinding(value[stagePostgresObjectBindingKey])
	if !pathOK || !bindingOK || !path.Equal(binding.Path) {
		return nil, false
	}
	return binding, true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
