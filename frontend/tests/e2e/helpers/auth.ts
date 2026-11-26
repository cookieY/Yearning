import { expect, type Page } from "@playwright/test";

/**
 * Mock session bootstrap for e2e (shared MSW handlers, code-generation-policy
 * mock_layer). The `yearning_session` HttpOnly cookie is planted directly in
 * the browser cookie jar exactly as a real login would leave it — no token
 * ever lives in web storage or a URL. The behavior dimension is read per
 * request by the auth mock handlers, so expiry and lock-out can be flipped
 * mid-test.
 */
export type MockAuthBehavior =
  | "default"
  | "admin"
  | "invalid_credentials"
  | "admin_locked"
  | "expired";

export async function mockSession(page: Page, behavior: MockAuthBehavior = "admin"): Promise<void> {
  await page.addInitScript(`window.localStorage.setItem('yearning-mock-auth', '${behavior}');`);
  await page.context().addCookies([
    {
      name: "yearning_session",
      value: "e2e-session-token",
      domain: "localhost",
      path: "/",
    },
    { name: "yearning_csrf", value: "e2e-csrf-token", domain: "localhost", path: "/" },
  ]);
}

/**
 * Flips the behavior mid-test. Uses an init script so it works before any
 * navigation (about:blank denies localStorage access) and an immediate write
 * once a real document exists, so handlers read the new value on the next
 * request without a reload.
 */
export async function setMockAuthBehavior(page: Page, behavior: MockAuthBehavior): Promise<void> {
  await page.addInitScript(`window.localStorage.setItem('yearning-mock-auth', '${behavior}');`);
  const url = page.url();
  if (url !== "" && !url.startsWith("about:")) {
    await page.evaluate((next) => {
      window.localStorage.setItem("yearning-mock-auth", next);
    }, behavior);
  }
}

/**
 * Mock identity dimension (§18.44): henry submits, reviewer-a holds the
 * frozen approval slots, executor-a the execution slots. Handlers read the
 * stored identity per request — /users/me answers with it too. Use before
 * the first navigation on a fresh world; use becomeIdentity to switch
 * inside a living page.
 */
export type MockIdentity = "henry" | "reviewer-a" | "executor-a";

export async function setMockIdentity(page: Page, identity: MockIdentity): Promise<void> {
  await page.addInitScript(`window.localStorage.setItem('yearning-mock-identity', '${identity}');`);
  const url = page.url();
  if (url !== "" && !url.startsWith("about:")) {
    await page.evaluate((next) => {
      window.localStorage.setItem("yearning-mock-identity", next);
    }, identity);
  }
}

/** Fills and submits the login form on the current /login view — no
 * navigation, so the in-memory mock world survives (F4 §11.4). */
async function submitLoginForm(page: Page, username: string, password: string): Promise<void> {
  await page.getByLabel(/\u7528\u6237\u540d|Username/).fill(username);
  await page.getByLabel(/\u5bc6\u7801|Password/).fill(password);
  await page.getByRole("button", { name: /\u767b\u5f55|Sign in/ }).click();
}

/**
 * Switches the acting identity inside a living page without a reload (a
 * reload would reset the in-memory mock world, F4 §11.4): flip the stored
 * identity, log out through the real UI, then log back in — /users/me and
 * every role surface re-resolve as the new identity while orders submitted
 * earlier in the same page session survive.
 */
export async function becomeIdentity(page: Page, identity: MockIdentity): Promise<void> {
  await setMockIdentity(page, identity);
  await page.getByRole("button", { name: "\u8d26\u6237\u83dc\u5355" }).click();
  const signOut = page.getByRole("button", { name: /\u9000\u51fa\u767b\u5f55/ });
  await expect(signOut).toBeEnabled();
  await signOut.click();
  await expect(page).toHaveURL(/\/login$/);
  await submitLoginForm(page, identity, "fixture-pw");
  // The login page returns to the pre-logout route (redirect-back), so the
  // only universal landing assertion is "back inside the app".
  await expect(page).not.toHaveURL(/\/login$/, { timeout: 8_000 });
}

/** Logs in through the real UI form. No landing assertion — failure
 * behaviors (invalid credentials, admin lock) must stay on /login. */
export async function loginViaForm(page: Page, username = "henry", password = "fixture-pw"): Promise<void> {
  await page.goto("/login");
  await submitLoginForm(page, username, password);
}
