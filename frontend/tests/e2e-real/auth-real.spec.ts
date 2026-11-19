import { expect, test } from "@playwright/test";
import { anonymousState, loginAdmin, statePath } from "./helpers/real-auth";

// FE-F12 real-backend e2e — session core: form login against the serve stack
// (proxied same-origin), mapped business error, logout round-trip, admin
// capability gating. Sessions come from the setup project's storage states
// (one login per role per run — the backend rate-limits logins at 10
// attempts/user/minute); the wrong-credentials case uses the anonymous state
// and exactly one failed attempt against a random unknown user.
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

test("admin login through the form lands on the workspace", async ({ page }) => {
  // This one still performs a real login (that is the claim under test).
  await loginAdmin(page);
  await expect(page.getByTestId("workspace-page")).toBeVisible();
});

test.describe("anonymous", () => {
  test.use({ storageState: anonymousState });

  test("wrong credentials show the mapped business error and stay on /login", async ({ page }) => {
    await page.goto("/login");
    await page.getByLabel(/\u7528\u6237\u540d|Username/).fill("f12-e2e-nobody");
    await page.getByLabel(/\u5bc6\u7801|Password/).fill("wrong-pw-1");
    await page.getByRole("button", { name: /\u767b\u5f55|Sign in/ }).click();
    await expect(page.getByText("\u7528\u6237\u540d\u6216\u5bc6\u7801\u4e0d\u6b63\u786e\u3002")).toBeVisible({ timeout: 15_000 });
    await expect(page).toHaveURL(/\/login$/);
  });
});

test.describe("admin session", () => {
  test.use({ storageState: statePath("admin") });

  test("logout revokes the real session and returns to the login page", async ({ page }) => {
    await page.goto("/workspace");
    await expect(page.getByTestId("workspace-page")).toBeVisible();
    await page.getByRole("button", { name: "\u8d26\u6237\u83dc\u5355" }).click();
    const signOut = page.getByRole("button", { name: /\u9000\u51fa\u767b\u5f55/ });
    await expect(signOut).toBeEnabled();
    await signOut.click();
    await expect(page).toHaveURL(/\/login$/);
    // The revoked session must not re-enter: a direct navigation bounces back.
    await page.goto("/workspace");
    await expect(page).toHaveURL(/\/login$/);
  });
});

test.describe("submitter session", () => {
  test.use({ storageState: statePath("submitter") });

  test("seeded submitter sees the workspace, not the admin surfaces", async ({ page }) => {
    await page.goto("/workspace");
    await expect(page.getByTestId("workspace-page")).toBeVisible();
    // can_access_admin is false for seeded users: admin routes redirect.
    await page.goto("/admin/users");
    await expect(page).toHaveURL(/\/403$/);
  });
});
