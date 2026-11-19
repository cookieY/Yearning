import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// Basic accessibility gate (FE-F2 acceptance: basic accessibility passed): the shell and
// login pages must be free of critical and serious violations in both
// locales under light and dark themes.
const scenarios = [
  { locale: "zh-CN", theme: "light" },
  { locale: "zh-CN", theme: "dark" },
  { locale: "en-US", theme: "light" },
  { locale: "en-US", theme: "dark" },
] as const;

for (const scenario of scenarios) {
  test(`workspace has no critical/serious a11y violations (${scenario.locale}, ${scenario.theme})`, async ({
    page,
  }) => {
    await page.addInitScript(
      `window.localStorage.setItem('yearning-locale', '${scenario.locale}');` +
        `window.localStorage.setItem('vite-ui-theme', '${scenario.theme}');`,
    );
    await mockSession(page, "admin");
    await page.goto("/workspace");
    await page.evaluate(() => document.fonts.ready);
    const results = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa"])
      .analyze();
    const serious = results.violations.filter(
      (violation) => violation.impact === "critical" || violation.impact === "serious",
    );
    expect(serious).toEqual([]);
  });

  test(`login has no critical/serious a11y violations (${scenario.locale}, ${scenario.theme})`, async ({
    page,
  }) => {
    await page.addInitScript(
      `window.localStorage.setItem('yearning-locale', '${scenario.locale}');` +
        `window.localStorage.setItem('vite-ui-theme', '${scenario.theme}');`,
    );
    await page.goto("/login");
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
