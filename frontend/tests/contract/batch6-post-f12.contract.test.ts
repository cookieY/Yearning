/**
 * Batch 6 — post-F12 operations closing the 34-operation coverage gap so all
 * 180/180 OpenAPI operations carry live Zod-validated contract evidence:
 *
 * - admin user password reset
 * - prompt-tool AI authoring (optimize, test-cases, test-runs)
 * - AI provider rate versions & AI usage calls/summary
 * - notification rules CRUD + preview
 * - scheduled execution cancellation
 * - SSE event stream JSON handshake
 * - AI assistant sessions, streaming messages, draft precheck & order submission
 * - SQL performance diagnostics config, overview, snapshots, order links & AI optimization
 */
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  confirmDiagnosticOrderLink,
  createAiProviderRateVersion,
  createNotificationChannel,
  createNotificationRule,
  createPermissionGroup,
  createUser,
  deleteNotificationChannel,
  deleteNotificationRule,
  deletePermissionGroup,
  deleteUser,
  diagnoseStatementWithAi,
  generatePromptToolTestCases,
  getDatasourceDiagnosticsConfig,
  getDiagnosticStatementDetail,
  getDiagnosticsOverview,
  getNotificationRule,
  listAiProviderRateVersions,
  listAiUsageCalls,
  listAiUsageSummary,
  listDiagnosticDatasources,
  listNotificationRules,
  optimizePromptToolPrompt,
  precheckDiagnosticOptimizationDraft,
  previewNotificationRule,
  replaceNotificationRule,
  resetUserPassword,
  runPromptToolTest,
  submitDiagnosticOptimizationOrder,
  triggerManualDiagnosticsSnapshot,
  updateDatasourceDiagnosticsConfig,
} from "@/api/generated/client/administration/administration";
import type { CreateNotificationChannelRequest } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { getCurrentUser } from "@/api/generated/client/authentication/authentication";
import {
  cancelAssistantMessageStream,
  createAssistantSession,
  deleteAssistantSession,
  getAssistantSession,
  listAssistantSessions,
  precheckAssistantSqlDraft,
  sendAssistantMessage,
  submitAssistantSqlDraftOrder,
} from "@/api/generated/client/ai-assistant/ai-assistant";
import {
  createChangeDraft,
  getReviewRun,
  replaceDraftSql,
  runDraftReview,
} from "@/api/generated/client/change-drafts/change-drafts";
import {
  cancelExecutionSchedule,
  createExecutionSchedule,
  decideChangeOrder,
  getChangeOrder,
  submitChangeDraft,
} from "@/api/generated/client/change-orders/change-orders";
import { streamDomainEvents } from "@/api/generated/client/events/events";
import { bootContract } from "./boot";
import { mode, record } from "./harness";
import { loginAs } from "./roles";

interface IdVersion {
  id: string;
  version?: number;
}

const asView = <T>(value: unknown): T => value as T;
const suffix = crypto.randomUUID().slice(0, 8);
const idem = (): Record<string, string> => ({
  "Idempotency-Key": `f12-contract-${crypto.randomUUID()}`,
});
const ifMatch = (version: number | undefined, withIdem = false): Record<string, string> => ({
  "If-Match": `"${String(version ?? 1)}"`,
  ...(withIdem ? idem() : {}),
});
const futureIso = (minutes: number): string =>
  new Date(Date.now() + minutes * 60_000).toISOString().replace(/\.\d{3}Z$/, "Z");

let flowId: string;
let datasourceId: string;
let providerId: string;
let channelId: string;
let tempChannel: IdVersion | null = null;

beforeAll(async () => {
  await bootContract();
  await loginAs("admin");
  if (mode === "mock") {
    const { FIXTURE_FLOW_ID, FIXTURE_DATASOURCE_ID } = await import("@/shared/mock/review-fixture");
    const {
      ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
      ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
    } = await import("@/shared/mock/admin-fixture");
    flowId = FIXTURE_FLOW_ID;
    datasourceId = FIXTURE_DATASOURCE_ID;
    providerId = ADMIN_FIXTURE_PROVIDER_PRIMARY_ID;
    channelId = ADMIN_FIXTURE_CHANNEL_EMAIL_ID;
    return;
  }
  const {
    listAiProviders,
    listDatasources,
    listFlows,
    listNotificationChannels,
  } = await import("@/api/generated/client/administration/administration");
  const flows = asView<{ items?: Array<{ id: string; name: string }> }>(await listFlows());
  const datasources = asView<{ items?: Array<{ id: string; name: string }> }>(await listDatasources());
  const providers = asView<{ items?: Array<{ id: string }> }>(await listAiProviders());
  const channels = asView<{ items?: Array<{ id: string }> }>(await listNotificationChannels());
  const flow = flows.items?.find((f) => f.name === "f12-change-flow") ?? flows.items?.[0];
  const ds = datasources.items?.find((d) => d.name === "f12-pg-target") ?? datasources.items?.[0];
  const provider = providers.items?.[0];
  let channel = channels.items?.[0];
  if (!channel) {
    // Freshly reset F12 databases have no persistent channel once batch4 deletes its scratch channel.
    const channelPayload: CreateNotificationChannelRequest = {
      kind: "email",
      name: `f12-b6-channel-${suffix}`,
      enabled: true,
      configuration: {
        host: "smtp.f12.invalid",
        port: 587,
        tls_mode: "starttls",
        username: "f12",
        from_address: "f12@example.invalid",
      },
      secret: { value: "f12-channel-secret" },
    };
    const createdCh = asView<IdVersion>(await createNotificationChannel(channelPayload, { headers: idem() }));
    if (createdCh.id) {
      tempChannel = createdCh;
      channel = { id: createdCh.id };
    }
  }
  if (!flow || !ds || !provider || !channel) {
    throw new Error("missing seeded admin fixtures for batch6 contract tests");
  }
  flowId = flow.id;
  datasourceId = ds.id;
  providerId = provider.id;
  channelId = channel.id;
});

afterAll(async () => {
  if (tempChannel?.id) {
    await loginAs("admin").catch(() => undefined);
    await deleteNotificationChannel(tempChannel.id, {
      headers: ifMatch(tempChannel.version ?? 1),
    }).catch(() => undefined);
  }
});

async function waitForReviewRunTerminal(runId: string): Promise<string | undefined> {
  let runState = asView<{ state?: string } | undefined>(await getReviewRun(runId))?.state;
  const deadline = Date.now() + 120_000;
  while (runState !== "ready" && runState !== "failed" && runState !== "blocked"
    && runState !== "partial" && runState !== "outdated" && runState !== "timeout" && Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 800));
    runState = asView<{ state?: string } | undefined>(await getReviewRun(runId))?.state;
  }
  return runState;
}

async function draftToSubmittedOrder(caseId: string, title: string): Promise<string> {
  const ddl = `CREATE TABLE IF NOT EXISTS f12_contract_b6_${suffix} (id bigint primary key, note text);`;
  await loginAs("submitter");
  const created = await record("createChangeDraft", caseId, () =>
    createChangeDraft({ flow_id: flowId, title, description: "f12 contract batch6" }, { headers: idem() }),
  );
  const draft = asView<IdVersion | undefined>(created.value);
  if (!draft?.id) throw new Error(`draft not created: ${created.errCode} ${created.status}`);
  await record("replaceDraftSql", caseId, () =>
    replaceDraftSql(draft.id, { sql: ddl }, { headers: ifMatch(draft.version, true) }),
  );
  const started = await record("runDraftReview", caseId, () =>
    runDraftReview({ flow_id: flowId, draft_id: draft.id, sql: ddl }, { headers: idem() }),
  );
  const runId = asView<{ run_id?: string } | undefined>(started.value)?.run_id;
  if (!runId) throw new Error("missing ephemeral run_id from runDraftReview");
  await waitForReviewRunTerminal(runId);
  const finalRun = await record("getReviewRun", caseId, () => getReviewRun(runId));
  const runState = asView<{ state?: string } | undefined>(finalRun.value)?.state;
  expect(runState).toBe("ready");
  const submitted = await record("submitChangeDraft", caseId, () =>
    submitChangeDraft({ flow_id: flowId, draft_id: draft.id, sql: ddl, run_id: runId }, { headers: idem() }),
  );
  const order = asView<IdVersion | undefined>(submitted.value);
  if (!order?.id) throw new Error(`order not submitted: ${submitted.errCode} ${submitted.status}`);
  return order.id;
}

describe("admin post-F12 operations", () => {
  it("resets a local user password", async () => {
    await loginAs("admin");
    const created = await createUser(
      {
        username: `f12-b6-${suffix}`,
        display_name: "Batch6 Temp User",
        email: `b6-${suffix}@yearning.test`,
        password: "InitialPassword!123",
      },
      { headers: idem() },
    );
    const user = asView<IdVersion | undefined>(created);
    expect(user?.id).toBeTruthy();

    const reset = await record("resetUserPassword", "temp-user", () =>
      resetUserPassword(user!.id, { new_password: "RotatedPassword!456" }),
    );
    expect(reset.status).toBe(200);
    expect(reset.errCode).toBe(0);

    await deleteUser(user!.id, { headers: ifMatch((user?.version ?? 1) + 1) });
  });

  it("exercises prompt-tool AI authoring endpoints", async () => {
    await loginAs("admin");
    const baseReq = {
      name: `b6-skill-${suffix}`,
      description: "Require WHERE bounds on DML updates",
      engine: "all" as const,
      knowledge_text: "All UPDATE statements must include a bounded WHERE clause.",
    };

    const opt = await record("optimizePromptToolPrompt", "basic", () =>
      optimizePromptToolPrompt(baseReq),
    );
    expect(opt.status).toBe(200);
    expect(opt.errCode).toBe(0);
    const refined = asView<{ knowledge_text: string }>(opt.value).knowledge_text;
    expect(refined.length).toBeGreaterThan(0);

    const generated = await record("generatePromptToolTestCases", "basic", () =>
      generatePromptToolTestCases({ ...baseReq, knowledge_text: refined }),
    );
    expect(generated.status).toBe(200);
    expect(generated.errCode).toBe(0);
    const cases = asView<{
      cases: Array<{ id: string; sql: string; expected_compliant: boolean; rationale: string }>;
    }>(generated.value).cases;
    expect(cases.length).toBeGreaterThan(0);

    const run = await record("runPromptToolTest", "basic", () =>
      runPromptToolTest({ ...baseReq, knowledge_text: refined, cases }),
    );
    expect(run.status).toBe(200);
    expect(run.errCode).toBe(0);
  });

  it("manages AI provider rate versions and queries AI usage ledger", async () => {
    await loginAs("admin");
    const effectiveFrom = new Date(Date.UTC(2027, 0, 1) + ((Date.now() % 86_400_000) * 1000))
      .toISOString()
      .replace(/\.\d{3}Z$/, "Z");
    const created = await record("createAiProviderRateVersion", "usd-full-day", () =>
      createAiProviderRateVersion(providerId, {
        currency: "USD",
        timezone: "UTC",
        effective_from: effectiveFrom,
        windows: [
          {
            start_minute: 0,
            end_minute: 1440,
            input_price: "1.50",
            output_price: "3.00",
          },
        ],
      }),
    );
    expect(created.status).toBe(200);
    expect(created.errCode).toBe(0);

    const listed = await record("listAiProviderRateVersions", "provider", () =>
      listAiProviderRateVersions(providerId),
    );
    expect(listed.status).toBe(200);
    expect(listed.errCode).toBe(0);

    const calls = await record("listAiUsageCalls", "provider-filter", () =>
      listAiUsageCalls({ provider_id: providerId, limit: 20 }),
    );
    expect(calls.status).toBe(200);
    expect(calls.errCode).toBe(0);

    const summary = await record("listAiUsageSummary", "window", () =>
      listAiUsageSummary(),
    );
    expect(summary.status).toBe(200);
    expect(summary.errCode).toBe(0);
  });

  it("executes notification rules preview and full CRUD lifecycle", async () => {
    await loginAs("admin");
    const listBefore = await record("listNotificationRules", "initial", () =>
      listNotificationRules({ limit: 50 }),
    );
    expect(listBefore.status).toBe(200);
    expect(listBefore.errCode).toBe(0);

    const writeBody = {
      name: `b6-rule-${suffix}`,
      enabled: true,
      flow_id: null,
      event_type: "execution.failed" as const,
      target_kind: "order_submitter" as const,
      channel_id: channelId,
      message_template: "Order {{order_display_number}} failed.",
    };

    const preview = await record("previewNotificationRule", "draft", () =>
      previewNotificationRule({ rule: writeBody }),
    );
    expect(preview.status).toBe(200);
    expect(preview.errCode).toBe(0);

    const created = await record("createNotificationRule", "crud", () =>
      createNotificationRule(writeBody),
    );
    expect(created.status).toBe(200);
    expect(created.errCode).toBe(0);
    const createdRule = asView<IdVersion>(created.value);

    const fetched = await record("getNotificationRule", "crud", () =>
      getNotificationRule(createdRule.id),
    );
    expect(fetched.status).toBe(200);
    expect(fetched.errCode).toBe(0);

    const replaced = await record("replaceNotificationRule", "crud", () =>
      replaceNotificationRule(
        createdRule.id,
        { ...writeBody, enabled: false, name: `b6-rule-updated-${suffix}` },
        { headers: ifMatch(createdRule.version) },
      ),
    );
    expect(replaced.status).toBe(200);
    expect(replaced.errCode).toBe(0);
    const updatedVersion = asView<IdVersion>(replaced.value).version ?? 2;

    const deleted = await record("deleteNotificationRule", "crud", () =>
      deleteNotificationRule(createdRule.id, { headers: ifMatch(updatedVersion) }),
    );
    expect(deleted.status).toBe(200);
    expect(deleted.errCode).toBe(0);
  });
});

describe("execution schedule cancellation and event stream handshake", () => {
  it("cancels a scheduled execution and completes SSE stream handshake", async () => {
    const orderId = await draftToSubmittedOrder("b6-cancel-schedule", `f12-cancel-schedule-${suffix}`);

    await loginAs("approver");
    const preApprove = await record("getChangeOrder", "b6-pre-approve", () => getChangeOrder(orderId));
    const v1 = asView<IdVersion>(preApprove.value).version ?? 1;
    await record(
      "decideChangeOrder",
      "b6-approve",
      () => decideChangeOrder(orderId, { decision: "approve", comment: "b6 approve" }, { headers: ifMatch(v1, true) }),
      "real execution_actors snapshot omits is_builtin_admin/version (divergence #006)",
    );

    await loginAs("executor");
    const approved = await record("getChangeOrder", "b6-approved", () => getChangeOrder(orderId));
    const v2 = asView<IdVersion>(approved.value).version ?? v1 + 1;
    const scheduled = await record("createExecutionSchedule", "b6-cancel-setup", () =>
      createExecutionSchedule(orderId, { scheduled_for: futureIso(120) }, { headers: ifMatch(v2, true) }),
    );
    expect(scheduled.status).toBe(200);
    expect(scheduled.errCode).toBe(0);

    const scheduledOrder = await record("getChangeOrder", "b6-scheduled", () => getChangeOrder(orderId));
    const v3 = asView<IdVersion>(scheduledOrder.value).version ?? v2 + 1;
    const cancelled = await record("cancelExecutionSchedule", "live-cancel", () =>
      cancelExecutionSchedule(orderId, { headers: ifMatch(v3, true) }),
    );
    expect(cancelled.status).toBe(200);
    expect(cancelled.errCode).toBe(0);

    await loginAs("submitter");
    const handshake = await record("streamDomainEvents", "handshake", () => streamDomainEvents());
    expect(handshake.status).toBe(200);
    expect(handshake.errCode).toBe(0);
  }, 180_000);
});

describe("AI assistant lifecycle", () => {
  it("exercises session CRUD, messaging, cancellation, precheck, and order submission", async () => {
    await loginAs("submitter");
    const listed = await record("listAssistantSessions", "initial", () =>
      listAssistantSessions({ limit: 20 }),
    );
    expect(listed.status).toBe(200);
    expect(listed.errCode).toBe(0);

    const created = await record("createAssistantSession", "lifecycle", () =>
      createAssistantSession({
        title: `b6 assistant ${suffix}`,
        active_datasource_id: datasourceId,
        active_flow_id: flowId,
      }),
    );
    expect(created.status).toBe(200);
    expect(created.errCode).toBe(0);
    const session = asView<IdVersion>(created.value);

    const detail = await record("getAssistantSession", "lifecycle", () =>
      getAssistantSession(session.id),
    );
    expect(detail.status).toBe(200);
    expect(detail.errCode).toBe(0);

    const sent = await record("sendAssistantMessage", "sql-optimize", () =>
      sendAssistantMessage(session.id, {
        content: "Update note to 'b6-draft' in public.f12_demo where id = 9001",
        capability_hint: "sql_generate",
        datasource_id: datasourceId,
        flow_id: flowId,
        schema_name: "public",
        target_table: "f12_demo",
        input_sql: "UPDATE public.f12_demo SET note = 'b6-draft' WHERE id = 9001;",
      }),
    );
    expect(sent.status).toBe(200);
    expect(sent.errCode).toBe(0);
    const sendView = asView<{
      assistant_message: { id: string };
      sql_draft: { id: string; revision: number; sql_hmac: string; flow_id: string | null } | null;
    }>(sent.value);
    expect(sendView.sql_draft).not.toBeNull();
    const draft = sendView.sql_draft!;

    const cancelMsg = await record("cancelAssistantMessageStream", "noop-completed", () =>
      cancelAssistantMessageStream(session.id, sendView.assistant_message.id),
    );
    expect(cancelMsg.status).toBe(200);
    expect(cancelMsg.errCode).toBe(0);

    const draftFlowId = draft.flow_id ?? flowId;
    const precheck = await record("precheckAssistantSqlDraft", "draft", () =>
      precheckAssistantSqlDraft(session.id, draft.id, {
        flow_id: draftFlowId,
        revision: draft.revision,
        sql_hmac: draft.sql_hmac,
      }),
    );
    expect(precheck.status).toBe(200);
    expect(precheck.errCode).toBe(0);
    const runId = asView<{ ephemeral_run_id: string }>(precheck.value).ephemeral_run_id;
    if (mode === "real") {
      const runState = await waitForReviewRunTerminal(runId);
      expect(runState).toBe("ready");
    }

    const submitted = await record("submitAssistantSqlDraftOrder", "draft", () =>
      submitAssistantSqlDraftOrder(
        session.id,
        draft.id,
        {
          flow_id: draftFlowId,
          run_id: runId,
          revision: draft.revision,
          sql_hmac: draft.sql_hmac,
          title: `b6 assistant draft order ${suffix}`,
        },
        { headers: idem() },
      ),
    );
    expect(submitted.status).toBe(200);
    expect(submitted.errCode).toBe(0);

    const latestSessionDetail = await getAssistantSession(session.id);
    const latestSessionView = asView<{ session?: IdVersion; version?: number }>(
      latestSessionDetail,
    );
    const deleteSessionVersion =
      latestSessionView.session?.version ?? latestSessionView.version ?? session.version ?? 1;
    const deleted = await record("deleteAssistantSession", "cleanup", () =>
      deleteAssistantSession(session.id, { headers: ifMatch(deleteSessionVersion) }),
    );
    expect(deleted.status).toBe(200);
    expect(deleted.errCode).toBe(0);
  }, 180_000);
});

describe("SQL performance diagnostics lifecycle", () => {
  it("exercises config, overview, snapshot, statement detail, order link, and AI optimization", async () => {
    await loginAs("admin");
    const diagDsList = await record("listDiagnosticDatasources", "initial", () =>
      listDiagnosticDatasources(),
    );
    expect(diagDsList.status).toBe(200);
    expect(diagDsList.errCode).toBe(0);
    const diagDsItems = asView<{ items: Array<{ id: string }> }>(diagDsList.value).items;
    const targetDsId =
      diagDsItems.find((d) => d.id === datasourceId)?.id ?? diagDsItems[0]?.id ?? datasourceId;

    const cfg = await record("getDatasourceDiagnosticsConfig", "target", () =>
      getDatasourceDiagnosticsConfig(targetDsId),
    );
    expect(cfg.status).toBe(200);
    expect(cfg.errCode).toBe(0);
    const cfgVersion = asView<{ version: number }>(cfg.value).version;

    const updatedCfg = await record("updateDatasourceDiagnosticsConfig", "enable", () =>
      updateDatasourceDiagnosticsConfig(
        targetDsId,
        { enabled: true, interval_seconds: 60, top_n: 200 },
        { headers: ifMatch(cfgVersion) },
      ),
    );
    expect(updatedCfg.status).toBe(200);
    expect(updatedCfg.errCode).toBe(0);

    if (mode === "real") {
      const lastCollectedAt =
        asView<{ last_collected_at?: string | null } | undefined>(updatedCfg.value)?.last_collected_at ??
        asView<{ last_collected_at?: string | null } | undefined>(cfg.value)?.last_collected_at;
      if (lastCollectedAt) {
        const elapsedMs = Date.now() - Date.parse(lastCollectedAt);
        const remainingMs = 32_000 - elapsedMs;
        if (remainingMs > 0 && remainingMs <= 32_000) {
          await new Promise((r) => setTimeout(r, remainingMs));
        }
      }
    }

    const snapshot = await record("triggerManualDiagnosticsSnapshot", "manual", () =>
      triggerManualDiagnosticsSnapshot(targetDsId),
    );
    expect(snapshot.status).toBe(200);
    expect(snapshot.errCode).toBe(0);

    const overview = await record("getDiagnosticsOverview", "target", () =>
      getDiagnosticsOverview(targetDsId),
    );
    expect(overview.status).toBe(200);
    expect(overview.errCode).toBe(0);
    const overviewStatements = asView<{
      statements: Array<{ statement_id: string; template_preview?: string }>;
    }>(overview.value).statements;
    const pickedStmt =
      overviewStatements.find((s) => (s.template_preview ?? "").includes("f12_demo")) ??
      overviewStatements.find((s) => (s.template_preview ?? "").includes("demo.accounts")) ??
      overviewStatements.find((s) => {
        const preview = s.template_preview ?? "";
        return (
          preview.length > 0 &&
          !preview.includes("pg_stat_statements") &&
          !preview.includes("s.queryid") &&
          !preview.includes("pg_catalog") &&
          !preview.includes("pg_control") &&
          !preview.includes("pg_class") &&
          !/^\s*(begin|rollback|set|create\s+database|drop\s+database)\b/i.test(preview)
        );
      }) ??
      overviewStatements[0];
    const stmtId = pickedStmt?.statement_id;
    expect(stmtId).toBeTruthy();

    const stmtDetail = await record("getDiagnosticStatementDetail", "target", () =>
      getDiagnosticStatementDetail(targetDsId, stmtId!),
    );
    expect(stmtDetail.status).toBe(200);
    expect(stmtDetail.errCode).toBe(0);

    const linkedOrderId = await draftToSubmittedOrder("b6-diag-link", `f12-diag-link-${suffix}`);
    await loginAs("admin");
    const orderLink = await record("confirmDiagnosticOrderLink", "confirm", () =>
      confirmDiagnosticOrderLink(targetDsId, stmtId!, {
        change_order_id: linkedOrderId,
        relation_kind: "index_or_schema_change",
      }),
    );
    expect(orderLink.status).toBe(200);
    expect(orderLink.errCode).toBe(0);

    const aiReport = await record("diagnoseStatementWithAi", "analyze", () =>
      diagnoseStatementWithAi(targetDsId, stmtId!, {}),
    );
    expect(aiReport.status).toBe(200);
    expect(aiReport.errCode).toBe(0);
    const reportId = asView<{ report_id: string }>(aiReport.value).report_id;

    // PrecheckOptimizationDraft enforces admin_is_not_business_override = true:
    // temporarily grant the admin actor submission access to flowId in real mode,
    // and clean up the ephemeral permission group unconditionally.
    let tempGroup: IdVersion | null = null;
    if (mode === "real") {
      const me = asView<{ user?: { id?: string }; id?: string }>(await getCurrentUser());
      const adminUserId = me.user?.id ?? me.id;
      if (!adminUserId) throw new Error("unable to resolve current admin user id");
      tempGroup = asView<IdVersion>(
        await createPermissionGroup(
          {
            name: `f12-b6-diag-opt-${suffix}`,
            enabled: true,
            member_user_ids: [adminUserId],
            granted_flow_ids: [flowId],
          },
          { headers: idem() },
        ),
      );
    }

    try {
      const candidateSql = `CREATE TABLE IF NOT EXISTS f12_diag_opt_${suffix} (id bigint primary key, note text);`;
      const precheck = await record("precheckDiagnosticOptimizationDraft", "optimize", () =>
        precheckDiagnosticOptimizationDraft(targetDsId, reportId, {
          flow_id: flowId,
          candidate_sql: candidateSql,
        }),
      );
      expect(precheck.status).toBe(200);
      expect(precheck.errCode).toBe(0);
      const runId = asView<{ run_id: string }>(precheck.value).run_id;
      if (mode === "real") {
        const runState = await waitForReviewRunTerminal(runId);
        expect(runState).toBe("ready");
      }

      const submitOpt = await record("submitDiagnosticOptimizationOrder", "optimize", () =>
        submitDiagnosticOptimizationOrder(
          targetDsId,
          reportId,
          {
            flow_id: flowId,
            ephemeral_run_id: runId,
            title: `b6 diag opt order ${suffix}`,
            remark: "f12 contract verification",
          },
          { headers: idem() },
        ),
      );
      expect(submitOpt.status).toBe(200);
      expect(submitOpt.errCode).toBe(0);
    } finally {
      if (tempGroup?.id) {
        await deletePermissionGroup(tempGroup.id, {
          headers: ifMatch(tempGroup.version),
        }).catch(() => undefined);
      }
    }
  }, 240_000);
});
