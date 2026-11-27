import { lazy, type ComponentType } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, Link, Outlet, RouterProvider } from "react-router";
import { describe, expect, it } from "vitest";
import "@/shared/i18n";
import { PageLoadingState } from "@/shared/components/status/page-loading-state";
import { Loadable } from "./loadable";

function createDeferredComponent() {
  let resolve!: (mod: { default: ComponentType }) => void;
  const promise = new Promise<{ default: ComponentType }>((res) => {
    resolve = res;
  });
  const LazyComponent = lazy(() => promise);
  return { LazyComponent, resolve };
}

describe("Loadable", () => {
  it("shows PageLoadingState immediately when switching between sibling lazy routes under a shared Outlet", async () => {
    const first = createDeferredComponent();
    const second = createDeferredComponent();
    first.resolve({ default: () => <div>sibling-first-page</div> });

    const router = createMemoryRouter(
      [
        {
          element: (
            <div>
              <Link to="/admin/second">go-second</Link>
              <Outlet />
            </div>
          ),
          children: [
            { path: "/admin/first", element: Loadable(first.LazyComponent) },
            { path: "/admin/second", element: Loadable(second.LazyComponent) },
          ],
        },
      ],
      { initialEntries: ["/admin/first"] },
    );

    render(<RouterProvider router={router} useTransitions={false} />);
    expect(await screen.findByText("sibling-first-page")).toBeVisible();

    const user = userEvent.setup();
    await user.click(screen.getByRole("link", { name: "go-second" }));

    expect(screen.queryByText("sibling-first-page")).not.toBeInTheDocument();
    const loading = screen.getByRole("status");
    expect(loading).toHaveAttribute("data-loading-variant", "page");
    expect(loading.querySelector('[data-slot="skeleton"]')).not.toBeNull();

    second.resolve({ default: () => <div>sibling-second-page</div> });
    expect(await screen.findByText("sibling-second-page")).toBeVisible();
  });

  it("shows PageLoadingState immediately when navigating between top-level lazy routes", async () => {
    const home = createDeferredComponent();
    const records = createDeferredComponent();
    home.resolve({ default: () => <div>top-home-page</div> });

    const router = createMemoryRouter(
      [
        {
          element: (
            <div>
              <nav>
                <Link to="/records">go-records</Link>
              </nav>
              <main>
                <Outlet />
              </main>
            </div>
          ),
          children: [
            { path: "/workspace", element: Loadable(home.LazyComponent) },
            { path: "/records", element: Loadable(records.LazyComponent) },
          ],
        },
      ],
      { initialEntries: ["/workspace"] },
    );

    render(<RouterProvider router={router} useTransitions={false} />);
    expect(await screen.findByText("top-home-page")).toBeVisible();

    const user = userEvent.setup();
    await user.click(screen.getByRole("link", { name: "go-records" }));

    expect(screen.queryByText("top-home-page")).not.toBeInTheDocument();
    const loading = screen.getByRole("status");
    expect(loading).toHaveAttribute("data-loading-variant", "page");
    expect(loading.querySelector('[data-slot="skeleton"]')).not.toBeNull();

    records.resolve({ default: () => <div>top-records-page</div> });
    expect(await screen.findByText("top-records-page")).toBeVisible();
  });
});

describe("PageLoadingState", () => {
  it("renders accessible status semantics and structured skeleton placeholders", () => {
    render(<PageLoadingState />);
    const loading = screen.getByRole("status");
    expect(loading).toHaveAttribute("aria-busy", "true");
    expect(loading).toHaveAttribute("data-loading-variant", "page");
    expect(loading.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThanOrEqual(5);
  });
});
