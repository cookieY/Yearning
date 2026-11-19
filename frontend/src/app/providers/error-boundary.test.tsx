import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ErrorBoundary } from "@/app/providers/error-boundary";

function Bomb(): never {
  throw new Error("render boom");
}

describe("ErrorBoundary", () => {
  it("renders a safe generic fallback without leaking the error text", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <ErrorBoundary>
        <Bomb />
      </ErrorBoundary>,
    );
    expect(screen.getByRole("alert")).toBeVisible();
    expect(screen.getByText(/\u9875\u9762\u51fa\u73b0\u5f02\u5e38|Something went wrong/)).toBeVisible();
    expect(screen.queryByText("render boom")).toBeNull();
    spy.mockRestore();
  });

  it("renders children when nothing throws", () => {
    render(
      <ErrorBoundary>
        <div>fine</div>
      </ErrorBoundary>,
    );
    expect(screen.getByText("fine")).toBeVisible();
  });
});
