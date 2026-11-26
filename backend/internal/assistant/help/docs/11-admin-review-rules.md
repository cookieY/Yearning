---
id: help-admin-review-rules
title: AI Review Engine Rules, Rule Sets, Prompt Tools & Knowledge Base (Admin)
source_ref: docs/ai-review-production-prd.md
route_path: /admin/review-engine/rules
admin_only: true
keywords: review engine, rule, rules, review rule, rule set, prompt tool, skill, knowledge entry, knowledge base, severity, critical, high, publish, rollout, threshold, max_affect_rows, builtin, 2008, 2009, 2011
summary: Administrators manage Prompt Tools / AI Review Skills (/admin/review-engine/skills, alias /admin/review-engine/rules), Internal Experience / Domain Knowledge (/admin/review-engine/knowledge), and Rule Set combinations (/admin/rule-sets, alias /admin/review-engine/rule-sets) bound to change_review flows.
---

## Steps
- Open `/admin/review-engine/skills` (alias `/admin/review-engine/rules`) to manage built-in (`is_builtin`) and custom Prompt Tools across `draft`, `enabled`, and `disabled` states and tune parameter values (such as `max_affect_rows`).
- Open `/admin/review-engine/knowledge` to maintain `datasource` or `table`-scoped non-blocking Internal Experience (`info`, `low`, or `medium`) with typed catalog-backed object references.
- Combine published (`enabled` / `disabled`) Prompt Tools into a versioned Rule Set in `/admin/rule-sets` (alias `/admin/review-engine/rule-sets`) and bind it to `change_review` flows in `/admin/flows`.

## Detailed Guide
- **1. Prompt Tools / AI Review Skills (`/admin/review-engine/skills`, alias `/admin/review-engine/rules`)**:
  - **Built-in vs. Custom Skills**: Built-in skills (`is_builtin = true`, e.g., require Primary Key on `CREATE TABLE`, forbid `SELECT *`, require `WHERE` on `UPDATE`/`DELETE`, cap index count and composite index columns, enforce `max_affect_rows`) lock their definition, name, engine, and parameter keys; administrators can toggle their state (`enabled` / `disabled`) and edit tunable parameter values, while deletion is disabled. Custom skills support `all`, `mysql`, or `postgresql` engines and reusable `{{variable}}` parameters.
  - **Lifecycle, Inline Eval Gate & Workbench AI Fail Closed**: Skills follow the `draft` -> `enabled` / `disabled` lifecycle with an immutable `config_hash` per save. Enabling a skill runs an inline **Eval gate** at save time; modifying a skill re-hashes its config and marks unsubmitted precheck runs referencing the old hash as `outdated`. Prompt Tool workbench prompt optimization, test-case generation, and test execution require an enabled AI Provider and fail closed with `2011 AI_PROVIDER_FAILED` when no active provider is available or output validation fails (never fabricating synthetic passing results).
- **2. Internal Experience / Enterprise Knowledge Base (`/admin/review-engine/knowledge`)**:
  - Follows the same `draft` / `enabled` / `disabled` lifecycle and `config_hash` invalidation model, with a dedicated evaluations endpoint.
  - **2 Allowed Scopes (`datasource` and `table`)**: Authors may only create or update `datasource` (`datasource_id + database_name`) or `table` (`datasource_id + database_name + table_name`) entries. The legacy `global` scope is retired: existing `global` entries are disabled on upgrade and kept read/delete-only until re-scoped to `datasource` or `table`.
  - **Non-Blocking Advisory Severity (`info` / `low` / `medium`)**: Internal Experience entries carry an advisory ceiling (`info`, `low`, or `medium`) and never block order submission; blocking rules belong in Review Skills.
  - **Shared with AI Assistant (`sql_generate` / `sql_optimize`)**: Enabled `datasource` and `table` entries matching the active assistant datasource/schema/tables are injected into AI Assistant SQL generation and optimization turns (bounded to 8 entries / 2,000 runes) to prevent issues proactively before formal AI precheck.
  - **Zero-Leak Typed Object References**: Supports catalog-backed inline references (`{{ref:table:["physical_schema","table"]}}`) that are verified against the live authorized catalog and compiled into anonymized Run tokens before calling default AI Providers so real object names never leak.
- **3. Rule Sets (`/admin/rule-sets`, alias `/admin/review-engine/rule-sets`)**:
  - In YearningX v4, a Rule Set is a **pure Prompt Tool (`skills`) combination** (`name`, `enabled`, `prompt_tool_ids`, with keyword search and select-all across non-draft skills; `draft` skills cannot be bound).
  - Bind Rule Sets to `change_review` Approval Flows (`flows.rule_set_id` in `/admin/flows`); editing a Rule Set displays reverse flow impact and invalidates unsubmitted prechecks referencing the old hash (`outdated`) while preserving frozen snapshots on submitted orders.
- **4. Finding & Blocking Governance (`RCP-20261126-KNOWLEDGE-ASSISTANT-SYNC`)**:
  - Any actual violation of an enabled, flow-bound **Prompt Tool / Review Skill** (`policy_finding`) always produces an effective **`critical`** finding and **blocks order submission (`2008 REVIEW_BLOCKED_BY_RISK`)**; its legacy `max_severity` metadata never downgrades a mandatory skill hit.
  - Scope-matched **Internal Experience** entries (`knowledge_advisory`) are governed strictly as non-blocking advisories capped at `min(max_severity, medium)` (and legacy `high`/`critical` knowledge rows are clamped to `medium` at runtime until updated).
