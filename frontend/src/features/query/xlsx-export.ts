import type { QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { nextCursorOf } from "./use-query-domain";
import { columnLetter, createXlsxSheet, type XlsxSheetBuffer } from "./xlsx-sheet-core";
import type { XlsxWorkerRequest, XlsxWorkerResponse } from "./xlsx-export.worker";

export { columnLetter, createXlsxSheet, type XlsxSheetBuffer };

/**
 * Browser-side incremental XLSX generation (query PRD §3/§7; Q006): the
 * server never persists result rows or export files, so the file is
 * assembled in the browser from cursor pages (`purpose=export`, every page
 * re-authorized server-side). In browser environments (`typeof Worker !== "undefined"`),
 * XML encoding and streaming `fflate.Zip`/`ZipDeflate` compression run in a
 * same-origin module Web Worker so large exports keep the main UI thread
 * responsive while respecting `worker-src 'self'` CSP.
 */

export interface ExportProgress {
  pagesRead: number;
  rowsExported: number;
  finished: boolean;
  failure: string | null;
}

export interface ExportControllerCallbacks {
  onProgress: (progress: ExportProgress) => void;
  /** Receives the finished zip bytes. */
  onDone: (bytes: Uint8Array, rowsExported: number) => void;
  /** Honest failure: message plus the range that WAS exported before the
   * failure so the UI never claims a complete file. */
  onFailure: (message: string, exportedRows: number) => void;
}

async function runMainThreadFallbackExport(
  firstPage: QueryResultPage,
  fetchExportPage: (cursor: string) => Promise<QueryResultPage>,
  callbacks: ExportControllerCallbacks,
  isCancelled: () => boolean,
): Promise<void> {
  const sheet = createXlsxSheet();
  let pagesRead = 0;
  let rowsExported = 0;
  try {
    let page = firstPage;
    let cursor = nextCursorOf(page);
    sheet.appendPage(page);
    pagesRead += 1;
    rowsExported = sheet.rowCount();
    callbacks.onProgress({ pagesRead, rowsExported, finished: false, failure: null });
    while (cursor !== null) {
      if (isCancelled()) {
        callbacks.onFailure("cancelled", rowsExported);
        return;
      }
      page = await fetchExportPage(cursor);
      if (isCancelled()) {
        callbacks.onFailure("cancelled", rowsExported);
        return;
      }
      sheet.appendPage(page);
      pagesRead += 1;
      rowsExported = sheet.rowCount();
      callbacks.onProgress({ pagesRead, rowsExported, finished: false, failure: null });
      cursor = nextCursorOf(page);
    }
    if (isCancelled()) {
      callbacks.onFailure("cancelled", rowsExported);
      return;
    }
    const bytes = sheet.finish();
    callbacks.onDone(bytes, rowsExported);
  } catch (error) {
    callbacks.onFailure(error instanceof Error ? error.message : String(error), rowsExported);
  }
}

async function runWorkerExport(
  firstPage: QueryResultPage,
  fetchExportPage: (cursor: string) => Promise<QueryResultPage>,
  callbacks: ExportControllerCallbacks,
  isCancelled: () => boolean,
): Promise<void> {
  let worker: Worker | null = null;
  let pagesRead = 0;
  let rowsExported = 0;

  let pendingResolve: ((response: XlsxWorkerResponse) => void) | null = null;
  let pendingReject: ((reason: Error) => void) | null = null;

  const clearPending = (): void => {
    pendingResolve = null;
    pendingReject = null;
  };

  try {
    worker = new Worker(new URL("./xlsx-export.worker.ts", import.meta.url), {
      type: "module",
    });

    worker.onmessage = (event: MessageEvent<XlsxWorkerResponse>) => {
      const response = event.data;
      if (response.type === "error") {
        const reject = pendingReject;
        clearPending();
        reject?.(new Error(response.message));
        return;
      }
      const resolve = pendingResolve;
      clearPending();
      resolve?.(response);
    };

    worker.onerror = (event: ErrorEvent) => {
      const reject = pendingReject;
      clearPending();
      reject?.(new Error(event.message || "XLSX export worker error"));
    };

    const activeWorker = worker;
    const postAndAwait = (request: XlsxWorkerRequest): Promise<XlsxWorkerResponse> =>
      new Promise<XlsxWorkerResponse>((resolve, reject) => {
        pendingResolve = resolve;
        pendingReject = reject;
        activeWorker.postMessage(request);
      });

    const sendPage = async (page: QueryResultPage): Promise<number> => {
      const response = await postAndAwait({ type: "page", page });
      if (response.type !== "pageAck") {
        throw new Error(`unexpected worker response: ${response.type}`);
      }
      return response.rowCount;
    };

    const sendFinish = async (): Promise<{ buffer: ArrayBuffer; rowCount: number }> => {
      const response = await postAndAwait({ type: "finish" });
      if (response.type !== "done") {
        throw new Error(`unexpected worker response: ${response.type}`);
      }
      return { buffer: response.buffer, rowCount: response.rowCount };
    };

    activeWorker.postMessage({ type: "init", sheetName: "QueryResult" } satisfies XlsxWorkerRequest);

    let cursor = nextCursorOf(firstPage);
    rowsExported = await sendPage(firstPage);
    pagesRead += 1;
    callbacks.onProgress({ pagesRead, rowsExported, finished: false, failure: null });

    while (cursor !== null) {
      if (isCancelled()) {
        callbacks.onFailure("cancelled", rowsExported);
        return;
      }
      const nextPage = await fetchExportPage(cursor);
      if (isCancelled()) {
        callbacks.onFailure("cancelled", rowsExported);
        return;
      }
      rowsExported = await sendPage(nextPage);
      if (isCancelled()) {
        callbacks.onFailure("cancelled", rowsExported);
        return;
      }
      pagesRead += 1;
      callbacks.onProgress({ pagesRead, rowsExported, finished: false, failure: null });
      cursor = nextCursorOf(nextPage);
    }

    if (isCancelled()) {
      callbacks.onFailure("cancelled", rowsExported);
      return;
    }

    const finished = await sendFinish();
    if (isCancelled()) {
      callbacks.onFailure("cancelled", rowsExported);
      return;
    }
    callbacks.onDone(new Uint8Array(finished.buffer), finished.rowCount);
  } catch (error) {
    callbacks.onFailure(error instanceof Error ? error.message : String(error), rowsExported);
  } finally {
    clearPending();
    if (worker !== null) {
      worker.onmessage = null;
      worker.onerror = null;
      worker.terminate();
    }
  }
}

/**
 * Drives the export loop: starts from the first page of an execution and
 * follows `purpose=export` cursors until exhaustion, cancellation, or an
 * authorization/transport failure. Each page read re-validates the frozen
 * can_export server-side (4003) — a mid-export revocation fails the export
 * instead of producing a truncated file.
 */
export async function exportExecutionToXlsx(
  firstPage: QueryResultPage,
  fetchExportPage: (cursor: string) => Promise<QueryResultPage>,
  callbacks: ExportControllerCallbacks,
  isCancelled: () => boolean,
): Promise<void> {
  if (typeof Worker !== "undefined") {
    await runWorkerExport(firstPage, fetchExportPage, callbacks, isCancelled);
    return;
  }
  await runMainThreadFallbackExport(firstPage, fetchExportPage, callbacks, isCancelled);
}

export function downloadXlsx(bytes: Uint8Array, filename: string): void {
  const blob = new Blob([bytes as BlobPart], {
    type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  // Revoking synchronously can race the download start (Safari); yield to
  // the task queue before releasing the object URL.
  window.setTimeout(() => { URL.revokeObjectURL(url); }, 0);
}
