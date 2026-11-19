package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/config"
	"github.com/cookieY/YearningX/backend/internal/migration"
	"github.com/cookieY/YearningX/backend/internal/migration/legacy"
	"github.com/cookieY/YearningX/backend/internal/migration/review"
	"github.com/cookieY/YearningX/backend/internal/migration/schema"
	"github.com/cookieY/YearningX/backend/internal/platform/httpserver"
)

// Exit code contract: api/migration/migration-cli.json exit_codes.
const (
	exitSuccess        = 0
	exitUsage          = 2
	exitSourcePrecond  = 3
	exitTargetMismatch = 4
	exitApproval       = 5
	exitMappingFailed  = 6
	exitDependency     = 10
	exitInternal       = 70
)

// runMigrate handles `Yearning migrate ...`. Dispatch rules:
//   - status/preflight/plan: Schema migrator;
//   - inspect/dry-run: Legacy (requires reading MySQL source);
//   - apply/verify: Legacy when --run-id is provided, Schema verify otherwise.
func runMigrate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("migrate requires a subcommand")
	}
	verb := args[0]
	if verb == "convert-config" {
		// convert-config uses its own FlagSet (--source-config/--output)
		// and must be dispatched before shared flag parsing so unknown flags do not error out.
		return runConvertConfig(args[1:])
	}
	fs := flag.NewFlagSet("migrate "+verb, flag.ContinueOnError)
	configPath := fs.String("config", "", "migration config json (absolute path)")
	reportDir := fs.String("report-dir", "", "report output directory (absolute path)")
	runIDFlag := fs.String("run-id", "", "migration run uuid for legacy apply/verify")
	migrationsDir := fs.String("migrations-dir", "../db/migrations", "schema migrations directory")
	catalogPath := fs.String("catalog", "../api/contracts/postgresql-column-catalog.json", "frozen column catalog contract")
	sourceSnapshot := fs.String("source-snapshot", "", "test facility: read legacy rows from snapshot dir instead of MySQL")
	evidencePath := fs.String("evidence", "../api/migration/legacy-current-status-evidence.json", "legacy status evidence contract path")
	fieldMappingPath := fs.String("field-mapping", "../api/migration/legacy-field-mapping.json", "legacy field mapping contract path")
	if err := fs.Parse(args[1:]); err != nil {
		return exitError(exitUsage, err.Error())
	}
	switch verb {
	case "status", "preflight", "plan", "apply", "verify", "inspect", "dry-run":
	default:
		return usageError("unknown migrate subcommand: " + verb)
	}
	if *configPath == "" {
		return usageError("--config is required")
	}
	abs, err := filepath.Abs(*configPath)
	if err != nil {
		return exitError(exitUsage, "resolve config path: "+err.Error())
	}
	cfg, err := legacy.LoadConfig(abs)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	if verb == "status" || verb == "preflight" || verb == "plan" || (verb == "apply" && *runIDFlag == "") || (verb == "verify" && *runIDFlag == "") {
		return runSchemaVerb(ctx, verb, cfg, *migrationsDir, *catalogPath)
	}
	return runLegacyVerb(ctx, verb, cfg, *reportDir, *runIDFlag, *sourceSnapshot, *evidencePath, *fieldMappingPath)
}

// runSchemaVerb executes Schema migrator commands.
func runSchemaVerb(ctx context.Context, verb string, cfg *legacy.Config, migrationsDir, catalogPath string) error {
	files, err := schema.LoadDir(migrationsDir)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	dsn, err := targetDSNOf(cfg)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	pool, err := migration.OpenTarget(ctx, dsn)
	if err != nil {
		return exitError(exitDependency, err.Error())
	}
	defer pool.Close()
	m := &schema.Migrator{Pool: pool}
	var report any
	switch verb {
	case "status":
		report, err = m.Status(ctx, files)
	case "preflight":
		report, err = m.Preflight(ctx, files)
	case "plan":
		report, err = m.Plan(ctx, files)
	case "apply":
		report, err = m.Apply(ctx, files)
		if err != nil {
			emitJSON(report)
			return exitError(exitTargetMismatch, err.Error())
		}
	case "verify":
		contract, loadErr := schema.LoadCatalogContract(catalogPath)
		if loadErr != nil {
			return exitError(exitUsage, loadErr.Error())
		}
		report, err = m.Verify(ctx, contract)
		if err == nil {
			if v, ok := report.(*schema.VerifyReport); ok && !v.Passed {
				emitJSON(v)
				return exitError(exitTargetMismatch, "schema verify failed")
			}
		}
	}
	if err != nil {
		return exitError(exitInternal, err.Error())
	}
	emitJSON(report)
	return nil
}

// runLegacyVerb executes Legacy migration commands.
func runLegacyVerb(ctx context.Context, verb string, cfg *legacy.Config, reportDir, runIDFlag, sourceSnapshot, evidencePath, fieldMappingPath string) error {
	if reportDir == "" {
		return usageError("--report-dir is required for " + verb)
	}
	if (verb == "apply" || verb == "verify") && runIDFlag == "" {
		return usageError("--run-id is required for legacy " + verb)
	}
	var src legacy.Source
	if sourceSnapshot != "" {
		snap, err := legacy.OpenSnapshot(sourceSnapshot)
		if err != nil {
			return exitError(exitUsage, err.Error())
		}
		src = snap
	} else {
		mysql, err := legacy.OpenMySQL(cfg.Source)
		if err != nil {
			return exitError(exitDependency, err.Error())
		}
		defer mysql.Close()
		src = mysql
	}
	if err := src.Ping(ctx); err != nil {
		return exitError(exitDependency, "source unreachable: "+err.Error())
	}
	dsn, err := targetDSNOf(cfg)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	pool, err := migration.OpenTarget(ctx, dsn)
	if err != nil {
		return exitError(exitDependency, err.Error())
	}
	defer pool.Close()
	key, err := cfg.Encryption.Key.Resolve()
	if err != nil {
		return exitError(exitUsage, "resolve encryption key: "+err.Error())
	}
	var legacySecretKey []byte
	if cfg.Source.LegacySecretKey != nil {
		legacySecretKey, err = cfg.Source.LegacySecretKey.Resolve()
		if err != nil {
			return exitError(exitUsage, "resolve legacy secret key: "+err.Error())
		}
	}
	crypter, err := legacy.NewCrypter(key, cfg.Encryption.KeyID, cfg.Encryption.DeploymentID, legacySecretKey)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		return exitError(exitDependency, err.Error())
	}
	evidence, err := legacy.LoadStatusEvidence(evidencePath)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	runner := &legacy.Runner{
		Src: src, Pool: pool, DB: db, Cfg: cfg, Crypter: crypter,
		Evidence:         evidence,
		FieldMappingPath: fieldMappingPath,
		MigratorVersion:  "yearning-v4-b2",
	}
	var result any
	switch verb {
	case "inspect":
		result, err = runner.Inspect(ctx)
	case "dry-run":
		runID := uuid.NewString()
		result, err = runner.DryRun(ctx, uuid.MustParse(runID))
		if result != nil {
			if m, ok := result.(*legacy.Manifest); ok {
				_ = writeReport(reportDir, "dry-run-manifest.json", m)
			}
		}
	case "apply":
		var pre *legacy.ApplyPreconditionError
		result, err = runner.Apply(ctx, uuid.MustParse(runIDFlag))
		if err != nil {
			if e, ok := err.(*legacy.ApplyPreconditionError); ok {
				pre = e
			}
			if result != nil {
				if m, ok := result.(*legacy.Manifest); ok {
					_ = writeReport(reportDir, "apply-manifest.json", m)
				}
			}
			emitJSON(ginH{"run_id": runIDFlag, "error": err.Error()})
			if pre != nil {
				return exitError(pre.Code, pre.Message)
			}
			return exitError(exitMappingFailed, err.Error())
		}
		if m, ok := result.(*legacy.Manifest); ok {
			_ = writeReport(reportDir, "apply-manifest.json", m)
		}
	case "verify":
		result, err = runner.Verify(ctx, uuid.MustParse(runIDFlag))
		if err != nil {
			if e, ok := err.(*legacy.ApplyPreconditionError); ok {
				return exitError(e.Code, e.Message)
			}
			return exitError(exitInternal, err.Error())
		}
		if v, ok := result.(*legacy.VerifyReport); ok && !v.AllPassed {
			emitJSON(v)
			return exitError(exitMappingFailed, "verify invariants failed")
		}
	}
	if err != nil {
		var pre *legacy.ApplyPreconditionError
		if e, ok := err.(*legacy.ApplyPreconditionError); ok {
			pre = e
		}
		emitJSON(ginH{"error": err.Error()})
		if pre != nil {
			return exitError(pre.Code, pre.Message)
		}
		return exitError(exitMappingFailed, err.Error())
	}
	emitJSON(result)
	return nil
}

// runServeMigrationMode starts migration review mode.
// SEC-0043: Listen address must be explicitly specified (defaults to loopback only — migration window
// review surface is not exposed outside the Pod network; K8s manifests use --listen 0.0.0.0:9100 with NetworkPolicy ingress rules).
func runServeMigrationMode(ctx context.Context, cfg *legacy.Config, bootstrapPasswordFile, listenAddr string) error {
	dsn, err := targetDSNOf(cfg)
	if err != nil {
		return exitError(exitUsage, err.Error())
	}
	pool, err := migration.OpenTarget(ctx, dsn)
	if err != nil {
		return exitError(exitDependency, err.Error())
	}
	defer pool.Close()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		return exitError(exitDependency, err.Error())
	}
	if bootstrapPasswordFile != "" {
		if err := bootstrapBuiltinAdmin(ctx, pool, bootstrapPasswordFile); err != nil {
			return exitError(exitUsage, "bootstrap admin: "+err.Error())
		}
	}
	logger, _ := zap.NewProduction()
	srv := &review.Server{Pool: pool, DB: db, Logger: logger}
	router := review.NewRouter(srv)
	server := httpserver.New(router, config.ServerConfig{
		ListenAddress:        listenAddr,
		ShutdownGraceSeconds: 15,
		ReadHeaderTimeoutSec: 10,
		ReadTimeoutSeconds:   60,
		WriteTimeoutSeconds:  120,
		IdleTimeoutSeconds:   120,
	})
	logger.Info("migration review server starting", zap.String("listen", listenAddr))
	return server.Run(ctx)
}

// bootstrapBuiltinAdmin creates the single built-in admin from a password file when users is empty.
// Rejects duplicate bootstrap if admin already exists; password never enters logs or CLI arguments.
func bootstrapBuiltinAdmin(ctx context.Context, pool *pgxpool.Pool, file string) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read password file: %w", err)
	}
	password := strings.TrimSpace(string(raw))
	if len(password) < 12 {
		return fmt.Errorf("password too short")
	}
	var count int
	if err := pool.QueryRow(ctx, `select count(*) from users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("users table not empty; bootstrap refused")
	}
	hash, err := legacy.MakeDjangoHash(password, 600000)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		insert into users (id, username, password_hash, display_name, is_builtin_admin, role, created_at, updated_at)
		values (gen_random_uuid(), 'admin', $1, 'Administrator', true, 'admin', now(), now())`, hash)
	return err
}

func targetDSNOf(cfg *legacy.Config) (string, error) {
	password, err := cfg.Target.Password.Resolve()
	if err != nil {
		return "", fmt.Errorf("resolve target password: %w", err)
	}
	ep := migration.TargetEndpoint{
		Host: cfg.Target.Host, Port: cfg.Target.Port, Database: cfg.Target.Database,
		Username: cfg.Target.Username, Password: string(password),
		TLSMode: cfg.Target.TLS.Mode, ServerName: cfg.Target.TLS.ServerName,
		CAFile: cfg.Target.TLS.CAFile, CertificateFile: cfg.Target.TLS.CertificateFile,
		PrivateKeyFile: cfg.Target.TLS.PrivateKeyFile,
	}
	return ep.TargetDSN()
}

func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeReport(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), raw, 0o640)
}

type exitCodeError struct {
	Code int
	Msg  string
}

func (e *exitCodeError) Error() string { return e.Msg }

func exitError(code int, msg string) error { return &exitCodeError{Code: code, Msg: msg} }

func usageError(msg string) error { return &exitCodeError{Code: exitUsage, Msg: msg} }

// RedactedMessage exports the redaction entry point (named redact inside the legacy package).
var _ = strings.TrimSpace

type ginH = map[string]any
