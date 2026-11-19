import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it } from "vitest";
import "@/shared/i18n";
import { SidebarProvider } from "@/shared/components/ui/sidebar";
import { ThemeProvider } from "@/app/providers/theme-provider";
import { SessionProvider } from "@/features/auth/session-provider";
import { AppHeader } from "./app-header";
import type { ReleaseSnapshot } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * The release bar (owner request 2026-10-23) is the shell's version surface:
 * every snapshot shape — pending, up-to-date, update available, unknown tag —
 * must render an honest line without touching the workspace pages.
 */

function snapshot(overrides: Partial<ReleaseSnapshot> = {}): ReleaseSnapshot {
  return {
    tag_name: "v4.1.0",
    name: "v4.1.0",
    html_url: "https://example.test/releases/v4.1.0",
    body: "notes",
    published_at: "2026-10-01T00:00:00Z",
    fetched_at: "2026-11-04T00:00:00Z",
    current_version: "v4.1.0",
    ...overrides,
  };
}

function renderHeader(release: ReleaseSnapshot | null | undefined) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  if (release !== undefined) {
    queryClient.setQueryData(["releases", "latest"], release);
  }
  return render(
    <MemoryRouter initialEntries={["/workspace"]}>
      <QueryClientProvider client={queryClient}>
        <SessionProvider>
          <ThemeProvider>
            <SidebarProvider>
              <AppHeader />
            </SidebarProvider>
          </ThemeProvider>
        </SessionProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

afterEach(() => {
  window.localStorage.removeItem("vite-ui-theme");
});

describe("AppHeader release bar", () => {
  it("shows a placeholder while the snapshot has not loaded", () => {
    renderHeader(undefined);
    expect(screen.getByTestId("header-release").textContent).toContain("—");
    expect(screen.queryByRole("link", { name: /\u66f4\u65b0\u65e5\u5fd7/ })).not.toBeInTheDocument();
  });

  it("reports up to date when the running version equals the latest tag", () => {
    renderHeader(snapshot());
    const bar = screen.getByTestId("header-release");
    expect(bar.textContent).toContain("v4.1.0");
    expect(bar.textContent).toContain("\u5df2\u662f\u6700\u65b0");
    expect(screen.getByRole("link", { name: /\u66f4\u65b0\u65e5\u5fd7/ })).toHaveAttribute(
      "href",
      "https://example.test/releases/v4.1.0",
    );
  });

  it("flags an available update with the latest tag", () => {
    renderHeader(snapshot({ tag_name: "v4.2.0" }));
    const bar = screen.getByTestId("header-release");
    expect(bar.textContent).toContain("v4.2.0");
    expect(bar.textContent).not.toContain("\u5df2\u662f\u6700\u65b0");
  });

  it("falls back to an empty latest tag when the snapshot omits it", () => {
    // tag_name is typed non-null, but the guard still renders when the field
    // is absent on the wire — the bar must not crash or show "null".
    renderHeader(snapshot({ tag_name: "" }));
    expect(screen.getByTestId("header-release").textContent).not.toContain("null");
  });

  it("shows a pending-version placeholder inside an otherwise loaded snapshot", () => {
    renderHeader(snapshot({ current_version: null }));
    const bar = screen.getByTestId("header-release");
    expect(bar.textContent).toContain("—");
    expect(bar.textContent).toContain("\u5df2\u662f\u6700\u65b0");
    // The snapshot is present, so the notes link still renders.
    expect(screen.getByRole("link", { name: /\u66f4\u65b0\u65e5\u5fd7/ })).toBeInTheDocument();
  });
});

describe("AppHeader command shortcut", () => {
  it("ignores keys that are not ⌘K/ctrl+K and plain k without modifiers", () => {
    renderHeader(undefined);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", metaKey: true }));
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "k" }));
    expect(screen.queryByRole("dialog", { name: /\u547d\u4ee4/ })).not.toBeInTheDocument();
  });
});
