import { afterEach, describe, expect, it } from "vitest";
import {
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
  type FixtureOrder,
} from "@/shared/mock/review-fixture";
import { resetQueryFixture, seedQueryScenario } from "@/shared/mock/query-fixture";
import {
  MOCK_IDENTITIES,
  setMockIdentity,
} from "@/shared/mock/identity-store";

/**
 * §18.44 identity role separation: the mock models three deterministic
 * identities — henry submits, reviewer-a holds the frozen approval slots,
 * executor-a holds the execution slots. These tests pin the exact guarantee
 * the owner ruled on: when the submitter and the reviewer are different
 * people, every acting surface keys on identity membership, and the
 * submitter can neither see nor perform the reviewer's (or executor's)
 * actions at the handler level.
 */

interface Envelope<T> {
  err_code: number;
  message: string;
  data: T;
}

async function call<T = Record<string, unknown>>(
  path: string,
  init?: RequestInit,
): Promise<{ status: number; body: Envelope<T> }> {
  const response = await fetch(`https://yearning.test${path}`, init);
  return { status: response.status, body: (await response.json()) as Envelope<T> };
}

const post = (body: unknown, headers: Record<string, string> = {}): RequestInit => ({
  method: "POST",
  headers: { "Content-Type": "application/json", ...headers },
  body: JSON.stringify(body),
});

const REVIEWER = MOCK_IDENTITIES["reviewer-a"];
const EXECUTOR = MOCK_IDENTITIES["executor-a"];

function approvalActiveOrder(): FixtureOrder {
  return {
    id: "8a6f1a2b-0000-4000-8000-000000001d01",
    display_number: "YR-20261013-000001",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "Identity separation verification order",
    state: "stage_approval_active",
    current_stage_position: 1,
    stages: [
      {
        id: "8a6f1a2b-0000-4000-8000-000000001d11",
        position: 1,
        datasource_name: "orders-mysql",
        state: "approval_active",
        approval_steps: [
          {
            id: "8a6f1a2b-0000-4000-8000-000000001d21",
            position: 1,
            state: "active",
            decided_at: null,
            actors: [{ id: REVIEWER.id, username: REVIEWER.username, display_name: REVIEWER.username }],
          },
        ],
        execution_actors: [
          {
            id: EXECUTOR.id,
            username: EXECUTOR.username,
            display_name: EXECUTOR.username,
            email: null,
            is_builtin_admin: false,
            version: 1,
            created_at: "2026-08-01T00:00:00Z",
            updated_at: "2026-08-01T00:00:00Z",
          },
        ],
      },
    ],
    has_sql: true,
    sql_hash: "hash-id-1",
    snapshot_hash: "snap-id-1",
    manually_verified: false,
    version: 1,
    submitted_at: "2026-10-13T07:35:35Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "UPDATE orders SET status = 1 WHERE user_id = 42;",
  };
}

function executionPendingOrder(): FixtureOrder {
  const approved = approvalActiveOrder();
  const stage = approved.stages[0];
  if (stage === undefined) throw new Error("approvalActiveOrder must seed a stage");
  return {
    ...approved,
    id: "8a6f1a2b-0000-4000-8000-000000001d02",
    display_number: "YR-20261013-000002",
    state: "stage_execution_pending",
    stages: [
      {
        ...stage,
        id: "8a6f1a2b-0000-4000-8000-000000001d12",
        state: "execution_pending",
        approval_steps: [
          {
            id: "8a6f1a2b-0000-4000-8000-000000001d22",
            position: 1,
            state: "approved",
            decided_at: "2026-10-13T07:40:00Z",
            actors: [{ id: REVIEWER.id, username: REVIEWER.username, display_name: REVIEWER.username }],
          },
        ],
      },
    ],
  };
}

afterEach(() => {
  setMockIdentity("henry");
  resetReviewFixture();
  resetQueryFixture();
});

describe("identity role separation (§18.44)", () => {
  it("/users/me answers with the stored identity", async () => {
    setMockIdentity("reviewer-a");
    const me = await call<{ id: string; username: string }>("/users/me", {
      headers: { cookie: "yearning_session=mock-session-token" },
    });
    expect(me.body.data.id).toBe(REVIEWER.id);
    expect(me.body.data.username).toBe("reviewer-a");
  });

  it("the submitter cannot decide the order: 3001 for a non-frozen actor", async () => {
    seedFixtureOrder(approvalActiveOrder());
    const refused = await call(
      "/change-orders/8a6f1a2b-0000-4000-8000-000000001d01/approval-decisions",
      post({ decision: "approve" }, { "If-Match": '"1"' }),
    );
    expect(refused.body.err_code).toBe(3001);
  });

  it("the frozen reviewer decides and the timeline credits the reviewer identity", async () => {
    seedFixtureOrder(approvalActiveOrder());
    setMockIdentity("reviewer-a");
    const decided = await call(
      "/change-orders/8a6f1a2b-0000-4000-8000-000000001d01/approval-decisions",
      post({ decision: "approve" }, { "If-Match": '"1"' }),
    );
    expect(decided.body.err_code).toBe(0);
    expect(decided.body.data.state).toBe("stage_execution_pending");

    const timeline = await call<{
      items: Array<{
        event_type: string;
        actor_user_id: string;
        actor_display_name: string | null;
        summary: string;
      }>;
    }>("/change-orders/8a6f1a2b-0000-4000-8000-000000001d01/timeline");
    const decisionEntry = timeline.body.data.items.find((entry) =>
      entry.event_type.includes("approval_decided"),
    );
    expect(decisionEntry?.actor_user_id).toBe(REVIEWER.id);
    // The audit record never contradicts itself: both the id and the
    // human-readable fields name the acting identity.
    expect(decisionEntry?.actor_display_name).toBe("reviewer-a");
    expect(decisionEntry?.summary).toContain("\u5ba1\u6279\u4eba reviewer-a");
    expect(decisionEntry?.summary).not.toContain("\u5ba1\u6279\u4eba henry");
  });

  it("W008: withdrawal and voidance belong to the submitter alone — a reviewer learns nothing", async () => {
    seedFixtureOrder(approvalActiveOrder());
    setMockIdentity("reviewer-a");
    const withdrawRefused = await call(
      "/change-orders/8a6f1a2b-0000-4000-8000-000000001d01/withdrawal",
      post({ reason: "Unauthorized withdraw" }, { "If-Match": '"1"' }),
    );
    expect(withdrawRefused.body.err_code).toBe(1002);

    const voidRefused = await call(
      "/change-orders/8a6f1a2b-0000-4000-8000-000000001d01/voidance",
      post({ reason: "Unauthorized void" }, { "If-Match": '"1"' }),
    );
    expect(voidRefused.body.err_code).toBe(1002);

    setMockIdentity("henry");
    const withdrawn = await call(
      "/change-orders/8a6f1a2b-0000-4000-8000-000000001d01/withdrawal",
      post({ reason: "Submitter withdraw" }, { "If-Match": '"1"' }),
    );
    expect(withdrawn.body.err_code).toBe(0);
    expect(withdrawn.body.data.state).toBe("withdrawn");
  });

  it("the submitter cannot start execution: only the frozen executor may begin", async () => {
    seedFixtureOrder(executionPendingOrder());
    const refused = await call(
      "/change-orders/8a6f1a2b-0000-4000-8000-000000001d02/execution-attempts",
      post({}, { "If-Match": '"1"' }),
    );
    expect(refused.body.err_code).toBe(3001);

    setMockIdentity("executor-a");
    const begun = await call(
      "/change-orders/8a6f1a2b-0000-4000-8000-000000001d02/execution-attempts",
      post({}, { "If-Match": '"1"' }),
    );
    expect(begun.body.err_code).toBe(0);
  });

  it("the relation-scoped order list follows the identity: submitter and reviewer both relate, but through their own slots", async () => {
    seedFixtureOrder(approvalActiveOrder());
    const asSubmitter = await call<{ items: Array<{ id: string }> }>("/change-orders");
    expect(asSubmitter.body.data.items.map((row) => row.id)).toContain(
      "8a6f1a2b-0000-4000-8000-000000001d01",
    );

    setMockIdentity("reviewer-a");
    const asReviewer = await call<{ items: Array<{ id: string }> }>("/change-orders");
    expect(asReviewer.body.data.items.map((row) => row.id)).toContain(
      "8a6f1a2b-0000-4000-8000-000000001d01",
    );
  });

  it("query access decisions belong to the reviewer: henry cannot even read the pending request", async () => {
    seedQueryScenario("query-approval");
    const asSubmitter = await call("/query-access-requests/qar-fixture-pending");
    expect(asSubmitter.body.err_code).toBe(1002);

    setMockIdentity("reviewer-a");
    const asReviewer = await call<{ state: string }>("/query-access-requests/qar-fixture-pending");
    expect(asReviewer.body.err_code).toBe(0);
    expect(asReviewer.body.data.state).toBe("access_pending");
  });
});
