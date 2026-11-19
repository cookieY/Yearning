package keyprovider

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// writeKeyring writes a minimal valid keyring (with a current encrypt_decrypt key for payload)
// using file mode 0400 per contract file_mode_maximum.
func writeKeyring(t *testing.T, material []byte, mode os.FileMode) string {
	t.Helper()
	doc := `{"contract_version":1,"deployment_id":"kp-test-01","keys":[` +
		`{"key_id":"pay-2026a","purpose":"payload","status":"encrypt_decrypt",` +
		`"created_at":"2026-08-25T00:00:00Z","key_base64":"` +
		base64.StdEncoding.EncodeToString(material) + `"}]}`
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(doc), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validMaterial() []byte {
	m := make([]byte, 32)
	for i := range m {
		m[i] = byte(i + 7)
	}
	return m
}

func TestFileProviderHealthy(t *testing.T) {
	path := writeKeyring(t, validMaterial(), 0o400)
	p, err := NewFileProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Health(context.Background()); err != nil {
		t.Fatalf("valid keyring must be healthy: %v", err)
	}
}

func TestFileProviderMissingFileFails(t *testing.T) {
	p, err := NewFileProvider(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatal(err)
	}
	err = p.Health(context.Background())
	if !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("missing keyring must be unhealthy: %v", err)
	}
}

// TestFileProviderModeGreaterThan0400Fails SEC-0017 regression:
// 0644/0600/0444 file modes must be reported unhealthy (contract file_mode_maximum=0400).
func TestFileProviderModeGreaterThan0400Fails(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o600, 0o444} {
		path := writeKeyring(t, validMaterial(), mode)
		p, err := NewFileProvider(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Health(context.Background()); !errors.Is(err, ErrUnhealthy) {
			t.Fatalf("mode %04o keyring must be unhealthy, got %v", mode, err)
		}
	}
}

// TestFileProviderSymlinkPolicy SEC-0017 regression: a keyring symlink pointing outside
// its parent directory must be unhealthy, whereas an intra-directory symlink (Kubernetes Secret volume
// ..data atomic update layout) must be healthy.
func TestFileProviderSymlinkPolicy(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "attacker-keyring.json")
	if err := os.WriteFile(target, []byte("whatever"), 0o400); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "keyring.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink not supported on this platform")
	}
	p, err := NewFileProvider(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Health(context.Background()); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("symlink escaping keyring dir must be unhealthy: %v", err)
	}

	// Kubernetes atomic update layout: real file inside ..data directory + entry symlink.
	realDir := filepath.Join(dir, "..20261021")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"contract_version":1,"deployment_id":"kp-test-01","keys":[` +
		`{"key_id":"pay-2026a","purpose":"payload","status":"encrypt_decrypt",` +
		`"created_at":"2026-08-25T00:00:00Z","key_base64":"` +
		base64.StdEncoding.EncodeToString(validMaterial()) + `"}]}`
	if err := os.WriteFile(filepath.Join(realDir, "keyring.json"), []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	k8sLink := filepath.Join(dir, "k8s-keyring.json")
	if err := os.Symlink(filepath.Join("..20261021", "keyring.json"), k8sLink); err != nil {
		t.Fatal(err)
	}
	p2, err := NewFileProvider(k8sLink)
	if err != nil {
		t.Fatal(err)
	}
	if err := p2.Health(context.Background()); err != nil {
		t.Fatalf("in-directory symlink (K8s secret volume form) must be healthy: %v", err)
	}
}

// TestFileProviderParentWritableFails SEC-0017 regression: a parent directory writable by group or others
// must be reported unhealthy.
func TestFileProviderParentWritableFails(t *testing.T) {
	for _, mode := range []os.FileMode{0o770, 0o707, 0o777} {
		dir := t.TempDir()
		if err := os.Chmod(dir, mode); err != nil {
			t.Skip("chmod parent not supported")
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		path := filepath.Join(dir, "keyring.json")
		if err := os.WriteFile(path, []byte("x"), 0o400); err != nil {
			t.Fatal(err)
		}
		p, err := NewFileProvider(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Health(context.Background()); !errors.Is(err, ErrUnhealthy) {
			t.Fatalf("parent dir mode %04o must make keyring unhealthy: %v", mode, err)
		}
	}
}

// TestFileProviderMissingCurrentKeyFails SEC-0046 regression: a keyring with valid file permissions
// but missing a current encrypt_decrypt key for the payload purpose must be reported unhealthy.
func TestFileProviderMissingCurrentKeyFails(t *testing.T) {
	m := validMaterial()
	doc := `{"contract_version":1,"deployment_id":"kp-test-01","keys":[` +
		`{"key_id":"pay-2025z","purpose":"payload","status":"decrypt_only",` +
		`"created_at":"2025-01-01T00:00:00Z","key_base64":"` +
		base64.StdEncoding.EncodeToString(m) + `"}]}`
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	p, err := NewFileProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Health(context.Background()); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("keyring without payload current key must be unhealthy: %v", err)
	}
}

// TestFileProviderGarbageContentFails: valid file permissions with invalid file contents must be reported unhealthy.
func TestFileProviderGarbageContentFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o400); err != nil {
		t.Fatal(err)
	}
	p, err := NewFileProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Health(context.Background()); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("garbage keyring content must be unhealthy: %v", err)
	}
}

func TestFileProviderEmptyPathRejected(t *testing.T) {
	_, err := NewFileProvider("")
	if err == nil {
		t.Fatal("empty path must be rejected")
	}
}

func TestHealthHonorsCancelledContext(t *testing.T) {
	p, err := NewFileProvider(writeKeyring(t, validMaterial(), 0o400))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Health(ctx); err == nil {
		t.Fatal("cancelled context must fail health check")
	}
}

// Keep the test coupled to crypto semantic constants (preventing silent test drift if detection rules evolve).
var _ = crypto.PurposePayload
