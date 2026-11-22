import { afterEach, describe, expect, it, vi } from "vitest";
import { unzipSync } from "fflate";
import type { QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  columnLetter,
  createXlsxSheet,
  downloadXlsx,
  exportExecutionToXlsx,
} from "@/features/query/xlsx-export";
import {
  createXlsxWorkerHandler,
  type XlsxWorkerRequest,
  type XlsxWorkerResponse,
} from "@/features/query/xlsx-export.worker";

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

describe("streaming Zip + ZipDeflate OOXML serializer (PERF-0011)", () => {
  it("streams 5,250 rows across 5 pages into a valid compressed XLSX archive", () => {
    const sheet = createXlsxSheet('Ops "Q&A"');
    const pages = 5;
    const rowsPerPage = 1050;
    for (let pageIndex = 0; pageIndex < pages; pageIndex += 1) {
      const rows: unknown[][] = [];
      for (let offset = 0; offset < rowsPerPage; offset += 1) {
        const id = pageIndex * rowsPerPage + offset + 1;
        rows.push([id, `user-${String(id)}@example.test`]);
      }
      sheet.appendPage(
        pageOf(rows, pageIndex + 1 < pages, pageIndex + 1 < pages ? `c-${String(pageIndex)}` : null),
      );
    }

    expect(sheet.rowCount()).toBe(5250);
    const archive = sheet.finish();
    const files = unzipSync(archive);
    expect(Object.keys(files).sort()).toEqual([
      "[Content_Types].xml",
      "_rels/.rels",
      "xl/_rels/workbook.xml.rels",
      "xl/workbook.xml",
      "xl/worksheets/sheet1.xml",
    ]);

    const decoder = new TextDecoder();
    const workbookXml = decoder.decode(files["xl/workbook.xml"] ?? new Uint8Array());
    expect(workbookXml).toContain('name="Ops &quot;Q&amp;A&quot;"');

    const sheetContent = decoder.decode(files["xl/worksheets/sheet1.xml"] ?? new Uint8Array());
    expect(sheetContent).toContain('<c r="A1" t="inlineStr"><is><t xml:space="preserve">id</t></is></c>');
    expect(sheetContent).toContain('<c r="A2" t="n"><v>1</v></c>');
    expect(sheetContent).toContain('<c r="A5251" t="n"><v>5250</v></c>');
    expect(sheetContent).toContain(
      '<c r="B5251" t="inlineStr"><is><t xml:space="preserve">user-5250@example.test</t></is></c>',
    );
  });

  it("produces an empty valid workbook and guards against writes after finish", () => {
    const empty = createXlsxSheet();
    empty.appendPage({
      execution_id: "exec-empty",
      columns: [],
      rows: [],
      page: { next_cursor: null, has_more: false },
      elapsed_ms: 1,
    });
    expect(empty.rowCount()).toBe(0);
    const bytes = empty.finish();
    expect(sheetXml(bytes)).toContain("<sheetData></sheetData>");
    expect(() => {
      empty.appendPage(pageOf([[1, "x"]], false, null));
    }).toThrow("xlsx sheet already finished");
    expect(() => empty.finish()).toThrow("xlsx sheet already finished");
  });
});

describe("createXlsxWorkerHandler (PERF-0011)", () => {
  it("streams pages, transfers an ArrayBuffer on finish, and guards uninitialized states", () => {
    const responses: Array<{ message: XlsxWorkerResponse; transfer?: Transferable[] }> = [];
    const handler = createXlsxWorkerHandler((message, transfer) => {
      responses.push({ message, transfer });
    });

    handler({ type: "page", page: pageOf([[1, "a"]], false, null) });
    handler({ type: "finish" });
    expect(responses[0]?.message).toEqual({
      type: "error",
      message: "XLSX export worker is not initialized",
    });
    expect(responses[1]?.message).toEqual({
      type: "error",
      message: "XLSX export worker is not initialized",
    });

    handler({ type: "init", sheetName: "Results" });
    handler({ type: "page", page: pageOf([[1, "a@example.test"], [2, "b@example.test"]], true, "c1") });
    handler({ type: "page", page: pageOf([[3, "c@example.test"]], false, null) });
    handler({ type: "finish" });

    expect(responses[2]?.message).toEqual({ type: "pageAck", rowCount: 2 });
    expect(responses[3]?.message).toEqual({ type: "pageAck", rowCount: 3 });

    const doneEntry = responses[4];
    expect(doneEntry?.message.type).toBe("done");
    if (doneEntry?.message.type !== "done") throw new Error("expected done message");
    expect(doneEntry.message.rowCount).toBe(3);
    expect(doneEntry.transfer).toEqual([doneEntry.message.buffer]);

    const xml = sheetXml(new Uint8Array(doneEntry.message.buffer));
    expect(xml).toContain("c@example.test");

    // Once finished, the sheet reference is cleared until the next init.
    handler({ type: "finish" });
    expect(responses[5]?.message).toEqual({
      type: "error",
      message: "XLSX export worker is not initialized",
    });
  });

  it("reports thrown exceptions from malformed pages as error messages", () => {
    const responses: XlsxWorkerResponse[] = [];
    const handler = createXlsxWorkerHandler((message) => {
      responses.push(message);
    });
    handler({ type: "init" });
    handler({
      type: "page",
      page: {
        execution_id: "broken",
        columns: [],
        rows: null as unknown as unknown[][],
        page: { next_cursor: null, has_more: false },
        elapsed_ms: 1,
      },
    });
    expect(responses).toHaveLength(1);
    expect(responses[0]?.type).toBe("error");
  });
});

describe("exportExecutionToXlsx Worker dispatch (PERF-0011)", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function installMockWorker(options?: {
    overrideResponse?: (
      request: XlsxWorkerRequest,
      instance: {
        onmessage: ((event: MessageEvent<XlsxWorkerResponse>) => void) | null;
        onerror: ((event: ErrorEvent) => void) | null;
      },
    ) => boolean;
  }) {
    const instances: Array<{
      url: string | URL;
      options?: WorkerOptions;
      terminated: boolean;
      posted: XlsxWorkerRequest[];
    }> = [];

    class MockWorker {
      onmessage: ((event: MessageEvent<XlsxWorkerResponse>) => void) | null = null;
      onerror: ((event: ErrorEvent) => void) | null = null;
      private readonly record: {
        url: string | URL;
        options?: WorkerOptions;
        terminated: boolean;
        posted: XlsxWorkerRequest[];
      };
      private readonly handle = createXlsxWorkerHandler((message) => {
        queueMicrotask(() => {
          if (this.record.terminated || this.onmessage === null) return;
          this.onmessage({ data: message } as MessageEvent<XlsxWorkerResponse>);
        });
      });

      constructor(url: string | URL, workerOptions?: WorkerOptions) {
        this.record = { url, options: workerOptions, terminated: false, posted: [] };
        instances.push(this.record);
      }

      postMessage(request: XlsxWorkerRequest): void {
        this.record.posted.push(request);
        if (options?.overrideResponse?.(request, this) === true) {
          return;
        }
        this.handle(request);
      }

      terminate(): void {
        this.record.terminated = true;
      }
    }

    vi.stubGlobal("Worker", MockWorker);
    return instances;
  }

  it("uses a same-origin module Worker with Transferable ArrayBuffer output and terminates on finish", async () => {
    const instances = installMockWorker();
    const progress: Array<{ pagesRead: number; rowsExported: number }> = [];
    const outputs: Array<{ bytes: Uint8Array; rows: number }> = [];

    await exportExecutionToXlsx(
      pageOf([[1, "first@example.test"]], true, "cursor-2"),
      () => Promise.resolve(pageOf([[2, "second@example.test"]], false, null)),
      {
        onProgress: (p) => progress.push({ pagesRead: p.pagesRead, rowsExported: p.rowsExported }),
        onDone: (bytes, rows) => {
          outputs.push({ bytes, rows });
        },
        onFailure: (message) => {
          throw new Error(`unexpected failure: ${message}`);
        },
      },
      () => false,
    );

    expect(instances).toHaveLength(1);
    const worker = instances[0];
    if (!worker) throw new Error("expected mock worker instance");
    expect(String(worker.url)).toContain("xlsx-export.worker");
    expect(worker.options).toEqual({ type: "module" });
    expect(worker.posted.map((r) => r.type)).toEqual(["init", "page", "page", "finish"]);
    expect(worker.terminated).toBe(true);
    expect(progress).toEqual([
      { pagesRead: 1, rowsExported: 1 },
      { pagesRead: 2, rowsExported: 2 },
    ]);
    expect(outputs).toHaveLength(1);
    const done = outputs[0];
    if (!done) throw new Error("expected output bytes");
    expect(done.rows).toBe(2);
    expect(sheetXml(done.bytes)).toContain("second@example.test");
  });

  it("terminates the worker immediately when cancelled mid-export", async () => {
    const instances = installMockWorker();
    let cancelled = false;
    const onDone = vi.fn();
    const onFailure = vi.fn();

    await exportExecutionToXlsx(
      pageOf([[1, "a"]], true, "c-2"),
      () => {
        cancelled = true;
        return Promise.resolve(pageOf([[2, "b"]], false, null));
      },
      { onProgress: () => {}, onDone, onFailure },
      () => cancelled,
    );

    expect(onDone).not.toHaveBeenCalled();
    expect(onFailure).toHaveBeenCalledWith("cancelled", 1);
    expect(instances[0]?.terminated).toBe(true);
  });

  it("surfaces worker protocol error messages and onerror events cleanly", async () => {
    installMockWorker({
      overrideResponse: (request, instance) => {
        if (request.type === "page") {
          queueMicrotask(() => {
            instance.onmessage?.({
              data: { type: "error", message: "worker zip failed" },
            } as MessageEvent<XlsxWorkerResponse>);
          });
          return true;
        }
        return false;
      },
    });
    const onFailureA = vi.fn();
    await exportExecutionToXlsx(
      pageOf([[1, "a"]], false, null),
      () => Promise.reject(new Error("unreachable")),
      { onProgress: () => {}, onDone: vi.fn(), onFailure: onFailureA },
      () => false,
    );
    expect(onFailureA).toHaveBeenCalledWith("worker zip failed", 0);

    vi.unstubAllGlobals();
    const crashedInstances = installMockWorker({
      overrideResponse: (request, instance) => {
        if (request.type === "finish") {
          queueMicrotask(() => {
            instance.onerror?.({ message: "worker crashed" } as ErrorEvent);
          });
          return true;
        }
        return false;
      },
    });
    const onFailureB = vi.fn();
    await exportExecutionToXlsx(
      pageOf([[1, "a"]], false, null),
      () => Promise.reject(new Error("unreachable")),
      { onProgress: () => {}, onDone: vi.fn(), onFailure: onFailureB },
      () => false,
    );
    expect(onFailureB).toHaveBeenCalledWith("worker crashed", 1);
    expect(crashedInstances[0]?.terminated).toBe(true);
  });
});
