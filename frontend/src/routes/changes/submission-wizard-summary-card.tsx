import { useTranslation } from "react-i18next";
import type {
  ChangeDraft,
  Flow,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { presentPhase } from "@/features/review/run-state";
import { SummaryRow, requestedForToIso } from "@/features/review/submission-dialogs";
import { PendingButton } from "@/shared/components/pending-button";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/components/ui/card";
import type { useSubmissionPrecheck } from "./use-submission-precheck";
import type { useSubmissionDraftActions } from "./use-submission-draft-actions";

type FlowStageItem = NonNullable<Flow["stages"]>[number];

const PRECHECK_TONE_CLASS = {
  idle: { text: "text-muted-foreground", dot: "bg-muted-foreground/50" },
  busy: { text: "text-foreground", dot: "bg-foreground/60" },
  pass: { text: "text-success", dot: "bg-success" },
  fail: { text: "text-destructive", dot: "bg-destructive" },
  stale: { text: "text-warning", dot: "bg-warning" },
} as const;

export interface DraftMetadataSnapshot {
  title: string;
  description: string | undefined;
  requested_scheduled_for: string | null;
  rollback_enabled: boolean;
  target_database: string | null;
}

export function draftMetadataSnapshot(
  title: string,
  description: string,
  requestedFor: string,
  rollbackEnabled = true,
  targetDatabase = "",
): DraftMetadataSnapshot {
  const trimmedTargetDb = targetDatabase.trim();
  return {
    title: title.trim(),
    description: description === "" ? undefined : description,
    requested_scheduled_for: requestedForToIso(requestedFor),
    rollback_enabled: rollbackEnabled,
    target_database: trimmedTargetDb === "" ? null : trimmedTargetDb,
  };
}

export function sameDraftMetadata(
  left: DraftMetadataSnapshot,
  right: DraftMetadataSnapshot,
): boolean {
  return left.title === right.title
    && left.description === right.description
    && left.requested_scheduled_for === right.requested_scheduled_for
    && left.rollback_enabled === right.rollback_enabled
    && left.target_database === right.target_database;
}

export interface SubmissionWizardSummaryCardProps {
  mode: "create" | "draft";
  adopted: boolean;
  draft: ChangeDraft | null;
  flow: Flow | null;
  primaryTargetStage: FlowStageItem | null;
  isPostgresTargetStage: boolean;
  resolvedTargetDatabase: string;
  effectiveTitle: string;
  effectiveRequestedFor: string;
  effectiveRollbackEnabled: boolean;
  stepError: string | null;
  precheck: ReturnType<typeof useSubmissionPrecheck>;
  draftActions: ReturnType<typeof useSubmissionDraftActions>;
}

export function SubmissionWizardSummaryCard({
  mode,
  adopted,
  draft,
  flow,
  primaryTargetStage,
  isPostgresTargetStage,
  resolvedTargetDatabase,
  effectiveTitle,
  effectiveRequestedFor,
  effectiveRollbackEnabled,
  stepError,
  precheck,
  draftActions,
}: SubmissionWizardSummaryCardProps) {
  const { t } = useTranslation();
  const { hasPrecheckResult, precheckExpired, phase, sqlChangedSincePrecheck, run } = precheck;
  const savePending =
    draftActions.adoptionPending ||
    draftActions.metaSaveMutationPending ||
    draftActions.saveMutationPending ||
    draftActions.createMutationPending;

  /** The presented precheck state for the summary card: label plus the
   * semantic tone the real state earns (green pass / red fail / amber
   * outdated) — idle and in-flight stay neutral. */
  function precheckStatus(): { label: string; tone: "idle" | "busy" | "pass" | "fail" | "stale" } {
    if (!hasPrecheckResult || precheckExpired) {
      return { label: t("precheck.wizard.precheck.idle"), tone: "idle" };
    }
    const presented = presentPhase(phase, sqlChangedSincePrecheck, hasPrecheckResult);
    if (presented === "idle") return { label: t("precheck.wizard.precheck.idle"), tone: "idle" };
    if (presented === "queued" || presented === "running")
      return { label: t("precheck.wizard.precheck.running"), tone: "busy" };
    if (presented === "outdated")
      return { label: t("precheck.wizard.precheck.outdated"), tone: "stale" };
    return run !== null && run.gate.passed
      ? { label: t("precheck.wizard.precheck.passed"), tone: "pass" }
      : { label: t("precheck.wizard.precheck.failed"), tone: "fail" };
  }

  const titleValue = effectiveTitle.trim();
  const requestedForValue = effectiveRequestedFor;
  const rollbackEnabled =
    draft?.ticket_type === "rollback" ? false : effectiveRollbackEnabled;
  const datasources = [...new Set((flow?.stages ?? []).map((stage) => stage.datasource_name))];
  const values = {
    title: titleValue === "" ? t("precheck.wizard.summary.empty") : titleValue,
    sql:
      run?.statement_count != null
        ? t("precheck.wizard.summary.statements", { count: run.statement_count })
        : (mode === "draft" || adopted) && draft?.has_sql
          ? t("precheck.wizard.summary.saved")
          : t("precheck.wizard.summary.empty"),
    requestedFor:
      requestedForValue === ""
        ? t("precheck.wizard.summary.unselected")
        : requestedForValue.replace("T", " "),
    rollbackEnabled,
    isRollbackDraft: draft?.ticket_type === "rollback",
    datasources:
      datasources.length > 0
        ? datasources.join(t("common.listSeparator"))
        : t("precheck.wizard.summary.unselected"),
  };

  const status = precheckStatus();

  return (
    <Card className="[--card-spacing:--spacing(4)]" data-testid="wizard-summary">
      <CardHeader>
        <CardTitle className="text-base font-semibold">{t("precheck.wizard.summaryTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2.5">
        <p className="text-muted-foreground text-xs font-medium tracking-wide">
          {t("precheck.wizard.summary.groupInfo")}
        </p>
        <dl className="flex flex-col gap-3">
          {/* Review flow is now rendered by the standalone timeline card below (owner 2026-10-31). */}
          <SummaryRow
            label={t("precheck.new.dialog.titleLabel")}
            value={values.title}
            muted={values.title === t("precheck.wizard.summary.empty")}
          />
          <SummaryRow
            label={t("precheck.wizard.summary.sql")}
            value={values.sql}
            testId="wizard-summary-sql"
            muted={values.sql === t("precheck.wizard.summary.empty")}
          />
          <SummaryRow
            label={t("precheck.wizard.summary.precheck")}
            muted={status.tone === "idle"}
            value={
              <span
                className={`inline-flex items-center gap-1.5 ${PRECHECK_TONE_CLASS[status.tone].text}`}
                data-testid="wizard-summary-precheck"
              >
                <span
                  aria-hidden
                  className={`size-1.5 rounded-full ${PRECHECK_TONE_CLASS[status.tone].dot}`}
                />
                {status.label}
              </span>
            }
          />
          <SummaryRow
            label={t("precheck.wizard.summary.requestedFor")}
            value={values.requestedFor}
            muted={values.requestedFor === t("precheck.wizard.summary.unselected")}
          />
          <SummaryRow
            label={t("precheck.wizard.summary.rollback")}
            testId="wizard-summary-rollback"
            muted={!values.rollbackEnabled && !values.isRollbackDraft}
            value={
              values.isRollbackDraft
                ? t("orders.rollback.ticketBadge")
                : values.rollbackEnabled
                  ? t("precheck.wizard.summary.rollbackEnabled")
                  : t("precheck.wizard.summary.rollbackDisabled")
            }
          />
          <SummaryRow
            label={t("precheck.wizard.summary.datasources")}
            muted={flow === null}
            value={
              flow === null ? (
                values.datasources
              ) : (
                <span
                  className="text-muted-foreground font-normal"
                  title={t("precheck.wizard.summary.frozenByFlow")}
                >
                  {values.datasources}
                </span>
              )
            }
          />
          {primaryTargetStage !== null && (
            <SummaryRow
              label={t(
                isPostgresTargetStage
                  ? "precheck.wizard.summary.targetSchema"
                  : "precheck.wizard.summary.targetDatabase",
              )}
              value={
                resolvedTargetDatabase ||
                t(
                  isPostgresTargetStage
                    ? "precheck.wizard.summary.defaultSearchPath"
                    : "precheck.wizard.summary.unselected",
                )
              }
              testId="wizard-summary-target-database"
              muted={resolvedTargetDatabase === ""}
            />
          )}
        </dl>
      </CardContent>
      <div className="flex flex-col gap-2.5 px-4">
        {stepError !== null && (
          <p role="alert" className="text-destructive text-sm" data-testid="wizard-step-error">
            {stepError}
          </p>
        )}
        {draftActions.actionError !== null && (
          <p role="alert" className="text-destructive text-sm" data-testid="wizard-action-error">
            {draftActions.actionError}
          </p>
        )}
        {/* Save and Return is the single draft persistence action in the summary card;
         * precheck progress, gate blockers, and Submit for Approval live inside precheck-dialog. */}
        <PendingButton
          variant="outline"
          className="h-9 w-full rounded-lg text-xs font-semibold"
          pending={savePending}
          onClick={draftActions.saveDraftAndLeave}
          data-testid="wizard-save-draft"
        >
          {t("precheck.wizard.saveAndReturn")}
        </PendingButton>
      </div>
    </Card>
  );
}
