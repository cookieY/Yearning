# 首次登录 15 分钟初始化教程

服务启动后，访问 `http://<你的地址>:8080` 并使用内置超级管理员 `admin` 登录。

很多初次使用 Yearning v4 的管理员容易卡在配置顺序上（例如直接去建变更流程却因为还没有规则集而报错 `1001`）。请严格按照下面 **6 步依赖顺序** 操作，15 分钟即可让整个研发团队跑通第一笔工单和第一条只读查询。

```text
第 1 步：配置 AI Provider         (/admin/review-engine/providers)
   │
   ▼
第 2 步：启用审核技能并建规则集     (/admin/review-engine/skills -> /admin/rule-sets)
   │
   ▼
第 3 步：注册业务数据源并测试连通   (/admin/datasources)
   │
   ▼
第 4 步：创建成员账号与审批/查询流程 (/admin/users -> /admin/flows)
   │
   ▼
第 5 步：通过权限组把流程授权给用户 (/admin/permission-groups)
   │
   ▼
第 6 步：配置公开访问地址与通知渠道 (/admin/settings/general & /notifications)
```

---

## 第 1 步：配置至少 1 个可用的 AI Provider

进入 **审核引擎 → Provider 管理（`/admin/review-engine/providers`）**，点击 **新建 Provider**：

- **如果你使用公有云模型（OpenAI / DeepSeek / 通义千问等 OpenAI 兼容接口）**：
  - Base URL 必须为 `https://`（例如 `https://api.openai.com/v1` 或 `https://api.deepseek.com/v1`）
  - System Prompt Profile 保持默认 **`default`**（全量内存脱敏，业务字面值与表列名绝不出内网）
- **如果你在内网自部署了本地模型（vLLM / Ollama）**：
  - 内网 RFC1918 地址支持 `http://`（例如 `http://10.20.4.18:8000/v1`）
  - 若运行 `SAN-2B` / `SAN-4B` / `SQL-Audit-Nano` 原生模型，Profile 选 **`san`**；其余通用本地模型保持 **`default`**
- 点击 **测试连接** 确认探测通过，开启 **启用** 开关。详细参数与分时参考计价见 [AI Provider 与审核规则手册](/admin/review-engine)。

## 第 2 步：启用审核技能并创建规则集（Rule Set）

1. 进入 **审核引擎 → 审核技能（`/admin/review-engine/skills`）**，系统已预置 **48 条内置审核技能**。
   - 推荐起步启用：`builtin.dml-require-where`、`builtin.forbid-truncate`、`builtin.ddl-forbid-drop-database`、`builtin.ddl-require-primary-key`、`builtin.max-affect-rows`（按需调大或调小阈值）。
2. 进入 **审核引擎 → 规则集（`/admin/rule-sets`）**，点击 **新建规则集**（例如命名为 `prod-mysql-baseline`），勾选刚才启用的已发布技能并保存。

:::warning 常见踩坑：为什么建流程时报错 `1001 VALIDATION_FAILED`？
在 Yearning v4 中，每一条 `change_review` 变更流程**必须绑定一个非空的规则集（`rule_set_id`）**，且只能绑定已启用/非草稿状态的技能组合。必须先完成第 2 步，再进入第 4 步。
:::

## 第 3 步：注册业务数据源并测试连接

进入 **管理 → 数据源（`/admin/datasources`）**，点击 **新建数据源**：

1. 选择数据库引擎与兼容模式（MySQL 5.7+ / 8.0 / 8.4 LTS、PostgreSQL 14–18+、TiDB、OceanBase、PolarDB）。
2. 填写 Host、Port 与 **必填的版本约束 `version_constraint`**（例如 MySQL 8.0 填 `>=8.0,<9.0`，PG 16 填 `>=16.0,<17.0`）。
3. 分别录入 **只读凭据（用于预审与查询）** 与 **执行凭据（用于变更落库）**。建权 SQL 可直接从 [数据源接入与目标库建权 SQL](/admin/datasources-and-grants) 复制。
4. 点击 **测试连接** 确认版本与身份指纹（`identity_fingerprint`）探测成功，保持 **启用** 状态。

## 第 4 步：创建团队用户并编排流程

1. 进入 **管理 → 用户（`/admin/users`）** 创建至少 1 个研发账号（提交人）与 1 个 DBA 账号（审批人/执行人）；如对接企业 LDAP/OIDC，也可直接在 `/admin/settings/general` 配好 SSO 后让员工首次登录自动建号。
2. 进入 **管理 → 流程（`/admin/flows`）**：
   - **创建变更流程（`change_review`）**：绑定第 2 步的规则集，添加 1 个或多个串行阶段（例如阶段 1 绑定预发库，阶段 2 绑定生产库），为每个阶段配置 `1..10` 级审批人及执行人列表。
   - **创建查询流程（`query_access`）**：选择允许查询的数据源，按数据源勾选是否开放 **导出权限（`can_export`）**，按需配置敏感列脱敏规则与审批人。

## 第 5 步：通过权限组把流程授权给团队成员

进入 **管理 → 权限组（`/admin/permission-groups`）**：

1. 新建权限组（如 `backend-dev`）。
2. 在成员中添加研发与 DBA 账号。
3. 在流程授权中勾选第 4 步创建的 `change_review` 与 `query_access` 流程。

:::tip 重要权限概念
在 Yearning v4 中，**权限组授权的是「流程」而非「数据源」**，且**内置 `admin` 也不会自动拥有审批/执行别人的冻结工单权限**。如果你想用 `admin` 自己做冒烟测试，请把 `admin` 也加入对应的权限组及流程审批/执行节点中。
:::

## 第 6 步：设置公开访问地址与通知渠道

1. 进入 **系统设置 → 常规设置（`/admin/settings/general`）**：填写 `public_base_url`（例如 `https://yearning.example.com`）、默认语言与系统时区（如 `Asia/Shanghai`）。
2. 进入 **系统设置 → 通知中心（`/admin/settings/notifications`）**：添加 Email / 钉钉 / 飞书 / Slack 渠道并点击 **测试发送**，再添加「工单提交 / 待审批 / 待执行 / 执行完成」通知规则。

至此初始化完成！现在切换到研发账号进入 `/changes/new`，就可以立即走完第一笔带 AI 预审、冻结审批与自动回滚保护的变更工单（见 [提单与 AI 预审实战](/features/ai-precheck)）。
