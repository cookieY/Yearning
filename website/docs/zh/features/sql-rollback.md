# 回滚保护与一键派生回滚工单实战

Yearning v4 的自动回滚彻底摆脱了对 MySQL Binlog 抓取与 PostgreSQL WAL 逻辑复制的依赖，支持在 **MySQL 族与 PostgreSQL 族** 上通过「事务内前像采集 + AST 逆向生成」实现受治理回滚。

## 1. 提交变更时如何开启回滚保护

1. 在 `/changes/new` 编写 DML 或 DDL 语句时，勾选 **启用回滚保护（Rollback Protection）**。
2. **DML 表前提检查**：待修改的目标表必须具备 **主键（Primary Key）** 或 **非空唯一键（Unique Key）**，否则无法唯一锚定被改行。
3. **注意 100,000 行硬上限（Fail-Closed 保护）**：
   - 单条 DML 语句及单笔工单最多采集 **`100,000` 行** 前像。
   - **触发超限时怎么表现？** 如果你的 `UPDATE` / `DELETE` 实际命中超过 100,000 行，Yearning 会**立刻中止并回滚整个正向事务**（`affected_rows_exceed_rollback_limit`），保证绝不会在缺少完整回滚证据的情况下把数据改掉。遇到此报错时，请按主键范围拆分成多批小工单。

## 2. 哪些语句支持自动回滚？（快速对照表）

| 语句类型 | 是否支持 | 生成的逆向回滚逻辑 | 注意事项 |
| :--- | :---: | :--- | :--- |
| `INSERT` | ✅ | 按新写入的主键/唯一键生成逆向 `DELETE` | 需表有主键或非空唯一键 |
| `UPDATE` | ✅ | 同事务采样旧行值，生成恢复原值的逆向 `UPDATE` | PG 18+ 用 `RETURNING OLD.*`；MySQL/旧 PG 用 `SELECT ... FOR UPDATE` |
| `DELETE` | ✅ | 同事务采样被删整行，生成逆向 `INSERT` | 同样受 `100,000` 行上限保护 |
| `CREATE TABLE` | ✅ | `DROP TABLE` | 仅恢复结构 |
| `DROP TABLE` | ✅ | 按执行前表与二级索引元数据重建 `CREATE TABLE` | **仅恢复表结构，不恢复已删除的历史行数据** |
| `ALTER TABLE ADD/DROP/MODIFY COLUMN` | ✅ | 逆向增删列或恢复原列类型、空值约束与默认值 | 删除列回滚仅恢复列定义，不恢复历史列值 |
| `CREATE/DROP INDEX`、`RENAME`、增删约束 | ✅ | 逆向建删索引 / 逆向重命名 / 逆向约束 | 自动记录原索引与约束定义 |
| 进程内 `gh-ost` `ALTER TABLE` | ✅ | 采集 `gh-ost` 切表前后结构快照生成逆向 `ALTER TABLE` | 切表完成后自动封存 |
| `TRUNCATE TABLE`、`DROP DATABASE/SCHEMA`、`DROP ... CASCADE` | ❌ | 标记为 `rollback_status = unsupported` | 高危清空操作需依赖数据库物理备份/PITR 恢复 |

## 3. 上线后发现问题：一步步发起回滚工单

Yearning v4 **禁止跳过审批直接一键无审核回滚**，请按以下 4 步安全回滚：

1. **打开已执行成功的原工单**：在 `/changes/mine` 找到目标工单进入 `/changes/orders/:id`，确认右上角显示 **`rollback_status = available`**。
2. **（推荐）先核对加密回滚 SQL**：点击 **查看回滚 SQL**（触发 `purpose = rollback-order-sql` 的带水印审计 Reveal），仔细检查逆向 SQL 是否符合预期。
3. **点击「创建回滚工单」**：系统一键生成绑定 `parent_order_id` 的派生回滚草稿（每笔原工单最多派生 1 笔有效回滚单，重复创建会提示 `3006`）。
4. **走完预审、审批与执行**：
   - 在派生工单页点击 **运行 AI 预审**（核验当前表结构与数据行是否发生漂移；若发现原修改行已被后续业务再次改动，状态会提示 **`conflicted`** 提醒人工核查）。
   - 提交审批通过后，由冻结执行人点击 **执行**，完成后原工单状态更新为 `rolled_back`。

## 4. 7 种回滚状态速查

| `rollback_status` | 含义与下一步操作 |
| :--- | :--- |
| `available` | 回滚 SQL 已 AES-256-GCM 加密封存完毕，可直接点击「创建回滚工单」 |
| `unsupported` | 提交时未勾选回滚保护，或属于 `TRUNCATE` 等不可逆操作 |
| `evidence_incomplete` | 前像采集未完整完成（如异常中断），系统禁止自动派生回滚单 |
| `rollback_in_progress` | 已派生回滚工单，正在审批或执行中（点关联链接查看进度） |
| `rolled_back` | 关联回滚工单已成功执行 |
| `rollback_failed` | 关联回滚工单执行失败，请排查报错详情 |
| `conflicted` | 检测到正向执行后目标行已被其他业务修改，请先核实再决定是否人工订正 |
