import type { OrderStageApprovalStepsItem } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * The OpenAPI types OrderStage.approval_steps as free objects; the de-facto
 * wire shape is the backend StepView JSON (changeorder application/read.go):
 * {id, position, state, decided_at?, actors:[{id, username, display_name}]}
 * — frozen actor snapshots with per-step decision state. Parsing lives here
 * so the pages never poke unknown-shaped objects and never guess fields the
 * wire does not carry.
 */

export interface ApprovalStepActorView {
  id: string;
  username: string;
  displayName: string;
}

export interface ApprovalStepView {
  id: string;
  position: number;
  state: string;
  decidedAt: string | null;
  actors: ApprovalStepActorView[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

export function parseApprovalStep(raw: OrderStageApprovalStepsItem): ApprovalStepView | null {
  if (!isRecord(raw)) return null;
  const { id, position, state, decided_at, actors } = raw;
  if (typeof id !== "string" || typeof state !== "string") return null;
  if (typeof position !== "number" || !Number.isInteger(position) || position < 1) return null;
  if (decided_at !== null && typeof decided_at !== "undefined" && typeof decided_at !== "string") {
    return null;
  }
  if (!Array.isArray(actors)) return null;
  const parsedActors: ApprovalStepActorView[] = [];
  for (const entry of actors) {
    if (!isRecord(entry)) return null;
    const { id: actorId, username, display_name } = entry;
    if (typeof actorId !== "string" || typeof username !== "string") return null;
    parsedActors.push({
      id: actorId,
      username,
      displayName: typeof display_name === "string" && display_name !== "" ? display_name : username,
    });
  }
  return {
    id,
    position,
    state,
    decidedAt: typeof decided_at === "string" ? decided_at : null,
    actors: parsedActors,
  };
}

export function stageApprovalSteps(stage: {
  approval_steps: OrderStageApprovalStepsItem[];
}): ApprovalStepView[] {
  return stage.approval_steps
    .map((raw) => parseApprovalStep(raw))
    .filter((step): step is ApprovalStepView => step !== null);
}
