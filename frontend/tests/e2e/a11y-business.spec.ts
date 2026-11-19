import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { KEY_PAGE_TYPES } from "./helpers/key-pages";
import { mockSession, setMockIdentity } from "./helpers/auth";

// FE-F11 WCAG 2.1 AA audit (migration contract §8, design spec §11): the six
// key page types must be free of critical and serious violations under
// wcag2a/wcag2aa in both locales and both themes, audited in the same
// informative states the screenshot baseline captures. The FE-F2 gate
// covered the shell and login; this extends the audit to every business
// surface delivered by FE-F4…F10.
const LOCALES = ["zh-CN", "en-US"] as const;
const THEMES = ["light", "dark"] as const;

for (const pageType of KEY_PAGE_TYPES) {
  for (const locale of LOCALES) {
    for (const theme of THEMES) {
      test(`${pageType.name} has no critical/serious a11y violations (${locale}, ${theme})`, async ({
        page,
      }) => {
        await page.addInitScript(
          `window.localStorage.setItem('yearning-locale', '${locale}');` +
            `window.localStorage.setItem('vite-ui-theme', '${theme}');`,
        );
        if (pageType.scenario !== null) {
          await page.addInitScript(
            `window.localStorage.setItem('yearning-mock-scenario', '${pageType.scenario}');`,
          );
        }
        await mockSession(page, pageType.session);
        await setMockIdentity(page, pageType.identity);
        await page.emulateMedia({ reducedMotion: "reduce" });
        await pageType.prepare(page);
        await page.evaluate(() => document.fonts.ready);
        const results = await new AxeBuilder({ page })
          .withTags(["wcag2a", "wcag2aa"])
          .analyze();
        const serious = results.violations.filter(
          (violation) => violation.impact === "critical" || violation.impact === "serious",
        );
        expect(serious).toEqual([]);
      });
    }
  }
}
