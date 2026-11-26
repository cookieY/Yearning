import { afterEach, describe, expect, it } from "vitest";
import type { FixtureOrder } from "./review-fixture";
import {
  FIXTURE_FLOW_ID,
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
} from "./review-fixture";

/**
 * Change-order fixture contract tests (frontend PRD F6). The lifecycle tests
 * pin the semantics the order pages and the acceptance gates rely on: the
 * submission persists an order and emits its event, the list is a cursor
 * page, withdrawal follows the change_order state machine (running →
 * withdrawn_after_partial_execution), voidance only from legal states, the
 * timeline is the audit projection and reason is mandatory.
 */

afterEach(() => {
  resetReviewFixture();
});

interface Envelope<T> {
  err_code: number;
  message: string;
  data: T;
}

async function jsonRequest(
  path: string,
  init?: RequestInit,
): Promise<{ status: number; body: Envelope<Record<string, unknown>> }> {
  const response = await fetch(
    `https://yearning.test${path.startsWith("/api/v4") ? path : `/api/v4${path}`}`,
    init,
  );
  return {
    status: response.status,
    body: (await response.json()) as Envelope<Record<string, unknown>>,
  };
}

function post(body: unknown, headers: Record<string, string> = {}): RequestInit {
  return {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  };
}

function seedOrder(overrides: Partial<FixtureOrder> = {}): FixtureOrder {
  const order: FixtureOrder = {
    id: "8a6f1a2b-0000-4000-8000-00000000ab01",
    display_number: "YR-20260830-000007",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "Fixture order",
    state: "submitted",
    current_stage_position: 1,
    stages: [
      {
        id: "8a6f1a2b-0000-4000-8000-00000000ab11",
        position: 1,
        datasource_name: "orders-mysql",
        state: "approval_active",
        approval_steps: [
          {
            id: "8a6f1a2b-0000-4000-8000-00000000ab21",
            position: 1,
            state: "pending",
            decided_at: null,
            actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
          },
        ],
        execution_actors: [
          {
            id: FIXTURE_OWNER_ID,
            username: "henry",
            display_name: "henry",
            email: null,
            is_builtin_admin: true,
            version: 1,
            created_at: "2026-08-01T00:00:00Z",
            updated_at: "2026-08-01T00:00:00Z",
          },
        ],
      },
    ],
    has_sql: true,
    sql_hash: "hash-1",
    snapshot_hash: "snap-1",
    manually_verified: false,
    version: 1,
    submitted_at: "2026-08-30T10:00:00Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "SELECT 1;",
  };
  return { ...order, ...overrides };
}

describe("change-order fixture contract", () => {
  it("seeds a running order for the partial-execution scenario via the list endpoint", async () => {
    localStorage.setItem("yearning-mock-scenario", "order-partial-execution");
    const list = await jsonRequest("/change-orders");
    expect(list.body.err_code).toBe(0);
    const items = list.body.data.items as Array<Record<string, unknown>>;
    expect(items).toHaveLength(1);
    expect(items[0]?.state).toBe("running");
    localStorage.removeItem("yearning-mock-scenario");
  });

  it("keeps the browser-only order gallery out of the vitest world", async () => {
    // The gallery decorates dev/e2e mock worlds only (import.meta.env.MODE
    // gate in seedOrderGallery); contract tests pin exact list shapes.
    const list = await jsonRequest("/change-orders?limit=50");
    expect(list.body.err_code).toBe(0);
    expect(list.body.data.items).toHaveLength(0);
  });

  it("withdraws a submitted order to withdrawn and appends a timeline entry", async () => {
    seedFixtureOrder(seedOrder());
    const result = await jsonRequest(
      `/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/withdrawal`,
      post({ reason: "No longer needed" }, { "If-Match": '"1"' }),
    );
    expect(result.body.err_code).toBe(0);
    expect(result.body.data.state).toBe("withdrawn");
    expect(result.body.data.terminal_at).not.toBeNull();

    const timeline = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/timeline",
    );
    const entries = timeline.body.data.items as Array<Record<string, unknown>>;
    expect(entries.length).toBe(1);
    expect(entries[0]?.event_type).toBe("change_order.withdrawn");
  });

  it("maps withdrawal from a running order to withdrawn_after_partial_execution", async () => {
    seedFixtureOrder(seedOrder({ state: "running", version: 3 }));
    const result = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/withdrawal",
      post({ reason: "Stop" }, { "If-Match": '"3"' }),
    );
    expect(result.body.err_code).toBe(0);
    expect(result.body.data.state).toBe("withdrawn_after_partial_execution");
  });

  it("refuses withdrawal from a terminal state with 1010 and rejects a version mismatch with 1004", async () => {
    seedFixtureOrder(seedOrder({ state: "completed" }));
    const illegal = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/withdrawal",
      post({ reason: "x" }, { "If-Match": '"1"' }),
    );
    expect(illegal.body.err_code).toBe(1010);

    seedFixtureOrder(
      seedOrder({ id: "8a6f1a2b-0000-4000-8000-00000000ab02" }),
    );
    const conflict = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab02/withdrawal",
      post({ reason: "x" }, { "If-Match": '"9"' }),
    );
    // order_lifecycle profile: concurrent modification on withdrawal is
    // 1004 (1003 belongs to the draft revision profiles).
    expect(conflict.body.err_code).toBe(1004);
  });

  it("requires a non-empty reason for withdrawal and voidance", async () => {
    seedFixtureOrder(seedOrder());
    const empty = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/withdrawal",
      post({ reason: "   " }, { "If-Match": '"1"' }),
    );
    expect(empty.body.err_code).toBe(1001);

    seedFixtureOrder(seedOrder({ id: "8a6f1a2b-0000-4000-8000-00000000ab03", state: "result_unknown" }));
    const emptyVoid = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab03/voidance",
      post({ reason: "" }, { "If-Match": '"1"' }),
    );
    expect(emptyVoid.body.err_code).toBe(1001);
  });

  it("voids from legal states and refuses voidance from running", async () => {
    seedFixtureOrder(seedOrder({ state: "result_unknown" }));
    const ok = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/voidance",
      post({ reason: "Outcome unknown, voiding" }, { "If-Match": '"1"' }),
    );
    expect(ok.body.err_code).toBe(0);
    expect(ok.body.data.state).toBe("voided");

    seedFixtureOrder(seedOrder({ id: "8a6f1a2b-0000-4000-8000-00000000ab04", state: "running" }));
    const illegal = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab04/voidance",
      post({ reason: "x" }, { "If-Match": '"1"' }),
    );
    expect(illegal.body.err_code).toBe(1010);
  });

  it("copies a terminal order into a fresh draft on a granted flow", async () => {
    seedFixtureOrder(seedOrder({ state: "withdrawn" }));
    const copy = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/draft-copies",
      post({ target_flow_id: FIXTURE_FLOW_ID, title: "Re-edited copy" }),
    );
    expect(copy.body.err_code).toBe(0);
    const draft = copy.body.data;
    expect(draft.state).toBe("draft");
    expect(draft.has_sql).toBe(false);

    const revoke = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000ab01/draft-copies",
      post({ target_flow_id: "other-flow", title: "x" }),
    );
    expect(revoke.body.err_code).toBe(2014);
  });

  it("orders the personal list newest first as a cursor page", async () => {
    seedFixtureOrder(
      seedOrder({ id: "8a6f1a2b-0000-4000-8000-00000000ab0a", submitted_at: "2026-08-30T08:00:00Z" }),
    );
    seedFixtureOrder(
      seedOrder({
        id: "8a6f1a2b-0000-4000-8000-00000000ab0b",
        display_number: "YR-20260830-000008",
        submitted_at: "2026-08-30T09:00:00Z",
      }),
    );
    const first = await jsonRequest("/change-orders?limit=1");
    const page = first.body.data as { items: Array<Record<string, unknown>>; page: { next_cursor: string | null } };
    expect(page.items).toHaveLength(1);
    expect(page.items[0]?.id).toBe("8a6f1a2b-0000-4000-8000-00000000ab0b");
    expect(page.page.next_cursor).toBe("8a6f1a2b-0000-4000-8000-00000000ab0b");

    const second = await jsonRequest(
      `/change-orders?limit=1&after=${String(page.page.next_cursor)}`,
    );
    const secondPage = second.body.data as { items: Array<Record<string, unknown>>; page: { has_more: boolean } };
    expect(secondPage.items[0]?.id).toBe("8a6f1a2b-0000-4000-8000-00000000ab0a");
    expect(secondPage.page.has_more).toBe(false);
  });
});

describe("change-order list filters (RCP-20260831-ORDER-LIST-FILTER)", () => {
  function seedFilterSet(): void {
    seedFixtureOrder(
      seedOrder({
        id: "8a6f1a2b-0000-4000-8000-00000000fa01",
        display_number: "YR-20260830-000101",
        title: "Legacy orders table fix",
        state: "running",
        submitted_at: "2026-08-28T09:30:00Z",
        stages: [
          {
            id: "8a6f1a2b-0000-4000-8000-00000000fb01",
            position: 1,
            datasource_name: "staging-mysql",
            state: "succeeded",
            approval_steps: [],
            execution_actors: [],
          },
        ],
      }),
    );
    seedFixtureOrder(
      seedOrder({
        id: "8a6f1a2b-0000-4000-8000-00000000fa02",
        display_number: "YR-20260830-000102",
        title: "Index rebuild",
        state: "withdrawn",
        submitted_at: "2026-08-30T18:00:00Z",
      }),
    );
  }

  async function listWith(query: string): Promise<Array<Record<string, unknown>>> {
    const result = await jsonRequest(`/change-orders?${query}`);
    expect(result.body.err_code).toBe(0);
    return result.body.data.items as Array<Record<string, unknown>>;
  }

  it("filters by exact aggregate state", async () => {
    seedFilterSet();
    const running = await listWith("state=running");
    expect(running).toHaveLength(1);
    expect(running[0]?.display_number).toBe("YR-20260830-000101");
  });

  it("matches the keyword case-insensitively against display_number and title", async () => {
    seedFilterSet();
    const byNumber = await listWith("q=000102");
    expect(byNumber.map((order) => order.display_number)).toEqual(["YR-20260830-000102"]);
    const byTitle = await listWith("q=" + encodeURIComponent("orders table"));
    expect(byTitle.map((order) => order.display_number)).toEqual(["YR-20260830-000101"]);
    const byCase = await listWith("q=yr-20260830-000102");
    expect(byCase).toHaveLength(1);
  });

  it("filters by exact stage datasource name", async () => {
    seedFilterSet();
    const staging = await listWith("datasource=staging-mysql");
    expect(staging).toHaveLength(1);
    expect(staging[0]?.display_number).toBe("YR-20260830-000101");
    const none = await listWith("datasource=unknown-ds");
    expect(none).toHaveLength(0);
  });

  it("applies inclusive system_timezone day bounds on submitted_at", async () => {
    seedFilterSet();
    const window = await listWith(
      "submitted_from=2026-08-28&submitted_to=2026-08-28",
    );
    expect(window.map((order) => order.display_number)).toEqual(["YR-20260830-000101"]);
    // 2026-08-30T18:00:00Z is 2026-08-31 02:00:00 in default Asia/Shanghai.
    const shanghaiWide = await listWith(
      "submitted_from=2026-08-28&submitted_to=2026-08-30",
    );
    expect(shanghaiWide.map((order) => order.display_number)).toEqual(["YR-20260830-000101"]);
    const shanghaiNextDay = await listWith("submitted_from=2026-08-31");
    expect(shanghaiNextDay.map((order) => order.display_number)).toEqual(["YR-20260830-000102"]);

    // Switching general.system_timezone to UTC takes effect immediately on the next list query.
    const { applySettingsToBridge } = await import("@/shared/mock/settings-bridge");
    applySettingsToBridge("general", { system_timezone: "UTC" });
    const utcWide = await listWith(
      "submitted_from=2026-08-28&submitted_to=2026-08-30",
    );
    expect(utcWide).toHaveLength(2);
    const utcFuture = await listWith("submitted_from=2026-08-31");
    expect(utcFuture).toHaveLength(0);
    applySettingsToBridge("general", { system_timezone: "Asia/Shanghai" });
  });

  it("keeps orders submitted exactly at Asia/Shanghai day-boundary instants", async () => {
    // 2026-08-28 00:00:00 CST = 2026-08-27T16:00:00Z; 23:59:59 CST = 2026-08-28T15:59:59Z.
    seedFixtureOrder(
      seedOrder({
        id: "8a6f1a2b-0000-4000-8000-00000000fa03",
        display_number: "YR-20260830-000103",
        submitted_at: "2026-08-27T16:00:00Z",
      }),
    );
    seedFixtureOrder(
      seedOrder({
        id: "8a6f1a2b-0000-4000-8000-00000000fa04",
        display_number: "YR-20260830-000104",
        submitted_at: "2026-08-28T15:59:59Z",
      }),
    );
    const both = await listWith("submitted_from=2026-08-28&submitted_to=2026-08-28");
    expect(both.map((order) => order.display_number).sort()).toEqual([
      "YR-20260830-000103",
      "YR-20260830-000104",
    ]);
  });

  it("combines filters conjunctively", async () => {
    seedFilterSet();
    const combined = await listWith("state=withdrawn&q=000102&datasource=orders-mysql");
    expect(combined).toHaveLength(1);
    const contradictory = await listWith("state=running&q=000102");
    expect(contradictory).toHaveLength(0);
  });
});

describe("approval decision fixture contract (FE-F7)", () => {
  function decisionOrder(overrides: Partial<FixtureOrder> = {}): FixtureOrder {
    const base = seedOrder({ id: "8a6f1a2b-0000-4000-8000-00000000de01", state: "stage_approval_active" });
    // The seeded step is pending; a decisionable order has the active step
    // of its active stage ready for the frozen reviewer.
    const baseStage = base.stages[0];
    if (baseStage !== undefined) {
      baseStage.approval_steps = [
        {
          id: "8a6f1a2b-0000-4000-8000-00000000de20",
          position: 1,
          state: "active",
          decided_at: null,
          actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
        },
      ];
    }
    return { ...base, ...overrides };
  }

  function decide(body: Record<string, unknown>, headers: Record<string, string> = { "If-Match": '"1"' }) {
    return jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000de01/approval-decisions",
      post(body, headers),
    );
  }

  it("rejects immediately rejecting the whole order: remaining steps skipped, stage cancelled", async () => {
    seedFixtureOrder(
      decisionOrder({
        stages: [
          {
            id: "8a6f1a2b-0000-4000-8000-00000000de11",
            position: 1,
            datasource_name: "orders-mysql",
            state: "approval_active",
            approval_steps: [
              {
                id: "8a6f1a2b-0000-4000-8000-00000000de21",
                position: 1,
                state: "active",
                decided_at: null,
                actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
              },
              {
                id: "8a6f1a2b-0000-4000-8000-00000000de22",
                position: 2,
                state: "pending",
                decided_at: null,
                actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
              },
            ],
            execution_actors: [],
          },
        ],
      }),
    );
    const result = await decide({ decision: "reject", comment: "Statement risk is unacceptable" });
    expect(result.body.err_code).toBe(0);
    expect(result.body.data.state).toBe("rejected");
    expect(result.body.data.terminal_at).not.toBeNull();
    const stages = result.body.data.stages as Array<{
      state: string;
      approval_steps: Array<{ state: string; decided_at: string | null }>;
    }>;
    expect(stages[0]?.state).toBe("cancelled");
    expect(stages[0]?.approval_steps.map((step) => step.state)).toEqual(["rejected", "skipped"]);

    const timeline = await jsonRequest("/change-orders/8a6f1a2b-0000-4000-8000-00000000de01/timeline");
    const entries = timeline.body.data.items as Array<Record<string, unknown>>;
    expect(entries.some((entry) => entry.event_type === "change_order.approval_decided")).toBe(true);
    expect(entries.some((entry) => entry.event_type === "change_order.rejected")).toBe(true);
    // BUG-004 read face: the decision comment rides the timeline entry as
    // its own field (backend projects approval_decisions.comment), not
    // glued into the summary — the page renders it in a dedicated element.
    const decided = entries.find((entry) => entry.event_type === "change_order.approval_decided");
    expect(decided?.comment).toBe("Statement risk is unacceptable");
    expect(String(decided?.summary)).not.toContain("Statement risk is unacceptable");
  });

  it("a non-final approve activates the next step and keeps the order in approval", async () => {
    seedFixtureOrder(
      decisionOrder({
        version: 4,
        stages: [
          {
            id: "8a6f1a2b-0000-4000-8000-00000000de12",
            position: 1,
            datasource_name: "orders-mysql",
            state: "approval_active",
            approval_steps: [
              {
                id: "8a6f1a2b-0000-4000-8000-00000000de23",
                position: 1,
                state: "active",
                decided_at: null,
                actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
              },
              {
                id: "8a6f1a2b-0000-4000-8000-00000000de24",
                position: 2,
                state: "pending",
                decided_at: null,
                actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
              },
            ],
            execution_actors: [],
          },
        ],
      }),
    );
    const result = await decide({ decision: "approve", comment: "" }, { "If-Match": '"4"' });
    expect(result.body.err_code).toBe(0);
    expect(result.body.data.state).toBe("stage_approval_active");
    expect(result.body.data.version).toBe(5);
    const stages = result.body.data.stages as Array<{
      approval_steps: Array<{ state: string }>;
    }>;
    expect(stages[0]?.approval_steps.map((step) => step.state)).toEqual(["approved", "active"]);
  });

  it("the final approve leaves the order at stage_execution_pending, never executing", async () => {
    seedFixtureOrder(decisionOrder());
    const result = await decide({ decision: "approve", comment: "Approved" });
    expect(result.body.err_code).toBe(0);
    expect(result.body.data.state).toBe("stage_execution_pending");
    const stages = result.body.data.stages as Array<{ state: string }>;
    expect(stages[0]?.state).toBe("execution_pending");
  });

  it("maps the order_decision profile errors: 1004 stale If-Match, 1010 outside approval, 3001 non-frozen actor, 3002 already decided", async () => {
    seedFixtureOrder(decisionOrder({ version: 2 }));
    const stale = await decide({ decision: "approve" }, { "If-Match": '"1"' });
    expect(stale.body.err_code).toBe(1004);

    seedFixtureOrder(decisionOrder({ id: "8a6f1a2b-0000-4000-8000-00000000de02", state: "completed" }));
    const illegal = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000de02/approval-decisions",
      post({ decision: "approve" }, { "If-Match": '"1"' }),
    );
    expect(illegal.body.err_code).toBe(1010);

    seedFixtureOrder(
      decisionOrder({
        id: "8a6f1a2b-0000-4000-8000-00000000de03",
        stages: [
          {
            id: "8a6f1a2b-0000-4000-8000-00000000de13",
            position: 1,
            datasource_name: "orders-mysql",
            state: "approval_active",
            approval_steps: [
              {
                id: "8a6f1a2b-0000-4000-8000-00000000de25",
                position: 1,
                state: "active",
                decided_at: null,
                actors: [{ id: "00000000-0000-4000-8000-000000009999", username: "other", display_name: "Other" }],
              },
            ],
            execution_actors: [],
          },
        ],
      }),
    );
    const notActor = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000de03/approval-decisions",
      post({ decision: "approve" }, { "If-Match": '"1"' }),
    );
    expect(notActor.body.err_code).toBe(3001);

    // 3002: a step that is still active but already carries a decision
    // (the racing state the single-effective-decision constraint guards —
    // the winner committed, the loser's view still shows the step active).
    seedFixtureOrder(
      decisionOrder({
        id: "8a6f1a2b-0000-4000-8000-00000000de05",
        stages: [
          {
            id: "8a6f1a2b-0000-4000-8000-00000000de15",
            position: 1,
            datasource_name: "orders-mysql",
            state: "approval_active",
            approval_steps: [
              {
                id: "8a6f1a2b-0000-4000-8000-00000000de27",
                position: 1,
                state: "active",
                decided_at: "2026-08-30T12:00:00Z",
                actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
              },
            ],
            execution_actors: [],
          },
        ],
      }),
    );
    const decided = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000de05/approval-decisions",
      post({ decision: "approve" }, { "If-Match": '"1"' }),
    );
    expect(decided.body.err_code).toBe(3002);
  });

  it("appends comments and reads them newest first", async () => {
    seedFixtureOrder(decisionOrder());
    const created = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000de01/comments",
      post({ content: "Please add rollback notes" }),
    );
    expect(created.body.err_code).toBe(0);
    const comment = created.body.data;
    expect(comment.author_display_name).toBe("henry");
    expect(comment.content).toBe("Please add rollback notes");

    const listed = await jsonRequest("/change-orders/8a6f1a2b-0000-4000-8000-00000000de01/comments");
    const items = listed.body.data.items as Array<Record<string, unknown>>;
    expect(items).toHaveLength(1);
    expect(items[0]?.content).toBe("Please add rollback notes");

    // Backend comments.go semantics: only the empty string (or >4096 runes)
    // is invalid; whitespace-only content is accepted.
    const empty = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000de01/comments",
      post({ content: "" }),
    );
    expect(empty.body.err_code).toBe(1001);

    const whitespace = await jsonRequest(
      "/change-orders/8a6f1a2b-0000-4000-8000-00000000de01/comments",
      post({ content: "   " }),
    );
    expect(whitespace.body.err_code).toBe(0);
  });

  it("serves the frozen submission findings as a pure read", async () => {
    seedFixtureOrder(decisionOrder({ review_run_id: "8a6f1a2b-0000-4000-8000-000000002e01" }));
    const listed = await jsonRequest("/change-orders/8a6f1a2b-0000-4000-8000-00000000de01/review-findings");
    expect(listed.body.err_code).toBe(0);
    expect(listed.body.data.items).toEqual([]);
  });
});

describe("requested execution time fixture contract (§18.56 ruling B)", () => {
  it("rejects a past requested time on draft create and clears via null on update", async () => {
    const past = new Date(Date.now() - 3600_000).toISOString().replace(/\.\d{3}Z$/, "Z");
    const future = new Date(Date.now() + 48 * 3600_000).toISOString().replace(/\.\d{3}Z$/, "Z");

    const bad = await jsonRequest("/change-drafts", post({
      flow_id: FIXTURE_FLOW_ID, title: "sched-mock", requested_scheduled_for: past,
    }));
    expect(bad.body.err_code).toBe(1001);

    const created = await jsonRequest("/change-drafts", post({
      flow_id: FIXTURE_FLOW_ID, title: "sched-mock", requested_scheduled_for: future,
    }));
    expect(created.body.err_code).toBe(0);
    expect(created.body.data.requested_scheduled_for).toBe(future);
    const draftId = created.body.data.id as string;

    const cleared = await jsonRequest(`/change-drafts/${draftId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ requested_scheduled_for: null }),
    });
    expect(cleared.body.err_code).toBe(0);
    expect(cleared.body.data.requested_scheduled_for).toBeNull();
  });
});
