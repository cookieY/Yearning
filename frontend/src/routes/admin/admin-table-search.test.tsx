import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "@/features/auth/session-provider";
import { HttpResponse, delay, http } from "msw";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/msw/server";
import "@/shared/i18n";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";
import { setMockAuthBehavior } from "@/shared/mock/auth-scenario-store";
import AdminDatasourcesPage from "@/routes/admin/admin-datasources-page";
import AdminFlowsPage from "@/routes/admin/admin-flows-page";
import AdminPermissionGroupsPage from "@/routes/admin/admin-permission-groups-page";
import AdminProvidersPage from "@/routes/admin/admin-providers-page";
import AdminReviewKnowledgePage from "@/routes/admin/admin-review-knowledge-page";
import AdminReviewSkillsPage from "@/routes/admin/admin-review-skills-page";
import AdminRuleSetsPage from "@/routes/admin/admin-rule-sets-page";
import AdminUsersPage from "@/routes/admin/admin-users-page";

/**
 * Table search gates (§18.37 → §18.73): every admin table toolbar drives the
 * server-side q parameter (case-insensitive substring over the domain's
 * declared fields) and distinguishes the search-empty state from the page's
 * native empty state. The mock mirrors the ILIKE semantics; assertions await
 * the async round trip (keepPreviousData keeps the previous rows during the
 * refetch).
 */

vi.stubGlobal("matchMedia", vi.fn().mockImplementation((query: string) => ({
  matches: query.includes("prefers-reduced-motion"),
  media: query,
  onchange: null,
  addListener: vi.fn(),
  removeListener: vi.fn(),
  addEventListener: vi.fn(),
  removeEventListener: vi.fn(),
  dispatchEvent: vi.fn(),
})));

class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
vi.stubGlobal("ResizeObserver", ResizeObserverStub);

function grantSession(): void {
  server.use(
    http.get("*/users/me", () =>
      HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: {
          id: "0198d9cc-1111-7111-8111-000000000001",
          username: "henry",
          display_name: "henry",
          email: null,
          is_builtin_admin: true,
          version: 1,
          created_at: "2026-08-28T08:00:00Z",
          updated_at: "2026-08-28T08:00:00Z",
          can_access_admin: true,
        },
        request_id: "x",
      }),
    ),
  );
}

function renderPage(ui: React.ReactElement): QueryClient {
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
  return queryClient;
}

function typeSearch(testId: string, value: string): void {
  fireEvent.change(screen.getByTestId(testId), { target: { value } });
}

beforeAll(() => {
  // Base UI Select pointer-capture stubs (admin-pages precedent).
  Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
  Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
  Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });
});

beforeEach(() => {
  server.resetHandlers();
  resetAdminFixture();
  setMockAuthBehavior("admin");
  grantSession();
});

describe("admin table search (§18.37)", () => {
  it("users: filters by username/display name/email and column chips, with create button in the toolbar", async () => {
    renderPage(<AdminUsersPage />);
    expect(screen.getByRole("heading", { level: 1, name: "\u7528\u6237" })).toBeVisible();
    const createBtn = screen.getByTestId("admin-users-create");
    expect(createBtn.closest('[data-slot="list-toolbar"]')).not.toBeNull();
    expect(createBtn.closest('[data-slot="page-actions"]')).toBeNull();
    const searchInput = screen.getByTestId("admin-users-search");
    expect(searchInput).toHaveAttribute("placeholder", "\u641c\u7d22\u7528\u6237\u540d / \u663e\u793a\u540d / \u90ae\u7bb1\uff08\u7c7b\u578b\u3001\u72b6\u6001\u7b49\u8bf7\u7528\u6761\u4ef6\u7b5b\u9009\uff09");
    fireEvent.focus(searchInput);
    expect(screen.getByRole("option", { name: "\u7528\u6237\u540d" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u663e\u793a\u540d" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u90ae\u7bb1" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u7c7b\u578b" })).toBeVisible();
    // Row testids carry the user id (uuid), not the username — assert on
    // visible text and row counts.
    expect(await screen.findByText("dba-anne")).toBeVisible();
    typeSearch("admin-users-search", "anne");
    await waitFor(() => {
      expect(screen.queryByText("Administrator")).toBeNull();
    });
    expect(screen.getByText("dba-anne")).toBeVisible();
    typeSearch("admin-users-search", "__nonexistent__");
    expect(await screen.findByTestId("admin-users-empty").then((node) => node.textContent)).toContain("\u6682\u65e0\u5339\u914d\u7ed3\u679c");
    typeSearch("admin-users-search", "");
    expect(await screen.findByText("Administrator")).toBeVisible();
  });

  it("flows: filters by flow name and exposes flow column search fields with toolbar create buttons", async () => {
    renderPage(<AdminFlowsPage />);
    expect(screen.getByRole("heading", { level: 1, name: "\u6d41\u7a0b" })).toBeVisible();
    expect(screen.getByTestId("admin-flows-create-change").closest('[data-slot="list-toolbar"]')).not.toBeNull();
    expect(screen.getByTestId("admin-flows-create-query").closest('[data-slot="list-toolbar"]')).not.toBeNull();
    const searchInput = screen.getByTestId("admin-flows-search");
    fireEvent.focus(searchInput);
    expect(screen.getByRole("option", { name: "\u540d\u79f0" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u7c7b\u578b" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u542f\u7528" })).toBeVisible();
    expect(screen.queryByRole("option", { name: "\u7ed3\u6784" })).toBeNull();
    expect((await screen.findAllByTestId(/admin-flow-row-/)).length).toBeGreaterThan(0);
    typeSearch("admin-flows-search", "\u5728\u7ebf\u53ea\u8bfb");
    await waitFor(() => {
      expect(screen.getAllByTestId(/^admin-flow-row-/).length).toBe(1);
    });
    typeSearch("admin-flows-search", "__nonexistent__");
    expect(await screen.findByTestId("admin-flows-empty").then((node) => node.textContent)).toContain("\u6682\u65e0\u5339\u914d\u7ed3\u679c");
  });

  it("permission groups: filters by group name and exposes group column search fields with toolbar create button", async () => {
    renderPage(<AdminPermissionGroupsPage />);
    expect(screen.getByRole("heading", { level: 1, name: "\u6743\u9650\u7ec4" })).toBeVisible();
    expect(screen.getByTestId("admin-groups-create").closest('[data-slot="list-toolbar"]')).not.toBeNull();
    const searchInput = screen.getByTestId("admin-groups-search");
    fireEvent.focus(searchInput);
    expect(screen.getByRole("option", { name: "\u540d\u79f0" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u542f\u7528" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u6210\u5458" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u6388\u6743\u6d41\u7a0b" })).toBeVisible();
    expect((await screen.findAllByTestId(/admin-group-row-/)).length).toBeGreaterThan(0);
    typeSearch("admin-groups-search", "\u53d8\u66f4\u53d1\u5e03\u7ec4");
    expect(screen.getAllByTestId(/^admin-group-row-/).length).toBe(1);
    typeSearch("admin-groups-search", "__nonexistent__");
    expect(await screen.findByTestId("admin-groups-empty").then((node) => node.textContent)).toContain("\u6682\u65e0\u5339\u914d\u7ed3\u679c");
  });

  it("permission groups: the effective-permission preview picks a user from a Select and can cancel the selection", async () => {
    renderPage(<AdminPermissionGroupsPage />);
    const trigger = await screen.findByTestId("admin-groups-preview-input");
    expect(trigger).toHaveAttribute("data-slot", "select-trigger");
    expect(trigger.tagName).not.toBe("INPUT");
    expect(trigger).toHaveTextContent("\u9009\u62e9\u7528\u6237\u67e5\u770b\u6709\u6548\u6743\u9650");
    expect(screen.queryByTestId("admin-groups-preview-result")).toBeNull();
    expect(screen.queryByTestId("admin-groups-preview-clear")).toBeNull();
    await userEvent.click(trigger);
    await userEvent.click(await screen.findByRole("option", { name: "dba-anne" }));
    const result = await screen.findByTestId("admin-groups-preview-result");
    expect(result).toHaveTextContent("dba-anne");
    expect(trigger).toHaveTextContent("dba-anne");
    const clearBtn = screen.getByTestId("admin-groups-preview-clear");
    expect(clearBtn).toHaveTextContent("\u53d6\u6d88");
    await userEvent.click(clearBtn);
    expect(screen.queryByTestId("admin-groups-preview-clear")).toBeNull();
    expect(screen.queryByTestId("admin-groups-preview-result")).toBeNull();
    expect(trigger).toHaveTextContent("\u9009\u62e9\u7528\u6237\u67e5\u770b\u6709\u6548\u6743\u9650");
  });

  it("typed field conditions ride the server filter params instead of client-only text (RCP-20261105)", async () => {
    // users kind -> kind=local_user/builtin_admin: narrowing happens server-side (the mock only
    // fuzzy-matches text columns for q per contract, so these two assertions can only pass via parameter filtering).
    renderPage(<AdminUsersPage />);
    const trigger = await screen.findByTestId("admin-users-search");
    expect(await screen.findByText("Administrator")).toBeVisible();
    await userEvent.click(trigger);
    await userEvent.click(await screen.findByRole("option", { name: "\u7c7b\u578b" }));
    await userEvent.click(await screen.findByRole("option", { name: "\u672c\u5730\u7528\u6237" }));
    await waitFor(() => {
      expect(screen.queryByText("Administrator")).toBeNull();
    });
    expect(screen.getByText("dba-anne")).toBeVisible();
    // Removing the condition brings back the builtin admin row.
    const chip = screen.getByRole("button", { name: /\u79fb\u9664\u6761\u4ef6 \u7c7b\u578b: \u672c\u5730\u7528\u6237/ });
    await userEvent.click(chip);
    expect(await screen.findByText("Administrator")).toBeVisible();

    // Permission group member -> member_user_id: likewise narrowed server-side.
    renderPage(<AdminPermissionGroupsPage />);
    const groupTrigger = await screen.findByTestId("admin-groups-search");
    await userEvent.click(groupTrigger);
    await userEvent.click(await screen.findByRole("option", { name: "\u6210\u5458" }));
    await userEvent.click(await screen.findByRole("option", { name: "dba-anne" }));
    await waitFor(() => {
      expect(screen.getAllByTestId(/^admin-group-row-/).length).toBe(1);
    });
    expect(screen.getByText("\u53d8\u66f4\u53d1\u5e03\u7ec4")).toBeVisible();
  });

  it("datasources: filters by name and host and exposes datasource column search fields with toolbar create button", async () => {
    renderPage(<AdminDatasourcesPage />);
    expect(screen.getByRole("heading", { level: 1, name: "\u6570\u636e\u6e90\u7ba1\u7406" })).toBeVisible();
    expect(screen.getByTestId("ds-create").closest('[data-slot="list-toolbar"]')).not.toBeNull();
    const searchInput = screen.getByTestId("admin-datasources-search");
    fireEvent.focus(searchInput);
    expect(screen.getByRole("option", { name: "\u540d\u79f0" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u5f15\u64ce" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u7aef\u70b9" })).toBeVisible();
    expect(screen.getByRole("option", { name: "\u51ed\u636e\u72b6\u6001" })).toBeVisible();
    expect(screen.queryByRole("option", { name: "\u6d41\u7a0b\u5f15\u7528" })).toBeNull();
    expect((await screen.findAllByTestId(/ds-row-/)).length).toBeGreaterThan(0);
    typeSearch("admin-datasources-search", "\u8ba2\u5355\u4e3b\u5e93");
    await waitFor(() => {
      expect(screen.getAllByTestId(/^ds-row-/).length).toBe(1);
    });
    // Host search hits a different row set than name search.
    typeSearch("admin-datasources-search", "10.0.0.11");
    await waitFor(() => {
      expect(screen.getByText("prod-order-mysql")).toBeVisible();
    });
    typeSearch("admin-datasources-search", "__nonexistent__");
    expect(await screen.findByTestId("admin-datasources-search-empty")).toBeVisible();
  });

  it("providers: filters by name, base url and model", async () => {
    renderPage(<AdminProvidersPage />);
    expect(screen.getByRole("heading", { level: 1, name: "Provider \u7ba1\u7406" })).toBeVisible();
    expect(screen.queryByRole("heading", { level: 2, name: "Provider \u5217\u8868" })).toBeNull();
    expect(screen.queryByText("\u67e5\u770b\u3001\u6d4b\u8bd5\u3001\u7f16\u8f91\u6216\u5220\u9664 Provider \u914d\u7f6e\u3002")).toBeNull();
    const providerSearch = screen.getByTestId("admin-providers-search");
    expect(providerSearch.closest('[data-slot="list-toolbar"]')?.firstElementChild).toContainElement(providerSearch);
    expect((await screen.findAllByTestId(/provider-row-/)).length).toBeGreaterThan(0);
    typeSearch("admin-providers-search", "backup");
    await waitFor(() => {
      expect(screen.getAllByTestId(/^provider-row-/).length).toBe(1);
    });
    typeSearch("admin-providers-search", "glm");
    await waitFor(() => {
      expect(screen.getAllByTestId(/^provider-row-/).length).toBe(1);
    });
    typeSearch("admin-providers-search", "__nonexistent__");
    expect(await screen.findByTestId("admin-providers-search-empty")).toBeVisible();
  });

  it("rule sets: filters by rule set name", async () => {
    renderPage(<AdminRuleSetsPage />);
    expect(screen.getByRole("heading", { level: 1, name: "\u89c4\u5219\u96c6" })).toBeVisible();
    const ruleSetSearch = screen.getByTestId("admin-rule-sets-search");
    expect(ruleSetSearch.closest('[data-slot="list-toolbar"]')?.firstElementChild).toContainElement(ruleSetSearch);
    expect((await screen.findAllByTestId(/rule-set-row-/)).length).toBeGreaterThan(0);
    typeSearch("admin-rule-sets-search", "change-review");
    expect(screen.getAllByTestId(/^rule-set-row-/).length).toBe(1);
    typeSearch("admin-rule-sets-search", "__nonexistent__");
    expect(await screen.findByTestId("admin-rule-sets-search-empty")).toBeVisible();
  });

  it("review skills: filters the prompt tool table", async () => {
    renderPage(<AdminReviewSkillsPage />);
    expect(screen.getByRole("heading", { level: 1, name: "\u5ba1\u6838\u6280\u80fd\uff08Prompt Tool\uff09" })).toBeVisible();
    const skillSearch = screen.getByTestId("admin-skills-search");
    expect(skillSearch.closest('[data-slot="list-toolbar"]')?.firstElementChild).toContainElement(skillSearch);
    expect((await screen.findAllByTestId(/review-input-row-/)).length).toBeGreaterThan(0);
    typeSearch("admin-skills-search", "dml");
    await waitFor(() => {
      expect(screen.getAllByTestId(/^review-input-row-/).length).toBe(1);
    });
    typeSearch("admin-skills-search", "__nonexistent__");
    expect(await screen.findByTestId("admin-skills-search-empty")).toBeVisible();
  });

  it("knowledge: exposes one page title above the existing master-detail layout", async () => {
    renderPage(<AdminReviewKnowledgePage />);
    expect(screen.getByRole("heading", { level: 1, name: "\u5185\u90e8\u7ecf\u9a8c\uff08\u77e5\u8bc6\u5e93\uff09" })).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect((await screen.findAllByTestId(/review-input-row-/))[0]).toBeInTheDocument();
  });

  it("keeps the page controls and table-shaped loading state during first load", () => {
    server.use(
      http.get("*/admin/datasources", async () => {
        await delay("infinite");
        return HttpResponse.json(null);
      }),
    );
    renderPage(<AdminDatasourcesPage />);

    expect(screen.getByRole("heading", { level: 1, name: "\u6570\u636e\u6e90\u7ba1\u7406" })).toBeVisible();
    expect(screen.getByTestId("admin-datasources-search")).toBeVisible();
    expect(screen.getByRole("status", { name: "\u52a0\u8f7d\u4e2d" })).toHaveAttribute(
      "data-loading-variant",
      "table",
    );
  });

  it("keeps existing rows while a background refresh is announced in the toolbar", async () => {
    const queryClient = renderPage(<AdminUsersPage />);
    expect(await screen.findByText("dba-anne")).toBeVisible();
    expect(screen.getByText("Administrator")).toBeVisible();

    server.use(
      http.get("*/admin/users", async () => {
        await delay("infinite");
        return HttpResponse.json(null);
      }),
    );
    void queryClient.refetchQueries({ queryKey: ["admin", "users"] });

    expect(await screen.findByRole("status", { name: "\u6b63\u5728\u5237\u65b0" })).toBeVisible();
    expect(screen.getByText("dba-anne")).toBeVisible();
    expect(screen.getByText("Administrator")).toBeVisible();
    expect(screen.queryByRole("status", { name: "\u52a0\u8f7d\u4e2d" })).not.toBeInTheDocument();
  });
});
