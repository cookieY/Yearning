import { expect, test, type Page } from "@playwright/test";
import { becomeIdentity, mockSession, setMockAuthBehavior, setMockIdentity } from "./helpers/auth";

// §18.44 acceptance (owner ruling 2026-10-13 on issue #1): when the
// submitter and the reviewer are different people, every acting surface
// keys on identity membership. The chain below is the machine-checked
// version of the ruling — henry submits and never sees approval actions;
// reviewer-a approves from the queue; the stage advances for the submitter;
// only executor-a sees the execution surface. Identity switches happen
// in-world (logout + login, no reload) so the submitted order survives.

const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";
const SQL_TEXT = "UPDATE orders SET status = 1 WHERE user_id = 42;";
const TITLE = "\u8eab\u4efd\u5206\u79bb\u9a8c\u6536\u8349\u7a3f";

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
    window.localStorage.setItem("yearning-mock-scenario", "review-ready");
  });
  await mockSession(page, "admin");
});

/** Submits a ready draft through the UI and stays on the detail page. */
async function submitOrder(page: Page): Promise<void> {
  await page.goto("/changes/new");
  await expect(page.getByTestId("changes-new-page")).toBeVisible();
  await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
  await page.getByTestId("draft-title-input").fill(TITLE);
  await page.getByTestId("sql-editor").click();
  await page.keyboard.type(SQL_TEXT);
  await page.getByTestId("run-review").click();
  await expect(page.getByTestId("review-status")).toContainText("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574", {
    timeout: 8_000,
  });
  // Ephemeral precheck (RCP-20261101): precheck does not adopt a draft; no draft id appears.
  await expect(page.getByTestId("changes-new-page")).not.toHaveAttribute(
    "data-draft-id",
    /.+/,
  );
  // One-page layout: the summary card is always mounted.
  await expect(page.getByTestId("wizard-summary")).toBeVisible();
  await page.getByTestId("submit-draft").click();
  await page.getByTestId("submit-confirm-accept").click();
  await expect(page.getByTestId("order-detail-page")).toBeVisible({ timeout: 8_000 });
}

test("the submitter sees no approval surface; the reviewer approves; the stage advances", async ({
  page,
}) => {
  test.setTimeout(60_000);
  await submitOrder(page);

  // Submitter view: lifecycle buttons only — no decision card.
  await expect(page.getByTestId("withdraw-order")).toBeVisible();
  await expect(page.getByTestId("void-order")).toBeVisible();
  await expect(page.getByTestId("approval-decision-card")).toHaveCount(0);

  // Reviewer view: login redirects back to the pre-logout order detail,
  // where the decision card renders and the lifecycle buttons disappear.
  // The queue holds the order too (frozen-actor relation).
  await becomeIdentity(page, "reviewer-a");
  await expect(page.getByTestId("approval-decision-card")).toBeVisible({
    timeout: 8_000,
  });
  await expect(page.getByTestId("withdraw-order")).toHaveCount(0);
  await expect(page.getByTestId("void-order")).toHaveCount(0);
  await page.getByRole("link", { name: "\u5de5\u5355\u5ba1\u6279" }).click();
  await expect(page.getByTestId("approval-queue-table")).toBeVisible();
  await expect(page.getByTestId("approval-queue-row").filter({ hasText: TITLE })).toHaveCount(1);
  await page.getByTestId("approval-queue-row").filter({ hasText: TITLE }).first().click();
  await expect(page.getByTestId("order-detail-page")).toBeVisible();

  await page.getByTestId("approval-approve").click();
  await page.getByTestId("approval-decision-confirm").click();
  await expect(page.getByTestId("order-detail-page")).toContainText("\u7b49\u5f85\u6267\u884c", {
    timeout: 8_000,
  });

  // Back as the submitter (redirect-back lands on the same detail): the
  // stage advanced, still no decision surface and no execution surface —
  // that belongs to executor-a.
  await becomeIdentity(page, "henry");
  await expect(page.getByTestId("order-detail-page")).toContainText("\u7b49\u5f85\u6267\u884c", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("approval-decision-card")).toHaveCount(0);
  await expect(page.getByTestId("execution-action-card")).toHaveCount(0);

  // Executor view: the execution surface finally renders on the same detail.
  await becomeIdentity(page, "executor-a");
  await expect(page.getByTestId("execution-action-card")).toBeVisible({ timeout: 8_000 });
  await expect(page.getByTestId("approval-decision-card")).toHaveCount(0);

  // The My Orders tab stays submitter-scoped (UI-spec §5.2): the executor
  // relates to the order but never submitted it, so the tab renders empty.
  await page.getByRole("link", { name: "\u6211\u7684\u5de5\u5355" }).click();
  await expect(page.getByTestId("mine-page")).toBeVisible();
  await expect(page.getByText("\u8fd8\u6ca1\u6709\u5df2\u63d0\u4ea4\u7684\u5de5\u5355\u3002")).toBeVisible();
});

test("query access approvals follow the same separation: only the reviewer sees the pending request", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-scenario", "query-approval");
  });
  // The builtin admin identity has NO query read face (authorization-policy
  // admin_is_not_business_override) — this leg must run as a regular
  // session, not the beforeEach admin behavior.
  await setMockAuthBehavior(page, "default");

  // henry (the requester's peer, not the actor) sees an empty queue.
  await page.goto("/approvals/query-access");
  await expect(page.getByTestId("query-access-approvals")).toBeVisible();
  await expect(page.getByTestId("query-access-pending-qar-fixture-pending")).toHaveCount(0);

  // reviewer-a holds the frozen approval slot: the pending row appears.
  // (Nothing user-created lives in this world, so a full navigation is
  // safe — the scenario re-seeds the pending request deterministically.)
  await becomeIdentity(page, "reviewer-a");
  await page.goto("/approvals/query-access");
  await expect(page.getByTestId("query-access-pending-qar-fixture-pending")).toBeVisible({
    timeout: 8_000,
  });
});

test("the default world seeds both pending queues for the reviewer (§18.44 follow-up)", async ({
  page,
}) => {
  test.setTimeout(30_000);
  // Default world, regular session (the builtin admin has no query read
  // face). No scenario override: the demo layer lives in the base world.
  await setMockAuthBehavior(page, "default");
  await setMockIdentity(page, "reviewer-a");

  // Order Approval: the seeded henry-submitted orders await reviewer-a's decision.
  await page.goto("/approvals/changes");
  await expect(page.getByTestId("approval-queue-table")).toBeVisible();
  // The gallery intentionally fills page 1; the default demo rows live in the
  // same client-side queue and remain discoverable through its toolbar.
  await page.getByTestId("approval-queue-search").fill("\u8ba2\u5355\u8868\u65b0\u589e\u5f52\u6863\u72b6\u6001\u5b57\u6bb5");
  const seededRow = page
    .getByTestId("approval-queue-row")
    .filter({ hasText: "\u8ba2\u5355\u8868\u65b0\u589e\u5f52\u6863\u72b6\u6001\u5b57\u6bb5" });
  await expect(seededRow).toHaveCount(1);
  await seededRow.click();
  await expect(page.getByTestId("approval-decision-card")).toBeVisible({ timeout: 8_000 });

  // Query Approval: the default world now carries pending access requests too.
  await page.goto("/approvals/query-access");
  await expect(page.getByTestId("query-access-approvals")).toBeVisible();
  await expect(page.getByTestId("query-access-pending-qar-demo-mysql")).toBeVisible({
    timeout: 8_000,
  });
  await expect(page.getByTestId("query-access-pending-qar-demo-pg")).toBeVisible();
});
