import { useTranslation } from "react-i18next";
import type { ChangeOrder } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { stageApprovalSteps } from "@/features/orders/approval-steps";
import { ORDER_STATE_TONE_DOT, orderStateTone } from "@/features/orders/order-state";
import { cn } from "@/shared/lib/utils";

/**
 * Order progress (owner page feedback 2026-11-04 progress bar semantics v2, superseding the earlier same-day
 * "nodes = all approval steps + one execution node per stage" semantics): fixed three nodes —
 * User Submit, Approval, Execution ("just User Submit, then Approval, and finally Execution").
 *   Submit node: completed as soon as the order exists (submitting finishes the first node).
 *   Approval node: every approval step of every stage has a decision (approved/rejected/skipped/
 *                  invalid; a stage skipped as a whole counts as decided) — rejection also counts as reviewed.
 *   Execution node: every stage is succeeded/skipped (completed orders read 100%, keeping the spirit of
 *                   "skipped counts as completed").
 * The progress bar remains a single-segment fill colored by the order's current state family (progress bar color matches
 * the current state badge); the stage list continues to display fine-grained status for each approval step. Accessible label
 * carries counts and state name so color is never the sole carrier.
 */

/** Approval-step states that carry a decision (approval step is decided). */
const DECIDED_STEP_STATES: ReadonlySet<string> = new Set([
  "approved",
  "rejected",
  "skipped",
  "invalid",
]);

export function ApprovalProgress({ order }: { order: ChangeOrder }) {
  const { t } = useTranslation();
  const stages = order.stages;

  // No stages ⇒ nothing to review or execute; the card stays hidden (the
  // empty order shape never reaches this page in practice).
  if (stages.length === 0) return null;

  // Node 1 User Submit: the order exists, so it was submitted — always done.
  // Node 2 Approval: every approval step of every stage is decided; a stage
  // skipped as a whole counts as decided regardless of its step records.
  const reviewDone = stages.every(
    (stage) =>
      stage.state === "skipped" ||
      stageApprovalSteps(stage).every((step) => DECIDED_STEP_STATES.has(step.state)),
  );
  // Node 3 Execution: every stage finished (successfully or skipped).
  const executionDone = stages.every(
    (stage) => stage.state === "succeeded" || stage.state === "skipped",
  );
  const total = 3;
  const done = 1 + (reviewDone ? 1 : 0) + (executionDone ? 1 : 0);
  const color = ORDER_STATE_TONE_DOT[orderStateTone(order.state)];

  return (
    <div className="flex flex-col gap-4" data-testid="approval-progress">
      <div className="flex items-baseline justify-between gap-3">
        <p className="text-sm font-medium">{t("orders.detail.progressLabel")}</p>
        <p className="text-muted-foreground text-sm tabular-nums" data-testid="approval-progress-figure">
          {done} / {total}
        </p>
      </div>

      <div
        role="img"
        aria-label={t("orders.detail.progressAria", {
          done,
          total,
          state: t(`orders.state.${order.state}`),
        })}
        className="bg-muted flex h-3 w-full overflow-hidden rounded-full"
        data-testid="approval-progress-bar"
      >
        <div
          className={cn("h-full rounded-full", color)}
          style={{ width: `${String(Math.min(100, (done / total) * 100))}%` }}
          data-testid="approval-progress-fill"
        />
      </div>
    </div>
  );
}
