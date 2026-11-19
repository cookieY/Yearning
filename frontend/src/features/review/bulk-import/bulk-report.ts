import type { SqlDigest } from "@/features/review/bulk-import/sql-digest";

/**
 * Builds the downloadable batch report (frontend PRD F5 item 5: provide full report
 * download without loading all content into the DOM). The report carries the digest summary and
 * per-statement metadata — never SQL text, comments or literal values — so it
 * cannot become a second copy of the source and cannot leak sensitive
 * content. Rows are assembled as bounded chunks handed straight to a Blob to
 * avoid materializing one huge string in the JS heap.
 */

export interface ReportFacts {
  /** Authoritative server numbers when a review run exists. */
  readonly serverStatementCount?: number;
  readonly serverGroupCount?: number;
  readonly draftTitle?: string;
}

const REPORT_ROW_CHUNK = 2048;

function csvCell(value: string | number | boolean): string {
  const text = String(value);
  if (/[",\n]/.test(text)) return `"${text.replaceAll('"', '""')}"`;
  return text;
}

export function buildBulkReportChunks(
  digest: SqlDigest,
  facts: ReportFacts = {},
): string[] {
  const header: string[] = [
    "# Yearning bulk report",
    `generated_at,${new Date().toISOString()}`,
    `draft_title,${csvCell(facts.draftTitle ?? "")}`,
    `size_bytes,${String(digest.sizeBytes)}`,
    `statement_count,${String(digest.statementCount)}`,
    `local_group_count,${String(digest.groupCount)}`,
    `server_statement_count,${facts.serverStatementCount === undefined ? "" : String(facts.serverStatementCount)}`,
    `server_fingerprint_group_count,${facts.serverGroupCount === undefined ? "" : String(facts.serverGroupCount)}`,
    `table_count,${String(digest.tableCount)}`,
    `statement_coverage,${digest.coverageRatio.toFixed(4)}`,
    `max_statement_bytes,${String(digest.maxStatementBytes)}`,
    `anomaly_count,${String(digest.anomalyCount)}`,
    `truncated,${String(digest.truncated)}`,
    `tables,${digest.tables.map((table) => csvCell(table)).join(" ")}`,
    "",
    "index,kind,table,bytes,group,has_where,oversized,terminated,anomaly",
  ];
  const chunks: string[] = [header.join("\n")];
  let buffer: string[] = [];
  const flush = (): void => {
    if (buffer.length > 0) {
      chunks.push(buffer.join("\n"));
      buffer = [];
    }
  };
  for (const statement of digest.statements) {
    buffer.push([
      String(statement.index),
      statement.kind,
      csvCell(statement.table ?? ""),
      String(statement.bytes),
      String(statement.group),
      String(statement.hasWhere),
      String(statement.oversized),
      String(statement.terminated),
      String(statement.anomaly),
    ].map(csvCell).join(","));
    if (buffer.length >= REPORT_ROW_CHUNK) flush();
  }
  flush();
  return chunks;
}

/** Creates the report Blob; the caller owns the object URL lifecycle. */
export function buildBulkReportBlob(digest: SqlDigest, facts: ReportFacts = {}): Blob {
  return new Blob(buildBulkReportChunks(digest, facts), { type: "text/csv" });
}
