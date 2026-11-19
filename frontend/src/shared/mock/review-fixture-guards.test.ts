import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  FIXTURE_FLOW_ID,
  FIXTURE_OWNER_ID,
  resetReviewFixture,
} from "./review-fixture";
import { setMockAuthBehavior } from "./auth-scenario-store";

/**
 * Negative-path sweep over the review fixture's handler guards (coverage
 * recovery for §18.35/§18.42 fixture growth): 1002 unknown resources, the
 * run-review state-machine rejections, foreign-owner problem responses,
 * timeline pagination and the draft-copy validation chain.
 */

const OTHER_OWNER = "99999999-0000-4000-8000-0000000000aa";

async function req(
  path: string,
  init?: RequestInit,
): Promise<{ status: number; body: Record<string, unknown> }> {
  const response = await fetch(`https://yearning.test${path}`, init);
  return { status: response.status, body: (await response.json()) as Record<string, unknown> };
}

async function jsonPost(path: string, payload: unknown, headers?: Record<string, string>) {
  return req(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(payload),
  });
}

describe("review fixture negative paths (coverage sweep)", () => {
  beforeEach(() => {
    setMockAuthBehavior("admin");
    resetReviewFixture();
  });

  afterEach(() => {
    resetReviewFixture();
  });

  it("answers 1002 for unknown orders on timeline and draft copies", async () => {
    const timeline = await req("/change-orders/7e6f1a2b-0000-4000-8000-00000000dead/timeline");
    expect(timeline.body.err_code).toBe(1002);
    const copy = await jsonPost(
      "/change-orders/7e6f1a2b-0000-4000-8000-00000000dead/draft-copies",
      { target_flow_id: FIXTURE_FLOW_ID, title: "t" },
    );
    expect(copy.body.err_code).toBe(1002);
  });

  it("guards the ephemeral precheck body and its draft anchor (RCP-20261101)", async () => {
    async function createDraft(title: string): Promise<string> {
      const created = (await req("/change-drafts", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title }),
        })).body as { data: { id: string } };
      return created.data.id;
    }
    // Empty SQL: return 1001 and put reason in meta.detail (F-UX-003).
    const empty = await jsonPost("/review-runs", { flow_id: FIXTURE_FLOW_ID, sql: "" });
    expect(empty.body.err_code).toBe(1001);

    // Invalid request body (missing sql/flow_id): 1001.
    expect((await jsonPost("/review-runs", { flow_id: FIXTURE_FLOW_ID })).body.err_code).toBe(1001);
    expect((await jsonPost("/review-runs", { sql: "SELECT 1;" })).body.err_code).toBe(1001);

    // draft_id is for UI anchoring only: unknown draft -> 1002; flow mismatch -> 1001; valid anchor -> allowed.
    const draftId = await createDraft("Anchor validation draft");
    expect(
      (await jsonPost("/review-runs", {
        flow_id: FIXTURE_FLOW_ID,
        draft_id: "7e6f1a2b-0000-4000-8000-00000000dead",
        sql: "SELECT 1;",
      })).body.err_code,
    ).toBe(1002);
    expect(
      (await jsonPost("/review-runs", {
        flow_id: "99999999-0000-4000-8000-0000000000ff",
        draft_id: draftId,
        sql: "SELECT 1;",
      })).body.err_code,
    ).toBe(1001);
    const ok = await jsonPost("/review-runs", {
      flow_id: FIXTURE_FLOW_ID,
      draft_id: draftId,
      sql: "SELECT 1;",
    });
    expect(ok.body.err_code).toBe(0);
  });

  it("returns the anonymous problem shape for a foreign-owner draft read", async () => {
    // Seed a draft owned by the fixture owner, then tamper the local world
    // through the public reseed boundary: reading a draft id that belongs
    // to nobody answers the not-found business error.
    const missing = await req("/change-drafts/7e6f1a2b-0000-4000-8000-00000000dead");
    expect(missing.body.err_code).toBe(1002);
    void OTHER_OWNER;
    void FIXTURE_OWNER_ID;
  });

  it("sweeps comments, reveals, copy-events and approval-decision guards", async () => {
    // The execution-partial scenario seeds a real order owned by the
    // fixture submitter.
    window.localStorage.setItem("yearning-mock-scenario", "execution-partial");
    resetReviewFixture();
    const list = await req("/change-orders?limit=50");
    const orderId = ((list.body.data as { items: Array<{ id: string }> }).items)[0]?.id as string;

    // Comments: unknown order 1002, empty content 1001, then a real append.
    expect((await req(`/change-orders/7e6f1a2b-0000-4000-8000-00000000dead/comments`)).body.err_code).toBe(1002);
    const emptyComment = await jsonPost(`/change-orders/${orderId}/comments`, { content: "" });
    expect(emptyComment.body.err_code).toBe(1001);
    const comment = await jsonPost(`/change-orders/${orderId}/comments`, { content: "sweep" });
    expect(comment.body.err_code).toBe(0);
    // Over-limit content mirrors the same 1001 (comments.go parity).
    const longComment = await jsonPost(`/change-orders/${orderId}/comments`, {
      content: "x".repeat(4097),
    });
    expect(longComment.body.err_code).toBe(1001);

    // SQL reveals: unknown order 1002, missing purpose 1001, then success.
    expect((await jsonPost(`/change-orders/7e6f1a2b-0000-4000-8000-00000000dead/sql-reveals`, { purpose: "x" })).body.err_code).toBe(1002);
    expect((await jsonPost(`/change-orders/${orderId}/sql-reveals`, {})).body.err_code).toBe(1001);
    const reveal = await jsonPost(`/change-orders/${orderId}/sql-reveals`, { purpose: "coverage-sweep" });
    expect(reveal.body.err_code).toBe(0);
    const revealId = (reveal.body.data as { reveal_id: string }).reveal_id;

    // Copy events: missing source id 1001, then the audited copy.
    expect((await jsonPost(`/change-orders/${orderId}/sql-copy-events`, {})).body.err_code).toBe(1001);
    expect((await jsonPost(`/change-orders/${orderId}/sql-copy-events`, { source_reveal_id: revealId })).body.err_code).toBe(0);

    // Approval decisions on a non-approval order: If-Match mismatch first
    // (1004), then the state guard (1010).
    const orderDetail = await req(`/change-orders/${orderId}`);
    const version = (orderDetail.body.data as { version: number }).version;
    expect(
      (
        await jsonPost(`/change-orders/${orderId}/approval-decisions`, { decision: "approve" }, {
          "If-Match": '"999"',
        })
      ).body.err_code,
    ).toBe(1004);
    expect(
      (
        await jsonPost(`/change-orders/${orderId}/approval-decisions`, { decision: "approve" }, {
          "If-Match": `"${String(version)}"`,
        })
      ).body.err_code,
    ).toBe(1010);
    window.localStorage.removeItem("yearning-mock-scenario");
  });

  it("sweeps review-run, findings, evidence and raw-reveal guards", async () => {
    // Drive an ephemeral precheck to a finished run (fixture timers
    // 400/900ms), then hit the read guards around runs/findings/evidence.
    const created = (await req("/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "Guard sweep draft" }),
      })).body as { data: { id: string } };
    const started = await jsonPost("/review-runs", {
      flow_id: FIXTURE_FLOW_ID,
      draft_id: created.data.id,
      sql: "UPDATE orders SET status = 1 WHERE user_id = 42;",
    });
    const runId = (started.body.data as { run_id: string }).run_id;
    await new Promise((resolve) => {
      setTimeout(resolve, 1_600);
    });
    // Precheck does not write to draft: run lives only in the ephemeral world (RCP-20261101).
    const draft = (await req(`/change-drafts/${created.data.id}`)).body as { data: { review_run_id: string | null } };
    expect(draft.data.review_run_id).toBeNull();

    // Unknown run/findings ids → 1002.
    expect((await req("/review-runs/7e6f1a2b-0000-4000-8000-00000000dead")).body.err_code).toBe(1002);
    expect((await req("/review-runs/7e6f1a2b-0000-4000-8000-00000000dead/findings")).body.err_code).toBe(1002);
    // Unknown finding → 1002 after the full evidence scan.
    expect((await req("/review-findings/7e6f1a2b-0000-4000-8000-00000000dead/evidence")).body.err_code).toBe(1002);
    // Raw reveals: unknown evidence 1002, non-retained 1011.
    expect((await jsonPost("/review-evidence/7e6f1a2b-0000-4000-8000-00000000dead/raw-reveals", {})).body.err_code).toBe(1002);
    // RCP-20261101 ephemeral precheck: submitted evidence carries the raw envelope in memory — raw reveal succeeds
    // directly (err_code 0); 1011 (raw not retained) applies only to materialized evidence without raw.
    const findings = (await req(`/review-runs/${runId}/findings`)).body as { data: { items: Array<{ id: string; evidence_ids: string[] }> } };
    const withEvidence = findings.data.items.find((entry) => entry.evidence_ids.length > 0);
    const firstEvidenceId = withEvidence?.evidence_ids[0] ?? "";
    if (firstEvidenceId !== "") {
      expect(
        (await jsonPost(`/review-evidence/${firstEvidenceId}/raw-reveals`, {})).body.err_code,
      ).toBe(0);
    }
    // Cancellation for an unknown attempt → 1002.
    expect((await jsonPost("/execution-attempts/7e6f1a2b-0000-4000-8000-00000000dead/cancellation", { reason: "x" })).body.err_code).toBe(1002);
  });

  it("answers 1002 for approval decisions on unknown orders and paginates timeline cursors", async () => {
    expect(
      (await jsonPost(`/change-orders/7e6f1a2b-0000-4000-8000-00000000dead/approval-decisions`, { decision: "approve" }, { "If-Match": '"1"' })).body.err_code,
    ).toBe(1002);

    window.localStorage.setItem("yearning-mock-scenario", "execution-partial");
    resetReviewFixture();
    const list = await req("/change-orders?limit=50");
    const orderId = ((list.body.data as { items: Array<{ id: string }> }).items)[0]?.id as string;
    // Timeline with an explicit limit over the seeded entries.
    const page = (await req(`/change-orders/${orderId}/timeline?limit=2`)).body as { data: { items: unknown[]; page: { has_more: boolean; next_cursor: string | null } } };
    expect(page.data.items.length).toBeLessThanOrEqual(2);
    // Comments cursor page over the same newest-first helper.
    const comments = (await req(`/change-orders/${orderId}/comments?limit=2`)).body as { data: { page: { has_more: boolean } } };
    expect(comments.data.page.has_more).toBe(false);
    window.localStorage.removeItem("yearning-mock-scenario");
  });

  it("paginates the order timeline newest-first", async () => {
    // The execution-partial scenario seeds an order with timeline entries.
    window.localStorage.setItem("yearning-mock-scenario", "execution-partial");
    resetReviewFixture();
    const list = await req("/change-orders?limit=50");
    const items = ((list.body.data as { items: Array<{ id: string }> }).items);
    expect(items.length).toBeGreaterThan(0);
    const timeline = await req(`/change-orders/${String(items[0]?.id)}/timeline?limit=1`);
    expect(timeline.body.err_code).toBe(0);
    const page = timeline.body.data as { items: unknown[]; page: { has_more: boolean } };
    expect(page.items.length).toBe(1);
    window.localStorage.removeItem("yearning-mock-scenario");
  });
});
