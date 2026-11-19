import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  ChangeDraft,
  Flow,
  ReviewFinding,
  ReviewRun,
  ReviewRunState,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  getChangeDraft,
  getReviewRun,
  listCurrentUserFlows,
  listReviewRunFindings,
} from "@/api/generated/client/change-drafts/change-drafts";
import { useDomainEvent } from "@/shared/events/review-event-client";
import { fetchAllCursorPages, type CursorPage } from "@/shared/lib/fetch-all-cursor-pages";
import { mergeRunPhase, type RunPhase } from "./run-state";

/**
 * Server-state hooks for the AI Precheck Workspace. All reads go through the
 * generated client; domain events (FE-F3 Review Event Client) are treated as
 * notifications that trigger an HTTP re-read plus an immediate monotonic
 * phase fold for responsive UI — the resource, not the event, stays the
 * truth (api/events/README.md). Generated client types describe the raw
 * envelope, so the mutator-unwrapped data is narrowed at the query boundary.
 */

const RUNNING_REFETCH_MS = 1_000;

export function useChangeDraft(draftId: string, enabled = true) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["change-draft", draftId],
    queryFn: async () => (await getChangeDraft(draftId)) as unknown as ChangeDraft,
    enabled: enabled && draftId !== "",
  });

  useDomainEvent(
    `change-drafts/${draftId}`,
    "io.yearning.v4.change_draft.state_changed",
    () => {
      void queryClient.invalidateQueries({ queryKey: ["change-draft", draftId] });
    },
  );

  return query;
}

export function useCurrentUserChangeFlows() {
  return useQuery({
    queryKey: ["current-user-flows", "change_review"],
    queryFn: async () => {
      const page = (await listCurrentUserFlows({ flow_type: "change_review" })) as unknown as {
        items: Flow[];
      };
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

  useDomainEvent(flowId === null ? null : `flows/${flowId}`, "io.yearning.v4.flow.updated", () => {
    setState({ flowId, updated: true });
    void queryClient.invalidateQueries({ queryKey: ["current-user-flows"] });
  });

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
    queryFn: async () =>
      (await getReviewRun(runId as string)) as unknown as ReviewRun,
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

  useDomainEvent(
    runId === null ? null : `review-runs/${runId}`,
    "any",
    (event) => {
      if (
        event.type === "io.yearning.v4.review.completed" ||
        event.type === "io.yearning.v4.review.blocked"
      ) {
        const data = event.data as { state?: ReviewRunState };
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
    queryFn: async () =>
      fetchAllCursorPages(async (after) =>
        (await listReviewRunFindings(runId as string, {
          limit: 200,
          ...(after === null ? {} : { after }),
        })) as unknown as CursorPage<ReviewFinding>,
      ),
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
