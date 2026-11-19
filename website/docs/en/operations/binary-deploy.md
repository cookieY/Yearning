# Binary & Standalone PostgreSQL Deployment

Deploy the single `yearning` binary directly with `systemd` against an existing HA PostgreSQL 14–18 cluster (RDS / Patroni / self-managed).

## 1. Create PostgreSQL Least-Privilege Roles

In production (`environment: production`), Yearning enforces `database_role_self_check` and rejects superuser, `CREATEDB`, `postgres`, or migrator roles on the runtime connection:

```sql
CREATE ROLE yearning_schema_owner NOLOGIN;
CREATE ROLE yearning_migrator LOGIN PASSWORD 'STRONG_MIGRATOR_PASS';
CREATE ROLE yearning_app LOGIN PASSWORD 'STRONG_APP_PASS';
CREATE ROLE yearning_retention LOGIN PASSWORD 'STRONG_RETENTION_PASS';
CREATE ROLE yearning_migration_workbench LOGIN PASSWORD 'STRONG_WORKBENCH_PASS';

CREATE DATABASE yearning OWNER yearning_schema_owner ENCODING 'UTF8';
GRANT yearning_schema_owner TO yearning_migrator;
```

## 2. Generate 6-Purpose `keyring.json` (`0400` / `0440` required)

```bash
sudo mkdir -p /etc/yearning/secrets /etc/yearning/tls
sudo useradd --system --no-create-home --shell /usr/sbin/nologin yearning

NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)
gen_key() { head -c 32 /dev/urandom | base64 | tr -d '\n'; }

sudo tee /etc/yearning/secrets/keyring.json >/dev/null <<EOF
{
  "contract_version": 1,
  "deployment_id": "yearning-prod-01",
  "keys": [
    {"key_id": "prod-k1-payload",  "purpose": "payload",               "status": "encrypt_decrypt", "created_at": "${NOW}", "key_base64": "$(gen_key)"},
    {"key_id": "prod-k1-sql-fp",   "purpose": "sql_fingerprint",       "status": "encrypt_decrypt", "created_at": "${NOW}", "key_base64": "$(gen_key)"},
    {"key_id": "prod-k1-identity", "purpose": "identifier_token",      "status": "encrypt_decrypt", "created_at": "${NOW}", "key_base64": "$(gen_key)"},
    {"key_id": "prod-k1-settings", "purpose": "settings_impact_token", "status": "encrypt_decrypt", "created_at": "${NOW}", "key_base64": "$(gen_key)"},
    {"key_id": "prod-k1-oidc",     "purpose": "oidc_state",            "status": "encrypt_decrypt", "created_at": "${NOW}", "key_base64": "$(gen_key)"},
    {"key_id": "prod-k1-affinity", "purpose": "cursor_affinity",       "status": "encrypt_decrypt", "created_at": "${NOW}", "key_base64": "$(gen_key)"}
  ]
}
EOF
sudo chown yearning:yearning /etc/yearning/secrets/keyring.json
sudo chmod 0400 /etc/yearning/secrets/keyring.json
```

## 3. Apply Versioned Migrations & `db/roles/grants.sql`

```bash
/opt/yearning/yearning migrate apply \
  --config /etc/yearning/migrate-schema.json \
  --migrations-dir /opt/yearning/db/migrations \
  --catalog /opt/yearning/api/contracts/postgresql-column-catalog.json

PGPASSWORD="STRONG_MIGRATOR_PASS" psql \
  "host=pg.internal.example.com port=5432 dbname=yearning user=yearning_migrator sslmode=verify-full sslrootcert=/etc/yearning/tls/pg-ca.pem" \
  -f /opt/yearning/db/roles/grants.sql
```

## 4. Runtime `/etc/yearning/config.yaml` & `systemd` Unit

```yaml
environment: production
server:
  listen_address: 127.0.0.1:8080
  shutdown_grace_seconds: 20
database:
  host: pg.internal.example.com
  port: "5432"
  user: yearning_app
  password: STRONG_APP_PASS
  dbname: yearning
  sslmode: verify-full
  sslrootcert: /etc/yearning/tls/pg-ca.pem
retention:
  database_dsn: "host=pg.internal.example.com port=5432 dbname=yearning user=yearning_retention password=STRONG_RETENTION_PASS sslmode=verify-full sslrootcert=/etc/yearning/tls/pg-ca.pem"
logging:
  level: info
  format: json
observability:
  service_name: yearning
  deployment_id: yearning-prod-01
session:
  cookie_name: yearning_session
  secure: true
key_provider:
  kind: file
  path: /etc/yearning/secrets/keyring.json
trusted_proxies:
  - 127.0.0.1/32
```

Start with `YEARNING_SCHEMA_MIGRATIONS_DIR=/opt/yearning/db/migrations /opt/yearning/yearning serve --config /etc/yearning/config.yaml --bootstrap-admin-password-file /etc/yearning/secrets/admin-password`, verify `GET /api/v4/health/ready`, and place behind HTTPS Nginx with `proxy_buffering off` on `/api/v4/events/stream`.
