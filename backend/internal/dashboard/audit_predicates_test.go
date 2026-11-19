// audit_predicates_test.go guards UTC anchoring of audit date predicates (R1 Medium-1):
// comparing date with timestamptz defaults to midnight in the PG session TimeZone, shifting day
// boundaries silently when deployment PGTZ is non-UTC; predicates must explicitly use
// `at time zone 'UTC'` and never contain unanchored bare `?::date` comparisons.
package dashboard

import (
	"strings"
	"testing"
)

func TestAuditDatePredicatesUTCAnchored(t *testing.T) {
	for name, pred := range map[string]string{
		"occurred_from": auditOccurredFromPred,
		"occurred_to":   auditOccurredToPred,
	} {
		if !strings.Contains(pred, "at time zone 'UTC'") {
			t.Fatalf("%s predicate lost UTC anchor: %q", name, pred)
		}
		if strings.Contains(pred, "occurred_at") && pred[0] != 'o' {
			t.Fatalf("%s predicate malformed: %q", name, pred)
		}
	}
	if !strings.HasPrefix(auditOccurredFromPred, "occurred_at >= ((?::date)::timestamp at time zone 'UTC')") {
		t.Fatalf("from predicate drifted: %q", auditOccurredFromPred)
	}
	if !strings.HasPrefix(auditOccurredToPred, "occurred_at < (((?::date)::timestamp at time zone 'UTC') + interval '1 day')") {
		t.Fatalf("to predicate drifted: %q", auditOccurredToPred)
	}
}
