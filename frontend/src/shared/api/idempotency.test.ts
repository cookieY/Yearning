import { createElement, type ReactNode } from "react";
import { renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useResourceMutation } from "@/features/admin/use-admin";
import { streamAssistantMessageSSE } from "@/features/assistant/use-assistant";
import { isBusinessErrorCode } from "@/shared/api/error-display";
import { useIdempotentMutation } from "@/shared/api/idempotency";
import {
  BusinessError,
  idempotencyKeyHeader,
  SESSION_EXPIRED_EVENT,
  TransportError,
} from "@/shared/api/mutator";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { testBusinessErrorJson, testProblemJson } from "@/test/envelope";

function createWrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

describe("Stage 4B shared API primitives (idempotency, error guards, resource mutations, SSE mutator)", () => {
  afterEach(() => {
    document.cookie = "yearning_csrf=; Max-Age=0; path=/";
    document.documentElement.lang = "zh-CN";
  });

  it("reuses the same Idempotency-Key across retries and mints a new key per user operation", async () => {
    const client = new QueryClient({
      defaultOptions: {
        mutations: { retry: 1, retryDelay: 1 },
      },
    });
    const observedHeaders: string[] = [];
    let callCount = 0;

    const { result } = renderHook(
      () =>
        useIdempotentMutation("order-action", {
          mutationFn: (payload: { note: string }, idempotencyKey) => {
            callCount += 1;
            const headers = idempotencyKeyHeader(idempotencyKey);
            observedHeaders.push(headers["Idempotency-Key"] ?? "");
            if (callCount === 1) {
              return Promise.reject(new Error("transient network error"));
            }
            return Promise.resolve(`${payload.note}:${idempotencyKey}`);
          },
        }),
      { wrapper: createWrapper(client) },
    );

    const firstOutcome = await result.current.mutateAsync({ note: "first" });
    expect(callCount).toBe(2);
    expect(observedHeaders).toHaveLength(2);
    expect(observedHeaders[0]).toMatch(/^order-action-/);
    expect(observedHeaders[1]).toBe(observedHeaders[0]);
    expect(firstOutcome).toBe(`first:${observedHeaders[0] ?? ""}`);

    await result.current.mutateAsync({ note: "second" });
    expect(callCount).toBe(3);
    expect(observedHeaders).toHaveLength(3);
    expect(observedHeaders[2]).toMatch(/^order-action-/);
    expect(observedHeaders[2]).not.toBe(observedHeaders[0]);
  });

  it("isolates Idempotency-Key per invocation when a second mutation runs while the first is retrying", async () => {
    const client = new QueryClient({
      defaultOptions: {
        mutations: { retry: 1, retryDelay: 30 },
      },
    });
    const keysByOp: { op1: string[]; op2: string[] } = { op1: [], op2: [] };
    let op1Attempts = 0;

    const { result } = renderHook(
      () =>
        useIdempotentMutation("order-action", {
          mutationFn: (payload: { note: "op1" | "op2" }, idempotencyKey) => {
            keysByOp[payload.note].push(idempotencyKey);
            if (payload.note === "op1") {
              op1Attempts += 1;
              if (op1Attempts === 1) {
                return Promise.reject(new Error("op1 transient failure"));
              }
            }
            return Promise.resolve(`${payload.note}:${idempotencyKey}`);
          },
        }),
      { wrapper: createWrapper(client) },
    );

    const p1 = result.current.mutateAsync({ note: "op1" });
    const out2 = await result.current.mutateAsync({ note: "op2" });
    const out1 = await p1;

    expect(keysByOp.op1).toHaveLength(2);
    expect(keysByOp.op2).toHaveLength(1);
    expect(keysByOp.op1[1]).toBe(keysByOp.op1[0]);
    expect(keysByOp.op2[0]).not.toBe(keysByOp.op1[0]);
    expect(out1).toBe(`op1:${keysByOp.op1[0] ?? ""}`);
    expect(out2).toBe(`op2:${keysByOp.op2[0] ?? ""}`);
  });

  it("matches BusinessError instances by catalog name or numeric err_code", () => {
    const notFound = new BusinessError({
      err_code: 1002,
      message: "missing",
      request_id: "11111111-1111-4111-8111-111111111111",
      retryable: false,
    });
    expect(isBusinessErrorCode(notFound, "RESOURCE_NOT_FOUND")).toBe(true);
    expect(isBusinessErrorCode(notFound, 1002)).toBe(true);
    expect(isBusinessErrorCode(notFound, "PRECONDITION_REQUIRED")).toBe(false);
    expect(isBusinessErrorCode(notFound, "UNKNOWN_CODE_NAME")).toBe(false);
    expect(isBusinessErrorCode(new Error("plain"), "RESOURCE_NOT_FOUND")).toBe(false);
    expect(isBusinessErrorCode({ err_code: 1002 }, 1002)).toBe(false);
  });

  it("invalidates declared query keys in useResourceMutation across success and settled modes", async () => {
    const client = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    });
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    const { result: onSuccessHook } = renderHook(
      () =>
        useResourceMutation({
          mutationFn: (input: { id: string }) => Promise.resolve({ updatedId: input.id }),
          invalidateKeys: (data, vars) => [
            ["admin", "datasources"],
            ["admin", "datasources", data.updatedId, vars.id],
          ],
        }),
      { wrapper: createWrapper(client) },
    );

    await onSuccessHook.current.mutateAsync({ id: "ds-1" });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["admin", "datasources"] });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ["admin", "datasources", "ds-1", "ds-1"],
    });

    invalidateSpy.mockClear();
    const { result: onSettledHook } = renderHook(
      () =>
        useResourceMutation({
          mutationFn: () => Promise.reject(new Error("boom")),
          invalidateKeys: [["admin", "users"]],
          when: "settled",
        }),
      { wrapper: createWrapper(client) },
    );

    await expect(onSettledHook.current.mutateAsync(undefined)).rejects.toThrow("boom");
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["admin", "users"] });

    invalidateSpy.mockClear();
    const afterSuccessSpy = vi.fn();
    const { result: voidCallbackHook } = renderHook(
      () =>
        useResourceMutation<undefined, { userId: string }>({
          mutationFn: () => Promise.resolve(undefined),
          invalidateKeys: (_data, vars) => [["admin", "users", vars.userId]],
          when: "settled",
          afterSuccess: afterSuccessSpy,
        }),
      { wrapper: createWrapper(client) },
    );

    await voidCallbackHook.current.mutateAsync({ userId: "u-9" });
    expect(afterSuccessSpy).toHaveBeenCalledWith(undefined, { userId: "u-9" });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["admin", "users", "u-9"] });
  });

  it("routes streamAssistantMessageSSE through shared mutator CSRF/locale headers, 401 expiry, ProblemDetails, and BusinessError handling", async () => {
    const sessionId = "a1111111-1111-4111-8111-111111110001";
    document.cookie = "yearning_csrf=csrf-sse-test; path=/";
    document.documentElement.lang = "en-US";

    let capturedCsrf: string | null = null;
    let capturedLang: string | null = null;
    const expiredEvents: Event[] = [];
    const onExpired = (ev: Event) => {
      expiredEvents.push(ev);
    };
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);

    try {
      // 1. 401 Problem Details announces session expiry and throws TransportError(401).
      server.use(
        http.post(apiRoute("/ai-assistant/sessions/:sessionId/messages"), ({ request }) => {
          capturedCsrf = request.headers.get("X-CSRF-Token");
          capturedLang = request.headers.get("Accept-Language");
          return testProblemJson(401, "session_expired", "expired");
        }),
      );

      await expect(
        streamAssistantMessageSSE({
          sessionId,
          body: { content: "hello" },
          forceFetch: true,
        }),
      ).rejects.toSatisfy(
        (err: unknown) => err instanceof TransportError && err.problem.status === 401,
      );
      expect(capturedCsrf).toBe("csrf-sse-test");
      expect(capturedLang).toBe("en-US");
      expect(expiredEvents).toHaveLength(1);

      // 2. Non-2xx Problem Details (503) throws TransportError with parsed problem payload.
      server.use(
        http.post(apiRoute("/ai-assistant/sessions/:sessionId/messages"), () =>
          testProblemJson(503, "service_unavailable", "upstream down"),
        ),
      );
      await expect(
        streamAssistantMessageSSE({
          sessionId,
          body: { content: "hello" },
          forceFetch: true,
        }),
      ).rejects.toSatisfy(
        (err: unknown) =>
          err instanceof TransportError &&
          err.problem.status === 503 &&
          err.problem.title === "service_unavailable",
      );

      // 3. HTTP 200 JSON BusinessError envelope unwraps via shared unwrapEnvelopeResponse.
      server.use(
        http.post(apiRoute("/ai-assistant/sessions/:sessionId/messages"), () =>
          testBusinessErrorJson(2010, "daily budget exhausted"),
        ),
      );
      await expect(
        streamAssistantMessageSSE({
          sessionId,
          body: { content: "hello" },
          forceFetch: true,
        }),
      ).rejects.toSatisfy((err: unknown) => isBusinessErrorCode(err, "AI_DAILY_BUDGET_EXHAUSTED"));

      // 4. SSE event:error frame parses into BusinessError via parseStreamBusinessError.
      server.use(
        http.post(
          apiRoute("/ai-assistant/sessions/:sessionId/messages"),
          () =>
            new HttpResponse(
              'event: error\ndata: {"err_code":6002,"message":"blocked","request_id":"req-sse-1","retryable":false}\n\n',
              {
                status: 200,
                headers: { "Content-Type": "text/event-stream" },
              },
            ),
        ),
      );
      await expect(
        streamAssistantMessageSSE({
          sessionId,
          body: { content: "hello" },
          forceFetch: true,
        }),
      ).rejects.toSatisfy(
        (err: unknown) =>
          err instanceof BusinessError && err.err_code === 6002 && err.requestId === "req-sse-1",
      );
    } finally {
      window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
    }
  });
});
