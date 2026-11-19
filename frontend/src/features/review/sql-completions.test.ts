import { describe, expect, it } from "vitest";
import {
  buildSqlSuggestions,
  extractSqlMetadataReferences,
  type SqlCompletionCatalog,
} from "./sql-completions";

/**
 * The suggestion builder is pure: schema/table/column entries come first in
 * catalog order, the curated keyword list follows. Kinds stay plain strings
 * — the panel maps them onto Monaco CompletionItemKind values.
 */

const CATALOG: SqlCompletionCatalog = {
  schemas: ["app", "analytics"],
  tables: [
    { schema: "app", name: "orders" },
    { schema: null, name: "standalone" },
  ],
  columns: [{ schema: "app", table: "orders", name: "user_id" }],
};

describe("buildSqlSuggestions", () => {
  it("suggests schemas, tables and columns ahead of the keyword list", () => {
    const items = buildSqlSuggestions(CATALOG);
    // Catalog entries come first: schemas, then tables, then columns.
    expect(items[0]).toMatchObject({ label: "app", kind: "schema", detail: "schema" });
    expect(items[1]).toMatchObject({ label: "analytics", kind: "schema" });
    expect(items[2]).toMatchObject({
      label: "app.orders",
      insertText: "app.orders",
      kind: "table",
      detail: "table",
    });
    expect(items[3]).toMatchObject({ label: "standalone", kind: "table" });
    expect(items[4]).toMatchObject({
      label: "user_id",
      kind: "column",
      detail: "column · orders",
    });
    expect(items.length).toBe(5 + 54);
  });

  it("keeps the keyword list last without catalog entries", () => {
    const items = buildSqlSuggestions({ schemas: [], tables: [], columns: [] });
    expect(items.length).toBeGreaterThanOrEqual(50);
    expect(items.every((item) => item.kind === "keyword")).toBe(true);
    expect(items.map((item) => item.label)).toContain("SELECT");
  });

  it("narrows dot-qualified schema and table prefixes without duplicating the qualifier", () => {
    const schemaScoped = buildSqlSuggestions(CATALOG, { qualifier: "app" });
    expect(schemaScoped).toEqual([
      { label: "orders", insertText: "orders", detail: "table · app", kind: "table" },
    ]);

    const tableScoped = buildSqlSuggestions(CATALOG, { qualifier: "orders" });
    expect(tableScoped).toEqual([
      { label: "user_id", insertText: "user_id", detail: "column · orders", kind: "column" },
    ]);
  });

  it("resolves table aliases when completing <alias>. column qualifiers", () => {
    const aliasScoped = buildSqlSuggestions(CATALOG, {
      qualifier: "o",
      sql: "SELECT o. FROM app.orders AS o WHERE o.user_id = 1",
    });
    expect(aliasScoped).toEqual([
      { label: "user_id", insertText: "user_id", detail: "column · orders", kind: "column" },
    ]);
  });
});

describe("extractSqlMetadataReferences", () => {
  it("extracts schema qualifiers, FROM/JOIN tables, aliases, and ignores comments/strings", () => {
    const refs = extractSqlMetadataReferences(`
      -- FROM ignored_comment.tbl
      SELECT u.id, 'FROM fake.str'
      FROM app.users AS u
      LEFT JOIN analytics.orders o ON o.user_id = u.id
      WHERE reporting.
    `);
    expect(refs.schemas).toEqual(["app", "analytics"]);
    expect(refs.tables).toEqual([
      { schema: "app", table: "users", alias: "u" },
      { schema: "analytics", table: "orders", alias: "o" },
    ]);
    expect(refs.aliases).toEqual({
      u: { schema: "app", table: "users", alias: "u" },
      o: { schema: "analytics", table: "orders", alias: "o" },
    });
    expect(refs.qualifiers).toEqual(["u", "app", "analytics", "o", "reporting"]);
  });

  it("does not misclassify SQL keywords following a table name as aliases", () => {
    const refs = extractSqlMetadataReferences("SELECT * FROM users WHERE id = 1");
    expect(refs.tables).toEqual([{ schema: null, table: "users", alias: null }]);
    expect(refs.aliases).toEqual({});
  });
});
