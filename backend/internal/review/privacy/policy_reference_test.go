package privacy

import (
	"errors"
	"strings"
	"testing"
)

func TestParsePolicyReferencesAcceptsAllSupportedKinds(t *testing.T) {
	text := `schema {{ref:schema:["app"]}}
table {{ref:table:["app","audit_log"]}}
view {{ref:view:["app","audit_view"]}}
column {{ref:column:["app","audit_log","actor"]}}
index {{ref:index:["app","audit_log","audit_actor_idx"]}}
constraint {{ref:constraint:["app","audit_log","audit_actor_fk"]}}`
	got, err := ParsePolicyReferences(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []PolicyObjectReference{
		{Kind: PolicyReferenceSchema, Schema: "app"},
		{Kind: PolicyReferenceTable, Schema: "app", Relation: "audit_log"},
		{Kind: PolicyReferenceView, Schema: "app", Relation: "audit_view"},
		{Kind: PolicyReferenceColumn, Schema: "app", Relation: "audit_log", Name: "actor"},
		{Kind: PolicyReferenceIndex, Schema: "app", Relation: "audit_log", Name: "audit_actor_idx"},
		{Kind: PolicyReferenceConstraint, Schema: "app", Relation: "audit_log", Name: "audit_actor_fk"},
	}
	if len(got) != len(want) {
		t.Fatalf("references=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reference[%d]=%+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParsePolicyReferencesAcceptsWhitespaceAndIgnoresParameters(t *testing.T) {
	text := `Use {{threshold}} and {{ref}} against {{ref:table: [ "app" , "orders" ] }}.`
	got, err := ParsePolicyReferences(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (PolicyObjectReference{
		Kind: PolicyReferenceTable, Schema: "app", Relation: "orders",
	}) {
		t.Fatalf("references=%v", got)
	}
}

func TestRewritePolicyReferencesUsesValidatedExactIdentity(t *testing.T) {
	var seen []PolicyObjectReference
	got, err := RewritePolicyReferences(
		`Use {{ref:table:["app","orders"]}} and {{threshold}}.`,
		func(ref PolicyObjectReference) (string, error) {
			seen = append(seen, ref)
			return "table_1", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Use table_1 and {{threshold}}." {
		t.Fatalf("rewritten=%q", got)
	}
	if len(seen) != 1 || seen[0] != (PolicyObjectReference{
		Kind: PolicyReferenceTable, Schema: "app", Relation: "orders",
	}) {
		t.Fatalf("resolved=%v", seen)
	}
}

func TestPolicyReferencesAllowRepeatedUseWithStableResolution(t *testing.T) {
	const text = `First {{ref:table:["app","orders"]}}, again {{ref:table:["app","orders"]}}.`
	parsed, err := ParsePolicyReferences(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 || parsed[0] != parsed[1] {
		t.Fatalf("repeated references=%v", parsed)
	}
	calls := 0
	rewritten, err := RewritePolicyReferences(text, func(PolicyObjectReference) (string, error) {
		calls++
		return "table_1", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || rewritten != "First table_1, again table_1." {
		t.Fatalf("calls=%d rewritten=%q", calls, rewritten)
	}
}

func TestParsePolicyReferencesAcceptsDelimiterRunesInsideJSONStrings(t *testing.T) {
	got, err := ParsePolicyReferences(`See {{ref:table:["app","a}}b"]}}.`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Relation != "a}}b" {
		t.Fatalf("references=%v", got)
	}
}

func TestParsePolicyReferencesRejectsMalformedReferencesWithoutDisclosure(t *testing.T) {
	tests := []string{
		`{{ref:function:["app","danger"]}}`,
		`{{ref:table:["app"]}}`,
		`{{ref:column:["app","orders"]}}`,
		`{{ref:table:["app",""]}}`,
		`{{ref:table:["app",7]}}`,
		`{{ref:table:null}}`,
		`{{ref:table:{"schema":"app"}}}`,
		`{{ref:table:["app","unterminated"]}`,
		`{{{ref:table:["app","orders"]}}}`,
		`{{ref:table:["app","orders"]}}}}`,
		`{{ref:table:[\"app\",\"escaped\"]}}`,
		`{{ref table:["app","orders"]}}`,
	}
	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			_, err := ParsePolicyReferences(text)
			if !errors.Is(err, ErrInvalidPolicyReference) {
				t.Fatalf("error=%v, want ErrInvalidPolicyReference", err)
			}
			if err != nil && (strings.Contains(err.Error(), "danger") || strings.Contains(err.Error(), "unterminated")) {
				t.Fatalf("error disclosed reference identifier: %v", err)
			}
		})
	}
}

func TestValidatePolicyReferenceSyntaxMatchesParser(t *testing.T) {
	const valid = `Use {{ref:view:["reporting","daily"]}}.`
	if err := ValidatePolicyReferenceSyntax(valid); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePolicyReferenceSyntax(`Use {{ref:view:["reporting"]}}.`); !errors.Is(err, ErrInvalidPolicyReference) {
		t.Fatalf("error=%v, want ErrInvalidPolicyReference", err)
	}
}
