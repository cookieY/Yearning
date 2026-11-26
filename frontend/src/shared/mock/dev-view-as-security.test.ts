import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";
import { setMockAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { DEV_VIEW_AS_HEADER } from "@/shared/dev/dev-view-as";
import {
  MOCK_IDENTITIES,
  identityUsernameOf,
  setMockIdentity,
} from "@/shared/mock/identity-store";
import {
  FIXTURE_OWNER_ID,
  resetReviewFixture,
  seedFixtureOrder,
  type FixtureOrder,
} from "@/shared/mock/review-fixture";
import { resetQueryFixture, seedQueryScenario } from "@/shared/mock/query-fixture";

const OWNER = MOCK_IDENTITIES.owner;
const REVIEWER = MOCK_IDENTITIES["reviewer-a"];
const EXECUTOR = MOCK_IDENTITIES["executor-a"];
const AUTH_HEADERS = { cookie: "yearning_session=test-session" };

function buildOrder(
  id: string,
  state: "stage_approval_active" | "stage_execution_pending",
): FixtureOrder {
  return {
    id,
    display_number: state === "stage_approval_active" ? "YR-20261125-000001" : "YR-20261125-000002",
    submitter_user_id: FIXTURE_OWNER_ID,
    requested_scheduled_for: null,
    title: `Security check ${state}`,
    state,
    current_stage_position: 1,
    stages: [
      {
        id: `${id.slice(0, 34)}11`,
        position: 1,
        datasource_name: "orders-mysql",
        state: state === "stage_approval_active" ? "approval_active" : "execution_pending",
        approval_steps: [
          {
            id: `${id.slice(0, 34)}21`,
            position: 1,
            state: state === "stage_approval_active" ? "active" : "approved",
            decided_at: state === "stage_approval_active" ? null : "2026-11-25T00:00:00Z",
            actors: [
              { id: REVIEWER.id, username: REVIEWER.username, display_name: REVIEWER.username },
              { id: OWNER.id, username: OWNER.username, display_name: OWNER.username },
            ],
          },
        ],
        execution_actors: [EXECUTOR, OWNER].map((actor) => ({
          id: actor.id,
          username: actor.username,
          display_name: actor.username,
          email: null,
          is_builtin_admin: false,
          version: 1,
          created_at: "2026-08-01T00:00:00Z",
          updated_at: "2026-08-01T00:00:00Z",
        })),
      },
    ],
    has_sql: true,
    sql_hash: "hash-sec-1",
    snapshot_hash: "snap-sec-1",
    manually_verified: false,
    version: 1,
    submitted_at: "2026-11-25T00:00:00Z",
    terminal_at: null,
    review_run_id: null,
    sql_text: "UPDATE orders SET status = 1 WHERE user_id = 42;",
  };
}

const APPROVAL_ORDER_ID = "8b6f1a2b-0000-4000-8000-000000001d01";
const EXECUTION_ORDER_ID = "8b6f1a2b-0000-4000-8000-000000001d02";

afterEach(() => {
  setMockIdentity("henry");
  setMockAuthBehavior("default");
  resetQueryFixture();
});

beforeEach(() => {
  resetAdminFixture();
  resetReviewFixture();
  resetQueryFixture();
  setMockAuthBehavior("default");
  seedFixtureOrder(buildOrder(APPROVAL_ORDER_ID, "stage_approval_active"));
  seedFixtureOrder(buildOrder(EXECUTION_ORDER_ID, "stage_execution_pending"));
});

describe("D9 real owner identity & D18 dev view-as security", () => {
  it("gives real all-role access to owner by default and downgrades cleanly via X-Yearning-Dev-View-As", async () => {
    setMockIdentity("owner");

    const meRes = await fetch("http://localhost/api/v4/users/me", { headers: AUTH_HEADERS });
    const meJson = (await meRes.json()) as {
      data: { username: string; is_builtin_admin: boolean; can_access_admin: boolean };
    };
    expect(meJson.data.username).toBe("owner");
    expect(meJson.data.is_builtin_admin).toBe(true);
    expect(meJson.data.can_access_admin).toBe(true);

    const adminOk = await fetch("http://localhost/api/v4/admin/users");
    expect(adminOk.status).toBe(200);

    for (const downgrade of ["user", "approver", "executor"] as const) {
      const meDownRes = await fetch("http://localhost/api/v4/users/me", {
        headers: { ...AUTH_HEADERS, [DEV_VIEW_AS_HEADER]: downgrade },
      });
      const meDownJson = (await meDownRes.json()) as {
        data: { is_builtin_admin: boolean; can_access_admin: boolean };
      };
      expect(meDownJson.data.is_builtin_admin).toBe(false);
      expect(meDownJson.data.can_access_admin).toBe(false);

      const adminBlocked = await fetch("http://localhost/api/v4/admin/users", {
        headers: { [DEV_VIEW_AS_HEADER]: downgrade },
      });
      expect(adminBlocked.status).toBe(403);
    }

    // Downgraded from approval when view-as=executor or user; succeeds under approver.
    const approveBlocked = await fetch(
      `http://localhost/api/v4/change-orders/${APPROVAL_ORDER_ID}/approval-decisions`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          [DEV_VIEW_AS_HEADER]: "executor",
        },
        body: JSON.stringify({ decision: "approve", comment: "blocked by executor view-as" }),
      },
    );
    expect(((await approveBlocked.json()) as { err_code: number }).err_code).toBe(3001);

    const approveAllowed = await fetch(
      `http://localhost/api/v4/change-orders/${APPROVAL_ORDER_ID}/approval-decisions`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          [DEV_VIEW_AS_HEADER]: "approver",
        },
        body: JSON.stringify({ decision: "approve", comment: "allowed under approver view-as" }),
      },
    );
    expect(((await approveAllowed.json()) as { err_code: number }).err_code).toBe(0);

    // Downgraded from execution when view-as=approver or user; succeeds under executor.
    const execBlocked = await fetch(
      `http://localhost/api/v4/change-orders/${EXECUTION_ORDER_ID}/execution-attempts`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          [DEV_VIEW_AS_HEADER]: "approver",
        },
        body: JSON.stringify({}),
      },
    );
    expect(((await execBlocked.json()) as { err_code: number }).err_code).toBe(3001);

    const execAllowed = await fetch(
      `http://localhost/api/v4/change-orders/${EXECUTION_ORDER_ID}/execution-attempts`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          [DEV_VIEW_AS_HEADER]: "executor",
        },
        body: JSON.stringify({}),
      },
    );
    expect(((await execAllowed.json()) as { err_code: number }).err_code).toBe(0);
  });

  it("strips downgraded role actor slots from MSW order and query projections per View-As preset", async () => {
    setMockIdentity("owner");
    const OWNER_SELF_ORDER_ID = "8b6f1a2b-0000-4000-8000-000000001d03";
    seedFixtureOrder({
      ...buildOrder(OWNER_SELF_ORDER_ID, "stage_approval_active"),
      submitter_user_id: OWNER.id,
    });

    interface ProjectedOrder {
      id: string;
      submitter_user_id: string;
      stages: Array<{
        approval_steps: Array<{ actors: Array<{ id: string }> }>;
        execution_actors: Array<{ id: string }>;
      }>;
    }

    const extractActors = (order: ProjectedOrder) => ({
      approvalIds: order.stages.flatMap((stage) =>
        stage.approval_steps.flatMap((step) => step.actors.map((actor) => actor.id)),
      ),
      execIds: order.stages.flatMap((stage) => stage.execution_actors.map((actor) => actor.id)),
    });

    const expectSlotVisibility = async (
      viewAs: "none" | "user" | "approver" | "executor",
      wantApprovalSlot: boolean,
      wantExecutionSlot: boolean,
    ) => {
      const headers: Record<string, string> =
        viewAs === "none" ? { ...AUTH_HEADERS } : { ...AUTH_HEADERS, [DEV_VIEW_AS_HEADER]: viewAs };

      for (const [orderId, expectSubmitter] of [
        [APPROVAL_ORDER_ID, FIXTURE_OWNER_ID],
        [EXECUTION_ORDER_ID, FIXTURE_OWNER_ID],
        [OWNER_SELF_ORDER_ID, OWNER.id],
      ] as const) {
        const detailRes = await fetch(`http://localhost/api/v4/change-orders/${orderId}`, { headers });
        const detailJson = (await detailRes.json()) as { data: ProjectedOrder };
        expect(detailJson.data.submitter_user_id).toBe(expectSubmitter);
        const { approvalIds, execIds } = extractActors(detailJson.data);
        expect(approvalIds.includes(OWNER.id)).toBe(wantApprovalSlot);
        expect(execIds.includes(OWNER.id)).toBe(wantExecutionSlot);
      }

      const listRes = await fetch("http://localhost/api/v4/change-orders?limit=100", { headers });
      const listJson = (await listRes.json()) as { data: { items: ProjectedOrder[] } };
      // Non-self order disappears from owner's list when all acting slots are stripped (view-as=user).
      const listNonSelf = listJson.data.items.find((item) => item.id === APPROVAL_ORDER_ID);
      expect(listNonSelf !== undefined).toBe(wantApprovalSlot || wantExecutionSlot);
      if (listNonSelf !== undefined) {
        const { approvalIds, execIds } = extractActors(listNonSelf);
        expect(approvalIds.includes(OWNER.id)).toBe(wantApprovalSlot);
        expect(execIds.includes(OWNER.id)).toBe(wantExecutionSlot);
      }
      // Submitter's own order remains visible even when downgraded to plain user, with slots stripped.
      const listSelf = listJson.data.items.find((item) => item.id === OWNER_SELF_ORDER_ID);
      expect(listSelf).toBeDefined();
      expect(listSelf?.submitter_user_id).toBe(OWNER.id);
      if (listSelf !== undefined) {
        const { approvalIds, execIds } = extractActors(listSelf);
        expect(approvalIds.includes(OWNER.id)).toBe(wantApprovalSlot);
        expect(execIds.includes(OWNER.id)).toBe(wantExecutionSlot);
      }
    };

    await expectSlotVisibility("none", true, true);
    await expectSlotVisibility("user", false, false);
    await expectSlotVisibility("approver", true, false);
    await expectSlotVisibility("executor", false, true);

    // Seeded query access requests & grants belong to henry (submitter) with owner in reviewer actors;
    // executor/user view-as must hide non-self rows while approver/none retain them.
    seedQueryScenario("query-session");
    for (const viewAs of ["none", "approver", "executor", "user"] as const) {
      const headers: Record<string, string> =
        viewAs === "none" ? { ...AUTH_HEADERS } : { ...AUTH_HEADERS, [DEV_VIEW_AS_HEADER]: viewAs };
      const reqRes = await fetch("http://localhost/api/v4/query-access-requests?limit=50", { headers });
      const reqJson = (await reqRes.json()) as { data: { items: Array<{ requester_user_id: string }> } };
      const grantRes = await fetch("http://localhost/api/v4/query-grants?limit=50", { headers });
      const grantJson = (await grantRes.json()) as { data: { items: Array<{ requester_user_id: string }> } };
      const hasNonSelfReq = reqJson.data.items.some((row) => row.requester_user_id !== OWNER.id);
      const hasNonSelfGrant = grantJson.data.items.some((row) => row.requester_user_id !== OWNER.id);
      const expectedVisible = viewAs === "none" || viewAs === "approver";
      expect(hasNonSelfReq).toBe(expectedVisible);
      expect(hasNonSelfGrant).toBe(expectedVisible);
    }
  });

  it("never escalates privileges when henry sends X-Yearning-Dev-View-As presets", async () => {
    setMockIdentity("henry");

    for (const attempted of ["admin", "approver", "executor", "owner"]) {
      const meRes = await fetch("http://localhost/api/v4/users/me", {
        headers: { ...AUTH_HEADERS, [DEV_VIEW_AS_HEADER]: attempted },
      });
      const meJson = (await meRes.json()) as {
        data: { username: string; is_builtin_admin: boolean; can_access_admin: boolean };
      };
      expect(meJson.data.username).toBe("henry");
      expect(meJson.data.is_builtin_admin).toBe(false);
      expect(meJson.data.can_access_admin).toBe(false);

      const adminRes = await fetch("http://localhost/api/v4/admin/users", {
        headers: { [DEV_VIEW_AS_HEADER]: attempted },
      });
      expect(adminRes.status).toBe(403);
    }

    const escalatedApprove = await fetch(
      `http://localhost/api/v4/change-orders/${APPROVAL_ORDER_ID}/approval-decisions`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          [DEV_VIEW_AS_HEADER]: "approver",
        },
        body: JSON.stringify({ decision: "approve", comment: "should fail for henry" }),
      },
    );
    expect(((await escalatedApprove.json()) as { err_code: number }).err_code).toBe(3001);

    const escalatedExec = await fetch(
      `http://localhost/api/v4/change-orders/${EXECUTION_ORDER_ID}/execution-attempts`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          [DEV_VIEW_AS_HEADER]: "executor",
        },
        body: JSON.stringify({}),
      },
    );
    expect(((await escalatedExec.json()) as { err_code: number }).err_code).toBe(3001);
  });

  it("formats unknown identity ids explicitly instead of falling back to henry", () => {
    expect(identityUsernameOf("nonexistent-id")).toBe("unknown-nonexistent-id");
    expect(identityUsernameOf("0198d9cc-e65d-7b9d-a8aa-3c81945f99ad")).toBe("owner");
  });
});
