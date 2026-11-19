package agent

import (
	"fmt"
	"regexp"
	"unicode"

	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// Technical identifiers and SQL snippets must stay in backticks, including
// fenced code. Only surrounding human prose is subject to the script guard.
var outputCode = regexp.MustCompile("(?s)```.*?```|`[^`]*`")

// ValidateOutputLocale rejects clearly wrong-language prose before any finding
// or tool request is accepted. This is a script guard, not semantic language
// identification: the fixed prompt remains responsible for fluent zh/en text.
// Errors contain field paths only; provider text must not enter public errors.
func ValidateOutputLocale(response *provider.Response, locale string) error {
	if response == nil {
		return fmt.Errorf("%w: missing response", provider.ErrSchemaInvalid)
	}
	check := func(field, value string, optional bool) error {
		if optional && value == "" {
			return nil
		}
		var han, latin, other bool
		for _, r := range outputCode.ReplaceAllString(value, "") {
			switch {
			case unicode.Is(unicode.Han, r):
				han = true
			case unicode.Is(unicode.Latin, r):
				latin = true
			case unicode.IsLetter(r):
				other = true
			}
		}
		valid := (han || latin) && !other
		if locale == "en-US" {
			valid = latin && !han && !other
		}
		if !valid {
			return fmt.Errorf("%w: output language mismatch at %s", provider.ErrSchemaInvalid, field)
		}
		return nil
	}
	if err := check("summary", response.Summary, false); err != nil {
		return err
	}
	for i, finding := range response.Findings {
		for _, field := range []struct {
			name, value string
			optional    bool
		}{
			{"title", finding.Title, false}, {"message", finding.Message, false}, {"suggestion", finding.Suggestion, true},
		} {
			if err := check(fmt.Sprintf("findings[%d].%s", i, field.name), field.value, field.optional); err != nil {
				return err
			}
		}
	}
	return nil
}
