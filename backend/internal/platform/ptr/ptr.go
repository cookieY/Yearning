// Package ptr provides generic pointer conversion helpers with explicit zero-value semantics.
package ptr

// To returns a pointer to a copy of v (always non-nil, including for zero values such as "").
func To[T any](v T) *T {
	return &v
}

// NonZero returns a pointer to a copy of v, or nil when v equals the zero value of T.
func NonZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// Deref returns *p when p is non-nil, or the zero value of T otherwise.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
