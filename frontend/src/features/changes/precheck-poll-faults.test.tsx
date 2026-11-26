import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useReviewRun } from "@/features/review/use-draft-workspace";
import { apiRoute } from "@/shared/mock/api-route";
import { withZodValidation } from "@/shared/mock/zod-guard";
import { server } from "@/test/msw/server";

function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });
}

function createWrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

interface EnvelopeOverrides {
  draftId?: string;
  draftRevision?: number;
}

const REQUEST_IDS: Record<"running" | "ready" | "failed", string> = {
  running: "0198d9cc-0000-7000-8000-000000000901",
  ready: "0198d9cc-0000-7000-8000-000000000902",
  failed: "0198d9cc-0000-7000-8000-000000000903",
};

function runEnvelope(
  id: string,
  state: "running" | "ready" | "failed",
  passed: boolean,
  overrides: EnvelopeOverrides = {},
) {
  return {
    err_code: 0,
    message: "ok",
    data: {
      id,
      draft_id: overrides.draftId ?? "0198d9cc-0000-7000-8000-000000000101",
      draft_revision: overrides.draftRevision ?? 1,
      state,
      statement_count: 1,
      fingerprint_group_count: 1,
      stage_results: [],
      gate: { passed, reason_codes: [] },
      failure_code: state === "failed" ? "AI_REVIEW_FAILED" : null,
      version: 1,
      created_at: "2026-11-25T05:00:00Z",
      started_at: "2026-11-25T05:00:01Z",
      finished_at: state === "running" ? null : "2026-11-25T05:00:08Z",
    },
    request_id: REQUEST_IDS[state],
  };
}

describe("DEF-14 precheck polling resilience", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("stays polling past 6s under slow jittered provider responses and stops cleanly once ready", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-25T05:00:00Z"), shouldAdvanceTime: true });
    let calls = 0;
    const runId = "0198d9cc-0000-7000-8000-000000000301";
    server.use(
      ...withZodValidation([
        http.get(apiRoute("/review-runs/:reviewRunId"), () => {
          calls += 1;
          // 8 running responses (>7 seconds at 1s interval) before settling on ready.
          if (calls < 8) {
            return HttpResponse.json(runEnvelope(runId, "running", false));
          }
          return HttpResponse.json(runEnvelope(runId, "ready", true));
        }),
      ]),
    );

    const client = createTestQueryClient();
    const { result } = renderHook(() => useReviewRun(runId), { wrapper: createWrapper(client) });

    await waitFor(() => {
      expect(result.current.phase).toBe("running");
    });
    for (let step = 0; step < 7; step += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_050);
      });
    }

    await waitFor(() => {
      expect(result.current.phase).toBe("ready");
      expect(result.current.run?.gate.passed).toBe(true);
    });
    const settledCalls = calls;
    expect(settledCalls).toBeGreaterThanOrEqual(8);

    // No leaked polling timers after reaching terminal ready state.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(calls).toBe(settledCalls);
    client.clear();
  });

  it("stops polling immediately when component unmounts mid-poll under slow provider responses", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-25T05:00:00Z"), shouldAdvanceTime: true });
    let calls = 0;
    const runId = "0198d9cc-0000-7000-8000-000000000302";
    server.use(
      ...withZodValidation([
        http.get(apiRoute("/review-runs/:reviewRunId"), () => {
          calls += 1;
          // Exercise MOCK-CONTRACT-DRIFT-01 draftless ephemeral precheck shape under runtime Zod validation.
          return HttpResponse.json(runEnvelope(runId, "running", false, { draftId: "", draftRevision: 0 }));
        }),
      ]),
    );

    const client = createTestQueryClient();
    const hook = renderHook(() => useReviewRun(runId), { wrapper: createWrapper(client) });

    await waitFor(() => {
      expect(hook.result.current.phase).toBe("running");
    });
    expect(calls).toBe(1);

    hook.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6_000);
    });
    expect(calls).toBe(1);
    client.clear();
  });
});
