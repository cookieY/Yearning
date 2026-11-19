import type { Page } from "@playwright/test";
import { expect, test } from "@playwright/test";
import { KEY_PAGE_TYPES } from "./helpers/key-pages";
import { mockSession, setMockIdentity } from "./helpers/auth";

// FE-F11 six key page types (yearning-ui-design-spec.md §7.1–§7.6), captured
// from the real React pages at the four standard viewports in zh-CN and
// en-US under light and dark themes. Together with the FE-F2 shell/login
// baseline these files are the stable visual baseline for the three-layer
// review: frozen template shell → legacy information organization → new
// Yearning pages. The page-type definitions live in helpers/key-pages.ts so
// the WCAG audit judges exactly the same informative states.
const VIEWPORTS = [
  { name: "1440", width: 1440, height: 900 },
  { name: "1280", width: 1280, height: 800 },
  { name: "1024", width: 1024, height: 768 },
  { name: "narrow", width: 390, height: 844 },
] as const;
const LOCALES = ["zh-CN", "en-US"] as const;
const THEMES = ["light", "dark"] as const;

async function prepare(page: Page, locale: string, theme: string): Promise<void> {
  await page.addInitScript(
    `window.localStorage.setItem('yearning-locale', '${locale}');` +
      `window.localStorage.setItem('vite-ui-theme', '${theme}');`,
  );
  await page.emulateMedia({ reducedMotion: "reduce" });
  // Baseline determinism (FE-F11): the mock world derives every rendered
  // timestamp from the real clock, so an unpinned baseline churns on every
  // regeneration. setFixedTime pins Date only — fixture timelines and
  // animations keep running on real timers.
  await page.clock.setFixedTime(new Date("2026-09-01T08:00:00Z"));
}

// Each capture is independent (own browser context, own mock world), so the
// 96 captures parallelize across workers instead of serializing one.
test.describe.configure({ mode: "parallel" });

for (const pageType of KEY_PAGE_TYPES) {
  for (const viewport of VIEWPORTS) {
    for (const locale of LOCALES) {
      for (const theme of THEMES) {
        test(`${pageType.name} baseline ${viewport.name} ${locale} ${theme}`, async ({ page }) => {
          await prepare(page, locale, theme);
          if (pageType.scenario !== null) {
            await page.addInitScript(
              `window.localStorage.setItem('yearning-mock-scenario', '${pageType.scenario}');`,
            );
          }
          await mockSession(page, pageType.session);
          await setMockIdentity(page, pageType.identity);
          await page.setViewportSize({ width: viewport.width, height: viewport.height });
          await pageType.prepare(page);
          await page.evaluate(() => document.fonts.ready);
          if (pageType.name === "query") {
            // Monaco lays out asynchronously; capture only when its painted
            // surface fits the host allocation instead of accepting overlap
            // with the result grid as a new baseline.
            await expect.poll(() => page.getByTestId("query-sql-editor").evaluate((editor) => {
              const host = editor.parentElement?.getBoundingClientRect();
              const painted = editor.querySelector(".monaco-editor")?.getBoundingClientRect();
              if (host === undefined || painted === undefined) return Number.POSITIVE_INFINITY;
              return Math.max(Math.abs(editor.getBoundingClientRect().height - host.height), painted.bottom - host.bottom);
            })).toBeLessThanOrEqual(1);
          }
          // Two frames flush the fonts/layout the ready signal does not cover:
          // under full-suite parallelism a container can snapshot a mid-paint
          // frame, which reads as a one-off baseline diff.
          await page.evaluate(
            () => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))),
          );
          await page.waitForTimeout(150);
          await expect(page).toHaveScreenshot(
            `${pageType.name}-${viewport.name}-${locale}-${theme}.png`,
            { fullPage: true },
          );
        });
      }
    }
  }
}
