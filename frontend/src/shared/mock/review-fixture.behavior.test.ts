import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  resetReviewFixture,
} from "./review-fixture";
import { setMockAuthBehavior } from "./auth-scenario-store";
import {
  readStoredScenario,
  useMockScenario,
  type MockScenario,
} from "./scenario-store";
import { createMockEventTransport } from "./review-fixture";

/**
 * Behavior-matrix tests for the stateful fixture: every review scenario
 * outcome (Ready/Blocked/Partial/Provider failure) produces the exact run
 * shape, gate reasons and failure codes the acceptance-gate E2E asserts
 * on, and the mock event transport replays the outbox with per-subject
 * sequence semantics (at-least-once with resume points).
 */

const FIXTURE_FLOW_ID = "4f6f1a2b-0000-4000-8000-000000000001";

const scenarioByBehavior: Record<string, MockScenario> = {
  ready: "review-ready",
  blocked: "review-blocked",
  partial: "review-partial",
  provider_failed: "review-provider-failed",
};

/** RCP-20261101 ephemeral precheck: POST /review-runs sends current SQL, response directly returns run_id. */
async function startPrecheck(flowId: string, draftId: string, sql: string): Promise<string> {
  const run = (await (
    await fetch("https://yearning.test/review-runs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: flowId, draft_id: draftId, sql }),
    })
  ).json()) as { data: { run_id: string } };
  return run.data.run_id;
}

async function createAndRunToTerminal(): Promise<{ draftId: string; runId: string }> {
  const create = (await (
    await fetch("https://yearning.test/change-drafts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "Behavior matrix" }),
    })
  ).json()) as { data: { id: string } };
  const draftId = create.data.id;
  await fetch(`https://yearning.test/change-drafts/${draftId}/sql`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sql: "SELECT 1;" }),
  });
  const runId = await startPrecheck(FIXTURE_FLOW_ID, draftId, "SELECT 1;");
  // Fixture timeline: queued → (400ms) → running → (900ms) → terminal.
  await new Promise((resolve) => {
    setTimeout(resolve, 1_600);
  });
  return { draftId, runId };
}

describe("review fixture behavior matrix", () => {
  beforeEach(() => {
    setMockAuthBehavior("admin");
    resetReviewFixture();
  });
  afterEach(() => {
    resetReviewFixture();
    useMockScenario.getState().setScenario("ready");
  });

  for (const [behavior, scenario] of Object.entries(scenarioByBehavior)) {
    it(`produces the ${behavior} run shape under scenario ${scenario}`, async () => {
      useMockScenario.getState().setScenario(scenario);
      expect(readStoredScenario()).toBe(scenario);
      const { draftId, runId } = await createAndRunToTerminal();
      expect(runId).not.toBe("");

      const run = (await (
        await fetch(`https://yearning.test/review-runs/${runId}`)
      ).json()) as {
        data: {
          state: string;
          gate: { passed: boolean; reason_codes: string[] };
          failure_code: string | null;
        } | null;
      };
      expect(run.data).not.toBeNull();

      if (behavior === "ready") {
        expect(run.data?.state).toBe("ready");
        expect(run.data?.gate.passed).toBe(true);
        expect(run.data?.gate.reason_codes).toEqual([]);
        expect(run.data?.failure_code).toBeNull();
      } else if (behavior === "blocked") {
        expect(run.data?.state).toBe("blocked");
        expect(run.data?.gate.reason_codes).toContain("stage_review_blocked");
        expect(run.data?.gate.reason_codes).toContain("critical_severity_finding");
      } else if (behavior === "partial") {
        expect(run.data?.state).toBe("partial");
        expect(run.data?.gate.reason_codes).toEqual(["stage_review_incomplete"]);
        expect(run.data?.failure_code).toBe("budget_exhausted");
      } else {
        expect(run.data?.state).toBe("failed");
        expect(run.data?.failure_code).toBe("provider_unavailable");
      }

      // Precheck does not write to draft (RCP-20261101): regardless of run terminal state, draft row stays in draft.
      const draft = (await (
        await fetch(`https://yearning.test/change-drafts/${draftId}`)
      ).json()) as { data: { state: string } };
      expect(draft.data.state).toBe("draft");
    });
  }

  it("replays outbox events through the mock transport with resume semantics", async () => { // Two-stage 1.6s timer + outbox drain — relax timeout for long test case
    useMockScenario.getState().setScenario("review-ready");
    const { draftId, runId } = await createAndRunToTerminal();
    // Ephemeral runs do not emit domain events (explicitly stated in backend ephemeralFinish, RCP-20261101) —
    // persistent domain event change_order.submitted is emitted only after submission materializes the order.
    const first = (await (
      await fetch("https://yearning.test/change-orders", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, draft_id: draftId, sql: "SELECT 1;", run_id: runId }),
      })
    ).json()) as { err_code: number; data: { id: string } | null };
    if (first.err_code !== 0 || first.data === null) throw new Error(`submission failed: ${JSON.stringify(first)}`);
    // A second draftless submission emits one more event on another subject.
    const secondRunId = await startPrecheck(FIXTURE_FLOW_ID, "", "SELECT 2;");
    await new Promise((resolve) => {
      setTimeout(resolve, 1_600);
    });
    const second = (await (
      await fetch("https://yearning.test/change-orders", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          flow_id: FIXTURE_FLOW_ID,
          sql: "SELECT 2;",
          run_id: secondRunId,
          metadata: { title: "Behavior matrix direct submit" },
        }),
      })
    ).json()) as { err_code: number; data: { id: string } | null };
    if (second.err_code !== 0 || second.data === null) throw new Error(`draftless submission failed: ${JSON.stringify(second)}`);

    const received: Array<{ subject: string; sequence: number }> = [];
    const transport = createMockEventTransport({});
    for await (const raw of transport) {
      const event = raw as { subject: string; sequence: number };
      received.push({ subject: event.subject, sequence: event.sequence });
      // One submitted event per order makes the full sweep stop point.
      if (received.filter((entry) => entry.subject.startsWith("change-orders/")).length >= 2) break;
    }
    const subjects = received.map((entry) => entry.subject);
    expect(subjects).toContain(`change-orders/${first.data.id}`);
    expect(subjects).toContain(`change-orders/${second.data.id}`);
    // A fresh transport seeded with a resume cursor re-feeds only later
    // events for that subject — the reconnect semantics the client relies on.
    const subject = `change-orders/${first.data.id}`;
    const sequence = received.find((entry) => entry.subject === subject)?.sequence ?? 1;
    // Under the ephemeral flow each order subject has only one event: there may be no further events after the cursor,
    // and the transport does not yield on empty polls so for-await would hang inside the generator — race
    // next() against sleep to drain, exiting after the time cap.
    const replayAtCursor = createMockEventTransport({ [subject]: sequence });
    const cursorIterator = replayAtCursor[Symbol.asyncIterator]();
    const atCursor: unknown[] = [];
    const replayDeadline = Date.now() + 400;
    while (Date.now() < replayDeadline) {
      const next = await Promise.race([
        cursorIterator.next(),
        new Promise<null>((resolve) => { setTimeout(() => { resolve(null); }, 80); }),
      ]);
      if (next === null) continue;
      if (next.done) break;
      const event = next.value as { subject: string };
      if (event.subject === subject) atCursor.push(event);
      if (atCursor.length > 10) break;
    }
    // Nothing at or before the resume cursor is re-fed.
    expect(atCursor).toHaveLength(0);
  }, 15_000);
});
