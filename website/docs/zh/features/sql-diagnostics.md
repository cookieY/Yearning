# Top SQL 性能诊断实战（管理员 `/admin/diagnostics`）

SQL 性能诊断是 Yearning v4.0 面向 DBA 与管理员打造的无侵入慢 SQL 治理闭环。它严格复用数据源已配置的 **只读 `query`/`review` 连接池**，绝不回退读写执行凭据、绝不自动安装数据库扩展、不修改统计配置、不重置计数器，也不对生产库执行 `EXPLAIN ANALYZE`。

:::tip 寻找工作台右下角对话助手？
面向全体用户的 **受控数据库 AI 助手（平台咨询 + 自然语言脱敏写 SQL + 两阶段衔接提单 + SQL 优化）** 请查看独立专题手册：[数据库 AI 助手实战指南](/features/ai-assistant)。
:::

## 第 1 步：在目标库确认开启原生语句统计

**MySQL 5.7+ / TiDB**：确认 `performance_schema` 已开启且只读账号有查询权限（见 [数据源建权 SQL](/admin/datasources-and-grants)）：

```sql
-- 核查是否已开启（返回 ON 即就绪）
SHOW VARIABLES LIKE 'performance_schema';
SELECT COUNT(*) FROM performance_schema.events_statements_summary_by_digest;
```

**PostgreSQL 14+**：确认 `pg_stat_statements` 已预加载并启用：

```sql
-- postgresql.conf 中需配置：shared_preload_libraries = 'pg_stat_statements'
-- 在目标业务库由 DBA 手工执行一次创建扩展（Yearning 绝不自动创建）：
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

-- 核查只读账号 yearning_ro 可正常读取：
SELECT count(*) FROM pg_stat_statements;
```

## 第 2 步：在数据源中启用诊断采集

进入 `/admin/datasources` 编辑目标数据源，打开 **SQL 性能诊断** 开关，设置采样间隔、Top-N 采集条数与保留天数：
- 常规采集仅抓取语句指纹聚合指标，**跳过原始 SQL 文本**以降低对目标库的影响。
- 查看详情或发起 AI 诊断时按需读取参数化模板并以 **AES-256-GCM** 加密存储（默认细粒度时间桶保留 **14 天**，模板与 AI 诊断报告保留 **30 天**）。
- 保存后直接点击数据源行内的 **SQL 诊断** 快捷入口跳转到 `/admin/diagnostics?datasource_id=<uuid>`。

## 第 3 步：6 图联动定位性能毛刺 + 一键优化闭环

1. 在 `/admin/diagnostics` 顶部选择时间窗（`1h` / `6h` / `24h` / `7d` 或自定义范围），或点击 **立即采集快照**（带防高频冷却保护，频繁点击返回 `7006`）。
2. 观察 **6 张联动趋势图**：
   - 总执行耗时趋势
   - 调用次数趋势
   - 平均延迟趋势
   - 扫描行数（MySQL）/ 共享块读取（PostgreSQL）
   - 锁等待时间
   - 错误与告警趋势
3. **拖动图表下方的 Brush 时间刷** 精准框住延迟抖动峰值区间，下方 **Top SQL 热点排行**、**性能退化排行**与**新增高负载 SQL** 会实时联动下钻（至少 3 个有效差分桶且 `Δcalls >= 10` 时自动计算退化与优化前后对比结论）。
4. 点击高耗时 SQL 指纹行打开详情抽屉，查看 **3 张单语句趋势图** 及参数化模板，点击 **AI 诊断**（统计数值原样入模，SQL 模板与单表元数据经请求级内存脱敏且保留原生 `?` / `$1` 占位符），生成结构化 **四段式优化报告**：
   - **① 根因分析（Root Cause Analysis）**
   - **② 索引建议（Index Recommendations）**
   - **③ SQL 改写建议（SQL Rewrite Recommendations）**
   - **④ 验证步骤（Verification Steps）**
5. 确认其中的 `CREATE INDEX` 或 SQL 优化建议后，点击 **送入变更预审**，一键推入正式 **AI SQL 预审（`StartEphemeralRun`）** 并创建标准变更工单（`SubmitOrderV2`）；索引上线后回到该 SQL 详情抽屉即可查看 **优化工单前后对比趋势图**。
