package agent

import "testing"

func TestPolicyParametersFailClosedBeforeRendering(t *testing.T) {
	tests := []struct {
		name, text string
		params     map[string]any
		valid      bool
	}{
		{name: "missing", text: "Check {{limit}}"},
		{name: "provided", text: "Check {{limit}}", params: map[string]any{"limit": 10}, valid: true},
		{name: "typed ref", text: `Check {{ref:table:["prod","audit"]}}`, valid: true},
		{name: "malformed ref", text: `Check {{ref:table:prod.audit}}`},
		{name: "malformed param", text: `Check {{Limit}}`},
		{name: "unsupported value", text: `Check {{limit}}`, params: map[string]any{"limit": map[string]any{"a": 1}}},
		{name: "nil value", text: `Check {{limit}}`, params: map[string]any{"limit": nil}},
		{name: "empty string is configured", text: `Check {{limit}}`, params: map[string]any{"limit": ""}, valid: true},
		{name: "array", text: `Check {{limit}}`, params: map[string]any{"limit": []any{"a", "b"}}, valid: true},
		{name: "mixed array", text: `Check {{limit}}`, params: map[string]any{"limit": []any{"a", 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePolicyParameters(tt.text, tt.params)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}
