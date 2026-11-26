import { expect, test, type Page } from "@playwright/test";
import { mockSession, setMockIdentity } from "./helpers/auth";

/**
 * FE-F10 acceptance gates (work package acceptance_gates):
 *
 * 1. Query allows single SELECT only with unbounded-total cursor pagination — the server's 4007 refusal renders
 *    inline; result footers report rows ALREADY READ from the cursor and
 *    never a fabricated total.
 * 2. Admin revocation is user-perceptible and admin cannot view queries — revocation flips the
 *    workspace into the non-dismissible blocked notice with the reason;
 *    the builtin admin identity sees no query objects (admin_is_not_
 *    business_override) and another user's session URL is refused.
 * 3. Flow/datasource/permission-group form fields match the v4 model — the query-flow form carries
 *    no SQL/rule-set/executor fields; permission groups grant flows only.
 * 4. Migration UI cannot start Apply — the migration workbench gates approval
 *    behind per-candidate confirmation and the APPROVE phrase and never
 *    offers an Apply entry (verified at component level; the route swap
 *    itself is unit-tested — migration mode needs the dedicated server).
 * 5. No undisposed legacy core pages — every remaining §2 nav target renders a real
 *    page and the prohibited scan keeps AutoTask/legacy transfers out.
 */

async function useScenario(page: Page, scenario: string): Promise<void> {
  await page.addInitScript(
    (value) => {
      window.localStorage.setItem("yearning-mock-scenario", value);
    },
    scenario,
  );
}

/** Types SQL through the real Monaco surface (precheck.spec precedent). */
async function typeSql(page: Page, sql: string): Promise<void> {
  const editor = page.getByTestId("query-sql-editor");
  // Monaco is a lazy chunk and this build edits through a
  // `native-edit-context` div (the `<textarea>` is only the hidden IME
  // fallback). A wrapper-relative coordinate click lands in the line-number
  // gutter whenever the container fonts resolve wider — on a cold container
  // the keystrokes are dropped, the model keeps its initial "SELECT "
  // template and the spec fails waiting for a request it never sent. Click
  // inside the lines container, whose origin already excludes the gutter.
  const lines = editor.locator(".monaco-editor .view-lines");
  await expect(lines).toBeVisible({ timeout: 15_000 });
  await lines.click({ position: { x: 200, y: 20 } });
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.type(sql);
  // Monaco tokenizes into spans and wraps long lines, so compare on
  // whitespace-normalized text (storage-telemetry precedent).
  const normalize = (value: string): string => value.replace(/\s+/g, " ").trim();
  await expect
    .poll(async () => normalize((await editor.textContent()) ?? ""), { timeout: 15_000 })
    .toContain(normalize(sql));
}

test.beforeEach(async ({ page }) => {
  // Chinese assertion vocabulary, pinned like the other specs (headless
  // Chromium reports en-US).
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
  await mockSession(page, "default");
});

test("gate 1: single-SELECT only and cursor pagination without totals", async ({ page }) => {
  await useScenario(page, "query-session");
  await page.goto("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
  await expect(page.getByTestId("query-workspace")).toBeVisible();

  // A write statement is refused by the server's safety check (4007) and
  // renders inline — the client hint never replaces server validation.
  await page.getByTestId("query-schema-input").click();
      await page.getByRole("option", { name: "app" }).click();
  await typeSql(page, "delete from app.users");
  await page.getByTestId("query-run").click();
  await expect(page.getByTestId("query-run-error")).toContainText("\u4ec5\u5141\u8bb8SELECT\u8bed\u53e5");

  // A legal SELECT streams the first cursor page; the footer states rows
  // already read and offers continuation — no total anywhere.
  await typeSql(page, "select id from app.users");
  await page.getByTestId("query-run").click();
  const loaded = page.locator("[data-testid^='query-result-loaded-']").first();
  // §18.50 follow-up (legacy parity): the background drain pulls every
  // cursor page automatically — the pager pages locally over the full
  // result and never fetches.
  await expect(loaded).toContainText("\u5df2\u8bfb\u53d6 1200 \u884c", { timeout: 15_000 });
  await expect(page.getByTestId("query-workspace")).not.toContainText("\u5171 ");

  // Masked vocabulary columns render the unified masking badge.
  await typeSql(page, "select id, email from app.users");
  await page.getByTestId("query-run").click();
  await expect(page.getByText("\u5df2\u6309\u654f\u611f\u5b57\u6bb5\u7b56\u7565\u8131\u654f").first()).toBeVisible();
});

test("gate 2: revocation is perceivable and the admin has no query read face", async ({ page }) => {
  await useScenario(page, "query-session");
  await page.goto("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
  await expect(page.getByTestId("query-workspace")).toBeVisible();

  // §18.44: the workspace belongs to the session owner (henry), while
  // revocation authority sits with the frozen reviewer identity — the
  // handlers read the identity dimension per request, so the revoke fetch
  // below runs as reviewer-a through a plain localStorage flip (no reload:
  // that would reset the in-memory mock world, F4-recorded limitation) and
  // the identity returns to the owner before the poll re-reads. The
  // approvals-page revocation UI is covered by component tests.
  await setMockIdentity(page, "reviewer-a");
  await page.evaluate(async () => {
    const grants = await (await fetch("/api/v4/query-grants")).json();
    const grant = grants.data.items.find((row: { state: string }) => row.state === "active");
    const response = await fetch(`/api/v4/query-grants/${grant.id}/revocations`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "If-Match": `"${String(grant.version)}"`, "Idempotency-Key": "e2e-revoke-1" },
      body: JSON.stringify({ reason: "e2e rotation" }),
    });
    const envelope = await response.json();
    if (envelope.err_code !== 0) throw new Error(`revoke failed: ${JSON.stringify(envelope)}`);
  });

  await setMockIdentity(page, "henry");

  // The workspace poll flips into the non-dismissible blocked notice with
  // the reason (Q004 revocation must be visible to the querying user).
  await expect(page.getByTestId("query-revoked-notice")).toContainText("\u8bbf\u95ee\u8d44\u683c\u5df2\u88ab\u64a4\u9500", { timeout: 10_000 });
  await expect(page.getByTestId("query-revoked-notice")).toContainText("e2e rotation");
  await expect(page.getByTestId("query-run")).toBeDisabled();

  // Another user's session is refused for every identity — including the
  // builtin admin (Q004 grants revocation, never reads). A fresh load is
  // fine here: the world reset only restores the seeded other-user session.
  await page.goto("/query/sessions/6f0f2b3c-5555-4555-8555-000000000102");
  await expect(page.getByTestId("query-workspace-error")).toBeVisible();

});

test("gate 2b: the builtin admin identity owns no query objects", async ({ page }) => {
  await useScenario(page, "query-session");
  await mockSession(page, "admin");
  await page.goto("/query");
  await expect(page.getByTestId("query-entry")).toBeVisible();
  // §18.48 (owner ruling): the builtin admin gets no business rights by
  // implicit override, but explicit grants apply to it like to any user —
  // this granted scenario world serves the flow catalog, while the admin
  // owns no grant of its own, so under the owner 2026-10-31 ruling
  // (no grant → no enter button) the row offers apply instead of enter.
  await expect(page.getByTestId(/query-flow-apply-/).first()).toBeVisible();
  await expect(page.getByTestId(/query-flow-enter-/)).toHaveCount(0);
  await expect(page.locator('[data-testid^="query-session-"]')).toHaveCount(0);
});

test("gate 3: flow and permission-group forms follow the new model", async ({ page }) => {
  await useScenario(page, "query-session");
  // The admin surfaces render under the admin capability from the start —
  // no mid-test identity flip needed here.
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-auth", "admin");
  });
  await page.goto("/admin/flows");
  await expect(page.getByTestId("admin-flows-table")).toBeVisible();

  await page.getByTestId("admin-flows-create-query").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const text = await dialog.textContent();
  // §3: query flows carry no SQL, rule set or executor fields.
  expect(text).not.toContain("Rule Set");
  expect(text).not.toContain("\u6267\u884c\u4eba");
  expect(text).toContain("\u4e0d\u4f1a\u5f71\u54cd\u5df2\u63d0\u4ea4\u5de5\u5355");
  await page.keyboard.press("Escape");

  // Permission groups grant flows (both kinds) — the legacy datasource
  // transfers have no v4 surface anywhere on the page.
  await page.goto("/admin/permission-groups");
  await expect(page.getByTestId("admin-groups-table")).toBeVisible();
  const bodyText = await page.locator("body").textContent();
  expect(bodyText).not.toContain("ddl_source");
  expect(bodyText).not.toContain("dml_source");
  expect(bodyText).not.toContain("query_source");
  expect(bodyText).not.toContain("\u81ea\u52a8\u5316\u4efb\u52a1");
});

test("gate 5: the remaining core pages render real content", async ({ page }) => {
  await useScenario(page, "query-session");
  await page.goto("/workspace");
  await expect(page.getByTestId("workspace-page")).toBeVisible();
  await expect(page.getByTestId("home-announcement")).toBeVisible();

  // My Orders - Query Orders tab shows the unified query view.
  await page.goto("/changes/mine");
  await page.getByTestId("tab-query-orders").click();
  await expect(page.getByTestId("orders-query-table")).toBeVisible();
  // §18.73: the merged query-orders view paginates locally (20/page default).
  await expect(page.getByTestId("orders-query-pagination")).toBeVisible();
  await expect(page.getByTestId("orders-query-page")).toContainText("1");
  const row = page.locator("[data-testid^='orders-query-row-']").first();
  await row.click();
  await expect(page.getByTestId("orders-query-detail")).toBeVisible();

});

test("gate 5b: audit records render for the admin read face", async ({ page }) => {
  await useScenario(page, "query-session");
  await mockSession(page, "admin");
  await page.goto("/records");
  await expect(page.getByTestId("records-table")).toBeVisible();
  const row = page.locator("[data-testid^='records-row-']").first();
  await row.click();
  await expect(page.getByTestId("records-detail")).toBeVisible();
});
