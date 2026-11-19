// Package assets owns the embedded Frontend static asset surface for release builds (FE-F13).
// SPA artifacts are embedded only in `-tags assets` release builds (embed_assets.go);
// default development and test builds use stub_assets.go with a disabled Provider so routes remain API-only.
// Release builds inject a deterministic dist tree hash via -ldflags so image scanning can verify that
// the binary embeds the exact static artifacts produced from the Gitlink-locked Frontend commit.
package assets

import (
	"fmt"
	"io/fs"
)

// Provider exposes release assets to httpapi: FS holds the embedded dist (nil when disabled),
// and Digest holds the build-injected 64-character hex tree hash (empty when not injected).
type Provider struct {
	fsys   fs.FS
	digest string
}

// NewProvider constructs a Provider; a nil fsys indicates the embedded asset surface is disabled.
func NewProvider(fsys fs.FS, digest string) Provider {
	return Provider{fsys: fsys, digest: digest}
}

// Enabled reports whether release assets are enabled (embedded FS is present).
func (p Provider) Enabled() bool { return p.fsys != nil }

// FS returns the embedded dist filesystem, or nil when disabled.
func (p Provider) FS() fs.FS { return p.fsys }

// Digest returns the build-injected dist tree hash, or an empty string when not injected.
func (p Provider) Digest() string { return p.digest }

// Validate verifies that an enabled Provider is self-consistent: Digest is a 64-character lowercase hex string
// and index.html exists at the dist root (SPA entry point). Called at release startup to fail fast on misconfiguration.
func (p Provider) Validate() error {
	if !p.Enabled() {
		return nil
	}
	if len(p.digest) != 64 {
		return fmt.Errorf("frontend assets digest not injected (len=%d, want 64 hex chars)", len(p.digest))
	}
	for _, r := range p.digest {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("frontend assets digest is not lowercase hex: %q", p.digest)
		}
	}
	if _, err := fs.Stat(p.fsys, "index.html"); err != nil {
		return fmt.Errorf("embedded dist missing index.html: %w", err)
	}
	return nil
}
