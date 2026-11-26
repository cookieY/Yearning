# 4 位业务错误码与高频排障手册

本页前半部分汇总部署启动期最常见报错，后半部分提供 `/api/v4/*` 全量 8 个号段（`10xx`–`70xx`）业务错误码排障表。

## 高频部署与启动报错速查

| 报错现象 / 日志关键词 | 根因 | 精确修复方法 |
| :--- | :--- | :--- |
| `non-loopback listen requires explicit environment` | 监听了 `0.0.0.0:8080` 但未设 `YEARNING_ENVIRONMENT` | 显式设置 `YEARNING_ENVIRONMENT=development` 或 `production` |
| `production requires retention.database_dsn` | 生产模式未配置独立清理角色 DSN | 注入 `YEARNING_RETENTION_DATABASE_DSN`（使用 `yearning_retention` 角色） |
| `sslmode ... must be verify-ca or verify-full` | 生产模式误用了 `sslmode=disable/prefer/require` | 改为 `YEARNING_DATABASE_SSLMODE=verify-full` 并提供 `SSLROOTCERT` |
| `YEARNING_ALLOW_LOOPBACK_TARGETS ... must be disabled in production` | 生产环境开启了本机环回绕过变量 | 从生产环境变量与 K8s 清单中彻底删除该变量 |
| `keyring file mode ... exceeds maximum` | `keyring.json` 文件权限过宽（如 `0644`） | 执行 `chmod 0400 keyring.json`（K8s 需设 `defaultMode: 0400` + `fsGroup: 10001`） |
| 网页审批/查询队列不实时刷新或每 60 秒断开 | 反代（Nginx/Ingress）开启了响应缓冲或超时过短 | 为 `/api/v4/events/stream` 添加 `proxy_buffering off; proxy_read_timeout 3600s;` |
| 创建 `change_review` 流程报 `1001` | 尚未创建规则集或未绑定 `rule_set_id` | 先在 `/admin/rule-sets` 创建至少 1 套规则集再建流程 |
| 编辑数据源保存报 `1001` | 编辑时不回显 `version_constraint` | 每次编辑保存确认重新填入如 `>=8.0,<9.0` |
| 内置 `admin` 连续输错 5 次报 `1102` 锁定 | 触发防爆破锁定，网页端不可解锁 | 在服务器终端执行 `./yearning --reset-admin-password` 交互重置 |

---

## `10xx` 通用校验、并发、幂等与配额

| 错误码 | 标识 | 常见原因与处理方式 |
| :--- | :--- | :--- |
| `1001` | `VALIDATION_FAILED` | 参数校验失败（数据源缺 `version_constraint`、变更流程缺 `rule_set_id`、通知模板含未知变量或 SQL 片段） |
| `1002` | `RESOURCE_NOT_FOUND` | 目标资源不存在或当前用户无权查看 |
| `1003` | `VERSION_CONFLICT` | 乐观锁 `If-Match` 版本过期，刷新页面获取最新版本号后重试 |
| `1004` | `CONCURRENT_MODIFICATION` | 另一请求正在并发修改同一对象，稍后重试 |
| `1005` | `RESOURCE_ALREADY_EXISTS` | 唯一名称冲突（用户名、数据源名、流程名、规则集名等重复） |
| `1006` | `RESOURCE_REFERENCED` | 资源仍被下游引用，禁止物理删除，请改为禁用或先解除引用 |
| `1007` | `IDEMPOTENCY_KEY_REUSED` | 同一 `Idempotency-Key` 复用了不同请求体，请生成新的 UUIDv4 |
| `1008` | `IDEMPOTENCY_IN_PROGRESS` | 相同幂等键请求仍在执行中，等待完成 |
| `1009` | `CURSOR_EXPIRED` | 分页游标已失效，回到首页重新拉取 |
| `1010` | `INVALID_STATE_TRANSITION` | 当前状态机不允许该动作（例如工单已被撤回或已完成） |
| `1011` | `PRECONDITION_REQUIRED` | 高危设置修改须先调用 `Assess` 接口获取一次性 `impact_token` |
| `1012` | `SYSTEM_CAPACITY_EXHAUSTED` | 达到配额上限（例如 AI 助手单用户最多 50 个活跃会话，删旧会话即可释放） |

## `11xx` 身份认证与模板引用保护

| 错误码 | 标识 | 处理方式 |
| :--- | :--- | :--- |
| `1101` | `INVALID_CREDENTIALS` | 账号密码错误或 LDAP/OIDC 认证失败 |
| `1102` | `ADMIN_PASSWORD_LOCKED` | 内置 `admin` 连续输错 5 次锁定，须执行 `./yearning --reset-admin-password` |
| `1103` | `ADMIN_IMMUTABLE` | 内置超级管理员禁止禁用、降权或删除 |
| `1104` | `USER_HAS_ACTIVE_ORDERS` | 用户仍有未终结工单或活跃查询授权，禁止删除 |
| `1105` | `USER_REFERENCED_BY_TEMPLATE` | 用户仍在流程模板中担任审批人或执行人，先从流程移除 |
| `1106` / `1107` | `FLOW_REFERENCED` / `DATASOURCE_REFERENCED` | 流程或数据源仍被引用，请改为禁用 |
| `1109` | `PASSWORD_MANAGED_BY_IDENTITY_SOURCE` | LDAP/OIDC 外部用户不可在 Yearning 内修改密码 |

## `20xx` AI SQL 预审、预算与提交门禁

| 错误码 | 标识 | 处理方式 |
| :--- | :--- | :--- |
| `2001` | `REVIEW_OUTDATED` | SQL、数据源、规则或 Provider 变更，或 30 分钟预审快照过期，重新运行 AI 预审 |
| `2002` | `SQL_BATCH_TOO_LARGE` | 超过 `32 MiB` / `100,000` 语句 / 单条 `512 KiB` 上限，拆分工单 |
| `2003` | `SQL_PARSE_FAILED` | 方言语法解析失败，按返回的精确行列号修正 SQL |
| `2004` | `SQL_FINGERPRINT_FAILED` | 指纹计算失败或混写 DML 与 DDL，拆分为纯 DML / 纯 DDL 工单 |
| `2005` | `SQL_ANONYMIZATION_FAILED` | 脱敏失败或检测到原始凭据/密钥，移除敏感信息后重试 |
| `2006` | `TOO_MANY_FINGERPRINTS` | 超过单工单 `1,000` 个不同结构指纹组上限，拆分工单 |
| `2007` | `REVIEW_COVERAGE_INCOMPLETE` | 预审尚未覆盖全部分组或处于 `partial`，等待或修复元数据后重跑 |
| `2008` | `REVIEW_BLOCKED_BY_RISK` | 命中强制规则或存在 `critical` 风险阻断提交，根据建议修改 SQL 后重跑预审 |
| `2009` | `REVIEW_SCHEMA_INVALID` | 模型返回未符合结构化输出 Schema，重试或更换更稳定的 Provider 模型 |
| `2010` | `AI_DAILY_BUDGET_EXHAUSTED` | 全局单日 AI 预算耗尽，由管理员在 `/admin/review-engine/ai-budget` 调整 |
| `2011` | `AI_PROVIDER_FAILED` | 无可用 AI Provider 或全部调用超时失败，检查 `/admin/review-engine/providers` |
| `2012` | `REVIEW_TOOL_FAILED` | 目标库只读元数据探测失败，检查数据源 `review` 凭据权限与网络连通性 |
| `2013` / `2014` | `SUBMISSION_GATE_FAILED` / `FLOW_GRANT_REVOKED` | 未通过门禁或流程授权已被撤销 |

## `30xx` 审批、执行、`gh-ost` 与结果核验

| 错误码 | 标识 | 处理方式 |
| :--- | :--- | :--- |
| `3001` | `NOT_FROZEN_ACTOR` | 当前用户不在该阶段冻结审批人或执行人快照中 |
| `3002` | `APPROVAL_ALREADY_DECIDED` | 该审批节点已由同级其他审批人完成决策 |
| `3003` / `3004` | `EXECUTION_ALREADY_STARTED` / `EXECUTION_RETRY_FORBIDDEN` | 执行已启动或当前状态禁止重试 |
| `3005` | `EXECUTION_RESULT_UNKNOWN` | 提交瞬间网络中断结果未知，冻结执行人查库后人工提交核验结论 |
| `3006` | `EXECUTION_PREFLIGHT_FAILED` | 执行前漂移或凭据预检失败 |
| `3007` / `3008` / `3009` | `SCHEDULE_OUT_OF_RANGE` / `OUTSIDE_EXECUTION_WINDOW` / `MISSED_SCHEDULE` | 预约时间非法、超出每周允许执行时段或错过 15 分钟宽限窗口 |
| `3010` | `DATASOURCE_UNAVAILABLE` | 目标数据源不可达，检查主机、端口与 TLS 证书 |
| `3011` | `GH_OST_UNSUPPORTED` | 目标表或语句不满足进程内 `gh-ost` 在线改表条件（例如非原生 MySQL 5.7+） |
| `3012` | `MANUAL_VERIFICATION_EVIDENCE_REQUIRED` | 核验 `result_unknown` 必须填写非空核验证据说明 |

## `40xx` 查询、`50xx` 迁移、`60xx` AI 助手与 `70xx` SQL 诊断

| 域 | 关键错误码与处理 |
| :--- | :--- |
| **`40xx` 查询** | `4001`/`4002` 未获授权需申请 Query Grant；`4003` 无导出权；`4004`/`4005` 授权被撤销或过期；`4007`/`4011` 仅允许单条只读查询；`4009` 超时需加索引或缩小 `LIMIT`；`4010` 游标失效重新查询 |
| **`50xx` v3 迁移** | `5001` 源库仍有未闭环旧工单；`5002` 未跑 Dry-Run；`5003` 权限映射未全部确认；`5004` 源版本不支持；`5005` 核验不变量失败 |
| **`60xx` AI 助手** | `6001` 超出数据库范畴或请求 `TRUNCATE`/`DROP DATABASE`；`6002` 仅只读权请求了 DML/DDL；`6003` 输入含明文密钥；`6004` 返回 SQL 未过 AST 校验；`6005` 草稿未预审或已修改；`6006` 超上下文预算；`6007` 正在流式输出 |
| **`70xx` SQL 诊断** | `7001` 仅管理员可访问；`7002` 未启用或缺少只读凭据；`7003` 引擎不支持；`7004` 未开启 `performance_schema`/`pg_stat_statements`；`7005` 只读账号缺统计视图权限；`7006` 手动采样冷却中；`7007` 目标库负载熔断；`7008` 无可用模板或 AI Provider |
