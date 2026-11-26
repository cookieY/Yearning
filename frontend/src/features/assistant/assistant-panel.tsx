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
import { AssistantMessageList } from "./assistant-message-list";
import {
  AssistantPanelHeader,
  buildAssistantSessionSelectItems,
  resolveLatestConversationTitleFallback,
  unwrapAssistantReplyContent,
} from "./assistant-panel-header";
import { useAssistantTurnStream } from "./use-assistant-turn-stream";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";

export { unwrapAssistantReplyContent };

export interface AssistantPanelProps {
  open: boolean;
  isExpanded?: boolean;
  onToggleExpand?: () => void;
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

  const turnStream = useAssistantTurnStream({
    effectiveSessionId,
    currentSession,
    messages,
    selectedDatasourceId,
    schemaName,
    selectedDatasourceFlowId: selectedDatasourceOption?.flowId ?? null,
    activeSelectedDraftId,
    createSessionMutation,
    deleteSessionMutation,
    sendMessageMutation,
    cancelStreamMutation,
    onSelectSessionId: setSelectedSessionId,
    onSelectDraftId: setSelectedDraftId,
    onResetComposerInputs: () => {
      setPromptInput("");
      setExecutionPlanInput("");
    },
  });

  const latestConversationTitleFallback = useMemo(
    () => resolveLatestConversationTitleFallback(messages, turnStream.pendingUserPayload),
    [messages, turnStream.pendingUserPayload],
  );

  const sessionSelectItems = useMemo(
    () =>
      buildAssistantSessionSelectItems({
        sessions,
        effectiveSessionId,
        currentSession,
        latestConversationTitleFallback,
        defaultTitle: t("assistant.newSessionDefaultTitle"),
      }),
    [
      currentSession,
      effectiveSessionId,
      latestConversationTitleFallback,
      sessions,
      t,
    ],
  );

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
    turnStream.pendingUserPayload,
    turnStream.liveStreamedReply,
    turnStream.streamedCharCount,
  ]);

  const handleQuickStarter = (intent: AssistantCapability) => {
    setActiveIntent(intent);
    turnStream.setPanelError(null);
    if (intent === "platform_help") {
      setPromptInput(t("assistant.starters.platformHelpPrompt"));
    } else if (intent === "sql_optimize") {
      setPromptInput(t("assistant.starters.sqlOptimizePrompt"));
    } else {
      setPromptInput(t("assistant.starters.sqlGeneratePrompt"));
    }
  };

  const handleNewSession = () => {
    turnStream.setPanelError(null);
    turnStream.setLastFailedPayload(null);
    turnStream.setTypewriter(null);
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
          turnStream.setPanelError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  const handleDeleteCurrentSession = () => {
    if (currentSession === null || deleteSessionMutation.isPending) return;
    turnStream.setPanelError(null);
    turnStream.setTypewriter(null);
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
          turnStream.setPanelError(msg);
          appToast.error(msg);
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
      void turnStream.handleClearContext();
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

    void turnStream.submitMessagePayload(payload);
  };

  const isBusy =
    sendMessageMutation.isPending ||
    createSessionMutation.isPending ||
    deleteSessionMutation.isPending ||
    turnStream.isTypewriting ||
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
      <AssistantPanelHeader
        sessions={sessions}
        effectiveSessionId={effectiveSessionId}
        currentSession={currentSession}
        sessionSelectItems={sessionSelectItems}
        activeSessionDisplayTitle={activeSessionDisplayTitle}
        activeIntent={activeIntent}
        selectedDatasourceId={selectedDatasourceId}
        schemaName={schemaName}
        datasourceOptions={datasourceOptions}
        datasourceSelectItems={datasourceSelectItems}
        isCreatePending={createSessionMutation.isPending}
        isDeletePending={deleteSessionMutation.isPending}
        isExpanded={isExpanded}
        onToggleExpand={onToggleExpand}
        onNewSession={handleNewSession}
        onDeleteCurrentSession={handleDeleteCurrentSession}
        onSelectSession={(val) => {
          setSelectedSessionId(val);
          setSelectedDraftId(null);
          turnStream.setTypewriter(null);
          turnStream.setPanelError(null);
        }}
        onSelectIntent={setActiveIntent}
        onSelectDatasource={setSelectedDatasourceId}
        onSchemaNameChange={setSchemaName}
      />

      <AssistantMessageList
        effectiveSessionId={effectiveSessionId}
        messageViewportRef={messageViewportRef}
        messages={messages}
        pendingUserPayload={turnStream.pendingUserPayload}
        activeIntent={activeIntent}
        showCompactedMarker={
          turnStream.contextCompactedBanner ||
          (currentSession !== null && currentSession.compacted_through_seq > 0)
        }
        typewriter={turnStream.typewriter}
        unwrapReplyContent={unwrapAssistantReplyContent}
        changeFlows={changeFlows}
        activeSelectedDraftId={activeSelectedDraftId}
        liveAgentStep={turnStream.liveAgentStep}
        liveReasoningDone={turnStream.liveReasoningDone}
        liveReasoningText={turnStream.liveReasoningText}
        liveValidatorStatus={turnStream.liveValidatorStatus}
        liveStreamedReply={turnStream.liveStreamedReply}
        onQuickStarter={handleQuickStarter}
        onSelectDraft={(draftId) => {
          setSelectedDraftId(draftId);
        }}
        onRetryFailed={turnStream.handleRetryFailed}
      />

      <AssistantComposer
        promptInputRef={promptInputRef}
        panelError={turnStream.panelError}
        lastFailedPayload={turnStream.lastFailedPayload}
        onRetryFailed={turnStream.handleRetryFailed}
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
          void turnStream.handleClearContext();
        }}
        onSend={handleSend}
        onStopGenerating={turnStream.handleStopGenerating}
      />
    </div>
  );
}
