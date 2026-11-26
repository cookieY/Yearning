import { useState } from "react";
import { Link, useLocation, useParams } from "react-router";
import { useTranslation } from "react-i18next";
import { OrderStateBadge, StageStateBadge } from "@/features/orders/order-state-badge";
import {
  activeApprovalStepFor,
  activeExecutorStageFor,
  canVoid,
  withdrawOutcome,
} from "@/features/orders/order-state";
import { stageApprovalSteps } from "@/features/orders/approval-steps";
import { ApprovalDecisionCard, ApprovalStepStateBadge } from "@/features/orders/approval-decision-card";
import { ApprovalProgress } from "@/features/orders/approval-progress";
import { FrozenReviewCard } from "@/features/orders/frozen-review-card";
import { OrderActivityCard, formatTimestamp } from "@/features/orders/order-activity-card";
import { OrderRollbackCard } from "@/features/orders/order-rollback-card";
import { OrderSqlCard } from "@/features/orders/order-sql-card";
import { ExecutionWorkspaceCard } from "@/features/orders/execution-card";
import {
  useChangeOrder,
  useVoidOrder,
  useWithdrawOrder,
} from "@/features/orders/use-orders";
import { useSession } from "@/features/auth/session-provider";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { describeError } from "@/shared/api/error-display";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Label } from "@/shared/components/ui/label";
import { Separator } from "@/shared/components/ui/separator";
import { cn } from "@/shared/lib/utils";
import { Textarea } from "@/shared/components/ui/textarea";
import { Ban, CircleAlert, Clock, FileCode2, Layers, Undo2 } from "lucide-react";

/**
 * Order detail (route /changes/orders/:id; migration contract §2 keeps the
 * legacy profile's header-actions + progress + timeline layout). Withdraw/
 * void mirror the change_order state machine: the withdraw dialog spells out
 * the partial-execution consequence before confirming (W007 — withdrawal after
 * partial execution explicitly warns of non-rollback), and a backend rejection re-renders inside the dialog
 * as an error — never as a success state (gate: backend rejection has no fake success).
 *
 * FE-F7 approval workspace: the same surface serves the frozen reviewer —
 * an approval decision card renders only for the active step's frozen
 * actors, the frozen submission review and comments are readable for every
 * related user, and an invalid notice explains the W008 actor-deletion
 * state. Opening or operating this page never creates a Review Run (R003).
 */

export default function OrderDetailPage() {
  const { t } = useTranslation();
  const { orderId } = useParams<{ orderId: string }>();
  const location = useLocation();
  const session = useSession();
  const orderQuery = useChangeOrder(orderId ?? "");
  const order = orderQuery.data ?? null;
  // Work-order PRD §5: only the submitter may withdraw or void — frozen
  // reviewers/executors can read the order but never see the lifecycle
  // buttons (the backend keeps the authoritative 403).
  const isSubmitter = order !== null && order.submitter_user_id === session.user?.id;
  // owner ruling 2026-10-31 + 2026-11-22 (self-review/self-execute button split): when the same user is
  // both submitter and active reviewer or frozen executor, the action set is mutually exclusive by entry
  // context — entering from the handler queue (/approvals/orders/:id) shows only Approve/Reject or
  // Execute/Schedule/Cancel/Verify (hiding Withdraw/Void), while entering from My Orders (/changes/orders/:id) shows
  // only Withdraw/Void (hiding ApprovalDecisionCard and ExecutionWorkspaceCard actions). Single-role
  // handlers (not the submitter) remain unaffected on both routes.
  const approvalEntry = location.pathname.startsWith("/approvals/");
  const isActiveApprover =
    order !== null && activeApprovalStepFor(order, session.user?.id) !== null;
  const isActiveExecutor =
    order !== null && activeExecutorStageFor(order, session.user?.id) !== null;
  const showSubmitterActions = isSubmitter && !approvalEntry;
  const showApprovalCard = !(!approvalEntry && isSubmitter && isActiveApprover);
  const showExecutionActions = !(!approvalEntry && isSubmitter && isActiveExecutor);

  const [dialog, setDialog] = useState<"withdraw" | "void" | null>(null);
  const [reason, setReason] = useState("");
  const [dialogError, setDialogError] = useState<string | null>(null);
  // The execution attempt this session knows about. The contract has no
  // list-attempts read — the id enters through the creation response and is
  // page-local state (FE-F8; events on the order subject keep it fresh).
  const [attemptId, setAttemptId] = useState<string | null>(null);

  const withdrawMutation = useWithdrawOrder(order);
  const voidMutation = useVoidOrder(order);
  const outcome = withdrawOutcome(order?.state);

  const closeDialog = (): void => {
    setDialog(null);
    setReason("");
    setDialogError(null);
  };

  const confirmAction = (): void => {
    const mutation = dialog === "withdraw" ? withdrawMutation : voidMutation;
    mutation.mutate(reason, {
      onSuccess: () => {
        appToast.success(
          dialog === "withdraw"
            ? t("orders.detail.withdrawAction")
            : t("orders.detail.voidAction"),
        );
        closeDialog();
      },
      onError: (error) => {
        // The dialog stays open and shows exactly what the backend said —
        // no optimistic state flip, no fake success (acceptance gate: backend rejection has no fake
        // success).
        const display = describeError(
          error,
          dialog === "withdraw" ? "withdrawChangeOrder" : "voidChangeOrder",
        );
        const errText = `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
        setDialogError(errText);
        appToast.error(errText);
      },
    });
  };

  if (orderQuery.isPending) {
    return (
      <PageLayout data-testid="order-detail-page">
        <PageHeader
          icon={FileCode2}
          title={t("orders.detail.title")}
          description={t("orders.detail.frozenNote")}
        />
        <LoadingState variant="page" />
      </PageLayout>
    );
  }
  if (orderQuery.isError) {
    return (
      <PageLayout data-testid="order-detail-page">
        <PageHeader
          icon={FileCode2}
          title={t("orders.detail.title")}
          description={t("orders.detail.frozenNote")}
        />
        <ErrorState
          error={orderQuery.error}
          operationId="getChangeOrder"
          onRetry={() => void orderQuery.refetch()}
        />
      </PageLayout>
    );
  }
  if (order === null) return null;

  const pending = withdrawMutation.isPending || voidMutation.isPending;

  return (
    <PageLayout data-testid="order-detail-page" className="gap-[30px]">
      <PageHeader
        icon={FileCode2}
        title={<span className="font-mono">{order.display_number}</span>}
        description={
          <span className="flex flex-wrap items-center gap-2">
            <span>{order.title}</span>
            <OrderStateBadge state={order.state} />
            {order.ticket_type === "rollback" && (
              <Badge variant="outline" data-testid="order-detail-rollback-badge">
                {t("orders.rollback.ticketBadge")}
              </Badge>
            )}
            {order.ticket_type === "rollback" && order.source_order_id != null && (
              <Link
                to={`/changes/orders/${order.source_order_id}`}
                className="text-muted-foreground hover:text-foreground font-mono text-xs underline underline-offset-4"
                data-testid="order-detail-source-order-link"
              >
                {t("orders.rollback.sourceOrderShort", {
                  number: order.source_order_display_number ?? order.source_order_id,
                })}
              </Link>
            )}
          </span>
        }
        actions={
          <>
          {showSubmitterActions && outcome !== null && (
            <PendingButton
              variant="outline"
              pending={withdrawMutation.isPending}
              disabled={voidMutation.isPending}
              onClick={() => { setDialog("withdraw"); setDialogError(null); }}
              data-testid="withdraw-order"
            >
              <Undo2 aria-hidden />
              {t("orders.detail.withdraw")}
            </PendingButton>
          )}
          {showSubmitterActions && canVoid(order.state) && (
            <PendingButton
              variant="destructive"
              pending={voidMutation.isPending}
              disabled={withdrawMutation.isPending}
              onClick={() => { setDialog("void"); setDialogError(null); }}
              data-testid="void-order"
            >
              <Ban aria-hidden />
              {t("orders.detail.void")}
            </PendingButton>
          )}
          </>
        }
      />

      {order.state === "invalid" && (
        <Alert data-testid="invalid-order-alert">
          <CircleAlert className="size-4" aria-hidden />
          <AlertTitle>{t("orders.detail.invalidTitle")}</AlertTitle>
          <AlertDescription>{t("orders.detail.invalidDescription")}</AlertDescription>
        </Alert>
      )}

      {/* Two-column layout shared with the submission page (8/4, 30px):
       * the left column is what needs doing or reading in depth — decision,
       * execution, SQL, frozen review, comments; the right column is the
       * at-a-glance state — approval progress, frozen snapshot, timeline. */}
      <div
        className={cn(
          "grid grid-cols-12 items-start gap-[30px]",
          "[&_[data-slot=card]]:[--card-spacing:--spacing(4)]",
          "[&_[data-slot=card-title]]:text-base [&_[data-slot=card-title]]:font-semibold",
          // Several feature cards add `pb-3` under their header on top of the
          // card gap; one rhythm for all: title → content is the card gap (16px).
          "[&_[data-slot=card-header]]:pb-0",
        )}
      >
        <div className="col-span-12 flex min-w-0 flex-col gap-[30px] lg:col-span-8">
          {showApprovalCard && (
            <ApprovalDecisionCard order={order} onRecover={() => void orderQuery.refetch()} />
          )}
          {/* §18.57: while waiting for execution, non-frozen-executor viewers previously saw no explanation (same class of
              defect as §18.47 addendum 5) — add an explanatory notice naming the frozen executors. */}
          {order.state === "stage_execution_pending" &&
            (() => {
              const pendingStage = order.stages.find((stage) => stage.state === "execution_pending");
              const isExecutor =
                pendingStage !== undefined &&
                session.user !== null &&
                pendingStage.execution_actors.some((actor) => actor.id === session.user?.id);
              if ((isExecutor && showExecutionActions) || pendingStage === undefined) return null;
              const names = pendingStage.execution_actors.map((actor) => actor.display_name).join(t("common.listSeparator"));
              return (
                <Card data-testid="execution-wait-note">
                  <CardHeader className="pb-2">
                    <CardTitle className="flex items-center gap-2">
                      <Clock aria-hidden />
                      {t("orders.execution.waitTitle")}
                    </CardTitle>
                    <CardDescription>{t("orders.execution.waitNote", { names })}</CardDescription>
                  </CardHeader>
                </Card>
              );
            })()}
          <ExecutionWorkspaceCard
            order={order}
            attemptId={attemptId ?? order.latest_attempt_id ?? null}
            onAttemptCreated={setAttemptId}
            onRecover={() => void orderQuery.refetch()}
            showActions={showExecutionActions}
          />
          {(order.state === "completed" ||
            order.ticket_type === "rollback" ||
            (order.rollback_status != null &&
              order.rollback_status !== "not_requested" &&
              order.rollback_status !== "pending")) && (
            <OrderRollbackCard order={order} />
          )}
          {/* One card, two sections: the frozen SQL and the review frozen with it. */}
          <Card className="gap-0 py-0" data-testid="order-sql-review">
            <OrderSqlCard orderId={order.id} embedded />
            <Separator />
            <FrozenReviewCard orderId={order.id} embedded />
          </Card>
        </div>

        <aside className="col-span-12 flex min-w-0 flex-col gap-[30px] lg:max-w-[460px] lg:col-span-4">
          <Card data-testid="order-stages">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Layers aria-hidden />
                {t("orders.detail.stages")}
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-5">
              <ApprovalProgress order={order} />
              <ol className="flex flex-col">
                {order.stages.map((stage, index) => {
                  const steps = stageApprovalSteps(stage);
                  const last = index === order.stages.length - 1;
                  return (
                    <li key={stage.id} className="flex gap-3" data-testid={`order-stage-${String(stage.position)}`}>
                      {/* Node column: numbered ring + connector to the next stage. */}
                      <div className="flex flex-col items-center">
                        <span
                          className="bg-muted/40 flex size-7 shrink-0 items-center justify-center rounded-full border text-xs font-semibold"
                          aria-hidden
                        >
                          {stage.position}
                        </span>
                        {!last && <span className="bg-border my-1 w-px flex-1" aria-hidden />}
                      </div>
                      <div className={cn("flex min-w-0 flex-1 flex-col gap-2", !last && "pb-5")}>
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <p className="flex min-w-0 items-center gap-2 text-sm font-medium">
                            <Badge variant="outline">{t("precheck.stagePath.stage", { position: stage.position })}</Badge>
                            <span className="truncate font-mono text-xs">{stage.datasource_name}</span>
                          </p>
                          <StageStateBadge state={stage.state} />
                        </div>
                        {steps.length > 0 && (
                          <ol className="flex flex-col gap-2" data-testid={`stage-steps-${String(stage.position)}`}>
                            {steps.map((step) => (
                              <li
                                key={step.id}
                                className="bg-muted/30 flex flex-col gap-1 rounded-lg px-3 py-2 text-xs"
                                data-testid={`stage-step-${String(step.position)}`}
                              >
                                <span className="flex flex-wrap items-center gap-2">
                                  <ApprovalStepStateBadge state={step.state} />
                                  <span className="text-muted-foreground">
                                    {t("orders.detail.stepTitle", { position: step.position })}
                                  </span>
                                </span>
                                <span>{step.actors.map((actor) => actor.displayName).join(t("common.listSeparator"))}</span>
                                {step.decidedAt !== null && (
                                  <span className="text-muted-foreground">
                                    {t("orders.detail.stepDecidedAt", { time: formatTimestamp(step.decidedAt) })}
                                  </span>
                                )}
                              </li>
                            ))}
                          </ol>
                        )}
                        <p className="text-muted-foreground text-xs">
                          {t("orders.detail.stageExecutors", { count: stage.execution_actors.length })}
                        </p>
                      </div>
                    </li>
                  );
                })}
              </ol>
            </CardContent>
          </Card>

          <OrderActivityCard order={order} />
        </aside>
      </div>

      <Dialog open={dialog !== null} onOpenChange={(open) => { if (!open) closeDialog(); }}>
        <DialogContent data-testid="order-action-dialog">
          <DialogHeader>
            <DialogTitle>
              {dialog === "withdraw" ? t("orders.detail.withdrawTitle") : t("orders.detail.voidTitle")}
            </DialogTitle>
            <DialogDescription>
              {dialog === "withdraw"
                ? outcome === "withdrawn_after_partial_execution"
                  ? t("orders.detail.withdrawPartialDescription")
                  : t("orders.detail.withdrawDescription")
                : t("orders.detail.voidDescription")}
            </DialogDescription>
          </DialogHeader>
          {dialog === "withdraw" && outcome === "withdrawn_after_partial_execution" && (
            <Alert variant="destructive" data-testid="partial-execution-warning">
              <CircleAlert className="size-4" aria-hidden />
              <AlertTitle>{t("orders.detail.partialWarningTitle")}</AlertTitle>
              <AlertDescription>{t("orders.detail.partialWarningDescription")}</AlertDescription>
            </Alert>
          )}
          <div className="flex flex-col gap-2">
            <Label htmlFor="order-action-reason">{t("orders.detail.reasonLabel")}</Label>
            <Textarea
              id="order-action-reason"
              value={reason}
              onChange={(event) => { setReason(event.target.value); }}
              rows={3}
              maxLength={4096}
              data-testid="order-action-reason"
            />
          </div>
          {dialogError !== null && (
            <p role="alert" className="text-destructive text-sm" data-testid="order-action-error">
              {dialogError}
            </p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={closeDialog} data-testid="order-action-cancel">
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant={dialog === "void" ? "destructive" : "default"}
              onClick={confirmAction}
              pending={pending}
              disabled={reason.trim() === ""}
              data-testid="order-action-confirm"
            >
              {dialog === "withdraw" ? t("orders.detail.withdraw") : t("orders.detail.void")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageLayout>
  );
}
