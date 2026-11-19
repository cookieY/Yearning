//go:build assets

package assets

import (
	"embed"
	"io/fs"
)

// distFS embeds the frontend dist artifacts copied in by the release build prerequisite step
// (Makefile frontend-release-assets: synced after building the Gitlink-locked frontend).
//
//go:embed all:dist
var distFS embed.FS

// frontendDigest is injected by release builds via -ldflags "-X ...assets.frontendDigest=<hex>";
// it matches the dist tree hash recorded by frontend-release-assets (asserted by container image scan).
var frontendDigest string

// SetDigestForTesting allows `-tags assets` test wiring to inject a valid digest:
// test binaries are not built with release ldflags, and a default empty string would cause Validate to fail fast
// during assembly (see TestMain in httpapi spa_assets_test.go). Present only in `assets` tag builds
// without affecting default builds or production release injection semantics.
func SetDigestForTesting(d string) {
	frontendDigest = d
}

// ReleaseProvider returns the embedded release assets (dist subtree + injected tree digest).
// This file is compiled only under `-tags assets`; if dist is missing, this package fails to compile,
// making it impossible for a release build to silently drop frontend artifacts.
func ReleaseProvider() Provider {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// fs.Sub on an existing directory inside embed.FS will not fail; keep defensive branch.
		return NewProvider(nil, "")
	}
	return NewProvider(sub, frontendDigest)
}
