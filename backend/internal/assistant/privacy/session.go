// Package privacy implements the per-turn in-memory anonymization session (TurnPrivacySession)
// and structured SQL restorer for the Database AI Assistant.
//
// Security invariants (PRD §5.6 / §5.7):
//  1. Before every model invocation (including generation, optimization, session compaction, retries,
//     and provider fallback), the server must convert real database names, schema names, table names,
//     column names, index names, SQL literals, and sensitive natural-language values into per-turn
//     anonymous tokens and distinguishable typed placeholders.
//  2. Comments are stripped at the lexical layer and added to the forbidden leak set; if credential text
//     or unredacted residue is detected, outbound requests fail closed immediately with 6003.
//  3. Anonymization mappings reside strictly in per-request server memory and are wiped by ZeroAndRelease
//     when the request completes; they are never persisted to the database, written to logs, or returned to the frontend.
package privacy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// ValueKind is the category of a distinguishable typed value placeholder (:val_<kind>_<n>).
type ValueKind string

const (
	ValString    ValueKind = "str"
	ValInt       ValueKind = "int"
	ValNum       ValueKind = "num"
	ValBool      ValueKind = "bool"
	ValTimestamp ValueKind = "ts"
	ValUUID      ValueKind = "uuid"
	ValJSON      ValueKind = "json"
	ValHex       ValueKind = "hex"
)

// BoundValue records the raw literal value and kind bound to a typed placeholder.
type BoundValue struct {
	Placeholder string
	Kind        ValueKind
	RawValue    string
}

// TurnPrivacySession is an in-memory anonymization session scoped to a single HTTP request lifecycle.
type TurnPrivacySession struct {
	mu       sync.Mutex
	released bool

	mapper *revprivacy.RunMapper

	valSeq     map[ValueKind]int
	valByKey   map[string]string     // kind + "\x00" + raw -> :val_<kind>_<n>
	valByToken map[string]BoundValue // :val_<kind>_<n> -> BoundValue

	// Authorized real object sets (lowercased key -> original name), used for natural-language recognition and restoration authorization checks.
	authSchemas map[string]string
	authTables  map[string]string            // lower(table) -> original table
	authColumns map[string]map[string]string // lower(table) -> lower(col) -> original col
	authAllCols map[string]string            // lower(col) -> original col
	authIndexes map[string]string            // lower(index) -> original index

	// Reverse token -> authorized ownership verification.
	allowedTokens map[string]revprivacy.IdentifierKind

	// Raw sensitive terms forbidden from appearing in any outbound model payload.
	forbidden []revprivacy.Forbidden
}

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
	s.authSchemas[strings.ToLower(schema)] = schema
	s.allowedTokens[tok] = revprivacy.KindSchema
	if !isSQLKeywordOrGeneric(schema) {
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

var (
	uuidLiteralRe      = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	timestampLiteralRe = regexp.MustCompile(`\b[0-9]{4}-[0-9]{2}-[0-9]{2}(?:[ T][0-9]{2}:[0-9]{2}(?::[0-9]{2})?(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:?[0-9]{2})?)?\b`)
	credentialLeakRe   = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret_key|api_key|access_key_secret|private_key)\s*[:=]\s*\S+|(?:postgres|postgresql|mysql)://[^\s]+:[^\s]+@`)
	quotedSingleRe     = regexp.MustCompile(`'([^'\r\n]{1,512})'`)
	quotedDoubleRe     = regexp.MustCompile(`"([^"\r\n]{1,512})"`)
	quotedChineseRe    = regexp.MustCompile("\u201c([^\u201d\\r\\n]{1,512})\u201d")
	kvValueRe          = regexp.MustCompile("([A-Za-z_][A-Za-z0-9_]{0,63})\\s*(?:=|\uff1a|:)\\s*([^\\s,\uff0c\u3002\uff1b;\u3001()\uff08\uff09'\"\u201c\u201d\u300c\u300d:\uff1a/]{1,128})")
	standaloneNumRe    = regexp.MustCompile(`\b[0-9]{2,}(?:\.[0-9]+)?\b`)
)

// AnonymizeSQL lexically scans input SQL according to the target dialect, strips all comments,
// replaces identifiers and literals with per-turn anonymous tokens and distinguishable typed
// placeholders (:val_<kind>_<n>), and asserts zero leakage.
func (s *TurnPrivacySession) AnonymizeSQL(sqlText string, profile fingerprint.Profile) (string, error) {
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return "", domain.ErrAnonymizationFailed("session_released")
	}
	s.mu.Unlock()

	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return "", domain.ErrAnonymizationFailed("empty_sql")
	}
	if err := profile.Validate(); err != nil {
		return "", domain.ErrAnonymizationFailed("unsupported_dialect_profile")
	}
	lex, err := fingerprint.Tokenize([]byte(trimmed), profile.Flags())
	if err != nil || len(lex.Tokens) == 0 {
		return "", domain.ErrAnonymizationFailed("sql_tokenize_failed")
	}

	s.mu.Lock()
	for _, c := range lex.Comments {
		if strings.TrimSpace(c) != "" {
			s.addForbiddenLocked(c, "comment")
		}
	}

	var parts []string
	toks := lex.Tokens
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.IsPunct(";") {
			if i != len(toks)-1 {
				s.mu.Unlock()
				return "", domain.ErrAnonymizationFailed("multi_statement_forbidden")
			}
			continue
		}
		switch t.Kind {
		case fingerprint.TokString, fingerprint.TokDollarString:
			vk := classifyStringValueKind(t.Text, t.Prefix)
			ph, err := s.registerValueLocked(vk, t.Text)
			if err != nil {
				s.mu.Unlock()
				return "", err
			}
			parts = append(parts, ph)
		case fingerprint.TokNumber:
			vk := classifyNumberValueKind(t.Text)
			ph, err := s.registerValueLocked(vk, t.Text)
			if err != nil {
				s.mu.Unlock()
				return "", err
			}
			parts = append(parts, ph)
		case fingerprint.TokParam:
			ph, err := s.registerValueLocked(ValString, t.Text)
			if err != nil {
				s.mu.Unlock()
				return "", err
			}
			parts = append(parts, ph)
		case fingerprint.TokQuotedIdent:
			tok, err := s.anonymizeIdentifierContextualLocked(t.Text, toks, i)
			if err != nil {
				s.mu.Unlock()
				return "", err
			}
			parts = append(parts, tok)
		case fingerprint.TokWord:
			low := strings.ToLower(t.Text)
			if low == "null" {
				parts = append(parts, "NULL")
				continue
			}
			if low == "true" || low == "false" {
				ph, err := s.registerValueLocked(ValBool, low)
				if err != nil {
					s.mu.Unlock()
					return "", err
				}
				parts = append(parts, ph)
				continue
			}
			if isAlreadyAnonTokenOrPlaceholder(low) {
				parts = append(parts, low)
				continue
			}
			if isSQLKeywordForAnon(low, toks, i) {
				parts = append(parts, strings.ToUpper(low))
				continue
			}
			tok, err := s.anonymizeIdentifierContextualLocked(t.Literal, toks, i)
			if err != nil {
				s.mu.Unlock()
				return "", err
			}
			parts = append(parts, tok)
		case fingerprint.TokPunct:
			parts = append(parts, t.Text)
		default:
			s.mu.Unlock()
			return "", domain.ErrAnonymizationFailed("unknown_token_kind")
		}
	}
	s.mu.Unlock()

	anonSQL := strings.Join(parts, " ")
	if err := s.AssertZeroLeak(anonSQL); err != nil {
		return "", err
	}
	return anonSQL, nil
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
	if credentialLeakRe.MatchString(text) {
		return "", domain.ErrAnonymizationFailed("credential_detected_in_prompt")
	}
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return "", domain.ErrAnonymizationFailed("session_released")
	}

	out := text

	// 1. UUIDs and timestamps
	out = uuidLiteralRe.ReplaceAllStringFunc(out, func(m string) string {
		ph, err := s.registerValueLocked(ValUUID, m)
		if err != nil {
			return m
		}
		return ph
	})
	out = timestampLiteralRe.ReplaceAllStringFunc(out, func(m string) string {
		ph, err := s.registerValueLocked(ValTimestamp, m)
		if err != nil {
			return m
		}
		return ph
	})

	// 2. Single-quoted and double-quoted business values (mapped to object tokens if matching a registered object name, otherwise :val_str_n)
	replaceQuoted := func(inner string) string {
		trimmed := strings.TrimSpace(inner)
		if trimmed == "" {
			return inner
		}
		lowTrimmed := strings.ToLower(trimmed)
		if isAlreadyAnonTokenOrPlaceholder(lowTrimmed) || strings.Contains(lowTrimmed, ":val_") || isTrivialBooleanOrKeyword(lowTrimmed) {
			return trimmed
		}
		if tok := s.lookupRegisteredObjectTokenLocked(trimmed); tok != "" {
			return tok
		}
		ph, err := s.registerValueLocked(ValString, trimmed)
		if err != nil {
			return inner
		}
		return ph
	}
	out = quotedSingleRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := quotedSingleRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		return replaceQuoted(sub[1])
	})
	out = quotedDoubleRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := quotedDoubleRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		return replaceQuoted(sub[1])
	})
	out = quotedChineseRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := quotedChineseRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		trimmed := strings.TrimSpace(sub[1])
		if tok := s.lookupRegisteredObjectTokenLocked(trimmed); tok != "" {
			return tok
		}
		if ph, ok := s.valByKey[string(ValString)+"\x00"+trimmed]; ok {
			return ph
		}
		return m
	})

	// 3. key=value patterns in natural language
	out = kvValueRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := kvValueRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		lhs, rhs := sub[1], sub[2]
		lowLhs := strings.ToLower(lhs)
		lowRhs := strings.ToLower(rhs)
		if isAlreadyAnonTokenOrPlaceholder(lowLhs) || strings.HasPrefix(lowLhs, "val_") {
			return m
		}
		if isAlreadyAnonTokenOrPlaceholder(lowRhs) || strings.HasPrefix(lowRhs, "val_") || isTrivialBooleanOrKeyword(lowRhs) {
			return m
		}
		if tok := s.lookupRegisteredObjectTokenLocked(rhs); tok != "" {
			return strings.Replace(m, rhs, tok, 1)
		}
		vk := ValString
		if classifyNumberValueKind(rhs) == ValInt && isDigitsOnly(rhs) {
			vk = ValInt
		}
		ph, err := s.registerValueLocked(vk, rhs)
		if err != nil {
			return m
		}
		lhsAnon := lhs
		if tok := s.lookupRegisteredObjectTokenLocked(lhs); tok != "" {
			lhsAnon = tok
		}
		return lhsAnon + " = " + ph
	})

	// 4. Standalone multi-digit numbers (preserve public platform numbers and error codes when no database tables are registered)
	noTablesRegistered := len(s.authTables) == 0 && len(s.authSchemas) == 0
	out = standaloneNumRe.ReplaceAllStringFunc(out, func(m string) string {
		if isPublicPlatformCapOrCode(m) || (noTablesRegistered && len(m) <= 6) {
			return m
		}
		vk := classifyNumberValueKind(m)
		ph, err := s.registerValueLocked(vk, m)
		if err != nil {
			return m
		}
		return ph
	})

	// 5. Replace registered Schema / Table / Column / Index names and bound literals appearing in natural language.
	out = s.scrubRegisteredLocked(out)
	s.mu.Unlock()

	if err := s.AssertZeroLeak(out); err != nil {
		return "", err
	}
	return out, nil
}

// AnonymizeAssistantHistory anonymizes an assistant-authored history turn by rejecting credential leaks,
// replacing UUIDs, timestamps, and numbers, and scrubbing all registered database object identifiers and
// bound user literals without registering assistant Markdown prose quotes as new forbidden database literals.
func (s *TurnPrivacySession) AnonymizeAssistantHistory(text string) (string, error) {
	if credentialLeakRe.MatchString(text) {
		return "", domain.ErrAnonymizationFailed("credential_detected_in_history")
	}
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return "", domain.ErrAnonymizationFailed("session_released")
	}

	out := text
	out = uuidLiteralRe.ReplaceAllStringFunc(out, func(m string) string {
		ph, err := s.registerValueLocked(ValUUID, m)
		if err != nil {
			return m
		}
		return ph
	})
	out = timestampLiteralRe.ReplaceAllStringFunc(out, func(m string) string {
		ph, err := s.registerValueLocked(ValTimestamp, m)
		if err != nil {
			return m
		}
		return ph
	})
	noTablesRegistered := len(s.authTables) == 0 && len(s.authSchemas) == 0
	out = standaloneNumRe.ReplaceAllStringFunc(out, func(m string) string {
		if isPublicPlatformCapOrCode(m) || (noTablesRegistered && len(m) <= 6) {
			return m
		}
		vk := classifyNumberValueKind(m)
		ph, err := s.registerValueLocked(vk, m)
		if err != nil {
			return m
		}
		return ph
	})
	out = s.scrubRegisteredLocked(out)
	s.mu.Unlock()

	if err := s.AssertZeroLeak(out); err != nil {
		return "", err
	}
	return out, nil
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

var singleAsciiCompTokenRe = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*|[0-9]+(?:\.[0-9]+)?)$`)

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

var anonTokenOrPhRe = regexp.MustCompile(`^(?:(?:database|schema|table|column|index|constraint|alias|cte|ident)_[0-9]+|:val_[a-z]+_[0-9]+)$`)

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

var textRestoreTokenRe = regexp.MustCompile(`:val_(?:str|int|num|bool|ts|uuid|json|hex)_[0-9]+|\b(?:database|schema|table|column|index|constraint|alias|cte|ident)_[0-9]+\b`)

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
