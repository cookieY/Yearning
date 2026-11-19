import { HttpResponse, http } from "msw";
import type { DefaultBodyType, HttpHandler } from "msw";
import { readStoredAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { MOCK_IDENTITY_IDS, currentIdentityId, mockSuperMode } from "@/shared/mock/identity-store";
import { settingsBridge } from "@/shared/mock/settings-bridge";
import { readStoredScenario } from "@/shared/mock/scenario-store";
import { emitMockSseEvent } from "@/shared/mock/sse-mock";

/**
 * Stateful MSW fixture for the query domain (FE-F10): query flows, access
 * requests, grants, sessions, metadata, executions and cursor pages. It
 * mirrors the frozen backend semantics from backend/internal/query/application:
 *
 * - Approval-enabled sessions require a grant the requester owns; the
 *   approval-disabled path freezes the flow's own capabilities (Q002/Q003).
 * - ExecuteSelect validates session usability → capability row → timeout
 *   range → single-SELECT safety → masking vocabulary, in that order, and
 *   returns the first result page directly.
 * - Cursor pages advance a live in-memory cursor; exhaustion consumes the
 *   token so any further read answers CURSOR_EXPIRED (no replay).
 * - FetchPage re-checks the session/grant per page and enforces the frozen
 *   can_export for purpose=export (Q006).
 * - Owner scoping: sessions/grants/requests only read back through owner or
 *   frozen-reviewer relations; the builtin admin gains no query read face
 *   (authorization-policy admin_is_not_business_override) but may revoke.
 *
 * The single-SELECT check is a pragmatic mirror of domain.CheckSingleSelect
 * (first word select/with, no top-level semicolon, no write surfaces). The
 * server stays authoritative; this approximation exists so UI error paths
 * are exercisable before the real backend is wired in FE-F12.
 */

const MOCK_REQUEST_ID = "46464646-4646-4646-8466-464646464646";

/** The mock session identity (auth-handlers UUID) — ordinary query user. */
export const QUERY_FIXTURE_SESSION_USER_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";
/** A second fixture user owning "someone else's" query objects. */
export const QUERY_FIXTURE_OTHER_USER_ID = "6f0f2b3c-1111-4111-8111-00000000c101";
export const QUERY_FIXTURE_FLOW_ID = "6f0f2b3c-2222-4222-8222-00000000f201";
export const QUERY_FIXTURE_DS_MYSQL_ID = "6f0f2b3c-3333-4333-8333-00000000a301";
export const QUERY_FIXTURE_DS_PG_ID = "6f0f2b3c-3333-4333-8333-00000000a302";

const QUERY_FLOW_NAME = "online-readonly";

interface FixtureColumnDef {
  column_name: string;
  ordinal: number;
  data_type: string;
  nullable: boolean;
  // §18.51 table structure enrichment (aligned with SHOW FULL FIELDS, matching optional backend fields);
  // omitted = none.
  column_key?: string;
  column_default?: string;
  extra?: string;
  comment?: string;
}

interface FixtureTableDef {
  table_name: string;
  relation_kind: "table" | "view";
  columns: FixtureColumnDef[];
  /** Deterministic row count served through the cursor. */
  rowCount: number;
}

interface FixtureSchemaDef {
  name: string;
  tables: FixtureTableDef[];
}

interface FixtureQueryDatasource {
  id: string;
  name: string;
  compatibility_mode: "mysql" | "postgresql";
  schemas: FixtureSchemaDef[];
}

interface FixtureFlowCapability {
  datasource_id: string;
  can_query: true;
  can_export: boolean;
}

interface FixtureApprovalStep {
  position: number;
  /** Frozen reviewer snapshot; the mock session user reviews scenario seeds. */
  actors: string[];
  state: "pending" | "active" | "approved" | "rejected" | "invalid";
  decided_at: string | null;
}

interface FixtureAccessRequest {
  id: string;
  requester_user_id: string;
  flow_id: string;
  state: "access_pending" | "grant_active" | "access_rejected" | "withdrawn" | "invalid";
  datasource_ids: string[];
  grant_id: string | null;
  /** Fixture-internal; the declared view has no reason/terminal fields. */
  reason: string;
  requested_until: string;
  steps: FixtureApprovalStep[];
  version: number;
  created_at: string;
}

interface FixtureGrantCapability {
  datasource_id: string;
  datasource_name_snapshot: string;
  can_query: true;
  can_export: boolean;
}

interface FixtureGrant {
  id: string;
  requester_user_id: string;
  flow_id: string;
  state: "active" | "revoked" | "expired" | "relinquished";
  revoked_reason: string | null;
  expires_at: string | null;
  capabilities: FixtureGrantCapability[];
  version: number;
  created_at: string;
}

interface FixtureSessionCapability {
  datasource_id: string;
  datasource_name: string;
  state: "active" | "datasource_unavailable" | "identity_changed";
  can_query: true;
  can_export: boolean;
}

interface FixtureQuerySession {
  id: string;
  user_id: string;
  flow_id: string;
  grant_id: string | null;
  state: "active" | "closed" | "revoked" | "expired" | "user_deleted";
  capabilities: FixtureSessionCapability[];
  created_at: string;
}

interface FixtureExecution {
  id: string;
  session_id: string;
  datasource_id: string;
  schema_name: string;
  table_name: string;
  sql: string;
  columns: { name: string; type: string }[];
  /** Case-folded mask flags frozen at execution time (per-run vocabulary). */
  maskFlags: boolean[];
  rowCount: number;
  state: "succeeded" | "failed" | "timed_out";
  failure_code: number | null;
  created_at: string;
}

interface FixtureCursor {
  token: string;
  execution_id: string;
  session_id: string;
  datasource_id: string;
  offset: number;
  pageSize: number;
  consumed: boolean;
}

interface QueryWorld {
  approvalEnabled: boolean;
  datasources: Map<string, FixtureQueryDatasource>;
  flowCapabilities: FixtureFlowCapability[];
  flowSteps: { position: number; actors: string[] }[];
  maskingRules: Map<string, string[]>;
  accessRequests: Map<string, FixtureAccessRequest>;
  grants: Map<string, FixtureGrant>;
  sessions: Map<string, FixtureQuerySession>;
  executions: Map<string, FixtureExecution>;
  cursors: Map<string, FixtureCursor>;
  flowGranted: boolean;
}

const world: QueryWorld = {
  approvalEnabled: true,
  datasources: new Map(),
  flowCapabilities: [],
  flowSteps: [],
  maskingRules: new Map(),
  accessRequests: new Map(),
  grants: new Map(),
  sessions: new Map(),
  executions: new Map(),
  cursors: new Map(),
  flowGranted: false,
};

function nowIso(): string {
  return new Date().toISOString();
}

function uuid(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return `id-${Math.random().toString(16).slice(2)}-${String(Date.now())}`;
}

function successEnvelope(data: DefaultBodyType) {
  return { err_code: 0, message: "ok", data, request_id: MOCK_REQUEST_ID };
}

function businessError(errCode: number, message: string) {
  return {
    err_code: errCode,
    message,
    data: null,
    request_id: MOCK_REQUEST_ID,
    retryable: false,
  };
}

function pageOf<T>(items: T[], limit: number | null, after: string | null) {
  const size = limit ?? 50;
  let rows = items;
  if (after !== null && after !== "") {
    const idx = items.findIndex((item) => (item as { id?: string }).id === after);
    rows = idx >= 0 ? items.slice(idx + 1) : [];
  }
  const page = rows.slice(0, size);
  const last = page[page.length - 1] as { id?: string } | undefined;
  // Contract pagination envelope: {items, page:{next_cursor, has_more}}.
  // The flat {items, next_cursor, has_more} shape mirrored the backend's
  // deviation (F12 divergence No.005) — the mock must mirror the contract.
  return {
    items: page,
    page: {
      next_cursor: rows.length > size && last?.id ? last.id : null,
      has_more: rows.length > size,
    },
  };
}

/** The builtin admin identity gains no query read face: when the mock
 * session runs the admin behavior the query actor owns nothing and relates
 * to nothing (authorization-policy admin_is_not_business_override). Super
 * mode (owner 2026-11-03) suspends that split in the dev browser: the
 * session keeps the henry query world alongside the admin capability. */
function queryActorUserId(): string {
  if (mockSuperMode()) return currentIdentityId();
  return readStoredAuthBehavior() === "admin" ? "__builtin_admin__" : currentIdentityId();
}

/** Session presence follows the admin fixture convention: jsdom fetches
 * carry no document cookies, so the behavior dimension (never "expired")
 * is the session-presence signal shared by all component tests. */
function sessionAuthenticated(): boolean {
  return readStoredAuthBehavior() !== "expired";
}

function unauthenticated() {
  return HttpResponse.json(
    { type: "about:blank", title: "session_expired", status: 401, detail: "no active session", request_id: MOCK_REQUEST_ID },
    { status: 401, headers: { "Content-Type": "application/problem+json" } },
  );
}

function parseIfMatch(request: Request): string | null {
  const raw = request.headers.get("If-Match");
  return raw === null ? null : raw.replace(/^"|"$/g, "");
}



// ---------------------------------------------------------------------------
// Deterministic data model
// ---------------------------------------------------------------------------

const MYSQL_SCHEMAS: FixtureSchemaDef[] = [
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

const PG_SCHEMAS: FixtureSchemaDef[] = [
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
const MAX_BATCH_STATEMENTS = 16;

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

function foldCase(value: string): string {
  return value.trim().toLocaleLowerCase("en-US");
}

function vocabularyFor(flowId: string, datasourceId: string): string[] {
  const stored = world.maskingRules.get(`${flowId}:${datasourceId}`);
  return stored === undefined ? [] : stored.map(foldCase);
}

function maskCell(): string {
  return "***";
}

// ---------------------------------------------------------------------------
// Views (declared OpenAPI shapes only)
// ---------------------------------------------------------------------------

function accessRequestView(row: FixtureAccessRequest) {
  return {
    id: row.id,
    requester_user_id: row.requester_user_id,
    state: row.state,
    datasource_ids: [...row.datasource_ids],
    grant_id: row.grant_id,
    version: row.version,
    created_at: row.created_at,
  };
}

function grantView(row: FixtureGrant) {
  return {
    id: row.id,
    requester_user_id: row.requester_user_id,
    state: row.state,
    revoked_reason: row.revoked_reason,
    expires_at: row.expires_at,
    version: row.version,
    created_at: row.created_at,
  };
}

function sessionView(row: FixtureQuerySession, includeExecutions = false) {
  const executions = includeExecutions
    ? [...world.executions.values()]
        .filter((ex) => ex.session_id === row.id)
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .slice(0, 50)
        .map((ex) => ({
          id: ex.id,
          datasource_id: ex.datasource_id,
          sql: ex.sql,
          state: ex.state,
          elapsed_ms: null,
          created_at: ex.created_at,
        }))
    : undefined;
  return {
    id: row.id,
    user_id: row.user_id,
    // Aligned with the real read face (session.go SessionView includes grant_id;
    // 2026-10-31 session reuse resolution depends on it).
    grant_id: row.grant_id,
    state: row.state,
    capabilities: row.capabilities.map((capability) => ({ ...capability })),
    ...(executions !== undefined ? { executions } : {}),
    created_at: row.created_at,
  };
}

function flowView() {
  return {
    id: QUERY_FIXTURE_FLOW_ID,
    name: QUERY_FLOW_NAME,
    flow_type: "query_access",
    enabled: true,
    status: "enabled",
    rule_set_id: null,
    stages: undefined,
    approval_steps: world.flowSteps.map((step) => ({
      position: step.position,
      actors: step.actors.map((user_id) => ({ user_id })),
    })),
    query_capabilities: world.flowCapabilities.map((capability) => ({ ...capability })),
    version: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  };
}

// ---------------------------------------------------------------------------
// Grant/session lifecycle helpers (mirrors grant.go / session.go cascades)
// ---------------------------------------------------------------------------

function terminateSessionsOfGrant(grantId: string, state: "revoked" | "expired" | "closed") {
  for (const session of world.sessions.values()) {
    if (session.grant_id === grantId && session.state === "active") {
      session.state = state;
    }
  }
}

function createGrantFromRequest(request: FixtureAccessRequest): FixtureGrant {
  const grant: FixtureGrant = {
    id: uuid(),
    requester_user_id: request.requester_user_id,
    flow_id: request.flow_id,
    state: "active",
    revoked_reason: null,
    expires_at: request.requested_until,
    capabilities: request.datasource_ids.flatMap((datasourceId) => {
      const ds = world.datasources.get(datasourceId);
      const flowCapability = world.flowCapabilities.find((c) => c.datasource_id === datasourceId);
      if (ds === undefined || flowCapability === undefined) return [];
      return [
        {
          datasource_id: datasourceId,
          datasource_name_snapshot: ds.name,
          can_query: true as const,
          can_export: flowCapability.can_export,
        },
      ];
    }),
    version: 1,
    created_at: nowIso(),
  };
  world.grants.set(grant.id, grant);
  return grant;
}

function activeGrantCapabilityRows(grant: FixtureGrant): FixtureSessionCapability[] {
  return grant.capabilities.map((capability) => ({
    datasource_id: capability.datasource_id,
    datasource_name: capability.datasource_name_snapshot,
    state: "active",
    can_query: true,
    can_export: capability.can_export,
  }));
}

function flowCapabilityRows(): FixtureSessionCapability[] {
  return world.flowCapabilities.flatMap((capability) => {
    const ds = world.datasources.get(capability.datasource_id);
    if (ds === undefined) return [];
    return [
      {
        datasource_id: capability.datasource_id,
        datasource_name: ds.name,
        state: "active",
        can_query: true,
        can_export: capability.can_export,
      },
    ];
  });
}

// ---------------------------------------------------------------------------
// Seeding & scenarios
// ---------------------------------------------------------------------------

export type QueryScenario =
  | "query-flow"
  | "query-flow-direct"
  | "query-session"
  | "query-revoked"
  | "query-approval";

function seedBase(): void {
  world.datasources.set(QUERY_FIXTURE_DS_MYSQL_ID, {
    id: QUERY_FIXTURE_DS_MYSQL_ID,
    name: "analytics-mysql",
    compatibility_mode: "mysql",
    schemas: structuredClone(MYSQL_SCHEMAS),
  });
  world.datasources.set(QUERY_FIXTURE_DS_PG_ID, {
    id: QUERY_FIXTURE_DS_PG_ID,
    name: "analytics-pg",
    compatibility_mode: "postgresql",
    schemas: structuredClone(PG_SCHEMAS),
  });
  world.flowCapabilities = [
    { datasource_id: QUERY_FIXTURE_DS_MYSQL_ID, can_query: true, can_export: true },
    { datasource_id: QUERY_FIXTURE_DS_PG_ID, can_query: true, can_export: false },
  ];
  world.flowSteps = [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer] }];
  world.maskingRules.set(`${QUERY_FIXTURE_FLOW_ID}:${QUERY_FIXTURE_DS_MYSQL_ID}`, [
    "email",
    "phone",
  ]);
  world.maskingRules.set(`${QUERY_FIXTURE_FLOW_ID}:${QUERY_FIXTURE_DS_PG_ID}`, ["payload"]);
}

let lastSeededScenario: string | null = null;
const DEFAULT_DEMO_SEED = "__default_demo__";

/** Lazy world sync: every query handler calls this first so a query-*
 * scenario always has its seeded world regardless of browser worker
 * bootstrap timing (self-healing reseed). Non-query scenarios never clobber
 * a world that a test seeded explicitly — except the browser default world,
 * which carries the pending-approval demo layer below. */
function ensureQueryWorld(): void {
  const scenario = readStoredScenario();
  if (scenario.startsWith("query-")) {
    if (scenario !== lastSeededScenario) {
      seedQueryScenario(scenario);
      lastSeededScenario = scenario;
    }
    return;
  }
  seedDefaultQueryDemo();
}


/** Q002: approval is required only when BOTH the scenario world enables it
 * AND the admin settings knob is on — the settings toggle (§18.47) opens
 * the direct path without touching the scenario. */
function approvalRequired(): boolean {
  return world.approvalEnabled && settingsBridge.queryApprovalEnabled;
}

/** Mock approximation for the Q002 direct path: the flow's group-grant
 * cannot be driven from the admin UI here (separate fixture worlds), so
 * with approval disabled the visible flow is treated as granted — the
 * demo-world equivalent of "the group already holds the query flow". With
 * approval on, world.flowGranted keeps its exact scenario semantics. */
function flowGrantEffective(): boolean {
  return world.flowGranted || !approvalRequired();
}

// ---- Default-world pending approvals (§18.44 follow-up, 2026-10-14) -------
//
// Owner-facing queue data: the query domain previously existed only under
// query-* scenarios, so the default mock world left the query approval queue empty
// for every identity. The default world now carries the base catalog plus
// two henry-requested access_pending requests whose frozen reviewer is
// reviewer-a — reviewer-a's queue has rows to decide, henry sees his own
// pending requests. Browser worlds only (vitest pins exact scenario shapes);
// timestamps anchored for determinism.

function seedDefaultQueryDemo(): void {
  if (import.meta.env.MODE === "test") return;
  if (lastSeededScenario === DEFAULT_DEMO_SEED) return;
  resetQueryFixture();
  seedBase();
  lastSeededScenario = DEFAULT_DEMO_SEED;
  const requests: Array<{
    id: string;
    datasourceIds: string[];
    reason: string;
    createdAt: string;
  }> = [
    {
      id: "qar-demo-mysql",
      datasourceIds: [QUERY_FIXTURE_DS_MYSQL_ID],
      reason: "月末对账，需要查询订单库",
      createdAt: "2026-09-01T07:40:00Z",
    },
    {
      id: "qar-demo-pg",
      datasourceIds: [QUERY_FIXTURE_DS_PG_ID],
      reason: "排查报表口径差异，需要读取报表库",
      createdAt: "2026-09-01T07:45:00Z",
    },
  ];
  for (const request of requests) {
    world.accessRequests.set(request.id, {
      id: request.id,
      requester_user_id: MOCK_IDENTITY_IDS.submitter,
      flow_id: QUERY_FIXTURE_FLOW_ID,
      state: "access_pending",
      datasource_ids: request.datasourceIds,
      grant_id: null,
      reason: request.reason,
      requested_until: "2026-09-15T08:00:00Z",
      steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer], state: "active", decided_at: null }],
      version: 1,
      created_at: request.createdAt,
    });
  }

  // ---- Query orders demo set (owner request 2026-11-04): one row per lifecycle
  // state so the query orders tab shows the full vocabulary in the default world —
  // approved→active grant, rejected, withdrawn, a revoked grant with its
  // frozen reason, an expired grant, and session records (query record rows come
  // from sessions when no request/grant exists for them). Browser worlds
  // only; vitest pins exact scenario shapes via MODE === "test".
  const mysqlCap = (canExport: boolean): FixtureGrantCapability => ({
    datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
    datasource_name_snapshot: "analytics-mysql",
    can_query: true,
    can_export: canExport,
  });
  const pgCap = (canExport: boolean): FixtureGrantCapability => ({
    datasource_id: QUERY_FIXTURE_DS_PG_ID,
    datasource_name_snapshot: "analytics-pg",
    can_query: true,
    can_export: canExport,
  });

  world.grants.set("qgrant-demo-active", {
    id: "qgrant-demo-active",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "active",
    revoked_reason: null,
    expires_at: "2026-12-01T00:00:00Z",
    capabilities: [mysqlCap(true), pgCap(false)],
    version: 1,
    created_at: "2026-09-05T02:10:00Z",
  });
  world.grants.set("qgrant-demo-revoked", {
    id: "qgrant-demo-revoked",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "revoked",
    revoked_reason: "安全审计：临时排查权限统一收回",
    expires_at: null,
    capabilities: [mysqlCap(true)],
    version: 2,
    created_at: "2026-08-20T06:30:00Z",
  });
  world.grants.set("qgrant-demo-expired", {
    id: "qgrant-demo-expired",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "expired",
    revoked_reason: null,
    expires_at: "2026-09-10T00:00:00Z",
    capabilities: [pgCap(false)],
    version: 1,
    created_at: "2026-08-25T09:00:00Z",
  });

  world.accessRequests.set("qar-demo-approved", {
    id: "qar-demo-approved",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "grant_active",
    datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID, QUERY_FIXTURE_DS_PG_ID],
    grant_id: "qgrant-demo-active",
    reason: "周度经营分析，需要两库长期只读权限",
    requested_until: "2026-12-01T00:00:00Z",
    steps: [
      {
        position: 1,
        actors: [MOCK_IDENTITY_IDS.reviewer],
        state: "approved",
        decided_at: "2026-09-05T02:10:00Z",
      },
    ],
    version: 2,
    created_at: "2026-09-05T01:55:00Z",
  });
  world.accessRequests.set("qar-demo-rejected", {
    id: "qar-demo-rejected",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "access_rejected",
    datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
    grant_id: null,
    reason: "一次性导出全量订单表",
    requested_until: "2026-08-25T00:00:00Z",
    steps: [
      {
        position: 1,
        actors: [MOCK_IDENTITY_IDS.reviewer],
        state: "rejected",
        decided_at: "2026-08-18T08:20:00Z",
      },
    ],
    version: 2,
    created_at: "2026-08-18T03:40:00Z",
  });
  world.accessRequests.set("qar-demo-withdrawn", {
    id: "qar-demo-withdrawn",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "withdrawn",
    datasource_ids: [QUERY_FIXTURE_DS_PG_ID],
    grant_id: null,
    reason: "报表口径已由 BI 平台覆盖，主动撤回",
    requested_until: "2026-08-20T00:00:00Z",
    steps: [
      {
        position: 1,
        actors: [MOCK_IDENTITY_IDS.reviewer],
        state: "pending",
        decided_at: null,
      },
    ],
    version: 1,
    created_at: "2026-08-10T05:15:00Z",
  });

  world.sessions.set("qsess-demo-active", {
    id: "qsess-demo-active",
    user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    grant_id: "qgrant-demo-active",
    state: "active",
    capabilities: [
      {
        datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
        datasource_name: "analytics-mysql",
        state: "active",
        can_query: true,
        can_export: true,
      },
      {
        datasource_id: QUERY_FIXTURE_DS_PG_ID,
        datasource_name: "analytics-pg",
        state: "active",
        can_query: true,
        can_export: false,
      },
    ],
    created_at: "2026-11-04T06:40:00Z",
  });
  world.sessions.set("qsess-demo-closed", {
    id: "qsess-demo-closed",
    user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    grant_id: "qgrant-demo-active",
    state: "closed",
    capabilities: [
      {
        datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
        datasource_name: "analytics-mysql",
        state: "active",
        can_query: true,
        can_export: true,
      },
    ],
    created_at: "2026-09-06T08:05:00Z",
  });
  world.sessions.set("qsess-demo-expired", {
    id: "qsess-demo-expired",
    user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    grant_id: "qgrant-demo-expired",
    state: "expired",
    capabilities: [
      {
        datasource_id: QUERY_FIXTURE_DS_PG_ID,
        datasource_name: "analytics-pg",
        state: "active",
        can_query: true,
        can_export: false,
      },
    ],
    created_at: "2026-08-26T07:20:00Z",
  });
}

export function resetQueryFixture(): void {
  lastSeededScenario = null;
  world.approvalEnabled = true;
  world.datasources.clear();
  world.flowCapabilities = [];
  world.flowSteps = [];
  world.maskingRules.clear();
  world.accessRequests.clear();
  world.grants.clear();
  world.sessions.clear();
  world.executions.clear();
  world.cursors.clear();
  world.flowGranted = false;
}

/** Scenario seeds are applied on world reset (vitest) or eagerly when the
 * stored scenario is a query one (browser worker). */
export function seedQueryScenario(scenario: string): void {
  resetQueryFixture();
  seedBase();
  const until = new Date(Date.now() + 12 * 3600 * 1000).toISOString();
  switch (scenario) {
    case "query-flow":
      world.flowGranted = true;
      break;
    case "query-flow-direct":
      world.flowGranted = true;
      world.approvalEnabled = false;
      break;
    case "query-session":
    case "query-revoked": {
      world.flowGranted = true;
      const request: FixtureAccessRequest = {
        id: uuid(),
        requester_user_id: QUERY_FIXTURE_SESSION_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        state: "grant_active",
        datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
        grant_id: null,
        reason: "incident analysis window",
        requested_until: until,
        steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer], state: "approved", decided_at: nowIso() }],
        version: 2,
        created_at: new Date(Date.now() - 3600 * 1000).toISOString(),
      };
      const grant = createGrantFromRequest(request);
      request.grant_id = grant.id;
      world.accessRequests.set(request.id, request);
      world.sessions.set("qs-fixture-active", {
        id: "qs-fixture-active",
        user_id: QUERY_FIXTURE_SESSION_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        grant_id: grant.id,
        state: "active",
        capabilities: activeGrantCapabilityRows(grant),
        created_at: new Date(Date.now() - 1800 * 1000).toISOString(),
      });
      // Another user's session: never visible to the session identity, which
      // keeps the owner-scoped 404 path (admin included) observable.
      world.sessions.set("qs-fixture-other", {
        id: "qs-fixture-other",
        user_id: QUERY_FIXTURE_OTHER_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        grant_id: null,
        state: "active",
        capabilities: flowCapabilityRows(),
        created_at: new Date(Date.now() - 1700 * 1000).toISOString(),
      });
      if (scenario === "query-revoked") {
        grant.state = "revoked";
        grant.revoked_reason = "least-privilege rotation";
        grant.version += 1;
        terminateSessionsOfGrant(grant.id, "revoked");
        request.state = "grant_active";
      }
      break;
    }
    case "query-huge": {
      // §18.50: large-result test world — granted flow + active session; mysql catalog
      // adds a high-row-count wide table (2600 rows x oversized text column) using
      // production-identical cursor pagination.
      world.flowGranted = true;
      world.approvalEnabled = true;
      const request: FixtureAccessRequest = {
        id: uuid(),
        requester_user_id: QUERY_FIXTURE_SESSION_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        state: "grant_active",
        datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
        grant_id: null,
        reason: "large result verification",
        requested_until: until,
        steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer], state: "approved", decided_at: nowIso() }],
        version: 2,
        created_at: new Date(Date.now() - 3600 * 1000).toISOString(),
      };
      const grant = createGrantFromRequest(request);
      request.grant_id = grant.id;
      world.accessRequests.set(request.id, request);
      world.sessions.set("qs-fixture-active", {
        id: "qs-fixture-active",
        user_id: QUERY_FIXTURE_SESSION_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        grant_id: grant.id,
        state: "active",
        capabilities: activeGrantCapabilityRows(grant),
        created_at: new Date(Date.now() - 1800 * 1000).toISOString(),
      });
      const mysql = world.datasources.get(QUERY_FIXTURE_DS_MYSQL_ID);
      if (mysql !== undefined && !mysql.schemas.some((entry) => entry.name === "events")) {
        mysql.schemas.push({
          name: "events",
          tables: [
            {
              table_name: "wide_events",
              relation_kind: "table",
              rowCount: 2600,
              columns: [
                { column_name: "id", ordinal: 1, data_type: "bigint", nullable: false, column_key: "PRI", extra: "auto_increment", comment: "事件主键" },
                { column_name: "kind", ordinal: 2, data_type: "varchar(16)", nullable: false, comment: "事件类型" },
                { column_name: "event_blob", ordinal: 3, data_type: "longtext", nullable: true, comment: "超大载荷列（展示压测用）" },
              ],
            },
          ],
        });
      }
      break;
    }
    case "query-approval": {
      world.flowGranted = true;
      const pending: FixtureAccessRequest = {
        id: "qar-fixture-pending",
        requester_user_id: QUERY_FIXTURE_OTHER_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        state: "access_pending",
        datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
        grant_id: null,
        reason: "month-end reconciliation",
        requested_until: until,
        steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer], state: "active", decided_at: null }],
        version: 1,
        created_at: new Date(Date.now() - 600 * 1000).toISOString(),
      };
      world.accessRequests.set(pending.id, pending);
      break;
    }
    default:
      break;
  }
}


// ---------------------------------------------------------------------------
// Execution engine (single page + cursor pages)
// ---------------------------------------------------------------------------

function executeSelect(
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

function cursorPage(execution: FixtureExecution, token: string, pageSize: number) {
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

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

/** Catalog page served by the review fixture's /users/me/flows handler for
 * flow_type=query_access (single route ownership stays there; this module
 * exports the data so both fixtures share one world). */
export function queryFlowsCatalogPage(authenticated: boolean) {
  // The catalog may be the first query-surface request in a fresh browser
  // page; sync the seeded world before answering (self-healing reseed).
  ensureQueryWorld();
  if (!authenticated || !flowGrantEffective()) {
    return pageOf([], null, null);
  }
  return pageOf([flowView()], null, null);
}

/** Owner/relation scoping for grants (grant.go ListGrants): owner or frozen
 * query-flow reviewer — pure relation matching, no identity exclusions (the
 * builtin admin reads exactly what its relations grant, §18.48). */
function canReadGrantRow(actorId: string, grant: FixtureGrant): boolean {
  if (grant.requester_user_id === actorId) return true;
  return world.flowSteps.some((step) => step.actors.includes(actorId));
}

/** Owner/relation scoping for access requests (access.go listRequestsFor):
 * requester ∪ frozen reviewer — no identity exclusions (§18.48). */
function canReadRequestRow(actorId: string, row: FixtureAccessRequest): boolean {
  if (row.requester_user_id === actorId) return true;
  return row.steps.some((step) => step.actors.includes(actorId));
}

function activeStepOf(row: FixtureAccessRequest): FixtureApprovalStep | null {
  return row.steps.find((step) => step.state === "active") ?? null;
}

export function queryFixtureHandlers(): HttpHandler[] {
  return [
    // ------------------------------------------------- access requests --
    http.get("*/query-access-requests", ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const url = new URL(request.url);
      // RCP-20261021: server-side state filter (mirrors backend ListAccessRequests).
      const stateFilter = url.searchParams.get("state");
      const rows = [...world.accessRequests.values()]
        .filter((row) => canReadRequestRow(actorId, row))
        .filter((row) => stateFilter === null || stateFilter === "" || row.state === stateFilter)
        .sort((a, b) => b.created_at.localeCompare(a.created_at));
      return HttpResponse.json(
        successEnvelope(
          pageOf(rows.map(accessRequestView), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after")),
        ),
      );
    }),

    http.post("*/query-access-requests", async ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const body = (await request.json().catch(() => null)) as {
        flow_id?: string;
        datasource_ids?: string[];
        requested_until?: string;
        reason?: string;
      } | null;
      if (
        body === null || body.flow_id !== QUERY_FIXTURE_FLOW_ID ||
        !Array.isArray(body.datasource_ids) || body.datasource_ids.length === 0 ||
        !body.requested_until || !body.reason || body.reason.length > 4096
      ) {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
      const outside = body.datasource_ids.filter(
        (id) => !world.flowCapabilities.some((capability) => capability.datasource_id === id),
      );
      if (outside.length > 0) return HttpResponse.json(businessError(2014, "flow not granted"));
      if (!approvalRequired()) return HttpResponse.json(businessError(1010, "approval disabled"));
      if (!flowGrantEffective()) {
        return HttpResponse.json(businessError(2014, "flow not granted"));
      }
      const open = [...world.accessRequests.values()].find(
        (row) => row.requester_user_id === actorId && row.state === "access_pending",
      );
      if (open !== undefined) return HttpResponse.json(businessError(1005, "request already exists"));
      const row: FixtureAccessRequest = {
        id: uuid(),
        requester_user_id: actorId,
        flow_id: body.flow_id,
        state: "access_pending",
        datasource_ids: body.datasource_ids,
        grant_id: null,
        reason: body.reason,
        requested_until: body.requested_until,
        steps: world.flowSteps.map((step, index) => ({
          position: step.position,
          actors: [...step.actors],
          state: index === 0 ? "active" : "pending",
          decided_at: null,
        })),
        version: 1,
        created_at: nowIso(),
      };
      world.accessRequests.set(row.id, row);
      return HttpResponse.json(successEnvelope(accessRequestView(row)));
    }),

    http.get("*/query-access-requests/:requestId", ({ params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const row = world.accessRequests.get(String(params.requestId));
      if (row === undefined || !canReadRequestRow(actorId, row)) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      return HttpResponse.json(successEnvelope(accessRequestView(row)));
    }),

    http.post("*/query-access-requests/:requestId/decisions", async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const row = world.accessRequests.get(String(params.requestId));
      if (row === undefined || !canReadRequestRow(actorId, row)) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      const body = (await request.json().catch(() => null)) as { decision?: string; comment?: string } | null;
      if (body?.decision !== "approve" && body?.decision !== "reject") {
        return HttpResponse.json(businessError(1001, "decision required"));
      }
      if (!approvalRequired()) return HttpResponse.json(businessError(1010, "approval disabled"));
      if (parseIfMatch(request) !== String(row.version)) {
        return HttpResponse.json(businessError(1004, "version mismatch"));
      }
      if (row.state !== "access_pending") return HttpResponse.json(businessError(1010, "not pending"));
      const step = activeStepOf(row);
      if (step === null) return HttpResponse.json(businessError(1010, "no active step"));
      const isActor = step.actors.includes(actorId);
      if (!isActor) return HttpResponse.json(businessError(3001, "not frozen actor"));
      const decidedAt = nowIso();
      if (body.decision === "reject") {
        step.state = "rejected";
        step.decided_at = decidedAt;
        for (const other of row.steps) if (other.state === "pending") other.state = "invalid";
        row.state = "access_rejected";
        row.version += 1;
      } else {
        step.state = "approved";
        step.decided_at = decidedAt;
        const next = row.steps.find((other) => other.position === step.position + 1);
        if (next !== undefined && next.state === "pending") {
          next.state = "active";
        } else {
          const grant = createGrantFromRequest(row);
          row.grant_id = grant.id;
          row.state = "grant_active";
        }
        row.version += 1;
      }
      return HttpResponse.json(successEnvelope(accessRequestView(row)));
    }),

    http.post("*/query-access-requests/:requestId/withdrawal", ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const row = world.accessRequests.get(String(params.requestId));
      if (row === undefined || row.requester_user_id !== actorId) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      if (parseIfMatch(request) !== String(row.version)) {
        return HttpResponse.json(businessError(1004, "version mismatch"));
      }
      if (row.state !== "access_pending") return HttpResponse.json(businessError(1010, "not pending"));
      row.state = "withdrawn";
      for (const step of row.steps) if (step.state !== "approved") step.state = "invalid";
      row.version += 1;
      return HttpResponse.json(successEnvelope(accessRequestView(row)));
    }),

    // ---------------------------------------------------------- grants --
    http.get("*/query-grants", ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const url = new URL(request.url);
      const rows = [...world.grants.values()]
        .filter((grant) => canReadGrantRow(actorId, grant))
        .sort((a, b) => b.created_at.localeCompare(a.created_at));
      return HttpResponse.json(
        successEnvelope(
          pageOf(rows.map(grantView), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after")),
        ),
      );
    }),

    http.get("*/query-grants/:grantId", ({ params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const grant = world.grants.get(String(params.grantId));
      if (grant === undefined || !canReadGrantRow(actorId, grant)) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      return HttpResponse.json(successEnvelope(grantView(grant)));
    }),

    http.post("*/query-grants/:grantId/revocations", async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const grant = world.grants.get(String(params.grantId));
      if (grant === undefined || !canReadGrantRow(actorId, grant)) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      const body = (await request.json().catch(() => null)) as { reason?: string } | null;
      if (body?.reason === undefined || body.reason === "" || body.reason.length > 4096) {
        return HttpResponse.json(businessError(1001, "reason required"));
      }
      if (parseIfMatch(request) !== String(grant.version)) {
        return HttpResponse.json(businessError(1004, "version mismatch"));
      }
      if (grant.state !== "active") return HttpResponse.json(businessError(4004, "grant not active"));
      const isReviewer = world.flowSteps.some((step) => step.actors.includes(actorId));
      if (!isReviewer && actorId !== "__builtin_admin__") {
        return HttpResponse.json(businessError(3001, "not frozen actor"));
      }
      grant.state = "revoked";
      grant.revoked_reason = body.reason;
      grant.version += 1;
      terminateSessionsOfGrant(grant.id, "revoked");
      emitMockSseEvent("io.yearning.v4.query_grant.revoked", {
        aggregate_id: grant.id,
        state: "revoked",
        subject: grant.requester_user_id,
      });
      return HttpResponse.json(successEnvelope(grantView(grant)));
    }),

    http.post("*/query-grants/:grantId/relinquishment", async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const grant = world.grants.get(String(params.grantId));
      if (grant === undefined || grant.requester_user_id !== actorId) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      const body = (await request.json().catch(() => null)) as { reason?: string } | null;
      if (body?.reason === undefined || body.reason === "" || body.reason.length > 4096) {
        return HttpResponse.json(businessError(1001, "reason required"));
      }
      if (parseIfMatch(request) !== String(grant.version)) {
        return HttpResponse.json(businessError(1004, "version mismatch"));
      }
      if (grant.state !== "active") return HttpResponse.json(businessError(4004, "grant not active"));
      grant.state = "relinquished";
      grant.version += 1;
      terminateSessionsOfGrant(grant.id, "closed");
      emitMockSseEvent("io.yearning.v4.query_grant.relinquished", {
        aggregate_id: grant.id,
        state: "relinquished",
        subject: grant.requester_user_id,
      });
      return HttpResponse.json(successEnvelope(grantView(grant)));
    }),

    http.post("*/query-grants/:grantId/renewal-requests", async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const grant = world.grants.get(String(params.grantId));
      if (grant === undefined || grant.requester_user_id !== actorId) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      const body = (await request.json().catch(() => null)) as { requested_until?: string; reason?: string } | null;
      if (!body?.requested_until || !body.reason || body.reason.length > 4096) {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
      if (grant.state !== "active") return HttpResponse.json(businessError(4004, "grant not active"));
      // Renewal creates a NEW access request from the current flow template;
      // the original grant is untouched.
      const row: FixtureAccessRequest = {
        id: uuid(),
        requester_user_id: grant.requester_user_id,
        flow_id: grant.flow_id,
        state: "access_pending",
        datasource_ids: grant.capabilities.map((capability) => capability.datasource_id),
        grant_id: null,
        reason: body.reason,
        requested_until: body.requested_until,
        steps: world.flowSteps.map((step, index) => ({
          position: step.position,
          actors: [...step.actors],
          state: index === 0 ? "active" : "pending",
          decided_at: null,
        })),
        version: 1,
        created_at: nowIso(),
      };
      world.accessRequests.set(row.id, row);
      return HttpResponse.json(successEnvelope(accessRequestView(row)));
    }),

    // -------------------------------------------------------- sessions --
    // RCP-20261021: combined query orders view (server-side merged pagination of requests + sessions).
    http.get("*/query/orders", ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const url = new URL(request.url);
      const keys = [
        ...[...world.accessRequests.values()]
          .filter((row) => canReadRequestRow(actorId, row))
          .map((row) => ({ id: row.id, created_at: row.created_at, kind: "access_request" as const })),
        ...[...world.sessions.values()]
          .filter((session) => session.user_id === actorId)
          .map((session) => ({ id: session.id, created_at: session.created_at, kind: "session" as const })),
      ].sort((a, b) => b.created_at.localeCompare(a.created_at));
      const pageKeys = pageOf(keys, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"));
      const entries = pageKeys.items.map((key) => {
        if (key.kind === "access_request") {
          const row = world.accessRequests.get(key.id);
          return row === undefined ? null : { kind: "access_request" as const, id: key.id, created_at: key.created_at, request: accessRequestView(row), session: null };
        }
        const row = world.sessions.get(key.id);
        return row === undefined ? null : { kind: "session" as const, id: key.id, created_at: key.created_at, request: null, session: sessionView(row) };
      }).filter((entry) => entry !== null);
      return HttpResponse.json(successEnvelope({ items: entries, page: pageKeys.page }));
    }),

    http.get("*/query-sessions", ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const url = new URL(request.url);
      const rows = [...world.sessions.values()]
        .filter((session) => session.user_id === actorId)
        .sort((a, b) => b.created_at.localeCompare(a.created_at));
      return HttpResponse.json(
        successEnvelope(
          pageOf(rows.map((row) => sessionView(row)), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after")),
        ),
      );
    }),

    http.post("*/query-sessions", async ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const actorId = queryActorUserId();
      const body = (await request.json().catch(() => null)) as {
        flow_id?: string;
        grant_id?: string | null;
        datasource_ids?: string[];
      } | null;
      if (
        body === null || body.flow_id !== QUERY_FIXTURE_FLOW_ID ||
        !Array.isArray(body.datasource_ids) || body.datasource_ids.length === 0
      ) {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
      if (!flowGrantEffective()) return HttpResponse.json(businessError(2014, "flow not granted"));
      const requested = new Set(body.datasource_ids);
      let capabilityRows: FixtureSessionCapability[];
      if (approvalRequired()) {
        const grant =
          body.grant_id !== undefined && body.grant_id !== null
            ? world.grants.get(body.grant_id)
            : undefined;
        if (grant === undefined || grant.requester_user_id !== actorId) {
          return HttpResponse.json(businessError(4001, "grant required"));
        }
        if (grant.state === "revoked") return HttpResponse.json(businessError(4004, "grant revoked"));
        if (grant.state !== "active") return HttpResponse.json(businessError(4005, "grant expired"));
        capabilityRows = activeGrantCapabilityRows(grant).filter((capability) =>
          requested.has(capability.datasource_id),
        );
        if (capabilityRows.length === 0) {
          return HttpResponse.json(businessError(4002, "datasource not granted"));
        }
        const session: FixtureQuerySession = {
          id: uuid(),
          user_id: actorId,
          flow_id: body.flow_id,
          grant_id: grant.id,
          state: "active",
          capabilities: capabilityRows,
          created_at: nowIso(),
        };
        world.sessions.set(session.id, session);
        return HttpResponse.json(successEnvelope(sessionView(session)));
      }
      // Approval disabled: freeze the flow's own capabilities (Q002).
      capabilityRows = flowCapabilityRows().filter((capability) => requested.has(capability.datasource_id));
      if (capabilityRows.length === 0) {
        return HttpResponse.json(businessError(4002, "datasource not granted"));
      }
      const session: FixtureQuerySession = {
        id: uuid(),
        user_id: actorId,
        flow_id: body.flow_id,
        grant_id: null,
        state: "active",
        capabilities: capabilityRows,
        created_at: nowIso(),
      };
      world.sessions.set(session.id, session);
      return HttpResponse.json(successEnvelope(sessionView(session)));
    }),

    http.get("*/query-sessions/:sessionId", ({ params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const session = world.sessions.get(String(params.sessionId));
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      return HttpResponse.json(successEnvelope(sessionView(session, true)));
    }),

    http.post("*/query-sessions/:sessionId/closure", async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const session = world.sessions.get(String(params.sessionId));
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      const body = (await request.json().catch(() => null)) as { reason?: string } | null;
      // §18.49: reason is OPTIONAL (backend session.go defaults empty to
      // "user_closed") — only the length needs guarding.
      if (body?.reason !== undefined && body.reason.length > 4096) {
        return HttpResponse.json(businessError(1001, "reason too long"));
      }
      if (session.state !== "active") return HttpResponse.json(businessError(4006, "session closed"));
      session.state = "closed";
      return HttpResponse.json(successEnvelope(sessionView(session)));
    }),

    // ------------------------------------------------------- metadata --
    http.get("*/query-sessions/:sessionId/metadata/schemas", ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const session = world.sessions.get(String(params.sessionId));
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      if (session.state !== "active") return HttpResponse.json(businessError(4006, "session closed"));
      const datasourceId = new URL(request.url).searchParams.get("datasource_id");
      const ds = world.datasources.get(datasourceId ?? "");
      if (ds === undefined) return HttpResponse.json(businessError(4002, "datasource not granted"));
      // F12 RCP-20261017 option A (No.010 family): {items} envelope (matches real backend).
      return HttpResponse.json(successEnvelope({ items: ds.schemas.map((schema) => ({ name: schema.name })) }));
    }),

    http.get("*/query-sessions/:sessionId/metadata/tables", ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const session = world.sessions.get(String(params.sessionId));
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      if (session.state !== "active") return HttpResponse.json(businessError(4006, "session closed"));
      const url = new URL(request.url);
      const ds = world.datasources.get(url.searchParams.get("datasource_id") ?? "");
      const schemaName = url.searchParams.get("schema_name") ?? "";
      const schema = ds?.schemas.find((s) => s.name === schemaName);
      if (ds === undefined || schema === undefined) {
        return HttpResponse.json(businessError(1001, "schema not found"));
      }
      return HttpResponse.json(
        successEnvelope(
          // F12 RCP-20261017 option A (No.010 family): {items} envelope.
          { items: schema.tables.map((table) => ({
            schema_name: schema.name,
            table_name: table.table_name,
            relation_kind: table.relation_kind,
          })) },
        ),
      );
    }),

    http.get("*/query-sessions/:sessionId/metadata/columns", ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const session = world.sessions.get(String(params.sessionId));
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      if (session.state !== "active") return HttpResponse.json(businessError(4006, "session closed"));
      const url = new URL(request.url);
      const ds = world.datasources.get(url.searchParams.get("datasource_id") ?? "");
      const schemaName = url.searchParams.get("schema_name") ?? "";
      const tableName = url.searchParams.get("table_name") ?? "";
      const table = ds?.schemas.find((s) => s.name === schemaName)?.tables.find((t) => t.table_name === tableName);
      if (ds === undefined || table === undefined) {
        return HttpResponse.json(businessError(1001, "table not found"));
      }
      // Live vocabulary drives the masked flags (query PRD §3).
      const vocabulary = vocabularyFor(session.flow_id, ds.id);
      return HttpResponse.json(
        successEnvelope(
          // F12 RCP-20261017 option A (No.010 family): {items} envelope.
          { items: table.columns.map((column) => ({
            schema_name: schemaName,
            table_name: table.table_name,
            column_name: column.column_name,
            ordinal: column.ordinal,
            data_type: column.data_type,
            nullable: column.nullable,
            masked: vocabulary.includes(foldCase(column.column_name)),
            column_key: column.column_key ?? "",
            column_default: column.column_default ?? "",
            extra: column.extra ?? "",
            comment: column.comment ?? "",
          })) },
        ),
      );
    }),

    // ------------------------------------------------------ executions --
    http.post("*/query-sessions/:sessionId/executions", async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const session = world.sessions.get(String(params.sessionId));
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      if (session.state === "revoked") return HttpResponse.json(businessError(4004, "grant revoked"));
      if (session.state === "expired") return HttpResponse.json(businessError(4005, "grant expired"));
      if (session.state !== "active") return HttpResponse.json(businessError(4006, "session closed"));
      const body = (await request.json().catch(() => null)) as {
        datasource_id?: string;
        schema_name?: string;
        sql?: string;
        timeout_ms?: number;
        page_size?: number;
      } | null;
      if (body === null) return HttpResponse.json(businessError(1001, "validation failed"));
      return HttpResponse.json(executeSelect(session, body));
    }),

    http.get("*/query-executions/:executionId/pages", ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
    ensureQueryWorld();
      const execution = world.executions.get(String(params.executionId));
      if (execution === undefined) return HttpResponse.json(businessError(1002, "not found"));
      const session = world.sessions.get(execution.session_id);
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      const url = new URL(request.url);
      const purpose = url.searchParams.get("purpose");
      const token = url.searchParams.get("cursor") ?? "";
      if (purpose !== "display" && purpose !== "export") {
        return HttpResponse.json(businessError(1001, "purpose required"));
      }
      const cursor = world.cursors.get(token);
      if (cursor === undefined || cursor.consumed || cursor.execution_id !== execution.id) {
        return HttpResponse.json(businessError(1009, "cursor expired"));
      }
      if (session.state === "revoked") return HttpResponse.json(businessError(4004, "grant revoked"));
      if (session.state !== "active") return HttpResponse.json(businessError(4006, "session closed"));
      if (purpose === "export") {
        const capability = session.capabilities.find((c) => c.datasource_id === execution.datasource_id);
        if (capability === undefined || !capability.can_export) {
          return HttpResponse.json(businessError(4003, "export not granted"));
        }
      }
      const pageSize = Number(url.searchParams.get("page_size") ?? cursor.pageSize);
      return HttpResponse.json(successEnvelope(cursorPage(execution, token, pageSize)));
    }),
  ];
}

/** Browser worker bootstrap: seed once from the stored scenario when it is a
 * query scenario so Playwright entry points start with a deterministic world. */

