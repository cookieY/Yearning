import { Zip, ZipDeflate, strToU8 } from "fflate";
import type { QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Streaming OOXML XLSX sheet serializer (`fflate.Zip` + `ZipDeflate`).
 * Each `appendPage` call serializes only that page's rows and feeds UTF-8
 * bytes directly into the DEFLATE stream so the full uncompressed worksheet XML
 * string never accumulates in memory.
 */

const SHEET_XML_HEADER =
  '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' +
  '<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>';
const SHEET_XML_FOOTER = "</sheetData></worksheet>";

const CONTENT_TYPES_XML =
  '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' +
  '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">' +
  '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>' +
  '<Default Extension="xml" ContentType="application/xml"/>' +
  '<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>' +
  '<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>' +
  "</Types>";

const ROOT_RELS_XML =
  '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' +
  '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">' +
  '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>' +
  "</Relationships>";

const WORKBOOK_RELS_XML =
  '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' +
  '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">' +
  '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>' +
  "</Relationships>";

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

function addDeflatedStaticPart(zip: Zip, filename: string, content: string): void {
  const part = new ZipDeflate(filename, { level: 6 });
  zip.add(part);
  part.push(strToU8(content), true);
}

export function createXlsxSheet(sheetName = "QueryResult"): XlsxSheetBuffer {
  const compressedChunks: Uint8Array[] = [];
  let compressedBytes = 0;
  let zipError: Error | null = null;

  const zip = new Zip((err, data) => {
    if (err !== null) {
      zipError = err instanceof Error ? err : new Error("zip compression error");
      return;
    }
    if (data.length > 0) {
      compressedChunks.push(data);
      compressedBytes += data.length;
    }
  });

  const sheetStream = new ZipDeflate("xl/worksheets/sheet1.xml", { level: 6 });
  zip.add(sheetStream);
  sheetStream.push(strToU8(SHEET_XML_HEADER), false);

  let count = 0;
  let headerWritten = false;
  let dataRows = 0;
  let finished = false;

  const throwIfZipErrored = (): void => {
    if (zipError !== null) {
      throw zipError;
    }
  };

  return {
    appendPage(page: QueryResultPage) {
      if (finished) {
        throw new Error("xlsx sheet already finished");
      }
      throwIfZipErrored();
      const pageRows: string[] = [];
      if (!headerWritten && page.columns.length > 0) {
        pageRows.push(rowXml(page.columns.map((column) => column.name), 1));
        headerWritten = true;
        count = 1;
      }
      for (const row of page.rows) {
        count += 1;
        dataRows += 1;
        pageRows.push(rowXml(row, count));
      }
      if (pageRows.length > 0) {
        sheetStream.push(strToU8(pageRows.join("")), false);
        throwIfZipErrored();
      }
    },
    rowCount() {
      return dataRows;
    },
    finish() {
      if (finished) {
        throw new Error("xlsx sheet already finished");
      }
      finished = true;
      throwIfZipErrored();
      sheetStream.push(strToU8(SHEET_XML_FOOTER), true);
      throwIfZipErrored();

      const escapedName = xmlEscape(sheetName);
      const workbookXml =
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' +
        '<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ' +
        'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">' +
        `<sheets><sheet name="${escapedName}" sheetId="1" r:id="rId1"/></sheets></workbook>`;

      addDeflatedStaticPart(zip, "[Content_Types].xml", CONTENT_TYPES_XML);
      addDeflatedStaticPart(zip, "_rels/.rels", ROOT_RELS_XML);
      addDeflatedStaticPart(zip, "xl/workbook.xml", workbookXml);
      addDeflatedStaticPart(zip, "xl/_rels/workbook.xml.rels", WORKBOOK_RELS_XML);
      zip.end();
      throwIfZipErrored();

      const output = new Uint8Array(compressedBytes);
      let offset = 0;
      for (const chunk of compressedChunks) {
        output.set(chunk, offset);
        offset += chunk.length;
      }
      compressedChunks.length = 0;
      return output;
    },
  };
}
