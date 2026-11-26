import { expect, test } from "@playwright/test";
import { statePath } from "./helpers/real-auth";

// FE-F12 real-backend e2e — the submitter's order surfaces over real data:
// the seeded chains (scripts/integration/f12_seed.py + the contract suite)
// leave YR-numbered orders behind, so /changes/mine lists rows and the
// detail page renders the real timeline.
test.use({ storageState: statePath("submitter") });

const STATE_OPTION_LABELS: Record<string, string> = {
  submitted: "已提交",
  stage_approval_active: "阶段审批中",
  stage_execution_pending: "等待执行",
  scheduled: "已排期",
  running: "执行中",
  completed: "已完成",
  rejected: "已拒绝",
  withdrawn: "已撤回",
};

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

test("submitter lists real orders and opens one detail", { tag: "@ai-real" }, async ({ page }) => {
  await page.goto("/changes/mine");
  const table = page.getByTestId("mine-orders-table");
  await expect(table).toBeVisible({ timeout: 20_000 });
  // The stack's chain orders carry contract display numbers YR-YYYYMMDD-NNNNNN.
  const firstRow = page.getByTestId("mine-order-row").first();
  await expect(firstRow).toBeVisible();
  await expect(firstRow).toContainText(/YR-\d{8}-\d{6}/);
  await firstRow.getByTestId(/^open-order-/).click();
  await expect(page).toHaveURL(/\/changes\/orders\/[0-9a-f-]{36}$/);
  await expect(page.getByTestId("order-detail-page")).toBeVisible();
  await expect(page.getByTestId("order-timeline")).toBeVisible();
  await expect(page.getByTestId("order-timeline-list").locator("li").first()).toBeVisible();
});

test("order state filters are served deterministically by the real backend", { tag: "@ai-real" }, async ({ page }) => {
  await page.goto("/changes/mine");
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 20_000 });

  // Differential oracle: read the submitter's live orders directly from the API
  // and select a concrete state that is guaranteed to have matching rows.
  const target = await page.evaluate(async () => {
    const response = await fetch("/api/v4/change-orders?limit=50");
    const envelope = (await response.json()) as {
      data?: { items?: Array<{ state: string }> };
    };
    return envelope.data?.items?.map((item) => item.state) ?? [];
  });
  const chosenState = target.find((state) => state in STATE_OPTION_LABELS);
  expect(chosenState, "expected at least one seeded order with a mappable state").toBeDefined();
  const chosenLabel = chosenState !== undefined ? STATE_OPTION_LABELS[chosenState] : undefined;
  if (chosenLabel === undefined) {
    throw new Error(`unmapped order state: ${String(chosenState)}`);
  }

  await page.getByTestId("filter-keyword").click();
  await page.getByRole("option", { name: "\u72b6\u6001" }).click();
  await Promise.all([
    page.waitForResponse(
      (resp) =>
        resp.url().includes("/api/v4/change-orders") &&
        resp.url().includes(`state=${chosenState}`) &&
        resp.status() === 200,
    ),
    page.getByRole("option", { name: chosenLabel, exact: true }).click(),
  ]);
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 20_000 });
  const filteredRows = page.getByTestId("mine-order-row");
  await expect(filteredRows.first()).toContainText(chosenLabel, { timeout: 20_000 });
  const filteredCount = await filteredRows.count();
  expect(filteredCount).toBeGreaterThan(0);
  for (let i = 0; i < filteredCount; i++) {
    await expect(filteredRows.nth(i)).toContainText(chosenLabel);
  }
  await page.getByTestId("filter-keyword-clear").click();
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 20_000 });
});
