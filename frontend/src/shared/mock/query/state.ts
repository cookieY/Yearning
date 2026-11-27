import { createMockResponder, fixtureUuid as uuid, nowIso } from "@/shared/mock/kit";
import { readStoredAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { currentIdentityId, readDevViewAs } from "@/shared/mock/identity-store";
import { settingsBridge } from "@/shared/mock/settings-bridge";

const MOCK_REQUEST_ID = "46464646-4646-4646-8466-464646464646";

/** The mock session identity (auth-handlers UUID) — ordinary query user. */
export const QUERY_FIXTURE_SESSION_USER_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";
/** A second fixture user owning "someone else's" query objects. */
export const QUERY_FIXTURE_OTHER_USER_ID = "6f0f2b3c-1111-4111-8111-00000000c101";
export const QUERY_FIXTURE_FLOW_ID = "6f0f2b3c-2222-4222-8222-00000000f201";
export const QUERY_FIXTURE_DS_MYSQL_ID = "6f0f2b3c-3333-4333-8333-00000000a301";
export const QUERY_FIXTURE_DS_PG_ID = "6f0f2b3c-3333-4333-8333-00000000a302";

export const QUERY_FLOW_NAME = "online-readonly";

export type QueryScenario =
  | "query-flow"
  | "query-flow-direct"
  | "query-session"
  | "query-revoked"
  | "query-approval";

export interface FixtureColumnDef {
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

export interface FixtureTableDef {
  table_name: string;
  relation_kind: "table" | "view";
  columns: FixtureColumnDef[];
  /** Deterministic row count served through the cursor. */
  rowCount: number;
}

export interface FixtureSchemaDef {
  name: string;
  tables: FixtureTableDef[];
}

export interface FixtureQueryDatasource {
  id: string;
  name: string;
  compatibility_mode: "mysql" | "postgresql";
  schemas: FixtureSchemaDef[];
}

export interface FixtureFlowCapability {
  datasource_id: string;
  can_query: true;
  can_export: boolean;
}

export interface FixtureApprovalStep {
  position: number;
  /** Frozen reviewer snapshot; the mock session user reviews scenario seeds. */
  actors: string[];
  state: "pending" | "active" | "approved" | "rejected" | "invalid";
  decided_at: string | null;
}

export interface FixtureAccessRequest {
  id: string;
  requester_user_id: string;
  flow_id: string;
  state: "access_pending" | "grant_active" | "access_rejected" | "withdrawn" | "invalid";
  datasource_ids: string[];
  grant_id: string | null;
  /** Application reason included in the OpenAPI QueryAccessRequest view. */
  reason: string;
  requested_until: string;
  steps: FixtureApprovalStep[];
  version: number;
  created_at: string;
}

export interface FixtureGrantCapability {
  datasource_id: string;
  datasource_name_snapshot: string;
  can_query: true;
  can_export: boolean;
}

export interface FixtureGrant {
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

export interface FixtureSessionCapability {
  datasource_id: string;
  datasource_name: string;
  state: "active" | "datasource_unavailable" | "identity_changed";
  can_query: true;
  can_export: boolean;
}

export interface FixtureQuerySession {
  id: string;
  user_id: string;
  flow_id: string;
  grant_id: string | null;
  state: "active" | "closed" | "revoked" | "expired" | "user_deleted";
  capabilities: FixtureSessionCapability[];
  created_at: string;
}

export interface FixtureExecution {
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

export interface FixtureCursor {
  token: string;
  execution_id: string;
  session_id: string;
  datasource_id: string;
  offset: number;
  pageSize: number;
  consumed: boolean;
}

export interface QueryWorld {
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

export const world: QueryWorld = {
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

export const queryState: { lastSeededScenario: string | null } = {
  lastSeededScenario: null,
};

export const {
  successEnvelope,
  businessErrorPayload: businessError,
  unauthenticated,
  pageOf,
} = createMockResponder(MOCK_REQUEST_ID, 50);

/** The builtin admin identity gains no query read face: when the mock
 * session runs the admin behavior the query actor owns nothing and relates
 * to nothing (authorization-policy admin_is_not_business_override). Super
 * mode (owner 2026-11-03) suspends that split in the dev browser: the
 * session keeps the henry query world alongside the admin capability. */
export function queryActorUserId(): string {
  return readStoredAuthBehavior() === "admin" ? "__builtin_admin__" : currentIdentityId();
}

/** Session presence follows the admin fixture convention: jsdom fetches
 * carry no document cookies, so the behavior dimension (never "expired")
 * is the session-presence signal shared by all component tests. */
export function sessionAuthenticated(): boolean {
  return readStoredAuthBehavior() !== "expired";
}

/** Q002: approval is required only when BOTH the scenario world enables it
 * AND the admin settings knob is on — the settings toggle (§18.47) opens
 * the direct path without touching the scenario. */
export function approvalRequired(): boolean {
  return world.approvalEnabled && settingsBridge.queryApprovalEnabled;
}

/** Mock approximation for the Q002 direct path: the flow's group-grant
 * cannot be driven from the admin UI here (separate fixture worlds), so
 * with approval disabled the visible flow is treated as granted — the
 * demo-world equivalent of "the group already holds the query flow". With
 * approval on, world.flowGranted keeps its exact scenario semantics. */
export function flowGrantEffective(): boolean {
  return world.flowGranted || !approvalRequired();
}

export function accessRequestView(row: FixtureAccessRequest, actorId?: string, request?: Request) {
  return {
    id: row.id,
    flow_id: row.flow_id,
    requester_user_id: row.requester_user_id,
    state: row.state,
    datasource_ids: [...row.datasource_ids],
    grant_id: row.grant_id,
    reason: row.reason,
    requested_until: row.requested_until,
    version: row.version,
    created_at: row.created_at,
    can_decide: actorId !== undefined && canDecideRequestRow(actorId, row, request),
  };
}

export function grantView(row: FixtureGrant, actorId?: string, request?: Request) {
  return {
    id: row.id,
    requester_user_id: row.requester_user_id,
    state: row.state,
    revoked_reason: row.revoked_reason,
    expires_at: row.expires_at,
    version: row.version,
    created_at: row.created_at,
    can_revoke: actorId !== undefined && canRevokeGrantRow(actorId, row, request),
  };
}

export function sessionView(row: FixtureQuerySession, includeExecutions = false) {
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

export function flowView() {
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
    query_capabilities: world.flowCapabilities.map((capability) => ({
      ...capability,
      datasource_name: world.datasources.get(capability.datasource_id)?.name ?? capability.datasource_id,
    })),
    version: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  };
}

export function terminateSessionsOfGrant(grantId: string, state: "revoked" | "expired" | "closed") {
  for (const session of world.sessions.values()) {
    if (session.grant_id === grantId && session.state === "active") {
      session.state = state;
    }
  }
}

export function createGrantFromRequest(request: FixtureAccessRequest): FixtureGrant {
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
  const capByDS = new Map(grant.capabilities.map((cap) => [cap.datasource_id, cap]));
  for (const session of world.sessions.values()) {
    if (
      session.state !== "active" ||
      session.user_id !== grant.requester_user_id ||
      session.grant_id === null ||
      session.grant_id === grant.id
    ) {
      continue;
    }
    const oldGrant = world.grants.get(session.grant_id);
    if (oldGrant === undefined || oldGrant.flow_id !== grant.flow_id) continue;
    if (
      oldGrant.state === "active" &&
      oldGrant.expires_at !== null &&
      grant.expires_at !== null &&
      oldGrant.expires_at > grant.expires_at
    ) {
      continue;
    }
    if (session.capabilities.length === 0) continue;
    if (session.capabilities.some((cap) => !capByDS.has(cap.datasource_id))) continue;
    session.grant_id = grant.id;
    session.capabilities = session.capabilities.map((cap) => ({
      ...cap,
      can_export: capByDS.get(cap.datasource_id)?.can_export ?? cap.can_export,
    }));
  }
  return grant;
}

export function activeGrantCapabilityRows(grant: FixtureGrant): FixtureSessionCapability[] {
  return grant.capabilities.map((capability) => ({
    datasource_id: capability.datasource_id,
    datasource_name: capability.datasource_name_snapshot,
    state: "active",
    can_query: true,
    can_export: capability.can_export,
  }));
}

export function flowCapabilityRows(): FixtureSessionCapability[] {
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

export function allowsReviewerViewAs(request?: Request): boolean {
  const viewAs = readDevViewAs(request);
  return viewAs !== "executor" && viewAs !== "user";
}

export function isReviewerOfRequestRow(actorId: string, row: FixtureAccessRequest, request?: Request): boolean {
  return allowsReviewerViewAs(request) && row.steps.some((step) => step.actors.includes(actorId));
}

export function canDecideRequestRow(actorId: string, row: FixtureAccessRequest, request?: Request): boolean {
  return row.state === "access_pending" && allowsReviewerViewAs(request) && (activeStepOf(row)?.actors.includes(actorId) ?? false);
}

export function canRevokeGrantRow(actorId: string, grant: FixtureGrant, request?: Request): boolean {
  return grant.state === "active" && allowsReviewerViewAs(request) && (actorId === "__builtin_admin__" || world.flowSteps.some((step) => step.actors.includes(actorId)));
}

export function canReadGrantRow(actorId: string, grant: FixtureGrant, request?: Request): boolean {
  return grant.requester_user_id === actorId || (allowsReviewerViewAs(request) && world.flowSteps.some((step) => step.actors.includes(actorId)));
}

export function canReadRequestRow(actorId: string, row: FixtureAccessRequest, request?: Request): boolean {
  return row.requester_user_id === actorId || isReviewerOfRequestRow(actorId, row, request);
}

export function activeStepOf(row: FixtureAccessRequest): FixtureApprovalStep | null {
  return row.steps.find((step) => step.state === "active") ?? null;
}
