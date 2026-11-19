import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { ChangeOrder, ChangeOrderTimelineEntry } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useCreateOrderComment, useOrderComments } from "@/features/orders/use-approvals";
import { useChangeOrderTimeline } from "@/features/orders/use-orders";
import {
  ORDER_STATES,
  orderStateTone,
  stageStateTone,
  type OrderStateTone,
} from "@/features/orders/order-state";
import { StateToneChip } from "@/features/orders/order-state-badge";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Avatar, AvatarFallback } from "@/shared/components/ui/avatar";
import { Badge } from "@/shared/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/shared/components/ui/empty";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/shared/components/ui/input-group";
import {
  Message,
  MessageContent,
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
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Spinner } from "@/shared/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { formatTimestamp } from "@/shared/lib/timezones";
import { cn } from "@/shared/lib/utils";
import {
  ArrowDown,
  ArrowUp,
  CalendarCheck,
  CalendarClock,
  Fingerprint,
  Flag,
  Hash,
  MessageCircleDashed,
  ShieldCheck,
  User,
  UserCheck,
  Wrench,
} from "lucide-react";

/**
 * Order activity card: the timeline, the comments and the frozen snapshot of
 * one order in a single card, switched by a segmented tab bar. Layout after the
 * "Upcoming Schedules" card the owner picked as reference (Shadcn Dashboard
 * Card 33 is a PRO block, so it is rebuilt from the free primitives): title
 * and subtitle, a full-width tab track whose active pill is bordered, then
 * rows of an icon tile + muted label over a bold value, with a badge on the
 * right. All three panels stay mounted — a half-written comment survives a
 * tab switch and every query is warm before its tab is opened.
 *
 * owner page feedback 2026-11-04 (fixed 380px): all three tab panels share a fixed
 * height of 380px — timeline and frozen snapshot use ScrollArea (viewportClassName sets
 * h-[380px], see scroll-area.tsx note) so short content fills the box and overflow scrolls internally;
 * comments is likewise a fixed 380px panel (message-scroller keeps the official kit's own scrolling,
 * see its file header comment).
 */

export { formatTimestamp };

const STATE_LABEL_ROOTS = ["orders.state", "orders.stageState"] as const;

const TIMELINE_EVENT_KEYS: Record<string, string> = {
  "change_order.submitted": "orders.detail.timelineEvents.orderSubmitted",
  "change_order.stage_ready_for_execution": "orders.detail.timelineEvents.stageReadyForExecution",
  "execution.scheduled": "orders.detail.timelineEvents.executionScheduled",
  "execution.schedule_cancelled": "orders.detail.timelineEvents.executionScheduleCancelled",
  "execution.started": "orders.detail.timelineEvents.executionStarted",
  "execution.succeeded": "orders.detail.timelineEvents.executionSucceeded",
  "execution.failed": "orders.detail.timelineEvents.executionFailed",
  "execution.partial_failed": "orders.detail.timelineEvents.executionPartialFailed",
  "execution.cancelled": "orders.detail.timelineEvents.executionCancelled",
  "execution.result_unknown": "orders.detail.timelineEvents.executionResultUnknown",
  "change_order.rejected": "orders.detail.timelineEvents.orderRejected",
  "change_order.withdrawn": "orders.detail.timelineEvents.orderWithdrawn",
  "change_order.voided": "orders.detail.timelineEvents.orderVoided",
  "workflow.node_invalidated": "orders.detail.timelineEvents.nodeInvalidated",
};

const CJK_SUMMARY_RE = /[\u4e00-\u9fff]/;

function resolveTimelineSummary(
  entry: ChangeOrderTimelineEntry,
  t: (key: string) => string,
): string {
  // Custom localized mock/fixture text stays verbatim; canonical pure-English
  // backend summaries map through the i18n dictionary per event_type.
  if (CJK_SUMMARY_RE.test(entry.summary)) {
    return entry.summary;
  }
  if (entry.event_type === "change_order.approval_decided") {
    const rejected = entry.state === "rejected" || /reject/i.test(entry.summary);
    return t(
      rejected
        ? "orders.detail.timelineEvents.approvalRejected"
        : "orders.detail.timelineEvents.approvalApproved",
    );
  }
  const key = TIMELINE_EVENT_KEYS[entry.event_type];
  return key !== undefined ? t(key) : entry.summary;
}

/** Timeline entry states mix order and stage enums; resolve the localized
 * label from either dictionary and fall back to the raw value. */
function useTimelineStateLabel(): (state: string | null | undefined) => string {
  const { t, i18n } = useTranslation();
  return (state): string => {
    if (state === null || state === undefined) return "";
    for (const root of STATE_LABEL_ROOTS) {
      const key = `${root}.${state}`;
      if (i18n.exists(key)) return t(key);
    }
    return state;
  };
}

function ActorIcon({ kind }: { kind: ChangeOrderTimelineEntry["actor_kind"] }) {
  if (kind === "user") return <User aria-hidden />;
  if (kind === "system") return <ShieldCheck aria-hidden />;
  return <Wrench aria-hidden />;
}

/** One row: bordered icon tile, muted label over a bold value, optional
 * trailing badge and body. */
function ActivityRow({
  icon,
  label,
  value,
  mono = false,
  strong = true,
  aside,
  children,
  ...props
}: {
  icon: ReactNode;
  label: ReactNode;
  value: ReactNode;
  mono?: boolean;
  strong?: boolean;
  aside?: ReactNode;
  children?: ReactNode;
  "data-testid"?: string;
}) {
  return (
    <li className="flex items-start gap-3" {...props}>
      <span className="bg-muted/30 text-muted-foreground flex size-10 shrink-0 items-center justify-center rounded-lg border [&_svg]:size-4">
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <p className="text-muted-foreground text-sm leading-snug">{label}</p>
        <p
          className={cn(
            "text-sm leading-snug whitespace-pre-wrap",
            strong && "font-semibold",
            mono ? "font-mono text-xs break-all" : "break-words",
          )}
        >
          {value}
        </p>
        {children}
      </div>
      {aside}
    </li>
  );
}

const ORDER_STATE_SET: ReadonlySet<string> = new Set(ORDER_STATES);

function resolveTimelineStateTone(state: string): OrderStateTone {
  if (ORDER_STATE_SET.has(state)) {
    return orderStateTone(state as ChangeOrder["state"]);
  }
  return stageStateTone(state as ChangeOrder["stages"][number]["state"]);
}

function TimelinePanel({ orderId }: { orderId: string }) {
  const { t } = useTranslation();
  const stateLabel = useTimelineStateLabel();
  const timelineQuery = useChangeOrderTimeline(orderId, true);
  const entries = timelineQuery.data ?? [];
  const latestStateIndex = entries.findIndex((entry) => entry.state != null);

  return (
    <div data-testid="order-timeline">
      {timelineQuery.isPending ? (
        <LoadingState variant="table" columns={2} />
      ) : timelineQuery.isError ? (
        <ErrorState
          error={timelineQuery.error}
          operationId="listChangeOrderTimeline"
          onRetry={() => void timelineQuery.refetch()}
        />
      ) : entries.length === 0 ? (
        <EmptyState title={t("orders.detail.timelineEmpty")} />
      ) : (
        <ol className="flex flex-col gap-4" data-testid="order-timeline-list">
          {entries.map((entry, index) => (
            <ActivityRow
              key={entry.id}
              icon={<ActorIcon kind={entry.actor_kind} />}
              label={
                <>
                  {formatTimestamp(entry.occurred_at)}
                  {entry.stage_position !== null && (
                    <> · {t("orders.detail.timelineStage", { position: entry.stage_position })}</>
                  )}
                </>
              }
              value={resolveTimelineSummary(entry, t)}
              aside={
                entry.state != null ? (
                  index === latestStateIndex ? (
                    <StateToneChip
                      tone={resolveTimelineStateTone(entry.state)}
                      label={stateLabel(entry.state)}
                      className="shrink-0"
                      data-testid="order-timeline-latest-state"
                    />
                  ) : (
                    <Badge
                      variant="outline"
                      className="text-muted-foreground shrink-0 border-dashed bg-transparent font-normal"
                      data-testid="order-timeline-historical-state"
                    >
                      → {stateLabel(entry.state)}
                    </Badge>
                  )
                ) : undefined
              }
            >
              {entry.comment != null && entry.comment !== "" && (
                <p
                  className="text-foreground/80 mt-1 text-xs break-words whitespace-pre-wrap"
                  data-testid="order-timeline-comment"
                >
                  {entry.comment}
                </p>
              )}
            </ActivityRow>
          ))}
        </ol>
      )}
    </div>
  );
}

/** First grapheme of a display name for the message avatar tile.
 * Intl.Segmenter keeps surrogate pairs and combining marks (CJK extension
 * characters, emoji) intact where spread/split would cut UTF-16 units. */
function avatarInitial(name: string): string {
  const trimmed = name.trim();
  if (trimmed === "") return "?";
  const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
  const first = segmenter.segment(trimmed)[Symbol.iterator]().next().value;
  return (first?.segment ?? "?").toUpperCase();
}

/** Order comments (S004 permanent retention). Append-only: no edit or delete
 * affordance exists anywhere on the contract surface. Errors render inline
 * through the operation's own error profile — never a fake success.
 *
 * owner ruling 2026-11-04: comments panel uses the official message / message-scroller components —
 * each comment is a message (avatar + byline + bubble), the list scrolls internally inside message-scroller
 * with a capped height (max-h-80), auto-follows the latest comment, and shows "jump to latest" when scrolled up;
 * the empty state also rendered as a quiet message bubble instead of a large EmptyState.
 *
 * Comment composer aligned on the same day per owner page feedback with the official composer pattern: InputGroup +
 * InputGroupTextarea, with an icon send button at the bottom-right block-end addon (Enter sends,
 * Shift+Enter inserts a newline, IME composition does not trigger), replacing the old Textarea + button row.
 *
 * Round 2 feedback (same day) aligned with the official demo screenshot: list and composer merge into a single
 * fixed-height (380px, changed from h-140 on 2026-11-04 to unify all three tabs) rounded panel,
 * with the composer docked at the bottom; empty state switched to a centered Empty
 * (icon tile + title + description), and the send button is a solid circular icon.
 * Round 3 feedback (same day): avatar switched to the site-wide Avatar component (initials fallback);
 * each message enters with a fade+slide animation; the experimental "+" insert menu was removed per owner request. */
function CommentsPanel({ orderId }: { orderId: string }) {
  const { t } = useTranslation();
  const session = useSession();
  const commentsQuery = useOrderComments(orderId, true);
  const createComment = useCreateOrderComment(orderId);
  const [content, setContent] = useState("");
  const [error, setError] = useState<string | null>(null);

  const submit = (): void => {
    // Enter-to-send reaches this path even when the button is disabled, so the
    // guard lives here and covers both entry points.
    if (content.trim() === "" || createComment.isPending) return;
    createComment.mutate(content, {
      onSuccess: () => {
        setContent("");
        setError(null);
        appToast.success(t("orders.comments.submit"));
      },
      onError: (mutationError) => {
        const display = describeError(mutationError, "createChangeOrderComment");
        const errText = `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
        setError(errText);
        appToast.error(errText);
      },
    });
  };

  const comments = commentsQuery.data ?? [];
  // The read model is newest-first (comments application cursor read); the
  // message flow reads oldest → newest.
  const ordered = [...comments].reverse();

  return (
    <div data-testid="order-comments">
      <MessageScrollerProvider autoScroll defaultScrollPosition="last-anchor">
        <div className="bg-card flex h-[380px] flex-col overflow-hidden rounded-xl border border-border">
          <MessageScroller className="min-h-0 flex-1">
            <MessageScrollerViewport>
              <MessageScrollerContent
                className={cn(
                  "gap-4 px-(--card-spacing) pt-(--card-spacing) pb-1",
                  comments.length === 0 && "justify-center",
                )}
                data-testid="order-comments-list"
              >
                {commentsQuery.isPending ? (
                  <LoadingState variant="inline" />
                ) : commentsQuery.isError ? (
                  <ErrorState
                    error={commentsQuery.error}
                    operationId="listChangeOrderComments"
                    onRetry={() => void commentsQuery.refetch()}
                  />
                ) : comments.length === 0 ? (
                  <Empty data-testid="order-comments-empty">
                    <EmptyHeader>
                      <EmptyMedia variant="icon">
                        <MessageCircleDashed aria-hidden />
                      </EmptyMedia>
                      <EmptyTitle>{t("orders.detail.commentsEmpty")}</EmptyTitle>
                      <EmptyDescription>{t("orders.detail.commentsEmptyHint")}</EmptyDescription>
                    </EmptyHeader>
                  </Empty>
                ) : (
                  ordered.map((comment) => {
                    // Chat layout (owner page feedback 2026-11-04): other
                    // people's comments sit left in the neutral bubble, the
                    // author's own sit right in the --comment-own blue.
                    const own = comment.author_user_id === session.user?.id;
                    return (
                      <MessageScrollerItem
                        key={comment.id}
                        messageId={comment.id}
                        className="[content-visibility:visible]"
                      >
                        <Message
                          align={own ? "end" : "start"}
                          data-testid="order-comment-item"
                        >
                          <Avatar className="self-start">
                            <AvatarFallback className="text-xs font-medium">
                              {avatarInitial(comment.author_display_name)}
                            </AvatarFallback>
                          </Avatar>
                          <MessageContent>
                            <MessageHeader className="gap-2 px-0">
                              <span className="font-medium text-foreground">
                                {comment.author_display_name}
                              </span>
                              <span className="text-muted-foreground/70">
                                {formatTimestamp(comment.occurred_at)}
                              </span>
                            </MessageHeader>
                            <p
                              className={cn(
                                "w-fit max-w-full rounded-lg px-3 py-2 text-xs break-words whitespace-pre-wrap",
                                own ? "self-end bg-comment-own text-white" : "self-start bg-muted",
                              )}
                            >
                              {comment.content}
                            </p>
                          </MessageContent>
                        </Message>
                      </MessageScrollerItem>
                    );
                  })
                )}
              </MessageScrollerContent>
            </MessageScrollerViewport>
            <MessageScrollerButton direction="end">
              <ArrowDown className="size-4" aria-hidden="true" />
              <span className="sr-only">{t("orders.detail.commentsScrollEnd")}</span>
            </MessageScrollerButton>
          </MessageScroller>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              submit();
            }}
            className="flex flex-col gap-2 p-(--card-spacing) pt-0"
          >
            <InputGroup>
              <InputGroupTextarea
                value={content}
                onChange={(event) => { setContent(event.target.value); }}
                onKeyDown={(event) => {
                  // Chat-style: Enter sends, Shift+Enter breaks a line; a pending
                  // IME composition must not fire the submit.
                  if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                    event.preventDefault();
                    submit();
                  }
                }}
                maxLength={4096}
                placeholder={t("orders.detail.commentPlaceholder")}
                aria-label={t("orders.detail.commentPlaceholder")}
                className="min-h-14 max-h-40 overflow-y-auto"
                data-testid="order-comment-input"
              />
              <InputGroupAddon align="block-end" className="pt-1">
                <InputGroupButton
                  type="submit"
                  variant="default"
                  size="icon-sm"
                  disabled={content.trim() === "" || createComment.isPending}
                  aria-busy={createComment.isPending || undefined}
                  className="ml-auto rounded-md"
                  data-testid="order-comment-submit"
                >
                  {createComment.isPending ? <Spinner aria-hidden /> : <ArrowUp aria-hidden />}
                  <span className="sr-only">{t("orders.detail.commentSubmit")}</span>
                </InputGroupButton>
              </InputGroupAddon>
            </InputGroup>
            {error !== null && (
              <p role="alert" className="text-destructive text-xs" data-testid="order-comment-error">
                {error}
              </p>
            )}
          </form>
        </div>
      </MessageScrollerProvider>
    </div>
  );
}

function SnapshotPanel({ order }: { order: ChangeOrder }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-4" data-testid="order-facts">
      <ul className="flex flex-col gap-4">
        <ActivityRow
          icon={<CalendarClock aria-hidden />}
          label={t("orders.detail.submittedAt")}
          value={formatTimestamp(order.submitted_at)}
        />
        {order.requested_scheduled_for != null && (
          <ActivityRow
            icon={<CalendarCheck aria-hidden />}
            label={t("orders.detail.requestedSchedule")}
            value={formatTimestamp(order.requested_scheduled_for)}
            data-testid="order-requested-schedule"
          />
        )}
        <ActivityRow
          icon={<Flag aria-hidden />}
          label={t("orders.detail.terminalAt")}
          value={formatTimestamp(order.terminal_at)}
        />
        <ActivityRow
          icon={<UserCheck aria-hidden />}
          label={t("orders.detail.manuallyVerified")}
          value={order.manually_verified ? t("common.yes") : t("common.no")}
        />
        <ActivityRow icon={<Hash aria-hidden />} label={t("orders.detail.sqlHash")} value={order.sql_hash} mono />
        <ActivityRow
          icon={<Fingerprint aria-hidden />}
          label={t("orders.detail.snapshotHash")}
          value={order.snapshot_hash}
          mono
        />
      </ul>
      <p className="text-muted-foreground text-xs">{t("orders.detail.frozenNote")}</p>
    </div>
  );
}

type ActivityTab = "timeline" | "comments" | "snapshot";
const ACTIVITY_TABS: readonly ActivityTab[] = ["timeline", "comments", "snapshot"];

export function OrderActivityCard({ order }: { order: ChangeOrder }) {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<ActivityTab>("timeline");
  return (
    <Card data-testid="order-activity">
      <CardHeader>
        <CardTitle>{t("orders.detail.activity")}</CardTitle>
        <CardDescription>{t("orders.detail.activityHint")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <Tabs
          value={activeTab}
          onValueChange={(next) => {
            if (ACTIVITY_TABS.includes(next as ActivityTab)) {
              setActiveTab(next as ActivityTab);
            }
          }}
        >
          <TabsList className="bg-muted/60 h-auto w-full gap-0 rounded-lg p-1">
            {ACTIVITY_TABS.map((tab) => (
              <TabsTrigger
                key={tab}
                value={tab}
                className="h-8 flex-1 rounded-md data-active:border-border data-active:bg-background data-active:text-foreground data-active:hover:bg-background data-active:hover:text-foreground dark:data-active:bg-background dark:data-active:text-foreground"
                data-testid={`order-activity-tab-${tab}`}
              >
                {t(
                  tab === "timeline"
                    ? "orders.detail.timeline"
                    : tab === "comments"
                      ? "orders.detail.comments"
                      : "orders.detail.facts",
                )}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        <TransitionPanel activeIndex={ACTIVITY_TABS.indexOf(activeTab)} keepMounted>
          <div className="text-sm">
            <ScrollArea viewportClassName="h-[380px]" data-testid="order-timeline-scroll">
              <TimelinePanel orderId={order.id} />
            </ScrollArea>
          </div>
          <div className="text-sm">
            <CommentsPanel orderId={order.id} />
          </div>
          <div className="text-sm">
            <ScrollArea viewportClassName="h-[380px]" data-testid="order-facts-scroll">
              <SnapshotPanel order={order} />
            </ScrollArea>
          </div>
        </TransitionPanel>
      </CardContent>
    </Card>
  );
}
