/**
 * Batch 2 — the change chain end to end: draft lifecycle, review run,
 * submission, order faces, approval, execution attempt (serve starts no
 * stage_execution worker — BLK-20261017-SERVE-BACKGROUND-WORKERS — so the
 * attempt stays running and the batch cancels it, which also exercises
 * cancelExecutionAttempt), withdraw/void/copy on dedicated orders, the
 * submitter metadata projection and the task read face.
 *
 * The generated client functions are typed as the full response union while
 * the mutator returns envelope.data — structural casts mirror use-admin.ts.
 */
import { beforeAll, describe, expect, it } from "vitest";
import {
  createChangeDraft, deleteChangeDraft, getChangeDraft, getReviewRun,
  listMyFlowDatasourceColumns, listMyFlowDatasourceSchemas, listMyFlowDatasourceTables,
  listReviewRunFindings, revealDraftSql, replaceDraftSql, runDraftReview,
  updateChangeDraft,
} from "@/api/generated/client/change-drafts/change-drafts";
import {
  cancelExecutionAttempt, copyChangeOrderToDraft, createChangeOrderComment,
  createExecutionAttempt, createRollbackDraft, decideChangeOrder, getChangeOrder,
  getExecutionAttempt, listChangeOrderComments, listChangeOrderTimeline,
  listExecutionAttemptStatements, listOrderReviewFindings, recordOrderSqlCopy,
  revealOrderSql, submitChangeDraft, voidChangeOrder, withdrawChangeOrder,
} from "@/api/generated/client/change-orders/change-orders";
import { getTask } from "@/api/generated/client/tasks/tasks";
import { bootContract } from "./boot";
import { mode, record } from "./harness";
import { loginAs } from "./roles";

interface IdVersion { id: string; version?: number; revision?: number }

/** Draft If-Match pins the draft ROW VERSION on every mutation (§18.74):
 * mock and backend share the semantics — the SQL revision is only the
 * review-binding content number and diverges from version after a run. */
const draftETag = (v: IdVersion | undefined): number => v?.version ?? 1;
interface TaskView { id: string; state?: string; result_ref?: string | null }

const asView = <T>(value: unknown): T => value as T;
const headers = (version?: number, idem = true): Record<string, string> => ({
  "If-Match": `"${String(version ?? 1)}"`,
  ...(idem ? { "Idempotency-Key": `f12-contract-${crypto.randomUUID()}` } : {}),
});

function runIdFromStart(value: unknown): string | null {
  const direct = asView<{ run_id?: string } | undefined>(value)?.run_id;
  if (direct) return direct;
  const ref = asView<TaskView | undefined>(value)?.result_ref;
  if (!ref) return null;
  const m = /(?:sql_grouping|review-runs)\/([0-9a-f-]{36})/i.exec(ref);
  return (m?.[1] as string | undefined) ?? null;
}

interface ChainFixtures { flowId: string; datasourceId: string; taskId: string | null }

let fx: ChainFixtures;

beforeAll(async () => {
  await bootContract();
  if (mode === "mock") {
    const { FIXTURE_FLOW_ID, FIXTURE_DATASOURCE_ID } = await import("@/shared/mock/review-fixture");
    fx = { flowId: FIXTURE_FLOW_ID, datasourceId: FIXTURE_DATASOURCE_ID, taskId: null } as ChainFixtures;
    return;
  }
  await loginAs("admin");
  const { listFlows, listDatasources, testDatasourceConnection } =
    await import("@/api/generated/client/administration/administration");
  // The mutator returns envelope.data: list values are {items, page} directly.
  const flows = asView<{ items?: Array<{ id: string; name: string }> }>(await listFlows());
  const flow = flows.items?.find((f) => f.name === "f12-change-flow");
  const datasources = asView<{ items?: Array<{ id: string; name: string }> }>(await listDatasources());
  const ds = datasources.items?.find((d) => d.name === "f12-pg-target");
  if (!flow || !ds) throw new Error("run scripts/integration/f12_seed.py seed first");
  const task = asView<TaskView>(await testDatasourceConnection(ds.id, { purpose: "review" } as never, {
    headers: { "Idempotency-Key": `f12-contract-${crypto.randomUUID()}` },
  }));
  fx = { flowId: flow.id, datasourceId: ds.id, taskId: task.id ?? null } as ChainFixtures;
});

const DDL = "create table if not exists f12_contract_b2 (id bigint primary key, note text);";

async function draftToSubmittedOrder(title: string): Promise<{ draftId: string; orderId: string }> {
  await loginAs("submitter");
  const created = await record("createChangeDraft", title, () =>
    createChangeDraft({ flow_id: fx.flowId, title, description: "f12 contract batch2" },
      { headers: { "Idempotency-Key": crypto.randomUUID() } }));
  const draft = asView<IdVersion | undefined>(created.value);
  if (!draft?.id) throw new Error(`draft not created: ${created.errCode} ${created.status}`);
  await record("replaceDraftSql", title, () =>
    replaceDraftSql(draft.id, { sql: DDL }, { headers: headers(draftETag(draft)) }));
  // RCP-20261101 ephemeral precheck: request body carries current SQL verbatim for review, response directly returns run_id
  // (no longer going through a task row), polling GET /review-runs/{id} until terminal state.
  const started = await record("runDraftReview", title, () =>
    runDraftReview(
      { flow_id: fx.flowId, draft_id: draft.id, sql: DDL },
      { headers: headers() },
    ));
  const runId = runIdFromStart(started.value);
  expect(runId, `review start carries the run id (${started.errCode} ${started.status})`).toBeTruthy();
  const run = await record("getReviewRun", title, () => getReviewRun(runId!));
  let runView = asView<{ state?: string; gate?: { passed?: boolean } } | undefined>(run.value);
  const runDeadline = Date.now() + 90_000;
  while (runView?.state !== "ready" && runView?.state !== "failed" && Date.now() < runDeadline) {
    await new Promise((r) => setTimeout(r, 800));
    const polled = await record("getReviewRun", "chain-poll", () => getReviewRun(runId!));
    runView = asView<{ state?: string } | undefined>(polled.value);
  }
  expect(runView?.state).toBe("ready");
  await record("listReviewRunFindings", title, () => listReviewRunFindings(runId!));
  // RCP-20261101 submission: POST /change-orders carries {flow, draft, sql, run_id},
  // server compares reviewed text; draft If-Match is no longer needed. Draft state before submission remains draft
  // (ephemeral precheck does not write draft state).
  await record("getChangeDraft", "pre-submit", () => getChangeDraft(draft.id));
  const submitted = await record("submitChangeDraft", title, () =>
    submitChangeDraft(
      { flow_id: fx.flowId, draft_id: draft.id, sql: DDL, run_id: runId! },
      { headers: headers() },
    ));
  const order = asView<IdVersion | undefined>(submitted.value);
  if (!order?.id) throw new Error(`order not submitted: ${submitted.errCode} ${submitted.status}`);
  return { draftId: draft.id, orderId: order.id };
}

describe("draft lifecycle", () => {
  it("scratch draft: create → get → update → replaceSql → reveal → delete", async () => {
    await loginAs("submitter");
    const created = await record("createChangeDraft", "scratch", () =>
      createChangeDraft({ flow_id: fx.flowId, title: "f12 scratch draft" },
        { headers: { "Idempotency-Key": crypto.randomUUID() } }));
    const draft = asView<IdVersion | undefined>(created.value);
    if (!draft?.id) throw new Error(`scratch draft not created: ${created.errCode} ${created.status}`);
    await record("getChangeDraft", "scratch", () => getChangeDraft(draft.id));
    await record("updateChangeDraft", "scratch-title", () =>
      updateChangeDraft(draft.id, { title: "f12 scratch draft renamed" }, { headers: headers(draft.version) }));
    const withSql = await record("replaceDraftSql", "scratch", () =>
      replaceDraftSql(draft.id, { sql: "select 1;" }, { headers: headers(draftETag(draft)) }));
    const afterSql = asView<IdVersion | undefined>(withSql.value) ?? draft;
    const version = draftETag(afterSql);
    await record("revealDraftSql", "scratch", () =>
      revealDraftSql(draft.id, { reason: "f12 contract reveal" } as never, { headers: headers(version) }));
    await record("deleteChangeDraft", "scratch", () =>
      deleteChangeDraft(draft.id, { headers: headers(version, false) }));
  });

  it("submitter metadata projection: schemas/tables/columns", async () => {
    await loginAs("submitter");
    await record("listMyFlowDatasourceSchemas", "submitter", () => listMyFlowDatasourceSchemas(fx.flowId, fx.datasourceId));
    await record("listMyFlowDatasourceTables", "submitter", () =>
      listMyFlowDatasourceTables(fx.flowId, fx.datasourceId, { schema_name: "public" } as never));
    await record("listMyFlowDatasourceColumns", "submitter", () =>
      listMyFlowDatasourceColumns(fx.flowId, fx.datasourceId, { schema_name: "public", table_name: "f12_demo" } as never));
  });
});

describe("change chain", () => {
  it("review → submit → order faces → approve → attempt (stuck) → cancel", async () => {
    const { orderId } = await draftToSubmittedOrder("f12 chain order");
    await loginAs("submitter");
    const order = await record("getChangeOrder", "chain", () => getChangeOrder(orderId));
    const orderVersion = asView<IdVersion | undefined>(order.value)?.version ?? 1;
    await record("listChangeOrderTimeline", "chain", () => listChangeOrderTimeline(orderId));
    await record("listOrderReviewFindings", "chain", () => listOrderReviewFindings(orderId));
    await record("revealOrderSql", "chain", () =>
      revealOrderSql(orderId, { reason: "f12 contract" } as never, { headers: headers(orderVersion) }));
    await record("recordOrderSqlCopy", "chain", () =>
      recordOrderSqlCopy(orderId, { reason: "f12 contract" } as never, { headers: headers(orderVersion) }));
    await record("listChangeOrderComments", "chain", () => listChangeOrderComments(orderId));
    await record("createChangeOrderComment", "chain", () =>
      createChangeOrderComment(orderId, { content: "f12 contract comment" }, { headers: headers(orderVersion) }));

    await loginAs("approver");
    await record("decideChangeOrder", "approve", () =>
      decideChangeOrder(orderId, { decision: "approve", comment: "f12 contract" }, { headers: headers(orderVersion) }),
      "real execution_actors snapshot omits is_builtin_admin/version (divergence #006)");

    await loginAs("executor");
    const approved = await record("getChangeOrder", "post-approve", () => getChangeOrder(orderId));
    const approvedVersion = asView<IdVersion | undefined>(approved.value)?.version ?? orderVersion + 1;
    const attempt = await record("createExecutionAttempt", "chain", () =>
      createExecutionAttempt(orderId, {} as never, { headers: headers(approvedVersion) }));
    const attemptView = asView<IdVersion | undefined>(attempt.value);
    if (!attemptView?.id) throw new Error(`attempt not created: ${attempt.errCode} ${attempt.status}`);
    const current = await record("getExecutionAttempt", "chain", () => getExecutionAttempt(attemptView.id));
    await record("listExecutionAttemptStatements", "chain", () =>
      listExecutionAttemptStatements(attemptView.id));
    if (mode === "real") {
      // No stage_execution worker in serve (BLK-20261017): cancelling the
      // stuck attempt is cleanup AND the cancel case.
      const version = asView<IdVersion | undefined>(current.value)?.version ?? 1;
      await record("cancelExecutionAttempt", "stuck-attempt", () =>
        cancelExecutionAttempt(attemptView.id, { reason: "f12: no worker in serve (BLK-20261017)" },
          { headers: headers(version) }),
        "real-only cleanup path: the mock attempt already reached a terminal state (BLK-20261017)");
    }
  }, 300_000);

  it("withdraw path", async () => {
    const { orderId } = await draftToSubmittedOrder("f12 withdraw order");
    await loginAs("submitter");
    const order = await record("getChangeOrder", "pre-withdraw", () => getChangeOrder(orderId));
    const version = asView<IdVersion | undefined>(order.value)?.version ?? 1;
    await record("withdrawChangeOrder", "submitter", () =>
      withdrawChangeOrder(orderId, { reason: "f12 contract withdraw" }, { headers: headers(version) }));
  }, 300_000);

  it("void + copy paths", async () => {
    const { orderId } = await draftToSubmittedOrder("f12 void order");
    await loginAs("submitter");
    const order = await record("getChangeOrder", "pre-void", () => getChangeOrder(orderId));
    const version = asView<IdVersion | undefined>(order.value)?.version ?? 1;
    await record("voidChangeOrder", "submitter", () =>
      voidChangeOrder(orderId, { reason: "f12 contract void" }, { headers: headers(version) }));
    const source = await draftToSubmittedOrder("f12 copy source");
    await record("copyChangeOrderToDraft", "submitter", () =>
      copyChangeOrderToDraft(source.orderId, {} as never, { headers: headers(1) }),
      "real business-error envelope carries meta:null (divergence #003)");
    await record("createRollbackDraft", "submitter", () =>
      createRollbackDraft(source.orderId, {}, { headers: headers(1) }),
      "real business-error envelope carries meta:null (divergence #003)");
  }, 300_000);
});

describe("task read face", () => {
  it("getTask via a live connection-test task (real)", async () => {
    if (mode === "real" && fx.taskId) {
      await loginAs("admin");
      const r = await record("getTask", "conn-test", () => getTask(fx.taskId!),
        "real-only case: the mock connection tests complete within the create response");
      expect(r.status).toBe(200);
    }
  });
});
