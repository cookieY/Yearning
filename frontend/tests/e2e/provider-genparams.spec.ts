// S008 FE (AI Provider generation parameters and reference pricing) acceptance over the mock world:
//   1. Three-group navigation — Connection / Generation Parameters / Reference Pricing tab switching (UI-01);
//   2. Parameter backfill — stored values take precedence (medium/0.7), unconfigured = provider default placeholder;
//   3. Persistent reference disclaimer — Reference Pricing group renders cost disclaimer text (UI-02).
// Assertions check rendered text (key existence alone does not count).
import { expect, test } from "@playwright/test";
import { mockSession } from "./helpers/auth";

test.describe("provider genparams & pricing (@provider-genparams)", () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      window.localStorage.setItem("yearning-locale", "zh-CN");
    });
    await mockSession(page, "admin");
    await page.goto("/admin/review-engine/providers");
    await expect(page.getByTestId(/^provider-row-/).first()).toBeVisible();
    await page.getByTestId(/^provider-actions-/).first().click();
    await page.getByTestId(/^provider-edit-/).first().click();
    await expect(page.getByRole("dialog")).toBeVisible();
  });

  test("three-group navigation and stored-params backfill @provider-genparams", async ({ page }) => {
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    // Three-group navigation.
    for (const tab of ["provider-tab-connection", "provider-tab-generation", "provider-tab-pricing"]) {
      await expect(dialog.getByTestId(tab)).toBeVisible();
    }
    // Generation parameter backfill: stored values take precedence (fixture primary: medium / 0.7), unconfigured = default placeholder.
    await dialog.getByTestId("provider-tab-generation").click();
    await expect(dialog.getByTestId("provider-gen-reasoning")).toContainText("medium");
    await expect(dialog.getByTestId("provider-gen-max-tokens")).toHaveValue("");
    await expect(dialog.getByTestId("provider-gen-max-tokens")).toHaveAttribute(
      "placeholder",
      "\u670d\u52a1\u5546\u9ed8\u8ba4",
    );
    await expect(dialog.getByTestId("provider-gen-temperature")).toHaveValue("0.7");
    // Persistent hint (including explicit zero semantics for temperature=0).
    await expect(dialog.getByText(/temperature \u586b 0 \u662f\u663e\u5f0f\u53d6\u503c/)).toBeVisible();
  });

  test("pricing tab keeps the standing reference declaration @provider-genparams", async ({ page }) => {
    const dialog = page.getByRole("dialog");
    await dialog.getByTestId("provider-tab-pricing").click();
    // UI-02: disclaimer is persistent and verified as rendered text (not key existence).
    await expect(dialog.getByText(/\u5747\u4e3a\u53c2\u8003\u6210\u672c\u4f30\u7b97/)).toBeVisible();
    await expect(dialog.getByText(/\u4e0d\u6784\u6210\u8d26\u5355\u6216\u5b9e\u9645\u6263\u6b3e/)).toBeVisible();
    // Usage disclaimer + ledger table (fixture has two rows: one complete, one unreported).
    await expect(dialog.getByText(/\u7f3a\u5931\u5b57\u6bb5\u663e\u793a\u300c\u672a\u8fd4\u56de\/\u672a\u8ba1\u7b97\u300d/)).toBeVisible();
    await expect(dialog.getByText(/\u8fd0\u884cAI\u9884\u5ba1\u540e\u6bcf\u6b21HTTP\u8c03\u7528|\u8c03\u7528\u660e\u7ec6/).first()).toBeVisible();
    await expect(dialog.getByTestId("provider-usage-panel")).toBeVisible();
  });
});
