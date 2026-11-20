package identity

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

func TestArgon2HashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash format: %q", hash[:20])
	}
	ok, upgrade, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok || upgrade {
		t.Fatalf("verify=%v upgrade=%v err=%v", ok, upgrade, err)
	}
	ok, _, _ = VerifyPassword("wrong password", hash)
	if ok {
		t.Fatal("wrong password accepted")
	}
	// Malformed hash must be rejected without panicking.
	ok, _, err = VerifyPassword("x", "not-a-hash")
	if ok || err == nil {
		t.Fatal("malformed hash must fail")
	}
	// Malformed Argon2id parameter/version/digest edge cases must be rejected without panicking.
	for _, bad := range []string{
		"$argon2id$v=19$m=65536,t=3,p=257$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2,x=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2$$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0$",
	} {
		ok, _, err = VerifyPassword("x", bad)
		if ok || err == nil {
			t.Fatalf("malformed argon2id hash %q must be rejected", bad)
		}
	}
}

func TestDjangoPBKDF2VerifyAndUpgrade(t *testing.T) {
	hash, err := MakeDjangoPBKDF2("legacy-secret-pass", 600000)
	if err != nil {
		t.Fatal(err)
	}
	ok, upgrade, err := VerifyPassword("legacy-secret-pass", hash)
	if err != nil || !ok || !upgrade {
		t.Fatalf("verify=%v upgrade=%v err=%v", ok, upgrade, err)
	}
	ok, _, _ = VerifyPassword("wrong", hash)
	if ok {
		t.Fatal("wrong legacy password accepted")
	}
	// Legacy hashes below the allowlisted iteration count must be rejected.
	weak := "pbkdf2_sha256$1000$c2FsdA$aaaa"
	if _, _, err := VerifyPassword("x", weak); err == nil {
		t.Fatal("weak legacy hash accepted")
	}
}

func TestLDAPConfigurationValidation(t *testing.T) {
	valid := LDAPConfiguration{
		Host: "ldap.example.com", Port: 636, Transport: "ldaps",
		ServerName: "ldap.example.com", BaseDN: "dc=example,dc=com",
		UserFilter:        "(&(objectClass=person)(uid={username}))",
		UsernameAttribute: "uid", DisplayNameAttribute: "cn",
		ConnectTimeoutMS: 3000, BindTimeoutMS: 3000, SearchTimeoutMS: 5000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Plaintext LDAP transport is forbidden.
	plain := valid
	plain.Transport = "plain"
	if err := plain.Validate(); err == nil {
		t.Fatal("plaintext ldap accepted")
	}
	// Invalid placeholder count.
	noPlaceholder := valid
	noPlaceholder.UserFilter = "(uid=user)"
	if err := noPlaceholder.Validate(); err == nil {
		t.Fatal("filter without {username} accepted")
	}
	dup := valid
	dup.UserFilter = "(|(uid={username})(cn={username}))"
	if err := dup.Validate(); err == nil {
		t.Fatal("filter with two placeholders accepted")
	}
	// Timeout bounds.
	short := valid
	short.ConnectTimeoutMS = 10
	if err := short.Validate(); err == nil {
		t.Fatal("short timeout accepted")
	}
}

func TestOIDCStateSignVerifyAndTamper(t *testing.T) {
	c := newTestCrypter(t)
	payload := oidcStatePayload{
		ProviderKey: "sso", Verifier: "v-v-v", Nonce: "n-n-n",
		ExpireAt:    time.Now().UTC().Add(stateTTL).Unix(),
		RedirectURI: "https://app.example.com/api/v4/auth/oidc/sso/callback",
	}
	state, err := SignState(c, payload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyState(c, state)
	if err != nil || got.ProviderKey != "sso" || got.Nonce != "n-n-n" {
		t.Fatalf("verify failed: %+v err=%v", got, err)
	}
	// Expired state must be rejected.
	payload.ExpireAt = time.Now().UTC().Add(-time.Minute).Unix()
	expired, _ := SignState(c, payload)
	if _, err := VerifyState(c, expired); err == nil {
		t.Fatal("expired state accepted")
	}
	// Tampered state must be rejected.
	if _, err := VerifyState(c, state+"x"); err == nil {
		t.Fatal("tampered state accepted")
	}
	if _, err := VerifyState(c, "not-a-state"); err == nil {
		t.Fatal("garbage state accepted")
	}
}

func TestOIDCConfigurationValidation(t *testing.T) {
	valid := OIDCConfiguration{
		IssuerURL: "https://sso.example.com", ClientID: "yearning",
		Scopes: []string{"openid", "profile"}, UsernameClaim: "preferred_username",
		ConnectTimeoutMS: 3000, RequestTimeoutMS: 5000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
	for name, mutate := range map[string]func(*OIDCConfiguration){
		"http issuer": func(c *OIDCConfiguration) { c.IssuerURL = "http://insecure" },
		"no openid":   func(c *OIDCConfiguration) { c.Scopes = []string{"profile"} },
		"no username": func(c *OIDCConfiguration) { c.UsernameClaim = "" },
		"bad timeout": func(c *OIDCConfiguration) { c.RequestTimeoutMS = 10 },
	} {
		mutated := valid
		mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// newTestCrypter constructs a Crypter for unit tests (using a temporary keyring file).
func newTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	dir := t.TempDir()
	// Use distinct seed material for the two keys: keyring validation forbids reusing key material
	// across purposes (AEAD/HMAC domain separation), so two all-zero keys would be rejected.
	payload := make([]byte, 32)
	state := make([]byte, 32)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	for i := range state {
		state[i] = byte(i + 65)
	}
	doc := map[string]any{
		"contract_version": 1,
		"deployment_id":    "identity-test",
		"keys": []map[string]any{
			{"key_id": "pay-1", "purpose": "payload", "status": "encrypt_decrypt",
				"key_base64": base64.StdEncoding.EncodeToString(payload)},
			{"key_id": "state-1", "purpose": "oidc_state", "status": "encrypt_decrypt",
				"key_base64": base64.StdEncoding.EncodeToString(state)},
		},
	}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidOptionalEmail(t *testing.T) {
	valid := []string{"", "   ", "alice@example.com", "qa.user+tag@sub.yearning.io"}
	for _, v := range valid {
		if !validOptionalEmail(v) {
			t.Fatalf("expected %q to be valid optional email", v)
		}
	}
	invalid := []string{"invalid_email_no_at_symbol", "@example.com", "alice@", "alice@localhost", "alice@domain.", "alice@.domain.com", "alice @example.com"}
	for _, v := range invalid {
		if validOptionalEmail(v) {
			t.Fatalf("expected %q to be rejected as invalid email", v)
		}
	}
}
