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

test("deep-link browser reloads render SPA routes cleanly without returning raw JSON", async ({ page }) => {
  for (const [route, testId] of [
    ["/admin/users", "admin-users-table"],
    ["/admin/permission-groups", "admin-groups-table"],
    ["/admin/datasources", "ds-create"],
  ] as const) {
    await page.goto(route);
    await expect(page.getByTestId(testId)).toBeVisible({ timeout: 20_000 });
    await page.reload();
    await expect(page.getByTestId(testId)).toBeVisible({ timeout: 20_000 });
    const me = await page.evaluate(async () => {
      const res = await fetch("/api/v4/users/me");
      return (await res.json()) as { err_code?: number; data?: { username?: string } };
    });
    expect(me.err_code).toBe(0);
    expect(me.data?.username).toBe("admin");
  }
});

test("admin permission group CRUD round-trips through UI search and API differential oracle", async ({ page }) => {
  const groupName = `e2e-real-grp-${Date.now()}`;
  await page.goto("/admin/permission-groups");
  await expect(page.getByTestId("admin-groups-table")).toBeVisible({ timeout: 20_000 });

  await page.getByTestId("admin-groups-create").click();
  await page.getByTestId("group-name").fill(groupName);
  await page.getByTestId("group-form-submit").click();
  await expect(page.getByTestId("group-name")).toBeHidden({ timeout: 15_000 });

  // Search via toolbar input & assert API differential oracle agrees.
  const searchInput = page.getByTestId("admin-groups-search");
  await searchInput.fill(groupName);
  await searchInput.press("Enter");
  const created = await page.evaluate(async (targetName: string) => {
    const res = await fetch(`/api/v4/admin/permission-groups?q=${encodeURIComponent(targetName)}`);
    const doc = (await res.json()) as { data?: { items?: Array<{ id: string; name: string }> } };
    return doc.data?.items?.find((g) => g.name === targetName) ?? null;
  }, groupName);
  expect(created, "created permission group must be persisted on backend").not.toBeNull();
  if (created === null) throw new Error("created permission group missing");
  const groupId = created.id;
  await expect(page.getByTestId(`admin-group-row-${groupId}`)).toBeVisible({ timeout: 15_000 });

  // Cleanup scratch group through real UI deletion flow.
  await page.getByTestId(`admin-group-actions-${groupId}`).click();
  await page.getByTestId(`admin-group-delete-${groupId}`).click();
  await page.getByTestId("admin-group-delete-confirm").click();
  await expect(page.getByTestId(`admin-group-row-${groupId}`)).toBeHidden({ timeout: 15_000 });
});
