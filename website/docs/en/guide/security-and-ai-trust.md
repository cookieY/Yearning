# Enterprise Security & AI Trust Architecture

Enterprise teams adopting AI for production database governance ask three decisive questions: **1) How do you guarantee schema and production data never leak to LLMs? 2) How do you guarantee AI is strictly non-intrusive? 3) How do you prevent AI hallucinations from blocking valid releases or generating broken SQL?**

Yearning v4 enforces every guarantee in Go runtime code rather than prompt wishes.

---

## 1. Zero-Leak AI Privacy (Dual-Track Isolation)

### Cloud OpenAI-Compatible Track (`default`)
1. **Literal & Comment Stripping**: Dialect lexers (`TiDB Parser` / `pg_query_go`) strip every business literal and comment before LLM dispatch, replacing values with typed placeholders (`:val_str_1`, `:val_int_1`).
2. **Per-Request In-Memory Identifier Anonymization**: `RunMapper` + `TurnValueMapper` rewrite real schemas, tables, and columns into `schema_1`, `table_1`, `column_1`. Mapping tables exist **strictly in per-HTTP-request server memory** and are freed immediately upon request exit; they are never persisted, logged, cached, or returned to browsers.
3. **Policy & Knowledge Anonymization**: Custom review skills and table-scoped knowledge entries (`{{ref:table:["schema","table"]}}`) compile through the same anonymizer so policy prose never leaks real object names.
4. **Fail-Closed Zero-Leak Assertion (`AssertZeroLeak` / `AssertNoLeak`)**: Any residual sensitive token, secret (`password=`, `sk-`, DSN), or unmapped literal aborts the outbound request immediately (`2005` / `6003`) without ever falling back to plaintext.
5. **Zero Row Sampling**: Read-only probes inspect schema metadata, indexes, and safe execution plans only; business table rows are never queried or sampled.
6. **Audited Restoration (`R014`)**: Stored evidence stays anonymized; only authorized participants (submitter, frozen approvers/executors) see real names restored in the UI with a `review.identifier_displayed` audit record.

### Self-Hosted Internal Track (`san`) & Cross-Profile Isolation
- Deploy self-hosted vLLM/Ollama (`SAN-2B`, `SAN-4B`, `SQL-Audit-Nano`) on private RFC1918 LANs protected by `addrguard` SSRF checks so zero bytes leave your corporate network.
- Mixed primary/fallback provider chains isolate `PrivacyContractHashDefault` vs. `PrivacyContractHashSAN` and rebuild payloads per target endpoint from raw source, guaranteeing raw payloads prepared for local `san` endpoints **never fall back to cloud `default` providers**.

---

## 2. Strictly Non-Intrusive AI

- **AI Precheck**: Calls 5 read-only metadata tools only; produces structured findings only; cannot approve, execute, or bypass the Submission Gate.
- **Database AI Assistant**: **Never executes any SQL (not even `SELECT` or `EXPLAIN`)**. Generated drafts carry `draft_id + revision + sql_hmac` and must pass formal AI Precheck (`StartEphemeralRun`) and multi-stage Approval (`SubmitOrderV2`).
- **SQL Performance Diagnostics**: Reuses read-only `query`/`review` pools exclusively (never falls back to `execution` credentials), never installs extensions, never modifies DB parameters or resets counters, and never runs `EXPLAIN ANALYZE` on production.
- **Frozen Governance (`admin_is_not_business_override`)**: Submitted orders freeze stages, approvers, executors, SQL hash, and review snapshots; even built-in `admin` cannot override or execute on behalf of frozen actors.

---

## 3. Anti-Hallucination Guardrails

1. **Three-Tier Finding Governance**: Violations of enabled rules/knowledge (`policy_finding`) and runtime-proven invalid statements (`proven_invalid_execution`) deterministically block as `Critical` (`2008`), whereas open-ended LLM suggestions (`agent_observation`) are **capped at `Medium`** so model hallucinations can never block valid deployments on their own.
2. **Fail-Closed `partial` Gate**: Missing or truncated catalog metadata locks precheck in `partial` status instead of letting the LLM guess "safe".
3. **Plan Fact Verification (`ValidateNoHallucinatedPlanFacts`)**: Verifies that tables, indexes, and plan facts cited by AI reports originate from actual read-only tool outputs.
4. **Strict JSON Schema + Dialect AST Verification**: Precheck output must validate against strict JSON schemas (`2009`); AI Assistant SQL must pass dialect AST parsing, authorized object bounds, and typed placeholder verification (`6004`), and must ask clarifying questions instead of inventing values for missing `NOT NULL` columns.

---

## 4. Platform Defense-in-Depth

- **6-Purpose AES-256-GCM AEAD Keyring + AAD Binding**: Every ciphertext binds `deployment_id + table + primary_key + column + purpose` into AAD; `keyring.json` requires `0400/0440` permissions.
- **Least-Privilege Roles**: Separated read-only vs. execution credentials on target DBs with TLS verification and `identity_fingerprint`; mandatory 3-role PostgreSQL separation in production (`yearning_app`, `yearning_migrator`, `yearning_retention`).
- **Hardened Auth, Transport & Runtime**: Argon2id password hashes, 5-attempt admin lockout with local TTY-only reset (`./yearning --reset-admin-password`), mandatory `LDAPS`/`StartTLS` and OIDC PKCE (`S256`), double-submit CSRF, `addrguard` anti-SSRF, watermarked audited SQL Reveal (`username + timestamp + trace_id`), 90-day append-only audit logs, and non-root read-only Distroless containers.
