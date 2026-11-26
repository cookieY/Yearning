// postgres_search_path_resolve.go holds postgres_search_path domain helpers and operations.
package application

import (
	"context"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"strings"
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

var (
	errPostgresSearchPathInvalid        = errors.New("review: postgres search_path snapshot invalid")
	errPostgresDisallowedExplicitSchema = errors.New("review: postgres explicit schema disallowed")
)

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
	ExplicitSchemas    []string                                  `json:"explicit_schemas,omitempty"`
	TargetSchema       string                                    `json:"target_schema,omitempty"`
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

func isBlockingSchemaFailureReason(reason string) bool {
	switch reason {
	case "datasource_database_required",
		"sql_schema_out_of_scope",
		"catalog_schema_missing",
		"catalog_permission_denied":
		return true
	default:
		return false
	}
}

func isBlockingPostgresContextStatus(status string) bool {
	switch status {
	case "sql_schema_out_of_scope", "catalog_schema_missing", "catalog_permission_denied":
		return true
	default:
		return false
	}
}
