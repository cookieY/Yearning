import { expect, test, type Page } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// FE-F9-REVIEW-ADMIN acceptance gates over the production build with the
// shared MSW worker and the stateful admin fixture:
//   1. Secrets are never prefilled — datasource credential fields and the provider API
//      key render empty on edit; no stored secret text ever reaches the DOM;
//   2. No external HTTP or DB-writing Custom Tool UI — the review-input editor exposes
//      the governed rule definition with fixed Critical severity and no
//      executable affordance;
//   3. Config Hash and Review invalidation impact are explicit — config hashes, the outdated-impact
//      note and the rule-set impact preview are visible;
//   4. Internal knowledge has no auto-learning entry — knowledge management offers only manual
//      authoring and finding conversion;
//   5. Accessible to admin only — every admin route redirects a non-admin to /403.

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
  await mockSession(page, "admin");
});

test("secrets never render: datasource credentials and provider API key stay empty on edit", async ({
  page,
}) => {
  await page.goto("/admin/datasources");
  await expect(page.getByTestId(/ds-row-/).first()).toBeVisible();
  await page.getByTestId(/ds-actions-/).first().click();
  await page.getByTestId(/ds-edit-/).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  for (const group of ["readonly", "execution"]) {
    const username = dialog.getByTestId(`credential-username-${group}`);
    if (await username.isVisible().catch(() => false)) {
      await expect(username).toHaveValue("");
    }
    const password = dialog.getByTestId(`credential-password-${group}`);
    if (await password.isVisible().catch(() => false)) {
      await expect(password).toHaveValue("");
    }
  }
  // Neither the fixture-internal plaintexts nor any secret marker leaks.
  const pageContent = await page.content();
  expect(pageContent).not.toContain("revpw-1");
  expect(pageContent).not.toContain("execpw-1");
  await page.keyboard.press("Escape");

  await page.goto("/admin/review-engine/providers");
  await expect(page.getByTestId(/provider-row-/).first()).toBeVisible();
  await page.getByTestId(/provider-actions-/).first().click();
  await page.getByTestId(/provider-edit-/).first().click();
  const providerDialog = page.getByRole("dialog");
  await expect(providerDialog.getByTestId("provider-api-key")).toHaveValue("");
  await expect(providerDialog.getByText(/API Key\uff1a\u5df2\u914d\u7f6e/)).toBeVisible();
  expect(await page.content()).not.toContain("pkey-a1");
});

test("review-input editor offers no executable-code, HTTP or database-write surface", async ({
  page,
}) => {
  await page.goto("/admin/review-engine/skills");
  await page.getByTestId("review-input-create").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("review-input-knowledge-text")).toBeVisible();
  // R013: enabled rules always produce effective Critical findings. The
  // legacy max_severity field remains on the wire but has no editable UI.
  await expect(dialog.getByTestId("review-input-severity-policy")).toBeVisible();
  await expect(dialog.getByTestId("review-input-critical-severity")).toContainText("\u4e25\u91cd");
  await expect(dialog.getByTestId(/^review-input-max-severity/)).toHaveCount(0);
  await expect(dialog.getByTestId("review-input-finding-key")).toHaveCount(0);
  await expect(dialog.getByTestId("review-input-title")).toHaveCount(0);
  await expect(dialog.getByTestId("review-input-message")).toHaveCount(0);
  for (const button of await dialog.getByRole("button").all()) {
    expect(await button.textContent()).not.toMatch(/HTTP|\u6267\u884c\u4ee3\u7801|\u5199\u5e93|Webhook/);
  }
  await dialog.getByTestId("review-input-name").fill("e2e-critical-policy");
  await dialog
    .getByTestId("review-input-knowledge-text")
    .fill("Every enabled rule violation is Critical and must block submission.");
  const saveRequest = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().endsWith("/admin/prompt-tools"),
  );
  await dialog.getByTestId("review-input-submit").click();
  const payload = (await saveRequest).postDataJSON() as {
    definition?: { max_severity?: string };
  };
  expect(payload.definition?.max_severity).toBe("critical");
  await expect(dialog).toBeHidden();
});

test("config hashes and review-invalidation impact live in the edit dialog (owner ruling)", async ({ page }) => {
  await page.goto("/admin/review-engine/skills");
  // Owner ruling: list does not show hash or persistent notice — both live inside the edit dialog.
  await expect(page.getByTestId("review-input-outdated-note")).toHaveCount(0);
  await expect(page.getByTestId("review-input-hash")).toHaveCount(0);
  await page.getByTestId(/review-input-actions-/).first().click();
  await page.getByTestId(/review-input-edit-/).first().click();
  await expect(page.getByTestId("review-input-outdated-note")).toContainText(
    "\u5f15\u7528\u65e7 Hash \u7684\u672a\u63d0\u4ea4\u9884\u5ba1\u7acb\u5373\u5931\u6548",
  );
  await expect(page.getByTestId("review-input-hash")).toBeVisible();
  await page.keyboard.press("Escape");

  await page.goto("/admin/rule-sets");
  await expect(page.getByTestId("rule-set-hash").first()).toBeVisible();
  await page.getByTestId(/rule-set-actions-/).first().click();
  await page.getByTestId(/rule-set-edit-/).first().click();
  const preview = page.getByTestId("rule-set-impact-preview");
  await expect(preview).toContainText("1 \u4e2a\u6d41\u7a0b");
  await expect(preview).toContainText("\u751f\u4ea7\u53d8\u66f4\u9ed8\u8ba4\u6d41\u7a0b");
  await expect(preview).toContainText("\u5f15\u7528\u65e7 Hash \u7684\u672a\u63d0\u4ea4\u9884\u5ba1\u5c06\u5931\u6548");
});

test("internal experience offers no auto-learning entry", async ({ page }) => {
  await page.goto("/admin/review-engine/knowledge");
  await expect(page.getByTestId(/review-input-row-/).first()).toBeVisible();
  for (const button of await page.getByRole("button").all()) {
    expect(await button.textContent()).not.toMatch(/\u81ea\u52a8\u5b66\u4e60|\u81ea\u52a8\u6c89\u6dc0|auto-learn/i);
  }
  await expect(page.getByText("\u4eba\u5de5\u6c89\u6dc0").first()).toBeVisible();
  await expect(page.getByText("Finding \u8f6c\u5316").first()).toBeVisible();
});

test("a datasource connection test materializes the capability matrix with explanations", async ({
  page,
}) => {
  await page.goto("/admin/datasources");
  // analytics-pg has no execution credential — probing review succeeds and
  // materializes capabilities where execution is explicitly unavailable.
  // §18.61: the probe purpose is the datasource's stored test_purpose
  // (seeded review) — the table carries no purpose select anymore.
  const pgRow = page.locator("tr", { hasText: "analytics-pg" });
  await pgRow.getByTestId(/ds-actions-/).click();
  await page.getByTestId(/ds-test-button-/).click();
  const capabilities = page.getByTestId("ds-capabilities");
  await expect(capabilities).toContainText("16.3", { timeout: 8_000 });
  await expect(capabilities.getByTestId("ds-capability-execution")).toContainText("\u4e0d\u53ef\u7528");
  await expect(page.getByText("\u90e8\u5206\u80fd\u529b\u4e0d\u53ef\u7528")).toBeVisible();
});

test("a high-impact budget change saves directly via transparent impact-token retry", async ({
  page,
}) => {
  await page.goto("/admin/review-engine/ai-budget");
  await expect(page.getByTestId("ai-budget-r008-note")).toContainText("\u4e0d\u5b58\u5728\u5355\u5de5\u5355");
  await page.getByTestId("ai-budget-enforced").click();
  await page.getByTestId("ai-budget-save").click();
  await expect(page.getByTestId("ai-budget-saved")).toBeVisible();
});

test("admin routes reject a non-admin session", async ({ page }) => {
  const nonAdmin: Page = page;
  await nonAdmin.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-auth", "default");
  });
  await nonAdmin.goto("/admin/datasources");
  await expect(nonAdmin.getByText("\u65e0\u6743\u8bbf\u95ee")).toBeVisible();
});

// FE-F9-REVIEW-ADMIN-ALIGNMENT gates (B13 contract surface, production build):
// builtin lock badge + locked definition face, the credential three-mode
// form (keep only on configured rows), and TLS material write-only form +
// tls_verified list states.

test("builtin skill renders the lock badge and only the state stays toggleable", async ({ page }) => {
  await page.goto("/admin/review-engine/skills");
  const builtinRow = page.locator("tr", { hasText: "builtin-lexical-guards" });
  await expect(builtinRow.getByTestId(/review-input-builtin-/)).toContainText("\u5185\u7f6e");
  // Owner ruling: inline explanation text moved into the edit dialog builtin notice.
  await expect(builtinRow.getByText(/\u4ec5\u72b6\u6001\u53ef\u5207\u6362/)).toHaveCount(0);
  await builtinRow.getByTestId(/review-input-actions-/).click();
  await expect(page.getByTestId(/review-input-delete-/)).toBeDisabled();
  await page.keyboard.press("Escape");
  await builtinRow.getByTestId(/review-input-actions-/).click();
  await page.getByTestId(/review-input-edit-/).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("review-input-builtin-notice")).toBeVisible();
  await expect(dialog.getByTestId("review-input-name")).toBeDisabled();
  await expect(dialog.getByTestId("review-input-knowledge-text")).toBeDisabled();
  await expect(dialog.getByTestId("review-input-state-select")).toBeEnabled();
  await dialog.getByRole("button", { name: "\u53d6\u6d88" }).click();
  await expect(dialog).toBeHidden();
});

test("credential rows edit in exactly one explicit mode; keep only on configured rows", async ({ page }) => {
  await page.goto("/admin/datasources");
  // Scoped to the two baseline rows: the owner test catalog adds many more
  // plaintext rows that would break the regex locators' strict mode.
  const mysqlRow = page.locator("tr", { hasText: "prod-order-mysql" });
  const pgRow = page.locator("tr", { hasText: "analytics-pg" });
  await expect(pgRow.getByTestId(/ds-tls-verified-/)).toContainText("\u5f3a\u5236\u6821\u9a8cTLS");
  await expect(mysqlRow.getByTestId(/ds-tls-plaintext-/)).toContainText("\u660e\u6587");


  await pgRow.getByTestId(/ds-actions-/).click();
  await page.getByTestId(/ds-edit-/).click();
  const dialog = page.getByRole("dialog");
  // Configured rows open in keep mode — the storage-preserving no-op.
  await expect(dialog.getByTestId("credential-keep-note-readonly")).toBeVisible();
  // The write-only TLS material block is checked but never prefilled.
  await expect(dialog.getByTestId("ds-tls-enabled")).toBeChecked();
  await expect(dialog.getByTestId("ds-tls-ca")).toHaveValue("");
  await expect(dialog.getByTestId("ds-tls-cert")).toHaveValue("");
  await expect(dialog.getByTestId("ds-tls-key")).toHaveValue("");
  await dialog.getByRole("button", { name: "\u53d6\u6d88" }).click();
  await expect(dialog).toBeHidden();

  // A create form offers neither keep nor reuse: nothing is stored yet.
  await page.getByTestId("ds-create").click();
  const createDialog = page.getByRole("dialog");
  await createDialog.getByTestId("credential-mode-readonly").click();
  // The popup portals to the document body (outside the dialog element) —
  // wait for it at page scope; the count itself is the assertion: replace
  // is the only offered mode on create.
  await expect(page.getByRole("option")).toHaveCount(1);
  const optionTexts = await page.getByRole("option").allTextContents();
  expect(optionTexts).toEqual(["\u66ff\u6362\uff08\u65b0\u7528\u6237\u540d\uff0b\u5bc6\u7801\uff09"]);
  await createDialog.getByRole("button", { name: "\u53d6\u6d88" }).click();
  await expect(createDialog).toBeHidden();
});
