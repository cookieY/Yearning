//go:build !assets

package assets

// ReleaseProvider returns a disabled Provider: default development and test builds do not embed the SPA,
// keeping the routing surface pure API (unmatched routes return Problem 404).
func ReleaseProvider() Provider {
	return NewProvider(nil, "")
}
