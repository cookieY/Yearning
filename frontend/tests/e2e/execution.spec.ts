import { expect, test, type Page } from "@playwright/test";
import { becomeIdentity, mockSession, setMockIdentity } from "./helpers/auth";

// FE-F8 acceptance gates (Execution Results and Scheduling), driven by the stateful execution
// fixture over the production build with the shared MSW worker:
//   1. Approved and executed SQL Hashes are displayed consistently — the executor area and the attempt card
//      both bind the executed SQL to the approved hash (preflight);
//   2. No new rollback entry — no rollback affordance exists anywhere (E003);
//   3. Non-not_started fates only copy to a new draft — after a sent-boundary fate the only
//      forward path is the copied draft (E004);
//   4. Scheduled authorization and due semantics are accurate — only the frozen executor schedules, the due
//      claim is the system's, and a missed schedule never catches up (E007);
//   5. Unknown is never displayed as not-executed — unknown statement results render in their
//      own high-risk wording and resolve only through manual verification
//      (E005).
//
// Identity role separation (§18.44): the seeded scenario orders freeze
// executor-a in their execution slots, so the executor legs run as that
// identity; the submit-approve-execute chain switches identities in-world
// (logout + login, no reload — a reload would reset the mock world). The
// backend keeps the authoritative checks (3001/3003/3004).

const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";
const SEEDED_EXECUTION_ORDER = "/changes/orders/7e6f1a2b-0000-4000-8000-00000000f801";
const SQL_TEXT = "UPDATE orders SET status = 1 WHERE user_id = 42;";


/** One-page layout (owner ruling 2026-10-24): the summary card and its
 * submission dock are mounted from the start — nothing to walk to. */
async function gotoConfirmStep(page: Page): Promise<void> {
  await expect(page.getByTestId("wizard-summary")).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
    window.localStorage.setItem("yearning-mock-scenario", "ready");
  });
  await mockSession(page, "admin");
});

/** Sets the mock scenario before any app code runs. */
async function useScenario(page: Page, scenario: string): Promise<void> {
  await page.addInitScript((value) => {
    window.localStorage.setItem("yearning-mock-scenario", value);
  }, scenario);
}

/** Submits a ready draft as henry, approves it as reviewer-a and lands on
 * the execution-pending order detail as executor-a (§18.44 role chain). */
async function submitAndApprove(page: Page, title: string): Promise<void> {
  await page.goto("/changes/new");
  await expect(page.getByTestId("changes-new-page")).toBeVisible();
  await page.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).click();
  await page.getByTestId("draft-title-input").fill(title);
  await page.getByTestId("sql-editor").click();
  await page.keyboard.type(SQL_TEXT);
  await page.getByTestId("run-review").click();
  await expect(page.getByTestId("review-status")).toContainText("\u5ba1\u6838\u7ed3\u679c\u5b8c\u6574", {
    timeout: 8_000,
  });
  // Ephemeral precheck (RCP-20261101): precheck does not adopt a draft; submission goes through POST /change-orders
  // carrying {sql, run_id}, and the server compares the reviewed text.
  await expect(page.getByTestId("changes-new-page")).not.toHaveAttribute(
    "data-draft-id",
    /.+/,
  );
  await gotoConfirmStep(page);
  await page.getByTestId("submit-draft").click();
  await page.getByTestId("submit-confirm-accept").click();

  await expect(page.getByTestId("order-detail-page")).toBeVisible({ timeout: 8_000 });

  // Final approval as the frozen reviewer of step 1 (§18.44: the submitter
  // henry is not an actor — switch identity in-world, the order survives;
  // login redirects back to this detail). Approval never auto-executes, the
  // executor area only appears after the executor identity takes over (W006).
  await becomeIdentity(page, "reviewer-a");
  await expect(page.getByTestId("approval-decision-card")).toBeVisible({ timeout: 8_000 });
  await page.getByTestId("approval-approve").click();
  await page.getByTestId("approval-decision-confirm").click();
  await expect(page.getByTestId("order-detail-page")).toContainText("\u7b49\u5f85\u6267\u884c", {
    timeout: 8_000,
  });

  // The executor logs in and lands on the same detail with the executor
  // area; My Orders is submitter-scoped and would never carry them here.
  await becomeIdentity(page, "executor-a");
  await expect(page.getByTestId("execution-action-card")).toBeVisible({ timeout: 8_000 });
}

/** Asserts the execution surface carries no rollback affordance (E003). */
async function expectNoRollbackEntry(page: Page): Promise<void> {
  await expect(page.getByRole("button", { name: /\u56de\u6eda/ })).toHaveCount(0);
  await expect(page.getByRole("link", { name: /\u56de\u6eda/ })).toHaveCount(0);
  await expect(page.getByTestId("rollback-entry")).toHaveCount(0);
}

test("shows the approved hash as the executed hash, with no rollback entry", async ({
  page,
}) => {
  test.setTimeout(40_000);
  await submitAndApprove(page, "\u54c8\u5e0c\u4e00\u81f4\u5c55\u793a\u8349\u7a3f");

  // The executor area binds execution to the approved SQL hash.
  await expect(page.getByTestId("execution-hash-line")).toContainText("\u4e0e\u6279\u51c6\u5feb\u7167\u4e00\u81f4");
  const hashLine = await page.getByTestId("execution-hash-line").textContent();
  expect(hashLine).toContain("hash-");

  await expectNoRollbackEntry(page);

  // Execute: preflight confirms the identical hash on the attempt card.
  await page.getByTestId("execution-start").click();
  await expect(page.getByTestId("execution-confirm-dialog")).toBeVisible();
  await page.getByTestId("execution-confirm-run").click();
  await expect(page.getByTestId("execution-attempt-card")).toContainText(
    "\u6267\u884cSQL\u4e0e\u6279\u51c6SQL\u4e00\u81f4",
    { timeout: 10_000 },
  );
  await expect(page.getByTestId("execution-attempt-card")).toContainText("\u6210\u529f", {
    timeout: 10_000,
  });
  await expect(page).not.toHaveURL(/rollback/);
  await expectNoRollbackEntry(page);
});

test("after a partial DDL fate only copy-to-new-draft remains", async ({ page }) => {
  test.setTimeout(40_000);
  await useScenario(page, "execution-partial");
  // The seeded order's frozen executor is executor-a (§18.44).
  await setMockIdentity(page, "executor-a");
  await page.goto(SEEDED_EXECUTION_ORDER);
  await expect(page.getByTestId("execution-action-card")).toBeVisible({ timeout: 8_000 });

  await page.getByTestId("execution-start").click();
  await page.getByTestId("execution-confirm-run").click();

  // partial_failed facts: succeeded / failed / skipped rows.
  await expect(page.getByTestId("execution-statement-1")).toContainText("\u6210\u529f", {
    timeout: 10_000,
  });
  await expect(page.getByTestId("execution-statement-2")).toContainText("\u5931\u8d25");
  await expect(page.getByTestId("execution-statement-3")).toContainText("\u5df2\u8df3\u8fc7");

  // The order terminalizes; no retry, no rollback — the copied draft only.
  await expect(page.getByTestId("copy-draft-card")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId("execution-start")).toHaveCount(0);
  await expectNoRollbackEntry(page);
});

test("schedules are executor-created and missed schedules never catch up", async ({
  page,
}) => {
  test.setTimeout(40_000);

  // Authorization: the seeded order's frozen executor can create a schedule;
  // the due semantics land the order in `scheduled` awaiting the system
  // claim — never an immediate execution (E007). The window check is real:
  // a due time beyond 30 days would keep the button disabled.
  await useScenario(page, "execution-partial");
  // The seeded order's frozen executor is executor-a (§18.44).
  await setMockIdentity(page, "executor-a");
  await page.goto(SEEDED_EXECUTION_ORDER);
  await expect(page.getByTestId("execution-action-card")).toBeVisible({ timeout: 8_000 });
  const due = new Date(Date.now() + 60 * 60 * 1000);
  const pad = (value: number): string => String(value).padStart(2, "0");
  // §18.56 addendum 2: antd-style picker — input opens panel -> ISO day -> "Time" column toggle -> hour/minute -> confirm.
  await page.getByTestId("execution-schedule-input").click();
  const iso = `${String(due.getFullYear())}-${pad(due.getMonth() + 1)}-${pad(due.getDate())}`;
  await page.getByTestId("execution-schedule-input-panel").getByRole("button", { name: iso, exact: true }).click();
  await page.getByTestId("execution-schedule-input-time-toggle").click();
  await page.getByTestId("dtp-hours").getByRole("button", { name: pad(due.getHours()), exact: true }).click();
  await page.getByTestId("dtp-minutes").getByRole("button", { name: pad(due.getMinutes()), exact: true }).click();
  await page.getByTestId("execution-schedule-input-confirm").click();
  await page.getByTestId("execution-schedule-submit").click();
  // The success note lives on the action card, which unmounts once the order
  // refetches into `scheduled` — assert the persisted surfaces instead: the
  // scheduled badge and the audit timeline entry.
  await expect(page.getByTestId("order-timeline")).toContainText("\u4e0d\u81ea\u52a8\u8865\u8dd1", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("order-detail-page")).toContainText("\u5df2\u9884\u7ea6", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("execution-action-card")).toHaveCount(0);

  // Cancel (owner ruling 2026-10-30): any frozen executor — executor-a here,
  // not necessarily the booker — cancels the live schedule; the order returns
  // to pending execution (fresh schedule or immediate run) and the timeline
  // keeps the cancellation trace for the submitter.
  await expect(page.getByTestId("execution-scheduled-card")).toBeVisible();
  await page.getByTestId("execution-schedule-cancel").click();
  await page.getByTestId("execution-schedule-cancel-confirm").click();
  await expect(page.getByTestId("execution-action-card")).toBeVisible({
    timeout: 8_000,
  });
  await expect(page.getByTestId("execution-scheduled-card")).toHaveCount(0);
  await expect(page.getByTestId("order-timeline")).toContainText("\u53d6\u6d88\u9884\u7ea6", {
    timeout: 8_000,
  });

  // A missed schedule is terminal: the seeded order shows the missed badge
  // and the copy-only path — no catch-up button anywhere.
  await useScenario(page, "schedule-missed");
  await page.goto(SEEDED_EXECUTION_ORDER);
  await expect(page.getByTestId("order-detail-page")).toContainText("\u9519\u8fc7", {
    timeout: 8_000,
  });
  await expect(page.getByTestId("copy-draft-card")).toBeVisible({ timeout: 8_000 });
  await expect(page.getByTestId("execution-start")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /\u8865\u8dd1|\u91cd\u65b0\u6267\u884c|\u91cd\u8bd5/ })).toHaveCount(0);
});

test("unknown statement results are never not-executed and verify manually", async ({
  page,
}) => {
  test.setTimeout(60_000);
  await useScenario(page, "execution-unknown");
  await setMockIdentity(page, "executor-a");
  await page.goto(SEEDED_EXECUTION_ORDER);
  await expect(page.getByTestId("execution-action-card")).toBeVisible({ timeout: 8_000 });

  await page.getByTestId("execution-start").click();
  await page.getByTestId("execution-confirm-run").click();

  // Statement #2 reads result-unknown — a distinct high-risk state, never not-executed.
  await expect(page.getByTestId("execution-statement-2")).toContainText("\u7ed3\u679c\u672a\u77e5", {
    timeout: 10_000,
  });
  await expect(page.getByTestId("execution-statement-2")).not.toContainText("\u672a\u6267\u884c");

  // The manual verification form: fixed verdicts, reason and evidence all
  // mandatory; the backend keeps 3012 authoritative.
  await expect(page.getByTestId("verification-form")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId("verification-submit")).toBeDisabled();
  await page.getByTestId("verification-result-confirmed_succeeded").click();
  await page.getByTestId("verification-reason").fill("\u4ece\u5e93\u590d\u5236\u65e0\u5ef6\u8fdf\uff0c\u884c\u6570\u4e0e\u9884\u671f\u4e00\u81f4");
  await page.getByTestId("verification-evidence-content-0").fill("SELECT COUNT(*) \u590d\u6838\u901a\u8fc7");
  await page.getByTestId("verification-submit").click();

  // The order completes and the manual-confirmation marker persists.
  await expect(page.getByTestId("order-detail-page")).toContainText("\u5df2\u5b8c\u6210", {
    timeout: 10_000,
  });
  await expect(page.getByTestId("order-facts")).toContainText("\u4eba\u5de5\u6838\u9a8c");
});
