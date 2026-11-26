/**
 * Batch 5 — the query domain remainder and the execution leftovers:
 *
 * - query access: request → decision → grant (+ withdrawal on a second
 *   request, revocation via a third grant, renewal on the primary grant)
 * - query sessions: create → list → get → schemas/tables/columns →
 *   executeSelect (paged: generate_series with page_size 1) →
 *   fetchQueryResultPage → closure
 * - execution: a scratch change order carried to approval, then schedule →
 *   attempt → verification → cancel (serve starts no stage_execution worker:
 *   BLK-20261017-SERVE-BACKGROUND-WORKERS — the verification honestly
 *   reports still_unknown and the attempt is cancelled as cleanup).
 */
import { beforeAll, describe, expect, it } from "vitest";
import {
  createQueryAccessRequest, createQueryGrantRenewalRequest, decideQueryAccess,
  getQueryAccessRequest, getQueryGrant, listQueryAccessRequests, listQueryGrants,
  relinquishQueryGrant, revokeQueryGrant, withdrawQueryAccessRequest,
} from "@/api/generated/client/query-access/query-access";
import {
  closeQuerySession, createQuerySession, executeSelect, fetchQueryResultPage,
  getQuerySession, listQuerySessionColumns, listQuerySessionSchemas,
  listQuerySessionTables, listQuerySessions,
} from "@/api/generated/client/query-sessions/query-sessions";
import {
  createChangeDraft, getReviewRun, replaceDraftSql, runDraftReview,
} from "@/api/generated/client/change-drafts/change-drafts";
import {
  cancelExecutionAttempt, createExecutionAttempt, createExecutionSchedule,
  createExecutionVerification, decideChangeOrder, getChangeOrder, getExecutionAttempt,
  submitChangeDraft,
} from "@/api/generated/client/change-orders/change-orders";
import { bootContract } from "./boot";
import { mode, record } from "./harness";
import { loginAs } from "./roles";

interface IdVersion { id: string; version?: number; revision?: number }

const asView = <T>(value: unknown): T => value as T;
const suffix = crypto.randomUUID().slice(0, 8);
// §18.74: If-Match target is row version (SQL revision diverges from it after review completes
// and must never be used as If-Match target) — aligned with batch2 helper and mock revisionConflict.
const draftETag = (v: IdVersion | undefined): number => v?.version ?? 1;
const ifMatch = (version: number | undefined, idem = false): Record<string, string> => ({
  "If-Match": `"${String(version ?? 1)}"`,
  ...(idem ? { "Idempotency-Key": `f12-contract-${crypto.randomUUID()}` } : {}),
});
const idem = (): Record<string, string> => ({ "Idempotency-Key": `f12-contract-${crypto.randomUUID()}` });
// OBS-009 / RCP-20261021: query-access requested_until is server-timezone
// wall clock with a Z marker (reanchorSystemTimezone reinterprets the digits
// by general.system_timezone, default Asia/Shanghai) — a UTC-moment ISO
// string reanchors 8h into the past and fails 1001. scheduled_for (B10
// execution schedule) stays a true UTC instant — no reanchor there.
const wallClockIso = (minutes: number): string => {
  const parts = Object.fromEntries(new Intl.DateTimeFormat("en-CA", {
    timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  }).formatToParts(new Date(Date.now() + minutes * 60_000)).map((p) => [p.type, p.value]));
  return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}:00Z`;
};
const futureIso = (minutes: number): string =>
  new Date(Date.now() + minutes * 60_000).toISOString().replace(/\.\d{3}Z$/, "Z");

const DDL = `create table if not exists f12_contract_b5_${suffix} (id bigint primary key, note text);`;

let queryFlowId: string;
let changeFlowId: string;
let datasourceId: string;

beforeAll(async () => {
  await bootContract();
  await loginAs("admin");
  if (mode === "mock") {
    const { FIXTURE_FLOW_ID, FIXTURE_DATASOURCE_ID } = await import("@/shared/mock/review-fixture");
    const { QUERY_FIXTURE_FLOW_ID, QUERY_FIXTURE_DS_PG_ID, seedQueryScenario } = await import("@/shared/mock/query-fixture");
    // The vitest mock world skips the browser demo seed; grant the query
    // flow explicitly so the access-request path is open for the session user.
    seedQueryScenario("query-flow");
    changeFlowId = FIXTURE_FLOW_ID;
    // The mock query world is keyed to its own fixture flow/datasource pair.
    queryFlowId = QUERY_FIXTURE_FLOW_ID;
    datasourceId = QUERY_FIXTURE_DS_PG_ID;
    void FIXTURE_DATASOURCE_ID;
    return;
  }
  const { listFlows, listDatasources } = await import("@/api/generated/client/administration/administration");
  const flows = asView<{ items?: Array<{ id: string; name: string }> }>(await listFlows());
  const datasources = asView<{ items?: Array<{ id: string; name: string }> }>(await listDatasources());
  const change = flows.items?.find((f) => f.name === "f12-change-flow");
  const query = flows.items?.find((f) => f.name === "f12-query-flow");
  const ds = datasources.items?.find((d) => d.name === "f12-pg-target");
  if (!change || !query || !ds) throw new Error("run scripts/integration/f12_seed.py seed first");
  changeFlowId = change.id;
  queryFlowId = query.id;
  datasourceId = ds.id;
});

/** Compact submit chain: draft → SQL → stub review → submitted order. */
async function draftToSubmittedOrder(title: string): Promise<string> {
  await loginAs("submitter");
  const created = await record("createChangeDraft", "exec-chain", () =>
    createChangeDraft({ flow_id: changeFlowId, title, description: "f12 batch5 execution chain" },
      { headers: idem() }));
  const draft = asView<IdVersion | undefined>(created.value);
  if (!draft?.id) throw new Error(`draft not created: ${created.errCode} ${created.status}`);
  await record("replaceDraftSql", "exec-chain", () =>
    replaceDraftSql(draft.id, { sql: DDL }, { headers: ifMatch(draftETag(draft), true) }));
  // RCP-20261101 ephemeral precheck: request body carries current SQL verbatim for review, response directly returns run_id
  // (no longer going through a task row), polling GET /review-runs/{id} until terminal state.
  const started = await record("runDraftReview", "exec-chain", () =>
    runDraftReview({ flow_id: changeFlowId, draft_id: draft.id, sql: DDL },
      { headers: idem() }));
  const startView = asView<{ run_id?: string; result_ref?: string | null } | undefined>(started.value);
  const match = /(?:sql_grouping|review-runs)\/([0-9a-f-]{36})/i.exec(startView?.result_ref ?? "");
  const runId = startView?.run_id ?? match?.[1] ?? null;
  if (!runId) throw new Error(`review start carries no run id (${started.errCode} ${started.status})`);
  const deadline = Date.now() + 120_000;
  let run = asView<{ state?: string } | undefined>(await getReviewRun(runId));
  while (run?.state !== "ready" && run?.state !== "failed" && run?.state !== "blocked"
    && run?.state !== "partial" && run?.state !== "timeout" && Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 800));
    run = asView<{ state?: string } | undefined>(await getReviewRun(runId));
  }
  expect(run?.state).toBe("ready");
  // RCP-20261101 submission: POST /change-orders carries {flow, draft, sql, run_id},
  // server compares reviewed text; draft If-Match is no longer needed. Idempotency key is required.
  const submitted = await record("submitChangeDraft", "exec-chain", () =>
    submitChangeDraft({ flow_id: changeFlowId, draft_id: draft.id, sql: DDL, run_id: runId! },
      { headers: idem() }));
  const order = asView<IdVersion | undefined>(submitted.value);
  if (!order?.id) throw new Error(`order not submitted: ${submitted.errCode} ${submitted.status}`);
  return order.id;
}

describe("query access family", () => {
  it("request → decision → grant → sessions → renewal → relinquish; withdrawal; revocation", async () => {
    // --- primary request: drives the grant used by the session family.
    await loginAs("quser");
    await record("listQueryAccessRequests", "quser-face", () => listQueryAccessRequests(),
      "real list omits the page envelope (divergence #016, #005 family)");
    // --- withdrawal path first: the mock allows one open request per
    // requester, so B is created and withdrawn before the primary request.
    const createdB = asView<IdVersion | undefined>(await createQueryAccessRequest({
      flow_id: queryFlowId, datasource_ids: [datasourceId],
      requested_until: wallClockIso(120), reason: `f12 batch5 withdrawal ${suffix}`,
    }, { headers: idem() }));
    if (!createdB?.id) throw new Error("withdrawal request not created");
    await record("withdrawQueryAccessRequest", "undecided", () =>
      withdrawQueryAccessRequest(createdB!.id, { reason: "f12 batch5 withdrawal" },
        { headers: ifMatch(createdB.version ?? 1, true) }));
    // --- primary request: drives the grant used by the session family.
    const created = await record("createQueryAccessRequest", "primary", () =>
      createQueryAccessRequest({
        flow_id: queryFlowId, datasource_ids: [datasourceId],
        requested_until: wallClockIso(120), reason: `f12 batch5 primary ${suffix}`,
      }, { headers: idem() }));
    const requestA = asView<IdVersion | undefined>(created.value);
    if (!requestA?.id) throw new Error(`query request not created: ${created.errCode} ${created.status}`);
    const gotA = await record("getQueryAccessRequest", "primary", () => getQueryAccessRequest(requestA.id));
    const versionA = asView<IdVersion | undefined>(gotA.value)?.version ?? 1;

    // --- decision grants the access.
    await loginAs("approver");
    await record("decideQueryAccess", "approve", () =>
      decideQueryAccess(requestA.id, { decision: "approve", comment: "f12 batch5" },
        { headers: ifMatch(versionA, true) }));
    await loginAs("quser");
    const granted = await record("getQueryAccessRequest", "granted", () => getQueryAccessRequest(requestA.id));
    const grantId = asView<{ grant_id?: string | null } | undefined>(granted.value)?.grant_id ?? null;
    expect(grantId, "approved request carries a grant").not.toBeNull();
    await record("getQueryGrant", "primary", () => getQueryGrant(grantId!));
    await record("listQueryGrants", "quser-face", () => listQueryGrants());

    // --- session family on the live grant.
    const session = await record("createQuerySession", "primary", () =>
      createQuerySession({ flow_id: queryFlowId, grant_id: grantId, datasource_ids: [datasourceId] },
        { headers: idem() }));
    const sessionId = asView<IdVersion | undefined>(session.value)?.id ?? null;
    if (!sessionId) throw new Error(`query session not created: ${session.errCode} ${session.status}`);
    await record("listQuerySessions", "quser-face", () => listQuerySessions());
    await record("getQuerySession", "primary", () => getQuerySession(sessionId));
    await record("listQuerySessionSchemas", "primary", () =>
      listQuerySessionSchemas(sessionId, { datasource_id: datasourceId }));
    await record("listQuerySessionTables", "primary", () =>
      listQuerySessionTables(sessionId, { datasource_id: datasourceId, schema_name: "public" }));
    await record("listQuerySessionColumns", "primary", () =>
      listQuerySessionColumns(sessionId, { datasource_id: datasourceId, schema_name: "public", table_name: "f12_demo" }));
    // Paged execution: two rows at page_size 1 guarantees a next cursor.
    // (Set-returning functions in FROM — e.g. generate_series — crash the
    // backend with 500 instead of a declared error: divergence #011.)
    const executed = await record("executeSelect", "paged", () =>
      executeSelect(sessionId, {
        datasource_id: datasourceId, schema_name: "public",
        sql: "select 1 as n union all select 2 as n", page_size: 1,
      }, { headers: idem() }));
    const page = asView<{ execution_id?: string; page?: { next_cursor?: string | null; has_more?: boolean } } | undefined>(executed.value);
    if (page?.execution_id && page.page?.next_cursor) {
      await record("fetchQueryResultPage", "second-page", () =>
        fetchQueryResultPage(page.execution_id!, { cursor: page.page!.next_cursor!, purpose: "display", page_size: 1 }));
    }
    await record("closeQuerySession", "primary", () =>
      closeQuerySession(sessionId, { reason: "f12 batch5 done" }, { headers: ifMatch(1, true) }));

    // --- revocation path on a third, short-lived grant. It runs before the
    // renewal: the renewal occupies the requester's single open-request slot
    // in the mock world.
    const createdC = asView<IdVersion | undefined>(await createQueryAccessRequest({
      flow_id: queryFlowId, datasource_ids: [datasourceId],
      requested_until: wallClockIso(120), reason: `f12 batch5 revocation ${suffix}`,
    }, { headers: idem() }));
    if (!createdC?.id) throw new Error("revocation request not created");
    await loginAs("approver");
    const gotC = asView<IdVersion | undefined>(await getQueryAccessRequest(createdC.id));
    await decideQueryAccess(createdC.id, { decision: "approve", comment: "f12 batch5 revoke" },
      { headers: ifMatch(gotC?.version ?? 1, true) });
    const grantC = asView<{ grant_id?: string | null } | undefined>(
      await getQueryAccessRequest(createdC.id))?.grant_id ?? null;
    expect(grantC, "third request carries a grant").not.toBeNull();
    await record("revokeQueryGrant", "third-grant", () =>
      revokeQueryGrant(grantC!, { reason: "f12 batch5 revoke" }, { headers: ifMatch(1, true) }));

    // --- renewal (creates a follow-up request) then relinquish the grant.
    await loginAs("quser");
    await record("createQueryGrantRenewalRequest", "primary", () =>
      // Renewal reanchors exactly like the create path since 2026-11-02
      // (RCP-20261021 OBS-009 parity, closing review F8): requested_until
      // is server-timezone wall clock with a Z marker — same wire form as
      // create. A raw UTC instant would reanchor 8h into the past and fail
      // the future check.
      createQueryGrantRenewalRequest(grantId!, { requested_until: wallClockIso(300), reason: "f12 batch5 renewal" },
        { headers: idem() }));
    await record("relinquishQueryGrant", "primary", () =>
      relinquishQueryGrant(grantId!, { reason: "f12 batch5 relinquish" }, { headers: ifMatch(1, true) }));
  }, 180_000);
});

if (process.env.F12_CONTRACT_SCOPE !== "non-ai") {
  describe("execution leftovers", () => {
    it("approved order → schedule (path A); approved order → attempt → verification → cancel (path B)", async () => {
      // Path A: the scheduled-execution face. A scheduled order no longer
      // accepts a manual attempt (state machine), so the attempt family runs
      // on its own order.
      const scheduleOrderId = await draftToSubmittedOrder(`f12 schedule ${suffix}`);
      await loginAs("approver");
      const scheduleOrder = await record("getChangeOrder", "schedule-chain", () => getChangeOrder(scheduleOrderId));
      const scheduleVersion = asView<IdVersion | undefined>(scheduleOrder.value)?.version ?? 1;
      await record("decideChangeOrder", "schedule-chain", () =>
        decideChangeOrder(scheduleOrderId, { decision: "approve", comment: "f12 batch5 schedule" },
          { headers: ifMatch(scheduleVersion, true) }),
        "real execution_actors snapshot omits is_builtin_admin/version (divergence #006)");
      await loginAs("executor");
      await record("createExecutionSchedule", "exec-chain", () =>
        createExecutionSchedule(scheduleOrderId, { scheduled_for: futureIso(60) },
          { headers: ifMatch(scheduleVersion + 1, true) }));

      // Path B: attempt + verification + cancel.
      const orderId = await draftToSubmittedOrder(`f12 exec ${suffix}`);
      await loginAs("approver");
      const order = await record("getChangeOrder", "exec-chain", () => getChangeOrder(orderId));
      const version = asView<IdVersion | undefined>(order.value)?.version ?? 1;
      await record("decideChangeOrder", "exec-chain", () =>
        decideChangeOrder(orderId, { decision: "approve", comment: "f12 batch5 exec" }, { headers: ifMatch(version, true) }),
        "real execution_actors snapshot omits is_builtin_admin/version (divergence #006)");

      await loginAs("executor");
      const approved = await record("getChangeOrder", "exec-post-approve", () => getChangeOrder(orderId));
      const approvedVersion = asView<IdVersion | undefined>(approved.value)?.version ?? version + 1;
      const attempt = await record("createExecutionAttempt", "exec-chain", () =>
        createExecutionAttempt(orderId, {} as never, { headers: ifMatch(approvedVersion, true) }));
      const attemptView = asView<IdVersion | undefined>(attempt.value);
      if (!attemptView?.id) throw new Error(`attempt not created: ${attempt.errCode} ${attempt.status}`);
      await record("createExecutionVerification", "still-unknown", () =>
        createExecutionVerification(attemptView.id, {
          result: "still_unknown",
          reason: "serve starts no stage_execution worker (BLK-20261017-SERVE-BACKGROUND-WORKERS): no statement outcome is observable",
          evidence: [{ kind: "text", content: "f12 contract batch5: attempt remains running" }],
        }, { headers: ifMatch(1, true) }),
        "verification honestly reports still_unknown under the serve-worker blocker");
      const current = await record("getExecutionAttempt", "exec-chain", () => getExecutionAttempt(attemptView.id));
      const attemptVersion = asView<IdVersion | undefined>(current.value)?.version ?? 1;
      await record("cancelExecutionAttempt", "exec-chain", () =>
        cancelExecutionAttempt(attemptView.id, { reason: "f12: no worker in serve (BLK-20261017)" },
          { headers: ifMatch(attemptVersion, true) }),
        "mock runs the attempt to a terminal state synchronously (1010 on cancel); the real attempt stays running under BLK-20261017 and cancels cleanly");
    }, 600_000);
  });
}
