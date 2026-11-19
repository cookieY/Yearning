import { writeFileSync, mkdirSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { KEY_PAGE_TYPES } from "./helpers/key-pages";
import { mockSession, setMockIdentity } from "./helpers/auth";

// FE-F11 runtime performance gate (migration contract §8: route-level code
// splitting, admin bundle stays off normal user first screen; §18 defines the budgets).
//
// Structural invariants are the regression gates: the normal user's first
// screen never fetches an admin/migration/Monaco chunk, Monaco loads only
// where an editor exists, and no mock chunk is ever requested. Wall-clock
// ceilings use the F5 harness budget family (long tasks < 2s) with generous
// room for shared CI runners — they catch pathological regressions (a heavy
// chunk landing on the first screen), not machine speed.

// Chunk patterns for the per-surface isolation matrix asserted in the test
// body: admin/migration chunks on normal-user pages, Monaco chunks on
// editorless pages, Monaco presence on editor pages.
const MONACO_CHUNK = /\/assets\/(sql-editor-panel-|editor\.worker-)/;

interface PageMetrics {
  page: string;
  readyMs: number;
  longTaskCount: number;
  longTaskTotalMs: number;
  scriptRequests: number;
}

async function instrument(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__longTasks = [] as number[];
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          window.__longTasks.push(entry.duration);
        }
      }).observe({ type: "longtask", buffered: true });
    } catch {
      // longtask observer unsupported in this context
    }
  });
}

const metrics: PageMetrics[] = [];

test.afterAll(() => {
  mkdirSync("tests/performance", { recursive: true });
  writeFileSync(
    "tests/performance/runtime-budget.json",
    `${JSON.stringify(
      {
        generated_at: new Date().toISOString(),
        budgets: {
          ready_ms_per_page: 10_000,
          long_task_total_ms_per_page: 2_000,
          chunk_isolation: "admin/migrations chunks never on normal-user pages; Monaco chunks only on editor pages (precheck, query)",
        },
        measured: metrics,
      },
      null,
      2,
    )}\n`,
  );
});

for (const pageType of KEY_PAGE_TYPES) {
  test(`${pageType.name} loads within budget with isolated chunks`, async ({ page }) => {
    test.setTimeout(30_000);
    const scriptRequests: string[] = [];
    page.on("response", (response) => {
      if (response.url().includes("/assets/") && response.url().endsWith(".js")) {
        scriptRequests.push(response.url());
      }
    });

    await instrument(page);
    await page.addInitScript(() => {
      window.localStorage.setItem("yearning-locale", "zh-CN");
    });
    if (pageType.scenario !== null) {
      await page.addInitScript(
        `window.localStorage.setItem('yearning-mock-scenario', '${pageType.scenario}');`,
      );
    }
    await mockSession(page, pageType.session);
    await setMockIdentity(page, pageType.identity);

    const started = Date.now();
    await pageType.prepare(page);
    const readyMs = Date.now() - started;

    // (Mock-world exclusion is proven by scripts/check-bundle-budget.mjs on
    // the release build; this suite runs against the mock-driven E2E build
    // where the fixture transport legitimately exists.)

    // Chunk isolation per surface (defense in depth over the release-build
    // structural gate): normal-user pages never fetch an admin/migration
    // chunk; pages without an editor never fetch a Monaco chunk either. The
    // workspace trend chart (recharts) is an authenticated-session surface
    // since §18.71 — it legitimately loads on normal-user dashboards.
    const ADMIN_CHUNK = /\/assets\/(admin-|admin-migrations-)/;
    if (pageType.session === "default") {
      for (const url of scriptRequests) {
        expect(url, `admin chunk on normal-user page ${pageType.name}: ${url}`).not.toMatch(
          ADMIN_CHUNK,
        );
      }
    }
    const EDITORLESS = new Set(["dashboard", "list", "detail"]);
    if (EDITORLESS.has(pageType.name)) {
      for (const url of scriptRequests) {
        expect(url, `Monaco chunk on editorless page ${pageType.name}: ${url}`).not.toMatch(
          MONACO_CHUNK,
        );
      }
    }
    if (pageType.name === "precheck" || pageType.name === "query") {
      expect(
        scriptRequests.some((url) => MONACO_CHUNK.test(url)),
        "editor surface never fetched the Monaco chunk",
      ).toBe(true);
    }

    await page.waitForTimeout(300);
    const longTasks = await page.evaluate(() => window.__longTasks ?? []);
    const longTaskTotalMs = longTasks.reduce((sum, duration) => sum + duration, 0);
    expect(readyMs, `${pageType.name} ready in ${readyMs}ms`).toBeLessThan(10_000);
    expect(
      longTaskTotalMs,
      `${pageType.name} long tasks total ${longTaskTotalMs}ms`,
    ).toBeLessThan(2_000);

    metrics.push({
      page: pageType.name,
      readyMs,
      longTaskCount: longTasks.length,
      longTaskTotalMs: Math.round(longTaskTotalMs),
      scriptRequests: scriptRequests.length,
    });
  });
}

declare global {
  interface Window {
    __longTasks: number[];
  }
}
