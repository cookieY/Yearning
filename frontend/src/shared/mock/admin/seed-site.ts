import { nowIsoSeconds as now } from "@/shared/mock/kit";
import {
  ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
  ADMIN_FIXTURE_DATASOURCE_MYSQL_ID,
  ADMIN_FIXTURE_DATASOURCE_PG_ID,
  ADMIN_FIXTURE_FLOW_CHANGE_ID,
  ADMIN_FIXTURE_FLOW_QUERY_ID,
  ADMIN_FIXTURE_GROUP_ID,
  ADMIN_FIXTURE_LDAP_ID,
  ADMIN_FIXTURE_MIGRATION_RUN_ID,
  ADMIN_FIXTURE_REVISION_PUBLISHED_ID,
  ADMIN_FIXTURE_USER_BLOCKED_ID,
  ADMIN_FIXTURE_USER_MEMBER_ID,
  SEED_ADMIN_ID,
} from "./ids";
import { sanitizeMarkdown, sha256Hex, world } from "./state";
import type { FixtureAnnouncementRevision } from "./types";

export function seedVolumeFixtures(): void {
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

export function seedSiteFixture(): void {
  const ts = now();
  const tsLater = new Date(Date.parse(ts) + 3600 * 1000).toISOString();
  const tsMuchLater = new Date(Date.parse(ts) + 24 * 3600 * 1000).toISOString();

  world.users.set(SEED_ADMIN_ID, {
    id: SEED_ADMIN_ID,
    username: "admin",
    display_name: "Administrator",
    email: "admin@yearning.test",
    is_builtin_admin: true,
    auth_provider: "local",
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
    auth_provider: "local",
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
    auth_provider: "local",
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
