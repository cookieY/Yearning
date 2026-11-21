import type { RefObject } from "react";
import { useTranslation } from "react-i18next";
import { BookOpen, Code2, RotateCcw, Sparkles, Wand2 } from "lucide-react";
import type {
  AssistantCapability,
  AssistantMessage,
  Flow,
  SendAssistantMessageRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/shared/components/ui/empty";
import { Marker, MarkerContent } from "@/shared/components/ui/marker";
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
import { AssistantMarkdown } from "./assistant-markdown";
import {
  AssistantLiveTraceView,
  AssistantTraceView,
} from "./assistant-trace-view";
import { SqlDraftCard } from "./sql-draft-card";

export interface AssistantTypewriterState {
  msgId: string;
  count: number;
  target: number;
}

export interface AssistantMessageListProps {
  effectiveSessionId: string | null;
  messageViewportRef: RefObject<HTMLDivElement | null>;
  messages: AssistantMessage[];
  pendingUserPayload: SendAssistantMessageRequest | null;
  activeIntent: AssistantCapability;
  showCompactedMarker: boolean;
  typewriter: AssistantTypewriterState | null;
  unwrapReplyContent: (raw: string) => string;
  changeFlows: Flow[];
  activeSelectedDraftId: string | null;
  liveAgentStep: number;
  liveReasoningDone: boolean;
  liveReasoningText: string;
  liveValidatorStatus: "idle" | "running" | "completed";
  liveStreamedReply: string;
  onQuickStarter: (intent: AssistantCapability) => void;
  onSelectDraft: (draftId: string) => void;
  onRetryFailed: () => void;
}

/** Renders the scrollable assistant message transcript, empty starter prompts, and live streaming turn. */
export function AssistantMessageList({
  effectiveSessionId,
  messageViewportRef,
  messages,
  pendingUserPayload,
  activeIntent,
  showCompactedMarker,
  typewriter,
  unwrapReplyContent,
  changeFlows,
  activeSelectedDraftId,
  liveAgentStep,
  liveReasoningDone,
  liveReasoningText,
  liveValidatorStatus,
  liveStreamedReply,
  onQuickStarter,
  onSelectDraft,
  onRetryFailed,
}: AssistantMessageListProps) {
  const { t } = useTranslation();

  return (
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
                        onQuickStarter("platform_help");
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
                        onQuickStarter("sql_optimize");
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
                        onQuickStarter("sql_generate");
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
                {showCompactedMarker && (
                  <MessageScrollerItem
                    key="compacted-marker"
                    messageId="compacted-marker"
                    className="[content-visibility:visible]"
                  >
                    <Marker
                      variant="separator"
                      data-testid="assistant-compacted-marker"
                    >
                      <MarkerContent>{t("assistant.trace.compactedNotice")}</MarkerContent>
                    </Marker>
                  </MessageScrollerItem>
                )}

                {messages.map((msg) => {
                  const isUser = msg.role === "user";
                  const cleanContent = isUser
                    ? msg.content
                    : unwrapReplyContent(msg.content);
                  const isMsgTypewriting =
                    !isUser &&
                    typewriter !== null &&
                    typewriter.msgId === msg.id &&
                    typewriter.count < cleanContent.length;
                  const displayedContent = isMsgTypewriting
                    ? cleanContent.slice(0, typewriter.count)
                    : cleanContent;
                  const isRunningState = msg.state === "streaming" || isMsgTypewriting;

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
                          className={isUser ? "max-w-[88%] items-end" : "max-w-[96%]"}
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
                            <AssistantTraceView
                              msg={msg}
                              isRunningState={isRunningState}
                            />
                          )}

                          {/* Message Content Surface */}
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
                                isSelected={activeSelectedDraftId === msg.sql_draft.id}
                                onSelectDraft={onSelectDraft}
                              />
                            )}

                          {msg.state === "failed" && (
                            <MessageFooter>
                              <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                className="h-6 px-2 text-xs"
                                onClick={onRetryFailed}
                                data-testid={`assistant-retry-message-${msg.id}`}
                              >
                                <RotateCcw data-icon="inline-start" aria-hidden="true" />
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
                      <Message align="start" data-testid="assistant-streaming-turn">
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

                          <AssistantLiveTraceView
                            capability={pendingUserPayload.capability_hint ?? activeIntent}
                            liveAgentStep={liveAgentStep}
                            liveReasoningDone={liveReasoningDone}
                            liveReasoningText={liveReasoningText}
                            liveValidatorStatus={liveValidatorStatus}
                          />

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
  );
}
