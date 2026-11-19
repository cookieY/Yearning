// normalize.go Tool fact normalization: retains only controlled JSON values
// (nil/bool/float64/string/recursive list-map, depth <= 6, bytes <= 64 KiB).
// Exceeding limits truncates to marker values — tool output is untrusted and never carries raw payloads.
package tool

import "encoding/json"

const (
	maxFactDepth = 6
	maxFactBytes = 65536
	truncateMark = "__truncated__"
)

// normalizeFact recursively sanitizes and caps values.
func normalizeFact(v any) map[string]any {
	out, _ := normalizeValue(v, 0).(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	if b, err := json.Marshal(out); err != nil || len(b) > maxFactBytes {
		return map[string]any{truncateMark: true}
	}
	return out
}

func normalizeValue(v any, depth int) any {
	if depth > maxFactDepth {
		return truncateMark
	}
	switch t := v.(type) {
	case nil, bool, string, float64, int, int64, json.Number:
		return t
	case int32:
		return float64(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = normalizeValue(vv, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = normalizeValue(vv, depth+1)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = vv
		}
		return out
	default:
		// Unknown shapes such as structs degrade via JSON round-trip.
		raw, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil
		}
		return normalizeValue(decoded, depth+1)
	}
}
