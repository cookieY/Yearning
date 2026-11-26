import { expect, test } from "@playwright/test";
import { statePath } from "./helpers/real-auth";

// FE-F12 / Stage 3B real-backend E2E — full browser submission wizard journey
// through a real OpenAI-compatible AI provider: select flow -> enter title + SQL
// in Monaco -> run ephemeral precheck -> wait for ready gate -> confirm submit ->
// verify redirect to real order detail page.
test.use({ storageState: statePath("submitter") });

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

test("submitter runs full UI submission wizard through real AI review", { tag: "@ai-real" }, async ({ page }) => {
  test.setTimeout(180_000);

  await page.goto("/changes/new");
  await expect(page.getByTestId("changes-new-page")).toBeVisible({ timeout: 20_000 });
  const targetFlow = page.locator('[data-testid^="use-flow-"]').filter({ hasText: "f12-change-flow" });
  await expect(targetFlow).toBeVisible({ timeout: 20_000 });
  await targetFlow.first().click();

  const title = `e2e-real-wizard-${Date.now()}`;
  await page.getByTestId("draft-title-input").fill(title);

  const lines = page.getByTestId("sql-editor").locator(".monaco-editor .view-lines");
  await expect(lines).toBeVisible({ timeout: 15_000 });
  await lines.click({ position: { x: 120, y: 20 } });
  await page.keyboard.type("create table if not exists f12_wizard_e2e (id bigint primary key);");

  await page.getByTestId("run-review").click();
  await expect(page.getByTestId("submit-draft")).toBeEnabled({ timeout: 120_000 });

  await page.getByTestId("submit-draft").click();
  await expect(page.getByTestId("submit-confirm-dialog")).toBeVisible();
  await page.getByTestId("submit-confirm-accept").click();

  await expect(page).toHaveURL(/\/changes\/orders\/[0-9a-f-]{36}$/, { timeout: 20_000 });
  await expect(page.getByTestId("order-detail-page")).toBeVisible();
  await expect(page.getByTestId("order-timeline")).toBeVisible();
});
