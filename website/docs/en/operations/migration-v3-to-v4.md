# Migrating from Yearning v3 to v4

:::warning Public Beta Migration Caution
Yearning v4.0 is currently in **Public Beta**. Always rehearse the full `preflight -> dry-run -> apply -> verify` workflow against a cloned copy of your v3 MySQL database before planning any production cutover.
:::

Yearning v4 moves metadata persistence from legacy MySQL to a versioned PostgreSQL domain schema using an explicit **downtime cutover pipeline** (Preflight -> Dry-Run -> Admin Mapping Confirmation -> Apply -> Verify).

## Non-Negotiable Migration Rules

1. **Zero Active Legacy Work**: Stop Yearning v3 and close all active tickets before migration (`5001 MIGRATION_ACTIVE_WORK_EXISTS` blocks otherwise).
2. **Never Resume a Failed Target**: If `migrate apply` fails or receives `SIGTERM` twice, the target database is invalid (exit code `5` rejects retries). Destroy the target PostgreSQL database, recreate an empty database, and re-run cleanly.
3. **Exact `deployment_id` Match**: `encryption.deployment_id` in `migration-config.json` must match the target `keyring.json` `deployment_id` character-for-character so AES-256-GCM AAD verification succeeds at runtime.
4. **Automatic Legacy `SecretKey` Re-Encryption**: Configure optional `source.legacy_secret_key` (`environment` or 0600 `file` reference, never CLI args) pointing to v3's 16-char `General.SecretKey` to decrypt legacy AES-128-CBC datasource passwords and re-seal them into v4 AES-256-GCM envelopes.

## Six-Step Cutover Procedure

```bash
# 1. Initialize target PostgreSQL schema
./Yearning migrate preflight --config migration-config.json
./Yearning migrate apply     --config migration-config.json
./Yearning migrate verify    --config migration-config.json

# 2. Boot temporary migration workbench (loopback 127.0.0.1:9100) & bootstrap admin
./Yearning serve --migration-mode \
  --config migration-config.json \
  --bootstrap-admin-password-file /run/secrets/admin_pw

# 3. Run Dry-Run to generate manifest and candidate mappings
./Yearning migrate dry-run \
  --config migration-config.json \
  --report-dir /var/lib/yearning/migration

# 4. Sign in at http://127.0.0.1:9100 as built-in admin, confirm all candidate mappings,
#    and enter approval phrase "APPROVE <run_uuid>"

# 5. Apply approved migration run
./Yearning migrate apply \
  --config migration-config.json \
  --run-id <approved_run_uuid>

# 6. Verify row counts, lineage, logical references, and runtime AAD decryption
./Yearning migrate verify \
  --config migration-config.json \
  --run-id <approved_run_uuid> \
  --report-dir /var/lib/yearning/migration
```

Once `verify` reports `all_passed=true`, shut down the `--migration-mode` process (or scale down `yearning-migration-review` in Kubernetes) and start the production `yearning serve` control plane.
