import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "@/features/auth/session-provider";
import { HttpResponse, http } from "msw";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { grantSession } from "@/test/session";
import "@/shared/i18n";
import {
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
  seedFixtureSchedule,
  type FixtureOrder,
} from "@/shared/mock/review-fixture";
import OrderDetailPage from "@/routes/changes/order-detail-page";

/**
 * Execution workspace gates (work package FE-F8): the executor area renders
 * only for the frozen executors of the execution_pending stage and always
 * shows the frozen SQL hash as identical to the approved snapshot (W006;
 * gate: approved and executed SQL Hashes are displayed consistently), there is no rollback entry anywhere
 * (E003), unknown statement results are never rendered as not-executed
 * (E005), and a sent-boundary fate only offers copy-to-new-draft (E004).
 */

vi.stubEnv("VITE_ENABLE_MOCK", "true");

beforeEach(() => {
  server.resetHandlers();
  resetReviewFixture();
  grantSession();
  window.localStorage.setItem("yearning-mock-scenario", "ready");
  window.localStorage.setItem("yearning-mock-auth", "admin");
});

afterEach(() => {
  resetReviewFixture();
  window.localStorage.removeItem("yearning-mock-scenario");
  window.localStorage.removeItem("yearning-mock-auth");
});

function executionOrder(overrides: Partial<FixtureOrder> = {}): FixtureOrder {
  return {
    id: "ee6f1a2b-0000-4000-8000-00000000f901",
    display_number: "YR-20260830-000301",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "\u6267\u884c\u5939\u5177\u5de5\u5355",
    state: "stage_execution_pending",
    current_stage_position: 1,
    stages: [
      {
        id: "ee6f1a2b-0000-4000-8000-00000000f911",
        position: 1,
        datasource_name: "orders-mysql",
        state: "execution_pending",
        approval_steps: [
          {
            id: "ee6f1a2b-0000-4000-8000-00000000f921",
            position: 1,
            state: "approved",
            decided_at: "2026-08-30T10:00:00Z",
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
    sql_hash: "hash-exec-901",
    snapshot_hash: "snap-exec-901",
    manually_verified: false,
    version: 2,
    submitted_at: "2026-08-30T11:00:00Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "UPDATE orders SET status = 1 WHERE user_id = 42; UPDATE orders SET status = 2 WHERE user_id = 43;",
    ...overrides,
  };
}

function renderDetail(orderId: string, entry: "approvals" | "changes" = "approvals") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <MemoryRouter initialEntries={[`/${entry}/orders/${orderId}`]}>
          <Routes>
            <Route path="/changes/orders/:orderId" element={<OrderDetailPage />} />
            <Route path="/approvals/orders/:orderId" element={<OrderDetailPage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

describe("execution workspace rendering", () => {
  it("shows the frozen-executor area with the hash-consistency marker and no rollback entry", async () => {
    const order = executionOrder();
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-action-card")).toBeVisible();
    });
    expect(screen.getByTestId("execution-hash-line")).toHaveTextContent("hash-exec-901");
    expect(screen.getByTestId("execution-hash-consistent")).toHaveTextContent("\u4e0e\u6279\u51c6\u5feb\u7167\u4e00\u81f4");
    // E003: no rollback affordance (action entry) anywhere on the surface —
    // the word itself legitimately appears in the semantics note.
    expect(screen.queryByRole("button", { name: /\u56de\u6eda/ })).toBeNull();
  });

  it("hides the executor area when the current user is not a frozen executor", async () => {
    const base = executionOrder();
    const baseStage = base.stages[0];
    if (baseStage === undefined) throw new Error("seed stage missing");
    const order = executionOrder({
      stages: [
        {
          ...baseStage,
          execution_actors: [
            {
              id: "1111d9cc-e65d-7b9d-a8aa-3c81945f99ac",
              username: "other",
              display_name: "other",
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
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page")).toBeVisible();
    });
    await waitFor(() => {
      expect(screen.queryByTestId("execution-action-card")).toBeNull();
    });
  });

  // owner ruling 2026-10-31 + 2026-11-22 (self-review/self-execute button split): when the same user is
  // both submitter and frozen executor, /changes/orders/:id shows only Withdraw/Void + wait note while
  // /approvals/orders/:id shows only ExecutionActionCard (never simultaneously showing execute and withdraw/void).
  it("scopes self-executor actions by entry route and keeps single-role executor routes intact", async () => {
    const selfOrder = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f981" });
    seedFixtureOrder(selfOrder);

    const mineView = renderDetail(selfOrder.id, "changes");
    expect(await mineView.findByTestId("withdraw-order")).toBeVisible();
    expect(mineView.getByTestId("void-order")).toBeVisible();
    expect(mineView.getByTestId("execution-wait-note")).toBeVisible();
    expect(mineView.queryByTestId("execution-action-card")).toBeNull();
    mineView.unmount();

    const execView = renderDetail(selfOrder.id, "approvals");
    expect(await execView.findByTestId("execution-action-card")).toBeVisible();
    expect(execView.getByTestId("execution-start")).toBeVisible();
    expect(execView.queryByTestId("withdraw-order")).toBeNull();
    expect(execView.queryByTestId("void-order")).toBeNull();
    execView.unmount();

    const pureExecutorOrder = executionOrder({
      id: "ee6f1a2b-0000-4000-8000-00000000f982",
      submitter_user_id: "1111d9cc-e65d-7b9d-a8aa-3c81945f99ac",
    });
    seedFixtureOrder(pureExecutorOrder);
    const pureView = renderDetail(pureExecutorOrder.id, "changes");
    expect(await pureView.findByTestId("execution-action-card")).toBeVisible();
    expect(pureView.queryByTestId("withdraw-order")).toBeNull();
    expect(pureView.queryByTestId("void-order")).toBeNull();
    pureView.unmount();
  });

  it("enforces self-executor entry split in running and result_unknown while preserving pure executors", async () => {
    const setupAttemptScenario = (
      orderId: string,
      attemptId: string,
      stageId: string,
      orderState: "running" | "result_unknown",
      submitterUserId: string,
    ): void => {
      const order = executionOrder({
        id: orderId,
        submitter_user_id: submitterUserId,
        state: orderState,
        stages: [
          {
            id: stageId,
            position: 1,
            datasource_name: "orders-mysql",
            state: orderState,
            approval_steps: [],
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
      seedFixtureOrder(order);
      server.use(
        http.get(apiRoute("/change-orders/:orderId"), () =>
          HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              id: order.id,
              display_number: order.display_number,
              submitter_user_id: order.submitter_user_id,
              submitter_username: "henry",
              submitter_display_name: "henry",
              title: order.title,
              state: order.state,
              current_stage_position: order.current_stage_position,
              requested_scheduled_for: null,
              stages: order.stages,
              has_sql: order.has_sql,
              sql_hash: order.sql_hash,
              snapshot_hash: order.snapshot_hash,
              manually_verified: false,
              has_ddl: false,
              osc_choice_available: false,
              version: order.version,
              submitted_at: order.submitted_at,
              terminal_at: null,
              latest_attempt_id: attemptId,
            },
            request_id: FIXTURE_OWNER_ID,
          }),
        ),
        http.get(apiRoute("/execution-attempts/:attemptId"), () =>
          HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              id: attemptId,
              stage_id: stageId,
              execution_kind: "dml",
              state: orderState,
              send_boundary: "sent",
              osc: null,
              version: 1,
              created_at: "2026-11-22T08:00:00Z",
            },
            request_id: FIXTURE_OWNER_ID,
          }),
        ),
        http.get(apiRoute("/execution-attempts/:attemptId/statements"), () =>
          HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              items: [
                {
                  id: `${attemptId}-stmt-1`,
                  ordinal: 1,
                  statement_kind: "dml",
                  state: orderState === "running" ? "sent" : "unknown",
                  affected_row_count: null,
                  failure_name: null,
                  started_at: "2026-11-22T08:00:01Z",
                  finished_at: null,
                },
              ],
              page: { next_cursor: null, has_more: false },
            },
            request_id: FIXTURE_OWNER_ID,
          }),
        ),
      );
    };

    // Running state: self-executor sees withdraw without execution-cancel on /changes/orders/:id,
    // and sees execution-cancel without withdraw/void on /approvals/orders/:id.
    const selfRunningId = "ee6f1a2b-0000-4000-8000-00000000f983";
    setupAttemptScenario(
      selfRunningId,
      "ee6f1a2b-0000-4000-8000-00000000a983",
      "ee6f1a2b-0000-4000-8000-000000000983",
      "running",
      FIXTURE_OWNER_ID,
    );
    const selfRunningChanges = renderDetail(selfRunningId, "changes");
    expect(await selfRunningChanges.findByTestId("withdraw-order")).toBeVisible();
    expect(await selfRunningChanges.findByTestId("execution-statement-1")).toBeVisible();
    expect(selfRunningChanges.queryByTestId("execution-cancel")).toBeNull();
    selfRunningChanges.unmount();

    const selfRunningApprovals = renderDetail(selfRunningId, "approvals");
    expect(await selfRunningApprovals.findByTestId("execution-cancel")).toBeVisible();
    expect(selfRunningApprovals.queryByTestId("withdraw-order")).toBeNull();
    expect(selfRunningApprovals.queryByTestId("void-order")).toBeNull();
    selfRunningApprovals.unmount();

    const pureRunningId = "ee6f1a2b-0000-4000-8000-00000000f984";
    setupAttemptScenario(
      pureRunningId,
      "ee6f1a2b-0000-4000-8000-00000000a984",
      "ee6f1a2b-0000-4000-8000-000000000984",
      "running",
      "1111d9cc-e65d-7b9d-a8aa-3c81945f99ac",
    );
    const pureRunningChanges = renderDetail(pureRunningId, "changes");
    expect(await pureRunningChanges.findByTestId("execution-cancel")).toBeVisible();
    expect(pureRunningChanges.queryByTestId("withdraw-order")).toBeNull();
    pureRunningChanges.unmount();

    // result_unknown state: self-executor sees withdraw + void + verification-waiting on /changes/orders/:id,
    // and sees verification-form + verification-submit without withdraw/void on /approvals/orders/:id.
    const selfUnknownId = "ee6f1a2b-0000-4000-8000-00000000f985";
    setupAttemptScenario(
      selfUnknownId,
      "ee6f1a2b-0000-4000-8000-00000000a985",
      "ee6f1a2b-0000-4000-8000-000000000985",
      "result_unknown",
      FIXTURE_OWNER_ID,
    );
    const selfUnknownChanges = renderDetail(selfUnknownId, "changes");
    expect(await selfUnknownChanges.findByTestId("withdraw-order")).toBeVisible();
    expect(selfUnknownChanges.getByTestId("void-order")).toBeVisible();
    expect(await selfUnknownChanges.findByTestId("verification-waiting")).toBeVisible();
    expect(selfUnknownChanges.queryByTestId("verification-form")).toBeNull();
    expect(selfUnknownChanges.queryByTestId("verification-submit")).toBeNull();
    selfUnknownChanges.unmount();

    const selfUnknownApprovals = renderDetail(selfUnknownId, "approvals");
    expect(await selfUnknownApprovals.findByTestId("verification-form")).toBeVisible();
    expect(selfUnknownApprovals.getByTestId("verification-submit")).toBeInTheDocument();
    expect(selfUnknownApprovals.queryByTestId("verification-waiting")).toBeNull();
    expect(selfUnknownApprovals.queryByTestId("withdraw-order")).toBeNull();
    expect(selfUnknownApprovals.queryByTestId("void-order")).toBeNull();
    selfUnknownApprovals.unmount();

    const pureUnknownId = "ee6f1a2b-0000-4000-8000-00000000f986";
    setupAttemptScenario(
      pureUnknownId,
      "ee6f1a2b-0000-4000-8000-00000000a986",
      "ee6f1a2b-0000-4000-8000-000000000986",
      "result_unknown",
      "1111d9cc-e65d-7b9d-a8aa-3c81945f99ac",
    );
    const pureUnknownChanges = renderDetail(pureUnknownId, "changes");
    expect(await pureUnknownChanges.findByTestId("verification-form")).toBeVisible();
    expect(pureUnknownChanges.queryByTestId("withdraw-order")).toBeNull();
    expect(pureUnknownChanges.queryByTestId("void-order")).toBeNull();
    pureUnknownChanges.unmount();
  });
});

describe("execution run interactions", () => {
  it("runs the happy path: execute → preflight hash confirmation → succeeded statements", async () => {
    const user = userEvent.setup();
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f902" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.queryByTestId("execution-ddl-warning")).toBeNull();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));

    // The attempt card appears and settles at succeeded with committed rows.
    await waitFor(
      () => {
        expect(screen.getByTestId("execution-attempt-card")).toHaveTextContent("\u6210\u529f");
      },
      { timeout: 5_000 },
    );
    await waitFor(() => {
      expect(screen.getByTestId("execution-statement-1")).toHaveTextContent("\u6210\u529f");
      expect(screen.getByTestId("execution-statement-2")).toHaveTextContent("\u6210\u529f");
    });
    // Preflight confirmed the executed SQL equals the approved SQL.
    expect(screen.getByTestId("execution-attempt-card")).toHaveTextContent("\u6267\u884cSQL\u4e0e\u6279\u51c6SQL\u4e00\u81f4");
    // The order completed — the executor area is gone.
    await waitFor(() => {
      expect(screen.queryByTestId("execution-action-card")).toBeNull();
    });
  }, 15_000);

  it("keeps unknown statements visually distinct from not-executed and verifies manually", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-unknown");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f903" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.queryByTestId("execution-ddl-warning")).toBeNull();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));

    // Statement #2 is unknown — its badge reads result-unknown, never not-executed.
    await waitFor(
      () => {
        expect(screen.getByTestId("execution-statement-2")).toHaveTextContent("\u7ed3\u679c\u672a\u77e5");
      },
      { timeout: 5_000 },
    );
    expect(screen.getByTestId("execution-statement-2")).not.toHaveTextContent("\u672a\u6267\u884c");

    // Verification form: the fixed verdict plus evidence are mandatory. The
    // form mounts once the terminal event lands and the order refetches.
    await waitFor(
      () => {
        expect(screen.getByTestId("verification-form")).toBeVisible();
      },
      { timeout: 5_000 },
    );
    expect(screen.getByTestId("verification-submit")).toBeDisabled();
    await user.click(screen.getByTestId("verification-result-confirmed_succeeded"));
    expect(screen.getByTestId("verification-submit")).toBeDisabled();
    await user.type(screen.getByTestId("verification-reason"), "\u4ece\u5e93\u590d\u5236\u65e0\u5ef6\u8fdf\uff0c\u884c\u6570\u4e00\u81f4");
    expect(screen.getByTestId("verification-submit")).toBeDisabled();
    await user.type(screen.getByTestId("verification-evidence-content-0"), "SELECT COUNT(*) \u590d\u6838\u901a\u8fc7");
    expect(screen.getByTestId("verification-submit")).toBeEnabled();
    await user.click(screen.getByTestId("verification-submit"));

    // The order completes with the manual-verification marker shown in the
    // frozen facts card.
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page")).toHaveTextContent("\u5df2\u5b8c\u6210");
    });
    await waitFor(() => {
      expect(screen.getByTestId("order-facts")).toHaveTextContent("\u662f");
    });
  }, 20_000);

  it("offers only copy-to-new-draft after a partial DDL fate", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-partial");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f904" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.getByTestId("execution-ddl-warning")).toBeVisible();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));

    // partial_failed facts: succeeded / failed / skipped rows.
    await waitFor(
      () => {
        expect(screen.getByTestId("execution-statement-1")).toHaveTextContent("\u6210\u529f");
      },
      { timeout: 5_000 },
    );
    expect(screen.getByTestId("execution-statement-2")).toHaveTextContent("\u5931\u8d25");
    expect(screen.getByTestId("execution-statement-3")).toHaveTextContent("\u5df2\u8df3\u8fc7");

    // No re-execute; the only forward path is the copied draft (E004). The
    // copy card mounts when the terminal event lands and the order refetches.
    await waitFor(() => {
      expect(screen.queryByTestId("execution-start")).toBeNull();
    });
    await waitFor(
      () => {
        expect(screen.getByTestId("copy-draft-card")).toBeVisible();
      },
      { timeout: 5_000 },
    );
  }, 15_000);
});

describe("execution cancellation and recovery paths", () => {
  it("cancels a gh-ost run and surfaces residuals with the copy-only path", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-ghost");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f905" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.getByTestId("execution-ddl-warning")).toBeVisible();
    expect(screen.getByTestId("execution-osc-overrides-box")).toBeVisible();
    await user.click(screen.getByTestId("execution-confirm-run"));

    // The gh-ost surface shows phase, progress and rows copied while copying.
    await waitFor(
      () => {
        expect(screen.getByTestId("execution-osc")).toBeVisible();
      },
      { timeout: 5_000 },
    );
    expect(screen.getByTestId("execution-osc-progress")).toBeVisible();

    // Any frozen executor may request cancellation; the outcome is a
    // request, not a promise — the attempt first moves to cancelling.
    await waitFor(() => {
      expect(screen.getByTestId("execution-cancel")).toBeEnabled();
    }, { timeout: 5_000 });
    await user.click(screen.getByTestId("execution-cancel"));
    expect(screen.getByTestId("execution-cancel-dialog")).toBeVisible();
    expect(screen.getByTestId("execution-cancel-confirm")).toBeDisabled();
    await user.type(screen.getByTestId("execution-cancel-reason"), "\u4e1a\u52a1\u7a97\u53e3\u5173\u95ed");
    await user.click(screen.getByTestId("execution-cancel-confirm"));

    // gh-ost cancellation surfaces leftover resources for cleanup and the
    // order terminalizes into the copy-only path (E006).
    await waitFor(
      () => {
        expect(screen.getByTestId("execution-osc")).toHaveTextContent("\u9700\u8981\u6e05\u7406");
      },
      { timeout: 5_000 },
    );
    await waitFor(
      () => {
        expect(screen.getByTestId("copy-draft-card")).toBeVisible();
      },
      { timeout: 5_000 },
    );
  }, 20_000);

  it("surfaces a preflight failure inside the dialog and allows re-execution", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-preflight");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f906" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.queryByTestId("execution-ddl-warning")).toBeNull();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));

    // The 3006 business error renders in place — no attempt is persisted, no
    // fake progress, and the executor may simply click again (not_started).
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-error")).toBeVisible();
    }, { timeout: 5_000 });
    expect(screen.getByTestId("execution-action-error")).toHaveTextContent("\u6267\u884c\u524d\u6821\u9a8c\u672a\u901a\u8fc7");
    expect(screen.queryByTestId("execution-attempt-card")).toBeNull();
    expect(screen.getByTestId("execution-confirm-cancel")).toBeEnabled();
  }, 15_000);

  it("copies a terminal order to a new draft through the dialog", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-partial");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f907" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.getByTestId("execution-ddl-warning")).toBeVisible();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(
      () => {
        expect(screen.getByTestId("copy-draft-card")).toBeVisible();
      },
      { timeout: 8_000 },
    );

    await user.click(screen.getByTestId("copy-draft-open"));
    expect(screen.getByTestId("copy-draft-dialog")).toBeVisible();
    // The flow catalog loads and the title is prefilled from the order.
    expect(screen.getByTestId("copy-draft-flow")).toBeVisible();
    const titleValue = screen.getByTestId<HTMLInputElement>("copy-draft-title").value;
    expect(titleValue).toContain("\u6267\u884c\u5939\u5177\u5de5\u5355");
    // The confirm enables once the flow catalog resolves and a flow is picked.
    await waitFor(() => {
      expect(screen.getByTestId("copy-draft-confirm")).toBeEnabled();
    });
  }, 20_000);
});

describe("execution error and evidence branches", () => {
  it("renders a rejected cancellation inside the dialog and converges on refetch", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-ghost");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f908" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.getByTestId("execution-ddl-warning")).toBeVisible();
    expect(screen.getByTestId("execution-osc-overrides-box")).toBeVisible();
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(() => {
      expect(screen.getByTestId("execution-cancel")).toBeEnabled();
    }, { timeout: 5_000 });

    // Force a lost race on the cancellation endpoint: the dialog shows the
    // backend rejection and the page refetches — no fake success.
    server.use(
      http.post(apiRoute("/execution-attempts/:attemptId/cancellation"), () =>
        HttpResponse.json(
          {
            err_code: 1004,
            message: "attempt changed elsewhere",
            data: null,
            request_id: "33333333-3333-4333-8333-333333333333",
            retryable: false,
          },
        ),
      ),
    );
    await user.click(screen.getByTestId("execution-cancel"));
    await user.type(screen.getByTestId("execution-cancel-reason"), "\u4e1a\u52a1\u7a97\u53e3\u5173\u95ed");
    await user.click(screen.getByTestId("execution-cancel-confirm"));
    expect(screen.getByTestId("execution-cancel-error")).toBeVisible();
    expect(screen.getByTestId("execution-cancel-error")).toHaveTextContent(/\u8d44\u6e90\u6b63\u5728\u88ab\u5176\u4ed6\u4eba\u4fee\u6539/);
    await user.click(screen.getByTestId("execution-cancel-cancel"));
    // Escape goes through the Dialog onOpenChange close path, complementing the button onClick.
    await user.click(screen.getByTestId("execution-cancel"));
    await user.type(screen.getByTestId("execution-cancel-reason"), "\u518d\u6b21\u53d6\u6d88");
    await user.keyboard("{Escape}");
    await waitFor(() => {
      expect(screen.queryByTestId("execution-cancel-dialog")).toBeNull();
    });
  }, 20_000);

  it("shows the statement ledger read failure in place", async () => {
    const user = userEvent.setup();
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f909" });
    seedFixtureOrder(order);
    server.use(
      http.get(apiRoute("/execution-attempts/:attemptId/statements"), () =>
        HttpResponse.json(
          {
            type: "about:blank",
            title: "internal error",
            status: 500,
            request_id: "33333333-3333-4333-8333-333333333333",
          },
          { status: 500 },
        ),
      ),
    );
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.queryByTestId("execution-ddl-warning")).toBeNull();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(() => {
      expect(screen.getByText("\u9010\u8bed\u53e5\u7ed3\u679c\u8bfb\u53d6\u5931\u8d25\u3002")).toBeVisible();
    }, { timeout: 5_000 });
  }, 15_000);

  it("keeps the order blocked on still_unknown and supports evidence editing", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-unknown");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f910" });
    seedFixtureOrder(order);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.queryByTestId("execution-ddl-warning")).toBeNull();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(
      () => {
        expect(screen.getByTestId("verification-form")).toBeVisible();
      },
      { timeout: 5_000 },
    );

    // Evidence rows can be added and removed, but never down to zero.
    await user.click(screen.getByTestId("verification-evidence-add"));
    expect(screen.getByTestId("verification-evidence-content-1")).toBeVisible();
    const secondRow = screen.getByTestId("verification-evidence-content-1").parentElement;
    const removeButton = secondRow?.querySelector('button[aria-label="\u79fb\u9664\u8bc1\u636e"]');
    if (!(removeButton instanceof HTMLButtonElement)) throw new Error("remove button missing");
    await user.click(removeButton);
    expect(screen.queryByTestId("verification-evidence-content-1")).toBeNull();

    // still_unknown leaves the block in place — the order stays result_unknown.
    await user.click(screen.getByTestId("verification-result-still_unknown"));
    await user.type(screen.getByTestId("verification-reason"), "\u4e3b\u4ece\u6570\u636e\u4e0d\u4e00\u81f4\uff0c\u9700DBA\u73b0\u573a\u590d\u6838");
    await user.type(screen.getByTestId("verification-evidence-content-0"), "SHOW SLAVE STATUS \u5ef6\u8fdf\u672a\u77e5");
    expect(screen.getByTestId("verification-submit")).toBeEnabled();
    await user.click(screen.getByTestId("verification-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("verification-form")).toBeVisible();
    }, { timeout: 5_000 });
    expect(screen.getByTestId("verification-result-still_unknown")).toHaveAttribute("aria-checked", "true");
  }, 20_000);
});

describe("execution schedule prefill (§18.56 ruling B)", () => {
  it("prefills the schedule input from the submitter's requested time while it stays in range", async () => {
    const requested = new Date(Date.now() + 6 * 3600 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z");
    const order = executionOrder({ requested_scheduled_for: requested });
    seedFixtureOrder(order);
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-card")).toBeVisible();
    });
    // DateTimePicker trigger shows the prefilled instant (no "--" placeholder).
    expect(screen.getByTestId("execution-schedule-input").textContent).not.toContain("--");
    expect(screen.getByTestId("execution-schedule-prefilled")).toBeVisible();
    // Prefilled value can be submitted directly (within valid range).
    expect(screen.getByTestId("execution-schedule-submit")).toBeEnabled();
  });

  it("does not prefill an expired requested time", async () => {
    const order = executionOrder({ requested_scheduled_for: "2020-01-01T00:00:00Z" });
    seedFixtureOrder(order);
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-card")).toBeVisible();
    });
    expect(screen.getByTestId("execution-schedule-input").textContent).not.toMatch(/\d{4}-\d{2}-\d{2}/);
    expect(screen.queryByTestId("execution-schedule-prefilled")).toBeNull();
    expect(screen.getByTestId("execution-schedule-submit")).toBeDisabled();
  });
});

describe("execution schedule interactions (UX-004 / BUG-006)", () => {
  /** Drives the desktop DateTimePicker panel: open → pick day → time columns
   * → confirm (same interaction the §18.56 wizard suite exercises). */
  function pickSlot(target: Date): void {
    fireEvent.click(screen.getByTestId("execution-schedule-input"));
    const iso = `${String(target.getFullYear())}-${String(target.getMonth() + 1).padStart(2, "0")}-${String(target.getDate()).padStart(2, "0")}`;
    fireEvent.click(screen.getByRole("button", { name: iso }));
    fireEvent.click(screen.getByTestId("execution-schedule-input-time-toggle"));
    fireEvent.click(
      within(screen.getByTestId("dtp-hours")).getByRole("button", {
        name: String(target.getHours()).padStart(2, "0"),
      }),
    );
    fireEvent.click(
      within(screen.getByTestId("dtp-minutes")).getByRole("button", {
        name: String(target.getMinutes()).padStart(2, "0"),
      }),
    );
    fireEvent.click(screen.getByTestId("execution-schedule-input-confirm"));
  }

  it("shows the five-minute lead hint, then accepts a valid window and clears back to idle", async () => {
    const user = userEvent.setup();
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f912" });
    seedFixtureOrder(order);
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-card")).toBeVisible();
    });

    // <= 5 minutes: show explicit hint and keep the schedule button disabled (UX-004) instead of silently swallowing clicks.
    pickSlot(new Date(Date.now() + 3 * 60 * 1000));
    expect(screen.getByTestId("execution-schedule-note")).toHaveTextContent(/\u81f3\u5c11 5 \u5206\u949f/);
    expect(screen.getByTestId("execution-schedule-submit")).toBeDisabled();

    // Within window (> 5 minutes): hint disappears and button is enabled; when the success receipt scheduled_for is
    // null, display "—" (timestamp placeholder branch).
    pickSlot(new Date(Date.now() + 10 * 60 * 1000));
    expect(screen.queryByTestId("execution-schedule-note")).toBeNull();
    expect(screen.getByTestId("execution-schedule-submit")).toBeEnabled();
    server.use(
      http.post(apiRoute("/change-orders/:orderId/execution-schedules"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { id: "ee6f1a2b-0000-4000-8000-00000000d912", scheduled_for: null },
          request_id: "eeeeeeee-0000-4000-8000-00000000e912",
        }),
      ),
    );
    await user.click(screen.getByTestId("execution-schedule-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("execution-schedule-note")).toHaveTextContent("\u5df2\u9884\u7ea6");
    });
    // Success receipt cleared the input; pick once more and click the clear button to return to the unselected state.
    pickSlot(new Date(Date.now() + 10 * 60 * 1000));
    fireEvent.click(screen.getByTestId("execution-schedule-input-clear"));
    expect(screen.queryByTestId("execution-schedule-note")).toBeNull();
    expect(screen.getByTestId("execution-schedule-submit")).toBeDisabled();
  }, 20_000);

  it("surfaces the schedule rejection detail verbatim from meta.detail", async () => {
    const user = userEvent.setup();
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f913" });
    seedFixtureOrder(order);
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-card")).toBeVisible();
    });
    server.use(
      http.post(apiRoute("/change-orders/:orderId/execution-schedules"), () =>
        HttpResponse.json({
          err_code: 3008,
          message: "outside execution window",
          data: null,
          request_id: "eeeeeeee-0000-4000-8000-00000000d011",
          retryable: false,
          meta: { detail: "execution window closes at 18:00" },
        }),
      ),
    );
    pickSlot(new Date(Date.now() + 10 * 60 * 1000));
    await user.click(screen.getByTestId("execution-schedule-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-error")).toBeVisible();
    });
    expect(screen.getByTestId("execution-action-error")).toHaveTextContent("execution window closes at 18:00");
    expect(screen.getByTestId("execution-action-error")).toHaveTextContent("eeeeeeee-0000-4000-8000-00000000d011");
  }, 15_000);

  it("surfaces the preflight rejection detail verbatim from meta.detail", async () => {
    const user = userEvent.setup();
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f914" });
    seedFixtureOrder(order);
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    server.use(
      http.post(apiRoute("/change-orders/:orderId/execution-attempts"), () =>
        HttpResponse.json({
          err_code: 3006,
          message: "preflight rejected",
          data: null,
          request_id: "eeeeeeee-0000-4000-8000-00000000d012",
          retryable: false,
          meta: { detail: "sql_changed_after_review: snapshot hash moved" },
        }),
      ),
    );
    await user.click(screen.getByTestId("execution-start"));
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-error")).toBeVisible();
    });
    expect(screen.getByTestId("execution-action-error")).toHaveTextContent("sql_changed_after_review: snapshot hash moved");
  }, 15_000);
});

describe("execution schedule cancel (owner ruling 2026-10-30)", () => {
  /** An order staged in the scheduled state with a live schedule row — the
   * create endpoint's post-state, seeded directly so the cancel path doesn't
   * have to drive the datetime picker. */
  function scheduledOrder(id: string, executorIsCurrentUser = true) {
    const base = executionOrder({ id });
    const baseStage = base.stages[0];
    if (baseStage === undefined) throw new Error("seed stage missing");
    const actors = executorIsCurrentUser
      ? baseStage.execution_actors
      : [
          {
            id: "1111d9cc-e65d-7b9d-a8aa-3c81945f99ac",
            username: "other",
            display_name: "other",
            email: null,
            is_builtin_admin: false,
            version: 1,
            created_at: "2026-08-01T00:00:00Z",
            updated_at: "2026-08-01T00:00:00Z",
          },
        ];
    const order = executionOrder({
      id,
      state: "scheduled",
      stages: [{ ...baseStage, state: "scheduled", execution_actors: actors }],
    });
    seedFixtureOrder(order);
    seedFixtureSchedule(order.id, baseStage.id, "2026-12-01T03:08:00Z");
    return order;
  }

  it("shows the planned time to frozen executors and cancels back to pending execution", async () => {
    const user = userEvent.setup();
    const order = scheduledOrder("ee6f1a2b-0000-4000-8000-00000000f916");
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("execution-scheduled-card")).toBeVisible();
    });
    // The planned time comes from the order read face (active_schedule) formatted in system timezone (Asia/Shanghai = UTC+8).
    expect(screen.getByTestId("execution-scheduled-card")).toHaveTextContent("2026-12-01 11:08:00");
    // While scheduled the regular action card is gone.
    expect(screen.queryByTestId("execution-action-card")).toBeNull();

    await user.click(screen.getByTestId("execution-schedule-cancel"));
    expect(screen.getByTestId("execution-schedule-cancel-dialog")).toBeVisible();
    await user.click(screen.getByTestId("execution-schedule-cancel-confirm"));

    // Back to stage_execution_pending: the scheduled card disappears and the
    // regular executor area (re-schedule or run now) returns.
    await waitFor(() => {
      expect(screen.queryByTestId("execution-scheduled-card")).toBeNull();
    });
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-card")).toBeVisible();
    });
    // Submitter awareness = timeline record (owner ruling: push notifications are a separate follow-up requirement).
    await waitFor(() => {
      expect(screen.getByTestId("order-timeline")).toHaveTextContent("\u53d6\u6d88\u9884\u7ea6");
    });
  }, 15_000);

  it("clears the schedule confirmation note after transitioning through scheduled and back via cancel", async () => {
    const user = userEvent.setup();
    const requested = new Date(Date.now() + 2 * 3600 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z");
    const order = executionOrder({
      id: "ee6f1a2b-0000-4000-8000-00000000f983",
      requested_scheduled_for: requested,
    });
    seedFixtureOrder(order);
    renderDetail(order.id, "approvals");

    await waitFor(() => {
      expect(screen.getByTestId("execution-schedule-submit")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-schedule-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("execution-scheduled-card")).toBeVisible();
    });
    await user.click(screen.getByTestId("execution-schedule-cancel"));
    await user.click(screen.getByTestId("execution-schedule-cancel-confirm"));
    await waitFor(() => {
      expect(screen.getByTestId("execution-action-card")).toBeVisible();
    });
    expect(screen.queryByTestId("execution-schedule-note")).toBeNull();
  }, 15_000);

  it("hides the scheduled card from non-executors", async () => {
    const order = scheduledOrder("ee6f1a2b-0000-4000-8000-00000000f917", false);
    renderDetail(order.id);

    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page")).toBeVisible();
    });
    await waitFor(() => {
      expect(screen.queryByTestId("execution-scheduled-card")).toBeNull();
    });
  });
});

describe("copy-to-draft dialog branches (E004)", () => {
  function failedOrder(id: string): FixtureOrder {
    return executionOrder({ id, state: "failed" });
  }

  it("keeps the copy dialog inert when the flow catalog is empty and reuses it on reopen", async () => {
    const user = userEvent.setup();
    const order = failedOrder("ee6f1a2b-0000-4000-8000-00000000f915");
    seedFixtureOrder(order);
    let flowsCalls = 0;
    server.use(
      http.get(apiRoute("/users/me/flows"), () => {
        flowsCalls += 1;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: [] },
          request_id: "eeeeeeee-0000-4000-8000-00000000e915",
        });
      }),
    );
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("copy-draft-card")).toBeVisible();
    });
    await user.click(screen.getByTestId("copy-draft-open"));
    expect(screen.getByTestId("copy-draft-dialog")).toBeVisible();
    await waitFor(() => {
      expect(flowsCalls).toBe(1);
    });
    // Empty catalog: no selectable flow, confirm stays disabled (empty items[0] fallback branch).
    expect(screen.getByTestId("copy-draft-confirm")).toBeDisabled();
    await user.click(screen.getByTestId("copy-draft-cancel"));
    await user.click(screen.getByTestId("copy-draft-open"));
    expect(screen.getByTestId("copy-draft-dialog")).toBeVisible();
    // Reuse loaded catalog: reopening does not fetch again (flowsLoaded early-return branch).
    expect(flowsCalls).toBe(1);
    expect(screen.getByTestId("copy-draft-confirm")).toBeDisabled();
  }, 15_000);

  it("surfaces the draft-copy rejection inside the dialog and closes on escape", async () => {
    const user = userEvent.setup();
    const order = failedOrder("ee6f1a2b-0000-4000-8000-00000000f916");
    seedFixtureOrder(order);
    let capturedIdempotencyKey: string | null = null;
    server.use(
      http.get(apiRoute("/users/me/flows"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: [{ id: "ee6f1a2b-0000-4000-8000-00000000f901", name: "\u9ed8\u8ba4\u53d8\u66f4\u6d41\u7a0b" }] },
          request_id: "eeeeeeee-0000-4000-8000-00000000e916",
        }),
      ),
      http.post(apiRoute("/change-orders/:orderId/draft-copies"), ({ request }) => {
        capturedIdempotencyKey = request.headers.get("Idempotency-Key");
        return HttpResponse.json({
          err_code: 2014,
          message: "target flow invalid",
          data: null,
          request_id: "eeeeeeee-0000-4000-8000-00000000d013",
          retryable: false,
        });
      }),
    );
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("copy-draft-card")).toBeVisible();
    });
    await user.click(screen.getByTestId("copy-draft-open"));
    await waitFor(() => {
      expect(screen.getByTestId("copy-draft-confirm")).toBeEnabled();
    });
    expect(screen.getByTestId("copy-draft-flow")).toHaveTextContent("\u9ed8\u8ba4\u53d8\u66f4\u6d41\u7a0b");
    expect(screen.getByTestId("copy-draft-flow")).not.toHaveTextContent("ee6f1a2b-0000-4000-8000-00000000f901");
    await user.click(screen.getByTestId("copy-draft-confirm"));
    expect(capturedIdempotencyKey).toMatch(/^copy-draft-/);
    expect(screen.getByTestId("copy-draft-error")).toBeVisible();
    expect(screen.getByTestId("copy-draft-error")).toHaveTextContent("eeeeeeee-0000-4000-8000-00000000d013");
    await user.keyboard("{Escape}");
    await waitFor(() => {
      expect(screen.queryByTestId("copy-draft-dialog")).toBeNull();
    });
  }, 15_000);
});

describe("verification rejection and evidence branches (E005)", () => {
  it("surfaces verification rejections with and without a request id and edits a second evidence row", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-unknown");
    const order = executionOrder({ id: "ee6f1a2b-0000-4000-8000-00000000f917" });
    seedFixtureOrder(order);
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("execution-start")).toBeEnabled();
    });
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.queryByTestId("execution-ddl-warning")).toBeNull();
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(
      () => {
        expect(screen.getByTestId("verification-form")).toBeVisible();
      },
      { timeout: 5_000 },
    );

    // Typing into and deleting the second evidence row covers the cross-row mapping branch.
    await user.click(screen.getByTestId("verification-evidence-add"));
    await user.type(screen.getByTestId("verification-evidence-content-1"), "\u4ece\u5e93\u5ef6\u8fdf 30s");
    const secondRow = screen.getByTestId("verification-evidence-content-1").parentElement;
    const removeButton = secondRow?.querySelector('button[aria-label="\u79fb\u9664\u8bc1\u636e"]');
    if (!(removeButton instanceof HTMLButtonElement)) throw new Error("remove button missing");
    await user.click(removeButton);
    expect(screen.queryByTestId("verification-evidence-content-1")).toBeNull();

    // Declared business error: error message carries the request id verbatim.
    await user.click(screen.getByTestId("verification-result-confirmed_succeeded"));
    await user.type(screen.getByTestId("verification-reason"), "\u6838\u5bf9\u4ece\u5e93\u6570\u636e\u4e00\u81f4");
    await user.type(screen.getByTestId("verification-evidence-content-0"), "SELECT COUNT(*) \u4e00\u81f4");
    server.use(
      http.post(apiRoute("/execution-attempts/:attemptId/verifications"), () =>
        HttpResponse.json({
          err_code: 3012,
          message: "verification shape invalid",
          data: null,
          request_id: "eeeeeeee-0000-4000-8000-00000000d014",
          retryable: false,
        }),
      ),
    );
    await user.click(screen.getByTestId("verification-submit"));
    expect(screen.getByTestId("verification-error")).toBeVisible();
    expect(screen.getByTestId("verification-error")).toHaveTextContent("eeeeeeee-0000-4000-8000-00000000d014");

    // Transport-layer error: no request id to attach, error still renders inline.
    server.use(
      http.post(apiRoute("/execution-attempts/:attemptId/verifications"), () =>
        HttpResponse.json(
          {
            type: "about:blank",
            title: "internal error",
            status: 500,
            request_id: "eeeeeeee-0000-4000-8000-00000000d015",
            detail: "boom",
          },
          { status: 500 },
        ),
      ),
    );
    await user.click(screen.getByTestId("verification-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("verification-error")).not.toHaveTextContent(
        "eeeeeeee-0000-4000-8000-00000000d014",
      );
    });
    expect(screen.getByTestId("verification-error")).toBeVisible();
  }, 20_000);

  it("switches DDL execution mode between gh-ost and online DDL and submits per-attempt gh-ost overrides", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("yearning-mock-scenario", "execution-ghost");
    const order = executionOrder({
      id: "ee6f1a2b-0000-4000-8000-00000000f918",
    });
    seedFixtureOrder(order);
    let capturedBody: Record<string, unknown> | null = null;
    server.use(
      http.post(apiRoute("/change-orders/:orderId/execution-attempts"), async ({ request }) => {
        capturedBody = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          err_code: 3001,
          message: "preflight failed",
          data: null,
          request_id: "eeeeeeee-0000-4000-8000-00000000d018",
          retryable: false,
        });
      }),
    );
    renderDetail(order.id);
    await waitFor(() => {
      expect(screen.getByTestId("execution-ddl-mode-radiogroup")).toBeVisible();
    });

    // Switch to online_ddl mode and verify the gh-ost overrides box is hidden in the confirm dialog.
    await user.click(screen.getByTestId("execution-ddl-mode-online-ddl"));
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.queryByTestId("execution-osc-overrides-box")).toBeNull();
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(() => {
      expect(capturedBody).toEqual({ ddl_execution_mode: "online_ddl" });
    });
    await user.click(screen.getByTestId("execution-confirm-cancel"));

    // Switch back to gh_ost mode, expand per-attempt overrides, set and clear values, and submit.
    await user.click(screen.getByTestId("execution-ddl-mode-gh-ost"));
    await user.click(screen.getByTestId("execution-start"));
    expect(screen.getByTestId("execution-osc-overrides-box")).toBeVisible();
    await user.click(screen.getByTestId("execution-osc-overrides-toggle"));
    const maxLoadInput = screen.getByTestId("exec-osc-max-load");
    await user.type(maxLoadInput, "Threads_running=40");
    await user.clear(maxLoadInput);
    await user.type(maxLoadInput, "Threads_running=60");
    const chunkInput = screen.getByTestId("exec-osc-chunk-size");
    await user.type(chunkInput, "500");
    await user.clear(chunkInput);
    await user.type(chunkInput, "1500");
    await user.click(screen.getByTestId("execution-confirm-run"));
    await waitFor(() => {
      expect(capturedBody).toEqual({
        osc_overrides: {
          max_load: "Threads_running=60",
          chunk_size: 1500,
        },
      });
    });
  }, 20_000);

  it("renders human-readable failure explanation alongside failure_name code in statement ledger", async () => {
    const attemptId = "ee6f1a2b-0000-4000-8000-00000000a922";
    const stageId = "ee6f1a2b-0000-4000-8000-000000000922";
    const order = executionOrder({
      id: "ee6f1a2b-0000-4000-8000-00000000f922",
      state: "failed",
      stages: [
        {
          id: stageId,
          position: 1,
          datasource_name: "pg-prod",
          state: "failed",
          approval_steps: [],
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
    seedFixtureOrder(order);
    server.use(
      http.get(apiRoute("/change-orders/:orderId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: order.id,
            display_number: order.display_number,
            submitter_user_id: order.submitter_user_id,
            submitter_username: "henry",
            submitter_display_name: "henry",
            title: order.title,
            state: order.state,
            current_stage_position: order.current_stage_position,
            requested_scheduled_for: null,
            stages: order.stages,
            has_sql: order.has_sql,
            sql_hash: order.sql_hash,
            snapshot_hash: order.snapshot_hash,
            manually_verified: false,
            has_ddl: true,
            osc_choice_available: false,
            version: order.version,
            submitted_at: order.submitted_at,
            terminal_at: "2026-11-19T06:30:31Z",
            latest_attempt_id: attemptId,
          },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
      http.get(apiRoute("/execution-attempts/:attemptId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: attemptId,
            stage_id: stageId,
            execution_kind: "ddl",
            state: "failed",
            send_boundary: "not_started",
            osc: null,
            version: 2,
            created_at: "2026-11-19T06:30:30Z",
          },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
      http.get(apiRoute("/execution-attempts/:attemptId/statements"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [
              {
                id: "ee6f1a2b-0000-4000-8000-000000000b22",
                ordinal: 1,
                statement_kind: "ddl",
                state: "failed",
                affected_row_count: null,
                failure_name: "ROLLBACK_UNSUPPORTED",
                started_at: "2026-11-19T06:30:31Z",
                finished_at: "2026-11-19T06:30:31Z",
              },
            ],
            page: { next_cursor: null, has_more: false },
          },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
    );

    renderDetail(order.id);
    const row = await screen.findByTestId("execution-statement-1");
    expect(row.textContent).toContain("\u672a\u5411\u76ee\u6807\u5e93\u53d1\u9001 SQL");
    expect(row.textContent).toContain("ROLLBACK_UNSUPPORTED");
    const attemptHeader = screen.getByTestId("execution-attempt-card").querySelector("[data-slot='card-header']");
    expect(attemptHeader?.querySelector("[data-slot='badge']")).toBeNull();
  });
});
