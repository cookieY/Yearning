package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

// Argon2id parameters (argon2id, m=64MiB, t=3, p=2, salt=16B).
// Parameters are frozen in the Release Manifest (cryptographic-key-contract); v4 uses these defaults.
const (
	argonTime    = 3
	argonMemoryK = 65536
	argonThreads = 2
	argonSaltLen = 16
	argonKeyLen  = 32
)

// djangoMinIterations matches the migration allowlist: legacy hashes below this iteration count are rejected.
const djangoMinIterations = 600000

// HashPassword generates a PHC-formatted Argon2id password hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("identity: salt source unavailable: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryK, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemoryK, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword verifies a stored password hash. Supports:
//   - argon2id (v4 native writes)
//   - pbkdf2_sha256 (migrated Django hashes, accepted only when iterations >= 600000)
//
// Malformed hashes return an error without panicking; match status and upgrade-needed flag are returned separately.
func VerifyPassword(password, stored string) (ok bool, needsUpgrade bool, err error) {
	switch {
	case strings.HasPrefix(stored, "$argon2id$"):
		ok, err := verifyArgon2id(password, stored)
		return ok, false, err
	case strings.HasPrefix(stored, "pbkdf2_sha256$"):
		ok, err := verifyDjangoPBKDF2(password, stored)
		return ok, ok, err
	default:
		return false, true, fmt.Errorf("identity: unsupported password hash format")
	}
}

func verifyArgon2id(password, stored string) (bool, error) {
	parts := strings.Split(stored, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("identity: malformed argon2id hash")
	}
	var m, t uint32
	var p uint8
	for _, kv := range strings.Split(parts[3], ",") {
		fields := strings.SplitN(kv, "=", 2)
		if len(fields) != 2 {
			return false, fmt.Errorf("identity: malformed argon2id parameters")
		}
		n, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return false, fmt.Errorf("identity: malformed argon2id parameters")
		}
		switch fields[0] {
		case "m":
			m = uint32(n)
		case "t":
			t = uint32(n)
		case "p":
			p = uint8(n)
		}
	}
	if m == 0 || t == 0 || p == 0 {
		return false, fmt.Errorf("identity: malformed argon2id parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("identity: malformed argon2id salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("identity: malformed argon2id digest")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	if len(got) != len(want) {
		return false, nil
	}
	// Constant-time comparison: never returns early on the first differing byte to prevent timing leaks.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, nil
	}
	return true, nil
}

// verifyDjangoPBKDF2 verifies a Django-formatted hash (pbkdf2_sha256$iter$salt_b64$hash_b64).
// Iteration counts below the allowlist are rejected outright (migration contract: malformed or weak legacy hashes must not pass).
func verifyDjangoPBKDF2(password, stored string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 {
		return false, fmt.Errorf("identity: malformed legacy hash")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < djangoMinIterations {
		return false, fmt.Errorf("identity: legacy hash below iteration allowlist")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return false, fmt.Errorf("identity: malformed legacy salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false, fmt.Errorf("identity: malformed legacy digest")
	}
	got := pbkdf2SHA256([]byte(password), salt, iterations, len(want))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, nil
	}
	return true, nil
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	// Matches Django pbkdf2(password, salt, iterations, dklen=32, sha256).
	return pbkdf2.Key(password, salt, iterations, keyLen, sha256.New)
}

// MakeDjangoPBKDF2 generates an allowlist-strength legacy hash (used in migration compatibility tests and CLI reset tests).
func MakeDjangoPBKDF2(password string, iterations int) (string, error) {
	if iterations < djangoMinIterations {
		return "", fmt.Errorf("identity: iterations below allowlist")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("identity: salt source unavailable: %w", err)
	}
	dk := pbkdf2.Key([]byte(password), salt, iterations, 32, sha256.New)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s",
		iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

// TokenHash returns the hex-encoded SHA-256 digest of a session or CSRF token (stored in auth_sessions.*_char64 columns).
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}

// RandomToken generates a 64-character hex-encoded high-entropy token.
func RandomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity: token source unavailable: %w", err)
	}
	return fmt.Sprintf("%x", raw), nil
}
