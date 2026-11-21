# AI Providers, 48 Built-In Audit Rules & Knowledge Base

## 1. Dual-Track AI Provider Configuration (`/admin/review-engine/providers`)

- **Cloud OpenAI-Compatible (`default` Profile)**: Public endpoints require `https://` (OpenAI, DeepSeek, Qwen, OpenRouter). Every request runs in-memory anonymization + `AssertZeroLeak` and restores real identifiers only for authorized reviewers.
- **Self-Hosted LAN Models (`san` Profile)**: Private RFC1918 vLLM/Ollama endpoints allow `http://` or `https://` under `addrguard` SSRF protection. Models named `san-*` or `sql-audit-nano*` route to the compact Raw-SQL Native contract (`PrivacyContractHashSAN`) and never fall back to cloud `default` providers with raw payloads.
- **Reference Pricing & Global Daily Budget (`/admin/review-engine/ai-budget`)**: Configure `CNY` / `USD` per-1M-token timezone-aware pricing windows and enforce a shared global daily budget across AI Review Precheck, Database AI Assistant, and SQL Performance AI Diagnostics (`2010 AI_DAILY_BUDGET_EXHAUSTED` when exceeded).

## 2. Core Built-In Review Skills (48 Seeded Rules in `/admin/review-engine/skills`)

Custom Prompt Tools and tuned built-in parameters follow the `draft` -> `enabled` / `disabled` lifecycle; Prompt Tool workbench prompt optimization, test-case generation, and test execution require an enabled AI Provider and fail closed with `2011 AI_PROVIDER_FAILED` when unavailable.

| Category | Built-In `finding_key` Examples | Tunable Parameters |
| :--- | :--- | :--- |
| **DML Safety** | `builtin.dml-require-where`, `builtin.forbid-truncate`, `builtin.max-affect-rows`, `builtin.dml-insert-explicit-columns`, `builtin.dml-insert-max-batch-rows`, `builtin.dml-forbid-limit`, `builtin.dml-forbid-order-by`, `builtin.dml-forbid-subselect`, `builtin.max-in-value-count` | `{{max_affect_rows}}`, `{{max_batch_rows}}`, `{{max_in_values}}` |
| **DDL Table & Columns** | `builtin.ddl-require-primary-key`, `builtin.ddl-pk-named-id`, `builtin.ddl-pk-integer`, `builtin.ddl-pk-auto-increment`, `builtin.ddl-require-table-comment`, `builtin.ddl-require-column-comments`, `builtin.ddl-column-nullable-check`, `builtin.ddl-column-default-check`, `builtin.ddl-max-char-length`, `builtin.ddl-float-double-decimal`, `builtin.ddl-charset-allowlist` | `{{max_char_length}}`, `{{max_varchar_length}}`, `{{allowed_charsets}}` |
| **DDL Destructive & Indexes** | `builtin.ddl-forbid-drop-database`, `builtin.ddl-forbid-drop-table`, `builtin.ddl-forbid-across-db-rename`, `builtin.ddl-forbid-column-type-change`, `builtin.ddl-forbid-foreign-key`, `builtin.ddl-forbid-unnamed-index`, `builtin.ddl-index-name-prefix`, `builtin.ddl-max-index-count`, `builtin.ddl-max-index-key-parts`, `builtin.ddl-max-affect-rows`, `builtin.ddl-multi-alter` | `{{max_index_count}}`, `{{max_index_key_parts}}`, `{{max_ddl_affect_rows}}` |

## 3. Zero-Leak Table-Scoped Knowledge Entries (`/admin/review-engine/knowledge`)

Reference real database objects using typed catalog tokens so the compiler anonymizes them automatically on `default` cloud tracks:

```text
Table {{ref:table:["billing","ledger_entries"]}} receives >5,000 writes/sec.
Avoid adding secondary indexes on {{ref:column:["billing","ledger_entries","remark"]}} without DBA review.
```

Any actual violation of an enabled flow-bound skill or scope-matched knowledge entry escalates deterministically to **`critical`** and blocks order submission (`2008`).
