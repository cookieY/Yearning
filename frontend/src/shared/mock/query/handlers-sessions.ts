import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIso } from "@/shared/mock/kit";
import { cursorPage, executeSelect, foldCase, vocabularyFor } from "./engine";
import { ensureQueryWorld } from "./seed";
import {
  type FixtureQuerySession,
  type FixtureSessionCapability,
  QUERY_FIXTURE_FLOW_ID,
  accessRequestView,
  activeGrantCapabilityRows,
  approvalRequired,
  businessError,
  canReadRequestRow,
  flowCapabilityRows,
  flowGrantEffective,
  pageOf,
  queryActorUserId,
  sessionAuthenticated,
  sessionView,
  successEnvelope,
  unauthenticated,
  world,
} from "./state";

export function querySessionHandlers(): HttpHandler[] {
  return [
    // -------------------------------------------------------- sessions --
    // RCP-20261021: combined query orders view (server-side merged pagination of requests + sessions).
    http.get(apiRoute("/query/orders"), ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const actorId = queryActorUserId();
      const url = new URL(request.url);
      const keys = [
        ...[...world.accessRequests.values()]
          .filter((row) => canReadRequestRow(actorId, row, request))
          .map((row) => ({ id: row.id, created_at: row.created_at, kind: "access_request" as const })),
        ...[...world.sessions.values()]
          .filter((session) => session.user_id === actorId)
          .map((session) => ({ id: session.id, created_at: session.created_at, kind: "session" as const })),
      ].sort((a, b) => b.created_at.localeCompare(a.created_at));
      const pageKeys = pageOf(keys, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"));
      const entries = pageKeys.items.map((key) => {
        if (key.kind === "access_request") {
          const row = world.accessRequests.get(key.id);
          return row === undefined ? null : { kind: "access_request" as const, id: key.id, created_at: key.created_at, request: accessRequestView(row, actorId, request) };
        }
        const row = world.sessions.get(key.id);
        return row === undefined ? null : { kind: "session" as const, id: key.id, created_at: key.created_at, session: sessionView(row) };
      }).filter((entry) => entry !== null);
      return HttpResponse.json(successEnvelope({ items: entries, page: pageKeys.page }));
    }),

    http.get(apiRoute("/query-sessions"), ({ request }) => {
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

    http.post(apiRoute("/query-sessions"), async ({ request }) => {
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

    http.get(apiRoute("/query-sessions/:sessionId"), ({ params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const session = world.sessions.get(String(params.sessionId));
      if (session === undefined || session.user_id !== queryActorUserId()) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      return HttpResponse.json(successEnvelope(sessionView(session, true)));
    }),

    http.post(apiRoute("/query-sessions/:sessionId/closure"), async ({ request, params }) => {
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
    http.get(apiRoute("/query-sessions/:sessionId/metadata/schemas"), ({ request, params }) => {
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

    http.get(apiRoute("/query-sessions/:sessionId/metadata/tables"), ({ request, params }) => {
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

    http.get(apiRoute("/query-sessions/:sessionId/metadata/columns"), ({ request, params }) => {
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
    http.post(apiRoute("/query-sessions/:sessionId/executions"), async ({ request, params }) => {
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

    http.get(apiRoute("/query-executions/:executionId/pages"), ({ request, params }) => {
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
