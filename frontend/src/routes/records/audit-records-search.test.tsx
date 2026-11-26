import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "@/features/auth/session-provider";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";
import { setMockAuthBehavior } from "@/shared/mock/auth-scenario-store";
import "@/shared/i18n";
import AuditRecordsPage from "./audit-records-page";

/**
 * Audit records search gates (§18.39): the toolbar narrows the loaded page
 * by time/actor/action/resource/outcome substrings and distinguishes the
 * search-empty state from the no-events state. The contract still declares
 * no filter parameters for the audit list — the filter is client-side over
 * the current page only.
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

import { server } from "@/test/msw/server";
import { grantSession } from "@/test/session";

beforeEach(() => {
  server.resetHandlers();
  resetAdminFixture();
  setMockAuthBehavior("admin");
  // Same session stub the admin pages tests plant: the audit page gates on
  // can_access_admin and the query enable flag.
  grantSession({ id: "0198d9cc-1111-7111-8111-000000000001", requestId: "x" });
});

function renderPage(): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <MemoryRouter initialEntries={["/records"]}>
          <AuditRecordsPage />
        </MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

describe("AuditRecordsPage search (§18.39)", () => {
  it("narrows the loaded events by action and actor substrings", async () => {
    renderPage();
    expect(await screen.findByRole("heading", { level: 1, name: "\u5ba1\u8ba1\u8bb0\u5f55" })).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    // The seeded world logs an admin login and datasource maintenance.
    await waitFor(() => {
      expect(document.querySelectorAll("[data-testid^='records-row-']").length).toBeGreaterThan(0);
    }, { timeout: 4000 });

    fireEvent.change(screen.getByTestId("records-search"), { target: { value: "login" } });
    const loginRows = screen.getAllByTestId(/^records-row-/);
    expect(loginRows.length).toBeGreaterThanOrEqual(1);
    expect(loginRows[0]?.textContent).toContain("login");

    fireEvent.change(screen.getByTestId("records-search"), { target: { value: "admin" } });
    expect(screen.getAllByTestId(/^records-row-/).length).toBeGreaterThanOrEqual(1);

    fireEvent.change(screen.getByTestId("records-search"), { target: { value: "\u4e0d\u5b58\u5728" } });
    expect(await screen.findByText("\u6682\u65e0\u5339\u914d\u7ed3\u679c")).toBeVisible();

    fireEvent.change(screen.getByTestId("records-search"), { target: { value: "" } });
    expect((await screen.findAllByTestId(/^records-row-/)).length).toBeGreaterThan(0);
  });
});
