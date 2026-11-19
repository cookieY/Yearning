# Binlog-Free Cross-Engine Automatic Rollback

Yearning v4 eliminates legacy MySQL `ROW` Binlog scraping and unapproved instant rollbacks, introducing **Binlog/WAL-free AST inverse generation + in-transaction before-image capture** across both **MySQL-compatible** and **PostgreSQL-compatible** engines.

## How It Works

1. **Zero Replication Privileges Required**: No `REPLICATION SLAVE` / `REPLICATION CLIENT` grants and no Binlog retention or WAL lag dependencies.
2. **Same-Transaction DML Before-Image Capture**:
   - `INSERT` -> inverse `DELETE`; `UPDATE` -> inverse `UPDATE` with before-image values; `DELETE` -> inverse `INSERT`.
   - **Engine Capture**: MySQL uses in-transaction `SELECT ... FOR UPDATE` and `LAST_INSERT_ID()`; PostgreSQL 18+ uses `RETURNING OLD.*, NEW.*` (PG 14–17 uses `SELECT ... FOR UPDATE` + `RETURNING *`).
   - Target DML tables must define a Primary Key or non-null Unique Key.

## 100,000-Row Fail-Closed Cap

- **Max rows per DML statement**: **`100,000` rows**
- **Max rows per change order**: **`100,000` rows**

:::danger Fail-Closed Transaction Protection
When Rollback Protection is enabled and any DML statement or total order exceeds `100,000` affected rows, Yearning **immediately aborts and rolls back the forward transaction** (`affected_rows_exceed_rollback_limit`) so changes never commit without complete rollback coverage.
:::

## Reversible DDL & `gh-ost` Schema Rollback

DDL rollback restores table and index structure (not dropped row data): `CREATE TABLE` <-> `DROP TABLE`, `ADD/DROP COLUMN`, `RENAME TABLE/COLUMN`, `CREATE/DROP INDEX`, `MODIFY/ALTER COLUMN`, `ADD/DROP CONSTRAINT`, and pre/post schema snapshots around in-process `gh-ost` `ALTER TABLE`. Operations such as `TRUNCATE TABLE`, `DROP DATABASE/SCHEMA`, and `DROP ... CASCADE` are marked `unsupported`.

## Governed Rollback Orders & 7 States

Inverse SQL is encrypted with **AES-256-GCM** in `order_rollback_evidences`, inspected via watermarked SQL Reveal (`purpose="rollback-order-sql"`), and converted into a linked rollback draft (`POST /change-orders/{order_id}/rollback-drafts`) that must pass formal AI Precheck, approval, and drift checks before execution.

All 7 `rollback_status` states: `available`, `unsupported`, `evidence_incomplete`, `rollback_in_progress`, `rolled_back`, `rollback_failed`, and `conflicted`.
