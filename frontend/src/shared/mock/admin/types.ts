import type {
  AiProvider,
  AnnouncementRevision,
  AuditEvent,
  Datasource,
  DatasourceCapabilities,
  IdentityProvider,
  KnowledgeEntryEvaluation,
  LegacyMigrationRun,
  NotificationChannel,
  NotificationDelivery,
  NotificationRule,
  PermissionGroup,
  PromptTool,
  SettingsRevision,
  SettingsValue,
  User,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { PURPOSES } from "./ids";

export interface CredentialLike {
  purpose: string;
  username?: string;
  password?: { value: string } | null;
  reuse_credential_purpose?: string | null;
}

export interface FixtureCredential {
  username: string;
  /** Fixture-internal plaintext; never serialized back to any read face. */
  password: string;
}

export interface FixtureDatasource extends Omit<Datasource, "tls_verified"> {
  credentials: Partial<Record<(typeof PURPOSES)[number], FixtureCredential>>;
  capabilities: DatasourceCapabilities | null;
  tls: { ca: string; cert: string; key: string } | null;
}

export interface FixtureProvider extends AiProvider {
  apiKey: string;
}

export interface FixtureReviewInput {
  id: string;
  name: string;
  state: "draft" | "enabled" | "disabled";
  definition: PromptTool["definition"];
  config_hash: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface FixturePromptTool extends FixtureReviewInput {
  description: string;
  engine: "all" | "mysql" | "postgresql";
  parameters: Record<string, number | string | boolean | string[]>;
  is_builtin: boolean;
}

export interface FixtureKnowledgeEntry extends FixtureReviewInput {
  purpose: string | null;
  scope_type: "global" | "datasource" | "table";
  datasource_id: string | null;
  database_name: string | null;
  table_name: string | null;
  provenance: "manual" | "finding_conversion";
  source_finding_id: string | null;
  evaluation: KnowledgeEntryEvaluation | null;
}

export interface FixtureRuleSet {
  id: string;
  name: string;
  enabled: boolean;
  prompt_tool_ids: string[];
  config_hash: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface FixtureFlowStage {
  position: number;
  datasource_id: string;
  schema_mappings: { logical_schema: string; physical_schema: string }[];
  approval_steps: { position: number; actors: { user_id: string }[] }[];
  execution_actors: { user_id: string }[];
}

export interface FixtureFlow {
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

export interface FixtureNamespace {
  settings: SettingsValue;
  version: number;
  updated_by: string;
  updated_at: string;
}

export interface FixtureAdminTask {
  id: string;
  kind:
    | "admin_connection_test"
    | "ai_provider_connection_test"
    | "identity_provider_connection_test"
    | "notification_test_delivery";
  state: "queued" | "running" | "succeeded" | "failed" | "cancelled";
  progress: { completed: number; total: number; unit: string };
  result_ref: string | null;
  error: null | { type: string; title: string; status: number; detail: string; instance?: string; request_id: string };
  created_at: string;
  updated_at: string;
}

export interface FixtureUser extends User {
  activeOrderCount: number;
  templateReferenceCount: number;
}

export type FixturePermissionGroup = PermissionGroup;

export type FixtureAnnouncementRevision = AnnouncementRevision;

export type FixtureAuditEvent = AuditEvent;

export interface FixtureIdentityProvider extends IdentityProvider {
  clientSecret: string | null;
}

export interface FixtureNotificationChannel extends NotificationChannel {
  secret: string | null;
}

export type FixtureNotificationDelivery = NotificationDelivery;

export type FixtureNotificationRule = NotificationRule;

export interface FixtureMigrationCandidate {
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

export interface FixtureMigrationRun {
  id: string;
  state: "planned" | "dry_run_running" | "awaiting_confirmation" | "approved" | "applying" | "verifying" | "verified" | "failed";
  manifest_hash: string | null;
  table_results: LegacyMigrationRun["table_results"];
  candidates: FixtureMigrationCandidate[];
  version: number;
  started_at: string;
  updated_at: string;
}

export interface AdminWorld {
  datasources: Map<string, FixtureDatasource>;
  providers: Map<string, FixtureProvider>;
  promptTools: Map<string, FixturePromptTool>;
  knowledgeEntries: Map<string, FixtureKnowledgeEntry>;
  ruleSets: Map<string, FixtureRuleSet>;
  flows: Map<string, FixtureFlow>;
  settings: Map<string, FixtureNamespace>;
  revisions: Map<string, SettingsRevision[]>;
  tasks: Map<string, FixtureAdminTask>;
  rateVersions: Map<string, Record<string, unknown>>;
  impactTokens: Map<string, string>;
  users: Map<string, FixtureUser>;
  groups: Map<string, FixturePermissionGroup>;
  maskingRules: Map<string, string[]>;
  announcementRevisions: Map<string, FixtureAnnouncementRevision>;
  announcementPublication: {
    revision_id: string | null;
    version: number;
    published_by: string | null;
    published_at: string | null;
  };
  auditEvents: FixtureAuditEvent[];
  identityProviders: Map<string, FixtureIdentityProvider>;
  notificationChannels: Map<string, FixtureNotificationChannel>;
  notificationDeliveries: FixtureNotificationDelivery[];
  notificationRules: Map<string, FixtureNotificationRule>;
  migrationRun: FixtureMigrationRun | null;
}
