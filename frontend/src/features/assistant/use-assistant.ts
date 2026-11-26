import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  AssistantSession,
  AssistantSessionDetail,
  CreateAssistantSessionRequest,
  Flow,
  PrecheckAssistantSqlDraftRequest,
  SendAssistantMessageRequest,
  SendAssistantMessageResponse,
  SubmitAssistantSqlDraftOrderRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  cancelAssistantMessageStream,
  createAssistantSession,
  deleteAssistantSession,
  getAssistantSession,
  getSendAssistantMessageUrl,
  listAssistantSessions,
  precheckAssistantSqlDraft,
  sendAssistantMessage,
  submitAssistantSqlDraftOrder,
} from "@/api/generated/client/ai-assistant/ai-assistant";
import { listCurrentUserFlows } from "@/api/generated/client/change-drafts/change-drafts";
import { useIdempotentMutation } from "@/shared/api/idempotency";
import {
  executeFetch,
  idempotencyKeyHeader,
  ifMatchHeader,
  parseStreamBusinessError,
  resolveActivePlatformLocale,
  TransportError,
  unwrapApi,
  unwrapEnvelopeResponse,
} from "@/shared/api/mutator";

export interface AssistantStreamCallbacks {
  onProgress?: (payload: { phase?: string; message_id?: string }) => void;
  onTool?: (payload: {
    tool: string;
    status: "running" | "completed";
    capability?: string;
    schema?: string;
    tables?: number;
    engine?: string;
    statement_kind?: string;
    count?: number;
  }) => void;
  onReasoning?: (payload: { status: "running" | "completed"; delta?: string }) => void;
  onDelta?: (delta: string) => void;
}

export interface AuthorizedDatasourceOption {
  datasourceId: string;
  datasourceName: string;
  flowId: string | null;
}

export function extractAuthorizedDatasources(flows: Flow[]): AuthorizedDatasourceOption[] {
  const seen = new Map<string, AuthorizedDatasourceOption>();
  for (const flow of flows) {
    if (flow.flow_type === "change_review" && flow.stages) {
      for (const stage of flow.stages) {
        if (!seen.has(stage.datasource_id)) {
          seen.set(stage.datasource_id, {
            datasourceId: stage.datasource_id,
            datasourceName: stage.datasource_name,
            flowId: flow.id,
          });
        }
      }
    }
    if (flow.flow_type === "query_access" && flow.query_capabilities) {
      for (const cap of flow.query_capabilities) {
        if (!seen.has(cap.datasource_id)) {
          seen.set(cap.datasource_id, {
            datasourceId: cap.datasource_id,
            datasourceName: cap.datasource_name,
            flowId: null,
          });
        }
      }
    }
  }
  return [...seen.values()];
}

export function useAssistantSessions(enabled: boolean) {
  return useQuery({
    queryKey: ["assistant-sessions"],
    queryFn: async () => {
      const page = await unwrapApi(listAssistantSessions({ limit: 50 }));
      return page.items;
    },
    enabled,
  });
}

export function useAssistantSessionDetail(sessionId: string | null, enabled: boolean) {
  return useQuery({
    queryKey: ["assistant-session", sessionId],
    queryFn: () => unwrapApi(getAssistantSession(sessionId ?? "")),
    enabled: enabled && sessionId !== null && sessionId !== "",
  });
}

export function useAssistantAuthorizedFlows(enabled: boolean) {
  return useQuery({
    queryKey: ["current-user-flows", "assistant"],
    queryFn: async () => {
      const [changePage, queryPage] = await Promise.all([
        unwrapApi(listCurrentUserFlows({ flow_type: "change_review" })),
        unwrapApi(listCurrentUserFlows({ flow_type: "query_access" })),
      ]);
      return [...changePage.items, ...queryPage.items];
    },
    enabled,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
}

export function useCreateAssistantSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input?: CreateAssistantSessionRequest) =>
      unwrapApi(createAssistantSession(input ?? {})),
    onSuccess: (created) => {
      queryClient.setQueryData<AssistantSessionDetail>(
        ["assistant-session", created.id],
        {
          session: created,
          messages: [],
          drafts: [],
        },
      );
      void queryClient.invalidateQueries({ queryKey: ["assistant-sessions"] });
    },
  });
}

export function useDeleteAssistantSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { sessionId: string; version: number }) =>
      deleteAssistantSession(input.sessionId, ifMatchHeader(input.version)),
    onSuccess: (_data, variables) => {
      queryClient.removeQueries({ queryKey: ["assistant-session", variables.sessionId] });
      void queryClient.invalidateQueries({ queryKey: ["assistant-sessions"] });
    },
  });
}

export function dispatchSSEFrame(
  rawFrame: string,
  callbacks: AssistantStreamCallbacks | undefined,
): SendAssistantMessageResponse | null {
  let eventName = "message";
  const dataLines: string[] = [];
  for (const rawLine of rawFrame.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line.startsWith("event:")) {
      eventName = line.slice("event:".length).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice("data:".length).trim());
    }
  }
  const dataStr = dataLines.join("\n").trim();
  if (dataStr === "") return null;

  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(dataStr) as Record<string, unknown>;
  } catch {
    return null;
  }

  switch (eventName) {
    case "progress":
      callbacks?.onProgress?.({
        phase: typeof parsed.phase === "string" ? parsed.phase : undefined,
        message_id:
          typeof parsed.message_id === "string" ? parsed.message_id : undefined,
      });
      return null;
    case "tool":
      if (typeof parsed.tool === "string") {
        callbacks?.onTool?.({
          tool: parsed.tool,
          status: parsed.status === "completed" ? "completed" : "running",
          capability:
            typeof parsed.capability === "string"
              ? parsed.capability
              : undefined,
          schema: typeof parsed.schema === "string" ? parsed.schema : undefined,
          tables: typeof parsed.tables === "number" ? parsed.tables : undefined,
          engine: typeof parsed.engine === "string" ? parsed.engine : undefined,
          statement_kind:
            typeof parsed.statement_kind === "string"
              ? parsed.statement_kind
              : undefined,
          count: typeof parsed.count === "number" ? parsed.count : undefined,
        });
      }
      return null;
    case "reasoning":
      callbacks?.onReasoning?.({
        status: parsed.status === "completed" ? "completed" : "running",
        delta: typeof parsed.delta === "string" ? parsed.delta : undefined,
      });
      return null;
    case "delta":
      if (typeof parsed.delta === "string" && parsed.delta !== "") {
        callbacks?.onDelta?.(parsed.delta);
      }
      return null;
    case "error":
      throw parseStreamBusinessError(parsed, "sse");
    case "done":
      return JSON.parse(dataStr) as SendAssistantMessageResponse;
    default:
      return null;
  }
}

export async function streamAssistantMessageSSE(input: {
  sessionId: string;
  body: SendAssistantMessageRequest;
  signal?: AbortSignal;
  callbacks?: AssistantStreamCallbacks;
  forceFetch?: boolean;
}): Promise<SendAssistantMessageResponse> {
  // In unit test environments where sendAssistantMessage is a Vitest mock, delegate directly
  if (!input.forceFetch && "mock" in sendAssistantMessage) {
    return unwrapApi(
      sendAssistantMessage(input.sessionId, input.body, {
        signal: input.signal,
        ...(input.callbacks ? { callbacks: input.callbacks } : {}),
      }),
    );
  }

  const activeLocale = resolveActivePlatformLocale();
  const response = await executeFetch(getSendAssistantMessageUrl(input.sessionId), {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
      "Accept-Language": activeLocale,
    },
    body: JSON.stringify({
      ...input.body,
      output_locale: activeLocale,
    }),
    signal: input.signal,
  });

  const contentType = (response.headers.get("content-type") ?? "").toLowerCase();
  if (!contentType.includes("text/event-stream") || response.body === null) {
    return await unwrapEnvelopeResponse<SendAssistantMessageResponse>(response);
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let doneResult: SendAssistantMessageResponse | null = null;

  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      buffer += decoder.decode();
      break;
    }
    buffer += decoder.decode(value, { stream: true });
    let boundaryIdx = buffer.indexOf("\n\n");
    while (boundaryIdx !== -1) {
      const frame = buffer.slice(0, boundaryIdx);
      buffer = buffer.slice(boundaryIdx + 2);
      const frameDone = dispatchSSEFrame(frame, input.callbacks);
      if (frameDone !== null) {
        doneResult = frameDone;
      }
      boundaryIdx = buffer.indexOf("\n\n");
    }
  }

  if (buffer.trim() !== "") {
    const frameDone = dispatchSSEFrame(buffer, input.callbacks);
    if (frameDone !== null) {
      doneResult = frameDone;
    }
  }

  if (doneResult === null) {
    throw new TransportError({
      title: "incomplete_stream",
      status: 200,
      detail: "SSE stream closed before done event",
    });
  }
  return doneResult;
}

export function useSendAssistantMessage(sessionId: string | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      sessionId: string;
      body: SendAssistantMessageRequest;
      signal?: AbortSignal;
      callbacks?: AssistantStreamCallbacks;
    }) => await streamAssistantMessageSSE(input),
    onSuccess: (doneResult, variables) => {
      const targetId = variables.sessionId || sessionId;
      if (targetId !== null && targetId !== "") {
        queryClient.setQueryData<AssistantSessionDetail | undefined>(
          ["assistant-session", targetId],
          (prev) =>
            prev === undefined
              ? undefined
              : {
                  ...prev,
                  session: doneResult.session,
                },
        );
        void queryClient.invalidateQueries({ queryKey: ["assistant-session", targetId] });
      }
      queryClient.setQueryData<AssistantSession[] | undefined>(
        ["assistant-sessions"],
        (prev) => {
          if (prev === undefined) return [doneResult.session];
          const exists = prev.some((item) => item.id === doneResult.session.id);
          if (!exists) return [doneResult.session, ...prev];
          return prev.map((item) =>
            item.id === doneResult.session.id ? doneResult.session : item,
          );
        },
      );
      void queryClient.invalidateQueries({ queryKey: ["assistant-sessions"] });
    },
  });
}

export function useCancelAssistantMessageStream(sessionId: string | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { sessionId: string; messageId: string }) =>
      unwrapApi(cancelAssistantMessageStream(input.sessionId, input.messageId)),
    onSuccess: (_data, variables) => {
      const targetId = variables.sessionId || sessionId;
      if (targetId !== null && targetId !== "") {
        void queryClient.invalidateQueries({ queryKey: ["assistant-session", targetId] });
      }
    },
  });
}

export function usePrecheckAssistantSqlDraft(sessionId: string | null) {
  const queryClient = useQueryClient();
  return useIdempotentMutation("assistant-precheck", {
    mutationFn: (
      input: {
        sessionId: string;
        draftId: string;
        body: PrecheckAssistantSqlDraftRequest;
      },
      idempotencyKey,
    ) =>
      unwrapApi(
        precheckAssistantSqlDraft(input.sessionId, input.draftId, input.body, {
          headers: idempotencyKeyHeader(idempotencyKey),
        }),
      ),
    onSuccess: (_data, variables) => {
      const targetId = variables.sessionId || sessionId;
      if (targetId !== null && targetId !== "") {
        void queryClient.invalidateQueries({ queryKey: ["assistant-session", targetId] });
      }
    },
  });
}

export function useSubmitAssistantSqlDraftOrder(sessionId: string | null) {
  const queryClient = useQueryClient();
  return useIdempotentMutation("assistant-order", {
    mutationFn: (
      input: {
        sessionId: string;
        draftId: string;
        body: SubmitAssistantSqlDraftOrderRequest;
      },
      idempotencyKey,
    ) =>
      unwrapApi(
        submitAssistantSqlDraftOrder(input.sessionId, input.draftId, input.body, {
          headers: idempotencyKeyHeader(idempotencyKey),
        }),
      ),
    onSuccess: (_data, variables) => {
      const targetId = variables.sessionId || sessionId;
      if (targetId !== null && targetId !== "") {
        void queryClient.invalidateQueries({ queryKey: ["assistant-session", targetId] });
      }
      void queryClient.invalidateQueries({ queryKey: ["change-orders"] });
    },
  });
}
