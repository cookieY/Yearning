// Package keyprovider defines health checks for cryptographic key providers.
//
// The file-based implementation backs the payload_keyring Secret mount; Health reuses the crypto
// package's file permission checks and full semantic validation (including active current keys per purpose),
// sharing the exact same validation path between startup and Readiness probes. Key material never enters logs or responses.
package keyprovider

import (
	"context"
	"errors"
	"fmt"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// ErrUnhealthy indicates the Provider is currently unhealthy; callers report only the check name without leaking internal details.
var ErrUnhealthy = errors.New("key provider unhealthy")

// Provider is a key provider probeable by Readiness checks.
type Provider interface {
	// Health verifies that the Provider can currently supply valid key material.
	Health(ctx context.Context) error
}

// FileProvider reads a keyring from a mounted file (e.g. a Kubernetes Secret volume).
type FileProvider struct {
	path string
}

// NewFileProvider constructs a file-backed Provider; an empty path is rejected immediately.
func NewFileProvider(path string) (*FileProvider, error) {
	if path == "" {
		return nil, fmt.Errorf("key provider path is empty")
	}
	return &FileProvider{path: path}, nil
}

// Health performs full validation on every call: file attributes (mode <= 0400, controlled symlinks,
// parent directory not writable by group/others -- SEC-0017) + keyring semantic parsing (valid JSON, 32-byte key material,
// no cross-purpose key reuse) + presence of an active encrypt_decrypt current key for the payload purpose
// (SEC-0020/SEC-0046). Shares the exact validation path with startup (crypto.LoadKeyring)
// and never writes file contents into error messages or logs.
func (p *FileProvider) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := crypto.ValidateKeyringFile(p.path); err != nil {
		return fmt.Errorf("%w: keyring file form invalid", ErrUnhealthy)
	}
	ring, err := crypto.LoadKeyring(p.path)
	if err != nil {
		return fmt.Errorf("%w: keyring content invalid", ErrUnhealthy)
	}
	if !ring.HasCurrentKey(crypto.PurposePayload) {
		return fmt.Errorf("%w: no encrypt_decrypt key for purpose payload", ErrUnhealthy)
	}
	return nil
}

// Path returns the keyring file path monitored by this Provider (for diagnostics, non-secret).
func (p *FileProvider) Path() string { return p.path }
