import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { MarkdownEditor } from "./markdown-editor";

// ProseMirror measures selection geometry on focus/scroll; jsdom has none.
beforeAll(() => {
  const rect = { x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, toJSON: () => ({}) };
  Range.prototype.getBoundingClientRect = () => rect;
  Range.prototype.getClientRects = () => ({ length: 0, item: () => null, [Symbol.iterator]: [][Symbol.iterator] });
});

describe("MarkdownEditor", () => {
  it("renders the Markdown value as formatted content with the template toolbar", async () => {
    render(<MarkdownEditor value={"# Heading\n\n- One\n- Two"} onChange={() => undefined} data-testid="ed" />);
    expect(await screen.findByRole("heading", { level: 1, name: "Heading" })).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    for (const name of ["H1", "H2", "H3"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.queryByRole("button", { name: /underline|\u4e0b\u5212\u7ebf/i })).not.toBeInTheDocument();
  });

  it("pulls an external value in without echoing it back through onChange", async () => {
    const onChange = vi.fn();
    const { rerender } = render(<MarkdownEditor value="" onChange={onChange} />);
    await screen.findByTestId("markdown-toolbar");
    rerender(<MarkdownEditor value="**Hydrated** description" onChange={onChange} />);
    expect(await screen.findByText("Hydrated")).toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("emits Markdown (never HTML) when a toolbar action edits the document", async () => {
    const onChange = vi.fn();
    render(<MarkdownEditor value="Note" onChange={onChange} />);
    await screen.findByTestId("markdown-toolbar");
    fireEvent.click(screen.getByRole("button", { name: "H2" }));
    await waitFor(() => { expect(onChange).toHaveBeenCalled(); });
    const emitted = onChange.mock.calls.at(-1)?.[0] as string;
    expect(emitted).toBe("## Note");
    expect(emitted).not.toContain("<");
  });

  it("rejects links that are not http(s)/mailto", async () => {
    const onChange = vi.fn();
    render(<MarkdownEditor value="Note" onChange={onChange} />);
    await screen.findByTestId("markdown-toolbar");
    fireEvent.click(screen.getByRole("button", { name: /link/i }));
    fireEvent.change(screen.getByTestId("markdown-link-input"), { target: { value: "javascript:alert(1)" } });
    act(() => { fireEvent.click(screen.getByTestId("markdown-link-apply")); });
    expect(onChange).not.toHaveBeenCalled();
  });

  it("shows the length counter and flags the limit", async () => {
    render(<MarkdownEditor value="abcd" onChange={() => undefined} maxLength={4} />);
    const counter = await screen.findByText("4 / 4");
    expect(counter).toHaveClass("text-destructive");
  });
});
