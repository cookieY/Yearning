import type * as React from "react";
import { useTranslation } from "react-i18next";
import type { SubmissionGate } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Send, ShieldAlert } from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import { Separator } from "@/shared/components/ui/separator";
import { Spinner } from "@/shared/components/ui/spinner";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import {
  gateBlockers,
  isTerminalPhase,
  submissionDecision,
  type RunPhase,
} from "./run-state";
import { knownFailureKey } from "./review-status-card";

/**
 * Submission readiness dock (migration contract §4 item 7): the bottom
 * decision area mirrors the backend gate — Ready + passing gate is the only
 * state that unlocks Submit for Approval, every blocker explains itself in place. The
 * enabled button is presentation only; the backend re-validates everything
 * inside the submission transaction (PRD §2 constraint 9).
 */

export interface SubmissionDockProps {
  /** Draft envelope state; only draft|submitted occur (RCP-20261101). */
  draftState: "draft" | "submitted" | null;
  phase: RunPhase;
  gate: SubmissionGate | null;
  failureCode?: string | null;
  /** Editor text differs from the SQL sent for the last AI precheck. */
  sqlChangedSincePrecheck: boolean;
  flowUpdated: boolean;
  submitting: boolean;
  onSubmit: () => void;
  /** Panel only: a secondary action rendered on the same row as the submit
   * button (owner ruling 2026-10-24: Submit for Approval + Save and Return share one row). */
  aside?: React.ReactNode;
  /** Hide the primary submit button when submission lives in the precheck modal footer. */
  hideSubmit?: boolean;
  /**
   * Presentation surface (owner layout ruling 2026-10-24): "dock" is the
   * legacy sticky bottom bar; "panel" embeds the same decision content into
   * the submission page's summary card — full-width submit, no own chrome.
   */
  variant?: "dock" | "panel";
}

export function SubmissionDock(props: SubmissionDockProps) {
  const { t } = useTranslation();
  const decision = submissionDecision({
    draftState: props.draftState,
    runPhase: props.phase,
    gate: props.gate,
    sqlChangedSincePrecheck: props.sqlChangedSincePrecheck,
    flowUpdated: props.flowUpdated,
  });
  const reviewInProgress =
    !props.flowUpdated && (props.phase === "queued" || props.phase === "running");
  const canShowGateResult =
    isTerminalPhase(props.phase) &&
    !props.sqlChangedSincePrecheck &&
    !props.flowUpdated;
  const blockers = canShowGateResult ? gateBlockers(props.gate) : [];
  const panel = props.variant === "panel";

  const statusAlerts = (
    <>
      {props.flowUpdated && (
        <Alert data-testid="flow-updated-alert">
          <ShieldAlert className="size-4" aria-hidden />
          <AlertTitle>{t("precheck.flowUpdated.title")}</AlertTitle>
          <AlertDescription>{t("precheck.flowUpdated.description")}</AlertDescription>
        </Alert>
      )}
      {/* Review in progress no longer uses a standalone Alert (owner 2026-11-03 feedback: inserting/removing caused
          the summary card to jump +-64px on every precheck). Progress is merged into the persistent readiness row for constant height. */}
      {!props.flowUpdated && blockers.length > 0 && (
        <Alert variant="destructive" data-testid="gate-blockers">
          <ShieldAlert className="size-4" aria-hidden />
          <AlertTitle>{t("precheck.blocked.gate")}</AlertTitle>
          <AlertDescription>
            <ul className="list-disc pl-4">
              {blockers.map((blocker) => (
                <li key={blocker.code}>{t(blocker.messageKey)}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
    </>
  );

  /** The always-present readiness line doubles as the live progress slot, so
   * the dock keeps a constant height while a precheck runs (see statusAlerts). */
  const specificFailureKey =
    canShowGateResult && props.failureCode != null
      ? knownFailureKey(props.failureCode)
      : null;
  const readinessText = reviewInProgress ? null
    : decision.submitEnabled
      ? t("precheck.submit.ready")
      : specificFailureKey !== null && specificFailureKey !== "generic"
        ? t(`precheck.failure.${specificFailureKey}`)
        : decision.reasonKey !== null
          ? t(decision.reasonKey)
          : t("precheck.blocked.gate");

  const inner = (
    <>
      {statusAlerts}
      <p className="text-muted-foreground text-sm" data-testid="submission-readiness">
        {reviewInProgress ? (
          <span
            className="inline-flex items-center gap-2"
            role="status"
            aria-live="polite"
            aria-atomic="true"
            data-testid="review-in-progress"
          >
            <Spinner className="size-4" aria-hidden />
            {t("precheck.progress.title")}
          </span>
        ) : (
          readinessText
        )}
      </p>
      {panel ? (
        <div className="flex gap-2">
          {!props.hideSubmit && (
            <Button
              variant="default"
              onClick={props.onSubmit}
              disabled={!decision.submitEnabled || props.submitting}
              aria-busy={props.submitting}
              className="disabled:border-border disabled:bg-muted disabled:text-muted-foreground h-9 flex-1 rounded-lg text-xs font-semibold disabled:opacity-100"
              data-testid="submit-draft"
            >
              {props.submitting ? (
                <Spinner data-icon="inline-start" aria-hidden />
              ) : null}
              {t("precheck.submit.action")}
            </Button>
          )}
          {props.aside}
        </div>
      ) : (
        <Button
          onClick={props.onSubmit}
          disabled={!decision.submitEnabled || props.submitting}
          aria-busy={props.submitting}
          data-testid="submit-draft"
        >
          {props.submitting ? (
            <Spinner data-icon="inline-start" aria-hidden />
          ) : (
            <Send data-icon="inline-start" aria-hidden />
          )}
          {t("precheck.submit.action")}
        </Button>
      )}
    </>
  );

  if (panel) {
    return (
      <div className="flex flex-col gap-2" data-testid="submission-dock">
        {inner}
      </div>
    );
  }

  return (
    <footer
      className="bg-background/95 supports-[backdrop-filter]:bg-background/80 sticky bottom-0 z-10 border-t backdrop-blur"
      data-testid="submission-dock"
    >
      <div className="mx-auto flex max-w-6xl flex-col gap-2 px-4 py-3">
        {statusAlerts}
        <div className="flex items-center gap-3">
          <p className="text-muted-foreground flex-1 text-sm" data-testid="submission-readiness">
            {reviewInProgress ? (
              <span
                className="inline-flex items-center gap-2"
                role="status"
                aria-live="polite"
                aria-atomic="true"
                data-testid="review-in-progress"
              >
                <Spinner className="size-4" aria-hidden />
                {t("precheck.progress.title")}
              </span>
            ) : (
              readinessText
            )}
          </p>
          <Separator orientation="vertical" className="hidden h-6 sm:block" />
          <Button
            onClick={props.onSubmit}
            disabled={!decision.submitEnabled || props.submitting}
            aria-busy={props.submitting}
            data-testid="submit-draft"
          >
            {props.submitting ? (
              <Spinner data-icon="inline-start" aria-hidden />
            ) : (
              <Send data-icon="inline-start" aria-hidden />
            )}
            {t("precheck.submit.action")}
          </Button>
        </div>
      </div>
    </footer>
  );
}
