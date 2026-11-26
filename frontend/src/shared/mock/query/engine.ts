import { MAX_BATCH_STATEMENTS } from "@/shared/lib/sql-highlight";
import { fixtureUuid as uuid, nowIso } from "@/shared/mock/kit";
import {
  type FixtureExecution,
  type FixtureQuerySession,
  type FixtureSchemaDef,
  businessError,
  successEnvelope,
  world,
} from "./state";

// ---------------------------------------------------------------------------
// Deterministic data model
// ---------------------------------------------------------------------------

export const MYSQL_SCHEMAS: FixtureSchemaDef[] = [
  {
    name: "app",
    tables: [
      {
        table_name: "users",
        relation_kind: "table",
        rowCount: 1200,
        columns: [
          { column_name: "id", ordinal: 1, data_type: "int", nullable: false, column_key: "PRI", extra: "auto_increment", comment: "主键" },
          { column_name: "username", ordinal: 2, data_type: "varchar(64)", nullable: false, comment: "用户名" },
          { column_name: "email", ordinal: 3, data_type: "varchar(255)", nullable: true, comment: "邮箱（脱敏词表命中）" },
          { column_name: "phone", ordinal: 4, data_type: "varchar(32)", nullable: true, comment: "手机号（脱敏词表命中）" },
          { column_name: "created_at", ordinal: 5, data_type: "datetime", nullable: false, column_default: "CURRENT_TIMESTAMP", comment: "注册时间" },
        ],
      },
      {
        table_name: "orders",
        relation_kind: "table",
        rowCount: 260,
        columns: [
          { column_name: "id", ordinal: 1, data_type: "bigint", nullable: false, column_key: "PRI", extra: "auto_increment", comment: "订单主键" },
          { column_name: "order_no", ordinal: 2, data_type: "varchar(32)", nullable: false, column_key: "UNI", comment: "业务单号（唯一）" },
          { column_name: "amount", ordinal: 3, data_type: "decimal(12,2)", nullable: false, column_default: "0.00", comment: "订单金额" },
          { column_name: "status", ordinal: 4, data_type: "varchar(16)", nullable: false, column_default: "created", comment: "订单状态" },
          { column_name: "user_id", ordinal: 5, data_type: "int", nullable: false, column_key: "MUL", comment: "下单用户（FK users.id）" },
        ],
      },
    ],
  },
  {
    name: "stats",
    tables: [
      {
        table_name: "daily_counts",
        relation_kind: "view",
        rowCount: 30,
        columns: [
          { column_name: "day", ordinal: 1, data_type: "date", nullable: false, comment: "统计日" },
          { column_name: "order_count", ordinal: 2, data_type: "int", nullable: false, comment: "当日订单数" },
          { column_name: "gmv", ordinal: 3, data_type: "decimal(14,2)", nullable: false, comment: "当日成交额" },
        ],
      },
    ],
  },
];

export const PG_SCHEMAS: FixtureSchemaDef[] = [
  {
    name: "public",
    tables: [
      {
        table_name: "analytics_events",
        relation_kind: "table",
        rowCount: 80,
        columns: [
          { column_name: "id", ordinal: 1, data_type: "bigint", nullable: false, extra: "auto_increment", comment: "事件主键（identity）" },
          { column_name: "kind", ordinal: 2, data_type: "text", nullable: false, comment: "事件类型" },
          { column_name: "payload", ordinal: 3, data_type: "jsonb", nullable: true, comment: "事件载荷（脱敏词表命中）" },
        ],
      },
      { table_name: "f12_demo", relation_kind: "table", rowCount: 1, columns: [{ column_name: "id", ordinal: 1, data_type: "bigint", nullable: false, column_key: "PRI" }, { column_name: "note", ordinal: 2, data_type: "text", nullable: true }] },
    ],
  },
];

/** Deterministic cell values — synthetic, no real identifiers. */
function cellValue(table: string, column: string, rowIndex: number): string {
  switch (column) {
    case "id":
      return String(rowIndex + 1);
    case "username":
      // §18.50 addendum 4: one oversized cell (first row of app.users) so the
      // ellipsis+tooltip and column-resize behavior is testable in the
      // default world without switching scenarios — exactly 1000 chars.
      // username is deliberately NOT in the masking vocabulary (email/phone
      // are), so the long content actually reaches the grid.
      if (table === "users" && rowIndex === 0) {
        return `big-cell-${"x".repeat(991)}`;
      }
      return `user_${String((rowIndex % 40) + 1)}`;
    case "email":
      return `user_${String((rowIndex % 40) + 1)}@example.test`;
    case "phone":
      return `138${String(10000000 + (rowIndex % 90000000))}`;
    case "created_at":
    case "day":
      return `2026-08-${String((rowIndex % 28) + 1).padStart(2, "0")}`;
    case "order_no":
      return `SO-${String(100000 + rowIndex)}`;
    case "amount":
    case "gmv":
      return String(((rowIndex * 37) % 10000) / 4 + 0.25);
    case "status":
      return (["created", "paid", "shipped", "done"][rowIndex % 4]) ?? "created";
    case "user_id":
      return String((rowIndex % 40) + 1);
    case "order_count":
      return String(rowIndex + 10);
    case "kind":
      return (["login", "view", "click"][rowIndex % 3]) ?? "login";
    case "payload":
      return JSON.stringify({ seq: rowIndex });
    case "event_blob":
      // §18.50: a deliberately huge deterministic string (~1400 chars) for
      // the query-huge scenario — ellipsis + tooltip stress data.
      return `blob-${String(rowIndex)}-` + "x".repeat(1400);
    default:
      return `${table}-${column}-${String(rowIndex)}`;
  }
}

// ---------------------------------------------------------------------------
// Single-SELECT approximation of domain.CheckSingleSelect
// ---------------------------------------------------------------------------

const WRITE_HEADS = new Set([
  "insert", "update", "delete", "replace", "truncate", "create", "alter", "drop",
  "grant", "revoke", "call", "do", "handler", "load", "lock", "optimize",
  "rename", "set", "show", "analyze", "explain", "describe", "desc", "checkpoint",
  "reindex", "vacuum", "copy", "merge", "values",
]);

/** Strips literals/identifiers/comments then splits on semicolons — the
 *  tokenizer-lite statement segmentation shared by the batch safety check
 *  and result-table resolution (F-Q-001: result set comes from the LAST
 *  statement). */
function topLevelStatements(sql: string): string[] {
  const stripped = sql
    .replace(/--[^\n]*/g, " ")
    .replace(/#[^\n]*/g, " ")
    .replace(/\/\*[\s\S]*?\*\//g, " ")
    .replace(/'(?:[^'\\]|\\.)*'/g, "''")
    .replace(/"(?:[^"\\]|\\.)*"/g, '""')
    .replace(/`(?:[^`])*`/g, "``");
  return stripped
    .split(";")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

/** Batch-split approximation of domain.CheckSelectBatch (F-Q-001): splits
 *  on top-level semicolons, validates each statement as a plausible pure
 *  SELECT; empty segments are dropped; trailing semicolons are fine.
 *  Mirrors the server's 16-statement batch cap (Review R1 M2). */
function checkSingleSelectError(sql: string): number | null {
  if (sql === "" || sql.includes("\0")) return 4007;
  const statements = topLevelStatements(sql);
  if (statements.length === 0) return 1001;
  if (statements.length > MAX_BATCH_STATEMENTS) return 4007;
  for (const statement of statements) {
    const words = statement.toLowerCase().match(/[a-z_]+/g) ?? [];
    if (words.length === 0) return 4007;
    const head = words[0];
    if (head !== "select" && head !== "with") return 4007;
    for (const word of words) {
      if (WRITE_HEADS.has(word) && word !== "select") {
        // Allow write-heads as column/table identifiers only when quoted; the
        // stripped scan sees bare words, so any bare write head is a rejection.
        return 4007;
      }
    }
  }
  return null;
}

// ---------------------------------------------------------------------------
// Masking (server-side mirror; run-frozen via execution maskFlags)
// ---------------------------------------------------------------------------

export function foldCase(value: string): string {
  return value.trim().toLocaleLowerCase("en-US");
}

export function vocabularyFor(flowId: string, datasourceId: string): string[] {
  const stored = world.maskingRules.get(`${flowId}:${datasourceId}`);
  return stored === undefined ? [] : stored.map(foldCase);
}

function maskCell(): string {
  return "***";
}

// ---------------------------------------------------------------------------
// Execution engine (single page + cursor pages)
// ---------------------------------------------------------------------------

export function executeSelect(
  session: FixtureQuerySession,
  body: {
    datasource_id?: string;
    schema_name?: string;
    sql?: string;
    timeout_ms?: number;
    page_size?: number;
  },
): ReturnType<typeof businessError> | ReturnType<typeof successEnvelope> {
  // Validation order mirrors ExecuteSelect: capability → timeout → page
  // size/schema → single SELECT → masking → execute.
  const capability = session.capabilities.find((c) => c.datasource_id === body.datasource_id);
  if (capability === undefined) return businessError(4002, "datasource not granted");
  if (capability.state === "datasource_unavailable" || capability.state === "identity_changed") {
    return businessError(3010, "datasource unavailable");
  }
  const timeoutMs = body.timeout_ms ?? 30000;
  if (timeoutMs < 1 || timeoutMs > 300000) return businessError(4008, "timeout out of range");
  const pageSize = body.page_size ?? 500;
  if (pageSize < 1 || pageSize > 5000) return businessError(1001, "invalid page size");
  if (body.schema_name === undefined || body.schema_name === "" || body.schema_name.length > 128) {
    return businessError(1001, "schema required");
  }
  const sql = body.sql ?? "";
  const singleSelectCode = checkSingleSelectError(sql);
  if (singleSelectCode !== null) {
    return businessError(singleSelectCode, "only a single SELECT is allowed");
  }
  const ds = world.datasources.get(body.datasource_id ?? "");
  const schema = ds?.schemas.find((s) => s.name === body.schema_name);
  if (ds === undefined || schema === undefined) return businessError(1001, "schema not found");
  // Resolve the target table from the LAST statement's
  // `from <schema>.<table>` / `from <table>` (F-Q-001 batch semantics).
  const lastStatement = topLevelStatements(sql).at(-1) ?? sql;
  const fromMatch = lastStatement.match(/from\s+(?:`?([a-z_][a-z0-9_]*)`?\.)?`?([a-z_][a-z0-9_]*)`?/i);
  const fallback = schema.tables[0];
  if (fallback === undefined) return businessError(1001, "no tables in schema");
  const table = fromMatch === null
    ? fallback
    : (schema.tables.find((t) => t.table_name === fromMatch[2]) ?? fallback);
  const vocabulary = vocabularyFor(session.flow_id, body.datasource_id ?? "");
  const columns = table.columns.map((column) => ({
    name: column.column_name,
    type: column.data_type,
    masked: vocabulary.includes(foldCase(column.column_name)),
  }));
  const execution: FixtureExecution = {
    id: uuid(),
    session_id: session.id,
    datasource_id: body.datasource_id ?? "",
    schema_name: body.schema_name,
    table_name: table.table_name,
    sql,
    columns: columns.map((column) => ({ name: column.name, type: column.type })),
    maskFlags: columns.map((column) => column.masked),
    rowCount: table.rowCount,
    state: "succeeded",
    failure_code: null,
    created_at: nowIso(),
  };
  world.executions.set(execution.id, execution);
  const token = `cursor-${execution.id}`;
  return successEnvelope(cursorPage(execution, token, pageSize));
}

export function cursorPage(execution: FixtureExecution, token: string, pageSize: number) {
  let current = world.cursors.get(token);
  if (current === undefined) {
    current = {
      token,
      execution_id: execution.id,
      session_id: execution.session_id,
      datasource_id: execution.datasource_id,
      offset: 0,
      pageSize,
      consumed: false,
    };
    world.cursors.set(token, current);
  }
  const cursor = current;
  const slice = Array.from({ length: Math.min(pageSize, execution.rowCount - cursor.offset) }, (_, i) => {
    const rowIndex = cursor.offset + i;
    return execution.columns.map((column, columnIndex) =>
      execution.maskFlags[columnIndex] ? maskCell() : cellValue(execution.table_name, column.name, rowIndex),
    );
  });
  cursor.offset += slice.length;
  const hasMore = cursor.offset < execution.rowCount;
  if (!hasMore) cursor.consumed = true;
  return {
    execution_id: execution.id,
    columns: execution.columns,
    rows: slice,
    page: { next_cursor: hasMore ? token : null, has_more: hasMore },
    elapsed_ms: 42,
  };
}
