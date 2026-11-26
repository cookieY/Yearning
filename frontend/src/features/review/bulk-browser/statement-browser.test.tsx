import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeAll, describe, expect, it, vi } from "vitest";
import "@/shared/i18n";
import { digestSqlText } from "@/features/review/bulk-import/sql-digest";
import { StatementBrowser } from "@/features/review/bulk-browser/statement-browser";

/**
 * The virtualized browser must keep the DOM bounded for huge drafts
 * (acceptance gate: never render all statements), surface anomalous statements outside any
 * aggregate (single anomalies are never hidden by aggregation), jump to an ordinal and download a report
 * that carries no SQL text.
 */

// jsdom has no layout engine: the virtualizer reads offsetWidth/offsetHeight
// (virtual-core getRect), so give every element a measurable size, and make
// scrollTo actually move scrollTop so the window follows the jump.
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get: () => 440,
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 800,
  });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    width: 800,
    height: 440,
    top: 0,
    left: 0,
    bottom: 440,
    right: 800,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  });
  // jsdom does not implement Element.scrollTo at all — define it.
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    writable: true,
    value: function mockScrollTo(
      this: HTMLElement,
      arg: number | ScrollToOptions,
    ): void {
      const top = typeof arg === "number" ? arg : (arg.top ?? 0);
      this.scrollTop = top;
    },
  });
  // jsdom stores scrollTop without firing scroll events; the virtualizer
  // follows the offset only through those events, so dispatch on set.
  const scrollTops = new WeakMap<HTMLElement, number>();
  Object.defineProperty(HTMLElement.prototype, "scrollTop", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollTops.get(this) ?? 0;
    },
    set(this: HTMLElement, value: number) {
      scrollTops.set(this, value);
      this.dispatchEvent(new Event("scroll"));
    },
  });
});

function buildBulkSql(statementCount: number, anomalyIndex: number): string {
  const parts: string[] = [];
  for (let i = 1; i <= statementCount; i += 1) {
    parts.push(
      i === anomalyIndex
        ? "UPDATE orders SET status = 'processed';\n"
        : `UPDATE orders SET status = 'processed', updated_at = '2026-08-25' WHERE id = ${String(i)};\n`,
    );
  }
  return parts.join("");
}

const SQL = buildBulkSql(3000, 1234);
const DIGEST = digestSqlText(SQL);

function renderBrowser(overrides: Partial<Parameters<typeof StatementBrowser>[0]> = {}) {
  return render(
    <StatementBrowser
      sql={SQL}
      digest={DIGEST}
      serverGroupCount={null}
      {...overrides}
    />,
  );
}

function highlightedIndex(): string | null {
  const rows = screen.getAllByTestId("bulk-statement-row");
  const highlighted = rows.find((row) => row.getAttribute("data-highlighted") !== null);
  return highlighted?.getAttribute("data-statement-index") ?? null;
}

describe("StatementBrowser", () => {
  it("renders a bounded number of rows for thousands of statements", () => {
    renderBrowser();
    // 3000 statements in two shape groups: the DOM must never materialize
    // one row per statement — only the virtual window (+ overscan).
    const groupRows = screen.getAllByTestId("bulk-group-row");
    expect(groupRows.length).toBeLessThanOrEqual(40);
    fireEvent.click(screen.getByTestId("bulk-statements-tab"));
    const statementRows = screen.getAllByTestId("bulk-statement-row");
    expect(statementRows.length).toBeGreaterThan(0);
    expect(statementRows.length).toBeLessThanOrEqual(60);
  });

  it("lists shape groups with counts, ranges and the anomaly badge", () => {
    renderBrowser();
    const rows = screen.getAllByTestId("bulk-group-row");
    expect(rows).toHaveLength(2);
    const [majorityGroup, anomalyGroup] = rows;
    if (majorityGroup === undefined || anomalyGroup === undefined) {
      throw new Error("expected two group rows");
    }
    expect(within(majorityGroup).getByText("2999 \u6761")).toBeInTheDocument();
    expect(within(anomalyGroup).getByText("1 \u6761")).toBeInTheDocument();
    expect(within(anomalyGroup).getByText("\u5f02\u5e38 1")).toBeInTheDocument();
  });

  it("shows the anomaly strip and jumps to the anomalous statement", async () => {
    renderBrowser();
    fireEvent.click(screen.getByTestId("bulk-jump-anomaly"));
    await waitFor(() => {
      expect(highlightedIndex()).toBe("1234");
    });
  });

  it("filters statements by a selected shape group", () => {
    renderBrowser();
    const groupRows = screen.getAllByTestId("bulk-group-row");
    fireEvent.click(groupRows[1] as HTMLElement);
    const rows = screen.getAllByTestId("bulk-statement-row");
    // The anomaly group holds exactly one statement: only that row can show.
    for (const row of rows) {
      expect(["1233", "1234", "1235"]).toContain(row.getAttribute("data-statement-index"));
    }
    expect(screen.getByText(/\u5df2\u6309SQL\u6307\u7eb9 #2/)).toBeInTheDocument();
  });

  it("previews lazily sliced text without rendering the full statement", () => {
    const long =
      `UPDATE t SET note = '${"x".repeat(2000)}' WHERE id = 1;\n`.repeat(20) +
      "SELECT 1;\n";
    render(<StatementBrowser sql={long} digest={digestSqlText(long)} serverGroupCount={null} />);
    fireEvent.click(screen.getByTestId("bulk-statements-tab"));
    const rows = screen.getAllByTestId("bulk-statement-row");
    for (const row of rows) {
      expect(row.textContent.length).toBeLessThan(400);
    }
  });

  it("downloads a report blob that carries no SQL text", async () => {
    const created: Blob[] = [];
    class BlobSpy extends Blob {
      constructor(parts: BlobPart[], options?: BlobPropertyBag) {
        super(parts, options);
        created.push(this);
      }
    }
    vi.stubGlobal("Blob", BlobSpy);
    vi.stubGlobal("URL", {
      createObjectURL: () => "blob:mock",
      revokeObjectURL: () => {},
    });
    renderBrowser();
    fireEvent.click(screen.getByTestId("bulk-report-download"));
    vi.unstubAllGlobals();
    expect(created.length).toBe(1);
    const reportBlob = created[0];
    if (reportBlob === undefined) throw new Error("report blob was not created");
    const text = await reportBlob.text();
    expect(text).toContain("statement_count,3000");
    expect(text).toContain("local_group_count,2");
    expect(text).not.toContain("processed");
    expect(text).not.toContain("2026-08-25");
  });

  it("shows the empty state for a draft without statements", () => {
    const empty = digestSqlText("");
    render(
      <StatementBrowser sql="" digest={empty} serverGroupCount={null} />,
    );
    expect(screen.queryAllByTestId("bulk-statement-row")).toHaveLength(0);
    expect(screen.queryByTestId("bulk-jump-anomaly")).not.toBeInTheDocument();
    // The report download stays available even for an empty draft.
    expect(screen.getByTestId("bulk-report-download")).toBeInTheDocument();
  });

  it("formats statement sizes across the byte, KiB and MiB bands", () => {
    const sql =
      `INSERT INTO tiny VALUES (1);\n` +
      `INSERT INTO kilo VALUES ('${"k".repeat(1600)}');\n` +
      `INSERT INTO mega VALUES ('${"m".repeat(1_200_000)}');\n`;
    render(<StatementBrowser sql={sql} digest={digestSqlText(sql)} serverGroupCount={null} />);
    fireEvent.click(screen.getByTestId("bulk-statements-tab"));
    const rows = screen.getAllByTestId("bulk-statement-row");
    const texts = rows.map((row) => row.textContent);
    expect(texts.some((text) => /\d+ B/.test(text))).toBe(true);
    expect(texts.some((text) => /\d+\.\d KiB/.test(text))).toBe(true);
    expect(texts.some((text) => /\d+\.\d MiB/.test(text))).toBe(true);
  });
});
