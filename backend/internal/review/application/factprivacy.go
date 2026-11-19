// factprivacy.go SEC-0002 fix: identifier tokenization at the FactSource -> NormalizedFact boundary.
//
// Contract (api/contracts/ai-review-runtime-contract.json input_privacy, read-only):
// raw_identifiers_to_provider=forbidden;
// identifier_representation=review_scoped_stable_anonymous_tokens.
//
// Before schema/table/column/index names in the five built-in tool facts enter NormalizedFact
// (the Provider request channel), they must be replaced with Run-scoped stable anonymous tokens; all tool
// calls and all stages within the same Review Run share a single RunMapper instance so identical
// (kind, originalName) pairs always receive the same token (a token space isomorphic to SQL template
// anonymization). Raw facts (real identifiers) remain exclusively in the RawFact ->
// evidence_raw_payloads controlled ciphertext channel (7 days, owner raw reveal).
//
// Defense in depth (NEW-AI-02 two layers):
//  1. Free-text fields (content outside the factFreeTextListFields allowlist must never enter the model
//     channel): content of known free-text fields is stripped entirely before tokenization — leak assertions
//     can only see identifiers that have been tokenized within this Run, so identifiers that were never
//     tokenized appearing in free text (such as an index name in an engine warning) would be invisible to
//     the assertion; therefore free-text content itself is barred from entering model context, while raw
//     content is preserved in the RawFact -> evidence_raw_payloads controlled ciphertext channel (7 days, owner raw reveal).
//  2. After tokenization, a leak assertion (privacy.AssertNoLeakValues) is executed across all remaining
//     string values using the full Run mapping from mapper.Snapshot() as the forbidden set — if any identifier
//     tokenized in a prior tool call is echoed back via an unknown key or free-text field, it likewise fails
//     closed and never degrades to sending raw identifiers.
package application

import (
	"errors"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// ErrFactTokenize indicates tool fact identifier tokenization failed (fail closed: the fact never
// enters model context, and the tool call is booked as failure 2012).
var ErrFactTokenize = errors.New("review: tool fact identifier anonymization failed")

// factFreeTextListFields lists the free-text list fields in NormalizedFact (NEW-AI-02 allowlist):
// their content is engine text (such as Explain warnings) that may carry arbitrary identifiers — including
// identifiers never tokenized in this Run and thus invisible to leak assertions — so their content is
// always stripped from model context, retaining only an empty list placeholder to keep the fact shape
// stable. Free-text keys not in this allowlist do not receive stripping treatment: if their content
// collides with the Run mapping forbidden set, they fail closed immediately.
var factFreeTextListFields = map[string]bool{"warnings": true}

// tokenizeToolFact preserves the existing compatibility entry point for calls without bindings.
func tokenizeToolFact(fact map[string]any, mapper *privacy.RunMapper) error {
	return tokenizeToolFactWithBindings(fact, mapper, nil)
}

// tokenizeToolFactWithBindings replaces schema/table/column/index identifiers in a tool fact in place
// with stable anonymous tokens allocated by mapper, and runs a leak assertion across all remaining string
// values of the tokenized fact. fact must be a NormalizedFact produced by registry.Execute (with normalized
// structure: lists are []any and maps are map[string]any).
//
// bindings holds the schema/table token arguments from the model's current-turn tool call that have already
// passed knownToolIdentifiers validation. If the top-level fact carries the corresponding real name, it must
// reuse that token to prevent PostgreSQL quoted identifiers from being re-allocated a different token after
// default case folding on the tool fact side. When columns, indexes, and the tables list have no independent
// argument binding, they first look up existing tokens by exact ScopeCaseSensitive match before falling back
// to compatible folded lookup; only identifiers that never appeared in the SQL object graph are allocated
// new tokens under folded identity.
//
// Handled identifier fields (corresponding to the five tool outputs in factsource.go):
//   - top-level schema (schema_metadata/table_statistics/index_metadata)
//   - top-level table (table_statistics/index_metadata)
//   - top-level tables list (schema_metadata)
//   - columns[].column_name (scoped schema_metadata)
//   - indexes[].index_name, indexes[].columns[],
//     indexes[].included_columns[] (index_metadata)
//   - top-level index_names list (safe_explain)
//
// Free-text fields (factFreeTextListFields) have their content stripped entirely before tokenization.
//
// Empty strings and non-string items are skipped (do not constitute identifiers); returns an error if
// mapper is nil or any identifier mapping fails (including non-round-trippable forms containing mapping delimiter characters).
func tokenizeToolFactWithBindings(
	fact map[string]any, mapper *privacy.RunMapper, bindings map[string]any,
) error {
	if fact == nil || mapper == nil {
		return ErrFactTokenize
	}
	// NEW-AI-02 Layer 1: strip free-text field content (allowlist semantics) —
	// because assertions cannot cover the path where an identifier never tokenized appears in free text,
	// free-text content never enters the model channel at all (raw text is preserved in RawFact ciphertext).
	for key := range factFreeTextListFields {
		if v, ok := fact[key]; ok {
			fact[key] = emptyListLike(v)
		}
	}
	type boundIdentifier struct {
		token    string
		original string
	}
	bound := map[string]boundIdentifier{}
	for key, want := range map[string]privacy.IdentifierKind{
		"schema": privacy.KindSchema,
		"table":  privacy.KindTable,
	} {
		rawToken, present := bindings[key]
		if !present {
			continue
		}
		token, ok := rawToken.(string)
		if !ok || token == "" {
			return ErrFactTokenize
		}
		kind, original, found := mapper.Original(token)
		if !found || kind != want || original == "" {
			return ErrFactTokenize
		}
		bound[key] = boundIdentifier{token: token, original: original}
	}
	preferredToken := func(kind privacy.IdentifierKind, original string) (string, error) {
		if token := mapper.LookupScoped(kind, privacy.ScopeCaseSensitive, original); token != "" {
			return token, nil
		}
		if token := mapper.Lookup(kind, original); token != "" {
			return token, nil
		}
		return mapper.Token(kind, original)
	}
	tokenFor := func(
		kind privacy.IdentifierKind, original, bindingKey string, requireBindingMatch bool,
	) (string, error) {
		if binding, ok := bound[bindingKey]; ok {
			if binding.original == original {
				return binding.token, nil
			}
			if requireBindingMatch {
				return "", ErrFactTokenize
			}
		}
		// The forbidden set is not accumulated here: it is constructed in full from mapper.Snapshot() right
		// before the assertion (NEW-AI-02 — the snapshot includes tokenization results from all prior calls).
		return preferredToken(kind, original)
	}
	replaceString := func(
		m map[string]any, key string, kind privacy.IdentifierKind, requireBindingMatch bool,
	) error {
		v, ok := m[key].(string)
		if !ok || v == "" {
			return nil
		}
		t, err := tokenFor(kind, v, key, requireBindingMatch)
		if err != nil {
			return err
		}
		m[key] = t
		return nil
	}
	replaceList := func(m map[string]any, key string, kind privacy.IdentifierKind) error {
		list, ok := m[key].([]any)
		if !ok {
			return nil
		}
		for i, item := range list {
			name, ok := item.(string)
			if !ok || name == "" {
				continue
			}
			t, err := tokenFor(kind, name, key, false)
			if err != nil {
				return err
			}
			list[i] = t
		}
		return nil
	}
	replaceStringWithReferences := func(
		m map[string]any, key string, kind privacy.IdentifierKind,
	) error {
		original, ok := m[key].(string)
		if !ok || original == "" {
			return nil
		}
		token, err := tokenFor(kind, original, key, false)
		if err != nil {
			return err
		}
		m[key] = token
		m["sql_reference_tokens"] = mapperReferenceTokens(mapper, kind, original)
		return nil
	}
	if err := replaceString(fact, "schema", privacy.KindSchema, true); err != nil {
		return ErrFactTokenize
	}
	if err := replaceString(fact, "table", privacy.KindTable, true); err != nil {
		return ErrFactTokenize
	}
	if err := replaceList(fact, "tables", privacy.KindTable); err != nil {
		return ErrFactTokenize
	}
	if err := replaceList(fact, "index_names", privacy.KindIndex); err != nil {
		return ErrFactTokenize
	}
	if columns, ok := fact["columns"].([]any); ok {
		for _, item := range columns {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if err := replaceStringWithReferences(
				m, "column_name", privacy.KindColumn,
			); err != nil {
				return ErrFactTokenize
			}
		}
	}
	if indexes, ok := fact["indexes"].([]any); ok {
		for _, item := range indexes {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if err := replaceStringWithReferences(
				m, "index_name", privacy.KindIndex,
			); err != nil {
				return ErrFactTokenize
			}
			if err := replaceList(m, "columns", privacy.KindColumn); err != nil {
				return ErrFactTokenize
			}
			if err := replaceList(m, "included_columns", privacy.KindColumn); err != nil {
				return ErrFactTokenize
			}
		}
	}
	// Defense in depth (NEW-AI-02 Layer 2): assert that all remaining string values (all text other than
	// engine vocabulary and rendered aggregate statistics) contain no raw identifiers. The forbidden set is
	// taken from the full Run mapping of mapper.Snapshot() — including identifiers tokenized across all prior
	// tool calls; cross-call free-text echoing of old identifiers likewise fails closed. Because the snapshot
	// is constructed after tokenization, identifiers newly added in the current call are also in the forbidden set.
	// Fails closed on any match.
	if err := privacy.AssertNoLeakValues(collectFactStringValues(fact),
		forbiddenFromSnapshot(mapper)); err != nil {
		return ErrFactTokenize
	}
	return nil
}

// projectToolFactSchema converts the physical schema returned by the database
// tool back to the schema identity requested with the model-visible token.
// Usually that identity is the logical mapping key; when the SQL explicitly
// names an allowed physical schema, it remains that physical identity. Scope
// checks and subsequent fact tokenization therefore operate on the same
// object named by the reviewed SQL and requested by the model.
func projectToolFactSchema(
	fact map[string]any, args map[string]any, mapper *privacy.RunMapper,
	binding *stageSchemaBinding,
) error {
	if fact == nil || mapper == nil || binding == nil {
		return ErrFactTokenize
	}
	rawSchema, supplied := args["schema"]
	if !supplied {
		return nil
	}
	schemaToken, ok := rawSchema.(string)
	if !ok || schemaToken == "" {
		return ErrFactTokenize
	}
	kind, requested, found := mapper.Original(schemaToken)
	if !found || kind != privacy.KindSchema {
		return ErrFactTokenize
	}
	physical, ok := binding.physicalSchema(requested)
	if !ok {
		return ErrFactTokenize
	}
	factSchema, present := fact["schema"]
	if !present {
		return nil // safe_explain and identity facts do not echo a schema
	}
	name, ok := factSchema.(string)
	if !ok || (name != physical && name != requested) {
		return ErrFactTokenize
	}
	fact["schema"] = requested
	return nil
}

// mapperReferenceTokens returns every Run token that denotes the same physical
// SQL identifier as the fact's canonical token. PostgreSQL quoted and unquoted
// spellings can share an original name while occupying distinct mapper scopes;
// both tokens must remain visible so metadata facts can cover every reference
// in the reviewed Run. CTE, alias, constraint, and fallback-lexer scopes are
// virtual namespaces and must not be projected as physical object references.
//
// The returned []any matches the normalized tool-fact JSON shape. Mapping
// snapshot order is the deterministic token-allocation order retained by
// encrypted Run replay.
func mapperReferenceTokens(
	mapper *privacy.RunMapper, kind privacy.IdentifierKind, original string,
) []any {
	seen := map[string]struct{}{}
	out := []any{}
	for _, pair := range mapper.Snapshot() {
		if pair.Kind != kind || pair.Original != original ||
			!isPhysicalSQLScope(pair.Scope) {
			continue
		}
		if _, ok := seen[pair.Token]; ok {
			continue
		}
		seen[pair.Token] = struct{}{}
		out = append(out, pair.Token)
	}
	return out
}

// isPhysicalSQLScope accepts only scopes produced for physical SQL
// identifiers: case-folded identity (empty scope) and case-sensitive quoted
// identity (exact). Named scopes such as "cte:fold", "alias:exact", and
// "constraint:fold" identify virtual program objects.
func isPhysicalSQLScope(scope string) bool {
	return scope == "" || scope == privacy.ScopeCaseSensitive
}

// forbiddenFromSnapshot constructs the leak assertion forbidden set from the full Run mapping snapshot
// (NEW-AI-02: the forbidden set must cover identifiers tokenized across all prior calls, not just the current call).
func forbiddenFromSnapshot(mapper *privacy.RunMapper) []privacy.Forbidden {
	snap := mapper.Snapshot()
	out := make([]privacy.Forbidden, 0, len(snap))
	for _, p := range snap {
		out = append(out, privacy.Forbidden{
			Value: p.Original, What: "identifier:" + string(p.Kind)})
	}
	return out
}

// emptyListLike returns an empty list isomorphic to the input ([]any/[]string -> empty placeholder;
// other forms return nil — free-text fields are lists by contract, so malformed shapes are cleared directly).
func emptyListLike(v any) any {
	switch v.(type) {
	case []any:
		return []any{}
	case []string:
		return []string{}
	default:
		return nil
	}
}

// collectFactStringValues recursively collects all string values inside a fact (map keys do not
// participate — keys are fixed contract key names, whereas values are content that might carry identifiers).
func collectFactStringValues(v any) []string {
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			for _, vv := range t {
				walk(vv)
			}
		case []any:
			for _, vv := range t {
				walk(vv)
			}
		}
	}
	walk(v)
	return out
}

// detokenizeToolArgs returns a copy of tool arguments: if a schema/table argument value is a token
// already allocated in this Run's mapping (a name learned by the model from tokenized facts), it is
// restored to the original identifier before handing off to FactSource to query the target DB — otherwise
// querying the real DB with "table_1" would inevitably fail, rendering the tool chain unusable after
// tokenization. The category must match argument semantics (the schema argument only restores schema tokens),
// and non-token values pass through unchanged (such as "public" guessed by the model); the model's
// original arguments are not mutated (the ledger still records the model-visible form).
func detokenizeToolArgs(args map[string]any, mapper *privacy.RunMapper) map[string]any {
	if mapper == nil || len(args) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	resolve := func(key string, want privacy.IdentifierKind) {
		s, ok := out[key].(string)
		if !ok || s == "" {
			return
		}
		if kind, original, found := mapper.Original(s); found && kind == want {
			out[key] = original
		}
	}
	resolve("schema", privacy.KindSchema)
	resolve("table", privacy.KindTable)
	return out
}
