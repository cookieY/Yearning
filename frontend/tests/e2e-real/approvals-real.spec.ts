import { expect, test, type Page } from "@playwright/test";
import { statePath } from "./helpers/real-auth";

// FE-F12 real-backend e2e — the approver's queue over real data. One pending
// order is created through the real API from the submitter's session (draft
// → SQL → ephemeral AI review via POST /review-runs → POST /change-orders,
// no decision), so the queue has a deterministic row; the decision then
// round-trips the CSRF-protected POST through the same-origin proxy.
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

/** Creates one stage_approval_active order via the real API in the page. */
async function ensurePendingOrder(page: Page): Promise<{ orderId: string; displayNumber: string }> {
  const made = await page.evaluate(async () => {
    const csrf = document.cookie.split("; ")
      .map((row) => row.split("="))
      .find(([name]) => name === "yearning_csrf");
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (csrf !== undefined) headers["X-CSRF-Token"] = csrf[1] ?? "";
    type Envelope = { data?: Record<string, unknown>; err_code?: number };
    const call = async (method: string, path: string, body?: unknown, ifMatch?: number): Promise<Envelope> => {
      const response = await fetch(path, {
        method,
        headers: {
          ...headers,
          ...(ifMatch !== undefined ? { "If-Match": `"${String(ifMatch)}"` } : {}),
          ...(method === "POST" ? { "Idempotency-Key": crypto.randomUUID() } : {}),
        },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      return (await response.json()) as Envelope;
    };
    const reviewSql = "create table if not exists f12_e2e_pending (id bigint primary key);";
    const flowList = (await (await fetch("/api/v4/users/me/flows?flow_type=change_review")).json()) as {
      data?: { items?: Array<{ id: string }> };
    };
    const flowId = flowList.data?.items?.[0]?.id;
    if (flowId === undefined) throw new Error("no change_review flow visible to the submitter");
    const draft = await call("POST", "/api/v4/change-drafts", {
      flow_id: flowId, title: `f12 e2e approval ${Date.now()}`, description: "real e2e pending order",
    });
    const draftId = draft.data?.id as string | undefined;
    const draftVersion = draft.data?.version as number | undefined;
    if (draftId === undefined) throw new Error(`draft create failed: ${draft.err_code}`);
    await call("PUT", `/api/v4/change-drafts/${draftId}/sql`, {
      sql: reviewSql,
    }, draftVersion);
    // Ephemeral precheck (RCP-20261101): the run carries the editor SQL and
    // persists nothing; submission (POST /change-orders) requires the same
    // SQL byte-identical and materializes the run.
    const started = await call("POST", "/api/v4/review-runs", {
      flow_id: flowId, draft_id: draftId, sql: reviewSql,
    });
    const runId = started.data?.run_id as string | undefined;
    if (runId === undefined) throw new Error(`review run failed: ${started.err_code}`);
    let ready = false;
    for (let i = 0; i < 180; i += 1) {
      await new Promise((resolve) => setTimeout(resolve, 1000));
      const view = (await (await fetch(`/api/v4/review-runs/${runId}`)).json()) as {
        data?: { state?: string; gate?: { passed?: boolean } | null };
      };
      const state = view.data?.state;
      if (state === undefined || state === "queued" || state === "running") continue;
      if (state === "ready" && view.data?.gate?.passed === true) ready = true;
      break;
    }
    if (!ready) throw new Error("review run did not reach a passing ready state");
    const submitted = await call("POST", "/api/v4/change-orders", {
      flow_id: flowId, draft_id: draftId, sql: reviewSql, run_id: runId,
    });
    return {
      orderId: submitted.data?.id,
      displayNumber: submitted.data?.display_number,
      err: submitted.err_code,
    };
  });
  const orderId: unknown = made.orderId;
  const displayNumber: unknown = made.displayNumber;
  if (typeof orderId !== "string" || typeof displayNumber !== "string") {
    throw new Error(`pending order not created: ${JSON.stringify(made)}`);
  }
  return { orderId, displayNumber };
}

test.describe("approver queue", () => {
  test.use({ storageState: statePath("approver") });

  test("queue lists the pending order and one decision round-trips", async ({ page }) => {
    // Seed one deterministic pending order from the submitter's session,
    // then switch the browser to the approver's storage state.
    const submitterContext = await page.context().browser()?.newContext({
      storageState: statePath("submitter"),
    });
    if (submitterContext === undefined) throw new Error("no browser for the submitter context");
    let pending: { orderId: string; displayNumber: string };
    try {
      const submitterPage = await submitterContext.newPage();
      await submitterPage.addInitScript(() => {
        window.localStorage.setItem("yearning-locale", "zh-CN");
      });
      await submitterPage.goto("/workspace");
      await expect(submittersWorkspace(submitterPage)).toBeVisible();
      pending = await ensurePendingOrder(submitterPage);
    } finally {
      await submitterContext.close();
    }

    await page.goto("/approvals/changes");
    const table = page.getByTestId("approval-queue-table");
    await expect(table).toBeVisible({ timeout: 20_000 });
    // §18.58 keeps already-decided rows in the queue: target THIS run's
    // order by its display number instead of the first row.
    const row = page.getByTestId("approval-queue-row").filter({ hasText: pending.displayNumber });
    await expect(row).toBeVisible({ timeout: 20_000 });
    await row.getByTestId(/^open-approval-/).click();
    await expect(page).toHaveURL(/\/changes\/orders\/[0-9a-f-]{36}$/);

    const decisionCard = page.getByTestId("approval-decision-card");
    await expect(decisionCard).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("approval-approve").click();
    await expect(page.getByTestId("approval-decision-dialog")).toBeVisible();
    await page.getByTestId("approval-decision-comment").fill("f12 real e2e approval");
    await page.getByTestId("approval-decision-confirm").click();
    await expect(page.getByTestId("approval-decision-dialog")).toBeHidden({ timeout: 20_000 });
    await expect(page.getByTestId("approval-decision-error")).toBeHidden();
    // A dialog that closes while swallowing a failed POST would pass the two
    // assertions above — re-read the order through the API and require the
    // decision to have landed.
    const decided = await page.evaluate(async (orderId: string) => {
      const envelope = (await (await fetch(`/api/v4/change-orders/${orderId}`)).json()) as {
        data?: { state?: string };
      };
      return envelope.data?.state ?? null;
    }, pending.orderId);
    expect(decided, "the approval decision landed server-side").not.toBe("stage_approval_active");
  });
});

function submittersWorkspace(page: Page) {
  return page.getByTestId("workspace-page");
}
