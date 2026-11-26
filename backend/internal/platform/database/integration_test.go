//go:build integration

// Integration tests require a live PostgreSQL instance configured via environment variables:
//
//	YEARNING_TEST_DATABASE_HOST     default: localhost
//	YEARNING_TEST_DATABASE_PORT     default: 54330 (ephemeral dev container)
//	YEARNING_TEST_DATABASE_USER     default: yearning
//	YEARNING_TEST_DATABASE_PASSWORD required; test is skipped when unset
//	YEARNING_TEST_DATABASE_DBNAME   default: yearning_v4
//	YEARNING_TEST_DATABASE_SSLMODE  default: disable (local only)
package database

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/config"
	"github.com/cookieY/YearningX/backend/internal/httpapi"
	"github.com/cookieY/YearningX/backend/internal/platform/keyprovider"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
)

func newListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func testDatabaseConfig(t *testing.T) config.DatabaseConfig {
	t.Helper()
	cfg := testenv.RequireMetaPG(t)
	return config.DatabaseConfig{
		Host:         cfg.Host,
		Port:         strconv.Itoa(cfg.Port),
		User:         cfg.User,
		Password:     cfg.Password,
		DBName:       cfg.DBName,
		SSLMode:      cfg.SSLMode,
		MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeMinutes: 5,
	}
}

func TestIntegrationPoolPingAndTransaction(t *testing.T) {
	cfg := testDatabaseConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	var one int
	if err := pool.Gorm().Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("select probe: %v", err)
	}
	if one != 1 {
		t.Fatalf("probe returned %d", one)
	}

	manager := NewTxManager(pool)
	if err := manager.WithinTx(ctx, func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("empty transaction must commit: %v", err)
	}
	sentinel := errors.New("rollback me")
	if err := manager.WithinTx(ctx, func(ctx context.Context) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("error must propagate through rollback: %v", err)
	}
}

// Acceptance Gate 4 (integration part): Readiness must fail (503) when PostgreSQL is unreachable,
// disclosing only the check name externally without leaking connection error details.
func TestIntegrationReadinessFailsWhenPGUnreachable(t *testing.T) {
	deadCfg := config.DatabaseConfig{
		Host: "127.0.0.1", Port: "1", User: "u", Password: "p",
		DBName: "d", SSLMode: "disable",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeMinutes: 1,
	}
	pool, err := Connect(context.Background(), deadCfg)
	if err != nil {
		t.Fatalf("lazy connect must not fail on unreachable host: %v", err)
	}
	defer pool.Close()

	readiness := httpapi.NewReadiness(zap.NewNop(),
		httpapi.ReadinessCheck{Name: "metadata_postgresql", Check: pool.Ping},
	)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v4/health/ready", readiness.HandleReady)

	w := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v4/health/ready", nil).WithContext(ctx)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness against unreachable PG = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "metadata_postgresql") {
		t.Fatalf("failing check name must be disclosed: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "127.0.0.1") {
		t.Fatal("host/port details must not leak into readiness response")
	}
}

func TestIntegrationReadinessFailsWhenKeyringMissing(t *testing.T) {
	cfg := testDatabaseConfig(t)
	pool, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	keyring, err := keyprovider.NewFileProvider(filepath.Join(t.TempDir(), "absent-keyring"))
	if err != nil {
		t.Fatal(err)
	}
	readiness := httpapi.NewReadiness(zap.NewNop(),
		httpapi.ReadinessCheck{Name: "metadata_postgresql", Check: pool.Ping},
		httpapi.ReadinessCheck{Name: "key_provider", Check: keyring.Health},
	)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v4/health/ready", readiness.HandleReady)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v4/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing keyring must fail readiness: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "key_provider") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestIntegrationServeSmoke(t *testing.T) {
	cfg := testDatabaseConfig(t)
	pool, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// key_provider Readiness now runs full keyring semantic validation (SEC-0017/0020):
	// file mode <= 0400, valid JSON, and an active encrypt_decrypt current key for the payload purpose.
	material := make([]byte, 32)
	for i := range material {
		material[i] = byte(i + 1)
	}
	keyringDoc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"readiness-itest","keys":[{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		base64.StdEncoding.EncodeToString(material))
	keyringPath := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(keyringPath, []byte(keyringDoc), 0o400); err != nil {
		t.Fatal(err)
	}
	keyring, err := keyprovider.NewFileProvider(keyringPath)
	if err != nil {
		t.Fatal(err)
	}

	logger := zap.NewNop()
	readiness := httpapi.NewReadiness(logger,
		httpapi.ReadinessCheck{Name: "metadata_postgresql", Check: pool.Ping},
		httpapi.ReadinessCheck{Name: "key_provider", Check: keyring.Health},
	)
	router := httpapi.NewServeRouter(httpapi.ServeDeps{Logger: logger, Readiness: readiness})

	srv := &http.Server{Addr: "127.0.0.1:0", Handler: router}
	listener, err := newListener()
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-serveErr
	}()

	base := fmt.Sprintf("http://%s", listener.Addr().String())
	check := func(path string, want int) {
		t.Helper()
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s = %d, want %d", path, resp.StatusCode, want)
		}
		if resp.Header.Get("X-Request-ID") == "" {
			t.Fatalf("%s must return X-Request-ID", path)
		}
	}
	check("/api/v4/health/live", 200)
	check("/api/v4/health/ready", 200)
	check("/api/v4/version", 200)
	check("/api/v4/unknown", 404)
}
