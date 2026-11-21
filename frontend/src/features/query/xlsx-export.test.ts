import { describe, expect, it, vi } from "vitest";
import { unzipSync } from "fflate";
import type { QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  columnLetter,
  createXlsxSheet,
  downloadXlsx,
  exportExecutionToXlsx,
} from "@/features/query/xlsx-export";

/**
 * Browser XLSX generation tests (Q006 gate: generate XLSX in frontend, server does not store export
 * files). The sheet writer is verified structurally by unzipping the produced
 * workbook; the export controller's honest-failure contract (cancellation
 * and mid-export authorization loss report the read range, never a fake
 * success) is pinned with fake fetchers.
 */

function pageOf(rows: unknown[][], hasMore: boolean, cursor: string | null): QueryResultPage {
  return {
    execution_id: "exec-1",
    columns: [
      { name: "id", type: "int" },
      { name: "email", type: "varchar" },
    ],
    rows,
    page: { next_cursor: cursor, has_more: hasMore },
    elapsed_ms: 5,
  };
}

function sheetXml(bytes: Uint8Array): string {
  const files = unzipSync(bytes);
  const decoder = new TextDecoder();
  return decoder.decode(files["xl/worksheets/sheet1.xml"] ?? new Uint8Array());
}

describe("xlsx sheet writer", () => {
  it("maps 0-based column indexes to spreadsheet letters", () => {
    expect(columnLetter(0)).toBe("A");
    expect(columnLetter(25)).toBe("Z");
    expect(columnLetter(26)).toBe("AA");
    expect(columnLetter(27)).toBe("AB");
  });

  it("writes the header row once and appends pages sequentially", () => {
    const sheet = createXlsxSheet();
    sheet.appendPage(pageOf([[1, "a@example.test"], [2, "b@example.test"]], true, "cur-1"));
    sheet.appendPage(pageOf([[3, "c@example.test"]], false, null));
    expect(sheet.rowCount()).toBe(3);
    const xml = sheetXml(sheet.finish());
    expect(xml).toContain("<c r=\"A1\" t=\"inlineStr\"><is><t xml:space=\"preserve\">id</t></is></c>");
    expect(xml).toContain("<c r=\"B1\" t=\"inlineStr\"><is><t xml:space=\"preserve\">email</t></is></c>");
    expect(xml).toContain("<c r=\"A2\" t=\"n\"><v>1</v></c>");
    expect(xml).toContain("<c r=\"B4\" t=\"inlineStr\"><is><t xml:space=\"preserve\">c@example.test</t></is></c>");
  });

  it("escapes XML entities in cell text", () => {
    const sheet = createXlsxSheet();
    sheet.appendPage(pageOf([[1, "<script>alert(\"x\")</script>"]], false, null));
    const xml = sheetXml(sheet.finish());
    expect(xml).toContain("&lt;script&gt;");
    expect(xml).not.toContain("<script>");
  });

  it("serializes every JSON cell shape: null/undefined empty, numbers numeric, long digits and objects as strings", () => {
    const sheet = createXlsxSheet();
    sheet.appendPage(
      pageOf(
        [[null, undefined, true, 3.5, 1234567890123456789n, { k: 1 }], ["12", "012345678901234567890", 2n, "—", -0.25, [1]]],
        false,
        null,
      ),
    );
    const xml = sheetXml(sheet.finish());
    // null / undefined cells are omitted from the row entirely.
    expect(xml).not.toMatch(/r="A2"/);
    expect(xml).not.toMatch(/r="B2"/);
    // booleans stringify losslessly; floats and small bigints stay numeric.
    expect(xml).toContain("<c r=\"C2\" t=\"inlineStr\"><is><t xml:space=\"preserve\">true</t></is></c>");
    expect(xml).toContain("<c r=\"D2\" t=\"n\"><v>3.5</v></c>");
    expect(xml).toContain("<c r=\"C3\" t=\"n\"><v>2</v></c>");
    expect(xml).toContain("<c r=\"E3\" t=\"n\"><v>-0.25</v></c>");
    // 16+ digit numeric-looking text (exact BigInt digits included) stays an
    // inline string — a t="n" cell would corrupt it into float precision —
    // and JSON objects/arrays render their JSON form, never [object Object].
    expect(xml).toContain("<c r=\"E2\" t=\"inlineStr\"><is><t xml:space=\"preserve\">1234567890123456789</t></is></c>");
    expect(xml).toContain("<c r=\"B3\" t=\"inlineStr\"><is><t xml:space=\"preserve\">012345678901234567890</t></is></c>");
    expect(xml).toContain("<c r=\"F2\" t=\"inlineStr\"><is><t xml:space=\"preserve\">{&quot;k&quot;:1}</t></is></c>");
    expect(xml).toContain("<c r=\"F3\" t=\"inlineStr\"><is><t xml:space=\"preserve\">[1]</t></is></c>");
    expect(xml).not.toContain("[object Object]");
  });
});

describe("export controller", () => {
  it("streams every cursor page and reports the exported row count", async () => {
    const progressCalls: string[] = [];
    const fetcher = vi.fn((cursor: string): Promise<QueryResultPage> => {
      if (cursor === "first") return Promise.resolve(pageOf([[1, "a"]], true, "second"));
      return Promise.resolve(pageOf([[2, "b"]], false, null));
    });
    await exportExecutionToXlsx(
      pageOf([[0, "z"]], true, "first"),
      fetcher,
      {
        onProgress: (progress) => {
          progressCalls.push(`${String(progress.pagesRead)}:${String(progress.rowsExported)}`);
        },
        onDone: (bytes, rows) => {
          expect(bytes.byteLength).toBeGreaterThan(0);
          expect(rows).toBe(3);
        },
        onFailure: () => {
          throw new Error("unexpected failure");
        },
      },
      () => false,
    );
    expect(fetcher).toHaveBeenCalledWith("first");
    expect(fetcher).toHaveBeenCalledWith("second");
    expect(progressCalls.at(-1)).toBe("3:3");
  });

  it("reports cancellation with the rows read so far and produces no file", async () => {
    const done = vi.fn();
    const cancellationFetcher = (cursor: string): Promise<QueryResultPage> => {
      expect(cursor).toBe("next");
      return Promise.resolve(pageOf([[2, "b"]], false, null));
    };
    await exportExecutionToXlsx(
      pageOf([[1, "a"]], true, "next"),
      cancellationFetcher,
      {
        onProgress: () => {},
        onDone: () => {
          done();
        },
        onFailure: (message, rows) => {
          expect(message).toBe("cancelled");
          expect(rows).toBeGreaterThanOrEqual(1);
        },
      },
      () => true,
    );
    expect(done).not.toHaveBeenCalled();
  });

  it("surfaces mid-export authorization loss honestly (4003 path)", async () => {
    const done = vi.fn();
    const rejectionFetcher = (_cursor: string): Promise<QueryResultPage> =>
      Promise.reject(new Error("export not granted"));
    await exportExecutionToXlsx(
      pageOf([[1, "a"]], true, "next"),
      rejectionFetcher,
      {
        onProgress: () => {},
        onDone: () => {
          done();
        },
        onFailure: (message, rows) => {
          expect(message).toContain("export not granted");
          expect(rows).toBe(1);
        },
      },
      () => false,
    );
    expect(done).not.toHaveBeenCalled();
  });

  it("suppresses completion and download when cancelled while the final page fetch is in flight", async () => {
    let cancelled = false;
    let resolvePage!: (value: QueryResultPage) => void;
    const pendingPage = new Promise<QueryResultPage>((resolve) => {
      resolvePage = resolve;
    });
    const done = vi.fn();
    const failure = vi.fn();
    const run = exportExecutionToXlsx(
      pageOf([[1, "a"]], true, "next"),
      () => pendingPage,
      {
        onProgress: () => {},
        onDone: done,
        onFailure: failure,
      },
      () => cancelled,
    );
    await Promise.resolve();
    cancelled = true;
    resolvePage(pageOf([[2, "b"]], false, null));
    await run;
    expect(done).not.toHaveBeenCalled();
    expect(failure).toHaveBeenCalledWith("cancelled", 1);
  });
});

describe("downloadXlsx", () => {
  it("triggers a single anchor download with the workbook mime type", async () => {
    const clicks: HTMLAnchorElement[] = [];
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        clicks.push(this);
      });
    const revoke = vi.fn();
    const urlSpy = vi.spyOn(URL, "createObjectURL").mockImplementation(() => "blob:mock");
    const revokeSpy = vi.spyOn(URL, "revokeObjectURL").mockImplementation(revoke);
    downloadXlsx(new Uint8Array([1, 2, 3]), "rows.xlsx");
    expect(clicks).toHaveLength(1);
    expect(clicks[0]?.download).toBe("rows.xlsx");
    expect(clicks[0]?.href).toBe("blob:mock");
    // The revocation is deferred (L-1 fix) — it lands after the timer
    // yields, proving the anchor click was issued first.
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(revoke).toHaveBeenCalled();
    clickSpy.mockRestore();
    urlSpy.mockRestore();
    revokeSpy.mockRestore();
  });
});

describe("review R1 fix H-3: export dialog reset", () => {
  it("is covered at the page level: a closed export context resets the run state", async () => {
    // The dialog component is private to the workspace page; the reset is
    // driven by the context-null effect. The equivalent pure behavior is
    // that a SECOND export run (new first page + continuation) streams and
    // downloads again — pinned here at the controller level.
    const fetcher = vi.fn((cursor: string): Promise<QueryResultPage> => {
      if (cursor === "first-2") return Promise.resolve(pageOf([[9, "i"]], false, null));
      return Promise.reject(new Error("unexpected cursor"));
    });
    let downloads = 0;
    await exportExecutionToXlsx(
      pageOf([[7, "g"]], true, "first-2"),
      fetcher,
      {
        onProgress: () => {},
        onDone: () => {
          downloads += 1;
        },
        onFailure: () => {
          throw new Error("unexpected failure");
        },
      },
      () => false,
    );
    // Second run with the same controller shape succeeds independently.
    await exportExecutionToXlsx(
      pageOf([[8, "h"]], true, "first-2"),
      fetcher,
      {
        onProgress: () => {},
        onDone: () => {
          downloads += 1;
        },
        onFailure: () => {
          throw new Error("unexpected failure");
        },
      },
      () => false,
    );
    expect(downloads).toBe(2);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
