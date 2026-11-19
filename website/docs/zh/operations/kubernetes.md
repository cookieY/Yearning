# Kubernetes 生产多副本部署

Yearning v4 是完全无状态的 Go 控制面，所有会话、单库执行排他租约、定时预约与多副本 SSE 实时广播均通过 PostgreSQL 协调，不依赖本地持久化卷。

仓库在 `backend/deploy/k8s/` 提供三份生产清单：
- `control-plane.yaml`：至少 2 副本 Deployment（`maxUnavailable: 0`）、PDB、Service、UID `10001` 非 root 只读容器
- `migration-workloads.yaml`：一次性 Schema 迁移 Job（`yearning-schema-migration`）与迁移窗口审查 Deployment（`yearning-migration-review`）
- `network-policy.yaml`：最小权限集群出入站 NetworkPolicy

## 1. 提前准备 5 份职责分离的 Secret

请严格按角色隔离创建以下 Secret（不要让迁移 Job 访问密钥环，也不要让控制面 Pod 访问迁移账号）：

```bash
NS=yearning
kubectl create namespace "$NS"

# 1. AEAD 密钥环 Secret（挂载为 0400/0440 只读文件）
kubectl create secret generic yearning-payload-keyring -n "$NS" \
  --from-file=keyring.json=./keyring.json

# 2. 控制面运行账号 Secret（yearning_app 非超级用户角色）
kubectl create secret generic yearning-metadata-database-app -n "$NS" \
  --from-literal=host=pg.internal.example.com \
  --from-literal=port=5432 \
  --from-literal=user=yearning_app \
  --from-literal=password='STRONG_APP_PASS' \
  --from-literal=dbname=yearning \
  --from-literal=sslmode=verify-full \
  --from-literal=sslrootcert=/var/run/secrets/yearning/metadata-db-app/ca.crt \
  --from-file=ca.crt=./pg-ca.pem

# 3. 90 天审计与 7 天 Raw Payload 清理角色 Secret（生产必填）
kubectl create secret generic yearning-metadata-database-retention -n "$NS" \
  --from-literal=dsn="host=pg.internal.example.com port=5432 dbname=yearning user=yearning_retention password=STRONG_RETENTION_PASS sslmode=verify-full sslrootcert=/var/run/secrets/yearning/metadata-db-app/ca.crt"

# 4. Schema 迁移账号 Secret（yearning_migrator 角色）
kubectl create secret generic yearning-metadata-database-migrator -n "$NS" \
  --from-literal=password='STRONG_MIGRATOR_PASS' \
  --from-file=ca.crt=./pg-ca.pem
```

为 `yearning-schema-migration` Job 渲染独立的迁移配置文件 Secret（注意 `target.password` 指向容器内挂载的 migrator 凭据文件绝对路径）：

```bash
cat > schema-migrate-config.json <<'EOF'
{
  "source": {
    "host": "unused.invalid",
    "port": 3306,
    "database": "Yearning",
    "username": "unused",
    "password": { "file": "/var/run/secrets/yearning/metadata-db-migrator/password" },
    "tls": { "mode": "verify_full", "server_name": "unused.invalid" }
  },
  "target": {
    "host": "pg.internal.example.com",
    "port": 5432,
    "database": "yearning",
    "username": "yearning_migrator",
    "password": { "file": "/var/run/secrets/yearning/metadata-db-migrator/password" },
    "tls": {
      "mode": "verify_full",
      "server_name": "pg.internal.example.com",
      "ca_file": "/var/run/secrets/yearning/metadata-db-migrator/ca.crt"
    }
  },
  "encryption": {
    "key_id": "prod-k1-payload",
    "key": { "file": "/var/run/secrets/yearning/metadata-db-migrator/password" },
    "deployment_id": "yearning-prod-01"
  },
  "source_timezone": "UTC",
  "batch_rows": 1000
}
EOF

kubectl create secret generic yearning-migration-config-schema -n "$NS" \
  --from-file=config.json=./schema-migrate-config.json
```

## 2. 渲染不可变 Digest 镜像清单并发布

仓库源清单刻意使用 `ghcr.io/cookiey/yearning@sha256:REPLACED_BY_RELEASE` 占位符，正式上线前先用实际发布镜像的 SHA-256 Digest 渲染到 `dist/release-manifests/`：

```bash
# 1. 替换为真实镜像 Digest 渲染清单
make release-render-manifests \
  RELEASE_IMAGE_DIGEST=ghcr.io/cookiey/yearning@sha256:<64位真实digest>

# 2. 应用最小权限 NetworkPolicy
kubectl apply -n "$NS" -f dist/release-manifests/network-policy.yaml

# 3. 先执行 Schema 迁移 Job 并等待完成（backoffLimit=0，失败不盲目重试）
kubectl apply -n "$NS" -f dist/release-manifests/migration-workloads.yaml
kubectl wait --for=condition=complete job/yearning-schema-migration -n "$NS" --timeout=300s

# 4. 首次初始化时对新表执行最小权限授权 grants.sql（详见二进制部署第 3 步）

# 5. 发布无状态多副本控制面（2 副本，maxUnavailable=0）
kubectl apply -n "$NS" -f dist/release-manifests/control-plane.yaml
kubectl rollout status deployment/yearning-control-plane -n "$NS" --timeout=300s
```

:::tip Ingress 对接 SSE 长连接
若使用 `ingress-nginx` 对外暴露 `Service/yearning:8080`，请在 Ingress Annotation 中关闭缓冲并放大超时，避免实时审批/执行推送断开：
```yaml
metadata:
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "40m"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
```
:::

## 3. 四项 Readiness 生产就绪探针排障

Pod 通过 `/api/v4/health/ready` 后才会接入 Service 流量。如果 Pod 一直处于 `0/1 Ready`，请执行 `kubectl logs deploy/yearning-control-plane -n yearning` 对照以下 4 项探针排查：

| 探针名称 | 常见未就绪原因 | 解决方法 |
| :--- | :--- | :--- |
| `metadata_postgresql` | PG 网络策略未放行或 `ca.crt` 域名与 `server_name` 不匹配 | 检查 NetworkPolicy 放行 PG 5432 端口及 TLS 证书 SAN |
| `key_provider` | `keyring.json` 缺少 6 个用途密钥之一，或文件权限过宽 | 确认 Secret 卷 `defaultMode: 0400` + `fsGroup: 10001` |
| `schema_ledger` / `exact_schema_version` | 未运行最新版本的 `yearning-schema-migration` Job，导致数据库版本落后于镜像 | 先运行与当前镜像同版本的迁移 Job 再滚动控制面 |
| `database_role_self_check` | `yearning-metadata-database-app` 误用了 `postgres` / 超级用户 / `yearning_migrator` | 切换为无超管权限的 `yearning_app` 角色 |

:::warning 生产升级前硬约束
滚动更新前请先确认平台中**没有正在运行中的 DML、DDL 或 `gh-ost` 任务**；若已存在 `yearning-schema-migration` 旧 Job 对象，发版前需先 `kubectl delete job yearning-schema-migration -n yearning` 再重新 apply。
:::
