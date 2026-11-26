import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useAdminTask, useDatasourceCapabilities, useNotificationTestTask } from "@/features/admin/use-admin";
import { useExecutionAttempt, useExecutionStatements } from "@/features/orders/use-execution";
import { useReviewRun } from "@/features/review/use-draft-workspace";
import { BusinessError } from "@/shared/api/mutator";
import { apiRoute } from "@/shared/mock/api-route";
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

describe("polling hooks stop on error state", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("stops polling useReviewRun after transitioning from running to 1002 even with stale running data", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-24T13:00:00Z"), shouldAdvanceTime: true });
    let calls = 0;
    server.use(
      http.get(apiRoute("/review-runs/:reviewRunId"), ({ params }) => {
        calls += 1;
        if (calls === 1) {
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              id: String(params.reviewRunId),
              draft_id: "0198d9cc-0000-7000-8000-000000000101",
              draft_revision: 1,
              state: "running",
              statement_count: 1,
              fingerprint_group_count: 1,
              stage_results: [],
              gate: { passed: false, reason_codes: [] },
              failure_code: null,
              version: 1,
              created_at: "2026-11-24T13:00:00Z",
              started_at: "2026-11-24T13:00:01Z",
              finished_at: null,
            },
            request_id: "req-review-ok-1",
          });
        }
        return HttpResponse.json({
          err_code: 1002,
          message: "not found",
          data: null,
          request_id: `req-review-err-${String(calls)}`,
          retryable: false,
        });
      }),
    );

    const client = createTestQueryClient();
    const { result } = renderHook(() => useReviewRun("0198d9cc-0000-7000-8000-000000000201"), {
      wrapper: createWrapper(client),
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(false);
      expect(result.current.run?.state).toBe("running");
    });
    expect(calls).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(calls).toBe(2);
    expect(result.current.error).toBeInstanceOf(BusinessError);
    expect((result.current.error as BusinessError).requestId).toBe("req-review-err-2");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_500);
    });
    expect(calls).toBe(2);
    expect((result.current.error as BusinessError).requestId).toBe("req-review-err-2");
    client.clear();
  });

  it("stops polling useAdminTask when 1002 is returned after running data", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-24T13:00:00Z"), shouldAdvanceTime: true });
    let calls = 0;
    server.use(
      http.get(apiRoute("/tasks/:taskId"), ({ params }) => {
        calls += 1;
        if (calls === 1) {
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              id: String(params.taskId),
              kind: "admin_connection_test",
              state: "running",
              progress: { completed: 0, total: 1, unit: "steps" },
              result_ref: null,
              error: null,
              created_at: "2026-11-24T13:00:00Z",
              updated_at: "2026-11-24T13:00:01Z",
            },
            request_id: "req-task-ok-1",
          });
        }
        return HttpResponse.json({
          err_code: 1002,
          message: "task not found",
          data: null,
          request_id: `req-task-err-${String(calls)}`,
          retryable: false,
        });
      }),
    );

    const client = createTestQueryClient();
    const { result } = renderHook(() => useAdminTask("0198d9cc-0000-7000-8000-000000000301", true), {
      wrapper: createWrapper(client),
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(false);
      expect(result.current.data?.state).toBe("running");
    });
    expect(calls).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(calls).toBe(2);
    expect(result.current.error).toBeInstanceOf(BusinessError);
    expect((result.current.error as BusinessError).requestId).toBe("req-task-err-2");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(calls).toBe(2);
    client.clear();
  });

  it("stops polling useNotificationTestTask on immediate error even when data is undefined", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-24T13:00:00Z"), shouldAdvanceTime: true });
    let calls = 0;
    server.use(
      http.get(apiRoute("/tasks/:taskId"), () => {
        calls += 1;
        return HttpResponse.json({
          err_code: 1002,
          message: "task not found",
          data: null,
          request_id: `req-notif-err-${String(calls)}`,
          retryable: false,
        });
      }),
    );

    const client = createTestQueryClient();
    const { result } = renderHook(() => useNotificationTestTask("0198d9cc-0000-7000-8000-000000000401"), {
      wrapper: createWrapper(client),
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(calls).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(4_000);
    });
    expect(calls).toBe(1);
    client.clear();
  });

  it("keeps polling useDatasourceCapabilities on 1002 but stops once an unexpected error occurs", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-24T13:00:00Z"), shouldAdvanceTime: true });
    let calls = 0;
    server.use(
      http.get(apiRoute("/admin/datasources/:datasourceId/capabilities"), () => {
        calls += 1;
        if (calls === 1) {
          return HttpResponse.json({
            err_code: 1002,
            message: "not probed yet",
            data: null,
            request_id: "req-cap-missing-1",
            retryable: false,
          });
        }
        return HttpResponse.json(
          {
            type: "about:blank",
            title: "Internal Server Error",
            status: 500,
            detail: "database unavailable",
            request_id: `req-cap-err-${String(calls)}`,
          },
          { status: 500, headers: { "Content-Type": "application/problem+json" } },
        );
      }),
    );

    const client = createTestQueryClient();
    const { result } = renderHook(
      () => useDatasourceCapabilities("0198d9cc-0000-7000-8000-000000000501", "probe-1", true),
      { wrapper: createWrapper(client) },
    );

    await waitFor(() => {
      expect(result.current.isError).toBe(false);
      expect(result.current.data).toBeNull();
    });
    expect(calls).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_500);
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(calls).toBe(2);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_500);
    });
    expect(calls).toBe(2);
    client.clear();
  });

  it("stops useExecutionAttempt polling and order invalidation interval on error, and stops useExecutionStatements on error", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-24T13:00:00Z"), shouldAdvanceTime: true });
    let attemptCalls = 0;
    let statementCalls = 0;
    server.use(
      http.get(apiRoute("/execution-attempts/:attemptId"), ({ params }) => {
        attemptCalls += 1;
        if (attemptCalls === 1) {
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              id: String(params.attemptId),
              stage_id: "0198d9cc-0000-7000-8000-000000000602",
              execution_kind: "dml",
              state: "running",
              send_boundary: "sending",
              osc: null,
              version: 1,
              created_at: "2026-11-24T13:00:00Z",
            },
            request_id: "req-attempt-ok-1",
          });
        }
        return HttpResponse.json({
          err_code: 1002,
          message: "attempt not found",
          data: null,
          request_id: `req-attempt-err-${String(attemptCalls)}`,
          retryable: false,
        });
      }),
      http.get(apiRoute("/execution-attempts/:attemptId/statements"), () => {
        statementCalls += 1;
        return HttpResponse.json({
          err_code: 1002,
          message: "attempt not found",
          data: null,
          request_id: `req-stmt-err-${String(statementCalls)}`,
          retryable: false,
        });
      }),
    );

    const client = createTestQueryClient();
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    const orderId = "0198d9cc-0000-7000-8000-000000000603";
    const attemptId = "0198d9cc-0000-7000-8000-000000000601";

    const attemptHook = renderHook(() => useExecutionAttempt(attemptId, orderId, true), {
      wrapper: createWrapper(client),
    });
    const stmtHook = renderHook(() => useExecutionStatements(attemptId, true, true), {
      wrapper: createWrapper(client),
    });

    await waitFor(() => {
      expect(attemptHook.result.current.isError).toBe(false);
      expect(attemptHook.result.current.data?.state).toBe("running");
    });
    await waitFor(() => {
      expect(stmtHook.result.current.isError).toBe(true);
    });
    expect(attemptCalls).toBe(1);
    expect(statementCalls).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    await waitFor(() => {
      expect(attemptHook.result.current.isError).toBe(true);
    });
    expect(attemptCalls).toBe(2);
    expect(statementCalls).toBe(1);

    const invalidationsAtError = invalidateSpy.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(attemptCalls).toBe(2);
    expect(statementCalls).toBe(1);
    expect(invalidateSpy.mock.calls.length).toBe(invalidationsAtError);
    client.clear();
  });
});
