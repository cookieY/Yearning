package application

import (
	"sort"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
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

// validityFactIndex projects normalized evidence into facts for every physical
// object named by the bound program. Missing evidence remains absent from the
// map. Contradictory or structurally incomplete evidence is represented as an
// incomplete unknown fact so it can never support a false invalidity proof.
func validityFactIndex(
	program ValidityProgram,
	evidence []provider.NormalizedEvidenceRef,
) ValidityFactIndex {
	objects := validityProgramObjects(program)
	if len(objects) == 0 {
		return ValidityFactIndex{}
	}
	snapshots := parseValidityEvidence(evidence)
	out := ValidityFactIndex{}
	for object := range objects {
		if object.Kind != fingerprint.ValidityObjectTable &&
			object.Kind != fingerprint.ValidityObjectColumn &&
			object.Kind != fingerprint.ValidityObjectIndex {
			continue
		}
		if object.SchemaToken == "" || object.Token == "" {
			continue
		}
		var fact validityFactObservation
		switch object.Kind {
		case fingerprint.ValidityObjectTable:
			fact = validityTableObservation(object, snapshots)
		case fingerprint.ValidityObjectColumn:
			fact = validityColumnObservation(object, snapshots)
		case fingerprint.ValidityObjectIndex:
			fact = validityIndexObservationForObject(object, snapshots)
		}
		if observed, ok := fact.result(); ok {
			out[object] = observed
		}
	}
	return out
}

func validityProgramObjects(program ValidityProgram) map[fingerprint.ValidityObject]struct{} {
	out := map[fingerprint.ValidityObject]struct{}{}
	add := func(object fingerprint.ValidityObject) {
		out[object] = struct{}{}
	}
	for _, statement := range program.Statements {
		if !statement.Facts.Complete {
			continue
		}
		for _, requirement := range statement.Facts.Requirements {
			add(requirement.Object)
		}
		for _, effect := range statement.Facts.Effects {
			add(effect.Object)
			if effect.Replacement != nil {
				add(*effect.Replacement)
			}
		}
	}
	return out
}

type validityEvidenceSnapshot struct {
	id         string
	kind       string
	schema     string
	table      string
	existence  string
	complete   bool
	columns    map[string]struct{}
	columnsOK  bool
	indexes    map[string]validityIndexSnapshot
	indexesOK  bool
	scopeValid bool
}

type validityIndexSnapshot struct {
	columns map[string]struct{}
}

func parseValidityEvidence(evidence []provider.NormalizedEvidenceRef) []validityEvidenceSnapshot {
	out := make([]validityEvidenceSnapshot, 0, len(evidence))
	for _, ref := range evidence {
		if ref.Fact == nil {
			continue
		}
		switch ref.FactKind {
		case tool.NameSchemaMetadata, tool.NameTableStatistics, tool.NameIndexMetadata:
		default:
			continue
		}
		snapshot := validityEvidenceSnapshot{id: ref.EvidenceID, kind: ref.FactKind}
		snapshot.schema, _ = ref.Fact["schema"].(string)
		snapshot.table, _ = ref.Fact["table"].(string)
		snapshot.existence, _ = ref.Fact["existence"].(string)
		existenceKnown := snapshot.existence == "present" || snapshot.existence == "absent"
		metadataComplete, _ := ref.Fact["metadata_complete"].(bool)
		version, versionOK := validityNonNegativeNumber(ref.Fact["fact_schema_version"])
		snapshot.complete = existenceKnown && metadataComplete && versionOK &&
			version == tool.FactSchemaVersion && metadataFactComplete(ref.Fact)
		snapshot.scopeValid = snapshot.schema != "" && snapshot.table != ""
		if ref.FactKind == tool.NameSchemaMetadata {
			snapshot.columns, snapshot.columnsOK = validityColumnTokens(ref.Fact["columns"])
		}
		if ref.FactKind == tool.NameIndexMetadata {
			snapshot.indexes, snapshot.indexesOK = validityIndexTokens(ref.Fact["indexes"])
		}
		out = append(out, snapshot)
	}
	return out
}

func validityColumnTokens(value any) (map[string]struct{}, bool) {
	if value == nil {
		return nil, false
	}
	items, ok := validityAnySlice(value)
	if !ok {
		return nil, false
	}
	out := map[string]struct{}{}
	for _, item := range items {
		mapping, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		token, ok := mapping["column_name"].(string)
		if !ok || token == "" {
			return nil, false
		}
		addValidityToken(out, token)
		if !addValidityReferenceTokens(out, mapping["sql_reference_tokens"]) {
			return nil, false
		}
	}
	return out, true
}

func validityIndexTokens(value any) (map[string]validityIndexSnapshot, bool) {
	if value == nil {
		return nil, false
	}
	items, ok := validityAnySlice(value)
	if !ok {
		return nil, false
	}
	out := map[string]validityIndexSnapshot{}
	for _, item := range items {
		mapping, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		token, ok := mapping["index_name"].(string)
		if !ok || token == "" {
			return nil, false
		}
		columns, ok := validityStringTokens(mapping["columns"])
		if !ok {
			return nil, false
		}
		included, ok := validityStringTokens(mapping["included_columns"])
		if !ok {
			return nil, false
		}
		if !validityIndexShapeComplete(mapping) {
			return nil, false
		}
		for column := range included {
			columns[column] = struct{}{}
		}
		aliases := map[string]struct{}{}
		addValidityToken(aliases, token)
		if !addValidityReferenceTokens(aliases, mapping["sql_reference_tokens"]) {
			return nil, false
		}
		for alias := range aliases {
			out[alias] = validityIndexSnapshot{columns: columns}
		}
	}
	return out, true
}

func validityIndexShapeComplete(index map[string]any) bool {
	expressionCount, expressionOK := validityNonNegativeNumber(index["expression_count"])
	hasExpression, hasExpressionOK := index["has_expression"].(bool)
	prefixCount, prefixOK := validityNonNegativeNumber(index["prefix_column_count"])
	hasPrefix, hasPrefixOK := index["has_prefix"].(bool)
	if !expressionOK || !hasExpressionOK || !prefixOK || !hasPrefixOK {
		return false
	}
	return !hasExpression && expressionCount == 0 && !hasPrefix && prefixCount == 0
}

func validityNonNegativeNumber(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, number >= 0
	case int32:
		return int(number), number >= 0
	case int64:
		return int(number), number >= 0
	case float64:
		if number < 0 || number != float64(int(number)) {
			return 0, false
		}
		return int(number), true
	default:
		return 0, false
	}
}

func validityStringTokens(value any) (map[string]struct{}, bool) {
	items, ok := validityAnySlice(value)
	if !ok {
		return nil, false
	}
	out := map[string]struct{}{}
	for _, item := range items {
		token, ok := item.(string)
		if !ok || token == "" {
			return nil, false
		}
		addValidityToken(out, token)
	}
	return out, true
}

func validityAnySlice(value any) ([]any, bool) {
	switch items := value.(type) {
	case []any:
		return items, true
	case []map[string]any:
		out := make([]any, len(items))
		for i := range items {
			out[i] = items[i]
		}
		return out, true
	case []string:
		out := make([]any, len(items))
		for i := range items {
			out[i] = items[i]
		}
		return out, true
	default:
		return nil, false
	}
}

func addValidityToken(out map[string]struct{}, token string) {
	if token != "" {
		out[token] = struct{}{}
	}
}

func addValidityReferenceTokens(out map[string]struct{}, value any) bool {
	if value == nil {
		return true
	}
	tokens, ok := validityStringTokens(value)
	if !ok {
		return false
	}
	for token := range tokens {
		addValidityToken(out, token)
	}
	return true
}

type validityFactObservation struct {
	present    bool
	absent     bool
	incomplete bool
	evidence   map[string]struct{}
}

func (o *validityFactObservation) observe(
	state ValidityFactState, complete bool, evidenceID string,
) {
	if !complete || state == ValidityFactUnknown {
		o.incomplete = true
	} else {
		switch state {
		case ValidityFactPresent:
			o.present = true
		case ValidityFactAbsent:
			o.absent = true
		default:
			o.incomplete = true
		}
	}
	if evidenceID != "" {
		if o.evidence == nil {
			o.evidence = map[string]struct{}{}
		}
		o.evidence[evidenceID] = struct{}{}
	}
}

func (o validityFactObservation) result() (ValidityObjectFact, bool) {
	if !o.present && !o.absent && !o.incomplete {
		return ValidityObjectFact{}, false
	}
	state := ValidityFactUnknown
	complete := false
	switch {
	case !o.incomplete && o.present && !o.absent:
		state, complete = ValidityFactPresent, true
	case !o.incomplete && !o.present && o.absent:
		state, complete = ValidityFactAbsent, true
	}
	ids := make([]string, 0, len(o.evidence))
	for id := range o.evidence {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ValidityObjectFact{State: state, Complete: complete, EvidenceIDs: ids}, true
}

func validityTableObservation(
	object fingerprint.ValidityObject,
	evidence []validityEvidenceSnapshot,
) validityFactObservation {
	var out validityFactObservation
	for _, item := range evidence {
		if !item.scopeValid || item.schema != object.SchemaToken || item.table != object.Token {
			continue
		}
		switch item.existence {
		case "present":
			out.observe(ValidityFactPresent, item.complete, item.id)
		case "absent":
			out.observe(ValidityFactAbsent, item.complete, item.id)
		default:
			out.observe(ValidityFactUnknown, false, item.id)
		}
	}
	return out
}

func validityColumnObservation(
	object fingerprint.ValidityObject,
	evidence []validityEvidenceSnapshot,
) validityFactObservation {
	var out validityFactObservation
	for _, item := range evidence {
		if !item.scopeValid || item.schema != object.SchemaToken ||
			item.table != object.ParentToken {
			continue
		}
		if item.existence == "absent" {
			out.observe(ValidityFactAbsent, item.complete, item.id)
			continue
		}
		if item.existence != "present" {
			out.observe(ValidityFactUnknown, false, item.id)
			continue
		}
		switch item.kind {
		case tool.NameSchemaMetadata:
			if !item.complete || !item.columnsOK {
				out.observe(ValidityFactUnknown, false, item.id)
				continue
			}
			_, present := item.columns[object.Token]
			if present {
				out.observe(ValidityFactPresent, true, item.id)
			} else {
				out.observe(ValidityFactAbsent, true, item.id)
			}
		case tool.NameIndexMetadata:
			present := validityIndexColumnsContain(item.indexes, object.Token)
			if !item.complete || !item.indexesOK {
				if present {
					out.observe(ValidityFactUnknown, false, item.id)
				}
				continue
			}
			if present {
				out.observe(ValidityFactPresent, true, item.id)
			}
		}
	}
	return out
}

func validityIndexObservationForObject(
	object fingerprint.ValidityObject,
	evidence []validityEvidenceSnapshot,
) validityFactObservation {
	var out validityFactObservation
	for _, item := range evidence {
		if !item.scopeValid || item.schema != object.SchemaToken ||
			item.table != object.ParentToken {
			continue
		}
		if item.existence == "absent" {
			out.observe(ValidityFactAbsent, item.complete, item.id)
			continue
		}
		if item.existence != "present" {
			out.observe(ValidityFactUnknown, false, item.id)
			continue
		}
		if item.kind != tool.NameIndexMetadata {
			continue
		}
		if !item.complete || !item.indexesOK {
			out.observe(ValidityFactUnknown, false, item.id)
			continue
		}
		if _, present := item.indexes[object.Token]; present {
			out.observe(ValidityFactPresent, true, item.id)
		} else {
			out.observe(ValidityFactAbsent, true, item.id)
		}
	}
	return out
}

func validityIndexColumnsContain(
	indexes map[string]validityIndexSnapshot,
	columnToken string,
) bool {
	for _, index := range indexes {
		if _, found := index.columns[columnToken]; found {
			return true
		}
	}
	return false
}
