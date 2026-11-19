# 从 Yearning v3 停机迁移实战

:::warning 公测版本迁移提醒（Public Beta）
Yearning v4.0 目前仍处于 **公测阶段（Public Beta）**。请务必先使用 v3 生产 MySQL 的**脱敏/克隆副本**完整演练一遍 `preflight -> dry-run -> apply -> verify` 并核对业务流转，切勿在未演练前直接对生产 v3 实例执行割接。
:::

Yearning v4 将元数据库从旧版 MySQL 迁移为全新 PostgreSQL 领域模型。为彻底杜绝双写漂移与权限误映射，v4 采用 **停机迁移 → Dry-Run → 管理员人工确认映射 → Apply → 全量核验** 的显式流程。

## 1. 迁移前必须满足的 4 个前提

1. **停止旧版 Yearning v3 服务**：禁止迁移期间产生新工单或双写。
2. **结清全部在途任务**：旧库中不能有未闭环的活动工单或未完成定时任务（否则报 `5001 MIGRATION_ACTIVE_WORK_EXISTS` 阻断）。
3. **备份旧 MySQL 数据库**：保存完整的 `mysqldump` 备份及校验和。
4. **准备好空白 PostgreSQL 14–18 目标库与 v4 `keyring.json`**：记下 `keyring.json` 里的 `deployment_id` 和 `payload` 密钥的 `key_id`、`key_base64`。

## 2. 编写完整 `migration-config.json`

注意以下两个关键字段：
- `source.legacy_secret_key`：填入旧版 v3 `conf.toml` 中 16 位 `General.SecretKey` 的引用（环境变量或 `0600` 文件，**严禁命令行传参**），迁移器将自动完成 **旧 AES-128-CBC 解密 → v4 AES-256-GCM 重加密**。
- `encryption.deployment_id`：必须与目标环境 `keyring.json` 中的 `deployment_id` **完全一致**，否则密文 AAD 校验会失败导致运行时无法解密。

```json
{
  "source": {
    "host": "old-mysql.internal.example.com",
    "port": 3306,
    "database": "Yearning",
    "username": "yearning_v3_readonly",
    "password": { "environment": "LEGACY_MYSQL_PASSWORD" },
    "legacy_secret_key": { "file": "/run/secrets/v3-secret-key" },
    "tls": {
      "mode": "verify_full",
      "server_name": "old-mysql.internal.example.com",
      "ca_file": "/etc/yearning/tls/mysql-ca.pem"
    }
  },
  "target": {
    "host": "new-pg.internal.example.com",
    "port": 5432,
    "database": "yearning_v4",
    "username": "yearning_migrator",
    "password": { "environment": "TARGET_PG_PASSWORD" },
    "tls": {
      "mode": "verify_full",
      "server_name": "new-pg.internal.example.com",
      "ca_file": "/etc/yearning/tls/pg-ca.pem"
    }
  },
  "encryption": {
    "key_id": "prod-k1-payload",
    "key": { "file": "/run/secrets/v4-payload-key-base64" },
    "deployment_id": "yearning-prod-01"
  },
  "source_timezone": "Asia/Shanghai",
  "batch_rows": 1000
}
```

## 3. 六步完成停机切换

```bash
export LEGACY_MYSQL_PASSWORD="OLD_MYSQL_READONLY_PASS"
export TARGET_PG_PASSWORD="NEW_PG_MIGRATOR_PASS"

# 步骤 1：在空白 PostgreSQL 上初始化 v4 Schema 与列目录
./yearning migrate preflight --config migration-config.json
./yearning migrate apply \
  --config migration-config.json \
  --migrations-dir ./db/migrations \
  --catalog ./api/contracts/postgresql-column-catalog.json
./yearning migrate verify --config migration-config.json

# 步骤 2：启动临时迁移审查工作台并引导内置 admin（默认仅监听本机 127.0.0.1:9100）
./yearning serve --migration-mode \
  --config migration-config.json \
  --bootstrap-admin-password-file /run/secrets/admin-password &

# 步骤 3：执行 Dry-Run，生成脱敏审计报告与候选权限映射（获得 <run_uuid>）
./yearning migrate dry-run \
  --config migration-config.json \
  --report-dir /var/lib/yearning/migration-reports
```

### 步骤 4：人工审查与批准权限映射

1. 浏览器访问 `http://127.0.0.1:9100`（若在 Kubernetes 集群内运行，先执行 `kubectl port-forward svc/yearning-migration-review 9100:9100 -n yearning`），使用刚才引导的 `admin` 账号登录。
2. 逐项核对 v3 迁移生成的用户、数据源、审批流程与权限组候选映射（如果某数据源未提供 `legacy_secret_key` 导致密码被隔离，界面会明确标注）。
3. 全部候选确认无误后，在确认框输入短语 **`APPROVE <run_uuid>`** 提交批准。

### 步骤 5 & 6：正式写入与全量抽样解密核验

```bash
# 步骤 5：绑定已批准的 run_uuid 执行正式数据迁移
./yearning migrate apply \
  --config migration-config.json \
  --run-id <approved_run_uuid>

# 步骤 6：全量核验行数、谱系（Lineage）、逻辑引用以及运行时 AAD 抽样解密
./yearning migrate verify \
  --config migration-config.json \
  --run-id <approved_run_uuid> \
  --report-dir /var/lib/yearning/migration-reports
```

确认输出 `all_passed=true` 且 `legacy_migration_runs.state = verified` 后：
1. 立即终止 `--migration-mode` 审查进程（K8s 环境将 `yearning-migration-review` 缩容为 0 或删除）。
2. 为目标库执行 `db/roles/grants.sql`，然后启动正式的 `yearning serve` 控制面切流。

## 4. 迁移失败时的唯一合法恢复方式

:::danger 禁止在失败目标库上续跑 `migrate apply`
`migrate apply` 失败或收到两次 `SIGTERM` 后，目标库处于半写入作废态，再次对同一失败 `run_id` 执行 `apply` 会被 CLI 直接以退出码 `5` 拒绝。切勿手工删表修数据后硬上线！
:::

正确恢复命令：

```bash
# 1. 销毁失败的 PostgreSQL 目标库并重建空库
psql "$PG_ADMIN_DSN" -c 'DROP DATABASE yearning_v4;'
psql "$PG_ADMIN_DSN" -c 'CREATE DATABASE yearning_v4 OWNER yearning_schema_owner;'

# 2. 修正配置或源数据问题后，从「步骤 1」完整重新跑一遍 Preflight -> Dry-Run -> 批准 -> Apply -> Verify
```
