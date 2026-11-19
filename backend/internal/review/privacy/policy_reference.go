package privacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PolicyReferenceKind is the closed database-object vocabulary accepted in
// administrator-authored policy and knowledge text.
type PolicyReferenceKind string

const (
	PolicyReferenceSchema     PolicyReferenceKind = "schema"
	PolicyReferenceTable      PolicyReferenceKind = "table"
	PolicyReferenceView       PolicyReferenceKind = "view"
	PolicyReferenceColumn     PolicyReferenceKind = "column"
	PolicyReferenceIndex      PolicyReferenceKind = "index"
	PolicyReferenceConstraint PolicyReferenceKind = "constraint"
)

// ErrInvalidPolicyReference is returned for any malformed, unsupported,
// repeated or unresolved-by-grammar typed object reference. Errors from this
// package never include a catalog identifier from the policy text.
var ErrInvalidPolicyReference = errors.New("privacy: invalid policy object reference")

// PolicyObjectReference is one exact, case-sensitive database object identity.
// Schema-only references leave Relation and Name empty; relation references
// leave Name empty.
type PolicyObjectReference struct {
	Kind     PolicyReferenceKind
	Schema   string
	Relation string
	Name     string
}

const policyReferencePrefix = "{{ref:"

// ParsePolicyReferences parses every typed object reference in text. Ordinary
// {{parameter}} placeholders are ignored. References are exact catalog
// identities: this function performs no case folding, aliasing or fuzzy
// matching.
func ParsePolicyReferences(text string) ([]PolicyObjectReference, error) {
	refs := make([]PolicyObjectReference, 0)
	cursor := 0
	for {
		start, err := nextPolicyReference(text, cursor)
		if err != nil {
			return nil, err
		}
		if start < 0 {
			return refs, nil
		}
		end, err := policyReferenceEnd(text, start+len(policyReferencePrefix))
		if err != nil {
			return nil, err
		}
		if !policyReferenceBoundariesValid(text, start, end) {
			return nil, ErrInvalidPolicyReference
		}
		ref, err := parsePolicyReferencePayload(text[start+len(policyReferencePrefix) : end])
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
		cursor = end + len("}}")
	}
}

// ValidatePolicyReferenceSyntax applies the complete typed-reference grammar
// without resolving the references against a database catalog.
func ValidatePolicyReferenceSyntax(text string) error {
	_, err := ParsePolicyReferences(text)
	return err
}

// RewritePolicyReferences validates the complete text before invoking resolve
// for each reference occurrence and replacing it with the returned anonymous
// value. Repeated references are allowed and normally resolve to one stable
// value. Ordinary {{parameter}} placeholders remain untouched.
func RewritePolicyReferences(
	text string,
	resolve func(PolicyObjectReference) (string, error),
) (string, error) {
	if _, err := ParsePolicyReferences(text); err != nil {
		return "", err
	}
	if !strings.Contains(text, policyReferencePrefix) {
		return text, nil
	}
	if resolve == nil {
		return "", ErrInvalidPolicyReference
	}
	var out strings.Builder
	cursor := 0
	for {
		start, err := nextPolicyReference(text, cursor)
		if err != nil {
			return "", err
		}
		if start < 0 {
			break
		}
		end, err := policyReferenceEnd(text, start+len(policyReferencePrefix))
		if err != nil {
			return "", err
		}
		if !policyReferenceBoundariesValid(text, start, end) {
			return "", ErrInvalidPolicyReference
		}
		ref, err := parsePolicyReferencePayload(text[start+len(policyReferencePrefix) : end])
		if err != nil {
			return "", err
		}
		replacement, err := resolve(ref)
		if err != nil || replacement == "" {
			return "", ErrInvalidPolicyReference
		}
		out.WriteString(text[cursor:start])
		out.WriteString(replacement)
		cursor = end + len("}}")
	}
	out.WriteString(text[cursor:])
	return out.String(), nil
}

func policyReferenceBoundariesValid(text string, start, end int) bool {
	if start > 0 && text[start-1] == '{' {
		return false
	}
	after := end + len("}}")
	return after >= len(text) || text[after] != '}'
}

func nextPolicyReference(text string, from int) (int, error) {
	if from >= len(text) {
		return -1, nil
	}
	search := from
	for {
		relative := strings.Index(text[search:], "{{ref")
		if relative < 0 {
			return -1, nil
		}
		start := search + relative
		afterName := start + len("{{ref")
		if strings.HasPrefix(text[start:], policyReferencePrefix) {
			return start, nil
		}
		// A parameter name such as {{ref}} or {{reference}} is ordinary
		// policy prose. A space-separated ref token or truncated prefix is
		// malformed reserved syntax. The caller independently validates the
		// complete ordinary parameter grammar.
		if afterName >= len(text) ||
			text[afterName] == ' ' ||
			text[afterName] == '\t' ||
			text[afterName] == '\n' ||
			text[afterName] == '\r' {
			return -1, ErrInvalidPolicyReference
		}
		search = afterName
	}
}

// policyReferenceEnd finds the first }} outside a JSON string. Catalog
// identifiers can legally contain braces, so delimiter search must respect
// JSON string and escape state rather than looking for the first raw }}.
func policyReferenceEnd(text string, from int) (int, error) {
	inString := false
	escaped := false
	for i := from; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '}':
			if i+1 < len(text) && text[i+1] == '}' {
				return i, nil
			}
		}
	}
	return -1, ErrInvalidPolicyReference
}

func parsePolicyReferencePayload(payload string) (PolicyObjectReference, error) {
	colon := strings.IndexByte(payload, ':')
	if colon < 0 {
		return PolicyObjectReference{}, ErrInvalidPolicyReference
	}
	kind := PolicyReferenceKind(strings.TrimSpace(payload[:colon]))
	var values []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(payload[colon+1:])), &values); err != nil ||
		values == nil {
		return PolicyObjectReference{}, fmt.Errorf("%w: JSON array", ErrInvalidPolicyReference)
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return PolicyObjectReference{}, fmt.Errorf("%w: empty identity", ErrInvalidPolicyReference)
		}
	}
	ref := PolicyObjectReference{Kind: kind}
	switch kind {
	case PolicyReferenceSchema:
		if len(values) != 1 {
			return PolicyObjectReference{}, ErrInvalidPolicyReference
		}
		ref.Schema = values[0]
	case PolicyReferenceTable, PolicyReferenceView:
		if len(values) != 2 {
			return PolicyObjectReference{}, ErrInvalidPolicyReference
		}
		ref.Schema, ref.Relation = values[0], values[1]
	case PolicyReferenceColumn, PolicyReferenceIndex, PolicyReferenceConstraint:
		if len(values) != 3 {
			return PolicyObjectReference{}, ErrInvalidPolicyReference
		}
		ref.Schema, ref.Relation, ref.Name = values[0], values[1], values[2]
	default:
		return PolicyObjectReference{}, fmt.Errorf("%w: unsupported kind", ErrInvalidPolicyReference)
	}
	return ref, nil
}
