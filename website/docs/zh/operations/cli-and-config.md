# CLI 命令、管理员密码重置与环境变量参考

Yearning v4 编译为单二进制文件 `yearning`，配置优先级严格固定为：

`内置默认值 < YAML 配置文件 (--config) < YEARNING_* 环境变量`

## 1. 高频运维操作：内置 `admin` 被锁后重置密码

内置超级管理员 `admin` 连续输错 5 次密码后会被锁定（登录返回 `1102 ADMIN_PASSWORD_LOCKED`），**无法在网页端解锁**，必须由运维人员在部署服务器通过交互式终端重置（支持服务运行中在线执行，无需停机）：

### 二进制部署环境

```bash
# 支持子命令或兼容旧版 --reset-admin-password 参数，必须从交互式 TTY 输入（禁止管道传明文）
./yearning --reset-admin-password --config /etc/yearning/config.yaml
# 按提示输入至少 12 位新密码并二次确认（屏幕不回显）
# 成功后输出：admin password reset: ok (sessions revoked, lockout cleared, audit written)
```

### Docker Compose 环境

```bash
# 使用 -it 分配交互式伪终端执行
docker compose exec -it yearning /yearning --reset-admin-password
```

### Kubernetes 环境

```bash
kubectl exec -it deploy/yearning-control-plane -n yearning -- /yearning --reset-admin-password
```

重置完成后：原 `admin` 全部活跃会话立刻失效、失败锁定计数清零，同时写入 `admin.password_reset_cli` 审计日志。

---

## 2. 全部 CLI 子命令速查

| 命令 | 用途 | 常用参数 |
| :--- | :--- | :--- |
| `yearning serve` | 启动控制面、调度器、SSE 总线与 Web UI | `--config <yaml>`、`--listen <addr>`、`--bootstrap-admin-password-file <file>`、`--migration-mode` |
| `yearning migrate` | Schema 迁移与 v3 -> v4 停机迁移 | `preflight` / `dry-run` / `apply` / `verify`、`--config`、`--run-id`、`--migrations-dir`、`--catalog`、`--report-dir` |
| `yearning reset-admin-password` | 交互式重置内置 `admin` 密码（等价 `--reset-admin-password`） | `--config <yaml>` |
| `yearning mock` | 启动无外部依赖的契约 Mock Server（前端联调用） | 基于 `api/openapi/yearning-v4.yaml` |
| `yearning version` | 输出版本号、Commit、架构与 Go 版本 | 无 |

---

## 3. 完整 `YEARNING_*` 环境变量与 `config.yaml` 映射

| 环境变量 | 对应 `config.yaml` 路径 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `YEARNING_ENVIRONMENT` | `environment` | 空（环回开发） | `development` 或 `production`；非环回监听（如 `0.0.0.0:8080`）时**必填** |
| `YEARNING_SERVER_LISTEN_ADDRESS` | `server.listen_address` | `0.0.0.0:8080` | HTTP 监听地址 |
| `YEARNING_SERVER_SHUTDOWN_GRACE_SECONDS` | `server.shutdown_grace_seconds` | `20` | 优雅退出等待秒数（`1..300`） |
| `YEARNING_SERVER_READ_HEADER_TIMEOUT_SECONDS` | `server.read_header_timeout_seconds` | `10` | 读请求头超时 |
| `YEARNING_SERVER_READ_TIMEOUT_SECONDS` | `server.read_timeout_seconds` | `60` | 读请求体超时 |
| `YEARNING_SERVER_WRITE_TIMEOUT_SECONDS` | `server.write_timeout_seconds` | `120` | 写响应超时 |
| `YEARNING_SERVER_IDLE_TIMEOUT_SECONDS` | `server.idle_timeout_seconds` | `120` | Keep-Alive 空闲超时 |
| `YEARNING_TRUSTED_PROXIES` | `trusted_proxies` | 空 | 逗号分隔的受信任反代 CIDR（如 `127.0.0.1/32,10.0.0.0/8`） |
| `YEARNING_DATABASE_HOST` | `database.host` | 必填 | PostgreSQL 主机 |
| `YEARNING_DATABASE_PORT` | `database.port` | 必填 | PostgreSQL 端口（通常 `5432`） |
| `YEARNING_DATABASE_USER` | `database.user` | 必填 | 应用角色（生产须为非超级用户 `yearning_app`） |
| `YEARNING_DATABASE_PASSWORD` | `database.password` | 必填 | 数据库密码 |
| `YEARNING_DATABASE_DBNAME` | `database.dbname` | 必填 | 元数据库名 |
| `YEARNING_DATABASE_SSLMODE` | `database.sslmode` | `prefer` | 生产模式强制仅接受 `verify-ca` 或 `verify-full` |
| `YEARNING_DATABASE_SSLROOTCERT` | `database.sslrootcert` | 空 | 私有 CA 证书路径（缺失时走系统信任库） |
| `YEARNING_DATABASE_MAX_OPEN_CONNS` | `database.max_open_conns` | `50` | 连接池最大连接数 |
| `YEARNING_DATABASE_MAX_IDLE_CONNS` | `database.max_idle_conns` | `15` | 连接池最大空闲连接 |
| `YEARNING_DATABASE_CONN_MAX_LIFETIME_MINUTES` | `database.conn_max_lifetime_minutes` | `10` | 连接最大存活分钟数 |
| `YEARNING_RETENTION_DATABASE_DSN` | `retention.database_dsn` | 生产必填 | `yearning_retention` 独立清理角色 libpq 连接串 |
| `YEARNING_KEY_PROVIDER_KIND` | `key_provider.kind` | `file` | 密钥提供者类型 |
| `YEARNING_KEY_PROVIDER_PATH` | `key_provider.path` | 必填 | `keyring.json` 路径（文件权限须 `0400` 或 `0440`） |
| `YEARNING_SESSION_COOKIE_NAME` | `session.cookie_name` | `yearning_session` | 会话 Cookie 名 |
| `YEARNING_SESSION_SECURE` | `session.secure` | `true` | 仅在无 HTTPS 的本地开发评测中可改为 `false` |
| `YEARNING_LOGGING_LEVEL` | `logging.level` | `info` | `debug` / `info` / `warn` / `error` |
| `YEARNING_LOGGING_FORMAT` | `logging.format` | `json` | 固定为结构化 `json` |
| `YEARNING_OBSERVABILITY_SERVICE_NAME` | `observability.service_name` | `yearning` | 服务名 |
| `YEARNING_OBSERVABILITY_DEPLOYMENT_ID` | `observability.deployment_id` | 空 | 部署环境标识 |
| `YEARNING_OBSERVABILITY_INSTANCE_ID` | `observability.instance_id` | 空 | Pod / 节点实例标识 |
| `YEARNING_SCHEMA_MIGRATIONS_DIR` | 仅环境变量 | `/usr/share/yearning/db-migrations` | Readiness `exact_schema_version` 校验使用的迁移目录 |
| `YEARNING_RELEASE_FEED_UPSTREAM_URL` | `release_feed.upstream_url` | GitHub Releases | 自定义版本检测上游 URL |
| `YEARNING_ALLOW_LOOPBACK_TARGETS` | 仅环境变量 | `false` | 仅允许在 `development` 调试本机目标库；`production` 开启会直接拒绝启动 |
