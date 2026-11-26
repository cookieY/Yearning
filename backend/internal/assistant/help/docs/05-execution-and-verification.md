---
id: help-execution-verify
title: Order Execution, Scheduled Windows & Result-Unknown Verification
source_ref: docs/features/05-execution-osc-prd.md
route_path: /orders
admin_only: false
keywords: execute, execution, verify, verification, result_unknown, scheduled, timeout, network, uncertain, confirm, confirm_succeeded, confirm_failed, lease, schedule, osc, gh-ost, pt-osc, pt-online-schema-change, online ddl, 2004, 2006, 2013, 3001, 3002, 3003, 3011, 3012, 3013
summary: After approval, execute change orders in /changes/orders/:id (/orders) or wait for the 15-minute scheduled window; track DML transactions or built-in gh-ost Online Schema Change, and resolve result_unknown states via manual verification (confirm_succeeded / confirm_failed).
---

## Steps
- Open the order detail page `/changes/orders/:id` (contract `/orders`) once the order reaches `approved` (click Execute) or `scheduled` (runs automatically within the `15-minute` window).
- Monitor per-statement execution progress or real-time built-in `gh-ost` Online Schema Change table copy percentage.
- If a network drop during commit causes `result_unknown`, inspect the target database and click Verify Execution (`confirm_succeeded` or `confirm_failed`).

## Detailed Guide
- **Manual vs. Scheduled Execution (`/changes/orders/:id`)**:
  - **Manual Execution**: Once an order reaches `approved`, an authorized executor can click **Execute** on the order detail page.
  - **Scheduled Execution**: Orders in `scheduled` status are automatically picked up by the background scheduler when `scheduled_at` arrives, within a **`15-minute` execution grace window** (`2013 SCHEDULE_WINDOW_EXPIRED` if the window has passed).
- **Single-Datasource Execution Lease & Transaction Atomicity**:
  - Only **1 change order** can execute on a given target datasource at a time. Concurrent execution attempts on the same datasource return `3001 EXECUTION_LEASE_CONFLICT`.
  - **DML Atomicity**: DML orders execute inside a database transaction. If any statement fails or affects more than the `100,000`-row rollback capture limit (when rollback protection is enabled), the entire transaction rolls back cleanly.
- **Online Schema Change (Built-in In-Process `gh-ost`; `pt-osc` Not Supported)**:
  - For MySQL `ALTER TABLE` DDL changes on large tables, Yearning embeds **`gh-ost`** (`github.com/github/gh-ost`) as an in-process Go library and invokes `logic.NewMigrator(...).Migrate()` directly via Go function calls (`base.Hooks` + atomic progress counters) without spawning an external binary. **`pt-osc` (`pt-online-schema-change`) is not supported**.
  - Configurable `gh-ost` parameters in **`/admin/settings/execution`** (and per-attempt overrides when clicking Execute) cover 18 options across load protection (`max_load`, `critical_load`, `critical_load_interval_millis`, `critical_load_hibernate_seconds`), copy & binlog batching (`chunk_size`, `dml_batch_size`, `max_lag`, `nice_ratio`), cut-over & retry backoff (`cut_over_lock_timeout_seconds`, `retries`, `heartbeat_interval_millis`, `exponential_backoff_max_interval`), and behavior switches (`exact_rowcount`, `concurrent_rowcount`, `ok_to_drop_table`, `timestamp_old_table`, `cut_over_exponential_backoff`, `attempt_instant_ddl`). SQL statements and target datasource credentials are automatically injected from the frozen order plan.
  - During `gh-ost` execution, the order detail page streams real-time table copy percentage, ETA, and cut-over progress. When Rollback Protection is enabled, Yearning captures the pre-execution and post-execution table schema around the `gh-ost` run and generates the inverse `ALTER TABLE` statement for schema-only rollback (`rollback_status = available`).
- **Resolving `result_unknown` (`3002` / `3003`)**:
  - If a network timeout, database crash, or worker lease loss happens at the exact moment of transaction commit, Yearning **never automatically retries** the SQL (to prevent duplicate DML updates or inserts). Instead, the order transitions to `result_unknown` (`3002 EXECUTION_RESULT_UNKNOWN`).
  - **How to Recover**: Check the actual target database to verify whether the changes took effect, then click **Verify Execution** on the order detail page and choose:
    - **Confirm Succeeded (`confirm_succeeded`)**: Marks the order as `succeeded` (and unlocks rollback if before-image capture was already completed).
    - **Confirm Failed (`confirm_failed`)**: Marks the order as `failed` so you can safely copy and resubmit the order.
