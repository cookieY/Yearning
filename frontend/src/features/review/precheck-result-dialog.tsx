import type { ReviewFinding, ReviewRun } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import "./precheck-progress.css";
import { FindingList, type FindingListState } from "@/features/review/finding-list";
import { ReviewAllClear } from "@/features/review/review-all-clear";
import { knownFailureKey, ReviewStatusCard } from "@/features/review/review-status-card";
import { extractReviewSummaries, ReviewSummarySection } from "@/features/review/review-summary-section";
import { type computePrecheckProgress, gateBlockers, isTerminalPhase, type RunPhase } from "@/features/review/run-state";

export type PrecheckProgress = ReturnType<typeof computePrecheckProgress>;
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Progress } from "@/shared/components/ui/progress";
import { Spinner } from "@/shared/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TAB_PANEL_VARIANTS, TransitionPanel } from "@/shared/components/ui/transition-panel";
import { CheckCircle2, CircleAlert, Loader2, Send, ShieldAlert } from "lucide-react";
import { useTranslation } from "react-i18next";

export interface MainDialogAnchor {
  left: number;
  maxWidth: number;
}

export interface PrecheckResultDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mainDialogAnchor: MainDialogAnchor | null;
  precheckRequestError: string | null;
  reviewStartPending: boolean;
  reviewMutationPending: boolean;
  precheckProgress: PrecheckProgress;
  phasePresented: RunPhase;
  run: ReviewRun | null;
  hasPresentedRun: boolean;
  reviewRunPending: boolean;
  reviewRunFetching: boolean;
  reviewRunError: boolean;
  reviewRunErrorDetail: unknown;
  onRefetchReviewRun: () => void;
  precheckExpired: boolean;
  flowUpdated: boolean;
  timedOut: boolean;
  onRetryTimedOut: () => void;
  resultsTab: "overview" | "findings";
  onResultsTabChange: (tab: "overview" | "findings") => void;
  findingsState: FindingListState;
  onOpenEvidence: (findings: ReviewFinding[]) => void;
  onRefetchFindings: () => void;
  stepError: string | null;
  submitEnabled: boolean;
  submitPending: boolean;
  adoptionPending: boolean;
  onAttemptSubmit: () => void;
}

/** Modal dialog showing real-time AI precheck progress, stage gate blockers, and grouped findings. */
export function PrecheckResultDialog({
  open,
  onOpenChange,
  mainDialogAnchor,
  precheckRequestError,
  reviewStartPending,
  reviewMutationPending,
  precheckProgress,
  phasePresented,
  run,
  hasPresentedRun,
  reviewRunPending,
  reviewRunFetching,
  reviewRunError,
  reviewRunErrorDetail,
  onRefetchReviewRun,
  precheckExpired,
  flowUpdated,
  timedOut,
  onRetryTimedOut,
  resultsTab,
  onResultsTabChange,
  findingsState,
  onOpenEvidence,
  onRefetchFindings,
  stepError,
  submitEnabled,
  submitPending,
  adoptionPending,
  onAttemptSubmit,
}: PrecheckResultDialogProps) {
  const { t } = useTranslation();

  const hasTerminalBlockerState =
    precheckExpired || reviewRunError || phasePresented === "outdated" || flowUpdated;
  const isComplete = precheckProgress.percent === 100;
  const showAuditDetails =
    !reviewStartPending &&
    !reviewMutationPending &&
    precheckRequestError === null &&
    (isComplete || hasTerminalBlockerState);

  let stageLabel = t("precheck.wizard.progressStarting");
  if (precheckProgress.stage === "queued") {
    stageLabel = t("precheck.wizard.progressQueued");
  } else if (precheckProgress.stage === "running") {
    stageLabel = precheckProgress.totalStages > 0
      ? t("precheck.wizard.progressRunning", {
          completed: precheckProgress.completedStages,
          total: precheckProgress.totalStages,
        })
      : t("precheck.wizard.progressRunningFallback");
  } else if (precheckProgress.stage === "finalizing") {
    stageLabel = t("precheck.wizard.progressFinalizing");
  } else if (precheckProgress.stage === "complete") {
    stageLabel = t("precheck.wizard.progressDoneHint");
  }

  const progressTone:
    | "starting"
    | "queued"
    | "running"
    | "finalizing"
    | "passed"
    | "warning"
    | "blocked" = !isComplete
    ? precheckProgress.stage === "queued" ||
      precheckProgress.stage === "running" ||
      precheckProgress.stage === "finalizing"
      ? precheckProgress.stage
      : "starting"
    : phasePresented === "blocked" || phasePresented === "failed" || run?.gate.passed === false
      ? "blocked"
      : phasePresented === "partial"
        ? "warning"
        : "passed";
  const progressActive = !isComplete && precheckRequestError === null;
  const progressPercent = isComplete ? 100 : precheckProgress.percent;
  const gateBlockerItems =
    showAuditDetails &&
    isComplete &&
    !hasTerminalBlockerState &&
    isTerminalPhase(phasePresented)
      ? gateBlockers(run?.gate ?? null)
      : [];
  const terminalFailureKey =
    showAuditDetails &&
    isComplete &&
    !hasTerminalBlockerState &&
    (phasePresented === "blocked" || phasePresented === "failed" || phasePresented === "partial") &&
    run?.failure_code != null
      ? knownFailureKey(run.failure_code)
      : null;
  const hasGateBlockerBanner = gateBlockerItems.length > 0 || terminalFailureKey !== null;
  const reviewSummaries =
    !precheckExpired &&
    !reviewRunError &&
    phasePresented !== "outdated" &&
    !flowUpdated &&
    isTerminalPhase(phasePresented)
      ? extractReviewSummaries(run)
      : [];

  return (
    <Dialog
      open={open}
      disablePointerDismissal
      onOpenChange={(nextOpen, eventDetails) => {
        // Only explicit close buttons (top-right X or footer Close) may dismiss the precheck dialog.
        if (!nextOpen && eventDetails.reason !== "close-press") return;
        onOpenChange(nextOpen);
      }}
    >
      <DialogContent
        className="max-h-[min(680px,78vh)] sm:max-w-[56rem]"
        viewportClassName="max-h-[calc(min(680px,78vh)-10.5rem)]"
        style={
          mainDialogAnchor !== null
            ? {
                left: `${String(mainDialogAnchor.left)}px`,
                maxWidth: `min(56rem, ${String(mainDialogAnchor.maxWidth)}px)`,
              }
            : undefined
        }
        data-testid="precheck-dialog"
      >
        <DialogHeader>
          <DialogTitle>{t("precheck.wizard.resultsCard")}</DialogTitle>
          <DialogDescription>
            {isComplete ? t("precheck.wizard.progressComplete") : t("precheck.wizard.progressTitle")}
          </DialogDescription>
        </DialogHeader>

        {precheckRequestError !== null ? (
          <Alert variant="destructive" data-testid="precheck-start-error">
            <CircleAlert className="size-4" aria-hidden />
            <AlertTitle>{t("errors.generic.business")}</AlertTitle>
            <AlertDescription>{precheckRequestError}</AlertDescription>
          </Alert>
        ) : (
          <div className="flex flex-col gap-4">
            {(!showAuditDetails || (isComplete && !hasTerminalBlockerState)) && (
              <div
                role="status"
                aria-live="polite"
                className={
                  showAuditDetails
                    ? "flex flex-col gap-2 rounded-lg bg-muted/30 p-3"
                    : "flex flex-col gap-4 py-2"
                }
                data-testid="precheck-progress-panel"
              >
                <div
                  className={
                    showAuditDetails
                      ? "flex items-center justify-between gap-3 text-xs"
                      : "flex items-center justify-between gap-3 text-sm"
                  }
                >
                  <span className="flex items-center gap-2 font-medium text-foreground">
                    {showAuditDetails ? (
                      progressTone === "blocked" ? (
                        <CircleAlert className="size-3.5 text-[var(--risk-critical)]" aria-hidden />
                      ) : progressTone === "warning" ? (
                        <CircleAlert className="size-3.5 text-[var(--tone-amber)]" aria-hidden />
                      ) : (
                        <CheckCircle2 className="size-3.5 text-[var(--risk-safe)]" aria-hidden />
                      )
                    ) : (
                      <Loader2 className="size-4 animate-spin text-primary" aria-hidden />
                    )}
                    {showAuditDetails ? t("precheck.wizard.progressDoneHint") : stageLabel}
                  </span>
                  <span
                    className="font-semibold tabular-nums text-foreground"
                    data-testid="precheck-progress-value"
                  >
                    {String(progressPercent)}%
                  </span>
                </div>
                <Progress
                  value={progressPercent}
                  className="precheck-progress"
                  data-progress-tone={progressTone}
                  data-progress-active={String(progressActive)}
                  data-testid="precheck-progress"
                  aria-label={t("precheck.wizard.progressPercent", { percent: progressPercent })}
                />
                {!showAuditDetails &&
                  timedOut &&
                  (phasePresented === "queued" || phasePresented === "running") && (
                    <div
                      role="alert"
                      className="text-muted-foreground flex items-center gap-2 rounded-md border p-3 text-sm"
                      data-testid="review-timeout"
                    >
                      <CircleAlert className="size-4" aria-hidden />
                      <span>{t("precheck.review.timeout")}</span>
                      <Button
                        variant="outline"
                        size="sm"
                        className="text-sm font-semibold"
                        onClick={onRetryTimedOut}
                      >
                        {t("errors.retry")}
                      </Button>
                    </div>
                  )}
              </div>
            )}

            {showAuditDetails && (
              <>
                {flowUpdated && (
                  <div
                    role="alert"
                    className="text-muted-foreground flex items-center gap-2 rounded-md border p-3 text-sm"
                    data-testid="precheck-modal-flow-updated"
                  >
                    <CircleAlert className="size-4" aria-hidden />
                    <span>{t("precheck.flowUpdated.banner")}</span>
                  </div>
                )}
                {hasGateBlockerBanner && (
                  <Alert variant="destructive" data-testid="gate-blockers">
                    <ShieldAlert className="size-4" aria-hidden />
                    <AlertTitle>{t("precheck.blocked.gate")}</AlertTitle>
                    <AlertDescription>
                      {gateBlockerItems.length > 0 && (
                        <ul className="list-disc pl-4">
                          {gateBlockerItems.map((blocker) => (
                            <li key={blocker.code}>{t(blocker.messageKey)}</li>
                          ))}
                        </ul>
                      )}
                      {terminalFailureKey !== null && (
                        <p
                          className={gateBlockerItems.length > 0 ? "mt-1.5 font-medium" : "font-medium"}
                          data-testid="gate-blocker-failure"
                        >
                          {t(`precheck.failure.${terminalFailureKey}`)}
                        </p>
                      )}
                    </AlertDescription>
                  </Alert>
                )}
                <Tabs
                  value={resultsTab}
                  onValueChange={(next) => { onResultsTabChange(next === "findings" ? "findings" : "overview"); }}
                >
                  <TabsList>
                    <TabsTrigger value="overview" data-testid="tab-overview">{t("precheck.tabs.overview")}</TabsTrigger>
                    <TabsTrigger value="findings" data-testid="tab-findings">{t("precheck.tabs.findings")}</TabsTrigger>
                  </TabsList>
                  <TransitionPanel
                    activeIndex={resultsTab === "findings" ? 1 : 0}
                    transition={{ duration: 0.2, ease: "easeInOut" }}
                    variants={TAB_PANEL_VARIANTS}
                    keepMounted
                  >
                    <div>
                      <ReviewStatusCard
                        flat
                        phase={phasePresented}
                        run={run}
                        hasRunId={hasPresentedRun}
                        isPending={reviewRunPending}
                        isFetching={reviewRunFetching}
                        expired={precheckExpired}
                        isError={reviewRunError}
                        error={reviewRunErrorDetail}
                        onRetry={onRefetchReviewRun}
                      />
                    </div>
                    <div>
                      {findingsState.status === "ready" && findingsState.findings.length === 0 ? (
                        <div className="flex flex-col gap-3">
                          {reviewSummaries.length > 0 && (
                            <ReviewSummarySection summaries={reviewSummaries} />
                          )}
                          <ReviewAllClear />
                        </div>
                      ) : (
                        <FindingList
                          state={findingsState}
                          summaries={reviewSummaries}
                          hideIncompleteNotice={hasGateBlockerBanner}
                          onOpenEvidence={onOpenEvidence}
                          onRetry={() => {
                            if (reviewRunError || run === null) {
                              onRefetchReviewRun();
                            } else {
                              onRefetchFindings();
                            }
                          }}
                        />
                      )}
                    </div>
                  </TransitionPanel>
                </Tabs>
              </>
            )}
          </div>
        )}

        <DialogFooter className="border-t pt-4">
          {precheckRequestError === null && stepError !== null && (
            <p
              role="alert"
              className="text-destructive mr-auto self-center text-sm"
              data-testid="precheck-dialog-step-error"
            >
              {stepError}
            </p>
          )}
          <Button
            variant="outline"
            onClick={() => { onOpenChange(false); }}
            data-testid="precheck-dialog-close"
          >
            {t("common.close")}
          </Button>
          {precheckRequestError === null && (
            <Button
              variant="default"
              onClick={onAttemptSubmit}
              disabled={
                !isComplete ||
                !submitEnabled ||
                submitPending ||
                adoptionPending
              }
              aria-busy={submitPending || adoptionPending}
              className="disabled:border-border disabled:bg-muted disabled:text-muted-foreground disabled:opacity-100"
              data-testid="submit-draft"
            >
              {submitPending || adoptionPending ? (
                <Spinner data-icon="inline-start" aria-hidden />
              ) : (
                <Send data-icon="inline-start" aria-hidden />
              )}
              {t("precheck.submit.action")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
