// seal.go Review-domain AEAD envelope and integrity hash helpers.
// Raw SQL, normalized contexts, normalized facts, and Raw Payloads all use
// purpose=payload envelopes with AAD binding table + primary key + column; plaintext HMACs use
// purpose=sql_fingerprint (the sole SQL-related purpose in the HMAC manifest, isolated from AEAD keys).
package application

import (
	"encoding/hex"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// sqlHMACPurpose is the SQL plaintext integrity HMAC purpose (a member of the key contract HMAC manifest).
const sqlHMACPurpose = "sql_fingerprint"

// sealPayload encrypts and returns (envelope, plaintext HMAC hex).
func sealPayload(c *crypto.Crypter, table, pk, column string, plaintext []byte) (crypto.Envelope, string, error) {
	env, err := c.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      table,
		PrimaryKey: pk,
		Column:     column,
	}, plaintext)
	if err != nil {
		return crypto.Envelope{}, "", err
	}
	mac, err := c.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		return crypto.Envelope{}, "", err
	}
	return env, hex.EncodeToString(mac), nil
}

// openPayload authenticates and decrypts; fails closed without returning partial plaintext.
func openPayload(c *crypto.Crypter, table, pk, column string, env crypto.Envelope) ([]byte, error) {
	return c.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      table,
		PrimaryKey: pk,
		Column:     column,
	}, env)
}

// sealRawEvidence seals a D006 raw payload envelope (SEC-0042): AAD binds the evidence_facts primary
// key factID (the evidence_raw_payloads row itself has no independent business primary-key semantics,
// matching the openPayload convention on the RevealRawEvidence read side), and payload_hash = HMAC of
// the plaintext raw bytes. Both write and read sides must pair via this function and openPayload;
// assembling AAD inline or reusing the normalized fact HMAC is forbidden.
func sealRawEvidence(c *crypto.Crypter, factID string, raw []byte) (crypto.Envelope, string, error) {
	return sealPayload(c, "evidence_raw_payloads", factID, "raw_payload_ciphertext", raw)
}

// factHMAC computes the hex HMAC of arbitrary bytes (fact/snapshot hash).
func factHMAC(c *crypto.Crypter, data []byte) (string, error) {
	mac, err := c.HMAC(sqlHMACPurpose, data)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(mac), nil
}
