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
import { AssistantComposer } from "./assistant-composer";
import {
  AssistantMessageList,
  type AssistantTypewriterState,
} from "./assistant-message-list";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import {
  BookOpen,
  Code2,
  Maximize2,
  Minimize2,
  Plus,
  Sparkles,
  Trash2,
  Wand2,
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
  const [typewriter, setTypewriter] = useState<AssistantTypewriterState | null>(null);
  const activeTypewriterMsgId = typewriter?.msgId ?? null;
  const streamedCharCount = typewriter?.count ?? 0;
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
    if (activeTypewriterMsgId === null) {
      return;
    }
    const timer = window.setInterval(() => {
      setTypewriter((prev) => {
        if (prev === null || prev.msgId !== activeTypewriterMsgId) {
          return prev;
        }
        const next = prev.count + 36;
        return next >= prev.target ? null : { ...prev, count: next };
      });
    }, 20);
    return () => {
      window.clearInterval(timer);
    };
  }, [activeTypewriterMsgId]);

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
    setTypewriter(null);
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
    setTypewriter(null);
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
          appToast.success(t("assistant.sessionDeleted"));
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
    setTypewriter(null);
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
            setTypewriter({
              msgId: response.assistant_message.id,
              count: 64,
              target: unwrapped.length,
            });
          } else {
            setTypewriter(null);
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
    setTypewriter(null);
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

  const isTypewriting = typewriter !== null && typewriter.count < typewriter.target;
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
                  setTypewriter(null);
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

      <AssistantMessageList
        effectiveSessionId={effectiveSessionId}
        messageViewportRef={messageViewportRef}
        messages={messages}
        pendingUserPayload={pendingUserPayload}
        activeIntent={activeIntent}
        showCompactedMarker={
          contextCompactedBanner ||
          (currentSession !== null && currentSession.compacted_through_seq > 0)
        }
        typewriter={typewriter}
        unwrapReplyContent={unwrapAssistantReplyContent}
        changeFlows={changeFlows}
        activeSelectedDraftId={activeSelectedDraftId}
        liveAgentStep={liveAgentStep}
        liveReasoningDone={liveReasoningDone}
        liveReasoningText={liveReasoningText}
        liveValidatorStatus={liveValidatorStatus}
        liveStreamedReply={liveStreamedReply}
        onQuickStarter={handleQuickStarter}
        onSelectDraft={(draftId) => {
          setSelectedDraftId(draftId);
        }}
        onRetryFailed={handleRetryFailed}
      />

      <AssistantComposer
        promptInputRef={promptInputRef}
        panelError={panelError}
        lastFailedPayload={lastFailedPayload}
        onRetryFailed={handleRetryFailed}
        activeIntent={activeIntent}
        showExecutionPlan={showExecutionPlan}
        onToggleExecutionPlan={() => {
          setShowExecutionPlan((prev) => !prev);
        }}
        executionPlanInput={executionPlanInput}
        onExecutionPlanChange={setExecutionPlanInput}
        isSlashClearMatch={isSlashClearMatch}
        promptInput={promptInput}
        onPromptInputChange={setPromptInput}
        isBusy={isBusy}
        onClearContext={() => {
          void handleClearContext();
        }}
        onSend={handleSend}
        onStopGenerating={handleStopGenerating}
      />
    </div>
  );
}
