import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { readIdempotencyKeyHeader } from "@/shared/api/mutator";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIso, parseIfMatch } from "@/shared/mock/kit";
import { emitMockSseEvent } from "@/shared/mock/sse-mock";
import { ensureQueryWorld } from "./seed";
import {
  type FixtureAccessRequest,
  QUERY_FIXTURE_FLOW_ID,
  accessRequestView,
  activeStepOf,
  allowsReviewerViewAs,
  approvalRequired,
  businessError,
  canReadGrantRow,
  canReadRequestRow,
  createGrantFromRequest,
  flowGrantEffective,
  grantView,
  isReviewerOfRequestRow,
  pageOf,
  queryActorUserId,
  sessionAuthenticated,
  successEnvelope,
  terminateSessionsOfGrant,
  unauthenticated,
  world,
} from "./state";

export function queryAccessHandlers(): HttpHandler[] {
  return [
    // ------------------------------------------------- access requests --
    http.get(apiRoute("/query-access-requests"), ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const actorId = queryActorUserId();
      const url = new URL(request.url);
      // RCP-20261021: server-side state and scope filters (mirrors backend ListAccessRequests).
      const stateFilter = url.searchParams.get("state");
      const scopeFilter = url.searchParams.get("scope");
      if (scopeFilter !== null && scopeFilter !== "" && scopeFilter !== "all" && scopeFilter !== "reviewer" && scopeFilter !== "requester") {
        return HttpResponse.json(businessError(1001, "invalid scope"));
      }
      const rows = [...world.accessRequests.values()]
        .filter((row) => {
          if (scopeFilter === "reviewer") return isReviewerOfRequestRow(actorId, row, request);
          if (scopeFilter === "requester") return row.requester_user_id === actorId;
          return canReadRequestRow(actorId, row, request);
        })
        .filter((row) => stateFilter === null || stateFilter === "" || row.state === stateFilter)
        .sort((a, b) => b.created_at.localeCompare(a.created_at));
      return HttpResponse.json(
        successEnvelope(
          pageOf(rows.map((row) => accessRequestView(row, actorId, request)), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after")),
        ),
      );
    }),

    http.post(apiRoute("/query-access-requests"), async ({ request }) => {
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
      return HttpResponse.json(successEnvelope(accessRequestView(row, actorId, request)));
    }),

    http.get(apiRoute("/query-access-requests/:requestId"), ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const actorId = queryActorUserId();
      const row = world.accessRequests.get(String(params.requestId));
      if (row === undefined || !canReadRequestRow(actorId, row, request)) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      return HttpResponse.json(successEnvelope(accessRequestView(row, actorId, request)));
    }),

    http.post(apiRoute("/query-access-requests/:requestId/decisions"), async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const actorId = queryActorUserId();
      const row = world.accessRequests.get(String(params.requestId));
      if (row === undefined || !canReadRequestRow(actorId, row, request)) {
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
      const isActor = allowsReviewerViewAs(request) && step.actors.includes(actorId);
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
      return HttpResponse.json(successEnvelope(accessRequestView(row, actorId, request)));
    }),

    http.post(apiRoute("/query-access-requests/:requestId/withdrawal"), ({ request, params }) => {
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
      return HttpResponse.json(successEnvelope(accessRequestView(row, actorId, request)));
    }),

    // ---------------------------------------------------------- grants --
    http.get(apiRoute("/query-grants"), ({ request }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const actorId = queryActorUserId();
      const url = new URL(request.url);
      const rows = [...world.grants.values()]
        .filter((grant) => canReadGrantRow(actorId, grant, request))
        .sort((a, b) => b.created_at.localeCompare(a.created_at));
      return HttpResponse.json(
        successEnvelope(
          pageOf(rows.map((grant) => grantView(grant, actorId, request)), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after")),
        ),
      );
    }),

    http.get(apiRoute("/query-grants/:grantId"), ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const actorId = queryActorUserId();
      const grant = world.grants.get(String(params.grantId));
      if (grant === undefined || !canReadGrantRow(actorId, grant, request)) {
        return HttpResponse.json(businessError(1002, "not found"));
      }
      return HttpResponse.json(successEnvelope(grantView(grant, actorId, request)));
    }),

    http.post(apiRoute("/query-grants/:grantId/revocations"), async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      ensureQueryWorld();
      const actorId = queryActorUserId();
      const grant = world.grants.get(String(params.grantId));
      if (grant === undefined || !canReadGrantRow(actorId, grant, request)) {
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
      const isReviewer =
        allowsReviewerViewAs(request) && world.flowSteps.some((step) => step.actors.includes(actorId));
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
      return HttpResponse.json(successEnvelope(grantView(grant, actorId, request)));
    }),

    http.post(apiRoute("/query-grants/:grantId/relinquishment"), async ({ request, params }) => {
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
      return HttpResponse.json(successEnvelope(grantView(grant, actorId, request)));
    }),

    http.post(apiRoute("/query-grants/:grantId/renewal-requests"), async ({ request, params }) => {
      if (!sessionAuthenticated()) return unauthenticated();
      const idemKey = readIdempotencyKeyHeader(request);
      if (idemKey === "" || idemKey.length > 128) {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
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
      return HttpResponse.json(successEnvelope(accessRequestView(row, actorId, request)));
    }),
  ];
}
