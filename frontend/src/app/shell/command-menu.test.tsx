import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "@/shared/i18n";
import { SessionProvider } from "@/features/auth/session-provider";
import { server } from "@/test/msw/server";
import { stubSession } from "@/test/session";
import { CommandMenu } from "./command-menu";

// The menu is a presentation shortcut over NAV_GROUPS, so the assertions are
// about what a given session may see and where Enter lands — never about
// authorization (the backend stays default-deny).
function renderMenu(canAccessAdmin: boolean, onOpenChange = vi.fn()) {
  stubSession({
    admin: canAccessAdmin,
    username: canAccessAdmin ? "admin" : "henry",
    authProvider: "local",
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <MemoryRouter initialEntries={["/workspace"]}>
      <QueryClientProvider client={queryClient}>
        <SessionProvider>
          <Routes>
            <Route
              path="/workspace"
              element={<CommandMenu open onOpenChange={onOpenChange} />}
            />
            <Route path="/changes/mine" element={<div data-testid="mine-route" />} />
          </Routes>
        </SessionProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return onOpenChange;
}

describe("CommandMenu", () => {
  beforeEach(() => {
    server.resetHandlers();
  });

  it("lists the workspace entries for a normal user and hides admin groups", async () => {
    renderMenu(false);
    const list = await screen.findByRole("listbox");
    expect(within(list).getAllByRole("option").length).toBeGreaterThan(3);
    expect(within(list).getAllByRole("option")[0]).toHaveTextContent("\u9996\u9875");
    await waitFor(() => {
      expect(screen.queryByRole("option", { name: /\u6570\u636e\u6e90/ })).not.toBeInTheDocument();
    });
    expect(screen.queryByRole("option", { name: /\u7cfb\u7edf\u8bbe\u7f6e/ })).not.toBeInTheDocument();
  });

  it("shows the admin groups once the server grants admin access", async () => {
    renderMenu(true);
    expect(await screen.findByRole("option", { name: /\u6570\u636e\u6e90/ })).toBeVisible();
    expect(screen.getByRole("option", { name: /\u7cfb\u7edf\u8bbe\u7f6e/ })).toBeVisible();
  });

  it("filters by the translated label and reports the result count", async () => {
    const user = userEvent.setup();
    renderMenu(true);
    await screen.findByRole("listbox");
    await user.type(screen.getByRole("combobox"), "\u5de5\u5355");
    await waitFor(() => {
      expect(screen.getAllByRole("option")).toHaveLength(3);
    });
    expect(screen.getByText("3 \u4e2a\u7ed3\u679c")).toBeVisible();
    expect(screen.queryByRole("option", { name: /\u6570\u636e\u6e90/ })).not.toBeInTheDocument();
  });

  it("navigates to the highlighted entry on Enter and closes", async () => {
    const user = userEvent.setup();
    const onOpenChange = renderMenu(true);
    const mine = await screen.findByRole("option", { name: /\u6211\u7684\u5de5\u5355/ });
    await user.click(mine);
    await waitFor(() => {
      expect(screen.getByTestId("mine-route")).toBeTruthy();
    });
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("moves the highlight with the arrow keys before Enter", async () => {
    const user = userEvent.setup();
    const onOpenChange = renderMenu(true);
    const input = await screen.findByRole("combobox");
    await screen.findByRole("listbox");
    await user.click(input);
    await user.keyboard("{ArrowDown}{Enter}");
    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false);
    });
    // Index 0 is Home and index 1 is My Orders, so ArrowDown{Enter} must land on
    // the orders route rather than stay on the workspace root.
    await waitFor(() => {
      expect(screen.getByTestId("mine-route")).toBeTruthy();
    });
  });

  it("shows the empty state when nothing matches", async () => {
    const user = userEvent.setup();
    renderMenu(false);
    await screen.findByRole("listbox");
    await user.type(screen.getByRole("combobox"), "zzz");
    expect(await screen.findByText("\u6ca1\u6709\u5339\u914d\u7684\u9875\u9762")).toBeVisible();
    expect(screen.queryAllByRole("option")).toHaveLength(0);
  });
});
