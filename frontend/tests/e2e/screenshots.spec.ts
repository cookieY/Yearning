import type { Page } from "@playwright/test";
import { expect, test } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// FE-F2 screenshot baseline (yearning-ui-design-spec.md §14): the real React
// shell captured at 1440px, 1280px, 1024px and narrow width, in zh-CN and
// en-US, light and dark. These are assertions, not captures: each one compares
// against the committed PNG in tests/screenshots/, which stays the stable
// baseline for owner review. Regenerate only through `pnpm screenshots:update`.
const VIEWPORTS = [
  { name: "1440", width: 1440, height: 900 },
  { name: "1280", width: 1280, height: 800 },
  { name: "1024", width: 1024, height: 768 },
  { name: "narrow", width: 390, height: 844 },
] as const;
const LOCALES = ["zh-CN", "en-US"] as const;
const THEMES = ["light", "dark"] as const;

async function prepare(page: Page, locale: string, theme: string) {
  await page.addInitScript(
    `window.localStorage.setItem('yearning-locale', '${locale}');` +
      `window.localStorage.setItem('vite-ui-theme', '${theme}');`,
  );
  await page.emulateMedia({ reducedMotion: "reduce" });
  // Baseline determinism (FE-F11): the mock world derives every rendered
  // timestamp from the real clock, so an unpinned baseline churns on every
  // regeneration. setFixedTime pins Date only — animations keep running on
  // real timers. Must match screenshots-business.spec.ts.
  await page.clock.setFixedTime(new Date("2026-09-01T08:00:00Z"));
}

// FE-F10: the workspace renders the real dashboard instead of the FE-F2
// placeholder. Owner ruling 2026-10-23 promoted the reference-derived home —
// the readiness signal is its first counter, which renders once the personal
// dashboard query resolves (no greeting line any more).
const READY_TESTID = "home-kpi-pending-approval";

for (const viewport of VIEWPORTS) {
  for (const locale of LOCALES) {
    for (const theme of THEMES) {
      test(`shell baseline ${viewport.name} ${locale} ${theme}`, async ({ page }) => {
        await prepare(page, locale, theme);
        await mockSession(page, "admin");
        await page.setViewportSize({ width: viewport.width, height: viewport.height });
        await page.goto("/workspace");
        await page.evaluate(() => document.fonts.ready);
        await expect(page.getByTestId(READY_TESTID)).toBeVisible();
        await page.evaluate(
          () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
        );
        await page.waitForTimeout(150);
        await expect(page).toHaveScreenshot(`shell-${viewport.name}-${locale}-${theme}.png`, {
          fullPage: true,
        });
      });

      test(`login baseline ${viewport.name === "1440" ? "desktop" : viewport.name} ${locale} ${theme}`, async ({
        page,
      }) => {
        test.skip(viewport.name !== "1440", "login captures use the desktop viewport");
        await prepare(page, locale, theme);
        await page.setViewportSize({ width: viewport.width, height: viewport.height });
        await page.goto("/login");
        await page.evaluate(() => document.fonts.ready);
        await expect(page.getByText(/\u767b\u5f55 Yearning|Sign in to Yearning/)).toBeVisible();
        await page.evaluate(
          () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
        );
        await page.waitForTimeout(150);
        await expect(page).toHaveScreenshot(`login-${locale}-${theme}.png`, { fullPage: true });
      });
    }
  }
}

// The modal overlay is the one visual change in the 10/10 pass that the page
// baselines cannot see: no other capture has a dialog open. The blur was
// removed because the full-screen backdrop-filter pushed the dialog-entry p95
// to 33.3ms against a 16.7ms budget; the 40% black veil keeps the modality
// without a full-screen filter pass.
for (const theme of THEMES) {
  test(`dialog overlay baseline ${theme}`, async ({ page }) => {
    await prepare(page, "zh-CN", theme);
    await mockSession(page, "admin");
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/admin/datasources");
    // analytics-pg is the unreferenced row: its ⋯ menu carries an enabled
    // delete (referenced rows like prod-order-mysql keep a disabled one).
    const row = page.locator("tr", { hasText: "analytics-pg" });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await row.getByTestId(/ds-actions-/).click();
    await page.getByTestId(/^ds-delete-/).first().click();
    await expect(page.locator("[data-slot='dialog-content']").first()).toBeVisible();
    await page.waitForTimeout(150);
    await expect(page).toHaveScreenshot(`dialog-overlay-${theme}.png`, { fullPage: true });
  });
}
