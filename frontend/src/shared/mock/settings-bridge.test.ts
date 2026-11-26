import { afterEach, describe, expect, it } from "vitest";
import {
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
  type FixtureOrder,
} from "@/shared/mock/review-fixture";
import {
  QUERY_FIXTURE_DS_MYSQL_ID,
  QUERY_FIXTURE_FLOW_ID,
  resetQueryFixture,
  seedQueryScenario,
} from "@/shared/mock/query-fixture";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";
import { MOCK_IDENTITIES, setMockIdentity } from "@/shared/mock/identity-store";
import { resetSettingsBridge } from "@/shared/mock/settings-bridge";

/**
 * §18.47 settings → domain bridge: the admin settings PUT must actually
 * change domain behavior. Query approval off opens the Q002 direct path
 * (session without a grant), and the execution window restriction rejects
 * begins and schedules with OUTSIDE_EXECUTION_WINDOW (3008) exactly like
 * the backend preflight.
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
  const response = await fetch(
    `https://yearning.test${path.startsWith("/api/v4") ? path : `/api/v4${path}`}`,
    init,
  );
  return { status: response.status, body: (await response.json()) as Envelope<T> };
}

const post = (body: unknown, ifMatch?: string): RequestInit => ({
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    "cookie": "yearning_session=mock-session-token",
    ...(ifMatch === undefined ? {} : { "If-Match": `"${ifMatch}"` }),
  },
  body: JSON.stringify(body),
});

/** High-impact saves need the one-time impact token (assess → PUT). */
async function putSettings(path: string, settings: unknown, version: string): Promise<{ status: number; body: Envelope<Record<string, unknown>> }> {
  window.localStorage.setItem("yearning-mock-auth", "admin");
  const assessed = await call<{ impact_token: string }>(`/admin/settings/${path}/impact-assessments`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ settings }),
  });
  expect(assessed.body.err_code).toBe(0);
  const token = assessed.body.data.impact_token;
  return call(`/admin/settings/${path}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": `"${version}"` },
    body: JSON.stringify({ settings, impact_token: token }),
  });
}

const REVIEWER = MOCK_IDENTITIES["reviewer-a"];
const EXECUTOR = MOCK_IDENTITIES["executor-a"];

function executionPendingOrder(id: string, stepId: string): FixtureOrder {
  return {
    id,
    display_number: id,
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: `Window verification order ${id.slice(-4)}`,
    state: "stage_execution_pending",
    current_stage_position: 1,
    stages: [
      {
        id: `${id.slice(0, 34)}a1`,
        position: 1,
        datasource_name: "orders-mysql",
        state: "execution_pending",
        approval_steps: [
          {
            id: `${id.slice(0, 34)}b1`,
            position: 1,
            state: "approved",
            decided_at: "2026-10-14T00:00:00Z",
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
    sql_hash: `hash-${stepId}`,
    snapshot_hash: `snap-${stepId}`,
    manually_verified: false,
    version: 1,
    submitted_at: "2026-10-14T00:00:00Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "UPDATE orders SET status = 1;",
  };
}

afterEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "default");
  setMockIdentity("henry");
  resetReviewFixture();
  resetQueryFixture();
  resetAdminFixture();
  resetSettingsBridge();
});

describe("settings bridge (§18.47)", () => {
  it("turning query approval off opens the Q002 direct session path", async () => {
    seedQueryScenario("query-session");
    const sessionBody = {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
    };

    // Approval on: a grant-less attempt is rejected (4001 grant required).
    const gated = await call("/query-sessions", post(sessionBody));
    expect(gated.body.err_code).toBe(4001);

    // The admin settings PUT flips the knob through the bridge.
    const saved = await putSettings("query", { approval_enabled: false }, "1");
    expect(saved.body.err_code).toBe(0);

    // Approval off: the same attempt creates the session directly (Q002).
    window.localStorage.setItem("yearning-mock-auth", "default");
    const direct = await call<{ id: string }>("/query-sessions", post(sessionBody));
    expect(direct.body.err_code).toBe(0);
    expect(direct.body.data.id).not.toBe("");
  });

  it("the execution window restriction rejects begins with 3008 and reopens inside a window", async () => {
    const orderA = executionPendingOrder("9b7f1a2b-0000-4000-8000-000000007001", "w1");
    seedFixtureOrder(orderA);
    setMockIdentity("executor-a");

    const enabled = await putSettings("execution", { restriction_enabled: true, windows: [] }, "1");
    expect(enabled.body.err_code).toBe(0);

    // Enabled with no windows rejects everything (backend loop semantics).
    const blocked = await call(`/change-orders/${orderA.id}/execution-attempts`, post({}, "1"));
    expect(blocked.body.err_code).toBe(3008);

    // A covering window reopens execution immediately.
    const everyDay = ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"];
    await putSettings("execution", {
      restriction_enabled: true,
      windows: [{ days_of_week: everyDay, start_local_time: "00:00", end_local_time: "23:59" }],
    }, "2");
    const allowed = await call(`/change-orders/${orderA.id}/execution-attempts`, post({}, "1"));
    expect(allowed.body.err_code).toBe(0);
  });

  it("schedules outside the window are rejected with 3008 (checkExecutionWindowAt)", async () => {
    const orderB = executionPendingOrder("9b7f1a2b-0000-4000-8000-000000007002", "w2");
    seedFixtureOrder(orderB);
    setMockIdentity("executor-a");

    await putSettings("execution", { restriction_enabled: true, windows: [] }, "1");
    window.localStorage.setItem("yearning-mock-auth", "default");

    const due = new Date(Date.now() + 60 * 60 * 1000).toISOString();
    const refused = await call(`/change-orders/${orderB.id}/execution-schedules`, post({ scheduled_for: due }, "1"));
    expect(refused.body.err_code).toBe(3008);

    // Restriction off again: the same schedule is accepted (3007 range ok).
    await putSettings("execution", { restriction_enabled: false, windows: [] }, "2");
    window.localStorage.setItem("yearning-mock-auth", "default");
    const accepted = await call(`/change-orders/${orderB.id}/execution-schedules`, post({ scheduled_for: due }, "1"));
    expect(accepted.body.err_code).toBe(0);
  });
});
