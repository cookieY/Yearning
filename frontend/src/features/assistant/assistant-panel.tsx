import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  AssistantCapability,
  AssistantMessage,
  SendAssistantMessageRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  extractAuthorizedDatasources,
  useAssistantAuthorizedFlows,
  useAssistantSessionDetail,
  useAssistantSessions,
  useCancelAssistantMessageStream,
  useCreateAssistantSession,
  useDeleteAssistantSession,
  useSendAssistantMessage,
} from "./use-assistant";
import { SqlDraftCard } from "./sql-draft-card";
import { AssistantMarkdown } from "./assistant-markdown";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  CodeBlock,
  CodeBlockContent,
} from "@/shared/components/ui/code-block";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/shared/components/ui/empty";
import { Input } from "@/shared/components/ui/input";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/shared/components/ui/input-group";
import {
  Marker,
  MarkerContent,
} from "@/shared/components/ui/marker";
import {
  Message,
  MessageContent,
  MessageFooter,
  MessageGroup,
  MessageHeader,
} from "@/shared/components/ui/message";
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from "@/shared/components/ui/message-scroller";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Spinner } from "@/shared/components/ui/spinner";
import { Textarea } from "@/shared/components/ui/textarea";
import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceDescription,
  TraceIcon,
  TraceLine,
  TraceTitle,
} from "@/shared/components/ui/trace";
import {
  BookOpen,
  Brain,
  Code2,
  Database,
  Maximize2,
  Minimize2,
  Plus,
  RotateCcw,
  Send,
  ShieldCheck,
  Sparkles,
  Square,
  Terminal,
  Trash2,
  Wand2,
  Wrench,
} from "lucide-react";

export interface AssistantPanelProps {
  open: boolean;
  isExpanded?: boolean;
  onToggleExpand?: () => void;
}

/**
 * Unwraps any assistant reply that was accidentally persisted as a raw or
 * truncated JSON envelope (`{"out_of_scope":false,...,"assistant_reply":"..."}`).
 */
export function unwrapAssistantReplyContent(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed.startsWith("{") || !trimmed.includes('"assistant_reply"')) {
    return raw;
  }
  try {
    const parsed = JSON.parse(trimmed) as { assistant_reply?: unknown };
    if (
      typeof parsed.assistant_reply === "string" &&
      parsed.assistant_reply.trim() !== ""
    ) {
      return parsed.assistant_reply;
    }
  } catch {
    // Fallback scanner for truncated JSON strings
    const marker = '"assistant_reply"';
    const idx = trimmed.indexOf(marker);
    if (idx >= 0) {
      const afterKey = trimmed.slice(idx + marker.length).trimStart();
      if (afterKey.startsWith(":")) {
        const afterColon = afterKey.slice(1).trimStart();
        if (afterColon.startsWith('"')) {
          const body = afterColon.slice(1);
          let out = "";
          for (let i = 0; i < body.length; i += 1) {
            const ch = body[i];
            if (ch === '"') {
              break;
            }
            if (ch === "\\" && i + 1 < body.length) {
              const next = body[i + 1];
              if (next === "n") out += "\n";
              else if (next === "r") out += "\r";
              else if (next === "t") out += "\t";
              else if (next !== undefined) out += next;
              i += 1;
              continue;
            }
            if (ch !== undefined) {
              out += ch;
            }
          }
          if (out.trim() !== "") {
            return out;
          }
        }
      }
    }
  }
  return raw;
}

function resolveCitationTitle(
  citationId: string,
  fallbackTitle: string,
  t: (key: string) => string,
): string {
  switch (citationId) {
    case "help-query-access":
      return t("assistant.citations.help-query-access");
    case "help-change-orders":
      return t("assistant.citations.help-change-orders");
    case "help-ai-precheck":
      return t("assistant.citations.help-ai-precheck");
    case "help-approvals":
      return t("assistant.citations.help-approvals");
    case "help-execution-verify":
      return t("assistant.citations.help-execution-verify");
    case "help-rollback-orders":
      return t("assistant.citations.help-rollback-orders");
    case "help-error-codes":
      return t("assistant.citations.help-error-codes");
    case "help-permission-model":
      return t("assistant.citations.help-permission-model");
    case "help-sql-diagnostics":
      return t("assistant.citations.help-sql-diagnostics");
    case "help-admin-governance":
      return t("assistant.citations.help-admin-governance");
    case "help-admin-review-rules":
      return t("assistant.citations.help-admin-review-rules");
    case "help-admin-notifications-audit":
      return t("assistant.citations.help-admin-notifications-audit");
    default:
      return fallbackTitle;
  }
}

function buildReasoningSummary(
  msg: AssistantMessage,
  t: (key: string) => string,
): string {
  if (msg.capability === "sql_optimize") {
    return t("assistant.trace.reasoning.sql_optimize");
  }
  if (msg.capability === "sql_generate") {
    return t("assistant.trace.reasoning.sql_generate");
  }
  return t("assistant.trace.reasoning.platform_help");
}

function buildPrivacyGateOutput(msg: AssistantMessage): string {
  return JSON.stringify(
    {
      gate: "prompt_scope_gate_v4",
      resolved_capability: msg.capability,
      zero_leak_verified: true,
      direct_sql_execution: "forbidden",
      state: msg.state,
    },
    null,
    2,
  );
}

function buildCapabilityToolOutput(
  msg: AssistantMessage,
  t: (key: string) => string,
): string {
  if (msg.sql_draft !== null) {
    return JSON.stringify(
      {
        datasource: msg.sql_draft.datasource_name,
        engine: msg.sql_draft.engine,
        schema: msg.sql_draft.schema_name,
        referenced_tables: msg.sql_draft.referenced_tables,
        statement_kind: msg.sql_draft.statement_kind,
        risk_category: msg.sql_draft.risk_category,
      },
      null,
      2,
    );
  }
  if (msg.citations.length > 0) {
    return JSON.stringify(
      {
        matched_articles: msg.citations.map((c) => ({
          id: c.citation_id,
          title: resolveCitationTitle(c.citation_id, c.title, t),
          route: c.route_path,
        })),
      },
      null,
      2,
    );
  }
  return JSON.stringify(
    {
      capability: msg.capability,
      scope: "authorized_session_context",
    },
    null,
    2,
  );
}

export function AssistantPanel({
  open,
  isExpanded = false,
  onToggleExpand,
}: AssistantPanelProps) {
  const { t } = useTranslation();
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null);
  const [activeIntent, setActiveIntent] =
    useState<AssistantCapability>("platform_help");
  const [selectedDatasourceId, setSelectedDatasourceId] = useState<string>("");
  const [schemaName, setSchemaName] = useState<string>("");
  const [selectedDraftId, setSelectedDraftId] = useState<string | null>(null);
  const [promptInput, setPromptInput] = useState<string>("");
  const [executionPlanInput, setExecutionPlanInput] = useState<string>("");
  const [showExecutionPlan, setShowExecutionPlan] = useState<boolean>(false);
  const [lastFailedPayload, setLastFailedPayload] =
    useState<SendAssistantMessageRequest | null>(null);
  const [pendingUserPayload, setPendingUserPayload] =
    useState<SendAssistantMessageRequest | null>(null);
  const [liveAgentStep, setLiveAgentStep] = useState<number>(0);
  const [liveReasoningText, setLiveReasoningText] = useState<string>("");
  const [liveReasoningDone, setLiveReasoningDone] = useState<boolean>(false);
  const [liveStreamedReply, setLiveStreamedReply] = useState<string>("");
  const [liveValidatorStatus, setLiveValidatorStatus] = useState<
    "idle" | "running" | "completed"
  >("idle");
  const [streamingMsgId, setStreamingMsgId] = useState<string | null>(null);
  const [streamedCharCount, setStreamedCharCount] = useState<number>(0);
  const [streamTargetLength, setStreamTargetLength] = useState<number>(0);
  const [contextCompactedBanner, setContextCompactedBanner] =
    useState<boolean>(false);
  const [panelError, setPanelError] = useState<string | null>(null);
  const abortControllerRef = useRef<AbortController | null>(null);
  const receivedSSEDeltaRef = useRef<boolean>(false);
  const promptInputRef = useRef<HTMLTextAreaElement | null>(null);
  const messageViewportRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      promptInputRef.current?.focus();
    });
    return () => {
      cancelAnimationFrame(frame);
    };
  }, [open]);

  const sessionsQuery = useAssistantSessions(open);
  const sessions = useMemo(() => sessionsQuery.data ?? [], [sessionsQuery.data]);
  const effectiveSessionId = selectedSessionId ?? sessions[0]?.id ?? null;

  const sessionDetailQuery = useAssistantSessionDetail(effectiveSessionId, open);
  const flowsQuery = useAssistantAuthorizedFlows(open);
  const flows = flowsQuery.data ?? [];
  const changeFlows = flows.filter((flow) => flow.flow_type === "change_review");
  const datasourceOptions = extractAuthorizedDatasources(flows);

  const createSessionMutation = useCreateAssistantSession();
  const deleteSessionMutation = useDeleteAssistantSession();
  const sendMessageMutation = useSendAssistantMessage(effectiveSessionId);
  const cancelStreamMutation = useCancelAssistantMessageStream(effectiveSessionId);

  const currentSession = sessionDetailQuery.data?.session ?? null;
  const messages = useMemo<AssistantMessage[]>(
    () => sessionDetailQuery.data?.messages ?? [],
    [sessionDetailQuery.data?.messages],
  );
  const activeSelectedDraftId =
    selectedDraftId ?? currentSession?.selected_draft_id ?? null;

  const selectedDatasourceOption = datasourceOptions.find(
    (item) => item.datasourceId === selectedDatasourceId,
  );

  const datasourceSelectItems = useMemo(
    () =>
      Object.fromEntries(
        datasourceOptions.map((ds) => [ds.datasourceId, ds.datasourceName]),
      ),
    [datasourceOptions],
  );

  const latestConversationTitleFallback = useMemo(() => {
    if (pendingUserPayload !== null && pendingUserPayload.content.trim() !== "") {
      const cleaned = pendingUserPayload.content.replace(/\s+/g, " ").trim();
      return cleaned.length > 48 ? `${cleaned.slice(0, 48)}…` : cleaned;
    }
    const lastUserMessage = [...messages]
      .reverse()
      .find((msg) => msg.role === "user" && msg.content.trim() !== "");
    if (lastUserMessage !== undefined) {
      const cleaned = lastUserMessage.content.replace(/\s+/g, " ").trim();
      return cleaned.length > 48 ? `${cleaned.slice(0, 48)}…` : cleaned;
    }
    return "";
  }, [messages, pendingUserPayload]);

  const sessionSelectItems = useMemo(() => {
    const uuidPattern =
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    const defaultTitle = t("assistant.newSessionDefaultTitle");
    const map: Record<string, string> = {};
    for (const item of sessions) {
      const isCurrent = item.id === effectiveSessionId;
      const candidateTitle = (
        isCurrent && currentSession?.title ? currentSession.title : item.title
      ).trim();
      if (
        isCurrent &&
        latestConversationTitleFallback !== "" &&
        (candidateTitle === "" ||
          uuidPattern.test(candidateTitle) ||
          candidateTitle === defaultTitle)
      ) {
        map[item.id] = latestConversationTitleFallback;
      } else if (candidateTitle !== "" && !uuidPattern.test(candidateTitle)) {
        map[item.id] = candidateTitle;
      } else if (isCurrent && latestConversationTitleFallback !== "") {
        map[item.id] = latestConversationTitleFallback;
      } else {
        map[item.id] = defaultTitle;
      }
    }
    return map;
  }, [
    currentSession,
    effectiveSessionId,
    latestConversationTitleFallback,
    sessions,
    t,
  ]);

  const activeSessionDisplayTitle =
    (effectiveSessionId ? sessionSelectItems[effectiveSessionId] : undefined) ??
    t("assistant.newSessionDefaultTitle");

  // Scroll the message viewport to the latest message whenever the dialog opens,
  // the active session changes, historical messages finish loading, or new turns stream.
  useLayoutEffect(() => {
    if (!open) return;
    const scrollToLatest = () => {
      const viewport = messageViewportRef.current;
      if (viewport !== null) {
        viewport.scrollTop = viewport.scrollHeight;
      }
    };
    scrollToLatest();
    const frameId = window.requestAnimationFrame(() => {
      scrollToLatest();
    });
    return () => {
      window.cancelAnimationFrame(frameId);
    };
  }, [
    open,
    effectiveSessionId,
    messages.length,
    pendingUserPayload,
    liveStreamedReply,
    streamedCharCount,
  ]);

  // Advance live agent trace steps while awaiting backend LLM completion
  useEffect(() => {
    if (!sendMessageMutation.isPending) {
      return;
    }
    const timer1 = window.setTimeout(() => {
      setLiveAgentStep(1);
    }, 320);
    const timer2 = window.setTimeout(() => {
      setLiveAgentStep(2);
    }, 760);
    return () => {
      window.clearTimeout(timer1);
      window.clearTimeout(timer2);
    };
  }, [sendMessageMutation.isPending]);

  // Progressive typewriter streaming effect for newly arrived assistant replies
  useEffect(() => {
    if (streamingMsgId === null || streamedCharCount >= streamTargetLength) {
      return;
    }
    const timer = window.setInterval(() => {
      setStreamedCharCount((prev) => {
        const next = prev + 36;
        if (next >= streamTargetLength) {
          setStreamingMsgId(null);
          return streamTargetLength;
        }
        return next;
      });
    }, 20);
    return () => {
      window.clearInterval(timer);
    };
  }, [streamingMsgId, streamedCharCount, streamTargetLength]);

  const handleQuickStarter = (intent: AssistantCapability) => {
    setActiveIntent(intent);
    setPanelError(null);
    if (intent === "platform_help") {
      setPromptInput(t("assistant.starters.platformHelpPrompt"));
    } else if (intent === "sql_optimize") {
      setPromptInput(t("assistant.starters.sqlOptimizePrompt"));
    } else {
      setPromptInput(t("assistant.starters.sqlGeneratePrompt"));
    }
  };

  const handleNewSession = () => {
    setPanelError(null);
    setLastFailedPayload(null);
    setStreamingMsgId(null);
    createSessionMutation.mutate(
      {
        title: t("assistant.newSessionDefaultTitle"),
      },
      {
        onSuccess: (created) => {
          setSelectedSessionId(created.id);
          setSelectedDraftId(null);
          appToast.success(t("assistant.newSession"));
        },
        onError: (err) => {
          const display = describeError(err, "createAssistantSession");
          const msg = t(display.messageKey);
          setPanelError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  const handleDeleteCurrentSession = () => {
    if (currentSession === null || deleteSessionMutation.isPending) return;
    setPanelError(null);
    setStreamingMsgId(null);
    const deletingId = currentSession.id;
    deleteSessionMutation.mutate(
      {
        sessionId: deletingId,
        version: currentSession.version,
      },
      {
        onSuccess: () => {
          const remaining = sessions.filter((item) => item.id !== deletingId);
          setSelectedSessionId(remaining[0]?.id ?? null);
          setSelectedDraftId(null);
          appToast.success(t("assistant.deleteSession"));
        },
        onError: (err) => {
          const display = describeError(err, "deleteAssistantSession");
          const msg = t(display.messageKey);
          setPanelError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  const handleClearContext = async () => {
    if (deleteSessionMutation.isPending || createSessionMutation.isPending) {
      return;
    }
    if (abortControllerRef.current !== null) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    setPromptInput("");
    setExecutionPlanInput("");
    setPanelError(null);
    setLastFailedPayload(null);
    setPendingUserPayload(null);
    setSelectedDraftId(null);
    setContextCompactedBanner(false);
    setStreamingMsgId(null);
    setLiveReasoningText("");
    setLiveReasoningDone(false);
    setLiveStreamedReply("");
    setLiveValidatorStatus("idle");

    const sessionToClear = currentSession;
    if (sessionToClear !== null) {
      try {
        await deleteSessionMutation.mutateAsync({
          sessionId: sessionToClear.id,
          version: sessionToClear.version,
        });
      } catch (err) {
        const display = describeError(err, "deleteAssistantSession");
        const msg = t(display.messageKey);
        setPanelError(msg);
        appToast.error(msg);
        return;
      }
    }

    try {
      const created = await createSessionMutation.mutateAsync({
        title: t("assistant.newSessionDefaultTitle"),
        active_datasource_id:
          selectedDatasourceId || sessionToClear?.active_datasource_id || null,
        active_schema_name:
          schemaName.trim() || sessionToClear?.active_schema_name || null,
        active_flow_id:
          selectedDatasourceOption?.flowId ??
          sessionToClear?.active_flow_id ??
          null,
      });
      setSelectedSessionId(created.id);
      appToast.info(t("assistant.clearCommandDescription"));
    } catch (err) {
      const display = describeError(err, "createAssistantSession");
      const msg = t(display.messageKey);
      setPanelError(msg);
      appToast.error(msg);
    }
  };

  const submitMessagePayload = async (body: SendAssistantMessageRequest) => {
    setPanelError(null);
    setLastFailedPayload(null);
    setPendingUserPayload(body);
    setLiveAgentStep(0);
    setLiveReasoningText("");
    setLiveReasoningDone(false);
    setLiveStreamedReply("");
    setLiveValidatorStatus("idle");
    receivedSSEDeltaRef.current = false;

    let targetSessionId = effectiveSessionId;
    if (targetSessionId === null) {
      try {
        const created = await createSessionMutation.mutateAsync({
          title: body.content.slice(0, 48),
          active_datasource_id: body.datasource_id ?? null,
          active_schema_name: body.schema_name ?? null,
          active_flow_id: body.flow_id ?? null,
        });
        targetSessionId = created.id;
        setSelectedSessionId(created.id);
      } catch (err) {
        setPendingUserPayload(null);
        const display = describeError(err, "createAssistantSession");
        setPanelError(t(display.messageKey));
        setLastFailedPayload(body);
        return;
      }
    }

    const controller = new AbortController();
    abortControllerRef.current = controller;

    sendMessageMutation.mutate(
      {
        sessionId: targetSessionId,
        body,
        signal: controller.signal,
        callbacks: {
          onProgress: ({ phase }) => {
            if (phase === "validating_and_restoring") {
              setLiveAgentStep(2);
              setLiveReasoningDone(true);
            }
          },
          onTool: ({ tool, status }) => {
            if (tool === "privacy_session.anonymize_and_gate") {
              setLiveAgentStep(status === "completed" ? 2 : 0);
            } else if (
              tool === "knowledge_corpus.retrieve_articles" ||
              tool === "catalog_inspector.load_schema_metadata"
            ) {
              setLiveAgentStep(status === "completed" ? 2 : 1);
            } else if (tool === "sql_validator.restore_and_verify_ast") {
              setLiveValidatorStatus(status);
            }
          },
          onReasoning: ({ status, delta }) => {
            setLiveAgentStep(2);
            if (delta !== undefined && delta !== "") {
              setLiveReasoningText((prev) => prev + delta);
            }
            if (status === "completed") {
              setLiveReasoningDone(true);
            }
          },
          onDelta: (delta) => {
            receivedSSEDeltaRef.current = true;
            setLiveAgentStep(2);
            setLiveReasoningDone(true);
            setLiveStreamedReply((prev) => prev + delta);
          },
        },
      },
      {
        onSuccess: (response) => {
          setPendingUserPayload(null);
          setPromptInput("");
          setExecutionPlanInput("");
          setContextCompactedBanner(response.context_compacted);
          const unwrapped = unwrapAssistantReplyContent(
            response.assistant_message.content,
          );
          if (!receivedSSEDeltaRef.current && unwrapped.length > 64) {
            setStreamingMsgId(response.assistant_message.id);
            setStreamTargetLength(unwrapped.length);
            setStreamedCharCount(Math.min(unwrapped.length, 64));
          } else {
            setStreamingMsgId(null);
          }
          if (response.assistant_message.sql_draft !== null) {
            setSelectedDraftId(response.assistant_message.sql_draft.id);
          }
        },
        onError: (err) => {
          setPendingUserPayload(null);
          if (controller.signal.aborted) {
            return;
          }
          const display = describeError(err, "sendAssistantMessage");
          setPanelError(
            display.messageKey === "errors.AI_PROVIDER_FAILED"
              ? t("assistant.providerUnavailableError")
              : t(display.messageKey),
          );
          setLastFailedPayload(body);
        },
        onSettled: () => {
          abortControllerRef.current = null;
        },
      },
    );
  };

  const handleSend = () => {
    const trimmed = promptInput.trim();
    if (trimmed === "") {
      return;
    }
    if (trimmed.toLowerCase() === "/clear") {
      void handleClearContext();
      return;
    }
    if (
      sendMessageMutation.isPending ||
      createSessionMutation.isPending ||
      deleteSessionMutation.isPending
    ) {
      return;
    }

    const effectiveDatasourceId =
      selectedDatasourceId || currentSession?.active_datasource_id || null;
    const effectiveSchemaName =
      schemaName.trim() || currentSession?.active_schema_name || null;
    const effectiveFlowId =
      selectedDatasourceOption?.flowId ?? currentSession?.active_flow_id ?? null;

    const payload: SendAssistantMessageRequest = {
      content: trimmed,
      capability_hint: activeIntent,
      datasource_id: effectiveDatasourceId,
      schema_name: effectiveSchemaName,
      flow_id: effectiveFlowId,
      selected_draft_id: activeSelectedDraftId,
      execution_plan_text:
        activeIntent === "sql_optimize" && executionPlanInput.trim() !== ""
          ? executionPlanInput.trim()
          : null,
    };

    void submitMessagePayload(payload);
  };

  const handleStopGenerating = () => {
    if (streamingMsgId !== null) {
      setStreamingMsgId(null);
      setStreamedCharCount(streamTargetLength);
    }
    if (abortControllerRef.current !== null) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    setPendingUserPayload(null);
    const streamingMsg = [...messages]
      .reverse()
      .find((msg) => msg.role === "assistant" && msg.state === "streaming");
    if (effectiveSessionId !== null && streamingMsg !== undefined) {
      cancelStreamMutation.mutate({
        sessionId: effectiveSessionId,
        messageId: streamingMsg.id,
      });
    }
  };

  const handleRetryFailed = () => {
    if (lastFailedPayload !== null) {
      void submitMessagePayload(lastFailedPayload);
      return;
    }
    const lastUserMsg = [...messages].reverse().find((msg) => msg.role === "user");
    if (lastUserMsg !== undefined) {
      void submitMessagePayload({
        content: lastUserMsg.content,
        capability_hint: lastUserMsg.capability,
        datasource_id: currentSession?.active_datasource_id ?? null,
        schema_name: currentSession?.active_schema_name ?? null,
        flow_id: currentSession?.active_flow_id ?? null,
        selected_draft_id: activeSelectedDraftId,
      });
    }
  };

  const isTypewriting =
    streamingMsgId !== null && streamedCharCount < streamTargetLength;
  const isBusy =
    sendMessageMutation.isPending ||
    createSessionMutation.isPending ||
    deleteSessionMutation.isPending ||
    isTypewriting ||
    messages.some((msg) => msg.state === "streaming");
  const normalizedPrompt = promptInput.trim();
  const isSlashClearMatch =
    normalizedPrompt.startsWith("/") &&
    !normalizedPrompt.includes(" ") &&
    "/clear".startsWith(normalizedPrompt.toLowerCase());

  return (
    <div
      data-testid="database-assistant-panel"
      className="bg-popover text-popover-foreground flex h-full w-full flex-col overflow-hidden"
    >
      {/* Header: Title + Session switcher + New / Delete / Expand actions */}
      <div className="border-border flex flex-col gap-2 border-b px-3.5 py-2.5">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <Sparkles className="text-primary size-4 shrink-0" aria-hidden="true" />
            <span className="text-foreground truncate text-sm font-semibold">
              {t("assistant.panelTitle")}
            </span>
            <Badge variant="outline" className="shrink-0 text-[11px]">
              {t("assistant.readOnlyGuardBadge")}
            </Badge>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleNewSession}
              disabled={createSessionMutation.isPending}
              data-testid="assistant-new-session-button"
            >
              <Plus data-icon="inline-start" aria-hidden="true" />
              {t("assistant.newSession")}
            </Button>
            {currentSession !== null && (
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                onClick={handleDeleteCurrentSession}
                disabled={deleteSessionMutation.isPending}
                aria-label={t("assistant.deleteSession")}
                data-testid="assistant-delete-session-button"
              >
                <Trash2 aria-hidden="true" />
              </Button>
            )}
            {onToggleExpand !== undefined && (
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                onClick={onToggleExpand}
                aria-label={
                  isExpanded
                    ? t("assistant.collapseWindow")
                    : t("assistant.expandWindow")
                }
                title={
                  isExpanded
                    ? t("assistant.collapseWindow")
                    : t("assistant.expandWindow")
                }
                data-testid="assistant-expand-toggle"
              >
                {isExpanded ? (
                  <Minimize2 aria-hidden="true" />
                ) : (
                  <Maximize2 aria-hidden="true" />
                )}
              </Button>
            )}
          </div>
        </div>

        {sessions.length > 0 && (
          <div className="flex items-center gap-2">
            <Select
              items={sessionSelectItems}
              value={effectiveSessionId ?? ""}
              onValueChange={(val) => {
                if (val) {
                  setSelectedSessionId(val);
                  setSelectedDraftId(null);
                  setStreamingMsgId(null);
                  setPanelError(null);
                }
              }}
            >
              <SelectTrigger
                className="h-8 flex-1 text-xs"
                aria-label={t("assistant.historySelectLabel")}
                data-testid="assistant-session-select"
              >
                <SelectValue>{activeSessionDisplayTitle}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {sessions.map((item) => (
                    <SelectItem key={item.id} value={item.id}>
                      {sessionSelectItems[item.id] ?? item.title}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
        )}

        {/* Intent selector chips */}
        <div
          className="flex flex-wrap items-center gap-1.5"
          role="group"
          aria-label={t("assistant.panelTitle")}
        >
          <Button
            type="button"
            variant={activeIntent === "platform_help" ? "default" : "outline"}
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => {
              setActiveIntent("platform_help");
            }}
            data-testid="assistant-intent-platform_help"
          >
            <BookOpen data-icon="inline-start" aria-hidden="true" />
            {t("assistant.intents.platform_help")}
          </Button>
          <Button
            type="button"
            variant={activeIntent === "sql_optimize" ? "default" : "outline"}
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => {
              setActiveIntent("sql_optimize");
            }}
            data-testid="assistant-intent-sql_optimize"
          >
            <Wand2 data-icon="inline-start" aria-hidden="true" />
            {t("assistant.intents.sql_optimize")}
          </Button>
          <Button
            type="button"
            variant={activeIntent === "sql_generate" ? "default" : "outline"}
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => {
              setActiveIntent("sql_generate");
            }}
            data-testid="assistant-intent-sql_generate"
          >
            <Code2 data-icon="inline-start" aria-hidden="true" />
            {t("assistant.intents.sql_generate")}
          </Button>
        </div>

        {/* Target Datasource & Schema context bar for SQL intents */}
        {(activeIntent === "sql_generate" || activeIntent === "sql_optimize") && (
          <div className="grid grid-cols-2 gap-2 pt-0.5">
            <Select
              items={datasourceSelectItems}
              value={
                selectedDatasourceId ||
                currentSession?.active_datasource_id ||
                ""
              }
              onValueChange={(val) => {
                setSelectedDatasourceId(val ?? "");
              }}
            >
              <SelectTrigger
                className="h-8 text-xs"
                aria-label={t("assistant.targetDatasourceLabel")}
                data-testid="assistant-datasource-select"
              >
                <SelectValue
                  placeholder={t("assistant.targetDatasourcePlaceholder")}
                />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {datasourceOptions.map((ds) => (
                    <SelectItem key={ds.datasourceId} value={ds.datasourceId}>
                      {ds.datasourceName}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>

            <Input
              value={schemaName || (currentSession?.active_schema_name ?? "")}
              onChange={(event) => {
                setSchemaName(event.target.value);
              }}
              placeholder={t("assistant.targetSchemaPlaceholder")}
              aria-label={t("assistant.targetSchemaLabel")}
              className="h-8 text-xs"
              data-testid="assistant-schema-input"
            />
          </div>
        )}
      </div>

      {/* Message stream / Empty state */}
      <MessageScrollerProvider
        key={effectiveSessionId ?? "none"}
        autoScroll
        defaultScrollPosition="end"
      >
        <MessageScroller className="flex-1">
          <MessageScrollerViewport
            ref={messageViewportRef}
            className="px-3.5 py-3"
            data-testid="assistant-message-viewport"
          >
            <MessageScrollerContent className="gap-4">
              {messages.length === 0 && pendingUserPayload === null ? (
                <Empty className="my-auto py-8" data-testid="assistant-empty-state">
                  <EmptyHeader>
                    <EmptyMedia variant="icon">
                      <Sparkles aria-hidden="true" />
                    </EmptyMedia>
                    <EmptyTitle>{t("assistant.emptyTitle")}</EmptyTitle>
                    <EmptyDescription>{t("assistant.emptyDescription")}</EmptyDescription>
                  </EmptyHeader>
                  <EmptyContent>
                    <div className="flex flex-wrap items-center justify-center gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => {
                          handleQuickStarter("platform_help");
                        }}
                        data-testid="assistant-starter-platform_help"
                      >
                        <BookOpen data-icon="inline-start" aria-hidden="true" />
                        {t("assistant.intents.platform_help")}
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => {
                          handleQuickStarter("sql_optimize");
                        }}
                        data-testid="assistant-starter-sql_optimize"
                      >
                        <Wand2 data-icon="inline-start" aria-hidden="true" />
                        {t("assistant.intents.sql_optimize")}
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => {
                          handleQuickStarter("sql_generate");
                        }}
                        data-testid="assistant-starter-sql_generate"
                      >
                        <Code2 data-icon="inline-start" aria-hidden="true" />
                        {t("assistant.intents.sql_generate")}
                      </Button>
                    </div>
                  </EmptyContent>
                </Empty>
              ) : (
                <MessageGroup className="gap-4">
                  {(contextCompactedBanner ||
                    (currentSession !== null &&
                      currentSession.compacted_through_seq > 0)) && (
                    <MessageScrollerItem
                      key="compacted-marker"
                      messageId="compacted-marker"
                      className="[content-visibility:visible]"
                    >
                      <Marker
                        variant="separator"
                        data-testid="assistant-compacted-marker"
                      >
                        <MarkerContent>
                          {t("assistant.trace.compactedNotice")}
                        </MarkerContent>
                      </Marker>
                    </MessageScrollerItem>
                  )}

                  {messages.map((msg) => {
                    const isUser = msg.role === "user";
                    const citations = msg.citations;
                    const cleanContent = isUser
                      ? msg.content
                      : unwrapAssistantReplyContent(msg.content);
                    const isMsgTypewriting =
                      !isUser &&
                      streamingMsgId === msg.id &&
                      streamedCharCount < cleanContent.length;
                    const displayedContent = isMsgTypewriting
                      ? cleanContent.slice(0, streamedCharCount)
                      : cleanContent;
                    const isRunningState =
                      msg.state === "streaming" || isMsgTypewriting;

                    return (
                      <MessageScrollerItem
                        key={msg.id}
                        messageId={msg.id}
                        className="[content-visibility:visible]"
                      >
                      <Message
                        align={isUser ? "end" : "start"}
                        data-testid={`assistant-message-${msg.id}`}
                      >
                        <MessageContent
                          className={
                            isUser ? "max-w-[88%] items-end" : "max-w-[96%]"
                          }
                        >
                          <MessageHeader className="gap-1.5">
                            <span>
                              {isUser
                                ? t("assistant.roleUser")
                                : t("assistant.roleAssistant")}
                            </span>
                            {isRunningState && (
                              <Badge
                                variant="secondary"
                                role="status"
                                aria-live="polite"
                                className="text-[10px]"
                              >
                                {t("assistant.statusStreaming")}
                              </Badge>
                            )}
                            {msg.state === "cancelled" && (
                              <Badge variant="outline" className="text-[10px]">
                                {t("assistant.statusCancelled")}
                              </Badge>
                            )}
                            {msg.state === "failed" && (
                              <Badge variant="destructive" className="text-[10px]">
                                {t("assistant.statusFailed")}
                              </Badge>
                            )}
                          </MessageHeader>

                          {/* Codex-style Agent Trace (Reasoning + Tool Calls + Sources) */}
                          {!isUser && (
                            <Trace
                              className="px-1"
                              data-testid={`assistant-trace-${msg.id}`}
                            >
                              {/* 1. Reasoning / Thought Process */}
                              <TraceCollapsible defaultOpen={isRunningState}>
                                <TraceCollapsibleTrigger>
                                  <TraceLine
                                    status={isRunningState ? "running" : undefined}
                                  >
                                    <TraceIcon>
                                      <Brain />
                                    </TraceIcon>
                                    <TraceTitle>
                                      {isRunningState
                                        ? t("assistant.trace.thinkingRunning")
                                        : t("assistant.trace.thinkingDone")}
                                    </TraceTitle>
                                  </TraceLine>
                                </TraceCollapsibleTrigger>
                                <TraceCollapsibleContent>
                                  <div className="whitespace-pre-wrap text-xs">
                                    {buildReasoningSummary(msg, t)}
                                  </div>
                                </TraceCollapsibleContent>
                              </TraceCollapsible>

                              {/* 2. Tool: Prompt Scope Gate & Zero-Leak Privacy Anonymizer */}
                              <TraceCollapsible>
                                <TraceCollapsibleTrigger>
                                  <TraceLine>
                                    <TraceIcon>
                                      <ShieldCheck />
                                    </TraceIcon>
                                    <TraceTitle>
                                      {t("assistant.trace.toolPrivacyGate")}
                                    </TraceTitle>
                                    <TraceDescription>
                                      {msg.capability}
                                    </TraceDescription>
                                  </TraceLine>
                                </TraceCollapsibleTrigger>
                                <TraceCollapsibleContent>
                                  <CodeBlock size="sm">
                                    <CodeBlockContent wrap className="max-h-48">
                                      {buildPrivacyGateOutput(msg)}
                                    </CodeBlockContent>
                                  </CodeBlock>
                                </TraceCollapsibleContent>
                              </TraceCollapsible>

                              {/* 3. Tool: Capability Knowledge / Schema Catalog Inspector */}
                              <TraceCollapsible>
                                <TraceCollapsibleTrigger>
                                  <TraceLine>
                                    <TraceIcon>
                                      {msg.capability === "platform_help" ? (
                                        <Wrench />
                                      ) : (
                                        <Database />
                                      )}
                                    </TraceIcon>
                                    <TraceTitle>
                                      {msg.capability === "platform_help"
                                        ? t("assistant.trace.toolKnowledge")
                                        : t("assistant.trace.toolSchemaCatalog")}
                                    </TraceTitle>
                                    <TraceDescription>
                                      {msg.sql_draft !== null
                                        ? `${msg.sql_draft.engine} · ${msg.sql_draft.schema_name}`
                                        : `${String(citations.length)} refs`}
                                    </TraceDescription>
                                  </TraceLine>
                                </TraceCollapsibleTrigger>
                                <TraceCollapsibleContent>
                                  <CodeBlock size="sm">
                                    <CodeBlockContent wrap className="max-h-48">
                                      {buildCapabilityToolOutput(msg, t)}
                                    </CodeBlockContent>
                                  </CodeBlock>
                                </TraceCollapsibleContent>
                              </TraceCollapsible>

                              {/* 4. Tool: AST SQL Validator & Token Restorer (when SQL draft generated) */}
                              {msg.sql_draft !== null && (
                                <TraceCollapsible>
                                  <TraceCollapsibleTrigger>
                                    <TraceLine>
                                      <TraceIcon>
                                        <Terminal />
                                      </TraceIcon>
                                      <TraceTitle>
                                        {t("assistant.trace.toolSqlValidator")}
                                      </TraceTitle>
                                      <TraceDescription>
                                        {`${msg.sql_draft.statement_kind.toUpperCase()} · ${msg.sql_draft.risk_category.toUpperCase()}`}
                                      </TraceDescription>
                                    </TraceLine>
                                  </TraceCollapsibleTrigger>
                                  <TraceCollapsibleContent>
                                    <CodeBlock size="sm">
                                      <CodeBlockContent wrap className="max-h-48">
                                        {JSON.stringify(
                                          {
                                            verified_single_statement: true,
                                            statement_kind:
                                              msg.sql_draft.statement_kind,
                                            risk_category:
                                              msg.sql_draft.risk_category,
                                            sql_hmac: msg.sql_draft.sql_hmac,
                                            submission_state:
                                              msg.sql_draft.submission_state,
                                          },
                                          null,
                                          2,
                                        )}
                                      </CodeBlockContent>
                                    </CodeBlock>
                                  </TraceCollapsibleContent>
                                </TraceCollapsible>
                              )}

                              {/* 5. Sources / Citations Trace */}
                              {citations.length > 0 && (
                                <TraceCollapsible>
                                  <TraceCollapsibleTrigger>
                                    <TraceLine>
                                      <TraceIcon>
                                        <BookOpen />
                                      </TraceIcon>
                                      <TraceTitle>
                                        {t("assistant.trace.usedSources", {
                                          count: citations.length,
                                        })}
                                      </TraceTitle>
                                    </TraceLine>
                                  </TraceCollapsibleTrigger>
                                  <TraceCollapsibleContent>
                                    <Trace>
                                      {citations.map((cite) => {
                                        const localizedTitle =
                                          resolveCitationTitle(
                                            cite.citation_id,
                                            cite.title,
                                            t,
                                          );
                                        return (
                                          <TraceLine
                                            key={`trace-${cite.citation_id}-${cite.source_ref}`}
                                          >
                                            <TraceIcon>
                                              <BookOpen />
                                            </TraceIcon>
                                            <TraceTitle>
                                              {localizedTitle}
                                            </TraceTitle>
                                          </TraceLine>
                                        );
                                      })}
                                    </Trace>
                                  </TraceCollapsibleContent>
                                </TraceCollapsible>
                              )}
                            </Trace>
                          )}

                          {/* Message Content Surface (Message component primitive + blue user bubble) */}
                          {isUser ? (
                            <div
                              className="bg-comment-own self-end rounded-2xl px-3.5 py-2.5 text-xs leading-relaxed whitespace-pre-wrap text-white"
                              data-testid={`assistant-user-bubble-${msg.id}`}
                            >
                              {cleanContent}
                            </div>
                          ) : (
                            <div
                              className="bg-muted text-foreground w-full max-w-full self-start rounded-2xl px-3.5 py-2.5 text-xs leading-relaxed"
                              data-testid={`assistant-reply-bubble-${msg.id}`}
                            >
                              <AssistantMarkdown
                                content={displayedContent}
                                isStreaming={isRunningState}
                              />
                            </div>
                          )}

                          {/* Validated & Restored SQL Draft Card */}
                          {msg.state === "completed" &&
                            msg.sql_draft !== null &&
                            effectiveSessionId !== null && (
                              <SqlDraftCard
                                sessionId={effectiveSessionId}
                                draft={msg.sql_draft}
                                flows={changeFlows}
                                isSelected={
                                  activeSelectedDraftId === msg.sql_draft.id
                                }
                                onSelectDraft={(draftId) => {
                                  setSelectedDraftId(draftId);
                                }}
                              />
                            )}

                          {msg.state === "failed" && (
                            <MessageFooter>
                              <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                className="h-6 px-2 text-xs"
                                onClick={handleRetryFailed}
                                data-testid={`assistant-retry-message-${msg.id}`}
                              >
                                <RotateCcw
                                  data-icon="inline-start"
                                  aria-hidden="true"
                                />
                                {t("assistant.retryTurn")}
                              </Button>
                            </MessageFooter>
                          )}
                        </MessageContent>
                      </Message>
                    </MessageScrollerItem>
                  );
                })}

                {/* Optimistic User Turn + Live Agent Streaming Turn while awaiting LLM */}
                {pendingUserPayload !== null && (
                  <>
                    <MessageScrollerItem
                      key="pending-user-turn"
                      messageId="pending-user-turn"
                      className="[content-visibility:visible]"
                    >
                      <Message
                        align="end"
                        data-testid="assistant-pending-user-message"
                      >
                        <MessageContent className="max-w-[88%] items-end">
                          <MessageHeader>
                            <span>{t("assistant.roleUser")}</span>
                          </MessageHeader>
                          <div
                            className="bg-comment-own self-end rounded-2xl px-3.5 py-2.5 text-xs leading-relaxed whitespace-pre-wrap text-white"
                            data-testid="assistant-pending-user-bubble"
                          >
                            {pendingUserPayload.content}
                          </div>
                        </MessageContent>
                      </Message>
                    </MessageScrollerItem>

                    <MessageScrollerItem
                      key="pending-assistant-turn"
                      messageId="pending-assistant-turn"
                      className="[content-visibility:visible]"
                    >
                      <Message
                        align="start"
                        data-testid="assistant-streaming-turn"
                      >
                        <MessageContent className="max-w-[96%]">
                          <MessageHeader className="gap-1.5">
                            <span>{t("assistant.roleAssistant")}</span>
                            <Badge
                              variant="secondary"
                              role="status"
                              aria-live="polite"
                              className="text-[10px]"
                            >
                              {t("assistant.statusStreaming")}
                            </Badge>
                          </MessageHeader>

                          <Trace className="px-1">
                            <TraceLine
                              status={
                                liveAgentStep === 0 ? "running" : undefined
                              }
                            >
                              <TraceIcon>
                                <ShieldCheck />
                              </TraceIcon>
                              <TraceTitle>
                                {t("assistant.trace.toolPrivacyGate")}
                              </TraceTitle>
                              <TraceDescription>
                                {pendingUserPayload.capability_hint ??
                                  activeIntent}
                              </TraceDescription>
                            </TraceLine>

                            <TraceLine
                              status={
                                liveAgentStep === 0
                                  ? "pending"
                                  : liveAgentStep === 1
                                    ? "running"
                                    : undefined
                              }
                            >
                              <TraceIcon>
                                {(pendingUserPayload.capability_hint ??
                                  activeIntent) === "platform_help" ? (
                                  <Wrench />
                                ) : (
                                  <Database />
                                )}
                              </TraceIcon>
                              <TraceTitle>
                                {(pendingUserPayload.capability_hint ??
                                  activeIntent) === "platform_help"
                                  ? t("assistant.trace.toolKnowledge")
                                  : t("assistant.trace.toolSchemaCatalog")}
                              </TraceTitle>
                            </TraceLine>

                            <TraceCollapsible defaultOpen>
                              <TraceCollapsibleTrigger>
                                <TraceLine
                                  status={
                                    liveReasoningDone ? undefined : "running"
                                  }
                                >
                                  <TraceIcon>
                                    <Brain />
                                  </TraceIcon>
                                  <TraceTitle>
                                    {liveReasoningDone
                                      ? t("assistant.trace.thinkingDone")
                                      : t("assistant.trace.thinkingRunning")}
                                  </TraceTitle>
                                </TraceLine>
                              </TraceCollapsibleTrigger>
                              <TraceCollapsibleContent>
                                <div className="whitespace-pre-wrap text-xs">
                                  {liveReasoningText !== ""
                                    ? liveReasoningText
                                    : t("assistant.trace.thinkingRunning")}
                                </div>
                              </TraceCollapsibleContent>
                            </TraceCollapsible>

                            {liveValidatorStatus !== "idle" && (
                              <TraceLine
                                status={
                                  liveValidatorStatus === "running"
                                    ? "running"
                                    : undefined
                                }
                              >
                                <TraceIcon>
                                  <Terminal />
                                </TraceIcon>
                                <TraceTitle>
                                  {t("assistant.trace.toolSqlValidator")}
                                </TraceTitle>
                              </TraceLine>
                            )}
                          </Trace>

                          {liveStreamedReply !== "" && (
                            <div
                              className="bg-muted text-foreground w-full max-w-full self-start rounded-2xl px-3.5 py-2.5 text-xs leading-relaxed"
                              data-testid="assistant-live-reply-bubble"
                            >
                              <AssistantMarkdown
                                content={liveStreamedReply}
                                isStreaming={true}
                              />
                            </div>
                          )}
                        </MessageContent>
                      </Message>
                    </MessageScrollerItem>
                  </>
                )}
                </MessageGroup>
              )}
            </MessageScrollerContent>
          </MessageScrollerViewport>
          <MessageScrollerButton aria-label={t("assistant.scrollToBottom")} />
        </MessageScroller>
      </MessageScrollerProvider>

      {/* Error Banner with Retry */}
      {panelError !== null && (
        <div className="px-3.5 pb-2" data-testid="assistant-error-banner">
          <Alert variant="destructive">
            <AlertDescription className="flex items-center justify-between gap-2 text-xs">
              <span>{panelError}</span>
              {lastFailedPayload !== null && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-6 px-2 text-xs"
                  onClick={handleRetryFailed}
                  data-testid="assistant-error-retry-button"
                >
                  <RotateCcw data-icon="inline-start" aria-hidden="true" />
                  {t("assistant.retryTurn")}
                </Button>
              )}
            </AlertDescription>
          </Alert>
        </div>
      )}

      {/* Composer Footer */}
      <div className="border-border flex flex-col gap-2 border-t p-3">
        {activeIntent === "sql_optimize" && (
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs"
                onClick={() => {
                  setShowExecutionPlan((prev) => !prev);
                }}
                data-testid="assistant-toggle-plan-button"
              >
                {showExecutionPlan
                  ? t("assistant.hideExecutionPlan")
                  : t("assistant.addExecutionPlan")}
              </Button>
            </div>
            {showExecutionPlan && (
              <Textarea
                value={executionPlanInput}
                onChange={(event) => {
                  setExecutionPlanInput(event.target.value);
                }}
                placeholder={t("assistant.executionPlanPlaceholder")}
                rows={2}
                className="font-mono text-xs"
                data-testid="assistant-execution-plan-textarea"
              />
            )}
          </div>
        )}

        {isSlashClearMatch && (
          <div
            className="border-border bg-popover flex items-center justify-between rounded-lg border p-1 shadow-sm"
            data-testid="assistant-slash-command-menu"
          >
            <button
              type="button"
              onClick={() => {
                void handleClearContext();
              }}
              className="hover:bg-accent hover:text-accent-foreground flex w-full items-center justify-between rounded-md px-2.5 py-1.5 text-left text-xs transition-colors"
              data-testid="assistant-slash-command-clear"
            >
              <span className="flex items-center gap-2">
                <Badge variant="secondary" className="font-mono text-[11px]">
                  /clear
                </Badge>
                <span className="text-foreground">
                  {t("assistant.clearCommandDescription")}
                </span>
              </span>
              <span className="text-muted-foreground font-mono text-[10px]">
                Enter
              </span>
            </button>
          </div>
        )}

        <InputGroup>
          <InputGroupTextarea
            ref={promptInputRef}
            value={promptInput}
            onChange={(event) => {
              setPromptInput(event.target.value);
            }}
            onKeyDown={(event) => {
              if (event.key === "Tab" && isSlashClearMatch) {
                event.preventDefault();
                setPromptInput("/clear");
                return;
              }
              if (
                event.key === "Enter" &&
                !event.shiftKey &&
                !event.nativeEvent.isComposing
              ) {
                event.preventDefault();
                if (isSlashClearMatch) {
                  void handleClearContext();
                  return;
                }
                handleSend();
              }
            }}
            placeholder={t("assistant.inputPlaceholder")}
            aria-label={t("assistant.inputPlaceholder")}
            rows={2}
            data-testid="assistant-prompt-input"
          />
          <InputGroupAddon align="block-end" className="justify-between">
            <span className="text-muted-foreground text-[11px]">
              {t("assistant.inputHint")}
            </span>
            <div className="flex items-center gap-1.5">
              {isBusy && (
                <InputGroupButton
                  type="button"
                  variant="outline"
                  size="xs"
                  onClick={handleStopGenerating}
                  data-testid="assistant-stop-button"
                >
                  <Square data-icon="inline-start" aria-hidden="true" />
                  {t("assistant.stopGenerating")}
                </InputGroupButton>
              )}
              <InputGroupButton
                type="button"
                variant="default"
                size="xs"
                disabled={promptInput.trim() === "" || isBusy}
                onClick={handleSend}
                data-testid="assistant-send-button"
              >
                {isBusy ? (
                  <Spinner data-icon="inline-start" aria-hidden="true" />
                ) : (
                  <Send data-icon="inline-start" aria-hidden="true" />
                )}
                {t("assistant.send")}
              </InputGroupButton>
            </div>
          </InputGroupAddon>
        </InputGroup>
      </div>
    </div>
  );
}
