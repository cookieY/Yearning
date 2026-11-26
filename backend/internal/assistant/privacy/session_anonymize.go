// session_anonymize.go holds session domain helpers and operations.
package privacy

import (
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// NewTurnPrivacySession creates a per-request in-memory anonymization session.
func NewTurnPrivacySession() *TurnPrivacySession {
	return &TurnPrivacySession{
		mapper:        revprivacy.NewRunMapper(),
		valSeq:        make(map[ValueKind]int),
		valByKey:      make(map[string]string),
		valByToken:    make(map[string]BoundValue),
		authSchemas:   make(map[string]string),
		authTables:    make(map[string]string),
		authColumns:   make(map[string]map[string]string),
		authAllCols:   make(map[string]string),
		authIndexes:   make(map[string]string),
		allowedTokens: make(map[string]revprivacy.IdentifierKind),
	}
}

// ZeroAndRelease overwrites and clears all in-memory mappings when a request finishes (success, failure, or cancellation).
func (s *TurnPrivacySession) ZeroAndRelease() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.valByKey {
		delete(s.valByKey, k)
	}
	for k := range s.valByToken {
		s.valByToken[k] = BoundValue{}
		delete(s.valByToken, k)
	}
	for k := range s.authSchemas {
		delete(s.authSchemas, k)
	}
	for k := range s.authTables {
		delete(s.authTables, k)
	}
	for k := range s.authColumns {
		delete(s.authColumns, k)
	}
	for k := range s.authAllCols {
		delete(s.authAllCols, k)
	}
	for k := range s.authIndexes {
		delete(s.authIndexes, k)
	}
	for k := range s.allowedTokens {
		delete(s.allowedTokens, k)
	}
	for i := range s.forbidden {
		s.forbidden[i] = revprivacy.Forbidden{}
	}
	s.forbidden = nil
	s.mapper = revprivacy.NewRunMapper()
	s.released = true
}

// IsReleased reports whether the session has been wiped and released (used by unit tests to verify lifecycle invariants).
func (s *TurnPrivacySession) IsReleased() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
}

func (s *TurnPrivacySession) addForbiddenLocked(val, what string) {
	v := strings.TrimSpace(val)
	if v == "" {
		return
	}
	// Register real identifiers and literals in the forbidden leak list.
	s.forbidden = append(s.forbidden, revprivacy.Forbidden{Value: v, What: what})
}

// RegisterSchema registers an authorized schema and returns its anonymous token (e.g., schema_1).
func (s *TurnPrivacySession) RegisterSchema(schema string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return "", domain.ErrAnonymizationFailed("session_released")
	}
	tok, err := s.mapper.Token(revprivacy.KindSchema, schema)
	if err != nil {
		return "", domain.ErrAnonymizationFailed("invalid_schema_identifier")
	}
	low := strings.ToLower(schema)
	_, already := s.authSchemas[low]
	s.authSchemas[low] = schema
	s.allowedTokens[tok] = revprivacy.KindSchema
	if !already && !isSQLKeywordOrGeneric(schema) {
		s.addForbiddenLocked(schema, "identifier:schema")
	}
	return tok, nil
}

// RegisterTable registers an authorized table and returns its anonymous token (e.g., table_1).
func (s *TurnPrivacySession) RegisterTable(schema, table string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return "", domain.ErrAnonymizationFailed("session_released")
	}
	if schema != "" {
		if st, err := s.mapper.Token(revprivacy.KindSchema, schema); err == nil {
			s.authSchemas[strings.ToLower(schema)] = schema
			s.allowedTokens[st] = revprivacy.KindSchema
			if !isSQLKeywordOrGeneric(schema) {
				s.addForbiddenLocked(schema, "identifier:schema")
			}
		}
	}
	tok, err := s.mapper.Token(revprivacy.KindTable, table)
	if err != nil {
		return "", domain.ErrAnonymizationFailed("invalid_table_identifier")
	}
	lowT := strings.ToLower(table)
	s.authTables[lowT] = table
	if s.authColumns[lowT] == nil {
		s.authColumns[lowT] = make(map[string]string)
	}
	s.allowedTokens[tok] = revprivacy.KindTable
	if !isSQLKeywordOrGeneric(table) {
		s.addForbiddenLocked(table, "identifier:table")
	}
	return tok, nil
}

// RegisterColumn registers an authorized column and returns its anonymous token (e.g., column_1).
func (s *TurnPrivacySession) RegisterColumn(schema, table, column string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return "", domain.ErrAnonymizationFailed("session_released")
	}
	tok, err := s.mapper.Token(revprivacy.KindColumn, column)
	if err != nil {
		return "", domain.ErrAnonymizationFailed("invalid_column_identifier")
	}
	lowT := strings.ToLower(table)
	lowC := strings.ToLower(column)
	if lowT != "" {
		if s.authColumns[lowT] == nil {
			s.authColumns[lowT] = make(map[string]string)
		}
		s.authColumns[lowT][lowC] = column
	}
	s.authAllCols[lowC] = column
	s.allowedTokens[tok] = revprivacy.KindColumn
	if !isSQLKeywordOrGeneric(column) {
		s.addForbiddenLocked(column, "identifier:column")
	}
	return tok, nil
}

// RegisterIndex registers an authorized index and returns its anonymous token (e.g., index_1).
func (s *TurnPrivacySession) RegisterIndex(schema, table, index string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return "", domain.ErrAnonymizationFailed("session_released")
	}
	tok, err := s.mapper.Token(revprivacy.KindIndex, index)
	if err != nil {
		return "", domain.ErrAnonymizationFailed("invalid_index_identifier")
	}
	s.authIndexes[strings.ToLower(index)] = index
	s.allowedTokens[tok] = revprivacy.KindIndex
	if !isSQLKeywordOrGeneric(index) {
		s.addForbiddenLocked(index, "identifier:index")
	}
	return tok, nil
}

// RegisterValue allocates a distinguishable typed placeholder (e.g., :val_str_1, :val_str_2) for a business literal.
// Within the same turn, identical (kind, raw) pairs reuse the same placeholder while distinct values receive distinct ordinals.
func (s *TurnPrivacySession) RegisterValue(kind ValueKind, raw string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return "", domain.ErrAnonymizationFailed("session_released")
	}
	return s.registerValueLocked(kind, raw)
}

func (s *TurnPrivacySession) registerValueLocked(kind ValueKind, raw string) (string, error) {
	if kind == "" {
		kind = ValString
	}
	key := string(kind) + "\x00" + raw
	if ph, ok := s.valByKey[key]; ok {
		return ph, nil
	}
	s.valSeq[kind]++
	ph := fmt.Sprintf(":val_%s_%d", kind, s.valSeq[kind])
	s.valByKey[key] = ph
	s.valByToken[ph] = BoundValue{
		Placeholder: ph,
		Kind:        kind,
		RawValue:    raw,
	}
	// Strings, UUIDs, timestamps, JSON, hex, and long numbers (>= 6 digits) are added to the forbidden leak list (avoiding false positives on short structural numbers such as varchar(64), positional parameter markers ?/$1, or public platform limits).
	if kind == ValString || kind == ValUUID || kind == ValTimestamp || kind == ValJSON || kind == ValHex || (len(raw) >= 6 && !isPublicPlatformCapOrCode(raw)) {
		if !isTrivialBooleanOrKeyword(raw) && !isPositionalParamPlaceholder(raw) {
			s.addForbiddenLocked(raw, "literal:"+string(kind))
		}
	}
	return ph, nil
}

func isPositionalParamPlaceholder(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "?" {
		return true
	}
	if len(trimmed) >= 2 && trimmed[0] == '$' && isDigitsOnly(trimmed[1:]) {
		return true
	}
	return false
}

// LookupIdentifier looks up (kind, original identifier, authorized) by anonymous token.
func (s *TurnPrivacySession) LookupIdentifier(token string) (revprivacy.IdentifierKind, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return "", "", false
	}
	kind, orig, ok := s.mapper.Original(token)
	if !ok {
		return "", "", false
	}
	if _, allowed := s.allowedTokens[token]; !allowed {
		// Alias / CTE tokens are valid within a single statement.
		if kind != revprivacy.KindAlias && kind != revprivacy.KindCTE {
			return "", "", false
		}
	}
	return kind, orig, true
}

// LookupValue looks up the bound raw value by placeholder (e.g., :val_str_1).
func (s *TurnPrivacySession) LookupValue(placeholder string) (BoundValue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return BoundValue{}, false
	}
	v, ok := s.valByToken[strings.ToLower(placeholder)]
	return v, ok
}

// AuthorizedTables returns the sorted list of real table names authorized in this turn.
func (s *TurnPrivacySession) AuthorizedTables() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.authTables))
	for _, t := range s.authTables {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// anonymizeIdentifierContextualLocked maps an identifier to an anonymous token using surrounding lexical context and the registered catalog.
func (s *TurnPrivacySession) anonymizeIdentifierContextualLocked(ident string, toks []fingerprint.Token, idx int) (string, error) {
	clean := strings.TrimSpace(ident)
	if clean == "" {
		return "", domain.ErrAnonymizationFailed("empty_identifier")
	}
	low := strings.ToLower(clean)

	// 1. Prefer matching object types in the authorized catalog (combining dot-qualification context).
	if idx+1 < len(toks) && toks[idx+1].IsPunct(".") {
		if _, ok := s.authSchemas[low]; ok {
			return s.mapper.Token(revprivacy.KindSchema, clean)
		}
		if _, ok := s.authTables[low]; ok {
			return s.mapper.Token(revprivacy.KindTable, clean)
		}
	}
	if prevIsTableContext(toks, idx) {
		tok, err := s.mapper.Token(revprivacy.KindTable, clean)
		if err != nil {
			return "", domain.ErrAnonymizationFailed("table_token_failed")
		}
		s.allowedTokens[tok] = revprivacy.KindTable
		if !isSQLKeywordOrGeneric(clean) {
			s.addForbiddenLocked(clean, "identifier:table")
		}
		return tok, nil
	}
	if prevIsIndexContext(toks, idx) {
		tok, err := s.mapper.Token(revprivacy.KindIndex, clean)
		if err != nil {
			return "", domain.ErrAnonymizationFailed("index_token_failed")
		}
		s.allowedTokens[tok] = revprivacy.KindIndex
		if !isSQLKeywordOrGeneric(clean) {
			s.addForbiddenLocked(clean, "identifier:index")
		}
		return tok, nil
	}
	if _, ok := s.authTables[low]; ok {
		return s.mapper.Token(revprivacy.KindTable, clean)
	}
	if _, ok := s.authAllCols[low]; ok {
		return s.mapper.Token(revprivacy.KindColumn, clean)
	}
	if _, ok := s.authSchemas[low]; ok {
		return s.mapper.Token(revprivacy.KindSchema, clean)
	}
	if _, ok := s.authIndexes[low]; ok {
		return s.mapper.Token(revprivacy.KindIndex, clean)
	}
	if prevIsAliasContext(toks, idx) {
		tok, err := s.mapper.Token(revprivacy.KindAlias, clean)
		if err != nil {
			return "", domain.ErrAnonymizationFailed("alias_token_failed")
		}
		if !isSQLKeywordOrGeneric(clean) {
			s.addForbiddenLocked(clean, "identifier:alias")
		}
		return tok, nil
	}

	// Default to classifying as a column identifier (unregistered column names in SELECT/WHERE/INSERT/UPDATE lists).
	tok, err := s.mapper.Token(revprivacy.KindColumn, clean)
	if err != nil {
		return "", domain.ErrAnonymizationFailed("column_token_failed")
	}
	s.allowedTokens[tok] = revprivacy.KindColumn
	if !isSQLKeywordOrGeneric(clean) {
		s.addForbiddenLocked(clean, "identifier:column")
	}
	return tok, nil
}

// AnonymizeNaturalLanguage applies the same per-turn anonymization pipeline to natural-language task descriptions:
//  1. Rejects inputs containing connection strings or password credentials (6003 Fail Closed);
//  2. Replaces quoted strings, key=value business literals, UUIDs, timestamps, and numbers with :val_<kind>_<n>;
//  3. Replaces registered database/schema/table/column/index names appearing in text with their corresponding schema_N/table_N/column_N tokens;
//  4. Asserts zero leakage.
func (s *TurnPrivacySession) AnonymizeNaturalLanguage(text string) (string, error) {
	return s.anonymizeText(text, "credential_detected_in_prompt", true)
}

// AnonymizeAssistantHistory anonymizes an assistant-authored history turn by rejecting credential leaks,
// replacing UUIDs, timestamps, and numbers, and scrubbing all registered database object identifiers and
// bound user literals without registering assistant Markdown prose quotes as new forbidden database literals.
func (s *TurnPrivacySession) AnonymizeAssistantHistory(text string) (string, error) {
	return s.anonymizeText(text, "credential_detected_in_history", false)
}

// ScrubRegisteredTerms replaces any newly registered schema/table/column/index names and bound literals
// across already-anonymized turn strings so multi-message history stays consistent with the turn's forbidden set.
func (s *TurnPrivacySession) ScrubRegisteredTerms(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return text
	}
	return s.scrubRegisteredLocked(text)
}

func (s *TurnPrivacySession) scrubRegisteredLocked(out string) string {
	type objPair struct {
		raw string
		tok string
	}
	var pairs []objPair
	for low, orig := range s.authSchemas {
		if tok := s.mapper.Lookup(revprivacy.KindSchema, orig); tok != "" {
			pairs = append(pairs, objPair{raw: orig, tok: tok}, objPair{raw: low, tok: tok})
		}
	}
	for low, orig := range s.authTables {
		if tok := s.mapper.Lookup(revprivacy.KindTable, orig); tok != "" {
			pairs = append(pairs, objPair{raw: orig, tok: tok}, objPair{raw: low, tok: tok})
		}
	}
	for low, orig := range s.authAllCols {
		if tok := s.mapper.Lookup(revprivacy.KindColumn, orig); tok != "" {
			pairs = append(pairs, objPair{raw: orig, tok: tok}, objPair{raw: low, tok: tok})
		}
	}
	for low, orig := range s.authIndexes {
		if tok := s.mapper.Lookup(revprivacy.KindIndex, orig); tok != "" {
			pairs = append(pairs, objPair{raw: orig, tok: tok}, objPair{raw: low, tok: tok})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		return len(pairs[i].raw) > len(pairs[j].raw)
	})
	for _, p := range pairs {
		if p.raw == "" {
			continue
		}
		out = replaceAsciiIdentifierBoundary(out, p.raw, p.tok)
	}

	var valPairs []objPair
	for _, bv := range s.valByToken {
		raw := strings.TrimSpace(bv.RawValue)
		if raw == "" || isTrivialBooleanOrKeyword(raw) {
			continue
		}
		if bv.Kind == ValString || bv.Kind == ValUUID || bv.Kind == ValTimestamp || bv.Kind == ValJSON || bv.Kind == ValHex || len(raw) >= 6 {
			valPairs = append(valPairs, objPair{raw: raw, tok: bv.Placeholder})
		}
	}
	sort.Slice(valPairs, func(i, j int) bool {
		if len(valPairs[i].raw) != len(valPairs[j].raw) {
			return len(valPairs[i].raw) > len(valPairs[j].raw)
		}
		return valPairs[i].tok < valPairs[j].tok
	})
	for _, vp := range valPairs {
		if isSingleAsciiComparisonToken(vp.raw) {
			out = replaceAsciiIdentifierBoundary(out, vp.raw, vp.tok)
		} else {
			out = strings.ReplaceAll(out, "'"+vp.raw+"'", vp.tok)
			out = strings.ReplaceAll(out, `"`+vp.raw+`"`, vp.tok)
			out = strings.ReplaceAll(out, "“"+vp.raw+"”", vp.tok)
			out = replaceCaseInsensitiveSubstring(out, vp.raw, vp.tok)
		}
	}
	return out
}

func isSingleAsciiComparisonToken(s string) bool {
	return singleAsciiCompTokenRe.MatchString(s)
}

func replaceCaseInsensitiveSubstring(text, raw, tok string) string {
	if raw == "" || text == "" {
		return text
	}
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(raw))
	return pattern.ReplaceAllString(text, tok)
}

func (s *TurnPrivacySession) lookupRegisteredObjectTokenLocked(word string) string {
	low := strings.ToLower(strings.Trim(word, "`\"'"))
	if orig, ok := s.authTables[low]; ok {
		return s.mapper.Lookup(revprivacy.KindTable, orig)
	}
	if orig, ok := s.authAllCols[low]; ok {
		return s.mapper.Lookup(revprivacy.KindColumn, orig)
	}
	if orig, ok := s.authSchemas[low]; ok {
		return s.mapper.Lookup(revprivacy.KindSchema, orig)
	}
	if orig, ok := s.authIndexes[low]; ok {
		return s.mapper.Lookup(revprivacy.KindIndex, orig)
	}
	return ""
}

// AssertZeroLeak verifies that the outbound payload sent to a model contains no registered real identifiers, SQL literals, comments, or credentials.
func (s *TurnPrivacySession) AssertZeroLeak(outbound string) error {
	if credentialLeakRe.MatchString(outbound) {
		return domain.ErrAnonymizationFailed("credential_leak_detected")
	}
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return domain.ErrAnonymizationFailed("session_released")
	}
	forbiddenCopy := make([]revprivacy.Forbidden, len(s.forbidden))
	copy(forbiddenCopy, s.forbidden)
	s.mu.Unlock()

	if err := revprivacy.AssertNoLeakValues([]string{outbound}, forbiddenCopy); err != nil {
		return domain.ErrAnonymizationFailed(err.Error())
	}
	return nil
}

func classifyStringValueKind(text string, prefix byte) ValueKind {
	if prefix == 'X' || prefix == 'B' || prefix == 'x' || prefix == 'b' {
		return ValHex
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") ||
		strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return ValJSON
	}
	switch fingerprint.ClassifyString(trimmed, prefix) {
	case fingerprint.LitUUID:
		return ValUUID
	case fingerprint.LitDate, fingerprint.LitTimestamp:
		return ValTimestamp
	case fingerprint.LitBinary:
		return ValHex
	default:
		return ValString
	}
}

func classifyNumberValueKind(text string) ValueKind {
	switch fingerprint.ClassifyNumber(text) {
	case fingerprint.LitBinary:
		return ValHex
	case fingerprint.LitDecimal:
		return ValNum
	default:
		return ValInt
	}
}

func isDigitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isDecimalNumber(s string) bool {
	dot := strings.IndexByte(s, '.')
	if dot <= 0 || dot >= len(s)-1 {
		return false
	}
	return isDigitsOnly(s[:dot]) && isDigitsOnly(s[dot+1:])
}

func replaceNumbersWithPlaceholders(text string, re *regexp.Regexp, fn func(match string, start, end int) (string, bool)) string {
	matches := re.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		repl, ok := fn(text[start:end], start, end)
		if !ok {
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString(repl)
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

// isVersionOrListMarkerNumber skips numeric fragments that belong to dotted version triples
// (such as v4.0.0 / 4.0.0) or single-digit line-start list markers (such as "2. " or "3) ").
func isVersionOrListMarkerNumber(text string, start, end int) bool {
	if start > 0 && text[start-1] == '.' {
		return true
	}
	if end+1 < len(text) && text[end] == '.' && text[end+1] >= '0' && text[end+1] <= '9' {
		return true
	}
	if end-start == 1 {
		// Check whether this single digit is a Markdown or Chinese list marker at the start of a line.
		lineStart := true
		for i := start - 1; i >= 0; i-- {
			c := text[i]
			if c == '\n' || c == '\r' {
				break
			}
			if c != ' ' && c != '\t' {
				lineStart = false
				break
			}
		}
		if lineStart && end < len(text) {
			rest := text[end:]
			if strings.HasPrefix(rest, ". ") || strings.HasPrefix(rest, ".\t") ||
				strings.HasPrefix(rest, ") ") || strings.HasPrefix(rest, ")\t") ||
				strings.HasPrefix(rest, "\u3001") || strings.HasPrefix(rest, "\uff09") {
				return true
			}
		}
	}
	return false
}

func isTrivialBooleanOrKeyword(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "false", "null",
		"result_unknown", "evidence_incomplete", "changes_requested", "pending_approval",
		"rollback_in_progress", "rolled_back", "rollback_failed", "rollback_enabled",
		"gh-ost", "pt-osc", "pt-online-schema-change", "any_one", "all_must_approve",
		"confirm_succeeded", "confirm_failed", "can_export", "can_query",
		"degrade_builtin", "allow_with_warning", "query_access", "change_review",
		"performance_schema", "pg_stat_statements", "sql_generate", "sql_optimize",
		"platform_help", "available", "unsupported", "conflicted", "approved",
		"scheduled", "executing", "succeeded", "failed", "rejected", "withdrawn",
		"draft", "ready", "blocked", "dml", "ddl", "osc", "oidc", "csv", "jsonl",
		"mysql", "postgresql", "mariadb", "tidb":
		return true
	}
	return false
}

func isPublicPlatformCapOrCode(s string) bool {
	trimmed := strings.TrimSpace(s)
	switch trimmed {
	case "100", "200", "500", "512", "1000", "1800", "3306", "3600", "5000", "5432",
		"10000", "21600", "50000", "100000", "524288", "33554432":
		return true
	}
	if len(trimmed) == 4 && isDigitsOnly(trimmed) {
		prefix := trimmed[:2]
		switch prefix {
		case "10", "20", "30", "40", "50", "60", "61":
			return true
		}
	}
	return false
}

func isAlreadyAnonTokenOrPlaceholder(w string) bool {
	return anonTokenOrPhRe.MatchString(w)
}

// replaceAsciiIdentifierBoundary replaces raw object names on full ASCII word boundaries without corrupting existing anonymous tokens (such as table_1).
func replaceAsciiIdentifierBoundary(text, raw, tok string) string {
	if raw == "" {
		return text
	}
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(raw))
	matches := pattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > 0 && isIdentBoundaryRune(rune(text[start-1])) {
			continue
		}
		if end < len(text) && isIdentBoundaryRune(rune(text[end])) {
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString(tok)
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

func isIdentBoundaryRune(r rune) bool {
	return r == '_' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') && unicode.IsPrint(r)
}

// RestoreTextTokens restores per-turn anonymous object tokens and value placeholders in natural-language/Markdown prose back to their original identifiers and literals.
// Note: SQL drafts themselves must always be structurally restored via ValidateAndRestoreSQL along lexical/syntactic boundaries.
func (s *TurnPrivacySession) RestoreTextTokens(text string) string {
	if s == nil || text == "" {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return text
	}
	return textRestoreTokenRe.ReplaceAllStringFunc(text, func(match string) string {
		low := strings.ToLower(match)
		if strings.HasPrefix(low, ":val_") {
			if bv, ok := s.valByToken[low]; ok {
				return bv.RawValue
			}
			return match
		}
		if _, orig, ok := s.mapper.Original(low); ok && orig != "" {
			return orig
		}
		return match
	})
}

// RestoreTextSlice applies natural-language anonymous token restoration to each item in a string slice.
func (s *TurnPrivacySession) RestoreTextSlice(items []string) []string {
	if len(items) == 0 {
		return items
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = s.RestoreTextTokens(item)
	}
	return out
}
