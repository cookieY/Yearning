import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useParams } from "react-router";
import { useTranslation } from "react-i18next";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useDraftEditorStore, selectDirty } from "@/features/review/draft-editor-store";
import { submissionDecision, type RunPhase } from "@/features/review/run-state";
import { SqlFileDropzone } from "@/features/review/bulk-import/sql-file-dropzone";
import type { SqlDigest } from "@/features/review/bulk-import/sql-digest";
import {
  isoToRequestedFor,
  requestedForToIso,
} from "@/features/review/submission-dialogs";
import { FlowTimelineCard } from "@/features/review/flow-timeline-card";
import {
  useChangeDraft,
  useCurrentUserChangeFlows,
  useFlowDatasourceSchemas,
  useFlowUpdated,
} from "@/features/review/use-draft-workspace";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { PageLayout } from "@/shared/components/page-layout";
import {
  SubmissionWizardSummaryCard,
  draftMetadataSnapshot,
  type DraftMetadataSnapshot,
} from "./submission-wizard-summary-card";
import {
  SubmissionWizardFlowSection,
  SubmissionWizardMetaSection,
  resolveFlowTargetDatabase,
} from "./submission-wizard-target-section";
import {
  SubmissionWizardDialogs,
  SubmissionWizardSqlSection,
} from "./submission-wizard-sql-section";
import {
  useMainDialogAnchor,
  useSubmissionBulkDigest,
  useSubmissionPrecheck,
} from "./use-submission-precheck";
import { useSubmissionDraftActions } from "./use-submission-draft-actions";

export default function SubmissionWizard({ mode }: { mode: "create" | "draft" }) {
  const { t } = useTranslation();
  const { draftId } = useParams<{ draftId: string }>();
  const mainColumnRef = useRef<HTMLElement | null>(null);

  // ---- create-mode state ----
  const [selectedFlowId, setSelectedFlowId] = useState<string | null>(null);
  const [createTitle, setCreateTitle] = useState("");
  const [createDescription, setCreateDescription] = useState("");
  const [createRequestedFor, setCreateRequestedFor] = useState("");
  const [createRollbackEnabled, setCreateRollbackEnabled] = useState(true);
  const [targetDatabase, setTargetDatabase] = useState("");
  const [schemasRequested, setSchemasRequested] = useState(false);
  const [stepError, setStepError] = useState<string | null>(null);
  const [localDraftId, setLocalDraftId] = useState<string | null>(null);
  const [createSql, setCreateSql] = useState("");
  const createSqlRef = useRef(createSql);

  const adopted = mode === "create" && localDraftId !== null;
  const metaBound = mode === "draft" || adopted;
  const activeDraftId = mode === "draft" ? draftId ?? null : localDraftId;

  // ---- shared data ----
  const draftQuery = useChangeDraft(
    (mode === "draft" ? draftId : localDraftId) ?? "",
    mode === "draft" || localDraftId !== null,
  );
  const flowsQuery = useCurrentUserChangeFlows();
  const draft = draftQuery.data ?? null;
  const flows = flowsQuery.data ?? [];
  const flow = (mode === "draft" || adopted
    ? flows.find((entry) => entry.id === draft?.flow_id)
    : flows.find((entry) => entry.id === selectedFlowId)) ?? null;
  const activeFlowId = draft?.flow_id ?? selectedFlowId ?? null;
  const {
    primaryTargetStage,
    isPostgresTargetStage,
    connectedPostgresDatabase,
    resolvedTargetDatabase,
    targetDatabaseForRequest,
  } = resolveFlowTargetDatabase(flow, targetDatabase, activeDraftId);

  const schemasQuery = useFlowDatasourceSchemas(
    activeFlowId,
    primaryTargetStage?.datasource_id ?? null,
    schemasRequested && primaryTargetStage !== null,
  );
  const fetchedSchemas = schemasQuery.data ?? [];
  const schemaOptions =
    resolvedTargetDatabase !== "" && !fetchedSchemas.includes(resolvedTargetDatabase)
      ? [resolvedTargetDatabase, ...fetchedSchemas]
      : fetchedSchemas;

  // ---- draft-mode metadata state ----
  const [metaTitle, setMetaTitle] = useState("");
  const [metaDescription, setMetaDescription] = useState("");
  const [metaRequestedFor, setMetaRequestedFor] = useState("");
  const [requestedForPast, setRequestedForPast] = useState(false);
  const [metaRollbackEnabled, setMetaRollbackEnabled] = useState(true);
  const metadataEditRevisionRef = useRef(0);
  const visibleMetadataRef = useRef<DraftMetadataSnapshot>(
    draftMetadataSnapshot("", "", "", true),
  );

  const updateTargetDatabase = (next: string): void => {
    setTargetDatabase(next);
    setStepError(null);
    metadataEditRevisionRef.current += 1;
    visibleMetadataRef.current = {
      ...visibleMetadataRef.current,
      target_database: next.trim() === "" ? null : next.trim(),
    };
  };

  // ---- workspace engine ----
  const store = useDraftEditorStore();
  const dirty = useDraftEditorStore(selectDirty);
  const flowUpdated = useFlowUpdated(draft?.flow_id ?? null);

  const [loadValue, setLoadValue] = useState<{ text: string; nonce: number } | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [droppedFile, setDroppedFile] = useState<File | null>(null);
  const [evidenceFindings, setEvidenceFindings] = useState<ReviewFinding[]>([]);

  const attachedDraftId = useRef<string | null>(null);
  const sessionContentDraftId = useRef<string | null>(null);
  const sessionMetadataDraftId = useRef<string | null>(null);

  useEffect(() => {
    if (draft === null || attachedDraftId.current === draft.id) return;
    attachedDraftId.current = draft.id;
    const preserveSessionContent =
      sessionContentDraftId.current === draft.id &&
      useDraftEditorStore.getState().draftId === draft.id;
    if (preserveSessionContent) {
      store.markVersioned(draft.version, draft.state);
    } else {
      store.attach({ id: draft.id, state: draft.state, version: draft.version, sql: null });
    }
    if (sessionMetadataDraftId.current !== draft.id) {
      const hydratedRollbackEnabled =
        draft.ticket_type === "rollback" ? false : (draft.rollback_enabled ?? true);
      const hydratedTargetDatabase = draft.target_database ?? "";
      const hydratedRequestedFor = isoToRequestedFor(draft.requested_scheduled_for);
      setMetaTitle(draft.title ?? "");
      setMetaDescription(draft.description ?? "");
      setMetaRequestedFor(hydratedRequestedFor);
      setRequestedForPast(
        hydratedRequestedFor !== "" && new Date(hydratedRequestedFor).getTime() <= Date.now(),
      );
      setMetaRollbackEnabled(hydratedRollbackEnabled);
      setTargetDatabase(hydratedTargetDatabase);
      visibleMetadataRef.current = draftMetadataSnapshot(
        draft.title ?? "",
        draft.description ?? "",
        hydratedRequestedFor,
        hydratedRollbackEnabled,
        hydratedTargetDatabase,
      );
    }
  }, [draft, store]);

  useEffect(() => {
    return () => {
      attachedDraftId.current = null;
      useDraftEditorStore.getState().reset();
    };
  }, []);

  useEffect(() => {
    if (mode === "create" && localDraftId === null) {
      attachedDraftId.current = null;
      useDraftEditorStore.getState().reset();
    }
  }, [localDraftId, mode]);

  const localSqlText = mode === "create" && localDraftId === null ? createSql : store.sql;
  const { isBulk, bulkDigest, applyImportedDigest } = useSubmissionBulkDigest(
    draft,
    localSqlText,
  );

  const unsavedCreateSql = mode === "create" && localDraftId === null && createSql !== "";
  useEffect(() => {
    if (!dirty && !unsavedCreateSql) return;
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", handler);
    return () => { window.removeEventListener("beforeunload", handler); };
  }, [dirty, unsavedCreateSql]);

  useEffect(() => {
    if (draft === null) return;
    const saved = useDraftEditorStore.getState().savedVersion;
    if (saved !== null && draft.version > saved) {
      store.markVersioned(draft.version, draft.state);
    }
  }, [draft, store]);

  const precheck = useSubmissionPrecheck({
    mode,
    activeDraftId,
    draftFlowId: draft?.flow_id,
    selectedFlowId,
    localSqlText,
    resolvedTargetDatabase,
    targetDatabaseForRequest,
    setStepError,
  });

  const onAdoptedSessionIds = useCallback((adoptedId: string) => {
    sessionContentDraftId.current = adoptedId;
    sessionMetadataDraftId.current = adoptedId;
  }, []);

  const draftActions = useSubmissionDraftActions({
    mode,
    metaBound,
    activeDraftId,
    draft,
    selectedFlowId,
    setLocalDraftId,
    createTitle,
    createDescription,
    createRequestedFor,
    createRollbackEnabled,
    metaTitle,
    metaDescription,
    metaRequestedFor,
    metaRollbackEnabled,
    setMetaTitle,
    setMetaDescription,
    setMetaRequestedFor,
    setMetaRollbackEnabled,
    setTargetDatabase,
    targetDatabaseForRequest,
    localSqlText,
    createSqlRef,
    metadataEditRevisionRef,
    visibleMetadataRef,
    onAdoptedSessionIds,
    setLoadValue,
    setStepError,
    lastPrecheckRunId: precheck.lastPrecheckRunId,
    setPrecheckModalOpen: precheck.setPrecheckModalOpen,
    cancelReviewStartPending: precheck.cancelReviewStartPending,
  });

  const mainDialogAnchor = useMainDialogAnchor(
    mainColumnRef,
    precheck.precheckModalOpen || draftActions.confirmSubmit,
  );

  const handleImportConfirm = useCallback((text: string, digest: SqlDigest) => {
    setImportOpen(false);
    applyImportedDigest(text, digest);
    if (mode === "create" && localDraftId === null) {
      createSqlRef.current = text;
      setCreateSql(text);
    } else {
      store.setSql(text);
    }
  }, [applyImportedDigest, localDraftId, mode, store]);

  const isSubmitted =
    activeDraftId !== null
      ? store.draftId === activeDraftId && store.serverState === "submitted"
      : draftActions.submitMutationSuccess;

  const presentedPhaseForDock: RunPhase = precheck.precheckExpired ? "idle" : precheck.phasePresented;
  const submitDecision = submissionDecision({
    draftState:
      activeDraftId !== null
        ? isSubmitted
          ? "submitted"
          : "draft"
        : null,
    runPhase: presentedPhaseForDock,
    gate: precheck.run?.gate ?? null,
    sqlChangedSincePrecheck: precheck.sqlChangedSincePrecheck,
    flowUpdated,
  });

  // ---- loading / error surfaces ----
  if (mode !== "create") {
    if (draftQuery.isPending) {
      return (
        <PageLayout data-testid="draft-workspace-page">
          <LoadingState variant="page" />
        </PageLayout>
      );
    }
    if (draftQuery.isError) {
      return (
        <PageLayout data-testid="draft-workspace-page">
          <ErrorState
            error={draftQuery.error}
            operationId="getChangeDraft"
            onRetry={() => void draftQuery.refetch()}
          />
        </PageLayout>
      );
    }
    if (draft === null) return null;
  }

  return renderPage();

  function renderPage(): ReactNode {
    const draftMode = mode === "draft" || adopted;
    const editorValue = draftMode ? store.sql : createSql;
    const editorChange = draftMode
      ? (sql: string) => { store.setSql(sql); }
      : (sql: string) => {
          createSqlRef.current = sql;
          setCreateSql(sql);
        };

    return (
      <PageLayout
        data-testid={mode === "create" ? "changes-new-page" : "draft-workspace-page"}
        data-draft-id={activeDraftId ?? undefined}
      >
        <div className="grid grid-cols-12 items-start gap-[30px]">
          <section
            ref={mainColumnRef}
            className="col-span-12 flex min-w-0 flex-col gap-[30px] lg:col-span-8"
            aria-label={t("precheck.wizard.pageTitle")}
          >
            <SubmissionWizardFlowSection
              mode={mode}
              adopted={adopted}
              flow={flow}
              draft={draft}
              flows={flows}
              flowsPending={flowsQuery.isPending}
              flowsError={flowsQuery.isError}
              flowsErrorDetail={flowsQuery.error}
              flowsSuccess={flowsQuery.isSuccess}
              selectedFlowId={selectedFlowId}
              onRetryFlows={() => void flowsQuery.refetch()}
              onSelectFlow={(id) => {
                setSelectedFlowId(id);
                setSchemasRequested(false);
                updateTargetDatabase("");
              }}
              primaryTargetStage={primaryTargetStage}
              isPostgresTargetStage={isPostgresTargetStage}
              connectedPostgresDatabase={connectedPostgresDatabase}
              isSubmitted={isSubmitted}
              isRollbackDraft={draft?.ticket_type === "rollback"}
              targetDatabase={targetDatabase}
              resolvedTargetDatabase={resolvedTargetDatabase}
              schemaOptions={schemaOptions}
              schemasRequested={schemasRequested}
              schemasPending={schemasQuery.isPending}
              schemasError={schemasQuery.isError}
              schemasSuccess={schemasQuery.isSuccess}
              schemasFetching={schemasQuery.isFetching}
              onRequestSchemas={() => { setSchemasRequested(true); }}
              onRefetchSchemas={() => {
                setSchemasRequested(true);
                if (schemasRequested) {
                  void schemasQuery.refetch();
                }
              }}
              onUpdateTargetDatabase={updateTargetDatabase}
            />

            <SubmissionWizardMetaSection
              metaBound={metaBound}
              metaTitle={metaTitle}
              createTitle={createTitle}
              metaDescription={metaDescription}
              createDescription={createDescription}
              metaRequestedFor={metaRequestedFor}
              createRequestedFor={createRequestedFor}
              requestedForError={
                metaBound
                  ? requestedForPast
                    ? t("precheck.wizard.requestedSchedulePast")
                    : null
                  : createRequestedFor !== "" && requestedForToIso(createRequestedFor) === null
                    ? t("precheck.wizard.requestedScheduleInvalid")
                    : requestedForPast
                      ? t("precheck.wizard.requestedSchedulePast")
                      : null
              }
              metaRollbackEnabled={metaRollbackEnabled}
              createRollbackEnabled={createRollbackEnabled}
              stepError={stepError}
              draft={draft}
              onTitleChange={(value) => {
                metadataEditRevisionRef.current += 1;
                visibleMetadataRef.current = {
                  ...visibleMetadataRef.current,
                  title: value.trim(),
                };
                if (metaBound) {
                  setMetaTitle(value);
                } else {
                  setCreateTitle(value);
                  setStepError(null);
                }
              }}
              onDescriptionChange={(value) => {
                metadataEditRevisionRef.current += 1;
                visibleMetadataRef.current = {
                  ...visibleMetadataRef.current,
                  description: value === "" ? undefined : value,
                };
                if (metaBound) {
                  setMetaDescription(value);
                } else {
                  setCreateDescription(value);
                }
              }}
              onRequestedForChange={(value) => {
                metadataEditRevisionRef.current += 1;
                visibleMetadataRef.current = {
                  ...visibleMetadataRef.current,
                  requested_scheduled_for: requestedForToIso(value),
                };
                setRequestedForPast(
                  value !== "" && new Date(value).getTime() <= Date.now(),
                );
                if (metaBound) {
                  setMetaRequestedFor(value);
                } else {
                  setCreateRequestedFor(value);
                }
                setStepError(null);
              }}
              onRollbackEnabledChange={(checked) => {
                metadataEditRevisionRef.current += 1;
                visibleMetadataRef.current = {
                  ...visibleMetadataRef.current,
                  rollback_enabled: checked,
                };
                if (metaBound) {
                  setMetaRollbackEnabled(checked);
                } else {
                  setCreateRollbackEnabled(checked);
                }
              }}
            />

            <SubmissionWizardSqlSection
              editorValue={editorValue}
              onEditorChange={editorChange}
              dirty={dirty}
              hasPresentedRun={precheck.hasPresentedRun}
              precheckPercent={precheck.precheckProgress.percent}
              runReviewPending={
                precheck.reviewStartPending ||
                precheck.reviewMutationPending ||
                draftActions.createMutationPending ||
                draftActions.adoptionPending
              }
              runReviewDisabled={
                (draftActions.saveMutationPending && dirty) ||
                draftActions.adoptionPending ||
                draftActions.createMutationPending ||
                ((precheck.phasePresented === "queued" || precheck.phasePresented === "running") && !precheck.sqlChangedSincePrecheck) ||
                isSubmitted ||
                flowUpdated
              }
              onOpenResults={() => { precheck.setPrecheckModalOpen(true); }}
              onRunReview={precheck.handleRunReview}
              flowUpdated={flowUpdated}
              isBulk={isBulk}
              bulkDigest={bulkDigest}
              run={precheck.run}
              isSubmitted={isSubmitted}
              draft={draft}
              loadValue={loadValue}
            />
          </section>
          <aside
            className="col-span-12 flex flex-col gap-[30px] lg:col-span-4 lg:sticky lg:top-0"
            aria-label={t("precheck.wizard.summaryTitle")}
          >
            <SqlFileDropzone
              disabled={draftActions.adoptionPending || draft?.ticket_type === "rollback"}
              onOpen={() => { setDroppedFile(null); setImportOpen(true); }}
              onDropFile={(file) => { setDroppedFile(file); setImportOpen(true); }}
            />
            <SubmissionWizardSummaryCard
              mode={mode}
              adopted={adopted}
              draft={draft}
              flow={flow}
              primaryTargetStage={primaryTargetStage}
              isPostgresTargetStage={isPostgresTargetStage}
              resolvedTargetDatabase={resolvedTargetDatabase}
              effectiveTitle={metaBound ? metaTitle : createTitle}
              effectiveRequestedFor={metaBound ? metaRequestedFor : createRequestedFor}
              effectiveRollbackEnabled={metaBound ? metaRollbackEnabled : createRollbackEnabled}
              stepError={stepError}
              precheck={precheck}
              draftActions={draftActions}
            />
            <FlowTimelineCard flow={flow} />
          </aside>
        </div>

        <SubmissionWizardDialogs
          precheck={precheck}
          draftActions={draftActions}
          mainDialogAnchor={mainDialogAnchor}
          flowUpdated={flowUpdated}
          evidenceFindings={evidenceFindings}
          onOpenEvidence={setEvidenceFindings}
          stepError={stepError}
          submitEnabled={submitDecision.submitEnabled}
          importOpen={importOpen}
          droppedFile={droppedFile}
          onImportOpenChange={(open) => {
            setImportOpen(open);
            if (!open) setDroppedFile(null);
          }}
          onImportConfirm={handleImportConfirm}
          flow={flow}
          fallbackFlowId={draft?.flow_id}
        />
      </PageLayout>
    );
  }
}
