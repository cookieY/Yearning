/**
 * Batch 4 — platform surfaces: scratch flow CRUD + flow masking rules, the
 * legacy migration faces (the mock drives its awaiting-confirmation run to
 * approval; the real stack has no run — offline-command domain — so those
 * cases record the declared error faces), the settings family on the
 * branding namespace (write-back of the current values only), notification
 * channels + test delivery (serve starts no notification worker:
 * BLK-20261017-SERVE-BACKGROUND-WORKERS), announcements, dashboards, the
 * audit feed, the submitter's draft/order lists, and the review evidence
 * family (list / reveal / copy) over a scratch review run.
 */
import { beforeAll, describe, expect, it } from "vitest";
import {
  approveLegacyMigrationRun, assessSettingsImpact, confirmLegacyMigrationCandidate,
  createAnnouncementRevision, createFlow, createNotificationChannel,
  createNotificationTestDelivery, deleteFlow, deleteNotificationChannel,
  getFlow, getLegacyMigrationRun, getNotificationChannel, getSettingsNamespace,
  getSettingsSchema, getSystemHealthDashboard, listAnnouncementRevisions,
  listAuditEvents, listFlowMaskingRules, listFlows, listNotificationChannels,
  listNotificationDeliveries, listSettingsRevisions, publishAnnouncementRevision,
  replaceFlow, replaceFlowMaskingRule, replaceNotificationChannel,
  replaceSettingsNamespace,
} from "@/api/generated/client/administration/administration";
import {
  createChangeDraft, getReviewRun, listChangeDrafts, listReviewRunFindings,
  replaceDraftSql, runDraftReview,
} from "@/api/generated/client/change-drafts/change-drafts";
import {
  listChangeOrders, listReviewFindingEvidence, recordRawReviewEvidenceCopy,
  revealRawReviewEvidence,
} from "@/api/generated/client/change-orders/change-orders";
import { getCurrentAnnouncement } from "@/api/generated/client/announcements/announcements";
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

let baseFlowId: string;
let baseRuleSetId: string;
let baseDatasourceId: string;
let actorUserId: string;

beforeAll(async () => {
  await bootContract();
  await loginAs("admin");
  if (mode === "mock") {
    const { FIXTURE_FLOW_ID, FIXTURE_DATASOURCE_ID } = await import("@/shared/mock/review-fixture");
    const { ADMIN_FIXTURE_RULE_SET_ID, ADMIN_FIXTURE_USER_MEMBER_ID } = await import("@/shared/mock/admin-fixture");
    baseFlowId = FIXTURE_FLOW_ID;
    baseDatasourceId = FIXTURE_DATASOURCE_ID;
    baseRuleSetId = ADMIN_FIXTURE_RULE_SET_ID;
    actorUserId = ADMIN_FIXTURE_USER_MEMBER_ID;
    return;
  }
  const { listFlows, listRuleSets, listDatasources, listUsers } =
    await import("@/api/generated/client/administration/administration");
  const flows = asView<{ items?: Array<{ id: string; name: string }> }>(await listFlows());
  const ruleSets = asView<{ items?: Array<{ id: string; name: string }> }>(await listRuleSets());
  const datasources = asView<{ items?: Array<{ id: string; name: string }> }>(await listDatasources());
  const users = asView<{ items?: Array<{ id: string; username: string }> }>(await listUsers());
  const flow = flows.items?.find((f) => f.name === "f12-change-flow");
  const ruleSet = ruleSets.items?.find((r) => r.name === "f12-rules");
  const ds = datasources.items?.find((d) => d.name === "f12-pg-target");
  const user = users.items?.find((u) => u.username === "f12-submitter");
  if (!flow || !ruleSet || !ds || !user) throw new Error("run scripts/integration/f12_seed.py seed first");
  baseFlowId = flow.id;
  baseDatasourceId = ds.id;
  baseRuleSetId = ruleSet.id;
  actorUserId = user.id;
});

describe("flow lifecycle + masking rules", () => {
  it("list → create → get → replace → masking rule → list masking → delete", async () => {
    await record("listFlows", "admin-face", () => listFlows());
    const flowBody = {
      name: `f12-flow-${suffix}`, flow_type: "change_review" as const, enabled: false,
      rule_set_id: baseRuleSetId,
      stages: [{
        position: 1, datasource_id: baseDatasourceId,
        approval_steps: [{ position: 1, actors: [{ user_id: actorUserId }] }],
        execution_actors: [{ user_id: actorUserId }],
      }],
    };
    const created = await record("createFlow", "scratch-create", () =>
      createFlow(flowBody, { headers: idem() }));
    const flow = asView<IdVersion | undefined>(created.value);
    if (!flow?.id) throw new Error(`flow not created: ${created.errCode} ${created.status}`);
    await record("getFlow", "scratch-face", () => getFlow(flow.id));
    await record("replaceFlow", "scratch-enable", () =>
      replaceFlow(flow.id, { ...flowBody, enabled: true }, { headers: ifMatch(flow.version) }));
    await record("replaceFlowMaskingRule", "scratch-rule", () =>
      replaceFlowMaskingRule(flow.id, baseDatasourceId, { sensitive_columns: ["email", "phone_number"] },
        { headers: ifMatch(1) }));
    await record("listFlowMaskingRules", "scratch-face", () => listFlowMaskingRules(flow.id));
    const flowFresh = asView<IdVersion | undefined>(await getFlow(flow.id));
    await record("deleteFlow", "scratch-cleanup", () =>
      deleteFlow(flow.id, { headers: ifMatch(flowFresh?.version) }));
    void baseFlowId;
  });
});

describe("legacy migration faces", () => {
  it("mock: run to approval; real: no run (offline-command domain)", async () => {
    if (mode === "mock") {
      const { ADMIN_FIXTURE_MIGRATION_RUN_ID } = await import("@/shared/mock/admin-fixture");
      const runResp = await record("getLegacyMigrationRun", "fixture-face", () =>
        getLegacyMigrationRun(ADMIN_FIXTURE_MIGRATION_RUN_ID),
        "mock fixture run; the real stack has no run (offline-command domain, see no-run cases)");
      const runView = asView<{
        version?: number; manifest_hash?: string | null;
        candidates?: Array<{ candidate_id: string; target_definition_hash: string; confirmed: boolean }>;
      }>(runResp.value);
      const runId = ADMIN_FIXTURE_MIGRATION_RUN_ID;
      if (runView?.candidates && runView.candidates.length > 0) {
        let version = runView.version ?? 1;
        for (const candidate of runView.candidates) {
          const confirmed = await record("confirmLegacyMigrationCandidate", `confirm-${candidate.candidate_id.slice(-4)}`, () =>
            confirmLegacyMigrationCandidate(runId, candidate.candidate_id,
              { confirmed: true, target_definition_hash: candidate.target_definition_hash },
              { headers: ifMatch(version) }),
            "mock fixture run confirmation; the real stack has no run (offline-command domain)");
          version = (asView<IdVersion | undefined>(confirmed.value)?.version ?? version + 1);
        }
        await record("approveLegacyMigrationRun", "fixture-approve", () =>
          approveLegacyMigrationRun(runId,
            { manifest_hash: runView.manifest_hash ?? "", confirmation_phrase: `APPROVE ${runId}` },
            { headers: ifMatch(version) }),
          "mock fixture run approval; the real stack has no run (offline-command domain)");
      }
    } else {
      // The serve surface has no migration run (runs come from the offline
      // `yearning migrate` command); the cases record the declared error
      // faces against a random run id.
      const missing = crypto.randomUUID();
      await record("getLegacyMigrationRun", "no-run", () => getLegacyMigrationRun(missing),
        "real stack has no migration run: offline-command domain");
      await record("confirmLegacyMigrationCandidate", "no-run", () =>
        confirmLegacyMigrationCandidate(missing, crypto.randomUUID(),
          { confirmed: true, target_definition_hash: `sha256:${"0".repeat(64)}` },
          { headers: ifMatch(1) }), "real stack has no migration run");
      await record("approveLegacyMigrationRun", "no-run", () =>
        approveLegacyMigrationRun(missing, { manifest_hash: `sha256:${"0".repeat(64)}`, confirmation_phrase: "APPROVE x" },
          { headers: ifMatch(1) }), "real stack has no migration run");
    }
  });
});

describe("settings (branding namespace, write-back of current values)", () => {
  it("schema → get → assess → replace → revisions", async () => {
    await record("getSettingsSchema", "branding", () => getSettingsSchema("branding"));
    const current = await record("getSettingsNamespace", "branding", () => getSettingsNamespace("branding"));
    const view = asView<{ version?: number; settings?: Record<string, unknown> } | undefined>(current.value);
    const settings = (view?.settings ?? {}) as never;
    await record("assessSettingsImpact", "branding-noop", () =>
      assessSettingsImpact("branding", { settings }, { headers: idem() }));
    await record("replaceSettingsNamespace", "branding-writeback", () =>
      replaceSettingsNamespace("branding", { settings }, { headers: ifMatch(view?.version) }));
    await record("listSettingsRevisions", "branding-face", () => listSettingsRevisions("branding"));
  });
});

describe("notification channels", () => {
  it("create → get → replace → test delivery → deliveries → delete", async () => {
    await record("listNotificationChannels", "admin-face", () => listNotificationChannels());
    const channelBody = {
      kind: "email" as const, name: `f12-channel-${suffix}`, enabled: false,
      configuration: {
        host: "smtp.f12.invalid", port: 587, tls_mode: "starttls" as const,
        username: "f12", from_address: "f12@example.invalid",
      },
    };
    const created = await record("createNotificationChannel", "scratch-create", () =>
      createNotificationChannel({ ...channelBody, secret: { value: "f12-channel-secret" } as never },
        { headers: idem() }));
    const channel = asView<IdVersion | undefined>(created.value);
    if (!channel?.id) throw new Error(`channel not created: ${created.errCode} ${created.status}`);
    await record("getNotificationChannel", "scratch-face", () => getNotificationChannel(channel.id));
    await record("replaceNotificationChannel", "scratch-enable", () =>
      replaceNotificationChannel(channel.id, { ...channelBody, enabled: true }, { headers: ifMatch(channel.version) }));
    await record("createNotificationTestDelivery", "scratch-unroutable", () =>
      createNotificationTestDelivery(channel.id, { recipient: "f12@example.invalid", locale: "zh-CN" },
        { headers: idem() }),
      "serve starts no notification worker (BLK-20261017-SERVE-BACKGROUND-WORKERS)");
    await record("listNotificationDeliveries", "admin-face", () => listNotificationDeliveries());
    const channelFresh = asView<IdVersion | undefined>(await
      getNotificationChannel(channel.id));
    await record("deleteNotificationChannel", "scratch-cleanup", () =>
      deleteNotificationChannel(channel.id, { headers: ifMatch(channelFresh?.version) }));
  });
});

describe("announcements", () => {
  it("create revision → list → publish", async () => {
    const created = await record("createAnnouncementRevision", "scratch-create", () =>
      createAnnouncementRevision({
        title: `F12 contract ${suffix}`,
        markdown_source: "# F12 contract announcement\nScratch revision from the contract suite.\n",
      }, { headers: idem() }));
    const revision = asView<IdVersion | undefined>(created.value);
    if (!revision?.id) throw new Error(`announcement revision not created: ${created.errCode} ${created.status}`);
    await record("listAnnouncementRevisions", "admin-face", () => listAnnouncementRevisions());
    // The publish If-Match pins the PUBLICATION POINTER version (the singleton
    // replaced by publishing — same semantics in the mock and the backend),
    // not the revision's version; 1 for the first-ever publish.
    const current = asView<{ version?: number } | null>(await getCurrentAnnouncement());
    const publicationVersion = current?.version ?? 1;
    await record("publishAnnouncementRevision", "scratch-publish", () =>
      publishAnnouncementRevision({ announcement_revision_id: revision.id },
        { headers: ifMatch(publicationVersion) }),
      "If-Match carries the publication pointer version (getCurrentAnnouncement.version), not the revision version");
  });
});

describe("dashboards and audit feed", () => {
  it("system health + audit events", async () => {
    await record("getSystemHealthDashboard", "admin-face", () => getSystemHealthDashboard());
    await record("listAuditEvents", "admin-face", () => listAuditEvents({ limit: 20 }));
  });
});

describe("submitter list faces", () => {
  it("my drafts and my orders", async () => {
    await loginAs("submitter");
    await record("listChangeDrafts", "submitter-face", () => listChangeDrafts({ limit: 20 }));
    await record("listChangeOrders", "submitter-face", () => listChangeOrders({ limit: 20 }),
      "real execution_actors snapshot omits is_builtin_admin/version (divergence #006)");
    await loginAs("admin");
  });
});

describe("review evidence family", () => {
  it("scratch run → findings → evidence → reveal → copy", async () => {
    // Compact chain: draft → SQL → review run to terminal state. The findings
    // live on the run; no submission is needed.
    await loginAs("submitter");
    const created = await record("createChangeDraft", "evidence-chain", () =>
      createChangeDraft({ flow_id: baseFlowId, title: `f12 evidence ${suffix}`, description: "batch4 evidence chain" },
        { headers: idem() }));
    const draft = asView<IdVersion | undefined>(created.value);
    if (!draft?.id) throw new Error(`draft not created: ${created.errCode} ${created.status}`);
    const evidenceSql = "create table if not exists f12_contract_b4 (id bigint primary key);";
    await record("replaceDraftSql", "evidence-chain", () =>
      replaceDraftSql(draft.id, { sql: evidenceSql },
        { headers: ifMatch(draftETag(draft), true) }));
    // RCP-20261101 ephemeral precheck: request body carries current SQL verbatim for review, response directly returns run_id
    // (no longer going through a task row), polling GET /review-runs/{id} until terminal state.
    const started = await record("runDraftReview", "evidence-chain", () =>
      runDraftReview({ flow_id: baseFlowId, draft_id: draft.id, sql: evidenceSql },
        { headers: idem() }),
      mode === "real" ? "stub-backed review (low-severity finding)" : undefined);
    const startView = asView<{ run_id?: string; result_ref?: string | null } | undefined>(started.value);
    const match = /(?:sql_grouping|review-runs)\/([0-9a-f-]{36})/i.exec(startView?.result_ref ?? "");
    const runId = startView?.run_id ?? match?.[1] ?? null;
    expect(runId, `review start carries the run id (${started.errCode} ${started.status})`).not.toBeNull();
    const deadline = Date.now() + 120_000;
    let run = asView<{ state?: string } | undefined>(await getReviewRun(runId!));
    while (run?.state !== "ready" && run?.state !== "failed" && run?.state !== "blocked"
      && run?.state !== "partial" && run?.state !== "timeout" && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 800));
      run = asView<{ state?: string } | undefined>(await getReviewRun(runId!));
    }
    const findings = await record("listReviewRunFindings", "evidence-chain", () => listReviewRunFindings(runId!));
    const findingList = asView<{ items?: Array<{ id: string }> } | undefined>(findings.value);
    const findingId = findingList?.items?.[0]?.id ?? null;
    expect(findingId, "review run produced at least one finding").not.toBeNull();
    const evidence = await record("listReviewFindingEvidence", "evidence-chain", () =>
      listReviewFindingEvidence(findingId!));
    const evidenceList = asView<Array<{ id: string }> | { items?: Array<{ id: string }> } | undefined>(evidence.value);
    const evidenceIds = Array.isArray(evidenceList) ? evidenceList : evidenceList?.items ?? [];
    if (evidenceIds.length > 0) {
      const evidenceId = evidenceIds[0]!.id;
      const revealed = await record("revealRawReviewEvidence", "evidence-chain", () =>
        revealRawReviewEvidence(evidenceId, { purpose: "FE-F12 contract verification" }, { headers: idem() }));
      const revealView = asView<{ reveal_id?: string } | undefined>(revealed.value);
      if (revealView?.reveal_id) {
        await record("recordRawReviewEvidenceCopy", "evidence-chain", () =>
          recordRawReviewEvidenceCopy(evidenceId, { source_reveal_id: revealView.reveal_id! }, { headers: idem() }));
      }
    } else {
      // Real stub findings carry no evidence rows (verified by probe) and
      // the reveal of a synthetic id panics (nil deref, divergence #012):
      // the reveal/copy faces are exercised through their error paths.
      const synthetic = crypto.randomUUID();
      const revealed = await record("revealRawReviewEvidence", "no-evidence-rows", () =>
        revealRawReviewEvidence(synthetic, { purpose: "FE-F12 contract verification" }, { headers: idem() }),
        "finding carries no evidence rows on the real chain (stub finding); synthetic reveal 500s (divergence #012)");
      const revealView = asView<{ reveal_id?: string } | undefined>(revealed.value);
      if (revealView?.reveal_id) {
        await record("recordRawReviewEvidenceCopy", "evidence-chain", () =>
          recordRawReviewEvidenceCopy(synthetic, { source_reveal_id: revealView.reveal_id! }, { headers: idem() }));
      } else {
        await record("recordRawReviewEvidenceCopy", "no-reveal", () =>
          recordRawReviewEvidenceCopy(synthetic, { source_reveal_id: crypto.randomUUID() }, { headers: idem() }),
          "copy against a synthetic reveal id; the real finding carries no evidence rows");
      }
    }
    await loginAs("admin");
  });
});
