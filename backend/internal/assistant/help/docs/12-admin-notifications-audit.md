---
id: help-admin-notifications-audit
title: Notification Channels, Audit Logs & Watermarked SQL Reveal (Admin)
source_ref: docs/features/08-notification-prd.md
route_path: /admin/audit
admin_only: true
keywords: notification, webhook, email, smtp, dingtalk, feishu, slack, delivery, dead_letter, rule, audit, audit log, records, watermark, reveal, security, compliance
summary: Administrators configure the 4 notification channel types (Email, DingTalk, Feishu, Slack), deterministic notification rules, and delivery logs in /admin/settings/notifications, and inspect 90-day append-only audit records in /records (/admin/audit) with watermarked SQL reveal.
---

## Steps
- Open `/admin/settings/notifications` to configure the 4 notification channel types (`email`, `dingtalk`, `feishu`, `slack`), run an async test delivery, create deterministic Notification Rules with live Preview, and inspect Delivery Logs.
- Open `/records` (alias `/admin/audit`) to search and inspect 90-day append-only audit events across authentication, orders, approvals, execution, queries, and admin changes.
- Use audited SQL Reveal on a change order (`order-review` or `rollback-order-sql`) when authorized to view plaintext SQL with a traceable viewer watermark (`username + timestamp + trace_id`).

## Detailed Guide
- **Notification Center (`/admin/settings/notifications` — 3 Views)**:
  1. **Notification Channels (4 Supported Channel Types)**:
     - Supported channel kinds (`S007`): **Email (`email`)**, **DingTalk (`dingtalk`)**, **Feishu (`feishu` custom bot)**, and **Slack (`slack` Incoming Webhook)**. Multiple channel instances of the same kind can be created.
     - Signing secrets, webhook URLs/tokens, and SMTP passwords are encrypted at rest inside an AES-256-GCM AEAD envelope and never echoed in API responses (returns `secret_configured: true/false` only).
     - Click **Test Channel** to create an asynchronous test delivery task and verify channel connectivity.
  2. **Notification Rules (Deterministic Routing, Custom Templates & Preview)**:
     - Each rule binds: **Rule Name** + **Enabled Switch** + **Flow Scope (`flow_id`, empty = all flows)** + **1 Trigger Event** + **1 Recipient Target (`target_kind`: `order_submitter`, `stage_approvers`, `stage_executors`, `grant_requester`, `builtin_admins`, or `channel_group`)** + **1 Target Channel** + optional **Custom Message Template (`message_template`, `<= 2000` chars)**, with non-sending **Preview** of recipient resolution and rendered message.
     - **Default Templates & Allowlisted Placeholders**: Leaving `message_template` empty uses the built-in default template for that `event_type` (populated with order ID, display number, title, submitter, submitted timestamp, state, approvers/approved timestamp, and executors/executed timestamp). Rules can also customize `message_template` using allowlisted `{{placeholder}}` variables (`{{order_display_number}}`, `{{order_no}}`, `{{order_id}}`, `{{order_title}}`, `{{event_type}}`, `{{event_label}}`, `{{order_state}}`, `{{flow_name}}`, `{{stage_position}}`, `{{stage_name}}`, `{{datasource_name}}`, `{{datasource_engine}}`, `{{submitter}}`, `{{submitter_username}}`, `{{submitted_at}}`, `{{approvers}}`, `{{approved_by}}`, `{{reviewer}}`, `{{approval_decision}}`, `{{approved_at}}`, `{{reviewed_at}}`, `{{executors}}`, `{{executed_by}}`, `{{executor}}`, `{{execution_started_at}}`, `{{executed_at}}`, `{{execution_finished_at}}`, `{{reason_code}}`, `{{order_url}}`, `{{grant_flow_name}}`, `{{grant_datasources}}`, `{{revoked_by}}`, `{{revoked_at}}`, `{{occurred_at}}`). Unknown placeholders, raw SQL, and credential patterns are rejected with `1001 REQUEST_VALIDATION_FAILED`.
     - **AI Precheck Events Structurally Excluded**: AI precheck completion, failure, `partial`, or `blocked` (`review.blocked`) events are **never pushed and cannot be subscribed to**. Subscribable events cover order submission (`change_order.submitted`), step activation (`change_order.step_activated`), approval progress (`change_order.approval_decided`), stage ready (`change_order.stage_ready_for_execution`), rejected/withdrawn/voided, execution scheduled/started/succeeded/failed/cancelled/`result_unknown`/missed schedule, query grant revoked, and AI budget/failover alerts.
     - **Deduplication & Zero-Leak Content**: Group IM channels (`dingtalk`, `feishu`, `slack`) send at most **1 group message per event and channel** regardless of recipient count; personal email deduplicates per user (`(domain_event_id, notification_channel_id, recipient_key)`). Messages never contain raw SQL, literals, query results, or free-text approval comments (`{{order_title}}` is sanitized to a single-line `<= 120`-character string and redacted if SQL/credential patterns appear); "View Order" links are generated from `general.public_base_url` (omitted when empty).
  3. **Delivery Logs**:
     - Tracks transactional Outbox deliveries across 5 states: `queued` -> `sending` -> `succeeded` / `failed` -> `dead_letter` (up to **5 attempts** with exponential backoff; `dead_letter` rows are retained for **90 days**). Unmatched rules, disabled channels, or already-handled todos are recorded as explainable skips.
- **Immutable Audit Records (`/records`, alias `/admin/audit`) & Watermarked SQL Reveal**:
  - Accessible to `builtin_admin` at `/records` (alias `/admin/audit`), recording append-only audit events (90-day retention) across authentication, change orders, AI precheck (including `review.identifier_displayed`), approvals, execution, rollback, query sessions/exports, AI Assistant turns, notifications, and admin settings.
  - **Audited SQL Reveal**: Plaintext SQL on change orders is never returned in default metadata responses (`has_sql: true`); viewing SQL triggers an audited reveal (`POST /api/v4/change-orders/{id}/sql-reveals` with `purpose = order-review | rollback-order-sql`), writes a secondary audit trail recording the viewer's identity, and displays the SQL with a **traceable security watermark** (`username + timestamp + trace_id`).
