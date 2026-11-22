package privacy

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Forbidden marks a raw secret/identifier value that must never appear in anonymized outbound text.
// What classifies the error without ever logging or returning the raw Value.
type Forbidden struct {
	Value string
	What  string
}

// AnonTokenBodyPattern is the canonical anonymous database object token body regex.
const AnonTokenBodyPattern = `(?:database|schema|table|column|index|constraint|alias|cte|ident)_[0-9]+`

// AnonTokenPattern matches safe Run-scoped anonymous tokens emitted by the privacy layer.
var AnonTokenPattern = regexp.MustCompile(`^` + AnonTokenBodyPattern + `$`)

// anonymizedForbiddenChars rejects unnormalized string delimiters and SQL comment starters.
// Hash characters (#) remain permitted for PostgreSQL JSON path operators (#>, #>>, #-).
var anonymizedForbiddenChars = []string{"'", `"`, "`", "--", "/*", "$$"}

// ExtractComparisonTokens splits anonymized output into lowercase comparable tokens while skipping
// anonymous tokens, typed literal placeholders, and collapse markers.
func ExtractComparisonTokens(anonymized string) []string {
	out := make([]string, 0, len(anonymized)/6+1)
	scanComparisonTokens(anonymized, func(tok string) {
		out = append(out, tok)
	})
	return out
}

func scanComparisonTokens(s string, emit func(string)) {
	n := len(s)
	i := 0
	for i < n {
		b := s[i]
		switch {
		case isASCIIAlpha(b) || b == '_':
			j := i + 1
			for j < n && isTokenRune(s[j]) {
				j++
			}
			tok := s[i:j]
			if !isAnonToken(tok) {
				emit(strings.ToLower(tok))
			}
			i = j
		case b == ':':
			if i+1 < n && isPlaceholderBodyByte(s[i+1]) {
				j := i + 2
				for j < n && isPlaceholderBodyByte(s[j]) {
					j++
				}
				i = j
			} else {
				i++
			}
		case b == '[':
			j := i + 1
			for j < n && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j > i+1 && j < n && s[j] == ']' {
				i = j + 1
			} else {
				i++
			}
		case b >= '0' && b <= '9':
			j := i + 1
			for j < n && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			if j+1 < n && s[j] == '.' && s[j+1] >= '0' && s[j+1] <= '9' {
				j += 2
				for j < n && s[j] >= '0' && s[j] <= '9' {
					j++
				}
			}
			emit(s[i:j])
			i = j
		case b < utf8.RuneSelf:
			// Single-byte non-token ASCII runes (including whitespace, newlines, and punctuation)
			// match the trailing `.` (or are skipped as `\n`) and are always discarded.
			i++
		default:
			rn, size := utf8.DecodeRuneInString(s[i:])
			if size > 1 && !unicode.IsSpace(rn) {
				emit(strings.ToLower(s[i : i+size]))
			}
			i += size
		}
	}
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isTokenRune(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isPlaceholderBodyByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z')
}

func isAnonToken(s string) bool {
	underscore := strings.IndexByte(s, '_')
	if underscore < 3 || underscore > 10 || underscore+1 >= len(s) {
		return false
	}
	switch s[:underscore] {
	case "database", "schema", "table", "column", "index", "constraint", "alias", "cte", "ident":
	default:
		return false
	}
	for i := underscore + 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isSingleComparisonToken reports whether s matches `^[A-Za-z_][A-Za-z0-9_]*$`
// or `^[0-9]+(?:\.[0-9]+)?$` (equivalent to tokenRunPattern matching the full string).
func isSingleComparisonToken(s string) bool {
	if s == "" {
		return false
	}
	if isASCIIAlpha(s[0]) || s[0] == '_' {
		for i := 1; i < len(s); i++ {
			if !isTokenRune(s[i]) {
				return false
			}
		}
		return true
	}
	if s[0] >= '0' && s[0] <= '9' {
		i := 1
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == len(s) {
			return true
		}
		if s[i] != '.' {
			return false
		}
		i++
		if i == len(s) {
			return false
		}
		for ; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// AssertNoLeakValues verifies tokenized string values contain no forbidden raw identifiers (SEC-0002).
// Unlike AssertNoLeak, JSON quote delimiters around tokenized values are expected and not rejected.
func AssertNoLeakValues(values []string, forbidden []Forbidden) error {
	if len(forbidden) == 0 {
		return nil
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		set := make(map[string]struct{}, len(v)/6+1)
		scanComparisonTokens(v, func(tok string) {
			set[tok] = struct{}{}
		})
		lowerV := ""
		haveLowerV := false
		for _, f := range forbidden {
			fv := strings.TrimSpace(f.Value)
			if fv == "" {
				continue
			}
			if isSingleComparisonToken(fv) {
				if _, ok := set[strings.ToLower(fv)]; ok {
					return fmt.Errorf("privacy: tokenized value leaks %s (value suppressed)", f.What)
				}
				continue
			}
			if !haveLowerV {
				lowerV = strings.ToLower(v)
				haveLowerV = true
			}
			if strings.Contains(lowerV, strings.ToLower(fv)) {
				return fmt.Errorf("privacy: tokenized value leaks %s (value suppressed)", f.What)
			}
		}
	}
	return nil
}

// AssertNoLeak fails closed when anonymized SQL or context retains forbidden delimiters or raw values.
func AssertNoLeak(anonymized string, forbidden []Forbidden) error {
	for _, ch := range anonymizedForbiddenChars {
		if strings.Contains(anonymized, ch) {
			return fmt.Errorf("privacy: anonymized output contains forbidden sequence %q", ch)
		}
	}
	if len(forbidden) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(anonymized)/6+1)
	scanComparisonTokens(anonymized, func(tok string) {
		set[tok] = struct{}{}
	})
	lowerAnon := ""
	haveLowerAnon := false
	leaked := map[string]struct{}{}
	for _, f := range forbidden {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		if isSingleComparisonToken(v) {
			if _, ok := set[strings.ToLower(v)]; ok {
				leaked[f.What] = struct{}{}
			}
			continue
		}
		if !haveLowerAnon {
			lowerAnon = strings.ToLower(anonymized)
			haveLowerAnon = true
		}
		if strings.Contains(lowerAnon, strings.ToLower(v)) {
			leaked[f.What] = struct{}{}
		}
	}
	if len(leaked) > 0 {
		kinds := make([]string, 0, len(leaked))
		for k := range leaked {
			kinds = append(kinds, k)
		}
		// Sort category names for deterministic error messages.
		for i := 1; i < len(kinds); i++ {
			for j := i; j > 0 && kinds[j] < kinds[j-1]; j-- {
				kinds[j], kinds[j-1] = kinds[j-1], kinds[j]
			}
		}
		return fmt.Errorf("privacy: anonymized output leaks %s (value suppressed)", strings.Join(kinds, ","))
	}
	return nil
}
