// identifier.go Identifier allowlist validation and engine-safe quoting.
// Identifiers are never allowed to contain metacharacters such as backticks, double quotes, NUL, or newlines;
// quoting rules: MySQL wraps in backticks and doubles internal backticks; PostgreSQL wraps in double quotes and doubles internal double quotes.
package database

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// identifierMaxBytes uses the strictest limit across both engines (PostgreSQL NAMEDATALEN-1 = 63;
// overlong identifiers in PG would be silently truncated and could point to a different object, so they must be rejected outright).
const identifierMaxBytes = 63

// ValidateIdentifier validates an unquoted identifier.
// Allowed: letters, digits, underscores, dollar signs (PG), non-digit first character; length <= 63 bytes
// (PostgreSQL NAMEDATALEN-1; overlong identifiers in PG would be silently truncated and must be rejected).
// Rejects all quoting metacharacters, whitespace, control characters, and empty strings.
func ValidateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrIdentifierInvalid)
	}
	if len(name) > identifierMaxBytes {
		return fmt.Errorf("%w: length", ErrIdentifierInvalid)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: invalid utf-8", ErrIdentifierInvalid)
	}
	for i, r := range name {
		switch {
		case r == '_' || r == '$':
		case unicode.IsLetter(r):
		case unicode.IsDigit(r):
			if i == 0 {
				return fmt.Errorf("%w: leading digit", ErrIdentifierInvalid)
			}
		default:
			return fmt.Errorf("%w: illegal rune", ErrIdentifierInvalid)
		}
	}
	return nil
}

// quoteMySQLOpen wraps a MySQL identifier in `name` with internal backticks doubled.
func quoteMySQLOpen(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

// quotePostgresOpen wraps a PG identifier in "name" with internal double quotes doubled.
func quotePostgresOpen(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}
