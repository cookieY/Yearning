# 15-Minute First-Boot Setup Guide

After signing in as `admin`, follow this exact 6-step dependency order so your engineering team can submit their first governed change order and read-only query without hitting `1001 VALIDATION_FAILED`:

1. **Add an AI Provider (`/admin/review-engine/providers`)**: Configure an HTTPS OpenAI-compatible cloud endpoint (`default` anonymized profile) or an internal RFC1918 vLLM/Ollama endpoint (`san` profile for `SAN-2B`/`SAN-4B`/`SQL-Audit-Nano`), run **Test Connection**, and enable it.
2. **Enable Built-in Skills & Create a Rule Set (`/admin/review-engine/skills` -> `/admin/rule-sets`)**: Enable baseline skills (`builtin.dml-require-where`, `builtin.forbid-truncate`, `builtin.ddl-require-primary-key`, `builtin.max-affect-rows`) and combine them into a published Rule Set. Every `change_review` flow requires a non-empty `rule_set_id`.
3. **Register Target Datasources (`/admin/datasources`)**: Create dedicated read-only (`yearning_ro`) and execution (`yearning_exec`) DB accounts (see [Datasources & DB Grant SQL](/en/admin/datasources-and-grants)), fill mandatory `version_constraint` (e.g. `>=8.0,<9.0`), and verify connection + identity fingerprint.
4. **Create Users & Approval/Query Flows (`/admin/users` -> `/admin/flows`)**: Configure `change_review` (`1..N` serial datasource stages, `1..10` approval steps, executors) and `query_access` (datasources, optional `can_export`, sensitive column masking).
5. **Grant Flows via Permission Groups (`/admin/permission-groups`)**: Add developers/DBAs to groups and attach the authorized flows. Note that built-in `admin` does not implicitly override frozen business flows.
6. **Configure `public_base_url` & Notifications (`/admin/settings/general` & `/notifications`)**: Set your public HTTPS URL (used for OIDC callbacks and order links) and configure Email / DingTalk / Feishu / Slack rules.
