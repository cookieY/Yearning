---
id: help-admin-review-rules
title: AI Review Engine Rules, Rule Sets, Prompt Tools & Knowledge Base (Admin)
source_ref: docs/prd/ai-sql-review-engine-prd.md#rule-management
route_path: /admin/review-engine/rules
admin_only: true
keywords: review engine, rule, rules, review rule, rule set, prompt tool, skill, knowledge entry, knowledge base, severity, critical, high, publish, rollout, threshold, max_affect_rows, builtin, 2008, 2009, 2011
summary: Administrators manage Prompt Tools / AI Review Skills (/admin/review-engine/skills, alias /admin/review-engine/rules), Internal Experience / Domain Knowledge (/admin/review-engine/knowledge), and Rule Set combinations (/admin/rule-sets, alias /admin/review-engine/rule-sets) bound to change_review flows.
---

## Steps
- Open `/admin/review-engine/skills` (alias `/admin/review-engine/rules`) to manage built-in (`is_builtin`) and custom Prompt Tools across `draft`, `enabled`, and `disabled` states and tune parameter values (such as `max_affect_rows`).
- Open `/admin/review-engine/knowledge` to maintain `global`, `datasource`, or `table`-scoped DBA guidelines with typed catalog-backed object references.
- Combine published (`enabled` / `disabled`) Prompt Tools into a versioned Rule Set in `/admin/rule-sets` (alias `/admin/review-engine/rule-sets`) and bind it to `change_review` flows in `/admin/flows`.

## Detailed Guide
- **1. Prompt Tools / AI Review Skills (`/admin/review-engine/skills`, alias `/admin/review-engine/rules`)**:
  - **Built-in vs. Custom Skills**: Built-in skills (`is_builtin = true`, e.g., require Primary Key on `CREATE TABLE`, forbid `SELECT *`, require `WHERE` on `UPDATE`/`DELETE`, cap index count and composite index columns, enforce `max_affect_rows`) lock their definition, name, engine, and parameter keys; administrators can toggle their state (`enabled` / `disabled`) and edit tunable parameter values, while deletion is disabled. Custom skills support `all`, `mysql`, or `postgresql` engines and reusable `{{variable}}` parameters.
  - **Lifecycle, Inline Eval Gate & Workbench AI Fail Closed**: Skills follow the `draft` -> `enabled` / `disabled` lifecycle with an immutable `config_hash` per save. Enabling a skill runs an inline **Eval gate** at save time; modifying a skill re-hashes its config and marks unsubmitted precheck runs referencing the old hash as `outdated`. Prompt Tool workbench prompt optimization, test-case generation, and test execution require an enabled AI Provider and fail closed with `2011 AI_PROVIDER_FAILED` when no active provider is available or output validation fails (never fabricating synthetic passing results).
- **2. Internal Experience / Enterprise Knowledge Base (`/admin/review-engine/knowledge`)**:
  - Follows the same `draft` / `enabled` / `disabled` lifecycle and `config_hash` invalidation model, with a dedicated evaluations endpoint.
  - **3 Scope Levels**: `global` (applies to all stages), `datasource` (`datasource_id + database_name`), or `table` (`datasource_id + database_name + table_name`).
  - **Zero-Leak Typed Object References**: Supports catalog-backed inline references (`{{ref:table:["physical_schema","table"]}}`) that are verified against the live authorized catalog and compiled into anonymized Run tokens before calling the AI Provider so real object names never leak.
- **3. Rule Sets (`/admin/rule-sets`, alias `/admin/review-engine/rule-sets`)**:
  - In YearningX v4, a Rule Set is a **pure Prompt Tool (`skills`) combination** (`name`, `enabled`, `prompt_tool_ids`, with keyword search and select-all across non-draft skills; `draft` skills cannot be bound).
  - Bind Rule Sets to `change_review` Approval Flows (`flows.rule_set_id` in `/admin/flows`); editing a Rule Set displays reverse flow impact and invalidates unsubmitted prechecks referencing the old hash (`outdated`) while preserving frozen snapshots on submitted orders.
- **4. Critical Review Blocking Governance (`RCP-20261025-CRITICAL-REVIEW-BLOCKING`)**:
  - Any actual violation of an enabled, flow-bound Prompt Tool (`policy_finding`) or scope-matched Internal Experience entry always produces an effective **`critical`** finding and **blocks order submission (`2008 REVIEW_BLOCKED_BY_RISK`)**.
  - The legacy `max_severity` field on Prompt Tool and Knowledge definitions is retained strictly as historical compatibility metadata—it never downgrades a rule hit or allows a violated mandatory rule to pass.
