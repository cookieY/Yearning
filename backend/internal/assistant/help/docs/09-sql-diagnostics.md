---
id: help-sql-diagnostics
title: SQL Performance Diagnostics, Top SQL & 4-Section AI Diagnosis (Admin)
source_ref: docs/iterations/v4.0.0/features/sql-performance-diagnostics-prd.md
route_path: /admin/diagnostics
admin_only: true
keywords: diagnostics, slow, slow sql, performance, top sql, digest, performance_schema, pg_stat_statements, snapshot, ai report, before after, index_ddl, latency, chart, brush, index, 6101, 6102, 6103
summary: Administrators (builtin_admin) can directly access SQL Performance Diagnostics (/admin/diagnostics) without permission group or flow grants to inspect Top SQL digest metrics across 6 interactive trend charts, trigger snapshots, and generate 4-section AI diagnostic reports.
---

## Steps
- Enable SQL Diagnostics on the target datasource in `/admin/datasources` (requires `performance_schema` on MySQL or `pg_stat_statements` on PostgreSQL).
- Open `/admin/diagnostics`, select the datasource and time range, or drag the Brush time selector on the 6 overview trend charts (or click Collect Snapshot Now).
- Click any Top SQL row to open its detail drawer with 3 statement trend charts, click AI Diagnose for a 4-section report, and send index/SQL recommendations to AI Precheck and Change Orders.

## Detailed Guide
- **Enabling SQL Diagnostics & Database Prerequisites (`/admin/datasources`)**:
  - Enable diagnostics per datasource in `/admin/datasources` (configure sampling interval, Top-N statement cap, and snapshot retention days).
  - **MySQL Prerequisite**: `performance_schema = ON` with `events_statements_summary_by_digest` enabled. Because `performance_schema` is a static MySQL parameter, managed cloud consoles (e.g., Tencent Cloud CDB) require enabling it in parameter settings and restarting the instance. On restricted cloud/SaaS instances where `performance_schema` cannot be enabled, disable automatic SQL Performance Diagnostics for that datasource and use the AI Assistant for manual SQL tuning—AI Precheck, Change Orders, and SQL Console do not depend on `performance_schema`.
  - **PostgreSQL Prerequisite**: `pg_stat_statements` extension installed and preloaded in `shared_preload_libraries` (`6102` if missing).
- **6 Overview Trend Charts & Top SQL Ranking (`/admin/diagnostics`)**:
  - Select an enabled datasource and time window (`1h`, `6h`, `24h`, `7d`, or custom range) to view **6 overview charts**: Total Execution Time, Execution Calls, Average Latency, Rows Examined (MySQL) / Shared Blocks Read (PG), Lock Wait Time, and Errors/Warnings.
  - Drag the interactive **Brush time selector** beneath the overview charts to zoom into any spike and filter the Top SQL table below.
  - Click **Collect Snapshot Now** to trigger an immediate on-demand snapshot (`6103` if clicked again during the cooldown window).
- **Statement Detail Drawer & 4-Section AI Diagnosis**:
  - Click any SQL digest row to open its detail drawer showing the normalized digest SQL, **3 statement-level trend charts**, and execution statistics.
  - Click **AI Diagnose** (requires an enabled AI Provider; fails closed with `2011 AI_PROVIDER_FAILED` or `2010 AI_DAILY_BUDGET_EXHAUSTED` rather than fabricating heuristic fallback reports) to generate a structured 4-section optimization report:
    1. **Root Cause Analysis**
    2. **Index Recommendations**
    3. **SQL Rewrite Recommendations**
    4. **Verification Steps**
  - Recommended `CREATE INDEX` or SQL changes can be sent directly to formal **AI Precheck** and submitted as a Change Order in `/changes/new`.
