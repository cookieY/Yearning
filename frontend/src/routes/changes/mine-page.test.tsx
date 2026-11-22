import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "@/features/auth/session-provider";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { HttpResponse, http } from "msw";
import { server } from "@/test/msw/server";
import {
  publishSseEvent,
  resetSseEventBusForTest,
} from "@/shared/events/sse-event-bus";
import "@/shared/i18n";
import {
  FIXTURE_FLOW_ID,
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
} from "@/shared/mock/review-fixture";
import { MOCK_IDENTITY_IDS } from "@/shared/mock/identity-store";
import MinePage from "./mine-page";
import OrderDetailPage from "./order-detail-page";

/**
 * My Orders list gates (work package FE-F6-ORDER-SUBMIT): drafts and submitted
 * orders render in separated sections, rows navigate to their detail routes,
 * and domain events never duplicate list rows — events trigger a full HTTP
 * re-read (api/events/README.md), so redelivery is invisible.
 */

afterEach(() => {
  resetReviewFixture();
});

function seedOrder(overrides: Record<string, unknown> = {}): void {
  seedFixtureOrder({
    id: "9b6f1a2b-0000-4000-8000-00000000cd01",
    display_number: "YR-20260830-000011",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "\u5217\u8868\u9875\u5939\u5177\u5de5\u5355",
    state: "submitted",
    current_stage_position: 1,
    stages: [],
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
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location-pathname">{location.pathname}</output>;
}

function renderMine(entry = "/changes/mine") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
      <MemoryRouter initialEntries={[entry]}>
        <LocationProbe />
        <Routes>
          <Route path="/changes/mine" element={<MinePage />} />
          <Route path="/changes/drafts/:draftId" element={<p data-testid="draft-target" />} />
          <Route path="/changes/orders/:orderId" element={<OrderDetailPage />} />
          <Route path="/approvals/orders/:orderId" element={<OrderDetailPage />} />
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

/** Pick a calendar day inside the date dialog, matched by its localized
 * aria-label so the query never depends on DOM order (review R2-L1 nit). */
async function pickCalendarDay(
  user: ReturnType<typeof userEvent.setup>,
  dialogName: string,
  day: number,
): Promise<void> {
  const now = new Date();
  const label = new RegExp(
    `(?:^|Today, |\u4eca\u5929\uff0c)${String(now.getFullYear())}\u5e74${String(now.getMonth() + 1)}\u6708${String(day)}\u65e5`,
  );
  const dialog = screen.getByRole("dialog", { name: dialogName });
  await user.click(within(dialog).getByRole("button", { name: label }));
}

describe("MinePage", () => {  it("renders draft and order sections separated, orders newest first", async () => {
    seedOrder();
    seedOrder({
      id: "9b6f1a2b-0000-4000-8000-00000000cd02",
      display_number: "YR-20260830-000012",
      submitted_at: "2026-08-30T11:00:00Z",
    });
    renderMine();
    expect(await screen.findByTestId("mine-orders-table")).toBeVisible();
    expect(screen.getByRole("heading", { level: 1, name: "\u6211\u7684\u5de5\u5355" })).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    const rows = screen.getAllByTestId("mine-order-row");
    expect(rows).toHaveLength(2);
    // Newest submission first.
    expect(rows[0]?.textContent).toContain("YR-20260830-000012");
    expect(rows[1]?.textContent).toContain("YR-20260830-000011");
    expect(screen.getByTestId("tab-audit-orders")).toBeVisible();
  });

  it("shows orders awaiting MY execution via ?scope=execution, hidden from the submitter view (F-UX-004)", async () => {
    // Submitted by another user, current user is the frozen executor, stage is pending execution — same semantics as dashboard pending_execution_count.
    const EXEC_PENDING_ID = "9b6f1a2b-0000-4000-8000-00000000ee01";
    seedOrder({
      id: EXEC_PENDING_ID,
      display_number: "YR-20261030-000001",
      submitter_user_id: MOCK_IDENTITY_IDS.executor,
      title: "\u5f85\u6211\u6267\u884c\u7684\u5de5\u5355",
      state: "stage_execution_pending",
      stages: [
        {
          id: `${EXEC_PENDING_ID}-s1`,
          position: 1,
          datasource_name: "prod-pg",
          state: "execution_pending",
          approval_steps: [
            {
              id: `${EXEC_PENDING_ID}-a1`,
              position: 1,
              state: "approved",
              decided_at: "2026-10-30T09:00:00Z",
              actors: [{ id: MOCK_IDENTITY_IDS.reviewer, username: "reviewer-a", display_name: "reviewer-a" }],
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
    });
    // Default scope (My Orders = submitter view): not submitted by current user -> invisible (empty state).
    const submitterView = renderMine();
    expect(await submitterView.findByTestId("orders-empty")).toBeVisible();
    expect(submitterView.queryByTestId("mine-orders-table")).toBeNull();
    submitterView.unmount();

    // Executor arrival path (workspace "Pending My Execution" card carries ?scope=execution): visible,
    // and clicking the row navigates to /approvals/orders/:id.
    const exec = renderMine("/changes/mine?scope=execution");
    expect(await exec.findByTestId("mine-orders-table")).toBeVisible();
    const rows = exec.getAllByTestId("mine-order-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("YR-20261030-000001");
    await userEvent.click(rows[0] as HTMLElement);
    expect(await exec.findByTestId("execution-action-card")).toBeVisible();
    expect(exec.getByTestId("location-pathname")).toHaveTextContent(`/approvals/orders/${EXEC_PENDING_ID}`);
    expect(exec.queryByTestId("withdraw-order")).toBeNull();
    expect(exec.queryByTestId("void-order")).toBeNull();
    exec.unmount();
  });

  it("keeps self-executor orders reachable in ?scope=execution (including result_unknown) and isolates My Orders vs execution actions", async () => {
    const SELF_PENDING_ID = "9b6f1a2b-0000-4000-8000-00000000ee02";
    const SELF_UNKNOWN_ID = "9b6f1a2b-0000-4000-8000-00000000ee03";
    const executorActor = [
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
    ];
    seedOrder({
      id: SELF_PENDING_ID,
      display_number: "YR-20261122-000101",
      submitter_user_id: FIXTURE_OWNER_ID,
      title: "self pending execution",
      state: "stage_execution_pending",
      submitted_at: "2026-11-22T09:00:00Z",
      stages: [
        {
          id: `${SELF_PENDING_ID}-s1`,
          position: 1,
          datasource_name: "prod-pg",
          state: "execution_pending",
          approval_steps: [],
          execution_actors: executorActor,
        },
      ],
    });
    seedOrder({
      id: SELF_UNKNOWN_ID,
      display_number: "YR-20261122-000102",
      submitter_user_id: FIXTURE_OWNER_ID,
      title: "self unknown verification",
      state: "result_unknown",
      submitted_at: "2026-11-22T08:00:00Z",
      stages: [
        {
          id: `${SELF_UNKNOWN_ID}-s1`,
          position: 1,
          datasource_name: "prod-pg",
          state: "result_unknown",
          approval_steps: [],
          execution_actors: executorActor,
        },
      ],
    });

    const exec = renderMine("/changes/mine?scope=execution");
    expect(await exec.findByTestId("mine-orders-table")).toBeVisible();
    const execRows = exec.getAllByTestId("mine-order-row");
    expect(execRows).toHaveLength(2);
    expect(exec.getByTestId("mine-orders-table").textContent).toContain("YR-20261122-000101");
    expect(exec.getByTestId("mine-orders-table").textContent).toContain("YR-20261122-000102");
    const execPendingRow = execRows.find((row) => row.textContent.includes("YR-20261122-000101"));
    expect(execPendingRow).toBeDefined();
    await userEvent.click(execPendingRow as HTMLElement);
    expect(await exec.findByTestId("execution-action-card")).toBeVisible();
    expect(exec.getByTestId("location-pathname")).toHaveTextContent(`/approvals/orders/${SELF_PENDING_ID}`);
    expect(exec.queryByTestId("withdraw-order")).toBeNull();
    expect(exec.queryByTestId("void-order")).toBeNull();
    exec.unmount();

    const mine = renderMine("/changes/mine");
    expect(await mine.findByTestId("mine-orders-table")).toBeVisible();
    const mineRows = mine.getAllByTestId("mine-order-row");
    const minePendingRow = mineRows.find((row) => row.textContent.includes("YR-20261122-000101"));
    expect(minePendingRow).toBeDefined();
    await userEvent.click(minePendingRow as HTMLElement);
    expect(await mine.findByTestId("withdraw-order")).toBeVisible();
    expect(mine.getByTestId("void-order")).toBeVisible();
    expect(await mine.findByTestId("execution-wait-note")).toBeVisible();
    expect(mine.getByTestId("location-pathname")).toHaveTextContent(`/changes/orders/${SELF_PENDING_ID}`);
    expect(mine.queryByTestId("execution-action-card")).toBeNull();
    mine.unmount();
  });

  it("deletes a saved draft through the drafts inbox popover and confirm dialog (owner ruling 2026-10-24)", async () => {
    const user = userEvent.setup();
    // Seed one draft through the fixture's own POST so version/state are real.
    const create = (await (
      await fetch("/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u5f85\u5220\u9664\u8349\u7a3f" }),
      })
    ).json()) as { data: { id: string; version: number } };

    renderMine();
    const trigger = await screen.findByTestId("mine-drafts-trigger");
    expect(screen.getByTestId("mine-drafts-count")).toHaveTextContent("1");
    await user.click(trigger);
    expect(await screen.findByTestId("mine-drafts-popover")).toBeVisible();
    const row = await screen.findByTestId(`delete-draft-${create.data.id}`);
    await user.click(row);
    expect(await screen.findByTestId("delete-draft-dialog")).toBeVisible();

    // Cancel keeps the draft.
    await user.click(screen.getByTestId("delete-draft-cancel"));
    await waitFor(() => {
      expect(screen.queryByTestId("delete-draft-dialog")).toBeNull();
    });
    expect(screen.getByTestId("mine-drafts-count")).toHaveTextContent("1");

    // Reopen popover and confirm delete: the row and count badge disappear.
    await user.click(screen.getByTestId("mine-drafts-trigger"));
    await user.click(await screen.findByTestId(`delete-draft-${create.data.id}`));
    await user.click(screen.getByTestId("delete-draft-accept"));
    await waitFor(() => {
      expect(screen.queryByTestId("mine-drafts-count")).toBeNull();
    });
    const drafts = (await (await fetch("/change-drafts")).json()) as {
      data: { items: Array<{ id: string }> };
    };
    expect(drafts.data.items.some((item) => item.id === create.data.id)).toBe(false);
  });

  it("renders the unified query-orders tab with its empty state", async () => {
    const user = userEvent.setup();
    renderMine();
    // FE-F10 replaces the placeholder with the unified query view (UI spec
    // §5.2): no server query objects exist for the default session, so the
    // honest empty state renders.
    const queryTab = await screen.findByTestId("tab-query-orders");
    // Page-level navigation tabs sit below PageHeader along the left reading axis, never inside right-aligned page-actions.
    expect(queryTab.closest('[data-slot="page-actions"]')).toBeNull();
    expect(queryTab.closest('[data-slot="page-header"]')).toBeNull();
    await user.click(queryTab);
    await waitFor(() => {
      expect(screen.getByTestId("orders-query-empty")).toBeVisible();
    });
    expect(screen.getByTestId("orders-query-empty")).toHaveTextContent("\u6682\u65e0\u67e5\u8be2\u5de5\u5355\u8bb0\u5f55\u3002");
  });

  it("navigates a row click to the order detail route", async () => {
    const user = userEvent.setup();
    seedOrder();
    renderMine();
    const row = await screen.findByTestId("mine-order-row");
    await user.click(row);
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page")).toBeVisible();
    });
  });

  it("keeps exactly one row per order across event redelivery (list events produce no duplicates)", async () => {
    seedOrder();
    renderMine();
    expect(await screen.findByTestId("mine-orders-table")).toBeVisible();
    expect(screen.getAllByTestId("mine-order-row")).toHaveLength(1);

    // The server applies the withdrawal; the event is only a notification.
    // The consumer re-reads the resource, so the list reflects the new state
    // in place instead of duplicating rows.
    server.use(
      http.get("*/change-orders", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [
              {
                id: "9b6f1a2b-0000-4000-8000-00000000cd01",
                display_number: "YR-20260830-000011",
                submitter_user_id: FIXTURE_OWNER_ID,
                requested_scheduled_for: null,
                title: "\u5217\u8868\u9875\u5939\u5177\u5de5\u5355",
                state: "withdrawn",
                current_stage_position: null,
                stages: [],
                has_sql: true,
                sql_hash: "hash-1",
                snapshot_hash: "snap-1",
                manually_verified: false,
                version: 2,
                submitted_at: "2026-08-30T10:00:00Z",
                terminal_at: "2026-08-30T10:05:00Z",
              },
            ],
            page: { next_cursor: null, has_more: false },
          },
          request_id: "33333333-3333-4333-8333-333333333333",
        }),
      ),
    );
    publishSseEvent({
      id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9f01",
      type: "io.yearning.v4.change_order.state_changed",
      subject: "change-orders/9b6f1a2b-0000-4000-8000-00000000cd01",
      data: {
        aggregate_id: "9b6f1a2b-0000-4000-8000-00000000cd01",
        from: "submitted",
        to: "withdrawn",
        reason_code: "submitter_withdrawn",
        aggregate_version: 2,
      },
    });
    await waitFor(() => {
      expect(screen.getAllByTestId("mine-order-row")).toHaveLength(1);
    });
    await waitFor(() => {
      expect(screen.getByTestId("mine-orders-table").textContent).toContain("\u5df2\u64a4\u56de");
    });
    // The exact same event redelivered (at-least-once) must be a no-op:
    // the SSE bus dedups by event.id and reports it undelivered.
    const duplicateEvent = {
      id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9f01",
      type: "io.yearning.v4.change_order.state_changed",
      subject: "change-orders/9b6f1a2b-0000-4000-8000-00000000cd01",
      data: {
        aggregate_id: "9b6f1a2b-0000-4000-8000-00000000cd01",
        from: "submitted",
        to: "withdrawn",
        reason_code: "submitter_withdrawn",
        aggregate_version: 2,
      },
    };
    expect(publishSseEvent(duplicateEvent)).toBe(false);
    resetSseEventBusForTest();
    await new Promise((resolve) => setTimeout(resolve, 100));
    expect(screen.getAllByTestId("mine-order-row")).toHaveLength(1);
  });

  it("filters server-side via the keyword and distinguishes the empty state", async () => {
    const user = userEvent.setup();
    seedOrder();
    renderMine();
    expect(await screen.findByTestId("mine-orders-table")).toBeVisible();

    // A keyword that matches nothing yields the filtered-empty copy after
    // the debounce window; the row disappears (server-side filter).
    await user.type(screen.getByTestId("filter-keyword"), "\u4e0d\u5b58\u5728\u7684\u5173\u952e\u5b57");
    await waitFor(() => {
      expect(screen.getByTestId("orders-empty").textContent).toContain("\u6ca1\u6709\u7b26\u5408\u7b5b\u9009\u6761\u4ef6\u7684\u5de5\u5355");
    }, { timeout: 3000 });
    expect(screen.queryByTestId("mine-orders-table")).toBeNull();

    // Clearing the keyword restores the row.
    await user.clear(screen.getByTestId("filter-keyword"));
    await waitFor(() => {
      expect(screen.getByTestId("mine-orders-table")).toBeVisible();
    }, { timeout: 3000 });
  });

  it("commits state, datasource, and date chips into server params and clears every filter (owner 2026-11-05)", async () => {
    const user = userEvent.setup();
    seedOrder({
      stages: [
        {
          position: 1,
          name: "\u5ba1\u6279",
          datasource_id: "ds-1",
          datasource_name: "prod-mysql",
          database_name: "app",
          state: "approval_pending",
          approval_mode: "any",
          approval_actors: [],
          execution_actors: [],
        },
      ],
    });
    const seenParams: URLSearchParams[] = [];
    const recordRequest = ({ request }: { request: Request }): void => {
      const url = new URL(request.url);
      if (url.pathname.endsWith("/change-orders") && url.searchParams.get("limit") === "20") {
        seenParams.push(url.searchParams);
      }
    };
    server.events.on("request:start", recordRequest);
    try {
      renderMine();
      expect(await screen.findByTestId("mine-orders-table")).toBeVisible();
      const input = screen.getByTestId("filter-keyword");
      expect(screen.queryByTestId("filter-keyword-clear")).toBeNull();

      // 1. State chip -> state=submitted (and Submitted-to is no longer offered)
      await user.click(input);
      expect(screen.queryByRole("option", { name: "\u63d0\u4ea4\u622a\u6b62\u65e5\u671f" })).toBeNull();
      await user.click(screen.getByRole("option", { name: "\u72b6\u6001" }));
      await user.click(screen.getByRole("option", { name: "\u5df2\u63d0\u4ea4" }));

      // 2. Datasource chip -> datasource=prod-mysql
      await user.click(input);
      await user.click(screen.getByRole("option", { name: "\u6570\u636e\u6e90" }));
      await user.click(await screen.findByRole("option", { name: "prod-mysql" }));

      // 3. Submitted-from date chip -> submitted_from=YYYY-MM-10
      await user.click(input);
      await user.click(screen.getByRole("option", { name: "\u63d0\u4ea4\u8d77\u59cb\u65e5\u671f" }));
      await pickCalendarDay(user, "\u63d0\u4ea4\u8d77\u59cb\u65e5\u671f — \u9009\u62e9\u65e5\u671f", 10);

      // 4. Live keyword -> q=YR (after debounce)
      await user.type(input, "YR");

      const now = new Date();
      const ym = `${String(now.getFullYear())}-${String(now.getMonth() + 1).padStart(2, "0")}`;
      await waitFor(() => {
        const last = seenParams[seenParams.length - 1];
        expect(last?.get("q")).toBe("YR");
        expect(last?.get("state")).toBe("submitted");
        expect(last?.get("datasource")).toBe("prod-mysql");
        expect(last?.get("submitted_from")).toBe(`${ym}-10`);
        expect(last?.get("submitted_to")).toBeNull();
      }, { timeout: 3000 });

      // Clear-all resets all active server filter params in one gesture.
      const countBeforeClear = seenParams.length;
      await user.click(screen.getByTestId("filter-keyword-clear"));
      await waitFor(() => {
        expect(screen.queryByTestId("filter-keyword-clear")).toBeNull();
        expect(seenParams.length).toBeGreaterThan(countBeforeClear);
        const last = seenParams[seenParams.length - 1];
        expect(last?.get("q")).toBeNull();
        expect(last?.get("state")).toBeNull();
        expect(last?.get("datasource")).toBeNull();
        expect(last?.get("submitted_from")).toBeNull();
      }, { timeout: 3000 });
      expect(input).toHaveValue("");
    } finally {
      server.events.removeListener("request:start", recordRequest);
    }
  });

  it("renders human-readable datasource_names for query access requests and falls back to raw IDs", async () => {
    const user = userEvent.setup();
    const rawIdNamed = "3c38b5a6-2a34-475a-9fd9-9cb6fb182fdc";
    const rawIdFallback = "4d49c6b7-3b45-486b-0ae0-0dc7fc293ged";
    server.use(
      http.get("*/query/orders", () =>
        HttpResponse.json({
          err_code: 0,
          err_message: "ok",
          data: {
            items: [
              {
                kind: "access_request",
                id: "qreq-named",
                created_at: "2026-11-22T17:38:46Z",
                request: {
                  id: "qreq-named",
                  requester_user_id: FIXTURE_OWNER_ID,
                  state: "grant_active",
                  datasource_ids: [rawIdNamed],
                  datasource_names: ["f12-pg-target"],
                  grant_id: null,
                  version: 1,
                  created_at: "2026-11-22T17:38:46Z",
                  reason: "qa",
                  requested_until: null,
                  terminal_at: null,
                },
              },
              {
                kind: "access_request",
                id: "qreq-fallback",
                created_at: "2026-11-22T17:37:32Z",
                request: {
                  id: "qreq-fallback",
                  requester_user_id: FIXTURE_OWNER_ID,
                  state: "rejected",
                  datasource_ids: [rawIdFallback],
                  grant_id: null,
                  version: 1,
                  created_at: "2026-11-22T17:37:32Z",
                  reason: "qa",
                  requested_until: null,
                  terminal_at: "2026-11-22T17:37:50Z",
                },
              },
            ],
            page: { limit: 20, next_cursor: null, has_more: false },
          },
        }),
      ),
    );

    renderMine();
    await user.click(await screen.findByRole("tab", { name: "\u67e5\u8be2\u5de5\u5355" }));

    const namedRow = await screen.findByTestId("orders-query-row-qreq-named");
    expect(within(namedRow).getByText("f12-pg-target")).toBeVisible();
    expect(within(namedRow).queryByText(rawIdNamed)).toBeNull();

    const fallbackRow = screen.getByTestId("orders-query-row-qreq-fallback");
    expect(within(fallbackRow).getByText(rawIdFallback)).toBeVisible();

    await user.click(namedRow);
    const sheet = await screen.findByTestId("orders-query-detail");
    expect(within(sheet).getByText("f12-pg-target")).toBeVisible();
    expect(within(sheet).queryByText(rawIdNamed)).toBeNull();
  });
});
