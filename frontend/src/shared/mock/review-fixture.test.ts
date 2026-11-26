import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  FIXTURE_FLOW_ID,
  FIXTURE_RAW_EVIDENCE_MARKER,
  resetReviewFixture,
} from "./review-fixture";
import { setMockAuthBehavior } from "./auth-scenario-store";

/**
 * The fixture owns GET /users/me/flows for the mock layer: the zero-
 * permission contract (auth PRD §11) derives from the auth behavior, and a
 * granted session sees the change flow for the precheck workspace. The
 * lifecycle tests pin the contract semantics the E2E relies on — revision
 * increments, run progression to a terminal state, the submission gate
 * failure code and the audited/watermarked raw-reveal envelope.
 *
 * The shared global MSW server (setup.ts) already registers the fixture via
 * baseHandlers; a second in-file server would double-intercept requests.
 */

afterEach(() => {
  resetReviewFixture();
});

interface FixtureDraftView {
  id: string;
  revision: number;
  version: number;
  state: string;
  has_sql: boolean;
}

async function createDraft(): Promise<FixtureDraftView> {
  const response = await fetch("https://yearning.test/api/v4/change-drafts", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "Fixture draft" }),
  });
  return ((await response.json()) as { data: FixtureDraftView }).data;
}

async function saveSql(draftId: string, sql: string): Promise<FixtureDraftView> {
  const response = await fetch(`https://yearning.test/api/v4/change-drafts/${draftId}/sql`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sql }),
  });
  return ((await response.json()) as { data: FixtureDraftView }).data;
}

async function startPrecheck(body: {
  flow_id: string;
  draft_id?: string;
  sql: string;
}): Promise<string> {
  // RCP-20261101 ephemeral precheck: POST /review-runs reviews request-body SQL verbatim and directly returns
  // run_id (no longer going through a task row).
  const response = await fetch("https://yearning.test/api/v4/review-runs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const parsed = (await response.json()) as {
    err_code: number;
    data: { run_id?: string } | null;
  };
  if (parsed.err_code !== 0 || parsed.data?.run_id === undefined) {
    throw new Error(`review-runs POST failed: ${JSON.stringify(parsed)}`);
  }
  return parsed.data.run_id;
}

async function runToTerminal(sql: string, draftId?: string): Promise<string> {
  const runId = await startPrecheck(
    draftId === undefined
      ? { flow_id: FIXTURE_FLOW_ID, sql }
      : { flow_id: FIXTURE_FLOW_ID, draft_id: draftId, sql },
  );
  // Fixture timeline: queued → (400ms) → running → (900ms) → terminal.
  // Real timers only: fake timers stall the undici/MSW network stack.
  await new Promise((resolve) => {
    setTimeout(resolve, 1_600);
  });
  return runId;
}

describe("review fixture flow catalog", () => {
  beforeEach(() => {
    setMockAuthBehavior("default");
  });

  it("returns an empty page for the zero-permission default session", async () => {
    const response = await fetch("https://yearning.test/api/v4/users/me/flows?flow_type=change_review");
    const body = (await response.json()) as { data: { items: unknown[]; page: { has_more: boolean } } };
    expect(body.data.items).toEqual([]);
    expect(body.data.page.has_more).toBe(false);
  });

  it("grants the change flow to an admin session", async () => {
    setMockAuthBehavior("admin");
    const response = await fetch("https://yearning.test/api/v4/users/me/flows?flow_type=change_review");
    const body = (await response.json()) as { data: { items: Array<{ id: string; flow_type: string; enabled: boolean }> } };
    // Default e2e flow first, then the 20-flow owner catalog (two catalog
    // entries are disabled and still listed — the wizard filters nothing;
    // the backend list only ever returns enabled flows, the mock mirrors
    // that by listing the disabled rows as enabled=false).
    expect(body.data.items).toHaveLength(21);
    expect(body.data.items[0]?.id).toBe(FIXTURE_FLOW_ID);
    expect(body.data.items.filter((item) => item.enabled)).toHaveLength(19);
  });

  it("returns an empty page for query_access flow type", async () => {
    setMockAuthBehavior("admin");
    const response = await fetch("https://yearning.test/api/v4/users/me/flows?flow_type=query_access");
    const body = (await response.json()) as { data: { items: unknown[] } };
    expect(body.data.items).toEqual([]);
  });
});

describe("review fixture draft and run lifecycle", () => {
  beforeEach(() => {
    setMockAuthBehavior("admin");
  });

  it("creates a draft and increments the revision on each SQL save", async () => {
    const draft = await createDraft();
    expect(draft.state).toBe("draft");
    expect(draft.revision).toBe(1);
    const saved = await saveSql(draft.id, "SELECT 1;");
    expect(saved.revision).toBe(2);
    expect(saved.has_sql).toBe(true);
  });

  it("states the empty-SQL cause in meta.detail when prechecking without SQL (F-UX-003)", async () => {
    const response = await fetch("https://yearning.test/api/v4/review-runs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, sql: "" }),
    });
    const body = (await response.json()) as { err_code: number; meta?: { detail?: string } };
    expect(body.err_code).toBe(1001);
    expect(body.meta?.detail).toContain("SQL\u4e3a\u7a7a");
  });

  it("advances the ephemeral run to ready without touching the draft (RCP-20261101)", async () => {
    const draft = await createDraft();
    const sql = "SELECT 1;";
    await saveSql(draft.id, sql);
    const runId = await runToTerminal(sql, draft.id);

    const finished = (await (
      await fetch(`https://yearning.test/api/v4/review-runs/${runId}`)
    ).json()) as { data: { state: string; gate: { passed: boolean } } };
    expect(finished.data.state).toBe("ready");
    expect(finished.data.gate.passed).toBe(true);

    // Precheck never writes to the draft: row version, state, and review_run_id all stay untouched (staleness is
    // a local client comparison; the server only compares SQL verbatim at submission time).
    const detail = (await (
      await fetch(`https://yearning.test/api/v4/change-drafts/${draft.id}`)
    ).json()) as { data: { state: string; version: number; review_run_id: string | null } };
    expect(detail.data.state).toBe("draft");
    expect(detail.data.version).toBe(draft.version + 1); // Advanced only by saveSql itself
    expect(detail.data.review_run_id).toBeNull();
  });

  it("rejects submission with REVIEW_OUTDATED when the precheck run is gone", async () => {
    const draft = await createDraft();
    await saveSql(draft.id, "DROP TABLE orders;");
    const response = await fetch("https://yearning.test/api/v4/change-orders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        flow_id: FIXTURE_FLOW_ID,
        draft_id: draft.id,
        sql: "DROP TABLE orders;",
        run_id: "7e6f1a2b-0000-4000-8000-00000000dead",
      }),
    });
    const body = (await response.json()) as { err_code: number; meta?: { detail?: string } };
    // Backend ErrReviewOutdated: Run disappeared (TTL expired / restart) -> 2001, with meta prompting a fresh precheck.
    expect(body.err_code).toBe(2001);
    expect(body.meta?.detail).toContain("\u91cd\u65b0\u9884\u5ba1");
  });

  it("succeeds submission for a ready precheck and freezes the order", async () => {
    const draft = await createDraft();
    const sql = "SELECT 1;";
    await saveSql(draft.id, sql);
    const runId = await runToTerminal(sql, draft.id);
    const response = await fetch("https://yearning.test/api/v4/change-orders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        flow_id: FIXTURE_FLOW_ID,
        draft_id: draft.id,
        sql,
        run_id: runId,
      }),
    });
    const body = (await response.json()) as {
      data: { display_number: string; state: string; snapshot_hash: string } | null;
    };
    // Contract ChangeOrder.display_number: YR-YYYYMMDD-NNNNNN (the real
    // backend emits this; the old UUID-as-number shape failed the generated
    // zod pattern — F12 batch 2).
    expect(body.data?.display_number).toMatch(/^YR-[0-9]{8}-[0-9]{6}$/);
    // Submission freezes the order directly in stage_approval_active with
    // the first approval step active (backend submit.go semantics) — the
    // approval queue fills the moment an order is submitted (FE-F7).
    expect(body.data?.state).toBe("stage_approval_active");
    expect(body.data?.snapshot_hash).not.toBe("");
  });

  it("submits draftless create-mode orders with metadata and rejects a missing title", async () => {
    const sql = "SELECT 1;";
    const runId = await runToTerminal(sql);
    const missing = await fetch("https://yearning.test/api/v4/change-orders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, sql, run_id: runId }),
    });
    expect(((await missing.json()) as { err_code: number }).err_code).toBe(1001);

    // Each submission consumes the Run: submitting again requires a fresh precheck.
    const secondRunId = await runToTerminal(sql);
    const created = await fetch("https://yearning.test/api/v4/change-orders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        flow_id: FIXTURE_FLOW_ID,
        sql,
        run_id: secondRunId,
        metadata: { title: "Draftless direct-submit order" },
      }),
    });
    const body = (await created.json()) as {
      data: { display_number: string; state: string } | null;
    };
    expect(body.data?.display_number).toMatch(/^YR-[0-9]{8}-[0-9]{6}$/);
    expect(body.data?.state).toBe("stage_approval_active");
  });

  it("enforces If-Match optimistic concurrency on SQL and metadata only (RCP-20261101)", async () => {
    const draft = await createDraft();
    await saveSql(draft.id, "SELECT 1;");
    const stale = { "If-Match": '"1"' };
    const put = await fetch(`https://yearning.test/api/v4/change-drafts/${draft.id}/sql`, {
      method: "PUT",
      headers: { "Content-Type": "application/json", ...stale },
      body: JSON.stringify({ sql: "SELECT 2;" }),
    });
    expect(((await put.json()) as { err_code: number }).err_code).toBe(1003);
    // Precheck and submission no longer carry If-Match (ephemeral endpoints have no row version to compare).
  });

  it("enforces If-Match on metadata PATCH against the draft row version (§18.74)", async () => {
    const draft = await createDraft();
    // After create, revision and version coincide; the row version is the
    // only If-Match target once a review run has bumped version alone.
    const stale = await fetch(`https://yearning.test/api/v4/change-drafts/${draft.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json", "If-Match": '"99"' },
      body: JSON.stringify({ title: "Immutable title" }),
    });
    expect(((await stale.json()) as { err_code: number }).err_code).toBe(1003);

    const live = await fetch(`https://yearning.test/api/v4/change-drafts/${draft.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json", "If-Match": `"${String(draft.version)}"` },
      body: JSON.stringify({ title: "Mutable title" }),
    });
    const body = (await live.json()) as { data: { title?: string } | null };
    expect(body.data?.title).toBe("Mutable title");
  });

  it("re-prechecks freely; each run is independent and the draft stays untouched (RCP-20261101)", async () => {
    const draft = await createDraft();
    await saveSql(draft.id, "SELECT 1;");
    const firstRunId = await startPrecheck({
      flow_id: FIXTURE_FLOW_ID,
      draft_id: draft.id,
      sql: "SELECT 1;",
    });
    expect(firstRunId).not.toBe("");
    // wait for terminal: ready (fixture timeline ≈0.4s running + 1.3s terminal)
    await new Promise((resolve) => {
      setTimeout(resolve, 1_800);
    });
    // Re-running precheck is always valid: the new Run exists independently, and the draft carries no run reference (presentation is
    // driven by the client's last precheck).
    const secondRunId = await startPrecheck({
      flow_id: FIXTURE_FLOW_ID,
      draft_id: draft.id,
      sql: "SELECT 1;",
    });
    expect(secondRunId).not.toBe(firstRunId);
    await new Promise((resolve) => {
      setTimeout(resolve, 1_900);
    });
    const detail = (await (
      await fetch(`https://yearning.test/api/v4/change-drafts/${draft.id}`)
    ).json()) as { data: { state: string; review_run_id: string | null } };
    expect(detail.data.state).toBe("draft");
    expect(detail.data.review_run_id).toBeNull();
  });

  it("rejects submission when the SQL differs from the prechecked text (prevent review-A-submit-B)", async () => {
    const draft = await createDraft();
    await saveSql(draft.id, "SELECT 1;");
    const runId = await runToTerminal("SELECT 1;", draft.id);

    const response = await fetch("https://yearning.test/api/v4/change-orders", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        flow_id: FIXTURE_FLOW_ID,
        draft_id: draft.id,
        sql: "DROP TABLE orders;",
        run_id: runId,
      }),
    });
    const body = (await response.json()) as { err_code: number; meta?: { detail?: string } };
    expect(body.err_code).toBe(2001);
    expect(body.meta?.detail).toContain("SQL\u4e0e\u9884\u5ba1\u6587\u672c\u4e0d\u4e00\u81f4");
  });

  it("keeps raw evidence reveals watermarked and copy actions audited", async () => {
    const draft = await createDraft();
    const sql = "SELECT 1;";
    await saveSql(draft.id, sql);
    const runId = await runToTerminal(sql, draft.id);

    const findings = (await (
      await fetch(`https://yearning.test/api/v4/review-runs/${runId}/findings`)
    ).json()) as { data: { items: Array<{ id: string; evidence_ids: string[] }> } };
    const finding = findings.data.items[0];
    if (finding === undefined) {
      throw new Error("the ready run carries no findings");
    }

    const evidence = (await (
      await fetch(`https://yearning.test/api/v4/review-findings/${finding.id}/evidence`)
    ).json()) as {
      data: Array<{ id: string; has_raw_payload: boolean; raw_payload_expires_at: string | null }>;
    };
    const evidenceId = evidence.data[0]?.id;
    expect(evidenceId).toBeDefined();
    expect(evidence.data[0]?.has_raw_payload).toBe(true);
    expect(evidence.data[0]?.raw_payload_expires_at).not.toBeNull();

    const reveal = (await (
      await fetch(`https://yearning.test/api/v4/review-evidence/${evidenceId as string}/raw-reveals`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ purpose: "review-evidence" }),
      })
    ).json()) as { data: { reveal_id: string; watermark: string; raw_payload: { note: string } } };
    expect(reveal.data.raw_payload.note).toContain(FIXTURE_RAW_EVIDENCE_MARKER);
    expect(reveal.data.watermark).toContain("henry");

    const copy = await fetch(
      `https://yearning.test/api/v4/review-evidence/${evidenceId as string}/raw-copy-events`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ source_reveal_id: reveal.data.reveal_id }),
      },
    );
    expect(copy.status).toBe(200);
  });
});
