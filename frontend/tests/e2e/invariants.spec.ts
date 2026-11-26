import { expect, test } from "@playwright/test";
import { setMockIdentity } from "./helpers/auth";
import { SEEDED_EXECUTION_ORDER } from "./helpers/key-pages";
import {
  assertOverlayInvariants,
  assertPageGeometry,
  bootstrapAuditPage,
  collectVisibleOverlayTriggers,
  installShiftTracker,
  readShiftScore,
  type AuditLocale,
  type AuditViewport,
} from "./helpers/invariant-probes";

const VIEWPORTS: readonly AuditViewport[] = [
  { width: 1440, height: 900 },
  { width: 1024, height: 768 },
];
const LOCALES: readonly AuditLocale[] = ["zh-CN", "en-US"];

const MAIN_ROUTES: readonly { path: string; ready: string; scenario?: string }[] = [
  { path: "/workspace", ready: '[data-testid="home-announcement"]', scenario: "query-session" },
  { path: "/changes/new", ready: '[data-testid="changes-new-page"]', scenario: "review-ready" },
  { path: "/changes/mine", ready: '[data-testid="mine-page"]', scenario: "execution-partial" },
  { path: "/approvals/changes", ready: '[data-testid="approval-queue-page"]', scenario: "review-ready" },
  { path: "/query", ready: '[data-testid="query-entry"]', scenario: "query-flow" },
  { path: "/admin/datasources", ready: '[data-testid="ds-create"]' },
  { path: "/admin/review-engine/providers", ready: '[data-testid="provider-create"]' },
  { path: "/admin/identity-providers", ready: '[data-testid="admin-idp-page"]' },
  { path: "/admin/flows", ready: '[data-testid="admin-flows-page"]' },
];

for (const viewport of VIEWPORTS) {
  for (const locale of LOCALES) {
    const matrixLabel = `${String(viewport.width)}x${String(viewport.height)} ${locale}`;

    test(`layout geometry and overflow invariants hold across main routes (${matrixLabel})`, async ({
      page,
    }) => {
      await bootstrapAuditPage(page, {
        viewport,
        locale,
        session: "admin",
        identity: "henry",
      });
      await page.goto("/workspace");

      for (const route of MAIN_ROUTES) {
        await page.evaluate((scenario) => {
          if (scenario !== undefined) {
            window.localStorage.setItem("yearning-mock-scenario", scenario);
          } else {
            window.localStorage.removeItem("yearning-mock-scenario");
          }
        }, route.scenario);

        await page.goto(route.path);
        await expect(page.locator(route.ready)).toBeVisible({ timeout: 10_000 });
        await page.evaluate(() => document.fonts.ready);
        await assertPageGeometry(page, `${matrixLabel} ${route.path}`);
      }
    });

    test(`long unspaced SSRF/DSN error wraps cleanly without shifting row actions (${matrixLabel})`, async ({
      page,
    }) => {
      const longUnspacedDetail =
        "connection_refused_ssrf_guard:" +
        "db-prod-shard-01.internal.corp.example.com:3306/app_schema?tls=required&connect_timeout=5000&ssl_verify_mode=strict&resolved_ips=".padEnd(
          265,
          "x",
        );

      await bootstrapAuditPage(page, { viewport, locale, session: "admin", identity: "henry" });
      await page.addInitScript((detail) => {
        const origFetch = window.fetch.bind(window);
        window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
          const url =
            typeof input === "string"
              ? input
              : input instanceof URL
                ? input.href
                : input.url;
          const method = (
            init?.method ??
            (typeof input !== "string" && !(input instanceof URL) ? input.method : "GET")
          ).toUpperCase();
          if (
            method === "POST" &&
            /\/api\/v4\/admin\/datasources\/[^/]+\/connection-tests$/.test(url)
          ) {
            return new Response(
              JSON.stringify({
                err_code: 3010,
                message: "DATASOURCE_UNAVAILABLE",
                data: null,
                request_id: "11111111-1111-4111-8111-111111111111",
                retryable: true,
                meta: { detail },
              }),
              { status: 200, headers: { "Content-Type": "application/json" } },
            );
          }
          return origFetch(input, init);
        };
      }, longUnspacedDetail);

      await page.goto("/admin/datasources");
      const firstRow = page.locator('[data-testid^="ds-row-"]').first();
      await expect(firstRow).toBeVisible({ timeout: 10_000 });

      const firstTestId = await firstRow.getAttribute("data-testid");
      const dsId = (firstTestId ?? "").replace(/^ds-row-/, "");
      const actionsBtn = page.getByTestId(`ds-actions-${dsId}`);
      await actionsBtn.scrollIntoViewIfNeeded();
      const beforeBox = await actionsBtn.boundingBox();
      expect(beforeBox).not.toBeNull();

      await actionsBtn.click();
      const menu = page.locator('[data-slot="dropdown-menu-content"]');
      await assertOverlayInvariants(page, menu, `${matrixLabel} ds row menu`);
      await page.getByTestId(`ds-test-button-${dsId}`).click();

      const errorDetail = page.getByTestId(`ds-test-error-detail-${dsId}`);
      await expect(errorDetail).toBeVisible({ timeout: 5_000 });
      await assertPageGeometry(page, `${matrixLabel} ds long error`);

      const metrics = await page.evaluate((id) => {
        const btn = document.querySelector(`[data-testid="ds-actions-${id}"]`);
        const table = btn?.closest("table");
        const detailEl = document.querySelector(`[data-testid="ds-test-error-detail-${id}"]`);
        return {
          vpW: window.innerWidth,
          btnRight: btn?.getBoundingClientRect().right ?? 0,
          tableRight: table?.getBoundingClientRect().right ?? window.innerWidth,
          detailRight: detailEl?.getBoundingClientRect().right ?? 0,
          scrollW: document.documentElement.scrollWidth,
        };
      }, dsId);

      expect(metrics.scrollW).toBeLessThanOrEqual(metrics.vpW + 1);
      expect(metrics.detailRight).toBeLessThanOrEqual(metrics.tableRight + 2);
      expect(metrics.btnRight).toBeLessThanOrEqual(metrics.vpW + 1);
      if (beforeBox !== null) {
        expect(Math.abs(metrics.btnRight - (beforeBox.x + beforeBox.width))).toBeLessThanOrEqual(2);
      }
    });

    test(`overlay portals stay unclipped inside viewport with zero trigger gaps (${matrixLabel})`, async ({
      page,
    }) => {
      const coveredTriggers = new Set<string>([
        "filter-keyword",
        "approval-queue-search",
        "draft-requested-for",
        "wizard-target-database",
        "execution-schedule-input",
        "provider-model",
        "ds-actions-",
      ]);

      await bootstrapAuditPage(page, {
        viewport,
        locale,
        session: "admin",
        identity: "henry",
        scenario: "review-ready",
      });

      // 1. AdminProvidersPage model dropdown portal + outside-click dialog safety
      await page.goto("/admin/review-engine/providers");
      await expect(page.getByTestId("provider-create")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("provider-create").click();
      await expect(page.getByTestId("provider-base-url")).toBeVisible();
      await page.getByTestId("provider-base-url").fill("https://api.deepseek.test");
      await page.getByTestId("provider-api-key").fill("sk-catalog");
      await page.getByTestId("provider-model").click();
      const modelOptions = page.getByTestId("provider-model-options");
      await assertOverlayInvariants(page, modelOptions, `${matrixLabel} provider model options`);
      await page.getByTestId("provider-model-option-mock-chat-pro").dispatchEvent("mousedown");
      await expect(page.getByTestId("provider-model")).toHaveValue("mock-chat-pro");
      await expect(page.getByTestId("provider-base-url")).toBeVisible();
      await page.keyboard.press("Escape");

      // 2. GlobalSearchBar listbox & Calendar date panel on /changes/mine and /approvals/changes
      await page.goto("/changes/mine");
      await expect(page.getByTestId("mine-page")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("filter-keyword").click();
      await assertOverlayInvariants(
        page,
        page.getByTestId("filter-keyword-menu"),
        `${matrixLabel} mine search menu`,
      );
      const dateOption = page.getByRole("option", {
        name: locale === "zh-CN" ? "\u63d0\u4ea4\u8d77\u59cb\u65e5\u671f" : "Submitted from",
      });
      await dateOption.click();
      await assertOverlayInvariants(
        page,
        page.getByTestId("filter-keyword-menu"),
        `${matrixLabel} mine search calendar`,
      );
      await page.keyboard.press("Escape");

      await page.goto("/approvals/changes");
      await expect(page.getByTestId("approval-queue-page")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("approval-queue-search").click();
      await assertOverlayInvariants(
        page,
        page.getByTestId("approval-queue-search-menu"),
        `${matrixLabel} approval search menu`,
      );
      await page.keyboard.press("Escape");

      // 3. SubmissionWizard DateTimePicker & target DB Select
      await page.goto("/changes/new");
      await expect(page.getByTestId("changes-new-page")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("use-flow-4f6f1a2b-0000-4000-8000-000000000001").click();
      await expect(page.getByTestId("draft-requested-for")).toBeVisible();
      await page.getByTestId("draft-requested-for").click();
      await assertOverlayInvariants(
        page,
        page.getByTestId("draft-requested-for-panel"),
        `${matrixLabel} wizard DateTimePicker`,
      );
      await page.keyboard.press("Escape");

      await page.getByTestId("wizard-target-database").click();
      await assertOverlayInvariants(
        page,
        page.locator('[data-slot="select-content"]').first(),
        `${matrixLabel} wizard target DB select`,
      );
      await page.keyboard.press("Escape");

      // 4. ExecutionCard DateTimePicker on detail route
      await page.addInitScript("window.localStorage.setItem('yearning-mock-scenario', 'execution-partial');");
      await setMockIdentity(page, "executor-a");
      await page.goto(SEEDED_EXECUTION_ORDER);
      await expect(page.getByTestId("execution-action-card")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("execution-schedule-input").click();
      await assertOverlayInvariants(
        page,
        page.getByTestId("execution-schedule-input-panel"),
        `${matrixLabel} execution DateTimePicker`,
      );
      await page.keyboard.press("Escape");

      // Trigger discovery ratchet: every remaining visible trigger stays clean when opened
      const remaining = await collectVisibleOverlayTriggers(page, coveredTriggers);
      for (const triggerKey of remaining) {
        if (triggerKey.startsWith("user-menu") || triggerKey.includes("账户") || triggerKey.includes("Account")) {
          await page.getByTestId("user-menu-trigger").click();
          await page.waitForTimeout(320);
          const popup = page.locator('[data-slot="sheet-content"], [data-slot="popover-content"], [data-slot="dropdown-menu-content"]').first();
          await assertOverlayInvariants(page, popup, `${matrixLabel} ${triggerKey}`);
          await page.keyboard.press("Escape");
          await page.waitForTimeout(220);
          coveredTriggers.add(triggerKey);
        } else if (triggerKey === "locale-toggle") {
          await page.getByTestId("locale-toggle").click();
          await assertOverlayInvariants(page, page.getByTestId("locale-menu"), `${matrixLabel} locale menu`);
          await page.keyboard.press("Escape");
          coveredTriggers.add(triggerKey);
        } else if (triggerKey === "database-assistant-trigger") {
          await page.getByTestId("database-assistant-trigger").click();
          await page.waitForTimeout(260);
          await assertOverlayInvariants(
            page,
            page.getByTestId("database-assistant-desktop-popover"),
            `${matrixLabel} database assistant`,
          );
          await page.keyboard.press("Escape");
          await expect(page.getByTestId("database-assistant-desktop-popover")).toBeHidden();
          coveredTriggers.add(triggerKey);
        }
      }
      const leftover = await collectVisibleOverlayTriggers(page, coveredTriggers);
      expect(leftover, `${matrixLabel}: uncovered overlay triggers found`).toEqual([]);
    });

    test(`post-interaction anti-jitter stays within 2px and 0.02 CLS (${matrixLabel})`, async ({
      page,
    }) => {
      await bootstrapAuditPage(page, {
        viewport,
        locale,
        session: "admin",
        identity: "henry",
        scenario: "execution-partial",
      });

      // GlobalSearchBar calendar open/close jitter check
      await page.goto("/changes/mine");
      const heading = page.locator("h1").first();
      await expect(heading).toBeVisible({ timeout: 10_000 });
      const beforeTop = (await heading.boundingBox())?.y ?? 0;

      await installShiftTracker(page);
      await page.getByTestId("filter-keyword").click();
      await page
        .getByRole("option", {
          name: locale === "zh-CN" ? "\u63d0\u4ea4\u8d77\u59cb\u65e5\u671f" : "Submitted from",
        })
        .click();
      await expect(page.getByTestId("filter-keyword-menu")).toBeVisible();
      await page.keyboard.press("Escape");
      await page.waitForTimeout(200);

      const afterTop = (await heading.boundingBox())?.y ?? 0;
      const shiftMine = await readShiftScore(page);
      expect(Math.abs(afterTop - beforeTop), `${matrixLabel}: heading shifted`).toBeLessThanOrEqual(2);
      expect(shiftMine, `${matrixLabel}: search bar CLS`).toBeLessThanOrEqual(0.02);

      // AdminProvidersPage dialog + model popup jitter check
      await page.goto("/admin/review-engine/providers");
      await expect(page.getByTestId("provider-create")).toBeVisible({ timeout: 10_000 });
      await page.getByTestId("provider-create").click();
      await expect(page.getByTestId("provider-base-url")).toBeVisible();
      await page.getByTestId("provider-base-url").fill("https://api.deepseek.test");
      await page.getByTestId("provider-api-key").fill("sk-catalog");
      await page.waitForTimeout(250);
      const modalTitle = page.locator('[data-slot="dialog-title"]').first();
      const titleBefore = (await modalTitle.boundingBox())?.y ?? 0;

      await installShiftTracker(page);
      await page.getByTestId("provider-model").click();
      await expect(page.getByTestId("provider-model-options")).toBeVisible({ timeout: 5_000 });
      await page.waitForTimeout(200);
      const titleAfter = (await modalTitle.boundingBox())?.y ?? 0;
      const shiftProvider = await readShiftScore(page);
      expect(Math.abs(titleAfter - titleBefore), `${matrixLabel}: modal title shifted`).toBeLessThanOrEqual(2);
      expect(shiftProvider, `${matrixLabel}: provider dropdown CLS`).toBeLessThanOrEqual(0.02);
    });
  }
}
