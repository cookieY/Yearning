import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  AssistantMessage,
  AssistantSession,
  SendAssistantMessageRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type {
  useCancelAssistantMessageStream,
  useCreateAssistantSession,
  useDeleteAssistantSession,
  useSendAssistantMessage,
} from "./use-assistant";
import type { AssistantTypewriterState } from "./assistant-message-list";
import { unwrapAssistantReplyContent } from "./assistant-panel-header";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";

interface UseAssistantTurnStreamArgs {
  effectiveSessionId: string | null;
  currentSession: AssistantSession | null;
  messages: readonly AssistantMessage[];
  selectedDatasourceId: string;
  schemaName: string;
  selectedDatasourceFlowId: string | null;
  activeSelectedDraftId: string | null;
  createSessionMutation: ReturnType<typeof useCreateAssistantSession>;
  deleteSessionMutation: ReturnType<typeof useDeleteAssistantSession>;
  sendMessageMutation: ReturnType<typeof useSendAssistantMessage>;
  cancelStreamMutation: ReturnType<typeof useCancelAssistantMessageStream>;
  onSelectSessionId: (sessionId: string) => void;
  onSelectDraftId: (draftId: string | null) => void;
  onResetComposerInputs: () => void;
}

export function useAssistantTurnStream({
  effectiveSessionId,
  currentSession,
  messages,
  selectedDatasourceId,
  schemaName,
  selectedDatasourceFlowId,
  activeSelectedDraftId,
  createSessionMutation,
  deleteSessionMutation,
  sendMessageMutation,
  cancelStreamMutation,
  onSelectSessionId,
  onSelectDraftId,
  onResetComposerInputs,
}: UseAssistantTurnStreamArgs) {
  const { t } = useTranslation();
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
  const [contextCompactedBanner, setContextCompactedBanner] = useState<boolean>(false);
  const [panelError, setPanelError] = useState<string | null>(null);
  const abortControllerRef = useRef<AbortController | null>(null);
  const receivedSSEDeltaRef = useRef<boolean>(false);

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

  const handleClearContext = async () => {
    if (deleteSessionMutation.isPending || createSessionMutation.isPending) {
      return;
    }
    if (abortControllerRef.current !== null) {
      abortControllerRef.current.abort();
      abortControllerRef.current = null;
    }
    onResetComposerInputs();
    setPanelError(null);
    setLastFailedPayload(null);
    setPendingUserPayload(null);
    onSelectDraftId(null);
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
          selectedDatasourceFlowId ??
          sessionToClear?.active_flow_id ??
          null,
      });
      onSelectSessionId(created.id);
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
        onSelectSessionId(created.id);
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
          onResetComposerInputs();
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
            onSelectDraftId(response.assistant_message.sql_draft.id);
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

  return {
    lastFailedPayload,
    setLastFailedPayload,
    pendingUserPayload,
    liveAgentStep,
    liveReasoningText,
    liveReasoningDone,
    liveStreamedReply,
    liveValidatorStatus,
    typewriter,
    setTypewriter,
    streamedCharCount,
    isTypewriting,
    contextCompactedBanner,
    panelError,
    setPanelError,
    submitMessagePayload,
    handleClearContext,
    handleStopGenerating,
    handleRetryFailed,
  };
}
