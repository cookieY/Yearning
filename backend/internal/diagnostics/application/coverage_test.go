//go:build integration

package application

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/persistence"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	reviewprovider "github.com/cookieY/YearningX/backend/internal/review/provider"
)

func generateSelfSignedCertAndKeyPEM(tb testing.TB) ([]byte, []byte) {
	tb.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "diag-test.local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privKey.PublicKey, privKey)
	if err != nil {
		tb.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privKey)})
	return certPEM, keyPEM
}

func TestCollector_HelpersAndLiveBackend(t *testing.T) {
	if _, err := NewServices(nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error for nil db/crypter")
	}
	if got := StaggerOffsetSeconds("ds-1", 0); got != 0 {
		t.Fatalf("expected 0 stagger offset for non-positive interval, got %d", got)
	}
	if got := StaggerOffsetSeconds("ds-1", 60); got < 0 || got >= 60 {
		t.Fatalf("expected stagger offset in [0,60), got %d", got)
	}

	var sNil *Services
	sNil.runScheduledCollectionPass(context.Background(), time.Now())

	if maxInt64(10, 5) != 10 || maxInt64(2, 8) != 8 {
		t.Fatal("unexpected maxInt64")
	}
	if maxFloat64(10.5, 5.2) != 10.5 || maxFloat64(2.1, 8.9) != 8.9 {
		t.Fatal("unexpected maxFloat64")
	}
	if maxInt(10, 5) != 10 || maxInt(2, 8) != 8 {
		t.Fatal("unexpected maxInt")
	}
	if diffOptionalMetric[int64](nil, nil, maxInt64) != nil {
		t.Fatal("expected nil for nil cur in diffOptionalMetric[int64]")
	}
	v10 := int64(10)
	v25 := int64(25)
	if d := diffOptionalMetric(&v10, nil, maxInt64); d == nil || *d != 10 {
		t.Fatalf("unexpected diffOptionalMetric[int64] with nil prev: %v", d)
	}
	if d := diffOptionalMetric(&v10, &v25, maxInt64); d == nil || *d != 10 {
		t.Fatalf("unexpected diffOptionalMetric[int64] on counter decrease: %v", d)
	}
	if d := diffOptionalMetric(&v25, &v10, maxInt64); d == nil || *d != 15 {
		t.Fatalf("unexpected diffOptionalMetric[int64] delta: %v", d)
	}

	f10 := 10.0
	f25 := 25.0
	if diffOptionalMetric[float64](nil, nil, maxFloat64) != nil {
		t.Fatal("expected nil for nil cur in diffOptionalMetric[float64]")
	}
	if d := diffOptionalMetric(&f10, nil, maxFloat64); d == nil || *d != 10.0 {
		t.Fatalf("unexpected diffOptionalMetric[float64] with nil prev: %v", d)
	}
	if d := diffOptionalMetric(&f10, &f25, maxFloat64); d == nil || *d != 10.0 {
		t.Fatalf("unexpected diffOptionalMetric[float64] on counter decrease: %v", d)
	}
	if d := diffOptionalMetric(&f25, &f10, maxFloat64); d == nil || *d != 15.0 {
		t.Fatalf("unexpected diffOptionalMetric[float64] delta: %v", d)
	}

	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	live := NewLiveStatsCollectorBackend(db, crypter)
	defer live.Close()

	ctx := context.Background()
	userID, dsNoCred, _ := seedUserAndDatasource(t, db, false)
	dsRowNoCred := DatasourceRow{
		ID:                dsNoCred,
		Engine:            "mysql",
		CompatibilityMode: "mysql",
		Host:              "127.0.0.1",
		Port:              3306,
	}

	// Probe without read-only credential returns StatsProbeInsufficientPriv.
	probeRes, err := live.Probe(ctx, dsRowNoCred)
	if err != nil || probeRes == nil || probeRes.State != database.StatsProbeInsufficientPriv {
		t.Fatalf("expected StatsProbeInsufficientPriv, got res=%+v err=%v", probeRes, err)
	}
	if _, err := live.ReadSnapshot(ctx, dsRowNoCred, 50); err == nil {
		t.Fatal("expected ReadSnapshot error without credential")
	}
	if _, err := live.ReadTemplate(ctx, dsRowNoCred, "fp", "db"); err == nil {
		t.Fatal("expected ReadTemplate error without credential")
	}
	if _, err := live.ListColumns(ctx, dsRowNoCred, "db", "tbl"); err == nil {
		t.Fatal("expected ListColumns error without credential")
	}
	if _, err := live.ListIndexes(ctx, dsRowNoCred, "db", "tbl"); err == nil {
		t.Fatal("expected ListIndexes error without credential")
	}

	// Insert credential with invalid JSON first, then valid sealed credential.
	badCredID := uuid.NewString()
	badEnv, err := crypter.Seal(crypto.PurposePayload, []byte("not-json"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   badCredID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		insert into datasource_credentials (
			id, datasource_id, purpose, credential_ciphertext, credential_nonce,
			credential_key_id, credential_algorithm, enabled, created_at, updated_at
		) values (?, ?, 'query', ?, ?, ?, ?, true, now(), now())`,
		badCredID, dsNoCred, badEnv.Ciphertext, badEnv.Nonce, badEnv.KeyID, badEnv.Algorithm).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := live.openReadOnlyCredential(ctx, dsNoCred); err == nil {
		t.Fatal("expected error when credential JSON is invalid")
	}

	// Seal valid review credential (from env if configured; placeholder otherwise for non-connecting branches).
	adapterPGPass := os.Getenv("YEARNING_TEST_ADAPTER_PG_PASSWORD")
	credPass := adapterPGPass
	if credPass == "" {
		credPass = "placeholder-not-used-without-env"
	}
	goodCredID := uuid.NewString()
	goodPlain, _ := json.Marshal(map[string]string{
		"username": envOr("YEARNING_TEST_ADAPTER_PG_USER", "yr"),
		"password": credPass,
	})
	goodEnv, err := crypter.Seal(crypto.PurposePayload, goodPlain, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   goodCredID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		insert into datasource_credentials (
			id, datasource_id, purpose, credential_ciphertext, credential_nonce,
			credential_key_id, credential_algorithm, enabled, created_at, updated_at
		) values (?, ?, 'review', ?, ?, ?, ?, true, now(), now())`,
		goodCredID, dsNoCred, goodEnv.Ciphertext, goodEnv.Nonce, goodEnv.KeyID, goodEnv.Algorithm).Error; err != nil {
		t.Fatal(err)
	}

	// Unsupported engine with valid credential -> StatsProbeUnsupportedEngine.
	dsUnsupported := dsRowNoCred
	dsUnsupported.CompatibilityMode = "oracle"
	probeUnsup, err := live.Probe(ctx, dsUnsupported)
	if err != nil || probeUnsup == nil || probeUnsup.State != database.StatsProbeUnsupportedEngine {
		t.Fatalf("expected StatsProbeUnsupportedEngine, got res=%+v err=%v", probeUnsup, err)
	}

	// Test openTLSMaterial.
	_ = db.Exec(`delete from datasource_tls_materials where datasource_id = ?`, dsNoCred).Error

	certPEM, keyPEM := generateSelfSignedCertAndKeyPEM(t)
	tlsID := uuid.NewString()
	combinedPEM := append(append([]byte{}, certPEM...), certPEM...)
	combinedPEM = append(combinedPEM, keyPEM...)
	tlsEnv, err := crypter.Seal(crypto.PurposePayload, combinedPEM, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_tls_materials",
		PrimaryKey:   tlsID,
		Column:       "material",
		Purpose:      "datasource_tls",
	})
	if err != nil {
		t.Fatal(err)
	}
	srvName := "diag-test.local"
	if err := db.Exec(`
		insert into datasource_tls_materials (
			id, datasource_id, purpose, material_ciphertext, material_nonce, material_key_id, material_algorithm, verify_server, server_name, created_at, updated_at
		) values (?, ?, 'query', ?, ?, ?, ?, true, ?, now(), now())`,
		tlsID, dsNoCred, tlsEnv.Ciphertext, tlsEnv.Nonce, tlsEnv.KeyID, tlsEnv.Algorithm, srvName).Error; err != nil {
		t.Fatal(err)
	}
	mat, err := live.openTLSMaterial(ctx, dsNoCred)
	if err != nil || mat == nil || len(mat.CertPEM) == 0 || len(mat.KeyPEM) == 0 || len(mat.CAPEM) == 0 || mat.ServerName != srvName {
		t.Fatalf("unexpected TLS material with key+certs: mat=%+v err=%v", mat, err)
	}

	// Also test CA-only PEM branch in openTLSMaterial.
	_ = db.Exec(`delete from datasource_tls_materials where datasource_id = ?`, dsNoCred).Error
	tlsID2 := uuid.NewString()
	tlsEnv2, _ := crypter.Seal(crypto.PurposePayload, certPEM, crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "datasource_tls_materials",
		PrimaryKey:   tlsID2,
		Column:       "material",
		Purpose:      "datasource_tls",
	})
	if err := db.Exec(`
		insert into datasource_tls_materials (
			id, datasource_id, purpose, material_ciphertext, material_nonce, material_key_id, material_algorithm, verify_server, created_at, updated_at
		) values (?, ?, 'query', ?, ?, ?, ?, false, now(), now())`,
		tlsID2, dsNoCred, tlsEnv2.Ciphertext, tlsEnv2.Nonce, tlsEnv2.KeyID, tlsEnv2.Algorithm).Error; err != nil {
		t.Fatal(err)
	}
	mat2, err := live.openTLSMaterial(ctx, dsNoCred)
	if err != nil || mat2 == nil || len(mat2.CAPEM) == 0 {
		t.Fatalf("unexpected CA-only TLS material: mat=%+v err=%v", mat2, err)
	}
	_ = db.Exec(`delete from datasource_tls_materials where datasource_id = ?`, dsNoCred).Error

	// Test MySQL connection failure branch in Probe.
	dsMySQLUnreachable := DatasourceRow{
		ID:                dsNoCred,
		Engine:            "mysql",
		CompatibilityMode: "mysql",
		Host:              "127.0.0.1",
		Port:              1,
	}
	probeFail, err := live.Probe(ctx, dsMySQLUnreachable)
	if err != nil || probeFail == nil || probeFail.State != database.StatsProbeCollectionFailed {
		t.Fatalf("expected StatsProbeCollectionFailed, got res=%+v err=%v", probeFail, err)
	}

	// Test PostgreSQL live connection & pool caching when adapter test PG password is provided.
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	if adapterPGPass != "" {
		pgDBName := envOr("YEARNING_TEST_ADAPTER_PG_DB", "yearning_adapter_test")
		dsPGLive := DatasourceRow{
			ID:                dsNoCred,
			Engine:            "postgresql",
			CompatibilityMode: "postgresql",
			Host:              envOr("YEARNING_TEST_ADAPTER_PG_HOST", "127.0.0.1"),
			Port:              54333,
			DatabaseName:      &pgDBName,
		}
		_, _ = live.Probe(ctx, dsPGLive)
		// Second call exercises cached pool path in acquireReadPool.
		_, _ = live.ReadSnapshot(ctx, dsPGLive, 50)
		_, _ = live.ReadTemplate(ctx, dsPGLive, "12345", "public")
		_, _ = live.ListColumns(ctx, dsPGLive, "public", "nonexistent_table")
		_, _ = live.ListIndexes(ctx, dsPGLive, "public", "nonexistent_table")
	}
	live.Close()
	_ = userID
}

func TestCollector_ConfigLeaseLoopAndRetentionBranches(t *testing.T) {
	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, _ := seedUserAndDatasource(t, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}

	fake := &fakeCollectorBackend{probeState: database.StatsProbeReady}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake

	// GetConfig on unknown datasource.
	if _, err := svc.GetConfig(context.Background(), uuid.NewString()); err == nil {
		t.Fatal("expected error for unknown datasource")
	}

	// UpdateConfig validation errors & version conflict.
	if _, err := svc.UpdateConfig(context.Background(), actor, dsID, 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 45,
		TopN:            200,
	}); err == nil {
		t.Fatal("expected validation error for invalid interval_seconds")
	}
	if _, err := svc.UpdateConfig(context.Background(), actor, dsID, 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            10,
	}); err == nil {
		t.Fatal("expected validation error for invalid top_n")
	}
	if _, err := svc.UpdateConfig(context.Background(), actor, uuid.NewString(), 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	}); err == nil {
		t.Fatal("expected not found error for unknown datasource in UpdateConfig")
	}
	if _, err := svc.UpdateConfig(context.Background(), actor, dsID, 999, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	}); err == nil {
		t.Fatal("expected version conflict error in UpdateConfig")
	}

	// UpdateConfig when Probe returns an error -> sets LastProbeState = collection_failed.
	fake.probeErr = errors.New("probe network timeout")
	cfgView, err := svc.UpdateConfig(context.Background(), actor, dsID, 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 300,
		TopN:            200,
	})
	if err != nil || cfgView.LastProbeState != string(database.StatsProbeCollectionFailed) {
		t.Fatalf("expected collection_failed probe state, got cfg=%+v err=%v", cfgView, err)
	}
	fake.probeErr = nil

	// ListDatasources with both configured and unconfigured datasources.
	_, dsID2, _ := seedUserAndDatasource(t, db, false)
	_ = db.Exec(`update datasources set deployment_kind = '' where id = ?`, dsID2).Error
	dsList, err := svc.ListDatasources(context.Background())
	if err != nil || len(dsList) < 2 {
		t.Fatalf("unexpected ListDatasources result: len=%d err=%v", len(dsList), err)
	}

	// Test AcquireInstanceLease with nil ServerIdentityFingerprint, ttl <= 0, and competing lease holder.
	dsRow := &DatasourceRow{
		ID:                dsID,
		CompatibilityMode: "mysql",
		Host:              "10.0.0.10",
		Port:              3306,
	}
	acq1, err := svc.AcquireInstanceLease(context.Background(), dsRow, 0)
	if err != nil || !acq1 {
		t.Fatalf("expected first lease acquisition to succeed, acq=%v err=%v", acq1, err)
	}
	otherSvc, _ := NewServices(db, crypter, recorder, nil, nil, nil)
	otherSvc.HolderID = "other-holder-id"
	acq2, err := otherSvc.AcquireInstanceLease(context.Background(), dsRow, 10*time.Second)
	if err != nil || acq2 {
		t.Fatalf("expected competing holder lease acquisition to return false, acq=%v err=%v", acq2, err)
	}
	svc.ReleaseInstanceLease(context.Background(), dsRow, time.Now().UTC())

	// Test collectSnapshotInternal when another holder holds the physical instance lease.
	realDSRow, _ := svc.loadDatasourceRow(context.Background(), dsID)
	_, _ = otherSvc.AcquireInstanceLease(context.Background(), realDSRow, 30*time.Second)
	if _, err := svc.TriggerManualSnapshot(context.Background(), actor, dsID); err == nil {
		t.Fatal("expected rate limited error when instance lease is held by another holder")
	}
	otherSvc.ReleaseInstanceLease(context.Background(), realDSRow, time.Now().UTC())

	// Test Probe states in collectSnapshotInternal: disabled, insufficient_privilege, unsupported_engine, collection_failed.
	for _, st := range []database.StatsProbeState{
		database.StatsProbeDisabled,
		database.StatsProbeInsufficientPriv,
		database.StatsProbeUnsupportedEngine,
		database.StatsProbeCollectionFailed,
	} {
		fake.probeState = st
		if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, time.Now().UTC()); err == nil {
			t.Fatalf("expected error for probe state %s", st)
		}
	}
	// Second failure with IntervalSeconds=300 hits effectiveInterval = 600 cap.
	if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, time.Now().UTC()); err == nil {
		t.Fatal("expected error for second collection_failed probe")
	}
	fake.probeState = database.StatsProbeReady

	// Test duplicate fingerprint merging, optional PG counters, and gap detection in persistSnapshotAndBuckets.
	t0 := time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC)
	p1 := int64(5)
	p2 := int64(7)
	pt1 := 12.5
	pt2 := 8.5
	tb1 := int64(2)
	tb2 := int64(3)
	sb1 := int64(100)
	sb2 := int64(200)
	fake.snapshotRows = []database.NativeStatementStatRow{
		{EngineFingerprint: ""}, // skipped
		{
			EngineFingerprint:                 "fp_merge_test",
			DatabaseOrSchema:                  "",
			CallsTotal:                        10,
			TotalExecTimeMS:                   100,
			RowsExaminedOrSharedBlksReadTotal: 50,
			RowsSentOrRowsReturnedTotal:       10,
			LockTimeMSOrTempBlksWrittenTotal:  1.0,
			PlansTotal:                        &p1,
			TotalPlanTimeMS:                   &pt1,
			TempBlksReadTotal:                 &tb1,
			SharedBlksHitTotal:                &sb1,
		},
		{
			EngineFingerprint:                 "fp_merge_test",
			DatabaseOrSchema:                  "trade_db",
			CallsTotal:                        15,
			TotalExecTimeMS:                   150,
			RowsExaminedOrSharedBlksReadTotal: 75,
			RowsSentOrRowsReturnedTotal:       15,
			LockTimeMSOrTempBlksWrittenTotal:  2.0,
			PlansTotal:                        &p2,
			TotalPlanTimeMS:                   &pt2,
			TempBlksReadTotal:                 &tb2,
			SharedBlksHitTotal:                &sb2,
		},
	}
	snap1, err := svc.CollectScheduledSnapshot(context.Background(), dsID, t0)
	if err != nil || snap1.StatementCount != 1 {
		t.Fatalf("expected 1 merged statement, got snap=%+v err=%v", snap1, err)
	}

	// Next snapshot after a 30-minute gap (> 3 * 300s) triggers gapDetected = true and diffOptional* calculations.
	p3 := int64(20)
	pt3 := 30.0
	tb3 := int64(10)
	sb3 := int64(500)
	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "fp_merge_test",
			DatabaseOrSchema:                  "trade_db",
			CallsTotal:                        40,
			TotalExecTimeMS:                   400,
			RowsExaminedOrSharedBlksReadTotal: 200,
			RowsSentOrRowsReturnedTotal:       40,
			LockTimeMSOrTempBlksWrittenTotal:  5.0,
			PlansTotal:                        &p3,
			TotalPlanTimeMS:                   &pt3,
			TempBlksReadTotal:                 &tb3,
			SharedBlksHitTotal:                &sb3,
		},
	}
	snap2, err := svc.CollectScheduledSnapshot(context.Background(), dsID, t0.Add(30*time.Minute))
	if err != nil || !snap2.GapDetected {
		t.Fatalf("expected GapDetected=true after 30m gap, got snap=%+v err=%v", snap2, err)
	}

	// Test RunCollectionLoop and PruneExpiredDiagnosticsData.
	loopCtx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.RunCollectionLoop(loopCtx)
	svc.runScheduledCollectionPass(context.Background(), t0.Add(31*time.Minute))
	svc.runScheduledCollectionPass(context.Background(), t0.Add(60*time.Minute))

	if err := svc.PruneExpiredDiagnosticsData(context.Background(), t0.Add(40*24*time.Hour)); err != nil {
		t.Fatalf("PruneExpiredDiagnosticsData failed: %v", err)
	}

	// Test Disable in UpdateConfig and collecting on disabled datasource.
	cfgView, err = svc.UpdateConfig(context.Background(), actor, dsID, 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         false,
		IntervalSeconds: 0,
		TopN:            100,
	})
	if err != nil || cfgView.Enabled || cfgView.State != "disabled" {
		t.Fatalf("expected disabled config, got %+v err=%v", cfgView, err)
	}
	if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, time.Now().UTC()); err == nil {
		t.Fatal("expected ErrDatasourceDisabled when collecting on disabled datasource")
	}

	// Test openStatementTemplate & computeYearningFingerprint edge cases.
	if _, ok := svc.openStatementTemplate(nil); ok {
		t.Fatal("expected false for nil statement model")
	}
	kID := "pay-1"
	alg := "aes-256-gcm"
	if _, ok := svc.openStatementTemplate(&persistence.SQLDiagnosticsStatementModel{
		ID:                 uuid.NewString(),
		TemplateCiphertext: []byte("corrupted"),
		TemplateNonce:      []byte("123456789012"),
		TemplateKeyID:      &kID,
		TemplateAlgorithm:  &alg,
	}); ok {
		t.Fatal("expected false for corrupted template ciphertext")
	}
	if svc.computeYearningFingerprint("mysql", "mysql", "   ") != nil {
		t.Fatal("expected nil fingerprint for blank SQL")
	}
}

func TestAggregator_BranchesAndOrderComparison(t *testing.T) {
	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, flowID := seedUserAndDatasource(t, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}

	longSQL := "SELECT `id`, `customer_code`, `created_at` FROM `orders` WHERE `customer_code` = ? AND `status` IN (...) AND `region_code` = ? AND `channel_code` = ? AND `warehouse_id` = ? ORDER BY `created_at` DESC LIMIT ?"
	fake := &fakeCollectorBackend{
		probeState: database.StatsProbeReady,
		templatesByFP: map[string]string{
			"fp_0": longSQL,
		},
	}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake

	// Enable config.
	_, err = svc.UpdateConfig(context.Background(), actor, dsID, 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	})
	if err != nil {
		t.Fatal(err)
	}

	// GetOverview with invalid sort_by and unknown datasource.
	if _, err := svc.GetOverview(context.Background(), uuid.NewString(), OverviewQuery{}); err == nil {
		t.Fatal("expected error for unknown datasource in GetOverview")
	}
	if _, err := svc.GetOverview(context.Background(), dsID, OverviewQuery{SortBy: "invalid_col"}); err == nil {
		t.Fatal("expected validation error for invalid sort_by")
	}

	// GetOverview on enabled datasource with 0 snapshots triggers on-demand initial snapshot collection
	// and exercises cumulative_only mode across Chart 1, Chart 4, and effectiveMetrics!
	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "fp_0",
			DatabaseOrSchema:                  "trade_db",
			CallsTotal:                        50,
			TotalExecTimeMS:                   250.0,
			RowsExaminedOrSharedBlksReadTotal: 500,
			RowsSentOrRowsReturnedTotal:       50,
			LockTimeMSOrTempBlksWrittenTotal:  5.0,
		},
	}
	ovAuto, err := svc.GetOverview(context.Background(), dsID, OverviewQuery{})
	if err != nil || ovAuto.SnapshotCount != 1 || ovAuto.CoverageMode != "cumulative_only" {
		t.Fatalf("expected auto-collected cumulative_only overview, got %+v err=%v", ovAuto, err)
	}

	// Seed an empty snapshot (no bucket rows) and 12 statements across baseline & current windows
	// to test empty-bucket LoadTrend, Heatmap unobserved cell, regressions sorting (>10), newHighLoad (>10), and all sort_by modes.
	baseT := time.Now().UTC().Add(-30 * time.Minute)
	emptySnap := persistence.SQLDiagnosticsSnapshotModel{
		ID:             uuid.NewString(),
		DatasourceID:   dsID,
		Engine:         "mysql",
		TriggerKind:    "scheduled",
		ProbeState:     "no_samples",
		TopN:           200,
		StatementCount: 0,
		DurationMS:     5,
		SampledAt:      baseT.Add(15 * time.Minute),
		CreatedAt:      baseT.Add(15 * time.Minute),
	}
	if err := db.Create(&emptySnap).Error; err != nil {
		t.Fatal(err)
	}

	// Collect 12 fingerprints in baseline (baseT..baseT+2m) and 12 regressions + 12 new fingerprints in current window (baseT+10m..baseT+12m).
	for step := 0; step < 2; step++ {
		var rows []database.NativeStatementStatRow
		for k := 0; k < 12; k++ {
			rows = append(rows, database.NativeStatementStatRow{
				EngineFingerprint:                 fmt.Sprintf("fp_reg_%d", k),
				DatabaseOrSchema:                  "trade_db",
				CallsTotal:                        int64(20 + step*10),
				TotalExecTimeMS:                   float64(200 + step*100),
				RowsExaminedOrSharedBlksReadTotal: int64(200 + step*100),
				RowsSentOrRowsReturnedTotal:       int64(20 + step*10),
			})
		}
		fake.snapshotRows = rows
		_, _ = svc.CollectScheduledSnapshot(context.Background(), dsID, baseT.Add(time.Duration(step+1)*time.Minute))
	}

	for step := 0; step < 2; step++ {
		var rows []database.NativeStatementStatRow
		for k := 0; k < 12; k++ {
			rows = append(rows, database.NativeStatementStatRow{
				EngineFingerprint:                 fmt.Sprintf("fp_reg_%d", k),
				DatabaseOrSchema:                  "trade_db",
				CallsTotal:                        int64(50 + step*20),
				TotalExecTimeMS:                   float64(1000 + step*(500+k*50)),
				RowsExaminedOrSharedBlksReadTotal: int64(1000 + step*(500+k*10)),
				RowsSentOrRowsReturnedTotal:       int64(50 + step*20),
			})
			rows = append(rows, database.NativeStatementStatRow{
				EngineFingerprint:                 fmt.Sprintf("fp_new_%d", k),
				DatabaseOrSchema:                  "trade_db",
				CallsTotal:                        int64(10 + step*15),
				TotalExecTimeMS:                   float64(100 + step*(300+k*20)),
				RowsExaminedOrSharedBlksReadTotal: int64(100 + step*200),
				RowsSentOrRowsReturnedTotal:       int64(10 + step*15),
			})
		}
		fake.snapshotRows = rows
		_, _ = svc.CollectScheduledSnapshot(context.Background(), dsID, baseT.Add(time.Duration(10+step)*time.Minute))
	}

	cmpFrom := baseT
	cmpTo := baseT.Add(5 * time.Minute)
	curFrom := baseT.Add(9 * time.Minute)
	curTo := baseT.Add(20 * time.Minute)

	for _, sortCol := range []string{"avg_exec_time_ms", "calls", "rows_examined", "total_exec_time_ms"} {
		ov, err := svc.GetOverview(context.Background(), dsID, OverviewQuery{
			From:        curFrom,
			To:          curTo,
			CompareFrom: &cmpFrom,
			CompareTo:   &cmpTo,
			SortBy:      sortCol,
			Limit:       5,
		})
		if err != nil {
			t.Fatalf("GetOverview sort=%s failed: %v", sortCol, err)
		}
		if len(ov.Statements) > 5 || len(ov.Regressions) > 10 || len(ov.NewHighLoad) > 10 {
			t.Fatalf("unexpected truncation limits: stmts=%d reg=%d new=%d", len(ov.Statements), len(ov.Regressions), len(ov.NewHighLoad))
		}
	}

	// Test GetStatementDetail edge cases: unknown datasource/statement, unavailable template, zero from/to, no buckets in window, and order comparison with order_stages!
	if _, err := svc.GetStatementDetail(context.Background(), uuid.NewString(), uuid.NewString(), time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected error for unknown datasource in GetStatementDetail")
	}
	if _, err := svc.GetStatementDetail(context.Background(), dsID, uuid.NewString(), time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected error for unknown statement in GetStatementDetail")
	}

	var stmt0 persistence.SQLDiagnosticsStatementModel
	if err := db.Where("datasource_id = ? and engine_fingerprint = 'fp_0'", dsID).First(&stmt0).Error; err != nil {
		t.Fatal(err)
	}
	var stmtReg0 persistence.SQLDiagnosticsStatementModel
	if err := db.Where("datasource_id = ? and engine_fingerprint = 'fp_reg_0'", dsID).First(&stmtReg0).Error; err != nil {
		t.Fatal(err)
	}

	// Seed a completed change_order + order_stages row on dsID so CandidateOrdersInWindow is populated.
	orderID := uuid.NewString()
	stageID := uuid.NewString()
	execTime := baseT.Add(8 * time.Minute)
	if err := db.Exec(`
		insert into change_orders (
			id, display_number, submitter_user_id_snapshot, submitter_username_snapshot, submitter_display_name_snapshot,
			title, description, state, sql_hmac, snapshot_hash, manually_verified, rollback_enabled,
			ticket_type, rollback_status, version, submitted_at, terminal_at
		) values (?, 'YR-20261108-000099', ?, 'diag_admin', 'Diag Admin',
			'Optimize index', 'desc', 'completed', repeat('a', 64), repeat('b', 64), false, false,
			'normal', 'not_requested', 1, ?, ?)`,
		orderID, userID, execTime, execTime).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		insert into order_stages (
			id, order_id, position, datasource_id, datasource_name_snapshot, datasource_engine_snapshot,
			datasource_identity_fingerprint, stage_review_snapshot_id, review_snapshot_hash, review_gate_passed,
			target_snapshot_ciphertext, target_snapshot_nonce, target_snapshot_key_id, target_snapshot_algorithm,
			state, version, activated_at, terminal_at
		) values (?, ?, 1, ?, 'prod-mysql', 'mysql', repeat('1', 64), ?, repeat('2', 64), true,
			'\x01', '\x02', 'pay-1', 'aes-256-gcm', 'succeeded', 1, ?, ?)`,
		stageID, orderID, dsID, uuid.NewString(), execTime, execTime).Error; err != nil {
		t.Fatal(err)
	}

	// ConfirmOrderLink error branches + create + update with TargetStatementID.
	if _, err := svc.ConfirmOrderLink(context.Background(), actor, dsID, stmt0.ID, orderID, "invalid_kind", nil); err == nil {
		t.Fatal("expected error for invalid relationKind")
	}
	if _, err := svc.ConfirmOrderLink(context.Background(), actor, dsID, uuid.NewString(), orderID, "same_fingerprint", nil); err == nil {
		t.Fatal("expected error for unknown statement in ConfirmOrderLink")
	}
	if _, err := svc.ConfirmOrderLink(context.Background(), actor, dsID, stmt0.ID, uuid.NewString(), "same_fingerprint", nil); err == nil {
		t.Fatal("expected error for unknown order in ConfirmOrderLink")
	}

	link1, err := svc.ConfirmOrderLink(context.Background(), actor, dsID, stmt0.ID, orderID, "same_fingerprint", nil)
	if err != nil || link1.ID == "" {
		t.Fatalf("ConfirmOrderLink create failed: %v", err)
	}
	// Update existing link with TargetStatementID and rewritten_fingerprint_confirmed.
	link2, err := svc.ConfirmOrderLink(context.Background(), actor, dsID, stmt0.ID, orderID, "rewritten_fingerprint_confirmed", &stmtReg0.ID)
	if err != nil || link2.ID != link1.ID || link2.RelationKind != "rewritten_fingerprint_confirmed" {
		t.Fatalf("ConfirmOrderLink update failed: got %+v err=%v", link2, err)
	}

	// Also set OrderExecutedAt = nil on the link to exercise the CreatedAt fallback in buildOrderComparison.
	_ = db.Model(&persistence.SQLDiagnosticsOrderLinkModel{}).Where("id = ?", link2.ID).Update("order_executed_at", nil).Error

	// Call GetStatementDetail on stmt0 with a narrow window (< 5m halfDur) that has no buckets for stmt0 (exercises len(stmtBuckets)==0, unobserved trend points, and long preview truncation).
	detail0, err := svc.GetStatementDetail(context.Background(), dsID, stmt0.ID, baseT.Add(10*time.Minute), baseT.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("GetStatementDetail stmt0 failed: %v", err)
	}
	if detail0.OrderComparison.LinkedChangeOrderID == nil || len(detail0.OrderComparison.CandidateOrdersInWindow) == 0 {
		t.Fatalf("expected linked order and candidate orders in comparison: %+v", detail0.OrderComparison)
	}
	if detail0.Summary.TemplatePreview == nil || !strings.HasSuffix(*detail0.Summary.TemplatePreview, "…") {
		t.Fatalf("expected truncated template preview for >160 rune SQL, got %v", detail0.Summary.TemplatePreview)
	}

	// Call GetStatementDetail on stmtReg0 (where template is not in fake.templatesByFP -> marks unavailable, then call again to hit already-unavailable branch).
	detailUnavail1, err := svc.GetStatementDetail(context.Background(), dsID, stmtReg0.ID, time.Time{}, time.Time{})
	if err != nil || detailUnavail1.TemplateStatus != "unavailable" || detailUnavail1.TemplateNote == nil {
		t.Fatalf("expected unavailable template status on first call, got %+v err=%v", detailUnavail1, err)
	}
	detailUnavail2, err := svc.GetStatementDetail(context.Background(), dsID, stmtReg0.ID, time.Time{}, time.Time{})
	if err != nil || detailUnavail2.TemplateStatus != "unavailable" || detailUnavail2.TemplateNote == nil {
		t.Fatalf("expected unavailable template status on second call, got %+v err=%v", detailUnavail2, err)
	}

	if notes := engineCostUnitNotes("postgresql"); notes["shared_blks_hit"] == "" {
		t.Fatalf("expected postgresql cost unit notes, got %+v", notes)
	}
	_ = flowID
}

func TestAIDiagnose_EnglishPostgresLLMAndSubmissionBranches(t *testing.T) {
	// 1. ValidateNoHallucinatedPlanFacts additional branches.
	if err := ValidateNoHallucinatedPlanFacts(nil, []domain.DiagnosticAIRootCauseHypothesisView{
		{Hypothesis: "Possible missing index", Confidence: "", EvidenceBasis: "stats"},
	}); err == nil {
		t.Fatal("expected error for empty confidence")
	}
	if err := ValidateNoHallucinatedPlanFacts(nil, []domain.DiagnosticAIRootCauseHypothesisView{
		{Hypothesis: "The execution plan confirmed a full scan", Confidence: "high", EvidenceBasis: "stats"},
	}); err == nil {
		t.Fatal("expected error for hallucinated plan phrase in hypothesis")
	}

	// 2. Helper unit tests.
	if (*privacy.TurnPrivacySession)(nil).RestoreTextTokens("table_1") != "table_1" {
		t.Fatal("expected unchanged text when privacy session is nil")
	}
	if classifyCandidateStatementKind("UPDATE orders SET status = 'paid' WHERE id = 1") != "dml" {
		t.Fatal("expected dml statement kind")
	}

	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, flowID := seedUserAndDatasource(t, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_admin", Role: "admin"}

	// Switch dsID to postgresql with nil database_name to test postgresql schema fallback and English PG diagnosis labels.
	if err := db.Exec(`update datasources set engine = 'postgresql', compatibility_mode = 'postgresql', database_name = NULL where id = ?`, dsID).Error; err != nil {
		t.Fatal(err)
	}

	longTable := "very_long_organization_financial_settlement_transaction_items"
	pgSQL := fmt.Sprintf("SELECT a FROM app_schema.%s JOIN other_schema.users ON a = b WHERE col_alpha = $1 AND col_beta = $2 AND col_gamma = $3 ORDER BY col_delta DESC", longTable)
	fake := &fakeCollectorBackend{
		probeState: database.StatsProbeReady,
		templatesByFP: map[string]string{
			"fp_pg_slow": pgSQL,
		},
	}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake

	if svc.VerifyCandidateSQLHMAC(nil, "SELECT 1") {
		t.Fatal("expected false for nil report in VerifyCandidateSQLHMAC")
	}

	// DesensitizeStatementTemplate with nil privacy session.
	dsRow, _ := svc.loadDatasourceRow(context.Background(), dsID)
	if _, err := svc.DesensitizeStatementTemplate(context.Background(), nil, dsRow, "", pgSQL); err == nil {
		t.Fatal("expected error for nil privacy session")
	}

	// DesensitizeStatementTemplate with Collector == nil (covers empty colTokens branch) and mysql default_db fallback.
	svcNoCol, _ := NewServices(db, crypter, recorder, nil, nil, nil)
	svcNoCol.Collector = nil
	priv1 := privacy.NewTurnPrivacySession()
	dsMySQLNoDB := &DatasourceRow{ID: dsID, Engine: "mysql", CompatibilityMode: "mysql", DatabaseName: nil}
	mysqlSQL := "SELECT `a` FROM `app_schema`.`orders` JOIN `other_schema`.`users` ON `a` = `b` WHERE `col_alpha` = ?"
	desNoCol, err := svcNoCol.DesensitizeStatementTemplate(context.Background(), priv1, dsMySQLNoDB, "", mysqlSQL)
	if err != nil || desNoCol.PrimarySchema != "default_db" {
		t.Fatalf("unexpected DesensitizeStatementTemplate with nil collector: res=%+v err=%v", desNoCol, err)
	}
	priv1.ZeroAndRelease()

	// DesensitizeStatementTemplate on postgresql with empty schemaHint -> defaults to "public".
	priv2 := privacy.NewTurnPrivacySession()
	desPG, err := svc.DesensitizeStatementTemplate(context.Background(), priv2, dsRow, "", pgSQL)
	if err != nil || desPG.PrimarySchema != "public" || !strings.Contains(desPG.AnonymizedTemplate, "$1") {
		t.Fatalf("unexpected PG desensitization result: %+v err=%v", desPG, err)
	}
	priv2.ZeroAndRelease()

	// DiagnoseStatementWithAI error branches: unknown datasource, unknown statement, unavailable template.
	if _, err := svc.DiagnoseStatementWithAI(context.Background(), actor, uuid.NewString(), uuid.NewString(), time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected error for unknown datasource in DiagnoseStatementWithAI")
	}
	if _, err := svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, uuid.NewString(), time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected error for unknown statement in DiagnoseStatementWithAI")
	}

	_, _ = svc.UpdateConfig(context.Background(), actor, dsID, 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            200,
	})
	t0 := time.Now().UTC().Add(-5 * time.Minute)
	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "fp_pg_slow",
			DatabaseOrSchema:                  "app_schema",
			CallsTotal:                        25,
			TotalExecTimeMS:                   1250.0,
			RowsExaminedOrSharedBlksReadTotal: 5000,
			RowsSentOrRowsReturnedTotal:       25,
			LockTimeMSOrTempBlksWrittenTotal:  15.0,
		},
		{
			EngineFingerprint:                 "fp_no_tmpl",
			DatabaseOrSchema:                  "app_schema",
			CallsTotal:                        10,
			TotalExecTimeMS:                   500.0,
			RowsExaminedOrSharedBlksReadTotal: 1000,
			RowsSentOrRowsReturnedTotal:       10,
		},
	}
	_, _ = svc.CollectScheduledSnapshot(context.Background(), dsID, t0)

	var stmtNoTmpl persistence.SQLDiagnosticsStatementModel
	if err := db.Where("datasource_id = ? and engine_fingerprint = 'fp_no_tmpl'", dsID).First(&stmtNoTmpl).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, stmtNoTmpl.ID, time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected ErrTemplateUnavailable for fp_no_tmpl")
	}

	var stmtPG persistence.SQLDiagnosticsStatementModel
	if err := db.Where("datasource_id = ? and engine_fingerprint = 'fp_pg_slow'", dsID).First(&stmtPG).Error; err != nil {
		t.Fatal(err)
	}

	// Without an active AI provider, DiagnoseStatementWithAI must fail with CodeAIProviderFailed (2011).
	enCtx := reviewapp.WithOutputLocale(context.Background(), "en-US")
	if _, err := svc.DiagnoseStatementWithAI(enCtx, actor, dsID, stmtPG.ID, time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected DiagnoseStatementWithAI to fail without an AI provider")
	}

	// Test invokeLLMDiagnosis path with an HTTP test server via reviewapp.Services:
	// first return invalid "{}" (must fail with 2011), then return a valid 4-section JSON payload.
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	var returnValidLLM bool
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !returnValidLLM {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
			return
		}
		payload := map[string]any{
			"observed_facts": []map[string]any{
				{"metric": "Shared Blocks Read", "value": "100", "window": "window"},
			},
			"root_cause_hypotheses": []map[string]any{
				{"hypothesis": "Likely missing index on filter columns (inference).", "confidence": "medium", "evidence_basis": "calls=5, shared_blks_read=500"},
			},
			"optimization_recommendations": []map[string]any{
				{
					"kind":                  "index_ddl",
					"title":                 "Add index",
					"rationale":             "Reduce scan cost",
					"applicable_conditions": []string{"Verify selectivity"},
					"candidate_sql":         "CREATE INDEX idx_pg_orders_status ON app_schema.pg_orders (status)",
					"risk_notes":            []string{"Validate DDL lock"},
				},
			},
			"verification_steps": []string{"Run precheck before submission."},
		}
		content, _ := json.Marshal(payload)
		resp, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": string(content)}},
			},
		})
		_, _ = w.Write(resp)
	}))
	defer llmServer.Close()

	// Also exercise RestoreTextTokens with registered and unregistered tokens.
	privRestore := privacy.NewTurnPrivacySession()
	_, _ = privRestore.RegisterTable("public", "orders")
	restoredText := privRestore.RestoreTextTokens("Scan on table_1 and table_99")
	if !strings.Contains(restoredText, "orders") || !strings.Contains(restoredText, "table_99") {
		t.Fatalf("unexpected restored text: %q", restoredText)
	}
	privRestore.ZeroAndRelease()

	reviewSvc := &reviewapp.Services{DB: db, Crypter: crypter}
	provID := uuid.NewString()
	provEnv, err := crypter.Seal(crypto.PurposePayload, []byte("sk-test-key"), crypto.AAD{
		DeploymentID: crypter.DeploymentID(),
		Table:        "ai_providers",
		PrimaryKey:   provID,
		Column:       "secret_ciphertext",
		Purpose:      "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Exec(`delete from ai_providers`).Error
	if err := db.Exec(`
		insert into ai_providers (
			id, name, provider_kind, base_url, model_name, enabled,
			selection_priority, privacy_contract_hash, output_schema_hash, configuration,
			secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
			version, created_by_user_id, created_at, updated_at
		) values (?, ?, 'openai_compatible', ?, 'gpt-4o-mini', true,
			1, ?, ?, '{"temperature":0.1}'::jsonb,
			?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		provID, "diag-llm-"+provID[:8], llmServer.URL,
		reviewprovider.PrivacyContractHash, reviewprovider.OutputSchemaHash,
		provEnv.Ciphertext, provEnv.Nonce, provEnv.KeyID, userID).Error; err != nil {
		t.Fatalf("seed ai_provider failed: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from ai_providers where id = ?`, provID).Error
	})
	svc.Review = reviewSvc

	// Invalid LLM JSON "{}" must fail with CodeAIProviderFailed (2011) instead of fabricating a fallback report.
	if _, err := svc.DiagnoseStatementWithAI(enCtx, actor, dsID, stmtPG.ID, time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected DiagnoseStatementWithAI to reject empty LLM JSON payload")
	}

	// Also collect a reset snapshot so resetDetected == true ("reset_or_gap") is covered in DiagnoseStatementWithAI!
	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "fp_pg_slow",
			DatabaseOrSchema:                  "app_schema",
			CallsTotal:                        5,
			TotalExecTimeMS:                   250.0,
			RowsExaminedOrSharedBlksReadTotal: 500,
			RowsSentOrRowsReturnedTotal:       5,
			LockTimeMSOrTempBlksWrittenTotal:  2.0,
		},
	}
	_, _ = svc.CollectScheduledSnapshot(context.Background(), dsID, t0.Add(1*time.Minute))

	returnValidLLM = true
	repLLM, err := svc.DiagnoseStatementWithAI(enCtx, actor, dsID, stmtPG.ID, t0.Add(-1*time.Minute), t0.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("DiagnoseStatementWithAI with valid LLM provider failed: %v", err)
	}
	if repLLM.CoverageMode != "reset_or_gap" {
		t.Fatalf("expected reset_or_gap from DiagnoseStatementWithAI, got %+v", repLLM)
	}

	// Test PrecheckOptimizationDraft and SubmitOptimizationOrder validation/permission/state branches.
	if _, err := svc.PrecheckOptimizationDraft(context.Background(), actor, dsID, repLLM.ReportID, "", nil, "idem-1"); err == nil {
		t.Fatal("expected error for empty flowID")
	}
	if _, err := svc.PrecheckOptimizationDraft(context.Background(), actor, dsID, uuid.NewString(), flowID, nil, "idem-1"); err == nil {
		t.Fatal("expected error for unknown reportID")
	}
	ungrantedActor := domain.Actor{UserID: uuid.NewString(), Username: "outsider", Role: "developer"}
	if _, err := svc.PrecheckOptimizationDraft(context.Background(), ungrantedActor, dsID, repLLM.ReportID, flowID, nil, "idem-1"); err == nil {
		t.Fatal("expected 7001 ErrDatasourceForbidden for ungranted user")
	}

	// PrecheckOptimizationDraft with candidateSQLOverride and nil PrecheckRunner & nil ReviewAgent (falls back to generated UUID runID).
	svc.Review = nil
	svc.PrecheckRunner = nil
	overrideSQL := "UPDATE orders SET status = 'archived' WHERE id = 100"
	preRes, err := svc.PrecheckOptimizationDraft(context.Background(), actor, dsID, repLLM.ReportID, flowID, &overrideSQL, "idem-override")
	if err != nil || preRes["run_id"] == "" {
		t.Fatalf("expected fallback run_id when PrecheckRunner and Review are nil, got %+v err=%v", preRes, err)
	}
	runID := preRes["run_id"].(string)

	// SubmitOptimizationOrder validation and state errors.
	if _, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, repLLM.ReportID, flowID, runID, "Title", "", ""); err == nil {
		t.Fatal("expected error for missing idempotencyKey")
	}
	if _, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, repLLM.ReportID, "", runID, "Title", "", "idem-s"); err == nil {
		t.Fatal("expected error for empty flowID")
	}
	if _, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, uuid.NewString(), flowID, runID, "Title", "", "idem-s"); err == nil {
		t.Fatal("expected error for unknown reportID")
	}
	if _, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, repLLM.ReportID, flowID, "wrong-run-id", "Title", "", "idem-s"); err == nil {
		t.Fatal("expected error for mismatched ephemeralRunID")
	}
	if _, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, repLLM.ReportID, uuid.NewString(), runID, "Title", "", "idem-s"); err == nil {
		t.Fatal("expected error for mismatched flowID")
	}
	if _, err := svc.SubmitOptimizationOrder(context.Background(), ungrantedActor, dsID, repLLM.ReportID, flowID, runID, "Title", "", "idem-s"); err == nil {
		t.Fatal("expected 7001 error for ungranted actor in SubmitOptimizationOrder")
	}
	// When both OrderSubmitter and Orders are nil -> ErrInvalidState.
	svc.OrderSubmitter = nil
	svc.Orders = nil
	if _, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, repLLM.ReportID, flowID, runID, "Title", "", "idem-s"); err == nil {
		t.Fatal("expected error when OrderSubmitter and Orders are nil")
	}

	// Succeeds with OrderSubmitter (and tests DML relKind = "same_fingerprint" + idempotent repeat call on already submitted report).
	fakeOrderID := uuid.NewString()
	svc.OrderSubmitter = func(_ context.Context, _ domain.Actor, _, _, stmtKind, _, _, _, _, _ string) (string, string, string, error) {
		if stmtKind != "dml" {
			return "", "", "", fmt.Errorf("expected dml statement kind, got %s", stmtKind)
		}
		return fakeOrderID, "YR-20261108-000100", "stage_approval_active", nil
	}
	subRes1, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, repLLM.ReportID, flowID, runID, "Archive order 100", "remark", "idem-s1")
	if err != nil || subRes1["order_id"] != fakeOrderID {
		t.Fatalf("SubmitOptimizationOrder failed: %+v err=%v", subRes1, err)
	}
	// Calling SubmitOptimizationOrder again on an already-submitted report returns the existing order idempotently.
	subRes2, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, repLLM.ReportID, flowID, runID, "Archive order 100", "remark", "idem-s2")
	if err != nil || subRes2["order_id"] != fakeOrderID {
		t.Fatalf("expected idempotent response on already-submitted report, got %+v err=%v", subRes2, err)
	}
	// Calling PrecheckOptimizationDraft on an already-submitted report returns ErrInvalidState.
	if _, err := svc.PrecheckOptimizationDraft(context.Background(), actor, dsID, repLLM.ReportID, flowID, nil, "idem-again"); err == nil {
		t.Fatal("expected ErrInvalidState when prechecking an already-submitted report")
	}
}

func TestServices_AggregatorAndRealReviewOrdersBranches(t *testing.T) {
	schemas := extractReferencedSchemas("SELECT * FROM s1.t1 JOIN s1.t2 ON s1.t1.id = s1.t2.id JOIN `s2`.`t3` ON 1=1")
	if len(schemas) != 2 {
		t.Fatalf("expected 2 unique schemas, got %v", schemas)
	}
	tables := extractReferencedTables("SELECT * FROM dual JOIN s1.t1 ON 1=1 JOIN t1 ON 1=1 JOIN t2 ON 1=1 JOIN t3 ON 1=1", 2)
	if len(tables) != 2 {
		t.Fatalf("expected maxTables=2 tables, got %v", tables)
	}
	cols := extractFilterAndOrderColumns("SELECT * FROM t1 WHERE col_a = 1 AND col_a = 2 ORDER BY col_b DESC")
	if len(cols) == 0 {
		t.Fatalf("expected extracted filter and order columns")
	}

	db := openDiagnosticsTestDB(t)
	crypter := newTestCrypter(t)
	recorder, _ := auditlog.NewRecorder(db)
	userID, dsID, flowID := seedUserAndDatasource(t, db, true)
	actor := domain.Actor{UserID: userID, Username: "diag_agg_tester", Role: "developer"}

	fake := &fakeCollectorBackend{
		probeState: database.StatsProbeReady,
		templatesByFP: map[string]string{
			"fp_agg_1": "SELECT id, email FROM app_db.users WHERE status = ? ORDER BY created_at DESC",
		},
		columns: []database.MetadataColumn{
			{ColumnName: "id", DatabaseType: "bigint"},
			{ColumnName: "status", DatabaseType: "varchar(32)"},
			{ColumnName: "created_at", DatabaseType: "datetime"},
		},
	}
	svc, err := NewServices(db, crypter, recorder, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Collector = fake
	if svc.VerifyCandidateSQLHMAC(nil, "SELECT 1") {
		t.Fatal("expected false for nil report row")
	}
	if svc.VerifyCandidateSQLHMAC(&persistence.SQLDiagnosticsAIReportModel{}, "SELECT 1") {
		t.Fatal("expected false for empty HMAC")
	}

	if _, err := svc.UpdateConfig(context.Background(), actor, dsID, 0, domain.UpdateDiagnosticsConfigInput{
		Enabled:         true,
		IntervalSeconds: 60,
		TopN:            50,
	}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	fake.snapshotRows = []database.NativeStatementStatRow{
		{
			EngineFingerprint:                 "fp_agg_1",
			DatabaseOrSchema:                  "app_db",
			CallsTotal:                        15,
			TotalExecTimeMS:                   300.0,
			RowsExaminedOrSharedBlksReadTotal: 1500,
			RowsSentOrRowsReturnedTotal:       15,
			LockTimeMSOrTempBlksWrittenTotal:  3.0,
		},
	}
	if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, t0); err != nil {
		t.Fatal(err)
	}

	var stmt persistence.SQLDiagnosticsStatementModel
	if err := db.Where("datasource_id = ? and engine_fingerprint = 'fp_agg_1'", dsID).First(&stmt).Error; err != nil {
		t.Fatal(err)
	}

	// 1. GetStatementDetail when only 1 snapshot exists -> overallCoverage == "cumulative_only" (aggregator.go:L654-L660),
	// which also seals the SQL template onto stmt.
	detCum, err := svc.GetStatementDetail(context.Background(), dsID, stmt.ID, t0.Add(-5*time.Minute), t0.Add(5*time.Minute))
	if err != nil || detCum.CoverageMode != "cumulative_only" || len(detCum.TrendSeries) != 1 {
		t.Fatalf("expected cumulative_only detail with 1 trend point, got %+v err=%v", detCum, err)
	}

	// 2. GetOverview now sees the sealed template on stmt and opens it via openStatementTemplate (aggregator.go:L291-L294).
	ov, err := svc.GetOverview(context.Background(), dsID, OverviewQuery{
		From:   t0.Add(-5 * time.Minute),
		To:     t0.Add(5 * time.Minute),
		SortBy: "total_exec_time_ms",
		Limit:  25,
	})
	if err != nil || len(ov.Statements) != 1 || ov.Statements[0].TemplatePreview == nil {
		t.Fatalf("expected overview with opened template preview, got %+v err=%v", ov, err)
	}

	// 3. GetStatementDetail on an empty bucket window exercises the len(stmtBuckets) == 0 fallback (aggregator.go:L685-L694).
	detEmpty, err := svc.GetStatementDetail(context.Background(), dsID, stmt.ID, t0.Add(2*time.Hour), t0.Add(3*time.Hour))
	if err != nil || detEmpty.Summary.Calls != 15 {
		t.Fatalf("expected empty-window detail to fall back to LatestCallsTotal=15, got %+v err=%v", detEmpty, err)
	}

	// 4. Collect delta snapshots before and after an order whose terminal_at is NULL, then ConfirmOrderLink and GetStatementDetail.
	for i := 1; i <= 6; i++ {
		fake.snapshotRows[0].CallsTotal = int64(15 + i*10)
		fake.snapshotRows[0].TotalExecTimeMS = float64(300 + i*100)
		if _, err := svc.CollectScheduledSnapshot(context.Background(), dsID, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	orderNullTermID := uuid.NewString()
	orderSubAt := t0.Add(3*time.Minute + 30*time.Second)
	if err := db.Exec(`
		insert into change_orders (
			id, display_number, submitter_user_id_snapshot, submitter_username_snapshot, submitter_display_name_snapshot,
			title, description, state, sql_hmac, snapshot_hash, manually_verified, rollback_enabled,
			ticket_type, rollback_status, version, submitted_at, terminal_at
		) values (?, 'YR-20261108-999992', ?, 'diag_admin', 'Diag Admin',
			'Null terminal_at order', 'desc', 'submitted', repeat('a', 64), repeat('b', 64), false, false,
			'normal', 'not_requested', 1, ?, NULL)`,
		orderNullTermID, userID, orderSubAt).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from sql_diagnostics_order_links where change_order_id = ?`, orderNullTermID).Error
		_ = db.Exec(`delete from change_orders where id = ?`, orderNullTermID).Error
	})
	if _, err := svc.ConfirmOrderLink(context.Background(), actor, dsID, stmt.ID, orderNullTermID, "index_or_schema_change", nil); err != nil {
		t.Fatalf("ConfirmOrderLink with NULL terminal_at failed: %v", err)
	}
	detWithComp, err := svc.GetStatementDetail(context.Background(), dsID, stmt.ID, t0.Add(-1*time.Minute), t0.Add(10*time.Minute))
	if err != nil || detWithComp.OrderComparison.BeforeWindow == nil || detWithComp.OrderComparison.BeforeWindow.Calls == 0 {
		t.Fatalf("expected non-zero BeforeWindow calls from computeWindowMetrics, got %+v err=%v", detWithComp, err)
	}

	// 5. Exercise PrecheckOptimizationDraft and SubmitOptimizationOrder with real Review and Orders services wired.
	svc.Review = wireMockDiagLLMProvider(t, db, crypter, userID, "CREATE INDEX idx_users_status ON users (status)")
	rep, err := svc.DiagnoseStatementWithAI(context.Background(), actor, dsID, stmt.ID, t0.Add(-1*time.Minute), t0.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	reviewSvc := &reviewapp.Services{
		DB:        db,
		Crypter:   crypter,
		Ephemeral: reviewapp.NewEphemeralStore(5 * time.Minute),
	}
	svc.PrecheckRunner = nil
	svc.Review = reviewSvc
	svc.ReviewAgent = &reviewapp.AgentRunner{}
	// Calling PrecheckOptimizationDraft with a very large SQL (>32MB) or invalid SQL triggers StartEphemeralRun error mapping (L722-L735).
	badSQL := "SELECT 'unterminated"
	if _, err := svc.PrecheckOptimizationDraft(context.Background(), actor, dsID, rep.ReportID, flowID, &badSQL, "idem-real-review-err"); err == nil {
		t.Fatal("expected error from StartEphemeralRun on malformed SQL")
	}

	// Now precheck with fallback UUID (ReviewAgent = nil) so report transitions to "prechecked",
	// clear stmt.DatabaseOrSchema to exercise L841-L846, and call SubmitOptimizationOrder with real Orders.SubmitOrderV2 (L852-L880).
	svc.ReviewAgent = nil
	ddlSQL := "CREATE INDEX idx_users_status ON users (status)"
	preOK, err := svc.PrecheckOptimizationDraft(context.Background(), actor, dsID, rep.ReportID, flowID, &ddlSQL, "idem-pre-ok")
	if err != nil {
		t.Fatal(err)
	}
	preRunID := preOK["run_id"].(string)

	_ = db.Exec(`update sql_diagnostics_statements set database_or_schema = '' where id = ?`, stmt.ID).Error
	svc.OrderSubmitter = nil
	svc.Orders = &orderapp.Services{DB: db, Crypter: crypter}
	// SubmitOrderV2 will return an *orderdom.Error (because preRunID is not in reviewSvc.Ephemeral), covering L852-L875!
	if _, err := svc.SubmitOptimizationOrder(context.Background(), actor, dsID, rep.ReportID, flowID, preRunID, "Add status index", "remark text", "idem-real-order-err"); err == nil {
		t.Fatal("expected orderdom.Error mapped to domain.Error when ephemeral run does not exist")
	}
}
