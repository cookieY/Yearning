import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import "@/shared/i18n";
import type { SqlEditorPanelProps } from "./sql-editor-panel";

const editorGate = vi.hoisted(() => {
  let resolve!: (mod: {
    SqlEditorPanel: (props: SqlEditorPanelProps) => React.ReactElement;
  }) => void;
  const promise = new Promise<{
    SqlEditorPanel: (props: SqlEditorPanelProps) => React.ReactElement;
  }>((res) => {
    resolve = res;
  });
  return { promise, resolve };
});

vi.mock("@/features/review/sql-editor-panel", () => editorGate.promise);

import { LazySqlEditorPanel } from "./lazy-sql-editor-panel";

describe("LazySqlEditorPanel", () => {
  it("renders a height-matched local skeleton while the editor chunk loads without blocking siblings", async () => {
    render(
      <div>
        <div data-testid="sibling-form">flow-and-metadata</div>
        <LazySqlEditorPanel
          value="SELECT 1;"
          onChange={vi.fn()}
          className="h-[260px] min-h-[200px] rounded-md border-0"
          data-testid="sql-editor"
        />
      </div>,
    );

    expect(screen.getByTestId("sibling-form")).toBeVisible();
    const skeleton = screen.getByRole("status");
    expect(skeleton).toHaveAttribute("aria-busy", "true");
    expect(skeleton).toHaveAttribute("data-slot", "sql-editor-skeleton");
    expect(skeleton).toHaveClass("h-[260px]", "min-h-[200px]");
    expect(skeleton.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThanOrEqual(5);
    expect(screen.queryByTestId("sql-editor")).not.toBeInTheDocument();

    await act(async () => {
      editorGate.resolve({
        SqlEditorPanel: ({ value, "data-testid": testId }) => (
          <div data-testid={testId}>{value}</div>
        ),
      });
      await editorGate.promise;
    });

    expect(await screen.findByTestId("sql-editor")).toHaveTextContent("SELECT 1;");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
