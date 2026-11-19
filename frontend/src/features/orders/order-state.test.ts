import { describe, expect, it } from "vitest";
import type { ChangeOrder } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  activeApprovalStepFor,
  approvalStepTone,
  executionAttemptTone,
  frozenExecutorStageFor,
  myApprovalStepFor,
  scheduledStageFor,
  stageStateTone,
  statementStateTone,
} from "./order-state";
import { FIXTURE_OWNER_ID } from "@/shared/mock/review-fixture";

/**
 * Pure presentation derivations for the execution workspace (FE-F8): the
 * frozen-executor gate mirrors W006 (admin gains no execution right — the
 * membership check is against the frozen stage snapshot only), and the tone
 * mappings keep `unknown` visually distinct from `not_started` (E005).
 */

function orderWithExecutor(
  overrides: Partial<ChangeOrder> = {},
  executorIds: string[] = [FIXTURE_OWNER_ID],
): ChangeOrder {
  const user = (id: string) => ({
    id,
    username: "u",
    display_name: "u",
    email: null,
    is_builtin_admin: false,
    version: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  });
  return {
    id: "aa6f1a2b-0000-4000-8000-00000000aa01",
    display_number: "YR-20260830-000501",
    submitter_user_id: FIXTURE_OWNER_ID,
    state: "stage_execution_pending",
    stages: [
      {
        id: "aa6f1a2b-0000-4000-8000-00000000aa11",
        position: 1,
        datasource_name: "orders-mysql",
        state: "execution_pending",
        approval_steps: [],
        execution_actors: executorIds.map(user),
      },
    ],
    has_sql: true,
    version: 1,
    submitted_at: "2026-08-30T11:00:00Z",
    ...overrides,
  } as unknown as ChangeOrder;
}

describe("frozenExecutorStageFor", () => {
  it("returns the execution_pending stage only for a frozen member of it", () => {
    const order = orderWithExecutor();
    const stage = frozenExecutorStageFor(order, FIXTURE_OWNER_ID);
    expect(stage?.id).toBe("aa6f1a2b-0000-4000-8000-00000000aa11");
  });

  it("rejects non-members, anonymous sessions and non-waiting states", () => {
    const order = orderWithExecutor({}, ["1111d9cc-e65d-7b9d-a8aa-3c81945f99ac"]);
    expect(frozenExecutorStageFor(order, FIXTURE_OWNER_ID)).toBeNull();
    expect(frozenExecutorStageFor(order, undefined)).toBeNull();
    expect(
      frozenExecutorStageFor(
        orderWithExecutor({ state: "stage_approval_active" }),
        FIXTURE_OWNER_ID,
      ),
    ).toBeNull();
    // A stage no longer waiting (already scheduled) yields no executor stage.
    const scheduledOrder = orderWithExecutor();
    const waitingStage = scheduledOrder.stages[0];
    if (waitingStage === undefined) throw new Error("seed stage missing");
    waitingStage.state = "scheduled";
    expect(frozenExecutorStageFor(scheduledOrder, FIXTURE_OWNER_ID)).toBeNull();
  });
});

describe("execution tone mappings", () => {
  it("tones attempt states: terminal outcomes, live progress and unknown", () => {
    expect(executionAttemptTone("succeeded")).toBe("emerald");
    expect(executionAttemptTone("failed")).toBe("red");
    expect(executionAttemptTone("partial_failed")).toBe("rose");
    expect(executionAttemptTone("result_unknown")).toBe("orange");
    expect(executionAttemptTone("cancelled")).toBe("stone");
    expect(executionAttemptTone("partial_cancelled")).toBe("rose");
    expect(executionAttemptTone("created")).toBe("indigo");
    expect(executionAttemptTone("preflight")).toBe("indigo");
    expect(executionAttemptTone("running")).toBe("indigo");
    expect(executionAttemptTone("cancelling")).toBe("indigo");
    expect(executionAttemptTone("mystery")).toBe("stone");
  });

  it("tones statement states: unknown is orange, skipped and not_started stone", () => {
    expect(statementStateTone("succeeded")).toBe("emerald");
    expect(statementStateTone("failed")).toBe("red");
    expect(statementStateTone("unknown")).toBe("orange");
    expect(statementStateTone("cancelled")).toBe("stone");
    expect(statementStateTone("sent")).toBe("sky");
    expect(statementStateTone("not_started")).toBe("stone");
    expect(statementStateTone("skipped")).toBe("stone");
    expect(statementStateTone("mystery")).toBe("stone");
  });
});

describe("stageStateTone", () => {
  it("maps every StageState value to its tone family", () => {
    // Exhaustive over the generated StageState literal set: an added state
    // must be given a tone here or TypeScript flags the switch.
    expect(stageStateTone("succeeded")).toBe("emerald");
    expect(stageStateTone("failed")).toBe("red");
    expect(stageStateTone("partial_failed")).toBe("rose");
    expect(stageStateTone("result_unknown")).toBe("orange");
    expect(stageStateTone("partial_cancelled")).toBe("rose");
    expect(stageStateTone("pending")).toBe("waiting");
    expect(stageStateTone("skipped")).toBe("stone");
    expect(stageStateTone("cancelled")).toBe("stone");
    expect(stageStateTone("approval_active")).toBe("violet");
    expect(stageStateTone("execution_pending")).toBe("waiting");
    expect(stageStateTone("scheduled")).toBe("teal");
    expect(stageStateTone("running")).toBe("indigo");
  });
});

describe("approvalStepTone", () => {
  it("maps decision states and defaults unknown spellings to stone", () => {
    expect(approvalStepTone("approved")).toBe("emerald");
    expect(approvalStepTone("rejected")).toBe("red");
    expect(approvalStepTone("invalid")).toBe("orange");
    expect(approvalStepTone("active")).toBe("violet");
    expect(approvalStepTone("pending")).toBe("waiting");
    expect(approvalStepTone("mystery")).toBe("stone");
  });
});

function approvalStep(
  id: string,
  position: number,
  state: string,
  actorIds: string[],
): Record<string, unknown> {
  return {
    id,
    position,
    state,
    actors: actorIds.map((actorId) => ({ id: actorId, username: "u" })),
  };
}

function approvalOrder(
  steps: unknown[],
  overrides: Partial<ChangeOrder> = {},
): ChangeOrder {
  return {
    ...orderWithExecutor({ state: "stage_approval_active", ...overrides }),
    stages: [
      {
        id: "aa6f1a2b-0000-4000-8000-00000000aa21",
        position: 1,
        datasource_name: "orders-mysql",
        state: "approval_active",
        approval_steps: steps,
        execution_actors: [],
      },
    ],
  } as unknown as ChangeOrder;
}

describe("activeApprovalStepFor", () => {
  it("returns the live step whose frozen actors include the session user", () => {
    const order = approvalOrder([
      approvalStep("st-1", 1, "approved", [FIXTURE_OWNER_ID]),
      approvalStep("st-2", 2, "active", [FIXTURE_OWNER_ID]),
    ]);
    const step = activeApprovalStepFor(order, FIXTURE_OWNER_ID);
    expect(step).toMatchObject({
      stepId: "st-2",
      stepPosition: 2,
      stagePosition: 1,
      datasourceName: "orders-mysql",
      actorCount: 1,
    });
  });

  it("returns null for anonymous sessions, other order states and non-active steps", () => {
    const order = approvalOrder([approvalStep("st-1", 1, "pending", [FIXTURE_OWNER_ID])]);
    expect(activeApprovalStepFor(order, undefined)).toBeNull();
    expect(
      activeApprovalStepFor(
        approvalOrder([approvalStep("st-1", 1, "active", [FIXTURE_OWNER_ID])], {
          state: "stage_execution_pending",
        }),
        FIXTURE_OWNER_ID,
      ),
    ).toBeNull();
    // An active order whose steps are not active yet yields nothing.
    expect(activeApprovalStepFor(order, FIXTURE_OWNER_ID)).toBeNull();
    // A step that skips a non-approval stage segment never matches either.
    const otherStage = approvalOrder([
      approvalStep("st-1", 1, "active", [FIXTURE_OWNER_ID]),
    ]);
    const otherStageView = otherStage.stages[0];
    if (otherStageView === undefined) throw new Error("seed stage missing");
    otherStageView.state = "pending";
    expect(activeApprovalStepFor(otherStage, FIXTURE_OWNER_ID)).toBeNull();
  });
});

describe("myApprovalStepFor", () => {
  it("prefers an active step and otherwise returns the first participated step", () => {
    const active = approvalOrder([
      approvalStep("st-1", 1, "approved", [FIXTURE_OWNER_ID]),
      approvalStep("st-2", 2, "active", [FIXTURE_OWNER_ID]),
    ]);
    expect(myApprovalStepFor(active, FIXTURE_OWNER_ID)?.stepId).toBe("st-2");
    expect(myApprovalStepFor(active, FIXTURE_OWNER_ID)?.stepState).toBe("active");

    // §18.58: on a decided order the queue still shows my participation.
    const decided = approvalOrder([
      approvalStep("st-1", 1, "rejected", [FIXTURE_OWNER_ID]),
      approvalStep("st-2", 2, "pending", [FIXTURE_OWNER_ID]),
    ]);
    expect(myApprovalStepFor(decided, FIXTURE_OWNER_ID)?.stepId).toBe("st-1");
    expect(myApprovalStepFor(decided, FIXTURE_OWNER_ID)?.stepState).toBe("rejected");
  });

  it("returns null for anonymous sessions and users outside every frozen step", () => {
    const order = approvalOrder([approvalStep("st-1", 1, "active", ["someone-else"])]);
    expect(myApprovalStepFor(order, undefined)).toBeNull();
    expect(myApprovalStepFor(order, FIXTURE_OWNER_ID)).toBeNull();
  });
});

describe("scheduledStageFor", () => {
  it("returns the scheduled stage for any frozen executor, including the pending spelling", () => {
    // Both spellings the read model can carry: the mock world marks the
    // stage `scheduled`; the backend read model keeps it execution_pending.
    const scheduled = orderWithExecutor({ state: "scheduled" });
    const scheduledStage = scheduled.stages[0];
    if (scheduledStage === undefined) throw new Error("seed stage missing");
    scheduledStage.state = "scheduled";
    expect(scheduledStageFor(scheduled, FIXTURE_OWNER_ID)?.id).toBe(
      "aa6f1a2b-0000-4000-8000-00000000aa11",
    );

    const pendingSpelling = orderWithExecutor({ state: "scheduled" });
    expect(scheduledStageFor(pendingSpelling, FIXTURE_OWNER_ID)?.id).toBe(
      "aa6f1a2b-0000-4000-8000-00000000aa11",
    );
  });

  it("returns null for anonymous sessions, other order states, no candidate or non-members", () => {
    expect(scheduledStageFor(orderWithExecutor({ state: "scheduled" }), undefined)).toBeNull();
    expect(scheduledStageFor(orderWithExecutor({ state: "stage_execution_pending" }), FIXTURE_OWNER_ID)).toBeNull();
    // No stage is scheduled/execution_pending at all.
    const running = orderWithExecutor({ state: "scheduled" });
    const runningStage = running.stages[0];
    if (runningStage === undefined) throw new Error("seed stage missing");
    runningStage.state = "running";
    expect(scheduledStageFor(running, FIXTURE_OWNER_ID)).toBeNull();
    // A scheduled order whose frozen executors do not include the user.
    expect(
      scheduledStageFor(
        orderWithExecutor({ state: "scheduled" }, ["1111d9cc-e65d-7b9d-a8aa-3c81945f99ac"]),
        FIXTURE_OWNER_ID,
      ),
    ).toBeNull();
  });
});
