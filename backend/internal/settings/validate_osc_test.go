// validate_osc_test.go verifies that legacy external OSC tool "pt-osc" is retired and rejected
// at settings validation time while "none" and built-in "gh-ost" remain accepted.
package settings

import (
	"errors"
	"strings"
	"testing"
)

func TestExecutionSettingsRejectsRetiredPtOscTool(t *testing.T) {
	_, _, err := Validate(NsExecution, []byte(`{"restriction_enabled":false,"windows":[],"osc":{"tool":"pt-osc"}}`))
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "osc tool") {
		t.Fatalf("Validate(pt-osc) = %v; want ErrValidation for osc tool", err)
	}
	for _, validTool := range []string{"none", "gh-ost"} {
		if _, _, err := Validate(NsExecution, []byte(`{"restriction_enabled":false,"windows":[],"osc":{"tool":"`+validTool+`"}}`)); err != nil {
			t.Errorf("Validate(%q) = %v; want nil", validTool, err)
		}
	}
}
