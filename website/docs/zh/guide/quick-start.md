# Docker Compose 部署与 Nginx 反向代理

:::warning 公测版本提示（Public Beta）
Yearning v4.0 当前处于 **Public Beta 公测阶段**，请优先在测试或预发环境试用体验；涉及重要数据时务必定期备份 PostgreSQL 数据卷与 `keyring.json`。
:::

本指南面向希望在 3 分钟内拉起完整 Yearning v4 公测环境的团队，同时包含**健康检查、Nginx HTTPS + SSE 反代配置、数据卷备份与无损升级**的完整命令。

## 1. 环境准备

- Linux / macOS 服务器，已安装 **Docker 24+** 与 **Docker Compose v2**（`docker compose version`）
- 推荐资源：**2 vCPU / 4 GiB RAM**（处理 32 MiB / 100,000 条大批量 SQL 时内存更充裕）
- 放通端口 `8080`（或通过反代暴露 `443`）

## 2. 一键启动（含自定义密码与端口）

```bash
# 1. 拉取 v4 分支（含内置 docker-compose.yml）
git clone --single-branch --branch v4 https://github.com/cookieY/Yearning.git
cd Yearning

# 2. 创建 .env 文件覆盖默认密钥和端口（管理员密码至少 12 位）
cat > .env <<'EOF'
POSTGRES_USER=yearning
POSTGRES_PASSWORD=Replace_With_Strong_PG_Password_2026!
POSTGRES_DB=yearning
YEARNING_ADMIN_PASSWORD=Replace_With_Strong_Admin_2026!
YEARNING_LISTEN_IP=0.0.0.0
YEARNING_PORT=8080
# 如果后面挂了 HTTPS 反向代理，改成 true
YEARNING_SESSION_SECURE=false
EOF

# 3. 后台启动全部 4 个编排容器
docker compose up -d
```

### 验证启动是否成功

等待 `bootstrap` 与 `migrate` 两个一次性任务执行完毕（通常 10–20 秒），检查服务状态与就绪探针：

```bash
# 确认 postgres 与 yearning 为 Up/healthy，bootstrap 与 migrate 为 Exited (0)
docker compose ps

# 验证控制面存活与就绪接口（返回 HTTP 200 且所有检查项通过）
curl -i http://127.0.0.1:8080/api/v4/health/live
curl -i http://127.0.0.1:8080/api/v4/health/ready
```

访问 `http://<你的服务器IP>:8080` 登录：
- **账号**：`admin`
- **密码**：`.env` 中设置的 `YEARNING_ADMIN_PASSWORD`（未配置 `.env` 时默认为 `Yearning@2026!`）

:::tip 刚登录不知道先点哪？
紧接着看 [首次登录 15 分钟初始化教程](/admin/first-time-setup)，按顺序配好 AI Provider → 审核规则集 → 数据源 → 流程 → 权限组即可发出第一笔工单。
:::

## 3. 容器分工与数据卷说明

`docker-compose.yml` 包含 4 个容器和 4 个持久化卷：

| 容器 | 职责 | 挂载的卷 |
| :--- | :--- | :--- |
| `bootstrap` | 首次启动自动生成 PostgreSQL 自签 CA/服务端证书、6 用途 `keyring.json` 与初始 admin 密码文件（幂等，重启不覆盖） | `yearning_pg_tls`、`yearning_migrate_secrets`、`yearning_app_secrets` |
| `postgres` | PostgreSQL 18 元数据库，强制开启 `ssl=on` | `yearning_pgdata`、`yearning_pg_tls` |
| `migrate` | 只读容器执行 `yearning migrate apply` 创建全部业务表并核验列目录 | `yearning_migrate_secrets` |
| `yearning` | Distroless 只读容器运行 `yearning serve`（内置前端 Web UI + Go 控制面） | `yearning_app_secrets`（只读） |

## 4. 生产 HTTPS 反向代理（Nginx 必配 SSE 参数）

Yearning v4 使用 **`GET /api/v4/events/stream`（Server-Sent Events）** 实时推送审批队列刷新、执行进度与查询授权撤销锁定。使用 Nginx 反代时**必须关闭该路径的响应缓冲**，否则前端会收不到实时推送或频繁断连。

完整 `nginx.conf` 站点配置（直接复制改域名与证书路径即可）：

```nginx
server {
    listen 443 ssl http2;
    server_name yearning.example.com;

    ssl_certificate     /etc/nginx/certs/yearning.fullchain.pem;
    ssl_certificate_key /etc/nginx/certs/yearning.privkey.pem;

    # 允许上传单工单最大 32 MiB 的批量 SQL 载荷（留少量 JSON 开销余量）
    client_max_body_size 40m;

    # 1. SSE 实时事件流专用配置（关键：禁止缓冲 + 拉长读超时）
    location = /api/v4/events/stream {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_buffering off;
        proxy_cache off;
        chunked_transfer_encoding on;
        proxy_read_timeout 3600s;
        add_header X-Accel-Buffering no;
    }

    # 2. 常规 Web UI 与 /api/v4/* 接口
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 180s;
    }
}
```

配置完 HTTPS 反代后，务必同步完成三件事：
1. `.env` 中将 `YEARNING_SESSION_SECURE=true` 并设置 `YEARNING_TRUSTED_PROXIES=127.0.0.1/32`（反代所在内网网段）。
2. 执行 `docker compose up -d` 重建 `yearning` 容器。
3. 登录后台进入 **系统设置 → 常规设置（`/admin/settings/general`）**，将 **公开访问地址（`public_base_url`）** 设为 `https://yearning.example.com`（用于生成通知消息中的「查看工单」链接及 OIDC 回调地址）。

## 5. 备份与无损版本升级

:::danger 绝对禁止升级时执行 `docker compose down -v`
删除 `yearning_app_secrets` 卷会永久销毁 AEAD 密钥环（`keyring.json`），导致 PostgreSQL 内已加密的全部数据源密码、AI Provider API Key、工单 SQL 与回滚证据**永远无法解密**。
:::

### 备份元数据库与密钥环

```bash
mkdir -p ./backups

# 1. 导出 PostgreSQL 元数据库逻辑备份
docker compose exec -T postgres pg_dump -U yearning -Fc yearning \
  > ./backups/yearning_pg_$(date +%Y%m%d_%H%M).dump

# 2. 将 keyring.json 导出到离线安全保险库（切勿与数据库备份放在同一公开目录）
docker run --rm -v yearning-beta_yearning_app_secrets:/secrets:ro alpine \
  cat /secrets/keyring/keyring.json > ./backups/keyring.json
chmod 600 ./backups/keyring.json
```

### 平滑升级镜像版本

```bash
# 拉取新版本镜像并按依赖顺序重建（migrate 会自动幂等执行新增 SQL 迁移）
export YEARNING_IMAGE=ghcr.io/cookiey/yearning:<新版本Tag>
docker compose pull
docker compose up -d

# 升级后核验就绪状态
curl -fsS http://127.0.0.1:8080/api/v4/health/ready
```
