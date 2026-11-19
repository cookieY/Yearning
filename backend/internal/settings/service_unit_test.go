// service_unit_test.go SEC-0010: mapping between contract namespaces and database storage keys.
package settings

import (
	"context"
	"testing"
)

func TestStorageNamespaceMapping(t *testing.T) {
	if got := storageNamespace(NsAIBudget); got != "ai_budget" {
		t.Fatalf("ai-budget must map to storage key ai_budget, got %q", got)
	}
	for _, ns := range []string{NsGeneral, NsQuery, NsExecution, NsBranding} {
		if got := storageNamespace(ns); got != ns {
			t.Fatalf("namespace %q must pass through unchanged, got %q", ns, got)
		}
	}
}

// TestSystemTimezoneIANAValidation F-SET-001: system_timezone must be a real IANA time zone name --
// legacy character-set validation allowed concatenated strings like "UTCUTCAsia/Shanghai".
func TestSystemTimezoneIANAValidation(t *testing.T) {
	accept := []string{"Asia/Shanghai", "UTC", "America/New_York", "Etc/GMT+8"}
	for _, tz := range accept {
		got, _, err := Validate(NsGeneral, []byte(`{"system_timezone":"`+tz+`"}`))
		if err != nil {
			t.Fatalf("valid tz %q rejected: %v", tz, err)
		}
		if got["system_timezone"] != tz {
			t.Fatalf("valid tz %q mutated to %v", tz, got["system_timezone"])
		}
	}
	reject := []string{"UTCUTCAsia/Shanghai", "Asia//Shanghai", "Mars/Olympus", "Local", ""}
	for _, tz := range reject {
		if _, _, err := Validate(NsGeneral, []byte(`{"system_timezone":"`+tz+`"}`)); err == nil {
			t.Fatalf("invalid tz %q accepted", tz)
		}
	}
}

func TestSummarizeLowImpactWhenHighImpactFieldUnchanged(t *testing.T) {
	s := &Service{}
	cur := defaultsOf(NsAIBudget)
	normalized, impacts, err := Validate(NsAIBudget, []byte(`{"enforced":false,"currency":"USD","daily_budget_minor":12000,"alert_threshold_percent":85,"input_price_per_million_minor":100,"output_price_per_million_minor":200}`))
	if err != nil {
		t.Fatalf("validate ai-budget: %v", err)
	}
	summary, err := s.summarize(context.Background(), NsAIBudget, cur, normalized, impacts)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if summary.Level != "low" {
		t.Fatalf("expected summary.Level='low' when enforced is unchanged, got %q", summary.Level)
	}
}
