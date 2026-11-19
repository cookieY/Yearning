//go:build integration

// b13_integration_test.go BE-B13-CONTRACT-ALIGNMENT acceptance:
// credential keep/replace/reuse three modes, TLS material declaration surface (write-only SecretInput +
// tls_verified state + material encryption shape), and settings HTTP-layer alignment (route mounting fix,
// real effects calculation, Page envelope, schema fields, token binding, and 1011).
package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// putWithIfMatch sends PUT with If-Match and CSRF headers.
func (cl *testClient) putWithIfMatch(path string, body any, ifMatch string) (int, map[string]any, string) {
	cl.t.Helper()
	rawBody := marshalJSON(cl.t, body)
	req := httptest.NewRequest("PUT", path, strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", `"`+ifMatch+`"`)
	}
	if token := cl.cookies["yearning_csrf"]; token != "" {
		req.Header.Set("X-CSRF-Token", token)
	}
	return cl.dispatch(req)
}

// dispatch sends request and collects cookies (reusing testClient session state).
func (cl *testClient) dispatch(req *http.Request) (int, map[string]any, string) {
	cl.t.Helper()
	req.Header.Set("Content-Type", "application/json")
	if len(cl.cookies) > 0 {
		parts := make([]string, 0, len(cl.cookies))
		for k, v := range cl.cookies {
			parts = append(parts, k+"="+v)
		}
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}
	rec := httptest.NewRecorder()
	cl.engine.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Value == "" || c.MaxAge < 0 {
			delete(cl.cookies, c.Name)
		} else {
			cl.cookies[c.Name] = c.Value
		}
	}
	bodyText := rec.Body.String()
	var parsed map[string]any
	_ = json.Unmarshal([]byte(bodyText), &parsed)
	return rec.Code, parsed, bodyText
}

func marshalJSON(t *testing.T, body any) string {
	t.Helper()
	if body == nil {
		return ""
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// genTestPEM generates a real self-signed certificate and private key PEM (parseable by pem/x509).
func genTestPEM(t *testing.T) (caPEM, certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(20260831),
		Subject:      pkix.Name{CommonName: "b13-itest"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	// CA reuses the same certificate (test verifies block shape and classification, not chain building).
	return certPEM, certPEM, keyPEM
}

// openCredentialRow white-box helper: decrypts credential row using runtime AAD.
func openCredentialRow(t *testing.T, fx *b3Fixture, rowID string) (username, password string) {
	t.Helper()
	var ct, nonce, keyID, alg string
	if err := fx.db.Raw(`select credential_ciphertext, credential_nonce, credential_key_id, credential_algorithm
		from datasource_credentials where id = ?`, rowID).Row().Scan(&ct, &nonce, &keyID, &alg); err != nil {
		t.Fatalf("load credential row: %v", err)
	}
	_ = ct
	plain, err := fx.crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: []byte(ct), Nonce: []byte(nonce),
		KeyID: keyID, Algorithm: alg,
	}, crypto.AAD{
		DeploymentID: fx.crypter.DeploymentID(),
		Table:        "datasource_credentials", PrimaryKey: rowID,
		Column: "credential", Purpose: "datasource_credential",
	})
	if err != nil {
		t.Fatalf("open credential (runtime AAD): %v", err)
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Username, payload.Password
}

// openTLSRow white-box helper: decrypts TLS material row using runtime AAD and returns plaintext.
func openTLSRow(t *testing.T, fx *b3Fixture, rowID string) []byte {
	t.Helper()
	var ct, nonce []byte
	var keyID, alg string
	if err := fx.db.Raw(`select material_ciphertext, material_nonce, material_key_id, material_algorithm
		from datasource_tls_materials where id = ?`, rowID).Row().Scan(&ct, &nonce, &keyID, &alg); err != nil {
		t.Fatalf("load tls row: %v", err)
	}
	if keyID == "" {
		t.Fatal("tls row not sealed")
	}
	plain, err := fx.crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: ct, Nonce: nonce,
		KeyID: keyID, Algorithm: alg,
	}, crypto.AAD{
		DeploymentID: fx.crypter.DeploymentID(),
		Table:        "datasource_tls_materials", PrimaryKey: rowID,
		Column: "material", Purpose: "datasource_tls",
	})
	if err != nil {
		t.Fatalf("open tls material (runtime AAD): %v", err)
	}
	return plain
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func datasourceWrite(name, host string, creds []any, tls any) map[string]any {
	return map[string]any{
		"name": name, "engine": "mysql", "compatibility_mode": "mysql",
		"deployment_kind": "native", "host": host, "port": 3306,
		"version_constraint": ">=5.7", "enabled": true,
		"credentials": creds, "tls": tls,
	}
}

func replaceCred(purpose, username string, password any, reuse any) map[string]any {
	cred := map[string]any{"purpose": purpose}
	if username != "" {
		cred["username"] = username
	}
	if password != nil {
		cred["password"] = password
	}
	if reuse != nil {
		cred["reuse_credential_purpose"] = reuse
	}
	return cred
}

// TestCredentialThreeModes three explicit credential modes (RCP decision 1):
// keep = self-reference copies full credential; replace = new SecretInput; reuse = cross-purpose reference;
// ambiguous or missing mode rejected; keep is valid only for existing rows.
func TestCredentialThreeModes(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	// Create with replace mode.
	create := datasourceWrite("ds-keep", "db-a.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-1"}, nil),
	}, nil)
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/datasources", create, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create: %d %v", code, body)
	}
	dsID := dataField(body, "id").(string)
	version := dataField(body, "version").(float64)

	// keep mode (self-reference, no username) while changing host: credential copied verbatim.
	putBody := datasourceWrite("ds-keep", "db-b.internal", []any{
		replaceCred("review", "", nil, "review"),
	}, nil)
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/datasources/"+dsID, putBody,
		fmt.Sprintf("%d", int(version)))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("keep replace: %d %v", code, body)
	}
	if dataField(body, "host") != "db-b.internal" {
		t.Fatalf("host not updated: %v", dataField(body, "host"))
	}
	rowID := scanOneString(t, fx, `select id from datasource_credentials
		where datasource_id = '`+dsID+`' and enabled = true and purpose = 'review'`)
	username, password := openCredentialRow(t, fx, rowID)
	if username != "rev-user" || password != "revpw-1" {
		t.Fatalf("keep must copy full credential, got %q/%q", username, password)
	}
	version = dataField(body, "version").(float64)

	// reuse mode: add query purpose, referencing existing review row password + new username.
	putBody = datasourceWrite("ds-keep", "db-b.internal", []any{
		replaceCred("review", "", nil, "review"),
		replaceCred("query", "qry-user", nil, "review"),
	}, nil)
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/datasources/"+dsID, putBody,
		fmt.Sprintf("%d", int(version)))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("reuse replace: %d %v", code, body)
	}
	qryRow := scanOneString(t, fx, `select id from datasource_credentials
		where datasource_id = '`+dsID+`' and enabled = true and purpose = 'query'`)
	username, password = openCredentialRow(t, fx, qryRow)
	if username != "qry-user" || password != "revpw-1" {
		t.Fatalf("reuse must take source password with new username, got %q/%q", username, password)
	}

	// Negative: ambiguous mode (both password and reuse sent).
	bad := datasourceWrite("ds-keep", "db-b.internal", []any{
		replaceCred("review", "x-user", map[string]any{"value": "newpw-1"}, "review"),
	}, nil)
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("ambiguous mode must 1001, got %v", body)
	}
	// Negative: missing mode.
	bad = datasourceWrite("ds-keep", "db-b.internal", []any{
		replaceCred("review", "x-user", nil, nil),
	}, nil)
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("no mode must 1001, got %v", body)
	}
	// Negative: keep on create (no existing row).
	bad = datasourceWrite("ds-keep2", "db-c.internal", []any{
		replaceCred("review", "", nil, "review"),
	}, nil)
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("keep on create must 1001, got %v", body)
	}
	// Negative: cross-purpose reuse on create (source row does not exist).
	bad = datasourceWrite("ds-keep3", "db-c.internal", []any{
		replaceCred("review", "a-user", map[string]any{"value": "apw-001"}, nil),
		replaceCred("query", "b-user", nil, "review"),
	}, nil)
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("reuse on create must 1001, got %v", body)
	}
}

// TestTLSMaterialSurface TLS material declaration surface (RCP decision 2 + RCP-20261016 tri-state):
// write-only SecretInput, per-purpose sealing, tls_verified state, PUT tri-state semantics
// (key omitted = preserve material and tls_verified / explicit null = remove / object = full replacement
// requiring non-null ca_pem), and shape validation (paired cert/key / parseable PEM / replacement without CA rejected).
func TestTLSMaterialSurface(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	caPEM, certPEM, keyPEM := genTestPEM(t)
	create := datasourceWrite("ds-tls", "db-tls.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-1"}, nil),
		replaceCred("query", "qry-user", map[string]any{"value": "qrypw-1"}, nil),
	}, map[string]any{
		"ca_pem":          map[string]any{"value": caPEM},
		"client_cert_pem": map[string]any{"value": certPEM},
		"client_key_pem":  map[string]any{"value": keyPEM},
	})
	code, body, _ := fx.admin.do("POST", "/api/v4/admin/datasources", create, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create with tls: %d %v", code, body)
	}
	dsID := dataField(body, "id").(string)
	if dataField(body, "tls_verified") != true {
		t.Fatalf("tls_verified must be true, got %v", dataField(body, "tls_verified"))
	}
	// One row per purpose; material is decryptable with runtime AAD and contains concatenated PEM.
	rows := scanStrings(t, fx.db, `select id from datasource_tls_materials
		where datasource_id = '`+dsID+`' order by purpose`)
	if len(rows) != 2 {
		t.Fatalf("tls rows = %d, want 2", len(rows))
	}
	for _, rowID := range rows {
		plain := openTLSRow(t, fx, rowID)
		certCount, keyCount := 0, 0
		rest := plain
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			switch block.Type {
			case "CERTIFICATE":
				certCount++
			case "PRIVATE KEY":
				keyCount++
			}
		}
		if certCount != 2 || keyCount != 1 {
			t.Fatalf("tls bundle blocks = %d certs/%d keys", certCount, keyCount)
		}
	}
	// Response contains no PEM material (Secrets are write-only).
	raw := marshalJSON(t, body)
	if strings.Contains(raw, "BEGIN CERTIFICATE") || strings.Contains(raw, "BEGIN PRIVATE KEY") {
		t.Fatal("TLS material leaked into view")
	}
	version := dataField(body, "version").(float64)

	// PUT tri-state [omitted = preserve] (RCP-20261016): payload omits tls key -- existing material
	// and tls_verified are preserved as-is (editing non-Secret fields no longer requires re-pasting material).
	keepBody := datasourceWrite("ds-tls", "db-tls-keep.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-2"}, nil),
		replaceCred("query", "qry-user", map[string]any{"value": "qrypw-2"}, nil),
	}, nil)
	delete(keepBody, "tls")
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/datasources/"+dsID, keepBody,
		fmt.Sprintf("%d", int(version)))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace omitting tls key: %d %v", code, body)
	}
	if dataField(body, "tls_verified") != true {
		t.Fatalf("omit must keep tls_verified true, got %v", dataField(body, "tls_verified"))
	}
	var remain int64
	fx.db.Raw(`select count(*) from datasource_tls_materials where datasource_id = ?`,
		dsID).Scan(&remain)
	if remain != 2 {
		t.Fatalf("omit must keep tls rows, remain=%d want 2", remain)
	}
	version = dataField(body, "version").(float64)

	// [omitted = preserve x purpose set expansion] (R1 High-1): adding execution credential while
	// omitting tls -- new purpose must inherit existing material (all 3 rows present, tls_verified=true),
	// preventing silent plaintext fallback on the new purpose while tls_verified stays true.
	addPurpose := datasourceWrite("ds-tls", "db-tls-keep.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-2c"}, nil),
		replaceCred("query", "qry-user", map[string]any{"value": "qrypw-2c"}, nil),
		replaceCred("execution", "exec-user", map[string]any{"value": "execpw-2c"}, nil),
	}, nil)
	delete(addPurpose, "tls")
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/datasources/"+dsID, addPurpose,
		fmt.Sprintf("%d", int(version)))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("omit tls + add purpose: %d %v", code, body)
	}
	if dataField(body, "tls_verified") != true {
		t.Fatalf("inherited tls_verified = %v want true", dataField(body, "tls_verified"))
	}
	var purposes []string
	fx.db.Raw(`select purpose from datasource_tls_materials where datasource_id = ? order by purpose`,
		dsID).Scan(&purposes)
	if len(purposes) != 3 || purposes[0] != "execution" || purposes[1] != "query" || purposes[2] != "review" {
		t.Fatalf("purpose set after keep+add: %v want [execution query review]", purposes)
	}
	// New purpose row is decryptable and its bundle matches existing material (re-sealed content unchanged).
	execRows := scanStrings(t, fx.db, `select id from datasource_tls_materials
		where datasource_id = '`+dsID+`' and purpose = 'execution'`)
	if len(execRows) != 1 {
		t.Fatalf("execution tls row = %d want 1", len(execRows))
	}
	if plain := openTLSRow(t, fx, execRows[0]); !strings.Contains(string(plain), "BEGIN CERTIFICATE") {
		t.Fatal("inherited execution material is not PEM")
	}
	version = dataField(body, "version").(float64)

	// [omitted = preserve x purpose set reduction]: remove query+execution, leaving only review --
	// material rows for removed purposes are cleaned up (row set mirrors purpose set), material content unchanged.
	dropPurposes := datasourceWrite("ds-tls", "db-tls-keep.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-2d"}, nil),
	}, nil)
	delete(dropPurposes, "tls")
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/datasources/"+dsID, dropPurposes,
		fmt.Sprintf("%d", int(version)))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("omit tls + drop purposes: %d %v", code, body)
	}
	if dataField(body, "tls_verified") != true {
		t.Fatalf("kept tls_verified after drop = %v want true", dataField(body, "tls_verified"))
	}
	purposes = nil
	fx.db.Raw(`select purpose from datasource_tls_materials where datasource_id = ?`, dsID).Scan(&purposes)
	if len(purposes) != 1 || purposes[0] != "review" {
		t.Fatalf("purpose set after keep+drop: %v want [review]", purposes)
	}
	version = dataField(body, "version").(float64)

	// PUT tri-state [object = replace]: re-seals new material (tls_verified remains true).
	replaceBody := datasourceWrite("ds-tls", "db-tls-replace.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-3"}, nil),
		replaceCred("query", "qry-user", map[string]any{"value": "qrypw-3"}, nil),
	}, map[string]any{
		"ca_pem": map[string]any{"value": caPEM},
	})
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/datasources/"+dsID, replaceBody,
		fmt.Sprintf("%d", int(version)))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace with tls object: %d %v", code, body)
	}
	if dataField(body, "tls_verified") != true {
		t.Fatalf("replace must keep tls_verified true, got %v", dataField(body, "tls_verified"))
	}
	fx.db.Raw(`select count(*) from datasource_tls_materials where datasource_id = ?`,
		dsID).Scan(&remain)
	if remain != 2 {
		t.Fatalf("replace rows=%d want 2 (per purpose)", remain)
	}
	version = dataField(body, "version").(float64)

	// PUT tri-state [explicit null = remove] (destructive action must be explicitly declared).
	putBody := datasourceWrite("ds-tls", "db-tls.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-2b"}, nil),
		replaceCred("query", "qry-user", map[string]any{"value": "qrypw-2b"}, nil),
	}, nil)
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/datasources/"+dsID, putBody,
		fmt.Sprintf("%d", int(version)))
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace with tls null: %d %v", code, body)
	}
	if dataField(body, "tls_verified") != false {
		t.Fatalf("tls_verified must be false after explicit removal, got %v", dataField(body, "tls_verified"))
	}
	fx.db.Raw(`select count(*) from datasource_tls_materials where datasource_id = ?`,
		dsID).Scan(&remain)
	if remain != 0 {
		t.Fatalf("tls rows remain: %d", remain)
	}

	// Replacement object must carry non-null ca_pem (RCP-20261016): paired cert/key
	// without CA -> 1001.
	bad := datasourceWrite("ds-tls2", "db-tls.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-3"}, nil),
	}, map[string]any{
		"client_cert_pem": map[string]any{"value": certPEM},
		"client_key_pem":  map[string]any{"value": keyPEM},
	})
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("mTLS material without ca_pem must 1001, got %v", body)
	}
	// Shape validation: client cert without private key (and without CA) -> 1001.
	bad = datasourceWrite("ds-tls2", "db-tls.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-3"}, nil),
	}, map[string]any{
		"client_cert_pem": map[string]any{"value": certPEM},
	})
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("unpaired client material must 1001, got %v", body)
	}
	// Non-PEM payload -> 1001.
	bad = datasourceWrite("ds-tls3", "db-tls.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-4"}, nil),
	}, map[string]any{
		"ca_pem": map[string]any{"value": "not-a-pem-blob"},
	})
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("non-PEM material must 1001, got %v", body)
	}
	// Mismatched block type and field (certificate block in key field) -> 1001 (R1 review fix).
	bad = datasourceWrite("ds-tls3b", "db-tls.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-6"}, nil),
	}, map[string]any{
		"client_cert_pem": map[string]any{"value": certPEM},
		"client_key_pem":  map[string]any{"value": certPEM},
	})
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("cert block in key field must 1001, got %v", body)
	}
	// Empty tls block -> 1001.
	bad = datasourceWrite("ds-tls4", "db-tls.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-5"}, nil),
	}, map[string]any{})
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", bad, true)
	if errCode(body) != 1001 {
		t.Fatalf("empty tls block must 1001, got %v", body)
	}
}

// TestSettingsHTTPLayerAligned settings HTTP layer (RCP decision 5 + route mounting fix):
// schema fields populated, revisions Page envelope, real effects calculation, high-impact two-step
// confirmation and 1011, and token session binding.
func TestSettingsHTTPLayerAligned(t *testing.T) {
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	fx.db.Exec(`delete from system_settings where namespace in ('general','query')`)
	fx.db.Exec(`delete from audit_events where event_type = 'settings.replaced'`)

	// GET schema: schema_version and ui_hints present.
	code, body, _ := fx.admin.do("GET", "/api/v4/admin/settings/ai-budget/schema", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("schema: %d %v", code, body)
	}
	schema := dataMap(body)
	if _, ok := schema["schema_version"]; !ok {
		t.Fatalf("schema_version missing: %v", schema)
	}
	hints, ok := schema["ui_hints"].(map[string]any)
	if !ok || len(hints) == 0 {
		t.Fatalf("ui_hints missing: %v", schema)
	}
	jsonSchema, ok := schema["json_schema"].(map[string]any)
	if !ok || jsonSchema["type"] != "object" {
		t.Fatalf("json_schema missing: %v", schema)
	}

	// Low-impact direct save.
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/settings/general",
		map[string]any{"settings": map[string]any{"default_locale": "en-US"}}, "1")
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("low-impact replace: %d %v", code, body)
	}

	// High-impact: missing token -> 1011.
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/settings/query",
		map[string]any{"settings": map[string]any{"approval_enabled": false}}, "1")
	if code != 200 || errCode(body) != 1011 {
		t.Fatalf("high without token must 1011, got %d %v", code, body)
	}
	// assess: effects has real {kind, count, consequence} shape.
	code, body, _ = fx.admin.do("POST", "/api/v4/admin/settings/query/impact-assessments",
		map[string]any{"settings": map[string]any{"approval_enabled": false}}, true)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("assess: %d %v", code, body)
	}
	assess := dataMap(body)
	impact := assess["impact"].(map[string]any)
	effects := impact["effects"].([]any)
	if len(effects) != 1 {
		t.Fatalf("effects: %v", effects)
	}
	effect := effects[0].(map[string]any)
	if effect["kind"] != "active_query_grants" || effect["consequence"] != "unchanged" {
		t.Fatalf("effect: %v", effect)
	}
	if _, ok := effect["count"].(float64); !ok {
		t.Fatalf("count missing: %v", effect)
	}
	// Succeeds with token.
	token, _ := assess["impact_token"].(string)
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/settings/query",
		map[string]any{"settings": map[string]any{"approval_enabled": false},
			"impact_token": token}, "1")
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("replace with token: %d %v", code, body)
	}
	// Replay same token (stale version) -> version conflict 1003.
	code, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/settings/query",
		map[string]any{"settings": map[string]any{"approval_enabled": false},
			"impact_token": token}, "1")
	if code != 200 || errCode(body) != 1003 {
		t.Fatalf("token replay must 1003, got %d %v", code, body)
	}

	// revisions: Page envelope + changed_by/changed_paths.
	code, body, _ = fx.admin.do("GET", "/api/v4/admin/settings/query/revisions?limit=10", nil, false)
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("revisions: %d %v", code, body)
	}
	page := dataMap(body)
	if _, ok := page["page"]; !ok {
		t.Fatalf("Page envelope missing: %v", page)
	}
	items := page["items"].([]any)
	if len(items) == 0 {
		t.Fatal("revisions empty")
	}
	rev := items[0].(map[string]any)
	if _, ok := rev["changed_by"].(string); !ok {
		t.Fatalf("changed_by missing: %v", rev)
	}
	if _, ok := rev["changed_paths"].([]any); !ok {
		t.Fatalf("changed_paths missing: %v", rev)
	}
	if _, ok := rev["changed_at"].(string); !ok {
		t.Fatalf("changed_at missing: %v", rev)
	}
}

func dataMap(body map[string]any) map[string]any {
	data, _ := body["data"].(map[string]any)
	return data
}

func dataField(body map[string]any, field string) any {
	return dataMap(body)[field]
}

func scanOneString(t *testing.T, fx *b3Fixture, query string) string {
	t.Helper()
	rows := scanStrings(t, fx.db, query)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for %s, got %d", query, len(rows))
	}
	return rows[0]
}
