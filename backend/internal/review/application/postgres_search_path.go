package application

import (
	"bytes"
	"encoding/json"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"sort"
	"strconv"
	"strings"
)

type postgresRelationRequest struct {
	Ordinal    int32
	TableToken string
	TableName  string
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
	return preparePostgresObjectBindingsWithOptions(
		path, statements, lookups, nil, false, nil, "", originalName, unsafeStatements...,
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
	return preparePostgresObjectBindingsWithOptions(
		path, statements, lookups, callables, callablesChecked, nil, "", originalName, unsafeStatements...,
	)
}

func preparePostgresObjectBindingsWithOptions(
	path FrozenPostgresSearchPath,
	statements []StatementValidityAtOrdinal,
	lookups []postgresRelationLookup,
	callables []database.PostgresCallableResolution,
	callablesChecked bool,
	explicitSchemas []string,
	targetSchema string,
	originalName func(token string) (string, bool),
	unsafeStatements ...map[int32]bool,
) (frozenPostgresPathBinding, bool) {
	normalizedExplicit, explicitOK := normalizePostgresExplicitSchemas(path, explicitSchemas)
	pathComplete := path.Validate() == nil && !path.HasTemporarySchema() &&
		explicitOK && validPostgresTargetSchemaForPath(path, targetSchema)
	out := frozenPostgresPathBinding{
		Version:         postgresObjectResolutionVersion,
		Path:            path,
		Complete:        pathComplete,
		CallablesSafe:   postgresCallablesSafe(path, callables, callablesChecked),
		ExplicitSchemas: normalizedExplicit,
		TargetSchema:    targetSchema,
		Baseline:        map[string]frozenPostgresBaselineRelation{},
		Relations:       map[string]frozenPostgresObjectBinding{},
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
	allowedSchemas := postgresAllowedSchemaSet(path.Effective, normalizedExplicit)
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
				if !allowedSchemas[schema] || kind == "" || prior[schema] != kind {
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
			if !allowedSchemas[schema] || kind == "" {
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
			} else if statementCreatesTable(facts, lookup.TableToken) || explicit {
				resolution.Explicit = explicit
				if !explicit {
					targetSchema = path.CreationSchema
				}
				if !allowedSchemas[targetSchema] {
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
				if !allowedSchemas[targetSchema] {
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
				} else if !allowedSchemas[targetSchema] {
					parserComplete = false
					continue
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
	pathSchemas := postgresAllowedSchemaSet(binding.Path.Effective, nil)
	if !validPostgresTargetSchemaForPath(binding.Path, binding.TargetSchema) {
		return nil, false
	}
	explicitSchemas := map[string]bool{}
	for _, schema := range binding.ExplicitSchemas {
		if !isValidPostgresExplicitSchema(schema) || pathSchemas[schema] || explicitSchemas[schema] {
			return nil, false
		}
		explicitSchemas[schema] = true
	}
	if len(binding.Path.Effective)+len(binding.ExplicitSchemas) > postgresSearchPathMaxSchemas+3 {
		return nil, false
	}
	allowedSchemas := postgresAllowedSchemaSet(binding.Path.Effective, binding.ExplicitSchemas)
	relationSchemaAllowed := func(relation frozenPostgresObjectBinding) bool {
		if relation.Explicit {
			return allowedSchemas[relation.Schema]
		}
		return pathSchemas[relation.Schema]
	}
	for tableName, baseline := range binding.Baseline {
		if database.ValidateIdentifier(tableName) != nil ||
			baseline.PresentBySchema == nil || !validPostgresBaselineForSchemas(allowedSchemas, baseline) {
			return nil, false
		}
	}
	for token, relation := range binding.UniformRelations {
		if token == "" ||
			relation.CreatedByOrdinal != 0 ||
			database.ValidateIdentifier(relation.TableName) != nil ||
			(relation.Present && (database.ValidateIdentifier(relation.Schema) != nil ||
				!validPostgresRelationKind(relation.Kind) ||
				!relationSchemaAllowed(relation) ||
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
				!relationSchemaAllowed(relation) ||
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

func validPostgresBaseline(
	path FrozenPostgresSearchPath,
	baseline frozenPostgresBaselineRelation,
) bool {
	pathSchemas := make(map[string]bool, len(path.Effective))
	for _, schema := range path.Effective {
		pathSchemas[schema] = true
	}
	return validPostgresBaselineForSchemas(pathSchemas, baseline)
}

func frozenPostgresBindingFromContext(
	c *crypto.Crypter,
	runID string,
	stagePosition int32,
	env crypto.Envelope,
) (*frozenPostgresPathBinding, bool) {
	binding, _ := openFrozenPostgresBindingFromContext(c, runID, stagePosition, env)
	return binding, binding != nil
}

func postgresUnavailableReasonFromContext(
	c *crypto.Crypter,
	runID string,
	stagePosition int32,
	env crypto.Envelope,
) string {
	_, reason := openFrozenPostgresBindingFromContext(c, runID, stagePosition, env)
	return reason
}
