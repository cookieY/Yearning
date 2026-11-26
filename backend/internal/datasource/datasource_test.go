package datasource

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

func TestResolveCapabilitiesMatrix(t *testing.T) {
	cases := []struct {
		engine, mode, version string
		wantTarget            string
		wantErr               bool
		wantErrSubstr         string
	}{
		{"mysql", "mysql", "5.7.44-log", "mysql", false, ""},
		{"mysql", "mysql", "8.0.36", "mysql", false, ""},
		{"mysql", "mysql", "8.4.13-0ubuntu0.24.04.1", "mysql", false, ""},
		{"mysql", "mysql", "9.1.0", "mysql", false, ""},
		{"mysql", "mysql", "5.6.51", "", true, ">=5.7"},
		{"mysql", "mysql", "5.7.", "", true, ">=5.7"},
		{"mysql", "mysql", "8.4.x", "", true, ">=5.7"},
		{"mysql", "mysql", "", "", true, ">=5.7"},
		{"mysql", "mysql", "invalid", "", true, ">=5.7"},
		{"postgresql", "postgresql", "14.11", "postgresql", false, ""},
		{"postgresql", "postgresql", "17.2", "postgresql", false, ""},
		{"postgresql", "postgresql", "18.0", "postgresql", false, ""},
		{"postgresql", "postgresql", "18", "postgresql", false, ""},
		{"postgresql", "postgresql", "13.3", "", true, ">=14"},
		{"postgresql", "postgresql", "14.0.bad", "", true, ">=14"},
		{"postgresql", "postgresql", "9.6.24", "", true, ">=14"},
		{"tidb", "mysql", "8.0.11-TiDB-v7.5.1", "tidb-mysql", false, ""},
		{"oceanbase", "mysql", "5.7.65-OceanBase", "oceanbase-mysql", false, ""},
		{"polardb", "mysql", "8.0.2", "polardb-mysql", false, ""},
		{"polardb", "postgresql", "15.4", "polardb-postgresql", false, ""},
		{"mysql", "postgresql", "8.0.1", "", true, ""}, // Engine and compatibility mode mismatch
	}
	for _, tc := range cases {
		target, caps, err := ResolveCapabilities(tc.engine, tc.mode, tc.version)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s/%s %s: expected unsupported", tc.engine, tc.mode, tc.version)
			}
			if !errors.Is(err, ErrUnsupportedCapability) {
				t.Fatalf("%s/%s %s: err = %v, want ErrUnsupportedCapability", tc.engine, tc.mode, tc.version, err)
			}
			if tc.wantErrSubstr != "" && (!strings.Contains(err.Error(), tc.wantErrSubstr) || (tc.version != "" && !strings.Contains(err.Error(), tc.version))) {
				t.Fatalf("%s/%s %s: err = %q, want substring %q and version", tc.engine, tc.mode, tc.version, err.Error(), tc.wantErrSubstr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s/%s %s: %v", tc.engine, tc.mode, tc.version, err)
		}
		if target != tc.wantTarget {
			t.Fatalf("target = %s want %s", target, tc.wantTarget)
		}
		if !caps[CapConnectionTest] || !caps[CapDmlExecution] {
			t.Fatalf("required capability missing for %s", target)
		}
		if tc.engine == "mysql" && !caps[CapGhOst] {
			t.Fatalf("native mysql (%s) must advertise gh_ost", tc.version)
		}
		if tc.engine == "postgresql" && caps[CapGhOst] {
			t.Fatalf("postgresql must not advertise gh_ost")
		}
	}
}

func TestEngineModeCombinations(t *testing.T) {
	valid := map[string]bool{}
	for _, tgt := range CapabilityMatrix {
		key := tgt.Engine + "/" + tgt.Mode
		valid[key] = true
	}
	for _, combo := range []string{"mysql/mysql", "tidb/mysql", "oceanbase/mysql", "postgresql/postgresql", "polardb/mysql", "polardb/postgresql"} {
		if !valid[combo] {
			t.Fatalf("engine/mode %s missing from matrix", combo)
		}
	}
	if engineModeOK("mysql", "postgresql") {
		t.Fatal("mysql/postgresql combo accepted")
	}
	if engineModeOK("postgresql", "mysql") {
		t.Fatal("postgresql/mysql combo accepted")
	}
}

func TestConstraintMatches(t *testing.T) {
	if !ConstraintMatches(">=5.7", "5.7.0") || !ConstraintMatches(">=5.7", "8.4.13-0ubuntu0.24.04.1") || !ConstraintMatches(">=5.7", "9.1.0") {
		t.Fatal(">=5.7 must accept 5.7+ releases")
	}
	if ConstraintMatches(">=5.7", "5.6.51") || ConstraintMatches(">=5.7", "5.7.") || ConstraintMatches(">=5.7", "8.4.x") || ConstraintMatches(">=5.7", "") || ConstraintMatches(">=5.7", "bad") {
		t.Fatal(">=5.7 must reject <5.7 and unparseable versions")
	}
	if !ConstraintMatches(">=14", "14") || !ConstraintMatches(">=14", "18.1 (Debian 18.1-1)") || ConstraintMatches(">=14", "13.9") || ConstraintMatches(">=14", "14.0.bad") || ConstraintMatches(">=14", "") {
		t.Fatal(">=14 matching wrong")
	}
	if !ConstraintMatches("5.7.x", "5.7.44") || ConstraintMatches("5.7.x", "5.6.50") {
		t.Fatal("5.7.x matching wrong")
	}
	if !ConstraintMatches("latest_stable_at_release", "anything-1.2") {
		t.Fatal("cloud constraint must accept latest stable")
	}
}

// TestOpenTLSMaterialCertClassification verifies the PEM classification heuristic:
// with private key => the last certificate block is the client certificate and remaining blocks enter the CA chain;
// without private key => all certificate blocks enter the CA chain; client certificates are never mixed into RootCAs.
func TestOpenTLSMaterialCertClassification(t *testing.T) {
	f := &Factory{}
	makePEM := func(blocks ...string) []byte {
		var out []byte
		for _, b := range blocks {
			out = append(out, []byte(b)...)
		}
		return out
	}
	m := &DatasourceTLSMaterialModel{VerifyServer: true}
	keyPEM := "-----BEGIN EC PRIVATE KEY-----\nZm9v\n-----END EC PRIVATE KEY-----\n"
	cert1 := "-----BEGIN CERTIFICATE-----\nY2VydDE=\n-----END CERTIFICATE-----\n"
	cert2 := "-----BEGIN CERTIFICATE-----\nY2VydDI=\n-----END CERTIFICATE-----\n"

	// With private key + two certs: cert1 -> CA chain, cert2 -> client certificate.
	m.MaterialCiphertext = makePEM(cert1, cert2, keyPEM)
	mat, err := f.openTLSMaterialForTest(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(mat.CertPEM) != cert2 {
		t.Fatalf("client certificate must be the last certificate block, got %q", mat.CertPEM)
	}
	if string(mat.CAPEM) != cert1 {
		t.Fatalf("CA chain must not contain client certificate, got %q", mat.CAPEM)
	}
	if string(mat.KeyPEM) != keyPEM {
		t.Fatal("private key parse error")
	}

	// Single certificate + private key (pure mTLS client material): cert is client certificate, CA chain is empty.
	m.MaterialCiphertext = makePEM(cert1, keyPEM)
	mat, err = f.openTLSMaterialForTest(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(mat.CertPEM) != cert1 {
		t.Fatalf("with single certificate + private key, certificate must be client certificate, got CAPEM=%q", mat.CAPEM)
	}
	if mat.CAPEM != nil {
		t.Fatalf("pure mTLS material must not be mixed into RootCAs, got %q", mat.CAPEM)
	}

	// Without private key + two certs: all go into CA chain, no client certificate.
	m.MaterialCiphertext = makePEM(cert1, cert2)
	mat, err = f.openTLSMaterialForTest(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(mat.CAPEM) != cert1+cert2 {
		t.Fatalf("without private key, all certificates must be in CA chain, got %q", mat.CAPEM)
	}
	if mat.CertPEM != nil {
		t.Fatalf("without private key, no client certificate must be produced, got %q", mat.CertPEM)
	}
}

func TestCapabilitiesServesFreshCachedProbeWithinRateLimitWindow(t *testing.T) {
	svc := &Service{}
	dsID := "0198d9cc-0000-7000-8000-000000000099"
	now := time.Now().UTC()
	svc.probeMu.Store(dsID, now)
	svc.lastCaps.Store(dsID, cachedCapabilities{
		purpose: "review",
		view: CapabilitiesView{
			DatasourceID:        dsID,
			DetectedVersion:     "8.0.46",
			IdentityFingerprint: "fp-1",
			Capabilities:        map[string]bool{"query": true, "gh_ost": true},
			CheckedAt:           now,
		},
		cachedAt: time.Now(),
	})

	got, err := svc.Capabilities(context.Background(), dsID, "review")
	if err != nil {
		t.Fatalf("Capabilities returned unexpected error: %v", err)
	}
	if got.DetectedVersion != "8.0.46" || !got.Capabilities["gh_ost"] {
		t.Fatalf("unexpected cached capabilities: %+v", got)
	}
	// Mutating the returned map must not corrupt the cached entry.
	got.Capabilities["gh_ost"] = false
	gotAgain, err := svc.Capabilities(context.Background(), dsID, "review")
	if err != nil {
		t.Fatalf("second Capabilities call failed: %v", err)
	}
	if !gotAgain.Capabilities["gh_ost"] {
		t.Fatal("cached capabilities map was mutated by caller")
	}

	// Different purpose misses the cache and hits the active rate limit instead of silently reusing review credentials.
	if _, err := svc.Capabilities(context.Background(), dsID, "execution"); !errors.Is(err, ErrTestRateLimited) {
		t.Fatalf("mismatched purpose error = %v, want ErrTestRateLimited", err)
	}
	if _, err := svc.Capabilities(context.Background(), dsID, "invalid"); !errors.Is(err, bizerr.ErrValidationFailed) {
		t.Fatalf("invalid purpose error = %v, want ErrValidationFailed", err)
	}
}

type asyncTaskFailFx struct {
	execQueries []string
	execArgs    [][]driver.NamedValue
	queryErr    error
	pendingCnt  int64
}

func (f *asyncTaskFailFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *asyncTaskFailFx) Driver() driver.Driver                        { return asyncTaskFailDriver{} }

type asyncTaskFailDriver struct{}

func (asyncTaskFailDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unused open")
}
func (f *asyncTaskFailFx) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused prepare")
}
func (f *asyncTaskFailFx) Close() error              { return nil }
func (f *asyncTaskFailFx) Begin() (driver.Tx, error) { return nil, errors.New("unused begin") }
func (f *asyncTaskFailFx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	f.execQueries = append(f.execQueries, query)
	f.execArgs = append(f.execArgs, args)
	return driver.RowsAffected(1), nil
}
func (f *asyncTaskFailFx) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(strings.ToLower(query), "from async_tasks") {
		return &asyncTaskCountRows{val: f.pendingCnt}, nil
	}
	return nil, f.queryErr
}

type asyncTaskCountRows struct {
	val  int64
	done bool
}

func (r *asyncTaskCountRows) Columns() []string { return []string{"count"} }
func (r *asyncTaskCountRows) Close() error      { return nil }
func (r *asyncTaskCountRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.val
	return nil
}

func TestTestAndCapabilitiesFinishesAsyncTaskFailedOnEarlyError(t *testing.T) {
	queryBoom := errors.New("transient metadata db query failure")
	fx := &asyncTaskFailFx{queryErr: queryBoom}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:             sqlDB,
		WithoutReturning: true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}

	svc := &Service{db: db}
	_, task, err := svc.TestAndCapabilities(context.Background(), "0198d9cc-0000-7000-8000-000000000123", "review")
	if !errors.Is(err, queryBoom) {
		t.Fatalf("expected original query error, got %v", err)
	}
	if task == nil || task.State != "failed" || task.FinishedAt == nil {
		t.Fatalf("expected failed task with non-nil finished_at, got %+v", task)
	}
	if task.BusinessErrCode == nil || *task.BusinessErrCode != 3010 || task.FailureName == nil || *task.FailureName != "internal_error" {
		t.Fatalf("expected business_err_code=3010 failure_name=internal_error, got %+v", task)
	}
	if len(fx.execQueries) < 2 || !strings.Contains(strings.ToLower(fx.execQueries[1]), `update "async_tasks" set`) {
		t.Fatalf("expected deferred UPDATE on async_tasks, got %v", fx.execQueries)
	}
}

func TestTestAndCapabilitiesReturnsErrQueueFullBeforeProbeRateLimit(t *testing.T) {
	defer tasks.SetPendingMaximumForTest(3)()
	fx := &asyncTaskFailFx{pendingCnt: 3, queryErr: errors.New("should not query datasources")}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:             sqlDB,
		WithoutReturning: true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}

	svc := &Service{db: db}
	dsID := "0198d9cc-0000-7000-8000-000000000456"
	_, task, err := svc.TestAndCapabilities(context.Background(), dsID, "review")
	if !errors.Is(err, tasks.ErrQueueFull) {
		t.Fatalf("TestAndCapabilities err = %v, want tasks.ErrQueueFull", err)
	}
	if task != nil {
		t.Fatalf("expected nil task when queue full, got %+v", task)
	}
	if len(fx.execQueries) != 0 {
		t.Fatalf("expected zero async_tasks inserts when queue full, got %v", fx.execQueries)
	}
	if _, stored := svc.probeMu.Load(dsID); stored {
		t.Fatal("probeMu must not be updated when TestAndCapabilities rejects due to queue capacity")
	}
}

func TestVersionConstraintOptionalAndMatrixDefault(t *testing.T) {
	if got := DefaultVersionConstraint("mysql", "mysql"); got != ">=5.7" {
		t.Fatalf("DefaultVersionConstraint(mysql, mysql) = %q, want >=5.7", got)
	}
	if got := DefaultVersionConstraint("postgresql", "postgresql"); got != ">=14" {
		t.Fatalf("DefaultVersionConstraint(postgresql, postgresql) = %q, want >=14", got)
	}
	if got := DefaultVersionConstraint("tidb", "mysql"); got != "latest_stable_at_release" {
		t.Fatalf("DefaultVersionConstraint(tidb, mysql) = %q, want latest_stable_at_release", got)
	}
	if got := DefaultVersionConstraint("mysql", "postgresql"); got != "" {
		t.Fatalf("DefaultVersionConstraint(mysql, postgresql) = %q, want empty", got)
	}

	pw := &struct {
		Value string `json:"value"`
	}{Value: "secret-123456"}
	validBase := func(vc string) *WriteInput {
		return &WriteInput{
			Name:              "ds-test",
			Engine:            "mysql",
			CompatibilityMode: "mysql",
			DeploymentKind:    "native",
			Host:              "db.example.com",
			Port:              3306,
			VersionConstraint: vc,
			Enabled:           true,
			Credentials: []CredentialInput{{
				Purpose:  "review",
				Username: "reader",
				Password: pw,
			}},
		}
	}

	if err := validateWrite(validBase("")); err != nil {
		t.Fatalf("validateWrite with empty version_constraint failed: %v", err)
	}
	if err := validateWrite(validBase(strings.Repeat("x", 64))); err != nil {
		t.Fatalf("validateWrite with 64-char version_constraint failed: %v", err)
	}
	if err := validateWrite(validBase(strings.Repeat("x", 65))); !errors.Is(err, bizerr.ErrValidationFailed) {
		t.Fatalf("validateWrite with 65-char version_constraint = %v, want ErrValidationFailed", err)
	}

	inOmitted := validBase("   ")
	if got := resolveCreateVersionConstraint(inOmitted); got != ">=5.7" {
		t.Fatalf("resolveCreateVersionConstraint omitted = %q, want >=5.7", got)
	}
	if got := resolveCreateVersionConstraint(validBase(">=8.0,<9.0")); got != ">=8.0,<9.0" {
		t.Fatalf("resolveCreateVersionConstraint explicit = %q, want >=8.0,<9.0", got)
	}

	if got := resolveReplaceVersionConstraint(inOmitted, "8.0.x"); got != "8.0.x" {
		t.Fatalf("resolveReplaceVersionConstraint preserve = %q, want 8.0.x", got)
	}
	if got := resolveReplaceVersionConstraint(inOmitted, ""); got != ">=5.7" {
		t.Fatalf("resolveReplaceVersionConstraint empty existing fallback = %q, want >=5.7", got)
	}
	if got := resolveReplaceVersionConstraint(validBase(">=8.4"), "8.0.x"); got != ">=8.4" {
		t.Fatalf("resolveReplaceVersionConstraint explicit override = %q, want >=8.4", got)
	}
}
