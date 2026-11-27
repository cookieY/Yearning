import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { openRowActions, openRowActionsAt } from "@/test/row-actions";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { HttpResponse, http } from "msw";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { stubSession } from "@/test/session";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";
import {
  ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
  ADMIN_FIXTURE_DATASOURCE_PG_ID,
  ADMIN_FIXTURE_KNOWLEDGE_TABLE_ID,
  ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
  ADMIN_FIXTURE_TOOL_BUILTIN_ID,
  ADMIN_FIXTURE_TOOL_DRAFT_ID,
} from "@/shared/mock/admin-fixture";
import { SessionProvider } from "@/features/auth/session-provider";
import { RequireAdminCapability } from "@/app/router/guards";
import i18n from "@/shared/i18n";
import AdminDatasourcesPage from "@/routes/admin/admin-datasources-page";
import AdminProvidersPage from "@/routes/admin/admin-providers-page";
import AdminAiBudgetPage from "@/routes/admin/admin-ai-budget-page";
import AdminRuleSetsPage from "@/routes/admin/admin-rule-sets-page";
import AdminReviewSkillsPage from "@/routes/admin/admin-review-skills-page";
import AdminReviewKnowledgePage from "@/routes/admin/admin-review-knowledge-page";
import { renderSkillDescription } from "@/features/admin/skill-description";
import { convertParamDrafts, paramTypeKey, stringifyParamDrafts } from "@/features/admin/skills-management-page";

/**
 * FE-F9-REVIEW-ADMIN acceptance-gate component tests. The five package
 * gates are exercised on the real page components against the stateful
 * admin fixture: Secret fields never carry a stored value (and edits are
 * replace-or-reuse), the review-input editor exposes no executable-code /
 * HTTP / database-write surface, config hashes and the review-invalidation
 * note are visible, internal experience offers no auto-learning entry, and
 * the admin capability guard keeps every surface behind can_access_admin.
 */

function setAuthBehavior(behavior: "admin" | "default"): void {
  window.localStorage.setItem("yearning-mock-auth", behavior);
}

// Base UI Select needs pointer-capture APIs jsdom does not implement; the
  // stubs let userEvent open the option popups in component tests.
beforeAll(() => {
  Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
  Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
  Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });
});

function renderPage(ui: React.ReactElement): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/"]}>
        <SessionProvider>{ui}</SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  setAuthBehavior("admin");
  stubSession(true);
  resetAdminFixture();
});

afterEach(() => {
  server.resetHandlers();
  resetAdminFixture();
  window.localStorage.removeItem("yearning-mock-auth");
});

describe("diagnostics config probe hint", () => {
  function stubDiagnosticsConfig(overrides: {
    last_probe_at: string | null;
    last_probe_state: string;
    state?: string;
  }): void {
    server.use(
      http.get(apiRoute("/admin/datasources/:datasourceId/diagnostics-config"), ({ params }) =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            datasource_id: String(params.datasourceId),
            enabled: false,
            interval_seconds: 60,
            effective_interval_seconds: 60,
            top_n: 200,
            state: overrides.state ?? "disabled",
            last_probe_state: overrides.last_probe_state,
            last_probe_at: overrides.last_probe_at,
            last_collected_at: null,
            consecutive_failures: 0,
            paused_reason: null,
            version: 1,
            updated_at: "2026-11-24T00:00:00Z",
          },
          request_id: "req-diag-config",
        }),
      ),
    );
  }

  async function openMysqlDiagnosticsConfigDialog(): Promise<void> {
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-diagnostics-config-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    await screen.findByTestId("ds-diagnostics-config-dialog");
    await screen.findByTestId("ds-diag-circuit-state");
  }

  it("hides probe state badge and error hint when no probe has completed yet", async () => {
    stubDiagnosticsConfig({
      last_probe_at: null,
      last_probe_state: "collection_failed",
    });
    await openMysqlDiagnosticsConfigDialog();
    expect(screen.queryByTestId("ds-diag-probe-state")).not.toBeInTheDocument();
    expect(screen.queryByTestId("ds-diag-probe-hint")).not.toBeInTheDocument();
  });

  it("shows static parameter and AI fallback guidance when probe completed with stats_disabled", async () => {
    stubDiagnosticsConfig({
      last_probe_at: "2026-11-24T00:00:00Z",
      last_probe_state: "stats_disabled",
    });
    await openMysqlDiagnosticsConfigDialog();
    expect(screen.getByTestId("ds-diag-probe-state")).toBeInTheDocument();
    const hint = await screen.findByTestId("ds-diag-probe-hint");
    expect(hint).toHaveTextContent("performance_schema");
    expect(hint).toHaveTextContent(/\u9759\u6001\u53c2\u6570/);
  });

  it("shows ready badge without hint when last probe succeeded", async () => {
    stubDiagnosticsConfig({
      last_probe_at: "2026-11-24T00:00:00Z",
      last_probe_state: "ready",
    });
    await openMysqlDiagnosticsConfigDialog();
    expect(screen.getByTestId("ds-diag-probe-state")).toBeInTheDocument();
    expect(screen.queryByTestId("ds-diag-probe-hint")).not.toBeInTheDocument();
  });
});

describe("gate: secrets are never prefilled", () => {
  it("leaves every datasource credential field empty when editing a configured row", async () => {
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-edit-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    await screen.findByTestId("ds-form-error", {}, { timeout: 100 }).catch(() => undefined);
    await screen.findByTestId("credential-row-readonly");
    expect(screen.queryByTestId("credential-row-query")).not.toBeInTheDocument();
    for (const group of ["readonly", "execution"]) {
      const username = screen.queryByTestId(`credential-username-${group}`);
      if (username !== null) expect(username).toHaveValue("");
      const password = screen.queryByTestId(`credential-password-${group}`);
      if (password !== null) expect(password).toHaveValue("");
    }
    const dialogTexts = screen.getAllByText(/\u5df2\u914d\u7f6e/);
    expect(dialogTexts.length).toBeGreaterThan(0);
  });

  it("never prefills the provider API key and shows the configured state instead", async () => {
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    const apiKey = await screen.findByTestId("provider-api-key");
    expect(apiKey).toHaveValue("");
    expect(screen.getByText(/API Key\uff1a\u5df2\u914d\u7f6e/)).toBeInTheDocument();
    expect(screen.queryByText(/pkey-a1/)).not.toBeInTheDocument();
  });

  it("offers no contract-hash inputs — both fingerprints are platform-derived", async () => {
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    await screen.findByTestId("provider-api-key");
    // §18.67: privacy contract / output Schema fingerprints are derived by the system (provider contract_hash.go)
    // and must never regress as user-editable inputs.
    expect(screen.queryByTestId("provider-privacy-hash")).not.toBeInTheDocument();
    expect(screen.queryByTestId("provider-schema-hash")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/\u9690\u79c1\u5951\u7ea6/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Schema Hash/)).not.toBeInTheDocument();
    // §18.68: provider kind is a controlled select (sole valid option openai_compatible), no longer free text.
    const kind = screen.getByTestId("provider-kind");
    expect(kind).toHaveAttribute("role", "combobox");
    expect(kind).toHaveTextContent(/OpenAI Compatible/);
    expect(screen.queryAllByTestId("provider-kind").filter((el) => el.tagName === "INPUT")).toHaveLength(0);
  });
});

describe("gate: no external HTTP or DB-writing Custom Tool UI", () => {
  it("renders only the governed rule definition editor — no executable affordances", async () => {
    renderPage(<AdminReviewSkillsPage />);
    await screen.findByTestId("review-input-create");
    await userEvent.click(screen.getByTestId("review-input-create"));
    await screen.findByTestId("review-input-knowledge-text");
    // Actual violation severity of mandatory rules is fixed to Critical; legacy max_severity is no longer an editable option.
    expect(screen.getByTestId("review-input-critical-severity")).toHaveTextContent("\u4e25\u91cd");
    expect(screen.queryByTestId(/^review-input-max-severity/)).not.toBeInTheDocument();
    expect(screen.queryByTestId("review-input-finding-key")).not.toBeInTheDocument();
    expect(screen.queryByTestId("review-input-title")).not.toBeInTheDocument();
    expect(screen.queryByTestId("review-input-message")).not.toBeInTheDocument();
    // …and every interactive affordance stays inside the governed surface:
    // no button or label offers code, HTTP or database writes.
    const buttons = screen.getAllByRole("button");
    for (const button of buttons) {
      expect(button.textContent).not.toMatch(/HTTP|\u6267\u884c\u4ee3\u7801|\u5199\u5e93|Webhook/);
    }
    const labels = screen.queryAllByLabelText(/\u4ee3\u7801|HTTP|\u5199\u5e93|\u7aef\u70b9/);
    expect(labels).toHaveLength(0);
  });
});

describe("gate: config hash and review invalidation impact are explicit (owner ruling: hidden in list, shown in edit dialog)", () => {
  it("keeps hashes and the outdated-impact note out of the list and inside the edit dialog", async () => {
    renderPage(<AdminReviewSkillsPage />);
    const rows = await screen.findAllByTestId(/review-input-row-/);
    // Zero hashes and zero persistent notices in the list (owner: meaningless to end users).
    expect(screen.queryByTestId("review-input-hash")).not.toBeInTheDocument();
    expect(screen.queryByTestId("review-input-outdated-note")).not.toBeInTheDocument();
    // The edit dialog hosts both: notice is always visible, hash is visible only in edit mode (existing rows always have a value).
    const row = rows[0];
    if (row === undefined) throw new Error("expected a seeded skill row");
    await openRowActionsAt(within(row).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-edit-/));
    expect(await screen.findByTestId("review-input-outdated-note")).toBeInTheDocument();
    expect(screen.getByTestId("review-input-hash").textContent).toMatch(/^[0-9a-f]{16}$/);
  });

  it("previews rule-set impact: bound flows and the hash-change warning", async () => {
    renderPage(<AdminRuleSetsPage />);
    const row = await screen.findByTestId(/rule-set-row-/);
    expect(within(row).getByTestId("rule-set-hash")).toBeInTheDocument();
    await openRowActions(/rule-set-actions-/);
    await userEvent.click(screen.getByTestId(/rule-set-edit-/));
    const preview = await screen.findByTestId("rule-set-impact-preview");
    expect(within(preview).getByText(/1 \u4e2a\u6d41\u7a0b/)).toBeInTheDocument();
    expect(within(preview).getByText(/\u751f\u4ea7\u53d8\u66f4\u9ed8\u8ba4\u6d41\u7a0b/)).toBeInTheDocument();
    expect(within(preview).getByText(/\u5f15\u7528\u65e7 Hash \u7684\u672a\u63d0\u4ea4\u9884\u5ba1\u5c06\u5931\u6548/)).toBeInTheDocument();
  });
});

describe("gate: internal knowledge has no auto-learning entry and is manageable by admin only", () => {
  it("offers no auto-learning entry and lists only governed provenances", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    await screen.findByTestId("review-input-create");
    await screen.findAllByTestId(/review-input-row-/);
    const buttons = screen.getAllByRole("button");
    for (const button of buttons) {
      expect(button.textContent).not.toMatch(/\u81ea\u52a8\u5b66\u4e60|\u81ea\u52a8\u6c89\u6dc0|auto-learn/i);
    }
    expect(screen.getAllByText("\u4eba\u5de5\u6c89\u6dc0").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Finding \u8f6c\u5316").length).toBeGreaterThan(0);
  });
});

describe("gate: accessible to admin only", () => {
  it("redirects a non-admin session to /403 on every admin route", async () => {
    setAuthBehavior("default");
    stubSession(false);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={["/admin/review-engine/skills"]}>
          <SessionProvider>
            <Routes>
              <Route
                path="/admin/review-engine/skills"
                element={
                  <RequireAdminCapability>
                    <AdminReviewSkillsPage />
                  </RequireAdminCapability>
                }
              />
              <Route path="/403" element={<div data-testid="forbidden">403</div>} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByTestId("forbidden")).toBeInTheDocument());
  });
});

describe("admin page flows", () => {
  it("creates a datasource through the form", async () => {
    renderPage(<AdminDatasourcesPage />);
    await userEvent.click(await screen.findByTestId("ds-create"));
    await userEvent.type(screen.getByTestId("ds-name"), "new-warehouse");
    await userEvent.type(screen.getByTestId("ds-host"), "10.0.0.77");
    expect(screen.queryByTestId("ds-version-constraint")).not.toBeInTheDocument();
    await userEvent.type(screen.getByTestId("credential-username-readonly"), "ro_user");
    await userEvent.type(screen.getByTestId("credential-password-readonly"), "secret-pw-1");
    await userEvent.click(screen.getByTestId("ds-submit"));
    // The owner test catalog pushed the table past one page; the created row
    // (newest created_at) sits on page 2 under the backend's asc ordering —
    // widen the page to see it.
    await userEvent.click(await screen.findByTestId("datasources-page-size-50"));
    await waitFor(() => {
      expect(screen.getByText("new-warehouse")).toBeInTheDocument();
    }, { timeout: 4000 });
  });

  it("runs the knowledge eval and shows the governed result", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    const tableEval = await screen.findByTestId(`review-input-evaluate-${ADMIN_FIXTURE_KNOWLEDGE_TABLE_ID}`);
    await userEvent.click(tableEval);
    const result = await screen.findByTestId("knowledge-eval-result");
    expect(within(result).getByText("\u901a\u8fc7")).toBeInTheDocument();
  });

  it("saves a high-impact budget change directly by transparently acquiring an impact token", async () => {
    renderPage(<AdminAiBudgetPage />);
    const enforced = await screen.findByTestId("ai-budget-enforced");
    await waitFor(() => expect(enforced).not.toBeDisabled());
    await userEvent.click(enforced);
    await userEvent.click(await screen.findByTestId("ai-budget-save"));
    await screen.findByTestId("ai-budget-saved");
  });

  it("confirms a provider delete before executing it", async () => {
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-delete-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("\u5220\u9664 Provider")).toBeInTheDocument();
    expect(within(dialog).getByText(/\u786e\u8ba4\u5220\u9664 Provider primary-glm/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "\u53d6\u6d88" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("marks the primary provider by selection order", async () => {
    renderPage(<AdminProvidersPage />);
    const primaryRole = await screen.findByTestId(`provider-role-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    expect(primaryRole).toHaveTextContent("\u4e3b");
  });

  it("marks draft skills as not bindable in the rule-set editor", async () => {
    renderPage(<AdminRuleSetsPage />);
    await openRowActionsAt(await screen.findByTestId(/rule-set-actions-/));
    await userEvent.click(await screen.findByTestId(/rule-set-edit-/));
    await screen.findByTestId("rule-set-tools");
    expect(screen.getByText("Draft \u4e0d\u53ef\u7ed1\u5b9a")).toBeInTheDocument();
  });

  it("composes skills by description with search and select-all (§18.80)", async () => {
    renderPage(<AdminRuleSetsPage />);
    await openRowActionsAt(await screen.findByTestId(/rule-set-actions-/));
    await userEvent.click(await screen.findByTestId(/rule-set-edit-/));
    await screen.findByTestId("rule-set-tools");
    // Hash display replaced by skill description: the dialog no longer renders a 64-char hash, rendering description copy instead.
    expect(screen.getByText(/\u5185\u7f6e\u8bcd\u6cd5\u5b88\u536b/)).toBeInTheDocument();
    expect(screen.queryByText(/^[0-9a-f]{64}$/)).not.toBeInTheDocument();
    // Select all: 2 bindable items with 1 already selected (indeterminate); clicking select-all once selects 2/2.
    expect(screen.getByTestId("rule-set-tools-select-all-count")).toHaveTextContent("1/2");
    expect(screen.getByTestId("rule-set-tools-select-all")).not.toBeChecked();
    await userEvent.click(screen.getByTestId("rule-set-tools-select-all"));
    expect(screen.getByTestId("rule-set-tools-select-all-count")).toHaveTextContent("2/2");
    expect(screen.getByTestId("rule-set-tools-select-all")).toBeChecked();
    // Search filters down to the sole draft row: when no bindable items match, select-all is disabled (via aria-disabled in Base UI) and count drops to 0.
    await userEvent.type(screen.getByTestId("rule-set-tools-search"), "table-comment");
    expect(screen.getByTestId("rule-set-tools-select-all")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByTestId("rule-set-tools-select-all-count")).toHaveTextContent("0/0");
    expect(screen.getByTestId(/rule-set-tool-option-/)).toBeInTheDocument();
    // Clearing search restores the full table.
    await userEvent.click(screen.getByTestId("rule-set-tools-search-clear"));
    expect(screen.getByTestId("rule-set-tools-select-all-count")).toHaveTextContent("2/2");
  });

  it("shows the skill description under the table name (§18.80)", async () => {
    renderPage(<AdminReviewSkillsPage />);
    const row = await screen.findByTestId(`review-input-row-${ADMIN_FIXTURE_TOOL_BUILTIN_ID}`);
    expect(
      within(row).getByTestId(`review-input-description-${ADMIN_FIXTURE_TOOL_BUILTIN_ID}`),
    ).toHaveTextContent("\u5185\u7f6e\u8bcd\u6cd5\u5b88\u536b");
  });

  it("tunes builtin parameter values without touching the system-owned rule text (§18.81)", async () => {
    renderPage(<AdminReviewSkillsPage />);
    const row = await screen.findByTestId(`review-input-row-${ADMIN_FIXTURE_TOOL_BUILTIN_ID}`);
    await openRowActionsAt(within(row).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-edit-/));
    const section = await screen.findByTestId("review-input-params");
    // Rule body is locked (owner ruling: prompt is immutable), while parameter controls remain editable.
    const ruleText = screen.getByTestId("review-input-knowledge-text");
    expect(ruleText).toBeDisabled();
    expect(section).toHaveTextContent("max_in_items");
    const valueInput = within(section).getByTestId("review-input-param-max_in_items");
    // Invalid value (cleared): intercepted inline without sending a request, keeping the dialog open.
    await userEvent.clear(valueInput);
    await userEvent.click(screen.getByTestId("review-input-submit"));
    expect(screen.getByTestId("review-input-param-error")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    // Valid value: 1000 -> 2000, save succeeds and dialog closes.
    await userEvent.type(valueInput, "2000");
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    // Reopening echoes the new value; rule body remains the original system prompt.
    await openRowActionsAt(within(row).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-edit-/));
    const reopened = await screen.findByTestId("review-input-params");
    expect(within(reopened).getByTestId("review-input-param-max_in_items")).toHaveValue(2000);
    expect(screen.getByTestId("review-input-knowledge-text")).toBeDisabled();
  });

  it("renders skill-description placeholders with current parameter values (§18.80 addendum 1)", () => {
    expect(
      renderSkillDescription("\u884c\u6570\u4e0d\u5f97\u8d85\u8fc7{{max_affect_rows}}", { max_affect_rows: 100000 }),
    ).toBe("\u884c\u6570\u4e0d\u5f97\u8d85\u8fc7100000");
    // Missing parameter keeps the placeholder verbatim (fallback when user-authored descriptions reference non-existent keys).
    expect(renderSkillDescription("\u4e0d\u5f97\u8d85\u8fc7{{missing_key}}", {})).toBe("\u4e0d\u5f97\u8d85\u8fc7{{missing_key}}");
    expect(renderSkillDescription("\u7eaf\u6587\u672c\u8bf4\u660e", {})).toBe("\u7eaf\u6587\u672c\u8bf4\u660e");
    expect(
      renderSkillDescription("\u7981\u7528\u5f15\u64ce\uff1a{{forbidden_engines}}", { forbidden_engines: ["myisam", "memory"] }),
    ).toBe("\u7981\u7528\u5f15\u64ce\uff1amyisam, memory");
  });
});

// ---- dialog flows: create/replace/delete on both registries ----

describe("review-input dialog flows", () => {
  it("submits a new skill with Critical as the effective severity", async () => {
    const writes: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/admin/prompt-tools"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        writes.push(body);
      }),
    );
    renderPage(<AdminReviewSkillsPage />);
    await userEvent.click(await screen.findByTestId("review-input-create"));
    await userEvent.type(screen.getByTestId("review-input-name"), "column-null-check");
    await userEvent.type(screen.getByTestId("review-input-knowledge-text"), "Columns must be NOT NULL unless justified.");
    expect(screen.getByTestId("review-input-critical-severity")).toBeInTheDocument();
    expect(screen.queryByTestId(/^review-input-max-severity/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => {
      expect(writes).toHaveLength(1);
    });
    expect(writes[0]?.definition).toMatchObject({ max_severity: "critical" });
  });

  it("preserves legacy draft max_severity while showing Critical as the effective severity", async () => {
    const writes: Array<Record<string, unknown>> = [];
    server.use(
      http.put(apiRoute("/admin/prompt-tools/:promptToolId"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        writes.push(body);
      }),
    );
    renderPage(<AdminReviewSkillsPage />);
    const row = await screen.findByTestId(`review-input-row-${ADMIN_FIXTURE_TOOL_DRAFT_ID}`);
    await openRowActionsAt(within(row).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-edit-/));
    await screen.findByTestId("review-input-critical-severity");
    expect(screen.queryByTestId(/^review-input-max-severity/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => {
      expect(writes).toHaveLength(1);
    });
    expect(writes[0]?.definition).toMatchObject({ max_severity: "low" });
  });

  it("submits a datasource-scoped internal-experience note with non-blocking advisory severity", async () => {
    const writes: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/admin/knowledge-entries"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        writes.push(body);
      }),
    );
    renderPage(<AdminReviewKnowledgePage />);
    await userEvent.click(await screen.findByTestId("review-input-create"));
    // Verify global scope is retired from creation choices.
    await userEvent.click(screen.getByTestId("knowledge-scope"));
    expect(screen.queryByRole("option", { name: "\u5168\u5c40" })).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("option", { name: "\u6570\u636e\u6e90 / \u5e93\u7ea7" }));

    await userEvent.click(screen.getByTestId("knowledge-datasource"));
    const dsOptions = await screen.findAllByRole("option");
    const firstDs = dsOptions[0];
    if (firstDs === undefined) throw new Error("expected datasource option");
    await userEvent.click(firstDs);
    await userEvent.type(screen.getByTestId("knowledge-database"), "app_db");

    await userEvent.type(screen.getByTestId("review-input-name"), "charset-policy");
    await userEvent.type(screen.getByTestId("review-input-knowledge-text"), "Keep the default charset.");
    expect(screen.getByTestId("knowledge-advisory-policy")).toBeInTheDocument();
    expect(screen.getByTestId("knowledge-max-severity")).toBeInTheDocument();
    expect(screen.queryByTestId("review-input-critical-severity")).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => {
      expect(writes).toHaveLength(1);
    });
    expect(writes[0]).toMatchObject({ scope_type: "datasource" });
    expect(writes[0]?.definition).toMatchObject({ max_severity: "medium" });
  });

  it("replaces an existing skill and bumps its version", async () => {
    renderPage(<AdminReviewSkillsPage />);
    const rows = await screen.findAllByTestId(/review-input-row-/);
    const secondRow = rows.at(1);
    if (secondRow === undefined) throw new Error("expected two seeded skills");
    await openRowActionsAt(within(secondRow).getByTestId(/review-input-actions-/));
    const editButton = screen.getByTestId(/review-input-edit-/);
    await userEvent.click(editButton);
    await screen.findByTestId("review-input-knowledge-text");
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("opens the skill test dialog, optimizes prompt, generates test cases, edits SQL, and runs the test", async () => {
    let optCount = 0;
    let genCount = 0;
    let runCount = 0;
    server.use(
      http.post(/\/admin\/prompt-tools\/prompt-optimizations$/, () => {
        optCount += 1;
        if (optCount === 1) {
          return HttpResponse.json(
            { err_code: 1001, message: "provider error", data: null, request_id: "req-opt-err" },
            { status: 400 },
          );
        }
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            knowledge_text: "Check that every UPDATE or DELETE includes a bounded WHERE clause.",
          },
          request_id: "req-skill-opt",
        });
      }),
      http.post(/\/admin\/prompt-tools\/test-cases$/, () => {
        genCount += 1;
        if (genCount === 1) {
          return HttpResponse.json(
            { err_code: 1001, message: "gen error", data: null, request_id: "req-gen-err" },
            { status: 400 },
          );
        }
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            cases: [
              {
                id: "tc-1",
                sql: "UPDATE orders SET status = 'shipped' WHERE id = 100;",
                expected_compliant: true,
                rationale: "Has explicit WHERE clause on primary key.",
              },
              {
                id: "tc-2",
                sql: "UPDATE orders SET status = 'shipped';",
                expected_compliant: false,
                rationale: "Missing WHERE clause updates entire table.",
              },
            ],
          },
          request_id: "req-skill-gen",
        });
      }),
      http.post(/\/admin\/prompt-tools\/test-runs$/, () => {
        runCount += 1;
        if (runCount === 1) {
          return HttpResponse.json(
            { err_code: 1001, message: "run error", data: null, request_id: "req-run-err" },
            { status: 400 },
          );
        }
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            passed: runCount > 2,
            matched_count: runCount > 2 ? 2 : 1,
            total_count: 2,
            results: [
              {
                id: "tc-1",
                expected_compliant: true,
                actual_compliant: true,
                matched_expectation: true,
                finding_summary: "Compliant statement.",
              },
              {
                id: "tc-2",
                expected_compliant: false,
                actual_compliant: runCount === 2,
                matched_expectation: runCount > 2,
                finding_summary: "Violation evaluated.",
              },
            ],
          },
          request_id: "req-skill-run",
        });
      }),
    );

    renderPage(<AdminReviewSkillsPage />);
    const rows = await screen.findAllByTestId(/review-input-row-/);
    const secondRow = rows.at(1);
    if (secondRow === undefined) throw new Error("expected two seeded skills");
    await openRowActionsAt(within(secondRow).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-edit-/));
    await screen.findByTestId("review-input-knowledge-text");

    // 1st click triggers error branch, 2nd click succeeds
    await userEvent.click(screen.getByTestId("skill-optimize-prompt"));
    await screen.findByTestId("review-input-error");
    await userEvent.click(screen.getByTestId("skill-optimize-prompt"));
    await waitFor(() =>
      expect(screen.getByTestId("review-input-knowledge-text")).toHaveValue(
        "Check that every UPDATE or DELETE includes a bounded WHERE clause.",
      ),
    );

    await userEvent.click(screen.getByTestId("skill-test-open"));
    const testDialog = await screen.findByTestId("skill-test-dialog");
    expect(within(testDialog).getByTestId("skill-test-empty")).toBeInTheDocument();

    // 1st generate fails, 2nd generate succeeds
    await userEvent.click(within(testDialog).getByTestId("skill-test-generate"));
    await waitFor(() => {
      expect(genCount).toBe(1);
    });
    await userEvent.click(within(testDialog).getByTestId("skill-test-generate"));
    const sqlInput1 = await within(testDialog).findByTestId("skill-test-sql-1");
    fireEvent.change(sqlInput1, {
      target: { value: "UPDATE orders SET status = 'paid' WHERE id = 1;" },
    });

    // 1st run fails, 2nd run returns passed=false, 3rd run returns passed=true
    await userEvent.click(within(testDialog).getByTestId("skill-test-run"));
    await waitFor(() => {
      expect(runCount).toBe(1);
    });
    await userEvent.click(within(testDialog).getByTestId("skill-test-run"));
    expect(await within(testDialog).findByTestId("skill-test-summary")).toBeInTheDocument();
    expect(within(testDialog).getByTestId("skill-test-summary-badge")).toBeInTheDocument();
    expect(within(testDialog).getByTestId("skill-test-actual-1")).toBeInTheDocument();
    expect(within(testDialog).getByTestId("skill-test-actual-2")).toBeInTheDocument();
    expect(within(testDialog).getByTestId("skill-test-match-1")).toBeInTheDocument();
    expect(within(testDialog).getByTestId("skill-test-match-2")).toBeInTheDocument();
    expect(within(testDialog).getByTestId("skill-test-finding-1")).toBeInTheDocument();

    await userEvent.click(within(testDialog).getByTestId("skill-test-run"));
    await waitFor(() => {
      expect(runCount).toBe(3);
    });
  });

  it("deletes a skill and surfaces the referenced error on a bound one", async () => {
    renderPage(<AdminReviewSkillsPage />);
    const rows = await screen.findAllByTestId(/review-input-row-/);
    // draft tool is unreferenced → delete succeeds
    const secondRow = rows.at(1);
    if (secondRow === undefined) throw new Error("expected seeded skills");
    await openRowActionsAt(within(secondRow).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-delete-/));
    await userEvent.click(screen.getByTestId("review-input-delete-confirm"));
    await waitFor(() => {
      // two rows remain: the bound enabled tool and the undeletable built-in
      expect(screen.queryAllByTestId(/review-input-row-/)).toHaveLength(2);
    });
    // enabled tool is referenced by the seeded rule set → 1006 error; the
    // delete affordance lives in the row's ⋯ menu (portal, screen-scoped)
    const boundRow = (await screen.findAllByTestId(/review-input-row-/))
      .find((row) => row.textContent.includes("dml-where-guard"));
    if (boundRow === undefined) throw new Error("bound row not rendered");
    await openRowActionsAt(within(boundRow).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-delete-/));
    await userEvent.click(screen.getByTestId("review-input-delete-confirm"));
    await screen.findByTestId("review-input-delete-error");
  });

  it("renders scope, purpose and provenance facts for the seeded entries", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    await screen.findAllByTestId(/review-input-row-/);
    // Table-scoped entry shows the database.table tuple.
    expect(screen.getByText("orderdb.orders")).toBeInTheDocument();
    // Datasource-scoped entry shows the scope label; converted rows keep
    // their provenance badge and purpose line.
    expect(screen.getAllByText("\u6570\u636e\u6e90 / \u5e93\u7ea7").length).toBeGreaterThan(0);
    expect(screen.getByText("Finding \u8f6c\u5316")).toBeInTheDocument();
    expect(screen.getAllByText("Converted from a historical review finding.").length).toBeGreaterThan(0);
  });

  it("shows the disabled state badge for a disabled entry", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    await screen.findAllByTestId(/review-input-row-/);
    const badges = screen.getAllByTestId("review-input-state");
    expect(badges.some((badge) => badge.textContent === "\u505c\u7528")).toBe(true);
    expect(badges.some((badge) => badge.textContent === "\u8349\u7a3f")).toBe(true);
  });
});

describe("connection test flow", () => {
  it("runs the test on the pg datasource and renders the capability matrix with unavailable badges", async () => {
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    // Expanding the matrix is a user-event side effect; the capabilities
    // query polls until the async probe task materializes the facts.
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    await userEvent.click(screen.getByTestId(`ds-test-button-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`));
    const capabilities = await screen.findByTestId("ds-capabilities", {}, { timeout: 4000 });
    await waitFor(() => expect(capabilities).toHaveTextContent("16.3"), { timeout: 4000 });
    await waitFor(() => expect(screen.getByTestId("ds-capability-execution")).toHaveTextContent("\u4e0d\u53ef\u7528"), { timeout: 4000 });
    expect(screen.getByText("\u90e8\u5206\u80fd\u529b\u4e0d\u53ef\u7528")).toBeInTheDocument();
    // Clicking the inline close button dismisses the expanded capability strip cleanly.
    await userEvent.click(screen.getByTestId("ds-capabilities-close"));
    expect(screen.queryByTestId("ds-capabilities")).toBeNull();
  });

  it("never renders an empty table row when capabilities read fails after connection test", async () => {
    server.use(
      http.get(apiRoute("/admin/datasources/:id/capabilities"), () =>
        HttpResponse.json(
          {
            type: "about:blank",
            title: "Too Many Requests",
            status: 429,
            detail: "connection test rate limited",
          },
          { status: 429 },
        ),
      ),
    );
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    const initialRows = document.querySelectorAll("tbody tr").length;

    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    await userEvent.click(screen.getByTestId(`ds-test-button-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`));
    await waitFor(() => {
      expect(screen.queryByTestId(`ds-capabilities-row-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`)).toBeNull();
    });
    expect(document.querySelectorAll("tbody tr")).toHaveLength(initialRows);
  });

  // §18.61: connection-test purpose is a persisted datasource attribute — no purpose selector appears in the table,
  // and changes happen only in the edit dialog (dialog switching and payload assertions live in admin-pages-popups).
  // §18.62: the form presents Read-Only / Read-Write classes (same vocabulary as the table credential badge).
  it("keeps the purpose select out of the table and prefills the credential class in the edit dialog", async () => {
    renderPage(<AdminDatasourcesPage />);
    const row = await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    expect(within(row).queryAllByTestId(/ds-test-purpose-/)).toHaveLength(0);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-edit-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    // Prefill = stored value (review) presented as Read-Only test credential option.
    expect(await screen.findByTestId("ds-test-purpose")).toHaveTextContent("\u53ea\u8bfb");
    expect(screen.getByText(/\u53ea\u8bfb\uff08\u5ba1\u6838 \/ \u67e5\u8be2\uff09\u8d26\u53f7\u63a2\u6d4b.*\u6267\u884c\uff08\u8bfb\u5199\uff09\u8d26\u53f7\u63a2\u6d4b/)).toBeInTheDocument();
  });

  // Merged read-only row: dropping read-only leaves only execution, so test_purpose falls back to execution.
  it("resolves test_purpose onto execution when the payload drops the merged read-only credential", async () => {
    const captured = captureDatasourcePut();
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-edit-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    await screen.findByTestId("credential-row-readonly");
    await userEvent.click(screen.getByTestId("credential-include-readonly"));
    await userEvent.click(screen.getByTestId("ds-submit"));
    await waitFor(() => { expect(captured.body).not.toBeNull(); });
    expect(captured.body?.test_purpose).toBe("execution");
  });
});

// ---- dialog submit paths, edit prefill, eval fail, list states ----

describe("review-input dialog coverage", () => {
  it("surfaces the validation error inside the dialog and closes on success", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    await userEvent.click(await screen.findByTestId("review-input-create"));
    // Empty name → 1001 → the dialog stays open with the localized error.
    await userEvent.click(screen.getByTestId("review-input-submit"));
    const error = await screen.findByTestId("review-input-error");
    expect(error).toBeInTheDocument();
    // Then a valid datasource-scoped create closes the dialog and adds the row.
    await userEvent.click(screen.getByTestId("knowledge-datasource"));
    const dsOptions = await screen.findAllByRole("option");
    const firstDs = dsOptions[0];
    if (firstDs === undefined) throw new Error("expected datasource option");
    await userEvent.click(firstDs);
    await userEvent.type(screen.getByTestId("knowledge-database"), "app_db");
    await userEvent.type(screen.getByTestId("review-input-name"), "ds-manual-entry");
    await userEvent.type(screen.getByTestId("review-input-knowledge-text"), "Keep default charset.");
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => expect(screen.getByText("ds-manual-entry")).toBeInTheDocument());
  });

  it("prefills the table-scoped edit form and submits the replace", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    const rows = await screen.findAllByTestId(/review-input-row-/);
    const tableRow = rows.find((row) => row.textContent.includes("orderdb.orders"));
    if (tableRow === undefined) throw new Error("table-scoped row not rendered");
    await userEvent.click(within(tableRow).getByTestId(/review-input-edit-/));
    // Scoped form prefills: datasource select, database and table inputs.
    expect(screen.getByTestId("knowledge-datasource")).toBeInTheDocument();
    expect(screen.getByTestId("knowledge-table")).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("omits hidden table_name when broadening table-scoped knowledge to datasource and requires re-scoping legacy global", async () => {
    const capturedBodies: Record<string, unknown>[] = [];
    server.use(
      http.put(apiRoute("/admin/knowledge-entries/:id"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        capturedBodies.push(body);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "k-broaden-1",
            name: typeof body.name === "string" ? body.name : "orderdb.orders",
            purpose: typeof body.purpose === "string" ? body.purpose : null,
            state: typeof body.state === "string" ? body.state : "draft",
            scope_type: typeof body.scope_type === "string" ? body.scope_type : "datasource",
            datasource_id: typeof body.datasource_id === "string" ? body.datasource_id : null,
            database_name: typeof body.database_name === "string" ? body.database_name : null,
            table_name: typeof body.table_name === "string" ? body.table_name : null,
            definition: body.definition ?? { knowledge_text: "t", max_severity: "medium", version: 1 },
            provenance: "manual",
            source_finding_id: null,
            version: 2,
            config_hash: "cfg-broaden",
            created_at: "2026-08-31T00:00:00Z",
            updated_at: "2026-08-31T00:00:00Z",
          },
          request_id: "req-k-broaden",
        });
      }),
    );

    renderPage(<AdminReviewKnowledgePage />);
    const rows = await screen.findAllByTestId(/review-input-row-/);
    const tableRow = rows.find((row) => row.textContent.includes("orderdb.orders"));
    if (tableRow === undefined) throw new Error("table-scoped row not rendered");

    // Case 1: broaden table -> datasource (datasource_id and database_name remain, table_name omitted; global absent).
    await userEvent.click(within(tableRow).getByTestId(/review-input-edit-/));
    await userEvent.click(screen.getByTestId("knowledge-scope"));
    expect(screen.queryByRole("option", { name: "\u5168\u5c40" })).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("option", { name: "\u6570\u636e\u6e90 / \u5e93\u7ea7" }));
    expect(screen.getByTestId("knowledge-database")).toBeInTheDocument();
    expect(screen.queryByTestId("knowledge-table")).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("review-input-submit"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    expect(capturedBodies).toHaveLength(1);
    expect(capturedBodies[0]?.scope_type).toBe("datasource");
    expect(capturedBodies[0]?.datasource_id).toBeTruthy();
    expect(capturedBodies[0]?.database_name).toBe("orderdb");
    expect(capturedBodies[0]?.table_name).toBeUndefined();

    // Case 2: legacy global + critical entry blocks submit until re-scoped & downgraded.
    const legacyRow = (await screen.findAllByTestId(/review-input-row-/)).find((r) =>
      r.textContent.includes("prod-window-policy"),
    );
    if (legacyRow === undefined) throw new Error("legacy global row not rendered");
    await userEvent.click(within(legacyRow).getByTestId(/review-input-edit-/));
    expect(screen.getByTestId("knowledge-legacy-global-warning")).toBeInTheDocument();
    expect(screen.getByTestId("knowledge-legacy-severity-warning")).toBeInTheDocument();
    expect(screen.getByTestId("review-input-submit")).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "\u53d6\u6d88" }));
  });

  it("deletes both a draft entry and a converted entry successfully", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    const rows = await screen.findAllByTestId(/review-input-row-/);
    const draftRow = rows.find((row) => row.textContent.includes("orders-huge-table"));
    if (draftRow === undefined) throw new Error("draft row not rendered");
    await userEvent.click(within(draftRow).getByTestId(/review-input-delete-/));
    await userEvent.click(screen.getByTestId("review-input-delete-confirm"));
    await waitFor(() => {
      expect(screen.getAllByTestId(/review-input-row-/)).toHaveLength(2);
    });
    const convertedButton = (await screen.findAllByTestId(/review-input-row-/))
      .map((row) => ({ row, button: within(row).queryByTestId(/review-input-delete-/) }))
      .find(({ row }) => row.textContent.includes("charset-uniformity"))?.button;
    if (convertedButton === undefined || convertedButton === null) throw new Error("converted row not rendered");
    await userEvent.click(convertedButton);
    await userEvent.click(screen.getByTestId("review-input-delete-confirm"));
    await waitFor(() => {
      expect(screen.getAllByTestId(/review-input-row-/)).toHaveLength(1);
    });
  });

  it("renders a failing evaluation with its findings list", async () => {
    server.use(
      http.post(apiRoute("/admin/knowledge-entries/:id/evaluations"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            pass: false,
            schema_subset_ok: false,
            privacy_ok: false,
            injection_ok: false,
            severity_ok: false,
            findings: ["definition must carry non-empty knowledge_text and a valid max_severity"],
            checked_at: "2026-08-31T00:00:00Z",
          },
          request_id: "eval-fail",
        }),
      ),
    );
    renderPage(<AdminReviewKnowledgePage />);
    const evalButtons = await screen.findAllByTestId(/review-input-evaluate-/);
    const firstEval = evalButtons.at(0);
    if (firstEval === undefined) throw new Error("no evaluate buttons rendered");
    await userEvent.click(firstEval);
    const result = await screen.findByTestId("knowledge-eval-result");
    expect(within(result).getByText("\u672a\u901a\u8fc7")).toBeInTheDocument();
    expect(within(result).getByText(/definition must carry non-empty knowledge_text/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "\u5173\u95ed" }));
    await waitFor(() => expect(screen.queryByTestId("knowledge-eval-result")).not.toBeInTheDocument());
  });

  it("renders the error state and the empty state of the registries", async () => {
    server.use(
      http.get(apiRoute("/admin/prompt-tools"), () =>
        HttpResponse.json({ err_code: 1002, message: "boom", data: null, request_id: "r1", retryable: false }),
      ),
    );
    renderPage(<AdminReviewSkillsPage />);
    await screen.findByText(/\u8bf7\u6c42\u672a\u80fd\u5b8c\u6210|\u64cd\u4f5c\u672a\u80fd\u5b8c\u6210|\u8d44\u6e90\u4e0d\u5b58\u5728/);
  });

  it("renders the empty registry state", async () => {
    server.use(
      http.get(apiRoute("/admin/prompt-tools"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: [], page: { next_cursor: null, has_more: false } },
          request_id: "r2",
        }),
      ),
    );
    renderPage(<AdminReviewSkillsPage />);
    await screen.findByText("\u6682\u65e0\u6280\u80fd\u5305");
  });
});

// ---- FE-F9-REVIEW-ADMIN-ALIGNMENT gates -----------------------------------

/** Captures the request body an MSW-overridden write actually sent. */
function captureDatasourcePut(): { body: Record<string, unknown> | null } {
  const captured: { body: Record<string, unknown> | null } = { body: null };
  server.use(
    http.put(apiRoute("/admin/datasources/:datasourceId"), async ({ request }) => {
      captured.body = (await request.json()) as Record<string, unknown>;
      return HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: {
          id: ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
          name: "prod-order-mysql",
          engine: "mysql",
          compatibility_mode: "mysql",
          deployment_kind: "native",
          host: "10.0.0.11",
          port: 3306,
          database_name: null,
          enabled: true,
          test_purpose: "review",
          credential_status: { review: true, query: true, execution: true },
          tls_verified: false,
          referenced_by_flow_count: 1,
          version: 99,
          created_at: "2026-08-31T00:00:00Z",
          updated_at: "2026-08-31T00:00:00Z",
        },
        request_id: "capture",
      });
    }),
  );
  return captured;
}

describe("gate: builtin skill lock badge and locked definition surface", () => {
  it("renders the lock badge with a visible explanation and disables delete", async () => {
    renderPage(<AdminReviewSkillsPage />);
    const row = await screen.findByTestId(`review-input-row-${ADMIN_FIXTURE_TOOL_BUILTIN_ID}`);
    expect(within(row).getByTestId(`review-input-builtin-${ADMIN_FIXTURE_TOOL_BUILTIN_ID}`)).toHaveTextContent(
      "\u5185\u7f6e",
    );
    // No inline explanation text on the row anymore (moved into the edit dialog builtin notice).
    expect(within(row).queryByText(/\u4ec5\u72b6\u6001\u53ef\u5207\u6362/)).not.toBeInTheDocument();
    await openRowActionsAt(within(row).getByTestId(/review-input-actions-/));
    // Base UI menu items disable via aria-disabled (divs carry no native disabled attr)
    expect(screen.getByTestId(/review-input-delete-/)).toHaveAttribute("aria-disabled", "true");
  });

  it("locks the definition face in the editor and keeps the state select active", async () => {
    renderPage(<AdminReviewSkillsPage />);
    const row = await screen.findByTestId(`review-input-row-${ADMIN_FIXTURE_TOOL_BUILTIN_ID}`);
    await openRowActionsAt(within(row).getByTestId(/review-input-actions-/));
    await userEvent.click(screen.getByTestId(/review-input-edit-/));
    expect(await screen.findByTestId("review-input-builtin-notice")).toBeInTheDocument();
    expect(screen.getByTestId("review-input-name")).toBeDisabled();
    expect(screen.getByTestId("review-input-engine")).toBeDisabled();
    expect(screen.getByTestId("review-input-knowledge-text")).toBeDisabled();
    expect(screen.getByTestId("review-input-knowledge-text")).toBeDisabled();
    expect(screen.getByTestId("review-input-critical-severity")).toHaveTextContent("\u4e25\u91cd");
    expect(screen.queryByTestId(/^review-input-max-severity/)).not.toBeInTheDocument();
    expect(screen.getByTestId("review-input-state-select")).toBeEnabled();
  });

  // The state TOGGLE itself lives in admin-pages-popups.test.tsx: Base UI
  // 1.7 select popups leak their layer stack across tests under jsdom, so
  // every popup interaction is consolidated into one isolated file.
});

describe("gate: three credential form modes (keep selectable only on configured rows)", () => {
  it("submits keep for configured rows with no username and no password in the payload", async () => {
    const captured = captureDatasourcePut();
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-edit-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    await screen.findByTestId("credential-row-readonly");
    // Configured rows open in keep mode: neither input is even rendered.
    expect(screen.queryByTestId("credential-username-readonly")).not.toBeInTheDocument();
    expect(screen.queryByTestId("credential-password-readonly")).not.toBeInTheDocument();
    expect(screen.getByTestId("credential-keep-note-readonly")).toBeInTheDocument();
    expect(screen.queryByTestId("ds-version-constraint")).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("ds-submit"));
    await waitFor(() => { expect(captured.body).not.toBeNull(); });
    const credentials = captured.body?.credentials as Array<Record<string, unknown>>;
    expect(credentials).toHaveLength(3);
    for (const credential of credentials) {
      expect(credential.reuse_credential_purpose).toBe(credential.purpose);
      expect("username" in credential).toBe(false);
      expect("password" in credential).toBe(false);
    }
    // Full-replacement TLS write is explicit: null, never absent.
    expect(captured.body?.tls).toBeNull();
  });

  // Option-availability and mode-switch assertions (which need an open
  // select popup) live in admin-pages-popups.test.tsx — see the isolation
  // note there. Popup-free coverage below proves the payload semantics.
  it("blocks the submit while base fields are missing or no purpose is included", async () => {
    renderPage(<AdminDatasourcesPage />);
    await userEvent.click(await screen.findByTestId("ds-create"));
    await screen.findByTestId("credential-row-readonly");
    // Missing base/credential fields still block submit without a version constraint input.
    expect(screen.queryByTestId("ds-version-constraint")).not.toBeInTheDocument();
    await userEvent.type(screen.getByTestId("ds-name"), "guard-check");
    await userEvent.type(screen.getByTestId("ds-host"), "10.0.0.95");
    expect(screen.getByTestId("ds-submit")).toBeDisabled();
    expect(screen.getByTestId("ds-form-invalid-hint")).not.toHaveTextContent("\u7248\u672c\u7ea6\u675f");
    await userEvent.type(screen.getByTestId("credential-username-readonly"), "ro");
    await userEvent.type(screen.getByTestId("credential-password-readonly"), "pw-guard-1");
    expect(screen.getByTestId("ds-submit")).toBeEnabled();
    // Unchecking the last purpose violates credentials minItems 1.
    await userEvent.click(screen.getByTestId("credential-include-readonly"));
    expect(screen.getByTestId("ds-submit")).toBeDisabled();
    expect(screen.getByTestId("ds-form-invalid-hint")).toBeInTheDocument();
  });
  it("drops purposes that are unchecked from the full-replacement payload", async () => {
    const captured = captureDatasourcePut();
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-edit-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    await screen.findByTestId("credential-row-readonly");
    // Unchecking execution removes it: the write is a full replacement.
    await userEvent.click(screen.getByTestId("credential-include-execution"));
    await userEvent.click(screen.getByTestId("ds-submit"));
    await waitFor(() => { expect(captured.body).not.toBeNull(); });
    const credentials = captured.body?.credentials as Array<Record<string, unknown>>;
    expect(credentials.map((credential) => credential.purpose)).toEqual(["review", "query"]);
  });
});

describe("gate: TLS material is never echoed and tls_verified is visible", () => {
  it("shows both TLS states in the list", async () => {
    renderPage(<AdminDatasourcesPage />);
    expect(
      await screen.findByTestId(`ds-tls-verified-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`),
    ).toHaveTextContent("\u5f3a\u5236\u6821\u9a8cTLS");
    expect(
      screen.getByTestId(`ds-tls-plaintext-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`),
    ).toHaveTextContent("\u660e\u6587");
  });

  it("never prefills material on a verified row and re-submits the full block", async () => {
    const captured = captureDatasourcePut();
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    await userEvent.click(screen.getByTestId(`ds-edit-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`));
    expect(screen.getByTestId("ds-tls-enabled")).toBeChecked();
    // Write-only: opening a verified row leaves every textarea empty.
    expect(screen.getByTestId("ds-tls-ca")).toHaveValue("");
    expect(screen.getByTestId("ds-tls-cert")).toHaveValue("");
    expect(screen.getByTestId("ds-tls-key")).toHaveValue("");
    await userEvent.type(screen.getByTestId("ds-tls-ca"), "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n");
    await userEvent.type(screen.getByTestId("ds-tls-cert"), "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----\n");
    await userEvent.type(screen.getByTestId("ds-tls-key"), "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\n");
    await userEvent.click(screen.getByTestId("ds-submit"));
    await waitFor(() => { expect(captured.body).not.toBeNull(); });
    expect(captured.body?.tls).toEqual({
      ca_pem: { value: "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n" },
      client_cert_pem: { value: "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----\n" },
      client_key_pem: { value: "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\n" },
    });
  });

  it("warns before the declared removal path and submits tls null", async () => {
    const captured = captureDatasourcePut();
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`);
    await userEvent.click(screen.getByTestId(`ds-edit-${ADMIN_FIXTURE_DATASOURCE_PG_ID}`));
    await screen.findByTestId("ds-tls-enabled");
    await userEvent.click(screen.getByTestId("ds-tls-enabled"));
    expect(await screen.findByTestId("ds-tls-removal-warning")).toHaveTextContent("\u6062\u590d\u660e\u6587\u8fde\u63a5");
    await userEvent.click(screen.getByTestId("ds-submit"));
    await waitFor(() => { expect(captured.body).not.toBeNull(); });
    expect(captured.body?.tls).toBeNull();
  });

  it("blocks unpaired and empty TLS blocks client-side", async () => {
    renderPage(<AdminDatasourcesPage />);
    await userEvent.click(await screen.findByTestId("ds-create"));
    await userEvent.type(screen.getByTestId("ds-name"), "tls-check");
    await userEvent.type(screen.getByTestId("ds-host"), "10.0.0.90");
    await userEvent.type(screen.getByTestId("credential-username-readonly"), "ro");
    await userEvent.type(screen.getByTestId("credential-password-readonly"), "pw-tls-1");
    // Enabled with no material at all.
    await userEvent.click(screen.getByTestId("ds-tls-enabled"));
    expect(await screen.findByTestId("ds-tls-empty-error")).toBeInTheDocument();
    expect(screen.getByTestId("ds-submit")).toBeDisabled();
    // Cert without key.
    await userEvent.type(
      screen.getByTestId("ds-tls-cert"),
      "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----\n",
    );
    expect(screen.queryByTestId("ds-tls-empty-error")).not.toBeInTheDocument();
    expect(await screen.findByTestId("ds-tls-pair-error")).toBeInTheDocument();
    expect(screen.getByTestId("ds-submit")).toBeDisabled();
    // Completing the pair unlocks the submit.
    await userEvent.type(
      screen.getByTestId("ds-tls-key"),
      "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\n",
    );
    expect(screen.queryByTestId("ds-tls-pair-error")).not.toBeInTheDocument();
    expect(screen.getByTestId("ds-submit")).toBeEnabled();
    // In slow environments (CI/containers + coverage), heavy character-by-character userEvent.type calls can exceed the 5s
    // default timeout (reproduced on CI 2026-11-04), so relax the timeout explicitly.
  }, 20_000);
});

describe("gate: Eval gate wording and inline rendering", () => {
  it("explains the save-time eval gate on the skills dialog", async () => {
    renderPage(<AdminReviewSkillsPage />);
    await userEvent.click(await screen.findByTestId("review-input-create"));
    expect(await screen.findByTestId("skills-eval-gate-hint")).toHaveTextContent(/\u4fdd\u5b58\u65f6\u6267\u884cEval\u95e8\u7981/);
    // No separate Eval entry exists anywhere on the skills surface; the
    // knowledge page keeps its declared evaluations endpoint.
    const buttons = screen.getAllByRole("button");
    for (const button of buttons) {
      expect(button.textContent).not.toMatch(/\u8fd0\u884c ?Eval|\u6267\u884c\u8bc4\u4f30/);
    }
    expect(screen.queryByTestId("knowledge-eval-dialog")).not.toBeInTheDocument();
  });

  // The inline failure rendering (enabled save blocked with the business
  // error inside the dialog) is exercised in admin-pages-popups.test.tsx —
  // it needs the state select popup.
});

describe("gate: §18.76-B model catalog autocomplete", () => {
  it("orders API key above the model field and fills the model from catalog suggestions", async () => {
    renderPage(<AdminProvidersPage />);
    await userEvent.click(await screen.findByTestId("provider-create"));
    await screen.findByTestId("provider-api-key");
    await screen.findByTestId("provider-model");
    // Owner ruling 2: API Key sits above Model (DOM order assertion, as jsdom has no geometry).
    const apiKey = screen.getByTestId("provider-api-key");
    const model = screen.getByTestId("provider-model");
    expect(apiKey.compareDocumentPosition(model) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    await userEvent.type(screen.getByTestId("provider-base-url"), "https://api.deepseek.test");
    await userEvent.type(apiKey, "sk-catalog");
    // Panel opens/closes with input focus: focusing the model input opens the dropdown once catalog items arrive.
    await userEvent.click(screen.getByTestId("provider-model"));
    const panel = await screen.findByTestId("provider-model-options", {}, { timeout: 2500 });
    await userEvent.click(within(panel).getByTestId("provider-model-option-mock-chat-pro"));
    expect(screen.getByTestId("provider-model")).toHaveValue("mock-chat-pro");
    // Live verification fix: selecting an option collapses the panel (no lingering popup).
    expect(screen.queryByTestId("provider-model-options")).not.toBeInTheDocument();
    // Same as above: character-by-character typing + catalog request in slow environments can exceed the default timeout.
  }, 20_000);

  it("keeps manual entry and shows the failure hint when the catalog fetch is rejected", async () => {
    renderPage(<AdminProvidersPage />);
    await userEvent.click(await screen.findByTestId("provider-create"));
    await userEvent.type(screen.getByTestId("provider-base-url"), "https://api.deepseek.test");
    await userEvent.type(screen.getByTestId("provider-api-key"), "mock-invalid-key");
    expect(await screen.findByTestId("provider-model-catalog-failed", {}, { timeout: 2500 })).toBeInTheDocument();
    expect(screen.queryByTestId("provider-model-options")).not.toBeInTheDocument();
    // Manual typing remains unaffected.
    await userEvent.type(screen.getByTestId("provider-model"), "deepseek-chat");
    expect(screen.getByTestId("provider-model")).toHaveValue("deepseek-chat");
  });

  it("reuses the stored credential via provider_id when editing without retyping the key", async () => {
    const bodies: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/admin/ai-providers/models"), async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({
          err_code: 0, message: "ok", data: { items: ["spy-model-a"] }, request_id: "spy",
        });
      }),
    );
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    await screen.findByTestId("provider-model");
    await waitFor(() => {
      expect(bodies.length).toBeGreaterThan(0);
    }, { timeout: 2500 });
    const lastBody = bodies.at(-1);
    expect(lastBody?.provider_id).toBe(ADMIN_FIXTURE_PROVIDER_PRIMARY_ID);
    expect(lastBody?.api_key).toBeUndefined();
  });
});

describe("gate: synchronous connection-test feedback (aligned with RCP-20261017 #019)", () => {
  it("shows the ok line after a synchronous successful connection test", async () => {
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-test-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    expect(await screen.findByTestId(`provider-test-ok-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`)).toBeInTheDocument();
  });

  it("surfaces the upstream reason when the connection test fails", async () => {
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/connection-tests"), () =>
        HttpResponse.json({
          err_code: 2011,
          message: "provider failed",
          data: null,
          request_id: "conn-detail-req",
          retryable: false,
          meta: { detail: 'provider: request rejected: status 401: {"error":{"message":"Authentication Fails"}}' },
        }),
      ),
    );
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-test-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    expect(
      await screen.findByTestId(`provider-test-error-detail-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`),
    ).toHaveTextContent(/Authentication Fails/);
  });
});

describe("gate: datasource connection-test refusal reason passthrough (§18.76 supplement)", () => {
  it("surfaces the upstream reason when the connection test fails", async () => {
    server.use(
      http.post(apiRoute("/admin/datasources/:dsId/connection-tests"), () =>
        HttpResponse.json({
          err_code: 3010,
          message: "\u6570\u636e\u6e90\u8fde\u63a5\u6d4b\u8bd5\u5931\u8d25",
          data: null,
          request_id: "ds-detail-req",
          retryable: false,
          meta: {
            detail: "datasource: connection failed: database: mysql dial failed: dial tcp 127.0.0.1:54334: connect: connection refused",
          },
        }),
      ),
    );
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-test-button-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    expect(
      await screen.findByTestId(`ds-test-error-detail-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`),
    ).toHaveTextContent(/connection refused/);
  });

  it("renders long capability-matrix errors in a full-width sub-row outside the actions cell, with copy and close", async () => {
    const user = userEvent.setup();
    const detail =
      "datasource: capability target unsupported: engine mysql mode mysql detected version 8.4.13 is not in the frozen capability matrix";
    server.use(
      http.post(apiRoute("/admin/datasources/:dsId/connection-tests"), () =>
        HttpResponse.json({
          err_code: 3010,
          message: "datasource unavailable",
          data: null,
          request_id: "ds-cap-req",
          retryable: false,
          meta: { detail },
        }),
      ),
    );
    const writeSpy = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    try {
      renderPage(<AdminDatasourcesPage />);
      const row = await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
      await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
      await user.click(screen.getByTestId(`ds-test-button-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));

      const errorRow = await screen.findByTestId(`ds-test-error-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
      expect(row.contains(errorRow)).toBe(false);
      const cell = errorRow.querySelector("td");
      expect(cell).toHaveAttribute("colspan", "6");
      expect(cell).toHaveClass("whitespace-normal");

      const expectedSummary = i18n.t("errors.DATASOURCE_UNAVAILABLE");
      expect(errorRow).toHaveTextContent(expectedSummary);
      expect(
        screen.getByTestId(`ds-test-error-detail-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`),
      ).toHaveTextContent(detail);

      await user.click(screen.getByTestId(`ds-test-error-copy-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
      expect(writeSpy).toHaveBeenCalledWith(`${expectedSummary}\n${detail}`);

      await user.click(screen.getByTestId(`ds-test-error-close-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
      await waitFor(() => {
        expect(screen.queryByTestId(`ds-test-error-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`)).toBeNull();
      });
    } finally {
      vi.restoreAllMocks();
    }
  });
});

describe("gate: datasource connection-test success feedback", () => {
  it("shows the ok line when the connection test task succeeds", async () => {
    server.use(
      http.post(apiRoute("/admin/datasources/:dsId/connection-tests"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "0198d9cc-0000-7000-8000-000000000001",
            kind: "admin_connection_test",
            state: "succeeded",
            progress: { completed: 1, total: 1, unit: "steps" },
            result_ref: null,
            error: null,
            created_at: "2026-10-19T00:00:00Z",
            updated_at: "2026-10-19T00:00:00Z",
          },
          request_id: "ds-ok-req",
        }),
      ),
    );
    // Polling GET is also pinned to the same succeeded task (the real fixture has no such task).
    server.use(
      http.get(apiRoute("/tasks/:taskId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "0198d9cc-0000-7000-8000-000000000001",
            kind: "admin_connection_test",
            state: "succeeded",
            progress: { completed: 1, total: 1, unit: "steps" },
            result_ref: null,
            error: null,
            created_at: "2026-10-19T00:00:00Z",
            updated_at: "2026-10-19T00:00:00Z",
          },
          request_id: "ds-ok-req",
        }),
      ),
    );
    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-test-button-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));
    expect(
      await screen.findByTestId(`ds-test-ok-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`),
    ).toBeInTheDocument();
  });

  it("re-enables test connection and surfaces error when task polling fails after running", async () => {
    let taskCalls = 0;
    server.use(
      http.post(apiRoute("/admin/datasources/:dsId/connection-tests"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "0198d9cc-0000-7000-8000-000000000099",
            kind: "admin_connection_test",
            state: "running",
            progress: { completed: 0, total: 1, unit: "steps" },
            result_ref: null,
            error: null,
            created_at: "2026-10-19T00:00:00Z",
            updated_at: "2026-10-19T00:00:00Z",
          },
          request_id: "ds-run-req",
        }),
      ),
      http.get(apiRoute("/tasks/:taskId"), ({ params }) => {
        taskCalls += 1;
        if (taskCalls === 1) {
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              id: String(params.taskId),
              kind: "admin_connection_test",
              state: "running",
              progress: { completed: 0, total: 1, unit: "steps" },
              result_ref: null,
              error: null,
              created_at: "2026-10-19T00:00:00Z",
              updated_at: "2026-10-19T00:00:00Z",
            },
            request_id: "ds-poll-ok-1",
          });
        }
        return HttpResponse.json({
          err_code: 1002,
          message: "task not found",
          data: null,
          request_id: "ds-poll-err-2",
          retryable: false,
        });
      }),
    );

    renderPage(<AdminDatasourcesPage />);
    await screen.findByTestId(`ds-row-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    await userEvent.click(screen.getByTestId(`ds-test-button-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`));

    const pollError = await screen.findByTestId(
      `ds-test-poll-error-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`,
      {},
      { timeout: 3000 },
    );
    expect(pollError).toHaveTextContent(/\u8bf7\u6c42\u7684\u5bf9\u8c61\u4e0d\u5b58\u5728/);
    expect(screen.queryByTestId(`ds-test-running-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`)).not.toBeInTheDocument();
    expect(taskCalls).toBe(2);

    await openRowActions(`ds-actions-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    const testButton = screen.getByTestId(`ds-test-button-${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`);
    expect(testButton).not.toBeDisabled();
    expect(testButton).not.toHaveAttribute("aria-disabled", "true");
  });
});

describe("parameter draft helpers (§18.81)", () => {
  it("stringifies every supported parameter type", () => {
    expect(
      stringifyParamDrafts({ n: 1000, b: false, s: "text", list: ["a", "b"] }),
    ).toEqual({ n: "1000", b: "false", s: "text", list: "a, b" });
    expect(stringifyParamDrafts({ b: true })).toEqual({ b: "true" });
  });

  it("restores typed values from drafts by the row's parameter types", () => {
    expect(
      convertParamDrafts({ n: "2000", b: "true", s: "hello", list: "x, y" }, {
        n: 1000, b: false, s: "old", list: ["x"],
      }),
    ).toEqual({ n: 2000, b: true, s: "hello", list: ["x", "y"] });
  });

  it("rejects number drafts that are empty, fractional or out of range", () => {
    const template = { n: 1000 };
    expect(convertParamDrafts({ n: "" }, template)).toBeNull();
    expect(convertParamDrafts({ n: "12.5" }, template)).toBeNull();
    expect(convertParamDrafts({ n: "0" }, template)).toBeNull();
    expect(convertParamDrafts({ n: "1e16" }, template)).toBeNull();
    expect(convertParamDrafts({ n: "1000" }, template)).toEqual({ n: 1000 });
  });

  it("rejects string drafts that are empty or over 128 characters", () => {
    expect(convertParamDrafts({ s: "" }, { s: "old" })).toBeNull();
    expect(convertParamDrafts({ s: "x".repeat(129) }, { s: "old" })).toBeNull();
    expect(convertParamDrafts({ s: "ok" }, { s: "old" })).toEqual({ s: "ok" });
  });

  it("rejects list drafts over 16 items or with items over 64 characters", () => {
    expect(convertParamDrafts({ list: Array.from({ length: 17 }, (_, i) => `i${String(i)}`).join(", ") }, { list: ["x"] })).toBeNull();
    expect(convertParamDrafts({ list: "x".repeat(65) }, { list: ["x"] })).toBeNull();
    expect(convertParamDrafts({ list: "a, b" }, { list: ["x"] })).toEqual({ list: ["a", "b"] });
    // Filter out empty items produced by trailing/consecutive commas.
    expect(convertParamDrafts({ list: "a, , b" }, { list: ["x"] })).toEqual({ list: ["a", "b"] });
  });

  it("maps parameter value types to their i18n label keys", () => {
    expect(paramTypeKey(1)).toBe("paramTypeNumber");
    expect(paramTypeKey("s")).toBe("paramTypeString");
    expect(paramTypeKey(true)).toBe("paramTypeBoolean");
    expect(paramTypeKey(["l"])).toBe("paramTypeStringList");
  });
});

describe("provider generation parameters & reference pricing (S008 FE, 2026-10-31)", () => {
  async function openEditDialog(): Promise<HTMLElement> {
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    return await screen.findByRole("dialog");
  }

  it("backs the generation params from the stored configuration (stored values take precedence)", async () => {
    const dialog = await openEditDialog();
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    // fixture primary: {reasoning_effort:"medium", temperature:0.7}
    expect(within(dialog).getByTestId("provider-gen-reasoning")).toHaveTextContent("medium");
    // jsdom number input returns null under jest-dom toHaveValue — assert the .value property directly.
    expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-max-tokens").value).toBe("");
    expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-temperature").value).toBe("0.7");
  });

  it("keeps temperature 0 as an explicit value through the write payload", async () => {
    const dialog = await openEditDialog();
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    const temperature = within(dialog).getByTestId("provider-gen-temperature");
    await userEvent.type(temperature, "0");
    await userEvent.click(within(dialog).getByTestId("provider-submit"));
    // fixture write handler validates configuration; "0" must arrive as 0
    // (never folded to default) — an invalid clear would 1001 and surface
    // the error alert.
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("separates per-field revert from provider-default clearing", async () => {
    const dialog = await openEditDialog();
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    const reasoning = within(dialog).getByTestId("provider-gen-reasoning");
    await userEvent.click(reasoning);
    await userEvent.click(await screen.findByRole("option", { name: "high" }));
    // dirty → revert appears; restore initial snapshot (medium).
    const revert = within(dialog).getByTestId("provider-gen-revert");
    await userEvent.click(revert);
    expect(within(dialog).getByTestId("provider-gen-reasoning")).toHaveTextContent("medium");
    expect(within(dialog).queryByTestId("provider-gen-revert")).not.toBeInTheDocument();
  });

  it("shows the pricing tab with the standing reference declaration and creates a version", async () => {
    const dialog = await openEditDialog();
    await userEvent.click(within(dialog).getByTestId("provider-tab-pricing"));
    // UI-02: disclaimer is persistent (asserted on rendered text).
    expect(within(dialog).getByText(/\u53c2\u8003\u6210\u672c\u4f30\u7b97/)).toBeInTheDocument();
    expect(within(dialog).getByText(/\u4e0d\u6784\u6210\u8d26\u5355\u6216\u5b9e\u9645\u6263\u6b3e/)).toBeInTheDocument();
    // Open create form: template button lives inside the form (explicit action); then save (fixture closes previous version + adds version row).
    await userEvent.click(within(dialog).getByTestId("rate-new-version"));
    expect(within(dialog).getByTestId("rate-apply-template")).toBeInTheDocument();
    await userEvent.type(within(dialog).getByTestId("rate-from"), "2026-11-01");
    await userEvent.type(within(dialog).getByTestId("rate-input-price-0"), "1.5");
    await userEvent.type(within(dialog).getByTestId("rate-output-price-0"), "6");
    await userEvent.click(within(dialog).getByTestId("rate-submit"));
    await waitFor(() => {
      expect(within(dialog).getByTestId("provider-rate-table")).toBeInTheDocument();
    });
  });

  it("blocks invalid generation params per field and sends no write request", async () => {
    // Count only write requests to provider resources (any POST/PUT/PATCH/DELETE on /admin/ai-providers paths,
    // including nested sub-resources like /rate-versions). The sole exception is the model catalog POST /models:
    // it is a query-style autocomplete, not a write; when the edit dialog opens, its catalog request can race into the listener
    // window in slow environments (reproduced on CI 2026-11-04), and counting it as a "form submission" is a false positive.
    // Record method+url rather than Request objects so failures immediately show which write request slipped in.
    const writes: string[] = [];
    server.events.on("request:start", ({ request }) => {
      const { pathname } = new URL(request.url);
      const isProviderPath = /\/admin\/ai-providers(\/|$)/.test(pathname);
      const isWriteMethod = ["POST", "PUT", "PATCH", "DELETE"].includes(request.method);
      const isModelCatalogLookup =
        request.method === "POST" && /\/admin\/ai-providers\/models\/?$/.test(pathname);
      if (isProviderPath && isWriteMethod && !isModelCatalogLookup) {
        writes.push(`${request.method} ${request.url}`);
      }
    });
    const dialog = await openEditDialog();
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    // out-of-range max_tokens (256..200000 integer) + out-of-range temperature (0..2).
    // NumberInput renders <input type="number">, so non-numeric text is normalised
    // to an empty value by the browser (= omitted field) and never reaches field
    // validation; out-of-range numbers are the invalid input this gate can assert.
    fireEvent.change(within(dialog).getByTestId("provider-gen-max-tokens"), { target: { value: "100" } });
    fireEvent.change(within(dialog).getByTestId("provider-gen-temperature"), { target: { value: "3" } });
    await userEvent.click(within(dialog).getByTestId("provider-submit"));
    // Per-field error text with the contract bounds; no request, dialog stays.
    expect(within(dialog).getByTestId("provider-gen-max-tokens-error").textContent).toContain("256");
    expect(within(dialog).getByTestId("provider-gen-temperature-error").textContent).toContain("0 – 2");
    expect(within(dialog).getByTestId("provider-gen-max-tokens")).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByTestId("provider-gen-temperature")).toHaveAttribute("aria-invalid", "true");
    expect(screen.queryByRole("dialog")).toBeInTheDocument();
    expect(writes).toHaveLength(0);
    server.events.removeAllListeners();
  });

  it("places server meta.fields genparams rejections next to the fields (RCP-20261102)", async () => {
    // Client-valid edit (genparams untouched/empty = omit): only the server
    // rejects, with field-level placement via meta.fields.
    server.use(
      http.put(apiRoute("/admin/ai-providers/:providerId"), () =>
        HttpResponse.json({
          err_code: 1001,
          message: "VALIDATION_FAILED",
          data: null,
          request_id: "req-gen-1",
          retryable: false,
          meta: {
            detail: "max_tokens must be an integer between 256 and 200000",
            fields: {
              max_tokens: ["must be an integer between 256 and 200000"],
              temperature: ["must be a number between 0 and 2"],
            },
          },
        }),
      ),
    );
    const dialog = await openEditDialog();
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    await userEvent.click(within(dialog).getByTestId("provider-submit"));
    // Dialog stays open; each named genparams field shows its server message
    // inline with aria-invalid — the raw server sentence, not a missing i18n key.
    await waitFor(() => {
      expect(within(dialog).getByTestId("provider-gen-max-tokens")).toHaveAttribute("aria-invalid", "true");
    });
    expect(within(dialog).getByTestId("provider-gen-max-tokens-error").textContent)
      .toContain("must be an integer between 256 and 200000");
    expect(within(dialog).getByTestId("provider-gen-temperature")).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByTestId("provider-gen-temperature-error").textContent)
      .toContain("must be a number between 0 and 2");
    expect(screen.queryByRole("dialog")).toBeInTheDocument();
  });

  it("sends temperature 0 as an explicit zero after per-field validation passes", async () => {
    // Wrapped in an object: property writes from the MSW handler keep the
    // declared union type for assertions after waitFor (plain `let` narrows
    // to null and TS loses the write).
    const capture: { put: Record<string, unknown> | null } = { put: null };
    server.use(
      http.put(apiRoute("/admin/ai-providers/:providerId"), async ({ request }) => {
        capture.put = (await request.clone().json().catch(() => null)) as Record<string, unknown> | null;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID, name: "captured", provider_kind: "openai_compatible",
            base_url: "https://captured.example.com", model_name: "glm-4.6", enabled: false,
            selection_priority: 10, version: 2,
            created_at: "2026-08-28T08:00:00Z", updated_at: "2026-08-28T08:00:00Z", configuration: {},
          },
          request_id: "x",
        });
      }),
    );
    const dialog = await openEditDialog();
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    const temperature = within(dialog).getByTestId("provider-gen-temperature");
    // Clear the fixture value (0.7) and type an explicit zero: "0" is a legal
    // temperature and must reach the wire as 0 (never folded to default).
    fireEvent.change(temperature, { target: { value: "" } });
    await userEvent.type(temperature, "0");
    await userEvent.click(within(dialog).getByTestId("provider-submit"));
    await waitFor(() => {
      expect(capture.put).not.toBeNull();
    });
    // M2 write semantics: touching any param sends the explicit whole object;
    // the untouched reasoning_effort rides along as an explicit value.
    expect((capture.put as { configuration?: Record<string, unknown> }).configuration).toEqual({
      reasoning_effort: "medium",
      temperature: 0,
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });
});

describe("provider system_prompt_profile dual-track isolation (FE-F13-XMODEL-PROMPT-PROFILE)", () => {
  it("shows RFC1918 Base URL hint and updates live profile inference as model_name changes", async () => {
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId("provider-create"));
    const dialog = await screen.findByRole("dialog");

    const baseUrlHint = within(dialog).getByTestId("provider-base-url-hint");
    expect(baseUrlHint.textContent).toContain("https://");
    expect(baseUrlHint.textContent).toContain("http://");

    const modelInput = within(dialog).getByTestId("provider-model");
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    expect(within(dialog).getByTestId("provider-gen-prompt-profile")).toHaveTextContent(/\u81ea\u52a8\u8bc6\u522b|Auto-detect/);
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("default");

    await userEvent.click(within(dialog).getByTestId("provider-tab-connection"));
    fireEvent.change(modelInput, { target: { value: "SAN-4B" } });
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    const sanStatus = within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent;
    expect(sanStatus).toContain("san");
    expect(sanStatus).toContain("SAN-4B");
    expect(sanStatus).toContain("Raw-SQL Native");

    await userEvent.click(within(dialog).getByTestId("provider-tab-connection"));
    fireEvent.change(modelInput, { target: { value: "san-v3.1" } });
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("default");
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("san-v3.1");

    await userEvent.click(within(dialog).getByTestId("provider-tab-connection"));
    fireEvent.change(modelInput, { target: { value: "san:v3.1" } });
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("default");
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("san:v3.1");

    await userEvent.click(within(dialog).getByTestId("provider-tab-connection"));
    fireEvent.change(modelInput, { target: { value: "deepseek-chat" } });
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("default");
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("deepseek-chat");
  });

  it("supports selecting, reverting, saving, backfilling, and clearing system_prompt_profile while keeping S008 semantics", async () => {
    const captures: Array<Record<string, unknown>> = [];
    server.events.on("request:start", async ({ request }) => {
      if (request.method === "PUT" && /\/admin\/ai-providers\/[^/]+$/.test(new URL(request.url).pathname)) {
        const parsed = (await request.clone().json().catch(() => null)) as Record<string, unknown> | null;
        if (parsed !== null) captures.push(parsed);
      }
    });

    try {
      renderPage(<AdminProvidersPage />);
      await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
      await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
      await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
      let dialog = await screen.findByRole("dialog");

      await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
      const trigger = within(dialog).getByTestId("provider-gen-prompt-profile");
      expect(trigger).toHaveTextContent(/\u81ea\u52a8\u8bc6\u522b|Auto-detect/);
      expect(within(dialog).queryByTestId("provider-gen-prompt-profile-revert")).not.toBeInTheDocument();

      // Choose default explicitly first, confirm pinned override status, then choose san and revert back to auto.
      await userEvent.click(trigger);
      await userEvent.click(await screen.findByRole("option", { name: /default \u2014/i }));
      expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("default");
      expect(within(dialog).getByTestId("provider-gen-reasoning")).toBeInTheDocument();

      await userEvent.click(within(dialog).getByTestId("provider-gen-prompt-profile"));
      await userEvent.click(await screen.findByRole("option", { name: /san \u2014/i }));
      expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("san");
      expect(within(dialog).queryByTestId("provider-gen-reasoning")).not.toBeInTheDocument();
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-max-tokens").value).toBe("4096");
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("8192");

      const revertBtn = within(dialog).getByTestId("provider-gen-prompt-profile-revert");
      await userEvent.click(revertBtn);
      expect(within(dialog).queryByTestId("provider-gen-prompt-profile-revert")).not.toBeInTheDocument();
      expect(within(dialog).getByTestId("provider-gen-prompt-profile")).toHaveTextContent(/\u81ea\u52a8\u8bc6\u522b|Auto-detect/);
      expect(within(dialog).getByTestId("provider-gen-reasoning")).toBeInTheDocument();
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-max-tokens").value).toBe("");
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("");

      // Re-select san and save; PUT must carry system_prompt_profile: "san" + SAN defaults and omit reasoning_effort.
      await userEvent.click(within(dialog).getByTestId("provider-gen-prompt-profile"));
      await userEvent.click(await screen.findByRole("option", { name: /san \u2014/i }));
      expect(within(dialog).queryByTestId("provider-gen-reasoning")).not.toBeInTheDocument();
      await userEvent.click(within(dialog).getByTestId("provider-submit"));
      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
      expect(captures).toHaveLength(1);
      expect((captures[0] as { configuration?: Record<string, unknown> }).configuration).toEqual({
        system_prompt_profile: "san",
        max_tokens: 4096,
        context_window: 8192,
        temperature: 0.7,
      });

      // Re-open Edit: confirm stored "san" backfills into the selector, keeps reasoning_effort hidden, and restores SAN defaults.
      await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
      await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
      dialog = await screen.findByRole("dialog");
      await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
      expect(within(dialog).getByTestId("provider-gen-prompt-profile")).toHaveTextContent(/san \u2014/);
      expect(within(dialog).getByTestId("provider-gen-prompt-profile-status").textContent).toContain("san");
      expect(within(dialog).queryByTestId("provider-gen-reasoning")).not.toBeInTheDocument();
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-max-tokens").value).toBe("4096");
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("8192");

      // Switch back to Auto-detect and save: configuration omits system_prompt_profile (and cleared reasoning_effort).
      await userEvent.click(within(dialog).getByTestId("provider-gen-prompt-profile"));
      await userEvent.click(await screen.findByRole("option", { name: /\u81ea\u52a8\u8bc6\u522b|Auto-detect/ }));
      expect(within(dialog).getByTestId("provider-gen-reasoning")).toBeInTheDocument();
      await userEvent.click(within(dialog).getByTestId("provider-submit"));
      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
      expect(captures).toHaveLength(2);
      expect((captures[1] as { configuration?: Record<string, unknown> }).configuration).toEqual({
        max_tokens: 4096,
        context_window: 8192,
        temperature: 0.7,
      });
    } finally {
      server.events.removeAllListeners();
    }
  });

  it("places server 1001 meta.fields.system_prompt_profile rejection next to the profile selector", async () => {
    server.use(
      http.put(apiRoute("/admin/ai-providers/:providerId"), () =>
        HttpResponse.json({
          err_code: 1001,
          message: "VALIDATION_FAILED",
          data: null,
          request_id: "req-profile-1",
          retryable: false,
          meta: {
            detail: "system_prompt_profile must be one of default, san",
            fields: {
              system_prompt_profile: ["must be one of default, san"],
            },
          },
        }),
      ),
    );
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    await userEvent.click(within(dialog).getByTestId("provider-submit"));

    await waitFor(() => {
      expect(within(dialog).getByTestId("provider-gen-prompt-profile")).toHaveAttribute("aria-invalid", "true");
    });
    expect(within(dialog).getByTestId("provider-gen-prompt-profile-error").textContent).toContain(
      "must be one of default, san",
    );
    expect(screen.queryByRole("dialog")).toBeInTheDocument();

    // Changing the selector clears the field error and aria-invalid; clicking revert also leaves error cleared.
    await userEvent.click(within(dialog).getByTestId("provider-gen-prompt-profile"));
    await userEvent.click(await screen.findByRole("option", { name: /san \u2014/i }));
    expect(within(dialog).getByTestId("provider-gen-prompt-profile")).toHaveAttribute("aria-invalid", "false");
    expect(within(dialog).queryByTestId("provider-gen-prompt-profile-error")).not.toBeInTheDocument();

    await userEvent.click(within(dialog).getByTestId("provider-gen-prompt-profile-revert"));
    expect(within(dialog).getByTestId("provider-gen-prompt-profile")).toHaveAttribute("aria-invalid", "false");
    expect(within(dialog).queryByTestId("provider-gen-prompt-profile-error")).not.toBeInTheDocument();
  });
});

describe("provider context_window configuration", () => {
  it("validates, persists, backfills, and clears optional context_window with S008 semantics", async () => {
    const writes: Array<Record<string, unknown>> = [];
    server.events.on("request:start", async ({ request }) => {
      if (request.method === "PUT" && request.url.includes("/admin/ai-providers/")) {
        const body = (await request.clone().json().catch(() => null)) as Record<string, unknown> | null;
        if (body !== null) writes.push(body);
      }
    });
    try {
      renderPage(<AdminProvidersPage />);
      await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
      await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
      await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
      let dialog = await screen.findByRole("dialog");
      await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));

      const ctxInput = within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window");
      expect(ctxInput.value).toBe("");

      // Client-side guard blocks non-positive context_window without sending a PUT request.
      fireEvent.change(ctxInput, { target: { value: "0" } });
      await userEvent.click(within(dialog).getByTestId("provider-submit"));
      expect(within(dialog).getByTestId("provider-gen-context-window")).toHaveAttribute("aria-invalid", "true");
      expect(within(dialog).getByTestId("provider-gen-context-window-error")).toBeInTheDocument();
      expect(writes).toHaveLength(0);

      // Enter valid context_window=8192 and verify revert works before saving.
      fireEvent.change(ctxInput, { target: { value: "16384" } });
      await userEvent.click(within(dialog).getByTestId("provider-gen-context-window-revert"));
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("");

      fireEvent.change(ctxInput, { target: { value: "8192" } });
      expect(within(dialog).getByTestId("provider-gen-context-window")).toHaveAttribute("aria-invalid", "false");
      await userEvent.click(within(dialog).getByTestId("provider-submit"));
      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
      expect(writes).toHaveLength(1);
      expect((writes[0] as { configuration?: Record<string, unknown> }).configuration).toEqual({
        reasoning_effort: "medium",
        context_window: 8192,
        temperature: 0.7,
      });

      // Re-open edit dialog: context_window=8192 is backfilled; clearing it omits context_window from PUT.
      await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
      await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
      dialog = await screen.findByRole("dialog");
      await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
      const ctxBackfilled = within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window");
      expect(ctxBackfilled.value).toBe("8192");

      fireEvent.change(ctxBackfilled, { target: { value: "" } });
      await userEvent.click(within(dialog).getByTestId("provider-submit"));
      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
      expect(writes).toHaveLength(2);
      const clearedCfg = (writes[1] as { configuration?: Record<string, unknown> }).configuration ?? {};
      expect("context_window" in clearedCfg).toBe(false);
    } finally {
      server.events.removeAllListeners();
    }
  });

  it("places server 1001 meta.fields.context_window rejection next to the context_window input", async () => {
    server.use(
      http.put(apiRoute("/admin/ai-providers/:providerId"), () =>
        HttpResponse.json({
          err_code: 1001,
          message: "VALIDATION_FAILED",
          data: null,
          request_id: "req-ctx-1",
          retryable: false,
          meta: {
            detail: "context_window must be a positive integer",
            fields: {
              context_window: ["must be a positive integer"],
            },
          },
        }),
      ),
    );
    renderPage(<AdminProvidersPage />);
    await screen.findByTestId(`provider-row-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await openRowActions(`provider-actions-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`);
    await userEvent.click(screen.getByTestId(`provider-edit-${ADMIN_FIXTURE_PROVIDER_PRIMARY_ID}`));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
    await userEvent.click(within(dialog).getByTestId("provider-submit"));

    await waitFor(() => {
      expect(within(dialog).getByTestId("provider-gen-context-window")).toHaveAttribute("aria-invalid", "true");
    });
    expect(within(dialog).getByTestId("provider-gen-context-window-error").textContent).toContain(
      "must be a positive integer",
    );
  });

  it("prefills create defaults and switches to SAN defaults while hiding reasoning_effort", async () => {
    const postPayloads: Array<Record<string, unknown>> = [];
    server.events.on("request:start", async ({ request }) => {
      if (request.method === "POST" && request.url.endsWith("/admin/ai-providers")) {
        const body = (await request.clone().json().catch(() => null)) as Record<string, unknown> | null;
        if (body !== null) postPayloads.push(body);
      }
    });
    try {
      renderPage(<AdminProvidersPage />);
      await userEvent.click(await screen.findByTestId("provider-create"));
      let dialog = await screen.findByRole("dialog");

      // Create mode opens with default-profile presets: max_tokens=4096, context_window=128000, reasoning visible.
      await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
      expect(within(dialog).getByTestId("provider-gen-reasoning")).toBeInTheDocument();
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-max-tokens").value).toBe("4096");
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("128000");

      // Typing a SAN model on the connection tab switches defaults to 4096 / 8192 and hides reasoning_effort.
      await userEvent.click(within(dialog).getByTestId("provider-tab-connection"));
      await userEvent.type(within(dialog).getByTestId("provider-name"), "san-local");
      await userEvent.type(within(dialog).getByTestId("provider-base-url"), "https://api.san.test/v1");
      await userEvent.type(within(dialog).getByTestId("provider-api-key"), "sk-san-1");
      await userEvent.type(within(dialog).getByTestId("provider-model"), "san-4b");

      await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
      expect(within(dialog).queryByTestId("provider-gen-reasoning")).not.toBeInTheDocument();
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-max-tokens").value).toBe("4096");
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("8192");

      // Submitting creates the provider with SAN defaults and without reasoning_effort.
      await userEvent.click(within(dialog).getByTestId("provider-submit"));
      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
      expect(postPayloads).toHaveLength(1);
      expect((postPayloads[0] as { configuration?: Record<string, unknown> }).configuration).toEqual({
        max_tokens: 4096,
        context_window: 8192,
      });

      // Second create flow: user-customized context_window is preserved across default -> san -> default switches.
      await userEvent.click(await screen.findByTestId("provider-create"));
      dialog = await screen.findByRole("dialog");
      await userEvent.click(within(dialog).getByTestId("provider-tab-generation"));
      const ctxInput = within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window");
      fireEvent.change(ctxInput, { target: { value: "32768" } });
      await userEvent.click(within(dialog).getByTestId("provider-gen-prompt-profile"));
      await userEvent.click(await screen.findByRole("option", { name: /san \u2014/i }));
      expect(within(dialog).queryByTestId("provider-gen-reasoning")).not.toBeInTheDocument();
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("32768");

      await userEvent.click(within(dialog).getByTestId("provider-gen-prompt-profile"));
      await userEvent.click(await screen.findByRole("option", { name: /default \u2014/i }));
      expect(within(dialog).getByTestId("provider-gen-reasoning")).toBeInTheDocument();
      expect(within(dialog).getByTestId<HTMLInputElement>("provider-gen-context-window").value).toBe("32768");
    } finally {
      server.events.removeAllListeners();
    }
  });
});
