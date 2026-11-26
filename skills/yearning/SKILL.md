---
name: yearning
description: Interact with the Yearning database change governance, automated + AI SQL review precheck, read-only SQL query, and SQL performance diagnostics platform by calling the existing `/api/v4/*` HTTP API directly (via the bundled zero-dependency `scripts/yearning_api.py` helper or standard HTTP requests). Use when an agent needs to query pending approvals or orders, discover granted flows and schemas, run SQL review prechecks, manage change drafts, submit/approve/execute/rollback SQL change orders, run governed read-only SELECT queries, or diagnose slow SQL statements.
---

# Yearning Platform Skill (Direct HTTP API Integration)

Yearning is an enterprise database change governance, automated + AI SQL review, read-only SQL query, and SQL performance diagnostics platform. AI agents interact with Yearning directly through its governed `/api/v4/*` HTTP REST API without requiring any MCP server or local `yearning` binary.

This skill includes a zero-dependency Python 3 standard-library helper script (`scripts/yearning_api.py`) that automatically handles session login, cookie jar persistence (`yearning_session` + `yearning_csrf`), double-submit `X-CSRF-Token` headers, `Idempotency-Key` UUID binding across retries, optimistic concurrency (`If-Match`), explicit audited SQL reveal (`/sql-reveals`), and SQL precheck polling.

---

## 1. Authentication & Environment Configuration

Set the following environment variables (or pass the equivalent CLI flags to `scripts/yearning_api.py`):

| Environment Variable | CLI Flag | Default | Description |
| --- | --- | --- | --- |
| `YEARNING_BASE_URL` | `--base-url` | `http://127.0.0.1:8080` | Base URL of the Yearning server |
| `YEARNING_USERNAME` | `--username` | *(empty)* | Username for `/api/v4/auth/login` or `/api/v4/auth/ldap/login` |
| `YEARNING_PASSWORD` | `--password` | *(empty)* | Login password |
| `YEARNING_PASSWORD_FILE` | `--password-file` | *(empty)* | Path to a file containing the login password |
| `YEARNING_AUTH_PROVIDER` | `--auth-provider` | `local` | Authentication provider (`local` or `ldap`) |
| `YEARNING_SESSION_COOKIE` | — | *(empty)* | Optional pre-authenticated `yearning_session` cookie |
| `YEARNING_CSRF_TOKEN` | — | *(empty)* | Optional `yearning_csrf` token matching the session |

---

## 2. Mandatory Context Resolution Rules (Never Guess Target Flow, Datasource, or Schema)

Before calling any SQL precheck, order submission, or read-only query command, you **must** verify that the required platform context is resolved:

1. **DDL/DML Precheck (`precheck`) & Order Submission (`submit-order`)**:
   - Yearning requires a concrete **`change_review` Flow (`flow_id`)** (which binds the stage datasource, engine dialect, and review Rule Set).
   - If the user did not specify which Flow or target Database/Schema (`schema_name`) to use:
     1. Run `flows --flow-type change_review` and `metadata --flow-id <flow_id> --datasource-id <ds_id>` first.
     2. If multiple candidate flows or schemas exist and the SQL does not uniquely identify one, **stop and ask the user to confirm** the target Flow and Schema before running `precheck`.
2. **Read-Only `SELECT` Query (`query`)**:
   - Yearning requires a concrete **`datasource_id`**, **`schema_name`**, and an active **Query Session (`session_id`) / Query Grant (`grant_id`)**.
   - If the user did not specify the target datasource or schema:
     1. Run `query-context` to inspect active query sessions, active query grants, and granted `query_access` flows.
     2. Run `query-metadata --datasource-id <ds_id>` to verify the exact `schema_name` and table/column names.
     3. If multiple datasources or schemas match, **ask the user to confirm** before executing `query`.

---

## 3. Quick Commands via `scripts/yearning_api.py`

Locate `scripts/yearning_api.py` relative to this `SKILL.md` file (e.g., `<skill_dir>/scripts/yearning_api.py`).

### 3.1 Workbench, Pending Approvals & Change Orders

```bash
# Current user profile + personal workbench counters (pending approvals, drafts, etc.)
python3 <skill_dir>/scripts/yearning_api.py me

# Filter change orders awaiting current user's approval, execution, or submitted by current user (traverses all cursor pages)
python3 <skill_dir>/scripts/yearning_api.py orders --pending-my-approval
python3 <skill_dir>/scripts/yearning_api.py orders --pending-my-execution
python3 <skill_dir>/scripts/yearning_api.py orders --my-submissions

# List visible change orders (cursor-paginated via --limit and --after)
python3 <skill_dir>/scripts/yearning_api.py orders --limit 50

# Inspect change order metadata, timeline, findings, and comments (default: metadata only per api-conventions §9)
python3 <skill_dir>/scripts/yearning_api.py order <order_id>

# Explicitly audit-reveal order SQL or rollback SQL when inspecting SQL contents
python3 <skill_dir>/scripts/yearning_api.py order <order_id> --reveal-sql
python3 <skill_dir>/scripts/yearning_api.py order <order_id> --reveal-sql --reveal-rollback-sql

# Approve or reject an active stage approval step (auto-resolves If-Match version)
python3 <skill_dir>/scripts/yearning_api.py decide-order <order_id> approve --comment "LGTM"
python3 <skill_dir>/scripts/yearning_api.py decide-order <order_id> reject --comment "Missing WHERE index"

# Post a collaboration comment on a change order
python3 <skill_dir>/scripts/yearning_api.py comment-order <order_id> --content "Please confirm maintenance window"

# Withdraw a pending order or void an approved order (submitter only)
python3 <skill_dir>/scripts/yearning_api.py withdraw-or-void <order_id> withdraw --reason "Need to revise SQL"

# Execute an approved change order (optional --ddl-execution-mode gh_ost|online_ddl) & inspect statement results
python3 <skill_dir>/scripts/yearning_api.py execute-order <order_id>
python3 <skill_dir>/scripts/yearning_api.py execute-order <order_id> --ddl-execution-mode gh_ost
python3 <skill_dir>/scripts/yearning_api.py execution-attempt <attempt_id>

# Create a governed rollback draft from an executed order (pass --reveal-sql to reveal the generated rollback SQL)
python3 <skill_dir>/scripts/yearning_api.py rollback-draft <order_id> --reveal-sql
```

### 3.2 Flow Discovery, Schema Inspection, Change Drafts, SQL Precheck & Order Submission

```bash
# 1. List granted change_review and query_access flows
python3 <skill_dir>/scripts/yearning_api.py flows

# 2. Browse schemas, tables, and columns in a change_review flow datasource
python3 <skill_dir>/scripts/yearning_api.py metadata --flow-id <flow_id> --datasource-id <datasource_id>
python3 <skill_dir>/scripts/yearning_api.py metadata --flow-id <flow_id> --datasource-id <datasource_id> --schema-name <db>
python3 <skill_dir>/scripts/yearning_api.py metadata --flow-id <flow_id> --datasource-id <datasource_id> --schema-name <db> --table-name <tbl>

# 3. List or create saved SQL change drafts
python3 <skill_dir>/scripts/yearning_api.py drafts --limit 50
python3 <skill_dir>/scripts/yearning_api.py create-draft \
  --flow-id <flow_id> \
  --title "Add status column to demo.users" \
  --sql "ALTER TABLE demo.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"

# 4. Run automated + AI SQL review precheck bound to flow_id (polls until ready/blocked/partial/failed/outdated)
#    Optional --target-database: for MySQL sets the default database override; for PostgreSQL sets an optional Target
#    Schema within the connected Database (never switches Databases; leave unset when SQL qualifies schema.table).
python3 <skill_dir>/scripts/yearning_api.py precheck \
  --flow-id <flow_id> \
  --sql "ALTER TABLE demo.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"

# 5. Submit a governed change order bound to the passing precheck run_id (pass matching --target-database if used in precheck)
python3 <skill_dir>/scripts/yearning_api.py submit-order \
  --flow-id <flow_id> \
  --run-id <run_id> \
  --title "Add status column to demo.users" \
  --sql "ALTER TABLE demo.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"
```

### 3.3 Governed Read-Only SQL Query (`SELECT`)

```bash
# 1. Inspect active query grants, active query sessions, and query_access flows
python3 <skill_dir>/scripts/yearning_api.py query-context

# 2. Request read-only query access if no active grant exists
python3 <skill_dir>/scripts/yearning_api.py query-request \
  --flow-id <query_flow_id> \
  --datasource-ids <datasource_id> \
  --requested-until "2026-12-31T23:59:59Z" \
  --reason "Investigate order status discrepancy"

# 3. Browse schemas, tables, and columns inside a query session (reuses active session or opens one)
python3 <skill_dir>/scripts/yearning_api.py query-metadata --datasource-id <datasource_id>
python3 <skill_dir>/scripts/yearning_api.py query-metadata --datasource-id <datasource_id> --schema-name <db>

# 4. Execute read-only SELECT (server-side sensitive column masking applied automatically)
python3 <skill_dir>/scripts/yearning_api.py query \
  --datasource-id <datasource_id> \
  --schema-name <db> \
  --sql "SELECT id, username, created_at FROM users LIMIT 20"

# 5. Fetch subsequent result pages when next_cursor is non-null (--purpose defaults to display)
python3 <skill_dir>/scripts/yearning_api.py query-page \
  --execution-id <execution_id> \
  --cursor <next_cursor> \
  --purpose display
```

### 3.4 SQL Performance Diagnostics & AI Optimization (`admin`)

```bash
# 1. List datasources and diagnostics collection status
python3 <skill_dir>/scripts/yearning_api.py diagnostics datasources

# 2. Trigger an immediate manual diagnostics snapshot collection
python3 <skill_dir>/scripts/yearning_api.py diagnostics snapshot --datasource-id <datasource_id>

# 3. Fetch 6-chart performance overview & top hotspot statement fingerprints
#    (--sort-by choices: total_exec_time_ms | avg_exec_time_ms | calls | rows_examined)
python3 <skill_dir>/scripts/yearning_api.py diagnostics overview \
  --datasource-id <datasource_id> \
  --sort-by total_exec_time_ms

# 4. Fetch 3-chart statement detail & parameterized SQL template
python3 <skill_dir>/scripts/yearning_api.py diagnostics detail \
  --datasource-id <datasource_id> \
  --statement-id <statement_id>

# 5. Run desensitized AI root-cause diagnosis & optimization recommendations
python3 <skill_dir>/scripts/yearning_api.py diagnostics ai-report \
  --datasource-id <datasource_id> \
  --statement-id <statement_id>

# 6. Precheck and submit an optimization change order from an AI report
python3 <skill_dir>/scripts/yearning_api.py diagnostics precheck-report \
  --datasource-id <datasource_id> --report-id <report_id> --flow-id <flow_id>
python3 <skill_dir>/scripts/yearning_api.py diagnostics submit-report \
  --datasource-id <datasource_id> --report-id <report_id> --flow-id <flow_id> \
  --ephemeral-run-id <run_id> --title "Optimize slow query index"
```

### 3.5 Calling Any `/api/v4/*` Endpoint Directly (`raw`)

```bash
python3 <skill_dir>/scripts/yearning_api.py raw GET /api/v4/change-drafts
python3 <skill_dir>/scripts/yearning_api.py raw POST /api/v4/change-orders/<order_id>/comments '{"content":"Please verify backup status"}'
python3 <skill_dir>/scripts/yearning_api.py raw PUT /api/v4/change-drafts/<draft_id>/sql '{"sql":"SELECT 1"}' --if-match 2
```

---

## 4. Direct `/api/v4/*` HTTP Protocol Contract Reference

1. **Response Envelope**: `{"err_code": 0, "message": "ok", "data": ...}`.
2. **Double-Submit CSRF**: `POST /api/v4/auth/login` sets `yearning_session` and `yearning_csrf` cookies. Every `POST`/`PUT`/`PATCH`/`DELETE` request **must** include header `X-CSRF-Token: <yearning_csrf>`.
3. **Idempotency**: Mutating endpoints require `Idempotency-Key: <UUIDv4>` preserved across retries of the same logical write.
4. **Optimistic Concurrency (`If-Match`)**: `POST /api/v4/change-orders/{id}/{approval-decisions|withdrawal|voidance|execution-attempts|execution-schedules}` and `PUT /api/v4/change-drafts/{id}/sql` require `If-Match: "<version>"`.
5. **Cursor Pagination & System-Timezone Day Boundaries**: List endpoints (`/change-orders`, `/change-drafts`, `/users/me/flows`, `/query-grants`, `/query-sessions`) use `?limit=50&after=<cursor>`. All `YYYY-MM-DD` day-boundary filters (`submitted_from` / `submitted_to` on `/change-orders`) and wall-clock `requested_until` timestamps (e.g. `Z`-suffixed digits on query access requests/renewals) are interpreted in the platform's configured `general.system_timezone` (default `Asia/Shanghai`, exposed via `GET /api/v4/users/me` `system_timezone`), taking effect immediately on the next operation after settings updates.
6. **Explicit Audited SQL Reveal**: Order SQL and Draft SQL are stored encrypted; `GET /api/v4/change-orders/{id}` returns metadata only. Call `POST /api/v4/change-orders/{id}/sql-reveals` (`{"purpose": "order-review"}` or `{"purpose": "rollback-order-sql"}`) only when explicitly inspecting SQL text.
7. **Real-Time Domain Event Stream (SSE)**: Authenticated clients can subscribe to `GET /api/v4/events/stream` (`Accept: text/event-stream`) using `yearning_session` cookie authentication to receive user-scoped `io.yearning.v4.*` change-order, approval, execution, query-access, query-grant, and query-session state notifications without polling.

---

## 5. Safety & Governance Guardrails

1. **Never Bypass Precheck**: `submit-order` (`POST /api/v4/change-orders`) requires a valid `run_id` from `precheck` (`POST /api/v4/review-runs`) whose reviewed `sql` matches the submitted `sql` byte-for-byte.
2. **Confirm Before State-Mutating Order Decisions**: Before executing `decide-order` (`approve`/`reject`), `execute-order`, or `withdraw-or-void`, confirm intent with the user unless explicitly instructed to execute autonomously.
3. **Read-Only Query Isolation**: Only `SELECT` statements are permitted in `/api/v4/query-sessions/{id}/executions`. Use `precheck` + `submit-order` for all DDL/DML changes.
4. **Review Skills vs. Internal Experience Boundaries**: Mandatory blocking rules (`critical`) are enforced by flow-bound Review Skills (Prompt Tools); `datasource`/`table`-scoped Internal Experience notes are non-blocking advisories (`info`/`low`/`medium`, with `global` scope retired) shared between AI Assistant (`sql_generate` / `sql_optimize`) and AI Review precheck.
5. **Supported Engine & Capability Matrix**:
   - **Class A (Full Live Container Parity)**: `MySQL 8.0`, `PostgreSQL 14–18+`.
   - **Class B (Dialects Validated via Static Golden Corpus, Catalog Record/Replay & Runtime Probe Fail-Closed Checks)**: `MySQL 5.7`, `MySQL 8.4 LTS`, `MariaDB`, `TiDB` (MySQL mode), `OceanBase` (MySQL mode), `PolarDB` (MySQL/PostgreSQL mode). Passing `--ddl-execution-mode gh_ost` on non-native MySQL targets or cloud dialects fails closed (`osc_unsupported_target`), and cross-schema `RENAME TABLE` fails closed for rollback protection.
