---
id: help-change-orders
title: Submitting DML/DDL Change Orders & Draft Management
source_ref: docs/prd/change-order-prd.md#submit-order
route_path: /orders/new
admin_only: false
keywords: order, change order, submit, draft, dml, ddl, insert, update, delete, alter, create, schedule, cron, change_review, withdraw, void, copy, resubmit, batch, size, limit, statements, 32mb, 512kb, 100000, 1000, 2001, 2002, 2014
summary: In New Change Order (/orders/new, UI /changes/new), select an authorized change_review flow and target database, enter pure DML or pure DDL statements (up to 32 MiB and 100,000 statements per order, 512 KiB per statement), pass formal AI Precheck, and submit or save as draft.
---

## Steps
- Open `/orders/new` (UI `/changes/new`), select an authorized `change_review` flow, target datasource, and schema, and toggle Rollback Protection or Scheduled Execution if needed.
- Enter pure DML or pure DDL statements (or import a `.sql`/`.txt` file up to `32 MiB` / `100,000` statements locally into the editor; single statement up to `512 KiB`), or click Save & Return to persist as a `draft`.
- Click Run AI Precheck to open the precheck modal with a live progress bar; once progress reaches `100%` (`ready`) with zero blocking findings, click Submit for Approval inside the modal and track progress in `/changes/mine`.

## Detailed Guide
- **Where to Create & View Change Orders**:
  - **Create Change Order / Edit Draft**: `/changes/new` (contract route `/orders/new`) and `/changes/drafts/:orderId`.
  - **My Change Orders**: `/changes/mine`.
  - **Order Detail & Execution Timeline**: `/changes/orders/:orderId`.
- **SQL Category Rules (No Mixing DML and DDL)**:
  - Each change order must contain **either pure DML** (`INSERT`, `UPDATE`, `DELETE`) **or pure DDL** (`CREATE`, `ALTER`, `DROP`, `RENAME`, `TRUNCATE`).
  - Mixing DML and DDL in the same order is rejected with `2001 ORDER_MIXED_SQL_CATEGORY`—split them into two separate orders.
  - Read-only `SELECT` queries are rejected in change orders with `2002 ORDER_INVALID_SQL_CATEGORY` (use `/query` instead).
- **Exact SQL Batch & Statement Limits**:
  - **Max SQL batch size per order**: **`32 MiB` (`33,554,432 bytes`)**.
  - **Max statements per order**: **`100,000` statements**.
  - **Max single statement size**: **`512 KiB` (`524,288 bytes`)**.
  - **Bulk SQL Virtualized Mode**: When SQL text exceeds **`512 KiB`** or **`1,000` statements**, the editor automatically switches to a virtualized statement browser and supports drag-and-drop local `.sql` / `.txt` file import.
- **All 11 Order Lifecycle States & Actions**:
  - `draft`: Saved via **Save & Return** without submitting; can be edited or deleted anytime.
  - `pending_approval`: Submitted and waiting for approvers in the active stage.
  - `changes_requested`: Approver requested modifications; the submitter can edit SQL directly on the same order, re-run AI Precheck, and **Resubmit**, or click **Void** to close it as `withdrawn`.
  - `approved`: All approval stages passed; ready for manual execution.
  - `scheduled`: All approval stages passed and waiting for the scheduled execution window (`15-minute` grace window).
  - `executing`: Currently running on the target datasource under a single-datasource execution lease.
  - `succeeded`: Execution committed successfully; if rollback SQL was captured (`rollback_status = available`), you can click **Create Rollback Order**.
  - `failed`: Execution failed and rolled back.
  - `result_unknown`: Network or lease interruption during commit; requires manual verification (`confirm_succeeded` or `confirm_failed`) before any retry.
  - `rejected`: Terminally rejected by an approver.
  - `withdrawn`: Withdrawn by the submitter (allowed from `pending_approval`, `approved`, `scheduled`, or voided from `changes_requested`).
  - **Copy as Draft**: Any historical order can be cloned into a new draft with one click via **Copy as New Draft**.
