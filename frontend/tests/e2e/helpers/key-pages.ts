import type { Page } from "@playwright/test";
import { expect } from "@playwright/test";
import type { MockIdentity } from "./auth";

/**
 * FE-F11 six key page types (yearning-ui-design-spec.md §7.1–§7.6): the
 * canonical definition shared by the screenshot baseline and the WCAG audit.
 * Each entry drives the real page into its informative state — the precheck
 * workspace waits for a terminal review, the query workspace has actually
 * executed a SELECT — never a skeleton or an empty shell, so both quality
 * gates judge the pages users actually work in.
 */
export interface KeyPageType {
  /** Short name used in file names and test titles. */
  name: string;
  /** Mock scenario; null keeps the default world. */
  scenario: string | null;
  /** Session identity the page requires. */
  session: "default" | "admin";
  /** Mock identity (§18.44): henry submits, reviewer-a approves, executor-a
   * executes — role surfaces only render for their identity. */
  identity: MockIdentity;
  /** Navigates and drives the page into its informative state. */
  prepare: (page: Page) => Promise<void>;
}

const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";
export const SEEDED_EXECUTION_ORDER = "/changes/orders/7e6f1a2b-0000-4000-8000-00000000f801";

/** Types SQL through the real Monaco surface (precheck.spec precedent). */
async function typeSql(page: Page, testid: string, sql: string): Promise<void> {
  await page.getByTestId(testid).click();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.type(sql);
}

export const KEY_PAGE_TYPES: KeyPageType[] = [
  {
    name: "dashboard",
    scenario: "query-session",
    session: "default",
    identity: "henry",
    prepare: async (page) => {
      await page.goto("/workspace");
      await expect(page.getByTestId("home-announcement")).toBeVisible();
      await expect(page.getByTestId("home-kpi-pending-approval")).toBeVisible();
    },
  },
  {
    // The seeded execution order (submitter = the default session user)
    // only exists in the execution-domain scenarios.
    name: "list",
    scenario: "execution-partial",
    session: "default",
    identity: "henry",
    prepare: async (page) => {
      await page.goto("/changes/mine");
      await expect(page.getByTestId("mine-order-row").first()).toBeVisible();
    },
  },
  {
    name: "detail",
    scenario: "execution-partial",
    session: "default",
    // The execution action card is the executor's surface (§18.44): the
    // seeded order's submitter (henry) never sees it.
    identity: "executor-a",
    prepare: async (page) => {
      await page.goto(SEEDED_EXECUTION_ORDER);
      await expect(page.getByTestId("order-detail-page")).toBeVisible();
      // The seeded order waits in execution_pending: the executor action
      // card with the approved SQL hash is its informative state (the
      // attempt card only exists once an execution has been started).
      await expect(page.getByTestId("execution-action-card")).toBeVisible({
        timeout: 8_000,
      });
    },
  },
  {
    name: "precheck",
    scenario: "review-ready",
    session: "admin",
    identity: "henry",
    prepare: async (page) => {
      await page.goto("/changes/new");
      await expect(page.getByTestId("changes-new-page")).toBeVisible();
      await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
      await page.getByTestId("draft-title-input").fill("\u89c6\u89c9\u57fa\u7ebf\u8349\u7a3f");
      // Ephemeral precheck (RCP-20261101): running the review sends the
      // editor SQL verbatim to POST /review-runs — no draft is created or
      // adopted, the URL never moves, and the page root stays draftless.
      await typeSql(page, "sql-editor", "UPDATE orders SET status = 1 WHERE user_id = 42;");
      await page.getByTestId("run-review").click();
      // The run button disables while queued/running and re-enables exactly
      // when the terminal state lands — a locale-neutral terminal signal
      // (fixture timeline ≈1.3s). The result card is fed by a follow-up read
      // that can land a frame or two later, so also wait for the verdict
      // itself: on a loaded CI runner the capture otherwise catches the empty
      // results card and reads as a one-off baseline diff.
      await expect(page.getByTestId("run-review")).toBeDisabled({ timeout: 5_000 });
      await expect(page.getByTestId("run-review")).toBeEnabled({ timeout: 10_000 });
      await expect(page.getByTestId("review-status")).toBeVisible();
      await expect(page.getByTestId("review-counts")).toBeVisible({ timeout: 10_000 });
      // Sampled AFTER the terminal state settles: a regression to
      // adopt-on-precheck would set data-draft-id inside that window and
      // fail here (an earlier sample would pass vacuously).
      await expect(page.getByTestId("changes-new-page")).not.toHaveAttribute(
        "data-draft-id", /.+/
      );
      // The verdict read and the run button settle independently: capture only
      // a state that holds for two samples, otherwise a busy runner records
      // the disabled button of a follow-up poll and reads as a pixel diff.
      const settled = async (): Promise<boolean> =>
        (await page.getByTestId("run-review").isEnabled()) &&
        (await page.getByTestId("review-counts").isVisible());
      await expect.poll(settled, { timeout: 15_000 }).toBe(true);
      await page.waitForTimeout(200);
      await expect.poll(settled, { timeout: 15_000 }).toBe(true);
    },
  },
  {
    name: "query",
    scenario: "query-session",
    session: "default",
    identity: "henry",
    prepare: async (page) => {
      await page.goto("/query/sessions/qs-fixture-active");
      await expect(page.getByTestId("query-workspace")).toBeVisible();
      await page.getByTestId("query-schema-input").click();
      await page.getByRole("option", { name: "app" }).click();
      await typeSql(page, "query-sql-editor", "select id, email from app.users");
      await page.getByTestId("query-run").click();
      await expect(page.locator("[data-testid^='query-result-loaded-']").first()).toBeVisible({
        timeout: 10_000,
      });
    },
  },
  {
    name: "admin",
    scenario: null,
    session: "admin",
    identity: "henry",
    prepare: async (page) => {
      await page.goto("/admin/datasources");
      await expect(page.getByTestId(/ds-row-/).first()).toBeVisible();
    },
  },
];
