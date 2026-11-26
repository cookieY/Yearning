import type { HttpHandler } from "msw";
import { adminChannelsRatesHandlers } from "./admin/handlers-channels-rates";
import { adminDatasourcesProvidersHandlers } from "./admin/handlers-datasources-providers";
import { adminFlowsSiteHandlers } from "./admin/handlers-flows-site";
import { adminKnowledgeRulesetsHandlers } from "./admin/handlers-knowledge-rulesets";
import { adminRulesMigrationsHandlers } from "./admin/handlers-rules-migrations";
import { adminSettingsSkillsHandlers } from "./admin/handlers-settings-skills";
import { adminUsersGroupsHandlers } from "./admin/handlers-users-groups";

/**
 * Stateful admin fixture backing FE-F9-REVIEW-ADMIN and FE-F10 site domains.
 */
export {
  ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
  ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
  ADMIN_FIXTURE_DATASOURCE_PG_ID,
  ADMIN_FIXTURE_FLOW_CHANGE_ID,
  ADMIN_FIXTURE_FLOW_QUERY_ID,
  ADMIN_FIXTURE_GROUP_ID,
  ADMIN_FIXTURE_KNOWLEDGE_CONVERTED_ID,
  ADMIN_FIXTURE_KNOWLEDGE_GLOBAL_ID,
  ADMIN_FIXTURE_KNOWLEDGE_TABLE_ID,
  ADMIN_FIXTURE_LDAP_ID,
  ADMIN_FIXTURE_MIGRATION_RUN_ID,
  ADMIN_FIXTURE_PROVIDER_BACKUP_ID,
  ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
  PURPOSES as ADMIN_FIXTURE_PURPOSES,
  ADMIN_FIXTURE_REVISION_PUBLISHED_ID,
  ADMIN_FIXTURE_RULE_SET_ID,
  SETTING_NAMESPACES as ADMIN_FIXTURE_SETTING_NAMESPACES,
  ADMIN_FIXTURE_TASK_TIMING,
  ADMIN_FIXTURE_TOOL_BUILTIN_ID,
  ADMIN_FIXTURE_TOOL_DRAFT_ID,
  ADMIN_FIXTURE_TOOL_ENABLED_ID,
  ADMIN_FIXTURE_USER_BLOCKED_ID,
  ADMIN_FIXTURE_USER_MEMBER_ID,
} from "./admin/ids";
export { adminFixtureInternals, adminFixtureTask } from "./admin/state";
export { resetAdminFixture, seedAdminFixture } from "./admin/seed-core";

export function siteFixtureHandlers(): HttpHandler[] {
  return [
    ...adminUsersGroupsHandlers(),
    ...adminFlowsSiteHandlers(),
    ...adminChannelsRatesHandlers(),
    ...adminRulesMigrationsHandlers(),
  ];
}

export function adminFixtureHandlers(): HttpHandler[] {
  return [
    ...adminDatasourcesProvidersHandlers(),
    ...adminSettingsSkillsHandlers(),
    ...adminKnowledgeRulesetsHandlers(),
  ];
}
