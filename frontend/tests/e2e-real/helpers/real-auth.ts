import { expect, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";

/**
 * Real-backend session bootstrap for the FE-F12 e2e-real suite. Login goes
 * through the actual login form; the backend (proxied at the same origin)
 * sets the session cookies. Credentials come from the stack environment —
 * the admin password is random per stack (F12_ADMIN_PASSWORD), the seeded
 * role users share the fixture password documented in
 * scripts/integration/f12_seed.py.
 *
 * The backend rate-limits logins (10 attempts/user/minute): the setup project
 * authenticates every role exactly once per run and persists the storage
 * state; specs reuse it instead of logging in again.
 */
export type RealRole = "admin" | "submitter" | "approver" | "executor" | "quser";

export const ADMIN_USERNAME = "admin";

export function adminPassword(): string {
  const value = process.env.F12_ADMIN_PASSWORD;
  if (!value) throw new Error("F12_ADMIN_PASSWORD is required for the real e2e suite");
  return value;
}

export function seedPassword(): string {
  return process.env.F12_SEED_USER_PASSWORD ?? "f12seed-pw12345";
}

export function seedUsername(role: Exclude<RealRole, "admin">): string {
  return `f12-${role}`;
}

/** Storage-state file for a role (auth.setup.ts writes, specs read). */
export function statePath(role: RealRole): string {
  const dir = join(process.cwd(), "tests", "e2e-real", ".auth");
  mkdirSync(dir, { recursive: true });
  return join(dir, `${role}.json`);
}

/** Anonymous storage state — forces the login page for unauthenticated specs. */
export const anonymousState: { cookies: never[]; origins: never[] } = { cookies: [], origins: [] };

export async function loginReal(page: Page, username: string, password: string): Promise<void> {
  // Drop any live session first: the login route redirects authenticated
  // users to the workspace, and role switches inside one test are the norm.
  await page.context().clearCookies();
  await page.goto("/login");
  await page.getByLabel(/\u7528\u6237\u540d|Username/).fill(username);
  await page.getByLabel(/\u5bc6\u7801|Password/).fill(password);
  await page.getByRole("button", { name: /\u767b\u5f55|Sign in/ }).click();
  await expect(page).toHaveURL(/\/workspace$/, { timeout: 20_000 });
}

export async function loginAdmin(page: Page): Promise<void> {
  await loginReal(page, ADMIN_USERNAME, adminPassword());
}

export async function loginSeedUser(page: Page, role: Exclude<RealRole, "admin">): Promise<void> {
  await loginReal(page, seedUsername(role), seedPassword());
}
