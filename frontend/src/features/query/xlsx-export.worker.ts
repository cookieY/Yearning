import type { QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { createXlsxSheet, type XlsxSheetBuffer } from "./xlsx-sheet-core";

export type XlsxWorkerRequest =
  | { type: "init"; sheetName?: string }
  | { type: "page"; page: QueryResultPage }
  | { type: "finish" };

export type XlsxWorkerResponse =
  | { type: "pageAck"; rowCount: number }
  | { type: "done"; buffer: ArrayBuffer; rowCount: number }
  | { type: "error"; message: string };

export type XlsxWorkerPostMessage = (
  message: XlsxWorkerResponse,
  transfer?: Transferable[],
) => void;

export function createXlsxWorkerHandler(
  postMessage: XlsxWorkerPostMessage,
): (request: XlsxWorkerRequest) => void {
  let sheet: XlsxSheetBuffer | null = null;

  return (request: XlsxWorkerRequest) => {
    try {
      switch (request.type) {
        case "init": {
          sheet = createXlsxSheet(request.sheetName ?? "QueryResult");
          return;
        }
        case "page": {
          if (sheet === null) {
            postMessage({ type: "error", message: "XLSX export worker is not initialized" });
            return;
          }
          sheet.appendPage(request.page);
          postMessage({ type: "pageAck", rowCount: sheet.rowCount() });
          return;
        }
        case "finish": {
          if (sheet === null) {
            postMessage({ type: "error", message: "XLSX export worker is not initialized" });
            return;
          }
          const activeSheet = sheet;
          sheet = null;
          const bytes = activeSheet.finish();
          const rowCount = activeSheet.rowCount();
          const buffer =
            bytes.byteOffset === 0 && bytes.byteLength === bytes.buffer.byteLength
              ? (bytes.buffer as ArrayBuffer)
              : bytes.slice().buffer;
          postMessage({ type: "done", buffer, rowCount }, [buffer]);
          return;
        }
      }
    } catch (error) {
      sheet = null;
      postMessage({
        type: "error",
        message: error instanceof Error ? error.message : String(error),
      });
    }
  };
}

interface WorkerGlobalLike {
  onmessage: ((event: MessageEvent<XlsxWorkerRequest>) => void) | null;
  postMessage: XlsxWorkerPostMessage;
}

const isDedicatedWorkerScope =
  typeof self !== "undefined" &&
  typeof window === "undefined" &&
  typeof (self as { postMessage?: unknown }).postMessage === "function";

if (isDedicatedWorkerScope) {
  const workerGlobal = self as unknown as WorkerGlobalLike;
  const handleRequest = createXlsxWorkerHandler((message, transfer) => {
    workerGlobal.postMessage(message, transfer);
  });
  workerGlobal.onmessage = (event: MessageEvent<XlsxWorkerRequest>) => {
    handleRequest(event.data);
  };
}
