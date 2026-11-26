---
id: help-rollback-orders
title: Automatic Rollback SQL Capture & Rollback Change Orders
source_ref: docs/iterations/v4.0.0/features/sql-rollback-system-prd.md
route_path: /orders
admin_only: false
keywords: rollback, undo, reverse, before-image, recovery, conflicted, evidence_incomplete, available, unsupported, rollback_enabled, ticket, dml, ddl, parent_order_id, 100000, limit, rows, 2015, 2016, 3004, 3005, 3006
summary: Yearning supports automatic rollback SQL generation for both DML (up to 100,000 rows per statement and 100,000 rows per order) and reversible DDL; once an order with Rollback Protection succeeds (rollback_status = available), click Create Rollback Order to submit a linked rollback order.
---

## Steps
- Enable Rollback Protection when submitting a DML (`INSERT`/`UPDATE`/`DELETE`) or reversible DDL change order in `/changes/new` (`/orders/new`).
- During execution, Yearning captures before-image reverse SQL (up to `100,000` rows per DML statement and `100,000` rows per order) or generates inverse DDL.
- Once the order succeeds with `rollback_status = available`, open `/changes/orders/:id` (`/orders`), click Create Rollback Order, pass AI Precheck, and approve/execute the rollback order.

## Detailed Guide
- **DML Rollback Row Limits (Exact Cap: `100,000` Rows)**:
  - **Max captured rows per single DML statement**: **`100,000` rows**.
  - **Max captured rows per change order**: **`100,000` rows**.
  - **What happens if a DML statement exceeds 100,000 rows**: When Rollback Protection is enabled, if any statement or the total order affects more than `100,000` rows during execution, Yearning **immediately aborts and rolls back the forward database transaction** (`affected_rows_exceed_rollback_limit`) so changes never commit without complete rollback coverage. If before-image capture is incomplete, the rollback artifact is marked `evidence_incomplete` (`3005 ROLLBACK_EVIDENCE_INCOMPLETE`) and automatic rollback order creation is blocked.
- **DML Rollback Requirements & Capture Strategy**:
  - Supported DML: `INSERT` (generates inverse `DELETE`), `UPDATE` (generates inverse `UPDATE` with before-image values), and `DELETE` (generates inverse `INSERT`).
  - **Primary / Unique Key Required**: The target table must have a Primary Key or non-null Unique Key to uniquely locate rows (`no_unique_key` otherwise).
  - **Engine Capture**: MySQL captures before-images inside the same transaction via `SELECT ... FOR UPDATE` and `LAST_INSERT_ID()`; PostgreSQL uses `RETURNING OLD.*, NEW.*` (PG 18+) or in-transaction `SELECT ... FOR UPDATE` + `RETURNING *` (PG <18).
- **DDL Rollback Support (Schema-Only Structural Rollback)**:
  - **DDL rollback restores table and schema structure only** (it does not back up or restore row data inside dropped tables or columns):
    - `CREATE TABLE` -> `DROP TABLE`
    - `DROP TABLE` -> `CREATE TABLE` (reconstructed from the pre-execution table schema and secondary indexes)
    - `ALTER TABLE ... ADD COLUMN` -> `ALTER TABLE ... DROP COLUMN`
    - `ALTER TABLE ... DROP COLUMN` -> `ALTER TABLE ... ADD COLUMN` (reconstructed from the pre-execution column definition)
    - `RENAME TABLE` / `RENAME COLUMN` -> inverse `RENAME`
    - `CREATE INDEX` / `ADD INDEX` -> `DROP INDEX`
    - `DROP INDEX` -> `CREATE/ADD INDEX` (reconstructed from existing index metadata)
    - `MODIFY COLUMN` / `ALTER COLUMN` -> restores previous column data type, nullability, and default value
    - `ADD CONSTRAINT` / `DROP CONSTRAINT` -> inverse constraint DDL
    - Online Schema Change (built-in `gh-ost` `ALTER TABLE`) -> captures pre/post table schema and generates the inverse `ALTER TABLE` statement (`pt-osc` is not supported)
  - **Unsupported DDL** (marks `rollback_status = unsupported`): Data-only operations like `TRUNCATE TABLE`, whole-database drops (`DROP DATABASE` / `DROP SCHEMA`), and `DROP ... CASCADE`.
- **How to Create a Rollback Order & All 7 Rollback Statuses**:
  - When a forward order with **Rollback Protection** enabled completes with `succeeded` and `rollback_status = available`, open `/changes/orders/:id` and click **Create Rollback Order**.
  - This creates a linked change order (`parent_order_id` pointing to the original order, max 1 rollback order per original order `3006`) that goes through standard AI Precheck and approval before execution.
  - **All 7 `rollback_status` values**: `available` (ready to create rollback order), `unsupported` (not reversible or switch was off), `evidence_incomplete` (capture failed or exceeded limit), `rollback_in_progress` (linked rollback order is active), `rolled_back` (rollback succeeded), `rollback_failed` (rollback execution failed), and `conflicted` (post-execution row drift detected).
