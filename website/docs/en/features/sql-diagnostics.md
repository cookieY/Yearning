# Top SQL Performance Diagnostics (Admin `/admin/diagnostics`)

SQL Performance Diagnostics is Yearning v4.0's non-intrusive slow-SQL governance loop for platform administrators. It strictly reuses existing **read-only `query`/`review` connection pools** (never falling back to execution credentials, never installing extensions, never modifying database configuration, and never resetting statistics counters).

:::tip Looking for the workspace chat launcher?
See the dedicated guide for the **Scoped Database AI Assistant** (platform help, metadata-grounded natural-language SQL generation, AST restoration, and two-stage order submission): [Database AI Assistant Guide](/en/features/ai-assistant).
:::

## Step 1: Verify Native Statement Statistics on Target DB

- **MySQL 5.7+ / TiDB**: Ensure `performance_schema = ON` and grant `SELECT ON performance_schema.events_statements_summary_by_digest` to `yearning_ro`.
- **PostgreSQL 14+**: Preload `pg_stat_statements` in `shared_preload_libraries`, run `CREATE EXTENSION IF NOT EXISTS pg_stat_statements;`, and grant `pg_read_all_stats` to `yearning_ro`.

## Step 2: Enable Collection & Drill Down Across 6 Linked Charts

1. Enable SQL Diagnostics per datasource in `/admin/datasources` (14-day fine-grained time buckets, 30-day AES-256-GCM encrypted templates and AI reports).
2. Open `/admin/diagnostics`, inspect the **6 linked overview charts** (Total Execution Time, Calls, Average Latency, Rows Examined / Shared Blocks Read, Lock Wait Time, Errors/Warnings), and drag the **Brush selector** to zoom into any spike.
3. Click a hotspot statement digest row to open its **3 statement trend charts**, run **AI Diagnose** (in-memory template/metadata desensitization preserving `?`/`$1` placeholders) to receive the **4-section report** (Root Cause Analysis, Index Recommendations, SQL Rewrite Recommendations, Verification Steps), and push recommended `CREATE INDEX` DDL directly into formal AI Precheck and Change Orders.
