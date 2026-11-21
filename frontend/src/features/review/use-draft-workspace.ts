import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  ChangeDraft,
  ChangeOrder,
  CreateChangeDraftRequest,
  ReviewFinding,
  ReviewRun,
  ReviewRunState,
  RunDraftReview200,
  RunDraftReviewBody,
  SqlReveal,
  SubmitChangeDraftBody,
  UpdateChangeDraftRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  createChangeDraft,
  getChangeDraft,
  getReviewRun,
  listCurrentUserFlows,
  listReviewRunFindings,
  replaceDraftSql,
  revealDraftSql,
  runDraftReview,
  updateChangeDraft,
} from "@/api/generated/client/change-drafts/change-drafts";
import { submitChangeDraft } from "@/api/generated/client/change-orders/change-orders";
import { ifMatchHeader, type UnwrapEnvelope, unwrapApi } from "@/shared/api/mutator";
import { subscribeSseEvent, useSseEvent } from "@/shared/events/sse-event-bus";
import { fetchAllCursorPages, type CursorPage } from "@/shared/lib/fetch-all-cursor-pages";
import { randomUUID } from "@/shared/lib/uuid";
import { useDraftEditorStore } from "./draft-editor-store";
import { mergeRunPhase, type RunPhase } from "./run-state";

/**
 * Server-state hooks for the AI Precheck Workspace. All reads go through the
 * generated client; domain events (SSE event bus) are treated as notifications
 * that trigger an HTTP re-read plus an immediate monotonic phase fold for
 * responsive UI — the resource, not the event, stays the truth
 * (api/events/README.md).
 */

const RUNNING_REFETCH_MS = 1_000;

export function useChangeDraft(draftId: string, enabled = true) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["change-draft", draftId],
    queryFn: () => unwrapApi(getChangeDraft(draftId)),
    enabled: enabled && draftId !== "",
  });

  useSseEvent(
    enabled && draftId !== "" ? "io.yearning.v4.change_draft.state_changed" : null,
    (event) => {
      const sub = event.subject;
      if (sub !== undefined && sub !== "" && sub !== `change-drafts/${draftId}` && sub !== `draft:${draftId}`) {
        return;
      }
      void queryClient.invalidateQueries({ queryKey: ["change-draft", draftId] });
    },
  );

  return query;
}

export function useCurrentUserChangeFlows() {
  return useQuery({
    queryKey: ["current-user-flows", "change_review"],
    queryFn: async () => {
      const page = await unwrapApi(listCurrentUserFlows({ flow_type: "change_review" }));
      return page.items;
    },
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
}

export function useFlowUpdated(flowId: string | null): boolean {
  // The flag resets when the page binds a different flow — adjusted during
  // render (React's recommended pattern) instead of a cascading effect.
  const [state, setState] = useState<{ flowId: string | null; updated: boolean }>({
    flowId,
    updated: false,
  });
  if (state.flowId !== flowId) {
    setState({ flowId, updated: false });
  }
  const queryClient = useQueryClient();

  useEffect(() => {
    if (flowId === null) return;
    return subscribeSseEvent("io.yearning.v4.flow.updated", (event) => {
      const sub = event.subject;
      if (
        sub !== undefined &&
        sub !== "" &&
        sub !== `flows/${flowId}` &&
        sub !== `flow:${flowId}` &&
        !sub.startsWith("permission-groups/")
      ) {
        return;
      }
      setState({ flowId, updated: true });
      void queryClient.invalidateQueries({ queryKey: ["current-user-flows"] });
    });
  }, [flowId, queryClient]);

  return state.updated;
}

interface ReviewProgress {
  run: ReviewRun | null;
  /** Monotonic presentation phase — never walks backwards (out-of-order events do not regress state). */
  phase: RunPhase;
  hasRunId: boolean;
  isPending: boolean;
  /** Background refetching (show loading even when cached data is present). */
  isFetching: boolean;
  isError: boolean;
  error: unknown;
  refetch: () => Promise<unknown>;
}

/**
 * RCP-20261101-EPHEMERAL-PRECHECK: precheck runs are ephemeral; the run id comes solely from the last
 * POST /review-runs response (page client state) and is no longer written to the draft row by the server;
 * once the page unmounts, the id is lost and reopening requires a fresh precheck. Callers pass this id to poll.
 */
export function useReviewRun(runId: string | null): ReviewProgress {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["review-run", runId],
    queryFn: () => unwrapApi(getReviewRun(runId as string)),
    enabled: runId !== null,
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state === "queued" || state === "running" ? RUNNING_REFETCH_MS : false;
    },
  });

  // The fold survives across run id changes for the lifetime of the page:
  // a re-run flips to the fresh run's actual state, so no stale terminal
  // phase of an abandoned run can leak into the new run's presentation. A
  // new run id adopts its observation as-is; the same run folds monotoni-
  // cally during render (idempotent for repeated HTTP observations), and
  // events fold in their callback.
  const [folded, setFolded] = useState<{ runId: string | null; phase: RunPhase }>({
    runId: null,
    phase: "idle",
  });

  const run = query.data ?? null;
  const runState = run?.state;

  let phase = folded.phase;
  if (folded.runId !== runId) {
    phase = runState ?? "idle";
  } else if (runState !== undefined) {
    phase = mergeRunPhase(folded.phase, runState);
  }
  if (phase !== folded.phase || folded.runId !== runId) {
    setFolded({ runId, phase });
  }

  useSseEvent(
    runId === null ? null : "*",
    (event) => {
      if (runId === null) return;
      const sub = event.subject;
      if (sub !== undefined && sub !== "" && sub !== `review-runs/${runId}` && sub !== `review_run:${runId}`) {
        return;
      }
      if (
        event.type === "io.yearning.v4.review.completed" ||
        event.type === "io.yearning.v4.review.blocked"
      ) {
        const data = (event.data ?? {}) as { state?: ReviewRunState };
        if (typeof data.state === "string") {
          setFolded((previous) => ({
            runId,
            phase:
              previous.runId === runId
                ? mergeRunPhase(previous.phase, data.state as ReviewRunState)
                : (data.state as ReviewRunState),
          }));
          void queryClient.invalidateQueries({ queryKey: ["review-run", runId] });
          void queryClient.invalidateQueries({ queryKey: ["review-findings", runId] });
        }
      }
    },
  );

  return {
    run,
    phase,
    hasRunId: runId !== null,
    isPending: query.isPending,
    isError: query.isError,
    error: query.error,
    isFetching: query.isFetching,
    refetch: query.refetch,
  };
}

export function useReviewFindings(runId: string | null, enabled: boolean) {
  return useQuery({
    queryKey: ["review-findings", runId],
    queryFn: () =>
      fetchAllCursorPages(async (after) => {
        const res = await unwrapApi(
          listReviewRunFindings(runId as string, {
            limit: 200,
            ...(after === null ? {} : { after }),
          }),
        );
        return {
          items: res.items,
          page: res.page,
        } satisfies CursorPage<ReviewFinding>;
      }),
    enabled: runId !== null && enabled,
  });
}

/** Severity filter state shared by the finding list header. */
type FindingSeverityFilter = "all" | ReviewFinding["severity"];

export function useSeverityFilter(): {
  severity: FindingSeverityFilter;
  setSeverity: (severity: FindingSeverityFilter) => void;
  visible: (findings: ReviewFinding[]) => ReviewFinding[];
} {
  const [severity, setSeverity] = useState<FindingSeverityFilter>("all");
  const visible = useMemo(
    () => (findings: ReviewFinding[]) =>
      severity === "all" ? findings : findings.filter((finding) => finding.severity === severity),
    [severity],
  );
  return { severity, setSeverity, visible };
}

function reviewOutputLanguage(language: string): "zh-CN" | "en-US" {
  return language.toLowerCase().startsWith("en") ? "en-US" : "zh-CN";
}

function draftIdempotencyHeaders(): Record<string, string> {
  return { "Idempotency-Key": `draft-${randomUUID()}` };
}

/** Sends a version-checked PUT to replace draft SQL and unwraps the updated draft envelope. */
export function saveDraftSqlRequest(
  draftId: string,
  sql: string,
  version: number | null | undefined,
): Promise<ChangeDraft> {
  return unwrapApi(replaceDraftSql(draftId, { sql }, ifMatchHeader(version ?? 1)));
}

export interface SaveDraftSqlVariables {
  draftId: string;
  sql: string;
  version?: number | null;
}

/** Saves draft SQL with optimistic concurrency and invalidates the draft query on success. */
export function useSaveDraftSqlMutation(options?: {
  onSuccess?: (saved: ChangeDraft, variables: SaveDraftSqlVariables) => void;
  onError?: (error: unknown) => void;
}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ draftId, sql, version }: SaveDraftSqlVariables) =>
      saveDraftSqlRequest(draftId, sql, version ?? useDraftEditorStore.getState().savedVersion),
    onSuccess: (saved, variables) => {
      options?.onSuccess?.(saved, variables);
      void queryClient.invalidateQueries({ queryKey: ["change-draft", variables.draftId] });
    },
    onError: (error) => {
      options?.onError?.(error);
    },
  });
}

/** Creates a change draft shell and unwraps the created ChangeDraft record. */
export function useCreateChangeDraftMutation(options?: {
  onError?: (error: unknown) => void;
}) {
  return useMutation({
    mutationFn: (body: CreateChangeDraftRequest) => unwrapApi(createChangeDraft(body)),
    onError: (error) => {
      options?.onError?.(error);
    },
  });
}

/** Reveals encrypted draft SQL plaintext for editing. */
export function useRevealDraftSqlMutation(
  draftId: string | null,
  options?: {
    onSuccess?: (reveal: SqlReveal) => void;
    onError?: (error: unknown) => void;
  },
) {
  return useMutation({
    mutationFn: () => unwrapApi(revealDraftSql(draftId as string, { purpose: "draft-edit" })),
    onSuccess: (reveal) => {
      options?.onSuccess?.(reveal);
    },
    onError: (error) => {
      options?.onError?.(error);
    },
  });
}

export type RunDraftReviewResult = UnwrapEnvelope<RunDraftReview200>;

export interface RunDraftReviewVariables {
  body: RunDraftReviewBody;
  language: string;
}

/** Starts an ephemeral AI precheck run and invalidates its polling query when accepted. */
export function useRunDraftReviewMutation(options?: {
  onSuccess?: (started: RunDraftReviewResult, variables: RunDraftReviewVariables) => void;
  onError?: (error: unknown) => void;
}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ body, language }: RunDraftReviewVariables) =>
      unwrapApi(
        runDraftReview(body, {
          headers: {
            ...draftIdempotencyHeaders(),
            "Accept-Language": reviewOutputLanguage(language),
          },
        }),
      ),
    onSuccess: (started, variables) => {
      options?.onSuccess?.(started, variables);
      if (started.run_id !== "") {
        void queryClient.invalidateQueries({ queryKey: ["review-run", started.run_id] });
      }
    },
    onError: (error) => {
      options?.onError?.(error);
    },
  });
}

export interface UpdateChangeDraftVariables {
  draftId: string;
  body: UpdateChangeDraftRequest;
  version?: number | null;
}

/** Updates draft metadata with optimistic concurrency and invalidates the draft query. */
export function useUpdateChangeDraftMutation(options?: {
  onSuccess?: (saved: ChangeDraft, variables: UpdateChangeDraftVariables) => void;
  onError?: (error: unknown) => void;
}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ draftId, body, version }: UpdateChangeDraftVariables) =>
      unwrapApi(
        updateChangeDraft(
          draftId,
          body,
          ifMatchHeader(version ?? useDraftEditorStore.getState().savedVersion ?? 1),
        ),
      ),
    onSuccess: (saved, variables) => {
      options?.onSuccess?.(saved, variables);
      void queryClient.invalidateQueries({ queryKey: ["change-draft", variables.draftId] });
    },
    onError: (error) => {
      options?.onError?.(error);
    },
  });
}

export interface SubmitChangeDraftVariables {
  body: SubmitChangeDraftBody;
  beforeSubmit?: () => Promise<void>;
}

/** Submits a prechecked SQL change order (saving bound draft metadata first when provided). */
export function useSubmitChangeDraftMutation(options?: {
  onSuccess?: (order: ChangeOrder) => void;
  onError?: (error: unknown) => void;
}) {
  return useMutation({
    mutationFn: async ({ body, beforeSubmit }: SubmitChangeDraftVariables) => {
      if (beforeSubmit !== undefined) {
        await beforeSubmit();
      }
      return unwrapApi(submitChangeDraft(body, { headers: draftIdempotencyHeaders() }));
    },
    onSuccess: (order) => {
      options?.onSuccess?.(order);
    },
    onError: (error) => {
      options?.onError?.(error);
    },
  });
}
