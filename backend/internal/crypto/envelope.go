package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// AEAD contract constants (cryptographic-key-contract).
const (
	// EnvelopeAlgorithm is the sole allowed persisted algorithm identifier.
	EnvelopeAlgorithm = "aes-256-gcm"
	// NonceBytes is the standard GCM nonce length; sourced from crypto/rand and never reused under the same key.
	NonceBytes = 12
	// PurposePayload is the AEAD purpose for sensitive payloads such as credentials, TLS material, Provider Secrets, and schema mappings.
	PurposePayload = "payload"
	// HMACPurposeOIDCState is the HMAC purpose for one-time OIDC state/nonce tokens.
	HMACPurposeOIDCState = "oidc_state"
)

// AAD binds the table, primary key, column, and deployment identifier to prevent cross-record or cross-column ciphertext substitution.
type AAD struct {
	DeploymentID string
	Table        string
	PrimaryKey   string
	Column       string
	Purpose      string
}

// EncodeAAD encodes length_prefixed_binary_v1:
// version byte 0x01 + 5 fields (big-endian uint32 length + bytes).
func (a AAD) EncodeAAD() []byte {
	fields := [...]string{a.DeploymentID, a.Table, a.PrimaryKey, a.Column, a.Purpose}
	size := 1
	for _, f := range fields {
		size += 4 + len(f)
	}
	out := make([]byte, 0, size)
	out = append(out, 0x01)
	var scratch [4]byte
	for _, f := range fields {
		binary.BigEndian.PutUint32(scratch[:], uint32(len(f)))
		out = append(out, scratch[:]...)
		out = append(out, f...)
	}
	return out
}

// Envelope is the persisted authenticated encryption result.
type Envelope struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	Algorithm  string
}

// ErrAuthenticationFailed indicates authentication failure; callers must fail closed and never emit partial plaintext.
var ErrAuthenticationFailed = errors.New("crypto: envelope authentication failed")

// Crypter provides envelope encryption and HMAC primitives backed by a keyring.
type Crypter struct {
	ring *Keyring
}

// DeploymentID returns the deployment identifier used in AAD.
func (c *Crypter) DeploymentID() string { return c.ring.DeploymentID() }

// NewCrypter constructs a Crypter; ring must not be nil.
func NewCrypter(ring *Keyring) (*Crypter, error) {
	if ring == nil {
		return nil, fmt.Errorf("crypto: nil keyring")
	}
	return &Crypter{ring: ring}, nil
}

// Seal encrypts plaintext using the current encrypt_decrypt key for purpose.
// Generates a fresh random nonce on every call so encrypting identical plaintext twice yields different ciphertexts.
func (c *Crypter) Seal(purpose string, plaintext []byte, aad AAD) (Envelope, error) {
	keyID, key, err := c.ring.CurrentKey(purpose)
	if err != nil {
		return Envelope{}, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, NonceBytes)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, fmt.Errorf("crypto: nonce source unavailable: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, aad.EncodeAAD())
	return Envelope{Ciphertext: ct, Nonce: nonce, KeyID: keyID, Algorithm: EnvelopeAlgorithm}, nil
}

// Open authenticates and decrypts an envelope. Unknown key_id, algorithm mismatch, or AAD mismatch
// all fail uniformly without distinguishing the failure cause or returning partial plaintext.
func (c *Crypter) Open(purpose string, env Envelope, aad AAD) ([]byte, error) {
	if env.Algorithm != EnvelopeAlgorithm {
		return nil, ErrAuthenticationFailed
	}
	key, err := c.ring.KeyByID(env.KeyID)
	if err != nil {
		return nil, ErrAuthenticationFailed
	}
	if len(env.Nonce) != NonceBytes || len(env.Ciphertext) == 0 {
		return nil, ErrAuthenticationFailed
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, ErrAuthenticationFailed
	}
	plaintext, err := gcm.Open(nil, env.Nonce, env.Ciphertext, aad.EncodeAAD())
	if err != nil {
		return nil, ErrAuthenticationFailed
	}
	return plaintext, nil
}

// HMACKeyID returns the key_id of the HMAC key for purpose (when the fingerprint key ID must be persisted).
func (c *Crypter) HMACKeyID(purpose string) (string, error) {
	id, _, err := c.ring.CurrentKey(purpose)
	return id, err
}

// HMAC computes HMAC-SHA-256 using the purpose-specific key; isolation from AEAD keys is enforced by keyring validation.
func (c *Crypter) HMAC(purpose string, data []byte) ([]byte, error) {
	_, key, err := c.ring.CurrentKey(purpose)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

// VerifyHMAC performs a constant-time HMAC comparison.
func (c *Crypter) VerifyHMAC(purpose string, data, expected []byte) (bool, error) {
	actual, err := c.HMAC(purpose, data)
	if err != nil {
		return false, err
	}
	return hmac.Equal(actual, expected), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: key init failed: %w", err)
	}
	return cipher.NewGCM(block)
}
