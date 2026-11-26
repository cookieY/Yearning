package application

import (
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// metadataScope records only identities that are valid for model tool use.
// objects and schemas allow table-scoped and schema-only tools; groups gates
// safe_explain because its arguments contain a group ID instead of a table.
type metadataScope struct {
	objects map[string]map[string]bool
	schemas map[string]bool
	groups  map[string]bool
}

func newMetadataScope() metadataScope {
	return metadataScope{
		objects: map[string]map[string]bool{},
		schemas: map[string]bool{},
		groups:  map[string]bool{},
	}
}

func metadataTokenLegend(initial []agent.TokenLegendEntry, groups []provider.AnonymizedGroup) []agent.TokenLegendEntry {
	out := append([]agent.TokenLegendEntry{}, initial...)
	seen := map[string]bool{}
	for _, item := range out {
		seen[item.Token] = true
	}
	add := func(token, kind string) {
		if token != "" && !seen[token] {
			out = append(out, agent.TokenLegendEntry{Token: token, Kind: kind})
			seen[token] = true
		}
	}
	for _, group := range groups {
		for _, object := range group.Objects {
			add(object.Token, object.Kind)
			add(object.SchemaToken, "schema")
		}
	}
	return out
}

// bindGroupMetadata assigns the unique stage default logical schema to
// unqualified references. Execution applies that schema's physical target only
// as the connection default and does not rewrite SQL qualifiers. Consequently,
// an explicitly qualified reference is eligible only when the SQL token names
// an allowed physical schema; an explicit logical alias makes context
// incomplete and leaves the object outside tool scope.
func bindGroupMetadata(groups []provider.AnonymizedGroup, binding *stageSchemaBinding, mapper *privacy.RunMapper) ([]provider.AnonymizedGroup, metadataScope, bool) {
	bound, scope, complete, _ := bindGroupMetadataWithReason(groups, binding, mapper)
	return bound, scope, complete
}

// bindGroupMetadataWithReason behaves identically to bindGroupMetadata while also
// returning a privacy-safe failure reason code when metadata context is incomplete.
func bindGroupMetadataWithReason(groups []provider.AnonymizedGroup, binding *stageSchemaBinding, mapper *privacy.RunMapper) ([]provider.AnonymizedGroup, metadataScope, bool, string) {
	if binding != nil && binding.postgres != nil {
		return bindPostgresGroupMetadataWithReason(groups, binding, mapper)
	}
	var defaultSchema string
	if logical := binding.defaultSchema(); logical != "" && mapper != nil {
		if tok := mapper.LookupScoped(privacy.KindSchema, privacy.ScopeCaseSensitive, logical); tok != "" {
			defaultSchema = tok
		} else {
			defaultSchema, _ = mapper.Token(privacy.KindSchema, logical)
		}
	}
	out := make([]provider.AnonymizedGroup, len(groups))
	scope := newMetadataScope()
	complete := binding == nil || binding.defensible
	hasExplicitOutOfScope := false
	hasUnqualifiedMissingDefault := false
	for i, group := range groups {
		out[i] = group
		out[i].Objects = append([]provider.AnonymizedObjectReference{}, group.Objects...)
		groupComplete := group.RepresentativeAnonymizedSQL != "" && group.MetadataContextComplete
		if !groupComplete {
			complete = false
		}
		for j, object := range out[i].Objects {
			if object.Kind == "alias" || object.Kind == "cte" {
				continue
			}
			if object.Kind == "schema" {
				schemaTok := object.SchemaToken
				if schemaTok == "" {
					schemaTok = object.Token
				}
				schemaAllowed := schemaTok != "" && stageExplicitPhysicalSchemaToken(binding, mapper, schemaTok)
				if object.Relation != "reference" || !schemaAllowed {
					if !schemaAllowed && binding != nil && binding.defensible {
						hasExplicitOutOfScope = true
					}
					groupComplete = false
					out[i].MetadataContextComplete = false
				} else {
					object.SchemaToken = schemaTok
					out[i].Objects[j] = object
					scope.schemas[schemaTok] = true
				}
				continue
			}
			if object.Kind == "column" && object.SchemaToken == "" && defaultSchema == "" {
				// CTE/derived/multi-table columns without an explicit schema rely on the enclosing
				// table/CTE references; physical tables strictly enforce schema qualification below.
				continue
			}
			explicitSchema := object.SchemaToken != ""
			if !explicitSchema {
				object.SchemaToken = defaultSchema
				out[i].Objects[j] = object
			}
			schemaAllowed := false
			if object.SchemaToken != "" {
				if explicitSchema {
					schemaAllowed = stageExplicitPhysicalSchemaToken(binding, mapper, object.SchemaToken)
				} else {
					schemaAllowed = object.SchemaToken == defaultSchema
				}
			}
			if !schemaAllowed {
				if binding != nil && binding.defensible {
					if explicitSchema {
						hasExplicitOutOfScope = true
					} else if defaultSchema == "" {
						hasUnqualifiedMissingDefault = true
					}
				}
				groupComplete = false
				out[i].MetadataContextComplete = false
			} else {
				scope.schemas[object.SchemaToken] = true
			}
			// A constraint without define relation or standalone index without an owning table
			// lacks complete safe catalog facts; do not let it bypass table coverage.
			unsupported := (object.Kind == "constraint" && object.Relation != "define") ||
				(object.Kind == "index" && (object.ParentToken == "" || object.SchemaToken == ""))
			if unsupported {
				groupComplete = false
				out[i].MetadataContextComplete = false
			}
			if object.Kind != "table" {
				continue
			}
			if object.SchemaToken == "" {
				groupComplete = false
				out[i].MetadataContextComplete = false
				continue
			}
			if schemaAllowed {
				if scope.objects[object.SchemaToken] == nil {
					scope.objects[object.SchemaToken] = map[string]bool{}
				}
				scope.objects[object.SchemaToken][object.Token] = true
			}
		}
		if groupComplete {
			if group.GroupID != "" {
				scope.groups[group.GroupID] = true
			}
		} else {
			complete = false
		}
	}
	reason := ""
	if !complete {
		if binding != nil && !binding.defensible && binding.unavailableReason != "" {
			reason = binding.unavailableReason
		} else if hasExplicitOutOfScope {
			reason = "sql_schema_out_of_scope"
		} else if hasUnqualifiedMissingDefault {
			reason = "datasource_database_required"
		}
	}
	return out, scope, complete, reason
}

func bindPostgresGroupMetadata(
	groups []provider.AnonymizedGroup,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) ([]provider.AnonymizedGroup, metadataScope, bool) {
	out, scope, complete, _ := bindPostgresGroupMetadataWithReason(groups, binding, mapper)
	return out, scope, complete
}

func bindPostgresGroupMetadataWithReason(
	groups []provider.AnonymizedGroup,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) ([]provider.AnonymizedGroup, metadataScope, bool, string) {
	out := make([]provider.AnonymizedGroup, len(groups))
	scope := newMetadataScope()
	complete := binding != nil && binding.defensible && mapper != nil
	hasExplicitOutOfScope := false
	for i, group := range groups {
		out[i] = group
		out[i].Objects = append([]provider.AnonymizedObjectReference{}, group.Objects...)
		groupComplete := group.RepresentativeAnonymizedSQL != "" && group.MetadataContextComplete
		objectSchemas := map[string]bool{}
		tableSchemaByToken := map[string]string{}
		for j, object := range out[i].Objects {
			switch object.Kind {
			case "table":
				schema, ok := binding.postgresObjectSchemaForReference(object)
				if !ok {
					if binding != nil && binding.defensible && object.SchemaToken != "" && !binding.postgresAllowsSchemaToken(mapper, object.SchemaToken) {
						hasExplicitOutOfScope = true
					}
					groupComplete = false
					continue
				}
				schemaToken := mapper.LookupScoped(privacy.KindSchema, privacy.ScopeCaseSensitive, schema)
				if schemaToken == "" {
					var err error
					schemaToken, err = mapper.Token(privacy.KindSchema, schema)
					if err != nil {
						groupComplete = false
						continue
					}
				}
				object.SchemaToken = schemaToken
				out[i].Objects[j] = object
				tableSchemaByToken[object.Token] = schemaToken
				objectSchemas[schemaToken] = true
				if scope.objects[schemaToken] == nil {
					scope.objects[schemaToken] = map[string]bool{}
				}
				scope.objects[schemaToken][object.Token] = true
			case "schema":
				if object.Relation != "reference" {
					// Standalone schema discovery or DDL targets have no complete catalog fact.
					groupComplete = false
					continue
				}
				if !binding.postgresAllowsSchemaToken(mapper, object.Token) {
					if binding != nil && binding.defensible {
						hasExplicitOutOfScope = true
					}
					groupComplete = false
					continue
				}
				objectSchemas[object.Token] = true
			}
		}
		for j, object := range out[i].Objects {
			switch object.Kind {
			case "index":
				parentSchema := ""
				if object.ParentToken != "" {
					parentSchema = tableSchemaByToken[object.ParentToken]
				}
				if object.ParentToken == "" || parentSchema == "" || (object.SchemaToken != "" && object.SchemaToken != parentSchema) {
					groupComplete = false
					continue
				}
				object.SchemaToken = parentSchema
				out[i].Objects[j] = object
				objectSchemas[parentSchema] = true
			case "constraint":
				if object.Relation != "define" || (object.ParentToken != "" && tableSchemaByToken[object.ParentToken] == "") {
					groupComplete = false
				}
			}
		}
		if len(out[i].Objects) == 0 && binding != nil && binding.postgres != nil {
			schema := binding.postgres.Path.CreationSchema
			if schemaToken := mapper.LookupScoped(privacy.KindSchema, privacy.ScopeCaseSensitive, schema); schemaToken != "" {
				objectSchemas[schemaToken] = true
			} else if schemaToken, err := mapper.Token(privacy.KindSchema, schema); err == nil {
				objectSchemas[schemaToken] = true
			} else {
				groupComplete = false
			}
		}
		for schemaToken := range objectSchemas {
			scope.schemas[schemaToken] = true
		}
		if groupComplete {
			if group.GroupID != "" {
				scope.groups[group.GroupID] = true
			}
		} else {
			out[i].MetadataContextComplete = false
			complete = false
		}
	}
	if len(groups) == 0 {
		complete = false
	}
	reason := ""
	if !complete {
		if binding != nil && !binding.defensible && binding.unavailableReason != "" {
			reason = binding.unavailableReason
		} else if hasExplicitOutOfScope {
			reason = "sql_schema_out_of_scope"
		}
	}
	return out, scope, complete, reason
}

func stageExplicitPhysicalSchemaToken(
	binding *stageSchemaBinding, mapper *privacy.RunMapper, token string,
) bool {
	if binding == nil || mapper == nil {
		return false
	}
	kind, original, found := mapper.Original(token)
	if !found || kind != privacy.KindSchema {
		return false
	}
	_, ok := binding.explicitPhysicalSchema(original)
	return ok
}

// Index operations require an observed index profile in addition to the table
// existence baseline. The full profile proves presence or absence of the
// SQL-referenced index; its business implications remain the model's decision.
func metadataAdditionalFactsObserved(groups []provider.AnonymizedGroup, evidence []provider.NormalizedEvidenceRef) bool {
	for _, group := range groups {
		for _, object := range group.Objects {
			if object.Kind != "index" {
				continue
			}
			observed := false
			for _, ref := range evidence {
				fact := ref.Fact
				if ref.FactKind == "index_metadata" && fact["schema"] == object.SchemaToken && fact["table"] == object.ParentToken &&
					fact["metadata_complete"] == true && metadataFactComplete(fact) &&
					(fact["existence"] == "present" || fact["existence"] == "absent") &&
					metadataCollectionPresent(fact["indexes"]) {
					observed = true
					break
				}
			}
			if !observed {
				return false
			}
		}
	}
	return true
}

func (s metadataScope) allows(args map[string]any) bool {
	if groupID, _ := args["group_id"].(string); groupID != "" {
		if !s.groups[groupID] {
			return false
		}
		if schema, _ := args["schema"].(string); schema != "" {
			return s.schemas[schema]
		}
		return true
	}
	schema, _ := args["schema"].(string)
	table, _ := args["table"].(string)
	if table != "" {
		return s.objects[schema][table]
	}
	if schema != "" {
		return s.schemas[schema]
	}
	return true
}

func (s metadataScope) observed(evidence []provider.NormalizedEvidenceRef) bool {
	seen := newMetadataScope()
	for _, e := range evidence {
		if e.FactKind != "schema_metadata" {
			continue
		}
		fact := e.Fact
		if fact["metadata_complete"] != true || !metadataFactComplete(fact) ||
			(fact["existence"] != "present" && fact["existence"] != "absent") ||
			!metadataCollectionPresent(fact["columns"]) {
			continue
		}
		schema, _ := fact["schema"].(string)
		table, _ := fact["table"].(string)
		if schema == "" || table == "" {
			continue
		}
		if seen.objects[schema] == nil {
			seen.objects[schema] = map[string]bool{}
		}
		seen.objects[schema][table] = true
	}
	for schema, tables := range s.objects {
		for table := range tables {
			if !seen.objects[schema][table] {
				return false
			}
		}
	}
	return true
}

func metadataCollectionPresent(value any) bool {
	if value == nil {
		return false
	}
	switch value.(type) {
	case []any:
		return true
	case []map[string]any:
		return true
	default:
		return false
	}
}

// knownToolIdentifiers rejects guessed raw names and tokens of the wrong kind.
func knownToolIdentifiers(args map[string]any, mapper *privacy.RunMapper) bool {
	for key, want := range map[string]privacy.IdentifierKind{"schema": privacy.KindSchema, "table": privacy.KindTable} {
		value, present := args[key]
		if !present {
			continue
		}
		token, ok := value.(string)
		if !ok || mapper == nil {
			return false
		}
		kind, _, found := mapper.Original(token)
		if !found || kind != want {
			return false
		}
	}
	return true
}

func factMatchesRequestedScope(name string, fact, args map[string]any) bool {
	if name != "schema_metadata" && name != "table_statistics" && name != "index_metadata" {
		return true
	}
	for _, key := range []string{"schema", "table"} {
		if value, supplied := args[key]; supplied && fact[key] != value {
			return false
		}
	}
	return true
}

// metadataFactComplete checks technical availability, never business risk.
// Requested unknown or truncated facts cannot be treated as observed evidence.
func metadataFactComplete(v any) bool {
	switch value := v.(type) {
	case string:
		return value != "__truncated__"
	case map[string]any:
		for key, child := range value {
			switch key {
			case "__truncated__", "truncated":
				if child == true {
					return false
				}
			case "metadata_complete":
				if child != true {
					return false
				}
			case "existence":
				if child != "present" && child != "absent" {
					return false
				}
			case "status", "row_estimate_status", "type_status":
				if child != "observed" {
					return false
				}
			}
			if !metadataFactComplete(child) {
				return false
			}
		}
	case []any:
		for _, child := range value {
			if !metadataFactComplete(child) {
				return false
			}
		}
	}
	return true
}
