# 架构设计与隐私流水线

Yearning v4 在底层放弃了多进程 RPC 耦合与本地状态假设，采用 **无状态 Go 模块化单体 + PostgreSQL 共享协调** 架构，保证在高并发审批、批量预审与 Kubernetes 多副本部署下的一致性与零泄漏边界。

## 核心技术栈

| 层次 | 关键组件 |
| :--- | :--- |
| **控制面后端** | Go 1.26 · Gin · GORM · `pgx/v5` · TiDB SQL Parser · `pg_query_go/v6` · 进程内 `gh-ost` · 零 RPC |
| **前端工作台** | React 19 · TypeScript · Vite · Tailwind CSS v4 · shadcn/ui · Monaco Editor · Recharts |
| **元数据库与协调** | PostgreSQL 14–18（显式版本化 Migration、三角色权限隔离、`LISTEN/NOTIFY` + 事务 Outbox） |
| **密码学与安全** | AES-256-GCM AEAD 分用途密钥环 + 表/列/主键 AAD 绑定 · 双提交 CSRF · Argon2id · OIDC PKCE (`S256`) · LDAPS/StartTLS |
| **交付形态** | Distroless 非 root 只读根文件系统容器镜像（`linux/amd64`、`linux/arm64`） |

## 端到端数据流与隐私边界

```text
研发人员 / AI Agent (Web UI 或 /api/v4/*)
        │
        ▼
[1] 词法解析与双指纹折叠 (TiDB Parser / pg_query_go)
    - 单批最高 100,000 语句 / 32 MiB，同构模板合并至 <= 1,000 组
        │
        ├── default 轨道（公有云 OpenAI 兼容模型）
        │     └─► 内存匿名化 (schema_1 / table_1 / col_1) + 剥离字面值与注释
        │     └─► AssertZeroLeak 零泄漏断言 (PrivacyContractHashDefault)
        │
        └── san 轨道（内网自部署 vLLM / Ollama）
              └─► addrguard SSRF + RFC1918 校验 + 精简 Raw-SQL 原生契约
              └─► 体积保护 (>256 字符字面值截断) + PrivacyContractHashSAN
        │
        ▼
[2] 5 项只读元数据工具 + Prompt 技能 + 内部经验库
    - check_syntax / describe_table / list_indexes / table_stats / safe_explain
    - 强制规则命中或运行时证明无效 -> 确定性 Critical 阻断提交
        │
        ▼
[3] 提交即冻结变更工单 (1..N 串行数据源阶段 · 1..10 级审批)
    - 冻结 SQL Hash、审核快照、数据源身份、审批人与执行人
        │
        ▼
[4] 单数据源排他租约执行引擎 (DML 事务 / DDL / 进程内 gh-ost)
    ├── 目标数据库集群 (MySQL / PostgreSQL / TiDB / OceanBase / PolarDB)
    ├── 加密回滚证据 (order_rollback_evidences · AES-256-GCM · 无需 Binlog/WAL)
    └── 事务 Outbox ──► 90 天审计日志 · SSE 实时事件流 · Email / 钉钉 / 飞书 / Slack
```

## 关键架构不变量

### 1. 角色分离与不可越权（`admin_is_not_business_override`）

- 平台严格隔离 **管理面**（`/admin/*`，由内置 `admin` 及 `role='admin'` 管理员管理）与 **业务面**（工单提交、审批、执行、SQL 原文查看与在线查询，由权限组与冻结流程决定）。
- 任何管理员均不可修改已提交工单的冻结审批链，不能代审、代执行、转交、加签或越权查看未授权工单的 SQL 明文。

### 2. 六用途分域 AEAD 密钥环与 AAD 防替换

Yearning v4 的 `keyring.json` 强制分离 6 个密码学用途域，避免单密钥复用风险：

| `purpose` | 用途说明 |
| :--- | :--- |
| `payload` | 数据源凭据、TLS 私钥、AI Provider 密钥、通知 Webhook、工单 SQL 与回滚 SQL 信封加密 |
| `sql_fingerprint` | SQL 规范化与方言指纹 HMAC 签名 |
| `identifier_token` | 匿名化标识符映射加密与受权还原校验 |
| `settings_impact_token` | 高危系统设置两步确认（`Assess` -> `impact_token`）签名 |
| `oidc_state` | OIDC PKCE 登录流程 `state` / `nonce` 加密 |
| `cursor_affinity` | 只读查询 6 小时分页游标与用户绑定 HMAC |

所有落盘密文均将 **`deployment_id + 表名 + 主键 + 列名 + purpose`** 绑定至 AES-256-GCM 的 AAD（附加认证数据），从密码学层面阻断跨表、跨记录密文替换攻击。

### 3. 双轨 AI 隐私隔离与跨轨道防回退

- `default` 轨道与 `san` 轨道分别绑定 `PrivacyContractHashDefault` 与 `PrivacyContractHashSAN`，并参与 `PlanHash` 计算。
- 在混合主备 Provider 链中，系统在每次发送前针对具体目标端点独立构造 System Prompt 与载荷：**为 `san` 本地模型准备的原始 SQL 载荷绝不会在故障转移时泄漏给 `default` 公有云模型**。

### 4. 事务 Outbox 与实时 SSE 广播总线

- 所有工单状态变更、审批决策、执行进度与查询授权撤销均与业务表在同一 PostgreSQL 事务内写入 Outbox 事件，确保通知投递与审计记录不丢不重。
- 已认证前端与外部客户端可通过单条长连接 `GET /api/v4/events/stream`（`Accept: text/event-stream`，25 秒 `: keepalive` 心跳）接收参与者隔离的 `io.yearning.v4.*` 实时元数据事件，帧内绝不包含原始 SQL 或审批自由文本。
