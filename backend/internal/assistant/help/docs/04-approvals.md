---
id: help-approvals
title: Approval Center & Multi-Stage Order Review
source_ref: docs/prd/change-order-prd.md#approval-flow
route_path: /approvals
admin_only: false
keywords: approval, approve, reject, return, revisions, changes_requested, request_changes, stage, claim, todo, pending, signoff, multi-stage, flow, any_one, all_must_approve, 2001, 2003, 2009, 2010
summary: Approvers use Approval Center (/approvals) to review pending change orders and query access requests across multi-stage chains (any_one or all_must_approve), and choose Approve, Request Changes (changes_requested), or Reject (rejected).
---

## Steps
- Open `/approvals/changes` (contract alias `/approvals`) for change orders or `/approvals/query-access` for query access requests, and filter orders waiting on your active stage.
- Click an order row in `/approvals/changes` to open `/approvals/orders/:orderId` and inspect the frozen submission review findings, SQL statements, and stage voting mode (`any_one` or `all_must_approve`).
- Click Approve (`approve`), Request Changes (`request_changes` -> `changes_requested`), or Reject (`reject` -> `rejected`).

## Detailed Guide
- **Approval Queues & Role-Context Detail Split (`/approvals/changes` vs. `/changes/mine`)**:
  - **Change Order Approvals (`/approvals/changes`, alias `/approvals`)**: Lists all orders where you are a frozen stage approver, including a **Reviewed by Me** (`Yes` / `No`) column indicating whether your frozen step carries your own decision (`approved` or `rejected`). Opening or operating the approval page consumes the frozen submission review and never creates a new Review Run.
  - **Order Detail Role-Context Split**: Opening an order from the Approval Queue navigates to **`/approvals/orders/:orderId`**, which displays only approver actions (**Approve / Request Changes / Reject**); opening from My Orders (`/changes/mine`) navigates to **`/changes/orders/:orderId`**, which displays submitter actions (**Withdraw / Void**)—preventing action conflicts when one user holds both submitter and approver roles.
  - **Query Access Approvals (`/approvals/query-access`)**: Dedicated queue for reviewing and approving read-only query access requests.
- **How Multi-Stage Approval Chains Work (`/approvals`)**:
  - Every Change Order and Query Access Request follows the ordered stages (`position 1..N`) defined in its Approval Flow (`flows`).
  - Only the current stage is `active`; subsequent stages stay `waiting` until the active stage is completed.
  - Each stage supports two voting modes configured by administrators in `/admin/flows`:
    - **Any One Approver (`any_one`)**: As soon as any eligible approver in the stage approves, the stage completes and advances to the next stage.
    - **All Must Approve (`all_must_approve`)**: Every designated approver in the stage must approve before advancing.
- **3 Approval Decisions**:
  1. **Approve (`approve`)**: Records your approval for the active stage. When the final stage passes, a change order transitions to `approved` (or `scheduled` if a future `scheduled_at` time was set), and a query access request immediately issues an active query grant/session.
  2. **Request Changes (`request_changes`)**: Transitions the change order to `changes_requested` with your review comment. The submitter can edit the SQL directly in the same order, re-run AI Precheck, and resubmit without creating a new order.
  3. **Reject (`reject`)**: Terminally closes the order or request as `rejected`.
- **Eligibility, Concurrency & Real-Time Queue Sync (`GET /api/v4/events/stream`)**:
  - Only eligible approvers assigned to the currently `active` stage can submit a decision (`2010 APPROVAL_ACTOR_NOT_ELIGIBLE` otherwise).
  - If another approver in an `any_one` stage has already completed the stage, a duplicate vote returns `2009 APPROVAL_STAGE_ALREADY_DECIDED` or `1004 VERSION_CONFLICT`—simply refresh the page to see the updated stage status.
  - Approval queues (`/approvals`) and order lists (`/changes/mine`) subscribe to `GET /api/v4/events/stream` (`Accept: text/event-stream`) for participant-scoped real-time state updates (`change_order.*`, `execution.*`, `query_access.*`, `query_grant.*`); SSE frames carry only metadata and never expose SQL text or approval comments.
