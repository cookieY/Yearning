---
id: help-error-codes
title: 4-Digit Business Error Codes & Troubleshooting Guide
source_ref: docs/contracts/error-catalog.md
route_path: /workspace
admin_only: false
keywords: error, err_code, code, 1001, 1002, 1003, 1004, 1005, 1006, 1007, 1008, 1009, 1010, 1011, 1012, 1101, 1102, 1103, 1104, 1105, 1106, 1107, 1108, 1109, 2001, 2002, 2003, 2004, 2005, 2006, 2007, 2008, 2009, 2010, 2011, 2012, 2013, 2014, 3001, 3002, 3003, 3004, 3005, 3006, 3007, 3008, 3009, 3010, 3011, 3012, 4001, 4002, 4003, 4004, 4005, 4006, 4007, 4008, 4009, 4010, 4011, 5001, 5002, 5003, 5004, 5005, 6001, 6002, 6003, 6004, 6005, 6006, 6007, 7001, 7002, 7003, 7004, 7005, 7006, 7007, 7008, validation_failed, ai_provider_failed
summary: YearningX uses canonical 4-digit business error codes across 10xx (common/concurrency/capacity), 11xx (identity/management/workflow references), 20xx (AI review precheck, daily budget, submission gate), 30xx (change order approval, execution, gh-ost, manual verification), 40xx (query access & sessions), 50xx (migration), 60xx (Database AI Assistant), and 70xx (SQL Performance Diagnostics).
---

## Steps
- Check the 4-digit business error code (`10xx`–`70xx`) and `request_id` returned in the UI alert/toast or API response envelope (`err_code`).
- Match the code prefix (`10xx` common/concurrency, `11xx` identity/references, `20xx` AI review/budget/submission, `30xx` approval/execution, `40xx` query workspace, `50xx` migration, `60xx` AI assistant, `70xx` SQL diagnostics).
- Apply the exact resolution step listed below for that error code.

## Detailed Guide
- **10xx Common, Concurrency, Idempotency & Capacity**:
  - `1001 VALIDATION_FAILED`: Input failed schema or business validation. Common causes:
    - **Creating/Editing a Datasource (`/admin/datasources`)**: Missing required `version_constraint` (e.g., `>=8.0,<9.0`; never echoed on edit and must be re-typed), invalid `port` (`1`–`65535`), missing credential `username`/`password`, or unpaired TLS client certificate and private key.
    - **Creating/Editing a Change Flow (`/admin/flows`)**: A `change_review` flow requires binding a non-empty **Rule Set (`rule_set_id`)** (create one first in `/admin/rule-sets`), at least one stage with an **enabled** target datasource (`enabled = true`), `1..10` approval steps with at least one approver, at least one execution actor, and all stages in a multi-stage change flow must use the same database engine and compatibility mode.
  - `1002 RESOURCE_NOT_FOUND`: Target resource does not exist or is hidden because the current user is not authorized to view it.
  - `1003 VERSION_CONFLICT`: Optimistic concurrency (`If-Match` / `version`) mismatch; refresh the page and re-apply your change.
  - `1004 CONCURRENT_MODIFICATION`: Another request is concurrently modifying the same resource; retry after it completes.
  - `1005 RESOURCE_ALREADY_EXISTS`: Duplicate unique name or identifier (e.g., duplicate username, datasource name, flow name, rule set name, or provider name).
  - `1006 RESOURCE_REFERENCED`: Resource is still referenced by downstream objects and cannot be physically deleted (disable it or remove references first).
  - `1007 IDEMPOTENCY_KEY_REUSED`: The same `Idempotency-Key` was reused with a different request payload; generate a fresh UUID v4 key.
  - `1008 IDEMPOTENCY_IN_PROGRESS`: A request with the same `Idempotency-Key` is still in flight; wait for it to finish.
  - `1009 CURSOR_EXPIRED`: Pagination cursor (`after`) is invalid or expired; reload from the first page.
  - `1010 INVALID_STATE_TRANSITION`: Action is not valid for the resource's current state machine status; refresh to see the latest state.
  - `1011 PRECONDITION_REQUIRED`: High-impact setting or provider change requires calling the `Assess` endpoint first and submitting the single-use `impact_token`.
  - `1012 SYSTEM_CAPACITY_EXHAUSTED`: Quota reached (e.g., maximum **50 active AI Assistant sessions** per user—delete unused sessions to free capacity).
- **11xx Identity, User Lifecycle & Template Reference Guards**:
  - `1101 INVALID_CREDENTIALS`: Incorrect username/password, disabled account, or failed LDAP/OIDC authentication.
  - `1102 ADMIN_PASSWORD_LOCKED`: Built-in administrator password login is temporarily locked after repeated failures.
  - `1103 ADMIN_IMMUTABLE`: The built-in administrator account (`is_builtin_admin = true`) cannot be disabled, deleted, or stripped of admin privileges.
  - `1104 USER_HAS_ACTIVE_ORDERS`: User still has non-terminal change orders or active query grants and cannot be deleted.
  - `1105 USER_REFERENCED_BY_TEMPLATE`: User is still referenced as an approver or execution actor in a flow template (`/admin/flows`); remove them from flows before deleting.
  - `1106 FLOW_REFERENCED`: Flow is still referenced by permission groups or orders and cannot be deleted; disable the flow instead.
  - `1107 DATASOURCE_REFERENCED`: Datasource is still referenced by flows or historical orders and cannot be physically deleted; disable the datasource instead.
  - `1108 FLOW_ACTOR_NODE_EMPTY`: Every approval step and execution actor list in a flow stage must contain at least one active user.
  - `1109 PASSWORD_MANAGED_BY_IDENTITY_SOURCE`: External LDAP/OIDC users cannot change or reset passwords locally in Yearning.
- **20xx AI Review Precheck, Daily Budget & Order Submission Gate**:
  - `2001 REVIEW_OUTDATED`: Precheck run is stale because the SQL, target datasource, rule set, skill/knowledge `config_hash`, or AI provider configuration changed, or the 30-minute precheck TTL expired; click **Run AI Precheck** again.
  - `2002 SQL_BATCH_TOO_LARGE`: SQL payload exceeds the batch limit (`32 MiB` / `100,000` statements per order, or `512 KiB` for a single statement); split into smaller change orders.
  - `2003 SQL_PARSE_FAILED`: SQL syntax error or unsupported dialect construct; fix the statement syntax for the target engine.
  - `2004 SQL_FINGERPRINT_FAILED`: Failed to compute the dialect AST fingerprint; verify SQL syntax and statement category (do not mix DML and DDL in one order).
  - `2005 SQL_ANONYMIZATION_FAILED`: Server-side SQL/prompt desensitization failed or detected a potential secret leak; remove raw credentials and retry.
  - `2006 TOO_MANY_FINGERPRINTS`: Order exceeds the maximum of **1,000 distinct SQL structural fingerprint groups**; split unrelated statements into separate orders.
  - `2007 REVIEW_COVERAGE_INCOMPLETE`: AI Review Precheck has not completed all fingerprint groups yet; wait for the run to reach `ready` or re-run precheck.
  - `2008 REVIEW_BLOCKED_BY_RISK`: AI Review Precheck produced one or more blocking `critical`/`high` risk findings; revise the SQL according to the suggestions and re-run precheck.
  - `2009 REVIEW_SCHEMA_INVALID`: AI Provider returned output that did not conform to the structured review JSON schema; retry precheck or verify the provider model in `/admin/review-engine/providers`.
  - `2010 AI_DAILY_BUDGET_EXHAUSTED`: Global daily AI cost budget (`ai_daily_budget_usage`) is exhausted when enforcement is enabled; an administrator can adjust the daily budget in `/admin/review-engine/ai-budget`.
  - `2011 AI_PROVIDER_FAILED`: No active AI Provider is configured/enabled, or all configured AI Providers failed/timed out. An administrator must add, test, and enable an HTTPS OpenAI-compatible AI Provider in `/admin/review-engine/providers`.
  - `2012 REVIEW_TOOL_FAILED`: Controlled read-only metadata/EXPLAIN inspection against the target datasource failed during precheck; verify the datasource `review` credential and network connectivity in `/admin/datasources`.
  - `2013 SUBMISSION_GATE_FAILED`: Order submission blocked because the bound precheck run is missing, not `ready`, or failed submission gate invariants.
  - `2014 FLOW_GRANT_REVOKED`: Current user no longer has permission to submit to the selected change flow (`/admin/permission-groups`).
- **30xx Change Order Approval, Execution, OSC (`gh-ost`) & Verification**:
  - `3001 NOT_FROZEN_ACTOR`: Current user is not in the frozen approver/executor snapshot for the active step or stage.
  - `3002 APPROVAL_ALREADY_DECIDED`: The current approval step has already been decided by another actor.
  - `3003 EXECUTION_ALREADY_STARTED`: Stage execution has already started or completed; duplicate execution is blocked.
  - `3004 EXECUTION_RETRY_FORBIDDEN`: Automatic or manual re-execution is forbidden in the current state (for `result_unknown`, complete manual verification first).
  - `3005 EXECUTION_RESULT_UNKNOWN`: Connection dropped after statement dispatch and the commit outcome is indeterminate; the frozen executor must inspect the target database and submit manual verification (`confirm_succeeded` or `confirm_failed`) on the order detail page.
  - `3006 EXECUTION_PREFLIGHT_FAILED`: Execution preflight check failed (e.g., statement fingerprint drift, cross-engine mismatch, or missing execution credential).
  - `3007 SCHEDULE_OUT_OF_RANGE`: Scheduled execution time (`scheduled_for`) is in the past or outside the allowed scheduling horizon.
  - `3008 OUTSIDE_EXECUTION_WINDOW`: Current or scheduled time falls outside the allowed weekly execution windows configured in `/admin/settings/execution`.
  - `3009 MISSED_SCHEDULE`: Scheduled order missed its execution window or was blocked by an unavailable datasource and requires manual rescheduling or cancellation.
  - `3010 DATASOURCE_UNAVAILABLE`: Target datasource connection failed; verify host, port, TLS material, and credentials in `/admin/datasources`.
  - `3011 GH_OST_UNSUPPORTED`: Target table or DDL statement is not eligible for built-in in-process `gh-ost` Online Schema Change (`pt-osc` is never supported).
  - `3012 MANUAL_VERIFICATION_EVIDENCE_REQUIRED`: Resolving `result_unknown` (`3005`) requires a non-empty verification note/evidence from the frozen executor.
- **40xx Query Workspace, Access Grants & Export (`/query`)**:
  - `4001 QUERY_GRANT_REQUIRED`: No active query access session exists; submit a Query Access Request in `/query` first.
  - `4002 QUERY_DATASOURCE_NOT_GRANTED`: Selected datasource is not included in your active query session's granted capabilities.
  - `4003 QUERY_EXPORT_NOT_GRANTED`: Current query capability has `can_export = false`; export is forbidden for this datasource.
  - `4004 QUERY_GRANT_REVOKED`: Query access grant was revoked by an administrator or permission group change.
  - `4005 QUERY_GRANT_EXPIRED`: Approved query access duration has expired; request a new query session in `/query`.
  - `4006 QUERY_SESSION_CLOSED`: Query session has already been closed.
  - `4007 QUERY_ONLY_SINGLE_SELECT`: Only a single read-only `SELECT` (including `WITH ... SELECT`), `SHOW`, or `EXPLAIN` statement is allowed in `/query`; DML/DDL and multi-statement batches are rejected.
  - `4008 QUERY_TIMEOUT_OUT_OF_RANGE`: Requested timeout exceeds the platform's configured query timeout bounds (`/admin/settings/query`).
  - `4009 QUERY_TIMEOUT`: Query execution exceeded the statement timeout (default `5,000 ms`); add index filters or narrow `LIMIT`.
  - `4010 QUERY_CURSOR_INVALID`: Result page cursor is invalid or belongs to a different query run; re-run the query.
  - `4011 QUERY_STATEMENT_REJECTED`: Statement violates read-only query security rules (e.g., `SELECT ... FOR UPDATE`, locking reads, or file/out-of-band functions).
- **50xx Legacy v3-to-v4 Migration (`/admin/migrations`)**:
  - `5001 MIGRATION_ACTIVE_WORK_EXISTS`: Source database still has active unclosed v3 tickets that block cutover.
  - `5002 MIGRATION_DRY_RUN_REQUIRED`: A passing dry-run reconciliation report is required before approval.
  - `5003 MIGRATION_MAPPING_UNCONFIRMED`: Candidate user/datasource/flow mappings must all be confirmed before approving migration.
  - `5004 MIGRATION_SOURCE_UNSUPPORTED`: Legacy source schema version is unsupported by the v4 migration tool.
  - `5005 MIGRATION_INVARIANT_FAILED`: Post-reconciliation integrity or envelope sealing invariant failed.
- **60xx Database AI Assistant**:
  - `6001 ASSISTANT_OUT_OF_SCOPE`: Request is outside database/Yearning scope or asks for forbidden high-risk SQL (`TRUNCATE`, `DROP DATABASE/SCHEMA`, `GRANT/REVOKE`).
  - `6002 ASSISTANT_DATASOURCE_FORBIDDEN`: Current user lacks permission on the selected datasource, or requested DML/DDL generation on a query-only datasource.
  - `6003 ASSISTANT_ANONYMIZATION_FAILED`: Input contained raw secrets/credentials (`password=`, `sk-`, DSN strings) or failed zero-leak verification; remove secrets and retry.
  - `6004 ASSISTANT_SQL_RESTORATION_INVALID`: Model-returned SQL failed dialect AST validation, referenced unauthorized tables/columns, or contained unbound placeholders.
  - `6005 ASSISTANT_DRAFT_REVISION_STALE`: SQL draft was modified after precheck or has not reached `ready` precheck status yet; click **Run Formal AI Precheck** on the draft card first.
  - `6006 ASSISTANT_CONTEXT_BUDGET_EXCEEDED`: Single-turn prompt/execution plan/metadata exceeded the context token budget; shorten the input or start a new session (`/clear`).
  - `6007 ASSISTANT_STREAM_IN_PROGRESS`: Another assistant reply is currently streaming in this session; wait for it to finish or click **Stop**.
- **70xx SQL Performance Diagnostics (`/admin/diagnostics`)**:
  - `7001 DIAGNOSTICS_DATASOURCE_FORBIDDEN`: Only authorized administrators can run or view diagnostics for the target datasource.
  - `7002 DIAGNOSTICS_NOT_ENABLED`: Datasource is disabled or lacks a valid read-only (`review` / `query`) credential.
  - `7003 DIAGNOSTICS_ENGINE_UNSUPPORTED`: Target datasource engine does not support Top SQL performance diagnostics.
  - `7004 DIAGNOSTICS_STATS_EXTENSION_DISABLED`: `performance_schema` (MySQL/TiDB) or `pg_stat_statements` (PostgreSQL) is not enabled on the target database.
  - `7005 DIAGNOSTICS_INSUFFICIENT_DB_PRIVILEGES`: Read-only credential lacks privilege to query `performance_schema.events_statements_summary_by_digest` or `pg_stat_statements`.
  - `7006 DIAGNOSTICS_SNAPSHOT_RATE_LIMITED`: Manual Top SQL snapshot collection is in cooldown; wait briefly before triggering another collection.
  - `7007 DIAGNOSTICS_CIRCUIT_BREAKER_OPEN`: Diagnostics collection is temporarily paused because the target database exceeded load protection thresholds.
  - `7008 DIAGNOSTICS_AI_TEMPLATE_UNAVAILABLE`: Statement digest has no desensitizable SQL text or active AI Provider available for 4-section AI diagnosis.
