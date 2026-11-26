// postgres_search_path_context.go holds postgres_search_path domain helpers and operations.
package application

import (
	"bytes"
	"encoding/json"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"sort"
	"strings"
)

func (b *frozenPostgresPathBinding) containsAllowedSchema(schema string) bool {
	if b == nil || schema == "" {
		return false
	}
	for _, candidate := range b.Path.Effective {
		if candidate == schema {
			return true
		}
	}
	for _, candidate := range b.ExplicitSchemas {
		if candidate == schema {
			return true
		}
	}
	return false
}

func postgresAllowedSchemaSet(effective, extra []string) map[string]bool {
	out := make(map[string]bool)
	for _, schema := range effective {
		out[schema] = true
	}
	for _, schema := range extra {
		out[schema] = true
	}
	return out
}

func (b *stageSchemaBinding) postgresAllowsSchemaToken(mapper *privacy.RunMapper, token string) bool {
	if b == nil || b.postgres == nil || mapper == nil || token == "" {
		return false
	}
	kind, original, found := mapper.Original(token)
	if !found || kind != privacy.KindSchema || original == "" || database.ValidateIdentifier(original) != nil {
		return false
	}
	if !strings.EqualFold(original, "pg_catalog") && database.IsDisallowedPostgresSystemSchema(original) {
		return false
	}
	return b.postgres.containsAllowedSchema(original)
}

func (b *stageSchemaBinding) allPostgresSchemas() []string {
	if b == nil || b.postgres == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	appendSchema := func(schema string) {
		if database.IsDisallowedPostgresSystemSchema(schema) || seen[schema] {
			return
		}
		seen[schema] = true
		out = append(out, schema)
	}
	for _, schema := range b.postgres.Path.Explicit {
		appendSchema(schema)
	}
	for _, schema := range b.postgres.ExplicitSchemas {
		appendSchema(schema)
	}
	return out
}

func normalizePostgresExplicitSchemas(path FrozenPostgresSearchPath, extra []string) ([]string, bool) {
	if len(extra) == 0 {
		return nil, true
	}
	effectiveSet := make(map[string]bool, len(path.Effective))
	for _, schema := range path.Effective {
		effectiveSet[schema] = true
	}
	seen := make(map[string]bool, len(extra))
	out := make([]string, 0, len(extra))
	for _, schema := range extra {
		if !isValidPostgresExplicitSchema(schema) {
			return nil, false
		}
		if effectiveSet[schema] || seen[schema] {
			continue
		}
		seen[schema] = true
		out = append(out, schema)
	}
	if len(path.Effective)+len(out) > postgresSearchPathMaxSchemas+3 {
		return nil, false
	}
	if len(out) == 0 {
		return nil, true
	}
	sort.Strings(out)
	return out, true
}

func validPostgresTargetSchemaForPath(path FrozenPostgresSearchPath, targetSchema string) bool {
	if targetSchema == "" {
		return true
	}
	return isValidPostgresExplicitSchema(targetSchema) &&
		path.CreationSchema == targetSchema &&
		equalStrings(path.Explicit, []string{targetSchema}) &&
		equalStrings(path.Effective, []string{"pg_catalog", targetSchema})
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

func validPostgresBaselineForSchemas(
	allowedSchemas map[string]bool,
	baseline frozenPostgresBaselineRelation,
) bool {
	for schema, kind := range baseline.PresentBySchema {
		if !allowedSchemas[schema] ||
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

func postgresUnavailableReasonFromValue(value map[string]any) string {
	if status, ok := value[stagePostgresSearchPathStatusKey].(string); ok && isBlockingPostgresContextStatus(status) {
		return status
	}
	return "postgres_search_path_unavailable"
}

func openFrozenPostgresBindingFromContext(
	c *crypto.Crypter,
	runID string,
	stagePosition int32,
	env crypto.Envelope,
) (*frozenPostgresPathBinding, string) {
	value, err := openStageReviewContext(c, runID, stagePosition, env)
	if err != nil {
		return nil, "postgres_search_path_unavailable"
	}
	reason := postgresUnavailableReasonFromValue(value)
	path, pathOK := decodeFrozenPostgresPath(value[stagePostgresSearchPathKey])
	binding, bindingOK := decodeFrozenPostgresPathBinding(value[stagePostgresObjectBindingKey])
	if !pathOK || !bindingOK || !path.Equal(binding.Path) {
		return nil, reason
	}
	return binding, reason
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
