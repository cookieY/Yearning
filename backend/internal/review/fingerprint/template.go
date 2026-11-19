package fingerprint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// kindShort is the short name of a role in the fingerprint sequence.
var kindShort = map[privacy.IdentifierKind]string{
	privacy.KindDatabase:   "db",
	privacy.KindSchema:     "sch",
	privacy.KindTable:      "tbl",
	privacy.KindColumn:     "col",
	privacy.KindIndex:      "idx",
	privacy.KindConstraint: "con",
	privacy.KindAlias:      "al",
	privacy.KindCTE:        "cte",
	privacy.KindGeneric:    "id",
}

// foldPunct folds equivalent surface operators into canonical form (canonicalize_equivalent_surface_syntax).
var foldPunct = map[string]string{
	"!=": "<>", "==": "=", "&&": "and", ":=": "=",
}

// renderState holds shared state for template rendering.
type renderState struct {
	toks     []Token
	roles    []idRole
	keepRaw  []bool
	specials []specialSpan
	// Per-statement role ordinal (deduplicated by lowercase original name for cross-dialect equivalence).
	seq map[string]int
}

func newRenderState(toks []Token, res walkResult) *renderState {
	st := &renderState{toks: toks, roles: res.roles, keepRaw: res.keepRaw, specials: res.specials}
	if st.roles == nil {
		st.roles = make([]idRole, len(toks))
	}
	if st.keepRaw == nil {
		st.keepRaw = make([]bool, len(toks))
	}
	st.seq = map[string]int{}
	return st
}

// roleOrdinal returns the per-statement ordinal for (kind, lowerName), allocating on first appearance.
func (st *renderState) roleOrdinal(kind privacy.IdentifierKind, name string) int {
	key := string(kind) + "\x00" + strings.ToLower(name)
	if n, ok := st.seq[key]; ok {
		return n
	}
	st.seq[key] = len(st.seq) + 1
	return st.seq[key]
}

// specialAt returns the collapse instruction covering token i (start <= i < end), or nil if none.
func (st *renderState) specialAt(i int) *specialSpan {
	for k := range st.specials {
		s := &st.specials[k]
		if i >= s.tokStart && i < s.tokEnd {
			return s
		}
	}
	return nil
}

// CanonicalSequence outputs the canonical fingerprint input sequence:
// role -> per-statement ordinal events, literal -> type events, IN/VALUES cardinality collapsing,
// equivalent surface operator folding, and MySQL LIMIT off,cnt canonicalized as limit cnt offset off.
func (st *renderState) CanonicalSequence() []string {
	out := make([]string, 0, len(st.toks))
	skipUntil := -1
	for i := 0; i < len(st.toks); i++ {
		if i < skipUntil {
			continue
		}
		if s := st.specialAt(i); s != nil {
			switch s.kind {
			case specialInList:
				out = append(out, "g:in")
				skipUntil = s.tokEnd
				continue
			case specialValuesTuples:
				// Retain only the structure of the first tuple and collapse remaining tuples.
				out = append(out, st.toks[i].Text) // '('
				emitted, _ := st.renderTupleInside(i)
				out = append(out, emitted...)
				out = append(out, ")")
				out = append(out, "g:tuples")
				skipUntil = s.tokEnd
				continue
			case specialLimitComma:
				// LIMIT off, cnt -> canonicalized as limit cnt offset off;
				// k:limit was already emitted by the regular token path, so only the count/offset sequence is appended here.
				out = append(out, st.literalEvent(s.tokStart+2))
				out = append(out, "k:offset")
				out = append(out, st.literalEvent(s.tokStart))
				skipUntil = s.tokEnd
				continue
			}
		}
		out = append(out, st.canonicalEvent(i))
	}
	return out
}

// renderTupleInside renders events inside the first VALUES tuple (up to the matching ')').
func (st *renderState) renderTupleInside(openIdx int) ([]string, int) {
	var out []string
	depth := 0
	for i := openIdx; i < len(st.toks); i++ {
		t := st.toks[i]
		if t.IsPunct("(") {
			depth++
			if i > openIdx {
				out = append(out, "(")
			}
			continue
		}
		if t.IsPunct(")") {
			depth--
			if depth == 0 {
				return out, i
			}
			out = append(out, ")")
			continue
		}
		out = append(out, st.canonicalEvent(i))
	}
	return out, len(st.toks)
}

// literalEvent returns the literal event text at position i.
func (st *renderState) literalEvent(i int) string {
	if lt, ok := LiteralTypeOf(st.toks[i]); ok {
		return "l:" + string(lt)
	}
	return "l:?"
}

// canonicalEvent outputs the canonical event at position i.
func (st *renderState) canonicalEvent(i int) string {
	t := st.toks[i]
	if r := st.roles[i]; r.kind != "" || r.canonicalKind != "" {
		if r.canonicalWord {
			return "k:" + t.Text
		}
		kind, name := r.kind, r.name
		if r.canonicalKind != "" {
			kind, name = r.canonicalKind, r.canonicalName
		}
		return fmt.Sprintf("r:%s:%d", kindShort[kind], st.roleOrdinal(kind, name))
	}
	switch t.Kind {
	case TokString, TokNumber, TokDollarString, TokParam:
		return st.literalEvent(i)
	case TokWord:
		if lt, ok := ClassifyWordLiteral(t.Text); ok {
			return "l:" + string(lt)
		}
		return "k:" + t.Text
	case TokQuotedIdent:
		// Quoted identifiers without a role are treated as generic identifiers (defensive path).
		return fmt.Sprintf("r:%s:%d", kindShort[privacy.KindGeneric], st.roleOrdinal(privacy.KindGeneric, t.Text))
	case TokPunct:
		if folded, ok := foldPunct[t.Text]; ok {
			if folded == "and" {
				return "k:and"
			}
			return "k:" + folded
		}
		return "k:" + t.Text
	}
	return "k:" + t.Text
}

// DialectSequence outputs the dialect fingerprint input: canonical sequence + profile + dialect features + Hint categories.
// Literal types are included in the canonical sequence, so differing types necessarily produce different dialect fingerprints.
func (st *renderState) DialectSequence(profile Profile, kind string, features, hints []string) string {
	var b strings.Builder
	b.WriteString(AlgorithmVersion)
	b.WriteString("|profile=")
	b.WriteString(profile.String())
	b.WriteString("|kind=")
	b.WriteString(kind)
	for _, e := range st.CanonicalSequence() {
		b.WriteString("|")
		b.WriteString(e)
	}
	fs := append([]string{}, features...)
	sort.Strings(fs)
	b.WriteString("|features=")
	b.WriteString(strings.Join(fs, ","))
	hs := append([]string{}, hints...)
	sort.Strings(hs)
	b.WriteString("|hints=")
	b.WriteString(strings.Join(hs, ","))
	return b.String()
}

// ExecutionShapeSequence outputs the execution shape fingerprint input:
// canonical structure + cardinality buckets (total parameters, VALUES tuple count, element count per IN list).
// Exact counts do not enter the fingerprint (contract: record_cardinality_outside_structural_fingerprint).
func (st *renderState) ExecutionShapeSequence(kind string, paramCount, tupleCount int, inSizes []int) string {
	var b strings.Builder
	b.WriteString(AlgorithmVersion)
	b.WriteString("|kind=")
	b.WriteString(kind)
	for _, e := range st.CanonicalSequence() {
		b.WriteString("|")
		b.WriteString(e)
	}
	b.WriteString("|pcount=")
	b.WriteString(CardinalityBucket(paramCount))
	b.WriteString("|tuples=")
	b.WriteString(CardinalityBucket(tupleCount))
	sizes := append([]int{}, inSizes...)
	sort.Ints(sizes)
	b.WriteString("|in=")
	for i, n := range sizes {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(CardinalityBucket(n))
	}
	return b.String()
}

// RenderAnonymized renders anonymized SQL (the model-visible form).
// Roles -> Run-scoped stable tokens; literals -> typed placeholders; IN/VALUES collapsed to [n];
// comments and full Hint text are omitted; mapping failures (including mapping-delimiter identifiers) propagate errors.
func RenderAnonymized(toks []Token, res walkResult, mapper *privacy.RunMapper, profile Profile) (string, error) {
	sql, _, err := RenderAnonymizedDetailed(toks, res, mapper, profile)
	return sql, err
}

// RenderAnonymizedDetailed uses the exact same token path as RenderAnonymized
// while also returning structured anonymous object relationships. Object references never rescan or re-parse SQL text.
func RenderAnonymizedDetailed(
	toks []Token,
	res walkResult,
	mapper *privacy.RunMapper,
	profile Profile,
) (string, []ObjectReference, error) {
	st := newRenderState(toks, res)
	collector := newObjectCollector(mapper)
	var parts []string
	skipUntil := -1
	for i := 0; i < len(toks); i++ {
		if i < skipUntil {
			continue
		}
		if s := st.specialAt(i); s != nil {
			switch s.kind {
			case specialInList:
				lt := "param"
				for j := s.tokStart; j < s.tokEnd; j++ {
					if l, ok := LiteralTypeOf(toks[j]); ok {
						lt = string(l)
						break
					}
				}
				parts = append(parts, "(", ":"+lt+fmt.Sprintf("[%d]", s.count), ")")
				skipUntil = s.tokEnd
				continue
			case specialValuesTuples:
				// Render only the first tuple and collapse the rest into an [n] marker.
				inner, _, err := st.renderTupleInsideAnon(i, mapper, collector)
				if err != nil {
					return "", nil, err
				}
				parts = append(parts, "(")
				parts = append(parts, inner...)
				parts = append(parts, ")", fmt.Sprintf("[%d]", s.count))
				skipUntil = s.tokEnd
				continue
			case specialLimitComma:
				// Anonymized output preserves surface order (limit :int , :int); canonical folding applies only to canonical.
				for j := s.tokStart; j < s.tokEnd; j++ {
					ev, err := st.anonEvent(j, mapper, collector)
					if err != nil {
						return "", nil, err
					}
					parts = append(parts, ev)
				}
				skipUntil = s.tokEnd
				continue
			}
		}
		ev, err := st.anonEvent(i, mapper, collector)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, ev)
	}
	return strings.Join(parts, " "), collector.objects, nil
}

// renderTupleInsideAnon renders anonymous tokens inside the first VALUES tuple; returns the position of the matching ')'.
func (st *renderState) renderTupleInsideAnon(
	openIdx int,
	mapper *privacy.RunMapper,
	collector *objectCollector,
) ([]string, int, error) {
	var out []string
	depth := 0
	for i := openIdx; i < len(st.toks); i++ {
		t := st.toks[i]
		if t.IsPunct("(") {
			depth++
			if i > openIdx {
				out = append(out, "(")
			}
			continue
		}
		if t.IsPunct(")") {
			depth--
			if depth == 0 {
				return out, i, nil
			}
			out = append(out, ")")
			continue
		}
		ev, err := st.anonEvent(i, mapper, collector)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ev)
	}
	return out, len(st.toks), nil
}

// anonEvent outputs the anonymous token text at position i; returns an error on mapping failure.
func (st *renderState) anonEvent(i int, mapper *privacy.RunMapper, collector *objectCollector) (string, error) {
	t := st.toks[i]
	if t.Kind == TokWord {
		if lt, ok := ClassifyWordLiteral(t.Text); ok {
			return ":" + string(lt), nil
		}
	}
	if r := st.roles[i]; r.kind != "" {
		tok, err := roleToken(mapper, r)
		if err != nil {
			// Mapping failure: propagate fail-closed, never outputting raw text or silently losing reversibility.
			return "", err
		}
		if err := collector.addRole(r, tok); err != nil {
			return "", err
		}
		return tok, nil
	}
	switch t.Kind {
	case TokString, TokNumber, TokDollarString, TokParam:
		lt, _ := LiteralTypeOf(t)
		return ":" + string(lt), nil
	case TokWord:
		if lt, ok := ClassifyWordLiteral(t.Text); ok {
			return ":" + string(lt), nil
		}
		if st.keepRaw[i] || isReserved(t.Text) || st.isControlledFunctionPosition(i) {
			return t.Text, nil
		}
		// Defensive path: unclassified bare words are always anonymized.
		tok, err := mapper.Token(privacy.KindGeneric, t.Literal)
		if err != nil {
			return "", err
		}
		return tok, nil
	case TokQuotedIdent:
		tok, err := mapper.Token(privacy.KindGeneric, t.Text)
		if err != nil {
			return "", err
		}
		return tok, nil
	case TokPunct:
		return t.Text, nil
	}
	return t.Text, nil
}

type objectCollector struct {
	mapper  *privacy.RunMapper
	seen    map[string]bool
	objects []ObjectReference
}

func newObjectCollector(mapper *privacy.RunMapper) *objectCollector {
	return &objectCollector{mapper: mapper, seen: map[string]bool{}, objects: []ObjectReference{}}
}

func (c *objectCollector) addRole(role idRole, token string) error {
	object := ObjectReference{
		Token:    token,
		Kind:     string(role.kind),
		Relation: role.relation,
	}
	if object.Relation == "" {
		object.Relation = RelationReference
	}
	if role.table.schema.name != "" {
		schema, err := identityToken(c.mapper, role.table.schema)
		if err != nil {
			return err
		}
		object.SchemaToken = schema
	}
	if role.parent.name != "" {
		parent, err := tableToken(c.mapper, role.parent)
		if err != nil {
			return err
		}
		object.ParentToken = parent
		if object.SchemaToken == "" && role.parent.schema.name != "" {
			schema, err := identityToken(c.mapper, role.parent.schema)
			if err != nil {
				return err
			}
			object.SchemaToken = schema
		}
	}
	key := object.Token + "\x00" + object.Kind + "\x00" + string(object.Relation) +
		"\x00" + object.SchemaToken + "\x00" + object.ParentToken
	if c.seen[key] {
		return nil
	}
	c.seen[key] = true
	c.objects = append(c.objects, object)
	return nil
}

func roleToken(mapper *privacy.RunMapper, role idRole) (string, error) {
	return mapper.ScopedToken(role.kind, roleScope(role.namespace, role.quoted), role.name)
}

func tableToken(mapper *privacy.RunMapper, table tableIdentity) (string, error) {
	kind := privacy.KindTable
	if table.namespace == "cte" {
		kind = privacy.KindCTE
	}
	return mapper.ScopedToken(kind, roleScope(table.namespace, table.quoted), table.name)
}

func identityToken(mapper *privacy.RunMapper, identity identifierIdentity) (string, error) {
	return mapper.ScopedToken(privacy.KindSchema, roleScope("", identity.quoted), identity.name)
}

func roleScope(namespace string, quoted bool) string {
	if namespace != "" {
		if quoted {
			return namespace + ":exact"
		}
		return namespace + ":fold"
	}
	if quoted {
		return privacy.ScopeCaseSensitive
	}
	return ""
}

// isControlledFunctionPosition reports an unqualified controlled built-in
// name immediately followed by '('. A built-in-looking column name must not
// become model-visible merely because it shares a function spelling.
func (st *renderState) isControlledFunctionPosition(i int) bool {
	if i < 0 || i+1 >= len(st.toks) || st.toks[i].Kind != TokWord {
		return false
	}
	if i > 0 && st.toks[i-1].IsPunct(".") {
		return false
	}
	return st.toks[i+1].IsPunct("(") && isBuiltinFunction(st.toks[i].Text)
}

// RenderFallbackAnonymized renders the compatible_lexer fallback form:
// reserved words and controlled built-in function names are preserved, while all other words / quoted identifiers
// are anonymized as generic ident_N; literals remain typed, comments and full Hint text are omitted, and mapping failures propagate errors.
func RenderFallbackAnonymized(lex LexOutput, mapper *privacy.RunMapper) (string, error) {
	st := newRenderState(lex.Tokens, walkResult{})
	var parts []string
	for i := 0; i < len(lex.Tokens); i++ {
		t := lex.Tokens[i]
		switch t.Kind {
		case TokString, TokNumber, TokDollarString, TokParam:
			lt, _ := LiteralTypeOf(t)
			parts = append(parts, ":"+string(lt))
		case TokWord:
			if lt, ok := ClassifyWordLiteral(t.Text); ok {
				parts = append(parts, ":"+string(lt))
				continue
			}
			if isReserved(t.Text) || st.isControlledFunctionPosition(i) {
				parts = append(parts, t.Text)
				continue
			}
			tok, err := mapper.Token(privacy.KindGeneric, t.Literal)
			if err != nil {
				return "", err
			}
			parts = append(parts, tok)
		case TokQuotedIdent:
			tok, err := mapper.Token(privacy.KindGeneric, t.Text)
			if err != nil {
				return "", err
			}
			parts = append(parts, tok)
		case TokPunct:
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " "), nil
}

// FallbackCanonicalSequence outputs the fallback canonical sequence:
// words / quoted identifiers -> generic identifier events (ordinals deduplicated by lowercase name); all else follows canonical rules.
func FallbackCanonicalSequence(lex LexOutput) []string {
	st := newRenderState(lex.Tokens, walkResult{})
	out := make([]string, 0, len(lex.Tokens))
	for i := 0; i < len(lex.Tokens); i++ {
		t := lex.Tokens[i]
		switch t.Kind {
		case TokWord:
			if _, ok := ClassifyWordLiteral(t.Text); ok {
				out = append(out, st.literalEvent(i))
				continue
			}
			if isReserved(t.Text) || st.isControlledFunctionPosition(i) {
				out = append(out, "k:"+t.Text)
				continue
			}
			name := t.Literal
			out = append(out, fmt.Sprintf("r:%s:%d", kindShort[privacy.KindGeneric], st.roleOrdinal(privacy.KindGeneric, name)))
		case TokQuotedIdent:
			out = append(out, fmt.Sprintf("r:%s:%d", kindShort[privacy.KindGeneric], st.roleOrdinal(privacy.KindGeneric, t.Text)))
		default:
			out = append(out, st.canonicalEvent(i))
		}
	}
	return out
}
