import {
  BULK_MODE_MIN_STATEMENTS,
  FINGERPRINT_MAX_STATEMENT_BYTES,
  FINGERPRINT_MAX_UNIQUE,
} from "@/features/review/bulk-constants";
import { CATALOG_FLOW_IDS } from "@/shared/mock/flow-catalog";
import { nowIsoSeconds as now } from "@/shared/mock/kit";
import { currentBehavior, world } from "./state";
import {
  FIXTURE_DATASOURCE_ID,
  FIXTURE_FLOW_ID,
  type FixtureFinding,
  type FixtureRun,
  type FixtureStageResult,
  type ReviewBehavior,
} from "./types";

const QUEUED_TO_RUNNING_MS = 400;
const RUNNING_TO_TERMINAL_MS = 900;

function terminalRunShape(
  behavior: ReviewBehavior,
  run: FixtureRun,
  summaryLocale: "zh-CN" | "en-US",
): void {
  const collectedAt = now();
  const expiry = new Date(Date.now() + 7 * 24 * 3600 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z");
  const evidenceId = `${run.id.slice(0, 24)}00000000e001`;
  const findingId = `${run.id.slice(0, 24)}00000000f001`;
  const criticalFindingId = `${run.id.slice(0, 24)}00000000f003`;
  world.evidence.set(evidenceId, {
    id: evidenceId,
    source_kind: "tool_call",
    source_reference: "built-in:mysql.table_stats",
    fact_status: "known",
    normalized_fact: {
      object_type: "table",
      object_name: "orders",
      row_estimate: 128400,
      index_present: true,
      collected_by: "built-in read-only tool",
    },
    has_raw_payload: true,
    raw_payload_expires_at: expiry,
    collected_at: collectedAt,
  });

  const setStage = (
    state: FixtureStageResult["state"],
    highest: FixtureStageResult["highest_severity"],
    gatePassed: boolean,
    findings: FixtureFinding[],
  ) => {
    run.stage_results = [
      {
        stage_position: 1,
        datasource_id: FIXTURE_DATASOURCE_ID,
        state,
        highest_severity: highest,
        gate_passed: gatePassed,
        finding_count: findings.length,
        evidence_count: 1,
        snapshot_hash: `snap-${run.id.slice(0, 8)}-${state}`,
        summary:
          state === "passed" || state === "blocked" || state === "partial"
            ? summaryLocale === "en-US"
              ? "The AI review is complete. This result is frozen to the current stage snapshot."
              : "AI 审核已完成，结论已与当前阶段快照冻结。"
            : null,
        summary_locale:
          state === "passed" || state === "blocked" || state === "partial"
            ? summaryLocale
            : null,
      },
    ];
    world.findings.set(run.id, findings);
  };

  const mediumFinding: FixtureFinding = {
    id: findingId,
    stage_position: 1,
    fingerprint_group_id: null,
    category: "performance",
    severity: "medium",
    title: "查询未使用索引过滤",
    message:
      "语句 `UPDATE orders SET status = 1 WHERE user_id = 42` 的 WHERE 条件无法命中现有索引，预估扫描 128400 行。",
    suggestion: "为 orders.user_id 建立索引，或收窄 WHERE 条件后重试。",
    model_confidence: 0.86,
    evidence_ids: [evidenceId],
  };

  const highFinding: FixtureFinding = {
    id: `${findingId.slice(0, 24)}00000000f002`,
    stage_position: 1,
    fingerprint_group_id: null,
    category: "security",
    severity: "high",
    title: "无 WHERE 条件的批量 UPDATE",
    message: "检测到缺少 WHERE 条件的 UPDATE，将影响全表所有行。",
    suggestion: "补充明确的 WHERE 条件并分批执行。",
    model_confidence: 0.94,
    evidence_ids: [evidenceId],
  };

  const criticalFinding: FixtureFinding = {
    id: criticalFindingId,
    stage_position: 1,
    fingerprint_group_id: null,
    category: "security",
    severity: "critical",
    title: "DROP TABLE 高危操作",
    message: "语句包含 DROP TABLE，属于不可自动回滚的破坏性操作。",
    suggestion: "确认是否确需删除整表；如需保留结构请改用 TRUNCATE 并单独评估。",
    model_confidence: 0.97,
    evidence_ids: [evidenceId],
  };

  const bulkAnomalyFindings = (): FixtureFinding[] => {
    const digest = run.digest;
    if (digest === undefined || digest.statementCount < BULK_MODE_MIN_STATEMENTS) return [];
    const groups = digest.groups.filter((group) => group.anomalyCount > 0).slice(0, 5);
    return groups.map((group, offset) => {
      const sample = digest.statements.find(
        (statement) => statement.group === group.ordinal && statement.anomaly,
      );
      const oversized = sample?.oversized ?? false;
      const sampleIndex = String(sample?.index ?? group.firstIndex);
      const groupId = `${run.id.slice(0, 8)}-a000-4000-8000-${String(group.ordinal).padStart(12, "0")}`;
      const findingUuid = `${run.id.slice(0, 8)}-b000-4000-8000-${String(offset).padStart(12, "0")}`;
      return {
        id: findingUuid,
        stage_position: 1,
        fingerprint_group_id: groupId,
        category: oversized ? "operability" : "correctness",
        severity: "high",
        title: oversized ? "单条语句超过尺寸上限" : "无 WHERE 条件的批量 DML",
        message: oversized
          ? `语句 \`#${sampleIndex}\` 超过单语句 ${String(FINGERPRINT_MAX_STATEMENT_BYTES)} 字节上限，无法安全指纹化。`
          : `指纹组 #${String(group.ordinal + 1)}（${String(group.count)} 条）缺少 WHERE 条件，将影响全表；定位语句 \`#${sampleIndex}\`。`,
        suggestion: oversized
          ? "拆分该语句或压缩单条尺寸后重新导入。"
          : "补充明确的 WHERE 条件并分批执行。",
        model_confidence: null,
        evidence_ids: [],
      };
    });
  };

  const fingerprintOverflow = (run.digest?.groupCount ?? 0) > FINGERPRINT_MAX_UNIQUE;

  switch (behavior) {
    case "ready": {
      const anomalies = bulkAnomalyFindings();
      if (fingerprintOverflow) {
        run.state = "failed";
        run.gate = { passed: false, reason_codes: ["stage_review_failed"] };
        run.failure_code = "fingerprint_failed";
        run.finished_at = now();
        setStage("failed", "none", false, []);
        break;
      }
      if (anomalies.length > 0) {
        run.state = "blocked";
        run.gate = { passed: false, reason_codes: ["high_severity_finding"] };
        run.finished_at = now();
        setStage("blocked", "high", false, [...anomalies, mediumFinding]);
        break;
      }
      run.state = "ready";
      run.gate = { passed: true, reason_codes: [] };
      run.finished_at = now();
      setStage("passed", "medium", true, [mediumFinding]);
      break;
    }
    case "blocked": {
      run.state = "blocked";
      run.gate = { passed: false, reason_codes: ["stage_review_blocked", "critical_severity_finding"] };
      run.finished_at = now();
      setStage("blocked", "critical", false, [...bulkAnomalyFindings(), highFinding, criticalFinding]);
      break;
    }
    case "partial": {
      run.state = "partial";
      run.gate = { passed: false, reason_codes: ["stage_review_incomplete"] };
      run.failure_code = "budget_exhausted";
      run.finished_at = now();
      setStage("partial", "medium", false, [...bulkAnomalyFindings(), mediumFinding]);
      break;
    }
    case "provider_failed": {
      run.state = "failed";
      run.gate = { passed: false, reason_codes: ["stage_review_failed"] };
      run.failure_code = "provider_unavailable";
      run.finished_at = now();
      setStage("failed", "none", false, []);
      break;
    }
  }
}

export function advanceEphemeralRun(run: FixtureRun, summaryLocale: "zh-CN" | "en-US"): void {
  const behavior = currentBehavior();
  setTimeout(() => {
    run.state = "running";
    run.started_at = now();
    run.version += 1;
    setTimeout(() => {
      terminalRunShape(behavior, run, summaryLocale);
      run.version += 1;
    }, RUNNING_TO_TERMINAL_MS);
  }, QUEUED_TO_RUNNING_MS);
}

interface MetadataWorld {
  schemas: string[];
  tables: Array<{ schema_name: string; table_name: string; relation_kind: "table" }>;
  columns: Array<{
    schema_name: string;
    table_name: string;
    column_name: string;
    ordinal: number;
    data_type: string;
    nullable: boolean;
    masked: boolean;
  }>;
}

const METADATA_WORLDS: Record<string, MetadataWorld> = {
  [FIXTURE_DATASOURCE_ID]: {
    schemas: ["app"],
    tables: [
      { schema_name: "app", table_name: "orders", relation_kind: "table" },
      { schema_name: "app", table_name: "users", relation_kind: "table" },
    ],
    columns: [
      { schema_name: "app", table_name: "orders", column_name: "id", ordinal: 1, data_type: "bigint", nullable: false, masked: false },
      { schema_name: "app", table_name: "orders", column_name: "user_id", ordinal: 2, data_type: "bigint", nullable: false, masked: false },
      { schema_name: "app", table_name: "orders", column_name: "amount", ordinal: 3, data_type: "decimal", nullable: true, masked: false },
      { schema_name: "app", table_name: "users", column_name: "id", ordinal: 1, data_type: "bigint", nullable: false, masked: false },
      { schema_name: "app", table_name: "users", column_name: "email", ordinal: 2, data_type: "varchar", nullable: true, masked: true },
    ],
  },
};

export function metadataWorld(datasourceId: string): MetadataWorld {
  return (
    METADATA_WORLDS[datasourceId] ?? {
      schemas: ["public"],
      tables: [{ schema_name: "public", table_name: "demo_table", relation_kind: "table" }],
      columns: [
        { schema_name: "public", table_name: "demo_table", column_name: "id", ordinal: 1, data_type: "bigint", nullable: false, masked: false },
      ],
    }
  );
}

export function grantedSubmitterDatasource(flowId: string, datasourceId: string): boolean {
  const flow = [FIXTURE_FLOW_ID, ...CATALOG_FLOW_IDS].find((id) => id === flowId);
  if (flow === undefined) return false;
  if (flowId === FIXTURE_FLOW_ID) return datasourceId === FIXTURE_DATASOURCE_ID;
  return datasourceId.startsWith("4f6f1a2b-0000-4000-8000-00000000c1");
}
