import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import type { AiProvider, Datasource } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import {
  applyCredentialReplacement,
  datasourceView,
  providerConfigurationError,
  providerView,
  testPurposeOfWrite,
  tlsFromWrite,
  touchedCredentialPurposes,
  validateDatasourceWrite,
} from "./helpers";
import { PURPOSES } from "./ids";
import {
  adminGuard,
  businessError,
  matchesQ,
  mockLatencyMs,
  pageOf,
  searchOf,
  sleep,
  successEnvelope,
  taskResponse,
  versionMismatch,
  world,
} from "./state";
import type { CredentialLike, FixtureDatasource, FixtureProvider } from "./types";

export function adminDatasourcesProvidersHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/admin/datasources"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const engine = url.searchParams.get("engine");
      const compatibilityMode = url.searchParams.get("compatibility_mode");
      const credentialMode = url.searchParams.get("credential_mode");
      const referenced = url.searchParams.get("referenced");
      const items = [...world.datasources.values()]
        .filter((ds) => engine === null || ds.engine === engine)
        .filter((ds) => compatibilityMode === null || ds.compatibility_mode === compatibilityMode)
        .filter((ds) => {
          if (credentialMode === null) return true;
          const readWrite = ds.credentials.execution !== undefined;
          return (credentialMode === "read_write") === readWrite;
        })
        .filter((ds) => {
          if (referenced === null) return true;
          const isReferenced = [...world.flows.values()].some((flow) =>
            flow.stages?.some((stage) => stage.datasource_id === ds.id) === true ||
            flow.query_capabilities?.some((cap) => cap.datasource_id === ds.id) === true,
          );
          return (referenced === "true") === isReferenced;
        })
        .filter((ds) => matchesQ([ds.name, ds.host], searchOf(url)))
        .map(datasourceView);
      await sleep(mockLatencyMs());
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),

    http.post(apiRoute("/admin/datasources"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const validationError = validateDatasourceWrite(body);
      if (validationError !== null) return businessError(1001, validationError);
      const duplicate = [...world.datasources.values()].some((ds) => ds.name === body.name);
      if (duplicate) return businessError(1005, "datasource name already exists");
      const applied = applyCredentialReplacement({}, body.credentials as CredentialLike[]);
      if (typeof applied === "string") return businessError(1001, applied);
      const tls = tlsFromWrite(body);
      const ts = now();
      const id = uuid();
      const ds: FixtureDatasource = {
        id,
        name: body.name as string,
        engine: body.engine as Datasource["engine"],
        compatibility_mode: body.compatibility_mode as Datasource["compatibility_mode"],
        deployment_kind: body.deployment_kind as Datasource["deployment_kind"],
        host: typeof body.host === "string" ? body.host : "",
        port: Number(body.port ?? 0),
        database_name: (body.database_name as string | null) ?? null,
        enabled: Boolean(body.enabled),
        test_purpose: testPurposeOfWrite(body),
        credential_status: {},
        referenced_by_flow_count: 0,
        version: 1,
        created_at: ts,
        updated_at: ts,
        credentials: applied,
        tls,
        capabilities: null,
      };
      touchedCredentialPurposes(ds);
      world.datasources.set(id, ds);
      return HttpResponse.json(successEnvelope(datasourceView(ds)));
    }),

    http.get(apiRoute("/admin/datasources/:datasourceId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      return HttpResponse.json(successEnvelope(datasourceView(ds)));
    }),

    http.put(apiRoute("/admin/datasources/:datasourceId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      if (versionMismatch(request, ds.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const validationError = validateDatasourceWrite(body);
      if (validationError !== null) return businessError(1001, validationError);
      const duplicate = [...world.datasources.values()].some(
        (other) => other.id !== ds.id && other.name === body.name,
      );
      if (duplicate) return businessError(1005, "datasource name already exists");
      const applied = applyCredentialReplacement(ds.credentials, body.credentials as CredentialLike[]);
      if (typeof applied === "string") return businessError(1001, applied);
      ds.tls = tlsFromWrite(body);
      if (typeof body.name === "string" && body.name !== "") ds.name = body.name;
      ds.engine = body.engine as Datasource["engine"];
      ds.compatibility_mode = body.compatibility_mode as Datasource["compatibility_mode"];
      ds.deployment_kind = body.deployment_kind as Datasource["deployment_kind"];
      if (typeof body.host === "string") ds.host = body.host;
      if (typeof body.port === "number") ds.port = body.port;
      if (typeof body.enabled === "boolean") ds.enabled = body.enabled;
      if ("database_name" in body) ds.database_name = (body.database_name as string | null) ?? null;
      ds.test_purpose = testPurposeOfWrite(body);
      ds.credentials = applied;
      ds.version += 1;
      ds.updated_at = now();
      touchedCredentialPurposes(ds);
      return HttpResponse.json(successEnvelope(datasourceView(ds)));
    }),

    http.delete(apiRoute("/admin/datasources/:datasourceId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      if (versionMismatch(request, ds.version)) return businessError(1004, "version mismatch");
      if ((ds.referenced_by_flow_count ?? 0) > 0) {
        return businessError(1107, "datasource is still referenced by flows");
      }
      world.datasources.delete(ds.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post(apiRoute("/admin/datasources/:datasourceId/connection-tests"), async ({ params, request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      const body = (await request.json().catch(() => null)) as { purpose?: string } | null;
      if (body === null || !PURPOSES.includes(body.purpose as (typeof PURPOSES)[number])) {
        return businessError(1001, "connection test purpose is required");
      }
      const credentialMissing = ds.credentials[body.purpose as (typeof PURPOSES)[number]] === undefined;
      const unreachableTarget = (ds.host === "127.0.0.1" && ds.port >= 59000) || ds.host.endsWith(".invalid");
      const failed = credentialMissing || unreachableTarget;
      return taskResponse(
        "admin_connection_test",
        `/admin/datasources/${ds.id}/capabilities`,
        () => {
          if (failed) return;
          const ts = now();
          ds.capabilities = {
            datasource_id: ds.id,
            detected_version: ds.engine === "postgresql" ? "16.3" : "8.0.36",
            identity_fingerprint: `${ds.engine}:${ds.name}:inst-stable`,
            capabilities: {
              change_review: true,
              execution: ds.engine !== "postgresql",
              query: true,
              export: true,
              gh_ost: false,
            },
            checked_at: ts,
          };
        },
        failed,
      );
    }),

    http.get(apiRoute("/admin/datasources/:datasourceId/capabilities"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      if (ds.capabilities === null) return businessError(1002, "capabilities not probed yet");
      return HttpResponse.json(successEnvelope(ds.capabilities));
    }),

    http.get(apiRoute("/admin/ai-providers"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const requested = Number(url.searchParams.get("limit"));
      const limit = Number.isFinite(requested) && requested > 0 ? requested : 50;
      const sorted = [...world.providers.values()]
        .filter((provider) => matchesQ([provider.name, provider.base_url, provider.model_name], searchOf(url)))
        .sort((a, b) => a.selection_priority - b.selection_priority)
        .map(providerView);
      const items = sorted.slice(0, limit);
      return HttpResponse.json(successEnvelope({
        items,
        page: { next_cursor: null, has_more: sorted.length > items.length },
      }));
    }),

    http.post(apiRoute("/admin/ai-providers"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name === "") {
        return businessError(1001, "name is required");
      }
      const duplicate = [...world.providers.values()].some((p) => p.name === body.name);
      if (duplicate) {
        return businessError(1005, "RESOURCE_ALREADY_EXISTS", { detail: "name 已被其他AI Provider占用" });
      }
      const createPrio = Number(body.selection_priority ?? 100);
      if ([...world.providers.values()].some((p) => p.selection_priority === createPrio)) {
        return businessError(1005, "RESOURCE_ALREADY_EXISTS", {
          detail: `AI Provider selection_priority=${String(createPrio)} 已被其他AI Provider占用`,
        });
      }
      const apiKey = body.api_key as { value?: string } | undefined;
      if (apiKey === undefined || typeof apiKey.value !== "string" || apiKey.value === "") {
        return businessError(1001, "api_key is required on create");
      }
      const configError = providerConfigurationError(body.configuration);
      if (configError !== null) return configError;
      const ts = now();
      const provider: FixtureProvider = {
        id: uuid(),
        name: body.name,
        provider_kind: typeof body.provider_kind === "string" ? body.provider_kind : "openai_compatible",
        base_url: typeof body.base_url === "string" ? body.base_url : "",
        model_name: typeof body.model_name === "string" ? body.model_name : "",
        enabled: Boolean(body.enabled),
        selection_priority: Number(body.selection_priority ?? 100),
        api_key_configured: true,
        ...(body.configuration !== undefined &&
        body.configuration !== null &&
        typeof body.configuration === "object" &&
        !Array.isArray(body.configuration)
          ? { configuration: { ...(body.configuration as NonNullable<AiProvider["configuration"]>) } }
          : {}),
        version: 1,
        created_at: ts,
        updated_at: ts,
        apiKey: apiKey.value,
      };
      world.providers.set(provider.id, provider);
      return HttpResponse.json(successEnvelope(providerView(provider)));
    }),

    http.get(apiRoute("/admin/ai-providers/:providerId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      return HttpResponse.json(successEnvelope(providerView(provider)));
    }),

    http.put(apiRoute("/admin/ai-providers/:providerId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const configError = providerConfigurationError(body.configuration);
      if (configError !== null) return configError;
      if (typeof body.name === "string" && body.name !== "" &&
        [...world.providers.values()].some((p) => p.id !== provider.id && p.name === body.name)) {
        return businessError(1005, "RESOURCE_ALREADY_EXISTS", { detail: "name 已被其他AI Provider占用" });
      }
      if (typeof body.selection_priority === "number" &&
        [...world.providers.values()].some((p) => p.id !== provider.id && p.selection_priority === body.selection_priority)) {
        return businessError(1005, "RESOURCE_ALREADY_EXISTS", {
          detail: `AI Provider selection_priority=${String(body.selection_priority)} 已被其他AI Provider占用`,
        });
      }
      if (typeof body.name === "string" && body.name !== "") provider.name = body.name;
      if (typeof body.base_url === "string") provider.base_url = body.base_url;
      if (typeof body.model_name === "string") provider.model_name = body.model_name;
      if (typeof body.enabled === "boolean") provider.enabled = body.enabled;
      if (typeof body.selection_priority === "number") provider.selection_priority = body.selection_priority;
      if (
        body.configuration !== undefined &&
        body.configuration !== null &&
        typeof body.configuration === "object" &&
        !Array.isArray(body.configuration)
      ) {
        const preservedMode = provider.configuration?.structured_output_mode;
        provider.configuration = {
          ...(body.configuration as NonNullable<AiProvider["configuration"]>),
          ...(preservedMode !== undefined ? { structured_output_mode: preservedMode } : {}),
        };
      }
      const apiKey = body.api_key as { value?: string } | undefined;
      if (apiKey !== undefined) {
        if (typeof apiKey.value !== "string" || apiKey.value === "") {
          return businessError(1001, "api_key replacement must not be empty");
        }
        provider.apiKey = apiKey.value;
        provider.api_key_configured = true;
      }
      provider.version += 1;
      provider.updated_at = now();
      return HttpResponse.json(successEnvelope(providerView(provider)));
    }),

    http.delete(apiRoute("/admin/ai-providers/:providerId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      world.providers.delete(provider.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post(apiRoute("/admin/ai-providers/:providerId/connection-tests"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (!provider.enabled) return businessError(5003, "provider disabled");
      return HttpResponse.json(successEnvelope({ ok: true }));
    }),

    http.post(apiRoute("/admin/ai-providers/models"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.base_url !== "string" || body.base_url === "") {
        return businessError(1001, "base_url is required");
      }
      const apiKey = typeof body.api_key === "string" ? body.api_key : "";
      const providerId = typeof body.provider_id === "string" ? body.provider_id : "";
      if (apiKey === "" && providerId === "") return businessError(1001, "api_key or provider_id is required");
      if (apiKey === "" && providerId !== "" && !world.providers.has(providerId)) {
        return businessError(1002, "provider not found");
      }
      if (apiKey === "mock-invalid-key") {
        return businessError(2011, "upstream rejected", {
          detail: 'provider: request rejected: status 401: {"error":{"message":"Authentication Fails, Your api key: ****mock is invalid"}}',
        });
      }
      return HttpResponse.json(successEnvelope({
        items: ["mock-chat-lite", "mock-chat-pro", "mock-reasoner"],
      }));
    }),
  ];
}
