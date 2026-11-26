import { expect, test } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// FE-F2/F3 shell e2e: the logged-in shell (sidebar, header, footer) renders
// with Yearning content over the production build with the shared MSW worker.
// Sessions are real cookie sessions produced by the mock auth handlers.
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
  await mockSession(page, "admin");
  await page.goto("/workspace");
  await page.evaluate(() => document.fonts.ready);
});

test("the shell renders navigation groups, header actions and the workspace dashboard", async ({
  page,
}) => {
  await expect(page.getByText("\u5de5\u4f5c\u53f0", { exact: true })).toBeVisible();
  await expect(page.getByText("\u5ba1\u8ba1", { exact: true })).toBeVisible();
  await expect(page.getByText("\u5ba1\u6838\u5f15\u64ce")).toBeVisible();
  // FE-F10 replaces the honest placeholder with the real dashboard cards
  // (admin session additionally renders the administration statistics).
  await expect(page.getByTestId("workspace-page")).toBeVisible();
});

test("admin navigation follows the server capability", async ({ page }) => {
  // Admin session (can_access_admin=true from /users/me): admin groups show.
  await expect(page.getByRole("link", { name: "\u7528\u6237" })).toBeVisible();
  await expect(page.getByRole("link", { name: "\u6570\u636e\u6e90" })).toBeVisible();

  // Zero-permission session: the same UI hides them — presentation only.
  await page.context().clearCookies();
  await mockSession(page, "default");
  await page.goto("/workspace");
  await expect(page.getByText("\u7b49\u5f85\u7ba1\u7406\u5458\u914d\u7f6e\u6743\u9650")).toBeVisible();
  await expect(page.getByRole("link", { name: "\u7528\u6237" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "\u6570\u636e\u6e90" })).toHaveCount(0);
});

test("the admin capability guard blocks /admin/users for zero-permission users", async ({
  page,
}) => {
  await page.context().clearCookies();
  await mockSession(page, "default");
  await page.goto("/admin/users");
  await expect(page.getByText("\u65e0\u6743\u8bbf\u95ee")).toBeVisible();
});

test("the global footer shows the exact license line and stays below the content", async ({
  page,
}) => {
  await expect(page.getByTestId("workspace-page")).toBeVisible();
  // One synchronous measurement: the dashboard queries resolve continuously
  // and shift the layout, so two separate boundingBox() calls could sample
  // different layout generations and compare unrelated positions.
  const boxes = await page.evaluate(() => {
    const footer = document.querySelector("footer");
    const cards = document.querySelector("[data-testid='workspace-page']");
    if (!footer || !cards) return null;
    const top = (element: Element) => element.getBoundingClientRect().top + window.scrollY;
    return { footer: top(footer), cards: top(cards) };
  });
  if (!boxes) {
    throw new Error("footer or workspace placeholder is not rendered");
  }
  expect(boxes.footer).toBeGreaterThanOrEqual(boxes.cards);
});

test("the footer carries the sponsor and docs links as plain text", async ({ page }) => {
  const sponsor = page.getByRole("link", { name: "\u8d5e\u52a9" });
  const docs = page.getByRole("link", { name: "\u6587\u6863" });
  await expect(sponsor).toHaveAttribute("href", "https://next.yearning.io/zh/about/w5jt71jw/");
  await expect(sponsor).toHaveAttribute("target", "_blank");
  await expect(docs).toHaveAttribute("href", "https://next.yearning.io/");
  await expect(docs).toHaveAttribute("target", "_blank");
  // Owner ruling: the links read as plain text — no underline, and the color
  // must not change on hover.
  for (const link of [sponsor, docs]) {
    await expect(link).toHaveCSS("text-decoration-line", "none");
    const before = await link.evaluate((element) => getComputedStyle(element).color);
    await link.hover();
    const after = await link.evaluate((element) => getComputedStyle(element).color);
    expect(after).toBe(before);
  }
});

test("the language picker switches the locale and persists the choice", async ({ page }) => {
  // Note: this spec's beforeEach re-plants zh-CN on every navigation, so a
  // reload here would mask the persisted choice — the storage value IS the
  // persistence contract (setLocale unit tests cover the resolver).
  await page.getByTestId("locale-toggle").click();
  await expect(page.getByTestId("locale-menu")).toBeVisible();
  await expect(page.getByTestId("locale-option-zh-CN")).toBeVisible();
  await expect(page.getByTestId("locale-option-en-US")).toBeVisible();
  await page.getByTestId("locale-option-en-US").click();
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  expect(await page.evaluate(() => localStorage.getItem("yearning-locale"))).toBe("en-US");
});

test("the header release cluster shows the installed and latest version", async ({ page }) => {
  // RCP-20261020: the release snapshot is backend-fed. Owner ruling 2026-10-23
  // moved the tag out of the sidebar into the header's right-hand action row,
  // next to the theme/language toggles (the manual refresh button was later
  // removed once SSE live updates landed).
  const release = page.getByTestId("header-release");
  await expect(release).toBeVisible();
  await expect(release).toContainText("v4.1.0");
  // The default mock world is up to date, so no "new version" tag renders.
  await expect(release).toContainText("\u5df2\u662f\u6700\u65b0");
  await expect(page.getByTestId("header-refresh")).toHaveCount(0);
});

test("the sidebar collapses to icon mode and back", async ({ page }) => {
  // data-state lives on the inner sidebar element, not the wrapper
  const sidebar = page.locator("[data-slot='sidebar']").first();
  await expect(sidebar).toHaveAttribute("data-state", "expanded");
  await page.getByRole("button", { name: "\u6298\u53e0\u4fa7\u8fb9\u680f" }).click();
  await expect(sidebar).toHaveAttribute("data-state", "collapsed");
  await page.getByRole("button", { name: "\u6298\u53e0\u4fa7\u8fb9\u680f" }).click();
  await expect(sidebar).toHaveAttribute("data-state", "expanded");
});

test("the collapsed sidebar scales the logo and reveals labels on hover", async ({ page }) => {
  // Owner ruling 2026-09-02: the wordmark scales proportionally instead of
  // clipping, and hovering the icon rail widens it back to the full menu with
  // visible labels (template mechanism). Owner ruling 2026-10-23: the sidebar
  // no longer carries a version badge — the release tag lives in the header.
  //
  // Collapsed rendering: the asset is drawn at its natural aspect ratio for a
  // 36px-tall rail (so the mark keeps its proportions instead of squashing) and
  // the wordmark to its right is clipped away by the 36px link box. The
  // *visible* width is therefore the link, not the image — asserting the image
  // box would demand a squashed logo.
  const sidebar = page.locator("[data-slot='sidebar']").first();
  const logoLink = page.locator("[data-slot='sidebar'] a[aria-label]").first();
  const logo = page.locator("[data-slot='sidebar'] img").first();

  await expect(logo).toBeVisible();

  await page.getByRole("button", { name: "\u6298\u53e0\u4fa7\u8fb9\u680f" }).click();
  await expect(sidebar).toHaveAttribute("data-state", "collapsed");
  const collapsedLinkWidth = (await logoLink.boundingBox())?.width ?? 999;
  expect(collapsedLinkWidth).toBeLessThanOrEqual(40);
  // The mark is clipped, not scaled to a sliver: the image keeps its natural
  // aspect ratio, so its box is wider than the rail it is clipped into.
  const collapsedLogoBox = await logo.boundingBox();
  expect(collapsedLogoBox?.height ?? 0).toBeGreaterThan(30);
  expect(collapsedLogoBox?.width ?? 0).toBeGreaterThan(collapsedLinkWidth);

  await page.locator(".sidebar-box").hover();
  await expect(page.getByText("\u6211\u7684\u5de5\u5355")).toBeVisible();

  // Leaving the rail auto-collapses back (hover-initiated expansion).
  await page.mouse.move(700, 400);
  await expect(sidebar).toHaveAttribute("data-state", "collapsed");
});

test("the active sidebar item renders with flat light-theme pill tokens", async ({ page }) => {
  // Flat Light Theme (owner ruling 2026-11-10): soft blue pill (--accent) with
  // primary-accent ink (--accent-foreground); dark mode retains white/black.
  await page.getByRole("link", { name: "\u67e5\u8be2" }).first().click();
  await expect(page).toHaveURL(/\/query$/);
  const active = page.locator("[data-sidebar='menu-button'][data-active]").first();
  await expect(active).toBeVisible();
  const readStyles = (): Promise<{ bg: string; fg: string }> =>
    active.evaluate((element) => {
      const computed = getComputedStyle(element);
      return { bg: computed.backgroundColor, fg: computed.color };
    });
  await expect
    .poll(async () => (await readStyles()).bg, { timeout: 3_000 })
    .toBe("oklch(0.9575 0.0205 254.91)");
  expect((await readStyles()).fg).toBe("oklch(0.4848 0.1683 256.44)");
});

test("the theme toggle switches the document class", async ({ page }) => {
  const html = page.locator("html");
  await page.evaluate(() => window.localStorage.setItem("vite-ui-theme", "light"));
  await page.reload();
  await expect(html).toHaveClass(/light/);
  await page.getByRole("button", { name: "\u5207\u6362\u4e3b\u9898" }).click();
  await expect(html).toHaveClass(/dark/);
});

test("sidebar navigation targets stay consistent with the built route tree", async ({ page }) => {
  // FE-F10 delivers the remaining §9.2 nav targets (/records, /query and
  // the admin entries). Every sidebar entry now resolves to a real page —
  // unknown routes still land on the not-found page (next test).
  await page.getByRole("link", { name: "\u5ba1\u8ba1\u8bb0\u5f55" }).click();
  await expect(page.getByTestId("records-page")).toBeVisible();
  await page.getByRole("link", { name: "\u67e5\u8be2" }).click();
  await expect(page.getByTestId("query-entry")).toBeVisible();
});

test("unknown routes land on the not-found page", async ({ page }) => {
  await page.goto("/definitely-not-a-route");
  await expect(page.getByText(/\u9875\u9762\u4e0d\u5b58\u5728|Page not found/)).toBeVisible();
});
