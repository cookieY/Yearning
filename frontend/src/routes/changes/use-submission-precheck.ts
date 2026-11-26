import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { ChangeDraft } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  BULK_MODE_MIN_BYTES,
  BULK_MODE_MIN_STATEMENTS,
} from "@/features/review/bulk-constants";
import {
  digestSqlText,
  type SqlDigest,
} from "@/features/review/bulk-import/sql-digest";
import {
  computePrecheckProgress,
  isTerminalPhase,
  presentPhase,
} from "@/features/review/run-state";
import {
  useReviewFindings,
  useReviewRun,
  useRunDraftReviewMutation,
} from "@/features/review/use-draft-workspace";
import { describeError, describeErrorText, isBusinessErrorCode } from "@/shared/api/error-display";
import { TransportError } from "@/shared/api/mutator";

const REVIEW_TIMEOUT_MS = 5 * 60 * 1000;

export function useMainDialogAnchor(
  mainColumnRef: RefObject<HTMLElement | null>,
  active: boolean,
) {
  const [mainDialogAnchor, setMainDialogAnchor] = useState<{
    left: number;
    maxWidth: number;
  } | null>(null);

  useEffect(() => {
    if (!active) return;
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
  }, [active, mainColumnRef]);

  return mainDialogAnchor;
}

export function useSubmissionBulkDigest(
  draft: ChangeDraft | null,
  localSqlText: string,
) {
  const [importedDigest, setImportedDigest] = useState<SqlDigest | null>(null);
  const digestSourceRef = useRef<string | null>(null);

  const isBulk =
    (draft?.sql_size_bytes ?? 0) > BULK_MODE_MIN_BYTES ||
    (draft?.statement_count ?? 0) > BULK_MODE_MIN_STATEMENTS ||
    localSqlText.length > BULK_MODE_MIN_BYTES ||
    (importedDigest?.statementCount ?? 0) > BULK_MODE_MIN_STATEMENTS;
  const bulkDigest = isBulk && localSqlText !== "" ? importedDigest : null;

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

  const applyImportedDigest = useCallback((text: string, digest: SqlDigest) => {
    digestSourceRef.current = text;
    setImportedDigest(digest);
  }, []);

  return { isBulk, bulkDigest, applyImportedDigest };
}

export interface UseSubmissionPrecheckArgs {
  mode: "create" | "draft";
  activeDraftId: string | null;
  draftFlowId: string | undefined;
  selectedFlowId: string | null;
  localSqlText: string;
  resolvedTargetDatabase: string;
  targetDatabaseForRequest: string | null | undefined;
  setStepError: (err: string | null) => void;
}

export function useSubmissionPrecheck({
  mode,
  activeDraftId,
  draftFlowId,
  selectedFlowId,
  localSqlText,
  resolvedTargetDatabase,
  targetDatabaseForRequest,
  setStepError,
}: UseSubmissionPrecheckArgs) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();

  const [precheckModalOpen, setPrecheckModalOpen] = useState(false);
  const [precheckRequestError, setPrecheckRequestError] = useState<string | null>(null);
  const [lastPrecheckTargetDatabase, setLastPrecheckTargetDatabase] = useState<string | null>(null);
  const pendingPrecheckTargetDbRef = useRef<string | null>(null);
  const reviewStartInFlightRef = useRef(false);
  // Reactive state mirroring reviewStartInFlightRef: between the click and the actual runDraftReview
  // call sit ensureDraft and SQL save (which can take seconds for large SQL), during which
  // reviewMutation.isPending is still false — without entering pending immediately, the button gives no in-progress
  // feedback and users click repeatedly, causing duplicate prechecks.
  const [reviewStartPending, setReviewStartPending] = useState(false);

  // ---- Ephemeral precheck (RCP-20261101) ----
  // The run id returned by the last POST /review-runs and the SQL sent verbatim at that time: both
  // live solely in page client state. Staleness is a pure local comparison (editor text != reviewed
  // text); the server no longer writes the run id back to the draft row, and results cannot be recovered after page unmount.
  const [lastPrecheckRunId, setLastPrecheckRunId] = useState<string | null>(null);
  // The last reviewed text is a render-relevant fact (staleness presentation), so use state rather than a ref —
  // reading a ref during render is rightly forbidden by react-hooks/refs.
  const [lastPrecheckSql, setLastPrecheckSql] = useState<string | null>(null);

  // tabs-02 pattern (owner ruling 2026-10-24): controlled results tab pair so
  // the content switch runs through TransitionPanel's slide+blur motion —
  // same behaviour as the mine page tabs.
  const [resultsTab, setResultsTab] = useState<"overview" | "findings">("overview");
  const [timedOutRunKey, setTimedOutRunKey] = useState<string | null>(null);

  const cancelReviewStartPending = useCallback(() => {
    reviewStartInFlightRef.current = false;
    setReviewStartPending(false);
  }, []);

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
  const targetDatabaseChangedSincePrecheck =
    hasPrecheckResult &&
    lastPrecheckTargetDatabase !== null &&
    resolvedTargetDatabase !== lastPrecheckTargetDatabase;
  const sqlChangedSincePrecheck =
    (hasPrecheckResult && lastPrecheckSql !== null && localSqlText !== lastPrecheckSql) ||
    targetDatabaseChangedSincePrecheck;

  // When polling the Run fails, distinguish "expired" (Run disappeared: HTTP 404 or business code 1002) from real
  // failures — the former switches to expired presentation while the latter keeps the error display, never masking real failures.
  const precheckExpired =
    hasPrecheckResult &&
    reviewRunError &&
    ((reviewRunErrorDetail instanceof TransportError && reviewRunErrorDetail.problem.status === 404) ||
      isBusinessErrorCode(reviewRunErrorDetail, "RESOURCE_NOT_FOUND"));

  const phasePresented = presentPhase(phase, sqlChangedSincePrecheck, hasPrecheckResult);
  const hasPresentedRun = hasPrecheckResult;

  const activeRunKey =
    !reviewRunError && (phase === "queued" || phase === "running") ? `${phase}:${run?.id ?? ""}` : null;
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

  const invalidateAll = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ["change-draft", activeDraftId] });
    await queryClient.invalidateQueries({ queryKey: ["review-run"] });
  }, [queryClient, activeDraftId]);

  // Ephemeral AI precheck (RCP-20261101): send current editor SQL verbatim for review without creating a draft, writing
  // SQL, or adopting; the run id returns to client state and drives both presentation and polling.
  const reviewMutation = useRunDraftReviewMutation({
    onSuccess: (started, { body }) => {
      setPrecheckRequestError(null);
      if (started.run_id === "") return;
      setLastPrecheckSql(body.sql);
      setLastPrecheckTargetDatabase(pendingPrecheckTargetDbRef.current ?? body.target_database ?? "");
      setLastPrecheckRunId(started.run_id);
    },
    onError: (error) => {
      setPrecheckRequestError(describeErrorText(describeError(error, "runDraftReview"), { detailSeparator: " " }));
    },
  });

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
    if (mode === "create" && selectedFlowId === null && draftFlowId == null) {
      setStepError(t("precheck.wizard.error.needFlow"));
      finish();
      return;
    }
    setStepError(null);
    setPrecheckRequestError(null);
    setResultsTab("overview");
    setPrecheckModalOpen(true);
    pendingPrecheckTargetDbRef.current = resolvedTargetDatabase;
    reviewMutation.mutate(
      {
        body: {
          flow_id: (draftFlowId ?? selectedFlowId) as string,
          draft_id: activeDraftId ?? undefined,
          sql: localSqlText,
          target_database: targetDatabaseForRequest,
        },
        language: i18n.language,
      },
      { onSettled: finish },
    );
  }, [activeDraftId, draftFlowId, i18n.language, localSqlText, mode, resolvedTargetDatabase, reviewMutation, selectedFlowId, setStepError, t, targetDatabaseForRequest]);

  const precheckProgress = computePrecheckProgress({
    starting:
      reviewStartPending ||
      reviewMutation.isPending ||
      (hasPresentedRun && !reviewRunError && (run === null || reviewRunPending)),
    phase: phasePresented,
    stageResults: run?.stage_results,
    findingsPending: run !== null && isTerminalPhase(phasePresented) && findingsQuery.isPending,
  });

  return {
    precheckModalOpen,
    setPrecheckModalOpen,
    precheckRequestError,
    lastPrecheckRunId,
    reviewStartPending,
    cancelReviewStartPending,
    resultsTab,
    setResultsTab,
    run,
    phase,
    phasePresented,
    hasPrecheckResult,
    hasPresentedRun,
    sqlChangedSincePrecheck,
    precheckExpired,
    timedOut,
    reviewRunPending,
    reviewRunFetching,
    reviewRunError,
    reviewRunErrorDetail,
    refetchReviewRun,
    findingsQuery,
    reviewMutationPending: reviewMutation.isPending,
    precheckProgress,
    invalidateAll,
    handleRunReview,
  };
}
