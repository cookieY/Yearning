import { expect, test, type Page } from "@playwright/test";
import { mockSession } from "./helpers/auth";
import { KEY_PAGE_TYPES } from "./helpers/key-pages";

type RouteCase = {
  path: string;
  listToolbar?: boolean;
  pageLayout?: boolean;
};

// Route-level presentation contract for the pages that have a document title
// band. Workflow-first surfaces (workspace, draft creation and query sessions)
// are exercised separately by the existing dashboard/business/query suites.
const PAGE_ROUTES: RouteCase[] = [
  { path: "/admin/users", listToolbar: true },
  { path: "/admin/permission-groups", listToolbar: true },
  { path: "/admin/flows", listToolbar: true },
  { path: "/admin/datasources", listToolbar: true },
  { path: "/admin/review-engine/providers", listToolbar: true },
  { path: "/admin/review-engine/skills", listToolbar: true },
  { path: "/admin/review-engine/knowledge" },
  { path: "/admin/rule-sets", listToolbar: true },
  { path: "/admin/announcements" },
  { path: "/admin/settings/general" },
  { path: "/admin/settings/query" },
  { path: "/admin/settings/execution" },
  { path: "/admin/settings/branding" },
  { path: "/admin/review-engine/ai-budget" },
  { path: "/admin/settings/notifications" },
  { path: "/admin/identity-providers" },
  { path: "/changes/mine", listToolbar: true },
  { path: "/approvals/changes", listToolbar: true },
  { path: "/approvals/query-access" },
  { path: "/records", listToolbar: true },
  { path: "/query" },
  { path: "/profile" },
  { path: "/403" },
  { path: "/404", pageLayout: false },
];

const RESPONSIVE_ROUTES = [
  "/admin/users",
  "/changes/mine",
  "/admin/settings/general",
  "/query",
] as const;

const THEMES = ["light", "dark"] as const;
const LOCALES = ["zh-CN", "en-US"] as const;

for (const viewport of [{ width: 1440, height: 900 }, { width: 1024, height: 768 }]) {
  test(`query editor stays inside its allocated height (${viewport.width})`, async ({ page }, testInfo) => {
    const queryPage = KEY_PAGE_TYPES.find((entry) => entry.name === "query");
    if (queryPage === undefined) throw new Error("Missing query page scenario");
    await setPreferences(page, "zh-CN", "light");
    await mockSession(page, queryPage.session);
    await page.addInitScript(() => {
      window.localStorage.setItem("yearning-mock-scenario", "query-session");
    });
    await page.setViewportSize(viewport);
    await queryPage.prepare(page);
    const geometry = await page.getByTestId("query-sql-editor").evaluate((editor) => {
      const allocated = editor.parentElement?.getBoundingClientRect();
      if (allocated === undefined) throw new Error("Missing editor height container");
      const actual = editor.getBoundingClientRect();
      const rendered = editor.querySelector(".monaco-editor")?.getBoundingClientRect();
      if (rendered === undefined) throw new Error("Missing mounted Monaco editor");
      return { bottom: actual.bottom, height: actual.height, renderedBottom: rendered.bottom, allocatedBottom: allocated.bottom, allocatedHeight: allocated.height };
    });
    expect(geometry.height).toBeCloseTo(geometry.allocatedHeight, 0);
    expect(geometry.bottom).toBeLessThanOrEqual(geometry.allocatedBottom + 1);
    expect(geometry.renderedBottom).toBeLessThanOrEqual(geometry.allocatedBottom + 1);
    await page.screenshot({ path: testInfo.outputPath("query-height.png"), animations: "disabled" });
  });
}

async function setPreferences(
  page: Page,
  locale: (typeof LOCALES)[number],
  theme: (typeof THEMES)[number],
): Promise<void> {
  await page.addInitScript(
    ({ nextLocale, nextTheme }) => {
      window.localStorage.setItem("yearning-locale", nextLocale);
      window.localStorage.setItem("vite-ui-theme", nextTheme);
    },
    { nextLocale: locale, nextTheme: theme },
  );
  await page.emulateMedia({ reducedMotion: "reduce" });
}

async function openRoute(
  page: Page,
  path: string,
  options: {
    width?: number;
    height?: number;
    locale?: (typeof LOCALES)[number];
    theme?: (typeof THEMES)[number];
  } = {},
): Promise<void> {
  await setPreferences(
    page,
    options.locale ?? "zh-CN",
    options.theme ?? "light",
  );
  await mockSession(page, "admin");
  await page.setViewportSize({
    width: options.width ?? 1440,
    height: options.height ?? 900,
  });
  await page.goto(path);
  await page.evaluate(() => document.fonts.ready);
}

async function expectNoDocumentOverflow(page: Page): Promise<void> {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

async function expectPagePresentation(
  page: Page,
  route: RouteCase,
): Promise<void> {
  const pageHeader = page.locator('[data-slot="page-header"]');
  await expect(pageHeader).toHaveCount(1, { timeout: 20_000 });
  await expect(pageHeader).toBeVisible();
  await expect(pageHeader.locator("h1")).toHaveCount(1);
  await expect(page.locator("h1")).toHaveCount(1);

  const typography = await pageHeader.locator("h1").evaluate((heading) => {
    const style = getComputedStyle(heading);
    return {
      fontSize: Number.parseFloat(style.fontSize),
      fontWeight: Number.parseInt(style.fontWeight, 10),
    };
  });
  expect(typography.fontSize).toBe(24);
  expect(typography.fontWeight).toBeGreaterThanOrEqual(600);

  if (route.pageLayout !== false) {
    const pageLayout = page.locator('[data-slot="page-layout"]').first();
    await expect(pageLayout).toBeVisible();
    const gap = await pageLayout.evaluate((layout) => {
      const style = getComputedStyle(layout);
      return Number.parseFloat(style.rowGap || style.gap);
    });
    expect(gap).toBe(20);
  }

  if (route.listToolbar === true) {
    const toolbar = page.locator('[data-slot="list-toolbar"]').first();
    await expect(toolbar).toBeVisible();
    expect(
      await toolbar.evaluate(
        (element) => element.closest('[data-slot="card"]') !== null,
      ),
    ).toBe(true);
    expect(
      await toolbar
        .locator('button, input, [role="combobox"], [contenteditable="true"]')
        .count(),
    ).toBeGreaterThan(0);
  }
}

test.describe("all titled routes use the shared visual language", () => {
  for (const route of PAGE_ROUTES) {
    test(`${route.path} uses one header and the shared 20px rhythm`, async ({
      page,
    }) => {
      await openRoute(page, route.path);
      await expectPagePresentation(page, route);
      await expectNoDocumentOverflow(page);
    });
  }
});

test.describe("representative pages stay within the viewport", () => {
  for (const path of RESPONSIVE_ROUTES) {
    for (const theme of THEMES) {
      for (const locale of LOCALES) {
        test(`${path} 390 ${locale} ${theme}`, async ({ page }) => {
          await openRoute(page, path, {
            width: 390,
            height: 844,
            locale,
            theme,
          });
          await expectPagePresentation(page, { path });
          await expectNoDocumentOverflow(page);
        });
      }
    }
  }
});

// Clearing a server-side search restores both the result set and keyboard focus.
test("search empty state clears back to results with focus restored", async ({ page }) => {
  await openRoute(page, "/admin/users");
  const search = page.getByTestId("admin-users-search");
  await search.fill("no-matching-user-visual-check");
  await expect(page.getByTestId("admin-users-empty")).toContainText("\u5339\u914d\u7ed3\u679c");
  await page.getByTestId("admin-users-search-clear").click();
  await expect(search).toBeFocused();
  await expect(search).toHaveValue("");
  await expect(page.getByTestId("admin-users-table")).toBeVisible();
});

test("header command menu loads on demand and supports keyboard reopen", async ({ page }) => {
  const loadedChunks: string[] = [];
  page.on("request", (request) => {
    if (/\/command-menu-[^/]+\.js$/.test(new URL(request.url()).pathname)) loadedChunks.push(request.url());
  });
  await openRoute(page, "/admin/users");
  await expect(page.getByTestId("admin-users-table")).toBeVisible();
  expect(loadedChunks).toHaveLength(0);
  const trigger = page.getByTestId("header-command-trigger");
  await trigger.click();
  await expect(page.getByRole("dialog")).toBeVisible();
  expect(loadedChunks.length).toBeGreaterThan(0);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(trigger).toBeFocused();
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeHidden();
});

for (const theme of THEMES) {
  test(`reference table hierarchy and comfortable rows (${theme})`, async ({ page }) => {
    await openRoute(page, "/admin/users", { theme });
    const table = page.getByTestId("admin-users-table");
    await expect(table).toBeVisible();
    const metrics = await table.evaluate((element) => {
      const head = element.querySelector("th");
      const row = element.querySelector("tbody tr");
      if (head === null || row === null) {
        throw new Error("Expected a populated user table");
      }
      const cells = row.querySelectorAll("td");
      const badge = row.querySelector('[data-slot="badge"]');
      const primaryCell = cells.item(0);
      const secondaryCell = cells.item(2);
      if (primaryCell === null || secondaryCell === null || badge === null) {
        throw new Error("Expected user row cells and status badge");
      }
      return {
        headerHeight: head.getBoundingClientRect().height,
        headerWeight: getComputedStyle(head).fontWeight,
        rowHeight: row.getBoundingClientRect().height,
        primaryWeight: getComputedStyle(primaryCell).fontWeight,
        primaryColor: getComputedStyle(primaryCell).color,
        secondaryColor: getComputedStyle(secondaryCell).color,
        badgeHeight: badge.getBoundingClientRect().height,
        badgeWeight: getComputedStyle(badge).fontWeight,
      };
    });
    expect(metrics.headerHeight).toBe(40);
    expect(metrics.headerWeight).toBe("500");
    expect(metrics.rowHeight).toBeGreaterThanOrEqual(44);
    expect(metrics.primaryWeight).toBe("600");
    expect(metrics.primaryColor).not.toBe(metrics.secondaryColor);
    expect(metrics.badgeHeight).toBe(24);
    expect(metrics.badgeWeight).toBe("600");
  });
}
