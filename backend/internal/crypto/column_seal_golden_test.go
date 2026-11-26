package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type columnGoldenSpec struct {
	table      string
	column     string
	purpose    string
	samplePK   string
	sampleBody string
}

// allRepositoryColumnAADSpecs enumerates every (Table, Column, Purpose) tuple across
// all encrypted database columns in Yearning v4.
var allRepositoryColumnAADSpecs = []columnGoldenSpec{
	{table: "ai_assistant_context_summaries", column: "summary", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111101", sampleBody: `{"summary":"compacted turn context"}`},
	{table: "ai_assistant_sessions", column: "title", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111102", sampleBody: "Investigate slow orders query"},
	{table: "ai_assistant_messages", column: "content", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111103", sampleBody: `{"content":"SELECT id FROM orders LIMIT 10"}`},
	{table: "ai_assistant_sql_drafts", column: "sql", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111104", sampleBody: `{"sql_text":"ALTER TABLE orders ADD COLUMN note text;"}`},
	{table: "ai_providers", column: "secret_ciphertext", purpose: PurposeProviderSecret, samplePK: "11111111-1111-4111-8111-111111111105", sampleBody: "sk-live-provider-key"},
	{table: "order_sql_payloads", column: "sql_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111106", sampleBody: "UPDATE accounts SET status = 'active' WHERE id = 42;"},
	{table: "draft_sql_payloads", column: "sql_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111107", sampleBody: "CREATE INDEX CONCURRENTLY idx_orders_created_at ON orders (created_at);"},
	{table: "order_flow_snapshots", column: "flow_snapshot_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111108", sampleBody: `{"flow_id":"flow-1","flow_name":"prod-change"}`},
	{table: "order_stages", column: "target_snapshot_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111109", sampleBody: `{"datasource_id":"ds-1","engine":"postgres"}`},
	{table: "stage_review_snapshots", column: "normalized_context_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111110#1", sampleBody: `{"search_path":["public"]}`},
	{table: "evidence_facts", column: "normalized_fact_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111111", sampleBody: `{"kind":"table_schema","table":"orders"}`},
	{table: "evidence_raw_payloads", column: "raw_payload_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111112", sampleBody: `{"raw_ddl":"CREATE TABLE orders (id bigint primary key)"}`},
	{table: "review_identifier_mappings", column: "identifier_mapping", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111113#1", sampleBody: `[{"token":"table_1","raw":"orders"}]`},
	{table: "datasource_credentials", column: "credential", purpose: PurposeDatasourceCredential, samplePK: "11111111-1111-4111-8111-111111111114", sampleBody: `{"username":"app_ro","password":"s3cr3t"}`},
	{table: "datasource_tls_materials", column: "material", purpose: PurposeDatasourceTLS, samplePK: "11111111-1111-4111-8111-111111111115", sampleBody: "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n"},
	{table: "sql_diagnostics_ai_reports", column: "report", purpose: PurposeDiagnosticsAIReport, samplePK: "11111111-1111-4111-8111-111111111116", sampleBody: `{"observed_facts":["seq scan on orders"]}`},
	{table: "sql_diagnostics_ai_reports", column: "candidate_sql", purpose: PurposeDiagnosticsCandidateSQL, samplePK: "11111111-1111-4111-8111-111111111116", sampleBody: "CREATE INDEX idx_orders_user_id ON orders(user_id);"},
	{table: "sql_diagnostics_statements", column: "template", purpose: PurposeDiagnosticsStatementTemplate, samplePK: "11111111-1111-4111-8111-111111111117", sampleBody: "SELECT * FROM orders WHERE user_id = ?"},
	{table: "execution_attempts", column: "osc_plan_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111118", sampleBody: `{"tool":"gh-ost","table":"orders"}`},
	{table: "execution_statements", column: "error_detail", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111118#2", sampleBody: "ERROR: duplicate key value violates unique constraint"},
	{table: "execution_verifications", column: "reason", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111118#reason", sampleBody: `"verified committed row count on primary"`},
	{table: "execution_verifications", column: "evidence_bundle", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111118#evidence", sampleBody: `{"txid":"981273"}`},
	{table: "order_rollback_evidences", column: "rollback_payload_ciphertext", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111119", sampleBody: `{"rollback_sql":"DELETE FROM orders WHERE id = 99;"}`},
	{table: "identity_providers", column: "secret", purpose: PurposeProviderSecret, samplePK: "11111111-1111-4111-8111-111111111120", sampleBody: "oidc-client-secret-value"},
	{table: "notification_channels", column: "secret", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111121", sampleBody: "smtp-password-or-webhook-secret"},
	{table: "notification_deliveries", column: "recipient_address", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111122", sampleBody: "dba-oncall@example.com"},
	{table: "idempotency_records", column: "response_body_ciphertext", purpose: PurposePayload, samplePK: "user-1|POST /api/v4/orders|agg-1|idem-1", sampleBody: `{"order_id":"ord-1"}`},
	{table: "query_executions", column: "query_payload", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111123", sampleBody: "SELECT id, email FROM users WHERE active = true"},
	{table: "query_cursors", column: "cursor_state", purpose: PurposePayload, samplePK: "deadbeefcafebabe", sampleBody: `{"page":2,"offset":100}`},
	{table: "query_access_requests", column: "flow_snapshot", purpose: PurposePayload, samplePK: "11111111-1111-4111-8111-111111111124", sampleBody: `{"flow_id":"query-flow-1"}`},
	{table: "flow_stages", column: "schema_mapping", purpose: PurposeFlowSchemaMapping, samplePK: "11111111-1111-4111-8111-111111111125", sampleBody: "[]"},
}

func crypterWithDeployment(t *testing.T, deploymentID string, keyMaterial []byte) *Crypter {
	t.Helper()
	doc := map[string]any{
		"contract_version": 1,
		"deployment_id":    deploymentID,
		"keys": []map[string]any{
			{
				"key_id":     "pay-golden-1",
				"purpose":    PurposePayload,
				"status":     string(StatusEncryptDecrypt),
				"created_at": "2026-08-25T00:00:00Z",
				"key_base64": base64.StdEncoding.EncodeToString(keyMaterial),
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := LoadKeyring(path)
	if err != nil {
		t.Fatalf("LoadKeyring(%s): %v", deploymentID, err)
	}
	c, err := NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestColumnSealGoldenBidirectionalInteroperabilityAndTamperRejection(t *testing.T) {
	material := make([]byte, 32)
	for i := range material {
		material[i] = byte(0x30 + i)
	}
	primary := crypterWithDeployment(t, "deploy-golden-prod", material)
	otherDeploy := crypterWithDeployment(t, "deploy-golden-staging", material)

	seen := make(map[string]struct{}, len(allRepositoryColumnAADSpecs))
	for _, spec := range allRepositoryColumnAADSpecs {
		tupleKey := spec.table + "." + spec.column + ":" + spec.purpose
		if _, dup := seen[tupleKey]; dup {
			t.Fatalf("duplicate golden spec tuple %s", tupleKey)
		}
		seen[tupleKey] = struct{}{}

		t.Run(tupleKey, func(t *testing.T) {
			plain := []byte(spec.sampleBody)
			legacyAAD := AAD{
				DeploymentID: primary.DeploymentID(),
				Table:        spec.table,
				PrimaryKey:   spec.samplePK,
				Column:       spec.column,
				Purpose:      spec.purpose,
			}
			ref := ColumnRef{
				Table:      spec.table,
				PrimaryKey: spec.samplePK,
				Column:     spec.column,
			}
			colPurpose := Purpose(spec.purpose)

			// Byte-level AAD encoding parity.
			if got, want := ColumnAAD(primary.DeploymentID(), colPurpose, ref).EncodeAAD(), legacyAAD.EncodeAAD(); !bytes.Equal(got, want) {
				t.Fatalf("EncodeAAD mismatch: got %x want %x", got, want)
			}

			// 1. Legacy Seal -> OpenColumn must recover exact plaintext bytes.
			legacyEnv, err := primary.Seal(PurposePayload, plain, legacyAAD)
			if err != nil {
				t.Fatalf("legacy Seal failed: %v", err)
			}
			openedViaColumn, err := primary.OpenColumn(colPurpose, ref, legacyEnv)
			if err != nil {
				t.Fatalf("OpenColumn rejected legacy-sealed envelope: %v", err)
			}
			if !bytes.Equal(openedViaColumn, plain) {
				t.Fatalf("legacy->OpenColumn plaintext mismatch: got %q want %q", openedViaColumn, plain)
			}

			// 2. SealColumn -> Legacy Open must recover exact plaintext bytes.
			colEnv, err := primary.SealColumn(colPurpose, ref, plain)
			if err != nil {
				t.Fatalf("SealColumn failed: %v", err)
			}
			openedViaLegacy, err := primary.Open(PurposePayload, colEnv, legacyAAD)
			if err != nil {
				t.Fatalf("legacy Open rejected SealColumn envelope: %v", err)
			}
			if !bytes.Equal(openedViaLegacy, plain) {
				t.Fatalf("SealColumn->legacy Open plaintext mismatch: got %q want %q", openedViaLegacy, plain)
			}

			// 3. Every AAD dimension tamper must fail closed with ErrAuthenticationFailed.
			tamperCases := []struct {
				name    string
				crypter *Crypter
				purpose Purpose
				ref     ColumnRef
			}{
				{
					name:    "table_tamper",
					crypter: primary,
					purpose: colPurpose,
					ref:     ColumnRef{Table: spec.table + "_other", PrimaryKey: spec.samplePK, Column: spec.column},
				},
				{
					name:    "pk_tamper",
					crypter: primary,
					purpose: colPurpose,
					ref:     ColumnRef{Table: spec.table, PrimaryKey: spec.samplePK + "-tampered", Column: spec.column},
				},
				{
					name:    "column_tamper",
					crypter: primary,
					purpose: colPurpose,
					ref:     ColumnRef{Table: spec.table, PrimaryKey: spec.samplePK, Column: spec.column + "_other"},
				},
				{
					name:    "purpose_tamper",
					crypter: primary,
					purpose: Purpose(spec.purpose + "_other"),
					ref:     ref,
				},
				{
					name:    "deployment_tamper",
					crypter: otherDeploy,
					purpose: colPurpose,
					ref:     ref,
				},
			}
			for _, tc := range tamperCases {
				if out, err := tc.crypter.OpenColumn(tc.purpose, tc.ref, colEnv); !errors.Is(err, ErrAuthenticationFailed) || out != nil {
					t.Fatalf("%s on SealColumn envelope must fail closed: out=%q err=%v", tc.name, out, err)
				}
				if out, err := tc.crypter.OpenColumn(tc.purpose, tc.ref, legacyEnv); !errors.Is(err, ErrAuthenticationFailed) || out != nil {
					t.Fatalf("%s on legacy Seal envelope must fail closed: out=%q err=%v", tc.name, out, err)
				}
			}
		})
	}
}

func TestColumnSealNilCrypterFailsClosed(t *testing.T) {
	var nilCrypter *Crypter
	ref := ColumnRef{Table: "order_sql_payloads", PrimaryKey: "row-1", Column: "sql_ciphertext"}
	if _, err := nilCrypter.SealColumn(PurposePayload, ref, []byte("SELECT 1")); !errors.Is(err, ErrNilCrypter) {
		t.Fatalf("nil SealColumn err = %v, want ErrNilCrypter", err)
	}
	if _, err := nilCrypter.OpenColumn(PurposePayload, ref, Envelope{Algorithm: EnvelopeAlgorithm}); !errors.Is(err, ErrNilCrypter) {
		t.Fatalf("nil OpenColumn err = %v, want ErrNilCrypter", err)
	}
}
