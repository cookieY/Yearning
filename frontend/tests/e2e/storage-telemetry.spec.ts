import { expect, test, type Page } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// FE-F11 sensitive-storage runtime gate (migration contract §6: plaintext must not enter
// URL, LocalStorage, IndexedDB, or error telemetry; §18 gate: zero findings in SQL/Evidence/Token/Secret
// storage scan). The static counterpart (scripts/scan-browser-storage.mjs)
// pins the storage allowlist in source; this spec proves at runtime that the
// deepest sensitive flows — an authorized SQL reveal on the order detail and
// a masked query execution — leave zero plaintext in any browser storage,
// never contact a third-party host and never echo sensitive material through
// the console.
//
// The client-visible storage allowlist (design: theme, locale, mock scenario
// selectors; session/CSRF ride HttpOnly/same-origin cookies):
const ALLOWED_STORAGE_KEYS = new Set([
  "vite-ui-theme",
  "yearning-locale",
  "yearning-mock-scenario",
  "yearning-mock-auth",
]);
const ALLOWED_COOKIE_NAMES = new Set(["yearning_session", "yearning_csrf", "sidebar_state"]);

interface StorageDump {
  local: Record<string, string>;
  session: Record<string, string>;
  cookies: { name: string; value: string }[];
  idbDatabases: string[];
}

async function dumpStorage(page: Page): Promise<StorageDump> {
  const web = await page.evaluate(() => {
    const collect = (storage: Storage): Record<string, string> => {
      const out: Record<string, string> = {};
      for (let i = 0; i < storage.length; i += 1) {
        const key = storage.key(i);
        if (key !== null) out[key] = storage.getItem(key) ?? "";
      }
      return out;
    };
    return {
      local: collect(window.localStorage),
      session: collect(window.sessionStorage),
    };
  });
  const cookies = await page.context().cookies();
  const idbDatabases = await page.evaluate(async () => {
    if (typeof indexedDB.databases !== "function") return [];
    return (await indexedDB.databases())
      .map((database) => database.name)
      .filter((name): name is string => typeof name === "string");
  });
  return {
    local: web.local,
    session: web.session,
    cookies: cookies.map(({ name, value }) => ({ name, value })),
    idbDatabases,
  };
}

function assertNoSensitiveMaterial(dump: StorageDump, sensitive: string[]): void {
  const surfaces: [string, string][] = [
    ...Object.entries(dump.local),
    ...Object.entries(dump.session),
    ...dump.cookies.map(({ name, value }) => [name, value] as [string, string]),
    ...dump.idbDatabases.map((name) => [name, name] as [string, string]),
  ];
  for (const [surface, value] of surfaces) {
    for (const marker of sensitive) {
      expect(
        value.includes(marker),
        `sensitive material found in ${surface}: marker "${marker.slice(0, 40)}…"`,
      ).toBe(false);
    }
  }
}

test("revealed SQL and query results never reach storage, third parties or the console", async ({
  page,
}) => {
  const consoleMessages: string[] = [];
  page.on("console", (message) => {
    consoleMessages.push(message.text());
  });
  const requestHosts = new Set<string>();
  page.on("request", (request) => {
    requestHosts.add(new URL(request.url()).host);
  });

  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
    window.localStorage.setItem("yearning-mock-scenario", "execution-partial");
  });
  await mockSession(page, "default");

  // Flow 1: authorized SQL reveal on the order detail — plaintext in the DOM
  // (the read-only Monaco viewer renders the revealed statements; §18.45).
  await page.goto("/changes/orders/7e6f1a2b-0000-4000-8000-00000000f801");
  await expect(page.getByTestId("order-sql-card")).toBeVisible();
  await page.getByTestId("reveal-order-sql").click();
  await expect(page.getByTestId("order-sql-readonly")).toBeVisible({ timeout: 15_000 });
  // Monaco tokenizes into spans and word-wrap splits the long line into
  // view-lines whose textContent join may drop the wrapping spaces —
  // normalize whitespace before asserting the plaintext is in the DOM.
  const revealedSql = (await page.getByTestId("order-sql-view").textContent()) ?? "";
  expect(revealedSql.replace(/\s+/g, " ")).toContain("ALTER TABLE orders");

  // Close the viewer: the reveal wipes from memory (unmount contract) — the
  // storage dump below must not find it anywhere.
  await page.getByTestId("close-order-sql").click();
  await expect(page.getByTestId("order-sql-view")).toHaveCount(0);

  // Flow 2: masked query execution — result data stays in component memory.
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-scenario", "query-session");
  });
  await page.goto("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
  await expect(page.getByTestId("query-workspace")).toBeVisible();
  await page.getByTestId("query-schema-input").click();
      await page.getByRole("option", { name: "app" }).click();
  await page.getByTestId("query-sql-editor").click();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.type("select id, email from app.users");
  await page.getByTestId("query-run").click();
  await expect(page.locator("[data-testid^='query-result-loaded-']").first()).toBeVisible({
    timeout: 10_000,
  });
  const firstCell = await page
    .locator("[data-testid^='query-result-'] table tbody td")
    .first()
    .textContent()
    .catch(() => null);

  // Strong full-string markers only: naive word splits ("orders", "note")
  // false-positive on MSW request logs and page URLs.
  const sensitive: string[] = [];
  if (revealedSql !== null) {
    sensitive.push(revealedSql.replace(/\s+/g, " ").trim());
  }
  if (firstCell !== null && firstCell.trim().length > 3) sensitive.push(firstCell.trim());

  const dump = await dumpStorage(page);

  // Storage keys stay on the declared allowlist; no invented key exists.
  for (const key of Object.keys(dump.local)) {
    expect(ALLOWED_STORAGE_KEYS.has(key), `unexpected localStorage key: ${key}`).toBe(true);
  }
  expect(Object.keys(dump.session), "sessionStorage must stay empty").toEqual([]);
  expect(dump.idbDatabases, "no IndexedDB database may exist").toEqual([]);
  for (const cookie of dump.cookies) {
    expect(ALLOWED_COOKIE_NAMES.has(cookie.name), `unexpected cookie: ${cookie.name}`).toBe(true);
  }

  assertNoSensitiveMaterial(dump, sensitive);

  // Telemetry: every request during both flows is same-origin.
  for (const host of requestHosts) {
    expect(host, `third-party transport target: ${host}`).toBe("localhost:4173");
  }

  // The console never echoes the revealed SQL or result data.
  for (const message of consoleMessages) {
    for (const marker of sensitive) {
      expect(message.includes(marker), `console echoed sensitive material: ${message.slice(0, 80)}`).toBe(false);
    }
  }
});

test("admin secret surfaces leave zero client-side secret material", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
  await mockSession(page, "admin");
  await page.goto("/admin/review-engine/providers");
  await expect(page.getByTestId(/provider-row-/).first()).toBeVisible();
  await page.goto("/admin/identity-providers");
  await expect(page.getByTestId("admin-idp-page")).toBeVisible();

  const dump = await dumpStorage(page);
  for (const key of Object.keys(dump.local)) {
    expect(ALLOWED_STORAGE_KEYS.has(key), `unexpected localStorage key: ${key}`).toBe(true);
  }
  expect(Object.keys(dump.session), "sessionStorage must stay empty").toEqual([]);
  expect(dump.idbDatabases, "no IndexedDB database may exist").toEqual([]);
  for (const cookie of dump.cookies) {
    expect(ALLOWED_COOKIE_NAMES.has(cookie.name), `unexpected cookie: ${cookie.name}`).toBe(true);
  }
});
