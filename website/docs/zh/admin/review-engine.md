# AI Provider、48 条内置审核规则与内部经验库

本手册指导管理员完成 **AI 模型接入（公有云与内网私有化双轨）**、**单日费用预算控制**、**48 条内置审核技能选配**，以及 **自定义 Prompt 技能与内部 DBA 经验库** 的编写。

## 1. 配置 AI Provider（`/admin/review-engine/providers`）

### 场景 A：接入公有云 OpenAI 兼容模型（`default` 零泄漏轨道）

适用于 OpenAI、DeepSeek、阿里云百炼（通义千问）、OpenRouter 等公网 HTTPS 服务：

| 配置项 | 示例值 | 说明 |
| :--- | :--- | :--- |
| **名称** | `deepseek-cloud-primary` | 唯一标识 |
| **Base URL** | `https://api.deepseek.com/v1` | 公网地址**强制 `https://`**，不支持 HTTP |
| **Model Name** | `deepseek-chat`（或 `gpt-4.1-mini` / `qwen-plus`） | 目标模型标识 |
| **API Key** | `sk-xxxx` | AES-256-GCM 加密存储，保存后永不回显 |
| **System Prompt Profile** | **`default`**（或留空自动识别） | 强制启用单轮内存匿名化 + `AssertZeroLeak` 零泄漏断言 |
| **优先级（`selection_priority`）** | `1`（数值越小越优先） | 可再配一个优先级 `2` 的同轨道备用 Provider 实现容灾切换 |
| **生成参数** | `temperature: 0.1`、`max_tokens: 4096`、`reasoning_effort: medium` | 建议低温度保证审核稳定；修改会刷新 `PlanHash` |

### 场景 B：接入内网自部署本地模型（`san` 原生免脱敏轨道）

适用于在内网 GPU 服务器通过 **vLLM / Ollama** 部署的专用 SQL 审核模型（如 `SAN-4B`、`SQL-Audit-Nano`）：

| 配置项 | 示例值 | 说明 |
| :--- | :--- | :--- |
| **Base URL** | `http://10.20.4.18:8000/v1` | 内网 RFC1918 地址（`10.x` / `172.16-31.x` / `192.168.x`）允许 `http://` 与 `https://`，受 `addrguard` DNS 重绑定防护 |
| **Model Name** | `san-4b` 或 `sql-audit-nano` | 系统自动识别进入 `san` 轨道（过渡期旧名 `xmodel-v3.1` / `san-v3.1` 会自动保持在 `default` 脱敏轨道） |
| **System Prompt Profile** | **`san`** | 启用精简无版本号三合一原生提示词，保留真实表名/列名/列注释，对 >256 字符字面值自动截断 |

:::danger 跨轨道防泄漏保障
即使你同时配置了优先级 `1` 的内网 `san` 模型和优先级 `2` 的公有云 `default` 模型，当内网模型超时时，系统会以目标端点的轨道从原始数据重新构建并执行全量脱敏 + 零泄漏校验，**绝不会把 `san` 的明文 SQL 载荷直接回退发给公有云 Provider**。
:::

## 2. 参考计价与单日全局 AI 预算（`/admin/review-engine/ai-budget`）

1. 在 Provider 表单第 3 栏 **参考计价** 中，按 `CNY` 或 `USD` / 百万 Token 配置生效日期与分时时段（例如 DeepSeek 夜间优惠时段 `[00:30, 08:30)`），分别填写普通输入、输出、缓存读取（`cached_tokens`）与缓存写入（`cache_write_tokens`）单价。
2. 在 **AI 预算（`/admin/review-engine/ai-budget`）** 中设置 **全局单日预算上限** 与 **告警百分比阈值**（例如每日 `¥100`，达 `80%` 触发通知告警）。
3. 开启 **预算强制阻断（`enforced`）** 需要两步确认（点击后系统弹窗展示影响范围并下发 `impact_token`）。达到预算上限时新预审返回 `2010 AI_DAILY_BUDGET_EXHAUSTED`。

## 3. 48 条内置审核技能分类速查（`/admin/review-engine/skills`）

内置技能锁定定义与名称（不可删除），管理员可按业务规范逐条开关并调节 `{{参数}}` 阈值。以下按场景列出核心内置规则：

### DML 安全与批量限制

| 技能标识（`finding_key`） | 检查内容 | 可调参数 |
| :--- | :--- | :--- |
| `builtin.dml-require-where` | `UPDATE` / `DELETE` 必须携带 `WHERE` 条件 | 无 |
| `builtin.forbid-truncate` | 禁止 `TRUNCATE TABLE` | 无 |
| `builtin.max-affect-rows` | 基于 `safe_explain` 预估影响行数不得超过阈值 | `{{max_affect_rows}}` |
| `builtin.dml-insert-explicit-columns` | `INSERT` 必须显式列出目标列名 | 无 |
| `builtin.dml-insert-max-batch-rows` | 单条 `INSERT ... VALUES` 批量行数上限 | `{{max_batch_rows}}` |
| `builtin.dml-forbid-limit` / `dml-forbid-order-by` | `UPDATE`/`DELETE` 禁止使用 `LIMIT` / `ORDER BY` | 无 |
| `builtin.dml-forbid-subselect` | DML 禁止内嵌 `SELECT` 子查询 | 无 |
| `builtin.dml-where-not-null-literal` / `dml-insert-forbid-null` | 禁止 `WHERE` 出现 `= NULL` 或 `INSERT` 显式写入 `NULL` | 无 |
| `builtin.max-in-value-count` | 单条语句 `IN (...)` 列表值数量上限 | `{{max_in_values}}` |
| `builtin.max-group-statement-count` / `max-statement-size` | 同构折叠组语句数上限 / 单语句字节上限 | `{{max_group_statements}}` / `{{max_statement_bytes}}` |
| `builtin.ddl-implicit-type-conversion` | `WHERE` 字面值类型必须与目标列类型一致（防隐式转换失效索引） | 无 |

### 建表、主键与列规范（DDL）

| 技能标识（`finding_key`） | 检查内容 | 可调参数 |
| :--- | :--- | :--- |
| `builtin.ddl-require-primary-key` | `CREATE TABLE` 必须声明主键 | 无 |
| `builtin.ddl-pk-named-id` / `ddl-pk-integer` / `ddl-pk-auto-increment` | 主键命名为 `id` / 整数类型 / 自增 | 无 |
| `builtin.ddl-autoincrement-init-one` / `ddl-autoincrement-unsigned` | 自增初始值为 `1` / 声明 `UNSIGNED` | 无 |
| `builtin.ddl-require-table-comment` / `ddl-require-column-comments` | 建表与全部列必须带注释 | 无 |
| `builtin.ddl-column-nullable-check` / `ddl-column-default-check` | 列应显式声明 `NOT NULL` 并提供默认值 | 无 |
| `builtin.ddl-max-char-length` / `ddl-max-table-name-length` | `CHAR`/`VARCHAR` 长度与表名长度上限 | `{{max_char_length}}` / `{{max_varchar_length}}` / `{{max_table_name_length}}` |
| `builtin.ddl-float-double-decimal` / `ddl-special-type` | 建议用 `DECIMAL` 替代 `FLOAT`/`DOUBLE`；特殊类型复核 | 无 |
| `builtin.ddl-charset-allowlist` / `ddl-collation-allowlist` | 字符集与排序规则白名单（如 `utf8mb4`） | `{{allowed_charsets}}` / `{{allowed_collations}}` |
| `builtin.ddl-identifier-keyword` | 禁止使用 SQL 保留关键字作为表名/列名 | 无 |

### 高危删改与索引治理（DDL）

| 技能标识（`finding_key`） | 检查内容 | 可调参数 |
| :--- | :--- | :--- |
| `builtin.ddl-forbid-drop-database` / `ddl-forbid-drop-table` / `ddl-forbid-drop-other` | 禁止 `DROP DATABASE` / `DROP TABLE` / 删视图索引触发器 | 无 |
| `builtin.ddl-forbid-across-db-rename` | 禁止跨库 `RENAME TABLE` | 无 |
| `builtin.ddl-forbid-column-type-change` / `ddl-forbid-column-position-change` | 禁止 `ALTER` 修改列类型或 `AFTER`/`FIRST` 挪动列位置 | 无 |
| `builtin.ddl-forbid-foreign-key` / `ddl-allow-partition` | 禁止外键 / 分区表需复核 | 无 |
| `builtin.ddl-allow-create-view` / `ddl-view-forbid-select-star` | 视图创建复核 / 禁止视图中使用 `SELECT *` | 无 |
| `builtin.ddl-forbid-unnamed-index` / `ddl-index-name-prefix` | 索引必须显式命名且以 `idx_` 或 `uniq_` 开头 | 无 |
| `builtin.ddl-max-index-count` / `ddl-max-index-key-parts` | 单表索引总数上限 / 联合索引最大列数 | `{{max_index_count}}` / `{{max_index_key_parts}}` |
| `builtin.ddl-max-affect-rows` | DDL 作用目标表行数上限（超限提示改用 `gh-ost`） | `{{max_ddl_affect_rows}}` |
| `builtin.ddl-multi-alter` / `ddl-multi-ddl` | 单工单禁止多条 `ALTER` 或多条 DDL | 无 |

## 4. 编写自定义审核技能与内部经验库

### 示例 1：新建自定义 Prompt 技能（`/admin/review-engine/skills`）

例如要求所有业务表必须包含审计时间字段 `created_at` 与 `updated_at`：
- **技能名称**：`custom.require-audit-timestamps`
- **适用引擎**：`all`（或指定 `mysql` / `postgresql`）
- **规则文本（`knowledge_text`）**：
  ```text
  Rule: Every CREATE TABLE statement in production schemas must define both created_at and updated_at timestamp columns declared NOT NULL with a default value.
  Output a finding whenever either column is missing or nullable.
  ```
- 保存并点击 **启用**（启用时系统会自动执行内联 Eval 门禁校验结构化输出合法性），再把它勾选进 `/admin/rule-sets` 规则集即可生效。

### 示例 2：沉淀表级 DBA 内部经验（`/admin/review-engine/knowledge`）

对于特定核心大表的专项约束（例如账务流水表禁止新增非唯一二级索引），无需写全局技能，可直接在 **内部经验库** 创建 **表级作用域（`table`）** 条目：
- **作用域**：`table`（选择目标数据源、库名 `billing`、表名 `ledger_entries`）
- **零泄漏类型化对象引用写法**：
  ```text
  Table {{ref:table:["billing","ledger_entries"]}} receives >5,000 writes/sec.
  Do not add secondary indexes on {{ref:column:["billing","ledger_entries","remark"]}} or alter column types without DBA capacity sign-off.
  ```
- 在公有云 `default` 轨道下，编译器会自动把 `{{ref:...}}` 校验并替换为本次预审的匿名 Token（如 `table_1` / `column_4`），**既让大模型准确命中该表规则，又保证真实表名绝不外泄**。

:::warning 强制命中必阻断原则
任何**已启用**且绑定到流程的审核技能或作用域命中的内部经验条目，一旦实际违规即固定归类为 `policy_finding`（有效级别 **`critical`**）并**直接阻断提交（`2008`）**；如果某条规则只想作建议不阻断，请不要将其作为强制规则启用，改由模型常规观察（最高 `medium`）提醒审批人。
:::
