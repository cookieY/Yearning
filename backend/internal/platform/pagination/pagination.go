// Package pagination provides shared page-size clamping utilities.
package pagination

// ClampLimit normalizes a requested page limit into [1, max], substituting def when v <= 0.
// When max > 0 and v > max, v is clamped to max.
func ClampLimit(v, def, max int) int {
	if def <= 0 {
		def = 50
	}
	if max > 0 && def > max {
		def = max
	}
	if v <= 0 {
		return def
	}
	if max > 0 && v > max {
		return max
	}
	return v
}
