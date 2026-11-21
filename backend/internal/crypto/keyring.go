// Package crypto implements the cryptographic facilities defined by cryptographic-key-contract:
// read-only file keyring, AEAD envelopes, and HMAC domain separation.
//
// The keyring file is a Kubernetes Secret volume (or root read-only file) mounted by the deployment
// as single-line JSON; private key material never enters databases, config files, environment variables, CLI flags, or logs.
package crypto

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// KeyStatus represents the capability status of a keyring entry.
type KeyStatus string

const (
	StatusEncryptDecrypt KeyStatus = "encrypt_decrypt"
	StatusDecryptOnly    KeyStatus = "decrypt_only"
)

// keyIDPattern matches key_id_pattern in cryptographic-key-contract.
var keyIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

// keyBytes is fixed at 32 bytes by contract (key_bytes=32 for both aead and hmac);
// invalid lengths fail immediately at load time and never reach runtime cipher initialization.
const keyBytes = 32

// keyringFile is the on-disk format of the keyring file. Material is base64-encoded;
// file layout is enforced by ValidateKeyringFile (<=0400, controlled symlinks, parent directory
// not writable by unprivileged users — cryptographic-key-contract key_provider).
type keyringFile struct {
	ContractVersion int               `json:"contract_version"`
	DeploymentID    string            `json:"deployment_id"`
	Keys            []keyringFileItem `json:"keys"`
}

type keyringFileItem struct {
	KeyID      string `json:"key_id"`
	Purpose    string `json:"purpose"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	KeyBase64  string `json:"key_base64"`
	MaterialFn string `json:"material_file,omitempty"`
}

// KeyMetadata is a displayable entry stripped of secret key material.
type KeyMetadata struct {
	KeyID     string    `json:"key_id"`
	Purpose   string    `json:"purpose"`
	Status    KeyStatus `json:"status"`
	CreatedAt string    `json:"created_at"`
}

// Keyring holds parsed keys; immutable once loaded by LoadKeyring and safe for concurrent reads.
type Keyring struct {
	deployment  string
	byID        map[string][]byte
	metaByID    map[string]KeyMetadata
	current     map[string]string // purpose -> encrypt_decrypt key_id
	hasStateKey bool
}

var (
	// ErrUnknownKey indicates key_id was not found in the keyring during decryption (fail closed).
	ErrUnknownKey = fmt.Errorf("crypto: unknown key id")
	// ErrNoCurrentKey indicates the purpose has no encrypt_decrypt key.
	ErrNoCurrentKey = fmt.Errorf("crypto: no encrypt_decrypt key for purpose")
)

// ValidateKeyringFile validates the deployment layout of the keyring file and returns its
// symlink-resolved real path (SEC-0017; cryptographic-key-contract key_provider:
// file_mode_maximum=0400, symlink and parent directory permissions must be verified):
//   - Final target must be a regular file (rejecting directories, devices, pipes, etc.);
//   - Permissions must not exceed contract file_mode_maximum=0400; group-read (0440) is accepted
//     only under the K8s Secret volume symlink layout (where the resolved path or chain contains a "..data" segment);
//   - Direct parent directory of the target must not be group- or world-writable;
//   - Symlinks are allowed only when their resolved target remains inside the keyring directory —
//     Kubernetes Secret volumes implement atomic updates via "keyring.json -> ..data/keyring.json -> ..<timestamp>/keyring.json",
//     which is valid; links pointing outside the keyring directory are rejected (preventing redirection to an attacker-controlled keyring).
func ValidateKeyringFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("crypto: keyring path empty")
	}
	clean := filepath.Clean(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return "", fmt.Errorf("crypto: keyring parent directory not resolvable: %w", err)
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", fmt.Errorf("crypto: keyring not readable: %w", err)
	}
	target := clean
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, rerr := filepath.EvalSymlinks(clean)
		if rerr != nil {
			return "", fmt.Errorf("crypto: keyring symlink unresolvable: %w", rerr)
		}
		if resolved != parent && !strings.HasPrefix(resolved, parent+string(filepath.Separator)) {
			return "", fmt.Errorf("crypto: keyring symlink escapes its directory")
		}
		target = resolved
		if info, err = os.Lstat(target); err != nil {
			return "", fmt.Errorf("crypto: keyring target not readable: %w", err)
		}
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("crypto: keyring is not a regular file")
	}
	// Contract file_mode_maximum=0400; narrowed in code: 0440 is accepted only under the K8s Secret volume
	// symlink layout (kubelet atomic write: keyring.json -> ..data/keyring.json -> ..<timestamp>/keyring.json,
	// where a non-root Pod + fsGroup cannot read a root-owned 0400 file so the tightest achievable mode is 0440).
	// Detected when either the resolved real path or any hop in the symlink chain contains a "..data" path segment;
	// regular file paths remain strictly capped at 0400. Any write bit or other-user bit (beyond 0440) is rejected in all layouts.
	if perm := info.Mode().Perm(); perm&^0o400 != 0 {
		if perm&^0o440 != 0 || !kubeletSecretVolumeForm(clean, target) {
			return "", fmt.Errorf("crypto: keyring file mode %04o exceeds contract maximum 0400 (0440 only for K8s Secret ..data volume form)", perm)
		}
	}
	pinfo, err := os.Stat(filepath.Dir(target))
	if err != nil || !pinfo.IsDir() {
		return "", fmt.Errorf("crypto: keyring parent directory not readable")
	}
	if pinfo.Mode().Perm()&(0o020|0o002) != 0 {
		return "", fmt.Errorf("crypto: keyring parent directory writable by group/others")
	}
	return target, nil
}

// kubeletSecretVolumeForm reports whether the keyring is in the K8s Secret volume symlink layout:
// the resolved real path (target or its parent chain) or any hop target in the symlink resolution chain
// contains a "..data" path segment. The kubelet atomic-write layout is:
//
//	<vol>/keyring.json -> <vol>/..data/keyring.json -> <vol>/..<timestamp>/keyring.json
//
// where ..data itself is a symlink to the timestamped directory — full resolution via filepath.EvalSymlinks
// collapses ..data (leaving only the timestamped directory in the final path), so we must also inspect
// each hop of the symlink chain (capped at 32 hops against loops; projected volume layouts where ..data
// is a real directory are covered by the final-path check).
func kubeletSecretVolumeForm(original, resolved string) bool {
	if pathHasDataSegment(resolved) {
		return true
	}
	cur := original
	for hop := 0; hop < 32; hop++ {
		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return false
		}
		link, err := os.Readlink(cur)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(cur), link)
		}
		cur = filepath.Clean(link)
		if pathHasDataSegment(cur) {
			return true
		}
	}
	return false
}

// pathHasDataSegment reports whether any component of p equals "..data".
func pathHasDataSegment(p string) bool {
	for _, seg := range strings.Split(filepath.Clean(p), string(filepath.Separator)) {
		if seg == "..data" {
			return true
		}
	}
	return false
}

// LoadKeyring reads and validates a keyring file.
// Duplicate key_id values, cross-purpose key material reuse, or multiple encrypt_decrypt keys
// within a single purpose are fatal startup errors.
func LoadKeyring(path string) (*Keyring, error) {
	resolved, err := ValidateKeyringFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("crypto: keyring not readable: %w", err)
	}
	if len(raw) < keyBytes || len(raw) > 1<<20 {
		return nil, fmt.Errorf("crypto: keyring size out of bounds")
	}
	var f keyringFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("crypto: keyring malformed: %w", err)
	}
	if f.ContractVersion != 1 {
		return nil, fmt.Errorf("crypto: unsupported keyring contract_version %d", f.ContractVersion)
	}
	if f.DeploymentID == "" || len(f.DeploymentID) > 128 {
		return nil, fmt.Errorf("crypto: keyring deployment_id invalid")
	}
	if len(f.Keys) == 0 {
		return nil, fmt.Errorf("crypto: keyring has no keys")
	}
	r := &Keyring{
		deployment: f.DeploymentID,
		byID:       map[string][]byte{},
		metaByID:   map[string]KeyMetadata{},
		current:    map[string]string{},
	}
	// Key material digest -> first seen purpose: reusing the same 32-byte material across purposes
	// breaks AEAD/HMAC domain separation (contract hmac.key_reuse_with_aead_or_password_hash=forbidden)
	// and is fatal at load time (SEC-0046 detection surface).
	materialPurpose := map[[32]byte]string{}
	for _, item := range f.Keys {
		if !keyIDPattern.MatchString(item.KeyID) {
			return nil, fmt.Errorf("crypto: key_id format invalid")
		}
		if item.Purpose == "" || len(item.Purpose) > 64 {
			return nil, fmt.Errorf("crypto: key purpose invalid")
		}
		status := KeyStatus(item.Status)
		if status != StatusEncryptDecrypt && status != StatusDecryptOnly {
			return nil, fmt.Errorf("crypto: key status invalid")
		}
		if _, dup := r.byID[item.KeyID]; dup {
			return nil, fmt.Errorf("crypto: duplicate key id at startup")
		}
		material, err := base64.StdEncoding.DecodeString(item.KeyBase64)
		if err != nil || len(material) != keyBytes {
			return nil, fmt.Errorf("crypto: key material must be exactly %d bytes", keyBytes)
		}
		var digest [32]byte
		digest = sha256.Sum256(material)
		if prevPurpose, dup := materialPurpose[digest]; dup && prevPurpose != item.Purpose {
			return nil, fmt.Errorf("crypto: key material reused across purposes %q and %q (AEAD/HMAC domain separation violated)", prevPurpose, item.Purpose)
		}
		materialPurpose[digest] = item.Purpose
		r.byID[item.KeyID] = material
		r.metaByID[item.KeyID] = KeyMetadata{
			KeyID: item.KeyID, Purpose: item.Purpose, Status: status, CreatedAt: item.CreatedAt,
		}
		if status == StatusEncryptDecrypt {
			if prev, dup := r.current[item.Purpose]; dup {
				return nil, fmt.Errorf("crypto: multiple encrypt_decrypt keys for purpose %q (%s, %s)", item.Purpose, prev, item.KeyID)
			}
			r.current[item.Purpose] = item.KeyID
		}
		if item.Purpose == HMACPurposeOIDCState {
			r.hasStateKey = true
		}
	}
	return r, nil
}

// HasStateKey reports whether the oidc_state HMAC key is present (must be true before configuring an OIDC Provider).
func (r *Keyring) HasStateKey() bool {
	return r.hasStateKey
}

// HasCurrentKey reports whether an encrypt_decrypt current key exists for purpose.
// Shared by serve startup and Readiness (contract availability:
// readiness_requires_current_and_all_referenced_keys; SEC-0046 detection surface).
func (r *Keyring) HasCurrentKey(purpose string) bool {
	_, ok := r.current[purpose]
	return ok
}

// CurrentKey returns the current encrypt_decrypt key for purpose.
func (r *Keyring) CurrentKey(purpose string) (string, []byte, error) {
	id, ok := r.current[purpose]
	if !ok {
		return "", nil, ErrNoCurrentKey
	}
	return id, r.byID[id], nil
}

// KeyByID returns the key for the specified key_id; unknown keys fail closed.
func (r *Keyring) KeyByID(id string) ([]byte, error) {
	key, ok := r.byID[id]
	if !ok {
		return nil, ErrUnknownKey
	}
	return key, nil
}

// ListMetadata returns the list of entries without secret key material.
func (r *Keyring) ListMetadata() []KeyMetadata {
	out := make([]KeyMetadata, 0, len(r.metaByID))
	for _, m := range r.metaByID {
		out = append(out, m)
	}
	return out
}

// DeploymentID returns the stable non-secret deployment identifier (part of AAD).
func (r *Keyring) DeploymentID() string {
	return r.deployment
}
