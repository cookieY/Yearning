package privacy

import (
	"fmt"
	"regexp"
	"strings"
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

// placeholderPattern matches typed literal placeholders such as :int, :decimal, or :string.
var placeholderPattern = regexp.MustCompile(`^:[a-z_]+$`)

// collapsePattern matches collapsed IN-list cardinality markers such as [5].
var collapsePattern = regexp.MustCompile(`^\[[0-9]+\]$`)

// tokenRunPattern extracts comparable single identifier or numeric tokens.
var tokenRunPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|[0-9]+(?:\.[0-9]+)?`)

// leakRunPattern scans anonymized output into raw token, placeholder, collapse, or punctuation runs.
var leakRunPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|:[a-z_]+|\[[0-9]+\]|[0-9]+(?:\.[0-9]+)?|.`)

// anonymizedForbiddenChars rejects unnormalized string delimiters and SQL comment starters.
// Hash characters (#) remain permitted for PostgreSQL JSON path operators (#>, #>>, #-).
var anonymizedForbiddenChars = []string{"'", `"`, "`", "--", "/*", "$$"}

// ExtractComparisonTokens splits anonymized output into lowercase comparable tokens while skipping
// anonymous tokens, typed literal placeholders, and collapse markers.
func ExtractComparisonTokens(anonymized string) []string {
	runs := leakRunPattern.FindAllString(anonymized, -1)
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		if AnonTokenPattern.MatchString(r) || placeholderPattern.MatchString(r) || collapsePattern.MatchString(r) {
			continue
		}
		if strings.TrimSpace(r) == "" || len(r) == 1 && !isTokenRune(r[0]) {
			continue
		}
		out = append(out, strings.ToLower(r))
	}
	return out
}

func isTokenRune(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// AssertNoLeakValues verifies tokenized string values contain no forbidden raw identifiers (SEC-0002).
// Unlike AssertNoLeak, JSON quote delimiters around tokenized values are expected and not rejected.
func AssertNoLeakValues(values []string, forbidden []Forbidden) error {
	for _, v := range values {
		if v == "" {
			continue
		}
		tokens := ExtractComparisonTokens(v)
		set := make(map[string]struct{}, len(tokens))
		for _, t := range tokens {
			set[t] = struct{}{}
		}
		for _, f := range forbidden {
			fv := strings.TrimSpace(f.Value)
			if fv == "" {
				continue
			}
			runs := tokenRunPattern.FindAllString(fv, -1)
			if len(runs) == 1 && len(runs[0]) == len(fv) {
				if _, ok := set[strings.ToLower(fv)]; ok {
					return fmt.Errorf("privacy: tokenized value leaks %s (value suppressed)", f.What)
				}
				continue
			}
			if strings.Contains(strings.ToLower(v), strings.ToLower(fv)) {
				return fmt.Errorf("privacy: tokenized value leaks %s (value suppressed)", f.What)
			}
		}
	}
	return nil
}

// AssertNoLeak fails closed when anonymized SQL or context retains forbidden delimiters or raw values.
func AssertNoLeak(anonymized string, forbidden []Forbidden) error {
	lowerAnon := strings.ToLower(anonymized)
	for _, ch := range anonymizedForbiddenChars {
		if strings.Contains(anonymized, ch) {
			return fmt.Errorf("privacy: anonymized output contains forbidden sequence %q", ch)
		}
	}
	tokens := ExtractComparisonTokens(anonymized)
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		set[t] = struct{}{}
	}
	leaked := map[string]struct{}{}
	for _, f := range forbidden {
		v := strings.TrimSpace(f.Value)
		if v == "" {
			continue
		}
		runs := tokenRunPattern.FindAllString(v, -1)
		if len(runs) == 1 && len(runs[0]) == len(v) {
			if _, ok := set[strings.ToLower(v)]; ok {
				leaked[f.What] = struct{}{}
			}
			continue
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
