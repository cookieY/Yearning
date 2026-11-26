import { useTranslation } from "react-i18next";
import type {
  ChangeDraft,
  Flow,
  ReviewFinding,
  ReviewRun,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { isTerminalPhase, type RunPhase } from "@/features/review/run-state";
import { CapacityBanner } from "@/features/review/bulk-browser/capacity-banner";
import { StatementBrowser } from "@/features/review/bulk-browser/statement-browser";
import { ImportDialog } from "@/features/review/bulk-import/import-dialog";
import type { SqlDigest } from "@/features/review/bulk-import/sql-digest";
import { EvidenceSheet } from "@/features/review/evidence-sheet";
import type { FindingListState } from "@/features/review/finding-list";
import { PrecheckResultDialog } from "@/features/review/precheck-result-dialog";
import { FormSection, SubmitConfirmDialog } from "@/features/review/submission-dialogs";
import { SqlEditorPanel } from "@/features/review/sql-editor-panel";
import { PendingButton } from "@/shared/components/pending-button";
import { Button } from "@/shared/components/ui/button";
import { CircleAlert, Play } from "lucide-react";
import type { useSubmissionPrecheck } from "./use-submission-precheck";
import type { useSubmissionDraftActions } from "./use-submission-draft-actions";

export function buildFindingsState({
  precheckExpired,
  hasPresentedRun,
  reviewRunError,
  reviewRunErrorDetail,
  reviewRunPending,
  phasePresented,
  run,
  flowUpdated,
  findingsError,
  findingsErrorDetail,
  findingsPending,
  findingsData,
}: {
  precheckExpired: boolean;
  hasPresentedRun: boolean;
  reviewRunError: boolean;
  reviewRunErrorDetail: unknown;
  reviewRunPending: boolean;
  phasePresented: RunPhase;
  run: ReviewRun | null;
  flowUpdated: boolean;
  findingsError: boolean;
  findingsErrorDetail: unknown;
  findingsPending: boolean;
  findingsData: ReviewFinding[] | undefined;
}): FindingListState {
  if (precheckExpired) {
    return { status: "expired" };
  }
  if (!hasPresentedRun) {
    return { status: "idle" };
  }
  if (reviewRunError) {
    return {
      status: "error",
      error: reviewRunErrorDetail,
      operationId: "getReviewRun",
    };
  }
  if (reviewRunPending) {
    return { status: "loading" };
  }
  if (phasePresented === "queued" || phasePresented === "running") {
    return { status: "waiting" };
  }
  if (run === null) {
    return { status: "loading" };
  }
  if (phasePresented === "outdated" || flowUpdated) {
    return { status: "stale" };
  }
  if (!isTerminalPhase(phasePresented)) {
    return { status: "waiting" };
  }
  if (findingsError) {
    return { status: "error", error: findingsErrorDetail };
  }
  if (findingsPending || findingsData === undefined) {
    return { status: "loading" };
  }
  const incomplete =
    phasePresented === "failed" ||
    phasePresented === "partial" ||
    (phasePresented === "blocked" && findingsData.length === 0);
  return incomplete
    ? { status: "incomplete", findings: findingsData, failureCode: run.failure_code ?? null }
    : { status: "ready", findings: findingsData };
}

export interface SubmissionWizardSqlSectionProps {
  editorValue: string;
  onEditorChange: (sql: string) => void;
  dirty: boolean;
  hasPresentedRun: boolean;
  precheckPercent: number;
  runReviewPending: boolean;
  runReviewDisabled: boolean;
  onOpenResults: () => void;
  onRunReview: () => void;
  flowUpdated: boolean;
  isBulk: boolean;
  bulkDigest: SqlDigest | null;
  run: ReviewRun | null;
  isSubmitted: boolean;
  draft: ChangeDraft | null;
  loadValue: { text: string; nonce: number } | null;
}

export function SubmissionWizardSqlSection({
  editorValue,
  onEditorChange,
  dirty,
  hasPresentedRun,
  precheckPercent,
  runReviewPending,
  runReviewDisabled,
  onOpenResults,
  onRunReview,
  flowUpdated,
  isBulk,
  bulkDigest,
  run,
  isSubmitted,
  draft,
  loadValue,
}: SubmissionWizardSqlSectionProps) {
  const { t } = useTranslation();

  return (
    <FormSection
      title={t("precheck.wizard.title.sql")}
      hint={t("precheck.wizard.description.sql")}
      action={
        <div className="flex items-center gap-2">
          {dirty && (
            <span className="text-muted-foreground text-xs font-normal">
              {t("precheck.workspace.unsaved")}
            </span>
          )}
          {hasPresentedRun && (
            <Button
              variant="outline"
              size="sm"
              onClick={onOpenResults}
              data-testid="view-precheck-results"
            >
              {t("precheck.wizard.viewResults")}
              {precheckPercent > 0 && precheckPercent < 100
                ? ` (${String(precheckPercent)}%)`
                : ""}
            </Button>
          )}
          <PendingButton
            size="sm"
            pending={runReviewPending}
            onClick={onRunReview}
            disabled={runReviewDisabled}
            className="text-sm font-semibold"
            data-testid="run-review"
          >
            <Play aria-hidden />
            {t("precheck.workspace.runReview")}
          </PendingButton>
        </div>
      }
      last
    >
      <div className="flex flex-col gap-3">
        {flowUpdated && (
          <div
            role="alert"
            className="text-muted-foreground flex items-center gap-2 rounded-md border p-3 text-sm"
            data-testid="flow-updated-banner"
          >
            <CircleAlert className="size-4" aria-hidden />
            <span>{t("precheck.flowUpdated.banner")}</span>
          </div>
        )}

        {/* Bulk mode needs a definite card height: with the auto-height
         * card the virtualized list would grow with its content and the
         * virtualizer would treat every row as visible. */}
        <div className={isBulk ? "flex h-[70vh] flex-col gap-3" : "flex flex-col gap-3"}>
          <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-hidden">
            {isBulk ? (
              <>
                <CapacityBanner
                  digest={bulkDigest}
                  serverStatementCount={run?.statement_count ?? null}
                  serverGroupCount={run?.fingerprint_group_count ?? null}
                />
                {bulkDigest === null ? (
                  <div
                    className="text-muted-foreground flex flex-1 items-center justify-center rounded-md border border-dashed p-6 text-sm"
                    data-testid="bulk-digest-pending"
                  >
                    {/* In unadopted create mode, imported text lives locally (RCP-20261101), so placeholder
                        hints check the current editor value rather than only the store. */}
                    {editorValue === ""
                      ? t("precheck.bulk.browser.revealFirst")
                      : t("precheck.bulk.browser.digesting")}
                  </div>
                ) : (
                  <StatementBrowser
                    sql={editorValue}
                    digest={bulkDigest}
                    serverGroupCount={run?.fingerprint_group_count ?? null}
                  />
                )}
              </>
            ) : (
              <SqlEditorPanel
                value={editorValue}
                onChange={onEditorChange}
                readOnly={isSubmitted || draft?.ticket_type === "rollback"}
                loadValue={loadValue}
                className="h-[260px] min-h-[200px] rounded-md border-0"
                data-testid="sql-editor"
              />
            )}
          </div>
        </div>
      </div>
    </FormSection>
  );
}

export interface SubmissionWizardDialogsProps {
  precheck: ReturnType<typeof useSubmissionPrecheck>;
  draftActions: ReturnType<typeof useSubmissionDraftActions>;
  mainDialogAnchor: { left: number; maxWidth: number } | null;
  flowUpdated: boolean;
  evidenceFindings: ReviewFinding[];
  onOpenEvidence: (findings: ReviewFinding[]) => void;
  stepError: string | null;
  submitEnabled: boolean;
  importOpen: boolean;
  droppedFile: File | null;
  onImportOpenChange: (open: boolean) => void;
  onImportConfirm: (text: string, digest: SqlDigest) => void;
  flow: Flow | null;
  fallbackFlowId: string | undefined;
}

export function SubmissionWizardDialogs({
  precheck,
  draftActions,
  mainDialogAnchor,
  flowUpdated,
  evidenceFindings,
  onOpenEvidence,
  stepError,
  submitEnabled,
  importOpen,
  droppedFile,
  onImportOpenChange,
  onImportConfirm,
  flow,
  fallbackFlowId,
}: SubmissionWizardDialogsProps) {
  const findingsState = buildFindingsState({
    precheckExpired: precheck.precheckExpired,
    hasPresentedRun: precheck.hasPresentedRun,
    reviewRunError: precheck.reviewRunError,
    reviewRunErrorDetail: precheck.reviewRunErrorDetail,
    reviewRunPending: precheck.reviewRunPending,
    phasePresented: precheck.phasePresented,
    run: precheck.run,
    flowUpdated,
    findingsError: precheck.findingsQuery.isError,
    findingsErrorDetail: precheck.findingsQuery.error,
    findingsPending: precheck.findingsQuery.isPending,
    findingsData: precheck.findingsQuery.data,
  });

  return (
    <>
      <PrecheckResultDialog
        open={precheck.precheckModalOpen}
        onOpenChange={precheck.setPrecheckModalOpen}
        mainDialogAnchor={mainDialogAnchor}
        precheckRequestError={precheck.precheckRequestError}
        reviewStartPending={precheck.reviewStartPending}
        reviewMutationPending={precheck.reviewMutationPending}
        precheckProgress={precheck.precheckProgress}
        phasePresented={precheck.phasePresented}
        run={precheck.run}
        hasPresentedRun={precheck.hasPresentedRun}
        reviewRunPending={precheck.reviewRunPending}
        reviewRunFetching={precheck.reviewRunFetching}
        reviewRunError={precheck.reviewRunError}
        reviewRunErrorDetail={precheck.reviewRunErrorDetail}
        onRefetchReviewRun={() => { void precheck.refetchReviewRun(); }}
        precheckExpired={precheck.precheckExpired}
        flowUpdated={flowUpdated}
        timedOut={precheck.timedOut}
        onRetryTimedOut={() => { void precheck.invalidateAll(); }}
        resultsTab={precheck.resultsTab}
        onResultsTabChange={precheck.setResultsTab}
        findingsState={findingsState}
        onOpenEvidence={onOpenEvidence}
        onRefetchFindings={() => { void precheck.findingsQuery.refetch(); }}
        stepError={stepError}
        submitEnabled={submitEnabled}
        submitPending={draftActions.submitMutationPending}
        adoptionPending={draftActions.adoptionPending}
        onAttemptSubmit={draftActions.handleAttemptSubmit}
      />

      <ImportDialog
        open={importOpen}
        initialFile={droppedFile}
        onOpenChange={onImportOpenChange}
        onConfirm={onImportConfirm}
      />
      <EvidenceSheet
        findings={evidenceFindings}
        open={evidenceFindings.length > 0}
        onOpenChange={(open) => {
          if (!open) onOpenEvidence([]);
        }}
      />

      <SubmitConfirmDialog
        open={draftActions.confirmSubmit}
        onOpenChange={draftActions.setConfirmSubmit}
        mainDialogAnchor={mainDialogAnchor}
        flow={flow}
        fallbackFlowId={fallbackFlowId}
        gatePassed={precheck.run?.gate.passed === true}
        submitError={draftActions.submitError}
        submitting={draftActions.submitMutationPending}
        hasPresentedRun={precheck.hasPresentedRun}
        onReopenPrecheck={() => { precheck.setPrecheckModalOpen(true); }}
        onConfirmSubmit={draftActions.handleConfirmSubmit}
      />
    </>
  );
}
