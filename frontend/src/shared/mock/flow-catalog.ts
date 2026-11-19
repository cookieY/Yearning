import type { Flow } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Deterministic multi-flow catalog for owner testing (owner request
 * 2026-09-04): 20 change-review flows with varying stage counts (1–4),
 * datasource chains and approver/executor counts, shared by the review
 * fixture (GET /users/me/flows, the submission wizard) and the admin
 * fixture (/admin/flows, the flow editor). One catalog, one set of
 * datasource identities, two adapters — the submission entry and the
 * admin editor always describe the same flows.
 *
 * Two flows are seeded disabled: they exist for the admin enable toggle
 * and never appear in the submission wizard (mirrors the backend
 * status='enabled' filter in CurrentUserFlows).
 */

export interface CatalogDatasource {
  id: string;
  name: string;
  engine: "mysql" | "postgresql" | "tidb" | "oceanbase";
  compatibility_mode: "mysql" | "postgresql";
}

export interface CatalogFlowSpec {
  id: string;
  name: string;
  enabled: boolean;
  /** Datasource id per ordered stage with approver/executor headcounts. */
  stages: Array<{ datasource_id: string; approvers: number; executors: number }>;
}

const ds = (suffix: string, name: string, engine: CatalogDatasource["engine"]): CatalogDatasource => ({
  id: `4f6f1a2b-0000-4000-8000-00000000c${suffix}`,
  name,
  engine,
  compatibility_mode: engine === "postgresql" ? "postgresql" : "mysql",
});

export const CATALOG_DATASOURCES: CatalogDatasource[] = [
  ds("101", "订单主库", "mysql"),
  ds("102", "订单从库", "mysql"),
  ds("103", "报表分析库", "postgresql"),
  ds("104", "数仓主库", "tidb"),
  ds("105", "报表输出库", "postgresql"),
  ds("106", "计费主库", "mysql"),
  ds("107", "计费从库", "mysql"),
  ds("108", "日志库", "postgresql"),
  ds("109", "CRM主库", "oceanbase"),
  ds("110", "CRM从库", "oceanbase"),
  ds("111", "库存主库", "mysql"),
  ds("112", "库存归档库", "tidb"),
  ds("113", "支付核心库", "mysql"),
  ds("114", "支付审计库", "postgresql"),
  ds("115", "指标库", "postgresql"),
  ds("116", "用户中心库", "mysql"),
  ds("117", "用户中心从库", "mysql"),
  ds("118", "历史档案库", "postgresql"),
  ds("119", "二级归档库", "tidb"),
  ds("120", "搜索索引库", "postgresql"),
  ds("121", "审计合规库", "postgresql"),
  ds("122", "配置中心库", "mysql"),
  ds("123", "消息中心库", "postgresql"),
  ds("124", "风控规则库", "mysql"),
  ds("125", "商品主库", "mysql"),
  ds("126", "优惠券库", "mysql"),
  ds("127", "网关配置库", "oceanbase"),
];

const stage = (datasource: CatalogDatasource, approvers = 1, executors = 1) => ({
  datasource_id: datasource.id,
  approvers,
  executors,
});

/** Name-based lookup with an explicit failure: a typo in the catalog table
 * must crash loudly, not inject an undefined datasource. */
function D(name: string): CatalogDatasource {
  const found = CATALOG_DATASOURCES.find((entry) => entry.name === name);
  if (found === undefined) throw new Error(`unknown catalog datasource: ${name}`);
  return found;
}

const flow = (suffix: string, name: string, enabled: boolean, stages: ReturnType<typeof stage>[]): CatalogFlowSpec => ({
  id: `4f6f1a2b-0000-4000-8000-00000000d${suffix}`,
  name,
  enabled,
  stages,
});

export const CATALOG_FLOWS: CatalogFlowSpec[] = [
  flow("201", "订单主库日常变更", true, [stage(D("订单主库"))]),
  flow("202", "订单读写分离变更", true, [stage(D("订单主库")), stage(D("订单从库"))]),
  flow("203", "数仓分层发布", true, [
    stage(D("数仓主库")),
    stage(D("报表输出库")),
    stage(D("报表分析库"), 2),
  ]),
  flow("204", "计费系统四段变更", true, [
    stage(D("计费主库")),
    stage(D("计费从库")),
    stage(D("报表输出库"), 2),
    stage(D("数仓主库"), 2, 2),
  ]),
  flow("205", "报表库索引优化", true, [stage(D("报表输出库"), 2)]),
  flow("206", "日志库分区清理", false, [stage(D("日志库"))]),
  flow("207", "CRM客户库变更", true, [stage(D("CRM主库")), stage(D("CRM从库"))]),
  flow("208", "库存核心三段变更", true, [
    stage(D("库存主库"), 2),
    stage(D("库存归档库")),
    stage(D("报表分析库")),
  ]),
  flow("209", "支付核心强审变更", true, [
    stage(D("支付核心库"), 3, 2),
    stage(D("支付审计库"), 3),
  ]),
  flow("210", "指标库口径变更", true, [stage(D("指标库"))]),
  flow("211", "用户中心读写变更", true, [stage(D("用户中心库")), stage(D("用户中心从库"), 2)]),
  flow("212", "历史档案四段归档", true, [
    stage(D("历史档案库")),
    stage(D("二级归档库")),
    stage(D("日志库")),
    stage(D("报表分析库")),
  ]),
  flow("213", "搜索索引重建变更", true, [stage(D("搜索索引库"), 2)]),
  flow("214", "审计合规库变更", true, [stage(D("审计合规库"), 2), stage(D("日志库"))]),
  flow("215", "配置中心变更", false, [stage(D("配置中心库"))]),
  flow("216", "消息中心变更", true, [stage(D("消息中心库")), stage(D("指标库"))]),
  flow("217", "风控规则库变更", true, [
    stage(D("风控规则库"), 2),
    stage(D("支付审计库"), 2),
    stage(D("审计合规库")),
  ]),
  flow("218", "商品主库变更", true, [stage(D("商品主库")), stage(D("搜索索引库"))]),
  flow("219", "优惠券库变更", true, [stage(D("优惠券库"))]),
  flow("220", "全链路发布流程", true, [
    stage(D("订单主库"), 2),
    stage(D("商品主库"), 2),
    stage(D("库存主库"), 2),
    stage(D("报表分析库")),
  ]),
];

export const CATALOG_FLOW_IDS: ReadonlySet<string> = new Set(CATALOG_FLOWS.map((entry) => entry.id));


/** The review fixture's read shape (FlowStageView): the datasource catalog
 * name is resolved server-side in the real backend, so the mock resolves it
 * from the catalog directly. Approval-step actors and execution actors are
 * distinct identity slots (§18.44): reviewerId fills the frozen approval
 * slots, executorId the execution slots — never the submitter. */
export function reviewCatalogFlowViews(reviewerId: string, executorId: string): Flow[] {
  return CATALOG_FLOWS.map((spec) => ({
    id: spec.id,
    name: spec.name,
    flow_type: "change_review",
    enabled: spec.enabled,
    status: spec.enabled ? "enabled" : "disabled",
    rule_set_id: null,
    stages: spec.stages.map((entry, index) => ({
      position: index + 1,
      datasource_id: entry.datasource_id,
      datasource_name:
        CATALOG_DATASOURCES.find((candidate) => candidate.id === entry.datasource_id)?.name ??
        entry.datasource_id,
      approval_steps: Array.from({ length: entry.approvers }, (_, position) => ({
        position: position + 1,
        actors: [{ user_id: reviewerId }],
      })),
      execution_actors: Array.from({ length: entry.executors }, () => ({
        user_id: executorId,
      })),
    })),
    version: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  }));
}
