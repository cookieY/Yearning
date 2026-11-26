import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import type { SettingsValue } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { validateReviewInputDefinition } from "@/shared/mock/admin-validation";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { applySettingsToBridge } from "@/shared/mock/settings-bridge";
import {
  assessImpact,
  diffChangedPaths,
  promptToolView,
  runReviewInputEval,
  verifyImpactToken,
} from "./helpers";
import { DEFAULT_SETTINGS, SEED_ADMIN_ID, SETTINGS_FIELD_IMPACT } from "./ids";
import {
  adminGuard,
  businessError,
  canonicalJson,
  configHash,
  matchesQ,
  pageOf,
  searchOf,
  successEnvelope,
  versionMismatch,
  world,
} from "./state";
import type { FixturePromptTool, FixtureReviewInput } from "./types";

export function adminSettingsSkillsHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/admin/settings/:namespace"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ns = String(params.namespace);
      const current = world.settings.get(ns);
      if (current === undefined) return businessError(1002, "unknown settings namespace");
      return HttpResponse.json(
        successEnvelope({
          namespace: ns,
          schema_version: 1,
          settings: current.settings,
          version: current.version,
          updated_by: current.updated_by,
          updated_at: current.updated_at,
        }),
      );
    }),

    http.post(apiRoute("/admin/settings/:namespace/impact-assessments"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ns = String(params.namespace);
      if (world.settings.get(ns) === undefined) return businessError(1002, "unknown settings namespace");
      const body = (await request.json().catch(() => null)) as { settings?: SettingsValue } | null;
      if (body === null || body.settings === undefined) return businessError(1001, "settings are required");
      const current = world.settings.get(ns);
      if (current === undefined) return businessError(1002, "unknown settings namespace");
      const assessment = assessImpact(ns, current.version, body.settings);
      if (assessment === null) return businessError(1002, "unknown settings namespace");
      return HttpResponse.json(successEnvelope(assessment));
    }),

    http.put(apiRoute("/admin/settings/:namespace"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ns = String(params.namespace);
      const current = world.settings.get(ns);
      if (current === undefined) return businessError(1002, "unknown settings namespace");
      if (versionMismatch(request, current.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as
        | { settings?: SettingsValue; impact_token?: string | null }
        | null;
      if (body === null || body.settings === undefined) return businessError(1001, "settings are required");
      const proposedKeys = Object.keys(body.settings);
      const declaredKeys = Object.keys(DEFAULT_SETTINGS[ns] as object);
      const unknown = proposedKeys.filter((key) => !declaredKeys.includes(key));
      if (unknown.length > 0) return businessError(1001, `unknown settings fields: ${unknown.join(", ")}`);
      const changedPaths = diffChangedPaths(current.settings, body.settings);
      const impacts = SETTINGS_FIELD_IMPACT[ns] ?? {};
      const high = changedPaths.some((path) => {
        const impact = impacts[path.slice(1)];
        return impact === "high" || impact === "immediate";
      });
      if (high) {
        if (body.impact_token === undefined || body.impact_token === null || body.impact_token === "") {
          return businessError(1011, "impact confirmation required for a high-impact change");
        }
        const valid = verifyImpactToken(body.impact_token, ns, body.settings, current.version);
        if (!valid) return businessError(1011, "impact token invalid, expired or already consumed");
      }
      current.settings = Object.assign({}, DEFAULT_SETTINGS[ns], body.settings);
      applySettingsToBridge(ns, body.settings);
      current.version += 1;
      current.updated_by = SEED_ADMIN_ID;
      current.updated_at = now();
      const revisions = world.revisions.get(ns) ?? [];
      revisions.unshift({
        namespace: ns,
        version: current.version,
        changed_by: SEED_ADMIN_ID,
        changed_at: current.updated_at,
        changed_paths: changedPaths,
        impact_summary: { level: high ? "high" : "low", changed_paths: changedPaths },
      });
      world.revisions.set(ns, revisions);
      return HttpResponse.json(
        successEnvelope({
          namespace: ns,
          schema_version: 1,
          settings: current.settings,
          version: current.version,
          updated_by: current.updated_by,
          updated_at: current.updated_at,
        }),
      );
    }),

    http.get(apiRoute("/admin/settings/:namespace/schema"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ns = String(params.namespace);
      if (world.settings.get(ns) === undefined) return businessError(1002, "unknown settings namespace");
      return HttpResponse.json(
        successEnvelope({
          namespace: ns,
          schema_version: 1,
          json_schema: { type: "object", additionalProperties: false },
          ui_hints: {},
        }),
      );
    }),

    http.get(apiRoute("/admin/settings/:namespace/revisions"), ({ params, request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ns = String(params.namespace);
      const revisions = world.revisions.get(ns);
      if (revisions === undefined) return businessError(1002, "unknown settings namespace");
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const capped = limit === null ? revisions : revisions.slice(0, Number(limit));
      return HttpResponse.json(
        successEnvelope({
          items: capped,
          page: { next_cursor: null, has_more: false },
        }),
      );
    }),

    http.get(apiRoute("/admin/prompt-tools"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const items = [...world.promptTools.values()]
        .filter((tool) => matchesQ([tool.name], searchOf(url)))
        .map(promptToolView);
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),

    http.post(apiRoute("/admin/prompt-tools"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name === "") {
        return businessError(1001, "name is required");
      }
      const duplicate = [...world.promptTools.values()].some((tool) => tool.name === body.name);
      if (duplicate) return businessError(1005, "prompt tool name already exists");
      const definitionError = validateReviewInputDefinition(body.definition);
      if (definitionError !== null) return businessError(1001, definitionError);
      const description = typeof body.description === "string" ? body.description : "";
      if (Array.from(description).length > 100) return businessError(1001, "description is too long");
      const engine = (body.engine as string | undefined) ?? "all";
      if (!["all", "mysql", "postgresql"].includes(engine)) return businessError(1001, "engine is invalid");
      const state = (body.state as string | undefined) ?? "draft";
      if (!["draft", "enabled", "disabled"].includes(state)) return businessError(1001, "state is invalid");
      const parameters = (body.parameters ?? {}) as Record<string, number | string | boolean | string[]>;
      const definition = body.definition as FixtureReviewInput["definition"];
      if (state === "enabled") {
        const evaluation = runReviewInputEval(definition, parameters);
        if (!evaluation.pass) return businessError(1001, "eval gate failed");
      }
      const ts = now();
      const tool: FixturePromptTool = {
        id: uuid(),
        name: body.name,
        description,
        state: state as FixturePromptTool["state"],
        engine: engine as FixturePromptTool["engine"],
        parameters,
        definition,
        config_hash: configHash(body.name, engine, JSON.stringify(definition), JSON.stringify(parameters)),
        version: 1,
        created_at: ts,
        updated_at: ts,
        is_builtin: false,
      };
      world.promptTools.set(tool.id, tool);
      return HttpResponse.json(successEnvelope(promptToolView(tool)));
    }),

    http.get(apiRoute("/admin/prompt-tools/:promptToolId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const tool = world.promptTools.get(String(params.promptToolId));
      if (tool === undefined) return businessError(1002, "prompt tool not found");
      return HttpResponse.json(successEnvelope(promptToolView(tool)));
    }),

    http.put(apiRoute("/admin/prompt-tools/:promptToolId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const tool = world.promptTools.get(String(params.promptToolId));
      if (tool === undefined) return businessError(1002, "prompt tool not found");
      if (versionMismatch(request, tool.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const definitionError = validateReviewInputDefinition(body.definition);
      if (definitionError !== null) return businessError(1001, definitionError);
      const state = (body.state as string | undefined) ?? tool.state;
      if (!["draft", "enabled", "disabled"].includes(state)) return businessError(1001, "state is invalid");
      const parameters = (body.parameters ?? tool.parameters) as Record<string, number | string | boolean | string[]>;
      const definition = body.definition as FixtureReviewInput["definition"];
      const engine = ((body.engine as string | undefined) ?? tool.engine) as FixturePromptTool["engine"];
      const name = (body.name as string | undefined) ?? tool.name;
      const description =
        typeof body.description === "string" && Array.from(body.description).length <= 100
          ? body.description
          : tool.description;
      if (tool.is_builtin) {
        const definitionFrozen =
          name === tool.name &&
          engine === tool.engine &&
          canonicalJson(definition) === canonicalJson(tool.definition);
        if (!definitionFrozen) return businessError(1001, "builtin skill definition is system-owned");
        const oldKeys = Object.keys(tool.parameters);
        const nextKeys = Object.keys(parameters);
        const parametersFrozen =
          nextKeys.length === oldKeys.length &&
          oldKeys.every((key) => key in parameters && typeof parameters[key] === typeof tool.parameters[key]);
        if (!parametersFrozen) return businessError(1001, "builtin skill parameter keys are frozen");
      }
      const nextHash = configHash(name, engine, JSON.stringify(definition), JSON.stringify(parameters));
      if (state === "enabled" && (tool.state !== "enabled" || nextHash !== tool.config_hash)) {
        const evaluation = runReviewInputEval(definition, parameters);
        if (!evaluation.pass) return businessError(1001, "eval gate failed");
      }
      tool.description = tool.is_builtin ? tool.description : description;
      tool.name = name;
      tool.state = state as FixturePromptTool["state"];
      tool.engine = engine;
      tool.parameters = parameters;
      tool.definition = definition;
      tool.config_hash = nextHash;
      tool.version += 1;
      tool.updated_at = now();
      return HttpResponse.json(successEnvelope(promptToolView(tool)));
    }),

    http.delete(apiRoute("/admin/prompt-tools/:promptToolId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const tool = world.promptTools.get(String(params.promptToolId));
      if (tool === undefined) return businessError(1002, "prompt tool not found");
      if (versionMismatch(request, tool.version)) return businessError(1004, "version mismatch");
      if (tool.is_builtin) return businessError(1001, "builtin skill cannot be deleted");
      const referenced = [...world.ruleSets.values()].some((ruleSet) =>
        ruleSet.prompt_tool_ids.includes(tool.id),
      );
      if (referenced) return businessError(1006, "prompt tool is referenced by a rule set");
      world.promptTools.delete(tool.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post(apiRoute("/admin/prompt-tools/prompt-optimizations"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (
        body === null ||
        typeof body.knowledge_text !== "string" ||
        body.knowledge_text.trim().length === 0 ||
        body.knowledge_text.length > 65536
      ) {
        return businessError(1001, "knowledge_text must be a non-empty string up to 65536 chars");
      }
      if (
        body.engine !== undefined &&
        body.engine !== "all" &&
        body.engine !== "mysql" &&
        body.engine !== "postgresql"
      ) {
        return businessError(1001, "engine is invalid");
      }
      const refined = `${body.knowledge_text.trim()}\n- Verify indexed predicates and bounded impact before execution.`;
      return HttpResponse.json(
        successEnvelope({ knowledge_text: refined.slice(0, 65536) }),
      );
    }),

    http.post(apiRoute("/admin/prompt-tools/test-cases"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (
        body === null ||
        typeof body.knowledge_text !== "string" ||
        body.knowledge_text.trim().length === 0 ||
        body.knowledge_text.length > 65536
      ) {
        return businessError(1001, "knowledge_text must be a non-empty string up to 65536 chars");
      }
      return HttpResponse.json(
        successEnvelope({
          cases: [
            {
              id: "case-compliant-1",
              sql: "UPDATE orders SET status = 'archived' WHERE id = 42 LIMIT 1;",
              expected_compliant: true,
              rationale: "Bounded primary-key predicate limits affected rows.",
            },
            {
              id: "case-violation-1",
              sql: "UPDATE orders SET status = 'archived';",
              expected_compliant: false,
              rationale: "Missing WHERE clause triggers full-table mutation finding.",
            },
          ],
        }),
      );
    }),

    http.post(apiRoute("/admin/prompt-tools/test-runs"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (
        body === null ||
        typeof body.knowledge_text !== "string" ||
        body.knowledge_text.trim().length === 0 ||
        body.knowledge_text.length > 65536 ||
        !Array.isArray(body.cases) ||
        body.cases.length === 0 ||
        body.cases.length > 20
      ) {
        return businessError(1001, "knowledge_text and 1..20 cases are required");
      }
      const results = [];
      for (const raw of body.cases) {
        if (
          raw === null ||
          typeof raw !== "object" ||
          typeof (raw as Record<string, unknown>).id !== "string" ||
          String((raw as Record<string, unknown>).id).length === 0 ||
          typeof (raw as Record<string, unknown>).sql !== "string" ||
          String((raw as Record<string, unknown>).sql).length === 0 ||
          typeof (raw as Record<string, unknown>).expected_compliant !== "boolean"
        ) {
          return businessError(1001, "each test case must provide id, sql, and expected_compliant");
        }
        const entry = raw as { id: string; sql: string; expected_compliant: boolean };
        results.push({
          id: entry.id,
          sql: entry.sql,
          expected_compliant: entry.expected_compliant,
          actual_compliant: entry.expected_compliant,
          matched_expectation: true,
          finding_summary: entry.expected_compliant
            ? "No policy violation detected."
            : "Matched rule finding against unguarded statement.",
        });
      }
      return HttpResponse.json(
        successEnvelope({
          results,
          total_count: results.length,
          matched_count: results.length,
          passed: true,
        }),
      );
    }),
  ];
}
