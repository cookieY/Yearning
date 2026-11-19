import { describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@/shared/i18n";
import { GlobalSearchBar, filterBySearchTerms, type SearchAccessor, type SearchTerm } from "@/shared/components/global-search-bar";

interface Row {
  number: string;
  title: string;
  state: string;
}

const accessors: SearchAccessor<Row>[] = [
  { key: "number", label: "Order No.", value: (row) => row.number },
  { key: "title", label: "Title", value: (row) => row.title },
  {
    key: "state",
    label: "State",
    value: (row) => `${row.state} ${row.state === "running" ? "Running" : "Submitted"}`,
    options: [{ label: "Running" }, { label: "Submitted" }],
  },
  // Owner feedback 2026-11-05: the bar also owns date filters — the value
  // comes from the inline calendar, never from typed text.
  { key: "from", label: "Start Date", value: () => "", input: "date" },
];

const rows: Row[] = [
  { number: "YR-1", title: "Archive columns", state: "running" },
  { number: "YR-2", title: "Adjust index", state: "submitted" },
];

describe("filterBySearchTerms", () => {
  it("matches free text against any field, case-insensitively", () => {
    expect(filterBySearchTerms(rows, [{ field: null, text: "yr-1" }], accessors)).toEqual([rows[0]]);
    expect(filterBySearchTerms(rows, [{ field: null, text: "Adjust" }], accessors)).toEqual([rows[1]]);
  });

  it("scopes a term to one field and ANDs multiple terms", () => {
    expect(filterBySearchTerms(rows, [{ field: "title", text: "YR" }], accessors)).toEqual([]);
    expect(
      filterBySearchTerms(rows, [{ field: "state", text: "Running" }, { field: "number", text: "yr" }], accessors),
    ).toEqual([rows[0]]);
  });

  it("matches the localized label of an enum field", () => {
    expect(filterBySearchTerms(rows, [{ field: "state", text: "Submitted" }], accessors)).toEqual([rows[1]]);
  });

  it("returns every row for empty terms", () => {
    expect(filterBySearchTerms(rows, [{ field: null, text: "  " }], accessors)).toEqual(rows);
  });
});

function renderBar(overrides: Partial<Parameters<typeof GlobalSearchBar>[0]> = {}) {
  const onTermsChange = vi.fn();
  const onLiveChange = vi.fn();
  const base = render(
    <GlobalSearchBar
      fields={accessors}
      terms={[]}
      onTermsChange={onTermsChange}
      onLiveChange={onLiveChange}
      testId="gsb"
      {...overrides}
    />,
  );
  return { ...base, onTermsChange, onLiveChange };
}

/** Terms flow back into the bar the way a real page feeds them. */
function StatefulBar({
  initialTerms,
  resetKey,
  onTermsChange,
  onLiveChange,
}: {
  initialTerms: SearchTerm[];
  resetKey?: number;
  onTermsChange?: (terms: SearchTerm[]) => void;
  onLiveChange?: (live: SearchTerm | null) => void;
}) {
  const [terms, setTerms] = useState(initialTerms);
  return (
    <GlobalSearchBar
      fields={accessors}
      terms={terms}
      onTermsChange={(next) => {
        onTermsChange?.(next);
        setTerms(next);
      }}
      onLiveChange={onLiveChange}
      testId="gsb"
      resetKey={resetKey}
    />
  );
}

describe("GlobalSearchBar", () => {
  it("reports the live text while typing and commits it as a chip on Enter", async () => {
    const user = userEvent.setup();
    const onTermsChange = vi.fn();
    const onLiveChange = vi.fn();
    render(
      <StatefulBar
        initialTerms={[]}
        onTermsChange={onTermsChange}
        onLiveChange={onLiveChange}
      />,
    );
    const input = screen.getByTestId("gsb");
    await user.type(input, "archive");
    expect(onLiveChange).toHaveBeenLastCalledWith({ field: null, text: "archive" });
    await user.type(input, "{Enter}");
    expect(onLiveChange).toHaveBeenLastCalledWith(null);
    expect(onTermsChange).toHaveBeenLastCalledWith([{ field: null, text: "archive" }]);
    expect(input).toHaveValue("");
    expect(screen.getByText("archive")).toBeVisible();
  });

  it("removes a committed chip through its affordance", async () => {
    const user = userEvent.setup();
    render(<StatefulBar initialTerms={[{ field: null, text: "archive" }]} />);
    await user.click(screen.getByRole("button", { name: /\u79fb\u9664\u6761\u4ef6/ }));
    expect(screen.queryByText("archive")).toBeNull();
  });

  it("replaces the previous any-field chip instead of stacking them", async () => {
    const user = userEvent.setup();
    render(
      <StatefulBar initialTerms={[{ field: null, text: "old-term" }, { field: "title", text: "title-term" }]} />,
    );
    await user.type(screen.getByTestId("gsb"), "new-term{Enter}");
    expect(screen.getByText("new-term")).toBeVisible();
    expect(screen.queryByText("old-term")).toBeNull();
    // The scoped chip survives — only the free-text slot is single.
    expect(screen.getByText("Title: title-term")).toBeVisible();
  });

  it("re-reports the live draft when a chip is removed (review M2)", async () => {
    const user = userEvent.setup();
    const onLiveChange = vi.fn();
    render(
      <StatefulBar
        initialTerms={[{ field: null, text: "archive" }]}
        onLiveChange={onLiveChange}
      />,
    );
    await user.type(screen.getByTestId("gsb"), "def");
    expect(onLiveChange).toHaveBeenLastCalledWith({ field: null, text: "def" });
    await user.click(screen.getByRole("button", { name: /\u79fb\u9664\u6761\u4ef6/ }));
    expect(screen.queryByText("archive")).toBeNull();
    expect(onLiveChange).toHaveBeenLastCalledWith({ field: null, text: "def" });
    expect(screen.getByTestId("gsb")).toHaveValue("def");
  });

  it("ignores Enter while an IME composition is active (review M3)", async () => {
    const user = userEvent.setup();
    const { onTermsChange } = renderBar();
    const input = screen.getByTestId("gsb");
    await user.type(input, "a");
    const composing = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    Object.defineProperty(composing, "isComposing", { value: true });
    input.dispatchEvent(composing);
    expect(onTermsChange).not.toHaveBeenCalled();
    expect(input).toHaveValue("a");
  });

  it("only commits exact enum labels on a scoped options field (review L5)", async () => {
    const user = userEvent.setup();
    const { onTermsChange } = renderBar();
    await user.click(screen.getByTestId("gsb"));
    await user.click(screen.getByRole("option", { name: "State" }));
    await user.type(screen.getByTestId("gsb"), "non-matching-value");
    await user.type(screen.getByTestId("gsb"), "{Enter}");
    expect(onTermsChange).not.toHaveBeenCalled();
    expect(screen.getByText("\u65e0\u5339\u914d\u9879")).toBeVisible();
  });

  it("closes the dropdown automatically when an option-less field is selected (owner 2026-11-05)", async () => {
    const user = userEvent.setup();
    renderBar();
    const input = screen.getByTestId("gsb");
    await user.click(input);
    expect(screen.getByRole("listbox")).toBeVisible();
    await user.click(screen.getByRole("option", { name: "Title" }));
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.queryByTestId("gsb-menu")).toBeNull();
    expect(input).toHaveAttribute("placeholder", "\u8f93\u5165Title\u540e\u6309\u56de\u8f66\u786e\u8ba4");
    expect(input).toHaveAttribute("aria-expanded", "false");
    expect(input).not.toHaveAttribute("aria-controls");
    expect(input).not.toHaveAttribute("aria-activedescendant");
  });

  /** The 15th of the current month, as the chip value / calendar text. */
  function currentMonthDay(day: number): { chip: string; label: string } {
    const now = new Date();
    const month = String(now.getMonth() + 1).padStart(2, "0");
    const paddedDay = String(day).padStart(2, "0");
    return {
      chip: `${String(now.getFullYear())}-${month}-${paddedDay}`,
      label: `${String(now.getFullYear())}\u5e74${String(now.getMonth() + 1)}\u6708${String(day)}\u65e5`,
    };
  }

  /** The calendar's day button — scoped to the dialog and matched by its
   * localized aria-label, so no DOM-order assumption is involved. */
  async function clickCalendarDay(user: ReturnType<typeof userEvent.setup>, day: number): Promise<void> {
    const now = new Date();
    const label = new RegExp(
      `^(?:Today,\\s*|\\u4eca\\u5929[\\uff0c,]\\s*)?${String(now.getFullYear())}\\u5e74${String(now.getMonth() + 1)}\\u6708${String(day)}\\u65e5`,
    );
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: label }));
  }

  it("scopes a date field to the inline calendar and commits the picked day (owner 2026-11-05)", async () => {
    const user = userEvent.setup();
    const onTermsChange = vi.fn();
    render(<StatefulBar initialTerms={[]} onTermsChange={onTermsChange} />);
    const input = screen.getByTestId("gsb");
    await user.click(input);
    await user.click(screen.getByRole("option", { name: "Start Date" }));
    // The suggestion listbox is replaced by the calendar dialog panel — no
    // hint or no-match copy, and combobox popup attributes wire to the
    // calendar dialog without a dangling activedescendant (review R2 M1).
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.queryByText("\u65e0\u5339\u914d\u9879")).toBeNull();
    const dialog = screen.getByRole("dialog", { name: "Start Date — \u9009\u62e9\u65e5\u671f" });
    expect(dialog).toBeVisible();
    expect(dialog.querySelector('[data-slot="calendar"]')).not.toBeNull();
    expect(input).toHaveAttribute("aria-expanded", "true");
    expect(input).toHaveAttribute("aria-haspopup", "dialog");
    expect(input).toHaveAttribute("aria-controls", dialog.id);
    expect(input).not.toHaveAttribute("aria-activedescendant");

    const picked = currentMonthDay(15);
    await clickCalendarDay(user, 15);
    expect(onTermsChange).toHaveBeenLastCalledWith([{ field: "from", text: picked.chip }]);
    expect(screen.getByText(`Start Date: ${picked.chip}`)).toBeVisible();
    expect(input).toHaveValue("");
    expect(input).toHaveAttribute("aria-expanded", "false");
    expect(input).not.toHaveAttribute("aria-controls");
  });

  it("never commits typed text for a date field; a second pick replaces the chip", async () => {
    const user = userEvent.setup();
    render(<StatefulBar initialTerms={[]} />);
    const input = screen.getByTestId("gsb");
    await user.click(input);
    await user.click(screen.getByRole("option", { name: "Start Date" }));
    await user.type(input, "2026");
    await user.type(input, "{Enter}");
    expect(screen.queryByText(/Start Date:/)).toBeNull();

    await clickCalendarDay(user, 15);
    const first = currentMonthDay(15);
    expect(screen.getByText(`Start Date: ${first.chip}`)).toBeVisible();
    // Picking closed the panel (ECS behavior); re-scoping reopens the
    // calendar with the committed day selected.
    expect(screen.queryAllByTestId("gsb-menu")).toHaveLength(0);
    await user.click(input);
    await user.click(screen.getByRole("option", { name: "Start Date" }));
    await clickCalendarDay(user, 16);
    const second = currentMonthDay(16);
    expect(screen.queryByText(`Start Date: ${first.chip}`)).toBeNull();
    expect(screen.getByText(`Start Date: ${second.chip}`)).toBeVisible();
  });

  it("scopes a field from the suggestion list, picks a translated option, and closes the dropdown", async () => {
    const user = userEvent.setup();
    const { onTermsChange } = renderBar();
    await user.click(screen.getByTestId("gsb"));
    await user.click(screen.getByRole("option", { name: "State" }));
    expect(screen.getByTestId("gsb")).toHaveAttribute("placeholder", "\u8f93\u5165State\u7684\u503c");
    await user.click(screen.getByRole("option", { name: "Running" }));
    expect(onTermsChange).toHaveBeenLastCalledWith([{ field: "state", text: "Running" }]);
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.queryByTestId("gsb-menu")).toBeNull();
    expect(screen.getByTestId("gsb")).toHaveAttribute("aria-expanded", "false");
  });

  it("typing filters field suggestions and Enter commits the global term", async () => {
    const user = userEvent.setup();
    const { onTermsChange, onLiveChange } = renderBar();
    await user.click(screen.getByTestId("gsb"));
    await user.type(screen.getByTestId("gsb"), "Title");
    expect(screen.getByRole("option", { name: "\u641c\u7d22 “Title”" })).toBeVisible();
    expect(screen.queryByRole("option", { name: "State" })).toBeNull();
    await user.type(screen.getByTestId("gsb"), "{Enter}");
    expect(onLiveChange).toHaveBeenCalledWith({ field: null, text: "Title" });
    expect(onTermsChange).toHaveBeenLastCalledWith([{ field: null, text: "Title" }]);
  });

  it("Backspace on an empty input removes the last chip and clear-all empties everything", async () => {
    const user = userEvent.setup();
    render(
      <StatefulBar
        initialTerms={[
          { field: null, text: "archive" },
          { field: "state", text: "Running" },
        ]}
      />,
    );
    await user.type(screen.getByTestId("gsb"), "{Backspace}");
    expect(screen.queryByText("State: Running")).toBeNull();
    expect(screen.getByText("archive")).toBeVisible();
    await user.click(screen.getByTestId("gsb-clear"));
    expect(screen.queryByText("archive")).toBeNull();
  });

  it("a resetKey bump clears the local draft", async () => {
    const user = userEvent.setup();
    const { rerender } = renderBar();
    await user.type(screen.getByTestId("gsb"), "draft-term");
    rerender(
      <GlobalSearchBar
        fields={accessors}
        terms={[]}
        onTermsChange={vi.fn()}
        onLiveChange={vi.fn()}
        testId="gsb"
        resetKey={1}
      />,
    );
    await waitFor(() => {
      expect(screen.getByTestId("gsb")).toHaveValue("");
    });
  });
});
