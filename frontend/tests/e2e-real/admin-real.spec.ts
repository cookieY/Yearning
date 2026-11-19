import { expect, test } from "@playwright/test";
import { statePath } from "./helpers/real-auth";

// FE-F12 real-backend e2e — admin read faces over real registries: the
// seeded stack users and the f12-pg-target datasource render in the admin
// tables. CRUD dialogs stay covered by the mock suite; here the claim under
// test is that the admin surfaces consume the real backend without the mock
// layer.
test.use({ storageState: statePath("admin") });

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

test("admin users page lists the seeded role users", async ({ page }) => {
  await page.goto("/admin/users");
  const table = page.getByTestId("admin-users-table");
  await expect(table).toBeVisible({ timeout: 20_000 });
  await expect(table).toContainText("f12-submitter");
  await expect(table).toContainText("f12-approver");
  await expect(table).toContainText("f12-executor");
});

test("admin datasources page lists the seeded target", async ({ page }) => {
  await page.goto("/admin/datasources");
  // The table rows are tagged per datasource id (ds-row-<uuid>); the seeded
  // target renders with its name in the first cell. (Scratch datasources
  // from interrupted contract runs are cleaned up; the count is not part of
  // the claim.)
  await expect(page.locator('[data-testid^="ds-row-"]').first()).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('[data-testid^="ds-row-"]').filter({ hasText: "f12-pg-target" }))
    .toBeVisible();
});
