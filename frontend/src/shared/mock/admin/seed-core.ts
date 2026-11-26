import type {
  ExecutionWindow,
  ReviewInputDefinition,
  SettingsValue,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { CATALOG_DATASOURCES, CATALOG_FLOWS } from "@/shared/mock/flow-catalog";
import { nowIsoSeconds as now } from "@/shared/mock/kit";
import { settingsBridge } from "@/shared/mock/settings-bridge";
import {
  ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
  ADMIN_FIXTURE_DATASOURCE_PG_ID,
  ADMIN_FIXTURE_FLOW_CHANGE_ID,
  ADMIN_FIXTURE_KNOWLEDGE_CONVERTED_ID,
  ADMIN_FIXTURE_KNOWLEDGE_GLOBAL_ID,
  ADMIN_FIXTURE_KNOWLEDGE_TABLE_ID,
  ADMIN_FIXTURE_PROVIDER_BACKUP_ID,
  ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
  ADMIN_FIXTURE_RULE_SET_ID,
  ADMIN_FIXTURE_TOOL_BUILTIN_ID,
  ADMIN_FIXTURE_TOOL_DRAFT_ID,
  ADMIN_FIXTURE_TOOL_ENABLED_ID,
  DEFAULT_SETTINGS,
  SEED_ADMIN_ID,
  SETTING_NAMESPACES,
} from "./ids";
import { seedSiteFixture, seedVolumeFixtures } from "./seed-site";
import { configHash, world } from "./state";

const PROD_WINDOW_DEFINITION: ReviewInputDefinition = {
  knowledge_text: "Production DDL is only allowed inside the announced maintenance window.",
  max_severity: "critical",
  version: 1,
};

const ORDERS_HUGE_DEFINITION: ReviewInputDefinition = {
  knowledge_text: "The orders table exceeds 500M rows; any unbounded scan is discouraged.",
  max_severity: "medium",
  version: 1,
};

const CHARSET_DEFINITION: ReviewInputDefinition = {
  knowledge_text: "This datasource standardizes on UTF8; new columns must not introduce latin1.",
  max_severity: "medium",
  version: 1,
};

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
    tls: {
      ca: "-----BEGIN CERTIFICATE-----\nMIIBfixtureCAA==\n-----END CERTIFICATE-----\n",
      cert: "-----BEGIN CERTIFICATE-----\nMIIBfixtureCertA\n-----END CERTIFICATE-----\n",
      key: "-----BEGIN PRIVATE KEY-----\nMIIBfixtureKey==\n-----END PRIVATE KEY-----\n",
    },
    capabilities: null,
  });
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
  for (const ns of SETTING_NAMESPACES) {
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
    state: "disabled",
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
    database_name: "analytics",
    table_name: null,
    definition: CHARSET_DEFINITION,
    config_hash: configHash("charset-uniformity", "datasource", ADMIN_FIXTURE_DATASOURCE_PG_ID, "analytics", "", JSON.stringify(CHARSET_DEFINITION)),
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

seedAdminFixture();
