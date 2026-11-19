import { describe, expect, it } from "vitest";
import { buildBulkReportChunks } from "@/features/review/bulk-import/bulk-report";
import { digestSqlText } from "@/features/review/bulk-import/sql-digest";

/**
 * The batch report is metadata-only: per-statement rows carry index/kind/
 * table/size/group — never SQL text, comments or literal values, so the
 * download cannot become a second copy of the source (acceptance gate:
 * never duplicate SQL copies) and cannot leak sensitive content.
 */

describe("buildBulkReportChunks", () => {
  it("writes a summary header followed by one metadata row per statement", () => {
    const digest = digestSqlText(
      "INSERT INTO orders VALUES (1, 'new');\nUPDATE orders SET status = 'processed';\n",
    );
    const chunks = buildBulkReportChunks(digest, {
      serverStatementCount: 2,
      serverGroupCount: 2,
      draftTitle: "Bulk update",
    });
    const text = chunks.join("\n");
    expect(text).toContain("statement_count,2");
    expect(text).toContain("local_group_count,2");
    expect(text).toContain("server_fingerprint_group_count,2");
    expect(text).toContain("draft_title,Bulk update");
    expect(text).toContain("1,INSERT,orders,");
    expect(text).toContain("2,UPDATE,orders,");
    expect(text).toContain("anomaly_count,1");
  });

  it("never contains SQL text or literals from the source", () => {
    const syntheticMarker = "SECRET-LITERAL-9f3a";
    const digest = digestSqlText(`INSERT INTO t VALUES ('${syntheticMarker}', 1);\nSELECT 1;\n`);
    const text = buildBulkReportChunks(digest).join("\n");
    expect(text).not.toContain(syntheticMarker);
    expect(text).not.toContain("INSERT INTO");
  });

  it("chunks rows instead of materializing one huge string", () => {
    const sql = Array.from(
      { length: 10000 },
      (_, i) => `INSERT INTO t VALUES (${String(i)});\n`,
    ).join("");
    const digest = digestSqlText(sql);
    const chunks = buildBulkReportChunks(digest);
    // 10,000 rows at 4,096 per chunk → several bounded chunks.
    expect(chunks.length).toBeGreaterThanOrEqual(4);
    for (const chunk of chunks) {
      expect(chunk.length).toBeLessThan(120000);
    }
  });
});
