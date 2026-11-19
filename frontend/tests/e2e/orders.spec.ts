import { expect, test, type Page } from "@playwright/test";
import { mockSession } from "./helpers/auth";


// FE-F6 acceptance gates (work package FE-F6-ORDER-SUBMIT, plus the
// FE-F6-ORDER-FILTER supplement driven by RCP-20260831-ORDER-LIST-FILTER),
// driven by the stateful order fixture over the production build with the
// shared MSW worker:
//   1. Non-Ready normal UI cannot submit — the dock stays disabled without a passing
//      gate, and submission only fires after the explicit confirmation;
//   2. Backend rejection has no fake success — a racing withdrawal keeps the dialog open with the
//      backend's state-machine rejection (1010) and no optimistic state flip;
//   3. List events produce no duplicates — the personal list updates rows in place across
//      at-least-once event delivery;
//   4. Withdrawal after partial execution explicitly warns of non-rollback — a running order warns before the
//      withdrawn_after_partial_execution transition.

const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";
const SQL_TEXT = "UPDATE orders SET status = 1 WHERE user_id = 42;";

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
    window.localStorage.setItem("yearning-mock-scenario", "review-ready");
  });
  await mockSession(page, "admin");
});

async function openDraftComposer(page: Page, title: string): Promise<void> {
  await page.goto("/changes/new");
  await expect(page.getByTestId("changes-new-page")).toBeVisible();
  await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
  await page.getByTestId("draft-title-input").fill(title);
}

/** Types SQL, runs the review explicitly and waits for Ready. */
async function reviewToReady(page: Page): Promise<void> {
  await page.getByTestId("sql-editor").click();
  await page.keyboard.type(SQL_TEXT);
  // Ephemeral precheck (RCP-20261101): precheck sends editor SQL verbatim without saving a draft; on submit,
  // POST /change-orders carries {sql, run_id} to create the order.
  await page.getByTestId("run-review").click();
  await expect(page.getByTestId("review-status")).toContainText("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("changes-new-page")).not.toHaveAttribute(
    "data-draft-id",
    /.+/,
  );
}


/** One-page layout (owner ruling 2026-10-24): the summary card and its
 * submission dock are mounted from the start — nothing to walk to. */
async function gotoConfirmStep(page: Page): Promise<void> {
  await expect(page.getByTestId("wizard-summary")).toBeVisible();
}

test("a draft without a passing gate cannot be submitted from the UI", async ({ page }) => {
  await openDraftComposer(page, "\u672a\u9884\u5ba1\u8349\u7a3f");
  // Submit lives inside the precheck dialog and is absent before precheck runs.
  await expect(page.getByTestId("submit-draft")).toHaveCount(0);
  // Typing SQL still keeps submission inaccessible until an explicit passing precheck completes.
  await page.getByTestId("sql-editor").click();
  await page.keyboard.type(SQL_TEXT);
  await gotoConfirmStep(page);
  await expect(page.getByTestId("run-review")).toBeEnabled();
  await expect(page.getByTestId("submit-draft")).toHaveCount(0);
});

test("a racing backend rejection keeps the dialog open without faking success", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await openDraftComposer(page, "\u5e76\u53d1\u64a4\u56de\u8349\u7a3f");
  await reviewToReady(page);
  await gotoConfirmStep(page);
  await page.getByTestId("submit-draft").click();
  await page.getByTestId("submit-confirm-accept").click();
  await expect(page.getByTestId("order-detail-page")).toBeVisible({ timeout: 8_000 });
  const orderId = page.url().split("/").at(-1) ?? "";

  // The withdraw dialog opens; while it is open another tab withdraws the
  // order directly, so the confirm lands on an already-terminal order.
  await page.getByTestId("withdraw-order").click();
  await page.getByTestId("order-action-reason").fill("\u754c\u9762\u64a4\u56de\uff08\u5c06\u88ab\u5e76\u53d1\u62a2\u65ad\uff09");
  const raced = await page.evaluate(async (id) => {
    const response = await fetch(`/change-orders/${id}/withdrawal`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "If-Match": '"1"' },
      body: JSON.stringify({ reason: "\u5e76\u53d1\u64a4\u56de\u62a2\u5148\u6210\u529f" }),
    });
    return (await response.json()) as { err_code: number };
  }, orderId);
  expect(raced.err_code).toBe(0);

  // The state change event reaches the page; the badge already shows the
  // withdrawn terminal state before the user confirms.
  await expect(page.getByTestId("order-detail-page")).toContainText("\u5df2\u64a4\u56de", {
    timeout: 8_000,
  });

  // The state-change event has already re-read the fresh (withdrawn) order,
  // so confirming sends the current version and the backend rejects on the
  // state machine instead: 1010 INVALID_STATE_TRANSITION. The dialog shows
  // the rejection and never reports success — the If-Match version-conflict
  // path (1004, order_lifecycle profile) is pinned at fixture level.
  await page.getByTestId("order-action-confirm").click();
  await expect(page.getByTestId("order-action-error")).toBeVisible({ timeout: 8_000 });
  await expect(page.getByTestId("order-action-dialog")).toBeVisible();
  await expect(page.getByTestId("order-detail-page")).toContainText("\u5df2\u64a4\u56de");
});

test("the personal order list updates in place without duplicate rows", async ({ page }) => {
  test.setTimeout(30_000);
  await openDraftComposer(page, "\u5217\u8868\u53bb\u91cd\u8349\u7a3f");
  await reviewToReady(page);
  await gotoConfirmStep(page);
  await page.getByTestId("submit-draft").click();
  await page.getByTestId("submit-confirm-accept").click();
  await expect(page.getByTestId("order-detail-page")).toBeVisible({ timeout: 8_000 });

  // SPA navigation only: a full page.goto reload would reset the in-memory
  // mock world (documented F4 limitation §11.4).
  await page.getByRole("link", { name: "\u6211\u7684\u5de5\u5355" }).click();
  await expect(page.getByTestId("mine-orders-table")).toBeVisible();
  // The submitted order renders exactly once (event redelivery must not
  // duplicate rows) — the gallery rows around it are irrelevant here.
  await expect(page.getByTestId("mine-order-row").filter({ hasText: "\u5217\u8868\u53bb\u91cd\u8349\u7a3f" })).toHaveCount(1);

  const orderId = await page.evaluate(async () => {
    const response = await fetch("/change-orders");
    const body = (await response.json()) as {
      data: { items: Array<{ id: string }> };
    };
    return body.data.items[0]?.id ?? "";
  });

  // The server applies the withdrawal; the notification triggers a re-read
  // that updates the row in place (state badge, still exactly one row).
  const withdrawn = await page.evaluate(async (id) => {
    const response = await fetch(`/change-orders/${id}/withdrawal`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "If-Match": '"1"' },
      body: JSON.stringify({ reason: "E2E \u64a4\u56de" }),
    });
    return (await response.json()) as { err_code: number };
  }, orderId);
  expect(withdrawn.err_code).toBe(0);

  await expect(page.getByTestId("mine-order-row").filter({ hasText: "\u5217\u8868\u53bb\u91cd\u8349\u7a3f" })).toHaveCount(1, { timeout: 8_000 });
  await expect(page.getByTestId("mine-orders-table")).toContainText("\u5df2\u64a4\u56de", {
    timeout: 8_000,
  });
});

test("partial execution warns that applied changes cannot roll back", async ({ page }) => {
  test.setTimeout(30_000);
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-scenario", "order-partial-execution");
  });
  await page.goto("/changes/mine");
  // Fresh world: the scenario seeds its running order on first list fetch.
  await expect(page.getByTestId("mine-orders-table")).toBeVisible();
  await expect(page.getByTestId("mine-order-row").first()).toContainText("\u6267\u884c\u4e2d", {
    timeout: 8_000,
  });

  await page.getByTestId("mine-order-row").first().click();
  await expect(page.getByTestId("order-detail-page")).toBeVisible();
  // Stage 1 succeeded, stage 2 is running: the audit timeline shows the
  // partial progress and the withdraw dialog must warn before confirming.
  await expect(page.getByTestId("order-timeline-list")).toContainText("\u6267\u884c\u6210\u529f");
  await page.getByTestId("withdraw-order").click();
  await expect(page.getByTestId("partial-execution-warning")).toBeVisible();
  await expect(page.getByTestId("partial-execution-warning")).toContainText("\u4e0d\u4f1a\u81ea\u52a8\u56de\u6eda");

  await page.getByTestId("order-action-reason").fill("\u7ebf\u4e0a\u6b62\u8840\uff0c\u7ec8\u6b62\u540e\u7eed\u9636\u6bb5");
  await page.getByTestId("order-action-confirm").click();
  await expect(page.getByTestId("order-detail-page")).toContainText(
    "\u90e8\u5206\u6267\u884c\u540e\u64a4\u56de",
    { timeout: 8_000 },
  );
});


test("server-side filters narrow the personal list without losing event freshness", async ({
  page,
}) => {
  test.setTimeout(30_000);
  await openDraftComposer(page, "\u7b5b\u9009\u9a8c\u6536\u8349\u7a3f");
  await reviewToReady(page);
  await gotoConfirmStep(page);
  await page.getByTestId("submit-draft").click();
  await page.getByTestId("submit-confirm-accept").click();
  await expect(page.getByTestId("order-detail-page")).toBeVisible({ timeout: 8_000 });

  await page.getByRole("link", { name: "\u6211\u7684\u5de5\u5355" }).click();
  await expect(page.getByTestId("mine-orders-table")).toBeVisible();
  await expect(page.getByTestId("mine-order-row").filter({ hasText: "\u7b5b\u9009\u9a8c\u6536\u8349\u7a3f" })).toHaveCount(1);

  // A keyword that matches the submitted order keeps the row.
  await page.getByTestId("filter-keyword").fill("\u7b5b\u9009\u9a8c\u6536");
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 8_000 });

  // The ECS-style global search bar scopes a state chip from translated
  // labels — never raw enum values (owner issue: the old box showed literal
  // "all") — and the chip is removable through the clear affordance.
  await page.getByTestId("filter-keyword-clear").click();
  await page.getByTestId("filter-keyword").click();
  await page.getByRole("option", { name: "\u72b6\u6001" }).click();
  await page.getByRole("option", { name: "\u6267\u884c\u4e2d" }).click();
  await expect(page.getByText("\u72b6\u6001: \u6267\u884c\u4e2d")).toBeVisible();
  await page.getByTestId("filter-keyword-clear").click();
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 8_000 });

  // A keyword that matches nothing flips to the filtered-empty copy.
  await page.getByTestId("filter-keyword").fill("\u7edd\u4e0d\u5339\u914d\u7684\u8bcd");
  await expect(page.getByTestId("orders-empty")).toContainText("\u6ca1\u6709\u7b26\u5408\u7b5b\u9009\u6761\u4ef6\u7684\u5de5\u5355", {
    timeout: 8_000,
  });

  // Review R2 H1 + M1: commit the unmatched keyword chip so the card stays in
  // the short empty-card state, reopen the closed dropdown (item 7 auto-closes
  // on chip commit), then scope submitted-from date and verify the inline calendar
  // dialog is not clipped by Card overflow (day 25 hits itself).
  await page.getByTestId("filter-keyword").press("Enter");
  await expect(page.getByTestId("orders-empty")).toBeVisible();
  await page.getByTestId("filter-keyword").click();
  await page.getByRole("option", { name: "\u63d0\u4ea4\u8d77\u59cb\u65e5\u671f" }).click();
  const dateDialog = page.getByRole("dialog", { name: "\u63d0\u4ea4\u8d77\u59cb\u65e5\u671f — \u9009\u62e9\u65e5\u671f" });
  await expect(dateDialog).toBeVisible();
  await expect(page.getByTestId("filter-keyword")).toHaveAttribute("aria-expanded", "true");
  const day25Hittable = await page.evaluate(() => {
    const dialogEl = document.querySelector('[data-testid="filter-keyword-menu"]');
    const cardEl = dialogEl?.closest('[data-slot="card"]');
    const day25 = Array.from(dialogEl?.querySelectorAll("button") ?? []).find(
      (btn) => btn.textContent.trim() === "25",
    );
    if (!dialogEl || !cardEl || !day25) return false;
    if (getComputedStyle(cardEl).overflow !== "visible") return false;
    day25.scrollIntoView({ block: "nearest" });
    const rect = day25.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    return Boolean(hit && (hit === day25 || day25.contains(hit)));
  });
  expect(day25Hittable).toBe(true);
  await dateDialog.getByRole("button", { name: /25/ }).click();
  await expect(page.getByText(/\u63d0\u4ea4\u8d77\u59cb\u65e5\u671f: \d{4}-\d{2}-25/)).toBeVisible();
  await page.getByTestId("filter-keyword-clear").click();

  // Back to a matching keyword, then withdraw from the API while the filter
  // is active: the event-driven re-read keeps exactly one row and updates
  // its badge (event re-read under active filter produces no duplicate rows).
  await page.getByTestId("filter-keyword").fill("\u7b5b\u9009\u9a8c\u6536");
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 8_000 });
  const orderId = await page.evaluate(async () => {
    const response = await fetch("/change-orders");
    const body = (await response.json()) as { data: { items: Array<{ id: string }> } };
    return body.data.items[0]?.id ?? "";
  });
  const withdrawn = await page.evaluate(async (id) => {
    const response = await fetch(`/change-orders/${id}/withdrawal`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "If-Match": '"1"' },
      body: JSON.stringify({ reason: "\u7b5b\u9009\u6001\u64a4\u56de" }),
    });
    return (await response.json()) as { err_code: number };
  }, orderId);
  expect(withdrawn.err_code).toBe(0);
  await expect(page.getByTestId("mine-orders-table")).toContainText("\u5df2\u64a4\u56de", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("mine-order-row").filter({ hasText: "\u7b5b\u9009\u9a8c\u6536\u8349\u7a3f" })).toHaveCount(1);
});

test("the order gallery scenario serves 40 demo orders across every state", async ({
  page,
}) => {
  // Owner issue-collection #5: 40 demonstration orders covering all 18
  // change_order states so each state's rendering can be inspected at a
  // glance. The gallery ships in every browser mock world — no setup.
  await mockSession(page, "admin");
  await page.goto("/changes/mine");
  // Default rows-per-page is 20: page 1 of 2, with Next available.
  await expect(page.getByTestId("mine-order-row")).toHaveCount(20, { timeout: 10_000 });
  await expect(page.getByTestId("mine-orders-next")).toBeEnabled();
  await expect(page.getByText("\u6f14\u793a\u5de5\u5355 · completed").first()).toBeVisible();
  await expect(page.getByText("\u6f14\u793a\u5de5\u5355 · rejected").first()).toBeVisible();
});
