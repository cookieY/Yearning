package application

import (
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// bindValidityProgram turns the SQL object graph into canonical runtime schema
// identities without weakening execution binding. Every authorized object is
// keyed by an anonymous token for the physical schema that execution will use.
// An unqualified object uses the one logical default schema; an explicitly
// qualified object must already name an allowed physical schema. A logical
// alias cannot be treated as its physical target because explicit SQL
// qualifiers are not rewritten. The input program is never mutated.
type validityMemoKey struct {
	complete            bool
	definitionsComplete bool
	operation           fingerprint.ValidityOperation
	unsupported         string
	reqPtr              *fingerprint.ValidityRequirement
	reqLen              int
	effPtr              *fingerprint.ValidityEffect
	effLen              int
}

func statementValidityMemoKey(facts fingerprint.StatementValidity) validityMemoKey {
	var k validityMemoKey
	k.complete = facts.Complete
	k.definitionsComplete = facts.DefinitionsComplete
	k.operation = facts.Operation
	k.unsupported = facts.Unsupported
	k.reqLen = len(facts.Requirements)
	if k.reqLen > 0 {
		k.reqPtr = &facts.Requirements[0]
	}
	k.effLen = len(facts.Effects)
	if k.effLen > 0 {
		k.effPtr = &facts.Effects[0]
	}
	return k
}

type memoizedBoundStatement struct {
	facts fingerprint.StatementValidity
	ok    bool
}

func bindValidityProgram(
	program ValidityProgram,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) (ValidityProgram, bool) {
	out := ValidityProgram{Complete: program.Complete}
	if program.Statements != nil {
		out.Statements = make([]StatementValidityAtOrdinal, len(program.Statements))
	}
	if binding == nil || !binding.defensible || mapper == nil {
		for i := range program.Statements {
			st := program.Statements[i]
			st.Facts = cloneStatementValidity(st.Facts)
			st.Facts.Complete = false
			out.Statements[i] = st
		}
		out.Complete = false
		return out, false
	}

	bindingComplete := true
	useMemo := binding.postgres == nil
	var memo map[validityMemoKey]memoizedBoundStatement
	if useMemo {
		memo = make(map[validityMemoKey]memoizedBoundStatement, min(len(program.Statements), 64))
	}
	for i := range program.Statements {
		st := program.Statements[i]
		var statement fingerprint.StatementValidity
		var ok bool
		if useMemo {
			key := statementValidityMemoKey(st.Facts)
			if cached, hit := memo[key]; hit {
				statement, ok = cached.facts, cached.ok
			} else {
				statement, ok = bindStatementValidity(st.Ordinal, st.Facts, binding, mapper)
				memo[key] = memoizedBoundStatement{facts: statement, ok: ok}
			}
		} else {
			statement, ok = bindStatementValidity(st.Ordinal, st.Facts, binding, mapper)
		}
		if !ok {
			bindingComplete = false
			statement.Complete = false
		}
		out.Statements[i] = StatementValidityAtOrdinal{
			Ordinal: st.Ordinal,
			Facts:   statement,
		}
	}
	out.Complete = program.Complete && bindingComplete
	return out, bindingComplete
}

func bindStatementValidity(
	ordinal int32,
	facts fingerprint.StatementValidity,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) (fingerprint.StatementValidity, bool) {
	out := cloneStatementValidity(facts)
	tableSchemas := map[string]string{}
	complete := true

	for _, object := range statementTableObjects(out) {
		bound, ok := bindValidityTableObject(ordinal, object, binding, mapper)
		if !ok {
			complete = false
			continue
		}
		if prior, seen := tableSchemas[bound.Token]; seen && prior != bound.SchemaToken {
			complete = false
			continue
		}
		tableSchemas[bound.Token] = bound.SchemaToken
	}

	bindChild := func(object fingerprint.ValidityObject) (fingerprint.ValidityObject, bool) {
		parentSchema, parentKnown := tableSchemas[object.ParentToken]
		bound, ok := bindValidityChildObject(object, parentSchema, parentKnown, binding, mapper)
		if !ok {
			complete = false
		}
		return bound, ok
	}

	for i := range out.Requirements {
		object := out.Requirements[i].Object
		if object.Kind == fingerprint.ValidityObjectTable {
			bound, ok := bindValidityTableObject(ordinal, object, binding, mapper)
			if ok {
				out.Requirements[i].Object = bound
			}
			continue
		}
		bound, ok := bindChild(object)
		if ok {
			out.Requirements[i].Object = bound
		}
	}
	for i := range out.Effects {
		object := out.Effects[i].Object
		if object.Kind == fingerprint.ValidityObjectTable {
			bound, ok := bindValidityTableObject(ordinal, object, binding, mapper)
			if ok {
				out.Effects[i].Object = bound
			}
		} else {
			bound, ok := bindChild(object)
			if ok {
				out.Effects[i].Object = bound
			}
		}
		if out.Effects[i].Replacement == nil {
			continue
		}
		replacement := *out.Effects[i].Replacement
		if replacement.Kind == fingerprint.ValidityObjectTable {
			bound, ok := bindValidityTableObject(ordinal, replacement, binding, mapper)
			if ok {
				out.Effects[i].Replacement = &bound
			}
		} else {
			bound, ok := bindChild(replacement)
			if ok {
				out.Effects[i].Replacement = &bound
			}
		}
	}
	return out, complete
}

func statementTableObjects(facts fingerprint.StatementValidity) []fingerprint.ValidityObject {
	out := make([]fingerprint.ValidityObject, 0)
	for _, requirement := range facts.Requirements {
		if requirement.Object.Kind == fingerprint.ValidityObjectTable {
			out = append(out, requirement.Object)
		}
	}
	for _, effect := range facts.Effects {
		if effect.Object.Kind == fingerprint.ValidityObjectTable {
			out = append(out, effect.Object)
		}
		if effect.Replacement != nil && effect.Replacement.Kind == fingerprint.ValidityObjectTable {
			out = append(out, *effect.Replacement)
		}
	}
	return out
}

func bindValidityTableObject(
	ordinal int32,
	object fingerprint.ValidityObject,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) (fingerprint.ValidityObject, bool) {
	if object.Kind != fingerprint.ValidityObjectTable || object.Token == "" {
		return object, false
	}
	if !canonicalValidityPhysicalObjectToken(object.Token, privacy.KindTable, mapper) {
		return object, false
	}
	if binding.postgres != nil {
		schema, _, ok := binding.postgresObjectSchemaAt(ordinal, object.Token)
		if !ok {
			return object, false
		}
		if object.SchemaToken != "" {
			kind, original, found := mapper.Original(object.SchemaToken)
			if !found || kind != privacy.KindSchema || original != schema {
				return object, false
			}
		}
		token, ok := canonicalValidityPhysicalSchemaToken(schema, mapper)
		if !ok {
			return object, false
		}
		object.SchemaToken = token
		return object, true
	}
	if object.SchemaToken == "" {
		logical := binding.defaultSchema()
		if logical == "" {
			return object, false
		}
		physical, ok := binding.physicalSchema(logical)
		if !ok {
			return object, false
		}
		token, ok := canonicalValidityPhysicalSchemaToken(physical, mapper)
		if !ok {
			return object, false
		}
		object.SchemaToken = token
		return object, true
	}
	token, ok := canonicalExplicitValidityPhysicalSchemaToken(
		object.SchemaToken, binding, mapper,
	)
	if !ok {
		return object, false
	}
	object.SchemaToken = token
	return object, true
}

func bindValidityChildObject(
	object fingerprint.ValidityObject,
	parentSchema string,
	parentKnown bool,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) (fingerprint.ValidityObject, bool) {
	if !parentKnown || object.ParentToken == "" || object.Token == "" {
		return object, false
	}
	if kind, _, found := mapper.Original(object.ParentToken); !found || kind != privacy.KindTable {
		return object, false
	}
	var want privacy.IdentifierKind
	switch object.Kind {
	case fingerprint.ValidityObjectColumn:
		want = privacy.KindColumn
	case fingerprint.ValidityObjectIndex:
		want = privacy.KindIndex
	default:
		return object, false
	}
	if !canonicalValidityPhysicalObjectToken(object.Token, want, mapper) {
		return object, false
	}
	if object.SchemaToken == "" {
		object.SchemaToken = parentSchema
		return object, parentSchema != ""
	}
	token, ok := canonicalExplicitValidityPhysicalSchemaToken(
		object.SchemaToken, binding, mapper,
	)
	if !ok || token != parentSchema {
		return object, false
	}
	object.SchemaToken = token
	return object, true
}

// canonicalValidityPhysicalObjectToken rejects object identities whose exact
// and folded scopes both denote the same source text. Their equivalence is
// engine-dependent, so treating them as one object could create a false
// Critical finding.
func canonicalValidityPhysicalObjectToken(
	token string,
	want privacy.IdentifierKind,
	mapper *privacy.RunMapper,
) bool {
	kind, original, found := mapper.Original(token)
	if !found || kind != want || original == "" {
		return false
	}
	candidates := map[string]struct{}{}
	for _, pair := range mapper.Snapshot() {
		if pair.Kind == want && pair.Original == original &&
			isPhysicalSQLScope(pair.Scope) {
			candidates[pair.Token] = struct{}{}
		}
	}
	return len(candidates) == 1
}

func canonicalExplicitValidityPhysicalSchemaToken(
	token string,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) (string, bool) {
	if token == "" || mapper == nil {
		return "", false
	}
	kind, original, found := mapper.Original(token)
	if !found || kind != privacy.KindSchema {
		return "", false
	}
	physical, ok := binding.explicitPhysicalSchema(original)
	if !ok {
		return "", false
	}
	return canonicalValidityPhysicalSchemaToken(physical, mapper)
}

// canonicalValidityPhysicalSchemaToken returns the one physical-SQL token
// that denotes the authorized physical schema. Separate quoted and unquoted
// spellings with the same text are distinct mapper identities; when both
// exist, their equivalence cannot be proved and binding fails closed.
func canonicalValidityPhysicalSchemaToken(
	physical string,
	mapper *privacy.RunMapper,
) (string, bool) {
	if physical == "" || mapper == nil {
		return "", false
	}
	candidates := map[string]struct{}{}
	for _, pair := range mapper.Snapshot() {
		if pair.Kind == privacy.KindSchema && pair.Original == physical &&
			isPhysicalSQLScope(pair.Scope) {
			candidates[pair.Token] = struct{}{}
		}
	}
	switch len(candidates) {
	case 1:
		for token := range candidates {
			return token, true
		}
	case 0:
		token, err := mapper.Token(privacy.KindSchema, physical)
		if err == nil && token != "" {
			return token, true
		}
	}
	return "", false
}

// bindValidityEvidence makes the runtime-only copy of Provider evidence whose
// schema token names the canonical physical identity used by the bound
// program. Only the top-level schema field participates; table, column, index,
// SQL, and Provider-visible evidence remain untouched. Any unprovable or
// ambiguous mapping is left unchanged so the evaluator remains unknown.
func bindValidityEvidence(
	refs []provider.NormalizedEvidenceRef,
	binding *stageSchemaBinding,
	mapper *privacy.RunMapper,
) []provider.NormalizedEvidenceRef {
	out := make([]provider.NormalizedEvidenceRef, len(refs))
	for i := range refs {
		out[i] = refs[i]
		if refs[i].Fact == nil {
			continue
		}
		out[i].Fact = cloneValidityFact(refs[i].Fact)
		token, _ := refs[i].Fact["schema"].(string)
		if token == "" || binding == nil || mapper == nil {
			continue
		}
		kind, original, found := mapper.Original(token)
		if !found || kind != privacy.KindSchema {
			continue
		}
		physical, ok := binding.physicalSchema(original)
		if !ok {
			continue
		}
		canonical, ok := canonicalValidityPhysicalSchemaToken(physical, mapper)
		if !ok {
			continue
		}
		out[i].Fact["schema"] = canonical
	}
	return out
}
