//go:build integration

package tool

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

func acquireToolTestLock(t *testing.T) {
	t.Helper()
	for _, name := range []string{"/yearningx-itest-shared.lock", "/yearningx-b13-assistant-test.lock"} {
		f, err := os.OpenFile(os.TempDir()+name, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("open lock %s: %v", name, err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			t.Fatalf("flock %s: %v", name, err)
		}
		file := f
		t.Cleanup(func() {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
		})
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func toolTestDBPassword(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("YEARNING_B13_TEST_DB_PASSWORD"); v != "" {
		return v
	}
	if v := os.Getenv("YEARNING_TEST_DATABASE_PASSWORD"); v != "" {
		return v
	}
	t.Skip("PostgreSQL test password not configured (set YEARNING_B13_TEST_DB_PASSWORD or YEARNING_TEST_DATABASE_PASSWORD)")
	return ""
}

func openToolTestDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	pass := toolTestDBPassword(t)
	acquireToolTestLock(t)
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	host := envOrDefault("YEARNING_B13_TEST_DB_HOST", "127.0.0.1")
	port := envOrDefault("YEARNING_B13_TEST_DB_PORT", "54332")
	user := envOrDefault("YEARNING_B13_TEST_DB_USER", "yearning")
	dbname := envOrDefault("YEARNING_B13_TEST_DB_NAME", "yearning_b13_test")
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, dbname)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Skipf("PostgreSQL test database not reachable (%v)", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, tbl := range []string{
		"query_grants",
		"query_sessions",
		"query_access_requests",
		"permission_group_flow_grants",
		"permission_group_members",
		"permission_groups",
		"flow_stages",
		"flows",
		"rule_sets",
		"datasource_credentials",
		"datasource_tls_materials",
		"datasources",
		"users",
	} {
		_ = db.Exec("TRUNCATE TABLE " + tbl + " CASCADE").Error
	}
	return db, pass
}

func makeToolKey32(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func newToolTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	dir := t.TempDir()
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"b13-tool-test","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"state-1","purpose":"oidc_state","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"ident-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"},
		{"key_id":"aff-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-11-07T00:00:00Z","key_base64":"%s"}
	]}`, makeToolKey32(1), makeToolKey32(2), makeToolKey32(3), makeToolKey32(4), makeToolKey32(5))
	path := dir + "/keyring.json"
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return crypter
}

func generateTestCertAndKeyPEM(t *testing.T) ([]byte, []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-db.local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM
}

func TestAdapterCatalogReaderAndAuthorizerBranches(t *testing.T) {
	db, dbPass := openToolTestDB(t)
	crypter := newToolTestCrypter(t)
	ctx := context.Background()

	reader := &AdapterCatalogReader{DB: db, Crypter: crypter}

	// Non-existent datasource ID
	if _, err := reader.ListSchemas(ctx, DatasourceTarget{DatasourceID: uuid.NewString()}); err == nil {
		t.Fatal("expected error for unknown datasource")
	}

	userID := uuid.NewString()
	if err := db.Exec(`INSERT INTO users (id, username, display_name, password_hash, is_builtin_admin, version, created_at, updated_at)
		VALUES (?, 'tooluser', 'tooluser', 'argon2id$test', false, 1, now(), now())`, userID).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Insert PostgreSQL datasource pointing to the test DB itself
	pgDSID := uuid.NewString()
	portStr := envOrDefault("YEARNING_B13_TEST_DB_PORT", "54332")
	dbName := envOrDefault("YEARNING_B13_TEST_DB_NAME", "yearning_b13_test")
	dbUser := envOrDefault("YEARNING_B13_TEST_DB_USER", "yearning")
	if err := db.Exec(`INSERT INTO datasources (id, name, engine, compatibility_mode, deployment_kind, host, port, database_name, version_constraint, enabled, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'live-pg-ds', 'postgresql', 'postgresql', 'native', '127.0.0.1', ?, ?, '15', true, 1, ?, now(), now())`,
		pgDSID, portStr, dbName, userID).Error; err != nil {
		t.Fatalf("insert pg datasource: %v", err)
	}

	// 1. No credentials yet -> openReadCredential fails
	if _, err := reader.ListSchemas(ctx, DatasourceTarget{DatasourceID: pgDSID}); err == nil {
		t.Fatal("expected error when no credentials exist")
	}

	// 2. Insert a corrupted review credential (bad ciphertext) and a corrupted JSON query credential
	badRevID := uuid.NewString()
	if err := db.Exec(`INSERT INTO datasource_credentials (id, datasource_id, purpose, credential_ciphertext, credential_nonce, credential_key_id, credential_algorithm, enabled, created_at, updated_at)
		VALUES (?, ?, 'review', '\xdeadbeef', '\x0102030405060708090a0b0c', 'pay-1', 'aes-256-gcm', true, now(), now())`,
		badRevID, pgDSID).Error; err != nil {
		t.Fatalf("insert bad review cred: %v", err)
	}

	badQryID := uuid.NewString()
	badJSONEnv, err := crypter.Seal(crypto.PurposePayload, []byte("not-json"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   badQryID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO datasource_credentials (id, datasource_id, purpose, credential_ciphertext, credential_nonce, credential_key_id, credential_algorithm, enabled, created_at, updated_at)
		VALUES (?, ?, 'query', ?, ?, ?, 'aes-256-gcm', true, now(), now())`,
		badQryID, pgDSID, badJSONEnv.Ciphertext, badJSONEnv.Nonce, badJSONEnv.KeyID).Error; err != nil {
		t.Fatalf("insert bad query cred: %v", err)
	}
	if _, err := reader.ListSchemas(ctx, DatasourceTarget{DatasourceID: pgDSID}); err == nil {
		t.Fatal("expected error when credentials fail open/unmarshal")
	}

	// Replace query credential with valid JSON credential
	_ = db.Exec(`DELETE FROM datasource_credentials WHERE id = ?`, badQryID).Error
	goodQryID := uuid.NewString()
	goodPlain := fmt.Sprintf(`{"username":%q,"password":%q}`, dbUser, dbPass)
	goodEnv, err := crypter.Seal(crypto.PurposePayload, []byte(goodPlain), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   goodQryID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO datasource_credentials (id, datasource_id, purpose, credential_ciphertext, credential_nonce, credential_key_id, credential_algorithm, enabled, created_at, updated_at)
		VALUES (?, ?, 'query', ?, ?, ?, 'aes-256-gcm', true, now(), now())`,
		goodQryID, pgDSID, goodEnv.Ciphertext, goodEnv.Nonce, goodEnv.KeyID).Error; err != nil {
		t.Fatalf("insert good query cred: %v", err)
	}

	// Test openTLSMaterial directly with corrupted TLS material, cert-only TLS material, and cert+key TLS material
	tlsID := uuid.NewString()
	if err := db.Exec(`INSERT INTO datasource_tls_materials (id, datasource_id, purpose, verify_server, server_name, material_ciphertext, material_nonce, material_key_id, material_algorithm, created_at, updated_at)
		VALUES (?, ?, 'query', true, 'db.example.internal', '\xdeadbeef', '\x0102030405060708090a0b0c', 'pay-1', 'aes-256-gcm', now(), now())`,
		tlsID, pgDSID).Error; err != nil {
		t.Fatalf("insert bad tls material: %v", err)
	}
	if _, err := reader.ListSchemas(ctx, DatasourceTarget{DatasourceID: pgDSID}); err == nil {
		t.Fatal("expected error when TLS material decryption fails")
	}

	certPEM, keyPEM := generateTestCertAndKeyPEM(t)
	// Cert-only bundle
	certOnlyEnv, err := crypter.Seal(crypto.PurposePayload, certPEM, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_tls_materials",
		PrimaryKey:   tlsID,
		Column:       "material",
		Purpose:      "datasource_tls",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE datasource_tls_materials SET material_ciphertext = ?, material_nonce = ?, material_key_id = ? WHERE id = ?`,
		certOnlyEnv.Ciphertext, certOnlyEnv.Nonce, certOnlyEnv.KeyID, tlsID).Error; err != nil {
		t.Fatal(err)
	}
	mat1, err := reader.openTLSMaterial(ctx, pgDSID)
	if err != nil || mat1 == nil || len(mat1.CAPEM) == 0 || len(mat1.CertPEM) != 0 {
		t.Fatalf("unexpected cert-only TLS material: %+v, err=%v", mat1, err)
	}

	// Cert + CA + Key bundle
	var bundle []byte
	bundle = append(bundle, certPEM...)
	bundle = append(bundle, certPEM...)
	bundle = append(bundle, keyPEM...)
	fullEnv, err := crypter.Seal(crypto.PurposePayload, bundle, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_tls_materials",
		PrimaryKey:   tlsID,
		Column:       "material",
		Purpose:      "datasource_tls",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE datasource_tls_materials SET material_ciphertext = ?, material_nonce = ?, material_key_id = ? WHERE id = ?`,
		fullEnv.Ciphertext, fullEnv.Nonce, fullEnv.KeyID, tlsID).Error; err != nil {
		t.Fatal(err)
	}
	mat2, err := reader.openTLSMaterial(ctx, pgDSID)
	if err != nil || mat2 == nil || len(mat2.CAPEM) == 0 || len(mat2.CertPEM) == 0 || len(mat2.KeyPEM) == 0 {
		t.Fatalf("unexpected full TLS material: %+v, err=%v", mat2, err)
	}

	// Remove TLS material so plaintext PostgreSQL test DB connection succeeds
	_ = db.Exec(`DELETE FROM datasource_tls_materials WHERE id = ?`, tlsID).Error

	schemas, err := reader.ListSchemas(ctx, DatasourceTarget{DatasourceID: pgDSID})
	if err != nil || len(schemas) == 0 {
		t.Fatalf("ListSchemas failed: %v (schemas=%v)", err, schemas)
	}
	tables, err := reader.ListTables(ctx, DatasourceTarget{DatasourceID: pgDSID}, "public")
	if err != nil || len(tables) == 0 {
		t.Fatalf("ListTables failed: %v (tables=%v)", err, tables)
	}
	cols, err := reader.ListColumns(ctx, DatasourceTarget{DatasourceID: pgDSID}, "public", "users")
	if err != nil || len(cols) == 0 {
		t.Fatalf("ListColumns failed: %v (cols=%v)", err, cols)
	}
	idxs, err := reader.ListIndexes(ctx, DatasourceTarget{DatasourceID: pgDSID}, "public", "users")
	if err != nil || len(idxs) == 0 {
		t.Fatalf("ListIndexes failed: %v (idxs=%v)", err, idxs)
	}

	// Test MySQL compatibility_mode branch in openReadPool (unreachable port triggers adapter.OpenPurposePool error)
	mysqlDSID := uuid.NewString()
	if err := db.Exec(`INSERT INTO datasources (id, name, engine, compatibility_mode, deployment_kind, host, port, database_name, version_constraint, enabled, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'unreachable-mysql', 'mysql', 'mysql', 'native', '127.0.0.1', 1, 'core_db', '8.0', true, 1, ?, now(), now())`,
		mysqlDSID, userID).Error; err != nil {
		t.Fatalf("insert mysql datasource: %v", err)
	}
	mysqlCredID := uuid.NewString()
	mysqlEnv, err := crypter.Seal(crypto.PurposePayload, []byte(goodPlain), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   mysqlCredID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO datasource_credentials (id, datasource_id, purpose, credential_ciphertext, credential_nonce, credential_key_id, credential_algorithm, enabled, created_at, updated_at)
		VALUES (?, ?, 'review', ?, ?, ?, 'aes-256-gcm', true, now(), now())`,
		mysqlCredID, mysqlDSID, mysqlEnv.Ciphertext, mysqlEnv.Nonce, mysqlEnv.KeyID).Error; err != nil {
		t.Fatal(err)
	}
	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := reader.ListSchemas(shortCtx, DatasourceTarget{DatasourceID: mysqlDSID}); err == nil {
		t.Fatal("expected error connecting to unreachable mysql port")
	}

	// Authorizer branches: nil/empty inputs, query_grants active, and approval_enabled=false
	auth := &Authorizer{DB: db, Crypter: crypter, Catalog: reader}
	if _, err := auth.AuthorizeDatasource(ctx, "", pgDSID, "", false); err == nil {
		t.Fatal("expected error on empty userID")
	}
	if _, err := auth.AuthorizeDatasource(ctx, userID, uuid.NewString(), "", false); err == nil {
		t.Fatal("expected error on unknown datasource")
	}
	if _, err := auth.AuthorizeDatasource(ctx, userID, pgDSID, uuid.NewString(), true); err == nil {
		t.Fatal("expected error when requireChangeGrant=true and user has no grant")
	}
	if _, err := auth.AuthorizeDatasource(ctx, userID, pgDSID, "", false); err == nil {
		t.Fatal("expected error when user has neither change nor query grant")
	}

	// Grant active query_grants + query_grant_capabilities
	qFlowID := uuid.NewString()
	if err := db.Exec(`INSERT INTO flows (id, name, flow_type, status, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'qflow', 'query_access', 'enabled', 1, ?, now(), now())`, qFlowID, userID).Error; err != nil {
		t.Fatalf("insert query flow: %v", err)
	}
	reqID := uuid.NewString()
	if err := db.Exec(`INSERT INTO query_access_requests (id, requester_user_id_snapshot, requester_username_snapshot, requester_display_name_snapshot, flow_id_snapshot, flow_name_snapshot, flow_snapshot_ciphertext, flow_snapshot_nonce, flow_snapshot_key_id, flow_snapshot_algorithm, snapshot_hash, state, reason, requested_until, version, created_at, terminal_at)
		VALUES (?, ?, 'tooluser', 'tooluser', ?, 'qflow', '\x01', '\x02', 'pay-1', 'aes-256-gcm', repeat('a', 64), 'grant_active', 'test', now() + interval '1 day', 1, now(), now())`,
		reqID, userID, qFlowID).Error; err != nil {
		t.Fatalf("insert query_access_request: %v", err)
	}
	grantID := uuid.NewString()
	if err := db.Exec(`INSERT INTO query_grants (id, query_access_request_id, requester_user_id_snapshot, requester_username_snapshot, state, expires_at, version, created_at)
		VALUES (?, ?, ?, 'tooluser', 'active', now() + interval '1 hour', 1, now())`,
		grantID, reqID, userID).Error; err != nil {
		t.Fatalf("insert query_grant: %v", err)
	}
	if err := db.Exec(`INSERT INTO query_grant_capabilities (grant_id, datasource_id, datasource_name_snapshot, datasource_identity_fingerprint, can_query, can_export)
		VALUES (?, ?, 'live-pg-ds', repeat('c', 64), true, false)`,
		grantID, pgDSID).Error; err != nil {
		t.Fatalf("insert query_grant_capability: %v", err)
	}

	target, err := auth.AuthorizeDatasource(ctx, userID, pgDSID, "", false)
	if err != nil || !target.HasQueryGrant {
		t.Fatalf("expected query grant authorization to succeed, got target=%+v err=%v", target, err)
	}

	// Remove query_grant and test approval_enabled=false path in checkQueryMetadataGranted
	_ = db.Exec(`DELETE FROM query_grants WHERE id = ?`, grantID).Error
	if err := db.Exec(`INSERT INTO system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		VALUES ('query', 1, '{"approval_enabled": false}'::jsonb, 1, ?, now(), now())
		ON CONFLICT (namespace) DO UPDATE SET settings = EXCLUDED.settings`, userID).Error; err != nil {
		t.Fatalf("upsert system_settings: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM system_settings WHERE namespace = 'query'`).Error
	})

	// Without flow_query_datasource_capabilities table/view, checkQueryMetadataGranted returns error
	_, _ = auth.AuthorizeDatasource(ctx, userID, pgDSID, "", false)

	// Create temp table flow_query_datasource_capabilities to cover the success return on L285
	if err := db.Exec(`CREATE TEMP TABLE IF NOT EXISTS flow_query_datasource_capabilities (
		flow_id uuid,
		datasource_id uuid,
		can_query boolean
	)`).Error; err == nil {
		t.Cleanup(func() {
			_ = db.Exec(`DROP TABLE IF EXISTS pg_temp.flow_query_datasource_capabilities`).Error
		})
		pgGroupID := uuid.NewString()
		_ = db.Exec(`INSERT INTO permission_groups (id, name, enabled, version, created_by_user_id, created_at, updated_at)
			VALUES (?, 'qgroup', true, 1, ?, now(), now())`, pgGroupID, userID).Error
		_ = db.Exec(`INSERT INTO permission_group_members (permission_group_id, user_id) VALUES (?, ?)`, pgGroupID, userID).Error
		_ = db.Exec(`INSERT INTO permission_group_flow_grants (permission_group_id, flow_id) VALUES (?, ?)`, pgGroupID, qFlowID).Error
		_ = db.Exec(`INSERT INTO flow_query_datasource_capabilities (flow_id, datasource_id, can_query) VALUES (?, ?, true)`, qFlowID, pgDSID).Error

		target2, err := auth.AuthorizeDatasource(ctx, userID, pgDSID, "", false)
		if err != nil || !target2.HasQueryGrant {
			t.Fatalf("expected query flow without approval to authorize, got target=%+v err=%v", target2, err)
		}

		// Also test change_review flow grant (both explicit flowID and auto-resolved flowID)
		ruleSetID := uuid.NewString()
		_ = db.Exec(`INSERT INTO rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
			VALUES (?, 'rset', true, '{}'::jsonb, repeat('a', 64), 1, ?, now(), now())`, ruleSetID, userID).Error
		changeFlowID := uuid.NewString()
		_ = db.Exec(`INSERT INTO flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
			VALUES (?, 'cflow', 'change_review', 'enabled', ?, 1, ?, now(), now())`, changeFlowID, ruleSetID, userID).Error
		_ = db.Exec(`INSERT INTO flow_stages (id, flow_id, position, datasource_id, schema_mapping_ciphertext, schema_mapping_nonce, schema_mapping_key_id, schema_mapping_algorithm)
			VALUES (?, ?, 1, ?, '\x01', '\x02', 'pay-1', 'aes-256-gcm')`, uuid.NewString(), changeFlowID, pgDSID).Error
		_ = db.Exec(`INSERT INTO permission_group_flow_grants (permission_group_id, flow_id) VALUES (?, ?)`, pgGroupID, changeFlowID).Error

		if t3, err := auth.AuthorizeDatasource(ctx, userID, pgDSID, changeFlowID, true); err != nil || !t3.HasChangeGrant {
			t.Fatalf("expected explicit changeFlowID grant to succeed, got %+v err=%v", t3, err)
		}
		if t4, err := auth.AuthorizeDatasource(ctx, userID, pgDSID, "", true); err != nil || !t4.HasChangeGrant {
			t.Fatalf("expected auto-resolved changeFlowID grant to succeed, got %+v err=%v", t4, err)
		}
	}
}
