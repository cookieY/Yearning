import { HttpResponse, http } from "msw";
import { applySettingsToBridge, settingsBridge } from "@/shared/mock/settings-bridge";
import type {
  NotificationRule, ExecutionWindow } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { DefaultBodyType, HttpHandler } from "msw";
import { readStoredAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { mockSuperMode } from "@/shared/mock/identity-store";

/** Test-only latency knob for e2e feedback assertions; 0 in every other run. */
declare global {
  interface Window {
    /** Test-only delay for the mock datasource list, set from Playwright. */
    __YEARNING_MOCK_LATENCY__?: number;
  }
}

function mockLatencyMs(): number {
  const raw = typeof window === "undefined" ? 0 : window.__YEARNING_MOCK_LATENCY__;
  return typeof raw === "number" && Number.isFinite(raw) && raw > 0 ? raw : 0;
}

const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));
import {
  CATALOG_DATASOURCES,
  CATALOG_FLOWS,
} from "@/shared/mock/flow-catalog";
import type {
  AiProvider,
  AuditEvent,
  AnnouncementRevision,
  Datasource,
  IdentityProvider,
  LegacyMigrationRun,
  NotificationChannel,
  NotificationDelivery,
  PermissionGroup,
  ReviewInputDefinition,
  DatasourceCapabilities,
  KnowledgeEntry,
  KnowledgeEntryEvaluation,
  PromptTool,
  RuleSet,
  SettingsImpactAssessment,
  SettingsRevision,
  SettingsValue,
  Task,
  User,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Stateful admin fixture backing FE-F9-REVIEW-ADMIN mock development and
 * E2E (code-generation-policy.json mock_layer). It mirrors the frozen
 * OpenAPI surface for the six review-admin domains — the 200 business
 * envelope, If-Match optimistic locking (1004), the declared error codes
 * (operation-error-profiles: admin_create/admin_replace/admin_delete/
 * datasource_delete/connection_test/settings_replace), the two-step
 * high-impact settings update (assess → impact_token → PUT), async
 * connection-test tasks, and the non-admin HTTP 403 boundary of the real
 * RequireOperation guard (backend guards.go; anonymous 401).
 *
 * Cross-repo facts mirrored as contract, not invented: providers chain in
 * selection_priority ascending order (provider/chain.go:3); enabling a
 * review input runs the static eval gate (prompttools.go RunPromptToolEval,
 * 1001 on failure); settings impact level is "high" when a changed field's
 * impact is "high" or "immediate" (settings/service.go:110). The B13
 * alignment surface is mirrored end to end: credentials edit in exactly one
 * of replace/reuse/keep modes (keep copies the stored full credential and
 * needs the row to exist), tls blocks replace fully (omitted or null
 * removes the material, cert/key must be paired, fields must be PEM), the
 * datasource read face exposes tls_verified, and prompt-tool views carry
 * is_builtin with the definition frozen and delete refused for built-ins.
 */

const ADMIN_REQUEST_ID = "45454545-4545-4545-8545-454545454545";

const PURPOSES = ["review", "query", "execution"] as const;

/** engineModeOK mirror (datasource/service.go:89): engine/compatibility
 * linkage enforced identically by the write validators. */
function engineModeOK(engine: string, mode: string): boolean {
  if (engine === "mysql" || engine === "tidb" || engine === "oceanbase") return mode === "mysql";
  if (engine === "postgresql") return mode === "postgresql";
  if (engine === "polardb") return mode === "mysql" || mode === "postgresql";
  return false;
}

interface CredentialLike {
  purpose: string;
  username?: string;
  password?: { value: string } | null;
  reuse_credential_purpose?: string | null;
}

/** validateTLS mirror (datasource/service.go validateTLS): a non-null tls
 * block needs at least one material, client cert and key must be supplied
 * together, and every provided field must carry complete BEGIN/END PEM
 * blocks whose type matches the field (CERTIFICATE for ca/cert, non-
 * CERTIFICATE for the key) — the same per-block rule the backend enforces.
 * The fixture still does not validate base64 bodies (a marker-wrapped
 * non-base64 body passes here while pem.Decode rejects it server-side);
 * recorded as an accepted fixture boundary. Omitted or null tls is the
 * declared removal path and never reaches this validator. */
function validateTLSWrite(tls: unknown): string | null {
  if (tls === null || tls === undefined) return null;
  if (typeof tls !== "object" || Array.isArray(tls)) return "tls must be an object or null";
  const block = tls as {
    ca_pem?: { value: string } | null;
    client_cert_pem?: { value: string } | null;
    client_key_pem?: { value: string } | null;
  };
  const ca = block.ca_pem?.value ?? "";
  const cert = block.client_cert_pem?.value ?? "";
  const key = block.client_key_pem?.value ?? "";
  if (ca === "" && cert === "" && key === "") return "tls block requires at least one material";
  if ((cert === "") !== (key === "")) return "client certificate and key must be supplied together";
  // Per-block type mirror (service.go validateTLS): certificate fields only
  // accept CERTIFICATE blocks, the key field only accepts private-key blocks
  // (anything that is not a CERTIFICATE), matching the backend's
  // block-type check rather than a loose BEGIN marker.
  const blockTypes = (material: string): string[] => {
    const types: string[] = [];
    const pattern = /-----BEGIN ([A-Z0-9 ]+)-----[\s\S]*?-----END \1-----/g;
    for (const match of material.matchAll(pattern)) types.push(match[1] ?? "");
    return types;
  };
  const materials: Array<[string, string, boolean]> = [
    ["ca_pem", ca, true],
    ["client_cert_pem", cert, true],
    ["client_key_pem", key, false],
  ];
  for (const [name, material, wantCert] of materials) {
    if (material === "") continue;
    const types = blockTypes(material);
    if (types.length === 0) return `${name} must be PEM-encoded`;
    if (types.some((type) => (type === "CERTIFICATE") !== wantCert)) {
      return `${name} blocks must be ${wantCert ? "CERTIFICATE" : "private-key"} PEM`;
    }
  }
  return null;
}

/** validateWrite mirror (datasource/service.go validateWrite, B13
 * three-mode contract): field lengths/enums, engine-mode matrix, 1..3
 * unique purposes, and EXACTLY ONE edit mode per credential row — replace
 * (username + password), reuse (username + reuse_credential_purpose of a
 * DIFFERENT purpose) or keep (reuse_credential_purpose equal to the row's
 * own purpose; username must be absent). */
function validateDatasourceWrite(body: Record<string, unknown>): string | null {
  const name = typeof body.name === "string" ? body.name : "";
  const host = typeof body.host === "string" ? body.host : "";
  if (name === "" || name.length > 128 || host === "" || host.length > 253) {
    return "name and host are required";
  }
  const port = Number(body.port ?? 0);
  const versionConstraint = typeof body.version_constraint === "string" ? body.version_constraint : "";
  if (port < 1 || port > 65535 || versionConstraint === "" || versionConstraint.length > 64) {
    return "port and version_constraint must be valid";
  }
  if (body.deployment_kind !== "native" && body.deployment_kind !== "cloud") {
    return "deployment_kind is invalid";
  }
  const engine = typeof body.engine === "string" ? body.engine : "";
  const compatibilityMode = typeof body.compatibility_mode === "string" ? body.compatibility_mode : "";
  if (!engineModeOK(engine, compatibilityMode)) {
    return "engine and compatibility_mode do not match the supported matrix";
  }
  const credentials = Array.isArray(body.credentials) ? (body.credentials as CredentialLike[]) : [];
  if (credentials.length < 1 || credentials.length > 3) {
    return "at least one and at most three credentials are required";
  }
  const seen = new Set<string>();
  for (const credential of credentials) {
    if (!PURPOSES.includes(credential.purpose as (typeof PURPOSES)[number])) {
      return "credential purpose is invalid";
    }
    if (seen.has(credential.purpose)) return "credential purposes must be unique";
    seen.add(credential.purpose);
    const hasPassword = credential.password != null && credential.password.value !== "";
    const reuse = credential.reuse_credential_purpose;
    if (hasPassword && reuse != null) {
      return "password and reuse_credential_purpose are mutually exclusive";
    }
    if (!hasPassword && (reuse == null || reuse === "")) {
      return "credential requires exactly one of password or reuse_credential_purpose";
    }
    if (!hasPassword && reuse === credential.purpose) {
      // keep: the stored username is copied verbatim, so the payload must
      // not carry one (the backend decodes an absent username to "" and
      // rejects any non-empty value, service.go:183).
      if (credential.username != null && credential.username !== "") {
        return "username must be absent in keep mode";
      }
      continue;
    }
    if (typeof credential.username !== "string" || credential.username === "") {
      return "credential username is required";
    }
  }
  const tlsError = validateTLSWrite("tls" in body ? body.tls : null);
  if (tlsError !== null) return tlsError;
  // §18.61: connection-test purpose is optional and falls back to review (full-replacement semantics: omitting on replace resets it).
  if (
    body.test_purpose !== undefined &&
    !PURPOSES.includes(body.test_purpose as (typeof PURPOSES)[number])
  ) {
    return "test_purpose is invalid";
  }
  return null;
}

/** Resolves the stored connection-test purpose of a write body (§18.61):
 * absent or invalid-enum values never reach here (validated earlier), absent
 * = review per the DatasourceWrite default. */
function testPurposeOfWrite(body: Record<string, unknown>): Datasource["test_purpose"] {
  return PURPOSES.includes(body.test_purpose as (typeof PURPOSES)[number])
    ? (body.test_purpose as Datasource["test_purpose"])
    : "review";
}

/** replaceCredentials mirror (datasource/service.go replaceCredentials, B13
 * three-mode contract): the write is a FULL replacement of the enabled
 * credential set — purposes absent from the payload lose their credential.
 * keep (reuse == own purpose) copies the OLD stored full credential
 * verbatim and is only legal when that row already exists; reuse (a
 * DIFFERENT purpose) copies the OLD stored secret of that purpose under
 * the supplied username (never a password from this payload). */
function applyCredentialReplacement(
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

const SETTING_NAMESPACES = ["general", "query", "execution", "ai-budget", "branding"] as const;

/** Per-field impact vocabulary from settings-namespaces.json (machine
 * authority) — "high" and "immediate" classify the assessment as high
 * impact (settings/service.go:110). */
const SETTINGS_FIELD_IMPACT: Record<string, Record<string, string>> = {
  general: { registration_enabled: "immediate", system_timezone: "next_operation", default_locale: "next_session" },
  query: {
    approval_enabled: "high",
    default_timeout_ms: "next_query",
    maximum_approved_access_duration_minutes: "next_request",
    max_result_rows: "next_query",
  },
  execution: { restriction_enabled: "high", windows: "high", osc: "high" },
  "ai-budget": {
    enforced: "immediate",
    currency: "next_reservation",
    daily_budget_minor: "next_reservation",
    alert_threshold_percent: "next_usage_entry",
  },
  branding: { product_name: "immediate", login_description: "next_session", logo_asset_id: "next_page_load", favicon_asset_id: "next_page_load" },
};

const DEFAULT_SETTINGS: Record<string, SettingsValue> = {
  general: { registration_enabled: false, system_timezone: "Asia/Shanghai", default_locale: "zh-CN", public_base_url: "" },
  query: { approval_enabled: true, default_timeout_ms: 30000, maximum_approved_access_duration_minutes: 43200, max_result_rows: 1000 },
  execution: { restriction_enabled: false, windows: [], osc: { tool: "none", defaults: {} } },
  "ai-budget": { enforced: false, currency: "USD", daily_budget_minor: 10000, alert_threshold_percent: 80, input_price_per_million_minor: 0, output_price_per_million_minor: 0 },
  branding: { product_name: "Yearning", login_description: "", logo_asset_id: null, favicon_asset_id: null },
};

interface FixtureCredential {
  username: string;
  /** Fixture-internal plaintext; never serialized back to any read face. */
  password: string;
}

// Extends the view minus tls_verified: inside the world, TLS presence is
// the tls material object alone; the view derives tls_verified from it so
// the two can never drift apart.
interface FixtureDatasource extends Omit<Datasource, "tls_verified"> {
  credentials: Partial<Record<(typeof PURPOSES)[number], FixtureCredential>>;
  capabilities: DatasourceCapabilities | null;
  /** Fixture-internal TLS material; the read face only exposes tls_verified
   * (any non-null value means verified TLS is enforced). */
  tls: { ca: string; cert: string; key: string } | null;
}

interface FixtureProvider extends AiProvider {
  /** Fixture-internal; the read face only exposes api_key_configured. */
  apiKey: string;
}

interface FixtureReviewInput {
  id: string;
  name: string;
  state: "draft" | "enabled" | "disabled";
  definition: PromptTool["definition"];
  config_hash: string;
  version: number;
  created_at: string;
  updated_at: string;
}

interface FixturePromptTool extends FixtureReviewInput {
  description: string;
  engine: "all" | "mysql" | "postgresql";
  parameters: Record<string, number | string | boolean | string[]>;
  is_builtin: boolean;
}

interface FixtureKnowledgeEntry extends FixtureReviewInput {
  purpose: string | null;
  scope_type: "global" | "datasource" | "table";
  datasource_id: string | null;
  database_name: string | null;
  table_name: string | null;
  provenance: "manual" | "finding_conversion";
  source_finding_id: string | null;
  evaluation: KnowledgeEntryEvaluation | null;
}

interface FixtureRuleSet {
  id: string;
  name: string;
  enabled: boolean;
  prompt_tool_ids: string[];
  config_hash: string;
  version: number;
  created_at: string;
  updated_at: string;
}

interface FixtureFlowStage {
  position: number;
  datasource_id: string;
  schema_mappings: { logical_schema: string; physical_schema: string }[];
  approval_steps: { position: number; actors: { user_id: string }[] }[];
  execution_actors: { user_id: string }[];
}

interface FixtureFlow {
  id: string;
  name: string;
  flow_type: "change_review" | "query_access";
  enabled: boolean;
  rule_set_id: string | null;
  stages: FixtureFlowStage[] | null;
  approval_steps: { position: number; actors: { user_id: string }[] }[] | null;
  query_capabilities: { datasource_id: string; datasource_name: string; can_query: true; can_export: boolean }[] | null;
  version: number;
  created_at: string;
  updated_at: string;
}

interface FixtureNamespace {
  settings: SettingsValue;
  version: number;
  updated_by: string;
  updated_at: string;
}

interface FixtureAdminTask {
  id: string;
  kind:
    | "admin_connection_test"
    | "ai_provider_connection_test"
    | "identity_provider_connection_test"
    | "notification_test_delivery";
  state: "queued" | "running" | "succeeded" | "failed" | "cancelled";
  progress: { completed: number; total: number; unit: string };
  result_ref: string | null;
  // When notification_test_delivery fails, carry a bounded failure name (aligned with the real backend
  // failure_name -> Task.error Problem shape).
  error: null | { type: string; title: string; status: number; detail: string; instance?: string; request_id: string };
  created_at: string;
  updated_at: string;
}

const TASK_QUEUED_TO_RUNNING_MS = 300;
const TASK_RUNNING_TO_DONE_MS = 600;

const SEED_ADMIN_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

export const ADMIN_FIXTURE_DATASOURCE_MYSQL_ID = "4f6f1a2b-0000-4000-8000-00000000a001";
export const ADMIN_FIXTURE_DATASOURCE_PG_ID = "4f6f1a2b-0000-4000-8000-00000000a002";
export const ADMIN_FIXTURE_PROVIDER_PRIMARY_ID = "4f6f1a2b-0000-4000-8000-00000000b001";
export const ADMIN_FIXTURE_PROVIDER_BACKUP_ID = "4f6f1a2b-0000-4000-8000-00000000b002";
export const ADMIN_FIXTURE_TOOL_ENABLED_ID = "4f6f1a2b-0000-4000-8000-00000000c001";
export const ADMIN_FIXTURE_TOOL_DRAFT_ID = "4f6f1a2b-0000-4000-8000-00000000c002";
export const ADMIN_FIXTURE_TOOL_BUILTIN_ID = "4f6f1a2b-0000-4000-8000-00000000c003";
export const ADMIN_FIXTURE_KNOWLEDGE_GLOBAL_ID = "4f6f1a2b-0000-4000-8000-00000000d001";
export const ADMIN_FIXTURE_KNOWLEDGE_TABLE_ID = "4f6f1a2b-0000-4000-8000-00000000d002";
export const ADMIN_FIXTURE_KNOWLEDGE_CONVERTED_ID = "4f6f1a2b-0000-4000-8000-00000000d003";
export const ADMIN_FIXTURE_RULE_SET_ID = "4f6f1a2b-0000-4000-8000-00000000e001";
export const ADMIN_FIXTURE_FLOW_CHANGE_ID = "4f6f1a2b-0000-4000-8000-00000000f001";

function now(): string {
  return new Date().toISOString().replace(/\.\d{3}Z$/, "Z");
}

function uuid(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return "00000000-0000-4000-8000-000000000000".replace(/0/g, () =>
    Math.floor(Math.random() * 16).toString(16),
  );
}

/** Deterministic stand-in for the backend config hash (stable for identical
 * content so tests can assert "hash changes iff definition changes"). */
function configHash(...parts: string[]): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  const text = parts.join("\u0000");
  for (let i = 0; i < text.length; i += 1) {
    h1 = (h1 ^ text.charCodeAt(i)) * 0x01000193;
    h2 = (h2 + text.charCodeAt(i) * (i + 7)) * 0x85ebca6b;
    h1 >>>= 0;
    h2 >>>= 0;
  }
  return (h1 >>> 0).toString(16).padStart(8, "0") + (h2 >>> 0).toString(16).padStart(8, "0");
}

const world = {
  datasources: new Map<string, FixtureDatasource>(),
  providers: new Map<string, FixtureProvider>(),
  promptTools: new Map<string, FixturePromptTool>(),
  knowledgeEntries: new Map<string, FixtureKnowledgeEntry>(),
  ruleSets: new Map<string, FixtureRuleSet>(),
  flows: new Map<string, FixtureFlow>(),
  settings: new Map<string, FixtureNamespace>(),
  revisions: new Map<string, SettingsRevision[]>(),
  tasks: new Map<string, FixtureAdminTask>(),
  /** S008 reference rate versions (append-only; new version start closes previous open version). */
  rateVersions: new Map<string, Record<string, unknown>>(),
  /** Single-use impact tokens: key → proposed settings hash. */
  impactTokens: new Map<string, string>(),
  users: new Map<string, FixtureUser>(),
  groups: new Map<string, FixturePermissionGroup>(),
  maskingRules: new Map<string, string[]>(),
  announcementRevisions: new Map<string, FixtureAnnouncementRevision>(),
  announcementPublication: {
    revision_id: null as string | null,
    version: 1,
    published_by: null as string | null,
    published_at: null as string | null,
  },
  auditEvents: [] as FixtureAuditEvent[],
  identityProviders: new Map<string, FixtureIdentityProvider>(),
  notificationChannels: new Map<string, FixtureNotificationChannel>(),
  notificationDeliveries: [] as FixtureNotificationDelivery[],
  notificationRules: new Map<string, FixtureNotificationRule>(),
  migrationRun: null as FixtureMigrationRun | null,
};

export function resetAdminFixture(): void {
  world.datasources.clear();
  world.providers.clear();
  world.promptTools.clear();
  world.knowledgeEntries.clear();
  world.ruleSets.clear();
  world.flows.clear();
  world.settings.clear();
  world.revisions.clear();
  world.tasks.clear();
  world.impactTokens.clear();
  world.users.clear();
  world.groups.clear();
  world.maskingRules.clear();
  world.announcementRevisions.clear();
  world.rateVersions.clear();
  world.announcementPublication = { revision_id: null, version: 1, published_by: null, published_at: null };
  world.auditEvents = [];
  world.identityProviders.clear();
  world.notificationChannels.clear();
  world.notificationDeliveries = [];
  world.notificationRules.clear();
  world.migrationRun = null;
  seedAdminFixture();
}

const PROD_WINDOW_DEFINITION: ReviewInputDefinition = {
  knowledge_text: "Production DDL is only allowed inside the announced maintenance window.",
  max_severity: "critical",
  version: 1,
};

const ORDERS_HUGE_DEFINITION: ReviewInputDefinition = {
  knowledge_text: "The orders table exceeds 500M rows; any unbounded scan is critical.",
  max_severity: "critical",
  version: 1,
};

const CHARSET_DEFINITION: ReviewInputDefinition = {
  knowledge_text: "This datasource standardizes on UTF8; new columns must not introduce latin1.",
  max_severity: "medium",
  version: 1,
};

/** Deterministic admin-domain baseline shared by vitest and e2e. */
export function seedAdminFixture(): void {
  const ts = now();
  world.datasources.set(ADMIN_FIXTURE_DATASOURCE_MYSQL_ID, {
    id: ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
    name: "prod-order-mysql",
    engine: "mysql",
    compatibility_mode: "mysql",
    deployment_kind: "native",
    host: "10.0.0.11",
    port: 3306,
    database_name: null,
    enabled: true,
    test_purpose: "review",
    credential_status: { review: true, query: true, execution: true },
    referenced_by_flow_count: 1,
    version: 3,
    created_at: ts,
    updated_at: ts,
    credentials: {
      review: { username: "review_ro", password: "revpw-1" },
      query: { username: "query_ro", password: "qrypw-1" },
      execution: { username: "exec_rw", password: "execpw-1" },
    },
    tls: null,
    capabilities: {
      datasource_id: ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
      detected_version: "8.0.36",
      identity_fingerprint: "mysql:prod-order-mysql:inst-77",
      capabilities: { change_review: true, execution: true, query: true, export: true, gh_ost: true },
      checked_at: ts,
    },
  });
  world.datasources.set(ADMIN_FIXTURE_DATASOURCE_PG_ID, {
    id: ADMIN_FIXTURE_DATASOURCE_PG_ID,
    name: "analytics-pg",
    engine: "postgresql",
    compatibility_mode: "postgresql",
    deployment_kind: "cloud",
    host: "10.0.0.12",
    port: 5432,
    database_name: "analytics",
    enabled: true,
    test_purpose: "review",
    credential_status: { review: true, query: true },
    referenced_by_flow_count: 0,
    version: 1,
    created_at: ts,
    updated_at: ts,
    credentials: {
      review: { username: "review_ro", password: "revpw-2" },
      query: { username: "query_ro", password: "qrypw-2" },
    },
    // Seeded with a full CA + client pair so the list renders both TLS
    // states (mysql plaintext, postgresql verified) without any write.
    tls: {
      ca: "-----BEGIN CERTIFICATE-----\nMIIB-fixture-ca\n-----END CERTIFICATE-----\n",
      cert: "-----BEGIN CERTIFICATE-----\nMIIB-fixture-client-cert\n-----END CERTIFICATE-----\n",
      key: "-----BEGIN PRIVATE KEY-----\nMIIB-fixture-client-key\n-----END PRIVATE KEY-----\n",
    },
    capabilities: null,
  });
  // Owner test catalog (flow-catalog.ts): one datasource row per catalog
  // identity so the flow editor joins real catalog names, not UUID
  // fallbacks. Plaintext connections, one credential purpose, no TLS.
  // Created a day after the baseline clock so the baseline rows stay on
  // page 1 of the paginated tables (created_at asc, id asc). Derived from
  // now() so the ordering holds under both the real clock (vitest) and the
  // pinned e2e clock.
  const catalogTs = new Date(Date.now() + 86_400_000).toISOString().replace(/\.\d{3}Z$/, "Z");
  for (const entry of CATALOG_DATASOURCES) {
    const referenced = CATALOG_FLOWS.filter((flow) =>
      flow.stages.some((stage) => stage.datasource_id === entry.id),
    ).length;
    world.datasources.set(entry.id, {
      id: entry.id,
      name: entry.name,
      engine: entry.engine,
      compatibility_mode: entry.compatibility_mode,
      deployment_kind: "native",
      host: `10.1.0.${String(10 + (Number(entry.id.slice(-2)) % 200))}`,
      port: entry.engine === "postgresql" ? 5432 : 3306,
      database_name: null,
      enabled: true,
      test_purpose: "review",
      credential_status: { review: true, query: true, execution: true },
      referenced_by_flow_count: referenced,
      version: 1,
      created_at: catalogTs,
      updated_at: catalogTs,
      credentials: {
        review: { username: "review_ro", password: `catpw-${entry.id.slice(-3)}` },
        query: { username: "query_ro", password: `catpw-${entry.id.slice(-3)}` },
        execution: { username: "exec_rw", password: `catpw-${entry.id.slice(-3)}` },
      },
      tls: null,
      capabilities: null,
    });
  }
  world.providers.set(ADMIN_FIXTURE_PROVIDER_PRIMARY_ID, {
    id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
    name: "primary-glm",
    provider_kind: "openai_compatible",
    base_url: "https://open.bigmodel.cn/api/paas/v4",
    model_name: "glm-4.6",
    enabled: true,
    selection_priority: 1,
    api_key_configured: true,
    // S008 generation parameters readback (owner 2026-10-31 FE package): stored values take precedence on prefill.
    configuration: { reasoning_effort: "medium", temperature: 0.7, structured_output_mode: "json_schema" },
    version: 1,
    created_at: ts,
    updated_at: ts,
    apiKey: "pkey-a1",
  });
  world.providers.set(ADMIN_FIXTURE_PROVIDER_BACKUP_ID, {
    id: ADMIN_FIXTURE_PROVIDER_BACKUP_ID,
    name: "backup-deepseek",
    provider_kind: "openai_compatible",
    base_url: "https://api.deepseek.com/v1",
    model_name: "deepseek-chat",
    enabled: true,
    selection_priority: 2,
    api_key_configured: true,
    version: 1,
    created_at: ts,
    updated_at: ts,
    apiKey: "pkey-b1",
  });
function seededSettingsFor(ns: string): SettingsValue {
  const base = structuredClone(DEFAULT_SETTINGS[ns]) as SettingsValue;
  if (ns === "query") {
    return { ...base, approval_enabled: settingsBridge.queryApprovalEnabled };
  }
  if (ns === "execution") {
    return {
      ...base,
      restriction_enabled: settingsBridge.executionRestrictionEnabled,
      windows: structuredClone(settingsBridge.executionWindows) as unknown as ExecutionWindow[],
    };
  }
  return base;
}

  for (const ns of SETTING_NAMESPACES) {
    // §18.47: seed query/execution from the persisted settings bridge so
    // the displayed rows match the behavior that survived the page load.
    const seed = seededSettingsFor(ns);
    world.settings.set(ns, {
      settings: seed,
      version: 1,
      updated_by: SEED_ADMIN_ID,
      updated_at: ts,
    });
    world.revisions.set(ns, []);
  }
  world.promptTools.set(ADMIN_FIXTURE_TOOL_ENABLED_ID, {
    id: ADMIN_FIXTURE_TOOL_ENABLED_ID,
    name: "dml-where-guard",
    description: "DML statements must carry a WHERE clause that bounds the affected rows.",
    state: "enabled",
    engine: "all",
    parameters: { max_full_scan_rows: 100000 },
    definition: {
      knowledge_text: "DML statements must carry a WHERE clause that bounds the affected rows.",
      max_severity: "high",
      version: 1,
    },
    config_hash: configHash(
      "dml-where-guard",
      "all",
      JSON.stringify({
        knowledge_text: "DML statements must carry a WHERE clause that bounds the affected rows.",
        max_severity: "high",
        version: 1,
      }),
      JSON.stringify({ max_full_scan_rows: 100000 }),
    ),
    version: 2,
    created_at: ts,
    updated_at: ts,
    is_builtin: false,
  });
  world.promptTools.set(ADMIN_FIXTURE_TOOL_DRAFT_ID, {
    id: ADMIN_FIXTURE_TOOL_DRAFT_ID,
    name: "table-comment-check",
    description: "Every new table must declare a table comment and column comments.",
    state: "draft",
    engine: "mysql",
    parameters: {},
    definition: {
      knowledge_text: "Every new table must declare a table comment and column comments.",
      max_severity: "low",
      version: 1,
    },
    config_hash: configHash(
      "table-comment-check",
      "mysql",
      JSON.stringify({
        knowledge_text: "Every new table must declare a table comment and column comments.",
        max_severity: "low",
        version: 1,
      }),
      JSON.stringify({}),
    ),
    version: 1,
    created_at: ts,
    updated_at: ts,
    is_builtin: false,
  });
  // The one built-in skill package (PRD 9.3: the lexical-layer guard set).
  // System-owned: the definition hash is frozen, parameters keep their key
  // set and types, only the state may be toggled, and delete is refused.
  const BUILTIN_LEXICAL_DEFINITION: ReviewInputDefinition = {
    knowledge_text:
      "Built-in lexical guards: unbounded DML without WHERE, TRUNCATE/DROP statements, and IN/batch/group-count/per-row size limits must be flagged deterministically.",
    max_severity: "critical",
    version: 1,
  };
  const BUILTIN_LEXICAL_PARAMETERS = {
    max_in_items: 1000,
    max_batch_rows: 10000,
    max_statement_bytes: 65536,
  };
  world.promptTools.set(ADMIN_FIXTURE_TOOL_BUILTIN_ID, {
    id: ADMIN_FIXTURE_TOOL_BUILTIN_ID,
    name: "builtin-lexical-guards",
    description:
      "内置词法守卫：无界DML必须携带WHERE，TRUNCATE/DROP阻断，IN列表/批量行数/语句尺寸超限判定。",
    state: "enabled",
    engine: "all",
    parameters: BUILTIN_LEXICAL_PARAMETERS,
    definition: BUILTIN_LEXICAL_DEFINITION,
    config_hash: configHash(
      "builtin-lexical-guards",
      "all",
      JSON.stringify(BUILTIN_LEXICAL_DEFINITION),
      JSON.stringify(BUILTIN_LEXICAL_PARAMETERS),
    ),
    version: 1,
    created_at: ts,
    updated_at: ts,
    is_builtin: true,
  });
  world.knowledgeEntries.set(ADMIN_FIXTURE_KNOWLEDGE_GLOBAL_ID, {
    id: ADMIN_FIXTURE_KNOWLEDGE_GLOBAL_ID,
    name: "prod-window-policy",
    purpose: "Global boundary: schema changes on production replicas follow the maintenance window.",
    state: "enabled",
    scope_type: "global",
    datasource_id: null,
    database_name: null,
    table_name: null,
    definition: PROD_WINDOW_DEFINITION,
    config_hash: configHash("prod-window-policy", "global", "", "", "", JSON.stringify(PROD_WINDOW_DEFINITION)),
    version: 1,
    created_at: ts,
    updated_at: ts,
    provenance: "manual",
    source_finding_id: null,
    evaluation: {
      pass: true,
      schema_subset_ok: true,
      privacy_ok: true,
      injection_ok: true,
      severity_ok: true,
      findings: [],
      checked_at: ts,
    },
  });
  world.knowledgeEntries.set(ADMIN_FIXTURE_KNOWLEDGE_TABLE_ID, {
    id: ADMIN_FIXTURE_KNOWLEDGE_TABLE_ID,
    name: "orders-huge-table",
    purpose: "orders table is huge: full scans are critical.",
    state: "draft",
    scope_type: "table",
    datasource_id: ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
    database_name: "orderdb",
    table_name: "orders",
    definition: ORDERS_HUGE_DEFINITION,
    config_hash: configHash("orders-huge-table", "table", ADMIN_FIXTURE_DATASOURCE_MYSQL_ID, "orderdb", "orders", JSON.stringify(ORDERS_HUGE_DEFINITION)),
    version: 1,
    created_at: ts,
    updated_at: ts,
    provenance: "manual",
    source_finding_id: null,
    evaluation: null,
  });
  world.knowledgeEntries.set(ADMIN_FIXTURE_KNOWLEDGE_CONVERTED_ID, {
    id: ADMIN_FIXTURE_KNOWLEDGE_CONVERTED_ID,
    name: "charset-uniformity",
    purpose: "Converted from a historical review finding.",
    state: "disabled",
    scope_type: "datasource",
    datasource_id: ADMIN_FIXTURE_DATASOURCE_PG_ID,
    database_name: null,
    table_name: null,
    definition: CHARSET_DEFINITION,
    config_hash: configHash("charset-uniformity", "datasource", ADMIN_FIXTURE_DATASOURCE_PG_ID, "", "", JSON.stringify(CHARSET_DEFINITION)),
    version: 1,
    created_at: ts,
    updated_at: ts,
    provenance: "finding_conversion",
    source_finding_id: "9a1b2c3d-0000-4000-8000-00000000e999",
    evaluation: {
      pass: true,
      schema_subset_ok: true,
      privacy_ok: true,
      injection_ok: true,
      severity_ok: true,
      findings: [],
      checked_at: ts,
    },
  });
  world.ruleSets.set(ADMIN_FIXTURE_RULE_SET_ID, {
    id: ADMIN_FIXTURE_RULE_SET_ID,
    name: "change-review-default",
    enabled: true,
    prompt_tool_ids: [ADMIN_FIXTURE_TOOL_ENABLED_ID],
    config_hash: configHash("change-review-default", "v1"),
    version: 1,
    created_at: ts,
    updated_at: ts,
  });
  world.flows.set(ADMIN_FIXTURE_FLOW_CHANGE_ID, {
    id: ADMIN_FIXTURE_FLOW_CHANGE_ID,
    name: "生产变更默认流程",
    flow_type: "change_review",
    enabled: true,
    rule_set_id: ADMIN_FIXTURE_RULE_SET_ID,
    stages: [
      {
        position: 1,
        datasource_id: ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
        schema_mappings: [{ logical_schema: "app", physical_schema: "app" }],
        approval_steps: [{ position: 1, actors: [{ user_id: SEED_ADMIN_ID }] }],
        execution_actors: [{ user_id: SEED_ADMIN_ID }],
      },
    ],
    approval_steps: null,
    query_capabilities: null,
    version: 1,
    created_at: ts,
    updated_at: ts,
  });
  seedSiteFixture();
  // Owner test catalog (flow-catalog.ts): the same 20 flows the submission
  // wizard offers, so a flow submitted from the wizard is editable here.
  // Approval steps and executors reference the seeded admin identity. Seeded
  // LAST so pageOf's insertion-order slicing keeps the baseline flows on
  // table page 1.
  for (const spec of CATALOG_FLOWS) {
    world.flows.set(spec.id, {
      id: spec.id,
      name: spec.name,
      flow_type: "change_review",
      enabled: spec.enabled,
      rule_set_id: ADMIN_FIXTURE_RULE_SET_ID,
      stages: spec.stages.map((stage, index) => ({
        position: index + 1,
        datasource_id: stage.datasource_id,
        schema_mappings: [{ logical_schema: "app", physical_schema: "app" }],
        approval_steps: Array.from({ length: stage.approvers }, (_, position) => ({
          position: position + 1,
          actors: [{ user_id: SEED_ADMIN_ID }],
        })),
        execution_actors: Array.from({ length: stage.executors }, () => ({
          user_id: SEED_ADMIN_ID,
        })),
      })),
      approval_steps: null,
      query_capabilities: null,
      version: 1,
      created_at: catalogTs,
      updated_at: catalogTs,
    });
  }
  seedVolumeFixtures();
}

// ---- Volume rows (owner 2026-11-03: behavior under high-volume data) --------------------
//
// Browser-only synthetic volume: fill the users table to 50 and add 12 more
// datasources so the admin tables, search and pagination run against bulk
// data. vitest keeps the deterministic minimal world (MODE=test guard), and
// the rows are seeded LAST so insertion-order paging keeps every baseline
// row on page 1 in the dev browser too.
function seedVolumeFixtures(): void {
  if (import.meta.env.MODE === "test") return;
  const base = now();
  const surnames = ["张", "李", "王", "刘", "陈", "杨", "赵", "黄", "周", "吴"];
  const given = ["伟", "芳", "娜", "敏", "静", "磊", "军", "洋", "勇", "艳", "杰", "涛"];
  let serial = 1;
  while (world.users.size < 50) {
    const index = serial;
    const id = `a0000000-0000-4000-8000-0000000${String(1000 + index).padStart(4, "0")}`;
    if (!world.users.has(id)) {
      const created = new Date(Date.parse(base) + index * 3600 * 1000)
        .toISOString().replace(/\.\d{3}Z$/, "Z");
      world.users.set(id, {
        id,
        username: `qa-dba-${String(index).padStart(2, "0")}`,
        display_name: `${surnames[index % surnames.length] ?? ""}${given[(index * 3) % given.length] ?? ""}`,
        email: index % 2 === 0 ? `qa-dba-${String(index).padStart(2, "0")}@yearning.test` : null,
        is_builtin_admin: false,
        version: 1,
        created_at: created,
        updated_at: created,
        activeOrderCount: index % 9 === 0 ? (index % 5) + 1 : 0,
        templateReferenceCount: index % 11 === 0 ? 1 : 0,
      });
    }
    serial += 1;
  }
  const volumeDatasources = [
    { name: "dev-orders-mysql", engine: "mysql" }, { name: "dev-users-mysql", engine: "mysql" },
    { name: "uat-orders-mysql", engine: "mysql" }, { name: "uat-risk-pg", engine: "postgresql" },
    { name: "perf-orders-mysql", engine: "mysql" }, { name: "perf-analytics-pg", engine: "postgresql" },
    { name: "archive-orders-mysql", engine: "mysql" }, { name: "archive-logs-pg", engine: "postgresql" },
    { name: "bi-dw-pg", engine: "postgresql" }, { name: "bi-mart-pg", engine: "postgresql" },
    { name: "mq-messages-mysql", engine: "mysql" }, { name: "inventory-mysql", engine: "mysql" },
  ] as const;
  volumeDatasources.forEach((entry, index) => {
    const id = `a0000000-0000-4000-8000-0000000${String(2000 + index).padStart(4, "0")}`;
    if (world.datasources.has(id)) return;
    const created = new Date(Date.parse(base) + (50 + index) * 3600 * 1000)
      .toISOString().replace(/\.\d{3}Z$/, "Z");
    world.datasources.set(id, {
      id,
      name: entry.name,
      engine: entry.engine,
      compatibility_mode: entry.engine,
      deployment_kind: index % 4 === 3 ? "cloud" : "native",
      host: `10.2.${String(index % 16)}.${String(20 + index)}`,
      port: entry.engine === "postgresql" ? 5432 : 3306,
      database_name: null,
      enabled: index % 6 !== 5,
      test_purpose: index % 3 === 2 ? "query" : "review",
      credential_status:
        index % 3 === 2 ? { query: true } : { review: true, query: true, execution: index % 2 === 0 },
      referenced_by_flow_count: 0,
      version: 1,
      created_at: created,
      updated_at: created,
      credentials:
        index % 3 === 2
          ? { query: { username: "query_ro", password: `volpw-${String(index).padStart(2, "0")}` } }
          : {
              review: { username: "review_ro", password: `volpw-${String(index).padStart(2, "0")}` },
              query: { username: "query_ro", password: `volpw-${String(index).padStart(2, "0")}` },
            },
      tls: null,
      capabilities: null,
    });
  });
}

// ===========================================================================
// FE-F10 site domains: users, permission groups, flows (full model + masking
// rules), announcements, audit events, identity providers, notification
// channels and the migration review run. Mirrors backend/internal/identity,
// workflow and the declared OpenAPI views; admin CRUD stays behind the
// adminGuard while /announcements/current answers any authenticated session.
// ===========================================================================

export const ADMIN_FIXTURE_USER_MEMBER_ID = "7a1a3c4d-1111-4111-8111-00000000e101";
export const ADMIN_FIXTURE_USER_BLOCKED_ID = "7a1a3c4d-1111-4111-8111-00000000e102";
export const ADMIN_FIXTURE_GROUP_ID = "7a1a3c4d-2222-4222-8222-00000000b101";
export const ADMIN_FIXTURE_FLOW_QUERY_ID = "7a1a3c4d-3333-4333-8333-00000000f002";
export const ADMIN_FIXTURE_REVISION_PUBLISHED_ID = "7a1a3c4d-4444-4444-8444-00000000c101";
export const ADMIN_FIXTURE_LDAP_ID = "7a1a3c4d-5555-4555-8555-00000000d101";
export const ADMIN_FIXTURE_CHANNEL_EMAIL_ID = "7a1a3c4d-6666-4666-8666-000000003101";
export const ADMIN_FIXTURE_MIGRATION_RUN_ID = "7a1a3c4d-7777-4777-8777-000000004101";

interface FixtureUser extends User {
  /** Fixture-internal: deletion blockers derived from seeded relations. */
  activeOrderCount: number;
  templateReferenceCount: number;
}

type FixturePermissionGroup = PermissionGroup;

type FixtureAnnouncementRevision = AnnouncementRevision;

type FixtureAuditEvent = AuditEvent;

interface FixtureIdentityProvider extends IdentityProvider {
  /** Fixture-internal client secret; the read face exposes secret_configured. */
  clientSecret: string | null;
}

interface FixtureNotificationChannel extends NotificationChannel {
  /** Fixture-internal secret; the read face exposes secret_configured. */
  secret: string | null;
}

type FixtureNotificationDelivery = NotificationDelivery;

type FixtureNotificationRule = NotificationRule;

interface FixtureMigrationCandidate {
  candidate_id: string;
  kind: "permission_group_flow_grant" | "single_stage_flow" | "multi_stage_flow" | "rule_set";
  source_refs: string[];
  target_definition_hash: string;
  risk: "no_expansion" | "possible_expansion" | "unmapped";
  coverage_added: string[];
  coverage_missing: string[];
  confirmed: boolean;
  confirmed_by: "admin" | null;
  confirmed_at: string | null;
}

interface FixtureMigrationRun {
  id: string;
  state: "planned" | "dry_run_running" | "awaiting_confirmation" | "approved" | "applying" | "verifying" | "verified" | "failed";
  manifest_hash: string | null;
  table_results: LegacyMigrationRun["table_results"];
  candidates: FixtureMigrationCandidate[];
  version: number;
  started_at: string;
  updated_at: string;
}

function sha256Hex(input: string): string {
  // Deterministic stand-in hash for fixture data (not a security boundary).
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  for (let i = 0; i < input.length; i += 1) {
    h1 = ((h1 ^ input.charCodeAt(i)) * 16777619) >>> 0;
    h2 = ((((h2 + input.charCodeAt(i) * 31) >>> 0) ^ (h1 << 3)) >>> 0);
  }
  return `${h1.toString(16).padStart(8, "0")}${h2.toString(16).padStart(8, "0")}`.repeat(4);
}

/** Markdown → sanitized HTML mirror (dashboard/announcements.go): the mock
 * supports headings, bold, inline code and paragraphs; everything else is
 * escaped. Script/iframe/event-attribute/style input is escaped away — the
 * server stays the sanitizer authority. */
/** Mock sanitizer: the production service renders markdown server-side and
 * hands back sanitized HTML, so this fixture renderer only needs to cover the
 * block shapes the UI styles (headings, lists, quotes, rules, code, links).
 * Everything is escaped first; no attribute ever comes from the source. */
function sanitizeMarkdown(source: string): string {
  const escapeHtml = (value: string) =>
    value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const inline = (value: string) =>
    escapeHtml(value)
      .replace(
        /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
        '<a href="$2" rel="noreferrer noopener" target="_blank">$1</a>',
      )
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/`([^`]+)`/g, "<code>$1</code>");
  const headingLevel = (line: string) =>
    Math.min(3, Math.max(2, (line.match(/^#+/)?.[0].length ?? 1) + 1));
  return source
    .split(/\n\n+/)
    .map((block) => {
      const lines = block.split("\n").filter((line) => line.trim() !== "");
      if (lines.length === 0) return "";
      if (lines.every((line) => line.startsWith("#"))) {
        const level = headingLevel(lines[0] ?? "");
        const text = lines.join(" ").replace(/^#+\s*/, "");
        return `<h${String(level)}>${escapeHtml(text)}</h${String(level)}>`;
      }
      if (lines.every((line) => /^[-*]\s+/.test(line))) {
        const items = lines
          .map((line) => `<li>${inline(line.replace(/^[-*]\s+/, ""))}</li>`)
          .join("");
        return `<ul>${items}</ul>`;
      }
      if (lines.every((line) => /^\d+\.\s+/.test(line))) {
        const items = lines
          .map((line) => `<li>${inline(line.replace(/^\d+\.\s+/, ""))}</li>`)
          .join("");
        return `<ol>${items}</ol>`;
      }
      if (lines.every((line) => line.startsWith(">"))) {
        const text = lines.map((line) => line.replace(/^>\s?/, "")).join(" ");
        return `<blockquote><p>${inline(text)}</p></blockquote>`;
      }
      if (lines.every((line) => /^(-{3,}|\*{3,})$/.test(line.trim()))) {
        return "<hr />";
      }
      return `<p>${lines.map((line) => inline(line)).join("<br />")}</p>`;
    })
    .join("\n");
}

function seedSiteFixture(): void {
  const ts = now();
  const tsLater = new Date(Date.parse(ts) + 3600 * 1000).toISOString();
  const tsMuchLater = new Date(Date.parse(ts) + 24 * 3600 * 1000).toISOString();

  world.users.set(SEED_ADMIN_ID, {
    id: SEED_ADMIN_ID,
    username: "admin",
    display_name: "Administrator",
    email: "admin@yearning.test",
    is_builtin_admin: true,
    version: 1,
    created_at: ts,
    updated_at: ts,
    activeOrderCount: 0,
    templateReferenceCount: 0,
  });
  world.users.set(ADMIN_FIXTURE_USER_MEMBER_ID, {
    id: ADMIN_FIXTURE_USER_MEMBER_ID,
    username: "dba-anne",
    display_name: "Anne Zhou",
    email: "anne@yearning.test",
    is_builtin_admin: false,
    version: 1,
    created_at: ts,
    updated_at: ts,
    activeOrderCount: 0,
    templateReferenceCount: 0,
  });
  world.users.set(ADMIN_FIXTURE_USER_BLOCKED_ID, {
    id: ADMIN_FIXTURE_USER_BLOCKED_ID,
    username: "busy-bob",
    display_name: "Bob Li",
    email: null,
    is_builtin_admin: false,
    version: 1,
    created_at: ts,
    updated_at: ts,
    activeOrderCount: 2,
    templateReferenceCount: 1,
  });

  world.groups.set(ADMIN_FIXTURE_GROUP_ID, {
    id: ADMIN_FIXTURE_GROUP_ID,
    name: "变更发布组",
    enabled: true,
    member_user_ids: [ADMIN_FIXTURE_USER_MEMBER_ID],
    granted_flow_ids: [ADMIN_FIXTURE_FLOW_CHANGE_ID, ADMIN_FIXTURE_FLOW_QUERY_ID],
    version: 1,
    created_at: ts,
    updated_at: ts,
  });

  world.flows.set(ADMIN_FIXTURE_FLOW_QUERY_ID, {
    id: ADMIN_FIXTURE_FLOW_QUERY_ID,
    name: "在线只读查询流程",
    flow_type: "query_access",
    enabled: true,
    rule_set_id: null,
    stages: null,
    approval_steps: [{ position: 1, actors: [{ user_id: SEED_ADMIN_ID }] }],
    query_capabilities: [
      { datasource_id: ADMIN_FIXTURE_DATASOURCE_MYSQL_ID, datasource_name: "prod-order-mysql", can_query: true, can_export: true },
      { datasource_id: ADMIN_FIXTURE_DATASOURCE_PG_ID, datasource_name: "analytics-pg", can_query: true, can_export: false },
    ],
    version: 1,
    created_at: ts,
    updated_at: ts,
  });
  world.maskingRules.set(`${ADMIN_FIXTURE_FLOW_QUERY_ID}:${ADMIN_FIXTURE_DATASOURCE_MYSQL_ID}`, [
    "email",
    "phone",
  ]);

  const publishedRevision: FixtureAnnouncementRevision = {
    id: ADMIN_FIXTURE_REVISION_PUBLISHED_ID,
    revision_number: 1,
    title: "季度维护窗口公告",
    sanitized_html: sanitizeMarkdown(
      [
        "## 本季度维护安排",
        "",
        "为保证生产库稳定，例行维护窗口固定为**每周日 02:00-06:00**，请避开该时段提交紧急变更。",
        "",
        "- 维护期内 `DDL` 工单暂停执行，`DML` 工单照常排队",
        "- 审批不受影响，执行队列会在维护结束后自动恢复",
        "- 紧急变更请提前在工单里注明，并在审批时同步值班 DBA",
        "",
        "> 维护窗口调整以本公告为准，历史公告不再另行通知。",
        "",
        "更多排期见 [值班表](https://yearning.example.com/oncall)。",
      ].join("\n"),
    ),
    content_sha256: sha256Hex("revision-1"),
    sanitizer_policy_version: "mock-sanitizer-1",
    created_by_username: "admin",
    created_at: ts,
  };
  world.announcementRevisions.set(publishedRevision.id, publishedRevision);
  world.announcementPublication = {
    revision_id: publishedRevision.id,
    version: 1,
    published_by: "admin",
    published_at: ts,
  };

  const auditBase = (index: number): string => new Date(Date.parse(ts) - index * 3600 * 1000).toISOString();
  world.auditEvents = [
    {
      id: "ab6f1a2b-0000-4000-8000-000000000001",
      event_type: "auth.login",
      actor_kind: "user",
      actor_id: SEED_ADMIN_ID,
      actor_username_snapshot: "admin",
      resource_type: "auth_session",
      resource_id: null,
      action: "login",
      outcome: "succeeded",
      request_id: null,
      metadata: { behavior: "local" },
      occurred_at: auditBase(0),
      expires_at: new Date(Date.parse(auditBase(0)) + 90 * 24 * 3600 * 1000).toISOString(),
    },
    {
      id: "ab6f1a2b-0000-4000-8000-000000000002",
      event_type: "datasource.updated",
      actor_kind: "user",
      actor_id: SEED_ADMIN_ID,
      actor_username_snapshot: "admin",
      resource_type: "datasource",
      resource_id: ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
      action: "replace",
      outcome: "succeeded",
      request_id: null,
      metadata: { secret: "changed" },
      occurred_at: auditBase(3),
      expires_at: new Date(Date.parse(auditBase(3)) + 90 * 24 * 3600 * 1000).toISOString(),
    },
    {
      id: "ab6f1a2b-0000-4000-8000-000000000003",
      event_type: "order.sql_revealed",
      actor_kind: "user",
      actor_id: ADMIN_FIXTURE_USER_MEMBER_ID,
      actor_username_snapshot: "dba-anne",
      resource_type: "change_order",
      resource_id: "7c6f1a2b-0000-4000-8000-00000000c0a1",
      action: "reveal_sql",
      outcome: "succeeded",
      request_id: null,
      metadata: { sql_hash: `sha256:${sha256Hex("audit")}` },
      occurred_at: auditBase(6),
      expires_at: new Date(Date.parse(auditBase(6)) + 90 * 24 * 3600 * 1000).toISOString(),
    },
  ];

  world.identityProviders.set(ADMIN_FIXTURE_LDAP_ID, {
    id: ADMIN_FIXTURE_LDAP_ID,
    provider_key: "ldap",
    provider_kind: "ldap",
    display_name: "Corporate LDAP",
    enabled: true,
    configuration: {
      host: "ldap.corp.test",
      port: 636,
      transport: "ldaps",
      server_name: "ldap.corp.test",
      bind_dn: "cn=yearning,ou=services,dc=corp,dc=test",
      base_dn: "ou=people,dc=corp,dc=test",
      user_filter: "(&(objectClass=organizationalPerson)(uid={username}))",
      username_attribute: "uid",
      display_name_attribute: "cn",
      email_attribute: "mail",
      connect_timeout_ms: 5000,
      bind_timeout_ms: 5000,
      search_timeout_ms: 5000,
    },
    secret_configured: true,
    version: 1,
    created_at: ts,
    updated_at: ts,
    clientSecret: "ldapsec-1",
  });

  world.notificationChannels.set(ADMIN_FIXTURE_CHANNEL_EMAIL_ID, {
    id: ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
    kind: "email",
    name: "ops-mail",
    enabled: true,
    configuration: {
      host: "smtp.corp.test",
      port: 465,
      tls_mode: "required",
      username: "yearning@corp.test",
      from_address: "yearning@corp.test",
    },
    secret_configured: true,
    version: 1,
    created_at: ts,
    updated_at: ts,
    secret: "smtppass-1",
  });
  world.notificationRules.set("8c5f1a2b-0000-4000-8000-000000000001", {
    id: "8c5f1a2b-0000-4000-8000-000000000001",
    name: "执行失败通知提交人",
    enabled: true,
    flow_id: null,
    event_type: "execution.failed",
    target_kind: "order_submitter",
    channel_id: ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
    version: 1,
    created_at: ts,
    updated_at: ts,
  });
  world.notificationDeliveries = [
    {
      id: "9d6f1a2b-0000-4000-8000-000000000001",
      domain_event_id: "5e6f1a2b-0000-4000-8000-000000000001",
      event_type: "execution.failed",
      notification_channel_id: ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
      channel_kind: "email",
      channel_name: "ops-mail",
      recipient_user_id: SEED_ADMIN_ID,
      masked_target: "ye***@corp.test",
      state: "succeeded",
      delivery_attempt_count: 1,
      next_attempt_at: null,
      last_error_code: null,
      created_at: tsLater,
      updated_at: tsLater,
    },
    {
      id: "9d6f1a2b-0000-4000-8000-000000000002",
      domain_event_id: "5e6f1a2b-0000-4000-8000-000000000002",
      event_type: "execution.result_unknown",
      notification_channel_id: ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
      channel_kind: "email",
      channel_name: "ops-mail",
      recipient_user_id: ADMIN_FIXTURE_USER_MEMBER_ID,
      masked_target: "op***@corp.test",
      state: "sending",
      delivery_attempt_count: 3,
      next_attempt_at: tsMuchLater,
      last_error_code: "smtp_timeout",
      created_at: tsLater,
      updated_at: tsMuchLater,
    },
  ];

  world.migrationRun = {
    id: ADMIN_FIXTURE_MIGRATION_RUN_ID,
    state: "awaiting_confirmation",
    manifest_hash: `sha256:${sha256Hex("migration-manifest")}`,
    table_results: [
      {
        source_table: "core_sqlorder",
        target_tables: ["change_orders", "change_order_stages"],
        read: 1200,
        written: 1198,
        excluded: 2,
        quarantined: 0,
        failed: 0,
        reconciliation_passed: true,
      },
      {
        source_table: "core_queryorder",
        target_tables: ["query_access_requests"],
        read: 340,
        written: 340,
        excluded: 0,
        quarantined: 0,
        failed: 0,
        reconciliation_passed: true,
      },
      {
        source_table: "core_workflowdetail",
        target_tables: ["flow_stages", "flow_approval_steps"],
        read: 88,
        written: 86,
        excluded: 0,
        quarantined: 2,
        failed: 0,
        reconciliation_passed: false,
      },
    ],
    candidates: [
      {
        candidate_id: "6c6f1a2b-0000-4000-8000-000000000001",
        kind: "permission_group_flow_grant",
        source_refs: ["core_group.permissions → group_a"],
        target_definition_hash: `sha256:${sha256Hex("candidate-1")}`,
        risk: "no_expansion",
        coverage_added: ["change_flow:生产变更默认流程", "query_flow:在线只读查询流程"],
        coverage_missing: [],
        confirmed: false,
        confirmed_by: null,
        confirmed_at: null,
      },
      {
        candidate_id: "6c6f1a2b-0000-4000-8000-000000000002",
        kind: "multi_stage_flow",
        source_refs: ["core_flow.tpl → multi-stage-legacy"],
        target_definition_hash: `sha256:${sha256Hex("candidate-2")}`,
        risk: "possible_expansion",
        coverage_added: ["stage-1:mysql", "stage-2:pg"],
        coverage_missing: ["stage-3:oracle（v4不支持）"],
        confirmed: false,
        confirmed_by: null,
        confirmed_at: null,
      },
    ],
    version: 1,
    started_at: ts,
    updated_at: ts,
  };
}

function userView(user: FixtureUser): User {
  const { activeOrderCount: _activeOrderCount, templateReferenceCount: _templateReferenceCount, ...view } = user;
  return view;
}

function deletionImpactOf(user: FixtureUser) {
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

/** FlowWrite validation mirror (workflow/service.go writeFlowGraph):
 * change flows need ≥1 stage with 1..10 approval steps and ≥1 execution
 * actor; query flows need ≥1 capability and 1..10 approval steps. */
function flowWriteError(body: Record<string, unknown>): string | null {
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

function flowViewOf(flow: FixtureFlow) {
  return {
    id: flow.id,
    name: flow.name,
    flow_type: flow.flow_type,
    enabled: flow.enabled,
    status: flow.enabled ? ("enabled" as const) : ("disabled" as const),
    rule_set_id: flow.rule_set_id,
    // FlowStageView datasource_name mirrors the backend server-side join over
    // the datasource catalog (RCP-20260904-FLOW-STAGE-DATASOURCE-NAME).
    stages:
      flow.stages === null
        ? undefined
        : flow.stages.map((stage) => ({
            ...stage,
            datasource_name:
              world.datasources.get(stage.datasource_id)?.name ?? stage.datasource_id,
          })),
    approval_steps: flow.approval_steps === null ? undefined : flow.approval_steps,
    query_capabilities: flow.query_capabilities === null ? undefined : flow.query_capabilities,
    version: flow.version,
    created_at: flow.created_at,
    updated_at: flow.updated_at,
  };
}

function migrationRunView(run: FixtureMigrationRun): LegacyMigrationRun {
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

export function siteFixtureHandlers(): HttpHandler[] {
  const requireSession = (): HttpResponse<DefaultBodyType> | null => {
    const hasSession = readStoredAuthBehavior() !== "expired";
    if (!hasSession) {
      return HttpResponse.json(
        { type: "about:blank", title: "session_expired", status: 401, detail: "no active session", request_id: ADMIN_REQUEST_ID },
        { status: 401, headers: { "Content-Type": "application/problem+json" } },
      );
    }
    return null;
  };

  return [
    // ------------------------------------------------------------- users --
    // §18.73 contract mirror: q matches username/display_name/email only; kind is
    // the RCP-20261105 column filter (builtin_admin/local_user), combined with AND.
    http.get("*/admin/users", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const q = searchOf(url);
      const kind = url.searchParams.get("kind");
      const items = [...world.users.values()]
        .filter((user) => kind === null || (kind === "builtin_admin") === user.is_builtin_admin)
        .filter((user) => matchesQ([user.username, user.display_name, user.email], q))
        .sort((a, b) => a.created_at.localeCompare(b.created_at));
      return HttpResponse.json(
        successEnvelope(pageOf(items.map(userView), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post("*/admin/users", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as {
        username?: string;
        display_name?: string;
        email?: string | null;
        password?: string;
      } | null;
      if (
        body === null || typeof body.username !== "string" || body.username.trim() === "" ||
        body.username.length > 64 || typeof body.display_name !== "string" ||
        body.display_name.trim() === "" || body.display_name.length > 128 ||
        typeof body.password !== "string" || body.password.length < 12 || body.password.length > 1024 ||
        (body.email !== null && body.email !== undefined && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(body.email))
      ) {
        return businessError(1001, "validation failed");
      }
      const requestedUsername = body.username ?? "";
      const exists = [...world.users.values()].some(
        (user) => user.username.toLowerCase() === requestedUsername.toLowerCase(),
      );
      if (exists) return businessError(1001, "username already exists");
      const user: FixtureUser = {
        id: uuid(),
        username: body.username,
        display_name: body.display_name,
        email: body.email ?? null,
        is_builtin_admin: false,
        version: 1,
        created_at: now(),
        updated_at: now(),
        activeOrderCount: 0,
        templateReferenceCount: 0,
      };
      world.users.set(user.id, user);
      return HttpResponse.json(successEnvelope(userView(user)));
    }),

    http.get("*/admin/users/:userId/deletion-impact", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const user = world.users.get(String(params.userId));
      if (user === undefined) return businessError(1002, "user not found");
      return HttpResponse.json(successEnvelope(deletionImpactOf(user)));
    }),

    http.patch("*/admin/users/:userId", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const user = world.users.get(String(params.userId));
      if (user === undefined) return businessError(1002, "user not found");
      if (versionMismatch(request, user.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as {
        display_name?: string;
        email?: string | null;
      } | null;
      if (body === null || (body.display_name === undefined && body.email === undefined)) {
        return businessError(1001, "nothing to update");
      }
      if (body.email !== null && body.email !== undefined && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(body.email)) {
        return businessError(1001, "email invalid");
      }
      if (body.display_name !== undefined) {
        if (body.display_name.trim() === "" || body.display_name.length > 128) {
          return businessError(1001, "display_name invalid");
        }
        user.display_name = body.display_name;
      }
      if (body.email !== undefined) user.email = body.email;
      user.version += 1;
      user.updated_at = now();
      return HttpResponse.json(successEnvelope(userView(user)));
    }),

    http.delete("*/admin/users/:userId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const user = world.users.get(String(params.userId));
      if (user === undefined) return businessError(1002, "user not found");
      if (user.is_builtin_admin) return businessError(1103, "builtin admin immutable");
      if (versionMismatch(request, user.version)) return businessError(1004, "version mismatch");
      if (user.activeOrderCount > 0) return businessError(1104, "user has active orders");
      if (user.templateReferenceCount > 0) {
        return businessError(1105, "user referenced by template");
      }
      world.users.delete(user.id);
      for (const group of world.groups.values()) {
        group.member_user_ids = group.member_user_ids.filter((id) => id !== user.id);
      }
      return HttpResponse.json(successEnvelope(null));
    }),

    // -------------------------------------------------- permission groups --
    // §18.73 contract mirror: q matches group name only; enabled/member_user_id/flow_id
    // are RCP-20261105 column filters combined with AND.
    http.get("*/admin/permission-groups", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const enabled = url.searchParams.get("enabled");
      const memberUserId = url.searchParams.get("member_user_id");
      const flowId = url.searchParams.get("flow_id");
      const items = [...world.groups.values()]
        .filter((group) => enabled === null || (enabled === "true") === group.enabled)
        .filter((group) => memberUserId === null || group.member_user_ids.includes(memberUserId))
        .filter((group) => flowId === null || group.granted_flow_ids.includes(flowId))
        .filter((group) => matchesQ([group.name], searchOf(url)))
        .sort((a, b) => a.created_at.localeCompare(b.created_at));
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post("*/admin/permission-groups", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as {
        name?: string;
        enabled?: boolean;
        member_user_ids?: string[];
        granted_flow_ids?: string[];
      } | null;
      if (
        body === null || typeof body.name !== "string" || body.name.trim() === "" ||
        body.name.length > 128 || typeof body.enabled !== "boolean" ||
        !Array.isArray(body.member_user_ids) || !Array.isArray(body.granted_flow_ids)
      ) {
        return businessError(1001, "validation failed");
      }
      const group: FixturePermissionGroup = {
        id: uuid(),
        name: body.name,
        enabled: body.enabled,
        member_user_ids: [...new Set(body.member_user_ids)],
        granted_flow_ids: [...new Set(body.granted_flow_ids)],
        version: 1,
        created_at: now(),
        updated_at: now(),
      };
      world.groups.set(group.id, group);
      return HttpResponse.json(successEnvelope(group));
    }),

    http.get("*/admin/permission-groups/:groupId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const group = world.groups.get(String(params.groupId));
      if (group === undefined) return businessError(1002, "group not found");
      return HttpResponse.json(successEnvelope(group));
    }),

    http.put("*/admin/permission-groups/:groupId", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const group = world.groups.get(String(params.groupId));
      if (group === undefined) return businessError(1002, "group not found");
      if (versionMismatch(request, group.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as {
        name?: string;
        enabled?: boolean;
        member_user_ids?: string[];
        granted_flow_ids?: string[];
      } | null;
      if (
        body === null || typeof body.name !== "string" || body.name.trim() === "" ||
        body.name.length > 128 || typeof body.enabled !== "boolean" ||
        !Array.isArray(body.member_user_ids) || !Array.isArray(body.granted_flow_ids)
      ) {
        return businessError(1001, "validation failed");
      }
      group.name = body.name;
      group.enabled = body.enabled;
      group.member_user_ids = [...new Set(body.member_user_ids)];
      group.granted_flow_ids = [...new Set(body.granted_flow_ids)];
      group.version += 1;
      group.updated_at = now();
      return HttpResponse.json(successEnvelope(group));
    }),

    http.delete("*/admin/permission-groups/:groupId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const group = world.groups.get(String(params.groupId));
      if (group === undefined) return businessError(1002, "group not found");
      if (versionMismatch(request, group.version)) return businessError(1004, "version mismatch");
      world.groups.delete(group.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    // ------------------------------------------------------ flows (full) --
    http.post("*/admin/flows", async ({ request }) => {
      const guard = adminGuard();
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

    http.get("*/admin/flows/:flowId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      return HttpResponse.json(successEnvelope(flowViewOf(flow)));
    }),

    http.put("*/admin/flows/:flowId", async ({ request, params }) => {
      const guard = adminGuard();
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

    http.delete("*/admin/flows/:flowId", ({ request, params }) => {
      const guard = adminGuard();
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

    // ---------------------------------------------------- masking rules --
    http.get("*/admin/flows/:flowId/masking-rules", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      if (flow.flow_type !== "query_access") {
        return businessError(1001, "masking rules only apply to query_access flows");
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

    http.put("*/admin/flows/:flowId/datasources/:datasourceId/masking-rules", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const flow = world.flows.get(String(params.flowId));
      if (flow === undefined) return businessError(1002, "flow not found");
      if (flow.flow_type !== "query_access") {
        return businessError(1001, "masking rules only apply to query_access flows");
      }
      const datasourceId = String(params.datasourceId);
      const belongs = flow.query_capabilities?.some((capability) => capability.datasource_id === datasourceId);
      if (belongs !== true) return businessError(1001, "datasource outside flow");
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
      // Unicode default full case-fold + trim, deduplicated by the fold key.
      const folded = [...new Set(body.sensitive_columns.map((column) => column.trim().toLocaleLowerCase("en-US")))];
      world.maskingRules.set(`${flow.id}:${datasourceId}`, folded);
      return HttpResponse.json(
        successEnvelope({ datasource_id: datasourceId, sensitive_columns: folded, version: 1 }),
      );
    }),

    // ---------------------------------------------------- announcements --
    http.get("*/announcements/current", () => {
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

    http.get("*/admin/announcement-revisions", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.announcementRevisions.values()].sort(
        (a, b) => b.revision_number - a.revision_number,
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post("*/admin/announcement-revisions", async ({ request }) => {
      const guard = adminGuard();
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

    http.put("*/admin/announcement-publication", async ({ request }) => {
      const guard = adminGuard();
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

    // ------------------------------------------------------ audit events --
    http.get("*/admin/audit-events", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.auditEvents].sort((a, b) => b.occurred_at.localeCompare(a.occurred_at));
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    // -------------------------------------------------- identity providers --
    http.get("*/admin/identity-providers", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.identityProviders.values()].sort((a, b) =>
        a.provider_key.localeCompare(b.provider_key),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items.map(({ clientSecret: _clientSecret, ...view }) => view), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post("*/admin/identity-providers", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.provider_key !== "string") return businessError(1001, "provider_key required");
      const kind = body.provider_kind;
      if (kind !== "ldap" && kind !== "oidc") return businessError(1001, "provider_kind invalid");
      if (kind === "ldap" && [...world.identityProviders.values()].some((p) => p.provider_kind === "ldap")) {
        return businessError(1001, "ldap is a singleton");
      }
      // Create requires the per-kind secret field (client_secret for OIDC,
      // bind_password for LDAP).
      const secret =
        kind === "oidc"
          ? (body.client_secret as { value?: string } | null | undefined)
          : (body.bind_password as { value?: string } | null | undefined);
      // SecretInput.value has minLength 1 — an empty replacement is a
      // contract violation, never a silent overwrite.
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

    http.get("*/admin/identity-providers/:providerId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      const { clientSecret: _clientSecret, ...view } = provider;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.put("*/admin/identity-providers/:providerId", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body required");
      // Secret field name follows the declared per-kind write schema:
      // client_secret for OIDC, bind_password for LDAP.
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

    http.delete("*/admin/identity-providers/:providerId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (provider.enabled) return businessError(1001, "disable before delete");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      world.identityProviders.delete(provider.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post("*/admin/identity-providers/:providerId/connection-tests", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.identityProviders.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      return taskResponse("identity_provider_connection_test", provider.id, () => {});
    }),

    // ------------------------------------------------ notification channels --
    http.get("*/admin/notification-channels", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.notificationChannels.values()].sort((a, b) =>
        a.created_at.localeCompare(b.created_at),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items.map(({ secret: _secret, ...view }) => view), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post("*/admin/notification-channels", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name.trim() === "") {
        return businessError(1001, "validation failed");
      }
      const secret = body.secret as { value?: string } | null | undefined;
      if (secret !== null && secret !== undefined && secret.value === "") {
        return businessError(1001, "secret value must not be empty");
      }
      if (secret?.value === undefined) return businessError(1001, "secret required on create");
      const channel: FixtureNotificationChannel = {
        id: uuid(),
        kind: body.kind as "email" | "dingtalk",
        name: body.name,
        enabled: body.enabled === undefined ? true : (body.enabled as boolean),
        configuration: body.configuration as FixtureNotificationChannel["configuration"],
        secret_configured: true,
        version: 1,
        created_at: now(),
        updated_at: now(),
        secret: secret.value,
      };
      world.notificationChannels.set(channel.id, channel);
      const { secret: _secret, ...view } = channel;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.get("*/admin/notification-channels/:channelId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      const { secret: _secret, ...view } = channel;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.put("*/admin/notification-channels/:channelId", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      if (versionMismatch(request, channel.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body required");
      const secret = body.secret as { value: string | null } | null | undefined;
      if (secret !== null && secret !== undefined && secret.value === "") {
        return businessError(1001, "secret value must not be empty");
      }
      const clearing = secret === null || secret?.value === null;
      if (clearing && channel.enabled) {
        return businessError(1001, "clear secret requires disabled channel");
      }
      channel.name = body.name === undefined ? channel.name : (body.name as string);
      channel.enabled = body.enabled === undefined ? channel.enabled : (body.enabled as boolean);
      channel.configuration = (body.configuration ?? channel.configuration) as FixtureNotificationChannel["configuration"];
      if (secret !== undefined && secret !== null) {
        if (secret.value !== null) {
          channel.secret = secret.value;
          channel.secret_configured = true;
        } else {
          channel.secret = null;
          channel.secret_configured = false;
        }
      }
      channel.version += 1;
      channel.updated_at = now();
      const { secret: _secret, ...view } = channel;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.delete("*/admin/notification-channels/:channelId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      if (versionMismatch(request, channel.version)) return businessError(1004, "version mismatch");
      world.notificationChannels.delete(channel.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post("*/admin/notification-channels/:channelId/test-deliveries", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      const delivery: FixtureNotificationDelivery = {
        id: uuid(),
        domain_event_id: uuid(),
        event_type: "notification_test_delivery",
        notification_channel_id: channel.id,
        channel_kind: channel.kind,
        channel_name: channel.name,
        recipient_user_id: null,
        masked_target: "group:" + channel.id.slice(0, 8),
        state: "queued",
        delivery_attempt_count: 0,
        next_attempt_at: now(),
        last_error_code: null,
        created_at: now(),
        updated_at: now(),
      };
      world.notificationDeliveries.unshift(delivery);
      // Outbox-driven delivery (S003): succeeds asynchronously unless the
      // channel is disabled, mirroring the retry-then-dead-letter loop.
      setTimeout(() => {
        delivery.delivery_attempt_count = 1;
        if (channel.enabled) {
          delivery.state = "succeeded";
        } else {
          delivery.state = "dead_letter";
          delivery.last_error_code = "channel_disabled";
        }
        delivery.updated_at = now();
      }, TASK_RUNNING_TO_DONE_MS);
      // F12 RCP-20261017 option A (No.015): acceptance receipt (task_id + channel_id).
      // 2026-10-31 round 3: real backend test delivery is an async_task (observed via
      // getTask polling); the fixture registers a pollable task with the same outcome
      // as the delivery row above (disabled channel fails with CHANNEL_DISABLED).
      const task: FixtureAdminTask = {
        id: uuid(),
        kind: "notification_test_delivery",
        state: "queued",
        progress: { completed: 0, total: 1, unit: "deliveries" },
        result_ref: null,
        error: null,
        created_at: now(),
        updated_at: now(),
      };
      world.tasks.set(task.id, task);
      advanceTask(task, () => {
        task.progress.completed = 1;
        if (!channel.enabled) {
          task.error = {
            type: "about:blank",
            title: "CHANNEL_DISABLED",
            status: 1001,
            detail: "channel disabled",
            request_id: "",
          };
        }
      }, !channel.enabled);
      return HttpResponse.json(successEnvelope({
        task_id: task.id, channel_id: channel.id,
      }));
    }),

    // S008 rate versions (FE package 2026-10-31): append-only list + create (closes previous open version).
    http.get("*/admin/ai-providers/:providerId/rate-versions", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const pid = String(params.providerId);
      const rows = [...world.rateVersions.values()]
        .filter((v) => v.provider_id === pid)
        .sort((a, b) => String(b.effective_from).localeCompare(String(a.effective_from)));
      return HttpResponse.json(successEnvelope({ items: rows, page: { next_cursor: null, has_more: false } }));
    }),
    http.post("*/admin/ai-providers/:providerId/rate-versions", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      // RCP-20261102-S008 parity: per-field 1001 validation with meta.fields locator.
      if (body === null) return businessError(1001, "rate version body invalid");
      if (typeof body.currency !== "string" || (body.currency !== "CNY" && body.currency !== "USD")) {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "currency must be CNY or USD",
          fields: { currency: ["must be CNY or USD"] },
        });
      }
      if (typeof body.timezone !== "string" || body.timezone === "") {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "timezone must be a valid IANA name",
          fields: { timezone: ["must be a valid IANA name"] },
        });
      }
      if (!Array.isArray(body.windows) || body.windows.length === 0) {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "windows must contain 1..24 entries",
          fields: { windows: ["must contain 1..24 entries"] },
        });
      }
      const pid = String(new URL(request.url).pathname.split("/")[5]);
      const provider = world.providers.get(pid);
      if (provider === undefined) return businessError(1002, "provider not found");
      // Budget currency consistency (fixture approximation of save-side validation): requires matching currency when budget enforcement is enabled.
      const budget = world.settings.get("ai-budget") as { currency?: string; enforced?: boolean } | undefined;
      if (budget?.enforced && budget.currency !== body.currency) {
        return businessError(1001, "RATE_CURRENCY_MISMATCH");
      }
      const now = new Date().toISOString();
      for (const prev of world.rateVersions.values()) {
        if (prev.provider_id === pid && prev.effective_until == null) {
          prev.effective_until = body.effective_from ?? now;
        }
      }
      const id = uuid();
      // The stored row keeps the wire shape verbatim: an absent
      // effective_until stays absent (the backend omits it via omitempty on
      // its non-nullable optional). The closing loop below therefore treats
      // absent and null alike — both mean "open version".
      const row = { id, provider_id: pid, ...body, version: world.rateVersions.size + 1, created_at: now };
      world.rateVersions.set(id, row);
      return HttpResponse.json(successEnvelope(row));
    }),

    // S008 call ledger (FE package 2026-10-31): per-HTTP-call ledger read face. provider_id
    // filtering runs server-side (RCP-20261102-S008 mirror); both fixture rows belong to the
    // primary provider, so filtering by another provider_id yields an empty page, matching the backend.
    http.get("*/admin/ai-usage/calls", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const providerFilter = new URL(request.url).searchParams.get("provider_id");
      const rows = [
        {
          id: "0b1e2f3a-0000-4000-8000-00000000d001",
          review_run_id: "0b1e2f3a-0000-4000-8000-000000000001",
          stage_position: 1, model_turn: 1, call_index: 1,
          provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
          model_name: "glm-4.6", sent_at: now(),
          reasoning_effort: "medium", max_tokens: 4096, temperature: "0.7",
          rate_version_id: null, rate_window_id: null, rate_source: "global_settings",
          currency: "CNY", input_tokens: 1200, output_tokens: 380,
          cached_tokens: null, cache_write_tokens: null, reasoning_tokens: 210,
          usage_reported: true, reference_amount: "0.00632100",
          calc_basis: "partial_detail",
        },
        {
          id: "0b1e2f3a-0000-4000-8000-00000000d002",
          review_run_id: "0b1e2f3a-0000-4000-8000-000000000001",
          stage_position: 1, model_turn: 2, call_index: 1,
          provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
          model_name: "glm-4.6", sent_at: now(),
          reasoning_effort: null, max_tokens: null, temperature: null,
          rate_version_id: null, rate_window_id: null, rate_source: "none",
          currency: null, input_tokens: null, output_tokens: null,
          cached_tokens: null, cache_write_tokens: null, reasoning_tokens: null,
          usage_reported: false, reference_amount: null,
          calc_basis: "not_computable",
        },
      ];
      const filtered = providerFilter === null
        ? rows
        : rows.filter((row) => row.provider_id === providerFilter);
      return HttpResponse.json(successEnvelope({
        items: filtered,
        page: { next_cursor: null, has_more: false },
      }));
    }),

    // S008 reference pricing summary read face (2026-10-31 round 3: consumed by the AI budget usage donut chart).
    // Fixed single USD row: reference_amount is a fixed-point decimal string in major currency units
    // (3.671 USD -> "3.67100000", matching the call row's "0.00632100" scale without nano-unit scaling).
    http.get("*/admin/ai-usage/summary", () => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      return HttpResponse.json(successEnvelope([
        {
          currency: "USD",
          calls: 3,
          reference_amount: "3.67100000",
          computable_calls: 3,
          input_tokens_known: 12000,
          input_tokens_cache_known: 8000,
          cached_tokens_known: 4000,
          cache_detail_samples: 2,
        },
      ]));
    }),

    // ----------------------------------------------- notification rules (S007) --
    http.get("*/admin/notification-rules", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.notificationRules.values()].sort((a, b) =>
        a.created_at.localeCompare(b.created_at),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),
    http.post("*/admin/notification-rules", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name.trim() === "") {
        return businessError(1001, "validation failed");
      }
      const rawTpl = typeof body.message_template === "string" ? body.message_template.trim() : "";
      const rule: FixtureNotificationRule = {
        id: uuid(),
        name: body.name,
        enabled: body.enabled === undefined ? true : (body.enabled as boolean),
        flow_id: (body.flow_id as string | null | undefined) ?? null,
        event_type: body.event_type as FixtureNotificationRule["event_type"],
        target_kind: body.target_kind as FixtureNotificationRule["target_kind"],
        channel_id: body.channel_id as string,
        message_template: rawTpl === "" ? null : rawTpl,
        version: 1,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      };
      world.notificationRules.set(rule.id, rule);
      return HttpResponse.json(successEnvelope(rule));
    }),
    http.post("*/admin/notification-rules/preview", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as { rule?: { message_template?: string | null } } | null;
      const customTpl = typeof body?.rule?.message_template === "string" ? body.rule.message_template.trim() : "";
      const sampleVars: Record<string, string> = {
        order_display_number: "YR-PREVIEW",
        order_no: "YR-PREVIEW",
        order_id: "00000000-0000-4000-8000-000000000001",
        order_title: "orders_index_change",
        submitter: "alice",
        submitter_username: "alice",
        submitted_at: "2026-11-10T09:00:00Z",
        order_state: "stage_approval_active",
        flow_name: "prod-standard-flow",
        stage_name: "prod-primary",
        datasource_name: "prod-mysql-01",
        approvers: "bob, carol",
        approved_by: "bob",
        approved_at: "2026-11-10T09:05:00Z",
        executors: "dba_ops",
        executed_by: "dba_ops",
        executed_at: "2026-11-10T09:10:03Z",
        execution_started_at: "2026-11-10T09:10:00Z",
        reason_code: "REVIEWED",
        order_url: "/changes/orders/00000000-0000-4000-8000-000000000001",
      };
      const rendered = customTpl === ""
        ? "\u5de5\u5355 YR-PREVIEW \u6267\u884c\u5931\u8d25\u3002"
        : customTpl.replace(/\{\{\s*([a-z_]+)\s*\}\}/g, (_, k: string) => sampleVars[k] ?? "-");
      return HttpResponse.json(successEnvelope({
        targets: [{ recipient_key: "group:preview", recipient_user_id: null, masked_address: "op***@corp.test" }],
        message_preview: rendered,
      }));
    }),
    http.get("*/admin/notification-rules/:ruleId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const rule = world.notificationRules.get(String(params.ruleId));
      if (rule === undefined) return businessError(1002, "rule not found");
      return HttpResponse.json(successEnvelope(rule));
    }),
    http.put("*/admin/notification-rules/:ruleId", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const existing = world.notificationRules.get(String(params.ruleId));
      if (existing === undefined) return businessError(1002, "rule not found");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "validation failed");
      const rawTpl = typeof body.message_template === "string" ? body.message_template.trim() : "";
      const updated: FixtureNotificationRule = {
        ...existing,
        name: body.name as string,
        enabled: body.enabled as boolean,
        flow_id: (body.flow_id as string | null | undefined) ?? null,
        event_type: body.event_type as FixtureNotificationRule["event_type"],
        target_kind: body.target_kind as FixtureNotificationRule["target_kind"],
        channel_id: body.channel_id as string,
        message_template: rawTpl === "" ? null : rawTpl,
        version: existing.version + 1,
        updated_at: new Date().toISOString(),
      };
      world.notificationRules.set(updated.id, updated);
      return HttpResponse.json(successEnvelope(updated));
    }),
    http.delete("*/admin/notification-rules/:ruleId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      if (world.notificationRules.delete(String(params.ruleId))) {
        return HttpResponse.json(successEnvelope(null));
      }
      return businessError(1002, "rule not found");
    }),

    http.get("*/admin/notification-deliveries", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.notificationDeliveries].sort((a, b) =>
        b.created_at.localeCompare(a.created_at),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    // ------------------------------------------------------- migrations --
    http.get("*/admin/migrations", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = world.migrationRun === null ? [] : [migrationRunView(world.migrationRun)];
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.get("*/admin/migrations/:runId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      if (world.migrationRun === null || world.migrationRun.id !== String(params.runId)) {
        return businessError(1002, "migration run not found");
      }
      return HttpResponse.json(successEnvelope(migrationRunView(world.migrationRun)));
    }),

    http.put("*/admin/migrations/:runId/candidate-mappings/:candidateId/confirmation", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const run = world.migrationRun;
      if (run === null || run.id !== String(params.runId)) {
        return businessError(1002, "migration run not found");
      }
      if (run.state !== "awaiting_confirmation") return businessError(1010, "not awaiting confirmation");
      if (versionMismatch(request, run.version)) return businessError(1004, "version mismatch");
      const candidate = run.candidates.find((row) => row.candidate_id === String(params.candidateId));
      if (candidate === undefined) return businessError(1002, "candidate not found");
      const body = (await request.json().catch(() => null)) as {
        confirmed?: boolean;
        target_definition_hash?: string;
        comment?: string;
      } | null;
      if (
        body === null || typeof body.confirmed !== "boolean" ||
        body.target_definition_hash !== candidate.target_definition_hash
      ) {
        return businessError(1001, "confirmed and matching target_definition_hash required");
      }
      candidate.confirmed = body.confirmed;
      candidate.confirmed_by = body.confirmed ? "admin" : null;
      candidate.confirmed_at = body.confirmed ? now() : null;
      run.version += 1;
      run.updated_at = now();
      // The contract's 200 is the migration run view (LegacyMigrationRunResult)
      // — not the candidate row (F12 R1 review finding, mock-side fix).
      return HttpResponse.json(successEnvelope(migrationRunView(run)));
    }),

    http.post("*/admin/migrations/:runId/approval", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const run = world.migrationRun;
      if (run === null || run.id !== String(params.runId)) {
        return businessError(1002, "migration run not found");
      }
      if (versionMismatch(request, run.version)) return businessError(1004, "version mismatch");
      if (run.state === "planned" || run.state === "dry_run_running") {
        return businessError(5002, "dry run required before approval");
      }
      if (run.state !== "awaiting_confirmation") return businessError(1010, "not awaiting confirmation");
      const body = (await request.json().catch(() => null)) as {
        manifest_hash?: string;
        confirmation_phrase?: string;
      } | null;
      if (body?.confirmation_phrase !== `APPROVE ${run.id}`) {
        return businessError(1001, "confirmation phrase mismatch");
      }
      if (run.manifest_hash === null || body.manifest_hash !== run.manifest_hash) {
        return businessError(1001, "manifest hash mismatch");
      }
      if (!run.candidates.every((candidate) => candidate.confirmed)) {
        return businessError(5003, "every candidate must be confirmed first");
      }
      run.state = "approved";
      run.updated_at = now();
      // Approval never starts Apply: only the offline migration command may.
      return HttpResponse.json(successEnvelope(migrationRunView(run)));
    }),
  ];
}

function successEnvelope(data: DefaultBodyType) {
  return { err_code: 0, message: "ok", data, request_id: ADMIN_REQUEST_ID };
}

function businessError(errCode: number, message: string, meta?: Record<string, unknown>) {
  return HttpResponse.json({
    err_code: errCode,
    message,
    data: null,
    request_id: ADMIN_REQUEST_ID,
    retryable: false,
    ...(meta === undefined ? {} : { meta }),
  });
}

/** RCP-20261102-S008 parity: governed configuration key validation mirrors backend
 * ValidateProviderConfiguration (1001 with meta.detail + meta.fields locator;
 * structured_output_mode is written only by probe negotiation and rejected as an unknown key on user input per §18.76-A). */
function providerConfigurationError(configuration: unknown): HttpResponse<DefaultBodyType> | null {
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

function problem(status: number, title: string, detail: string): HttpResponse<DefaultBodyType> {
  return HttpResponse.json(
    { type: "about:blank", title, status, detail, request_id: ADMIN_REQUEST_ID },
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}

/** §18.73 mock mirror of server-side q: case-insensitive substring match against any target field. */
function searchOf(url: URL): string {
  const q = url.searchParams.get("q");
  return q === null ? "" : q.trim();
}

function matchesQ(fields: (string | null | undefined)[], q: string): boolean {
  if (q === "") return true;
  const needle = q.toLowerCase();
  return fields.some((field) => (field ?? "").toLowerCase().includes(needle));
}

function pageOf<T extends { id: string }>(items: T[], limit: number | null, after: string | null) {
  const start =
    after === null
      ? 0
      : (() => {
          const index = items.findIndex((item) => item.id === after);
          return index === -1 ? items.length : index + 1;
        })();
  const sliced = items.slice(start, limit === null ? undefined : start + limit);
  const next = limit !== null && start + limit < items.length ? sliced.at(-1)?.id ?? null : null;
  return {
    items: sliced,
    page: { next_cursor: next, has_more: next !== null },
  };
}

/**
 * RequireOperation mirror (backend guards.go): anonymous callers get 401,
 * authenticated non-admin callers get 403. `yearning-mock-auth` carries the
 * behavior dimension exactly as the auth handlers read it.
 */
function adminGuard(): HttpResponse<DefaultBodyType> | null {
  const behavior = readStoredAuthBehavior();
  const hasSession = behavior !== "expired";
  if (!hasSession) return problem(401, "authentication required", "no active session");
  // Super mode (owner 2026-11-03): the dev-browser session passes the admin
  // guard without the admin behavior dimension.
  if (behavior !== "admin" && !mockSuperMode()) {
    return problem(403, "permission denied", "admin capability required");
  }
  return null;
}

function parseIfMatch(request: Request): string | null {
  const raw = request.headers.get("If-Match");
  if (raw === null) return null;
  return raw.replace(/^"|"$/g, "");
}

function versionMismatch(request: Request, current: number): boolean {
  const got = parseIfMatch(request);
  return got !== String(current);
}

function taskView(task: FixtureAdminTask): Task {
  return { ...task };
}

function advanceTask(task: FixtureAdminTask, onDone: () => void, fail = false): void {
  const target = fail ? "failed" : "succeeded";
  if (task.state === "queued") {
    setTimeout(() => {
      task.state = "running";
      task.updated_at = now();
      setTimeout(() => {
        task.state = target;
        task.updated_at = now();
        onDone();
      }, TASK_RUNNING_TO_DONE_MS);
    }, TASK_QUEUED_TO_RUNNING_MS);
  }
}

function taskResponse(
  kind: FixtureAdminTask["kind"],
  resultRef: string,
  onDone: () => void,
  fail = false,
): HttpResponse<DefaultBodyType> {
  const task: FixtureAdminTask = {
    id: uuid(),
    kind,
    state: "queued",
    progress: { completed: 0, total: 1, unit: "checks" },
    result_ref: resultRef,
    error: null,
    created_at: now(),
    updated_at: now(),
  };
  world.tasks.set(task.id, task);
  advanceTask(task, onDone, fail);
  return HttpResponse.json(successEnvelope(taskView(task)));
}

/** Static eval gate mirror (RunPromptToolEval): deterministic checks over
 * the review-input definition. Violations come back as findings so the UI
 * can show the governed refusal, and enabling fails with 1001. */
function runReviewInputEval(
  definition: FixtureReviewInput["definition"],
  parameters: Record<string, unknown>,
): KnowledgeEntryEvaluation {
  const findings: string[] = [];
  // §18.66: definition={knowledge_text,max_severity,version} — structural checks only cover
  // non-empty body and severity enum; finding key is system-derived and severity allowlist semantics are replaced by max_severity closure.
  const schemaSubsetOk =
    definition.knowledge_text.trim() !== "" &&
    ["info", "low", "medium", "high", "critical"].includes(definition.max_severity);
  if (!schemaSubsetOk) findings.push("definition must carry non-empty knowledge_text and a valid max_severity");
  const severityOk = ["info", "low", "medium", "high", "critical"].includes(definition.max_severity);
  if (!severityOk) findings.push("max_severity must be one of info/low/medium/high/critical");
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

function datasourceView(ds: FixtureDatasource): Datasource {
  const { credentials: _credentials, capabilities: _capabilities, tls: _tls, ...view } = ds;
  void _credentials;
  void _capabilities;
  void _tls;
  // tls_verified is the only TLS fact the read face declares: material is
  // never echoed, presence alone decides verified-vs-plaintext.
  return { ...view, tls_verified: _tls !== null };
}

function providerView(provider: FixtureProvider): AiProvider {
  const { apiKey: _apiKey, ...view } = provider;
  void _apiKey;
  return { ...view };
}

function promptToolView(tool: FixturePromptTool): PromptTool {
  return { ...tool };
}

/** Key-order-insensitive JSON for definition comparison — the backend
 * freezes built-ins by a canonical definition hash (prompttools.go:505),
 * so a reordered-key payload that the backend accepts must pass here too. */
function canonicalJson(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  const entries = Object.entries(value as Record<string, unknown>).sort(([a], [b]) =>
    a < b ? -1 : a > b ? 1 : 0,
  );
  return `{${entries.map(([key, entry]) => `${JSON.stringify(key)}:${canonicalJson(entry)}`).join(",")}}`;
}

/** Extracts the stored TLS material from a DatasourceWrite body. Called
 * only after validateDatasourceWrite accepted the tls block, so the
 * shape/pairing/PEM guarantees hold here. */
function tlsFromWrite(body: Record<string, unknown>): FixtureDatasource["tls"] {
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

function knowledgeEntryView(entry: FixtureKnowledgeEntry): KnowledgeEntry {
  const {
    evaluation: _evaluation, datasource_id: ds, database_name: db, table_name: tbl,
    source_finding_id: sfid, purpose, ...rest
  } = entry;
  void _evaluation;
  // The contract types the optionals as Uuid/minLength-1 strings — never
  // null. The fixture world stores null for a global-scope entry; the view
  // omits those keys instead of serializing null (F12 divergence No.008). The
  // optional purpose follows the same rule: zod allows string|undefined, not
  // null (F12 batch-3 mock fix).
  const out: Record<string, unknown> = { ...rest };
  if (sfid !== null) out.source_finding_id = sfid;
  if (ds !== null) out.datasource_id = ds;
  if (db !== null) out.database_name = db;
  if (tbl !== null) out.table_name = tbl;
  if (purpose !== null) out.purpose = purpose;
  return out as unknown as KnowledgeEntry;
}

function ruleSetView(ruleSet: FixtureRuleSet): RuleSet {
  // rule_sets.rules is structurally an empty object in v4 (pure prompt-tool
  // combination); the fixture keeps no rules content at all.
  return { ...ruleSet, rules: {} };
}

function credentialStatus(ds: FixtureDatasource): Datasource["credential_status"] {
  const status: Record<string, boolean> = {};
  for (const purpose of PURPOSES) {
    status[purpose] = ds.credentials[purpose] !== undefined;
  }
  return status;
}

function touchedCredentialPurposes(ds: FixtureDatasource): void {
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

function diffChangedPaths(current: SettingsValue, proposed: SettingsValue): string[] {
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

function assessImpact(
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
  // PRD 10 §4.2: the token is bound to namespace + current version +
  // canonical settings hash + admin session, single-use, 300s window.
  // F12 No.014: contract requires impact_token >= 32 chars — two-round binding digest (16+16) +
  // "|" + timestamp; verify splits on "|" and recomputes both segments.
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

/** Token binding mirror (VerifyImpactToken): namespace + proposed hash +
 * current version + admin session, single-use, expiring. Any mismatch, an
 * expired token or a consumed token fails the PUT with PRECONDITION_REQUIRED
 * (1011). */
function verifyImpactToken(
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
  // First real execution consumes the token; a replay hits the same guard.
  world.impactTokens.delete(token);
  const proposedSha = proposedSha256(proposed);
  const expected = impactTokenBinding(namespace, proposedSha, currentVersion)
    + configHash(namespace, proposedSha, String(currentVersion), "admin-session", "impact-token-round-2");
  return boundSha === proposedSha && binding === expected;
}

function validateReviewInputDefinition(definition: unknown): string | null {
  if (typeof definition !== "object" || definition === null) return "definition is required";
  const def = definition as Record<string, unknown>;
  if (typeof def.knowledge_text !== "string" || def.knowledge_text.length === 0) {
    return "knowledge_text is required";
  }
  if (typeof def.max_severity !== "string" || !["info", "low", "medium", "high", "critical"].includes(def.max_severity)) {
    return "max_severity must be one of info/low/medium/high/critical";
  }
  if (typeof def.version !== "number" || def.version < 1) {
    return "definition.version must be a positive integer";
  }
  return null;
}

export function adminFixtureHandlers(): HttpHandler[] {
  return [
    // ---- datasources -------------------------------------------------
    // §18.73 contract mirror: q matches name/host only; engine/compatibility_mode/
    // credential_mode/referenced are RCP-20261105 column filters combined with AND.
    http.get("*/admin/datasources", async ({ request }) => {
      const guard = adminGuard();
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
    http.post("*/admin/datasources", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const validationError = validateDatasourceWrite(body);
      if (validationError !== null) return businessError(1001, validationError);
      const duplicate = [...world.datasources.values()].some((ds) => ds.name === body.name);
      if (duplicate) return businessError(1005, "datasource name already exists");
      // Create has no stored credentials, so keep/reuse modes resolve
      // against an empty old set and fail here with 1001 (B13 semantics).
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
    http.get("*/admin/datasources/:datasourceId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      return HttpResponse.json(successEnvelope(datasourceView(ds)));
    }),
    http.put("*/admin/datasources/:datasourceId", async ({ request, params }) => {
      const guard = adminGuard();
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
      // Full-replacement semantics: omitted or null tls removes every
      // stored material row (verified → plaintext), same as the backend.
      ds.tls = tlsFromWrite(body);
      if (typeof body.name === "string" && body.name !== "") ds.name = body.name;
      ds.engine = body.engine as Datasource["engine"];
      ds.compatibility_mode = body.compatibility_mode as Datasource["compatibility_mode"];
      ds.deployment_kind = body.deployment_kind as Datasource["deployment_kind"];
      if (typeof body.host === "string") ds.host = body.host;
      if (typeof body.port === "number") ds.port = body.port;
      if (typeof body.enabled === "boolean") ds.enabled = body.enabled;
      if ("database_name" in body) ds.database_name = (body.database_name as string | null) ?? null;
      // §18.61: full-replacement semantics — omitting test_purpose resets it to review (matches backend).
      ds.test_purpose = testPurposeOfWrite(body);
      ds.credentials = applied;
      ds.version += 1;
      ds.updated_at = now();
      touchedCredentialPurposes(ds);
      return HttpResponse.json(successEnvelope(datasourceView(ds)));
    }),
    http.delete("*/admin/datasources/:datasourceId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      if (versionMismatch(request, ds.version)) return businessError(1004, "version mismatch");
      // Physical delete is only possible for a never-referenced datasource
      // (datasource_delete profile: DATASOURCE_REFERENCED).
      if ((ds.referenced_by_flow_count ?? 0) > 0) {
        return businessError(1107, "datasource is still referenced by flows");
      }
      world.datasources.delete(ds.id);
      return HttpResponse.json(successEnvelope(null));
    }),
    http.post("*/admin/datasources/:datasourceId/connection-tests", async ({ params, request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      const body = (await request.json().catch(() => null)) as { purpose?: string } | null;
      if (body === null || !PURPOSES.includes(body.purpose as (typeof PURPOSES)[number])) {
        return businessError(1001, "connection test purpose is required");
      }
      // A purpose without a configured credential fails the task, exactly as
      // the real probe finishes with credential_missing (service.go:515).
      const credentialMissing = ds.credentials[body.purpose as (typeof PURPOSES)[number]] === undefined;
      return taskResponse(
        "admin_connection_test",
        `/admin/datasources/${ds.id}/capabilities`,
        () => {
          if (credentialMissing) return;
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
        credentialMissing,
      );
    }),
    http.get("*/admin/datasources/:datasourceId/capabilities", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const ds = world.datasources.get(String(params.datasourceId));
      if (ds === undefined) return businessError(1002, "datasource not found");
      if (ds.capabilities === null) return businessError(1002, "capabilities not probed yet");
      return HttpResponse.json(successEnvelope(ds.capabilities));
    }),

    // ---- ai providers ------------------------------------------------
    http.get("*/admin/ai-providers", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      // §18.73: standard paged envelope — mirrors the backend writePage
      // ({items, page}); ordered by selection_priority ascending
      // (provider/chain.go:3) — index 0 is the primary provider.
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
    http.post("*/admin/ai-providers", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name === "") {
        return businessError(1001, "name is required");
      }
      const duplicate = [...world.providers.values()].some((p) => p.name === body.name);
      // F-UX-002 parity: conflicting axis is reported in meta.detail (backend distinguishes by constraint name).
      if (duplicate) {
        return businessError(1005, "RESOURCE_ALREADY_EXISTS", { detail: "name 已被其他AI Provider占用" });
      }
      // §18.75 parity: selection_priority is globally unique (mirrors backend unique index).
      const createPrio = Number(body.selection_priority ?? 100);
      if ([...world.providers.values()].some((p) => p.selection_priority === createPrio)) {
        return businessError(1005, "RESOURCE_ALREADY_EXISTS", {
          detail: `AI Provider selection_priority=${String(createPrio)} 已被其他AI Provider占用`,
        });
      }
      // Create requires an api key (CreateAiProviderRequest Pick<api_key>);
      // the plaintext never reaches any read face.
      const apiKey = body.api_key as { value?: string } | undefined;
      if (apiKey === undefined || typeof apiKey.value !== "string" || apiKey.value === "") {
        return businessError(1001, "api_key is required on create");
      }
      // RCP-20261102-S008 parity: out-of-range or unknown genparams keys return 1001 with field locators.
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
    http.get("*/admin/ai-providers/:providerId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      return HttpResponse.json(successEnvelope(providerView(provider)));
    }),
    http.put("*/admin/ai-providers/:providerId", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      // RCP-20261102-S008 parity: out-of-range or unknown genparams keys return 1001 with field locators.
      const configError = providerConfigurationError(body.configuration);
      if (configError !== null) return configError;
      // §18.75 parity: renaming or changing priority to another provider's unique value returns 1005;
      // F-UX-002 parity: conflicting axis is reported in meta.detail.
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
    http.delete("*/admin/ai-providers/:providerId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      if (versionMismatch(request, provider.version)) return businessError(1004, "version mismatch");
      world.providers.delete(provider.id);
      return HttpResponse.json(successEnvelope(null));
    }),
    http.post("*/admin/ai-providers/:providerId/connection-tests", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const provider = world.providers.get(String(params.providerId));
      if (provider === undefined) return businessError(1002, "provider not found");
      // F12 RCP-20261017 option A (No.019): synchronous probe result {ok} (real backend
      // immediate probe returns {ok:true} and routes failures through business error members —
      // replacing the older Task-shaped contract per ruling).
      if (!provider.enabled) return businessError(5003, "provider disabled");
      return HttpResponse.json(successEnvelope({ ok: true }));
    }),
    // §18.76-B parity: model catalog autocomplete. POST carries base_url/api_key (plaintext
    // credentials never enter the URL); when api_key is omitted, provider_id is required (edit mode reuses stored key).
    // "mock-invalid-key" simulates an upstream 401 rejection (for detail passthrough assertions).
    http.post("*/admin/ai-providers/models", async ({ request }) => {
      const guard = adminGuard();
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

    // ---- settings ----------------------------------------------------
    http.get("*/admin/settings/:namespace", ({ params }) => {
      const guard = adminGuard();
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
    http.post("*/admin/settings/:namespace/impact-assessments", async ({ request, params }) => {
      const guard = adminGuard();
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
    http.put("*/admin/settings/:namespace", async ({ request, params }) => {
      const guard = adminGuard();
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
        // The server never downgrades a high-impact change because the
        // client skipped the assessment (PRD 10 §4.4); a missing, expired,
        // consumed or mismatched token is PRECONDITION_REQUIRED (1011).
        if (body.impact_token === undefined || body.impact_token === null || body.impact_token === "") {
          return businessError(1011, "impact confirmation required for a high-impact change");
        }
        const valid = verifyImpactToken(body.impact_token, ns, body.settings, current.version);
        if (!valid) return businessError(1011, "impact token invalid, expired or already consumed");
      }
      current.settings = body.settings;
      // §18.47: the saved knobs drive the query/execution domain worlds.
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
    http.get("*/admin/settings/:namespace/schema", ({ params }) => {
      const guard = adminGuard();
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
    http.get("*/admin/settings/:namespace/revisions", ({ params, request }) => {
      const guard = adminGuard();
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

    // ---- prompt tools (review skills) ---------------------------------
    http.get("*/admin/prompt-tools", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const items = [...world.promptTools.values()]
        .filter((tool) => matchesQ([tool.name], searchOf(url)))
        .map(promptToolView);
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),
    http.post("*/admin/prompt-tools", async ({ request }) => {
      const guard = adminGuard();
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
      // §18.80 mirror: display metadata capped at 100 runes, never hashed.
      if (Array.from(description).length > 100) return businessError(1001, "description is too long");
      const engine = (body.engine as string | undefined) ?? "all";
      if (!["all", "mysql", "postgresql"].includes(engine)) return businessError(1001, "engine is invalid");
      const state = (body.state as string | undefined) ?? "draft";
      if (!["draft", "enabled", "disabled"].includes(state)) return businessError(1001, "state is invalid");
      const parameters = (body.parameters ?? {}) as Record<string, number | string | boolean | string[]>;
      const definition = body.definition as FixtureReviewInput["definition"];
      // B8 ruling: the eval gate runs when a row is being (re-)enabled —
      // on create that is every enabled save.
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
        // state does not enter the config hash (backend prompttools.go hash
        // covers name+engine+params+definition only).
        config_hash: configHash(body.name, engine, JSON.stringify(definition), JSON.stringify(parameters)),
        version: 1,
        created_at: ts,
        updated_at: ts,
        // User-defined only: built-in packages are system-seeded.
        is_builtin: false,
      };
      world.promptTools.set(tool.id, tool);
      return HttpResponse.json(successEnvelope(promptToolView(tool)));
    }),
    http.get("*/admin/prompt-tools/:promptToolId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const tool = world.promptTools.get(String(params.promptToolId));
      if (tool === undefined) return businessError(1002, "prompt tool not found");
      return HttpResponse.json(successEnvelope(promptToolView(tool)));
    }),
    http.put("*/admin/prompt-tools/:promptToolId", async ({ request, params }) => {
      const guard = adminGuard();
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
      // B8 ruling (governance tri-split): a built-in row's definition face
      // (name/engine/definition) is frozen — the payload must hash exactly
      // like the stored row — and parameters may only change values, never
      // the key set or types. State stays toggleable.
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
      // §18.80 mirror: a built-in row's description is system-seeded — the
      // payload value is ignored exactly like the definition face.
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
    http.delete("*/admin/prompt-tools/:promptToolId", ({ request, params }) => {
      const guard = adminGuard();
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

    // ---- knowledge entries (internal experience) ----------------------
    http.get("*/admin/knowledge-entries", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const items = [...world.knowledgeEntries.values()]
        .filter((entry) => matchesQ([entry.name], searchOf(url)))
        .map(knowledgeEntryView);
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),
    http.post("*/admin/knowledge-entries", async ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name === "") {
        return businessError(1001, "name is required");
      }
      const duplicate = [...world.knowledgeEntries.values()].some((entry) => entry.name === body.name);
      if (duplicate) return businessError(1005, "knowledge entry name already exists");
      const scopeType = body.scope_type as string;
      if (!["global", "datasource", "table"].includes(scopeType)) {
        return businessError(1001, "scope_type is invalid");
      }
      if (scopeType !== "global" && typeof body.datasource_id !== "string") {
        return businessError(1001, "datasource_id is required for a scoped entry");
      }
      if (scopeType === "table" && (typeof body.database_name !== "string" || typeof body.table_name !== "string")) {
        return businessError(1001, "database_name and table_name are required for a table-scoped entry");
      }
      const definitionError = validateReviewInputDefinition(body.definition);
      if (definitionError !== null) return businessError(1001, definitionError);
      const state = (body.state as string | undefined) ?? "draft";
      if (!["draft", "enabled", "disabled"].includes(state)) return businessError(1001, "state is invalid");
      const definition = body.definition as FixtureReviewInput["definition"];
      if (state === "enabled") {
        const evaluation = runReviewInputEval(definition, {});
        if (!evaluation.pass) return businessError(1001, "eval gate failed");
      }
      const ts = now();
      const entry: FixtureKnowledgeEntry = {
        id: uuid(),
        name: body.name,
        purpose: (body.purpose as string | undefined) ?? null,
        state: state as FixtureKnowledgeEntry["state"],
        scope_type: scopeType as FixtureKnowledgeEntry["scope_type"],
        datasource_id: (body.datasource_id as string | undefined) ?? null,
        database_name: (body.database_name as string | undefined) ?? null,
        table_name: (body.table_name as string | undefined) ?? null,
        definition,
        provenance: ((body.provenance as string | undefined) ?? "manual") as FixtureKnowledgeEntry["provenance"],
        source_finding_id: (body.source_finding_id as string | undefined) ?? null,
        // Backend knowledge hash covers name+scope coordinates+definition (entry.go),
        // never the state.
        config_hash: configHash(
          body.name,
          scopeType,
          typeof body.datasource_id === "string" ? body.datasource_id : "",
          typeof body.database_name === "string" ? body.database_name : "",
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
    http.get("*/admin/knowledge-entries/:knowledgeEntryId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      return HttpResponse.json(successEnvelope(knowledgeEntryView(entry)));
    }),
    http.put("*/admin/knowledge-entries/:knowledgeEntryId", async ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      if (versionMismatch(request, entry.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body is required");
      const definitionError = validateReviewInputDefinition(body.definition);
      if (definitionError !== null) return businessError(1001, definitionError);
      const state = (body.state as string | undefined) ?? entry.state;
      if (!["draft", "enabled", "disabled"].includes(state)) return businessError(1001, "state is invalid");
      const definition = body.definition as FixtureReviewInput["definition"];
      if (state === "enabled") {
        const evaluation = runReviewInputEval(definition, {});
        if (!evaluation.pass) return businessError(1001, "eval gate failed");
      }
      entry.state = state as FixtureKnowledgeEntry["state"];
      entry.purpose = (body.purpose as string | undefined) ?? entry.purpose;
      entry.definition = definition;
      if (typeof body.datasource_id === "string") entry.datasource_id = body.datasource_id;
      if (typeof body.database_name === "string") entry.database_name = body.database_name;
      if (typeof body.table_name === "string") entry.table_name = body.table_name;
      entry.config_hash = configHash(
        entry.name,
        entry.scope_type,
        entry.datasource_id ?? "",
        entry.database_name ?? "",
        entry.table_name ?? "",
        JSON.stringify(definition),
      );
      entry.version += 1;
      entry.updated_at = now();
      entry.evaluation = null;
      return HttpResponse.json(successEnvelope(knowledgeEntryView(entry)));
    }),
    http.delete("*/admin/knowledge-entries/:knowledgeEntryId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      if (versionMismatch(request, entry.version)) return businessError(1004, "version mismatch");
      // Deletion follows the prompt-tool governance: an entry ever frozen by
      // a review run cannot be physically deleted (seed marks converted rows
      // as already referenced).
      if (entry.provenance === "finding_conversion") {
        return businessError(1006, "knowledge entry is referenced by a review run");
      }
      world.knowledgeEntries.delete(entry.id);
      return HttpResponse.json(successEnvelope(null));
    }),
    http.post("*/admin/knowledge-entries/:knowledgeEntryId/evaluations", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const entry = world.knowledgeEntries.get(String(params.knowledgeEntryId));
      if (entry === undefined) return businessError(1002, "knowledge entry not found");
      const evaluation = runReviewInputEval(entry.definition, {});
      entry.evaluation = evaluation;
      return HttpResponse.json(successEnvelope(evaluation));
    }),

    // ---- rule sets ----------------------------------------------------
    http.get("*/admin/rule-sets", ({ request }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const limit = url.searchParams.get("limit");
      const after = url.searchParams.get("after");
      const items = [...world.ruleSets.values()]
        .filter((rs) => matchesQ([rs.name], searchOf(url)))
        .map(ruleSetView);
      return HttpResponse.json(successEnvelope(pageOf(items, limit === null ? null : Number(limit), after)));
    }),
    http.post("*/admin/rule-sets", async ({ request }) => {
      const guard = adminGuard();
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
        // rule_sets.rules is structurally an empty object in v4: the rule
        // set is a pure prompt-tool combination (ai-review-production PRD).
        config_hash: configHash(body.name, toolIds.join(","), String(Boolean(body.enabled))),
        version: 1,
        created_at: ts,
        updated_at: ts,
      };
      world.ruleSets.set(ruleSet.id, ruleSet);
      return HttpResponse.json(successEnvelope(ruleSetView(ruleSet)));
    }),
    http.get("*/admin/rule-sets/:ruleSetId", ({ params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const ruleSet = world.ruleSets.get(String(params.ruleSetId));
      if (ruleSet === undefined) return businessError(1002, "rule set not found");
      return HttpResponse.json(successEnvelope(ruleSetView(ruleSet)));
    }),
    http.put("*/admin/rule-sets/:ruleSetId", async ({ request, params }) => {
      const guard = adminGuard();
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
    http.delete("*/admin/rule-sets/:ruleSetId", ({ request, params }) => {
      const guard = adminGuard();
      if (guard !== null) return guard;
      const ruleSet = world.ruleSets.get(String(params.ruleSetId));
      if (ruleSet === undefined) return businessError(1002, "rule set not found");
      if (versionMismatch(request, ruleSet.version)) return businessError(1004, "version mismatch");
      const referenced = [...world.flows.values()].some((flow) => flow.rule_set_id === ruleSet.id);
      if (referenced) return businessError(1006, "rule set is referenced by a flow");
      world.ruleSets.delete(ruleSet.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    // ---- flows (read-only reverse-reference source for rule sets) -----
    // §18.73 contract mirror: q matches flow name only; flow_type/enabled are
    // RCP-20261105 column filters combined with AND.
    http.get("*/admin/flows", ({ request }) => {
      const guard = adminGuard();
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

/** Poll seam for tests: expose task timing constants without magic numbers
 * leaking into specs. */
export const ADMIN_FIXTURE_TASK_TIMING = {
  QUEUED_TO_RUNNING_MS: TASK_QUEUED_TO_RUNNING_MS,
  RUNNING_TO_DONE_MS: TASK_RUNNING_TO_DONE_MS,
};

export { PURPOSES as ADMIN_FIXTURE_PURPOSES, SETTING_NAMESPACES as ADMIN_FIXTURE_SETTING_NAMESPACES };

/** Test-only seam: assertions on never-serialized internals (stored
 * credential plaintexts for the reuse semantics, provider key state). */
export function adminFixtureInternals() {
  return {
    datasourceCredentials: (datasourceId: string, purpose: (typeof PURPOSES)[number]) =>
      world.datasources.get(datasourceId)?.credentials[purpose],
    providerApiKey: (providerId: string) => world.providers.get(providerId)?.apiKey,
  };
}

/** Shared task read: the review fixture owns the single `GET /tasks/:id`
 * route and consults this accessor for admin-domain tasks (connection
 * tests) before answering 1002. */
export function adminFixtureTask(taskId: string): FixtureAdminTask | null {
  return world.tasks.get(taskId) ?? null;
}

// Module-scope baseline: the admin pages read persisted configuration, so
// the fixture world is born seeded (tests reseed via resetAdminFixture).
seedAdminFixture();
