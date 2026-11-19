package legacy

import (
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

func TestJCSKnownVector(t *testing.T) {
	// Integer/string subset of the RFC 8785 appendix examples.
	got, err := JCSSerialize(map[string]any{"b": int64(2), "a": "x", "c": []any{int64(1), nil, true}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"x","b":2,"c":[1,null,true]}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestJCSHashStableAcrossKeyOrder(t *testing.T) {
	a, _ := JCSHash(map[string]any{"z": int64(1), "y": "s"})
	b, _ := JCSHash(map[string]any{"y": "s", "z": int64(1)})
	if a != b {
		t.Fatal("hash must be key-order independent")
	}
}

func TestJCSRejectsFloats(t *testing.T) {
	if _, err := JCSSerialize(map[string]any{"x": 1.5}); err == nil {
		t.Fatal("float must be rejected")
	}
}

func TestCrypterSealOpenAndHMAC(t *testing.T) {
	c, err := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	aad := c.AAD("order_sql_payloads", "o-1", "sql_ciphertext", crypto.PurposePayload)
	sealed, err := c.Seal([]byte("secret-sql"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Algorithm != "aes-256-gcm" || len(sealed.Nonce) != 12 {
		t.Fatalf("envelope shape wrong: %+v", sealed)
	}
	opened, err := c.Open(sealed, aad)
	if err != nil || string(opened) != "secret-sql" {
		t.Fatalf("roundtrip failed: %v %q", err, opened)
	}
	if h1, h2 := c.DomainHMAC("sql", []byte("s")), c.DomainHMAC("sql", []byte("s")); h1 != h2 || len(h1) != 64 {
		t.Fatal("domain hmac must be deterministic 64-hex")
	}
}

func TestLegacyAESRoundtrip(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes, for testing only
	c, err := NewCrypter([]byte(strings.Repeat("v", 32)), "key-1", "deploy-test", key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := LegacyEncrypt(key, []byte("ds-password-1"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := c.LegacyDecrypt(ct)
	if err != nil || string(pt) != "ds-password-1" {
		t.Fatalf("legacy roundtrip failed: %v %q", err, pt)
	}
	// Must return ErrLegacyKeyMissing when the legacy secret key is not configured.
	c2, _ := NewCrypter([]byte(strings.Repeat("v", 32)), "key-1", "deploy-test", nil)
	if _, err := c2.LegacyDecrypt(ct); err != ErrLegacyKeyMissing {
		t.Fatalf("want ErrLegacyKeyMissing, got %v", err)
	}
}

func TestConfigStrictValidation(t *testing.T) {
	dir := t.TempDir()
	writeCfg := func(content string) string {
		p := filepath.Join(dir, "config.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := `{
	  "source": {"host": "mysql.internal", "port": 3306, "database": "yearning", "username": "y", "password": {"environment": "SRC_PW"}, "tls": {"mode": "verify_full", "server_name": "mysql.internal"}},
	  "target": {"host": "pg.internal", "port": 5432, "database": "yearning_v4", "username": "y", "password": {"environment": "TGT_PW"}, "tls": {"mode": "verify_full", "server_name": "pg.internal"}},
	  "encryption": {"key_id": "k1", "key": {"environment": "KEY"}, "deployment_id": "deploy-main"},
	  "source_timezone": "Asia/Shanghai",
	  "batch_rows": 500
	}`
	cfg, err := LoadConfig(writeCfg(good))
	if err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
	if cfg.BatchRows != 500 || cfg.Source.TLS.Mode != "verify_full" {
		t.Fatalf("parsed wrong: %+v", cfg)
	}
	// After RCP-20260825-B2-LEGACY-SECRET-KEY approval: source.legacy_secret_key is valid.
	withLegacy := strings.Replace(good, `"tls": {"mode": "verify_full", "server_name": "mysql.internal"}}`,
		`"tls": {"mode": "verify_full", "server_name": "mysql.internal"}, "legacy_secret_key": {"environment": "LSK"}}`, 1)
	cfgLegacy, err := LoadConfig(writeCfg(withLegacy))
	if err != nil {
		t.Fatalf("legacy_secret_key must be accepted after RCP approval: %v", err)
	}
	if cfgLegacy.Source.LegacySecretKey == nil || cfgLegacy.Source.LegacySecretKey.Environment != "LSK" {
		t.Fatalf("legacy secret key ref not parsed: %+v", cfgLegacy.Source.LegacySecretKey)
	}
	// The target section does not permit legacy_secret_key.
	badTarget := strings.Replace(good, `"tls": {"mode": "verify_full", "server_name": "pg.internal"}}`,
		`"tls": {"mode": "verify_full", "server_name": "pg.internal"}, "legacy_secret_key": {"environment": "LSK"}}`, 1)
	if _, err := LoadConfig(writeCfg(badTarget)); err == nil {
		t.Fatal("legacy_secret_key must stay source-only")
	}
	// Missing required fields, invalid time zone, or invalid batch size.
	bad := strings.Replace(good, `"batch_rows": 500`, `"batch_rows": 99999`, 1)
	if _, err := LoadConfig(writeCfg(bad)); err == nil {
		t.Fatal("out-of-range batch_rows must be rejected")
	}
	badTZ := strings.Replace(good, "Asia/Shanghai", "Not/AZone", 1)
	if _, err := LoadConfig(writeCfg(badTZ)); err == nil {
		t.Fatal("bad timezone must be rejected")
	}
	// RCP-20260831-LEGACY-MIGRATION-AAD: encryption.deployment_id is required (matching the target keyring).
	if cfg.Encryption.DeploymentID != "deploy-main" {
		t.Fatalf("deployment_id not parsed: %+v", cfg.Encryption)
	}
	noDeploy := strings.Replace(good, `, "deployment_id": "deploy-main"`, "", 1)
	if noDeploy == good {
		t.Fatal("fixture replace failed")
	}
	if _, err := LoadConfig(writeCfg(noDeploy)); err == nil {
		t.Fatal("missing encryption.deployment_id must be rejected")
	}
	emptyDeploy := strings.Replace(good, `"deployment_id": "deploy-main"`, `"deployment_id": ""`, 1)
	if _, err := LoadConfig(writeCfg(emptyDeploy)); err == nil {
		t.Fatal("empty encryption.deployment_id must be rejected")
	}
	longDeploy := strings.Replace(good, `"deployment_id": "deploy-main"`, `"deployment_id": "`+strings.Repeat("d", 129)+`"`, 1)
	if _, err := LoadConfig(writeCfg(longDeploy)); err == nil {
		t.Fatal("over-length encryption.deployment_id must be rejected")
	}
}

func TestStatusClassification(t *testing.T) {
	ev := &StatusEvidence{}
	// Construct the mapping directly to override the loaded file and test the classification logic itself.
	ev.CoreSQLOrders = []StatusMapping{
		{LegacyValue: 0, Activity: "terminal", TargetValue: "rejected", Confidence: "verified"},
		{LegacyValue: 2, Activity: "active", TargetValue: "BLOCK", Confidence: "verified"},
		{LegacyValue: 1, Activity: "terminal", TargetValue: "completed", Confidence: "ambiguous"},
	}
	ev.CoreQueryOrders = []StatusMapping{
		{LegacyValue: 1, Activity: "active", TargetValue: "BLOCK", Confidence: "verified"},
		{LegacyValue: 3, Activity: "terminal", TargetValue: "closed", Confidence: "verified"},
	}
	if o := ev.ClassifySQLOrder(0); o.Kind != StatusMapped || o.State != "rejected" {
		t.Fatalf("0 → %+v", o)
	}
	if o := ev.ClassifySQLOrder(2); o.Kind != StatusActive {
		t.Fatalf("2 must be active: %+v", o)
	}
	if o := ev.ClassifySQLOrder(1); o.Kind != StatusAmbiguous {
		t.Fatalf("1 must be ambiguous: %+v", o)
	}
	if o := ev.ClassifySQLOrder(99); o.Kind != StatusUnknown {
		t.Fatalf("99 must be unknown: %+v", o)
	}
	if o := ev.ClassifyQueryOrder(3); o.Kind != StatusMapped {
		t.Fatalf("query 3 → %+v", o)
	}
}

func TestDjangoHashAllowlist(t *testing.T) {
	hash, err := MakeDjangoHash("correct horse battery", 600000)
	if err != nil {
		t.Fatal(err)
	}
	if !djangoHashAllowlisted(hash) {
		t.Fatal("generated hash must satisfy allowlist")
	}
	if !VerifyDjangoHash("correct horse battery", hash) {
		t.Fatal("verify must succeed")
	}
	if VerifyDjangoHash("wrong", hash) {
		t.Fatal("verify must fail on wrong password")
	}
	weak := "pbkdf2_sha256$10000$c2FsdA==$68656c6c6f"
	if djangoHashAllowlisted(weak) {
		t.Fatal("weak iterations must be rejected → require reset")
	}
	// SEC-0043 regression: verification path also enforces the minimum iteration floor (weak-parameter hashes are rejected before comparison).
	if VerifyDjangoHash("correct horse battery", weak) {
		t.Fatal("VerifyDjangoHash must reject below-floor iteration counts")
	}
}

func TestTransformCoreAccountBasics(t *testing.T) {
	c, _ := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	tc := NewTransformContext(mustUUID("11111111-1111-1111-1111-111111111111"), c, time.UTC)
	ev := &StatusEvidence{}
	weakHash := "pbkdf2_sha256$10000$c2FsdA==$68656c6c6f6c6f"
	m := tc.TransformCoreAccount(Row{"id": int64(1), "username": " Alice ", "password": weakHash, "real_name": "", "email": "a@b.c"}, ev)
	if m == nil {
		t.Fatal("row must map")
	}
	if m.Username != "Alice" || m.PasswordHash != nil {
		t.Fatalf("weak hash must force reset: %+v", m)
	}
	if m.DisplayName != "Alice" {
		t.Fatalf("display name fallback: %+v", m)
	}
	// Duplicate usernames (under case folding) must be quarantined.
	m2 := tc.TransformCoreAccount(Row{"id": int64(2), "username": "alice", "password": "", "real_name": "A", "email": ""}, ev)
	if m2 != nil {
		t.Fatal("duplicate username must be quarantined")
	}
}

func TestSnapshotSourceOrderingAndRows(t *testing.T) {
	dir := t.TempDir()
	schema := map[string][]ColumnSpec{"core_accounts": {{Name: "id"}, {Name: "username"}}}
	raw, _ := json.Marshal(schema)
	if err := os.WriteFile(filepath.Join(dir, "_schema.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rows := "{\"id\":2,\"username\":\"b\"}\n{\"id\":1,\"username\":\"a\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "core_accounts.jsonl"), []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := OpenSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = src.StreamRows(nil, "core_accounts", 10, func(batch []Row) error { return nil })
	if err == nil {
		t.Fatal("descending ids must be rejected")
	}
	os.WriteFile(filepath.Join(dir, "core_accounts.jsonl"), []byte("{\"id\":1,\"username\":\"a\"}\n{\"id\":2,\"username\":\"b\"}\n"), 0o600)
	count, _ := src.CountRows(nil, "core_accounts")
	if count != 2 {
		t.Fatalf("count = %d", count)
	}
	n, _ := src.CountRows(nil, "core_total_tickets")
	if n != 0 {
		t.Fatal("missing file means zero rows")
	}
}

func mustUUID(s string) uuid.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// TestTransformRollbackReadonlyArchive E003/M002: legacy rollback SQL is sealed into
// order_flow_snapshots ciphertext via FinalizeRollbackBundles (ordered rollback_bundle archive) without writing to any other v4 runtime table.
func TestTransformRollbackReadonlyArchive(t *testing.T) {
	c, _ := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	tc := NewTransformContext(mustUUID("22222222-2222-2222-2222-222222222222"), c, time.UTC)
	orderID := mustUUID("33333333-3333-3333-3333-333333333333")
	tc.Orders["work-1"] = orderID

	// Simulate the snapshot write already persisted by TransformSQLOrder (prior to attaching rollback_bundle).
	snapshot := map[string]any{"data_base": "db"}
	sealed, err := tc.sealJSON("order-snapshot", tc.aad("order_flow_snapshots", orderID.String(), "flow_snapshot_ciphertext", crypto.PurposePayload), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tc.OrderSnapshots["work-1"] = snapshot
	tc.addWrite("order_flow_snapshots", OrderFlowSnapshotModel{
		OrderID: orderID.String(), FlowSnapshotCiphertext: sealed.Ciphertext,
		FlowSnapshotNonce: sealed.Nonce, FlowSnapshotKeyID: sealed.KeyID,
		FlowSnapshotAlgorithm: sealed.Algorithm,
		SnapshotHash:          tc.hash64("order-snapshot", []byte(mustJSON(snapshot))),
	})
	// change_orders.snapshot_hash and the snapshot table hash both reference the updated snapshot (recomputed in lockstep per CL-1).
	tc.addWrite("change_orders", ChangeOrderModel{ID: orderID.String(),
		SnapshotHash: tc.hash64("order-snapshot", []byte(mustJSON(snapshot)))})
	snapWrites := len(tc.writes)
	ciphertextBefore := string(sealed.Ciphertext)

	// Multi-row rollbacks are archived in arrival order (ascending source row ID).
	if !tc.TransformRollback(Row{"id": int64(7), "work_id": "work-1", "sql": "update t set a = 1 where id = 2"}) {
		t.Fatal("mapped rollback row must be archived")
	}
	if !tc.TransformRollback(Row{"id": int64(9), "work_id": "work-1", "sql": "delete from t where id = 3"}) {
		t.Fatal("second rollback row must be archived")
	}
	if len(tc.writes) != snapWrites {
		t.Fatalf("rollback collection must not add writes before finalize: %+v", tc.writes[snapWrites:])
	}

	if err := tc.FinalizeRollbackBundles(); err != nil {
		t.Fatal(err)
	}
	// The persisted write updates the existing order_flow_snapshots entry (resealed in place rather than inserting a new row) and touches no other table.
	if len(tc.writes) != snapWrites {
		t.Fatalf("finalize must replace in place, got writes: %+v", tc.writes)
	}
	var snap OrderFlowSnapshotModel
	var order ChangeOrderModel
	for _, w := range tc.writes {
		switch m := w.Model.(type) {
		case OrderFlowSnapshotModel:
			if m.OrderID != orderID.String() {
				t.Fatalf("unexpected snapshot write: %+v", w)
			}
			snap = m
		case ChangeOrderModel:
			if m.ID != orderID.String() {
				t.Fatalf("unexpected order write: %+v", w)
			}
			order = m
		default:
			t.Fatalf("rollback archive must not write other tables: %s", w.Table)
		}
	}
	if string(snap.FlowSnapshotCiphertext) == ciphertextBefore {
		t.Fatal("snapshot ciphertext must be resealed with rollback bundle")
	}
	oldHash := tc.hash64("order-snapshot", []byte(mustJSON(map[string]any{"data_base": "db"})))
	if snap.SnapshotHash == oldHash {
		t.Fatal("snapshot hash must cover the bundle")
	}
	if order.SnapshotHash != snap.SnapshotHash || order.SnapshotHash == oldHash {
		t.Fatalf("change_orders.snapshot_hash must sync with resealed snapshot (CL-1): %s vs %s", order.SnapshotHash, snap.SnapshotHash)
	}
	// In the plaintext snapshot map, rollback_bundle is an ordered slice (exposed for test/audit inspection; persisted as ciphertext).
	bundle, ok := snapshot["rollback_bundle"].([]string)
	if !ok || len(bundle) != 2 || bundle[0] != "update t set a = 1 where id = 2" || bundle[1] != "delete from t where id = 3" {
		t.Fatalf("rollback bundle must be ordered list: %v", snapshot["rollback_bundle"])
	}
	if len(tc.Quarantine) != 0 {
		t.Fatalf("mapped rollback must not be quarantined: %+v", tc.Quarantine)
	}

	// Unmapped parent ticket: quarantine the row rather than fabricating a target.
	if tc.TransformRollback(Row{"id": int64(8), "work_id": "missing", "sql": "drop table t"}) {
		t.Fatal("unmapped rollback row must be quarantined")
	}
	if len(tc.Quarantine) != 1 || tc.Quarantine[0].Table != "core_rollbacks" {
		t.Fatalf("expected core_rollbacks quarantine, got %+v", tc.Quarantine)
	}
	if err := tc.FinalizeRollbackBundles(); err != nil {
		t.Fatalf("finalize with only mapped pendings must succeed: %v", err)
	}
	if len(tc.writes) != snapWrites {
		t.Fatal("quarantined rollback must not create writes either")
	}
}

// TestTransformAutoTaskArchiveOnly M002: AutoTask produces only an archive report entry
// (covering the full contracted field set: task_id/name_hash/source/source_id/data_base/table/tp/
// affectrow/status/idc, marked non_executable with a hashed name) and creates no v4 runtime resources.
func TestTransformAutoTaskArchiveOnly(t *testing.T) {
	c, _ := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	tc := NewTransformContext(mustUUID("44444444-4444-4444-4444-444444444444"), c, time.UTC)
	writesBefore := len(tc.writes)

	if !tc.TransformAutoTask(Row{
		"task_id": "task-9", "name": "nightly cleanup", "status": "1",
		"affectrow": int64(5000), "tp": int64(1), "source": "src", "source_id": "1",
		"data_base": "db", "table": "t", "idc": "idc-1",
	}) {
		t.Fatal("autotask row must be archived")
	}
	archive, ok := tc.Report["autotask_archive"].([]map[string]any)
	if !ok || len(archive) != 1 {
		t.Fatalf("expected one autotask archive entry: %+v", tc.Report["autotask_archive"])
	}
	entry := archive[0]
	if entry["task_id"] != "task-9" || entry["non_executable"] != true {
		t.Fatalf("archive entry must be non-executable and keep identifiers: %+v", entry)
	}
	if _, hasName := entry["name"]; hasName {
		t.Fatal("archive must not keep raw task name")
	}
	if entry["name_hash"] == "" || entry["name_hash"] == "nightly cleanup" {
		t.Fatalf("archive name must be hashed: %+v", entry)
	}
	for key, want := range map[string]any{
		"source": "src", "source_id": "1", "data_base": "db", "table": "t",
		"tp": int64(1), "affectrow": int64(5000), "status": "1", "idc": "idc-1",
	} {
		if entry[key] != want {
			t.Fatalf("archive metadata %s = %v, want %v (contract field subset forbidden)", key, entry[key], want)
		}
	}
	if len(tc.writes) != writesBefore {
		t.Fatalf("autotask archive must not create runtime table writes: %+v", tc.writes[writesBefore:])
	}
}

// TestTransformDataSourceTLSPEMBundle BE-B13 alignment test: TLS material plaintext must be
// a concatenated PEM bundle (CA chain -> client cert -> private key) matching the runtime consumer's
// splitTLS* parser format; previously wrote a JSON bundle that pem.Decode could not parse, silently dropping custom CAs.
func TestTransformDataSourceTLSPEMBundle(t *testing.T) {
	ca := "-----BEGIN CERTIFICATE-----\nYWJjZGVmZ2hpamtsbW5vcA==\n-----END CERTIFICATE-----\n"
	cert := "-----BEGIN CERTIFICATE-----\ncXdlcnR5dWlvcGFzZA==\n-----END CERTIFICATE-----\n"
	key := "-----BEGIN PRIVATE KEY-----\nem94Y3Zibm0xMjM0NTY=\n-----END PRIVATE KEY-----\n"
	c, _ := NewCrypter([]byte(strings.Repeat("k", 32)), "key-1", "deploy-test", nil)
	tc := NewTransformContext(mustUUID("11111111-1111-1111-1111-111111111111"), c, time.UTC)
	ev := &StatusEvidence{}
	err := tc.TransformDataSource(Row{
		"id": int64(1), "source_id": "src-1", "source": "ds-tls",
		"db_type": int64(0), "ip": "db.internal", "port": int64(3306),
		"is_query": int64(0), "user": "u1", "password": "plainpw",
		"ca_file": ca, "cert": cert, "key_file": key,
	}, ev)
	if err != nil {
		t.Fatal(err)
	}
	var tlsModel *DatasourceTLSMaterialModel
	for i := range tc.writes {
		if tc.writes[i].Table != "datasource_tls_materials" {
			continue
		}
		m, ok := tc.writes[i].Model.(DatasourceTLSMaterialModel)
		if !ok {
			t.Fatalf("unexpected model type %T", tc.writes[i].Model)
		}
		tlsModel = &m
	}
	if tlsModel == nil {
		for _, q := range tc.Quarantine {
			t.Logf("quarantine: %s/%s: %s", q.Table, q.Key, q.Reason)
		}
		t.Fatal("tls material row missing")
	}
	if !tlsModel.VerifyServer {
		t.Fatal("verify_server must stay true")
	}
	plain, err := c.Open(Sealed{
		Ciphertext: tlsModel.MaterialCiphertext, Nonce: tlsModel.MaterialNonce,
		KeyID: tlsModel.MaterialKeyID, Algorithm: tlsModel.MaterialAlgorithm,
	}, c.AAD("datasource_tls_materials", tlsModel.ID, "material", "datasource_tls"))
	if err != nil {
		t.Fatalf("decrypt tls bundle: %v", err)
	}
	if strings.HasPrefix(string(plain), "{") {
		t.Fatalf("bundle must be concatenated PEM, got JSON: %.40s", plain)
	}
	certs, keys := 0, 0
	rest := plain
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			certs++
		case "PRIVATE KEY":
			keys++
		}
	}
	if certs != 2 || keys != 1 {
		t.Fatalf("bundle blocks = %d certs/%d keys", certs, keys)
	}
}

func TestRowInt64BoundsAndSpecialValues(t *testing.T) {
	if got, ok := rowInt64(uint64(1<<63 - 1)); !ok || got != 1<<63-1 {
		t.Fatalf("expected MaxInt64 uint64 to succeed, got (%d, %v)", got, ok)
	}
	if _, ok := rowInt64(^uint64(0)); ok {
		t.Fatal("expected MaxUint64 overflow to return ok=false")
	}
	if _, ok := rowInt64(uint64(1) << 63); ok {
		t.Fatal("expected 2^63 uint64 overflow to return ok=false")
	}
	if got, ok := rowInt64(float64(-0x1p63)); !ok || got != -1<<63 {
		t.Fatalf("expected MinInt64 float64 to succeed, got (%d, %v)", got, ok)
	}
	if _, ok := rowInt64(float64(0x1p63)); ok {
		t.Fatal("expected +2^63 float64 overflow to return ok=false")
	}
	if _, ok := rowInt64(1.5); ok {
		t.Fatal("expected fractional float64 to return ok=false")
	}
	nan := 0.0
	nan /= nan
	if _, ok := rowInt64(nan); ok {
		t.Fatal("expected NaN float64 to return ok=false")
	}
	posInf := 1.0
	zero := 0.0
	posInf /= zero
	if _, ok := rowInt64(posInf); ok {
		t.Fatal("expected +Inf float64 to return ok=false")
	}
}
