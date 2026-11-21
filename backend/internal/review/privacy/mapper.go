// Package privacy provides Run-scoped anonymous identifier mapping, AEAD envelope sealing,
// and outbound leak assertions to keep raw SQL identifiers out of standard Provider requests.
package privacy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// IdentifierKind identifies the SQL object category of an anonymized token.
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
	KindGeneric    IdentifierKind = "ident"
)

// ScopeCaseSensitive marks case-sensitive quoted SQL identifier scopes (vs default case-folding).
const ScopeCaseSensitive = "exact"

// ValidIdentifierKind reports whether k is one of the recognized IdentifierKind constants.
func ValidIdentifierKind(k IdentifierKind) bool {
	switch k {
	case KindDatabase, KindSchema, KindTable, KindColumn, KindIndex, KindConstraint, KindAlias, KindCTE, KindGeneric:
		return true
	}
	return false
}

type mappingEntry struct {
	Kind     IdentifierKind
	Scope    string
	Original string
}

// RunMapper maintains a concurrency-safe bijection between raw scoped identifiers and anonymous tokens.
type RunMapper struct {
	mu        sync.Mutex
	seq       map[IdentifierKind]int
	tokens    map[string]string       // key: kind+"\x00"+scope+"\x00"+normalized original -> token
	originals map[string]mappingEntry // token -> mapping entry
	entries   []mappingEntry          // insertion-ordered entries for deterministic snapshots
}

// NewRunMapper constructs an empty RunMapper ready for tokenization or state restoration.
func NewRunMapper() *RunMapper {
	return &RunMapper{
		seq:       make(map[IdentifierKind]int),
		tokens:    make(map[string]string),
		originals: make(map[string]mappingEntry),
	}
}

// Delimiter characters rejected in identifiers and scopes to preserve the line-oriented TSV mapping format.
const mappingForbiddenRunes = "\t\n\r\x00"

// Token returns the stable anonymous token for original under the default case-folding scope.
func (m *RunMapper) Token(kind IdentifierKind, original string) (string, error) {
	return m.ScopedToken(kind, "", original)
}

// ScopedToken allocates or returns the deterministic anonymous token for (kind, scope, original).
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

// Lookup returns an already-allocated token for (kind, original) without allocating a new one.
func (m *RunMapper) Lookup(kind IdentifierKind, original string) string {
	return m.LookupScoped(kind, "", original)
}

// LookupScoped returns an already-allocated token for (kind, scope, original) without mutating state.
func (m *RunMapper) LookupScoped(kind IdentifierKind, scope, original string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens[identityKey(kind, canonicalScope(scope), original)]
}

// Original resolves an anonymous token back to its kind and raw identifier if registered.
func (m *RunMapper) Original(token string) (IdentifierKind, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.originals[token]
	if !ok {
		return "", "", false
	}
	return e.Kind, e.Original, true
}

// Restore validates and atomically merges persisted MappingPair entries into the mapper without partial mutation on error.
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
			// Identical pre-existing mapping entry; idempotently continue validation.
			continue
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

// Len returns the number of registered identifier mappings.
func (m *RunMapper) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// MappingPair represents one serialized identifier mapping entry stored in an AEAD envelope.
type MappingPair struct {
	Kind     IdentifierKind
	Scope    string
	Original string
	Token    string
}

// Snapshot returns all registered mappings in deterministic insertion order.
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

// MarshalMapping encodes mapping pairs into the versionless tab-delimited format sealed inside AEAD ciphertext.
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

// UnmarshalMapping parses and validates tab-delimited mapping pairs decoded from AEAD plaintext.
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

// SortPairsForToken sorts mapping pairs lexicographically by anonymous token.
func SortPairsForToken(pairs []MappingPair) {
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Token < pairs[j].Token })
}
