// validity_fact_lookup.go Normalized metadata evidence parsing and object-state observation indexing for execution validity.
package application

import (
	"sort"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

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
