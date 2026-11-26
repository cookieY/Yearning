import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { HttpResponse, http } from "msw";
import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { SESSION_USER_ID, stubSession } from "@/test/session";
import { ADMIN_FIXTURE_CHANNEL_EMAIL_ID, resetAdminFixture } from "@/shared/mock/admin-fixture";
import { openRowActions } from "@/test/row-actions";
import { resetQueryFixture, seedQueryScenario } from "@/shared/mock/query-fixture";
import { SessionProvider } from "@/features/auth/session-provider";
import "@/shared/i18n";
import AuditRecordsPage from "@/routes/records/audit-records-page";
import ProfilePage from "@/routes/profile/profile-page";
import AdminSettingsNamespacePage from "@/routes/admin/admin-settings-page";
import AdminAiBudgetPage from "@/routes/admin/admin-ai-budget-page";
import AdminIdentityProvidersPage from "@/routes/admin/admin-identity-providers-page";
import WorkspaceHomePage from "@/routes/workspace/workspace-home-page";
import { QueryOrdersTab } from "@/routes/changes/query-orders-tab";

/**
 * FE-F10 remaining surfaces (render + state coverage; popup-free). These
 * complement the gate-focused admin-f10-pages tests with the pages not yet
 * asserted: records (admin read face + non-admin empty state), profile
 * five-tab layout, the generic settings namespaces, identity providers,
 * notification channels, the workspace dashboard and the query-orders tab.
 */

function renderPage(ui: React.ReactElement): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <SessionProvider>
        <MemoryRouter>{ui}</MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

function renderNotificationsPage(): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <SessionProvider>
        <MemoryRouter initialEntries={["/admin/settings/notifications"]}>
          <Routes>
            <Route path="/admin/settings/:namespace" element={<AdminSettingsNamespacePage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

beforeAll(() => {
  Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
  Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
  Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });
});

beforeEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "admin");
  resetAdminFixture();
  resetQueryFixture();
});

describe("audit records page", () => {
  it("renders the admin read face with detail sheet", async () => {
    stubSession(true);
    renderPage(<AuditRecordsPage />);
    await screen.findByTestId("records-table");
    const rows = screen.getAllByTestId(/records-row-/);
    expect(rows.length).toBeGreaterThan(0);
    const firstRow = rows[0];
    if (firstRow !== undefined) await userEvent.click(firstRow);
    expect(await screen.findByTestId("records-detail")).toBeVisible();
    expect(screen.getByTestId("records-detail").textContent).toContain("auth.login");
  });

  it("shows the honest no-scope state for non-admin sessions", async () => {
    stubSession(false);
    renderPage(<AuditRecordsPage />);
    expect(await screen.findByTestId("records-no-scope")).toBeTruthy();
  });
});

describe("profile page", () => {
  it("renders all four constrained sections", async () => {
    stubSession(false);
    renderPage(<ProfilePage />);
    expect(await screen.findByTestId("profile-tab-identity")).toBeVisible();
    expect(screen.getByTestId("profile-tab-security")).toBeVisible();
    expect(screen.getByTestId("profile-tab-appearance")).toBeVisible();
    // §18.69: notifications section removed (zero user-configurable items at the personal level, so do not occupy space).
    expect(screen.queryByTestId("profile-tab-notifications")).not.toBeInTheDocument();
    expect(screen.getByTestId("profile-tab-display")).toBeVisible();
    // §18.52: the security tab now renders the self-service password form
    // for local accounts (mock identities are always local).
    await userEvent.click(screen.getByTestId("profile-tab-security"));
    expect(await screen.findByTestId("profile-old-password")).toBeVisible();
    expect(screen.getByTestId("profile-new-password")).toBeVisible();
    expect(screen.getByTestId("profile-confirm-password")).toBeVisible();
    expect(screen.getByTestId("profile-change-password-submit")).toBeVisible();
  });
});

describe("settings namespaces", () => {
  it("renders the query namespace without any limit fields (Q005)", async () => {
    stubSession(true);
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <SessionProvider>
          <MemoryRouter initialEntries={["/admin/settings/query"]}>
            <Routes>
              <Route path="/admin/settings/:namespace" element={<AdminSettingsNamespacePage />} />
            </Routes>
          </MemoryRouter>
        </SessionProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByTestId("admin-settings-query")).toBeVisible();
    const approval = await screen.findByTestId("settings-query-approval");
    expect(approval).toBeChecked();
    expect(await screen.findByTestId("settings-query-no-limit")).toBeTruthy();
    expect(await screen.findByTestId("settings-query-max-rows")).toHaveValue(1000);
  });

  it("saving a high-impact toggle directly succeeds via the auto assess retry (§18.47)", async () => {
    stubSession(true);
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <SessionProvider>
          <MemoryRouter initialEntries={["/admin/settings/query"]}>
            <Routes>
              <Route path="/admin/settings/:namespace" element={<AdminSettingsNamespacePage />} />
            </Routes>
          </MemoryRouter>
        </SessionProvider>
      </QueryClientProvider>,
    );
    const approval = await screen.findByTestId("settings-query-approval");
    expect(approval).toBeChecked();
    // The owner journey: toggle the high-impact knob and hit Save WITHOUT
    // the manual Assess Impact ceremony — the first PUT answers 1011 and the
    // page must run assess → retry with the fresh token by itself.
    await userEvent.click(approval);
    await userEvent.click(screen.getByTestId("settings-save"));
    expect(await screen.findByTestId("settings-saved-note", {}, { timeout: 4000 })).toBeVisible();
    // The auto-generated assessment renders, so the effects are still shown.
    expect(screen.getByTestId("settings-assessment")).toBeVisible();
  });
});


describe("settings tabs (§18.47; AI budget moved out 2026-10-31)", () => {
  it("the AI-budget page no longer carries the settings tab bar (moved to review engine)", async () => {
    stubSession(true);
    renderPage(<AdminAiBudgetPage />);
    expect(await screen.findByTestId("admin-settings-ai-budget")).toBeVisible();
    // AI Budget belongs to the Review Engine group (owner ruling) — the settings tab bar does not render on this page.
    expect(screen.queryByTestId("settings-tab-general")).not.toBeInTheDocument();
    expect(screen.queryByTestId("settings-tab-ai-budget")).not.toBeInTheDocument();
  });

  it("the notifications page keeps the five-tab settings bar", async () => {
    stubSession(true);
    renderNotificationsPage();
    for (const label of ["\u901a\u7528", "\u67e5\u8be2", "\u6267\u884c", "\u54c1\u724c", "\u901a\u77e5"]) {
      const tab = await screen.findByRole("tab", { name: label });
      expect(tab).toBeVisible();
      expect(tab.closest('[data-slot="page-actions"]')).toBeNull();
      expect(tab.closest('[data-slot="page-header"]')).toBeNull();
    }
    expect(screen.queryByTestId("settings-tab-ai-budget")).not.toBeInTheDocument();
  });
});

describe("identity providers page", () => {
  it("shows LDAP singleton state and secret presence without echoing", async () => {
    stubSession(true);
    renderPage(<AdminIdentityProvidersPage />);
    const ldapText = (await screen.findAllByTestId(/admin-idp-row-/))
      .map((row) => row.textContent)
      .join(" ");
    expect(ldapText).toContain("ldaps");
    expect(document.body.textContent).not.toContain("ldapsec-1");
    expect(screen.getByTestId("admin-idp-create-ldap")).toBeDisabled();
  });

  it("embeds LDAP and OIDC identity provider management inside /admin/settings/general", async () => {
    stubSession(true);
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <SessionProvider>
          <MemoryRouter initialEntries={["/admin/settings/general"]}>
            <Routes>
              <Route path="/admin/settings/:namespace" element={<AdminSettingsNamespacePage />} />
            </Routes>
          </MemoryRouter>
        </SessionProvider>
      </QueryClientProvider>,
    );
    const idpSection = await screen.findByTestId("settings-general-idp");
    expect(idpSection).toBeVisible();
    const createLdap = await within(idpSection).findByTestId("admin-idp-create-ldap");
    const createOidc = within(idpSection).getByTestId("admin-idp-create-oidc");
    expect(createLdap).toBeDisabled();
    expect(createOidc).toBeEnabled();
    expect(createLdap.closest('[data-slot="card-action"]')).not.toBeNull();
    expect(createOidc.closest('[data-slot="card-action"]')).not.toBeNull();
  });
});

describe("AI budget usage ring (2026-10-31 round 3)", () => {
  it("renders the today-usage ring with fixture summary totals", async () => {
    stubSession(true);
    renderPage(<AdminAiBudgetPage />);
    const ring = await screen.findByTestId("ai-budget-usage-ring");
    // fixture: daily 10000 minor = 100; reference usage 3.671 major units -> 3.67 major currency units.
    await waitFor(() => { expect(ring.textContent).toContain("3.67"); });
    expect(ring.textContent).toContain("100");
    expect(ring.textContent).toContain("USD");

    const outputPrice = screen.getByTestId("ai-budget-output-price");
    const dailyBudget = screen.getByTestId("ai-budget-daily");
    expect(outputPrice.compareDocumentPosition(dailyBudget) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(ring.querySelector("[data-chart]")).toHaveStyle({ width: "300px", height: "300px" });
    expect(dailyBudget).toHaveValue(100);
  });

  it("edits budget and fallback prices in major currency units and saves integer minor units directly", async () => {
    stubSession(true);
    let savedSettings: Record<string, unknown> | null = null;
    server.use(
      http.put(apiRoute("/admin/settings/:namespace"), async ({ request }) => {
        const body = (await request.json()) as { settings: Record<string, unknown> };
        savedSettings = body.settings;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            namespace: "ai-budget",
            schema_version: 1,
            settings: body.settings,
            version: 2,
            updated_by: SESSION_USER_ID,
            updated_at: "2026-10-25T10:00:00Z",
          },
          request_id: "req-ai-budget-major-unit",
        });
      }),
    );

    renderPage(<AdminAiBudgetPage />);
    const inputPrice = await screen.findByTestId("ai-budget-input-price");
    const outputPrice = screen.getByTestId("ai-budget-output-price");
    const dailyBudget = screen.getByTestId("ai-budget-daily");
    expect(screen.queryByTestId("ai-budget-assess")).not.toBeInTheDocument();

    await userEvent.clear(inputPrice);
    await userEvent.type(inputPrice, "1.05");
    await userEvent.clear(outputPrice);
    await userEvent.type(outputPrice, "2.5");
    await userEvent.clear(dailyBudget);
    await userEvent.type(dailyBudget, "85.5");

    expect(screen.getByTestId("ai-budget-usage-ring").textContent).toContain("85.50");

    await userEvent.click(screen.getByTestId("ai-budget-save"));
    await waitFor(() => { expect(savedSettings).not.toBeNull(); });
    expect(savedSettings).toMatchObject({
      input_price_per_million_minor: 105,
      output_price_per_million_minor: 250,
      daily_budget_minor: 8550,
    });
  });
});

describe("notification channels page", () => {
  it("lists channels with secret presence and the Outbox deliveries (S007 three views)", async () => {
    stubSession(true);
    renderNotificationsPage();
    await screen.findByTestId("admin-notifications-table");
    expect(screen.getByTestId("admin-notifications-table").textContent).toContain("ops-mail");
    // Delivery records moved into the three-view switch: switch to the deliveries view before asserting. Base UI Tab does not respond
    // to programmatic element.click() (activation requires a real pointer sequence), so use userEvent.
    await userEvent.click(screen.getByTestId("admin-notifications-view-deliveries"));
    const deliveries = await screen.findByTestId("admin-deliveries-table");
    expect(deliveries.textContent).toContain("smtp_timeout");
    // Round 4 regression: single-view mutual exclusion — once switched to deliveries, the channels table is no longer mounted.
    expect(screen.queryByTestId("admin-notifications-table")).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("admin-notifications-view-channels"));
    await screen.findByTestId("admin-notifications-table");
    expect(screen.queryByTestId("admin-deliveries-table")).not.toBeInTheDocument();
  });

  it("keeps the create button in the table card header, not the page header (round 4)", async () => {
    stubSession(true);
    renderNotificationsPage();
    const create = await screen.findByTestId("admin-notifications-create");
    // Button sits in the card header (card-action slot), not in the page header actions.
    expect(create.closest('[data-slot="card-action"]')).not.toBeNull();
    expect(create.closest('[data-slot="page-actions"]')).toBeNull();
  });

  it("shows the test-delivery outcome on the channel row (async task poll)", async () => {
    stubSession(true);
    let submittedBody: unknown = null;
    server.use(
      http.post(apiRoute("/admin/notification-channels/:channelId/test-deliveries"), async ({ request }) => {
        submittedBody = await request.json().catch(() => null);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { task_id: "0198da00-0000-7000-8000-000000000099", channel_id: ADMIN_FIXTURE_CHANNEL_EMAIL_ID },
          request_id: "notif-test-1",
        });
      }),
      http.get(apiRoute("/tasks/:taskId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "0198da00-0000-7000-8000-000000000099",
            kind: "notification_test_delivery",
            state: "succeeded",
            aggregate_type: "notification_channel",
            aggregate_id: ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
            progress_completed: 1,
            progress_total: 1,
            progress_unit: "deliveries",
            created_at: "2026-11-26T00:00:00Z",
            updated_at: "2026-11-26T00:00:01Z",
          },
          request_id: "notif-task-1",
        }),
      ),
    );
    renderNotificationsPage();
    await screen.findByTestId("admin-notifications-table");
    await openRowActions(`admin-channel-actions-${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}`);
    await userEvent.click(screen.getByTestId(`admin-channel-test-${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}`));

    const dialog = await screen.findByTestId("channel-test-email-dialog");
    const recipientInput = within(dialog).getByTestId("channel-test-recipient");
    await userEvent.clear(recipientInput);
    await userEvent.type(recipientInput, "qa-inbox@example.com");
    await userEvent.click(within(dialog).getByTestId("channel-test-submit"));

    const result = await screen.findByTestId(
      `admin-channel-test-result-${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}`,
      {},
      { timeout: 4000 },
    );
    await waitFor(
      () => { expect(result.textContent).toContain("\u6d4b\u8bd5\u6295\u9012\u6210\u529f"); },
      { timeout: 4000 },
    );
    expect(submittedBody).toEqual({ recipient: "qa-inbox@example.com" });
  });

  it("surfaces an error instead of spinning indefinitely when test-delivery task polling fails", async () => {
    stubSession(true);
    let taskCalls = 0;
    server.use(
      http.get(apiRoute("/tasks/:taskId"), () => {
        taskCalls += 1;
        return HttpResponse.json({
          err_code: 1002,
          message: "task not found",
          data: null,
          request_id: "notif-poll-err-1",
          retryable: false,
        });
      }),
    );
    renderNotificationsPage();
    await screen.findByTestId("admin-notifications-table");
    await openRowActions(`admin-channel-actions-${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}`);
    await userEvent.click(screen.getByTestId(`admin-channel-test-${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}`));

    const dialog = await screen.findByTestId("channel-test-email-dialog");
    await userEvent.click(within(dialog).getByTestId("channel-test-submit"));

    const result = await screen.findByTestId(
      `admin-channel-test-result-${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}`,
      {},
      { timeout: 4000 },
    );
    await waitFor(
      () => {
        expect(result.textContent).toContain("\u8bf7\u6c42\u7684\u5bf9\u8c61\u4e0d\u5b58\u5728");
      },
      { timeout: 4000 },
    );
    expect(result.querySelector('[data-slot="spinner"]')).toBeNull();
    expect(taskCalls).toBe(1);
  });
});

describe("notification rules view (S007)", () => {
  it("lists rules with event vocabulary and no AI pre-review triggers", async () => {
    stubSession(true);
    renderNotificationsPage();
    (await screen.findByTestId("admin-notifications-view-rules"));
    await userEvent.click(screen.getByTestId("admin-notifications-view-rules"));
    await screen.findByTestId("admin-rules-table");
    const text = screen.getByTestId("admin-rules-table").textContent;
    expect(text).toContain("\u6267\u884c\u5931\u8d25\u901a\u77e5\u63d0\u4ea4\u4eba");
    expect(text).toContain("\u542f\u7528");
    expect(text).not.toContain("common.enabled");
    expect(text).not.toContain("common.disabled");
  });

  it("supports filling the default template, inserting allowlisted placeholders, previewing, and saving a custom rule template", async () => {
    stubSession(true);
    renderNotificationsPage();
    await userEvent.click(await screen.findByTestId("admin-notifications-view-rules"));
    await screen.findByTestId("admin-rules-table");
    await userEvent.click(screen.getByTestId("admin-rules-create"));

    const tplInput = await screen.findByTestId<HTMLTextAreaElement>("rule-message-template");
    expect(tplInput.value).toBe("");

    await userEvent.click(screen.getByTestId("rule-template-fill-default"));
    expect(tplInput.value).toContain("{{order_display_number}}");
    expect(tplInput.value).toContain("{{order_id}}");
    expect(tplInput.value).toContain("{{submitter}}");
    expect(tplInput.value).toContain("{{submitted_at}}");
    expect(tplInput.value).toContain("{{order_state}}");
    expect(tplInput.value).toContain("{{approvers}}");
    expect(tplInput.value).toContain("{{approved_by}}");
    expect(tplInput.value).toContain("{{approved_at}}");
    expect(tplInput.value).toContain("{{executors}}");
    expect(tplInput.value).toContain("{{executed_by}}");
    expect(tplInput.value).toContain("{{executed_at}}");

    await userEvent.click(screen.getByTestId("rule-template-reset"));
    expect(tplInput.value).toBe("");

    await userEvent.click(screen.getByTestId("rule-placeholder-chip-order_id"));
    await userEvent.click(screen.getByTestId("rule-placeholder-chip-submitter"));
    expect(tplInput.value).toBe("{{order_id}} {{submitter}}");

    await userEvent.type(screen.getByTestId("rule-name"), "custom-template-rule");
    await userEvent.click(screen.getByTestId("rule-preview-run"));
    const previewBox = await screen.findByTestId("rule-preview-result");
    expect(previewBox.textContent).toContain("00000000-0000-4000-8000-000000000001 alice");

    await userEvent.click(screen.getByTestId("rule-form-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("admin-rules-table").textContent).toContain("custom-template-rule");
    });
  });
});

describe("workspace home", () => {
  it("renders the counters, announcement and platform statistics for a granted user (§18.71)", async () => {
    seedQueryScenario("query-flow");
    stubSession(false);
    renderPage(<WorkspaceHomePage />);
    expect(await screen.findByTestId("workspace-page")).toBeVisible();
    // Row 1 — the caller's own actionable counters (/dashboard/me).
    expect(await screen.findByTestId("home-kpi-pending-approval")).toBeVisible();
    expect(screen.getByTestId("home-kpi-pending-execution")).toBeVisible();
    expect(screen.getByTestId("home-kpi-blocked-review")).toBeVisible();
    // F-UX-004: "Pending My Execution" links directly to the executor pending view instead of a submitter-scoped dead end.
    expect(screen.getByTestId("home-kpi-pending-execution-link")).toHaveAttribute(
      "href",
      "/changes/mine?scope=execution",
    );
    // The announcement mounts immediately and fills in once the query lands.
    await screen.findByText("\u5b63\u5ea6\u7ef4\u62a4\u7a97\u53e3\u516c\u544a");
    expect(screen.getByTestId("home-announcement")).toHaveTextContent("\u5b63\u5ea6\u7ef4\u62a4\u7a97\u53e3\u516c\u544a");
    // §18.71 (owner ruling 2026-10-16): platform statistics are an
    // authenticated-session surface — a non-admin session sees the same
    // operations numbers the admin home shows.
    expect(await screen.findByTestId("home-order-overview")).toBeVisible();
    expect(screen.getByTestId("home-distribution")).toBeVisible();
    // The loaded-state counterpart to the unknown-state test below. Without it
    // a "render unknown forever" regression would satisfy every unknown-state
    // assertion: the fixture is 41/47 = 87%, so the ring must announce 87% and
    // carry both the track and the progress circle.
    const loadedRing = screen.getByTestId("home-completion-ring");
    expect(loadedRing).toHaveAccessibleName("\u5de5\u5355\u5b8c\u6210\u7387 87%");
    expect(loadedRing.querySelectorAll("circle")).toHaveLength(2);
    // State distribution shares the same scope as success rate (RCP-20261023-HOME-METRICS-ALIGNMENT):
    // the four-segment counts come from platform state_distribution, summing to the 30-day window total of 47 orders;
    // the old implementation aggregated the user's own 20 recent orders and got 20.
    expect(screen.getByTestId("home-distribution")).toHaveTextContent("\u5de5\u5355\u72b6\u6001\u5206\u5e03");
    // Platform window of 47 orders = 41 completed + 1 running + 5 failed (3 failed + 2 partial) + 0 other.
    const counts = ["done", "active", "failed", "other"].map((key) =>
      Number(screen.getByTestId(`home-distribution-count-${key}`).textContent),
    );
    expect(counts).toEqual([41, 1, 5, 0]);
    expect(counts.reduce((sum, n) => sum + n, 0)).toBe(47);
    // §18.83 (owner ruling 2026-10-23): system-health is open to all authenticated sessions,
    // so regular users also receive component health dots (previously admin-only, leaving regular users permanently without data).
    expect(await screen.findByTestId("home-health-dots")).toBeVisible();
  });

  it("renders the zero-permission notice alongside the dashboard, never instead of it (auth PRD §11)", async () => {
    stubSession(false);
    server.use(
      http.get(apiRoute("/users/me/flows"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: [] },
          request_id: "req-flows-empty",
        }),
      ),
    );
    renderPage(<WorkspaceHomePage />);
    expect(await screen.findByTestId("workspace-page")).toBeVisible();
    // The default fixture world grants nothing: the notice appears…
    expect(await screen.findByText("\u7b49\u5f85\u7ba1\u7406\u5458\u914d\u7f6e\u6743\u9650")).toBeVisible();
    // …while the dashboard blocks keep rendering underneath.
    expect(await screen.findByTestId("home-kpi-pending-approval")).toBeVisible();
    expect(screen.getByTestId("home-order-overview")).toBeVisible();
  });

  it("never asserts a completion percentage it has not loaded (review L4)", async () => {
    seedQueryScenario("query-flow");
    stubSession(false);
    // The operations aggregate fails: the page must degrade to "not measured"
    // rather than claim 0 / 0 done. Regression guard for the round-1 finding
    // that an unresolved query rendered a confident zero with a 0% arc.
    server.use(
      http.get(apiRoute("/admin/dashboard/operations"), () =>
        HttpResponse.json(
          { err_code: 5000, message: "internal error", data: null, request_id: "req-ops-fail" },
          { status: 500 },
        ),
      ),
    );
    renderPage(<WorkspaceHomePage />);
    expect(await screen.findByTestId("workspace-page")).toBeVisible();

    // The ring announces "loading", not "0%".
    const ring = await screen.findByTestId("home-completion-ring");
    expect(ring).toHaveAccessibleName("\u5de5\u5355\u5b8c\u6210\u7387\uff08\u52a0\u8f7d\u4e2d\uff09");
    // An absent arc is the point: a zero-length arc would be a claim.
    expect(ring.querySelectorAll("circle")).toHaveLength(1);
    // The centre must read "— / —". If CountUp ever fell back to 0 for an
    // undefined value this would be "0 / 0" — the exact original symptom —
    // while the three assertions above stayed green.
    expect(ring.parentElement).toHaveTextContent("— / —");
    // The four breakdown columns show an em dash, not zeros that sum to 47.
    // Exact match, not substring: "—%" must not pass as "—".
    for (const key of ["done", "active", "failed", "other"]) {
      expect(screen.getByTestId(`home-distribution-count-${key}`).textContent.trim()).toBe("—");
    }
    // Copy must say "still measuring", never a count of zero orders.
    expect(await screen.findByText("\u6b63\u5728\u7edf\u8ba1\u6267\u884c\u7ed3\u679c…")).toBeVisible();
    expect(screen.getByText("\u6b63\u5728\u52a0\u8f7d\u7edf\u8ba1\u7a97\u53e3\u5185\u7684\u5de5\u5355\u72b6\u6001…")).toBeVisible();
  });

  it("keeps the announcement card frame when no announcement is published (RCP-20261020)", async () => {
    seedQueryScenario("query-flow");
    stubSession(false);
    server.use(
      http.get(apiRoute("/announcements/current"), () =>
        HttpResponse.json({ err_code: 0, message: "ok", data: null, request_id: "req-ann-null" }),
      ),
    );
    renderPage(<WorkspaceHomePage />);
    expect(await screen.findByTestId("workspace-page")).toBeVisible();
    const announcement = await screen.findByTestId("home-announcement");
    expect(announcement).toBeVisible();
    expect(await screen.findByTestId("home-announcement-empty")).toHaveTextContent(
      "\u5f53\u524d\u6ca1\u6709\u5df2\u53d1\u5e03\u7684\u516c\u544a",
    );
    expect(screen.getByTestId("home-announcement-title")).toHaveTextContent("\u6682\u65e0\u516c\u544a");
  });
});

describe("query orders tab", () => {
  it("shows the seeded request/grant/session rows with the revocation reason detail", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    window.localStorage.setItem("yearning-mock-auth", "default");
    renderPage(<QueryOrdersTab />);
    await screen.findByTestId("orders-query-table");
    const rows = screen.getAllByTestId(/orders-query-row-/);
    // Own access request (grant_active) + own session; the other user's
    // session is relation-scoped away server-side.
    expect(rows.length).toBe(2);
    const firstRow = rows[0];
    if (firstRow !== undefined) await userEvent.click(firstRow);
    expect(await screen.findByTestId("orders-query-detail")).toBeVisible();
    expect(screen.getByTestId("orders-query-detail").textContent).toContain("\u6570\u636e\u6e90\u51bb\u7ed3\u96c6\u5408");
  });
});
