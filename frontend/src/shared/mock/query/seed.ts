import { MOCK_IDENTITY_IDS } from "@/shared/mock/identity-store";
import { fixtureUuid as uuid, nowIso } from "@/shared/mock/kit";
import { readStoredScenario } from "@/shared/mock/scenario-store";
import { MYSQL_SCHEMAS, PG_SCHEMAS } from "./engine";
import {
  type FixtureAccessRequest,
  type FixtureGrantCapability,
  QUERY_FIXTURE_DS_MYSQL_ID,
  QUERY_FIXTURE_DS_PG_ID,
  QUERY_FIXTURE_FLOW_ID,
  QUERY_FIXTURE_OTHER_USER_ID,
  QUERY_FIXTURE_SESSION_USER_ID,
  activeGrantCapabilityRows,
  createGrantFromRequest,
  flowCapabilityRows,
  flowGrantEffective,
  flowView,
  pageOf,
  queryState,
  terminateSessionsOfGrant,
  world,
} from "./state";

const DEFAULT_DEMO_SEED = "__default_demo__";

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
  world.flowSteps = [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner] }];
  world.maskingRules.set(`${QUERY_FIXTURE_FLOW_ID}:${QUERY_FIXTURE_DS_MYSQL_ID}`, [
    "email",
    "phone",
  ]);
  world.maskingRules.set(`${QUERY_FIXTURE_FLOW_ID}:${QUERY_FIXTURE_DS_PG_ID}`, ["payload"]);
}

/** Lazy world sync: every query handler calls this first so a query-*
 * scenario always has its seeded world regardless of browser worker
 * bootstrap timing (self-healing reseed). Non-query scenarios never clobber
 * a world that a test seeded explicitly — except the browser default world,
 * which carries the pending-approval demo layer below. */
export function ensureQueryWorld(): void {
  const scenario = readStoredScenario();
  if (scenario.startsWith("query-")) {
    if (scenario !== queryState.lastSeededScenario) {
      seedQueryScenario(scenario);
      queryState.lastSeededScenario = scenario;
    }
    return;
  }
  seedDefaultQueryDemo();
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
  if (queryState.lastSeededScenario === DEFAULT_DEMO_SEED) return;
  resetQueryFixture();
  seedBase();
  queryState.lastSeededScenario = DEFAULT_DEMO_SEED;
  const requests: Array<{
    id: string;
    datasourceIds: string[];
    reason: string;
    createdAt: string;
  }> = [
    {
      id: "6f0f2b3c-5555-4555-8555-000000000202",
      datasourceIds: [QUERY_FIXTURE_DS_MYSQL_ID],
      reason: "月末对账，需要查询订单库",
      createdAt: "2026-09-01T07:40:00Z",
    },
    {
      id: "6f0f2b3c-5555-4555-8555-000000000203",
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
      steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner], state: "active", decided_at: null }],
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

  world.grants.set("6f0f2b3c-5555-4555-8555-000000000301", {
    id: "6f0f2b3c-5555-4555-8555-000000000301",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "active",
    revoked_reason: null,
    expires_at: "2026-12-01T00:00:00Z",
    capabilities: [mysqlCap(true), pgCap(false)],
    version: 1,
    created_at: "2026-09-05T02:10:00Z",
  });
  world.grants.set("6f0f2b3c-5555-4555-8555-000000000302", {
    id: "6f0f2b3c-5555-4555-8555-000000000302",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "revoked",
    revoked_reason: "安全审计：临时排查权限统一收回",
    expires_at: null,
    capabilities: [mysqlCap(true)],
    version: 2,
    created_at: "2026-08-20T06:30:00Z",
  });
  world.grants.set("6f0f2b3c-5555-4555-8555-000000000303", {
    id: "6f0f2b3c-5555-4555-8555-000000000303",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "expired",
    revoked_reason: null,
    expires_at: "2026-09-10T00:00:00Z",
    capabilities: [pgCap(false)],
    version: 1,
    created_at: "2026-08-25T09:00:00Z",
  });

  world.accessRequests.set("6f0f2b3c-5555-4555-8555-000000000204", {
    id: "6f0f2b3c-5555-4555-8555-000000000204",
    requester_user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    state: "grant_active",
    datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID, QUERY_FIXTURE_DS_PG_ID],
    grant_id: "6f0f2b3c-5555-4555-8555-000000000301",
    reason: "周度经营分析，需要两库长期只读权限",
    requested_until: "2026-12-01T00:00:00Z",
    steps: [
      {
        position: 1,
        actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner],
        state: "approved",
        decided_at: "2026-09-05T02:10:00Z",
      },
    ],
    version: 2,
    created_at: "2026-09-05T01:55:00Z",
  });
  world.accessRequests.set("6f0f2b3c-5555-4555-8555-000000000205", {
    id: "6f0f2b3c-5555-4555-8555-000000000205",
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
        actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner],
        state: "rejected",
        decided_at: "2026-08-18T08:20:00Z",
      },
    ],
    version: 2,
    created_at: "2026-08-18T03:40:00Z",
  });
  world.accessRequests.set("6f0f2b3c-5555-4555-8555-000000000206", {
    id: "6f0f2b3c-5555-4555-8555-000000000206",
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
        actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner],
        state: "pending",
        decided_at: null,
      },
    ],
    version: 1,
    created_at: "2026-08-10T05:15:00Z",
  });

  world.sessions.set("6f0f2b3c-5555-4555-8555-000000000401", {
    id: "6f0f2b3c-5555-4555-8555-000000000401",
    user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    grant_id: "6f0f2b3c-5555-4555-8555-000000000301",
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
  world.sessions.set("6f0f2b3c-5555-4555-8555-000000000402", {
    id: "6f0f2b3c-5555-4555-8555-000000000402",
    user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    grant_id: "6f0f2b3c-5555-4555-8555-000000000301",
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
  world.sessions.set("6f0f2b3c-5555-4555-8555-000000000403", {
    id: "6f0f2b3c-5555-4555-8555-000000000403",
    user_id: MOCK_IDENTITY_IDS.submitter,
    flow_id: QUERY_FIXTURE_FLOW_ID,
    grant_id: "6f0f2b3c-5555-4555-8555-000000000303",
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
  queryState.lastSeededScenario = null;
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
        steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner], state: "approved", decided_at: nowIso() }],
        version: 2,
        created_at: new Date(Date.now() - 3600 * 1000).toISOString(),
      };
      const grant = createGrantFromRequest(request);
      request.grant_id = grant.id;
      world.accessRequests.set(request.id, request);
      world.sessions.set("6f0f2b3c-5555-4555-8555-000000000101", {
        id: "6f0f2b3c-5555-4555-8555-000000000101",
        user_id: QUERY_FIXTURE_SESSION_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        grant_id: grant.id,
        state: "active",
        capabilities: activeGrantCapabilityRows(grant),
        created_at: new Date(Date.now() - 1800 * 1000).toISOString(),
      });
      // Another user's session: never visible to the session identity, which
      // keeps the owner-scoped 404 path (admin included) observable.
      world.sessions.set("6f0f2b3c-5555-4555-8555-000000000102", {
        id: "6f0f2b3c-5555-4555-8555-000000000102",
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
        steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner], state: "approved", decided_at: nowIso() }],
        version: 2,
        created_at: new Date(Date.now() - 3600 * 1000).toISOString(),
      };
      const grant = createGrantFromRequest(request);
      request.grant_id = grant.id;
      world.accessRequests.set(request.id, request);
      world.sessions.set("6f0f2b3c-5555-4555-8555-000000000101", {
        id: "6f0f2b3c-5555-4555-8555-000000000101",
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
        id: "6f0f2b3c-5555-4555-8555-000000000201",
        requester_user_id: QUERY_FIXTURE_OTHER_USER_ID,
        flow_id: QUERY_FIXTURE_FLOW_ID,
        state: "access_pending",
        datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
        grant_id: null,
        reason: "month-end reconciliation",
        requested_until: until,
        steps: [{ position: 1, actors: [MOCK_IDENTITY_IDS.reviewer, MOCK_IDENTITY_IDS.owner], state: "active", decided_at: null }],
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
