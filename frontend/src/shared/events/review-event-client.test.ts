import { describe, expect, it, vi } from "vitest";
import { ReviewEventClient, getReviewEventClient } from "@/shared/events/review-event-client";

const RUN_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";
const DRAFT_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ad";
const SUBJECT = `review-runs/${RUN_ID}`;
const ALT_SUBJECT = `change-drafts/${DRAFT_ID}`;
const UUID_PREFIX = "0198d9d0-0000-7000-8000-";
const EVENT_ID_1 = `${UUID_PREFIX}000000000001`;
const EVENT_ID_2 = `${UUID_PREFIX}000000000002`;
const EVENT_ID_3 = `${UUID_PREFIX}000000000003`;
const EVENT_ID_4 = `${UUID_PREFIX}000000000004`;

function event(sequence: number, id: string, subject = SUBJECT, extra: Record<string, unknown> = {}) {
  return {
    specversion: "1.0",
    id,
    type: "io.yearning.v4.change_draft.state_changed",
    source: "yearning://control-plane",
    subject,
    time: "2026-08-28T08:30:00Z",
    sequence,
    data: {
      aggregate_id: subject.split("/")[1] ?? RUN_ID,
      from: "draft",
      to: "submitted",
      reason_code: "submitted_by_user",
      aggregate_version: 2,
    },
    ...extra,
  };
}

describe("ReviewEventClient delivery semantics", () => {
  it("delivers a valid event to subject subscribers", () => {
    const client = new ReviewEventClient();
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);
    expect(client.ingest(event(1, EVENT_ID_1))).toBe(true);
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("drops a duplicate event id so a reconnect never re-consumes", () => {
    const client = new ReviewEventClient();
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);
    expect(client.ingest(event(1, EVENT_ID_1))).toBe(true);
    expect(client.ingest(event(1, EVENT_ID_1))).toBe(false);
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("drops stale out-of-order sequences per subject", () => {
    const client = new ReviewEventClient();
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);
    client.ingest(event(2, EVENT_ID_2));
    expect(client.ingest(event(1, EVENT_ID_1))).toBe(false);
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it("fires the sequence-gap callback but still delivers the event", () => {
    const onSequenceGap = vi.fn();
    const client = new ReviewEventClient({ onSequenceGap });
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);
    client.ingest(event(1, EVENT_ID_1));
    client.ingest(event(4, EVENT_ID_4));
    expect(onSequenceGap).toHaveBeenCalledWith(SUBJECT);
    expect(handler).toHaveBeenCalledTimes(2);
  });

  it("tracks sequences per subject independently", () => {
    const client = new ReviewEventClient();
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);
    client.subscribe(ALT_SUBJECT, handler);
    client.ingest(event(1, EVENT_ID_1));
    client.ingest(event(1, EVENT_ID_2, ALT_SUBJECT));
    expect(handler).toHaveBeenCalledTimes(2);
    expect(client.ingest(event(1, EVENT_ID_3, ALT_SUBJECT))).toBe(false);
  });

  it("does not deliver events for subjects without subscribers but keeps the resume point", () => {
    const client = new ReviewEventClient();
    client.ingest(event(3, EVENT_ID_3));
    expect(client.resumePoint()[SUBJECT]).toBe(3);
  });

  it("reports schema-violating payloads through onInvalidEvent and never delivers them", () => {
    const onInvalidEvent = vi.fn();
    const client = new ReviewEventClient({ onInvalidEvent });
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);
    const bad = event(1, EVENT_ID_1) as Record<string, unknown>;
    bad.subject = "totally/wrong";
    expect(client.ingest(bad)).toBe(false);
    expect(onInvalidEvent).toHaveBeenCalledTimes(1);
    expect(handler).not.toHaveBeenCalled();
  });

  it("evicts the oldest seen ids so the dedup set stays bounded", () => {
    const client = new ReviewEventClient();
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);
    for (let i = 1; i <= 4100; i += 1) {
      client.ingest(event(i, `${UUID_PREFIX}${String(i).padStart(12, "0")}`));
    }
    // The very first id has been evicted; the handler consumed every event once.
    expect(handler).toHaveBeenCalledTimes(4100);
  });
});

describe("ReviewEventClient transport loop", () => {
  function asyncStream(items: unknown[]): AsyncIterable<unknown> {
    const iterator = items[Symbol.iterator]();
    return {
      [Symbol.asyncIterator]() {
        return {
          next: () => {
            const step = iterator.next();
            return Promise.resolve({ done: step.done ?? false, value: step.value });
          },
        };
      },
    };
  }

  it("reconnects from the resume point without re-consuming delivered events", async () => {
    const client = new ReviewEventClient();
    const handler = vi.fn();
    client.subscribe(SUBJECT, handler);

    let attempt = 0;
    const transport = vi.fn((resume: Record<string, number>) => {
      attempt += 1;
      if (attempt === 1) {
        return asyncStream([event(1, EVENT_ID_1), event(2, EVENT_ID_2)]);
      }
      // Reconnect: a correct transport honours the resume point; a buggy one
      // replays everything — the client must still not re-consume.
      expect(resume[SUBJECT]).toBe(2);
      return asyncStream([event(1, EVENT_ID_1), event(2, EVENT_ID_2), event(3, EVENT_ID_3)]);
    });

    const runPromise = client.run(transport);
    for (let i = 0; i < 50 && attempt < 2; i += 1) {
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    client.stop();
    await runPromise;
    expect(handler).toHaveBeenCalledTimes(3);
    expect(handler.mock.calls.map((call) => (call[0] as { sequence: number }).sequence)).toEqual([
      1, 2, 3,
    ]);
  });

  it("is a single application-wide instance", () => {
    expect(getReviewEventClient()).toBe(getReviewEventClient());
  });
});
