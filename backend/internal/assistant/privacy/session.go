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
	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	revprivacy "github.com/cookieY/YearningX/backend/internal/review/privacy"
	"regexp"
	"strings"
	"sync"
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

var (
	uuidLiteralRe      = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	timestampLiteralRe = regexp.MustCompile(`\b[0-9]{4}-[0-9]{2}-[0-9]{2}(?:[ T][0-9]{2}:[0-9]{2}(?::[0-9]{2})?(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:?[0-9]{2})?)?\b`)
	credentialLeakRe   = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret_key|api_key|access_key_secret|private_key)\s*[:=]\s*\S+|(?:postgres|postgresql|mysql)://[^\s]+:[^\s]+@`)
	quotedSingleRe     = regexp.MustCompile(`'([^'\r\n]{1,512})'`)
	quotedDoubleRe     = regexp.MustCompile(`"([^"\r\n]{1,512})"`)
	quotedChineseRe    = regexp.MustCompile("\u201c([^\u201d\\r\\n]{1,512})\u201d")
	kvValueRe          = regexp.MustCompile("([A-Za-z_][A-Za-z0-9_]{0,63})[ \\t]*(?:=|\uff1a|:)[ \\t]*([^\\s,\uff0c\u3002\uff1b;\u3001()\uff08\uff09'\"\u201c\u201d\u300c\u300d:\uff1a/]{1,128})")
	standaloneNumRe    = regexp.MustCompile(`\b[0-9]{2,}(?:\.[0-9]+)?\b`)
	boundNumRe         = regexp.MustCompile(`\b(?:[0-9]+\.[0-9]+|[0-9]{2,}|[2-9])\b`)
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

func (s *TurnPrivacySession) anonymizeText(text, credReason string, includeQuotesAndKV bool) (string, error) {
	if credentialLeakRe.MatchString(text) {
		return "", domain.ErrAnonymizationFailed(credReason)
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

	noTablesRegistered := len(s.authTables) == 0 && len(s.authSchemas) == 0
	if includeQuotesAndKV {
		// 2. Single-quoted and double-quoted business values (mapped to object tokens if matching a registered object name, otherwise :val_str_n)
		replaceQuoted := func(inner string) string {
			trimmed := strings.TrimSpace(inner)
			if trimmed == "" {
				return inner
			}
			lowTrimmed := strings.ToLower(trimmed)
			if isAlreadyAnonTokenOrPlaceholder(lowTrimmed) || strings.Contains(lowTrimmed, ":val_") || (noTablesRegistered && isTrivialBooleanOrKeyword(lowTrimmed)) {
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
			if !noTablesRegistered {
				return replaceQuoted(sub[1])
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
			if isAlreadyAnonTokenOrPlaceholder(lowRhs) || strings.HasPrefix(lowRhs, "val_") || (noTablesRegistered && isTrivialBooleanOrKeyword(lowRhs)) || lowRhs == "true" || lowRhs == "false" || lowRhs == "null" || isSQLKeywordOrGeneric(lowRhs) {
				return m
			}
			if tok := s.lookupRegisteredObjectTokenLocked(rhs); tok != "" {
				return strings.Replace(m, rhs, tok, 1)
			}
			vk := ValString
			if isDigitsOnly(rhs) {
				vk = ValInt
			} else if isDecimalNumber(rhs) {
				vk = ValNum
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
	}

	// 4. Standalone numeric literals:
	//    - When no database tables/schemas are registered (platform_help), preserve public platform caps, error codes, and <=6-digit numbers.
	//    - When database objects are registered (sql_generate / sql_optimize), parameterize business numbers (including public platform caps/codes such as 100/200/500/1000 and single-digit decimals or 2..9 integers in user prompts) so generated SQL uses :val_int_n / :val_num_n without hitting 6004.
	if noTablesRegistered {
		out = standaloneNumRe.ReplaceAllStringFunc(out, func(m string) string {
			if isPublicPlatformCapOrCode(m) || len(m) <= 6 {
				return m
			}
			vk := classifyNumberValueKind(m)
			ph, err := s.registerValueLocked(vk, m)
			if err != nil {
				return m
			}
			return ph
		})
	} else {
		numRe := standaloneNumRe
		if includeQuotesAndKV {
			numRe = boundNumRe
		}
		out = replaceNumbersWithPlaceholders(out, numRe, func(m string, start, end int) (string, bool) {
			if isVersionOrListMarkerNumber(out, start, end) {
				return m, false
			}
			vk := classifyNumberValueKind(m)
			ph, err := s.registerValueLocked(vk, m)
			if err != nil {
				return m, false
			}
			return ph, true
		})
	}

	// 5. Replace registered Schema / Table / Column / Index names and bound literals appearing in natural language.
	out = s.scrubRegisteredLocked(out)
	s.mu.Unlock()

	if err := s.AssertZeroLeak(out); err != nil {
		return "", err
	}
	return out, nil
}

var singleAsciiCompTokenRe = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*|[0-9]+(?:\.[0-9]+)?)$`)

var anonTokenOrPhRe = regexp.MustCompile(`^(?:` + revprivacy.AnonTokenBodyPattern + `|:val_[a-z]+_[0-9]+)$`)

var textRestoreTokenRe = regexp.MustCompile(`(?i):val_(?:str|int|num|bool|ts|uuid|json|hex)_[0-9]+|\b` + revprivacy.AnonTokenBodyPattern + `\b`)
