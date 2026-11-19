import { describe, expect, it } from "vitest";
import {
  digestSqlFile,
  digestSqlText,
  SqlDigestScanner,
} from "@/features/review/bulk-import/sql-digest";
import { FINGERPRINT_MAX_STATEMENT_BYTES } from "@/features/review/bulk-constants";

/** Feeds text in fixed-size character chunks to simulate streaming. */
function digestInChunks(sql: string, chunkChars: number) {
  const scanner = new SqlDigestScanner();
  for (let i = 0; i < sql.length; i += chunkChars) {
    scanner.push(sql.slice(i, i + chunkChars));
  }
  return scanner.finish();
}

describe("sql-digest", () => {
  it("splits simple statements and reports counts, sizes and coverage", () => {
    const digest = digestSqlText("UPDATE t SET a = 1 WHERE id = 2;\nINSERT INTO t VALUES (3);\n");
    expect(digest.statementCount).toBe(2);
    expect(digest.groupCount).toBe(2);
    expect(digest.coverageRatio).toBe(1);
    expect(digest.truncated).toBe(false);
    expect(digest.statements[0]?.kind).toBe("UPDATE");
    expect(digest.statements[0]?.hasWhere).toBe(true);
    expect(digest.statements[1]?.kind).toBe("INSERT");
  });

  it("produces an identical digest regardless of chunk boundaries", () => {
    const sql =
      "INSERT INTO `orders` (`id`,`status`) VALUES (1,'new');\n" +
      "-- a comment with ; a semicolon\n" +
      "UPDATE orders SET note = 'semi;colon' /* block ; comment */ WHERE id = 7;\n" +
      "# hash comment ;\n" +
      "DELETE FROM t WHERE id = 9;\n";
    const whole = digestSqlText(sql);
    for (const chunk of [1, 3, 7, 64, 1024]) {
      const chunked = digestInChunks(sql, chunk);
      expect(chunked.statementCount).toBe(whole.statementCount);
      expect(chunked.groupCount).toBe(whole.groupCount);
      expect(chunked.statements.map((s) => s.shapeKey)).toEqual(
        whole.statements.map((s) => s.shapeKey),
      );
      expect(chunked.statements.map((s) => s.bytes)).toEqual(whole.statements.map((s) => s.bytes));
      expect(chunked.statements.map((s) => s.startByte)).toEqual(
        whole.statements.map((s) => s.startByte),
      );
    }
  });

  it("never splits inside strings, quoted identifiers or comments", () => {
    const digest = digestSqlText(
      "INSERT INTO messages VALUES ('hello; world');\nINSERT INTO `weird;name` VALUES (1);\n",
    );
    expect(digest.statementCount).toBe(2);
    expect(digest.statements[0]?.table).toBe("messages");
    expect(digest.statements[1]?.table).toBe("weird;name");
  });

  it("treats # and -- line comments and /* block */ comments as non-structural", () => {
    const digest = digestSqlText(
      "-- lead ; comment\nSELECT 1; /* inline ; */ SELECT 2; # trail ;\nSELECT 3;\n",
    );
    expect(digest.statementCount).toBe(3);
  });

  it("extracts target tables for the main DML/DDL kinds", () => {
    const digest = digestSqlText(
      [
        "INSERT INTO db.orders (id) VALUES (1);",
        "UPDATE products SET price = 1 WHERE id = 2;",
        "DELETE FROM logs WHERE id = 3;",
        "ALTER TABLE orders ADD COLUMN c INT;",
        "CREATE TABLE IF NOT EXISTS tmp_a (id INT);",
        "DROP TABLE old_things;",
        "TRUNCATE TABLE staging;",
        "REPLACE INTO stock VALUES (1);",
      ].join("\n"),
    );
    const byKind = new Map(digest.statements.map((s) => [s.kind, s.table]));
    expect(byKind.get("INSERT")).toBe("db.orders");
    expect(byKind.get("UPDATE")).toBe("products");
    expect(byKind.get("DELETE")).toBe("logs");
    expect(byKind.get("ALTER")).toBe("orders");
    expect(byKind.get("CREATE")).toBe("tmp_a");
    expect(byKind.get("DROP")).toBe("old_things");
    expect(byKind.get("TRUNCATE")).toBe("staging");
    expect(byKind.get("REPLACE")).toBe("stock");
    expect(digest.tableCount).toBe(8);
  });

  it("flags UPDATE/DELETE without WHERE and oversized statements as anomalies", () => {
    const digest = digestSqlText(
      "UPDATE orders SET status = 'processed';\nDELETE FROM logs;\nUPDATE t SET a = 1 WHERE id = 1;\n",
    );
    expect(digest.statements[0]?.anomaly).toBe(true);
    expect(digest.statements[0]?.hasWhere).toBe(false);
    expect(digest.statements[1]?.anomaly).toBe(true);
    expect(digest.statements[2]?.anomaly).toBe(false);
    expect(digest.anomalyCount).toBe(2);
    expect(digest.groups.find((g) => g.ordinal === digest.statements[0]?.group)?.anomalyCount).toBe(1);
  });

  it("detects WHERE beyond the 240-char shape key truncation", () => {
    const longSet = `UPDATE t SET ${Array.from(
      { length: 120 },
      (_, i) => `col_${String(i)} = ${String(i)}`,
    ).join(", ")} WHERE id = 1;`;
    const digest = digestSqlText(longSet);
    expect(digest.statements[0]?.hasWhere).toBe(true);
    expect(digest.statements[0]?.anomaly).toBe(false);
  });

  it("never treats WHERE inside identifiers or string literals as the keyword", () => {
    const digest = digestSqlText(
      "UPDATE somewhere_table SET note = 'please WHERE by hand' WHERE id = 1;\n" +
        "UPDATE WHEREX SET a = 1 WHERE id = 2;\n" +
        "DELETE FROM logs;\n",
    );
    expect(digest.statements[0]?.hasWhere).toBe(true);
    expect(digest.statements[1]?.hasWhere).toBe(true);
    expect(digest.statements[2]?.hasWhere).toBe(false);
    expect(digest.anomalyCount).toBe(1);
  });

  it("ends the keyword run at punctuation and quote boundaries", () => {
    const digest = digestSqlText(
      "UPDATE t SET a=1 WHERE(id=1);\n" +
        "UPDATE t SET a=1 WHERE((a=1));\n" +
        "UPDATE t SET a='b'WHERE id=1;\n" +
        "DELETE FROM logs;\n",
    );
    expect(digest.statements[0]?.hasWhere).toBe(true);
    expect(digest.statements[1]?.hasWhere).toBe(true);
    expect(digest.statements[2]?.hasWhere).toBe(true);
    expect(digest.statements[3]?.hasWhere).toBe(false);
    expect(digest.anomalyCount).toBe(1);
  });

  it("marks statements above the single-statement byte limit as oversized", () => {
    const big = `UPDATE t SET payload = '${"x".repeat(FINGERPRINT_MAX_STATEMENT_BYTES)}' WHERE id = ${String(1)};`;
    const digest = digestSqlText(big);
    expect(digest.statements[0]?.oversized).toBe(true);
    expect(digest.statements[0]?.anomaly).toBe(true);
    expect(digest.maxStatementBytes).toBeGreaterThan(FINGERPRINT_MAX_STATEMENT_BYTES);
  });

  it("groups high-similarity DML into one shape and keeps the odd statement separate", () => {
    const parts: string[] = [];
    for (let i = 1; i <= 999; i += 1) {
      parts.push(`UPDATE orders SET status = 'processed', updated_at = '2026-08-25' WHERE id = ${String(i)};\n`);
    }
    parts.push("UPDATE orders SET status = 'processed';\n");
    const digest = digestSqlText(parts.join(""));
    expect(digest.statementCount).toBe(1000);
    expect(digest.groupCount).toBe(2);
    expect(digest.groups[0]?.count).toBe(999);
    expect(digest.groups[1]?.count).toBe(1);
    expect(digest.groups[1]?.anomalyCount).toBe(1);
  });

  it("collapses literals and numbers in shape keys and bounds their length", () => {
    const a = digestSqlText("INSERT INTO t VALUES ('alpha', 123, 4.5);").statements[0]?.shapeKey;
    const b = digestSqlText("INSERT INTO t VALUES ('beta', 999, 7.25);").statements[0]?.shapeKey;
    expect(a).toBe(b);
    expect(a).not.toContain("alpha");
    const long = digestSqlText(`SELECT ${"col".repeat(500)} FROM t;`).statements[0]?.shapeKey;
    expect((long ?? "").length).toBeLessThanOrEqual(260);
  });

  it("reports unterminated trailing statements and keeps coverage below 1", () => {
    const digest = digestSqlText("SELECT 1;\nSELECT 2");
    expect(digest.statementCount).toBe(2);
    expect(digest.truncated).toBe(true);
    expect(digest.coverageRatio).toBe(0.5);
  });

  it("retains no statement text: metadata only", () => {
    const marker = "SECRET-LITERAL-VALUE-9e7b";
    const digest = digestSqlText(`INSERT INTO t VALUES ('${marker}');`);
    const serialized = JSON.stringify(digest);
    expect(serialized).not.toContain(marker);
  });

  it("digests a File incrementally and matches the whole-text digest", async () => {
    const sql = Array.from(
      { length: 500 },
      (_, i) => `INSERT INTO orders VALUES (${String(i)}, 'status-${String(i % 3)}');\n`,
    ).join("");
    const file = new File([sql], "bulk.sql", { type: "text/plain" });
    const progress: number[] = [];
    const result = await digestSqlFile(file, {
      chunkBytes: 512,
      onProgress: (p) => { progress.push(p.bytes); },
    });
    expect(result.text).toBe(sql);
    expect(result.digest.statementCount).toBe(500);
    // Literal collapse: all 500 statements share one normalized shape — the
    // exact property that makes high-similarity DML group into few buckets.
    expect(result.digest.groupCount).toBe(1);
    expect(progress[progress.length - 1]).toBe(file.size);
    const whole = digestSqlText(sql);
    expect(result.digest.groupCount).toBe(whole.groupCount);
    expect(result.digest.statements.map((s) => s.startByte)).toEqual(
      whole.statements.map((s) => s.startByte),
    );
  });

  it("keeps multi-byte characters intact across chunk boundaries", async () => {
    const sql = "INSERT INTO t VALUES ('\u72b6\u6001\u66f4\u65b0——\u4e2d\u6587;\u542b\u5206\u53f7');\nINSERT INTO t VALUES ('\u4e8c');\n";
    const file = new File([sql], "zh.sql");
    const result = await digestSqlFile(file, { chunkBytes: 7 });
    expect(result.digest.statementCount).toBe(2);
    expect(result.text).toBe(sql);
    expect(result.digest.statements[0]?.table).toBe("t");
  });

  it("aborts incremental reading between chunks", async () => {
    const sql = "SELECT 1;".repeat(5000);
    const file = new File([sql], "bulk.sql");
    const controller = new AbortController();
    const pending = digestSqlFile(file, { chunkBytes: 256, signal: controller.signal });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });

  it("charges UTF-8 bytes for surrogate-pair code points exactly once", () => {
    // Same UTF-16 length, different byte widths: 😀 is one code point as a
    // surrogate pair (4 UTF-8 bytes), "ab" is two code points (2 bytes).
    // The difference isolates surrogate-pair accounting from the terminator
    // policy (bytes/chars cover statement text without the `;`).
    const emoji = digestSqlText("INSERT INTO t VALUES ('😀');");
    const ascii = digestSqlText("INSERT INTO t VALUES ('ab');");
    expect(emoji.statements[0]?.bytes).toBe((ascii.statements[0]?.bytes ?? 0) + 2);
    expect(emoji.statements[0]?.chars).toBe(ascii.statements[0]?.chars);
    // And the emoji counts as four bytes, not eight (no double charge).
    expect(emoji.statements[0]?.bytes).toBe(
      new TextEncoder().encode("INSERT INTO t VALUES ('😀')").length,
    );
  });

  it("keeps an escaped quote inside a single-quoted string structural-free", () => {
    // MySQL backslash escape: the '; inside the literal must not split.
    const digest = digestSqlText(
      "INSERT INTO t VALUES ('it\\'s; fine');\nINSERT INTO t VALUES (2);\n",
    );
    expect(digest.statementCount).toBe(2);
    expect(digest.statements[1]?.kind).toBe("INSERT");
  });

  it("flags the replacement character as a decode error", () => {
    expect(digestSqlText("SELECT '�';").decodeError).toBe(true);
    expect(digestSqlText("SELECT 'ok';").decodeError).toBe(false);
  });

  it("ends a line comment on a carriage return", () => {
    const digest = digestSqlText("-- note\rSELECT 1;\rSELECT 2;\n");
    expect(digest.statementCount).toBe(2);
  });

  it("caps the displayed table list while keeping the true table count", () => {
    const parts: string[] = [];
    for (let i = 0; i < 205; i += 1) parts.push(`INSERT INTO tbl_${String(i)} VALUES (1);`);
    const digest = digestSqlText(parts.join("\n"));
    expect(digest.tableCount).toBe(205);
    expect(digest.tables.length).toBe(200);
  });
});
