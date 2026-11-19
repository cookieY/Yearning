import { expect, test, type Page } from "@playwright/test";
import { becomeIdentity, mockSession } from "./helpers/auth";

// FE-F7 acceptance gates (Approval Workspace), driven by the stateful order
// fixture over the production build with the shared MSW worker:
//   1. Opening/operating the approval page never creates a Review Run — the queue and the decision surface
//      only ever read the frozen submission review (R003);
//   2. Any rejection immediately rejects the whole order — a reviewer rejection terminates the whole
//      order and removes it from the queue (W003);
//   3. Concurrent conflict is recoverable — a racing approval surfaces the state-machine
//      rejection inside the dialog while the page converges on the real
//      aggregate state, never a fake success;
//   4. No transfer/add-signer/remove-signer entry — the frozen order offers no delegation surface
//      anywhere (W004).
//
// Identity role separation (§18.44): henry submits, reviewer-a holds the
// frozen approval slots. The reviewer legs switch identity in-world
// (logout + login, no reload — a reload would reset the mock world), so
// the submitted order survives across the role change; the backend keeps
// the authoritative frozen-actor checks (3001/3002).

const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";
const SQL_TEXT = "UPDATE orders SET status = 1 WHERE user_id = 42;";


/** One-page layout (owner ruling 2026-10-24): the summary card and its
 * submission dock are mounted from the start — nothing to walk to. */
async function gotoConfirmStep(page: Page): Promise<void> {
  await expect(page.getByTestId("wizard-summary")).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
    window.localStorage.setItem("yearning-mock-scenario", "review-ready");
  });
  await mockSession(page, "admin");
});

/** Submits a ready draft through the UI and lands on the order detail. */
async function submitOrder(page: Page, title: string): Promise<void> {
  await page.goto("/changes/new");
  await expect(page.getByTestId("changes-new-page")).toBeVisible();
  await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
  await page.getByTestId("draft-title-input").fill(title);
  await page.getByTestId("sql-editor").click();
  await page.keyboard.type(SQL_TEXT);
  await page.getByTestId("run-review").click();
  await expect(page.getByTestId("review-status")).toContainText("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574", {
    timeout: 8_000,
  });
  // Ephemeral precheck (RCP-20261101): precheck does not adopt a draft; only submission sends {sql, run_id} to
  // POST /change-orders.
  await expect(page.getByTestId("changes-new-page")).not.toHaveAttribute(
    "data-draft-id",
    /.+/,
  );
  await gotoConfirmStep(page);
  await page.getByTestId("submit-draft").click();
  await page.getByTestId("submit-confirm-accept").click();

  await expect(page.getByTestId("order-detail-page")).toBeVisible({ timeout: 8_000 });
}

test("opening and operating the approval workspace never creates a Review Run", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await submitOrder(page, "\u96f6\u91cd\u8dd1\u5ba1\u6279\u8349\u7a3f");
  await becomeIdentity(page, "reviewer-a");

  // From here on the user acts as a reviewer: every request must stay on
  // the frozen review reads — not one Review Run creation.
  const reviewRunPosts: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/review-runs")) {
      reviewRunPosts.push(request.url());
    }
  });

  // SPA navigation only: a full page.goto reload would reset the in-memory
  // mock world (documented F4 limitation §11.4).
  await page.getByRole("link", { name: "\u5de5\u5355\u5ba1\u6279" }).click();
  await expect(page.getByTestId("approval-queue-table")).toBeVisible();
  await page.getByTestId("approval-queue-row").first().click();
  await expect(page.getByTestId("approval-decision-card")).toBeVisible();
  // The frozen review card renders from the submission snapshot.
  await expect(page.getByTestId("frozen-review-card")).toContainText(
    "\u4e0d\u4f1a\u91cd\u65b0\u8fd0\u884cAI\u5ba1\u6838",
  );

  await page.getByTestId("approval-approve").click();
  await page.getByTestId("approval-decision-confirm").click();
  await expect(page.getByTestId("order-detail-page")).toContainText("\u7b49\u5f85\u6267\u884c", {
    timeout: 8_000,
  });
  expect(reviewRunPosts).toEqual([]);
});

test("any rejection immediately rejects the whole order and empties the queue", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await submitOrder(page, "\u5ba1\u6279\u62d2\u7edd\u8349\u7a3f");
  await becomeIdentity(page, "reviewer-a");

  await page.getByRole("link", { name: "\u5de5\u5355\u5ba1\u6279" }).click();
  await expect(page.getByTestId("approval-queue-table")).toBeVisible();
  await page.getByTestId("approval-queue-row").first().click();
  await page.getByTestId("approval-reject").click();
  const dialog = page.getByTestId("approval-decision-dialog");
  await expect(dialog).toContainText("\u7acb\u5373\u62d2\u7edd\u6574\u5355");
  await page.getByTestId("approval-decision-comment").fill("\u8bed\u53e5\u98ce\u9669\u4e0d\u53ef\u63a5\u53d7");
  await page.getByTestId("approval-decision-confirm").click();
  await expect(page.getByTestId("order-detail-page")).toContainText("\u5df2\u62d2\u7edd", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("order-timeline")).toContainText("\u5ba1\u6279\u62d2\u7edd", {
    timeout: 8_000,
  });

  // §18.58 owner ruling: the rejected order STAYS in the queue (the user
  // was its frozen approver) — the row now carries the decided outcome.
  await page.getByRole("link", { name: "\u5de5\u5355\u5ba1\u6279" }).click();
  await expect(page.getByTestId("approval-queue-table")).toBeVisible({ timeout: 8_000 });
  const rejectedRow = page
    .getByTestId("approval-queue-row")
    .filter({ hasText: "\u5ba1\u6279\u62d2\u7edd\u8349\u7a3f" });
  await expect(rejectedRow).toHaveCount(1);
  await expect(rejectedRow).toContainText("\u5df2\u62d2\u7edd");
  // §18.60: I have already given a rejection decision -> approved/decided status = Yes.
  await expect(rejectedRow).toContainText("\u662f");
});

test("a concurrent peer decision is recoverable: the UI converges on the real state", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await submitOrder(page, "\u5e76\u53d1\u5ba1\u6279\u8349\u7a3f");
  const orderId = page.url().split("/").at(-1) ?? "";

  // The submitter's detail view carries no decision surface anymore; the
  // reviewer logs in and lands back on the same order (redirect-back).
  await becomeIdentity(page, "reviewer-a");
  await expect(page.getByTestId("approval-decision-card")).toBeVisible({ timeout: 8_000 });

  await page.getByTestId("approval-approve").click();
  await expect(page.getByTestId("approval-decision-dialog")).toBeVisible();
  // While the dialog is open, a peer reviewer approves from another tab:
  // the fixture's winning decision advances the order to execution-pending.
  const raced = await page.evaluate(async (id) => {
    const response = await fetch(`/change-orders/${id}/approval-decisions`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "If-Match": '"1"' },
      body: JSON.stringify({ decision: "approve", comment: "\u5e76\u53d1\u5ba1\u6279\u62a2\u5148\u6210\u529f" }),
    });
    return (await response.json()) as { err_code: number };
  }, orderId);
  expect(raced.err_code).toBe(0);

  // The decision event reaches the page; the UI converges on the true
  // aggregate state — the decision surface removes itself (nothing left to
  // decide) and no success state is ever faked for the interrupted dialog.
  await expect(page.getByTestId("order-detail-page")).toContainText("\u7b49\u5f85\u6267\u884c", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("approval-decision-dialog")).toHaveCount(0);
  await expect(page.getByTestId("approval-decision-card")).toHaveCount(0);
  // The step shows its approved decision in the frozen stages card.
  await expect(page.getByTestId("stage-step-1")).toContainText("\u5df2\u901a\u8fc7", {
    timeout: 8_000,
  });
  // Back on the queue the order no longer awaits anyone's decision.
  await page.getByRole("link", { name: "\u5de5\u5355\u5ba1\u6279" }).click();
  await expect(
    page.getByTestId("approval-queue-row").filter({ hasText: "\u5e76\u53d1\u5ba1\u6279\u8349\u7a3f" }),
  ).toHaveCount(0);
});

test("the approval surface offers no transfer, add-signer or remove-signer entry", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await submitOrder(page, "\u51bb\u7ed3\u5de5\u5355\u8349\u7a3f");
  await becomeIdentity(page, "reviewer-a");

  await page.getByRole("link", { name: "\u5de5\u5355\u5ba1\u6279" }).click();
  await expect(page.getByTestId("approval-queue-table")).toBeVisible();
  const queueText = await page.getByTestId("approval-queue-page").textContent();
  for (const forbidden of ["\u8f6c\u4ea4", "\u52a0\u7b7e", "\u51cf\u7b7e", "\u6539\u6d3e"]) {
    expect(queueText).not.toContain(forbidden);
  }

  await page.getByTestId("approval-queue-row").first().click();
  await expect(page.getByTestId("approval-decision-card")).toBeVisible();
  const detailText = await page.getByTestId("order-detail-page").textContent();
  for (const forbidden of ["\u8f6c\u4ea4", "\u52a0\u7b7e", "\u51cf\u7b7e", "\u6539\u6d3e"]) {
    expect(detailText).not.toContain(forbidden);
  }
});
