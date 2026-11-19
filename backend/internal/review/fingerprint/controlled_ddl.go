package fingerprint

import "strings"

// IsControlledCharsetWord reports whether an engine charset spelling is in the
// frozen privacy allowlist. Callers must still prove it came from DDL syntax.
func IsControlledCharsetWord(value string) bool {
	return controlledCharsets[strings.ToLower(value)]
}

// IsControlledCollationWord reports whether a collation spelling is in the
// frozen privacy allowlist. Unknown values must not enter Provider context.
func IsControlledCollationWord(value string) bool {
	return controlledCollations[strings.ToLower(value)]
}
