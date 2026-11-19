// Package buildinfo holds build-time version metadata injected via ldflags.
// Falls back to dev defaults when ldflags are not injected rather than guessing release facts.
package buildinfo

import "runtime"

var (
	// Version is injected via -ldflags "-X ...buildinfo.Version=v4.0.0".
	Version = "dev"
	// Commit is injected by the build system; defaults to "unknown" when not injected.
	Commit = "unknown"
)

// Summary returns the stable set of keys exposed by the version endpoint.
func Summary() map[string]string {
	return map[string]string{
		"service":    "yearning",
		"version":    Version,
		"commit":     Commit,
		"go_version": runtime.Version(),
	}
}
