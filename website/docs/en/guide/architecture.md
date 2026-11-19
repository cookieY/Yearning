# Architecture & Privacy Pipeline

Yearning v4 runs as a **stateless Go modular monolith** backed by **PostgreSQL** for shared state, execution leases, scheduled jobs, transactional outbox notifications, and multi-replica realtime SSE fan-out, with zero RPC dependencies.

## Technology Stack

| Layer | Technologies |
| :--- | :--- |
| **Control Plane** | Go 1.26 · Gin · GORM · `pgx/v5` · TiDB SQL Parser · `pg_query_go/v6` · In-process `gh-ost` · Zero RPC |
| **Web Workspace** | React 19 · TypeScript · Vite · Tailwind CSS v4 · shadcn/ui · Monaco Editor · Recharts |
| **Metadata & Security** | PostgreSQL 14–18 · 3-Role Isolation · AES-256-GCM AEAD Keyring · Double-submit CSRF · Argon2id · OIDC PKCE (`S256`) · LDAPS/StartTLS |
| **Packaging** | Multi-arch Distroless non-root read-only containers (`linux/amd64`, `linux/arm64`) |

## End-to-End Governance Dataflow

```text
Developer / AI Agent (Web UI or /api/v4/*)
        │
        ▼
[1] Dialect AST Lexing & Dual-Fingerprint Collapse (TiDB Parser / pg_query_go)
    - Up to 100,000 statements / 32 MiB collapsed into <= 1,000 structural groups
        │
        ├── default Track (Cloud OpenAI-Compatible APIs)
        │     └─► In-memory anonymizer (schema_1 / table_1 / col_1) + literal & comment stripping
        │     └─► AssertZeroLeak check (PrivacyContractHashDefault)
        │
        └── san Track (Self-Hosted RFC1918 vLLM / Ollama)
              └─► addrguard SSRF guard + compact versionless Raw-SQL Native contract
              └─► Volume guard (>256-char literal truncation) + PrivacyContractHashSAN
        │
        ▼
[2] 5 Read-Only Metadata Probes + Prompt Skills + Enterprise Knowledge Base
    - check_syntax / describe_table / list_indexes / table_stats / safe_explain
    - Mandatory policy hit or proven invalid execution -> deterministic Critical block
        │
        ▼
[3] Frozen Change Order (1..N serial stages · 1..10 approval steps)
    - Freezes SQL hash, review snapshots, datasource identity, approvers & executors
        │
        ▼
[4] Exclusive-Lease Execution Engine (DML TX / DDL / In-Process gh-ost)
    ├── Target Database Clusters (MySQL · PostgreSQL · TiDB · OceanBase · PolarDB)
    ├── Encrypted Rollback Evidence (order_rollback_evidences · AES-256-GCM · No Binlog/WAL)
    └── Transactional Outbox ──► 90-Day Audit Log · SSE Stream · Email / DingTalk / Feishu / Slack
```

## Core Security Invariants

1. **No Admin Business Override (`admin_is_not_business_override`)**: Platform admins govern `/admin/*` configuration, datasources, flows, and SQL diagnostics, but cannot modify frozen orders, approve/execute on behalf of frozen actors, or reveal unauthorized SQL.
2. **Six-Purpose AEAD Keyring & AAD Binding**: `keyring.json` isolates six cryptographic domains (`payload`, `sql_fingerprint`, `identifier_token`, `settings_impact_token`, `oidc_state`, `cursor_affinity`). Every ciphertext binds `deployment_id + table + primary_key + column + purpose` into AES-256-GCM AAD to block cross-row ciphertext substitution.
3. **Dual-Track AI Profile Isolation**: Mixed primary/fallback provider chains build payloads per endpoint from raw source and isolate `PrivacyContractHashDefault` vs `PrivacyContractHashSAN`, guaranteeing raw payloads prepared for local `san` models never fall back to public cloud endpoints.
4. **Transactional Outbox & Metadata-Only SSE**: State changes commit outbox records atomically with domain state. `GET /api/v4/events/stream` streams participant-scoped metadata frames (`io.yearning.v4.*`) with 25-second `: keepalive` heartbeats and never emits plaintext SQL or free-text comments.
