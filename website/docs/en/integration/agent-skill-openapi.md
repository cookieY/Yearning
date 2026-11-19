# AI Agent Skill & OpenAPI v4

Yearning v4 ships a complete **OpenAPI 3 specification** (`api/openapi/yearning-v4.yaml`), a **realtime SSE event bus** (`GET /api/v4/events/stream`), and a zero-dependency **`yearning` Agent Skill** (`skills/yearning/`).

AI coding assistants (Claude Code, Codex CLI, Cursor, Antigravity) integrate directly with `/api/v4/*` under full RBAC, double-submit CSRF, `Idempotency-Key`, `If-Match`, and Submission Gate enforcement without deploying a standalone MCP server.

## Quick Commands (`skills/yearning/scripts/yearning_api.py`)

```bash
export YEARNING_BASE_URL="http://localhost:8080"
export YEARNING_USERNAME="admin"
export YEARNING_PASSWORD_FILE="/path/to/password"

# Workbench & pending queues
python3 skills/yearning/scripts/yearning_api.py me
python3 skills/yearning/scripts/yearning_api.py orders --pending-my-approval

# Flow discovery, metadata inspection, AI precheck & order submission
python3 skills/yearning/scripts/yearning_api.py flows
python3 skills/yearning/scripts/yearning_api.py precheck \
  --flow-id <flow_id> \
  --sql "ALTER TABLE app.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"
python3 skills/yearning/scripts/yearning_api.py submit-order \
  --flow-id <flow_id> --run-id <run_id> \
  --title "Add status column" \
  --sql "ALTER TABLE app.users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active';"

# Governed read-only SELECT query (server-side PII masking applied automatically)
python3 skills/yearning/scripts/yearning_api.py query \
  --datasource-id <datasource_id> --schema-name app \
  --sql "SELECT id, email, created_at FROM users LIMIT 20"

# SQL Performance Diagnostics & 4-section AI diagnosis (admin)
python3 skills/yearning/scripts/yearning_api.py diagnostics overview \
  --datasource-id <datasource_id> --sort-by total_exec_time_ms
```

## HTTP Protocol Contract

1. **Envelope**: `{"err_code": 0, "message": "ok", "data": ...}`.
2. **Double-Submit CSRF**: Include `X-CSRF-Token: <yearning_csrf>` on every `POST`, `PUT`, `PATCH`, and `DELETE`.
3. **Idempotency**: Preserve `Idempotency-Key: <UUIDv4>` across retries of mutating requests.
4. **Optimistic Concurrency**: Supply `If-Match: "<version>"` on order decisions, withdrawals, execution attempts, schedules, and draft edits.
5. **Audited SQL Reveal**: Order metadata endpoints never inline plaintext SQL; call `POST /api/v4/change-orders/{id}/sql-reveals` explicitly.
6. **Realtime SSE Stream**: Subscribe to `GET /api/v4/events/stream` (`Accept: text/event-stream`) for participant-scoped `io.yearning.v4.*` events.
