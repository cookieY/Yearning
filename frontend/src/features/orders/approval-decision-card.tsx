import { useState } from "react";
import { useTranslation } from "react-i18next";
import { approvalStepTone, activeApprovalStepFor } from "@/features/orders/order-state";
import { StateToneChip } from "@/features/orders/order-state-badge";
import { useApprovalDecision } from "@/features/orders/use-approvals";
import type { ChangeOrder } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PendingButton } from "@/shared/components/pending-button";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Label } from "@/shared/components/ui/label";
import { Textarea } from "@/shared/components/ui/textarea";
import { CircleAlert, ShieldCheck } from "lucide-react";

/**
 * The approval decision surface (frontend PRD F7 items 3-4, W003). Rendered
 * only when the current user is a frozen reviewer of the currently active
 * step; approve and reject go through POST /approval-decisions with
 * If-Match + Idempotency-Key. The dialog restates the propagation rule
 * (any rejection immediately rejects the whole order) before confirming.
 * There is deliberately no transfer/add-signer/remove-signer affordance —
 * the frozen order cannot be reassigned (W004, gate: no transfer/add-signer/remove-signer entry).
 *
 * Concurrency (gate: concurrent conflict is recoverable): a lost race surfaces as an in-dialog
 * business error (1004 stale If-Match, 1010/3002 after a peer's decision
 * landed first) while the order refetches — the page then shows the real
 * state instead of pretending success.
 */
export function ApprovalDecisionCard({
  order,
  onRecover,
}: {
  order: ChangeOrder;
  onRecover: () => void;
}) {
  const { t } = useTranslation();
  const session = useSession();
  const pending = activeApprovalStepFor(order, session.user?.id);
  const decision = useApprovalDecision(order);

  const [dialog, setDialog] = useState<"approve" | "reject" | null>(null);
  const [comment, setComment] = useState("");
  const [dialogError, setDialogError] = useState<string | null>(null);

  if (pending === null) return null;

  const closeDialog = (): void => {
    setDialog(null);
    setComment("");
    setDialogError(null);
  };

  const confirmDecision = (): void => {
    const trimmed = comment.trim();
    // UX-003 ruling: rejection requires a reason (after BUG-004 fix, the reason enters the approval timeline).
    if (dialog === "reject" && trimmed === "") {
      setDialogError(t("approvals.decision.rejectReasonRequired"));
      return;
    }
    decision.mutate(
      { decision: dialog === "approve" ? "approve" : "reject", comment: trimmed },
      {
        onSuccess: () => {
          appToast.success(
            dialog === "approve"
              ? t("approvals.decision.approve")
              : t("approvals.decision.reject"),
          );
          closeDialog();
        },
        onError: (error) => {
          const display = describeError(error, "decideChangeOrder");
          const errText = `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
          setDialogError(errText);
          appToast.error(errText);
          // Converge on the real aggregate state regardless of the outcome
          // shape: the decision card unmounts itself if the step moved on.
          onRecover();
        },
      },
    );
  };

  return (
    <Card data-testid="approval-decision-card">
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <ShieldCheck className="size-4" aria-hidden />
          {t("approvals.decision.cardTitle")}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-sm" data-testid="approval-decision-context">
          {t("approvals.decision.pendingStep", {
            stage: pending.stagePosition,
            step: pending.stepPosition,
            datasource: pending.datasourceName,
          })}
        </p>
        <p className="text-muted-foreground text-xs">{t("approvals.decision.frozenReviewNote")}</p>
        <div className="flex flex-wrap gap-2">
          <Button
            onClick={() => { setDialog("approve"); setDialogError(null); }}
            disabled={decision.isPending}
            data-testid="approval-approve"
          >
            {t("approvals.decision.approve")}
          </Button>
          <Button
            variant="destructive"
            onClick={() => { setDialog("reject"); setDialogError(null); }}
            disabled={decision.isPending}
            data-testid="approval-reject"
          >
            {t("approvals.decision.reject")}
          </Button>
        </div>
      </CardContent>

      <Dialog open={dialog !== null} onOpenChange={(open) => { if (!open) closeDialog(); }}>
        <DialogContent data-testid="approval-decision-dialog">
          <DialogHeader>
            <DialogTitle>
              {dialog === "approve"
                ? t("approvals.decision.approveTitle")
                : t("approvals.decision.rejectTitle")}
            </DialogTitle>
            <DialogDescription>
              {dialog === "approve"
                ? t("approvals.decision.approveDescription")
                : t("approvals.decision.rejectDescription")}
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="approval-decision-comment">{t("approvals.decision.commentLabel")}</Label>
            <Textarea
              id="approval-decision-comment"
              value={comment}
              onChange={(event) => { setComment(event.target.value); }}
              rows={3}
              maxLength={4096}
              data-testid="approval-decision-comment"
            />
          </div>
          {dialogError !== null && (
            <Alert variant="destructive" data-testid="approval-decision-error">
              <CircleAlert className="size-4" aria-hidden />
              <AlertTitle>{t("approvals.decision.conflictTitle")}</AlertTitle>
              <AlertDescription>
                {dialogError}
                <br />
                {t("approvals.decision.conflictHint")}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={closeDialog} data-testid="approval-decision-cancel">
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant={dialog === "reject" ? "destructive" : "default"}
              onClick={confirmDecision}
              pending={decision.isPending}
              disabled={dialog === "reject" && comment.trim() === ""}
              data-testid="approval-decision-confirm"
            >
              {dialog === "approve"
                ? t("approvals.decision.approve")
                : t("approvals.decision.reject")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

/** Step badge rendering shared by the detail stages card — tone classes
 * differentiate step states visually (approved/rejected/invalid/active). */
export function ApprovalStepStateBadge({ state }: { state: string }) {
  const { t } = useTranslation();
  // owner 2026-11-04 (Sonner 08 adoption): step status badge shares the same tone visual as order status.
  return (
    <StateToneChip
      tone={approvalStepTone(state)}
      label={t(`approvals.stepState.${state}`, { defaultValue: state })}
    />
  );
}
