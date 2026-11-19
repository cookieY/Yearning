# 企业级安全与 AI 可信架构（防泄露 · 零侵入 · 抗幻觉）

对企业用户而言，引入 AI 管理生产数据库最核心的三个顾虑是：**① 生产数据和表结构会不会泄露给大模型？② AI 会不会越权改库或绕过审批？③ 大模型产生幻觉乱报或胡写 SQL 怎么办？**

Yearning v4 从底层架构而不是提示词口号上回答了这三个问题，并配套完整的纵深防御体系。

---

## 一、结合 AI 后，如何保证生产数据与表结构零泄露？

Yearning v4 的 AI 预审、数据库 AI 助手与 SQL 性能诊断三条 AI 链路均受 **双轨隐私隔离（`default` 云端脱敏轨道 vs. `san` 内网原生轨道）** 严格治理：

### 1. 公有云模型（`default` 轨道）：六重防泄露机制

| 防护层 | 工程实现细节 |
| :--- | :--- |
| **① 字面值与注释物理剥离** | 在发往 LLM 之前，TiDB Parser / `pg_query_go` 将全部业务字面值（手机号、姓名、金额、`IN (...)` 列表）与 SQL 注释彻底剥离，替换为类型化占位符（`:val_str_1`、`:val_int_1`） |
| **② 标识符单轮内存匿名化** | `RunMapper` 与 `TurnValueMapper` 将真实库名、表名、列名映射为 `schema_1`、`table_1`、`column_1`；**映射表仅驻留在单次 HTTP 请求的服务端内存中，请求结束、失败或取消瞬间释放**，绝不写库、不写日志、不进缓存、不返回浏览器 |
| **③ 策略与经验文本同样脱敏** | 管理员编写的自定义技能、表级内部经验（`{{ref:table:["schema","table"]}}`）与历史上下文摘要在编译期统一匿名化，避免「SQL 脱敏了但规则描述泄密」 |
| **④ 发送前零泄漏硬断言（Fail-Closed）** | 发送请求前强制执行 `AssertZeroLeak` / `AssertNoLeak`：若检测到任何残余敏感标识、原始密码、`sk-` 密钥或 DSN 连接串，**立即中止外部调用并返回 `2005` / `6003`**，绝不降级明文发送 |
| **⑤ 绝不读取业务表行数据** | 所有只读元数据工具只查表结构、索引、估算统计与只读执行计划，严禁查询或采样任何业务表行数据 |
| **⑥ 授权视图按需还原 + 审计** | 持久化证据与模型输入永久保持匿名；仅草稿所有者、工单提交人与冻结审批人/执行人在页面查看报告时由服务端还原真实表列名，并写入 `review.identifier_displayed` 审计日志（`admin` 无隐式还原资格） |

### 2. 内网完全私有化模型（`san` 轨道）与跨轨道防回退

- 企业可在内网 GPU 服务器（RFC1918 私网段，受 `addrguard` DNS 重绑定与 SSRF 防护）通过 vLLM / Ollama 部署本地 `SAN-2B` / `SAN-4B` / `SQL-Audit-Nano` 模型，实现 **0 字节出内网**。
- `default` 与 `san` 绑定各自独立的隐私契约哈希（`PrivacyContractHashDefault` vs. `PrivacyContractHashSAN`）。即使配置了「内网主模型 + 公有云备用模型」的混合链，系统也会在发往每个端点前**从原始数据独立构造载荷**：**为内网 `san` 准备的未脱敏载荷绝不会在故障转移时回退发送给公有云 Provider**。

---

## 二、如何保证 AI 零侵入性（不改库、不越权、不破坏治理）？

Yearning v4 坚持 **「AI 只负责风险分析、草稿辅助与证据综合；结构化契约掌控全部审批、执行与审计」** 的不可违背边界：

| AI 模块 | 权限与侵入性硬约束 |
| :--- | :--- |
| **AI SQL 预审** | 仅允许调用 5 个受控只读工具（`check_syntax`、`describe_table`、`list_indexes`、`table_stats`、`safe_explain`）；只产出结构化 Finding，**不能审批工单、不能执行 SQL、不能跳过 Submission Gate** |
| **数据库 AI 助手** | **连 `SELECT` 和 `EXPLAIN` 都不执行**！绝不直连目标库跑用户 SQL、不采样表行数据；生成的草稿附带 `draft_id` + `revision` + `sql_hmac`，点击提单后必须重新鉴权、完整走通正式 AI 预审与多级审批 |
| **SQL 性能诊断** | **严禁使用或回退读写执行凭据**（只复用只读 `query`/`review` 连接池）；绝不自动安装 `pg_stat_statements` 扩展、不修改目标库参数、不重置统计计数器、不对生产库跑 `EXPLAIN ANALYZE`；优化建议的建索引 DDL 仍须走标准工单审批 |
| **外部 AI Agent Skill** | 以真实登录用户的会话与权限组身份调用 `/api/v4/*`，完整受 RBAC、双提交 CSRF、`Idempotency-Key`、`If-Match` 与提交门禁约束，绝无绕过通道 |

此外，平台实行 **提交即冻结** 与 **管理面不越权业务面（`admin_is_not_business_override = true`）**：工单一旦提交，含内置超级管理员 `admin` 在内的任何人都不能修改审批链、转交、加签或代执行。

---

## 三、如何防止 AI 幻觉（抗乱报、抗误拦、抗胡写 SQL）？

企业用 AI 审核最怕两种幻觉：**「假阳性误拦正常发版」** 与 **「假阴性漏放高危 SQL 或瞎写错语法」**。Yearning v4 通过五重工程护栏系统性消除幻觉影响：

1. **三类发现分权治理（防假阳性误杀 + 防假阴性放行）**：
   - **`policy_finding`（强制规则命中）**：48 条内置规则、绑定技能或作用域命中的 DBA 内部经验实际违规，一律**确定性归为 `Critical` 并阻断提交（`2008`）**，不依赖模型主观情绪打分。
   - **`proven_invalid_execution`（运行时证明无效）**：由可信 Go 运行时结合完整表结构与语句顺序证明必定执行失败（如删除不存在的表），确定性 `Critical` 阻断。
   - **`agent_observation`（模型自由观察）**：大模型的开放建议**强制封顶 `Medium`**，仅供审批人参考，**永远不会因模型幻觉独自阻断合法上线**。
2. **元数据缺失强制 `partial`（Fail-Closed）**：当目标库表结构缺失、截断或有效性为 `unknown` 时，状态锁定为 `partial` 阻断提交并给出原因，绝不允许模型在「看不见表」的情况下臆断放行。
3. **执行计划事实核验（`ValidateNoHallucinatedPlanFacts`）**：服务端校验 AI 输出引用的表名、索引名与执行计划事实确实来自本次只读工具的真实返回，拦截凭空捏造的索引或行数结论。
4. **强 Schema 输出校验 + 方言 AST 二次验真**：
   - AI 预审响应必须符合严格 JSON Schema，格式违规直接拦截（`2009 REVIEW_SCHEMA_INVALID`）。
   - 数据库 AI 助手生成的每一条 SQL 在展示前必须通过 **TiDB SQL Parser / `pg_query_go`** 方言 AST 解析、授权对象边界检查与类型化占位符核对；出现未知占位符、越权表列或语法错误直接拒绝生成草稿（`6004`）。
   - `INSERT`/`UPDATE` 缺 `NOT NULL` 必填列时强制向用户追问澄清，严禁编造业务值；SQL 优化严禁虚构未实测的性能倍率，改变语义时必须显式给出语义差异警示。
5. **自定义技能发布前 Eval 门禁**：管理员新建或修改自定义 Prompt 技能从 `draft` 启用时，系统执行内联 Eval 校验，防止含糊提示词污染线上预审。

---

## 四、平台纵深安全基线（加密 · 权限 · 网络 · 审计）

| 安全维度 | Yearning v4 生产级落地方案 |
| :--- | :--- |
| **静态加密（AEAD + AAD）** | 六用途隔离 `keyring.json`（`payload`、`sql_fingerprint`、`identifier_token`、`settings_impact_token`、`oidc_state`、`cursor_affinity`），所有密文采用 **AES-256-GCM** 并将 `deployment_id + 表名 + 主键 + 列名 + purpose` 绑定进 AAD，彻底防跨表/跨行密文替换；密钥文件强制 `0400/0440` 权限 |
| **数据库最小权限隔离** | ① 目标库分离 **只读凭据（预审/查询/诊断）** 与 **执行凭据（落库）**，强制校验 TLS 与不可伪造的 `identity_fingerprint`；② 元数据库 PostgreSQL 生产强制 **三角色隔离**（`yearning_app` 非超管、`yearning_migrator` 仅迁移、`yearning_retention` 仅清理） |
| **认证与防爆破** | 本地密码采用 **Argon2id** 哈希；内置 `admin` 连续 5 次输错锁定仅允许本机 TTY `./yearning --reset-admin-password` 重置；企业 SSO 强制 **LDAPS/StartTLS（禁止明文与跳过证书）** 与 **OIDC Authorization Code + PKCE (`S256`)** |
| **Web 与 SSRF 防护** | 全写接口强制 **双提交 CSRF (`X-CSRF-Token`)**、**UUIDv4 `Idempotency-Key`** 与 **`If-Match` 乐观锁**；全出站连接（AI Provider / Webhook / 数据源）经 **`addrguard`** 防 SSRF 与 DNS 重绑定；高危设置须经两步 **`Assess -> impact_token`** 确认 |
| **明文防泄密与可追溯审计** | 工单列表/详情默认不返 SQL 明文；查看须显式触发 **SQL Reveal**，写入二次审计并叠加 **`用户名 + 时间戳 + Trace ID` 水印**；通知与 SSE 事件帧严禁携带原始 SQL、字面值或查询行；追加写审计日志保留 **90 天**，工单与执行事实永久保留 |
| **容器与集群加固** | Distroless 多架构镜像，非 root UID `10001`/`65532`、`readOnlyRootFilesystem: true`、`allowPrivilegeEscalation: false`、`capabilities.drop: ["ALL"]` + Kubernetes NetworkPolicy 最小放行 |
