import { expect, test } from "@playwright/test";

// FE-F1 smoke: the skeleton renders its two lazy routes. Mock-driven flows are
// covered by vitest against the shared MSW handlers; browser-worker scenarios
// extend this suite when the shell lands in FE-F2.
test("login page renders the structural placeholder", async ({ page }) => {
  await page.goto("/login");
  // base-lyra CardTitle renders a div, not a heading; assert on visible text
  await expect(page.getByText(/\u767b\u5f55 Yearning|Sign in to Yearning/)).toBeVisible();
  await expect(page.getByLabel(/\u7528\u6237\u540d|Username/)).toBeVisible();
  await expect(page.getByLabel(/\u5bc6\u7801|Password/)).toBeVisible();
});

test("unknown routes land on the not-found page", async ({ page }) => {
  await page.goto("/definitely-not-a-route");
  await expect(page.getByText(/\u9875\u9762\u4e0d\u5b58\u5728|Page not found/)).toBeVisible();
});
