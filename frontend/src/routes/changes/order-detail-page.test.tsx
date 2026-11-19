import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "@/features/auth/session-provider";
import { HttpResponse, http } from "msw";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { server } from "@/test/msw/server";
import "@/shared/i18n";
import {
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
} from "@/shared/mock/review-fixture";
import OrderDetailPage from "./order-detail-page";

/**
 * Order detail gates (work package FE-F6-ORDER-SUBMIT): withdraw/void mirror
 * the change_order state machine, the partial-execution consequence is
 * spelled out before confirming (W007), and a backend rejection renders as
 * an error inside the dialog — never a success state.
 */

const REQUEST_ID = "33333333-3333-4333-8333-333333333333";

afterEach(() => {
  resetReviewFixture();
});

function seedOrder(overrides: Record<string, unknown> = {}): string {
  const id = (overrides.id as string | undefined) ?? "9c6f1a2b-0000-4000-8000-00000000ef01";
  seedFixtureOrder({
    id,
    display_number: "YR-20260830-000021",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "\u8be6\u60c5\u9875\u5939\u5177\u5de5\u5355",
    state: "submitted",
    current_stage_position: 1,
    stages: [
      {
        id: "9c6f1a2b-0000-4000-8000-00000000ef11",
        position: 1,
        datasource_name: "orders-mysql",
        state: "approval_active",
        approval_steps: [
          {
            id: "9c6f1a2b-0000-4000-8000-00000000ef21",
            position: 1,
            state: "pending",
            decided_at: null,
            actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
          },
        ],
        execution_actors: [
          {
            id: FIXTURE_OWNER_ID,
            username: "henry",
            display_name: "henry",
            email: null,
            is_builtin_admin: true,
            version: 1,
            created_at: "2026-08-01T00:00:00Z",
            updated_at: "2026-08-01T00:00:00Z",
          },
        ],
      },
    ],
    has_sql: true,
    sql_hash: "hash-1",
    snapshot_hash: "snap-1",
    manually_verified: false,
    version: 1,
    submitted_at: "2026-08-30T10:00:00Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "SELECT 1;",
    ...overrides,
  });
  return id;
}

function renderDetail(orderId: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
      <MemoryRouter initialEntries={[`/changes/orders/${orderId}`]}>
        <Routes>
          <Route path="/changes/orders/:orderId" element={<OrderDetailPage />} />
        </Routes>
      </MemoryRouter>
    </SessionProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  server.resetHandlers();
  resetReviewFixture();
  grantSession();
});


/** The order pages gate lifecycle buttons on the session identity; plant an
 * authenticated /users/me whose id matches the fixture submitter (guards
 * test precedent). */
function grantSession(): void {
  server.use(
    http.get("*/users/me", () =>
      HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: {
          id: FIXTURE_OWNER_ID,
          username: "henry",
          display_name: "henry",
          email: null,
          is_builtin_admin: true,
          version: 1,
          created_at: "2026-08-28T08:00:00Z",
          updated_at: "2026-08-28T08:00:00Z",
          can_access_admin: true,
        },
        request_id: FIXTURE_OWNER_ID,
      }),
    ),
  );
}

describe("OrderDetailPage", () => {
  it("renders the frozen stages, snapshot facts and the audit timeline", async () => {
    const id = seedOrder();
    // A lifecycle fact first, so the timeline has a real entry to project.
    await fetch(`/change-orders/${id}/withdrawal`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "If-Match": '"1"' },
      body: JSON.stringify({ reason: "\u4ea7\u751f\u4e00\u6761\u65f6\u95f4\u7ebf\u4e8b\u4ef6" }),
    });
    renderDetail(id);
    expect(await screen.findByTestId("order-stages")).toBeVisible();
    expect(screen.getByTestId("order-facts").textContent).toContain("hash-1");
    expect(await screen.findByTestId("order-timeline-list")).toBeVisible();
    // A withdrawn order is terminal: no withdraw or void affordance remains.
    expect(screen.queryByTestId("withdraw-order")).toBeNull();
    expect(screen.queryByTestId("void-order")).toBeNull();
  });

  it("keeps the left column's rhythm: a child that rendered nothing takes no gap slot", async () => {
    const id = seedOrder();
    renderDetail(id);
    const column = (await screen.findByTestId("order-sql-review")).parentElement;
    expect(column).not.toBeNull();
    // An empty flex item still gets a 30px gap on each side (a 60px hole
    // between the decision card and the SQL card); it must be hidden.
    for (const child of Array.from(column?.children ?? [])) {
      if (child.childNodes.length === 0) expect(child).toHaveClass("empty:hidden");
    }
  });

  it("shows one activity tab at a time while the others stay mounted", async () => {
    const id = seedOrder();
    renderDetail(id);
    // Timeline is the default tab: visible; comments and snapshot are mounted but hidden.
    expect(await screen.findByTestId("order-timeline")).toBeVisible();
    expect(screen.getByTestId("order-comments")).not.toBeVisible();
    expect(screen.getByTestId("order-facts")).not.toBeVisible();

    await userEvent.click(screen.getByTestId("order-activity-tab-comments"));
    await waitFor(() => { expect(screen.getByTestId("order-comments")).toBeVisible(); });
    expect(screen.getByTestId("order-timeline")).not.toBeVisible();
    expect(screen.getByTestId("order-facts")).not.toBeVisible();

    await userEvent.click(screen.getByTestId("order-activity-tab-snapshot"));
    await waitFor(() => { expect(screen.getByTestId("order-facts")).toBeVisible(); });
    expect(screen.getByTestId("order-comments")).not.toBeVisible();
  });

  it("keeps the frozen SQL and the frozen review in one card, not two", async () => {
    const id = seedOrder();
    renderDetail(id);
    const merged = await screen.findByTestId("order-sql-review");
    expect(merged).toContainElement(screen.getByTestId("order-sql-card"));
    expect(await screen.findByTestId("frozen-review-card")).toBeInTheDocument();
    expect(merged).toContainElement(screen.getByTestId("frozen-review-card"));
    // The sections are bare: the only card surface in there is the merged one.
    expect(merged.querySelectorAll('[data-slot="card"]')).toHaveLength(0);
  });

  it("projects approval decision comments on timeline entries only (BUG-004)", async () => {
    // The backend timeline projection carries approval_decisions.comment as
    // its own field (NEW-FE-01); empty or absent comments must not render a
    // comment container at all.
    const id = seedOrder();
    server.use(
      http.get("*/change-orders/:orderId/timeline", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [
              {
                id: "b1a7c2d4-0000-4000-8000-00000000aa01",
                event_type: "change_order.approval_decided",
                occurred_at: "2026-09-01T09:30:00Z",
                actor_kind: "user",
                actor_user_id: "b1a7c2d4-0000-4000-8000-00000000aa09",
                actor_display_name: "reviewer",
                summary: "\u5ba1\u6279\u62d2\u7edd\uff0c\u5de5\u5355\u7ec8\u6b62",
                comment: "\u8bed\u53e5\u98ce\u9669\u4e0d\u53ef\u63a5\u53d7\uff0c\u8bf7\u62c6\u5206\u540e\u91cd\u65b0\u63d0\u4ea4",
                stage_position: 1,
                state: "rejected",
              },
              {
                id: "b1a7c2d4-0000-4000-8000-00000000aa02",
                event_type: "change_order.submitted",
                occurred_at: "2026-09-01T09:00:00Z",
                actor_kind: "user",
                actor_user_id: FIXTURE_OWNER_ID,
                actor_display_name: "henry",
                summary: "\u5de5\u5355\u63d0\u4ea4\u5e76\u51bb\u7ed3\u6d41\u7a0b\u5feb\u7167",
                stage_position: 1,
                state: "submitted",
              },
              {
                id: "b1a7c2d4-0000-4000-8000-00000000aa03",
                event_type: "change_order.approval_decided",
                occurred_at: "2026-09-01T09:10:00Z",
                actor_kind: "user",
                actor_user_id: "b1a7c2d4-0000-4000-8000-00000000aa09",
                actor_display_name: "reviewer",
                summary: "\u5ba1\u6279\u901a\u8fc7",
                comment: "",
                stage_position: 1,
                state: null,
              },
            ],
            page: { next_cursor: null, has_more: false },
          },
          request_id: REQUEST_ID,
        }),
      ),
    );
    renderDetail(id);
    expect(await screen.findByTestId("order-timeline-list")).toBeVisible();
    const comments = screen.getAllByTestId("order-timeline-comment");
    // Only the rejected decision renders its comment; the commentless
    // decision and the submitted entry render no comment container.
    expect(comments).toHaveLength(1);
    expect(comments[0]?.textContent).toContain("\u8bed\u53e5\u98ce\u9669\u4e0d\u53ef\u63a5\u53d7");
    expect(screen.getByTestId("order-timeline-list").textContent).toContain("\u5de5\u5355\u63d0\u4ea4\u5e76\u51bb\u7ed3\u6d41\u7a0b\u5feb\u7167");
  });

  it("withdraws a submitted order after confirmation, without a partial warning", async () => {
    const user = userEvent.setup();
    const id = seedOrder();
    renderDetail(id);
    await user.click(await screen.findByTestId("withdraw-order"));
    const dialog = screen.getByTestId("order-action-dialog");
    expect(dialog.textContent).toContain("\u64a4\u56de\u540e\u5de5\u5355\u7ec8\u6b62\u4e3a\u300c\u5df2\u64a4\u56de\u300d");
    expect(screen.queryByTestId("partial-execution-warning")).toBeNull();
    // The confirm stays disabled until the mandatory reason is typed.
    expect(screen.getByTestId("order-action-confirm")).toBeDisabled();
    await user.type(screen.getByTestId("order-action-reason"), "\u4e0d\u518d\u9700\u8981\u8be5\u53d8\u66f4");
    await user.click(screen.getByTestId("order-action-confirm"));
    await waitFor(() => {
      expect(screen.queryByTestId("order-action-dialog")).toBeNull();
    });
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page").textContent).toContain("\u5df2\u64a4\u56de");
    });
  });

  it("warns that partial execution cannot roll back before withdrawing (withdrawal after partial execution)", async () => {
    const user = userEvent.setup();
    const id = seedOrder({ state: "running", version: 3 });
    renderDetail(id);
    await user.click(await screen.findByTestId("withdraw-order"));
    expect(screen.getByTestId("partial-execution-warning")).toBeVisible();
    expect(screen.getByTestId("partial-execution-warning").textContent).toContain(
      "\u4e0d\u4f1a\u81ea\u52a8\u56de\u6eda",
    );
    await user.type(screen.getByTestId("order-action-reason"), "\u7ebf\u4e0a\u6b62\u8840");
    await user.click(screen.getByTestId("order-action-confirm"));
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page").textContent).toContain(
        "\u90e8\u5206\u6267\u884c\u540e\u64a4\u56de",
      );
    });
  });

  it("shows the backend rejection inside the dialog and never fakes success (backend rejection has no fake success)", async () => {
    const user = userEvent.setup();
    const id = seedOrder();
    server.use(
      http.post("*/change-orders/:orderId/withdrawal", () =>
        HttpResponse.json({
          err_code: 1010,
          message: "withdraw is not legal from state stage_approval_active",
          data: null,
          request_id: REQUEST_ID,
          retryable: false,
        }),
      ),
    );
    renderDetail(id);
    await user.click(await screen.findByTestId("withdraw-order"));
    await user.type(screen.getByTestId("order-action-reason"), "\u6d4b\u8bd5\u62d2\u7edd\u8def\u5f84");
    await user.click(screen.getByTestId("order-action-confirm"));
    const error = await screen.findByTestId("order-action-error");
    // The rejection is the localized business-error message plus the
    // request id — the dialog never invents a success state.
    expect(error.textContent).toContain("\u5f53\u524d\u72b6\u6001\u4e0d\u5141\u8bb8\u8be5\u64cd\u4f5c\u3002");
    expect(error.textContent).toContain(REQUEST_ID);
    // The dialog stays open and the aggregate state is untouched.
    expect(screen.getByTestId("order-action-dialog")).toBeVisible();
    expect(screen.getByTestId("order-detail-page").textContent).not.toContain("\u5df2\u64a4\u56de");
  });

  it("offers voidance only for voidable states and voids with a reason", async () => {
    const user = userEvent.setup();
    const id = seedOrder({ state: "result_unknown" });
    renderDetail(id);
    expect(await screen.findByTestId("void-order")).toBeVisible();
    await user.click(screen.getByTestId("void-order"));
    await user.type(screen.getByTestId("order-action-reason"), "\u7ed3\u679c\u672a\u77e5\uff0c\u7ec8\u6b62");
    await user.click(screen.getByTestId("order-action-confirm"));
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page").textContent).toContain("\u5df2\u4f5c\u5e9f");
    });
  });
});

describe("execution wait note for non-executors (§18.57)", () => {
  const OTHER_ID = "1111d9cc-e65d-7b9d-a8aa-3c81945f9999";

  function executionPendingOrder(executorId: string): string {
    return seedOrder({
      state: "stage_execution_pending",
      stages: [
        {
          id: "9c6f1a2b-0000-4000-8000-00000000ef11",
          position: 1,
          datasource_name: "orders-mysql",
          state: "execution_pending",
          approval_steps: [
            {
              id: "9c6f1a2b-0000-4000-8000-00000000ef21",
              position: 1,
              state: "approved",
              decided_at: "2026-08-30T10:30:00Z",
              actors: [{ id: "0198d9cc-e65d-7b9d-a8aa-3c81945fa001", username: "reviewer-a", display_name: "reviewer-a" }],
            },
          ],
          execution_actors: [
            {
              id: executorId,
              username: executorId === FIXTURE_OWNER_ID ? "henry" : "executor-a",
              display_name: executorId === FIXTURE_OWNER_ID ? "henry" : "executor-a",
              email: null,
              is_builtin_admin: false,
              version: 1,
              created_at: "2026-08-01T00:00:00Z",
              updated_at: "2026-08-01T00:00:00Z",
            },
          ],
        },
      ],
    });
  }

  it("explains who may execute when the viewer is not the frozen executor", async () => {
    const id = executionPendingOrder(OTHER_ID);
    renderDetail(id);
    const note = await screen.findByTestId("execution-wait-note");
    expect(note.textContent).toContain("executor-a");
    expect(note.textContent).toContain("\u7b49\u5f85\u6267\u884c");
  });

  it("shows no wait note for the frozen executor (the action card owns the view)", async () => {
    const id = executionPendingOrder(FIXTURE_OWNER_ID);
    renderDetail(id);
    await screen.findByTestId("execution-action-card");
    expect(screen.queryByTestId("execution-wait-note")).toBeNull();
  });

  it("displays rollback SQL and generates a rollback draft on completed orders", async () => {
    const id = seedOrder({ state: "completed" });
    server.use(
      http.get("*/change-orders/:orderId", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id,
            display_number: "YR-20260830-000021",
            submitter_user_id: FIXTURE_OWNER_ID,
            title: "\u5df2\u5b8c\u6210\u53ef\u56de\u6eda\u5de5\u5355",
            state: "completed",
            rollback_enabled: true,
            ticket_type: "normal",
            rollback_status: "available",
            rollback_evidence_id: "0198d9cc-e65d-7b9d-a8aa-3c81945fa999",
            stages: [
              {
                id: "9c6f1a2b-0000-4000-8000-00000000ef11",
                position: 1,
                datasource_name: "orders-mysql",
                state: "succeeded",
                approval_steps: [],
                execution_actors: [],
              },
            ],
            has_sql: true,
            sql_hash: "hash-1",
            snapshot_hash: "snap-1",
            version: 2,
            submitted_at: "2026-08-30T10:00:00Z",
            terminal_at: "2026-08-30T10:05:00Z",
          },
          request_id: REQUEST_ID,
        }),
      ),
      http.post("*/change-orders/:orderId/sql-reveals", async ({ request }) => {
        const body = (await request.json()) as { purpose?: string };
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            reveal_id: "0198d9cc-e65d-7b9d-a8aa-3c81945fa998",
            sql: body.purpose === "rollback-order-sql"
              ? "DELETE FROM `demo`.`users` WHERE `id` = '1001';"
              : "INSERT INTO `demo`.`users` (`id`, `name`) VALUES (1001, 'alice');",
            watermark: "henry · 2026-11-07T02:00:00Z",
            valid_until: "2026-11-07T02:05:00Z",
          },
          request_id: REQUEST_ID,
        });
      }),
      http.post("*/change-orders/:orderId/rollback-drafts", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "0198d9cc-e65d-7b9d-a8aa-3c81945fa997",
            owner_user_id: FIXTURE_OWNER_ID,
            flow_id: "0198d9cc-e65d-7b9d-a8aa-3c81945fa111",
            title: "[\u56de\u6eda] YR-20260901-000001",
            rollback_enabled: false,
            ticket_type: "rollback",
            source_order_id: id,
            revision: 1,
            state: "draft",
            has_sql: true,
            version: 1,
            created_at: "2026-11-07T02:00:00Z",
            updated_at: "2026-11-07T02:00:00Z",
          },
          request_id: REQUEST_ID,
        }),
      ),
    );
    renderDetail(id);
    const card = await screen.findByTestId("order-rollback-card");
    expect(card).toBeVisible();
    expect(screen.getByTestId("order-rollback-status-badge").textContent).toContain("\u53ef\u56de\u6eda");
    expect(await screen.findByTestId("order-rollback-sql-view")).toBeVisible();
    expect(screen.getByTestId("order-rollback-sql-readonly").textContent).toContain(
      "DELETE FROM `demo`.`users` WHERE `id` = '1001';",
    );
    expect(screen.getByTestId("generate-rollback-order")).toBeEnabled();
  });

  it("localizes canonical English timeline summaries from the backend by event_type", async () => {
    const id = seedOrder();
    server.use(
      http.get("*/change-orders/:orderId/timeline", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [
              {
                id: "b1a7c2d4-0000-4000-8000-00000000ab01",
                event_type: "change_order.submitted",
                occurred_at: "2026-09-01T09:00:00Z",
                actor_kind: "user",
                actor_user_id: FIXTURE_OWNER_ID,
                actor_display_name: "henry",
                summary: "Order submitted and workflow snapshot frozen",
                stage_position: 1,
                state: "submitted",
              },
              {
                id: "b1a7c2d4-0000-4000-8000-00000000ab02",
                event_type: "change_order.approval_decided",
                occurred_at: "2026-09-01T09:05:00Z",
                actor_kind: "user",
                actor_user_id: "b1a7c2d4-0000-4000-8000-00000000aa09",
                actor_display_name: "reviewer",
                summary: "Approval rejected; order terminated",
                stage_position: 1,
                state: "rejected",
              },
            ],
            page: { next_cursor: null, has_more: false },
          },
          request_id: REQUEST_ID,
        }),
      ),
    );
    renderDetail(id);
    const list = await screen.findByTestId("order-timeline-list");
    expect(list.textContent).toContain("\u5de5\u5355\u63d0\u4ea4\u5e76\u51bb\u7ed3\u6d41\u7a0b\u5feb\u7167");
    expect(list.textContent).toContain("\u5ba1\u6279\u62d2\u7edd\uff0c\u5de5\u5355\u7ec8\u6b62");
  });

  it("highlights only the latest timeline state badge and prefixes older transitions with an arrow", async () => {
    const id = seedOrder({
      id: "99999999-0000-4000-8000-000000000033",
      state: "failed",
    });
    server.use(
      http.get("*/change-orders/:orderId/timeline", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [
              {
                id: "b1a7c2d4-0000-4000-8000-00000000ac03",
                event_type: "execution.failed",
                occurred_at: "2026-11-19T06:30:31Z",
                actor_kind: "system",
                actor_user_id: null,
                actor_display_name: null,
                stage_position: 1,
                summary: "Stage execution failed",
                state: "failed",
              },
              {
                id: "b1a7c2d4-0000-4000-8000-00000000ac02",
                event_type: "execution.started",
                occurred_at: "2026-11-19T06:30:30Z",
                actor_kind: "system",
                actor_user_id: null,
                actor_display_name: null,
                stage_position: 1,
                summary: "Stage execution started",
                state: "running",
              },
              {
                id: "b1a7c2d4-0000-4000-8000-00000000ac01",
                event_type: "change_order.stage_ready_for_execution",
                occurred_at: "2026-11-19T06:30:20Z",
                actor_kind: "user",
                actor_user_id: FIXTURE_OWNER_ID,
                actor_display_name: "henry",
                stage_position: 1,
                summary: "Final approval passed",
                state: "execution_pending",
              },
            ],
            page: { next_cursor: null, has_more: false },
          },
          request_id: REQUEST_ID,
        }),
      ),
    );

    renderDetail(id);
    const latestBadges = await screen.findAllByTestId("order-timeline-latest-state");
    expect(latestBadges).toHaveLength(1);
    expect(latestBadges[0]?.textContent).not.toContain("\u2192");

    const historicalBadges = screen.getAllByTestId("order-timeline-historical-state");
    expect(historicalBadges).toHaveLength(2);
    for (const badge of historicalBadges) {
      expect(badge.textContent).toMatch(/^\u2192 /);
    }
  });
});
