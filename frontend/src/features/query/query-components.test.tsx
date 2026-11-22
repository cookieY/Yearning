import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HttpResponse, http } from "msw";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/msw/server";
import { resetQueryFixture, seedQueryScenario, QUERY_FIXTURE_DS_MYSQL_ID } from "@/shared/mock/query-fixture";
import { SessionProvider } from "@/features/auth/session-provider";
import "@/shared/i18n";
import { MetadataTree } from "@/features/query/metadata-tree";
import { ResultGrid, stringifyCell, type ResultTabState } from "@/features/query/result-grid";

/**
 * Query workspace domain components: the frozen-scope metadata tree (lazy
 * schema → table → column levels with live masked flags and the SELECT
 * template insertion) and the cursor-driven result grid (virtualized rows,
 * rows-loaded footer, continuation and exhaustion states — never a total).
 */

const SESSION_USER_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

function stubSession(): void {
  server.use(
    http.get("*/users/me", () =>
      HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: {
          id: SESSION_USER_ID,
          username: "henry",
          display_name: "henry",
          email: null,
          is_builtin_admin: false,
          version: 1,
          created_at: "2026-08-28T08:00:00Z",
          updated_at: "2026-08-28T08:00:00Z",
          can_access_admin: false,
        },
        request_id: SESSION_USER_ID,
      }),
    ),
  );
}

beforeAll(() => {
  Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
  Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
  Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });
});

beforeEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "default");
  resetQueryFixture();
});

describe("MetadataTree", () => {
  async function renderTree(onTableSelected: (schema: string, table: string) => void = vi.fn()) {
    seedQueryScenario("query-session");
    stubSession();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const session = {
      id: "qs-fixture-active",
      user_id: SESSION_USER_ID,
      state: "active" as const,
      capabilities: [
        {
          datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
          datasource_name: "analytics-mysql",
          state: "active" as const,
          can_query: true as const,
          can_export: true,
        },
      ],
      created_at: "2026-08-31T00:00:00Z",
    };
    render(
      <QueryClientProvider client={client}>
        <SessionProvider>
          <MetadataTree session={session} activeDatasourceId={QUERY_FIXTURE_DS_MYSQL_ID} onTableSelected={onTableSelected} />
        </SessionProvider>
      </QueryClientProvider>,
    );
    await screen.findByTestId("query-metadata-tree");
    await screen.findByTestId("query-tree-schema-app");
    return onTableSelected;
  }

  it("expands schema → table → columns with the live masked flag", async () => {
    await renderTree();
    await userEvent.click(screen.getByTestId("query-tree-schema-app"));
    await screen.findByTestId("query-tree-table-app-users");
    await userEvent.click(screen.getByTestId("query-tree-expand-app-users"));
    const columns = await screen.findByTestId("query-tree-columns-app-users");
    expect(columns.textContent).toContain("email");
    expect(columns.textContent).toContain("\u8131\u654f");
  });

  it("inserts a SELECT template when a table is clicked", async () => {
    const onTableSelected = await renderTree();
    await userEvent.click(screen.getByTestId("query-tree-schema-app"));
    await userEvent.click(await screen.findByTestId("query-tree-table-app-users"));
    expect(onTableSelected).toHaveBeenCalledWith("app", "users");
  });

  it("filters tables by the search box", async () => {
    await renderTree();
    await userEvent.click(screen.getByTestId("query-tree-schema-app"));
    await screen.findByTestId("query-tree-table-app-users");
    await userEvent.type(screen.getByTestId("query-tree-filter"), "order");
    await waitFor(() => {
      expect(screen.queryByTestId("query-tree-table-app-users")).not.toBeInTheDocument();
    });
    expect(screen.getByTestId("query-tree-table-app-orders")).toBeVisible();
  });
});

describe("ResultGrid", () => {
  function tabOf(overrides: Partial<ResultTabState> = {}): ResultTabState {
    return {
      executionId: "exec-grid",
      columns: [
        { name: "id", type: "int" },
        { name: "email", type: "varchar" },
      ],
      rows: [
        [1, "***"],
        [2, "***"],
      ],
      nextCursor: "next",
      elapsedMs: 12,
      exhausted: false,
      maskedByName: new Map([["email", true]]),
      ...overrides,
    };
  }

  it("renders virtualized rows with the masked header badge and loaded count", () => {
    render(
      <ResultGrid
        tab={tabOf()}
        continuationError={null}
      />,
    );
    expect(screen.getByTestId("query-result-exec-grid")).toBeVisible();
    expect(screen.getByTestId("query-result-elapsed-exec-grid")).toHaveTextContent("12 ms");
    expect(screen.getByTestId("query-result-loaded-exec-grid")).toHaveTextContent("\u5df2\u8bfb\u53d6 2 \u884c");
    expect(screen.getByText("\u5df2\u6309\u654f\u611f\u5b57\u6bb5\u7b56\u7565\u8131\u654f")).toBeVisible();
    // §18.50 follow-up (legacy parity): paging is purely local over the
    // loaded rows — with everything loaded the pager's Next is disabled.
    expect(within(screen.getByTestId("query-result-pagination")).getByRole("button", { name: "\u4e0b\u4e00\u9875" })).toBeDisabled();
  });

  it("reports exhaustion instead of a fabricated total", () => {
    render(
      <ResultGrid
        tab={tabOf({ rows: [[1]], nextCursor: null, exhausted: true })}
        continuationError={null}
      />,
    );
    expect(screen.getByTestId("query-result-exhausted-exec-grid")).toHaveTextContent("\u5df2\u8bfb\u53d6\u5168\u90e8\u7ed3\u679c");
    // Everything loaded and exhausted: the local pager has nothing more.
    expect(within(screen.getByTestId("query-result-pagination")).getByRole("button", { name: "\u4e0b\u4e00\u9875" })).toBeDisabled();
    expect(screen.getByTestId("query-result-exec-grid").textContent).not.toContain("\u5171 ");
  });

  it("pages the loaded rows locally and offers a resize handle per column (§18.50)", () => {
    render(
      <ResultGrid
        tab={tabOf({ rows: Array.from({ length: 250 }, (_, i) => [i + 1, "***"]) })}
        continuationError={null}
      />,
    );
    // Client-side pager over the loaded rows (no fabricated total) with FAB clearance on desktop.
    expect(screen.getByTestId("query-result-pagination")).toBeVisible();
    expect(screen.getByTestId("query-result-pagination").parentElement?.parentElement).toHaveClass("md:pr-32");
    expect(screen.getByTestId("query-result-page").textContent).toContain("1");
    // Drag-resize handles exist for every column with a screen-reader label.
    for (const name of ["id", "email"]) {
      expect(screen.getByTestId(`query-col-resize-${name}`)).toBeVisible();
    }
    expect(screen.getByRole("separator", { name: "\u8c03\u6574\u5217\u5bbd\uff1aid" })).toBeVisible();
  });

  it("stringifies non-string cells losslessly (never [object Object])", () => {
    // jsdom gives the virtualizer no layout, so the renderer is asserted
    // directly instead of through the DOM.
    expect(stringifyCell("text")).toBe("text");
    expect(stringifyCell(true)).toBe("true");
    expect(stringifyCell({ nested: 1 })).toBe('{"nested":1}');
    expect(stringifyCell(null)).toBe("null");
  });

  it("shows the continuation error inline for CURSOR_EXPIRED paths", () => {
    render(
      <ResultGrid
        tab={tabOf({ rows: [[1]] })}
        continuationError="Cursor expired, please re-run the query"
      />,
    );
    expect(screen.getByTestId("query-result-continuation-error-exec-grid")).toHaveTextContent("Cursor expired");
  });
});
