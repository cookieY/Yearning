import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import "@/shared/i18n";
import { PageHeader, PageLayout, ListToolbar } from "./page-layout";
import { PendingButton } from "./pending-button";
import { LoadingState } from "./status/loading-state";
import { EmptyState } from "./status/empty-state";

describe("shared page presentation", () => {
  it("keeps the page heading, description and primary action accessible", () => {
    render(<PageLayout><PageHeader title="Users" description="Manage user profiles" actions={<button>Create User</button>} /></PageLayout>);
    expect(screen.getByRole("heading", { level: 1, name: "Users" })).toBeVisible();
    expect(screen.getByText("Manage user profiles")).toBeVisible();
    expect(screen.getByRole("button", { name: "Create User" })).toBeEnabled();
  });

  it("announces refresh without removing the active filters or search", () => {
    const { rerender } = render(<ListToolbar busy search={<input aria-label="Search" defaultValue="prod" />}><button>Filter</button></ListToolbar>);
    expect(screen.getByRole("status")).toHaveAccessibleName("\u6b63\u5728\u5237\u65b0");
    expect(screen.getByRole("textbox", { name: "Search" })).toHaveValue("prod");
    rerender(<ListToolbar search={<input aria-label="Search" defaultValue="prod" />}><button>Filter</button></ListToolbar>);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Filter" })).toBeEnabled();
  });

  it("prevents duplicate actions while preserving button name and explicit disabled state", () => {
    const click = vi.fn();
    const { rerender } = render(<PendingButton pending onClick={click}>Save</PendingButton>);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByRole("button")).toHaveAttribute("aria-busy", "true");
    fireEvent.click(screen.getByRole("button"));
    expect(click).not.toHaveBeenCalled();
    rerender(<PendingButton pending={false} disabled onClick={click}>Save</PendingButton>);
    expect(screen.getByRole("button")).toBeDisabled();
    rerender(<PendingButton pending={false} onClick={click}>Save</PendingButton>);
    fireEvent.click(screen.getByRole("button"));
    expect(click).toHaveBeenCalledTimes(1);
  });

  it.each(["page", "panel", "table", "inline"] as const)("announces %s loading without exposing fake data", (variant) => {
    render(<LoadingState variant={variant} columns={4} label="Loading orders" />);
    expect(screen.getByRole("status")).toHaveAccessibleName("Loading orders");
    expect(screen.queryByText("0")).not.toBeInTheDocument();
  });

  it("provides an actionable empty or filtered result with the original test id", () => {
    const clear = vi.fn();
    render(<EmptyState title="No matching results" filtered description="Adjust filters" testId="empty-orders" action={<button onClick={clear}>Clear filters</button>} />);
    expect(screen.getByTestId("empty-orders")).toHaveAttribute("data-slot", "empty");
    expect(screen.getByText("Adjust filters")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(clear).toHaveBeenCalledOnce();
  });
});
