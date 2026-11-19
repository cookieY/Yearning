import { expect, test } from "@playwright/test";
import { statePath } from "./helpers/real-auth";

// FE-F12 real-backend e2e — the submitter's order surfaces over real data:
// the seeded chains (scripts/integration/f12_seed.py + the contract suite)
// leave YR-numbered orders behind, so /changes/mine lists rows and the
// detail page renders the real timeline. Execution-stage actions are not
// exercised here: serve starts no stage_execution worker
// (BLK-20261017-SERVE-BACKGROUND-WORKERS) — attempts stay running.
test.use({ storageState: statePath("submitter") });

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

test("submitter lists real orders and opens one detail", async ({ page }) => {
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

test("order state filters are served by the real backend", async ({ page }) => {
  await page.goto("/changes/mine");
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 20_000 });
  // The ECS-style search bar's state chip drives a server round-trip (F6
  // filter contract): pick one state and assert the table still renders —
  // the exact row set depends on the live stack, the contract is that the
  // query succeeds.
  await page.getByTestId("filter-keyword").click();
  await page.getByRole("option", { name: "\u72b6\u6001" }).click();
  await page.getByRole("option", { name: /\u5df2\u901a\u8fc7|\u6267\u884c\u4e2d|\u5df2\u64a4\u56de/ }).first().click();
  await expect(
    page.getByTestId("mine-orders-table").or(page.getByTestId("orders-empty")),
  ).toBeVisible({ timeout: 20_000 });
  await page.getByTestId("filter-keyword-clear").click();
  await expect(page.getByTestId("mine-orders-table")).toBeVisible({ timeout: 20_000 });
});
