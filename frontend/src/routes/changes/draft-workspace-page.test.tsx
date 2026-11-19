import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MotionConfig } from "motion/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "@/features/auth/session-provider";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HttpResponse, http } from "msw";
import { server } from "@/test/msw/server";
import "@/shared/i18n";
import type {
  GetReviewRun200,
  ReviewFinding,
  ReviewRun,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  FIXTURE_FLOW_ID,
  FIXTURE_OWNER_ID,
  fixtureRunIds,
  resetReviewFixture,
} from "@/shared/mock/review-fixture";
import { getReviewEventClient } from "@/shared/events/review-event-client";
import DraftWorkspacePage from "./draft-workspace-page";
import OrderDetailPage from "./order-detail-page";

/**
 * Workspace behavior gates (work package FE-F4-PRECHECK, RCP-20261101
 * ephemeral precheck):
 * - Opening editor does not auto-run Review — mounting the page never starts a run;
 * - Precheck sends only the current editor SQL — the run id lives in client state only;
 * - SQL change immediately invalidates result — a local edit voids a ready result (staleness is the
 *   editor text vs the text sent for the last precheck);
 * - remount loses the ephemeral result (in-memory run, no draft linkage);
 * - events stay notification-only: a flow.updated event blocks submission.
 * Monaco is stubbed with a plain textarea: the editor internals have their
 * own lifecycle guarantees, the page tests target draft/gate behavior.
 */

const OWNER = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";
const WORKSPACE_SQL = "UPDATE orders SET status = 1 WHERE user_id = 42;";

function findingFixture(overrides: Partial<ReviewFinding> = {}): ReviewFinding {
  return {
    id: "4f6f1a2b-0000-4000-8000-00000000f101",
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

// jsdom lacks ResizeObserver; the resizable panels need it for layout.
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
vi.stubGlobal("ResizeObserver", ResizeObserverStub);

vi.mock("@/features/review/sql-editor-panel", () => ({
  SqlEditorPanel: ({
    value,
    onChange,
    ...rest
  }: {
    value: string;
    onChange: (sql: string) => void;
    readOnly?: boolean;
    loadValue?: unknown;
    onLocate?: unknown;
    "data-testid"?: string;
  }) => (
    <textarea
      aria-label="SQL Editor Stub"
      value={value}
      onChange={(event) => {
        onChange(event.target.value);
      }}
      data-testid={rest["data-testid"]}
    />
  ),
}));

async function createDraftWithSql(title = "Preflight E2E Draft"): Promise<string> {
  const create = (await (
    await fetch("/change-drafts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title }),
    })
  ).json()) as { data: { id: string } };
  await fetch(`/change-drafts/${create.data.id}/sql`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sql: WORKSPACE_SQL }),
  });
  return create.data.id;
}

function renderWorkspace(draftId: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const utils = render(
    <QueryClientProvider client={queryClient}>
      {/* MotionConfig: motion opacity/transform variants do not read
       * prefers-reduced-motion by default (the matchMedia stub in setup.ts only affects
       * MotionConfig) — without this wrapper, Tab entrance animations play on rAF and sampling toBeVisible within the 200ms
       * animation window races. Static rendering matches the reduced-motion user experience. */}
      <MotionConfig reducedMotion="always">
        <SessionProvider>
        <MemoryRouter initialEntries={[`/changes/drafts/${draftId}`]}>
          <Routes>
            <Route path="/changes/drafts/:draftId" element={<DraftWorkspacePage />} />
            <Route path="/changes/orders/:orderId" element={<OrderDetailPage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>
      </MotionConfig>
    </QueryClientProvider>,
  );
  return { ...utils, queryClient };
}

/** Loads the server SQL into the editor (memory discipline: explicit reveal)
 * so a UI precheck has text to send. */
async function revealEditor(): Promise<HTMLElement> {
  // Owner ruling 2026-11-01: entering a draft automatically loads the full SQL text (the manual SQL reveal gate was removed).
  const editor = await screen.findByTestId("sql-editor");
  await waitFor(() => {
    expect(editor).toHaveValue(WORKSPACE_SQL);
  });
  return editor;
}

/** Drives the real fixture path: reveal -> AI precheck -> wait for ready. Returns
 * the started run id (client state; the draft row never carries it). */
async function precheckToReady(): Promise<string> {
  await revealEditor();
  fireEvent.click(screen.getByTestId("run-review"));
  expect(await screen.findByTestId("precheck-dialog")).toBeVisible();
  await waitFor(
    () => {
      expect(screen.getByTestId("precheck-progress-value")).toHaveTextContent("100%");
      expect(screen.getByTestId("review-status").textContent).toContain("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574");
    },
    { timeout: 4_000 },
  );
  const runId = fixtureRunIds().at(-1);
  if (runId === undefined) throw new Error("fixture carries no review run");
  return runId;
}

/** A full UI-driven ready workspace: draft + SQL + precheck through the
 * page (the only writer of the run is the AI precheck click). */
async function renderReadyWorkspace(): Promise<{
  utils: ReturnType<typeof renderWorkspace>;
  draftId: string;
  runId: string;
}> {
  const draftId = await createDraftWithSql();
  const utils = renderWorkspace(draftId);
  expect(await screen.findByTestId("run-review")).toBeVisible();
  const runId = await precheckToReady();
  return { utils, draftId, runId };
}

/** Forces the run query to re-read through the current MSW handlers. */
async function refetchRunQuery(
  queryClient: ReturnType<typeof renderWorkspace>["queryClient"],
  reviewRunId: string,
): Promise<void> {
  await queryClient.invalidateQueries({ queryKey: ["review-run", reviewRunId] });
  await queryClient.refetchQueries({ queryKey: ["review-run", reviewRunId] });
}

/** A frozen queued snapshot the wizard polls instead of the live timers. */
function frozenQueuedRun(runId: string): ReviewRun {
  return {
    id: runId,
    draft_id: FIXTURE_FLOW_ID,
    draft_revision: 0,
    state: "queued",
    statement_count: 1,
    fingerprint_group_count: 1,
    stage_results: [],
    gate: { passed: false, reason_codes: ["stage_review_incomplete"] },
    failure_code: null,
    version: 1,
    created_at: "2026-08-30T00:00:00Z",
    started_at: null,
    finished_at: null,
  };
}

/** A frozen terminal run snapshot for freeze-then-precheck drives. */
function frozenTerminalRun(runId: string, state: "failed" | "partial" | "blocked"): ReviewRun {
  return {
    ...frozenQueuedRun(runId),
    state,
    finished_at: "2026-08-30T00:00:02Z",
    failure_code: state === "blocked" ? "risk_blocked" : "coverage_incomplete",
    gate: { passed: false, reason_codes: [state === "blocked" ? "critical_severity_finding" : "stage_review_incomplete"] },
  };
}

function deferred(): { promise: Promise<void>; resolve: () => void } {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
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

describe("DraftWorkspacePage", () => {
  it("mounts, loads draft state and never auto-creates a review run", async () => {
    const draftId = await createDraftWithSql();
    renderWorkspace(draftId);
    // The workspace mounts idle — no run exists and no precheck modal opens until explicit click.
    expect(await screen.findByTestId("run-review")).toBeVisible();
    expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("view-precheck-results")).not.toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 1500));
    expect(fixtureRunIds()).toHaveLength(0);
    expect(screen.queryByTestId("submit-draft")).not.toBeInTheDocument();
    expect(screen.queryByTestId("submission-readiness")).not.toBeInTheDocument();
    expect(screen.getByTestId("wizard-summary-precheck")).toBeVisible();
  });

  it("prechecks the current editor SQL and unlocks submit on a passing gate", async () => {
    const { runId } = await renderReadyWorkspace();
    // The precheck run exists in the ephemeral world and passed the gate.
    const run = (await (
      await fetch(`/review-runs/${runId}`)
    ).json()) as GetReviewRun200;
    expect(run.err_code).toBe(0);
    expect(run.data?.state).toBe("ready");
    expect(run.data?.gate.passed).toBe(true);
    expect(screen.getByTestId("submit-draft")).toBeEnabled();
  });

  it("voids a ready result the instant the SQL changes locally", async () => {
    await renderReadyWorkspace();
    // userEvent.type does not reliably synthesize input events through the
    // full page tree in jsdom; the gate is the page's reaction to a SQL
    // change, which fireEvent exercises directly.
    fireEvent.change(screen.getByTestId("sql-editor"), { target: { value: "SELECT 2" } });
    await waitFor(() => {
      expect(screen.getByTestId("review-status").textContent).toContain("SQL\u5df2\u4fee\u6539");
    });
    // The dock blocks: the editor text no longer matches the prechecked text
    // (staleness is a pure local comparison, RCP-20261101).
    expect(screen.getByTestId("submit-draft")).toBeDisabled();
    fireEvent.click(screen.getByTestId("tab-findings"));
    expect(await screen.findByTestId("findings-stale")).toHaveTextContent("SQL\u5df2\u4fee\u6539");
  });

  it("presents the expired state when the precheck run disappears (TTL/restart)", async () => {
    // Expiry occurs within the polling window (404 while running): freeze 404 then start precheck so the first
    // poll hits it -> presents expired state, keeping submission locked (derived semantics, RCP-20261101).
    const draftId = await createDraftWithSql("\u8fc7\u671f\u6001\u9884\u5ba1");
    server.use(
      http.get("*/review-runs/:runId", () =>
        HttpResponse.json(
          {
            type: "about:blank",
            title: "Not Found",
            status: 404,
            detail: "review run expired",
            request_id: OWNER,
          },
          { status: 404 },
        ),
      ),
    );
    renderWorkspace(draftId);
    expect(await screen.findByTestId("run-review")).toBeVisible();
    await revealEditor();
    fireEvent.click(screen.getByTestId("run-review"));
    expect(await screen.findByTestId("precheck-dialog")).toBeVisible();
    fireEvent.click(await screen.findByTestId("tab-findings"));
    expect(await screen.findByTestId("findings-expired")).toHaveTextContent("\u9884\u5ba1\u7ed3\u679c\u5df2\u8fc7\u671f");
    expect(screen.getByTestId("submit-draft")).toBeDisabled();
  });

  it("distinguishes terminal findings loading from an empty review", async () => {
    const requestStarted = deferred();
    const releaseRequest = deferred();
    server.use(
      http.get("*/review-runs/:runId/findings", async () => {
        requestStarted.resolve();
        await releaseRequest.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [],
            page: { next_cursor: null, has_more: false },
          },
          request_id: OWNER,
        });
      }),
    );

    const draftId = await createDraftWithSql();
    renderWorkspace(draftId);
    await revealEditor();
    fireEvent.click(screen.getByTestId("run-review"));
    await requestStarted.promise;

    // Terminal run is fetched, but findings remain in flight: progress holds at 95% before revealing detailed tabs.
    await waitFor(() => {
      expect(screen.getByTestId("precheck-progress-value")).toHaveTextContent("95%");
    });
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();

    releaseRequest.resolve();
    await waitFor(() => {
      expect(screen.getByTestId("precheck-progress-value")).toHaveTextContent("100%");
    });
    fireEvent.click(await screen.findByTestId("tab-findings"));
    await waitFor(() => {
      expect(screen.getByTestId("findings-empty-all")).toBeVisible();
    });
  });

  it("loads every finding page before applying the severity filter", async () => {
    const user = userEvent.setup();
    // Install the paginated handler first so the initial fetch at run terminal state consumes the two-page cursor.
    const firstPage = Array.from({ length: 50 }, (_, index) =>
      findingFixture({
        id: `4f6f1a2b-0000-4000-8000-${String(index + 1).padStart(12, "0")}`,
        title: `\u4f4e\u98ce\u9669\u53d1\u73b0 ${String(index + 1)}`,
      }),
    );
    const secondPageFinding = findingFixture({
      id: "4f6f1a2b-0000-4000-8000-000000000051",
      severity: "critical",
      title: "\u7b2c\u4e8c\u9875\u4e25\u91cd\u53d1\u73b0",
      message: "\u4ec5\u4ece\u7b2c\u4e8c\u9875\u8fd4\u56de\u7684\u4e25\u91cd\u98ce\u9669\u3002",
    });
    const requestedCursors: Array<string | null> = [];
    const requestedLimits: Array<string | null> = [];
    server.use(
      http.get("*/review-runs/:runId/findings", ({ request }) => {
        const url = new URL(request.url);
        const after = url.searchParams.get("after");
        requestedCursors.push(after);
        requestedLimits.push(url.searchParams.get("limit"));
        const isSecondPage = after === "cursor-page-2";
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: isSecondPage ? [secondPageFinding] : firstPage,
            page: isSecondPage
              ? { next_cursor: null, has_more: false }
              : { next_cursor: "cursor-page-2", has_more: true },
          },
          request_id: OWNER,
        });
      }),
    );

    await renderReadyWorkspace();
    fireEvent.click(screen.getByTestId("tab-findings"));
    await waitFor(() => {
      // Initial fetch completes full pagination: null -> cursor-page-2, with page limit always 200.
      expect(requestedCursors).toEqual([null, "cursor-page-2"]);
    });
    expect(requestedLimits).toEqual(["200", "200"]);

    await user.click(screen.getByLabelText("\u7b5b\u9009\u4e25\u91cd\u7ea7\u522b"));
    await user.click(await screen.findByRole("option", { name: "\u4e25\u91cd" }));
    expect(screen.getAllByTestId("finding-item")).toHaveLength(1);
    expect(screen.getByText("\u7b2c\u4e8c\u9875\u4e25\u91cd\u53d1\u73b0")).toBeInTheDocument();
  });

  it("never presents a run read failure as an idle or empty review", async () => {
    const { utils, runId } = await renderReadyWorkspace();
    server.use(
      http.get("*/review-runs/:runId", () =>
        HttpResponse.json(
          {
            err_code: 5000,
            message: "review run temporarily unavailable",
            data: null,
            request_id: OWNER,
          },
          { status: 500 },
        ),
      ),
    );
    await refetchRunQuery(utils.queryClient, runId);

    expect(await screen.findByTestId("review-status-error")).toHaveTextContent("\u8bf7\u6c42\u5931\u8d25");
    expect(screen.queryByText(/\u5c1a\u672a\u8fd0\u884c\u9884\u5ba1/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("tab-findings"));
    expect(await screen.findByTestId("findings-error")).toHaveTextContent("\u8bf7\u6c42\u5931\u8d25");
    expect(screen.queryByTestId("findings-empty")).not.toBeInTheDocument();
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
  });

  it("keeps run loading distinct from a draft that never started a review", async () => {
    const { utils, runId } = await renderReadyWorkspace();
    const snapshot = (await (
      await fetch(`/review-runs/${runId}`)
    ).json()) as GetReviewRun200;
    const requestStarted = deferred();
    const releaseRequest = deferred();
    server.use(
      http.get("*/review-runs/:runId", async () => {
        requestStarted.resolve();
        await releaseRequest.promise;
        return HttpResponse.json(snapshot);
      }),
    );
    // fire-and-forget: the refetch suspended by deferred is the exact intermediate state under assertion; awaiting would deadlock.
    void refetchRunQuery(utils.queryClient, runId);

    await requestStarted.promise;
    // owner 2026-11-04 (zero precheck jitter): background refetch no longer inserts a loading block — flipping
    // isFetching during polling previously made the results area jump up and down. Cached content stays in place, and loading
    // appears only when there is truly no data (run===null); the invariant remains never regressing to "precheck not yet run".
    expect(screen.getByTestId("review-counts")).toBeInTheDocument();
    expect(screen.queryByTestId("review-status-loading")).not.toBeInTheDocument();
    expect(screen.queryByText(/\u5c1a\u672a\u8fd0\u884c\u9884\u5ba1/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("tab-findings"));
    // Background refetch targets the run query (cached data exists, isPending=false) — findings query is
    // not re-sent, and cached findings stay visible (stale-while-revalidate: no loading flash when data exists).
    // Invariant = never regress to the idle "precheck not yet run" presentation. Visibility crosses the fade-in animation window.
    await waitFor(() => {
      expect(screen.getByTestId("finding-item")).toBeVisible();
    });
    expect(screen.queryByTestId("findings-idle")).not.toBeInTheDocument();
    expect(screen.queryByTestId("findings-loading")).not.toBeInTheDocument();

    releaseRequest.resolve();
    fireEvent.click(screen.getByTestId("tab-overview"));
    await screen.findByTestId("review-counts");
    await waitFor(() => {
      expect(screen.getByTestId("review-counts")).toBeVisible();
    });
  });

  it("keeps the findings tab in a waiting state while the review is queued", async () => {
    const draftId = await createDraftWithSql("\u7b49\u5f85\u4e2d\u7684\u98ce\u9669\u53d1\u73b0");
    // Freeze every run read at queued: the wizard never observes a terminal
    // snapshot no matter how the fixture timers advance.
    server.use(
      http.get("*/review-runs/:runId", ({ params }) =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: frozenQueuedRun(String(params.runId)),
          request_id: OWNER,
        }),
      ),
    );

    renderWorkspace(draftId);
    expect(await screen.findByTestId("run-review")).toBeVisible();
    await revealEditor();
    fireEvent.click(screen.getByTestId("run-review"));
    expect(await screen.findByTestId("precheck-dialog")).toBeVisible();

    await waitFor(() => {
      expect(screen.getByTestId("precheck-progress-panel")).toBeVisible();
      expect(screen.getByTestId("precheck-progress-value")).toHaveTextContent("25%");
    });
    expect(screen.queryByTestId("tab-findings")).not.toBeInTheDocument();
    expect(screen.queryByTestId("findings-empty")).not.toBeInTheDocument();
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
    expect(screen.getByTestId("submit-draft")).toBeDisabled();
  });

  it("shows a retryable findings error instead of claiming no risks", async () => {
    // Initial fetch returns 500: query fires and fails the instant the run reaches terminal state — error state renders inline with retry.
    server.use(
      http.get("*/review-runs/:runId/findings", () =>
        HttpResponse.json(
          {
            err_code: 5000,
            message: "findings temporarily unavailable",
            data: null,
            request_id: OWNER,
          },
          { status: 500 },
        ),
      ),
    );
    await renderReadyWorkspace();
    fireEvent.click(screen.getByTestId("tab-findings"));

    expect(await screen.findByTestId("findings-error")).toHaveTextContent("\u8bf7\u6c42\u5931\u8d25");
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "\u91cd\u8bd5" })).toBeVisible();
    });
  });

  it.each(["failed", "partial", "blocked"] as const)(
    "does not claim no risks when a %s review has no findings",
    async (state) => {
      // freeze-then-precheck: freeze the terminal state from the very first tick (under the ephemeral flow, the state of a single Run
      // never regresses — ready->failed server behavior does not exist and monotonic fold rightly rejects it;
      // to assert terminal presentation, let the precheck observe only that terminal state from start to finish).
      const draftId = await createDraftWithSql(state + "\u6001\u65e0\u53d1\u73b0");
      server.use(
        http.get("*/review-runs/:runId", ({ params }) =>
          HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: frozenTerminalRun(String(params.runId), state),
            request_id: OWNER,
          }),
        ),
        http.get("*/review-runs/:runId/findings", () =>
          HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: { items: [], page: { next_cursor: null, has_more: false } },
            request_id: OWNER,
          }),
        ),
      );
      renderWorkspace(draftId);
      expect(await screen.findByTestId("run-review")).toBeVisible();
      await revealEditor();
      fireEvent.click(screen.getByTestId("run-review"));
      fireEvent.click(await screen.findByTestId("tab-findings", {}, { timeout: 4_000 }));

      const incomplete = await screen.findByTestId("findings-incomplete");
      expect(incomplete).toHaveTextContent("\u5ba1\u6838\u672a\u5b8c\u6574\u5b8c\u6210");
      expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
      expect(screen.getByTestId("submit-draft")).toBeDisabled();
    },
  );

  it("treats a flow.updated domain event as a submission blocker", async () => {
    await renderReadyWorkspace();
    expect(await screen.findByTestId("review-status")).toBeVisible();
    getReviewEventClient().ingest({
      specversion: "1.0",
      id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
      type: "io.yearning.v4.flow.updated",
      source: "yearning://control-plane",
      subject: `flows/${FIXTURE_FLOW_ID}`,
      time: "2026-08-30T00:00:00Z",
      sequence: 1,
      data: {
        resource_id: FIXTURE_FLOW_ID,
        action: "updated",
        aggregate_version: 2,
      },
    });
    await waitFor(() => {
      expect(screen.getByTestId("flow-updated-banner")).toBeVisible();
    });
    expect(screen.getByTestId("submit-draft")).toBeDisabled();
  });

  it("loses the ephemeral result on remount and requires a re-precheck (ephemeral Run is not persisted)", async () => {
    const { utils, draftId } = await renderReadyWorkspace();
    expect(screen.getByTestId("submit-draft")).toBeEnabled();
    utils.unmount();

    renderWorkspace(draftId);
    // The fresh page instance has no client-state run id: no precheck modal
    // or view-results trigger shows — the workspace honestly starts unreviewed.
    expect(await screen.findByTestId("run-review")).toBeVisible();
    expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("view-precheck-results")).not.toBeInTheDocument();
    expect(fixtureRunIds().length).toBeGreaterThan(0);
    expect(screen.queryByTestId("submit-draft")).not.toBeInTheDocument();
    expect(screen.queryByTestId("submission-readiness")).not.toBeInTheDocument();
    expect(screen.getByTestId("wizard-summary-precheck")).toBeVisible();
    // A fresh precheck restores the result through the same UI path.
    await precheckToReady();
    expect(screen.getByTestId("submit-draft")).toBeEnabled();
    // Two full precheck flows (including poll progression) can exceed the 5s default timeout in slow environments
    // (CI/containers + coverage, reproduced on CI 2026-11-04), so relax the timeout explicitly.
  }, 20_000);

  it("a stale HTTP snapshot never walks the phase backwards (out-of-order events do not regress state)", async () => {
    const { utils, runId } = await renderReadyWorkspace();
    await waitFor(() => {
      expect(screen.getByTestId("review-status").textContent).toContain("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574");
    });
    // Simulate an interleaved stale snapshot: the next run refetch answers
    // an older running state (e.g. a CDN/cacheaged response). The fold must
    // keep the presented terminal phase and the submission unlock.
    server.use(
      http.get("*/review-runs/:runId", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            ...frozenQueuedRun(runId),
            state: "running",
            started_at: "2026-08-30T00:00:01Z",
          },
          request_id: OWNER,
        }),
      ),
    );
    await refetchRunQuery(utils.queryClient, runId);
    // The monotonic fold keeps the presented phase terminal (no backwards
    // walk); the gate, however, re-evaluates against whatever snapshot the
    // query last returned, so submission stays conservatively locked until
    // the next fresh read reconciles — never a fake unlock.
    expect(screen.getByTestId("review-status").textContent).toContain("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574");
    // Terminal presentation sticks; submission side stays conservatively locked based on the last snapshot (gate not passed on running snapshot).
    await waitFor(() => {
      expect(screen.getByTestId("submit-draft")).toBeDisabled();
    });
  });

  it("survives a draft fetch failure with the shared error state", async () => {
    server.use(
      http.get("*/change-drafts/unknown-draft", () =>
        HttpResponse.json({
          err_code: 1002,
          message: "not found",
          data: null,
          request_id: OWNER,
          retryable: false,
        }),
      ),
    );
    renderWorkspace("unknown-draft");
    expect(await screen.findByRole("alert")).toBeVisible();
  });

  it("renders a bulk draft in the virtualized browser, never in the editor", async () => {
    // 2000 similar statements + one no-WHERE anomaly: the precheck digests
    // the bulk SQL into two shape groups and the anomaly blocks the gate.
    const create = (await (
      await fetch("/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u6279\u91cf\u9884\u68c0\u8349\u7a3f" }),
      })
    ).json()) as { data: { id: string } };
    const draftId = create.data.id;
    const parts: string[] = [];
    for (let i = 1; i <= 2000; i += 1) {
      parts.push(
        `UPDATE orders SET status = 'processed', updated_at = '2026-08-25' WHERE id = ${String(i)};\n`,
      );
    }
    // Deterministic single anomaly at ordinal 432.
    parts[431] = "UPDATE orders SET status = 'processed';\n";
    const bulkSql = parts.join("");
    await fetch(`/change-drafts/${draftId}/sql`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sql: bulkSql }),
    });

    renderWorkspace(draftId);
    // Owner ruling 2026-11-01 (manual SQL reveal gate was over-engineering): entering a draft
    // automatically loads the full text — the statement browser appears directly, the editor never mounts, and the precheck payload carries the text.
    expect(await screen.findByTestId("bulk-browser")).toBeVisible();
    expect(screen.queryByTestId("sql-editor")).not.toBeInTheDocument();
    // The precheck sends the editor SQL verbatim; the run mirrors its counts.
    fireEvent.click(screen.getByTestId("run-review"));
    await waitFor(() => {
      expect(screen.getByTestId("capacity-statements").textContent).toBe("2000");
      expect(screen.getByTestId("capacity-groups").textContent).toContain("2");
    }, { timeout: 4_000 });
    // Open the findings Tab only after reaching terminal state: the fixture advances queued->running->terminal via real timers
    // (~1.3s), so the synchronization point is the run's terminal presentation rather than the instant local digest.
    await waitFor(() => {
      expect(screen.getByTestId("review-status").textContent).toContain("\u5df2\u963b\u65ad");
    }, { timeout: 4_000 });
    expect(screen.getByTestId("submit-draft")).toBeDisabled();
    // The anomalous statement is a finding on its own fingerprint group —
    // visible outside the aggregate (single anomalies are never hidden by aggregation).
    fireEvent.click(screen.getByTestId("tab-findings"));
    const findings = await screen.findAllByTestId("finding-item", {}, { timeout: 4_000 });
    const anomalyFinding = findings.find((node) =>
      node.textContent.includes("\u65e0 WHERE \u6761\u4ef6\u7684\u6279\u91cf DML"),
    );
    expect(anomalyFinding).not.toBeUndefined();
    if (anomalyFinding === undefined) throw new Error("anomaly finding missing");
    expect(anomalyFinding.textContent).toContain("#432");
    expect(anomalyFinding.textContent).toContain("\u6307\u7eb9\u7ec4 #2");
  });

  it("requires the explicit submit confirmation and lands on the order detail (F6)", async () => {
    await renderReadyWorkspace();
    expect(screen.getByTestId("submit-draft")).toBeEnabled();

    // The dock click only opens the confirmation; no submission fires yet.
    fireEvent.click(screen.getByTestId("submit-draft"));
    expect(await screen.findByTestId("submit-confirm-dialog")).toBeVisible();
    expect(screen.getByTestId("submit-confirm-gate").textContent).toContain("\u5168\u90e8\u9636\u6bb5\u5ba1\u6838\u901a\u8fc7");

    // Confirming submits (POST /change-orders carries the editor SQL plus
    // the precheck run id) and navigates to the immutable order detail.
    fireEvent.click(screen.getByTestId("submit-confirm-accept"));
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page")).toBeVisible();
    });
    const order = (await (
      await fetch("/change-orders")
    ).json()) as { data: { items: Array<{ display_number: string }> } };
    expect(order.data.items).toHaveLength(1);
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-page").textContent).toContain(
        order.data.items[0]?.display_number ?? "",
      );
    });
  });

  it("keeps the confirm dialog open with the backend rejection on a failed submit (backend rejection has no fake success)", async () => {
    await renderReadyWorkspace();
    // Simulate a gate change between the dock's presentation and the
    // submission transaction: the backend answers 2013 through MSW.
    server.use(
      http.post("*/change-orders", () =>
        HttpResponse.json({
          err_code: 2013,
          message: "submission gate failed",
          data: null,
          request_id: OWNER,
          retryable: false,
        }),
      ),
    );
    fireEvent.click(screen.getByTestId("submit-draft"));
    expect(await screen.findByTestId("submit-confirm-dialog")).toBeVisible();
    fireEvent.click(screen.getByTestId("submit-confirm-accept"));
    const error = await screen.findByTestId("submit-confirm-error");
    expect(error.textContent.length).toBeGreaterThan(0);
    expect(screen.queryByTestId("order-detail-page")).toBeNull();
    // The draft stays unsubmitted — no fake success anywhere.
    expect(screen.getByTestId("submit-confirm-dialog")).toBeVisible();
  });
});
