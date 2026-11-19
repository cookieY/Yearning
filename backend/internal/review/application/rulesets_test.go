package application

import "testing"

func TestRuleSetConfigHashCanonicalMembership(t *testing.T) {
	t.Parallel()

	base := ruleSetConfigHash(true, []string{"tool-a", "tool-b"})
	if got := ruleSetConfigHash(true, []string{"tool-b", "tool-a"}); got != base {
		t.Fatalf("reorder changed config hash: base=%s got=%s", base, got)
	}
	if got := ruleSetConfigHash(true, []string{"tool-a", "tool-b"}); got != base {
		t.Fatalf("no-op replacement changed config hash: base=%s got=%s", base, got)
	}
	if got := ruleSetConfigHash(true, []string{"tool-a", "tool-b", "tool-c"}); got == base {
		t.Fatal("adding a member did not change config hash")
	}
	if got := ruleSetConfigHash(true, []string{"tool-a"}); got == base {
		t.Fatal("removing a member did not change config hash")
	}
	if got := ruleSetConfigHash(false, []string{"tool-a", "tool-b"}); got == base {
		t.Fatal("changing enabled did not change config hash")
	}
}
