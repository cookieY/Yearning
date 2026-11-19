# 二进制与独立 PostgreSQL 手动部署

如果你的生产环境已有高可用 PostgreSQL 集群（云 RDS / Patroni / 自建主从），不希望通过 Docker Compose 运行内置数据库，可以采用单二进制 `yearning` + `systemd` 方式直接部署。

## 1. 准备 PostgreSQL 14–18 数据库与最小权限角色

生产环境（`environment: production`）强制校验最小权限角色隔离：运行时连接账号 `yearning_app` **不得为超级用户、不得拥有 `CREATEDB`，且不能使用 `postgres` 或迁移账号**。

以 PostgreSQL 超级用户连接集群，依次创建库与角色：

```sql
-- 1. 创建角色（请将密码替换为高强度随机密码）
CREATE ROLE yearning_schema_owner NOLOGIN;
CREATE ROLE yearning_migrator LOGIN PASSWORD 'STRONG_MIGRATOR_PASS';
CREATE ROLE yearning_app LOGIN PASSWORD 'STRONG_APP_PASS';
CREATE ROLE yearning_retention LOGIN PASSWORD 'STRONG_RETENTION_PASS';
-- 如需执行旧版 v3 -> v4 数据迁移，再额外创建工作台角色：
CREATE ROLE yearning_migration_workbench LOGIN PASSWORD 'STRONG_WORKBENCH_PASS';

-- 2. 创建独立元数据库并指定 Schema Owner
CREATE DATABASE yearning OWNER yearning_schema_owner ENCODING 'UTF8';
GRANT yearning_schema_owner TO yearning_migrator;
```

## 2. 生成六用途 AEAD 密钥环（`keyring.json`）

Yearning v4 使用分域密钥环保护凭据、工单 SQL、游标与脱敏映射。密钥文件权限必须严格限制为 `0400` 或 `0440`（更宽权限启动会直接失败关闭）。

```bash
sudo mkdir -p /etc/yearning/secrets /etc/yearning/tls
sudo useradd --system --no-create-home --shell /usr/sbin/nologin yearning

# 一次性生成 6 组独立 256-bit 随机密钥
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

:::warning 牢记 `deployment_id`
`deployment_id` 会参与每一条密文的 AES-256-GCM AAD 校验，一旦投入使用**不可更改**；请立即将 `keyring.json` 加密备份到企业 KMS / 保险库。
:::

## 3. 执行版本化 Schema 迁移与角色授权

从 [GitHub Releases](https://github.com/cookieY/Yearning/releases) 下载对应架构的发行包（含 `yearning` 二进制、`db/migrations/`、`db/roles/grants.sql` 与 `api/contracts/postgresql-column-catalog.json`）解压至 `/opt/yearning/`。

### 第 1 步：准备迁移配置文件 `migrate-schema.json`

```json
{
  "source": {
    "host": "unused.invalid",
    "port": 3306,
    "database": "Yearning",
    "username": "unused",
    "password": { "environment": "YEARNING_MIGRATOR_PASSWORD" },
    "tls": { "mode": "verify_full", "server_name": "unused.invalid" }
  },
  "target": {
    "host": "pg.internal.example.com",
    "port": 5432,
    "database": "yearning",
    "username": "yearning_migrator",
    "password": { "environment": "YEARNING_MIGRATOR_PASSWORD" },
    "tls": {
      "mode": "verify_full",
      "server_name": "pg.internal.example.com",
      "ca_file": "/etc/yearning/tls/pg-ca.pem"
    }
  },
  "encryption": {
    "key_id": "prod-k1-payload",
    "key": { "environment": "YEARNING_MIGRATE_UNUSED" },
    "deployment_id": "yearning-prod-01"
  },
  "source_timezone": "UTC",
  "batch_rows": 1000
}
```

### 第 2 步：建表 + 校验列目录 + 执行最小权限授权

```bash
export YEARNING_MIGRATOR_PASSWORD="STRONG_MIGRATOR_PASS"
export YEARNING_MIGRATE_UNUSED="unused-for-schema-only"

# 执行版本化迁移（严禁使用 AutoMigrate）
/opt/yearning/yearning migrate apply \
  --config /etc/yearning/migrate-schema.json \
  --migrations-dir /opt/yearning/db/migrations \
  --catalog /opt/yearning/api/contracts/postgresql-column-catalog.json

# 授予 yearning_app 与 yearning_retention 最小表权限
PGPASSWORD="STRONG_MIGRATOR_PASS" psql \
  "host=pg.internal.example.com port=5432 dbname=yearning user=yearning_migrator sslmode=verify-full sslrootcert=/etc/yearning/tls/pg-ca.pem" \
  -f /opt/yearning/db/roles/grants.sql
```

## 4. 编写运行时配置文件 `/etc/yearning/config.yaml`

配置解析使用严格模式（出现未知字段直接启动报错），以下字段均与后端源码 `backend/internal/config/config.go` 一一对应：

```yaml
environment: production

server:
  listen_address: 127.0.0.1:8080
  shutdown_grace_seconds: 20
  read_header_timeout_seconds: 10
  read_timeout_seconds: 60
  write_timeout_seconds: 120
  idle_timeout_seconds: 120

database:
  host: pg.internal.example.com
  port: "5432"
  user: yearning_app
  password: STRONG_APP_PASS
  dbname: yearning
  # 生产模式仅接受 verify-ca 或 verify-full（require 会被拒绝）
  sslmode: verify-full
  sslrootcert: /etc/yearning/tls/pg-ca.pem
  max_open_conns: 50
  max_idle_conns: 15
  conn_max_lifetime_minutes: 10

retention:
  # 生产模式必填：独立清理角色，负责定期清理 >90 天审计与 >7 天 Raw Payload
  database_dsn: "host=pg.internal.example.com port=5432 dbname=yearning user=yearning_retention password=STRONG_RETENTION_PASS sslmode=verify-full sslrootcert=/etc/yearning/tls/pg-ca.pem"

logging:
  level: info
  format: json

observability:
  service_name: yearning
  deployment_id: yearning-prod-01
  instance_id: node-01

session:
  cookie_name: yearning_session
  # 生产必须走 HTTPS 反代并设为 true
  secure: true

key_provider:
  kind: file
  path: /etc/yearning/secrets/keyring.json

trusted_proxies:
  - 127.0.0.1/32
  - 10.0.0.0/8
```

设置文件权限与一次性初始管理员密码文件（至少 12 位，仅在 `users` 表为空时生效，引导完可删除该文件）：

```bash
sudo chown yearning:yearning /etc/yearning/config.yaml
sudo chmod 0600 /etc/yearning/config.yaml

printf '%s\n' 'YourStrongAdmin@2026!' | sudo tee /etc/yearning/secrets/admin-password >/dev/null
sudo chown yearning:yearning /etc/yearning/secrets/admin-password
sudo chmod 0400 /etc/yearning/secrets/admin-password
```

## 5. 配置 `systemd` 托管服务

```ini
# /etc/systemd/system/yearning.service
[Unit]
Description=Yearning v4 Database Governance Control Plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=yearning
Group=yearning
WorkingDirectory=/opt/yearning
Environment=YEARNING_SCHEMA_MIGRATIONS_DIR=/opt/yearning/db/migrations
ExecStart=/opt/yearning/yearning serve \
  --config /etc/yearning/config.yaml \
  --bootstrap-admin-password-file /etc/yearning/secrets/admin-password
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/tmp

[Install]
WantedBy=multi-user.target
```

启动并核验健康探针：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now yearning
sudo journalctl -u yearning -n 50 --no-pager

curl -i http://127.0.0.1:8080/api/v4/health/ready
```

看到 `HTTP 200` 后，按 [Docker Compose 指南中的 Nginx 配置](/guide/quick-start#4-生产-https-反向代理nginx-必配-sse-参数) 挂载 HTTPS 反代即可对外服务。
