import { SQL_COMPLETION_KEYWORDS } from "@/shared/lib/sql-highlight";

export interface SqlCompletionCatalog {
  schemas: string[];
  tables: Array<{ schema: string | null; name: string }>;
  columns: Array<{ schema: string | null; table: string; name: string }>;
}

/**
 * Pure suggestion builder and SQL metadata reference extractor for the SQL
 * completion provider (§18.40/§18.40 supplement). Kept free of the Monaco
 * namespace so unit tests exercise it directly without an editor instance.
 */

export const SQL_KEYWORDS = SQL_COMPLETION_KEYWORDS;

const SQL_KEYWORD_SET = new Set<string>([
  ...SQL_KEYWORDS,
  "CROSS",
  "FULL",
  "NATURAL",
  "USING",
  "FORCE",
  "USE",
  "IGNORE",
  "FOR",
  "LOCK",
  "WINDOW",
  "FETCH",
  "NEXT",
  "ROWS",
  "ONLY",
  "RETURNING",
  "WITH",
]);

export interface SqlTableReference {
  schema: string | null;
  table: string;
  alias: string | null;
}

export interface SqlMetadataReferences {
  /** Schemas explicitly qualified before a dot or in FROM/JOIN clauses. */
  schemas: string[];
  /** Explicit table references extracted from FROM / JOIN / UPDATE / INTO clauses. */
  tables: SqlTableReference[];
  /** Raw identifiers appearing immediately before a dot (schema, table, or alias). */
  qualifiers: string[];
  /** Lowercased alias -> table reference map for resolving `<alias>.` completions. */
  aliases: Record<string, SqlTableReference>;
}

/**
 * Extracts explicit schema prefixes, FROM/JOIN table references, aliases, and
 * `<qualifier>.` tokens from SQL text so the query workspace can lazily fetch
 * only the referenced schema tables and table columns instead of fanning out.
 */
export function extractSqlMetadataReferences(sql: string): SqlMetadataReferences {
  if (sql.trim() === "") {
    return { schemas: [], tables: [], qualifiers: [], aliases: {} };
  }

  // Strip single-line comments, block comments, and single-quoted string literals
  // so identifiers inside comments or string values do not trigger metadata fetches.
  const sanitized = sql
    .replace(/\/\*[\s\S]*?\*\//g, " ")
    .replace(/--[^\n]*/g, " ")
    .replace(/'(?:''|\\'|[^'])*'/g, " '' ");

  const schemas: string[] = [];
  const seenSchemas = new Set<string>();
  const pushSchema = (raw: string) => {
    const key = raw.toLowerCase();
    if (key === "" || SQL_KEYWORD_SET.has(raw.toUpperCase()) || seenSchemas.has(key)) return;
    seenSchemas.add(key);
    schemas.push(raw);
  };

  const tables: SqlTableReference[] = [];
  const seenTables = new Set<string>();
  const aliases: Record<string, SqlTableReference> = {};

  // Match FROM/JOIN/UPDATE/INTO [schema.]table [[AS] alias].
  const tableClauseRe =
    /\b(?:FROM|JOIN|UPDATE|INTO)\s+(?:[`"]([A-Za-z0-9_$-]+)[`"]|([A-Za-z_][A-Za-z0-9_$-]*))(?:\s*\.\s*(?:[`"]([A-Za-z0-9_$-]+)[`"]|([A-Za-z_][A-Za-z0-9_$-]*)))?(?:\s+(?:AS\s+)?(?:[`"]([A-Za-z0-9_$-]+)[`"]|([A-Za-z_][A-Za-z0-9_$-]*)))?/gi;

  for (const match of sanitized.matchAll(tableClauseRe)) {
    const first = match[1] ?? match[2] ?? "";
    const second = match[3] ?? match[4] ?? "";
    const rawAlias = match[5] ?? match[6] ?? "";

    if (first === "") continue;
    let schema: string | null = null;
    let table = first;
    if (second !== "") {
      schema = first;
      table = second;
    }
    if (SQL_KEYWORD_SET.has(table.toUpperCase())) continue;

    let alias: string | null = null;
    if (rawAlias !== "" && !SQL_KEYWORD_SET.has(rawAlias.toUpperCase())) {
      alias = rawAlias;
    }

    if (schema !== null) {
      pushSchema(schema);
    }

    const ref: SqlTableReference = { schema, table, alias };
    const tableKey = `${(schema ?? "").toLowerCase()}\u0000${table.toLowerCase()}`;
    if (!seenTables.has(tableKey)) {
      seenTables.add(tableKey);
      tables.push(ref);
    }
    if (alias !== null) {
      aliases[alias.toLowerCase()] = ref;
    }
  }

  // Match every `<ident>.` qualifier across the SQL text (e.g. `app.` or `users.` or `u.`).
  const qualifiers: string[] = [];
  const seenQualifiers = new Set<string>();
  const dotQualifierRe = /(?:[`"]([A-Za-z0-9_$-]+)[`"]|([A-Za-z_][A-Za-z0-9_$-]*))\s*\./g;
  for (const match of sanitized.matchAll(dotQualifierRe)) {
    const ident = match[1] ?? match[2] ?? "";
    const key = ident.toLowerCase();
    if (key === "" || SQL_KEYWORD_SET.has(ident.toUpperCase()) || seenQualifiers.has(key)) continue;
    seenQualifiers.add(key);
    qualifiers.push(ident);
  }

  return { schemas, tables, qualifiers, aliases };
}

export interface SqlCompletionSuggestion {
  label: string;
  insertText: string;
  detail: string;
  kind: "keyword" | "schema" | "table" | "column";
}

export interface SqlCompletionContext {
  /** Identifier immediately before a trailing dot (for schema.table or table.column completion). */
  qualifier?: string;
  /** Optional full SQL text used to resolve table aliases (e.g. `FROM app.users u` -> `u.`). */
  sql?: string;
}

export function buildSqlSuggestions(
  catalog: SqlCompletionCatalog,
  context?: SqlCompletionContext,
): SqlCompletionSuggestion[] {
  const rawQualifier = context?.qualifier?.trim() ?? "";
  if (rawQualifier !== "") {
    const qLower = rawQualifier.toLowerCase();
    const aliasTarget =
      context?.sql !== undefined && context.sql !== ""
        ? extractSqlMetadataReferences(context.sql).aliases[qLower]
        : undefined;

    const schemaTables = catalog.tables.filter(
      (table) => table.schema !== null && table.schema.toLowerCase() === qLower,
    );
    const tableColumns = catalog.columns.filter((column) => {
      if (column.table.toLowerCase() === qLower) return true;
      if (aliasTarget === undefined) return false;
      if (column.table.toLowerCase() !== aliasTarget.table.toLowerCase()) return false;
      if (aliasTarget.schema === null || column.schema === null) return true;
      return column.schema.toLowerCase() === aliasTarget.schema.toLowerCase();
    });
    const qualifiedResults: SqlCompletionSuggestion[] = [
      ...schemaTables.map((table) => ({
        label: table.name,
        insertText: table.name,
        detail: `table · ${table.schema ?? rawQualifier}`,
        kind: "table" as const,
      })),
      ...tableColumns.map((column) => ({
        label: column.name,
        insertText: column.name,
        detail: `column · ${column.table}`,
        kind: "column" as const,
      })),
    ];
    if (qualifiedResults.length > 0) {
      return qualifiedResults;
    }
    if (catalog.columns.length > 0) {
      return catalog.columns.map((column) => ({
        label: column.name,
        insertText: column.name,
        detail: `column · ${column.table}`,
        kind: "column" as const,
      }));
    }
  }
  const schemaSuggestions = catalog.schemas.map((schema) => ({
    label: schema,
    insertText: schema,
    detail: "schema",
    kind: "schema" as const,
  }));
  const tableSuggestions = catalog.tables.map((table) => ({
    label: table.schema === null ? table.name : `${table.schema}.${table.name}`,
    insertText: table.schema === null ? table.name : `${table.schema}.${table.name}`,
    detail: "table",
    kind: "table" as const,
  }));
  const columnSuggestions = catalog.columns.map((column) => ({
    label: column.name,
    insertText: column.name,
    detail: `column · ${column.table}`,
    kind: "column" as const,
  }));
  const keywordSuggestions = SQL_KEYWORDS.map((keyword) => ({
    label: keyword,
    insertText: keyword,
    detail: "SQL",
    kind: "keyword" as const,
  }));
  return [...schemaSuggestions, ...tableSuggestions, ...columnSuggestions, ...keywordSuggestions];
}
