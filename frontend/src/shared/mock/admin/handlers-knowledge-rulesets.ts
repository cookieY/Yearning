import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { validateKnowledgeDefinition } from "@/shared/mock/admin-validation";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import {
  flowViewOf,
  knowledgeEntryView,
  ruleSetView,
  runReviewInputEval,
} from "./helpers";
import {
  adminGuard,
  businessError,
  configHash,
  matchesQ,
  pageOf,
  searchOf,
  successEnvelope,
  versionMismatch,
  world,
} from "./state";
import type {
  FixtureKnowledgeEntry,
  FixtureReviewInput,
  FixtureRuleSet,
} from "./types";

export function adminKnowledgeRulesetsHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/admin/knowledge-entries"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const items = [...world.knowledgeEntries.values()]
        .filter((entry) => matchesQ([entry.name], searchOf(url)))
        .map(knowledgeEntryView);
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),

    http.post(apiRoute("/admin/knowledge-entries"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name === "") {
        return businessError(1001, "name is required");
      }
      const duplicate = [...world.knowledgeEntries.values()].some((entry) => entry.name === body.name);
      if (duplicate) return businessError(1005, "knowledge entry name already exists");
      const scopeType = body.scope_type as string;
      if (!["datasource", "table"].includes(scopeType)) {
        return businessError(1001, "scope_type must be datasource or table");
      }
      if (typeof body.datasource_id !== "string" || body.datasource_id === "" || typeof body.database_name !== "string" || body.database_name === "") {
        return businessError(1001, "datasource_id and database_name are required for a scoped entry");
      }
      if (scopeType === "table" && (typeof body.table_name !== "string" || body.table_name === "")) {
        return businessError(1001, "database_name and table_name are required for a table-scoped entry");
      }
      const definitionError = validateKnowledgeDefinition(body.definition);
      if (definitionError !== null) return businessError(1001, definitionError);
      const state = (body.state as string | undefined) ?? "draft";
      if (!["draft", "enabled", "disabled"].includes(state)) return businessError(1001, "state is invalid");
      const definition = body.definition as FixtureReviewInput["definition"];
      if (state === "enabled") {
        const evaluation = runReviewInputEval(definition, {}, { allowBlockingSeverities: false });
        if (!evaluation.pass) return businessError(1001, "eval gate failed");
      }
      const ts = now();
      const entry: FixtureKnowledgeEntry = {
        id: uuid(),
        name: body.name,
        purpose: (body.purpose as string | undefined) ?? null,
        state: state as FixtureKnowledgeEntry["state"],
        scope_type: scopeType as FixtureKnowledgeEntry["scope_type"],
        datasource_id: body.datasource_id,
        database_name: body.database_name,
        table_name: (body.table_name as string | undefined) ?? null,
        definition,
        provenance: ((body.provenance as string | undefined) ?? "manual") as FixtureKnowledgeEntry["provenance"],
        source_finding_id: (body.source_finding_id as string | undefined) ?? null,
        config_hash: configHash(
          body.name,
          scopeType,
          body.datasource_id,
          body.database_name,
          typeof body.table_name === "string" ? body.table_name : "",
          JSON.stringify(definition),
        ),
        version: 1,
        created_at: ts,
        updated_at: ts,
        evaluation: null,
      };
      world.knowledgeEntries.set(entry.id, entry);
      return HttpResponse.json(successEnvelope(knowledgeEntryView(entry)));
    }),

    http.get(apiRoute("/admin/knowledge-entries/:knowledgeEntryId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      return HttpResponse.json(successEnvelope(knowledgeEntryView(entry)));
    }),

    http.put(apiRoute("/admin/knowledge-entries/:knowledgeEntryId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      if (versionMismatch(request, entry.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const scopeType = (body.scope_type as string | undefined) ?? entry.scope_type;
      if (!["datasource", "table"].includes(scopeType)) {
        return businessError(1001, "scope_type must be datasource or table");
      }
      const nextDatasourceId =
        typeof body.datasource_id === "string" ? body.datasource_id : (entry.datasource_id ?? "");
      const nextDatabaseName =
        typeof body.database_name === "string" && body.database_name !== "" ? body.database_name : "";
      if (nextDatasourceId === "" || nextDatabaseName === "") {
        return businessError(1001, "datasource_id and database_name are required for a scoped entry");
      }
      const nextTableName =
        scopeType === "table" && typeof body.table_name === "string" && body.table_name !== ""
          ? body.table_name
          : null;
      if (scopeType === "table" && nextTableName === null) {
        return businessError(1001, "database_name and table_name are required for a table-scoped entry");
      }
      const definitionError = validateKnowledgeDefinition(body.definition);
      if (definitionError !== null) return businessError(1001, definitionError);
      const state = (body.state as string | undefined) ?? entry.state;
      if (!["draft", "enabled", "disabled"].includes(state)) return businessError(1001, "state is invalid");
      const definition = body.definition as FixtureReviewInput["definition"];
      if (state === "enabled") {
        const evaluation = runReviewInputEval(definition, {}, { allowBlockingSeverities: false });
        if (!evaluation.pass) return businessError(1001, "eval gate failed");
      }
      entry.state = state as FixtureKnowledgeEntry["state"];
      entry.scope_type = scopeType as FixtureKnowledgeEntry["scope_type"];
      entry.purpose = (body.purpose as string | undefined) ?? entry.purpose;
      entry.definition = definition;
      entry.datasource_id = nextDatasourceId;
      entry.database_name = nextDatabaseName;
      entry.table_name = nextTableName;
      entry.config_hash = configHash(
        entry.name,
        entry.scope_type,
        nextDatasourceId,
        nextDatabaseName,
        entry.table_name ?? "",
        JSON.stringify(definition),
      );
      entry.version += 1;
      entry.updated_at = now();
      entry.evaluation = null;
      return HttpResponse.json(successEnvelope(knowledgeEntryView(entry)));
    }),

    http.delete(apiRoute("/admin/knowledge-entries/:knowledgeEntryId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      if (versionMismatch(request, entry.version)) return businessError(1004, "version mismatch");
      if (entry.provenance === "finding_conversion") {
        return businessError(1006, "knowledge entry is referenced by a review run");
      }
      world.knowledgeEntries.delete(entry.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post(apiRoute("/admin/knowledge-entries/:knowledgeEntryId/evaluations"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      if (entry.scope_type === "global") {
        return businessError(1001, "global scope is retired; re-scope to datasource or table");
      }
      const evaluation = runReviewInputEval(entry.definition, {}, { allowBlockingSeverities: false });
      entry.evaluation = evaluation;
      return HttpResponse.json(successEnvelope(evaluation));
    }),

    http.get(apiRoute("/admin/rule-sets"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const items = [...world.ruleSets.values()]
        .filter((rs) => matchesQ([rs.name], searchOf(url)))
        .map(ruleSetView);
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),

    http.post(apiRoute("/admin/rule-sets"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name === "") {
        return businessError(1001, "name is required");
      }
      const duplicate = [...world.ruleSets.values()].some((ruleSet) => ruleSet.name === body.name);
      if (duplicate) return businessError(1005, "rule set name already exists");
      const toolIds = Array.isArray(body.prompt_tool_ids) ? (body.prompt_tool_ids as string[]) : [];
      for (const toolId of toolIds) {
        if (!world.promptTools.has(toolId)) return businessError(1001, "unknown prompt tool reference");
      }
      const ts = now();
      const ruleSet: FixtureRuleSet = {
        id: uuid(),
        name: body.name,
        enabled: Boolean(body.enabled),
        prompt_tool_ids: toolIds,
        config_hash: configHash(body.name, toolIds.join(","), String(Boolean(body.enabled))),
        version: 1,
        created_at: ts,
        updated_at: ts,
      };
      world.ruleSets.set(ruleSet.id, ruleSet);
      return HttpResponse.json(successEnvelope(ruleSetView(ruleSet)));
    }),

    http.get(apiRoute("/admin/rule-sets/:ruleSetId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ruleSet = world.ruleSets.get(String(params.ruleSetId));
      if (ruleSet === undefined) return businessError(1002, "rule set not found");
      return HttpResponse.json(successEnvelope(ruleSetView(ruleSet)));
    }),

    http.put(apiRoute("/admin/rule-sets/:ruleSetId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ruleSet = world.ruleSets.get(String(params.ruleSetId));
      if (ruleSet === undefined) return businessError(1002, "rule set not found");
      if (versionMismatch(request, ruleSet.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const toolIds = Array.isArray(body.prompt_tool_ids) ? (body.prompt_tool_ids as string[]) : [];
      for (const toolId of toolIds) {
        if (!world.promptTools.has(toolId)) return businessError(1001, "unknown prompt tool reference");
      }
      if (typeof body.name === "string" && body.name !== "") ruleSet.name = body.name;
      if (typeof body.enabled === "boolean") ruleSet.enabled = body.enabled;
      ruleSet.prompt_tool_ids = toolIds;
      ruleSet.config_hash = configHash(ruleSet.name, toolIds.join(","), String(ruleSet.enabled));
      ruleSet.version += 1;
      ruleSet.updated_at = now();
      return HttpResponse.json(successEnvelope(ruleSetView(ruleSet)));
    }),

    http.delete(apiRoute("/admin/rule-sets/:ruleSetId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const ruleSet = world.ruleSets.get(String(params.ruleSetId));
      if (ruleSet === undefined) return businessError(1002, "rule set not found");
      if (versionMismatch(request, ruleSet.version)) return businessError(1004, "version mismatch");
      const referenced = [...world.flows.values()].some((flow) => flow.rule_set_id === ruleSet.id);
      if (referenced) return businessError(1006, "rule set is referenced by a flow");
      world.ruleSets.delete(ruleSet.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.get(apiRoute("/admin/flows"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const flowType = url.searchParams.get("flow_type");
      const enabled = url.searchParams.get("enabled");
      const items = [...world.flows.values()]
        .filter((flow) => flowType === null || flow.flow_type === flowType)
        .filter((flow) => enabled === null || (enabled === "true") === flow.enabled)
        .filter((flow) => matchesQ([flow.name], searchOf(url)))
        .sort((a, b) => a.created_at.localeCompare(b.created_at))
        .map(flowViewOf);
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),
  ];
}
