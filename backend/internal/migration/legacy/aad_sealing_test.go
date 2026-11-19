package legacy

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

// This file pins the core invariants of RCP-20260831-LEGACY-MIGRATION-AAD (Option A):
// migration-side AAD bindings match runtime reader AAD values field for field, migrated ciphertexts
// decrypt cleanly at runtime, and AAD cross-record/cross-field/cross-deployment tamper resistance holds for migrated rows.
//
// The runtime AAD table is copied verbatim from the runtime reader call sites (annotated in comments);
// if runtime AAD fields change, this table must be updated in lockstep (guarded by
// TestRuntimeAADSpecGuardsMigrationTable).

const (
	aadTestDeployment = "deploy-aad-test"
	aadTestKeyID      = "mig-key-1"
)

var aadTestKey = []byte(strings.Repeat("m", 32))

// runtimeCrypter constructs a runtime Crypter using the target environment's keyring file format
// (single payload key sharing the same key material as the migration Crypter).
func runtimeCrypter(t *testing.T, deploymentID string) *crypto.Crypter {
	t.Helper()
	kr := map[string]any{
		"contract_version": 1,
		"deployment_id":    deploymentID,
		"keys": []map[string]any{{
			"key_id": aadTestKeyID, "purpose": "payload",
			"status": "encrypt_decrypt", "created_at": "2026-01-01T00:00:00Z",
			"key_base64": base64.StdEncoding.EncodeToString(aadTestKey),
		}},
	}
	raw, err := json.Marshal(kr)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(p, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(p)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// runtimeAADSpec defines the runtime reader AAD specification for a single migration sealing domain.
type runtimeAADSpec struct {
	name string
	// migrationAAD is constructed by migration-side tc.aad (the implementation under test).
	migrationAAD crypto.AAD
	// runtimeAAD is copied field for field from the runtime reader source code (test literal, not shared implementation).
	runtimeAAD crypto.AAD
}

// migrationAADSpecs covers every migration sealing domain (10 sealBytes call sites +
// 3 sealJSON snapshot domains). Table/Column/Purpose values in runtimeAAD come from:
//   - identity providers.go CreateProvider/ReplaceProvider, service.go provider secret Open
//   - notification service.go openSecret, loop.go openSecret
//   - ai providerstore.go (consistent across create/replace/list/TestConnection)
//   - datasource factory.go OpenCredential/openTLSMaterial, service.go seal surface
//   - execution service.go openCredential/openTLS
//   - changeorder read.go orderSQL, submit.go draft -> order resealing
//   - execution service.go sealErrorDetail/openErrorDetail
//   - query execute.go queryPayload, service.go sealPayload
//   - execution preflight.go order flow snapshot reader
//   - review fakerunner.go stage review context writer (table has no runtime reader; mirrors writer)
//   - query access.go flow_snapshot writer binds row primary key requestID (see transforms_history note)
func migrationAADSpecs(tc *TransformContext) []runtimeAADSpec {
	spec := func(table, pk, column, purpose string) (crypto.AAD, crypto.AAD) {
		return tc.aad(table, pk, column, purpose), crypto.AAD{
			DeploymentID: aadTestDeployment, Table: table,
			PrimaryKey: pk, Column: column, Purpose: purpose,
		}
	}
	mk := func(name string, m, r crypto.AAD) runtimeAADSpec {
		return runtimeAADSpec{name: name, migrationAAD: m, runtimeAAD: r}
	}
	var out []runtimeAADSpec
	for _, s := range []struct {
		name, table, pk, column, purpose string
	}{
		{"identity provider secret", "identity_providers", "11111111-1111-1111-1111-111111111111", "secret", "provider_secret"},
		{"notification channel secret (email)", "notification_channels", "22222222-2222-2222-2222-222222222222", "secret", crypto.PurposePayload},
		{"notification channel secret (dingtalk)", "notification_channels", "33333333-3333-3333-3333-333333333333", "secret", crypto.PurposePayload},
		{"ai provider api key", "ai_providers", "44444444-4444-4444-4444-444444444444", "secret_ciphertext", "provider_secret"},
		{"datasource credential", "datasource_credentials", "55555555-5555-5555-5555-555555555555", "credential", "datasource_credential"},
		{"datasource tls material", "datasource_tls_materials", "66666666-6666-6666-6666-666666666666", "material", "datasource_tls"},
		{"order sql payload", "order_sql_payloads", "77777777-7777-7777-7777-777777777777", "sql_ciphertext", crypto.PurposePayload},
		{"draft sql payload", "draft_sql_payloads", "88888888-8888-8888-8888-888888888888", "sql_ciphertext", crypto.PurposePayload},
		{"statement error detail", "execution_statements", "99999999-9999-9999-9999-999999999999#3", "error_detail", crypto.PurposePayload},
		{"query sql payload", "query_executions", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "query_payload", crypto.PurposePayload},
		{"order flow snapshot", "order_flow_snapshots", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "flow_snapshot_ciphertext", crypto.PurposePayload},
		{"stage review context", "stage_review_snapshots", "cccccccc-cccc-cccc-cccc-cccccccccccc#1", "normalized_context_ciphertext", crypto.PurposePayload},
		{"order stage target snapshot", "order_stages", "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", "target_snapshot_ciphertext", crypto.PurposePayload},
		{"query access snapshot", "query_access_requests", "dddddddd-dddd-dddd-dddd-dddddddddddd", "flow_snapshot", crypto.PurposePayload},
	} {
		m, r := spec(s.table, s.pk, s.column, s.purpose)
		out = append(out, mk(s.name, m, r))
	}
	return out
}

// TestRuntimeAADSpecGuardsMigrationTable asserts that the migration AAD table matches the runtime
// specification field for field -- all five fields produced by tc.aad must equal the runtime literals.
func TestRuntimeAADSpecGuardsMigrationTable(t *testing.T) {
	c, err := NewCrypter(aadTestKey, aadTestKeyID, aadTestDeployment, nil)
	if err != nil {
		t.Fatal(err)
	}
	tc := NewTransformContext(mustUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), c, time.UTC)
	for _, spec := range migrationAADSpecs(tc) {
		got, want := spec.migrationAAD, spec.runtimeAAD
		if got.DeploymentID != want.DeploymentID || got.Table != want.Table ||
			got.PrimaryKey != want.PrimaryKey || got.Column != want.Column || got.Purpose != want.Purpose {
			t.Fatalf("%s: migration AAD %+v != runtime %+v", spec.name, got, want)
		}
	}
}

// TestMigrationSealOpensWithRuntimeCrypter core gate: ciphertexts across all migration sealing domains
// decrypt cleanly via the runtime Crypter using the reader's AAD and match the original plaintext
// (full verification matrix for Option A's "migrated secret rows are decryptable at runtime" invariant).
func TestMigrationSealOpensWithRuntimeCrypter(t *testing.T) {
	c, err := NewCrypter(aadTestKey, aadTestKeyID, aadTestDeployment, nil)
	if err != nil {
		t.Fatal(err)
	}
	tc := NewTransformContext(mustUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), c, time.UTC)
	runtime := runtimeCrypter(t, aadTestDeployment)
	for _, spec := range migrationAADSpecs(tc) {
		plain := []byte("plaintext-for:" + spec.name)
		sealed, err := c.Seal(plain, spec.migrationAAD)
		if err != nil {
			t.Fatalf("%s: seal: %v", spec.name, err)
		}
		opened, err := runtime.Open(crypto.PurposePayload, crypto.Envelope{
			Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
			KeyID: sealed.KeyID, Algorithm: sealed.Algorithm,
		}, spec.runtimeAAD)
		if err != nil {
			t.Fatalf("%s: runtime open: %v (AAD asymmetry)", spec.name, err)
		}
		if string(opened) != string(plain) {
			t.Fatalf("%s: plaintext mismatch", spec.name)
		}
	}
}

// TestMigrationAADTamperInvariants gate 2: AAD tamper resistance against cross-record, cross-field,
// cross-deployment, and cross-purpose ciphertext substitution holds for migrated artifacts (all tamper/swap cases must fail).
func TestMigrationAADTamperInvariants(t *testing.T) {
	c, err := NewCrypter(aadTestKey, aadTestKeyID, aadTestDeployment, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimeCrypter(t, aadTestDeployment)

	seal := func(aad crypto.AAD, plain string) crypto.Envelope {
		s, err := c.Seal([]byte(plain), aad)
		if err != nil {
			t.Fatal(err)
		}
		return crypto.Envelope{Ciphertext: s.Ciphertext, Nonce: s.Nonce, KeyID: s.KeyID, Algorithm: s.Algorithm}
	}
	open := func(aad crypto.AAD, env crypto.Envelope) bool {
		_, err := runtime.Open(crypto.PurposePayload, env, aad)
		return err == nil
	}
	credA := c.AAD("datasource_credentials", "row-a", "credential", "datasource_credential")
	credB := c.AAD("datasource_credentials", "row-b", "credential", "datasource_credential")
	envA, envB := seal(credA, "secret-a"), seal(credB, "secret-b")

	if !open(credA, envA) || !open(credB, envB) {
		t.Fatal("baseline: same-AAD open must succeed")
	}
	// Cross-record swap: ciphertext from row A opened with row B's AAD.
	if open(credB, envA) {
		t.Fatal("cross-record swap must fail")
	}
	// Same PK across tables: credential ciphertext opened as TLS material.
	tlsOfA := c.AAD("datasource_tls_materials", "row-a", "material", "datasource_tls")
	if open(tlsOfA, envA) {
		t.Fatal("cross-table swap must fail")
	}
	// Same table and PK across columns (column name drift).
	wrongColumn := c.AAD("datasource_credentials", "row-a", "material", "datasource_credential")
	if open(wrongColumn, envA) {
		t.Fatal("cross-column swap must fail")
	}
	// Purpose drift.
	wrongPurpose := c.AAD("datasource_credentials", "row-a", "credential", "provider_secret")
	if open(wrongPurpose, envA) {
		t.Fatal("purpose drift must fail")
	}
	// Deployment drift: a keyring from another deployment cannot decrypt this deployment's ciphertext.
	otherDeploy := runtimeCrypter(t, "deploy-other")
	if _, err := otherDeploy.Open(crypto.PurposePayload, envA, crypto.AAD{
		DeploymentID: "deploy-other", Table: "datasource_credentials",
		PrimaryKey: "row-a", Column: "credential", Purpose: "datasource_credential",
	}); err == nil {
		t.Fatal("deployment drift must fail")
	}
}

// TestCrypterDeploymentIDValidation constructor guard: missing or overlong deployment_id is rejected,
// matching the deployment_id constraint (1..128 chars) in keyring.go.
func TestCrypterDeploymentIDValidation(t *testing.T) {
	if _, err := NewCrypter(aadTestKey, aadTestKeyID, "", nil); err == nil {
		t.Fatal("empty deployment id must be rejected")
	}
	if _, err := NewCrypter(aadTestKey, aadTestKeyID, strings.Repeat("d", 129), nil); err == nil {
		t.Fatal("over-length deployment id must be rejected")
	}
	if _, err := NewCrypter(aadTestKey, aadTestKeyID, aadTestDeployment, nil); err != nil {
		t.Fatalf("valid deployment id rejected: %v", err)
	}
	if id := (&Crypter{deploymentID: aadTestDeployment}).DeploymentID(); id != aadTestDeployment {
		t.Fatal("DeploymentID accessor broken")
	}
}

// TestTransformDataSourceCredentialChain (R1 Medium-1) tests the full credential chain starting from
// the actual Transform: legacy row username/password -> TransformDataSource migration sealing
// (credentialPayload JSON + domain AAD binding) -> runtime keyring Crypter opening with
// factory.OpenCredential's exact AAD and recovering username/password.
// Previously datasource package tests reconstructed the seal manually without going through Transform,
// which would stay green even if Transform wrote the wrong field name or primary key.
func TestTransformDataSourceCredentialChain(t *testing.T) {
	c, err := NewCrypter(aadTestKey, aadTestKeyID, aadTestDeployment, nil)
	if err != nil {
		t.Fatal(err)
	}
	tc := NewTransformContext(mustUUID("12121212-1212-4121-8121-121212121212"), c, time.UTC)
	if err := tc.TransformDataSource(Row{
		"id": int64(1), "source_id": "src-cred", "source": "ds-cred",
		"db_type": int64(0), "ip": "db.internal", "port": int64(3306),
		"is_query": int64(0),
		"username": "legacy-user", "password": "legacy-pw",
	}, &StatusEvidence{}); err != nil {
		t.Fatal(err)
	}
	var cred *DatasourceCredentialModel
	for i := range tc.writes {
		if tc.writes[i].Table != "datasource_credentials" {
			continue
		}
		m := tc.writes[i].Model.(DatasourceCredentialModel)
		cred = &m
	}
	if cred == nil {
		t.Fatal("credential row missing from writes")
	}
	runtime := runtimeCrypter(t, aadTestDeployment)
	// Literal AAD matches datasource factory.go OpenCredential (constructed without tc.aad
	// to avoid tautological self-verification).
	plain, err := runtime.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: cred.CredentialCiphertext, Nonce: cred.CredentialNonce,
		KeyID: cred.CredentialKeyID, Algorithm: cred.CredentialAlgorithm,
	}, crypto.AAD{
		DeploymentID: aadTestDeployment,
		Table:        "datasource_credentials", PrimaryKey: cred.ID,
		Column: "credential", Purpose: "datasource_credential",
	})
	if err != nil {
		t.Fatalf("runtime reader rejected transform-sealed credential: %v", err)
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("credential payload not runtime credentialPayload JSON: %v", err)
	}
	if payload.Username != "legacy-user" || payload.Password != "legacy-pw" {
		t.Fatalf("payload mismatch: %+v", payload)
	}
}
