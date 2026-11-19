import { afterEach, describe, expect, it } from "vitest";
import {
  getReviewEventClient,
  setReviewEventClient,
} from "@/shared/events/review-event-client";
import { resetReviewFixture } from "@/shared/mock/review-fixture";
import { startReviewEvents, stopReviewEvents } from "./review-events";

/**
 * Transport wiring: under the mock flag the shared event client runs on the
 * stateful fixture transport (same ingest/dedup/sequence semantics as the
 * F12 real transport); without the flag it stays idle and consumers rely on
 * HTTP polling. start/stop are idempotent around the page lifecycle.
 */

// vitest needs to flip the flag per test; the app type pins it readonly.
const mockFlag = (): { VITE_ENABLE_MOCK?: string } => import.meta.env;

describe("review events wiring", () => {
  afterEach(() => {
    stopReviewEvents();
    setReviewEventClient(null);
  });

  it("delivers fixture events through the shared client under the mock flag", async () => {
    const previous = mockFlag().VITE_ENABLE_MOCK;
    mockFlag().VITE_ENABLE_MOCK = "true";
    resetReviewFixture();
    try {
      await startReviewEvents();
      const client = getReviewEventClient();
      const seen: string[] = [];
      client.subscribe("review-runs/4f6f1a2b-0000-4000-8000-00000000aa01", (event) => {
        seen.push(event.type);
      });
      // Ingest straight into the client — the transport loop shares it.
      expect(
        client.ingest({
          specversion: "1.0",
          id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
          type: "io.yearning.v4.review.completed",
          source: "yearning://control-plane",
          subject: "review-runs/4f6f1a2b-0000-4000-8000-00000000aa01",
          time: "2026-08-30T10:00:00Z",
          sequence: 1,
          data: {
            review_run_id: "4f6f1a2b-0000-4000-8000-00000000aa01",
            draft_id: "4f6f1a2b-0000-4000-8000-00000000aa02",
            draft_revision: 1,
            state: "ready",
            stage_results: [
              {
                stage_position: 1,
                state: "passed",
                highest_severity: "none",
                finding_count: 0,
                evidence_count: 0,
                snapshot_hash: "snap",
              },
            ],
            gate: { passed: true, reason_codes: [] },
            aggregate_version: 2,
          },
        }),
      ).toBe(true);
      expect(seen).toEqual(["io.yearning.v4.review.completed"]);
    } finally {
      mockFlag().VITE_ENABLE_MOCK = previous;
    }
  });

  it("restarts the feed after a stop/start cycle under the mock flag", async () => {
    const previous = mockFlag().VITE_ENABLE_MOCK;
    mockFlag().VITE_ENABLE_MOCK = "true";
    resetReviewFixture();
    try {
      await startReviewEvents();
      stopReviewEvents();
      // Drive the chain while the feed is down: the outbox holds the events
      // (ephemeral runs do not emit events — RCP-20261101 backend explicitly skips ephemeralFinish events; here
      // submitting an order produces the persistent domain event change_order.submitted).
      const create = (await (
        await fetch("/change-drafts", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            flow_id: "4f6f1a2b-0000-4000-8000-000000000001",
            title: "restart",
          }),
        })
      ).json()) as { data: { id: string } };
      await fetch(`/change-drafts/${create.data.id}/sql`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sql: "SELECT 1;" }),
      });
      // RCP-20261101 ephemeral precheck: POST /review-runs sends current SQL, response directly returns run_id.
      const run = (await (
        await fetch("/review-runs", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            flow_id: "4f6f1a2b-0000-4000-8000-000000000001",
            draft_id: create.data.id,
            sql: "SELECT 1;",
          }),
        })
      ).json()) as { data: { run_id: string } };
      const runId = run.data.run_id;
      await new Promise((resolve) => {
        setTimeout(resolve, 1_700);
      });
      const frozenRun = (await (
        await fetch(`/review-runs/${runId}`)
      ).json()) as {
        data: {
          stage_results: Array<{
            summary: string | null;
            summary_locale: "zh-CN" | "en-US" | null;
          }>;
        };
      };
      // The authoritative HTTP read model keeps the accepted frozen summary.
      expect(frozenRun.data.stage_results[0]?.summary).toContain("AI \u5ba1\u6838\u5df2\u5b8c\u6210");
      expect(frozenRun.data.stage_results[0]?.summary_locale).toBe("zh-CN");

      // Submission materializes the ephemeral run and emits the persisted
      // domain event (change_order.submitted) — the outbox holds it while
      // the feed is down.
      const submitted = (await (
        await fetch("/change-orders", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            flow_id: "4f6f1a2b-0000-4000-8000-000000000001",
            draft_id: create.data.id,
            sql: "SELECT 1;",
            run_id: runId,
          }),
        })
      ).json()) as { data: { id: string } };
      const orderSubject = `change-orders/${submitted.data.id}`;

      // Immediate remount after stop: the client converges the previous loop
      // instead of silently dying (the stop→start race the review gate
      // caught), and the new transport drains the outbox from the resume
      // point — the subscriber sees the submitted event.
      // Subscribe before restarting: the mock transport drains the entire outbox immediately on restart,
      // so subscribing after restart would miss the initial emission (race); subscribing before starting is the
      // deterministic assertion for the behavior under test (outbox resumption after stop->start).
      const client = getReviewEventClient();
      const seen: string[] = [];
      client.subscribe(orderSubject, (event) => {
        seen.push(event.type);
      });
      await startReviewEvents();
      await new Promise((resolve) => {
        setTimeout(resolve, 600);
      });
      expect(seen).toContain("io.yearning.v4.change_order.submitted");
    } finally {
      mockFlag().VITE_ENABLE_MOCK = previous;
      stopReviewEvents();
    }
  });

  it("stays idle without the mock flag and tolerates start/stop cycles", async () => {
    const previous = mockFlag().VITE_ENABLE_MOCK;
    mockFlag().VITE_ENABLE_MOCK = undefined;
    try {
      await startReviewEvents();
      await startReviewEvents();
      stopReviewEvents();
      stopReviewEvents();
      // No transport was started: the client runs nothing and stays resumable.
      expect(getReviewEventClient().resumePoint()).toEqual({});
    } finally {
      mockFlag().VITE_ENABLE_MOCK = previous;
    }
  });
});
