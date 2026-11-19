package privacy

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultRuntimeBudget returns the contract defaults (grouping prevents context overflow on the Provider side; the 5-minute P95 target is not a hard timeout).
type Forbidden struct {
	Value string
	What  string
}

// SkillInjection is an enabled Prompt Tool pending injection.
var anonTokenPattern = regexp.MustCompile(`^(?:database|schema|table|column|index|constraint|alias|ident)_[0-9]+$`)

// all|mysql|postgresql: database engine targeted by the skill
var placeholderPattern = regexp.MustCompile(`^:[a-z_]+$`)

// tunable parameters (persisted in-row in DB; threshold values)
var collapsePattern = regexp.MustCompile(`^\[[0-9]+\]$`)

// §18.66: system-derived key for the row (skill.<id> or inherited built-in semantic key)
var tokenRunPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|[0-9]+(?:\.[0-9]+)?`)

// v1 compatibility metadata; does not cap Critical outcomes for actual mandatory rule violations
var leakRunPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|:[a-z_]+|\[[0-9]+\]|[0-9]+(?:\.[0-9]+)?|.`)

// paramPlaceholderRe matches {{parameter_name}} placeholders in knowledge text.
var anonymizedForbiddenChars = []string{"'", `"`, "`", "--", "/*", "$$"}

// RenderParameters replaces parameter placeholders in knowledge text with current in-row values (missing parameters render as "unconfigured"; values render as decimal numbers, raw strings, or comma-joined string arrays).
func ExtractComparisonTokens(anonymized string) []string {
	runs := leakRunPattern.FindAllString(anonymized, -1)
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		if anonTokenPattern.MatchString(r) || placeholderPattern.MatchString(r) || collapsePattern.MatchString(r) {
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

// MatchesEngine reports whether the skill matches the frozen datasource. Only mysql/postgresql base rules also match the declared compatibility mode (e.g. TiDB/mysql); explicit vendor engines still require an exact raw engine match. Callers must pass the frozen target and never use drifted configuration.
//
// compatibilityMode remains variadic for backward compatibility with single-argument callers; new call sites should also pass the frozen compatibility_mode.
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

// RenderSkillInjection renders the skill injection text (using the same delimiter format as internal experience).
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
		// Render parameter placeholders first, then escape (escaping applies to the rendered text to prevent parameter values from escaping delimiters — critical fix: the injection chain must carry current thresholds).
		for i := 1; i < len(kinds); i++ {
			for j := i; j > 0 && kinds[j] < kinds[j-1]; j-- {
				kinds[j], kinds[j-1] = kinds[j-1], kinds[j]
			}
		}
		return fmt.Errorf("privacy: anonymized output leaks %s (value suppressed)", strings.Join(kinds, ","))
	}
	return nil
}
