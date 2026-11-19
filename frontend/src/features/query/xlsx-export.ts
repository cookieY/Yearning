import { strToU8, zipSync } from "fflate";
import type { QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Browser-side incremental XLSX generation (query PRD §3/§7; Q006): the
 * server never persists result rows or export files, so the file is
 * assembled in the browser from cursor pages (`purpose=export`, every page
 * re-authorized server-side). Sheet XML is appended per page while rows
 * stream in; only the final zip step touches the whole buffer, and the
 * total in-memory footprint is bounded by the exported XML text (browser
 * resource exhaustion is reported honestly by the caller instead of being
 * disguised as success).
 */

/** Cells arrive as JSON values; numbers/booleans stringify losslessly and
 * anything else renders its JSON form (never "[object Object]"). */
function stringifyCell(value: unknown): string {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    return String(value);
  }
  return JSON.stringify(value);
}

function xmlEscape(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** Column letter for a 0-based index (A..Z, AA..). */
export function columnLetter(index: number): string {
  let letter = "";
  let remaining = index;
  while (remaining >= 0) {
    letter = String.fromCharCode((remaining % 26) + 65) + letter;
    remaining = Math.floor(remaining / 26) - 1;
  }
  return letter;
}

/** Numeric cell for parseable numbers, inline-string otherwise. */
function cellXml(value: unknown): string {
  if (value === null || value === undefined) return "";
  const text = stringifyCell(value);
  if (/^-?\d+(\.\d+)?$/.test(text) && text.length < 16) {
    return `<c t="n"><v>${text}</v></c>`;
  }
  return `<c t="inlineStr"><is><t xml:space="preserve">${xmlEscape(text)}</t></is></c>`;
}

export interface XlsxSheetBuffer {
  /** Appends one fetched page of rows. */
  appendPage(page: QueryResultPage): void;
  /** Serializes the workbook; returns the zip bytes. */
  finish(): Uint8Array;
  /** Data rows written (the header row is not an exported data row). */
  rowCount(): number;
}

function rowXml(values: unknown[], rowNumber: number): string {
  const cells = values
    .map((value, index) => {
      const reference = `${columnLetter(index)}${String(rowNumber)}`;
      const xml = cellXml(value);
      if (xml === "") return "";
      return xml.replace("<c ", `<c r="${reference}" `).replace("<c>", `<c r="${reference}">`);
    })
    .join("");
  return `<row r="${String(rowNumber)}">${cells}</row>`;
}

export function createXlsxSheet(sheetName = "QueryResult"): XlsxSheetBuffer {
  const rows: string[] = [];
  let count = 0;
  let headerWritten = false;
  let dataRows = 0;

  return {
    appendPage(page: QueryResultPage) {
      if (!headerWritten && page.columns.length > 0) {
        rows.push(rowXml(page.columns.map((column) => column.name), 1));
        headerWritten = true;
        count = 1;
      }
      for (const row of page.rows) {
        count += 1;
        dataRows += 1;
        rows.push(rowXml(row, count));
      }
    },
    rowCount() {
      return dataRows;
    },
    finish() {
      const sheetData = rows.join("");
      const sheetXml = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>${sheetData}</sheetData></worksheet>`;
      const escapedName = xmlEscape(sheetName);
      return zipSync(
        {
          "[Content_Types].xml": strToU8(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`),
          "_rels/.rels": strToU8(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`),
          "xl/workbook.xml": strToU8(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="${escapedName}" sheetId="1" r:id="rId1"/></sheets></workbook>`),
          "xl/_rels/workbook.xml.rels": strToU8(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`),
          "xl/worksheets/sheet1.xml": strToU8(sheetXml),
        },
        { level: 6 },
      );
    },
  };
}

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
  const sheet = createXlsxSheet();
  let pagesRead = 0;
  let rowsExported = 0;
  try {
    let page = firstPage;
    let cursor: string | null = null;
    const pageInfo = (page as { page?: { next_cursor?: string | null } }).page;
    cursor = pageInfo?.next_cursor ?? null;
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
      sheet.appendPage(page);
      pagesRead += 1;
      rowsExported = sheet.rowCount();
      callbacks.onProgress({ pagesRead, rowsExported, finished: false, failure: null });
      cursor = ((page as { page?: { next_cursor?: string | null } }).page?.next_cursor as string | null) ?? null;
    }
    const bytes = sheet.finish();
    callbacks.onDone(bytes, rowsExported);
  } catch (error) {
    callbacks.onFailure(error instanceof Error ? error.message : String(error), rowsExported);
  }
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
