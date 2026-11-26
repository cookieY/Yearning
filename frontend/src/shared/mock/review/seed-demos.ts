import { MOCK_IDENTITY_IDS } from "@/shared/mock/identity-store";
import { nowIsoSeconds as now } from "@/shared/mock/kit";
import { GALLERY_TOTAL, derivedUuid } from "./seed-gallery";
import { fixtureActor, fixtureUser, recordOrderEvent, world } from "./state";
import { FIXTURE_OWNER_ID, type FixtureApprovalStep, type FixtureOrder } from "./types";

const PENDING_DEMO_FIRST_ID = "7e6f1a2b-0000-4000-8000-a00000000001";
const PENDING_RISK_HINTS_RUN_ID = "7e6f1a2b-0000-4000-8000-a0000000f901";
const PENDING_EXECUTION_DEMO_ID = "a0000000-0000-4000-8000-00000000e001";
const BULK_DEMO_FIRST_ID = "b0000000-0000-4000-8000-00000000b001";

function seedPendingRiskHints(order: FixtureOrder): void {
  const idPrefix = PENDING_RISK_HINTS_RUN_ID.slice(0, 24);
  const evidenceTable = `${idPrefix}00000000e001`;
  const evidenceIndex = `${idPrefix}00000000e002`;
  const expiry = new Date(Date.now() + 7 * 24 * 3600 * 1000)
    .toISOString()
    .replace(/\.\d{3}Z$/, "Z");
  world.evidence.set(evidenceTable, {
    id: evidenceTable,
    source_kind: "tool_call",
    source_reference: "built-in:mysql.table_stats",
    fact_status: "known",
    normalized_fact: {
      object_type: "table",
      object_name: "point_ledger",
      row_estimate: 2317000,
      index_present: true,
      collected_by: "built-in read-only tool",
    },
    has_raw_payload: true,
    raw_payload_expires_at: expiry,
    collected_at: now(),
  });
  world.evidence.set(evidenceIndex, {
    id: evidenceIndex,
    source_kind: "tool_call",
    source_reference: "built-in:mysql.index_stats",
    fact_status: "known",
    normalized_fact: {
      object_type: "index",
      table_name: "point_ledger",
      column: "created_at",
      index_present: false,
      collected_by: "built-in read-only tool",
    },
    has_raw_payload: true,
    raw_payload_expires_at: expiry,
    collected_at: now(),
  });
  world.findings.set(PENDING_RISK_HINTS_RUN_ID, [
    {
      id: `${idPrefix}00000000f001`,
      stage_position: 1,
      fingerprint_group_id: null,
      category: "performance",
      severity: "medium",
      title: "更新条件无法命中索引",
      message:
        "语句 `UPDATE point_ledger SET settled = 1 WHERE created_at < '2026-06-01'` 的 WHERE 列 created_at 无可用索引，预估扫描约 231 万行。",
      suggestion: "为 point_ledger.created_at 建立索引，或按主键分段分批更新。",
      model_confidence: 0.88,
      evidence_ids: [evidenceTable, evidenceIndex],
    },
    {
      id: `${idPrefix}00000000f002`,
      stage_position: 1,
      fingerprint_group_id: null,
      category: "operability",
      severity: "medium",
      title: "批量变更缺少分批策略",
      message: "单条 UPDATE 预估影响行数过大，长事务持锁可能阻塞线上写入。",
      suggestion: "按主键范围拆分为多次小批量执行，并安排在低峰期。",
      model_confidence: 0.81,
      evidence_ids: [evidenceTable],
    },
    {
      id: `${idPrefix}00000000f003`,
      stage_position: 1,
      fingerprint_group_id: null,
      category: "correctness",
      severity: "low",
      title: "时间字面量依赖隐式时区转换",
      message: "`'2026-06-01'` 与 DATETIME 列比较时依赖会话时区，跨环境重放可能产生偏移。",
      suggestion: "改用显式 UTC 时间字面量，或以 CAST 统一时区后比较。",
      model_confidence: 0.64,
      evidence_ids: [],
    },
  ]);
  order.review_run_id = PENDING_RISK_HINTS_RUN_ID;
  order.sql_text = "-- 积分流水表历史数据批量修正\nUPDATE point_ledger SET settled = 1 WHERE created_at < '2026-06-01';";
}

export function seedPendingApprovalOrders(): void {
  if (import.meta.env.MODE === "test") return;
  if (world.orders.has(PENDING_DEMO_FIRST_ID)) return;
  const specs: Array<{
    suffix: string;
    title: string;
    datasource: string;
    steps: number;
    riskHints?: boolean;
  }> = [
    { suffix: "01", title: "订单表新增归档状态字段", datasource: "staging-mysql", steps: 2 },
    { suffix: "02", title: "清理失效账号并回收配额", datasource: "report-pg", steps: 1 },
    { suffix: "03", title: "会员表补充索引与分区维护", datasource: "prod-mysql", steps: 1 },
    { suffix: "04", title: "账单表拆分历史冷数据", datasource: "archive-mysql", steps: 2 },
    { suffix: "05", title: "风控规则表新增灰度开关", datasource: "risk-pg", steps: 1 },
    { suffix: "06", title: "消息队列表扩容与索引重建", datasource: "mq-mysql", steps: 3 },
    { suffix: "07", title: "日志表按月分区滚动调整", datasource: "log-pg", steps: 1 },
    { suffix: "08", title: "商品库存表字段精简", datasource: "inventory-mysql", steps: 2 },
    { suffix: "09", title: "积分流水表历史数据批量修正", datasource: "crm-mysql", steps: 1, riskHints: true },
  ];
  specs.forEach((spec, index) => {
    const id = `${PENDING_DEMO_FIRST_ID.slice(0, -2)}${spec.suffix}`;
    const submittedAt = new Date(Date.parse("2026-09-02T01:00:00Z") + index * 3600 * 1000)
      .toISOString()
      .slice(0, 19)
      .concat("Z");
    const steps: FixtureApprovalStep[] = Array.from({ length: spec.steps }, (_, stepIndex) => ({
      id: derivedUuid(id, 0x2001 + stepIndex),
      position: stepIndex + 1,
      state: stepIndex === 0 ? "active" : "pending",
      decided_at: null,
      actors: [fixtureActor(MOCK_IDENTITY_IDS.reviewer), fixtureActor(MOCK_IDENTITY_IDS.owner)],
    }));
    const order: FixtureOrder = {
      id,
      display_number: `YR-20260901-0002${spec.suffix}`,
      submitter_user_id: FIXTURE_OWNER_ID,
      requested_scheduled_for: null,
      title: spec.title,
      state: "stage_approval_active",
      current_stage_position: 1,
      stages: [
        {
          id: derivedUuid(id, 0x1001),
          position: 1,
          datasource_name: spec.datasource,
          state: "approval_active",
          approval_steps: steps,
          execution_actors: [fixtureUser(MOCK_IDENTITY_IDS.executor), fixtureUser(MOCK_IDENTITY_IDS.owner)],
        },
      ],
      has_sql: true,
      sql_hash: `hash-pending-${spec.suffix}`,
      snapshot_hash: `snap-pending-${spec.suffix}`,
      manually_verified: false,
      version: 2,
      submitted_at: submittedAt,
      terminal_at: null,
      review_run_id: null,
      sql_text: `-- ${spec.title}\nUPDATE demo SET applied = 1 WHERE id = ${String(index + 1)};`,
    };
    world.orders.set(order.id, order);
    recordOrderEvent(order, "change_order.submitted", "user", order.submitter_user_id, "工单提交，审核快照已冻结", 1, "submitted");
    if (spec.riskHints) {
      seedPendingRiskHints(order);
      recordOrderEvent(
        order,
        "stage.review_completed",
        "system",
        null,
        "AI 审核完成：门禁通过，2 条中风险、1 条低风险提示随快照冻结",
        1,
        "stage_approval_active",
      );
    }
  });
}

export function seedPendingExecutionOrder(): void {
  if (import.meta.env.MODE === "test") return;
  if (world.orders.has(PENDING_EXECUTION_DEMO_ID)) return;
  const requested = new Date(Date.now() + 26 * 3600 * 1000)
    .toISOString().replace(/\.\d{3}Z$/, "Z");
  const submittedAt = new Date(Date.parse("2026-09-01T08:00:00Z") - (GALLERY_TOTAL + 9) * 86_400_000)
    .toISOString().slice(0, 19).concat("Z");
  const order: FixtureOrder = {
    id: PENDING_EXECUTION_DEMO_ID,
    display_number: "YR-20260901-000010",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: requested,
    title: "结算表分区切换（期望次日低峰执行）",
    state: "stage_execution_pending",
    current_stage_position: 1,
    stages: [
      {
        id: derivedUuid(PENDING_EXECUTION_DEMO_ID, 0x1001),
        position: 1,
        datasource_name: "prod-mysql",
        state: "execution_pending",
        approval_steps: [
          {
            id: derivedUuid(PENDING_EXECUTION_DEMO_ID, 0x2001),
            position: 1,
            state: "approved",
            decided_at: "2026-09-01T09:00:00Z",
            actors: [fixtureActor(MOCK_IDENTITY_IDS.reviewer), fixtureActor(MOCK_IDENTITY_IDS.owner)],
          },
        ],
        execution_actors: [fixtureUser(MOCK_IDENTITY_IDS.executor), fixtureUser(MOCK_IDENTITY_IDS.owner)],
      },
    ],
    has_sql: true,
    sql_hash: "hash-pending-e01",
    snapshot_hash: "snap-pending-e01",
    manually_verified: false,
    version: 3,
    submitted_at: submittedAt,
    terminal_at: null,
    review_run_id: null,
    sql_text: "ALTER TABLE settlement REORGANIZE PARTITION p_current;",
  };
  world.orders.set(order.id, order);
  recordOrderEvent(order, "change_order.submitted", "user", order.submitter_user_id, "工单提交，审核快照已冻结", 1, "submitted");
  recordOrderEvent(order, "approval.decided", "user", MOCK_IDENTITY_IDS.reviewer, "审批通过，等待执行", 1, "stage_execution_pending");
}

function bulkSqlText(seed: number): string {
  const lines: string[] = [];
  const tables = ["orders", "order_items", "customers", "payments", "coupons", "inventory", "refunds", "settlements"];
  for (let index = 1; index <= 500; index += 1) {
    const bucket = (index + seed) % 8;
    const table = tables[bucket] ?? "orders";
    if (bucket % 3 === 0) {
      lines.push(`UPDATE ${table} SET updated_at = NOW() WHERE id = ${String(index * seed)}`);
    } else if (bucket % 3 === 1) {
      lines.push(`UPDATE ${table} SET status = ${String(index % 4)} WHERE region_id = ${String(index % 32)}`);
    } else {
      lines.push(`DELETE FROM ${table} WHERE id = ${String(index * seed + 1)} AND created_at < '2025-01-01'`);
    }
  }
  return lines.join(";\n").concat(";");
}

export function seedBulkStatementOrders(): void {
  if (import.meta.env.MODE === "test") return;
  if (world.orders.has(BULK_DEMO_FIRST_ID)) return;
  const submittedAt = new Date(Date.parse("2026-09-01T20:00:00Z"))
    .toISOString().slice(0, 19).concat("Z");
  const specs = [
    { suffix: "01", title: "压测·跨8表批量数据修正（500条）", datasource: "prod-mysql" },
    { suffix: "02", title: "压测·历史订单清理脚本（500条）", datasource: "archive-mysql" },
    { suffix: "03", title: "压测·结算批处理回填（500条）", datasource: "report-pg" },
  ] as const;
  specs.forEach((spec, index) => {
    const id = `${BULK_DEMO_FIRST_ID.slice(0, -2)}${spec.suffix}`;
    const order: FixtureOrder = {
      id,
      display_number: `YR-20260901-0005${String(index + 1).padStart(2, "0")}`,
      submitter_user_id: FIXTURE_OWNER_ID,
      requested_scheduled_for: null,
      title: spec.title,
      state: "stage_execution_pending",
      current_stage_position: 1,
      stages: [
        {
          id: derivedUuid(id, 0x1001),
          position: 1,
          datasource_name: spec.datasource,
          state: "execution_pending",
          approval_steps: [
            {
              id: derivedUuid(id, 0x2001),
              position: 1,
              state: "approved",
              decided_at: submittedAt,
              actors: [fixtureActor(MOCK_IDENTITY_IDS.reviewer), fixtureActor(MOCK_IDENTITY_IDS.owner)],
            },
          ],
          execution_actors: [fixtureUser(MOCK_IDENTITY_IDS.executor), fixtureUser(MOCK_IDENTITY_IDS.owner)],
        },
      ],
      has_sql: true,
      sql_hash: `hash-bulk-${spec.suffix}`,
      snapshot_hash: `snap-bulk-${spec.suffix}`,
      manually_verified: false,
      version: 3,
      submitted_at: submittedAt,
      terminal_at: null,
      review_run_id: null,
      sql_text: bulkSqlText(index + 1),
    };
    world.orders.set(order.id, order);
    recordOrderEvent(order, "change_order.submitted", "user", order.submitter_user_id, "工单提交，审核快照已冻结", 1, "submitted");
    recordOrderEvent(order, "approval.decided", "user", MOCK_IDENTITY_IDS.reviewer, "审批通过，等待执行", 1, "stage_execution_pending");
  });
}
