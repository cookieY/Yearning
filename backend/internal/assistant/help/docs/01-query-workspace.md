---
id: help-query-access
title: Query Workspace & Ephemeral Query Sessions
source_ref: docs/prd/query-system-prd.md#query-session
route_path: /query
admin_only: false
keywords: query, select, workspace, cursor, export, session, read-only, grant, request, timeout, page, limit, rows, csv, json, jsonl, explain, show, describe, schema, autocomplete, metadata, sse, events/stream, real-time, 5000, 21600, 4001, 4002, 4003, 4004, 4005, 4006
summary: In Query Workspace (/query), request or use an active query session (query_access) to run single-statement read-only SQL (1 to 5,000 rows per page, 6-hour cursor TTL, 5,000 ms default timeout) and stream CSV or JSONL exports when permitted.
---

## Steps
- Open `/query`, select an authorized `query_access` flow and target datasource/schema, or submit a Query Access Request if you do not have an active session yet.
- Enter a single read-only statement (`SELECT`, `WITH ... SELECT`, `SHOW`, `DESCRIBE`, or `EXPLAIN`) in the editor and click Run (up to `5,000` rows per page, `5,000 ms` default timeout).
- Browse paginated results via the `6-hour` (`21,600 seconds`) streaming cursor, or export to `CSV` / `JSONL` when `can_export` is granted.

## Detailed Guide
- **Access & Query Permission Request (`/query`)**:
  - Querying a datasource requires an active **Query Session** or **Query Grant** authorized through a `query_access` flow (`/admin/flows`) granted to your Permission Group (`/admin/permission-groups`).
  - If you do not have access yet, click **Request Query Access** in `/query`, select the query flow and target datasource, choose the requested duration and whether **Export Permission (`can_export`)** is needed, and fill in the reason. Once approved in `/approvals` (or auto-approved if `approval_required = false`), your time-bounded session becomes active immediately.
- **Allowed SQL Statements (Strictly Read-Only, Single Statement)**:
  - Supported: `SELECT`, `SHOW`, `EXPLAIN`, `DESCRIBE` / `DESC`, and read-only `WITH ... SELECT` (CTEs).
  - Rejected (`4003 QUERY_NOT_READ_ONLY`): Multi-statement batches separated by `;`, locking reads (`SELECT ... FOR UPDATE`, `LOCK IN SHARE MODE`), DML (`INSERT`, `UPDATE`, `DELETE`), and DDL (`CREATE`, `ALTER`, `DROP`, `TRUNCATE`). Data or schema changes must be submitted via Change Orders in `/changes/new` (`/orders/new`).
- **Exact Limits (Pagination, Rows, Cursor TTL & Timeout)**:
  - **Rows per page (`page_size`)**: `1` to **`5,000` rows per page**.
  - **Maximum online query result rows (`max_result_rows`)**: Configurable in `/admin/settings/query` (`1..100,000` rows, default **`1,000` rows**); once reached, the online query stops reading and immediately releases the database connection, while streaming exports remain unbounded (`unlimited_by_product`).
  - **Pagination cursor lifetime (`cursor_ttl`)**: **`6 hours` (`21,600 seconds`)**, bound to the current user with HMAC signature verification (`4004 QUERY_CURSOR_EXPIRED` when expired).
  - **Default statement timeout**: **`5,000 ms` (`5 seconds`)**, configurable by administrators in `/admin/settings/query` (`1..300,000 ms`; returns `4005 QUERY_EXECUTION_TIMEOUT` when exceeded).
- **Streaming Data Export (`CSV` & `JSONL`)**:
  - Supported formats: **CSV** and **JSONL**, streamed directly without loading the full result set into memory.
  - Every page fetch and export request re-verifies live `can_export` authorization on your active grant/session (`4006 QUERY_EXPORT_FORBIDDEN` if export permission is not granted or has expired).
- **Schema Browser & Editor Productivity**:
  - The left sidebar lets you switch authorized datasources and schemas, inspect table columns, primary/unique keys, and indexes, and use schema-aware SQL autocomplete.
- **Real-Time Session & Grant Updates (`GET /api/v4/events/stream`)**:
  - Authenticated sessions maintain a single shared Server-Sent Events (SSE) subscription on `GET /api/v4/events/stream` (`Accept: text/event-stream`, returning `{"status":"ready","transport":"sse"}` for JSON handshake requests, with 25-second `: keepalive` pings).
  - When an active grant or session is revoked, relinquished, closed, or expired, the workspace receives the participant-scoped metadata event (`{id, type, subject, aggregate_id, state, occurred_at}`) immediately and locks the editor without polling.
