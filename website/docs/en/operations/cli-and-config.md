# CLI Commands & Environment Reference

Yearning v4 ships as a single `yearning` binary with strict configuration precedence:

`built-in defaults < YAML config (--config) < YEARNING_* environment variables`

## Subcommands

| Command | Purpose | Key Flags |
| :--- | :--- | :--- |
| `yearning serve` | Run control plane HTTP server, scheduler, SSE hub, and embedded Web UI | `--config`, `--listen`, `--bootstrap-admin-password-file`, `--migration-mode` |
| `yearning migrate` | Run PostgreSQL schema migrations and v3 -> v4 data cutover | `preflight` / `dry-run` / `apply` / `verify`, `--config`, `--run-id`, `--migrations-dir`, `--catalog`, `--report-dir` |
| `yearning reset-admin-password` | Reset locked built-in `admin` password out-of-band (also `--reset-admin-password`) | `--config` |
| `yearning mock` | Contract-driven mock server with zero external dependencies | Uses `api/openapi/yearning-v4.yaml` |
| `yearning version` | Print build version, commit, OS/arch, and Go version | None |

## Key `YEARNING_*` Environment Variables

| Variable | Description |
| :--- | :--- |
| `YEARNING_ENVIRONMENT` | Runtime mode (`development` or `production`; required for non-loopback listen) |
| `YEARNING_SERVER_LISTEN_ADDRESS` | Listen address (default `127.0.0.1:8080`, container `0.0.0.0:8080`) |
| `YEARNING_TRUSTED_PROXIES` | Comma-separated trusted reverse proxy IPs / CIDRs |
| `YEARNING_DATABASE_HOST` / `PORT` / `USER` / `PASSWORD` / `DBNAME` | PostgreSQL metadata database connection settings |
| `YEARNING_DATABASE_SSLMODE` / `SSLROOTCERT` | TLS verification mode (`verify-full` recommended) and CA file path |
| `YEARNING_RETENTION_DATABASE_DSN` | **Required in production**: dedicated `yearning_retention` role DSN |
| `YEARNING_KEY_PROVIDER_KIND` / `PATH` | AEAD keyring provider (`file`) and path to `keyring.json` |
| `YEARNING_SESSION_COOKIE_NAME` / `SECURE` | Session cookie name (`yearning_session`) and HTTPS `Secure` flag |
| `YEARNING_LOGGING_LEVEL` / `FORMAT` | Structured logging level (`info`) and format (`json` / `console`) |
| `YEARNING_OBSERVABILITY_SERVICE_NAME` / `DEPLOYMENT_ID` / `INSTANCE_ID` | Service telemetry identifiers |
| `YEARNING_SCHEMA_MIGRATIONS_DIR` | Migration directory validated by `exact_schema_version` readiness probe |
| `YEARNING_ALLOW_LOOPBACK_TARGETS` | Dev-only loopback target override (rejected at startup in `production`) |
