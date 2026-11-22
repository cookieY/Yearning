import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type {
  ChangeDraft,
  ReviewFinding,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { useDraftEditorStore, selectDirty } from "@/features/review/draft-editor-store";
import {
  computePrecheckProgress,
  isTerminalPhase,
  presentPhase,
  submissionDecision,
  type RunPhase,
} from "@/features/review/run-state";
import { CapacityBanner } from "@/features/review/bulk-browser/capacity-banner";
import { StatementBrowser } from "@/features/review/bulk-browser/statement-browser";
import { ImportDialog } from "@/features/review/bulk-import/import-dialog";
import { SqlFileDropzone } from "@/features/review/bulk-import/sql-file-dropzone";
import {
  digestSqlText,
  type SqlDigest,
} from "@/features/review/bulk-import/sql-digest";
import {
  BULK_MODE_MIN_BYTES,
  BULK_MODE_MIN_STATEMENTS,
} from "@/features/review/bulk-constants";
import { EvidenceSheet } from "@/features/review/evidence-sheet";
import type { FindingListState } from "@/features/review/finding-list";
import { PrecheckResultDialog } from "@/features/review/precheck-result-dialog";
import {
  FlowPicker,
  FormSection,
  MetaFields,
  SubmitConfirmDialog,
  SummaryRow,
  isoToRequestedFor,
  requestedForToIso,
} from "@/features/review/submission-dialogs";
import { SqlEditorPanel } from "@/features/review/sql-editor-panel";
import { StagePath } from "@/features/review/stage-path";
import { FlowTimelineCard } from "@/features/review/flow-timeline-card";
import {
  saveDraftSqlRequest,
  useChangeDraft,
  useCreateChangeDraftMutation,
  useCurrentUserChangeFlows,
  useFlowUpdated,
  useRevealDraftSqlMutation,
  useReviewFindings,
  useReviewRun,
  useRunDraftReviewMutation,
  useSaveDraftSqlMutation,
  useSubmitChangeDraftMutation,
  useUpdateChangeDraftMutation,
} from "@/features/review/use-draft-workspace";
import { businessErrCodeByName, describeError, describeErrorText } from "@/shared/api/error-display";
import { BusinessError, TransportError } from "@/shared/api/mutator";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { CircleAlert, Play } from "lucide-react";

/**
 * Workspace for the order submission journey (one-page continuous form +
 * sticky right-hand summary, owner ruling 2026-10-24, superseding the
 * §18.34 three-step wizard): the left column walks Change Flow -> Title & Description ->
 * SQL Change Content as one continuous form separated by hairlines; the right
 * column carries a live Order Summary card whose action area hosts the submission
 * dock (gate-mirroring readiness, blockers, submit) and the draft actions.
 * /changes/new hosts the create mode (flow + metadata + a SQL placeholder
 * until the draft exists); /changes/drafts/:id binds the same page to an
 * existing draft with the editor live.
 *
 * RCP-20261101-EPHEMERAL-PRECHECK: AI precheck is ephemeral — the AI precheck button sends the current
 * editor SQL verbatim to POST /review-runs without creating a draft, writing SQL, or adopting; the run id lives
 * only in page client state (no longer written back to the draft row by the server), and both presentation and polling
 * use the last precheck's run id. Expiry (TTL/restart, read error) is presented as "precheck result expired". Save
 * and Return is the sole writer of draft + SQL; submission sends {sql, run_id, metadata} to
 * POST /change-orders, where the server compares the reviewed text.
 *
 * The audited workspace machinery (draft editor store, explicit review,
 * monotonic phase fold, bulk browser, gate-mirroring dock, submit confirm)
 * is retained verbatim as the engine — all of its state lives at the page
 * level so the run keeps polling while the user scrolls. Nothing here
 * triggers a run by itself (opening editor does not auto-run Review).
 */

const REVIEW_TIMEOUT_MS = 5 * 60 * 1000;

const PRECHECK_TONE_CLASS = {
  idle: { text: "text-muted-foreground", dot: "bg-muted-foreground/50" },
  busy: { text: "text-foreground", dot: "bg-foreground/60" },
  pass: { text: "text-success", dot: "bg-success" },
  fail: { text: "text-destructive", dot: "bg-destructive" },
  stale: { text: "text-warning", dot: "bg-warning" },
} as const;

interface DraftMetadataSnapshot {
  title: string;
  description: string | undefined;
  requested_scheduled_for: string | null;
  rollback_enabled: boolean;
}

function draftMetadataSnapshot(
  title: string,
  description: string,
  requestedFor: string,
  rollbackEnabled = true,
): DraftMetadataSnapshot {
  return {
    title: title.trim(),
    description: description === "" ? undefined : description,
    requested_scheduled_for: requestedForToIso(requestedFor),
    rollback_enabled: rollbackEnabled,
  };
}

function sameDraftMetadata(
  left: DraftMetadataSnapshot,
  right: DraftMetadataSnapshot,
): boolean {
  return left.title === right.title
    && left.description === right.description
    && left.requested_scheduled_for === right.requested_scheduled_for
    && left.rollback_enabled === right.rollback_enabled;
}


export default function SubmissionWizard({ mode }: { mode: "create" | "draft" }) {
  const { t, i18n } = useTranslation();
  const { draftId } = useParams<{ draftId: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const mainColumnRef = useRef<HTMLElement | null>(null);
  const [mainDialogAnchor, setMainDialogAnchor] = useState<{
    left: number;
    maxWidth: number;
  } | null>(null);
  const [precheckModalOpen, setPrecheckModalOpen] = useState(false);
  const [precheckRequestError, setPrecheckRequestError] = useState<string | null>(null);

  // ---- create-mode state ----
  const [selectedFlowId, setSelectedFlowId] = useState<string | null>(null);
  const [createTitle, setCreateTitle] = useState("");
  const [createDescription, setCreateDescription] = useState("");
  const [createRequestedFor, setCreateRequestedFor] = useState("");
  const [createRollbackEnabled, setCreateRollbackEnabled] = useState(true);
  const [stepError, setStepError] = useState<string | null>(null);
  // Implicit drafts (owner ruling 2026-10-24 + RCP-20261101): the server
  // submission envelope is created silently by Save and Return — now the ONLY
  // writer of draft + SQL (neither precheck nor submission adopts or writes SQL). Until adoption
  // the SQL lives in plain local state; after adoption the page behaves
  // exactly like the draft mode, in place.
  const [localDraftId, setLocalDraftId] = useState<string | null>(null);
  const adoptedDraftIdRef = useRef<string | null>(null);
  // Whether both halves of adoption (POST shell creation + SQL PUT) have completed: an adoption whose PUT failed
  // is an "incomplete action", so subsequent acting operations finish it first (completing what the user already initiated,
  // not saving new edits behind their back — owner ruling 2026-10-31).
  const adoptionSqlCompleteRef = useRef(false);
  const [adoptionPending, setAdoptionPending] = useState(false);
  const [createSql, setCreateSql] = useState("");
  const createSqlRef = useRef(createSql);
  const ensureDraftPromiseRef = useRef<Promise<string | null> | null>(null);
  const reviewStartInFlightRef = useRef(false);
  // Reactive state mirroring reviewStartInFlightRef: between the click and the actual runDraftReview
  // call sit ensureDraft and SQL save (which can take seconds for large SQL), during which
  // reviewMutation.isPending is still false — without entering pending immediately, the button gives no in-progress
  // feedback and users click repeatedly, causing duplicate prechecks.
  const [reviewStartPending, setReviewStartPending] = useState(false);
  const adopted = mode === "create" && localDraftId !== null;
  /** Route drafts and adopted implicit drafts share the persisted metadata
   * state; create-before-adoption keeps metadata in plain local state and
   * carries it in the adoption create call. */
  const metaBound = mode === "draft" || adopted;
  /** The draft this page currently operates on (explicit route draft or the
   * silently adopted implicit one). */
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

  // ---- draft-mode metadata state (keyed remount via draft.id below) ----
  const [metaTitle, setMetaTitle] = useState("");
  const [metaDescription, setMetaDescription] = useState("");
  // §18.56 Option B: requested execution time (datetime-local; empty = clear).
  const [metaRequestedFor, setMetaRequestedFor] = useState("");
  const [metaRollbackEnabled, setMetaRollbackEnabled] = useState(true);
  const metadataEditRevisionRef = useRef(0);
  const visibleMetadataRef = useRef<DraftMetadataSnapshot>(
    draftMetadataSnapshot("", "", "", true),
  );

  // ---- workspace engine; lives at the page level so the run keeps polling
  // while the user scrolls the form or reads the summary. ----
  const store = useDraftEditorStore();
  const dirty = useDraftEditorStore(selectDirty);
  const flowUpdated = useFlowUpdated(draft?.flow_id ?? null);

  const [loadValue, setLoadValue] = useState<{ text: string; nonce: number } | null>(null);
  const [locate, setLocate] = useState<{ target: string; nonce: number } | null>(null);
  const [bulkLocate, setBulkLocate] = useState<{ ordinal: number; nonce: number } | null>(null);
  const [importedDigest, setImportedDigest] = useState<SqlDigest | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [droppedFile, setDroppedFile] = useState<File | null>(null);
  const [evidenceFindings, setEvidenceFindings] = useState<ReviewFinding[]>([]);
  const [actionError, setActionError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [confirmSubmit, setConfirmSubmit] = useState(false);

  useEffect(() => {
    if (!precheckModalOpen && !confirmSubmit) return;
    const updateAnchor = (): void => {
      const mainEl = mainColumnRef.current;
      if (mainEl === null || typeof window === "undefined") return;
      const rect = mainEl.getBoundingClientRect();
      if (rect.width <= 0 || rect.width >= window.innerWidth * 0.85) {
        setMainDialogAnchor(null);
        return;
      }
      setMainDialogAnchor({
        left: Math.round(rect.left + rect.width / 2),
        maxWidth: Math.max(320, Math.round(rect.width - 24)),
      });
    };
    updateAnchor();
    window.addEventListener("resize", updateAnchor);
    const observer =
      typeof ResizeObserver === "function" && mainColumnRef.current !== null
        ? new ResizeObserver(updateAnchor)
        : null;
    if (observer !== null && mainColumnRef.current !== null) {
      observer.observe(mainColumnRef.current);
    }
    return () => {
      window.removeEventListener("resize", updateAnchor);
      observer?.disconnect();
    };
  }, [confirmSubmit, precheckModalOpen]);
  // ---- Ephemeral precheck (RCP-20261101) ----
  // The run id returned by the last POST /review-runs and the SQL sent verbatim at that time: both
  // live solely in page client state. Staleness is a pure local comparison (editor text != reviewed
  // text); the server no longer writes the run id back to the draft row, and results cannot be recovered after page unmount.
  const [lastPrecheckRunId, setLastPrecheckRunId] = useState<string | null>(null);
  // The last reviewed text is a render-relevant fact (staleness presentation), so use state rather than a ref —
  // reading a ref during render is rightly forbidden by react-hooks/refs.
  const [lastPrecheckSql, setLastPrecheckSql] = useState<string | null>(null);
  // Polling read found the Run disappeared (TTL expired / server restart, 404 or 1002) — present "expired" and clear
  // the previous Run display; reset upon a fresh precheck.

  // tabs-02 pattern (owner ruling 2026-10-24): controlled results tab pair so
  // the content switch runs through TransitionPanel's slide+blur motion —
  // same behaviour as the mine page tabs.
  const [resultsTab, setResultsTab] = useState<"overview" | "findings">("overview");
  const [timedOutRunKey, setTimedOutRunKey] = useState<string | null>(null);

  // Attach the editor store once per draft: the server draft carries no SQL
  // plaintext (it never does), so the editor starts empty until an explicit
  // reveal loads it.
  const attachedDraftId = useRef<string | null>(null);
  // The implicit-adoption path has just put the current session's plaintext
  // into the store. Its first draft query may land afterwards; that response
  // enriches metadata/version but must not masquerade as a fresh visit and
  // wipe the SQL. A component remount resets this ref, preserving Reveal.
  const sessionContentDraftId = useRef<string | null>(null);
  // Adoption seeds the visible metadata from the local create form. Preserve
  // edits made while POST or the first draft query is in flight instead of
  // replacing them with the older server snapshot when that query lands.
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
      // A normal draft route starts from persisted metadata.
      const hydratedRollbackEnabled =
        draft.ticket_type === "rollback" ? false : (draft.rollback_enabled ?? true);
      setMetaTitle(draft.title ?? "");
      setMetaDescription(draft.description ?? "");
      setMetaRequestedFor(isoToRequestedFor(draft.requested_scheduled_for));
      setMetaRollbackEnabled(hydratedRollbackEnabled);
      visibleMetadataRef.current = draftMetadataSnapshot(
        draft.title ?? "",
        draft.description ?? "",
        isoToRequestedFor(draft.requested_scheduled_for),
        hydratedRollbackEnabled,
      );
    }
  }, [draft, store]);

  // Clear client-only draft editor state when leaving the wizard or entering a
  // fresh unadopted create session so a previously submitted order never leaves
  // /changes/new stuck in read-only state (and SQL plaintext never lingers in
  // module memory after unmount).
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

  // Bulk mode (frontend PRD F5): drafts beyond one max-statement size or a
  // thousand statements never mount the Monaco editor — the virtualized
  // browser takes over so the full SQL is never rendered.
  const localSqlText = mode === "create" && localDraftId === null ? createSql : store.sql;
  const isBulk =
    (draft?.sql_size_bytes ?? 0) > BULK_MODE_MIN_BYTES ||
    (draft?.statement_count ?? 0) > BULK_MODE_MIN_STATEMENTS ||
    localSqlText.length > BULK_MODE_MIN_BYTES ||
    // RCP-20261101: now that import is purely local, bulk mode is determined from the local digest (no server
    // row exists to read back sql_size_bytes/statement_count).
    (importedDigest?.statementCount ?? 0) > BULK_MODE_MIN_STATEMENTS;
  const bulkDigest = isBulk && localSqlText !== "" ? importedDigest : null;

  const digestSourceRef = useRef<string | null>(null);
  useEffect(() => {
    if (!isBulk || localSqlText === "" || digestSourceRef.current === localSqlText) return;
    let cancelled = false;
    const timer = setTimeout(() => {
      if (!cancelled) {
        digestSourceRef.current = localSqlText;
        setImportedDigest(digestSqlText(localSqlText));
      }
    }, 0);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [isBulk, localSqlText]);

  const {
    run,
    phase,
    isPending: reviewRunPending,
    isFetching: reviewRunFetching,
    isError: reviewRunError,
    error: reviewRunErrorDetail,
    refetch: refetchReviewRun,
  } = useReviewRun(lastPrecheckRunId);
  const findingsQuery = useReviewFindings(
    lastPrecheckRunId,
    run !== null && isTerminalPhase(phase),
  );
  const hasPrecheckResult = lastPrecheckRunId !== null;
  // RCP-20261101: staleness = editor text != last reviewed text (holds only when a precheck result exists —
  // all legacy coupling to draft revision/dirty is obsolete).
  const sqlChangedSincePrecheck =
    hasPrecheckResult && lastPrecheckSql !== null && localSqlText !== lastPrecheckSql;
  // When polling the Run fails, distinguish "expired" (Run disappeared: HTTP 404 or business code 1002) from real
  // failures — the former switches to expired presentation while the latter keeps the error display, never masking real failures.
  // Pure derivation (no state write): retaining the runId polling reference while expired has no side effects — the read side
  // already short-circuits on expired, and recovery is driven by the user re-running precheck.
  const precheckExpired =
    hasPrecheckResult &&
    reviewRunError &&
    ((reviewRunErrorDetail instanceof TransportError && reviewRunErrorDetail.problem.status === 404) ||
      (reviewRunErrorDetail instanceof BusinessError &&
        reviewRunErrorDetail.err_code === businessErrCodeByName("RESOURCE_NOT_FOUND")));
  const phasePresented = presentPhase(phase, sqlChangedSincePrecheck, hasPrecheckResult);
  const hasPresentedRun = hasPrecheckResult;

  const activeRunKey =
    phase === "queued" || phase === "running" ? `${phase}:${run?.id ?? ""}` : null;
  const timedOut = activeRunKey !== null && timedOutRunKey === activeRunKey;
  useEffect(() => {
    if (activeRunKey === null) return;
    const timer = setTimeout(() => {
      setTimedOutRunKey(activeRunKey);
    }, REVIEW_TIMEOUT_MS);
    return () => {
      clearTimeout(timer);
    };
  }, [activeRunKey]);

  // Leave guard: unsaved SQL would be lost silently in a refresh — the
  // pre-adoption local SQL of the create mode counts too.
  const unsavedCreateSql = mode === "create" && localDraftId === null && createSql !== "";
  useEffect(() => {
    if (!dirty && !unsavedCreateSql) return;
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", handler);
    return () => { window.removeEventListener("beforeunload", handler); };
  }, [dirty, unsavedCreateSql]);

  const invalidateAll = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ["change-draft", activeDraftId] });
    await queryClient.invalidateQueries({ queryKey: ["review-run"] });
  }, [queryClient, activeDraftId]);

  // §18.74: monotonically sync higher row version into store when query refresh returns it — terminal review runs
  // asynchronously advance version server-side, and failing to sync would leave subsequent If-Match headers stale.
  useEffect(() => {
    if (draft === null) return;
    const saved = useDraftEditorStore.getState().savedVersion;
    if (saved !== null && draft.version > saved) {
      store.markVersioned(draft.version, draft.state);
    }
  }, [draft, store]);

  // Ephemeral Runs no longer advance draft row version (RCP-20261101), so Run terminal state requires no draft refetch.

  const showActionError = useCallback((error: unknown, operationId: string) => {
    const msg = describeErrorText(describeError(error, operationId), { detailSeparator: " " });
    setActionError(msg);
    appToast.error(msg);
  }, []);

  const saveMutation = useSaveDraftSqlMutation({
    onSuccess: (savedDraft, { sql }) => {
      store.markSaved(savedDraft.version, savedDraft.state, sql);
      setActionError(null);
      appToast.success(t("common.savedToast"));
    },
    onError: (error) => { showActionError(error, "replaceDraftSql"); },
  });

  const editorIsCurrent = useCallback((expectedSql: string): boolean => {
    const current = useDraftEditorStore.getState();
    return current.draftId !== null
      && current.sql === expectedSql
      && !selectDirty(current);
  }, []);

  const savedSqlMatchesEditor = useCallback((savedSql: string): boolean => {
    const current = useDraftEditorStore.getState();
    return current.savedSql === savedSql && editorIsCurrent(savedSql);
  }, [editorIsCurrent]);

  const metadataIsCurrent = useCallback(
    (snapshot: DraftMetadataSnapshot, revision: number): boolean =>
      metadataEditRevisionRef.current === revision
      && sameDraftMetadata(visibleMetadataRef.current, snapshot),
    [],
  );

  const createMutation = useCreateChangeDraftMutation({
    onError: (error) => { showActionError(error, "createChangeDraft"); },
  });

  /**
   * Implicit-draft adoption (owner ruling 2026-10-24): create the server
   * submission envelope — silently, in place — on the first acting
   * operation. The flow + title gate stays (the envelope cannot exist
   * without them) but it surfaces as a near-field error at the moment of
   * the action, never as a mandatory user step. Local SQL travels with the
   * adoption so the editor never blanks.
   */
  const ensureDraft = useCallback(async (): Promise<string | null> => {
    if (ensureDraftPromiseRef.current !== null) return ensureDraftPromiseRef.current;
    const needsAdoption = activeDraftId === null && adoptedDraftIdRef.current === null;
    if (needsAdoption) {
      // Keep validation synchronous: rejection must set the error and clear pending in the same synchronous
      // batch as the click (validation errors must not appear as "in progress"), and must never leave a settled promise
      // that would cause the next click to be swallowed by deduplication.
      const rejectDraft = (message: string): null => {
        setStepError(message);
        reviewStartInFlightRef.current = false;
        setReviewStartPending(false);
        return null;
      };
      const requestedIssue = (value: string): string | null => {
        if (value === "") return null;
        if (requestedForToIso(value) === null) return t("precheck.wizard.requestedScheduleInvalid");
        if (new Date(value).getTime() <= Date.now()) return t("precheck.wizard.requestedSchedulePast");
        return null;
      };
      if (selectedFlowId === null) {
        return rejectDraft(t("precheck.wizard.error.needFlow"));
      }
      if (createTitle.trim() === "") {
        return rejectDraft(t("precheck.wizard.error.needTitle"));
      }
      const issue = requestedIssue(createRequestedFor);
      if (issue !== null) {
        return rejectDraft(issue);
      }
    }
    const completion = (async (): Promise<string | null> => {
      // Complete partial adoption: when the SQL PUT of a prior adoption did not land (after POST authoritatively created the shell),
      // finish it first so the current acting operation faces saved SQL consistent with the editor; if the retry still
      // fails, abort the current operation without creating a second draft. This finishes an action the user already
      // initiated rather than saving new edits unprompted (owner ruling 2026-10-31).
      const adoptedDraftId = adoptedDraftIdRef.current;
      if (adoptedDraftId !== null && !adoptionSqlCompleteRef.current) {
        const current = useDraftEditorStore.getState();
        if (current.sql !== "" && current.savedVersion !== null) {
          try {
            const saved = await saveDraftSqlRequest(adoptedDraftId, current.sql, current.savedVersion);
            store.markSaved(saved.version, saved.state, current.sql);
            // Same stale-cache hazard as the first-adoption PUT below: the
            // detail query may hold the envelope-only snapshot (has_sql
            // false) from the failed first attempt's window.
            void queryClient.invalidateQueries({ queryKey: ["change-draft", adoptedDraftId] });
          } catch (error) {
            showActionError(error, "replaceDraftSql");
            return null;
          }
        }
        adoptionSqlCompleteRef.current = true;
      }
      if (activeDraftId !== null) return activeDraftId;
      if (adoptedDraftId !== null) return adoptedDraftId;
      setStepError(null);
      setAdoptionPending(true);
      const metadataAtCreate = visibleMetadataRef.current;
      let created: ChangeDraft;
      try {
        created = await createMutation.mutateAsync({
          flow_id: selectedFlowId as string,
          title: metadataAtCreate.title,
          description: metadataAtCreate.description,
          requested_scheduled_for: metadataAtCreate.requested_scheduled_for ?? undefined,
          rollback_enabled: metadataAtCreate.rollback_enabled,
        });
      } catch (error) {
        showActionError(error, "createChangeDraft");
        return null;
      }

      // The POST is now authoritative: keep this page attached to the created
      // row even if the following SQL PUT fails. Retrying the first action must
      // update this draft, never create an orphan replacement.
      const sqlAtCreate = createSqlRef.current;
      adoptedDraftIdRef.current = created.id;
      sessionContentDraftId.current = created.id;
      sessionMetadataDraftId.current = created.id;
      const metadataAtAdoption = visibleMetadataRef.current;
      setMetaTitle(metadataAtAdoption.title);
      setMetaDescription(metadataAtAdoption.description ?? "");
      setMetaRequestedFor(isoToRequestedFor(metadataAtAdoption.requested_scheduled_for));
      setMetaRollbackEnabled(metadataAtAdoption.rollback_enabled);
      store.attach({
        id: created.id,
        state: created.state,
        version: created.version,
        sql: null,
      });
      if (sqlAtCreate !== "") store.setSql(sqlAtCreate);
      setLocalDraftId(created.id);
      setLoadValue({ text: sqlAtCreate, nonce: Date.now() });

      if (sqlAtCreate !== "") {
        try {
          const saved = await saveDraftSqlRequest(created.id, sqlAtCreate, created.version);
          store.markSaved(saved.version, saved.state, sqlAtCreate);
          // The adoption PUT changes the draft row (has_sql/sql_size_bytes/
          // statement_count/version); without this invalidation the detail
          // query keeps serving the envelope-only snapshot for a full
          // staleTime window, and a quick re-entry renders a SQL-less draft
          // (empty editor, no auto-reveal). The completion PUT above shares
          // the hazard; every plain save path invalidates too.
          void queryClient.invalidateQueries({ queryKey: ["change-draft", created.id] });
        } catch (error) {
          showActionError(error, "replaceDraftSql");
          return null;
        }
      }
      adoptionSqlCompleteRef.current = true;

      // The create or SQL PUT can span user typing. markSaved acknowledges only
      // the submitted text, so any later input remains visible and dirty.
      const sqlAfterSave = createSqlRef.current;
      if (sqlAfterSave !== sqlAtCreate) store.setSql(sqlAfterSave);
      return created.id;
    })();
    ensureDraftPromiseRef.current = completion;
    try {
      return await completion;
    } finally {
      if (ensureDraftPromiseRef.current === completion) {
        ensureDraftPromiseRef.current = null;
        setAdoptionPending(false);
      }
    }
  }, [activeDraftId, createMutation, createRequestedFor, createTitle, queryClient, selectedFlowId, showActionError, store, t]);

  // Bulk import lands SQL locally only (RCP-20261101): Save and Return is the only server write;
  // in unadopted create mode the imported text enters local createSql state, while in adopted/draft mode it enters the editor store.
  const handleImportConfirm = useCallback((text: string, digest: SqlDigest) => {
    setImportOpen(false);
    setImportedDigest(digest);
    digestSourceRef.current = text;
    setBulkLocate(null);
    if (mode === "create" && localDraftId === null) {
      createSqlRef.current = text;
      setCreateSql(text);
    } else {
      store.setSql(text);
    }
  }, [localDraftId, mode, store]);

  const revealMutation = useRevealDraftSqlMutation(activeDraftId, {
    onSuccess: (payload) => {
      if (draft === null) return;
      store.attach({
        id: draft.id,
        state: draft.state,
        version: draft.version,
        sql: payload.sql,
      });
      setLoadValue({ text: payload.sql, nonce: Date.now() });
      setActionError(null);
    },
    onError: (error) => { showActionError(error, "revealDraftSql"); },
  });
  // Auto-load the saved SQL on draft entry (owner ruling 2026-11-01: the
  // read-SQL threshold was over-design — display it directly; precheck must
  // always have the editor text, bulk drafts included).
  const autoRevealedRef = useRef<string | null>(null);
  useEffect(() => {
    if (mode !== "draft" || draft === null) return;
    if (!draft.has_sql) return;
    const storeState = useDraftEditorStore.getState();
    if (storeState.draftId !== draft.id || storeState.savedSql !== null) return;
    if (autoRevealedRef.current === draft.id || revealMutation.isPending) return;
    autoRevealedRef.current = draft.id;
    revealMutation.mutate();
  }, [mode, draft, revealMutation]);

  // Ephemeral AI precheck (RCP-20261101): send current editor SQL verbatim for review without creating a draft, writing
  // SQL, or adopting; the run id returns to client state and drives both presentation and polling.
  const reviewMutation = useRunDraftReviewMutation({
    onSuccess: (started, { body }) => {
      setPrecheckRequestError(null);
      if (started.run_id === "") return;
      setLastPrecheckSql(body.sql);
      setLastPrecheckRunId(started.run_id);
    },
    onError: (error) => {
      setPrecheckRequestError(describeErrorText(describeError(error, "runDraftReview"), { detailSeparator: " " }));
    },
  });

  const metaSaveMutation = useUpdateChangeDraftMutation({
    onSuccess: () => {
      setActionError(null);
    },
    onError: (error) => { showActionError(error, "updateChangeDraft"); },
  });

  const submitMutation = useSubmitChangeDraftMutation({
    onSuccess: (created) => {
      store.markServerState("submitted");
      setSubmitError(null);
      setConfirmSubmit(false);
      appToast.success(t("common.createdToast"), {
        description: created.display_number,
      });
      // F6 flow: submission lands on the immutable order detail (the order
      // id is returned by the submission response itself).
      void navigate(`/changes/orders/${created.id}`);
    },
    onError: (error) => {
      // Rejection keeps the dialog open with the backend's answer — the UI
      // never flips to a success state on its own (acceptance gate: backend rejection has no fake
      // success); the user can cancel or retry from the same context.
      const msg = describeErrorText(describeError(error, "submitChangeDraft"), { detailSeparator: " " });
      setSubmitError(msg);
      appToast.error(msg);
    },
  });

  // Remote schema/table/column prefetching is intentionally omitted from the
  // change submission wizard so opening or editing a draft never fans out
  // metadata queries against target datasources; SqlEditorPanel uses its
  // default EMPTY_CATALOG (local SQL keyword completions + syntax highlighting).

  // Owner ruling 2026-10-31 + RCP-20261101: precheck never piggybacks a save — the AI precheck button
  // sends current editor SQL verbatim to the ephemeral endpoint; create mode only validates near-field that a flow is selected (no
  // draft shell to adopt). Clicking still sets pending synchronously (reviewStartPending, owner priority ruling)
  // so a slow start never induces duplicate prechecks.
  const handleRunReview = useCallback(() => {
    if (reviewStartInFlightRef.current) return;
    reviewStartInFlightRef.current = true;
    setReviewStartPending(true);
    const finish = () => {
      reviewStartInFlightRef.current = false;
      setReviewStartPending(false);
    };
    if (mode === "create" && selectedFlowId === null && draft?.flow_id == null) {
      setStepError(t("precheck.wizard.error.needFlow"));
      finish();
      return;
    }
    setStepError(null);
    setPrecheckRequestError(null);
    setResultsTab("overview");
    setPrecheckModalOpen(true);
    reviewMutation.mutate(
      {
        body: {
          flow_id: (draft?.flow_id ?? selectedFlowId) as string,
          draft_id: activeDraftId ?? undefined,
          sql: localSqlText,
        },
        language: i18n.language,
      },
      { onSettled: finish },
    );
  }, [activeDraftId, draft?.flow_id, i18n.language, localSqlText, mode, reviewMutation, selectedFlowId, t]);

  const handleConfirmSubmit = (): void => {
    submitMutation.mutate({
      body: {
        flow_id: (draft?.flow_id ?? selectedFlowId) as string,
        draft_id: activeDraftId ?? undefined,
        sql: localSqlText,
        run_id: lastPrecheckRunId as string,
        metadata: mode === "create"
          ? {
              title: (metaBound ? metaTitle : createTitle).trim(),
              description: (metaBound ? metaDescription : createDescription) === ""
                ? undefined
                : (metaBound ? metaDescription : createDescription),
              requested_scheduled_for:
                requestedForToIso(metaBound ? metaRequestedFor : createRequestedFor) ?? undefined,
              rollback_enabled: metaBound ? metaRollbackEnabled : createRollbackEnabled,
            }
          : undefined,
      },
      beforeSubmit:
        metaBound && activeDraftId !== null
          ? async () => {
              await metaSaveMutation.mutateAsync({
                draftId: activeDraftId,
                body: visibleMetadataRef.current,
              });
            }
          : undefined,
    });
  };

  // ---- Save and Return: the single secondary action (owner ruling 2026-10-24,
  // Save Info folded away) — persist SQL then metadata, then leave. ----
  const saveDraftAndLeave = (): void => {
    const leave = () => {
      appToast.success(t("common.savedToast"));
      void navigate("/changes/mine");
    };
    const metadataSnapshot = visibleMetadataRef.current;
    const metadataRevision = metadataEditRevisionRef.current;
    const metadataUnchanged = () => metadataIsCurrent(metadataSnapshot, metadataRevision);
    if (mode === "create" && (activeDraftId === null || !adoptionSqlCompleteRef.current)) {
      // The one explicit opt-in to the draft: adopt implicitly, then leave.
      // Half-completed adoption (POST created shell, SQL PUT failed) also goes through here: ensureDraft first finishes
      // the save initiated by the user (§18.90), using the version from adoption time for If-Match
      // rather than mismatching against an already-advanced version on a second PUT.
      void (async () => {
        const draftId = await ensureDraft();
        if (draftId === null) return;
        const current = useDraftEditorStore.getState();
        if (!selectDirty(current)) {
          if (metadataUnchanged()) leave();
          return;
        }
        saveMutation.mutate({ draftId, sql: current.sql }, {
          onSuccess: (_saved, variables) => {
            if (savedSqlMatchesEditor(variables.sql) && metadataUnchanged()) leave();
          },
        });
      })();
      return;
    }
    if (metaBound && metaTitle.trim() === "") {
      setStepError(t("precheck.wizard.error.needTitle"));
      return;
    }
    const current = useDraftEditorStore.getState();
    if (selectDirty(current)) {
      // SQL first: markSaved keeps the store version chain monotonic for the
      // metadata PUT that follows.
      saveMutation.mutate({ draftId: activeDraftId as string, sql: current.sql }, {
        onSuccess: (_saved, variables) => {
          if (!savedSqlMatchesEditor(variables.sql)) return;
          if (metaBound) {
            metaSaveMutation.mutate({ draftId: activeDraftId as string, body: metadataSnapshot }, {
              onSuccess: () => {
                if (editorIsCurrent(variables.sql) && metadataUnchanged()) leave();
              },
            });
          }
          else leave();
        },
      });
      return;
    }
    if (metaBound) {
      metaSaveMutation.mutate({ draftId: activeDraftId as string, body: metadataSnapshot }, {
        onSuccess: () => {
          if (editorIsCurrent(current.sql) && metadataUnchanged()) leave();
        },
      });
    }
    else leave();
  };

  const precheckProgress = computePrecheckProgress({
    starting:
      reviewStartPending ||
      reviewMutation.isPending ||
      (hasPresentedRun && !reviewRunError && (run === null || reviewRunPending)),
    phase: phasePresented,
    stageResults: run?.stage_results,
    findingsPending: run !== null && isTerminalPhase(phasePresented) && findingsQuery.isPending,
  });

  const isSubmitted =
    activeDraftId !== null
      ? store.draftId === activeDraftId && store.serverState === "submitted"
      : submitMutation.isSuccess;

  const presentedPhaseForDock: RunPhase = precheckExpired ? "idle" : phasePresented;
  const submitDecision = submissionDecision({
    draftState:
      activeDraftId !== null
        ? isSubmitted
          ? "submitted"
          : "draft"
        : null,
    runPhase: presentedPhaseForDock,
    gate: run?.gate ?? null,
    sqlChangedSincePrecheck,
    flowUpdated,
  });

  const handleAttemptSubmit = (): void => {
    // RCP-20261101: submission no longer saves SQL first — the request body carries editor SQL and the precheck run
    // id for verbatim server-side comparison; only perform create-mode near-field validation here (flow/title/
    // requested time), opening the confirmation dialog once valid.
    if (mode === "create") {
      if (selectedFlowId === null && draft?.flow_id == null) {
        setStepError(t("precheck.wizard.error.needFlow"));
        return;
      }
      if ((metaBound ? metaTitle : createTitle).trim() === "") {
        setStepError(t("precheck.wizard.error.needTitle"));
        return;
      }
      const requestedFor = metaBound ? metaRequestedFor : createRequestedFor;
      if (requestedFor !== "") {
        if (requestedForToIso(requestedFor) === null) {
          setStepError(t("precheck.wizard.requestedScheduleInvalid"));
          return;
        }
        if (new Date(requestedFor).getTime() <= Date.now()) {
          setStepError(t("precheck.wizard.requestedSchedulePast"));
          return;
        }
      }
    }
    setStepError(null);
    setSubmitError(null);
    setPrecheckModalOpen(false);
    setConfirmSubmit(true);
  };

  // ---- loading / error surfaces ----
  if (mode === "create") {
    return renderPage();
  }
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
  return renderPage();

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


  /** Summary values share one source of truth with the form fields. */
  function summaryValues() {
    const presented = presentPhase(phase, sqlChangedSincePrecheck, hasPrecheckResult);
    // F-UX-001: summary and metadata fields share the same source — both draft mode (reopening a saved draft) and
    // adopted create mode read meta* (hydrated when the draft loads); old code branched on adopted,
    // causing draft mode to read the permanently empty createTitle and always display "Unfilled" for the title.
    const title = metaBound ? metaTitle.trim() : createTitle.trim();
    const requestedFor = metaBound ? metaRequestedFor : createRequestedFor;
    const rollbackEnabled =
      draft?.ticket_type === "rollback"
        ? false
        : metaBound
          ? metaRollbackEnabled
          : createRollbackEnabled;
    const datasources = [...new Set((flow?.stages ?? []).map((stage) => stage.datasource_name))];
    return {
      presented,
      title: title === "" ? t("precheck.wizard.summary.empty") : title,
      sql:
        run?.statement_count != null
          ? t("precheck.wizard.summary.statements", { count: run.statement_count })
          : (mode === "draft" || adopted) && draft?.has_sql
            ? t("precheck.wizard.summary.saved")
            : t("precheck.wizard.summary.empty"),
      requestedFor:
        requestedFor === ""
          ? t("precheck.wizard.summary.unselected")
          : requestedFor.replace("T", " "),
      rollbackEnabled,
      isRollbackDraft: draft?.ticket_type === "rollback",
      datasources:
        datasources.length > 0
          ? datasources.join(t("common.listSeparator"))
          : t("precheck.wizard.summary.unselected"),
    };
  }

  function renderSummaryCard(): ReactNode {
    const values = summaryValues();
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
              muted={precheckStatus().tone === "idle"}
              value={
                <span
                  className={`inline-flex items-center gap-1.5 ${PRECHECK_TONE_CLASS[precheckStatus().tone].text}`}
                  data-testid="wizard-summary-precheck"
                >
                  <span
                    aria-hidden
                    className={`size-1.5 rounded-full ${PRECHECK_TONE_CLASS[precheckStatus().tone].dot}`}
                  />
                  {precheckStatus().label}
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
          </dl>
        </CardContent>
        <div className="flex flex-col gap-2.5 px-4">
          {stepError !== null && (
            <p role="alert" className="text-destructive text-sm" data-testid="wizard-step-error">
              {stepError}
            </p>
          )}
          {actionError !== null && (
            <p role="alert" className="text-destructive text-sm" data-testid="wizard-action-error">
              {actionError}
            </p>
          )}
          {/* Save and Return is the single draft persistence action in the summary card;
           * precheck progress, gate blockers, and Submit for Approval live inside precheck-dialog. */}
          <PendingButton
            variant="outline"
            className="h-9 w-full rounded-lg text-xs font-semibold"
            pending={
              adoptionPending ||
              metaSaveMutation.isPending ||
              saveMutation.isPending ||
              createMutation.isPending
            }
            onClick={saveDraftAndLeave}
            data-testid="wizard-save-draft"
          >
            {t("precheck.wizard.saveAndReturn")}
          </PendingButton>
        </div>
      </Card>
    );
  }

  function renderFlowSection(): ReactNode {
    if (mode === "draft" || adopted) {
      return (
        <FormSection
          title={t("precheck.wizard.section.flow")}
          hint={t("precheck.wizard.section.flowFrozenHint")}
        >
          <div className="rounded-xl border p-4" data-testid="wizard-flow-frozen">
            <p className="text-sm font-semibold">{flow?.name ?? draft?.flow_id}</p>
            <div className="mt-2">
              <StagePath flow={flow} />
            </div>
          </div>
        </FormSection>
      );
    }
    return (
      <FormSection
        title={t("precheck.wizard.section.flow")}
        hint={t("precheck.wizard.section.flowHint")}
      >
        {flowsQuery.isPending && <LoadingState variant="table" columns={3} />}
        {flowsQuery.isError && (
          <ErrorState
            error={flowsQuery.error}
            operationId="listCurrentUserFlows"
            onRetry={() => void flowsQuery.refetch()}
          />
        )}
        {flowsQuery.isSuccess && flows.length === 0 && (
          <div className="rounded-lg border">
            <EmptyState
              title={t("precheck.new.empty")}
              testId="wizard-flow-empty"
            />
          </div>
        )}
        {flows.length > 0 && (
          <FlowPicker flows={flows} selectedFlowId={selectedFlowId} onSelect={(id) => { setSelectedFlowId(id); setStepError(null); }} />
        )}
      </FormSection>
    );
  }

  function renderMetaSection(): ReactNode {
    return (
      <FormSection
        title={t("precheck.wizard.section.meta")}
        hint={t("precheck.wizard.section.metaHint")}
      >
        <MetaFields
          title={metaBound ? metaTitle : createTitle}
          description={metaBound ? metaDescription : createDescription}
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
          requestedFor={metaBound ? metaRequestedFor : createRequestedFor}
          onRequestedForChange={(value) => {
            metadataEditRevisionRef.current += 1;
            visibleMetadataRef.current = {
              ...visibleMetadataRef.current,
              requested_scheduled_for: requestedForToIso(value),
            };
            if (metaBound) {
              setMetaRequestedFor(value);
            } else {
              setCreateRequestedFor(value);
            }
            setStepError(null);
          }}
          requestedForError={
            metaBound
              ? metaRequestedFor !== "" && new Date(metaRequestedFor).getTime() <= Date.now()
                ? t("precheck.wizard.requestedSchedulePast")
                : null
              : createRequestedFor !== "" && requestedForToIso(createRequestedFor) === null
                ? t("precheck.wizard.requestedScheduleInvalid")
                : createRequestedFor !== "" && new Date(createRequestedFor).getTime() <= Date.now()
                  ? t("precheck.wizard.requestedSchedulePast")
                  : null
          }
          titleError={stepError}
          rollbackEnabled={metaBound ? metaRollbackEnabled : createRollbackEnabled}
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
          isRollbackDraft={draft?.ticket_type === "rollback"}
          sourceOrderId={draft?.source_order_id ?? null}
          sourceOrderDisplayNumber={draft?.source_order_display_number ?? null}
        />
      </FormSection>
    );
  }

  function buildFindingsState(): FindingListState {
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
    if (findingsQuery.isError) {
      return { status: "error", error: findingsQuery.error };
    }
    if (findingsQuery.isPending) {
      return { status: "loading" };
    }
    const findings = findingsQuery.data;
    const incomplete =
      phasePresented === "failed" ||
      phasePresented === "partial" ||
      (phasePresented === "blocked" && findings.length === 0);
    return {
      status: incomplete ? "incomplete" : "ready",
      findings,
    };
  }

  function renderSqlSection(): ReactNode {
    const draftMode = mode === "draft" || adopted;
    const editorValue = draftMode ? store.sql : createSql;
    const editorChange = draftMode
      ? (sql: string) => { store.setSql(sql); }
      : (sql: string) => {
          createSqlRef.current = sql;
          setCreateSql(sql);
        };

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
                onClick={() => { setPrecheckModalOpen(true); }}
                data-testid="view-precheck-results"
              >
                {t("precheck.wizard.viewResults")}
                {precheckProgress.percent > 0 && precheckProgress.percent < 100
                  ? ` (${String(precheckProgress.percent)}%)`
                  : ""}
              </Button>
            )}
            <PendingButton
              size="sm"
              pending={
                reviewStartPending ||
                reviewMutation.isPending ||
                createMutation.isPending ||
                adoptionPending
              }
              onClick={handleRunReview}
              disabled={
                (saveMutation.isPending && dirty) ||
                adoptionPending ||
                createMutation.isPending ||
                ((phasePresented === "queued" || phasePresented === "running") && !sqlChangedSincePrecheck) ||
                isSubmitted ||
                flowUpdated
              }
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
                      locate={bulkLocate}
                    />
                  )}
                </>
              ) : (
                <SqlEditorPanel
                  value={editorValue}
                  onChange={editorChange}
                  readOnly={isSubmitted || draft?.ticket_type === "rollback"}
                  loadValue={loadValue}
                  onLocate={locate}
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

  function renderPage(): ReactNode {
    return (
      <PageLayout
        data-testid={mode === "create" ? "changes-new-page" : "draft-workspace-page"}
        // The implicitly adopted draft id (e2e probes it; the URL never moves).
        data-draft-id={activeDraftId ?? undefined}
      >
        {/* No page title band (owner ruling 2026-10-24, same ruling as the
         * breadcrumb removal): the form starts directly; the summary card
         * carries the draft status. */}

        {/* Left: one continuous form. Right: sticky live summary. */}
        <div className="grid grid-cols-12 items-start gap-[30px]">
          <section
            ref={mainColumnRef}
            className="col-span-12 flex min-w-0 flex-col gap-[30px] lg:col-span-8"
            aria-label={t("precheck.wizard.pageTitle")}
          >
            {renderFlowSection()}
            {renderMetaSection()}
            {renderSqlSection()}
          </section>
          <aside
            className="col-span-12 flex flex-col gap-[30px] lg:col-span-4 lg:sticky lg:top-0"
            aria-label={t("precheck.wizard.summaryTitle")}
          >
            <SqlFileDropzone
              disabled={adoptionPending || draft?.ticket_type === "rollback"}
              onOpen={() => { setDroppedFile(null); setImportOpen(true); }}
              onDropFile={(file) => { setDroppedFile(file); setImportOpen(true); }}
            />
            {renderSummaryCard()}
            <FlowTimelineCard flow={flow} />
          </aside>
        </div>

        <PrecheckResultDialog
          open={precheckModalOpen}
          onOpenChange={setPrecheckModalOpen}
          mainDialogAnchor={mainDialogAnchor}
          precheckRequestError={precheckRequestError}
          reviewStartPending={reviewStartPending}
          reviewMutationPending={reviewMutation.isPending}
          precheckProgress={precheckProgress}
          phasePresented={phasePresented}
          run={run}
          hasPresentedRun={hasPresentedRun}
          reviewRunPending={reviewRunPending}
          reviewRunFetching={reviewRunFetching}
          reviewRunError={reviewRunError}
          reviewRunErrorDetail={reviewRunErrorDetail}
          onRefetchReviewRun={() => { void refetchReviewRun(); }}
          precheckExpired={precheckExpired}
          flowUpdated={flowUpdated}
          timedOut={timedOut}
          onRetryTimedOut={() => { void invalidateAll(); }}
          resultsTab={resultsTab}
          onResultsTabChange={setResultsTab}
          findingsState={buildFindingsState()}
          onOpenEvidence={setEvidenceFindings}
          onRefetchFindings={() => { void findingsQuery.refetch(); }}
          isBulk={isBulk}
          bulkDigest={bulkDigest}
          localSqlText={localSqlText}
          onLocate={(target) => { setLocate({ target, nonce: Date.now() }); }}
          onBulkLocate={(ordinal) => { setBulkLocate({ ordinal, nonce: Date.now() }); }}
          stepError={stepError}
          submitEnabled={submitDecision.submitEnabled}
          submitPending={submitMutation.isPending}
          adoptionPending={adoptionPending}
          onAttemptSubmit={handleAttemptSubmit}
        />

        {/* Pure local batch import (RCP-20261101): confirming writes directly into the editor without uploading. */}
        <ImportDialog
          open={importOpen}
          initialFile={droppedFile}
          onOpenChange={(open) => {
            setImportOpen(open);
            if (!open) setDroppedFile(null);
          }}
          onConfirm={handleImportConfirm}
        />
        <EvidenceSheet
          findings={evidenceFindings}
          open={evidenceFindings.length > 0}
          onOpenChange={(open) => {
            if (!open) setEvidenceFindings([]);
          }}
        />

        <SubmitConfirmDialog
          open={confirmSubmit}
          onOpenChange={setConfirmSubmit}
          mainDialogAnchor={mainDialogAnchor}
          flow={flow}
          fallbackFlowId={draft?.flow_id}
          gatePassed={run?.gate.passed === true}
          submitError={submitError}
          submitting={submitMutation.isPending}
          hasPresentedRun={hasPresentedRun}
          onReopenPrecheck={() => { setPrecheckModalOpen(true); }}
          onConfirmSubmit={handleConfirmSubmit}
        />
      </PageLayout>
    );
  }
}
