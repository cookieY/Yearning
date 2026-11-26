import type { SettingsValue } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

export const PURPOSES = ["review", "query", "execution"] as const;

export const SETTING_NAMESPACES = ["general", "query", "execution", "ai-budget", "branding"] as const;

/** Per-field impact vocabulary from settings-namespaces.json (machine authority). */
export const SETTINGS_FIELD_IMPACT: Record<string, Record<string, string>> = {
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

export const DEFAULT_SETTINGS: Record<string, SettingsValue> = {
  general: { registration_enabled: false, system_timezone: "Asia/Shanghai", default_locale: "zh-CN", public_base_url: "" },
  query: { approval_enabled: true, default_timeout_ms: 30000, maximum_approved_access_duration_minutes: 43200, max_result_rows: 1000 },
  execution: { restriction_enabled: false, windows: [], osc: { tool: "none", defaults: {} } },
  "ai-budget": { enforced: false, currency: "USD", daily_budget_minor: 10000, alert_threshold_percent: 80, input_price_per_million_minor: 0, output_price_per_million_minor: 0 },
  branding: { product_name: "Yearning", login_description: "", logo_asset_id: null, favicon_asset_id: null },
};

export const TASK_QUEUED_TO_RUNNING_MS = 300;
export const TASK_RUNNING_TO_DONE_MS = 600;

export const ADMIN_FIXTURE_TASK_TIMING = {
  QUEUED_TO_RUNNING_MS: TASK_QUEUED_TO_RUNNING_MS,
  RUNNING_TO_DONE_MS: TASK_RUNNING_TO_DONE_MS,
};

export const SEED_ADMIN_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

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

export const ADMIN_FIXTURE_USER_MEMBER_ID = "7a1a3c4d-1111-4111-8111-00000000e101";
export const ADMIN_FIXTURE_USER_BLOCKED_ID = "7a1a3c4d-1111-4111-8111-00000000e102";
export const ADMIN_FIXTURE_GROUP_ID = "7a1a3c4d-2222-4222-8222-00000000b101";
export const ADMIN_FIXTURE_FLOW_QUERY_ID = "7a1a3c4d-3333-4333-8333-00000000f002";
export const ADMIN_FIXTURE_REVISION_PUBLISHED_ID = "7a1a3c4d-4444-4444-8444-00000000c101";
export const ADMIN_FIXTURE_LDAP_ID = "7a1a3c4d-5555-4555-8555-00000000d101";
export const ADMIN_FIXTURE_CHANNEL_EMAIL_ID = "7a1a3c4d-6666-4666-8666-000000003101";
export const ADMIN_FIXTURE_MIGRATION_RUN_ID = "7a1a3c4d-7777-4777-8777-000000004101";
