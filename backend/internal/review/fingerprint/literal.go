package fingerprint

import "regexp"

// LiteralType is a typed placeholder category (typed_placeholder_without_value).
type LiteralType string

const (
	LitInt       LiteralType = "int"
	LitDecimal   LiteralType = "decimal"
	LitString    LiteralType = "string"
	LitBool      LiteralType = "bool"
	LitDate      LiteralType = "date"
	LitTimestamp LiteralType = "timestamp"
	LitUUID      LiteralType = "uuid"
	LitBinary    LiteralType = "binary"
	LitNull      LiteralType = "null"
	LitParam     LiteralType = "param"
)

var (
	uuidPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	datePattern      = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2}(:[0-9]{2})?(\.[0-9]+)?(Z|[+-][0-9]{2}:?[0-9]{2})?$`)
	hexEvenPattern   = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

// ClassifyString classifies string literal content (uuid/date/timestamp/binary/string).
// X'/B' introducers force binary; content proof takes precedence, falling back to string when unproven.
func ClassifyString(content string, prefix byte) LiteralType {
	switch prefix {
	case 'X', 'B':
		return LitBinary
	}
	c := content
	if len(c) >= 2 {
		switch {
		case uuidPattern.MatchString(c):
			return LitUUID
		case datePattern.MatchString(c):
			return LitDate
		case timestampPattern.MatchString(c):
			return LitTimestamp
		}
	}
	return LitString
}

// ClassifyNumber classifies numeric literals: hex -> binary, containing ./e -> decimal, otherwise int.
func ClassifyNumber(text string) LiteralType {
	if len(text) > 2 && (text[0] == '0') && (text[1] == 'x' || text[1] == 'X') {
		return LitBinary
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '.' || text[i] == 'e' {
			return LitDecimal
		}
	}
	return LitInt
}

// ClassifyWordLiteral classifies word literals (NULL/TRUE/FALSE).
func ClassifyWordLiteral(word string) (LiteralType, bool) {
	switch word {
	case "null":
		return LitNull, true
	case "true", "false":
		return LitBool, true
	}
	return "", false
}

// LiteralTypeOf returns the literal category of a token, or false for non-literal tokens.
func LiteralTypeOf(t Token) (LiteralType, bool) {
	switch t.Kind {
	case TokString:
		return ClassifyString(t.Text, t.Prefix), true
	case TokDollarString:
		return ClassifyString(t.Text, 0), true
	case TokNumber:
		return ClassifyNumber(t.Text), true
	case TokParam:
		return LitParam, true
	case TokWord:
		return ClassifyWordLiteral(t.Text)
	}
	return "", false
}

// CardinalityBucket folds cardinality into a deterministic scale bucket (powers of 4).
// The execution shape fingerprint uses only bucket values, while exact counts are recorded outside the fingerprint
// (contract: record_cardinality_outside_structural_fingerprint).
func CardinalityBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n <= 4:
		return "4"
	case n <= 16:
		return "16"
	case n <= 64:
		return "64"
	case n <= 256:
		return "256"
	case n <= 1024:
		return "1024"
	default:
		return ">1024"
	}
}
