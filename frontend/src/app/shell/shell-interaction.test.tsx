import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import "@/shared/i18n";
import { SidebarProvider } from "@/shared/components/ui/sidebar";
import { ThemeProvider } from "@/app/providers/theme-provider";
import { SessionProvider } from "@/features/auth/session-provider";
import { AppHeader } from "./app-header";
import { ThemeToggle } from "./theme-toggle";
import { BrandLogo } from "./brand-logo";
import { UserMenu } from "./user-menu";
import type { SessionUser } from "@/shared/session/session";

function renderWithProviders(ui: React.ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <MemoryRouter initialEntries={["/workspace"]}>
      <QueryClientProvider client={queryClient}>
        <SessionProvider>
          <ThemeProvider>
            <SidebarProvider>{ui}</SidebarProvider>
          </ThemeProvider>
        </SessionProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  // A test that assigns document.startViewTransition shadows the prototype
  // method with an own property; deleting it restores the original behavior.
  Reflect.deleteProperty(document, "startViewTransition");
  window.localStorage.removeItem("vite-ui-theme");
});

describe("AppHeader", () => {
  it("renders the sidebar toggle, theme toggle and account menu", () => {
    renderWithProviders(<AppHeader />);
    expect(screen.getByRole("button", { name: "\u6298\u53e0\u4fa7\u8fb9\u680f" })).toBeVisible();
    expect(screen.getByRole("button", { name: "\u5207\u6362\u4e3b\u9898" })).toBeVisible();
    expect(screen.getByRole("button", { name: "\u8d26\u6237\u83dc\u5355" })).toBeVisible();
    expect(screen.getByLabelText("\u5168\u5c40\u64cd\u4f5c")).toBeVisible();
  });
});

describe("ThemeToggle", () => {
  it("switches light to dark on click", async () => {
    window.localStorage.setItem("vite-ui-theme", "light");
    renderWithProviders(<ThemeToggle />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "\u5207\u6362\u4e3b\u9898" }));
    expect(document.documentElement).toHaveClass("dark");
  });

  it("switches dark to light on click", async () => {
    window.localStorage.setItem("vite-ui-theme", "dark");
    renderWithProviders(<ThemeToggle />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "\u5207\u6362\u4e3b\u9898" }));
    expect(document.documentElement).toHaveClass("light");
  });

  it("falls back to a direct class flip without startViewTransition", async () => {
    window.localStorage.setItem("vite-ui-theme", "light");
    renderWithProviders(<ThemeToggle />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "\u5207\u6362\u4e3b\u9898" }));
    expect(document.documentElement).toHaveClass("dark");
  });

  it("skips the view-transition sweep when the user prefers reduced motion", async () => {
    window.localStorage.setItem("vite-ui-theme", "light");
    const startViewTransition = vi.fn();
    document.startViewTransition = startViewTransition;
    vi.spyOn(window, "matchMedia").mockImplementation(((query: string) => ({
      matches: query.includes("prefers-reduced-motion"),
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })) as unknown as typeof window.matchMedia);
    renderWithProviders(<ThemeToggle />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "\u5207\u6362\u4e3b\u9898" }));
    expect(document.documentElement).toHaveClass("dark");
    expect(startViewTransition).not.toHaveBeenCalled();
  });
});

describe("BrandLogo", () => {
  it("renders both theme variants and names the product on the link", () => {
    renderWithProviders(<BrandLogo />);
    const link = screen.getByRole("link", { name: "Yearning" });
    expect(link).toHaveAttribute("href", "/workspace");
    expect(link.querySelectorAll("img")).toHaveLength(2);
  });
});

describe("UserMenu role badge", () => {
  it("shows the administrator badge for an admin session", async () => {
    const admin: SessionUser = { displayName: "henry", canAccessAdmin: true, migrationReview: false };
    renderWithProviders(<UserMenu user={admin} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "\u8d26\u6237\u83dc\u5355" }));
    expect(await screen.findByText("\u7ba1\u7406\u5458")).toBeVisible();
  });
});
