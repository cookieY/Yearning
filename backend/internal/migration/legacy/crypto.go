package legacy

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// EnvelopeAlgorithm is the v4 ciphertext envelope algorithm identifier written to all *_algorithm columns.
const EnvelopeAlgorithm = "aes-256-gcm"

// Crypter provides v4 envelope encryption and legacy AES-128-CBC decryption.
// Key material is never written to logs, reports, or the Manifest.
type Crypter struct {
	key             []byte
	keyID           string
	deploymentID    string // Part of the AAD, matching the target keyring's deployment_id (RCP-20260831 Option A)
	legacySecretKey []byte // May be nil; see RCP-20260825-B2-LEGACY-SECRET-KEY
}

// NewCrypter constructs a Crypter; the v4 key must be 32 bytes (AES-256).
// deploymentId must match the target environment keyring's deployment_id (1..128 chars)
// so runtime reader AADs can authenticate migrated ciphertexts.
func NewCrypter(key []byte, keyID, deploymentId string, legacySecretKey []byte) (*Crypter, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("envelope key must be 32 bytes for AES-256-GCM, got %d", len(key))
	}
	if keyID == "" {
		return nil, fmt.Errorf("envelope key id required")
	}
	if deploymentId == "" || len(deploymentId) > 128 {
		return nil, fmt.Errorf("envelope deployment id required (must match the target keyring deployment_id)")
	}
	if len(legacySecretKey) > 0 && len(legacySecretKey) != 16 {
		return nil, fmt.Errorf("legacy secret key must be 16 bytes for AES-128-CBC")
	}
	return &Crypter{key: key, keyID: keyID, deploymentID: deploymentId, legacySecretKey: legacySecretKey}, nil
}

// DeploymentID returns the deployment identifier used in AAD bindings (non-secret).
func (c *Crypter) DeploymentID() string { return c.deploymentID }

// AAD constructs a runtime AAD bound to this deployment ID; the caller supplies table/primaryKey/column/purpose
// matching the corresponding runtime reader field for field (preventing cross-record and cross-field ciphertext swaps).
func (c *Crypter) AAD(table, primaryKey, column, purpose string) crypto.AAD {
	return crypto.AAD{
		DeploymentID: c.deploymentID,
		Table:        table,
		PrimaryKey:   primaryKey,
		Column:       column,
		Purpose:      purpose,
	}
}

// Sealed holds the envelope encryption result mapped to each table's ciphertext/nonce/key_id/algorithm columns.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	Algorithm  string
}

// Seal encrypts plaintext with AES-256-GCM bound to the runtime AAD, generating a fresh random nonce per call.
// aad must be constructed via Crypter.AAD (or match the runtime reader field for field); leaving fields empty
// forfeits binding and will fail authentication on the runtime reader side.
func (c *Crypter) Seal(plaintext []byte, aad crypto.AAD) (Sealed, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return Sealed{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("generate nonce: %w", err)
	}
	out := gcm.Seal(nil, nonce, plaintext, aad.EncodeAAD())
	return Sealed{Ciphertext: out, Nonce: nonce, KeyID: c.keyID, Algorithm: EnvelopeAlgorithm}, nil
}

// Open decrypts a Sealed envelope using the runtime AAD for sample decryption verification during the verify phase.
// Any AAD mismatch (cross-record/cross-field swap or deployment_id drift) fails authentication.
func (c *Crypter) Open(s Sealed, aad crypto.AAD) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(s.Nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("nonce size mismatch")
	}
	return gcm.Open(nil, s.Nonce, s.Ciphertext, aad.EncodeAAD())
}

// DomainHMAC computes a domain-separated plaintext HMAC (irreversible sample fingerprint).
// Reports and Manifests may include only this HMAC and never raw hashes of low-entropy fields.
func (c *Crypter) DomainHMAC(domain string, plaintext []byte) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte("yearning:v4:hmac:" + domain + ":"))
	mac.Write(plaintext)
	return fmt.Sprintf("%x", mac.Sum(nil))
}

// LegacyDecrypt decrypts a legacy datasource password ciphertext using legacy General.SecretKey
// (AES-128-CBC, IV = first 16 bytes of key, base64-encoded, PKCS#7 padded).
// Returns ErrLegacyKeyMissing when the legacy key is not configured, requiring the caller to quarantine the row.
func (c *Crypter) LegacyDecrypt(ciphertextB64 string) ([]byte, error) {
	if len(c.legacySecretKey) == 0 {
		return nil, ErrLegacyKeyMissing
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("legacy ciphertext base64: %w", err)
	}
	block, err := aes.NewCipher(c.legacySecretKey)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("legacy ciphertext length invalid")
	}
	iv := c.legacySecretKey[:block.BlockSize()]
	mode := cipher.NewCBCDecrypter(block, iv)
	out := make([]byte, len(raw))
	mode.CryptBlocks(out, raw)
	out, err = pkcs7Unpad(out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ErrLegacyKeyMissing indicates the legacy secret key is not configured; encrypted rows must be quarantined and block Apply.
var ErrLegacyKeyMissing = fmt.Errorf("legacy secret key not configured; ciphertext rows must be quarantined")

// LegacyEncrypt is the inverse of LegacyDecrypt, used strictly for test fixtures and round-trip tests.
func LegacyEncrypt(secretKey []byte, plaintext []byte) (string, error) {
	if len(secretKey) != 16 {
		return "", fmt.Errorf("legacy key must be 16 bytes")
	}
	block, err := aes.NewCipher(secretKey)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plaintext, block.BlockSize())
	iv := secretKey[:block.BlockSize()]
	mode := cipher.NewCBCEncrypter(block, iv)
	out := make([]byte, len(padded))
	mode.CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	return append(append([]byte{}, data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > len(data) {
		return nil, fmt.Errorf("legacy padding invalid")
	}
	return data[:len(data)-pad], nil
}

// loadLocation wraps time.LoadLocation so tests can inject a fixed time zone.
func loadLocation(name string) (*time.Location, error) {
	return time.LoadLocation(name)
}
