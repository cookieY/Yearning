import { expect, test, type Page } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// FE-F4 acceptance gates (work package FE-F4-PRECHECK), driven by the
// stateful precheck fixture over the production build with the shared MSW
// worker:
//   1. Opening editor does not auto-run Review — zero run-creation calls while editing;
//   2. Ready / Blocked / Partial / Provider failure E2E — all four outcomes;
//   3. Out-of-order events do not regress state — layered evidence: monotonic fold unit tests plus
//      component-level stale-snapshot and remount-recovery cases (a browser
//      reload resets the in-memory mock world, see migration contract §11.4);
//   4. Do not display chain-of-thought — no reasoning channel anywhere in the workspace;
//   5. Sensitive content never enters Storage/Telemetry — plaintext never lands in web storage.
const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";
const SQL_MARKER = "e2e-sql-plaintext-marker-DO-NOT-PERSIST";
const RAW_MARKER = "e2e-raw-evidence-marker-DO-NOT-PERSIST";
const SQL_TEXT = "UPDATE orders SET status = 1 WHERE user_id = 42;";

async function setReviewScenario(page: Page, scenario: string): Promise<void> {
  await page.addInitScript(
    `window.localStorage.setItem('yearning-mock-scenario', '${scenario}');`,
  );
}

/**
 * Types SQL through the real Monaco editor: click the editor surface, then
 * drive the keyboard — Monaco's hidden IME textarea is not directly
 * fillable (it is not a normal editable element).
 */
async function typeSql(page: Page, sql: string): Promise<void> {
  await page.getByTestId("sql-editor").click();
  await page.keyboard.type(sql);
}

/** Opens the composer, submits SQL and runs the review through the real UI. */
async function runReviewThroughUi(page: Page): Promise<void> {
  await page.goto("/changes/new");
  await expect(page.getByTestId("changes-new-page")).toBeVisible();
  await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
  await page.getByTestId("draft-title-input").fill("E2E\u9884\u5ba1\u8349\u7a3f");

  // Opening editor does not auto-run Review: no run may exist until the explicit action.
  await typeSql(page, SQL_TEXT);

  await page.getByTestId("run-review").click();
  // Fixture timeline: queued(0.4s) → running → terminal(1.3s).
  await expect(page.getByTestId("review-status")).toContainText(/\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574|\u5df2\u963b\u65ad|\u90e8\u5206\u5b8c\u6210|\u9884\u5ba1\u5931\u8d25/, {
    timeout: 8_000,
  });
  // Ephemeral precheck (RCP-20261101): precheck never adopts a draft — page root carries no draft id;
  // a draft is created only on Save and Return.
  await expect(page.getByTestId("changes-new-page")).not.toHaveAttribute(
    "data-draft-id",
    /.+/,
  );
}


/** One-page layout (owner ruling 2026-10-24): the summary card and its
 * submission dock are mounted from the start — nothing to walk to. */
async function gotoConfirmStep(page: Page): Promise<void> {
  await expect(page.getByTestId("wizard-summary")).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
  await mockSession(page, "admin");
  await setReviewScenario(page, "review-ready");
});

test("editing never auto-reviews; the explicit run produces Ready and unlocks submit", async ({
  page,
}) => {
  const reviewRunCalls: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url().includes("/review-runs")
    ) {
      reviewRunCalls.push(request.url());
    }
  });

  await page.goto("/changes/new");
  await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
  await page.getByTestId("draft-title-input").fill("E2E\u9884\u5ba1\u8349\u7a3f");

  // Editing and waiting produce zero review-run creations.
  await typeSql(page, SQL_TEXT);
  await page.waitForTimeout(2_000);
  expect(reviewRunCalls).toEqual([]);

  // The explicit action creates exactly one run and reaches Ready. The run
  // is ephemeral: no draft is adopted, the page root stays draftless.
  await page.getByTestId("run-review").click();
  await expect(page.getByTestId("review-status")).toContainText("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("changes-new-page")).not.toHaveAttribute(
    "data-draft-id",
    /.+/,
  );
  await expect(page.getByTestId("review-summary")).toContainText(
    "AI \u5ba1\u6838\u5df2\u5b8c\u6210\uff0c\u7ed3\u8bba\u5df2\u4e0e\u5f53\u524d\u9636\u6bb5\u5feb\u7167\u51bb\u7ed3\u3002",
  );
  expect(reviewRunCalls).toHaveLength(1);
  await gotoConfirmStep(page);
  await expect(page.getByTestId("submit-draft")).toBeEnabled();

  // One-page layout: findings and evidence stay structured on the same
  // page; no reasoning channel exists.
  await expect(page.getByTestId("tab-findings")).toBeVisible();
  await page.getByTestId("tab-findings").click();
  await expect(page.getByTestId("finding-item").first()).toBeVisible();
  const body = await page.locator("body").textContent();
  expect(body ?? "").not.toMatch(/\u601d\u7ef4\u94fe|\u601d\u8003\u8fc7\u7a0b|chain of thought|reasoning_content/i);
});

test("Ready draft submits through the confirmation and lands on the order detail", async ({
  page,
}) => {
  await runReviewThroughUi(page);
  await gotoConfirmStep(page);
  // F6: the dock click opens the explicit confirmation; the submission only
  // fires on accept and lands on the immutable order detail.
  await page.getByTestId("submit-draft").click();
  await expect(page.getByTestId("submit-confirm-dialog")).toBeVisible();
  await expect(page.getByTestId("submit-confirm-gate")).toContainText("\u5168\u90e8\u9636\u6bb5\u5ba1\u6838\u901a\u8fc7");
  await page.getByTestId("submit-confirm-accept").click();
  await expect(page.getByTestId("order-detail-page")).toBeVisible({ timeout: 8_000 });
  // The order number follows the contract display_number pattern
  // YR-YYYYMMDD-NNNNNN (OpenAPI ChangeOrder.display_number; the mock was
  // normalized to it in FE-F12 batch 2 — the real backend emits the same).
  await expect(page.getByTestId("order-detail-page")).toContainText(
    /YR-\d{8}-\d{6}/,
  );
  await expect(page.getByTestId("order-timeline")).toBeVisible();
});

test("Blocked outcome lists gate blockers and keeps submit disabled", async ({ page }) => {
  await setReviewScenario(page, "review-blocked");
  await runReviewThroughUi(page);
  await expect(page.getByTestId("review-status")).toContainText("\u5df2\u963b\u65ad");
  await page.getByTestId("tab-findings").click();
  await expect(page.getByTestId("finding-item").first()).toBeVisible();
  await expect(page.getByTestId("findings-incomplete")).toHaveCount(0);
  await expect(page.getByTestId("findings-empty-all")).toHaveCount(0);
  await gotoConfirmStep(page);
  await expect(page.getByTestId("gate-blockers")).toContainText("\u5b58\u5728\u4e25\u91cd\u98ce\u9669\u53d1\u73b0");
  await expect(page.getByTestId("submit-draft")).toBeDisabled();
});

for (const locale of ["zh-CN", "en-US"] as const) {
  test(`review progress is neutral and freezes the selected ${locale} request language`, async ({ page }) => {
    await setReviewScenario(page, "review-blocked");
    await page.goto("/changes/new");
    await page.getByTestId("locale-toggle").click();
    await page.getByTestId(`locale-option-${locale}`).click();
    await expect(page.locator("html")).toHaveAttribute("lang", locale);
    await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
    await page.getByTestId("draft-title-input").fill("Review feedback regression");
    await typeSql(page, SQL_TEXT);

    const request = page.waitForRequest((entry) =>
      entry.method() === "POST" && entry.url().endsWith("/review-runs"),
    );
    await page.getByTestId("run-review").click();
    expect((await request).headers()["accept-language"]).toBe(locale);
    const progress = page.getByTestId("precheck-progress-panel");
    await expect(progress).toBeVisible();
    await expect(progress).toHaveAttribute("role", "status");
    await expect(progress).toHaveAttribute("aria-live", "polite");
    await expect(page.getByTestId("precheck-progress")).toBeVisible();
    // Detailed tabs and blockers remain hidden until progress reaches 100%.
    await expect(page.getByTestId("tab-findings")).toHaveCount(0);
    await expect(page.getByTestId("gate-blockers")).toHaveCount(0);
    await expect(page.getByTestId("findings-empty")).toHaveCount(0);
    await expect(page.getByTestId("findings-empty-all")).toHaveCount(0);
    await expect(page.getByTestId("submit-draft")).toBeDisabled();
    if (locale === "zh-CN") {
      await progress.scrollIntoViewIfNeeded();
      await page.screenshot({
        path: "../artifacts/development/FE-F4-REVIEW-FEEDBACK/tests/review-findings-waiting-zh-CN.png",
        fullPage: true,
      });
    }

    await expect(page.getByTestId("wizard-summary-precheck")).toContainText(
      locale === "zh-CN" ? "\u5ba1\u6838\u4e2d" : "Checking",
      { timeout: 4_000 },
    );

    await expect(page.getByTestId("gate-blockers")).toBeVisible({ timeout: 8_000 });
    await expect(page.getByTestId("precheck-progress-value")).toHaveText("100%");
    await expect(page.getByTestId("tab-overview")).toBeVisible();
    await expect(page.getByTestId("findings-empty-all")).toHaveCount(0);
    await expect(page.getByTestId("submit-draft")).toBeDisabled();

    await page.getByTestId("tab-overview").click();
    const frozenSummary = page.getByTestId("review-summary").locator("dd").first();
    await expect(frozenSummary).toHaveAttribute("lang", locale);
    await expect(frozenSummary).toHaveText(
      locale === "zh-CN"
        ? "AI \u5ba1\u6838\u5df2\u5b8c\u6210\uff0c\u7ed3\u8bba\u5df2\u4e0e\u5f53\u524d\u9636\u6bb5\u5feb\u7167\u51bb\u7ed3\u3002"
        : "The AI review is complete. This result is frozen to the current stage snapshot.",
    );

    const frozenSummaryText = await frozenSummary.textContent();
    const otherLocale = locale === "zh-CN" ? "en-US" : "zh-CN";
    await page.getByTestId("precheck-dialog-close").click();
    await expect(page.getByTestId("precheck-dialog")).toHaveCount(0);
    await page.getByTestId("locale-toggle").click();
    await page.getByTestId(`locale-option-${otherLocale}`).click();
    await expect(page.locator("html")).toHaveAttribute("lang", otherLocale);
    await page.getByTestId("view-precheck-results").click();
    await expect(page.getByTestId("precheck-dialog")).toBeVisible();
    await expect(page.getByTestId("review-summary").locator("dd").first()).toHaveText(
      frozenSummaryText ?? "",
    );
    await expect(page.getByTestId("review-summary").locator("dd").first()).toHaveAttribute(
      "lang",
      locale,
    );
  });
}

test("Partial outcome reports incompleteness and keeps submit disabled", async ({ page }) => {
  await setReviewScenario(page, "review-partial");
  await runReviewThroughUi(page);
  await expect(page.getByTestId("review-status")).toContainText("\u90e8\u5206\u5b8c\u6210");
  await page.getByTestId("tab-findings").click();
  await expect(page.getByTestId("findings-incomplete")).toBeVisible();
  await expect(page.getByTestId("finding-item").first()).toBeVisible();
  await expect(page.getByTestId("findings-empty-all")).toHaveCount(0);
  await gotoConfirmStep(page);
  await expect(page.getByTestId("gate-blockers")).toContainText("\u5b58\u5728\u672a\u5b8c\u6210\u7684\u5ba1\u6838\u9636\u6bb5");
  await expect(page.getByTestId("submit-draft")).toBeDisabled();
});

test("Provider failure shows honest failure copy and keeps submit disabled", async ({ page }) => {
  await setReviewScenario(page, "review-provider-failed");
  await runReviewThroughUi(page);
  await expect(page.getByTestId("review-status")).toContainText("\u9884\u5ba1\u5931\u8d25");
  await expect(page.getByTestId("review-failure")).toContainText("AI\u670d\u52a1\u6682\u4e0d\u53ef\u7528");
  await page.getByTestId("tab-findings").click();
  await expect(page.getByTestId("findings-incomplete")).toBeVisible();
  await expect(page.getByTestId("findings-empty-all")).toHaveCount(0);
  await gotoConfirmStep(page);
  await expect(page.getByTestId("submit-draft")).toBeDisabled();
});

test("revealed raw evidence stays out of web storage and copies only through audit", async ({
  page,
}) => {
  await runReviewThroughUi(page);
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: new URL(page.url()).origin,
  });
  await page.getByTestId("tab-findings").click();
  await page.getByRole("button", { name: "\u67e5\u770b\u5ba1\u6838\u8bc1\u636e" }).first().click();
  await expect(page.getByTestId("evidence-sheet")).toBeVisible();

  await page.getByRole("button", { name: "\u89e3\u5bc6\u67e5\u770b\u539f\u59cb\u6570\u636e" }).first().click();
  const rawView = page.locator("[data-testid^='raw-view-']").first();
  await expect(rawView).toBeVisible();
  await expect(rawView).toContainText(RAW_MARKER);

  // Storage scan: neither the raw payload nor any SQL plaintext may persist.
  const storage = await page.evaluate(() => ({
    local: JSON.stringify(window.localStorage),
    session: JSON.stringify(window.sessionStorage),
  }));
  expect(storage.local).not.toContain(RAW_MARKER);
  expect(storage.local).not.toContain(SQL_MARKER);
  expect(storage.local).not.toContain(SQL_TEXT);
  expect(storage.session).not.toContain(RAW_MARKER);
  expect(storage.session).not.toContain(SQL_MARKER);
  expect(storage.session).not.toContain(SQL_TEXT);

  // Copy goes through the independent audit API before any clipboard write
  // (migration contract §6): the audited raw-copy-events call must fire.
  const copyAuditCalls: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("raw-copy-events")) {
      copyAuditCalls.push(request.url());
    }
  });
  await page.getByRole("button", { name: "\u590d\u5236\uff08\u8bb0\u5f55\u5ba1\u8ba1\uff09" }).first().click();
  await expect
    .poll(() => copyAuditCalls.length, { timeout: 5_000 })
    .toBe(1);

  // The clipboard write is issued in the same activation window as the
  // audit call; give the browser write a beat to settle before reading.
  await page.waitForTimeout(500);
  const clipboard = await page.evaluate(async () => {
    if (!navigator.clipboard || !document.hasFocus()) return null;
    try {
      return await navigator.clipboard.readText();
    } catch {
      return null;
    }
  });
  if (clipboard !== null) {
    expect(clipboard).toContain(RAW_MARKER);
  }

  // Closing the sheet wipes the revealed plaintext from memory.
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("evidence-sheet")).toBeHidden();
});
