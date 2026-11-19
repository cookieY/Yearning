import { describe, expect, it } from "vitest";
import {
  buildResultTabLabels,
  extractQueryTarget,
  resolveStatementSchema,
  splitSqlStatements,
} from "./query-sql-utils";

describe("query-sql-utils", () => {
  it("extracts schema and table from qualified, quoted, CTE, and subquery SELECT statements", () => {
    expect(extractQueryTarget("SELECT * FROM demo.accounts WHERE id = 1")).toEqual({
      schema: "demo",
      table: "accounts",
    });
    expect(extractQueryTarget('SELECT * FROM "demo"."accounts" LIMIT 10')).toEqual({
      schema: "demo",
      table: "accounts",
    });
    expect(extractQueryTarget("SELECT * FROM `app`.`orders` WHERE status = 'from fake.tbl'")).toEqual({
      schema: "app",
      table: "orders",
    });
    expect(extractQueryTarget("SELECT * FROM accounts")).toEqual({
      schema: null,
      table: "accounts",
    });
    expect(
      extractQueryTarget(
        "WITH recent AS (SELECT * FROM audit.logs) SELECT * FROM demo.accounts WHERE balance > 0",
      ),
    ).toEqual({
      schema: "demo",
      table: "accounts",
    });
    expect(
      extractQueryTarget(
        "SELECT (SELECT count(*) FROM public.other) AS c, id FROM demo.accounts",
      ),
    ).toEqual({
      schema: "demo",
      table: "accounts",
    });
    expect(extractQueryTarget("SELECT 1")).toEqual({
      schema: null,
      table: null,
    });
  });

  it("resolves statement schema from dropdown first and falls back to explicit schema.table", () => {
    expect(resolveStatementSchema("SELECT * FROM demo.accounts", "")).toBe("demo");
    expect(resolveStatementSchema("SELECT * FROM demo.accounts", "public")).toBe("public");
    expect(resolveStatementSchema("SELECT * FROM accounts", "")).toBe("");
    expect(resolveStatementSchema("SELECT * FROM accounts", "demo")).toBe("demo");
  });

  it("formats result tab labels as schema.table and appends sequential counters for duplicates", () => {
    const labels = buildResultTabLabels(
      [
        { sql: "SELECT * FROM demo.accounts LIMIT 10", schemaName: "" },
        { sql: "SELECT * FROM demo.accounts WHERE id = 2", schemaName: "" },
        { sql: "SELECT * FROM public.users", schemaName: "" },
        { sql: "SELECT * FROM accounts", schemaName: "demo" },
        { sql: "SELECT 1", schemaName: "demo" },
      ],
      (index) => `Result ${String(index + 1)}`,
    );
    expect(labels).toEqual([
      "demo.accounts",
      "demo.accounts 2",
      "public.users",
      "demo.accounts 3",
      "Result 5",
    ]);
  });

  it("splits semicolon-delimited statements while ignoring semicolons inside strings and comments", () => {
    expect(
      splitSqlStatements(
        "SELECT ';' FROM demo.accounts; -- comment ;\nSELECT * FROM demo.orders;",
      ),
    ).toEqual([
      "SELECT ';' FROM demo.accounts",
      "-- comment ;\nSELECT * FROM demo.orders",
    ]);
  });
});
