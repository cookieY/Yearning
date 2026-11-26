import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { flowViewOf, flowWriteError } from "./helpers";
import {
  adminGuard,
  businessError,
  pageOf,
  requireSession,
  sanitizeMarkdown,
  sha256Hex,
  successEnvelope,
  taskResponse,
  versionMismatch,
  world,
} from "./state";
import type {
  FixtureAnnouncementRevision,
  FixtureFlow,
  FixtureFlowStage,
  FixtureIdentityProvider,
} from "./types";

export function adminFlowsSiteHandlers(): HttpHandler[] {
  return [
    http.post(apiRoute("/admin/flows"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body required");
      const writeError = flowWriteError(body);
      if (writeError !== null) return businessError(1108, writeError);
      const flowType = body.flow_type as FixtureFlow["flow_type"];
      const flow: FixtureFlow = {
        id: uuid(),
        name: body.name as string,
        flow_type: flowType,
        enabled: body.enabled as boolean,
        rule_set_id: body.rule_set_id === undefined ? null : (body.rule_set_id as string | null),
        stages: flowType === "change_review" ? ((body.stages ?? null) as FixtureFlowStage[] | null) : null,
        approval_steps: flowType === "query_access" ? ((body.approval_steps ?? null) as FixtureFlow["approval_steps"]) : null,
        query_capabilities:
          flowType === "query_access" ? ((body.query_capabilities as FixtureFlow["query_capabilities"]) ?? null) : null,
        version: 1,
        created_at: now(),
        updated_at: now(),
      };
      world.flows.set(flow.id, flow);
      return HttpResponse.json(successEnvelope(flowViewOf(flow)));
    }),

    http.get(apiRoute("/admin/flows/:flowId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      return HttpResponse.json(successEnvelope(flowViewOf(flow)));
    }),

    http.put(apiRoute("/admin/flows/:flowId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      if (versionMismatch(request, flow.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body required");
      const writeError = flowWriteError(body);
      if (writeError !== null) return businessError(1108, writeError);
      const flowType = body.flow_type as FixtureFlow["flow_type"];
      flow.name = body.name as string;
      flow.enabled = body.enabled as boolean;
      flow.rule_set_id = body.rule_set_id === undefined ? null : (body.rule_set_id as string | null);
      flow.stages = flowType === "change_review" ? ((body.stages ?? null) as FixtureFlowStage[] | null) : null;
      flow.approval_steps =
        flowType === "query_access" ? ((body.approval_steps ?? null) as FixtureFlow["approval_steps"]) : null;
      flow.query_capabilities =
        flowType === "query_access" ? ((body.query_capabilities as FixtureFlow["query_capabilities"]) ?? null) : null;
      flow.version += 1;
      flow.updated_at = now();
      return HttpResponse.json(successEnvelope(flowViewOf(flow)));
    }),

    http.delete(apiRoute("/admin/flows/:flowId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      if (versionMismatch(request, flow.version)) return businessError(1004, "version mismatch");
      const referenced = [...world.groups.values()].some((group) =>
        group.granted_flow_ids.includes(flow.id),
      );
      if (referenced) return businessError(1106, "flow referenced by permission groups");
      world.flows.delete(flow.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.get(apiRoute("/admin/flows/:flowId/masking-rules"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      if (flow.flow_type !== "query_access") {
        return businessError(1002, "flow not found");
      }
      const items = flow.query_capabilities?.map((capability) => {
        const vocabulary = world.maskingRules.get(`${flow.id}:${capability.datasource_id}`) ?? [];
        return {
          datasource_id: capability.datasource_id,
          sensitive_columns: [...vocabulary],
          version: 1,
        };
      });
      return HttpResponse.json(successEnvelope({ items: items ?? [] }));
    }),

    http.put(apiRoute("/admin/flows/:flowId/datasources/:datasourceId/masking-rules"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      if (flow.flow_type !== "query_access") {
        return businessError(1002, "flow not found");
      }
      const datasourceId = String(params.datasourceId);
      const belongs = flow.query_capabilities?.some((capability) => capability.datasource_id === datasourceId);
      if (belongs !== true) return businessError(1002, "datasource not found");
      const body = (await request.json().catch(() => null)) as {
        sensitive_columns?: string[];
      } | null;
      if (
        body === null || !Array.isArray(body.sensitive_columns) ||
        body.sensitive_columns.length > 256 ||
        body.sensitive_columns.some(
          (column) => typeof column !== "string" || column.length < 1 || column.length > 128,
        )
      ) {
        return businessError(1001, "sensitive_columns invalid");
      }
      const folded = [...new Set(body.sensitive_columns.map((column) => column.trim().toLocaleLowerCase("en-US")))];
      world.maskingRules.set(`${flow.id}:${datasourceId}`, folded);
      return HttpResponse.json(
        successEnvelope({ datasource_id: datasourceId, sensitive_columns: folded, version: 1 }),
      );
    }),

    http.get(apiRoute("/announcements/current"), () => {
      const guard = requireSession();
      if (guard !== null) return guard;
      const revisionId = world.announcementPublication.revision_id;
      if (revisionId === null) return HttpResponse.json(successEnvelope(null));
      const revision = world.announcementRevisions.get(revisionId);
      if (revision === undefined) return HttpResponse.json(successEnvelope(null));
      return HttpResponse.json(
        successEnvelope({
          revision,
          published_by_username: world.announcementPublication.published_by,
          published_at: world.announcementPublication.published_at,
          version: world.announcementPublication.version,
        }),
      );
    }),

    http.get(apiRoute("/admin/announcement-revisions"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.announcementRevisions.values()].sort(
        (a, b) => b.revision_number - a.revision_number,
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post(apiRoute("/admin/announcement-revisions"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as {
        title?: string;
        markdown_source?: string;
      } | null;
      if (
        body === null || typeof body.title !== "string" || body.title.trim() === "" ||
        body.title.length > 200 || typeof body.markdown_source !== "string" ||
        body.markdown_source.length < 1 || body.markdown_source.length > 20000
      ) {
        return businessError(1001, "validation failed");
      }
      const revisionNumber =
        Math.max(0, ...[...world.announcementRevisions.values()].map((row) => row.revision_number)) + 1;
      const revision: FixtureAnnouncementRevision = {
        id: uuid(),
        revision_number: revisionNumber,
        title: body.title,
        sanitized_html: sanitizeMarkdown(body.markdown_source),
        content_sha256: sha256Hex(body.markdown_source),
        sanitizer_policy_version: "mock-sanitizer-1",
        created_by_username: "admin",
        created_at: now(),
      };
      world.announcementRevisions.set(revision.id, revision);
      return HttpResponse.json(successEnvelope(revision));
    }),

    http.put(apiRoute("/admin/announcement-publication"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as {
        announcement_revision_id?: string;
      } | null;
      if (body === null || typeof body.announcement_revision_id !== "string") {
        return businessError(1001, "revision required");
      }
      const revision = world.announcementRevisions.get(body.announcement_revision_id);
      if (revision === undefined) return businessError(1002, "revision not found");
      if (versionMismatch(request, world.announcementPublication.version)) {
        return businessError(1004, "version mismatch");
      }
      world.announcementPublication = {
        revision_id: revision.id,
        version: world.announcementPublication.version + 1,
        published_by: "admin",
        published_at: now(),
      };
      return HttpResponse.json(
        successEnvelope({
          revision,
          published_by_username: "admin",
          published_at: world.announcementPublication.published_at,
          version: world.announcementPublication.version,
        }),
      );
    }),

    http.get(apiRoute("/admin/audit-events"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.auditEvents].sort((a, b) => b.occurred_at.localeCompare(a.occurred_at));
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.get(apiRoute("/admin/identity-providers"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.identityProviders.values()].sort((a, b) =>
        a.provider_key.localeCompare(b.provider_key),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items.map(({ clientSecret: _clientSecret, ...view }) => view), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post(apiRoute("/admin/identity-providers"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.provider_key !== "string") return businessError(1001, "provider_key required");
      const kind = body.provider_kind;
      if (kind !== "ldap" && kind !== "oidc") return businessError(1001, "provider_kind invalid");
      if (kind === "ldap" && [...world.identityProviders.values()].some((p) => p.provider_kind === "ldap")) {
        return businessError(1001, "ldap is a singleton");
      }
      const secret =
        kind === "oidc"
          ? (body.client_secret as { value?: string } | null | undefined)
          : (body.bind_password as { value?: string } | null | undefined);
      if (secret !== null && secret !== undefined && secret.value === "") {
        return businessError(1001, "secret value must not be empty");
      }
      if (secret?.value === undefined) return businessError(1001, "secret required on create");
      const provider: FixtureIdentityProvider = {
        id: uuid(),
        provider_key: body.provider_key,
        provider_kind: kind,
        display_name: body.display_name === undefined ? body.provider_key : (body.display_name as string),
        enabled: body.enabled === undefined ? true : (body.enabled as boolean),
        configuration: body.configuration as FixtureIdentityProvider["configuration"],
        secret_configured: true,
        version: 1,
        created_at: now(),
        updated_at: now(),
        clientSecret: secret.value,
      };
      world.identityProviders.set(provider.id, provider);
      const { clientSecret: _clientSecret, ...view } = provider;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.get(apiRoute("/admin/identity-providers/:providerId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      const { clientSecret: _clientSecret, ...view } = provider;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.put(apiRoute("/admin/identity-providers/:providerId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body required");
      const secret =
        provider.provider_kind === "oidc"
          ? (body.client_secret as { value: string | null } | null | undefined)
          : (body.bind_password as { value: string | null } | null | undefined);
      if (secret !== null && secret !== undefined && secret.value === "") {
        return businessError(1001, "secret value must not be empty");
      }
      const clearing = secret === null || secret?.value === null;
      if (clearing && provider.provider_kind === "oidc" && provider.enabled) {
        return businessError(1001, "oidc secret cannot be cleared while enabled");
      }
      provider.display_name = body.display_name === undefined ? provider.display_name : (body.display_name as string);
      provider.enabled = body.enabled === undefined ? provider.enabled : (body.enabled as boolean);
      provider.configuration = (body.configuration ?? provider.configuration) as FixtureIdentityProvider["configuration"];
      if (secret !== undefined && secret !== null) {
        if (secret.value !== null) {
          provider.clientSecret = secret.value;
          provider.secret_configured = true;
        } else {
          provider.clientSecret = null;
          provider.secret_configured = false;
        }
      }
      provider.version += 1;
      provider.updated_at = now();
      const { clientSecret: _clientSecret, ...view } = provider;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.delete(apiRoute("/admin/identity-providers/:providerId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (provider.enabled) return businessError(1001, "disable before delete");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      world.identityProviders.delete(provider.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post(apiRoute("/admin/identity-providers/:providerId/connection-tests"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      return taskResponse("identity_provider_connection_test", provider.id, () => {});
    }),
  ];
}
