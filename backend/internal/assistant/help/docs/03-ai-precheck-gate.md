---
id: help-ai-precheck
title: AI Review Precheck & Submission Gate
source_ref: docs/ai-review-production-prd.md#11-submission-gate
route_path: /orders/new
admin_only: false
keywords: precheck, review, gate, blocked, finding, error, warning, critical, high, medium, low, outdated, fingerprint, deduplication, dedup, homologous, hmac, submission gate, explain, 200, 1000, 1800, 2006, 2007, 2008, 2009, 2015, 2016
summary: Every change order in /orders/new (/changes/new) must pass AI Review Precheck: Yearning deduplicates homologous SQL statements by structural fingerprint (sending only 1 anonymized SQL per fingerprint group to the AI, up to 1,000 groups per order) and requires a ready run with zero blocking errors.
---

## Steps
- In `/orders/new` (UI `/changes/new`), enter your SQL statements and click Run AI Precheck to open the precheck modal and monitor live stage progress. If dialect lexing fails (`2003 SQL_PARSE_FAILED`), the modal reports the exact line, column, byte offset, statement ordinal, dialect, and lexical reason without leaking raw SQL values.
- Once progress reaches `100%`, review the overview and findings tabs inside the modal: Yearning collapses structurally identical statements into fingerprint groups (up to `1,000` groups per order) and inspects metadata and execution plans without leaking raw values.
- Fix any blocking findings (`2008`) and re-run precheck if you edit the SQL (`2016`); once status is `ready` (30-minute TTL), click Submit for Approval inside the precheck modal.

## Detailed Guide
- **How SQL Fingerprint Deduplication Works**:
  - Before invoking the AI model, Yearning lexes every SQL statement, strips all comments, and replaces literal values (strings, numbers, `IN (...)` value lists) with typed placeholders (`?` / `:val_*`) to compute a structural **Canonical Fingerprint** (`canonical_fingerprint`) and **Dialect Fingerprint** (`dialect_fingerprint`).
  - **Homologous Statement Collapse**: If a batch contains dozens or tens of thousands of statements with the exact same syntax and columns that only differ in parameter values (for example, 5,000 `UPDATE users SET score = 10 WHERE id = 1` statements), they are grouped into **1 single fingerprint group** (`fingerprint_group_count = 1`).
  - **What Gets Sent to the AI**: Deterministic syntax and safety rules check all statements, while the **AI Review Agent receives only 1 representative anonymized SQL per fingerprint group** (along with the statement count in that group). Duplicate templates are never sent repeatedly to the model, and raw business values never leave the server.
  - **Fingerprint Group Limits**:
    - Soft warning threshold: **`200` distinct fingerprint groups** per order.
    - Hard limit: **`1,000` distinct fingerprint groups** per order (`TOO_MANY_FINGERPRINTS` if a single order contains more than 1,000 structurally different SQL templates).
- **2-Stage Precheck Pipeline & Read-Only Agent Tools**:
  1. **Deterministic AST & Rule Check**: Evaluates syntax, SQL category consistency, and bound Rule Set rules (such as missing `WHERE` on `UPDATE`/`DELETE`, `SELECT *`, primary key presence, index limits, and `max_affect_rows`).
  2. **AI Review Agent Inspection**: Uses 5 read-only metadata tools (`check_syntax`, `describe_table`, `list_indexes`, `table_stats`, and `safe_explain`) together with bound Review Skills and scoped Internal Experience to inspect index selectivity, lock impact, and execution plans without ever modifying data.
- **Finding Governance, Severities & Submission Gate Rules**:
  - **4 Finding Categories (`RCP-20261126-KNOWLEDGE-ASSISTANT-SYNC`)**:
    1. `policy_finding`: Any actual violation of an enabled, flow-bound **Prompt Tool / Review Skill (`/admin/review-engine/skills`)** always has an effective severity of **`critical`** and **blocks submission (`2008 PRECHECK_BLOCKED_BY_ERRORS`)**. The `max_severity` field on Prompt Tools is retained strictly for schema compatibility and never downgrades a blocking rule violation.
    2. `proven_invalid_execution`: Statements proven unexecutable against complete scope metadata by the trusted runtime (e.g., dropping a non-existent table without `IF EXISTS` or prior creation in the batch) are also **`critical`** and block submission.
    3. `knowledge_advisory`: Scope-matched `datasource` or `table` **Internal Experience (`/admin/review-engine/knowledge`)** notes yield non-blocking advisory findings capped deterministically at `min(max_severity, medium)` (`info`, `low`, or `medium`) and never block order submission. Legacy `global` entries are retired and disabled.
    4. `agent_observation`: Unbound model observations are capped at **`medium`** (`medium` / `low` / `info`) and remain visible as advisories without blocking submission on their own.
  - **Incomplete Metadata / `partial` Gate**: If required catalog metadata is missing, truncated, or execution validity is `unknown`, the stage remains in **`partial`** status and blocks submission until resolved.
  - **Automatic Identifier Restoration (`R014`) & Frozen Locale (`R011`)**: While the AI Provider only ever receives anonymized tokens (`schema_1`, `table_1`, `column_1`), authorized readers (draft owner, order submitter, or frozen stage approver/executor) see real table and column names automatically restored in precheck summaries and findings (recording a `review.identifier_displayed` audit event). Findings and summaries are generated and frozen in the UI language (`zh-CN` or `en-US`) active when the precheck run started.
  - **PostgreSQL Schema Scope & Empty Schema Mapping (`R012`)**: When a PostgreSQL flow stage has an empty schema mapping, Yearning probes the target execution role's real `search_path` (`current_schemas`, `current_schema`) and resolves unqualified objects in exact path order (requiring `pg_catalog` before user schemas for callable safety). Note that on PostgreSQL the optional `Target Schema` selector (API field `target_database`) and explicit `schema.table` SQL qualifiers always refer to a **Schema inside the connected Database, not a Database**: explicit non-system user schemas outside `search_path` (including multi-schema references in the same connected Database) are permitted once verified to exist with `USAGE` privilege, while `pg_temp*`, `pg_toast*`, and `information_schema` are always disallowed (`sql_schema_out_of_scope`).
  - **Stale Check (`2016 PRECHECK_RUN_STALE`)**: The precheck `run_id` is cryptographically bound to the exact SQL batch fingerprint, target flow/datasource, and active policy/provider plan hash. Editing even a single character of SQL or modifying bound skills/provider parameters invalidates the run (`outdated`) and requires re-running AI Precheck.
  - **Precheck Run TTL**: Ephemeral precheck runs expire after **`30 minutes` (`1,800 seconds`)** (`2006` if expired).
- **Database AI Assistant Bridge (`/assistant`)**:
  - In `sql_generate` and `sql_optimize` turns, the AI Assistant automatically injects matched enabled `datasource` and `table` Internal Experience notes (within bounded per-turn budgets and privacy controls) so SQL drafts avoid known pitfalls before precheck.
  - Clicking **Submit Change Order** on an AI-generated SQL draft in `/assistant` is never a shortcut: it must still execute and pass the exact same formal AI Precheck (`PrecheckDraft` -> `ready`) before creating a change order (`6005` if not prechecked or modified).
