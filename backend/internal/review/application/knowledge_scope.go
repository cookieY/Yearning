package application

import (
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// resolveKnowledgeScope is the pure identity resolver used by the freeze path
// and focused tests. Grouping objects are the persisted SQL object graph, not
// a regex over plaintext SQL. Tokens are resolved inside the trusted process;
// only physical identities enter the locally evaluated knowledge matcher.
func resolveKnowledgeScope(
	datasourceID string,
	groups []provider.AnonymizedGroup,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) (knowledge.TouchedObjects, bool, error) {
	scope := knowledge.TouchedObjects{
		DatasourceID:  datasourceID,
		PreciseScopes: true,
		ScopeComplete: true,
		Schemas:       []knowledge.TouchedSchema{},
		Objects:       []knowledge.TouchedObject{},
	}
	if binding == nil || !binding.defensible || mapper == nil {
		scope.ScopeComplete = false
		return scope, false, nil
	}
	if binding.postgres != nil {
		return resolvePostgresKnowledgeScope(datasourceID, groups, binding, mapper, scope)
	}
	pairs := map[string]privacy.MappingPair{}
	for _, pair := range mapper.Snapshot() {
		pairs[pair.Token] = pair
	}

	for _, group := range groups {
		if group.RepresentativeAnonymizedSQL == "" || !group.MetadataContextComplete {
			scope.ScopeComplete = false
		}
		for _, object := range group.Objects {
			switch object.Kind {
			case string(privacy.KindSchema):
				schemaPair, ok := pairs[object.Token]
				if !ok || schemaPair.Kind != privacy.KindSchema {
					scope.ScopeComplete = false
					continue
				}
				physical, ok := binding.explicitPhysicalSchema(schemaPair.Original)
				if !ok {
					scope.ScopeComplete = false
					continue
				}
				appendTouchedSchema(&scope.Schemas, knowledge.TouchedSchema{
					Name:          physical,
					CaseSensitive: mappingScopeCaseSensitive(schemaPair.Scope),
				})
			case string(privacy.KindTable):
				tablePair, ok := pairs[object.Token]
				if !ok || tablePair.Kind != privacy.KindTable {
					scope.ScopeComplete = false
					continue
				}
				schemaName, schemaCaseSensitive, ok :=
					resolveKnowledgeSchema(object.SchemaToken, binding, pairs)
				if !ok {
					scope.ScopeComplete = false
					continue
				}
				scope.Objects = append(scope.Objects, knowledge.TouchedObject{
					SchemaName:          schemaName,
					TableName:           tablePair.Original,
					SchemaCaseSensitive: schemaCaseSensitive,
					TableCaseSensitive:  mappingScopeCaseSensitive(tablePair.Scope),
				})
				appendTouchedSchema(&scope.Schemas, knowledge.TouchedSchema{
					Name: schemaName, CaseSensitive: schemaCaseSensitive,
				})
			}
		}
	}

	// A stage with no object reference still has one defensible database
	// target only when the frozen mapping has exactly one logical default.
	if len(scope.Schemas) == 0 {
		if logical := binding.defaultSchema(); logical != "" {
			if physical, ok := binding.physicalSchema(logical); ok {
				appendTouchedSchema(&scope.Schemas, knowledge.TouchedSchema{
					Name: physical, CaseSensitive: true,
				})
			}
		}
	}
	if len(groups) == 0 || scope.ScopeComplete && len(scope.Schemas) == 0 {
		scope.ScopeComplete = false
	}
	return scope, scope.ScopeComplete, nil
}

func resolvePostgresKnowledgeScope(
	datasourceID string,
	groups []provider.AnonymizedGroup,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
	scope knowledge.TouchedObjects,
) (knowledge.TouchedObjects, bool, error) {
	pairs := map[string]privacy.MappingPair{}
	for _, pair := range mapper.Snapshot() {
		pairs[pair.Token] = pair
	}
	for _, group := range groups {
		if group.RepresentativeAnonymizedSQL == "" || !group.MetadataContextComplete {
			scope.ScopeComplete = false
		}
		for _, object := range group.Objects {
			switch object.Kind {
			case string(privacy.KindSchema):
				pair, ok := pairs[object.Token]
				if !ok || pair.Kind != privacy.KindSchema ||
					!binding.allowsPostgresLookupSchema(pair.Original) {
					scope.ScopeComplete = false
					continue
				}
				appendTouchedSchema(&scope.Schemas, knowledge.TouchedSchema{
					Name:          pair.Original,
					CaseSensitive: mappingScopeCaseSensitive(pair.Scope),
				})
			case string(privacy.KindTable):
				tablePair, ok := pairs[object.Token]
				if !ok || tablePair.Kind != privacy.KindTable {
					scope.ScopeComplete = false
					continue
				}
				schema, ok := binding.postgresObjectSchemaForReference(object)
				if !ok || database.ValidateIdentifier(schema) != nil {
					scope.ScopeComplete = false
					continue
				}
				scope.Objects = append(scope.Objects, knowledge.TouchedObject{
					SchemaName:          schema,
					TableName:           tablePair.Original,
					SchemaCaseSensitive: true,
					TableCaseSensitive:  mappingScopeCaseSensitive(tablePair.Scope),
				})
				appendTouchedSchema(&scope.Schemas, knowledge.TouchedSchema{
					Name: schema, CaseSensitive: true,
				})
			}
		}
	}
	if len(scope.Schemas) == 0 {
		appendTouchedSchema(&scope.Schemas, knowledge.TouchedSchema{
			Name: binding.postgres.Path.CreationSchema, CaseSensitive: true,
		})
	}
	if len(groups) == 0 {
		scope.ScopeComplete = false
	}
	return scope, scope.ScopeComplete, nil
}

func resolveKnowledgeSchema(
	token string,
	binding *stageSchemaBinding,
	pairs map[string]privacy.MappingPair,
) (string, bool, bool) {
	if token == "" {
		logical := binding.defaultSchema()
		if logical == "" {
			return "", false, false
		}
		physical, ok := binding.physicalSchema(logical)
		// The default's physical target comes from the trusted frozen map and
		// is therefore an exact configuration identifier.
		return physical, true, ok
	}
	pair, ok := pairs[token]
	if !ok || pair.Kind != privacy.KindSchema {
		return "", false, false
	}
	physical, ok := binding.explicitPhysicalSchema(pair.Original)
	if !ok {
		return "", false, false
	}
	return physical, mappingScopeCaseSensitive(pair.Scope), true
}

func mappingScopeCaseSensitive(scope string) bool {
	return scope == privacy.ScopeCaseSensitive
}

func appendTouchedSchema(dst *[]knowledge.TouchedSchema, candidate knowledge.TouchedSchema) {
	for _, current := range *dst {
		if current.CaseSensitive == candidate.CaseSensitive &&
			current.Name == candidate.Name {
			return
		}
		if !current.CaseSensitive && !candidate.CaseSensitive &&
			current.Name == candidate.Name {
			return
		}
	}
	*dst = append(*dst, candidate)
}
