package application

import (
	"errors"
	"regexp"
)

var ErrPolicyPrivacy = errors.New("review: policy privacy sanitization failed")

func policyIdentifierRegex(identifier string, caseInsensitive bool) (*regexp.Regexp, error) {
	body := regexp.QuoteMeta(identifier)
	if caseInsensitive {
		body = "(?i:" + body + ")"
	}
	// Policy prose uses Chinese words immediately beside object names. Treat
	// non-ASCII characters as boundaries conservatively; ASCII identifier
	// continuations still prevent id matching identity and orders matching
	// orders_archive. This is a privacy rejection check, not a SQL lexer.
	return regexp.Compile(`(^|[^A-Za-z0-9_$])(` + body + `)([^A-Za-z0-9_$]|$)`)
}

func policyIdentifierPresent(value, identifier string, caseInsensitive bool) bool {
	if value == "" || identifier == "" {
		return false
	}
	pattern, err := policyIdentifierRegex(identifier, caseInsensitive)
	return err == nil && pattern.MatchString(value)
}
