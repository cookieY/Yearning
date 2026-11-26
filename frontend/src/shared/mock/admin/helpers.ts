import type { DefaultBodyType, HttpResponse } from "msw";
import type {
  AiProvider,
  Datasource,
  KnowledgeEntry,
  KnowledgeEntryEvaluation,
  LegacyMigrationRun,
  PromptTool,
  RuleSet,
  SettingsImpactAssessment,
  SettingsValue,
  User,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { validateDatasourceWrite as validateDatasourceWriteStrict } from "@/shared/mock/admin-validation";
import { nowIsoSeconds as now } from "@/shared/mock/kit";
import { PURPOSES, SETTINGS_FIELD_IMPACT } from "./ids";
import { businessError, configHash, world } from "./state";
import type {
  CredentialLike,
  FixtureDatasource,
  FixtureFlow,
  FixtureKnowledgeEntry,
  FixtureMigrationRun,
  FixturePromptTool,
  FixtureProvider,
  FixtureReviewInput,
  FixtureRuleSet,
  FixtureUser,
} from "./types";

export function engineModeOK(engine: string, mode: string): boolean {
  if (engine === "mysql" || engine === "tidb" || engine === "oceanbase") return mode === "mysql";
  if (engine === "postgresql") return mode === "postgresql";
  if (engine === "polardb") return mode === "mysql" || mode === "postgresql";
  return false;
}

export const validateDatasourceWrite = (body: Record<string, unknown>): string | null =>
  validateDatasourceWriteStrict(body, PURPOSES, engineModeOK);

export function testPurposeOfWrite(body: Record<string, unknown>): Datasource["test_purpose"] {
  return PURPOSES.includes(body.test_purpose as (typeof PURPOSES)[number])
    ? (body.test_purpose as Datasource["test_purpose"])
    : "review";
}

export function applyCredentialReplacement(
  oldCredentials: FixtureDatasource["credentials"],
  credentials: CredentialLike[],
): FixtureDatasource["credentials"] | string {
  const next: FixtureDatasource["credentials"] = {};
  for (const credential of credentials) {
    const purpose = credential.purpose as (typeof PURPOSES)[number];
    const password = credential.password?.value ?? "";
    if (password !== "") {
      next[purpose] = { username: credential.username ?? "", password };
      continue;
    }
    const sourcePurpose = credential.reuse_credential_purpose as (typeof PURPOSES)[number] | undefined;
    if (sourcePurpose == null) return "credential requires an explicit mode";
    const source = oldCredentials[sourcePurpose];
    if (source === undefined) {
      return sourcePurpose === purpose
        ? "keep requires an existing credential for the purpose"
        : "reuse credential source is not configured";
    }
    if (sourcePurpose === purpose) {
      next[purpose] = { ...source };
      continue;
    }
    next[purpose] = { username: credential.username ?? "", password: source.password };
  }
  return next;
}

export function userView(user: FixtureUser): User {
  const { activeOrderCount: _activeOrderCount, templateReferenceCount: _templateReferenceCount, ...view } = user;
  return view;
}

export function deletionImpactOf(user: FixtureUser) {
  const blockers: { code: string; count: number }[] = [];
  if (user.is_builtin_admin) blockers.push({ code: "builtin_admin_immutable", count: 1 });
  if (user.activeOrderCount > 0) blockers.push({ code: "active_orders", count: user.activeOrderCount });
  if (user.templateReferenceCount > 0) {
    blockers.push({ code: "referenced_by_template", count: user.templateReferenceCount });
  }
  return {
    user_id: user.id,
    can_delete: blockers.length === 0,
    blockers,
    flow_actor_references: user.templateReferenceCount,
    permission_group_memberships: [...world.groups.values()].filter((group) =>
      group.member_user_ids.includes(user.id),
    ).length,
    active_query_grants: 0,
    active_query_sessions: 0,
    historical_snapshots_preserved: true,
    calculated_at: now(),
  };
}

export function flowWriteError(body: Record<string, unknown>): string | null {
  const flowType = body.flow_type;
  if (flowType !== "change_review" && flowType !== "query_access") return "flow_type invalid";
  if (typeof body.name !== "string" || body.name.trim() === "" || body.name.length > 128) {
    return "name required";
  }
  if (flowType === "change_review") {
    const stages = body.stages;
    if (!Array.isArray(stages) || stages.length < 1) return "stages required";
    for (const stage of stages) {
      const typed = stage as Record<string, unknown>;
      const steps = typed.approval_steps;
      if (!Array.isArray(steps) || steps.length < 1 || steps.length > 10) return "approval steps 1..10";
      if (steps.some((step) => !Array.isArray((step as Record<string, unknown>).actors) || ((step as Record<string, unknown>).actors as unknown[]).length < 1)) {
        return "step actors required";
      }
      const actors = typed.execution_actors;
      if (!Array.isArray(actors) || actors.length < 1) return "execution actors required";
    }
    return null;
  }
  const capabilities = body.query_capabilities;
  if (!Array.isArray(capabilities) || capabilities.length < 1) return "query capabilities required";
  const steps = body.approval_steps;
  if (!Array.isArray(steps) || steps.length < 1 || steps.length > 10) return "approval steps 1..10";
  if (steps.some((step) => !Array.isArray((step as Record<string, unknown>).actors) || ((step as Record<string, unknown>).actors as unknown[]).length < 1)) {
    return "step actors required";
  }
  return null;
}

export function flowViewOf(flow: FixtureFlow) {
  return {
    id: flow.id,
    name: flow.name,
    flow_type: flow.flow_type,
    enabled: flow.enabled,
    status: flow.enabled ? ("enabled" as const) : ("disabled" as const),
    rule_set_id: flow.rule_set_id,
    stages: flow.stages?.map((s) => ({
      ...s,
      datasource_name: world.datasources.get(s.datasource_id)?.name ?? s.datasource_id,
    })),
    approval_steps: flow.approval_steps === null ? undefined : flow.approval_steps,
    query_capabilities: flow.query_capabilities?.map((c) => ({
      ...c,
      datasource_name: world.datasources.get(c.datasource_id)?.name ?? c.datasource_id,
    })),
    version: flow.version,
    created_at: flow.created_at,
    updated_at: flow.updated_at,
  };
}

export function migrationRunView(run: FixtureMigrationRun): LegacyMigrationRun {
  return {
    id: run.id,
    source_schema_version: "legacy-2026-08",
    manifest_hash: run.manifest_hash,
    state: run.state,
    active_work_count: run.table_results.reduce(
      (sum, result) => sum + (result.reconciliation_passed ? 0 : 1),
      0,
    ),
    unknown_status_count: 0,
    ambiguous_status_count: run.table_results.reduce(
      (sum, result) => sum + result.quarantined,
      0,
    ),
    table_results: run.table_results,
    candidates: run.candidates,
    all_candidates_confirmed: run.candidates.every((candidate) => candidate.confirmed),
    approved_manifest_hash: run.state === "approved" ? run.manifest_hash : null,
    approved_by: run.state === "approved" ? "admin" : null,
    approved_at: null,
    version: run.version,
    started_at: run.started_at,
    updated_at: run.updated_at,
    finished_at: null,
  };
}

export function providerConfigurationError(configuration: unknown): HttpResponse<DefaultBodyType> | null {
  if (configuration === undefined || configuration === null) return null;
  if (typeof configuration !== "object" || Array.isArray(configuration)) {
    return businessError(1001, "VALIDATION_FAILED", { detail: "configuration must be an object" });
  }
  for (const [key, value] of Object.entries(configuration as Record<string, unknown>)) {
    if (key === "max_tokens") {
      if (typeof value !== "number" || !Number.isInteger(value) || value < 256 || value > 200000) {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "max_tokens must be an integer between 256 and 200000",
          fields: { max_tokens: ["must be an integer between 256 and 200000"] },
        });
      }
    } else if (key === "context_window") {
      if (typeof value !== "number" || !Number.isInteger(value) || value < 1 || value > 2147483647) {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "context_window must be a positive integer",
          fields: { context_window: ["must be a positive integer"] },
        });
      }
    } else if (key === "temperature") {
      if (typeof value !== "number" || !Number.isFinite(value) || value < 0 || value > 2) {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "temperature must be a number between 0 and 2",
          fields: { temperature: ["must be a number between 0 and 2"] },
        });
      }
    } else if (key === "reasoning_effort") {
      if (value !== "low" && value !== "medium" && value !== "high") {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "reasoning_effort must be one of low, medium, high",
          fields: { reasoning_effort: ["must be one of low, medium, high"] },
        });
      }
    } else if (key === "system_prompt_profile") {
      if (value !== "default" && value !== "san") {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "system_prompt_profile must be one of default, san",
          fields: { system_prompt_profile: ["must be one of default, san"] },
        });
      }
    } else {
      return businessError(1001, "VALIDATION_FAILED", {
        detail: `unknown configuration key "${key}" (allowed: max_tokens, context_window, temperature, reasoning_effort, system_prompt_profile)`,
        fields: { [key]: ["unknown configuration key (allowed: max_tokens, context_window, temperature, reasoning_effort, system_prompt_profile)"] },
      });
    }
  }
  return null;
}

export function runReviewInputEval(
  definition: FixtureReviewInput["definition"],
  parameters: Record<string, unknown>,
  options: { allowBlockingSeverities?: boolean } = {},
): KnowledgeEntryEvaluation {
  const allowBlocking = options.allowBlockingSeverities ?? true;
  const allowedSeverities = allowBlocking
    ? ["info", "low", "medium", "high", "critical"]
    : ["info", "low", "medium"];
  const findings: string[] = [];
  const schemaSubsetOk =
    definition.knowledge_text.trim() !== "" &&
    allowedSeverities.includes(definition.max_severity);
  if (!schemaSubsetOk) {
    findings.push(
      allowBlocking
        ? "definition must carry non-empty knowledge_text and a valid max_severity"
        : "definition must carry non-empty knowledge_text and non-blocking max_severity (info/low/medium)",
    );
  }
  const severityOk = allowedSeverities.includes(definition.max_severity);
  if (!severityOk) {
    findings.push(
      allowBlocking
        ? "max_severity must be one of info/low/medium/high/critical"
        : "max_severity must be one of info/low/medium",
    );
  }
  const text = definition.knowledge_text.toLowerCase();
  const privacyOk = !/(password|api[_-]?key|secret)\s*[:=]/.test(text);
  if (!privacyOk) findings.push("knowledge text must not request credentials or secrets");
  const injectionOk = !/(ignore (all )?previous|disregard .*instructions|reveal .*system prompt)/.test(text);
  if (!injectionOk) findings.push("knowledge text contains a prompt-injection pattern");
  const parameterKeysOk = Object.keys(parameters).every((key) => /^[a-z][a-z0-9_]{0,31}$/.test(key));
  if (!parameterKeysOk) findings.push("parameter keys must match ^[a-z][a-z0-9_]{0,31}$");
  return {
    pass: findings.length === 0,
    schema_subset_ok: schemaSubsetOk,
    privacy_ok: privacyOk,
    injection_ok: injectionOk,
    severity_ok: severityOk,
    findings,
    checked_at: now(),
  };
}

export function datasourceView(ds: FixtureDatasource): Datasource {
  const { credentials: _credentials, capabilities: _capabilities, tls: _tls, ...view } = ds;
  void _credentials;
  void _capabilities;
  void _tls;
  return { ...view, tls_verified: _tls !== null };
}

export function providerView(provider: FixtureProvider): AiProvider {
  const { apiKey: _apiKey, ...view } = provider;
  void _apiKey;
  return { ...view };
}

export function promptToolView(tool: FixturePromptTool): PromptTool {
  return { ...tool };
}

export function tlsFromWrite(body: Record<string, unknown>): FixtureDatasource["tls"] {
  const tls = body.tls as
    | { ca_pem?: { value: string } | null; client_cert_pem?: { value: string } | null; client_key_pem?: { value: string } | null }
    | null
    | undefined;
  if (tls == null) return null;
  return {
    ca: tls.ca_pem?.value ?? "",
    cert: tls.client_cert_pem?.value ?? "",
    key: tls.client_key_pem?.value ?? "",
  };
}

export function knowledgeEntryView(entry: FixtureKnowledgeEntry): KnowledgeEntry {
  const {
    evaluation: _evaluation, datasource_id: ds, database_name: db, table_name: tbl,
    source_finding_id: sfid, purpose, ...rest
  } = entry;
  void _evaluation;
  const out: Record<string, unknown> = { ...rest };
  if (sfid !== null) out.source_finding_id = sfid;
  if (ds !== null) out.datasource_id = ds;
  if (db !== null) out.database_name = db;
  if (tbl !== null) out.table_name = tbl;
  if (purpose !== null) out.purpose = purpose;
  return out as unknown as KnowledgeEntry;
}

export function ruleSetView(ruleSet: FixtureRuleSet): RuleSet {
  return { ...ruleSet, rules: {} };
}

export function credentialStatus(ds: FixtureDatasource): Datasource["credential_status"] {
  const status: Record<string, boolean> = {};
  for (const purpose of PURPOSES) {
    status[purpose] = ds.credentials[purpose] !== undefined;
  }
  return status;
}

export function touchedCredentialPurposes(ds: FixtureDatasource): void {
  ds.credential_status = credentialStatus(ds);
}

function canonicalSettings(value: SettingsValue): string {
  return JSON.stringify(value, Object.keys(value).sort());
}

function proposedSha256(proposed: SettingsValue): string {
  return configHash(canonicalSettings(proposed)).repeat(8).slice(0, 64);
}

function impactTokenBinding(namespace: string, sha: string, currentVersion: number): string {
  return configHash(namespace, sha, String(currentVersion), "admin-session");
}

export function diffChangedPaths(current: SettingsValue, proposed: SettingsValue): string[] {
  const currentMap = current as unknown as Record<string, unknown>;
  const proposedMap = proposed as unknown as Record<string, unknown>;
  const paths: string[] = [];
  for (const key of Object.keys(proposedMap)) {
    if (JSON.stringify(currentMap[key]) !== JSON.stringify(proposedMap[key])) {
      paths.push(`/${key}`);
    }
  }
  return paths;
}

function impactEffectKind(namespace: string): SettingsImpactAssessment["impact"]["effects"][number]["kind"] {
  switch (namespace) {
    case "query":
      return "active_query_grants";
    case "execution":
      return "scheduled_executions";
    case "ai-budget":
      return "running_reviews";
    default:
      return "future_operations";
  }
}

function effectConsequence(
  impact: string,
): SettingsImpactAssessment["impact"]["effects"][number]["consequence"] {
  if (impact === "high" || impact === "immediate") return "applies_immediately";
  if (impact.startsWith("next_")) return "applies_on_next_operation";
  return "unchanged";
}

export function assessImpact(
  namespace: string,
  currentVersion: number,
  proposed: SettingsValue,
): SettingsImpactAssessment | null {
  const current = world.settings.get(namespace);
  if (current === undefined) return null;
  const changedPaths = diffChangedPaths(current.settings, proposed);
  const impacts = SETTINGS_FIELD_IMPACT[namespace] ?? {};
  const effects = changedPaths.map((path) => {
    const impact = impacts[path.slice(1)] ?? "next_operation";
    return {
      kind: impactEffectKind(namespace),
      count: 0,
      consequence: effectConsequence(impact),
    };
  });
  const level = changedPaths.some((path) => {
    const impact = impacts[path.slice(1)];
    return impact === "high" || impact === "immediate";
  })
    ? "high"
    : "low";
  const sha = proposedSha256(proposed);
  const expiresAt = new Date(Date.now() + 300_000).toISOString();
  const token = `${impactTokenBinding(namespace, sha, currentVersion)}${configHash(namespace, sha, String(currentVersion), "admin-session", "impact-token-round-2")}|${String(Date.now() + 300_000)}`;
  world.impactTokens.set(token, sha);
  return {
    namespace: namespace as SettingsImpactAssessment["namespace"],
    current_version: currentVersion,
    proposed_settings_sha256: sha,
    impact: { level, changed_paths: changedPaths, effects },
    impact_token: token,
    expires_at: expiresAt,
  };
}

export function verifyImpactToken(
  token: string,
  namespace: string,
  proposed: SettingsValue,
  currentVersion: number,
): boolean {
  const parts = token.split("|");
  if (parts.length !== 2) return false;
  const [binding, expiry] = parts;
  if (Number(expiry) < Date.now()) {
    world.impactTokens.delete(token);
    return false;
  }
  const boundSha = world.impactTokens.get(token);
  if (boundSha === undefined) return false;
  world.impactTokens.delete(token);
  const proposedSha = proposedSha256(proposed);
  const expected = impactTokenBinding(namespace, proposedSha, currentVersion)
    + configHash(namespace, proposedSha, String(currentVersion), "admin-session", "impact-token-round-2");
  return boundSha === proposedSha && binding === expected;
}
