package agent

import (
	"errors"
	"math"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

var ErrPolicyParameters = errors.New("agent: policy parameters or object references unresolved")

// ValidatePolicyParameters validates original frozen text before rendering can
// erase a missing parameter. Target-specific object resolution happens later,
// against the trusted catalog, and never through a Provider tool.
func ValidatePolicyParameters(text string, params map[string]any) error {
	stripped, err := privacy.RewritePolicyReferences(text, func(privacy.PolicyObjectReference) (string, error) { return "reference", nil })
	if err != nil {
		return ErrPolicyParameters
	}
	missing := false
	stripped = paramPlaceholderRe.ReplaceAllStringFunc(stripped, func(m string) string {
		value, ok := params[strings.Trim(m, "{}")]
		if !ok || !validPolicyParameterValue(value) {
			missing = true
		}
		return "parameter"
	})
	if missing || strings.Contains(stripped, "{{") || strings.Contains(stripped, "}}") {
		return ErrPolicyParameters
	}
	return nil
}

func validPolicyParameterValue(value any) bool {
	switch v := value.(type) {
	case string, bool, int, int64:
		return true
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	case []string:
		return true
	case []any:
		for _, item := range v {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}
