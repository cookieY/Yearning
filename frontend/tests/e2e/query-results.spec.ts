import { expect, test } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// §18.50 acceptance gates (oversized query results), driven by the query-huge scenario
// world: 2600 seeded rows with a ~1400-char longtext column, served through
// the production cursor pages exactly like the backend.
//   1. Result pagination — the grid pages the loaded rows locally (page size selector
//      included) and hands over to the server cursor when paging past them;
//   2. Column width drag-resize — every header carries a drag handle that resizes the column;
//   3. Oversized ellipsis + Tooltip — oversized cells ellipsize and hover reveals the
//      full content through a tooltip.

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
    window.localStorage.setItem("yearning-mock-scenario", "query-huge");
  });
  await mockSession(page, "default");
});

test("a huge result pages locally, resizes columns and tooltips long cells", async ({
  page,
}) => {
  test.setTimeout(60_000);

  await page.goto("/query");
  await page.getByTestId(/query-flow-enter-/).first().click();
  await page.waitForURL(/\/query\/sessions\//, { timeout: 10_000 });
  await expect(page.getByTestId("query-workspace")).toBeVisible();

  // The huge table lives in the events schema of analytics-mysql.
  await page.getByTestId("query-schema-input").click();
  await page.getByRole("option", { name: "events" }).click();
  await page.getByTestId("query-sql-editor").click();
  await page.keyboard.insertText("select id, kind, event_blob from events.wide_events");
  await page.getByTestId("query-run").click();
  const loaded = page.locator("[data-testid^='query-result-loaded-']").first();
  // Legacy parity: the background drain loads the WHOLE result (2600 rows)
  // automatically; paging afterwards is purely frontend.
  await expect(loaded).toContainText("\u5df2\u8bfb\u53d6 2600 \u884c", { timeout: 20_000 });

  // 1. Pure frontend pagination: 100 rows per page over the loaded result. The
  // virtualizer only paints the viewport, so the page boundary is asserted
  // through the first global row index each page starts at.
  await expect(page.getByTestId("query-result-pagination")).toBeVisible();
  await expect(page.getByTestId("query-result-page")).toContainText("1");
  await expect(page.locator("[data-row='0']")).toBeVisible();
  await page.getByTestId("query-result-pagination").getByRole("button", { name: /\u4e0b\u4e00\u9875|Next/ }).click();
  await expect(page.getByTestId("query-result-page")).toContainText("2");
  await expect(page.locator("[data-row='100']")).toBeVisible();
  await expect(page.locator("[data-row='0']")).toHaveCount(0);
  await page.getByTestId("query-result-pagination").getByRole("button", { name: /\u4e0a\u4e00\u9875|Previous|Prev/ }).click();
  await expect(page.getByTestId("query-result-page")).toContainText("1");
  await expect(page.locator("[data-row='0']")).toBeVisible();

  // 2. Column resize: dragging the header handle widens the longtext column.
  const blobHeader = page.getByTestId("query-col-resize-event_blob");
  const tableBefore = await page.evaluate(() => {
    const th = document.querySelector('[data-testid="query-col-resize-event_blob"]')?.parentElement;
    return th instanceof HTMLElement ? th.getBoundingClientRect().width : 0;
  });
  const handleBox = await blobHeader.boundingBox();
  if (handleBox !== null) {
    await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
    await page.mouse.down();
    await page.mouse.move(handleBox.x + 240, handleBox.y + handleBox.height / 2, { steps: 6 });
    await page.mouse.up();
  }
  const tableAfter = await page.evaluate(() => {
    const th = document.querySelector('[data-testid="query-col-resize-event_blob"]')?.parentElement;
    return th instanceof HTMLElement ? th.getBoundingClientRect().width : 0;
  });
  expect(tableAfter - tableBefore).toBeGreaterThan(100);

  // 3. Long cells ellipsize and the tooltip carries the full content.
  const longCell = page.getByTestId("query-cell-event_blob-0");
  await expect(longCell).toBeVisible();
  await longCell.hover();
  const tip = page.getByTestId("query-cell-tip-event_blob-0");
  await expect(tip).toBeVisible({ timeout: 5_000 });
  const tipText = (await tip.textContent()) ?? "";
  expect(tipText.length).toBeGreaterThan(1000);
});
