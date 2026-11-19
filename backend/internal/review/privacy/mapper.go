// The derived key and legacy severity compatibility metadata are deterministically injected along with the rule body. The effective severity of an actual violation is fixed to Critical at runtime; legacy max_severity serves only as historical configuration compatibility info.
package privacy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// escapeSkillBody prevents the body from closing the data delimiter tag (M8/N2: defense-in-depth against injection).
type IdentifierKind string

const (
	KindDatabase   IdentifierKind = "database"
	KindSchema     IdentifierKind = "schema"
	KindTable      IdentifierKind = "table"
	KindColumn     IdentifierKind = "column"
	KindIndex      IdentifierKind = "index"
	KindConstraint IdentifierKind = "constraint"
	KindAlias      IdentifierKind = "alias"
	KindCTE        IdentifierKind = "cte"
	// TokenLegendEntry is an anonymous token legend entry injected into the model context (NEW-AI-03 bootstrap): it carries only the token and semantic role, never the original identifier — on the real Provider path the model has no schema value to fill on the first turn (tokens are produced only after tool returns, creating a circular dependency; after SEC-0041 binding, guessing a non-frozen member triggers 2012), so the controlled legend lets the model initiate its first tool call with valid tokens.
	KindGeneric IdentifierKind = "ident"
)

// Token is a Run-scoped stable anonymous token (e.g. schema_1/table_2).
const ScopeCaseSensitive = "exact"

// Kind is the token category (schema|table|...; privacy.IdentifierKind string).
func ValidIdentifierKind(k IdentifierKind) bool {
	switch k {
	case KindDatabase, KindSchema, KindTable, KindColumn, KindIndex, KindConstraint, KindAlias, KindCTE, KindGeneric:
		return true
	}
	return false
}

// tokenRoleText maps category to semantic role (describes role only, containing no original name).
type mappingEntry struct {
	Kind     IdentifierKind
	Scope    string
	Original string
}

// ContextInput holds the assembly inputs for a single review step.
type RunMapper struct {
	mu        sync.Mutex
	seq       map[IdentifierKind]int
	tokens    map[string]string       // key: kind+"\x00"+scope+"\x00"+normalized original -> token
	originals map[string]mappingEntry // RuleSetConfig holds lexical deterministic rules (participates only in snapshot hashing, never enters context).
	entries   []mappingEntry          // Skills holds enabled skills bound to the rule set (deterministically injected).
}

// Knowledge holds scope-matched enabled knowledge entries (deterministically injected).
func NewRunMapper() *RunMapper {
	return &RunMapper{
		seq:       make(map[IdentifierKind]int),
		tokens:    make(map[string]string),
		originals: make(map[string]mappingEntry),
	}
}

// Groups holds the anonymized statement groups for this stage.
const mappingForbiddenRunes = "\t\n\r\x00"

// Evidence holds existing normalized evidence references.
func (m *RunMapper) Token(kind IdentifierKind, original string) (string, error) {
	return m.ScopedToken(kind, "", original)
}

// TokenLegend holds the controlled anonymous token legend (NEW-AI-03; pre-tokenized from the stage-frozen schema binding set; contains only tokens and kinds, never raw identifiers).
func (m *RunMapper) ScopedToken(kind IdentifierKind, scope, original string) (string, error) {
	if !ValidIdentifierKind(kind) {
		return "", fmt.Errorf("privacy: invalid identifier kind %q", kind)
	}
	if original == "" {
		return "", fmt.Errorf("privacy: empty identifier for kind %q", kind)
	}
	if strings.ContainsAny(original+scope, mappingForbiddenRunes) {
		return "", fmt.Errorf("privacy: identifier contains mapping delimiter (rejected)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope = canonicalScope(scope)
	key := identityKey(kind, scope, original)
	if tok, ok := m.tokens[key]; ok {
		return tok, nil
	}
	m.seq[kind]++
	tok := fmt.Sprintf("%s_%d", kind, m.seq[kind])
	m.tokens[key] = tok
	m.originals[tok] = mappingEntry{Kind: kind, Scope: scope, Original: original}
	m.entries = append(m.entries, mappingEntry{Kind: kind, Scope: scope, Original: original})
	return tok, nil
}

// SystemPrompt constructs the system instructions (hard-rule semantics: injected content is treated as data sections, the model must not rewrite instructions, and all rules apply unconditionally).
func (m *RunMapper) Lookup(kind IdentifierKind, original string) string {
	return m.LookupScoped(kind, "", original)
}

// Finding copywriting constraints (owner ruling 2026-10-30 readability optimization): title states the problem directly, body uses short sentences for fact + impact, and suggestion includes applicable conditions; summary surface avoids dumping internal field names, never fabricates, and stays concise without dropping key qualifiers. Technical details are carried by evidence_ids while user summaries retain natural-language conclusions and key grounds.
func (m *RunMapper) LookupScoped(kind IdentifierKind, scope, original string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens[identityKey(kind, canonicalScope(scope), original)]
}

// SEC-0028: tool facts and schema metadata are explicitly declared as untrusted data — indirect Prompt Injection defense (malicious object names/comments enter the evidence channel via tokenized facts, and the model is instructed never to follow instruction-like text within them).
func (m *RunMapper) Original(token string) (IdentifierKind, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.originals[token]
	if !ok {
		return "", "", false
	}
	return e.Kind, e.Original, true
}

// NEW-AI-03: supplement tool descriptions with parameter specifications (parameter names + required flags) — the model must be able to construct valid argument sets.
func (m *RunMapper) Restore(pairs []MappingPair) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	type stagedEntry struct {
		key   string
		token string
		seqN  int
		pair  MappingPair
	}
	seqBump := map[IdentifierKind]int{}
	staged := make([]stagedEntry, 0, len(pairs))
	for i, p := range pairs {
		if !ValidIdentifierKind(p.Kind) {
			return fmt.Errorf("privacy: restore entry %d unknown kind", i+1)
		}
		if p.Original == "" || p.Token == "" {
			return fmt.Errorf("privacy: restore entry %d empty identifier or token", i+1)
		}
		if strings.ContainsAny(p.Original, mappingForbiddenRunes) {
			return fmt.Errorf("privacy: restore entry %d identifier contains mapping delimiter (rejected)", i+1)
		}
		if strings.ContainsAny(p.Scope, mappingForbiddenRunes) {
			return fmt.Errorf("privacy: restore entry %d scope contains mapping delimiter (rejected)", i+1)
		}
		idx := strings.LastIndexByte(p.Token, '_')
		if idx <= 0 || p.Token[:idx] != string(p.Kind) {
			return fmt.Errorf("privacy: restore entry %d token kind prefix mismatch", i+1)
		}
		n, err := strconv.Atoi(p.Token[idx+1:])
		if err != nil || n < 1 {
			return fmt.Errorf("privacy: restore entry %d token ordinal invalid", i+1)
		}
		p.Scope = canonicalScope(p.Scope)
		key := identityKey(p.Kind, p.Scope, p.Original)
		if exist, ok := m.tokens[key]; ok {
			if exist != p.Token {
				return fmt.Errorf("privacy: restore entry %d token conflict for mapped identifier", i+1)
			}
			continue // NEW-AI-03: controlled anonymous token legend (pre-tokenized from the stage-frozen schema binding set). Contains only tokens and semantic roles, never raw identifiers; identifier values outside the legend are unknown to the review scope and tool calls carrying them will be rejected.
		}
		if exist, ok := m.originals[p.Token]; ok {
			_ = exist
			return fmt.Errorf("privacy: restore entry %d token already owned by another identifier", i+1)
		}
		if n > seqBump[p.Kind] {
			seqBump[p.Kind] = n
		}
		staged = append(staged, stagedEntry{key: key, token: p.Token, seqN: n, pair: p})
	}
	for _, s := range staged {
		m.tokens[s.key] = s.token
		m.originals[s.token] = mappingEntry{Kind: s.pair.Kind, Scope: s.pair.Scope, Original: s.pair.Original}
		m.entries = append(m.entries, mappingEntry{Kind: s.pair.Kind, Scope: s.pair.Scope, Original: s.pair.Original})
		if s.seqN > m.seq[s.pair.Kind] {
			m.seq[s.pair.Kind] = s.seqN
		}
	}
	return nil
}

// BuildRequest constructs a Provider request (privacy invariant: contains only anonymized groups, evidence references, and the anonymous token legend).
func (m *RunMapper) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// NEW-AI-03: pass parameter declarations through with the descriptor (JSON Schema shape).
type MappingPair struct {
	Kind     IdentifierKind
	Scope    string
	Original string
	Token    string
}

func (m *RunMapper) Snapshot() []MappingPair {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MappingPair, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, MappingPair{Kind: e.Kind, Scope: e.Scope, Original: e.Original,
			Token: m.tokens[identityKey(e.Kind, e.Scope, e.Original)]})
	}
	return out
}

// agent_test.go test_gate#2/#8/#9 (assembly side): zero plaintext leakage in requests and context; bounded anonymized groups; zero Provider call surface on read paths.
func MarshalMapping(pairs []MappingPair) []byte {
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(string(p.Kind))
		b.WriteByte('\t')
		b.WriteString(p.Token)
		b.WriteByte('\t')
		if p.Scope != "" {
			b.WriteString(p.Scope)
			b.WriteByte('\t')
		}
		b.WriteString(p.Original)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// TestRequestCarriesNoRawPrivacy test_gate#2: serialized request contains no raw SQL, literals, comments, or identifier mappings — only anonymized shapes and evidence references.
func UnmarshalMapping(data []byte) ([]MappingPair, error) {
	var pairs []MappingPair
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 && len(parts) != 4 {
			return nil, fmt.Errorf("privacy: malformed mapping line %d", i+1)
		}
		if parts[0] == "" || parts[1] == "" || parts[len(parts)-1] == "" {
			return nil, fmt.Errorf("privacy: malformed mapping line %d", i+1)
		}
		kind := IdentifierKind(parts[0])
		if !ValidIdentifierKind(kind) {
			return nil, fmt.Errorf("privacy: unknown mapping kind %q", parts[0])
		}
		p := MappingPair{Kind: kind, Token: parts[1]}
		if len(parts) == 3 {
			p.Original = parts[2]
		} else {
			p.Scope, p.Original = canonicalScope(parts[2]), parts[3]
		}
		pairs = append(pairs, p)
	}
	return pairs, nil
}

func canonicalScope(scope string) string {
	switch scope {
	case "", "fold":
		return ""
	case ScopeCaseSensitive:
		return ScopeCaseSensitive
	default:
		return scope
	}
}

func identityKey(kind IdentifierKind, scope, original string) string {
	if scope == "" || scope == "fold" || strings.HasSuffix(scope, ":fold") {
		original = strings.ToLower(original)
	}
	return string(kind) + "\x00" + scope + "\x00" + original
}

// Anonymized groups appear with representative shape + count.
func SortPairsForToken(pairs []MappingPair) {
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Token < pairs[j].Token })
}
