// Command yearning is the v4 backend composition root.
//
// Subcommands:
//
//	yearning serve              Production mode: connect to PG and Key Provider, expose probes
//	yearning mock               Contract Mock Server: scenario-driven, no external dependencies
//	yearning version            Print build version and commit (injected via -ldflags in release builds)
//
// Configuration precedence: defaults < YAML file (--config) < YEARNING_* environment variables.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/config"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/httpapi"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/migration/legacy"
	"github.com/cookieY/YearningX/backend/internal/platform/buildinfo"
	"github.com/cookieY/YearningX/backend/internal/platform/database"
	"github.com/cookieY/YearningX/backend/internal/platform/httpserver"
	"github.com/cookieY/YearningX/backend/internal/platform/keyprovider"
	"github.com/cookieY/YearningX/backend/internal/platform/logging"
	"github.com/cookieY/YearningX/backend/internal/realtime"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/workflow"
)

const defaultOpenAPIPath = "api/openapi/yearning-v4.yaml"
const defaultScenariosPath = "testdata/development/mock-scenarios.json"

// defaultMigrationModeListen is the default listen address for the migration review surface: loopback only (SEC-0043).
// In-cluster exposure requires explicit --listen and source restriction via NetworkPolicy.
const defaultMigrationModeListen = "127.0.0.1:9100"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "yearning: %v\n", err)
		var codeErr *exitCodeError
		if errors.As(err, &codeErr) {
			os.Exit(codeErr.Code)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if promptForReset(args) {
		return runResetAdminPassword(context.Background(), flagArgsAfterReset(args))
	}
	if len(args) == 0 {
		return errors.New("missing subcommand: usage: yearning serve|mock|migrate|reset-admin-password|version")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "migrate":
		return runMigrate(ctx, args[1:])
	case "serve":
		return runServe(ctx, args[1:])
	case "mock":
		return runMock(ctx, args[1:])
	case "reset-admin-password":
		return runResetAdminPassword(ctx, args[1:])
	case "version":
		// Build facts come solely from buildinfo injection (release build -ldflags); never guess release status.
		s := buildinfo.Summary()
		fmt.Printf("yearning %s (commit %s, %s/%s, %s)\n",
			s["version"], s["commit"], runtime.GOOS, runtime.GOARCH, s["go_version"])
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q: usage: yearning serve|mock|migrate|reset-admin-password|version", args[0])
	}
}

func runServe(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", "", "Path to YAML configuration file (optional)")
	listen := flags.String("listen", "", "Override listen address (same precedence as env var)")
	migrationMode := flags.Bool("migration-mode", false, "Start in migration review mode (requires migration JSON config)")
	bootstrapAdminPasswordFile := flags.String("bootstrap-admin-password-file", "", "Path to bootstrap admin password file when users table is empty")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *migrationMode {
		if *configPath == "" {
			return errors.New("--config is required in --migration-mode (migration config JSON)")
		}
		// Contract: migration-mode uses migration-config (workbench credentials),
		// exposing only login/logout/current-user and /admin/migrations*.
		mcfg, err := legacy.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		migrationListen := *listen
		if migrationListen == "" {
			migrationListen = defaultMigrationModeListen
		}
		return runServeMigrationMode(ctx, mcfg, *bootstrapAdminPasswordFile, migrationListen)
	}
	cfg, err := config.Load(ctx, config.ModeServe, *configPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Server.ListenAddress = *listen
	}
	// SEC-0018: Must re-validate after --listen override; non-loopback listen requires explicit
	// environment (preventing default development mode from silently exposing production surfaces).
	if lerr := config.RequireExplicitEnvironmentForListen(cfg); lerr != nil {
		return lerr
	}
	applyInstanceDefaults(&cfg)
	logger := logging.New(cfg.Logging, cfg.Observability)
	// #3 (effective 2026-11-01 fix): zap.L() inside workers/goroutines (structured logs for
	// review policy compile failure, provider failure, transient Run terminal state, etc.) was previously a no-op —
	// serve never replaced the global logger. See RCP-20261101 diagnostic record.
	zap.ReplaceGlobals(logger)
	defer func() { _ = logger.Sync() }()

	// Do not exit when PG is unavailable: Readiness exposes the fault while the process stays alive for K8s retries.
	pool, err := database.Connect(ctx, cfg.Database)
	if err != nil {
		logger.Error("metadata database init failed", zap.Error(err))
		return err
	}
	defer func() {
		if err := pool.Close(); err != nil {
			logger.Warn("database close failed", zap.Error(err))
		}
	}()

	keyring, err := keyprovider.NewFileProvider(cfg.KeyProvider.Path)
	if err != nil {
		return err
	}

	// B3: Parse keyring into crypter and wire identity/flow/datasource services.
	ring, err := crypto.LoadKeyring(cfg.KeyProvider.Path)
	if err != nil {
		logger.Error("keyring load failed", zap.Error(err))
		return err
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		return err
	}
	gormDB := pool.Gorm()
	recorder, err := auditlog.NewRecorder(gormDB)
	if err != nil {
		return err
	}
	hub := realtime.NewHub()
	recorder.SetRealtimeHub(hub)
	go realtime.StartPGListener(ctx, cfg.Database.DSN(), hub, logger)
	sessionStore, err := identity.NewSessionStore(gormDB, identity.DefaultSessionTTL)
	if err != nil {
		return err
	}
	identitySvc, err := identity.NewService(gormDB, sessionStore, crypter, recorder)
	if err != nil {
		return err
	}
	workflowSvc, err := workflow.NewService(gormDB, crypter, recorder)
	if err != nil {
		return err
	}
	dsFactory, err := datasource.NewFactory(crypter)
	if err != nil {
		return err
	}
	datasourceSvc, err := datasource.NewService(gormDB, dsFactory, crypter, recorder)
	if err != nil {
		return err
	}
	// Bootstrap built-in admin: only when users table is empty and a password file is provided (initial install).
	if *bootstrapAdminPasswordFile != "" {
		pw, readErr := os.ReadFile(*bootstrapAdminPasswordFile)
		if readErr != nil {
			return readErr
		}
		if err := identitySvc.BootstrapAdmin(ctx, strings.TrimSpace(string(pw))); err != nil {
			logger.Warn("builtin admin bootstrap skipped", zap.Error(err))
		} else {
			logger.Info("builtin admin bootstrapped")
		}
	}

	checks := []httpapi.ReadinessCheck{
		{Name: "metadata_postgresql", Check: pool.Ping},
		{Name: "key_provider", Check: keyring.Health},
		// SEC-0020: Complete the four contract readiness checks — schema ledger (exists,
		// latest version applied successfully, no failed rows) and database role self-check (current role
		// is not superuser, has no createdb, and is not postgres/schema_owner/migrator).
		{Name: "schema_ledger", Check: func(ctx context.Context) error {
			return schemaLedgerHealthy(ctx, gormDB)
		}},
	}
	// SEC-0020/NEW-DEPLOY-02: exact_schema_version (second item of kubernetes contract
	// readiness) — highest successfully applied ledger version must equal the expected version from
	// the image migrations directory, preventing new Pods from becoming Ready when the schema Job was
	// skipped or migrations stopped at an older version.
	// Expected version is computed once at startup (directory is immutable image content;
	// overridden by YEARNING_SCHEMA_MIGRATIONS_DIR, defaulting to Dockerfile COPY path
	// /usr/share/yearning/db-migrations). Tiering: in production, a missing/empty/unparseable
	// directory mounts the check and always fails (fail-closed not ready); in development,
	// an unavailable directory skips the check and logs a warning (consistent with mounting
	// database_role_self_check only in production).
	schemaExp := computeSchemaExpectation()
	if cfg.Environment != "production" && schemaExp.err != nil {
		logger.Warn("schema migrations dir unavailable; exact_schema_version readiness check disabled (development)",
			zap.String("dir", schemaExp.dir),
			zap.Error(schemaExp.err),
		)
	} else {
		checks = append(checks, httpapi.ReadinessCheck{
			Name: "exact_schema_version",
			Check: func(ctx context.Context) error {
				return checkSchemaVersionExact(ctx, gormSchemaLedger{db: gormDB}, schemaExp)
			},
		})
	}
	// Environment tiering (scope of SEC-0020 role separation): database_role_self_check is mounted
	// only in production. schema_ledger and key_provider remain unconditional — dev/f12 stack metadata
	// databases are also built by the migration ledger and require a working keyring; role self-check
	// asserts three-role separation in production deployments (app is non-superuser). The dev stack
	// metadata DB runs as superuser yearning (single-container form in docker/development/compose.yaml),
	// so unconditional mounting would leave dev/f12 permanently not ready; production role separation
	// is guaranteed by the deployment surface and verified here.
	if cfg.Environment == "production" {
		checks = append(checks, httpapi.ReadinessCheck{
			Name: "database_role_self_check",
			Check: func(ctx context.Context) error {
				return databaseRoleSelfCheck(ctx, gormDB)
			},
		})
	}
	readiness := httpapi.NewReadiness(logger, checks...)

	// SEC-0040: Periodic retention cleanup task (7d raw evidence / 24h idempotency / 90d audit;
	// runs on the same tick cadence as scheduler, logging failures without draining traffic or exiting).
	// NEW-QUERY-02: The retention execution role is yearning_retention — the application role lacks
	// DELETE privileges on managed tables, causing permission denied on the first round when using the
	// serve app connection (root cause of silent cleanup failure). When a retention DSN is provided,
	// construct the service with a dedicated connection pool (dialed via addrguard); missing DSN in
	// production already fails closed during config validation, so falling back to the primary connection
	// can only occur in development (single-role local dev metadata DB).
	retentionDB := gormDB
	if cfg.Retention.DatabaseDSN != "" {
		rdb, rclose, ropenErr := openRetentionPool(cfg.Retention.DatabaseDSN)
		if ropenErr != nil {
			return ropenErr
		}
		defer func() {
			if err := rclose(); err != nil {
				logger.Warn("retention database close failed", zap.Error(err))
			}
		}()
		retentionDB = rdb
		logger.Info("retention running on dedicated yearning_retention connection")
	} else {
		logger.Warn("retention database_dsn empty; retention falls back to app connection (development only)")
	}
	retention, rerr := reviewapp.NewRetentionService(retentionDB)
	if rerr != nil {
		return rerr
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// NEW-QUERY-02: RunOnce tolerates per-table errors — a single table failure does not block
				// remaining tables, and aggregated errors are logged once at Warn (remaining tables are still cleaned on partial success).
				if counts, err := retention.RunOnce(ctx); err != nil {
					logger.Warn("retention sweep failed",
						zap.Any("deleted", counts),
						zap.Error(err),
					)
				} else if len(counts) > 0 {
					logger.Info("retention sweep done", zap.Any("deleted", counts))
				}
			}
		}
	}()

	router := httpapi.NewServeRouter(httpapi.ServeDeps{
		Logger:       logger,
		Readiness:    readiness,
		SessionStore: sessionStore,
		SessionOption: session.Options{
			CookieName:       cfg.Session.CookieName,
			Secure:           cfg.Session.Secure,
			AnonymousAllowed: true,
		},
		// ADR-0001 modular monolith: start stage_execution worker, scheduler,
		// and notification worker in-process within serve (BLK-20261017 ruling A).
		StartBackgroundLoops: true,
		// RCP-20261020: release snapshot source (config release_feed.upstream_url).
		ReleaseFeedURL: cfg.ReleaseFeed.UpstreamURL,
		// NEW-AUTH-01: trusted proxy CIDRs (empty/nil = trust no proxies, direct-connection semantics).
		TrustedProxies: cfg.TrustedProxies,
		RealtimeHub:    hub,
		B3: &httpapi.B3Deps{
			Identity:     identitySvc,
			Sessions:     sessionStore,
			Workflow:     workflowSvc,
			Datasource:   datasourceSvc,
			Recorder:     recorder,
			DB:           gormDB,
			Crypter:      crypter,
			SecureCookie: cfg.Session.Secure,
		},
	})

	logger.Info("serve starting",
		zap.String("listen", cfg.Server.ListenAddress),
		zap.String("environment", cfg.Environment),
	)
	server := httpserver.New(router, cfg.Server)
	err = server.Run(ctx)
	logger.Info("serve stopped")
	return err
}

func runMock(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("mock", flag.ContinueOnError)
	configPath := flags.String("config", "", "Path to YAML configuration file (optional)")
	openapiPath := flags.String("openapi", defaultOpenAPIPath, "Path to OpenAPI contract file")
	scenariosPath := flags.String("scenarios", defaultScenariosPath, "Path to mock scenarios file")
	listen := flags.String("listen", "", "Override listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(ctx, config.ModeMock, *configPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Server.ListenAddress = *listen
	}
	applyInstanceDefaults(&cfg)
	logger := logging.New(cfg.Logging, cfg.Observability)
	defer func() { _ = logger.Sync() }()

	engine, err := httpapi.NewMockEngine(*openapiPath, *scenariosPath, logger)
	if err != nil {
		return err
	}
	readiness := httpapi.NewReadiness(logger)
	router := httpapi.NewMockRouter(engine, logger, readiness)

	logger.Info("mock server starting",
		zap.String("listen", cfg.Server.ListenAddress),
		zap.String("openapi", *openapiPath),
		zap.String("scenarios", *scenariosPath),
	)
	server := httpserver.New(router, cfg.Server)
	err = server.Run(ctx)
	logger.Info("mock server stopped")
	return err
}

// applyInstanceDefaults populates observability field defaults: instance_id uses hostname+PID,
// deployment_id defaults to local. Introduces no global mutable state.
func applyInstanceDefaults(cfg *config.Config) {
	// SEC-0018: An undeclared environment is materialized as development after passing
	// the listen check (only loopback listen can reach here; non-loopback was rejected earlier).
	if cfg.Environment == "" {
		cfg.Environment = "development"
	}
	if cfg.Observability.InstanceID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown-host"
		}
		cfg.Observability.InstanceID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}
	if cfg.Observability.DeploymentID == "" {
		cfg.Observability.DeploymentID = "local"
	}
}

// schemaLedgerHealthy SEC-0020: schema_migrations ledger health — table exists,
// at least one migration succeeded, and no failed rows exist (app Pods never auto-migrate; abnormal ledger = unserviceable).
func schemaLedgerHealthy(ctx context.Context, db *gorm.DB) error {
	var failed, applied int
	if err := db.WithContext(ctx).Raw(
		`select count(*) filter (where not success) from schema_migrations`).Scan(&failed).Error; err != nil {
		return fmt.Errorf("schema ledger unreadable: %w", err)
	}
	if failed > 0 {
		return fmt.Errorf("schema ledger has %d failed migration rows", failed)
	}
	if err := db.WithContext(ctx).Raw(
		`select count(*) from schema_migrations where success`).Scan(&applied).Error; err != nil {
		return fmt.Errorf("schema ledger unreadable: %w", err)
	}
	if applied == 0 {
		return fmt.Errorf("schema ledger empty: run schema migration job first")
	}
	return nil
}

// databaseRoleSelfCheck SEC-0020: metadata database role self-check — application connection must not
// be a superuser/createdb role and must not reuse postgres/schema_owner/migrator identities
// (database-roles.json role separation; read-only self-check without probing write privileges).
func databaseRoleSelfCheck(ctx context.Context, db *gorm.DB) error {
	var role struct {
		Name     string `gorm:"column:current_user"`
		Super    bool   `gorm:"column:rolsuper"`
		CreateDB bool   `gorm:"column:rolcreatedb"`
	}
	if err := db.WithContext(ctx).Raw(`
		select current_user as current_user, rolsuper, rolcreatedb
		from pg_roles where rolname = current_user`).Scan(&role).Error; err != nil {
		return fmt.Errorf("database role self check unreadable: %w", err)
	}
	if role.Name == "" {
		return fmt.Errorf("database role self check: role row missing")
	}
	for _, forbidden := range []string{"postgres", "yearning_schema_owner", "yearning_migrator"} {
		if role.Name == forbidden {
			return fmt.Errorf("app connection must not use role %s", forbidden)
		}
	}
	if role.Super {
		return fmt.Errorf("app connection must not be superuser")
	}
	if role.CreateDB {
		return fmt.Errorf("app connection must not have createdb")
	}
	return nil
}
