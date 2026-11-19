//go:build integration

package application

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// TestAdapterFactSourceFactoryRealPostgreSQLWiring exercises the production
// composition boundary with a real target PostgreSQL instance:
// sealed review credential -> live identity probe -> frozen target -> AdapterFactSourceFor.
// It deliberately does not use Services.DatasourceIdentityProbe or fake facts.
func TestAdapterFactSourceFactoryRealPostgreSQLWiring(t *testing.T) {
	password := os.Getenv("YEARNING_TEST_ADAPTER_PG_PASSWORD")
	if password == "" {
		t.Skip("target PostgreSQL not configured")
	}
	port, err := strconv.Atoi(itestEnv("YEARNING_TEST_ADAPTER_PG_PORT", "54333"))
	if err != nil {
		t.Fatal(err)
	}
	host := itestEnv("YEARNING_TEST_ADAPTER_PG_HOST", "127.0.0.1")
	user := itestEnv("YEARNING_TEST_ADAPTER_PG_USER", "yearning")
	databaseName := itestEnv("YEARNING_TEST_ADAPTER_PG_DB", "yearning")

	t.Setenv("YEARNING_ALLOW_LOOPBACK_TARGETS", "1")
	admin, err := sql.Open("pgx", fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, databaseName,
	))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	ctx := context.Background()
	schema := fmt.Sprintf("ir03_factory_%d", time.Now().UnixNano())
	table := "review_target"
	if _, err := admin.ExecContext(ctx, `create schema "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `drop schema if exists "`+schema+`" cascade`)
	})
	if _, err := admin.ExecContext(ctx,
		`create table "`+schema+`"."`+table+`" (id bigint primary key, label text not null)`); err != nil {
		t.Fatal(err)
	}

	f := newFixture(t)
	crypter, recorder := newFactoryTestCrypterAndRecorder(t, f)
	svc, err := NewServices(f.db, crypter, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	// This test must exercise the production probe, never the deterministic
	// fixture seam installed by newFixture.
	svc.DatasourceIdentityProbe = nil

	dsID := insertFactoryDatasource(t, f, host, port, databaseName)
	credential := sealFactoryReviewCredential(t, crypter, dsID, user, password, true)
	insertFactoryCredential(t, f, dsID, credential)

	row, err := loadDatasourceTargetRow(ctx, f.db, dsID)
	if err != nil {
		t.Fatal(err)
	}
	probeAdapter, _, _, identity, err := probeDatasourceConnection(ctx, f.db, crypter, row)
	if err != nil {
		t.Fatal(err)
	}
	if identity == nil {
		t.Fatal("production identity probe returned nil")
	}
	_ = probeAdapter.Close()
	fingerprint, err := configuredIdentityFingerprint(crypter, row, identity.InstanceMarker)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`update datasources set identity_fingerprint = ? where id = ?`,
		fingerprint, dsID).Error; err != nil {
		t.Fatal(err)
	}
	row, err = loadDatasourceTargetRow(ctx, f.db, dsID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := svc.probeFrozenDatasourceTarget(ctx, row)
	if err != nil {
		t.Fatalf("probe frozen target: %v", err)
	}

	factory := AdapterFactSourceFor(f.db, crypter)
	source, closeSource, err := factory(ctx, dsID, target, uuid.NewString())
	if err != nil {
		t.Fatalf("AdapterFactSourceFor: %v", err)
	}
	if closeSource == nil {
		t.Fatal("AdapterFactSourceFor returned nil close function")
	}
	identityFact, err := source.DatasourceIdentity(ctx)
	if err != nil {
		closeSource()
		t.Fatalf("read datasource identity: %v", err)
	}
	if identityFact["engine"] != "postgresql" ||
		identityFact["instance_identity_status"] != "matched" ||
		identityFact["database_identity_status"] != "matched" ||
		identityFact["metadata_complete"] != true {
		closeSource()
		t.Fatalf("identity fact = %#v", identityFact)
	}
	scoped, ok := source.(tool.TableMetadataSource)
	if !ok {
		closeSource()
		t.Fatal("AdapterFactSourceFor result does not implement table metadata")
	}
	metadata, err := scoped.TableMetadata(ctx, schema, table)
	closeSource()
	if err != nil {
		t.Fatalf("read target table metadata: %v", err)
	}
	if metadata["schema"] != schema || metadata["table"] != table ||
		metadata["existence"] != "present" || metadata["metadata_complete"] != true ||
		metadata["truncated"] != false {
		t.Fatalf("target table metadata = %#v", metadata)
	}
	columns, _ := metadata["columns"].([]map[string]any)
	if len(columns) != 2 || columns[0]["column_name"] != "id" ||
		columns[0]["type_class"] != "integer" || columns[1]["column_name"] != "label" ||
		columns[1]["type_class"] != "string" {
		t.Fatalf("target columns = %#v", metadata["columns"])
	}

	// Existing encrypted credentials cannot be retargeted by changing the
	// configured database name after the stage target was frozen.
	if err := f.db.Exec(`update datasources set database_name = ? where id = ?`,
		databaseName+"_drift", dsID).Error; err != nil {
		t.Fatal(err)
	}
	if source, closeSource, err := factory(ctx, dsID, target, uuid.NewString()); !errors.Is(err, ErrFactSourceIdentityChanged) || source != nil || closeSource != nil {
		t.Fatalf("database-name drift = source:%v close:%t err:%v",
			source, closeSource != nil, err)
	}
	if err := f.db.Exec(`update datasources set database_name = ? where id = ?`,
		databaseName, dsID).Error; err != nil {
		t.Fatal(err)
	}

	// The live server identity must match the encrypted/frozen target identity.
	driftedIdentity := target
	driftedIdentity.InstanceMarker += "-drift"
	if source, closeSource, err := factory(ctx, dsID, driftedIdentity, uuid.NewString()); !errors.Is(err, ErrFactSourceIdentityChanged) || source != nil || closeSource != nil {
		t.Fatalf("instance-identity drift = source:%v close:%t err:%v",
			source, closeSource != nil, err)
	}

	// A credential sealed for another row's AAD must never be accepted.
	badAADDatasourceID := insertFactoryDatasource(t, f, host, port, databaseName)
	badCredential := sealFactoryReviewCredential(
		t, crypter, badAADDatasourceID, user, password, false)
	insertFactoryCredential(t, f, badAADDatasourceID, badCredential)
	badRow, err := loadDatasourceTargetRow(ctx, f.db, badAADDatasourceID)
	if err != nil {
		t.Fatal(err)
	}
	badFingerprint, err := configuredIdentityFingerprint(crypter, badRow, identity.InstanceMarker)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`update datasources set identity_fingerprint = ? where id = ?`,
		badFingerprint, badAADDatasourceID).Error; err != nil {
		t.Fatal(err)
	}
	badTarget := target
	badTarget.DatasourceID = badAADDatasourceID
	badTarget.ConfiguredIdentityFP = badFingerprint
	if source, closeSource, err := factory(ctx, badAADDatasourceID, badTarget, uuid.NewString()); err == nil || source != nil || closeSource != nil {
		t.Fatalf("wrong credential AAD = source:%v close:%t err:%v",
			source, closeSource != nil, err)
	}
}

type factoryCredential struct {
	id         string
	ciphertext []byte
	nonce      []byte
	keyID      string
	algorithm  string
}

func newFactoryTestCrypterAndRecorder(t *testing.T, f *fx) (*crypto.Crypter, *auditlog.Recorder) {
	t.Helper()
	keyringDoc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"ir03-factory-itest","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"ident-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		base64.StdEncoding.EncodeToString(make32(11)),
		base64.StdEncoding.EncodeToString(make32(12)),
		base64.StdEncoding.EncodeToString(make32(13)))
	path := t.TempDir() + "/keyring.json"
	if err := os.WriteFile(path, []byte(keyringDoc), 0o400); err != nil {
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
	recorder, err := auditlog.NewRecorder(f.db)
	if err != nil {
		t.Fatal(err)
	}
	return crypter, recorder
}

func insertFactoryDatasource(t *testing.T, f *fx, host string, port int, databaseName string) string {
	t.Helper()
	id := uuid.NewString()
	if err := f.db.Exec(`insert into datasources
		(id, name, engine, compatibility_mode, deployment_kind, host, port, database_name,
		 version_constraint, enabled, identity_fingerprint, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'postgresql', 'postgresql', 'native', ?, ?, ?, '16.x', true, ?, 1, ?, now(), now())`,
		id, "ir03-factory-"+id[:8], host, port, databaseName,
		make32Hex(0), f.userID).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func make32Hex(seed byte) string {
	raw := make32(seed)
	return fmt.Sprintf("%x", raw)
}

func sealFactoryReviewCredential(
	t *testing.T, crypter *crypto.Crypter, datasourceID, username, password string, matchingAAD bool,
) factoryCredential {
	t.Helper()
	plain, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{Username: username, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	credentialID := uuid.NewString()
	aadID := credentialID
	if !matchingAAD {
		aadID = uuid.NewString()
	}
	env, err := crypter.Seal(crypto.PurposePayload, plain, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   aadID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	return factoryCredential{
		id:         credentialID,
		ciphertext: env.Ciphertext,
		nonce:      env.Nonce,
		keyID:      env.KeyID,
		algorithm:  env.Algorithm,
	}
}

func insertFactoryCredential(
	t *testing.T, f *fx, datasourceID string, credential factoryCredential,
) {
	t.Helper()
	if err := f.db.Exec(`insert into datasource_credentials
		(id, datasource_id, purpose, credential_ciphertext, credential_nonce,
		 credential_key_id, credential_algorithm, enabled, created_at, updated_at)
		values (?, ?, 'review', ?, ?, ?, ?, true, now(), now())`,
		credential.id, datasourceID, credential.ciphertext, credential.nonce,
		credential.keyID, credential.algorithm).Error; err != nil {
		t.Fatal(err)
	}
}
