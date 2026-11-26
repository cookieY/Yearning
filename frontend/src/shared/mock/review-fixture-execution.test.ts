import { afterEach, describe, expect, it } from "vitest";
import type { FixtureOrder } from "./review-fixture";
import { FIXTURE_OWNER_ID, resetReviewFixture, seedFixtureOrder } from "./review-fixture";

/**
 * Execution-domain fixture contract tests (frontend PRD F8, W006, E001–E007).
 * They pin the semantics the execution workspace relies on before the real
 * backend exists: the begin error chain mirrors backend beginOnce (3001
 * frozen executor → 3004 sent boundary → 3003 live attempt → 1010 state),
 * DML succeeds as one committed fact set, DDL partial failure preserves prior
 * successes and marks the suffix skipped (Unknown/skipped are distinct from
 * not-executed), result_unknown blocks until a manual verification
 * terminalizes the order, cancellation is executor-scoped and idempotent on
 * terminal attempts, and schedules enforce the executor authorization plus
 * the 5-minute/30-day window.
 */

afterEach(() => {
  resetReviewFixture();
  window.localStorage.removeItem("yearning-mock-scenario");
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

function setScenario(scenario: string): void {
  window.localStorage.setItem("yearning-mock-scenario", scenario);
}

const OTHER_USER = "9998d9cc-e65d-7b9d-a8aa-3c81945f99ac";

function fixtureUser(id: string) {
  const at = "2026-08-01T00:00:00Z";
  return {
    id,
    username: "henry",
    display_name: "henry",
    email: null,
    is_builtin_admin: true,
    version: 1,
    created_at: at,
    updated_at: at,
  };
}

/** `suffix` keeps stage ids distinct across orders — the begin facts are
 * stage-scoped, so two orders sharing a stage id would see each other's
 * attempts. */
function seedExecutionOrder(overrides: Partial<FixtureOrder> = {}, suffix = "cd"): FixtureOrder {
  const stageId = `8a6f1a2b-0000-4000-8000-00000000${suffix}11`;
  const order: FixtureOrder = {
    id: `8a6f1a2b-0000-4000-8000-00000000${suffix}01`,
    display_number: "YR-20260830-000201",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: "Execution fixture order",
    state: "stage_execution_pending",
    current_stage_position: 1,
    stages: [
      {
        id: stageId,
        position: 1,
        datasource_name: "orders-mysql",
        state: "execution_pending",
        approval_steps: [
          {
            id: `8a6f1a2b-0000-4000-8000-00000000${suffix}21`,
            position: 1,
            state: "approved",
            decided_at: "2026-08-01T00:00:00Z",
            actors: [{ id: FIXTURE_OWNER_ID, username: "henry", display_name: "henry" }],
          },
        ],
        execution_actors: [fixtureUser(FIXTURE_OWNER_ID)],
      },
    ],
    has_sql: true,
    sql_hash: "hash-exec-fixture",
    snapshot_hash: "snap-exec-fixture",
    manually_verified: false,
    version: 2,
    submitted_at: "2026-08-01T00:00:00Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "UPDATE orders SET status = 1 WHERE user_id = 42; UPDATE orders SET status = 2 WHERE user_id = 43;",
    ...overrides,
  };
  seedFixtureOrder(order);
  return order;
}

function beginHeaders(version: number): Record<string, string> {
  return { "If-Match": `"${String(version)}"`, "Idempotency-Key": `begin-${String(Math.random())}` };
}

async function begin(order: FixtureOrder): Promise<{ err_code: number; data: Record<string, unknown> }> {
  const { body } = await jsonRequest(`/change-orders/${order.id}/execution-attempts`, post({}, beginHeaders(order.version)));
  return { err_code: body.err_code, data: body.data };
}

/** Waits for the deterministic attempt progression to settle (0.3s preflight
 * + 0.5s running + 0.9s terminal; the ghost outcome never settles). */
async function waitTerminal(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 2_000));
}

describe("execution attempt begin", () => {
  it("answers the backend error chain: 1002 → 1004 → 3001 → 1010", async () => {
    const order = seedExecutionOrder();
    const missing = await begin({ ...order, id: "8a6f1a2b-0000-4000-8000-00000000dead" });
    expect(missing.err_code).toBe(1002);

    const stale = await jsonRequest(
      `/change-orders/${order.id}/execution-attempts`,
      post({}, beginHeaders(order.version + 5)),
    );
    expect(stale.body.err_code).toBe(1004);

    // A non-frozen executor is rejected before the state check (admin gains
    // no execution right, W006).
    const frozenStage = order.stages[0];
    if (frozenStage === undefined) throw new Error("seed stage missing");
    const notExecutor = seedExecutionOrder({
      stages: [
        {
          ...frozenStage,
          execution_actors: [fixtureUser(OTHER_USER)],
        },
      ],
    });
    const forbidden = await begin(notExecutor);
    expect(forbidden.err_code).toBe(3001);

    const wrongState = seedExecutionOrder({ state: "stage_approval_active" });
    const illegal = await begin(wrongState);
    expect(illegal.err_code).toBe(1010);
  });

  it("runs the happy path to completion with committed statement facts", async () => {
    const order = seedExecutionOrder();
    const started = await begin(order);
    expect(started.err_code).toBe(0);
    const attemptId = started.data.id as string;
    expect(started.data.state).toBe("created");
    expect(started.data.send_boundary).toBe("not_started");

    // A live attempt blocks a second begin with 3003 — `order` aliases the
    // world aggregate, so its version already reflects the first begin.
    const racing = await begin(order);
    expect(racing.err_code).toBe(3003);

    await waitTerminal();

    const attempt = await jsonRequest(`/execution-attempts/${attemptId}`);
    expect(attempt.body.data.state).toBe("succeeded");
    expect(attempt.body.data.send_boundary).toBe("sent");

    const statements = await jsonRequest(`/execution-attempts/${attemptId}/statements`);
    const items = statements.body.data.items as Array<Record<string, unknown>>;
    expect(items).toHaveLength(2);
    for (const statement of items) {
      expect(statement.state).toBe("succeeded");
      expect(statement.affected_row_count).not.toBeNull();
    }

    const detail = await jsonRequest(`/change-orders/${order.id}`);
    expect(detail.body.data.state).toBe("completed");
    expect(detail.body.data.terminal_at).not.toBeNull();

    // A terminal attempt with a sent boundary forbids re-begin (E004: the
    // order is done; the only restart is a copied draft).
    const retry = await begin({ ...order, version: 4 });
    expect(retry.err_code).toBe(3004);
  });

  it("keeps prior DDL successes and marks the suffix skipped on partial failure", async () => {
    setScenario("execution-partial");
    const order = seedExecutionOrder();
    const started = await begin(order);
    expect(started.err_code).toBe(0);
    const attemptId = started.data.id as string;
    await waitTerminal();

    const statements = await jsonRequest(`/execution-attempts/${attemptId}/statements`);
    const items = statements.body.data.items as Array<Record<string, unknown>>;
    expect(items).toHaveLength(3);
    expect(items[0]?.state).toBe("succeeded");
    expect(items[1]?.state).toBe("failed");
    expect(items[1]?.failure_name).toBe("lock_wait_timeout");
    expect(items[2]?.state).toBe("skipped");

    const attempt = await jsonRequest(`/execution-attempts/${attemptId}`);
    expect(attempt.body.data.state).toBe("partial_failed");
    expect(attempt.body.data.send_boundary).toBe("sent");

    const detail = await jsonRequest(`/change-orders/${order.id}`);
    expect(detail.body.data.state).toBe("partial_failed");

    // Retry after the send boundary is 3004 — copy to a new draft instead.
    const retry = await begin({ ...order, version: 4 });
    expect(retry.err_code).toBe(3004);
  });

  it("answers 3006 preflight failure without persisting an attempt", async () => {
    setScenario("execution-preflight");
    const order = seedExecutionOrder();
    const started = await begin(order);
    expect(started.err_code).toBe(3006);

    // Nothing persisted: the executor may simply click again (not_started).
    const again = await begin(order);
    expect(again.err_code).toBe(3006);
  });

  it("rejects unknown typed osc override keys with 1001", async () => {
    const order = seedExecutionOrder();
    const { body } = await jsonRequest(
      `/change-orders/${order.id}/execution-attempts`,
      post({ osc_overrides: { free_form: "x" } }, beginHeaders(order.version)),
    );
    expect(body.err_code).toBe(1001);
  });
});

describe("execution cancellation", () => {
  it("is executor-scoped, validated, and idempotent on terminal attempts", async () => {
    setScenario("execution-ghost");
    const order = seedExecutionOrder();
    const started = await begin(order);
    const attemptId = started.data.id as string;
    await new Promise((resolve) => setTimeout(resolve, 1_100)); // running
    const live = await jsonRequest(`/execution-attempts/${attemptId}`);
    const liveVersion = live.body.data.version as number;

    const noReason = await jsonRequest(
      `/execution-attempts/${attemptId}/cancellation`,
      post({ reason: "  " }, { "If-Match": `"${String(liveVersion)}"`, "Idempotency-Key": "c2" }),
    );
    expect(noReason.body.err_code).toBe(1001);

    const wrongVersion = await jsonRequest(
      `/execution-attempts/${attemptId}/cancellation`,
      post({ reason: "Business reason" }, { "If-Match": '"99"', "Idempotency-Key": "c3" }),
    );
    expect(wrongVersion.body.err_code).toBe(1004);

    // The cancel is a request: the attempt first moves to cancelling…
    const accepted = await jsonRequest(
      `/execution-attempts/${attemptId}/cancellation`,
      post({ reason: "Business reason" }, { "If-Match": `"${String(liveVersion)}"`, "Idempotency-Key": "c4" }),
    );
    expect(accepted.body.err_code).toBe(0);
    expect(accepted.body.data.state).toBe("cancelling");

    await new Promise((resolve) => setTimeout(resolve, 900));
    const settled = await jsonRequest(`/execution-attempts/${attemptId}`);
    expect(settled.body.data.state).toBe("cancelled");

    // gh-ost leftover resources surface for cleanup (E006)…
    expect((settled.body.data.osc as Record<string, unknown>).residual_state).toBe("cleanup_required");

    // …the order terminalizes, and a repeat cancel answers idempotently.
    const detail = await jsonRequest(`/change-orders/${order.id}`);
    expect(detail.body.data.state).toBe("cancelled");
    const again = await jsonRequest(
      `/execution-attempts/${attemptId}/cancellation`,
      post({ reason: "Business reason" }, { "If-Match": '"99"', "Idempotency-Key": "c5" }),
    );
    expect(again.body.err_code).toBe(0);

    // A non-frozen executor is rejected before the terminal idempotency —
    // admin gains no cancellation right either (seed swaps the frozen
    // executor set on the same aggregate).
    const swappedStage = order.stages[0];
    if (swappedStage === undefined) throw new Error("seed stage missing");
    seedExecutionOrder({
      stages: [{ ...swappedStage, execution_actors: [fixtureUser(OTHER_USER)] }],
    });
    const forbidden = await jsonRequest(
      `/execution-attempts/${attemptId}/cancellation`,
      post({ reason: "Do not execute" }, { "If-Match": '"1"', "Idempotency-Key": "c1" }),
    );
    expect(forbidden.body.err_code).toBe(3001);
  });
});

describe("manual unknown-result verification", () => {
  async function seedUnknownAttempt(): Promise<{ orderId: string; attemptId: string; version: number }> {
    setScenario("execution-unknown");
    const order = seedExecutionOrder();
    const started = await begin(order);
    const attemptId = started.data.id as string;
    await waitTerminal();
    const attempt = await jsonRequest(`/execution-attempts/${attemptId}`);
    expect(attempt.body.data.state).toBe("result_unknown");
    return { orderId: order.id, attemptId, version: attempt.body.data.version as number };
  }

  it("keeps statement #2 unknown — distinct from not-executed", async () => {
    const { attemptId } = await seedUnknownAttempt();
    const statements = await jsonRequest(`/execution-attempts/${attemptId}/statements`);
    const items = statements.body.data.items as Array<Record<string, unknown>>;
    expect(items[0]?.state).toBe("succeeded");
    expect(items[1]?.state).toBe("unknown");
    expect(items[1]?.started_at).not.toBeNull();
    expect(items[1]?.finished_at).toBeNull();
  });

  it("enforces shape (1001/3012), frozen executor (3001) and state (1010)", async () => {
    const { attemptId, version } = await seedUnknownAttempt();
    const headers = { "If-Match": `"${String(version)}"`, "Idempotency-Key": `v-${String(Math.random())}` };

    const badResult = await jsonRequest(
      `/execution-attempts/${attemptId}/verifications`,
      post({ result: "maybe_ok", reason: "x", evidence: [{ kind: "text", content: "y" }] }, headers),
    );
    expect(badResult.body.err_code).toBe(1001);

    const noEvidence = await jsonRequest(
      `/execution-attempts/${attemptId}/verifications`,
      post({ result: "confirmed_failed", reason: "Data rolled back", evidence: [] }, headers),
    );
    expect(noEvidence.body.err_code).toBe(3012);

    const noReason = await jsonRequest(
      `/execution-attempts/${attemptId}/verifications`,
      post({ result: "confirmed_failed", reason: "", evidence: [{ kind: "text", content: "y" }] }, headers),
    );
    expect(noReason.body.err_code).toBe(1001);

    // A non-frozen executor is rejected before the state guard (seed swaps
    // the frozen executor set on the same aggregate).
    seedExecutionOrder({
      state: "result_unknown",
      stages: [
        {
          id: "8a6f1a2b-0000-4000-8000-00000000cd11",
          position: 1,
          datasource_name: "orders-mysql",
          state: "result_unknown",
          approval_steps: [],
          execution_actors: [fixtureUser(OTHER_USER)],
        },
      ],
    });
    const notExecutor = await jsonRequest(
      `/execution-attempts/${attemptId}/verifications`,
      post(
        { result: "confirmed_failed", reason: "Data rolled back", evidence: [{ kind: "text", content: "y" }] },
        { "If-Match": `"${String(version)}"`, "Idempotency-Key": `v-${String(Math.random())}` },
      ),
    );
    expect(notExecutor.body.err_code).toBe(3001);

    // A verification on a non-unknown attempt is illegal (1010).
    setScenario("ready");
    const happyOrder = seedExecutionOrder({}, "ce");
    const happy = await begin(happyOrder);
    const happyId = happy.data.id as string;
    await waitTerminal();
    const onTerminal = await jsonRequest(
      `/execution-attempts/${happyId}/verifications`,
      post(
        { result: "confirmed_failed", reason: "Data rolled back", evidence: [{ kind: "text", content: "y" }] },
        { "If-Match": '"1"', "Idempotency-Key": `v-${String(Math.random())}` },
      ),
    );
    expect(onTerminal.body.err_code).toBe(1010);
  });

  it("terminalizes the order on the first fixed verdict and bars a second", async () => {
    const { orderId, attemptId, version } = await seedUnknownAttempt();
    const verify = (result: string, ifMatch: string): Promise<{ status: number; body: Envelope<Record<string, unknown>> }> =>
      jsonRequest(
        `/execution-attempts/${attemptId}/verifications`,
        post(
          {
            result,
            reason: "Replica SHOW SLAVE STATUS confirmed zero replication lag and expected row count",
            evidence: [{ kind: "database_fact", content: "SELECT COUNT(*) = 5 verified" }],
          },
          { "If-Match": ifMatch, "Idempotency-Key": `v-${String(Math.random())}` },
        ),
      );

    // still_unknown keeps the block in place.
    const stillUnknown = await verify("still_unknown", `"${String(version)}"`);
    expect(stillUnknown.body.err_code).toBe(0);
    const afterStill = await jsonRequest(`/change-orders/${orderId}`);
    expect(afterStill.body.data.state).toBe("result_unknown");

    // confirmed_succeeded terminalizes with the manual-verification marker;
    // the attempt version is untouched by verification (backend verify.go).
    const succeeded = await verify("confirmed_succeeded", `"${String(version)}"`);
    expect(succeeded.body.err_code).toBe(0);
    const detail = await jsonRequest(`/change-orders/${orderId}`);
    expect(detail.body.data.state).toBe("completed");
    expect(detail.body.data.manually_verified).toBe(true);

    // The attempt row stays result_unknown; a second verdict hits the order
    // guard (1010) — the outcome is frozen (E005).
    const second = await verify("confirmed_failed", `"${String(version)}"`);
    expect(second.body.err_code).toBe(1010);
  });
});

describe("deferred execution schedules", () => {
  it("enforces executor authorization, state and the 5-minute window", async () => {
    const order = seedExecutionOrder();

    // Authorization first (backend order): a non-frozen executor answers
    // 3001 before any range validation.
    const swappedStage = order.stages[0];
    if (swappedStage === undefined) throw new Error("seed stage missing");
    seedExecutionOrder({
      stages: [{ ...swappedStage, execution_actors: [fixtureUser(OTHER_USER)] }],
    });
    const forbidden = await jsonRequest(
      `/change-orders/${order.id}/execution-schedules`,
      post({ scheduled_for: new Date(Date.now() + 3600_000).toISOString() }, beginHeaders(order.version)),
    );
    expect(forbidden.body.err_code).toBe(3001);

    // Restore the owner-executor aggregate for the range and success flow.
    const fresh = seedExecutionOrder();
    const headers = beginHeaders(fresh.version);
    void order;

    const tooSoon = await jsonRequest(
      `/change-orders/${fresh.id}/execution-schedules`,
      post({ scheduled_for: new Date(Date.now() + 60_000).toISOString() }, headers),
    );
    expect(tooSoon.body.err_code).toBe(3007);

    const tooFar = await jsonRequest(
      `/change-orders/${fresh.id}/execution-schedules`,
      post({ scheduled_for: new Date(Date.now() + 31 * 24 * 3600_000).toISOString() }, headers),
    );
    expect(tooFar.body.err_code).toBe(3007);

    const created = await jsonRequest(
      `/change-orders/${fresh.id}/execution-schedules`,
      post({ scheduled_for: new Date(Date.now() + 3600_000).toISOString() }, headers),
    );
    expect(created.body.err_code).toBe(0);
    expect(created.body.data.state).toBe("scheduled");

    // The order waits in scheduled for the due-time claim (E007).
    const detail = await jsonRequest(`/change-orders/${fresh.id}`);
    expect(detail.body.data.state).toBe("scheduled");

    // From `scheduled` the state guard precedes the live-facts check (backend
    // createScheduleOnce order), so a duplicate schedule and a re-begin both
    // answer 1010 — `fresh` aliases the world aggregate, so its version
    // already reflects the successful schedule.
    const duplicate = await jsonRequest(
      `/change-orders/${fresh.id}/execution-schedules`,
      post({ scheduled_for: new Date(Date.now() + 7200_000).toISOString() }, beginHeaders(fresh.version)),
    );
    expect(duplicate.body.err_code).toBe(1010);
    const reBegin = await begin(fresh);
    expect(reBegin.err_code).toBe(1010);
  });

  it("seeds the missed-schedule scenario as a terminal, non-catching-up order", async () => {
    setScenario("schedule-missed");
    // The scenario seeder runs inside the list handler.
    const list = await jsonRequest("/change-orders?limit=50");
    const items = list.body.data.items as Array<Record<string, unknown>>;
    const missed = items.find((item) => item.display_number === "YR-20260830-000101");
    expect(missed).toBeDefined();
    expect(missed?.state).toBe("missed_schedule");
    expect(missed?.terminal_at).not.toBeNull();
  });
});

describe("execution handler edge branches", () => {
  it("tolerates a bodyless begin and rejects unknown attempt reads", async () => {
    const order = seedExecutionOrder();
    const headers = beginHeaders(order.version);
    const bodyless = await fetch(`https://yearning.test/api/v4/change-orders/${order.id}/execution-attempts`, {
      method: "POST",
      headers,
    });
    const bodylessEnvelope = (await bodyless.json()) as Envelope<Record<string, unknown>>;
    expect(bodylessEnvelope.err_code).toBe(0);

    await waitTerminal();
    const attemptId = bodylessEnvelope.data.id as string;
    const missingAttempt = await jsonRequest(`/execution-attempts/8a6f1a2b-0000-4000-8000-00000000dead`);
    expect(missingAttempt.body.err_code).toBe(1002);
    const missingStatements = await jsonRequest(
      `/execution-attempts/8a6f1a2b-0000-4000-8000-00000000dead/statements`,
    );
    expect(missingStatements.body.err_code).toBe(1002);
    void attemptId;
  });

  it("answers 1001 for a malformed schedule timestamp and an empty verification body", async () => {
    const order = seedExecutionOrder();
    const headers = beginHeaders(order.version);
    const badTime = await jsonRequest(
      `/change-orders/${order.id}/execution-schedules`,
      post({ scheduled_for: "not-a-time" }, headers),
    );
    expect(badTime.body.err_code).toBe(1001);

    const attempt = await begin(order);
    const attemptId = attempt.data.id as string;
    await waitTerminal();
    // The scenario terminalizes as succeeded; the order leaves result_unknown
    // so a verification body would fail on state anyway — send no body at all
    // to pin the shape guard branch.
    const noBody = await fetch(`https://yearning.test/api/v4/execution-attempts/${attemptId}/verifications`, {
      method: "POST",
      headers: { "If-Match": '"1"', "Idempotency-Key": "v-empty" },
    });
    const noBodyEnvelope = (await noBody.json()) as Envelope<Record<string, unknown>>;
    expect(noBodyEnvelope.err_code).toBe(1001);
  });
});
