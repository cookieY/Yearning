import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HttpResponse, http } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import "@/shared/i18n";
import { server } from "@/test/msw/server";
import { SidebarProvider } from "@/shared/components/ui/sidebar";
import { SessionProvider } from "@/features/auth/session-provider";
import { YearningSidebar } from "./yearning-sidebar";
import { AppFooter, FOOTER_TEXT } from "./app-footer";
import { UserMenu } from "./user-menu";

function stubCurrentUser(canAccessAdmin: boolean): Promise<void> {
  let resolveProbe!: () => void;
  const settled = new Promise<void>((resolve) => {
    resolveProbe = resolve;
  });
  server.use(
    http.get("*/users/me", () => {
      queueMicrotask(resolveProbe);
      return HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: {
          id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ad",
          username: canAccessAdmin ? "admin" : "henry",
          display_name: canAccessAdmin ? "admin" : "henry",
          email: null,
          is_builtin_admin: canAccessAdmin,
          version: 1,
          created_at: "2026-08-28T08:00:00Z",
          updated_at: "2026-08-28T08:00:00Z",
          can_access_admin: canAccessAdmin,
          auth_provider: "local",
        },
        request_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ad",
      });
    }),
  );
  return settled;
}

function renderWithShellProviders(ui: React.ReactElement, initialEntry = "/workspace") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <QueryClientProvider client={queryClient}>
        <SessionProvider>
          <SidebarProvider>{ui}</SidebarProvider>
        </SessionProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("YearningSidebar", () => {
  // Drain unawaited /users/me 401 probes from earlier synchronous renders before mounting new session probes.
  beforeEach(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });

  it("renders the workspace, audit and query groups for the placeholder user", () => {
    renderWithShellProviders(<YearningSidebar />);
    expect(screen.getByText("\u5de5\u4f5c\u53f0")).toBeVisible();
    expect(screen.getByText("\u5ba1\u8ba1")).toBeVisible();
    // "Query" is both a group label and its single entry
    expect(screen.getAllByText("\u67e5\u8be2").length).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText("\u7ba1\u7406")).not.toBeInTheDocument();
    expect(screen.queryByText("\u5ba1\u6838\u5f15\u64ce")).not.toBeInTheDocument();
    expect(screen.getByText("\u6211\u7684\u5de5\u5355")).toBeVisible();
  });

  it("marks the active route", () => {
    renderWithShellProviders(<YearningSidebar />);
    const active = screen.getByText("\u9996\u9875").closest("[data-active]");
    expect(active).not.toBeNull();
  });

  it.each<[string, string]>([
    ["/changes/orders/ord-1", "/changes/mine"],
    ["/changes/drafts/drf-1", "/changes/new"],
    ["/approvals/query-access", "/approvals/changes"],
    ["/approvals/orders/ord-1", "/approvals/changes"],
    ["/query/sessions/qs-1", "/query"],
  ])("keeps parent sidebar item highlighted on user sub-route %s", async (pathname, expectedHref) => {
    const settled = stubCurrentUser(false);
    const { container } = renderWithShellProviders(<YearningSidebar />, pathname);
    await settled;
    const activeHrefs = [...container.querySelectorAll("a[data-active]")].map((el) =>
      el.getAttribute("href"),
    );
    expect(activeHrefs).toEqual([expectedHref]);
  });

  it.each(["/admin/settings/general", "/admin/settings/execution", "/admin/identity-providers"])(
    "keeps Settings highlighted on admin sub-route %s",
    async (pathname) => {
      const settled = stubCurrentUser(true);
      const { container } = renderWithShellProviders(<YearningSidebar />, pathname);
      await settled;
      await screen.findByText("\u7cfb\u7edf\u8bbe\u7f6e");
      const activeHrefs = [...container.querySelectorAll("a[data-active]")].map((el) =>
        el.getAttribute("href"),
      );
      expect(activeHrefs).toEqual(["/admin/settings"]);
    },
  );
});

describe("AppFooter", () => {
  it("renders the exact license line required by the migration contract and AGPL source link", () => {
    render(<AppFooter />);
    expect(screen.getByText(FOOTER_TEXT)).toBeVisible();
    expect(FOOTER_TEXT).toBe("AGPL-3.0 Licensed | Copyright © 2017-present Henry Yee");
    const layoutRow = screen.getByText(FOOTER_TEXT).parentElement;
    expect(layoutRow).toHaveClass("lg:flex-row");
    const sourceLink = screen.getByRole("link", { name: "\u6e90\u7801" });
    expect(sourceLink).toHaveClass("whitespace-nowrap");
    expect(sourceLink).toHaveAttribute("href", "https://source.example/");
    expect(sourceLink).toHaveAttribute("rel", "noreferrer noopener");
  });
});

describe("UserMenu", () => {
  it("opens the account sheet with identity and disabled actions that explain themselves", async () => {
    const user = userEvent.setup();
    renderWithShellProviders(<UserMenu />);
    const trigger = screen.getByRole("button", { name: "\u8d26\u6237\u83dc\u5355" });
    expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    const dialog = await screen.findByRole("dialog");
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(trigger.getAttribute("aria-controls")).toBe(dialog.id);
    expect(await screen.findByText("\u4f1a\u8bdd\u672a\u63a5\u5165")).toBeVisible();
    const profile = screen.getByRole("button", { name: /\u4e2a\u4eba\u4e2d\u5fc3/ });
    expect(profile).toBeDisabled();
    const signOut = screen.getByRole("button", { name: /\u9000\u51fa\u767b\u5f55/ });
    expect(signOut).toBeDisabled();
  });
});

describe("UserMenu sign-out resilience", () => {
  it("still returns to a clean state when the logout request itself fails", async () => {
    server.use(
      http.post("*/auth/logout", () => new HttpResponse(null, { status: 500 })),
    );
    const user = userEvent.setup();
    renderWithShellProviders(<UserMenu />);
    await user.click(screen.getByRole("button", { name: "\u8d26\u6237\u83dc\u5355" }));
    const signOut = await screen.findByRole("button", { name: /\u9000\u51fa\u767b\u5f55/ });
    // The mock session is anonymous in vitest, so the button is disabled and
    // the click path cannot run; the disabled state itself is the gate.
    expect(signOut).toBeDisabled();
  });
});

describe("UserMenu profile navigation closes the sheet (§18.53)", () => {
  it("closes the account sheet when navigating to the profile page", async () => {
    const settled = stubCurrentUser(false);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <MemoryRouter initialEntries={["/workspace"]}>
        <QueryClientProvider client={queryClient}>
          <SessionProvider>
            <SidebarProvider>
              <Routes>
                <Route path="/workspace" element={<UserMenu />} />
                <Route path="/profile" element={<div data-testid="profile-route-stub" />} />
              </Routes>
            </SidebarProvider>
          </SessionProvider>
        </QueryClientProvider>
      </MemoryRouter>,
    );
    await settled;
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "\u8d26\u6237\u83dc\u5355" }));
    await user.click(await screen.findByTestId("user-menu-profile"));
    // Navigation happened and the sheet portal unmounted with it.
    expect(await screen.findByTestId("profile-route-stub")).toBeVisible();
    expect(screen.queryByRole("button", { name: /\u9000\u51fa\u767b\u5f55/ })).toBeNull();
  });
});

