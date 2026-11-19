import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import i18n from "@/shared/i18n";

import { TablePagination } from "./table-pagination";

// Immediate acknowledgement of a page change.
//
// Cursor paging keeps the previous page on screen while the next one loads
// (`keepPreviousData`), so the rows never flash empty and the footer control is
// the immediate acknowledgement of the click. These assertions pin that the
// busy state is rendered on the same commit as the click: the Next control is
// disabled, the footer reports aria-busy, and a busy dot is present. The
// browser timing budget for the general "feedback within 100ms" criterion lives
// in tests/e2e/motion-feedback.spec.ts.

function renderFooter(busy: boolean, hasMore = true) {
  return render(
    <TablePagination
      page={2}
      hasMore={hasMore}
      isFirst={false}
      onPrev={vi.fn()}
      onNext={vi.fn()}
      pageSize={20}
      onPageSizeChange={vi.fn()}
      testIdPrefix="probe"
      busy={busy}
    />,
  );
}

// The speculative page+1 pill and Next only make sense while the current
// page has rows; an empty walked page must terminate the walk and walk
// itself back to page 1 (owner bug report 2026-10-24: an empty table kept
// offering two pages and an endless walk forward).
function renderEmptyPage(onJumpPage?: (page: number) => void) {
  return render(
    <TablePagination
      page={2}
      hasMore
      isFirst={false}
      onPrev={vi.fn()}
      onNext={vi.fn()}
      onJumpPage={onJumpPage}
      pageSize={20}
      onPageSizeChange={vi.fn()}
      testIdPrefix="probe"
      rowsOnPage={0}
    />,
  );
}

describe("TablePagination empty-page handling", () => {
  it("offers no forward page when the current page is empty", () => {
    renderEmptyPage();

    expect(screen.getByTestId("probe-next")).toBeDisabled();
    // page 2 stays listed (it was walked), but the speculative page 3 must
    // not exist.
    expect(screen.queryByRole("button", { name: i18n.t("table.pagination.page", { page: 3 }) }))
      .not.toBeInTheDocument();
  });

  it("walks itself back to page 1 when a visited page comes back empty", () => {
    const onJumpPage = vi.fn();
    renderEmptyPage(onJumpPage);

    expect(onJumpPage).toHaveBeenCalledWith(1);
  });
});

describe("TablePagination busy feedback", () => {
  it("marks the footer busy and shows the dot while a page is loading", () => {
    renderFooter(true);

    expect(screen.getByTestId("probe-pagination")).toHaveAttribute("aria-busy", "true");
    expect(screen.getByTestId("probe-busy")).toBeInTheDocument();
    expect(screen.getByTestId("probe-next")).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent(i18n.t("table.pagination.loading"));
  });

  it("renders no busy affordance once the page has settled", () => {
    renderFooter(false);

    expect(screen.getByTestId("probe-pagination")).not.toHaveAttribute("aria-busy");
    expect(screen.queryByTestId("probe-busy")).not.toBeInTheDocument();
    expect(screen.getByTestId("probe-next")).toBeEnabled();
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
  });

  it("keeps a last page disabled for a reason the user can see", () => {
    renderFooter(false, false);

    expect(screen.getByTestId("probe-next")).toBeDisabled();
    expect(screen.queryByTestId("probe-busy")).not.toBeInTheDocument();
  });
});

// The component test above only proves TablePagination renders busy correctly.
// This guard proves every call site actually passes it — a page can otherwise
// lose the acknowledgement while the component suite stays green.
const SOURCES = import.meta.glob("/src/**/*.tsx", {
  eager: true,
  query: "?raw",
  import: "default",
}) as Record<string, string>;

// Client-side slicing never leaves the page, so there is no fetch to signal.
const CLIENT_ONLY_PAGERS = new Set(["src/features/query/result-grid.tsx"]);

describe("TablePagination call sites", () => {
  it("wire busy and rowsOnPage on every paginated table", () => {
    const offenders = Object.entries(SOURCES).flatMap(([file, source]) => {
      const relative = file.replace(/^\//, "");
      if (relative.endsWith(".test.tsx") || CLIENT_ONLY_PAGERS.has(relative)) return [];
      const tags = [
        ...source.matchAll(/<TablePagination\b[\s\S]*?\/>/g),
        ...source.matchAll(/<TablePagination\b[\s\S]*?<\/TablePagination>/g),
      ].map((match) => match[0]);
      return tags
        .filter(
          (tag) =>
            !/busy=\{(?!\s*false\s*\})[^}]*\}/.test(tag) || !/rowsOnPage=\{/.test(tag),
        )
        .map(() => relative);
    });
    expect(offenders).toEqual([]);
  });
});
