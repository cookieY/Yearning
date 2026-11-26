import { render, screen, waitFor } from "@testing-library/react";
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
  FIXTURE_FLOW_ID,
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
  type FixtureOrder,
} from "@/shared/mock/review-fixture";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import OrderDetailPage from "@/routes/changes/order-detail-page";

// jsdom cannot host the lazy Monaco chunk (query-pages precedent); the
// revealed SQL renders through the read-only viewer, so the reveal test
// below needs the module stubbed with a plain text surface.
vi.mock("@/features/review/sql-editor-panel", () => ({
  SqlEditorPanel: ({
    value,
    "data-testid": testId,
  }: {
    value: string;
    "data-testid"?: string;
  }) => (
    <textarea data-testid={testId ?? "sql-editor-stub"} readOnly value={value} onChange={() => {}} />
  ),
}));

/**
 * Approval decision gates (work package FE-F7): the decision card renders
 * for the frozen reviewer of the active step, any rejection immediately
 * rejects the whole order, the final approve only enters execution-pending,
 * and a concurrent conflict surfaces inside the dialog while the page
 * refetches — no fake success (W003; gate: concurrent conflict is recoverable).
 */

const REQUEST_ID = "33333333-3333-4333-8333-333333333333";

beforeEach(() => {
  server.resetHandlers();
  resetReviewFixture();
  grantSession();
});

afterEach(() => {
  resetReviewFixture();
});

function approvalOrder(overrides: Partial<FixtureOrder> = {}): FixtureOrder {
  return {
    id: "ad6f1a2b-0000-4000-8000-00000000f701",
    display_number: "YR-20260830-000071",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "\u5ba1\u6279\u5939\u5177\u5de5\u5355",
    state: "stage_approval_active",
    current_stage_position: 1,
    stages: [
      {
        id: "ad6f1a2b-0000-4000-8000-00000000f711",
        position: 1,
        datasource_name: "orders-mysql",
        state: "approval_active",
        approval_steps: [
          {
            id: "ad6f1a2b-0000-4000-8000-00000000f721",
            position: 1,
            state: "active",
            decided_at: null,
            actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
          },
        ],
        execution_actors: [],
      },
    ],
    has_sql: true,
    sql_hash: "hash-71",
    snapshot_hash: "snap-71",
    manually_verified: false,
    version: 1,
    submitted_at: "2026-08-30T11:00:00Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "UPDATE orders SET status = 1 WHERE user_id = 42;",
    ...overrides,
  };
}

function findingFixture(overrides: Partial<ReviewFinding> = {}): ReviewFinding {
  return {
    id: "cd6f1a2b-0000-4000-8000-00000000f101",
    stage_position: 1,
    fingerprint_group_id: null,
    category: "performance",
    severity: "low",
    title: "\u7d22\u5f15\u7f3a\u5931",
    message: "\u8bed\u53e5 `UPDATE orders SET status = 1` \u65e0\u6cd5\u547d\u4e2d\u7d22\u5f15\u3002",
    suggestion: "\u8865\u5145\u7d22\u5f15\u3002",
    model_confidence: 0.9,
    evidence_ids: [],
    ...overrides,
  };
}

function renderDetail(orderId: string, entry: "changes" | "approvals" = "changes") {
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

interface SubmittedFixture {
  id: string;
  review_run_id: string;
}

/** Drives the real fixture path to a submitted order: create → SQL →
 * ephemeral precheck → ready → submit. The run plants findings; the order
 * freezes the run link so the approval page has a genuine frozen snapshot
 * to project (RCP-20261101: precheck carries the SQL verbatim, submit posts
 * /change-orders with the run id). */
async function submitReadyDraft(): Promise<string> {
  const created = (await (
    await fetch("/api/v4/change-drafts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u5ba1\u6279\u5939\u5177\u63d0\u4ea4\u6d41" }),
    })
  ).json()) as { data: { id: string } };
  const draftId = created.data.id;
  const sql = "UPDATE orders SET status = 1 WHERE user_id = 42;";
  await fetch(`/api/v4/change-drafts/${draftId}/sql`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sql }),
  });
  const started = (await (
    await fetch("/api/v4/review-runs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, draft_id: draftId, sql }),
    })
  ).json()) as { data: { run_id: string } };
  await waitFor(
    async () => {
      const response = (await (await fetch(`/api/v4/review-runs/${started.data.run_id}`)).json()) as {
        data: { state: string };
      };
      if (response.data.state !== "ready") throw new Error("run not finished yet");
      return response.data;
    },
    { timeout: 4_000 },
  );
  const submitted = (await (
    await fetch("/api/v4/change-orders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        flow_id: FIXTURE_FLOW_ID,
        draft_id: draftId,
        sql,
        run_id: started.data.run_id,
      }),
    })
  ).json()) as { data: SubmittedFixture };
  return submitted.data.id;
}

describe("approval decision on the order detail page", () => {
  it("renders the decision card for the frozen reviewer and rejects the whole order immediately", async () => {
    const user = userEvent.setup();
    seedFixtureOrder(approvalOrder());
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701", "approvals");
    const card = await screen.findByTestId("approval-decision-card");
    expect(card).toBeVisible();

    await user.click(screen.getByTestId("approval-reject"));
    const dialog = screen.getByTestId("approval-decision-dialog");
    // The propagation rule is restated before confirming (W003).
    expect(dialog.textContent).toContain("\u7acb\u5373\u62d2\u7edd\u6574\u5355");
    await user.type(screen.getByTestId("approval-decision-comment"), "\u8bed\u53e5\u98ce\u9669\u4e0d\u53ef\u63a5\u53d7");
    await user.click(screen.getByTestId("approval-decision-confirm"));
    await waitFor(() => {
      expect(screen.queryByTestId("approval-decision-card")).toBeNull();
    });
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page").textContent).toContain("\u5df2\u62d2\u7edd");
    });
  });

  // owner ruling 2026-10-31 (self-review/self-execute button split): when the same user is both submitter and current
  // frozen reviewer, the action set is mutually exclusive by entry — the approval entry shows only Approve/Reject, while the
  // My Orders entry shows only Withdraw/Void. The fixture models this exact case (both submitter and reviewer are henry).
  it("scopes the self-actor's action set by entry context", async () => {
    seedFixtureOrder(approvalOrder());
    // My Orders entry: Withdraw/Void are present; approval decision does not render.
    const mine = renderDetail("ad6f1a2b-0000-4000-8000-00000000f701");
    expect(await mine.findByTestId("withdraw-order")).toBeInTheDocument();
    expect(mine.getByTestId("void-order")).toBeInTheDocument();
    expect(mine.queryByTestId("approval-approve")).toBeNull();
    expect(mine.queryByTestId("approval-reject")).toBeNull();
    mine.unmount();
    // Approval queue entry: Approve/Reject are present; Withdraw/Void do not render.
    const queue = renderDetail("ad6f1a2b-0000-4000-8000-00000000f701", "approvals");
    expect(await queue.findByTestId("approval-approve")).toBeInTheDocument();
    expect(queue.getByTestId("approval-reject")).toBeInTheDocument();
    // When the decision card arrives, the header has already rendered, so asserting absence directly suffices.
    expect(queue.queryByTestId("withdraw-order")).toBeNull();
    expect(queue.queryByTestId("void-order")).toBeNull();
    queue.unmount();
  });

  it("the final approve leaves the order at execution-pending and removes the decision card", async () => {
    const user = userEvent.setup();
    seedFixtureOrder(approvalOrder());
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701", "approvals");
    await user.click(await screen.findByTestId("approval-approve"));
    await user.click(screen.getByTestId("approval-decision-confirm"));
    await waitFor(() => {
      expect(screen.queryByTestId("approval-decision-card")).toBeNull();
    });
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page").textContent).toContain("\u5f85\u6267\u884c");
    });
  });

  it("recovers from a concurrent conflict: error inside the dialog, then the refetched real state", async () => {
    const user = userEvent.setup();
    const orderId = "ad6f1a2b-0000-4000-8000-00000000f701";
    seedFixtureOrder(approvalOrder());
    // Observe the recovery path directly: count detail-GET fetches via a
    // manual wrapper in front of the MSW-patched fetch, while the decision
    // itself always answers 1004.
    let detailGets = 0;
    const originalFetch = window.fetch.bind(window);
    const countingFetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof Request ? input.url : String(input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (url.includes(`/change-orders/${orderId}`) && method === "GET") detailGets += 1;
      return originalFetch(input, init);
    }) as typeof window.fetch;
    window.fetch = countingFetch;
    try {
      server.use(
        http.post(apiRoute("/change-orders/:orderId/approval-decisions"), () =>
          HttpResponse.json({
            err_code: 1004,
            message: "order changed elsewhere",
            data: null,
            request_id: REQUEST_ID,
            retryable: false,
          }),
        ),
      );
      renderDetail(orderId, "approvals");
      await screen.findByTestId("approval-decision-card");
      const readsBeforeConfirm = detailGets;
      await user.click(screen.getByTestId("approval-approve"));
      await user.click(screen.getByTestId("approval-decision-confirm"));
      const error = await screen.findByTestId("approval-decision-error");
      expect(error.textContent).toContain("\u8d44\u6e90\u6b63\u5728\u88ab\u5176\u4ed6\u4eba\u4fee\u6539");
      expect(error.textContent).toContain(REQUEST_ID);
      // Recovery: the dialog stays open, no success state is faked, and the
      // card's onRecover has re-read the aggregate (kept in approval state).
      expect(screen.getByTestId("approval-decision-dialog")).toBeVisible();
      expect(screen.getByTestId("order-detail-page").textContent).not.toContain("\u5df2\u62d2\u7edd");
      await waitFor(() => {
        expect(detailGets).toBeGreaterThan(readsBeforeConfirm);
      });
    } finally {
      window.fetch = originalFetch;
    }
  });

  it("offers no transfer, add-signer or remove-signer affordance anywhere (W004)", async () => {
    seedFixtureOrder(approvalOrder());
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701", "approvals");
    await screen.findByTestId("approval-decision-card");
    const page = screen.getByTestId("order-detail-page");
    for (const forbidden of ["\u8f6c\u4ea4", "\u52a0\u7b7e", "\u51cf\u7b7e", "\u6539\u6d3e"]) {
      expect(page.textContent).not.toContain(forbidden);
    }
    expect(screen.queryByTestId(/transfer|delegate|reassign/i)).toBeNull();
  });

  it("shows the invalid notice for a W008-invalidated order", async () => {
    seedFixtureOrder(approvalOrder({ state: "invalid" }));
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701");
    const alert = await screen.findByTestId("invalid-order-alert");
    expect(alert.textContent).toContain("\u51bb\u7ed3\u5ba1\u6838\u4eba\u5df2\u88ab\u79fb\u9664");
    // The decision card cannot render for an invalid order.
    expect(screen.queryByTestId("approval-decision-card")).toBeNull();
  });
});

describe("frozen review and comments on the order detail page", () => {
  it("serves the frozen submission findings with evidence through the full submission flow", async () => {
    const user = userEvent.setup();
    // Drive the real fixture flow: review-ready plants a medium finding on
    // the run, submission freezes it onto the order, and the approval page
    // projects it as a pure read (no review-run request is ever issued —
    // asserted in E2E).
    const orderId = await submitReadyDraft();
    renderDetail(orderId);
    await screen.findByTestId("frozen-review-card");
    const items = await screen.findAllByTestId("finding-item");
    expect(items.length).toBeGreaterThan(0);
    // Evidence opens through the same controlled sheet as the submission
    // workspace.
    const evidenceButtons = screen.getAllByRole("button", { name: "\u67e5\u770b\u5ba1\u6838\u8bc1\u636e" });
    const firstEvidenceButton = evidenceButtons[0];
    if (firstEvidenceButton === undefined) throw new Error("no evidence button found");
    await user.click(firstEvidenceButton);
    await waitFor(() => {
      expect(screen.getAllByTestId("evidence-item").length).toBeGreaterThan(0);
    });
  });

  it("reads an empty frozen snapshot as the honest empty state", async () => {
    seedFixtureOrder(
      approvalOrder({
        review_run_id: "cd6f1a2b-0000-4000-8000-000000003e01",
      }),
    );
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701");
    await screen.findByTestId("frozen-review-card");
    await waitFor(() => {
      expect(screen.getByTestId("frozen-review-card").textContent).toContain(
        "\u4e0d\u4f1a\u91cd\u65b0\u8fd0\u884cAI\u5ba1\u6838",
      );
    });
    // A review_run_id that resolves to no planted findings reads as the
    // honest empty state from the shared finding list.
    await waitFor(() => {
      expect(screen.getByTestId("findings-empty-all")).toBeVisible();
    });
  });

  it("loads every frozen finding page before applying the severity filter", async () => {
    const user = userEvent.setup();
    const orderId = "ad6f1a2b-0000-4000-8000-00000000f701";
    seedFixtureOrder(
      approvalOrder({
        id: orderId,
        review_run_id: "cd6f1a2b-0000-4000-8000-000000003e02",
      }),
    );
    const firstPage = Array.from({ length: 50 }, (_, index) =>
      findingFixture({
        id: `cd6f1a2b-0000-4000-8000-${String(index + 1).padStart(12, "0")}`,
        title: `\u4f4e\u98ce\u9669\u53d1\u73b0 ${String(index + 1)}`,
      }),
    );
    const secondPageFinding = findingFixture({
      id: "cd6f1a2b-0000-4000-8000-000000000051",
      severity: "critical",
      title: "\u51bb\u7ed3\u53d1\u73b0\u7b2c\u4e8c\u9875\u4e25\u91cd\u9879",
      message: "\u4ec5\u4ece\u51bb\u7ed3\u53d1\u73b0\u7b2c\u4e8c\u9875\u8fd4\u56de\u7684\u4e25\u91cd\u98ce\u9669\u3002",
    });
    const requestedCursors: Array<string | null> = [];
    const requestedLimits: Array<string | null> = [];
    server.use(
      http.get(apiRoute("/change-orders/:orderId/review-findings"), ({ request }) => {
        const url = new URL(request.url);
        const after = url.searchParams.get("after");
        requestedCursors.push(after);
        requestedLimits.push(url.searchParams.get("limit"));
        const isSecondPage = after === "frozen-cursor-page-2";
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: isSecondPage ? [secondPageFinding] : firstPage,
            page: isSecondPage
              ? { next_cursor: null, has_more: false }
              : { next_cursor: "frozen-cursor-page-2", has_more: true },
          },
          request_id: REQUEST_ID,
        });
      }),
    );

    renderDetail(orderId);
    await screen.findByTestId("frozen-review-card");
    await waitFor(() => {
      expect(requestedCursors).toEqual([null, "frozen-cursor-page-2"]);
    });
    expect(requestedLimits).toEqual(["200", "200"]);

    await user.click(screen.getByLabelText("\u7b5b\u9009\u4e25\u91cd\u7ea7\u522b"));
    await user.click(await screen.findByRole("option", { name: "\u4e25\u91cd" }));
    expect(screen.getAllByTestId("finding-item")).toHaveLength(1);
    expect(screen.getByText("\u51bb\u7ed3\u53d1\u73b0\u7b2c\u4e8c\u9875\u4e25\u91cd\u9879")).toBeInTheDocument();
  });

  it("reveals the frozen SQL plaintext with watermark and audits copies, keeping it out of storage", async () => {
    const user = userEvent.setup();
    seedFixtureOrder(approvalOrder({ sql_text: "UPDATE orders SET status = 1;" }));
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701");
    await user.click(await screen.findByTestId("reveal-order-sql"));
    // The viewer chunk is lazy (§18.45): wait for the stub surface, not the
    // instantly-rendered card frame.
    const view = await screen.findByTestId("order-sql-readonly");
    expect(view.textContent).toContain("UPDATE orders SET status = 1;");
    expect(screen.getByTestId("order-sql-view").textContent).toContain("Yearning SQL Viewer: henry @");
    // Memory-only: plaintext never touches web storage.
    expect(JSON.stringify(localStorage)).not.toContain("UPDATE orders SET status = 1;");
    expect(sessionStorage.length).toBe(0);
    // Copy goes through the audited path: the clipboard write resolves first,
    // then the copy-audit endpoint is observed (SEC-0035 ordering).
    let copyAudited = false;
    server.use(
      http.post(apiRoute("/change-orders/:orderId/sql-copy-events"), () => {
        copyAudited = true;
        return HttpResponse.json({ err_code: 0, message: "ok", data: { recorded: true }, request_id: REQUEST_ID });
      }),
    );
    await user.click(screen.getByTestId("copy-order-sql"));
    await waitFor(() => {
      expect(copyAudited).toBe(true);
    });
    // Closing the viewer wipes the plaintext from memory.
    await user.click(screen.getByTestId("close-order-sql"));
    expect(screen.queryByTestId("order-sql-view")).toBeNull();
  });

  it("posts a comment and lists it, keeping backend errors inline", async () => {
    const user = userEvent.setup();
    seedFixtureOrder(approvalOrder());
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701");
    await user.click(await screen.findByTestId("order-activity-tab-comments"));
    await screen.findByTestId("order-comments");
    await user.type(screen.getByTestId("order-comment-input"), "\u8bf7\u8865\u5145\u56de\u6eda\u8bf4\u660e");
    await user.click(screen.getByTestId("order-comment-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("order-comment-item").textContent).toContain("\u8bf7\u8865\u5145\u56de\u6eda\u8bf4\u660e");
    });
    // Own comment: right-aligned chat row (owner page feedback 2026-11-04).
    expect(screen.getByTestId("order-comment-item")).toHaveAttribute("data-align", "end");

    server.use(
      http.post(apiRoute("/change-orders/:orderId/comments"), () =>
        HttpResponse.json({
          err_code: 1010,
          message: "comment is not legal from this state",
          data: null,
          request_id: REQUEST_ID,
          retryable: false,
        }),
      ),
    );
    await user.type(screen.getByTestId("order-comment-input"), "\u7b2c\u4e8c\u6761");
    await user.click(screen.getByTestId("order-comment-submit"));
    const error = await screen.findByTestId("order-comment-error");
    expect(error.textContent).toContain("\u5f53\u524d\u72b6\u6001\u4e0d\u5141\u8bb8\u8be5\u64cd\u4f5c\u3002");
    expect(error.textContent).toContain(REQUEST_ID);
  });

  it("sends on Enter and keeps Shift+Enter as a plain line break", async () => {
    const user = userEvent.setup();
    seedFixtureOrder(approvalOrder());
    renderDetail("ad6f1a2b-0000-4000-8000-00000000f701");
    await user.click(await screen.findByTestId("order-activity-tab-comments"));
    await screen.findByTestId("order-comments");

    // Enter submits the draft instead of inserting a newline (composer ruling
    // 2026-11-04); the composer resets once the mutation settles.
    await user.type(screen.getByTestId("order-comment-input"), "Enter \u76f4\u63a5\u53d1\u9001{enter}");
    await waitFor(() => {
      expect(screen.getByTestId("order-comment-item").textContent).toContain("Enter \u76f4\u63a5\u53d1\u9001");
    });
    await waitFor(() => {
      expect(screen.getByTestId<HTMLTextAreaElement>("order-comment-input").value).toBe("");
    });

    // Shift+Enter keeps editing — a wrong submit would have cleared the draft.
    await user.type(screen.getByTestId("order-comment-input"), "\u7b2c\u4e8c{shift>}{enter}{/shift}\u884c");
    expect(screen.getByTestId<HTMLTextAreaElement>("order-comment-input").value).toBe("\u7b2c\u4e8c\n\u884c");
  });
});
