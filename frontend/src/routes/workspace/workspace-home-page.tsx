import { useId } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
  ArrowRight,
  CircleAlert,
  Clock3,
  Ellipsis,
  Hourglass,
  Megaphone,
  Stamp,
  Terminal,
  TrendingDown,
  TrendingUp,
} from "lucide-react";
import { getSystemHealthDashboard } from "@/api/generated/client/administration/administration";
import { unwrapApi } from "@/shared/api/mutator";
import { FlowType } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { listCurrentUserFlows } from "@/api/generated/client/change-drafts/change-drafts";
import { ChangeOrderState } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type {
  AnnouncementPublication,
  ChangeOrder,
  ChangeOrderStateDistribution,
  DailyMetricPoint,
  SystemHealthDashboard,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { OrderStateBadge } from "@/features/orders/order-state-badge";
import {
  ORDER_STATE_TONE_CLASS,
  ORDER_STATE_TONE_DOT,
  orderStateTone,
} from "@/features/orders/order-state";
import { useMyChangeOrders } from "@/features/orders/use-orders";
import { CountUp } from "@/shared/components/count-up";
import { Badge } from "@/shared/components/ui/badge";
import { buttonVariants } from "@/shared/components/ui/button";
import { Card } from "@/shared/components/ui/card";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/shared/components/ui/empty";
import { Skeleton } from "@/shared/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/shared/components/ui/tooltip";
import { ANNOUNCEMENT_PROSE } from "@/shared/lib/announcement-prose";
import { formatTimestamp } from "@/shared/lib/timezones";
import { cn } from "@/shared/lib/utils";
import { formatCompactCount } from "@/shared/lib/format";
import { useCurrentAnnouncementQuery } from "@/features/announcements/use-current-announcement";
import {
  useMyDashboardQuery,
  useOperationsDashboardQuery,
} from "@/features/dashboard/use-dashboard-queries";

/**
 * Business home (dashboard PRD §2/§9). Replaces the reference-derived block
 * layout the owner reviewed as /workspace/preview (owner ruling 2026-10-23:
 * "promote the new home page to replace the old one"): the geometry follows the owner's
 * reference screenshot 1:1 — row 1 three actionable counters, left 4/12
 * "Order Overview" over "Recent Orders", right 8/12 "System Announcement" beside the completion ring
 * over "Execution Success Rate" with the state strip.
 *
 * Every number comes from an endpoint this instance already serves — no new
 * endpoint and no fabricated metric (dashboard PRD §6): missing data renders
 * an em dash instead of a made-up value. The first-login zero-permission
 * contract (auth PRD §11) still renders its notice alongside the dashboard,
 * never instead of it. Colour comes from the brand-adjacent chart tokens in
 * global.css (--chart-brand / -soft / -sky) plus the per-state tone tokens.
 */

const DASHBOARD_WINDOW_DAYS = 30;
const RECENT_ORDER_LIMIT = 20;
const RECENT_ROW_COUNT = 3;

function useSystemHealthQuery(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "dashboard", "system-health"],
    queryFn: () => unwrapApi(getSystemHealthDashboard()),
    enabled,
    staleTime: 60_000,
    // §18.83 (owner 2026-10-23): the health read surface is open to every
    // signed-in session, so plain users see the same dots as admins. `retry:
    // false` still keeps a transient failure from becoming a visible error.
    retry: false,
  });
}

/** Grant data for the first-login zero-permission notice: the page never
 * guesses permissions client-side (auth PRD §11), it asks the two flow lists. */
function useCurrentUserFlows(flowType: FlowType) {
  return useQuery({
    queryKey: ["auth", "flows", flowType],
    queryFn: () => unwrapApi(listCurrentUserFlows({ flow_type: flowType })),
    retry: false,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
}

/** One formatter per unit, hoisted to module scope: CountUp takes `format` as
 * an effect dependency, so handing it a fresh closure on every render would
 * restart the tick. */
const DURATION_FORMATTERS = {
  missing: (): string => "—",
  ms: (value: number): string => `${String(Math.round(value))}ms`,
  seconds: (value: number): string => `${String(Math.round(value / 1000))}s`,
  minutes: (value: number): string => {
    // CountUp never feeds a negative value (its progress is clamped), but a
    // formatter that could print "-1m -4s" is a trap for the next caller.
    const seconds = Math.max(0, Math.round(value / 1000));
    const whole = Math.floor(seconds / 60);
    const rest = seconds % 60;
    // Mid-tween the elapsed seconds are still under a minute; "0m 5s" would
    // read like a formatting bug, so the minute unit only appears once it
    // actually carries a minute — the same shape the settled value has.
    if (whole === 0) return `${String(rest)}s`;
    return rest === 0 ? `${String(whole)}m` : `${String(whole)}m ${String(rest)}s`;
  },
} as const;

/** Formats an in-flight tick in the unit the settled value will use. Without
 * this a latency would blur "4ms → 300ms → 2s → 18s" and read as four
 * different units in half a second; pinning the unit keeps the digits the only
 * thing that moves. */
function durationFormatter(target: number | undefined): (value: number) => string {
  if (target === undefined) return DURATION_FORMATTERS.missing;
  if (target < 1000) return DURATION_FORMATTERS.ms;
  if (target < 60_000) return DURATION_FORMATTERS.seconds;
  return DURATION_FORMATTERS.minutes;
}

/** Module-level so CountUp's effect dependency stays stable across renders. */
function formatPercent(percent: number): string {
  return `${String(percent)}%`;
}

/** Recent 7 days against the 7 days before them. The server's `order_trend`
 * is `group by day`, so days with zero submissions are **absent**, not zero —
 * slicing the last 14 array entries would silently compare 14 orders-days
 * against a different span. Rebuild the calendar first (missing day = 0),
 * then take the two weeks. A window without a predecessor still renders
 * nothing instead of +0%. */
const DAY_MS = 24 * 60 * 60 * 1000;

function weekOverWeek(points: DailyMetricPoint[]): number | null {
  if (points.length === 0) return null;
  const byDay = new Map<string, number>();
  for (const point of points) byDay.set(point.day, point.value);
  const first = points[0];
  if (first === undefined) return null;
  const latest = points.reduce((max, point) => (point.day > max ? point.day : max), first.day);
  const latestMs = Date.parse(`${latest}T00:00:00Z`);
  if (Number.isNaN(latestMs)) return null;
  const dayKey = (offset: number): string =>
    new Date(latestMs - offset * DAY_MS).toISOString().slice(0, 10);
  const sumRange = (from: number, to: number): number => {
    let total = 0;
    for (let offset = from; offset < to; offset += 1) total += byDay.get(dayKey(offset)) ?? 0;
    return total;
  };
  // offsets are days back from the latest point: 0..6 recent, 7..13 prior.
  const prior = sumRange(7, 14);
  if (prior === 0) return null;
  const recent = sumRange(0, 7);
  return Math.round(((recent - prior) / prior) * 100);
}

/** Stage completion of one order: the server freezes the stage list at submit
 * time, so position/total is a real progress reading (§18.56). */
function stageRatio(order: ChangeOrder): number {
  const total = order.stages.length;
  if (total === 0) return 0;
  return Math.min(1, (order.current_stage_position ?? 1) / total);
}

/** The home reuses the shared order-state tone vocabulary, so a state has
 * one colour across every surface (badge, strip dot, legend) and that colour
 * is theme-specific — never a second local map. */
const stateDot = (state: ChangeOrder["state"]) => ORDER_STATE_TONE_DOT[orderStateTone(state)];
const stateChip = (state: ChangeOrder["state"]) => ORDER_STATE_TONE_CLASS[orderStateTone(state)];

/** Panel: the reference's card anatomy (20px padding, 12px radius, hairline
 * ring) without the shared card's 16px rhythm, so this page can hold the
 * reference's larger type scale without changing every other screen. */
function Panel({ className, ...props }: React.ComponentProps<typeof Card>) {
  return (
    <Card
      className={cn("gap-0 rounded-xl p-5 [--card-spacing:0px]", className)}
      {...props}
    />
  );
}

/** Product-insight glyph: violet bars rising from the baseline with a 12ms
 * stagger (chart entrance vocabulary, global.css --animate-bar-grow).
 *
 * 900ms per bar + 13 x 12ms stagger = ~1.06s for the whole glyph. The 400ms
 * --animate-bar-rise budget is right for the 20-dot state strip (a decoration
 * that should get out of the way) but reads as a single pop on a histogram
 * whose whole job is to be read: owner 2026-10-23, "histogram chart animation is too fast". */
function MiniBars({ points }: { points: DailyMetricPoint[] }) {
  const values = points.slice(-14);
  const max = Math.max(...values.map((point) => point.value), 1);
  return (
    <div
      className="flex h-14 w-24 items-end gap-[3px]"
      data-testid="home-mini-bars"
      aria-hidden
    >
      {values.map((point, index) => (
        <span
          key={point.day}
          className="bg-chart-brand min-h-px flex-1 origin-bottom rounded-[2px] opacity-80 motion-safe:animate-bar-grow"
          style={{
            height: `${Math.max(4, (point.value / max) * 100).toFixed(2)}%`,
            opacity: (0.35 + (point.value / max) * 0.65).toFixed(2),
            animationDelay: `${String(index * 12)}ms`,
          }}
        />
      ))}
    </div>
  );
}

/** Completion ring. `pathLength={100}` lets the draw keyframes speak in
 * percent instead of arc length, and the stroke is a brand gradient so the
 * page is not monochrome. */
function CompletionRing({
  completed,
  total,
}: {
  completed: number | undefined;
  total: number | undefined;
}) {
  const { t } = useTranslation();
  const gradientId = `ring-${useId().replace(/:/g, "")}`;
  // `undefined` means the dashboard query has not resolved (or failed). Drawing
  // 0/0 there would claim "nothing is done", which is a different statement from
  // "not loaded yet" — the KPI cards already show a skeleton in that state, so
  // the ring must not assert a number it does not have.
  const known = completed !== undefined && total !== undefined;
  const percent = !known || total === 0 ? 0 : Math.round((completed / total) * 100);
  return (
    <div className="relative flex items-center justify-center">
      <svg
        viewBox="0 0 100 100"
        className="size-36"
        role="img"
        aria-label={
          known
            ? t("home.metrics.completionAria", { percent })
            : t("home.metrics.completionAriaUnknown")
        }
        data-testid="home-completion-ring"
      >
        <defs>
          <linearGradient id={gradientId} x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stopColor="var(--chart-brand-soft)" />
            <stop offset="100%" stopColor="var(--chart-brand)" />
          </linearGradient>
        </defs>
        <circle cx="50" cy="50" r="42" fill="none" stroke="var(--border)" strokeWidth="9" />
        {/* Unknown state draws no arc at all: a 0% arc would be a claim, an
            absent arc reads as "not measured yet" (review L4). */}
        {known && (
          <circle
            cx="50"
            cy="50"
            r="42"
            fill="none"
            stroke={`url(#${gradientId})`}
            strokeWidth="9"
            strokeLinecap="round"
            pathLength={100}
            strokeDasharray="100"
            strokeDashoffset={100 - percent}
            transform="rotate(-90 50 50)"
            className="motion-safe:animate-ring-draw"
            style={{ "--ring-offset": String(100 - percent) } as React.CSSProperties}
          />
        )}
      </svg>
      <div className="absolute flex flex-col items-center gap-0.5">
        <span className="text-2xl font-semibold tabular-nums">
          <CountUp value={completed} format={formatCompactCount} /> /{" "}
          <CountUp value={total} format={formatCompactCount} />
        </span>
        <span className="text-muted-foreground text-[13px]">{t("home.metrics.done")}</span>
      </div>
    </div>
  );
}

/** Reference KPI card: icon chip beside the oversized number, label under it,
 * meta line last (our meta line is the action link — the counts carry no
 * history, and inventing a delta would break the no-fabrication rule). */
function KpiCard({
  icon,
  label,
  value,
  to,
  testId,
  tone,
}: {
  icon: React.ReactNode;
  label: string;
  value: number | undefined;
  to: string;
  testId: string;
  tone: string;
}) {
  const { t } = useTranslation();
  return (
    <Panel className="flex flex-col gap-2.5">
      <div className="flex items-center gap-3">
        <span
          className={cn("flex size-9 items-center justify-center rounded-lg", tone)}
          aria-hidden
        >
          {icon}
        </span>
        {value === undefined ? (
          <Skeleton className="h-8 w-12" aria-hidden />
        ) : (
          <CountUp
            value={value}
            format={formatCompactCount}
            className="text-[2rem] leading-none font-semibold tabular-nums"
            testId={testId}
          />
        )}
      </div>
      <div className="flex flex-col gap-1.5">
        <p className="text-base font-semibold">{label}</p>
        <Link
          to={to}
          data-testid={`${testId}-link`}
          className="text-muted-foreground hover:text-foreground inline-flex w-fit items-center gap-1 text-[13px]"
        >
          {t("home.kpi.action")}
          <ArrowRight className="size-3" aria-hidden />
        </Link>
      </div>
    </Panel>
  );
}

/** Compact label-over-value stat for the rate panel's secondary numbers. */
function MiniStat({
  label,
  value,
  format,
}: {
  label: string;
  value: number | undefined;
  format: (value: number) => string;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="text-muted-foreground truncate text-[12px]">{label}</span>
      <CountUp
        value={value}
        format={format}
        className="truncate text-lg font-semibold tabular-nums"
      />
    </div>
  );
}

/** Announcement slot (owner ruling 2026-10-23: "this slot should display the system announcement"). It keeps
 * the reference card's anatomy — a header block, a divided body, and the
 * nested ring panel on the right — but the header now speaks for the notice
 * instead of an operator identity, and the body is the server-sanitized
 * markdown render. Long notices scroll inside the card so the grid row height
 * stays tied to the left column rather than to the announcement length. */
function AnnouncementPanel({
  publication,
  health,
  completed,
  orderTotal,
  className,
}: {
  publication: AnnouncementPublication | null;
  health: SystemHealthDashboard | undefined;
  completed: number | undefined;
  orderTotal: number | undefined;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <Panel
      className={cn("flex min-w-0 flex-col", className)}
      data-testid="home-announcement"
    >
      <div className="grid min-h-0 flex-1 gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,16rem)]">
        <div className="flex min-h-0 flex-col">
          <div className="flex items-start gap-3">
            <span
              className="bg-chart-brand/15 text-chart-brand flex size-11 shrink-0 items-center justify-center rounded-full"
              aria-hidden
            >
              <Megaphone className="size-5" />
            </span>
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <div className="flex items-center justify-between gap-3">
                <span className="text-muted-foreground text-[11px] font-medium tracking-wider uppercase">
                  {t("home.announcement.kicker")}
                </span>
                <HealthDots health={health} />
              </div>
              <div className="flex min-w-0 items-center gap-2">
                <span
                  className="truncate text-lg font-semibold"
                  data-testid="home-announcement-title"
                >
                  {publication === null
                    ? t("home.announcement.emptyTitle")
                    : publication.revision.title}
                </span>
                {publication !== null && (
                  <Badge variant="outline" className="text-muted-foreground shrink-0 font-mono">
                    {t("home.announcement.version", { version: publication.version })}
                  </Badge>
                )}
              </div>
              <span className="text-muted-foreground truncate text-[13px]">
                {publication === null
                  ? t("home.announcement.emptyMeta")
                  : t("home.announcement.meta", {
                      time: formatTimestamp(publication.published_at),
                      user: publication.published_by_username,
                    })}
              </span>
            </div>
          </div>

          <div className="border-border/70 my-4 border-t" />

          {publication === null ? (
            <div
              className="text-muted-foreground flex h-44 items-center gap-2 text-sm"
              data-testid="home-announcement-empty"
            >
              <Ellipsis className="size-4" aria-hidden />
              {t("home.announcement.empty")}
            </div>
          ) : (
            <ScrollArea className="h-44" viewportClassName="h-44 pr-3">
              <div
                className={cn("pr-1", ANNOUNCEMENT_PROSE)}
                data-testid="home-announcement-body"
                // Server-sanitized HTML (sanitizer_policy_version recorded on the
                // revision); the client renders, it does not re-sanitize.
                dangerouslySetInnerHTML={{ __html: publication.revision.sanitized_html }}
              />
            </ScrollArea>
          )}
        </div>

        {/* No border and no padding: the ring column sits on the card surface
            itself so the announcement and its completion figure read as one
            block (owner request 2026-10-23). */}
        <div className="flex flex-col justify-between gap-4 py-1">
          <span className="text-lg font-semibold">{t("home.metrics.completion")}</span>
          <CompletionRing completed={completed} total={orderTotal} />
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground text-[13px]">
              {t("home.metrics.planCompleted")}
            </span>
            <CountUp
              value={
                orderTotal === undefined || completed === undefined || orderTotal === 0
                  ? undefined
                  : Math.round((completed / orderTotal) * 100)
              }
              format={formatPercent}
              className="text-xl font-semibold tabular-nums"
            />
          </div>
        </div>
      </div>
    </Panel>
  );
}

function HealthDots({ health }: { health: SystemHealthDashboard | undefined }) {
  const { t } = useTranslation();
  const components = health?.components ?? [];
  if (components.length === 0) return null;
  const dotTone = (status: string) =>
    status === "healthy"
      ? "bg-success"
      : status === "unavailable"
        ? "bg-destructive"
        : "bg-muted-foreground/50";
  const componentLabel = (name: string) =>
    t(`home.metrics.healthComponent.${name}`, { defaultValue: name });
  const statusLabel = (status: string) =>
    t(`home.metrics.healthStatus.${status}`, { defaultValue: status });

  return (
    <TooltipProvider delay={100}>
      <Tooltip>
        <TooltipTrigger
          render={
            <div
              className="flex cursor-default items-center gap-2"
              data-testid="home-health-dots"
              aria-label={t("home.metrics.health")}
            />
          }
        >
          <span className="text-muted-foreground text-[13px]">{t("home.metrics.health")}</span>
          <div className="flex items-center gap-1.5">
            {components.map((component) => (
              <span
                key={component.component}
                className={cn("size-2 rounded-full", dotTone(component.status))}
                title={`${componentLabel(component.component)}: ${statusLabel(component.status)}${component.failure_name ? ` (${component.failure_name})` : ""}`}
              />
            ))}
          </div>
        </TooltipTrigger>
        <TooltipContent
          side="bottom"
          align="end"
          className="flex-col items-stretch gap-1.5 py-2"
          data-testid="home-health-tooltip"
        >
          {components.map((component) => (
            <div key={component.component} className="flex items-center justify-between gap-4">
              <span className="inline-flex items-center gap-1.5">
                <span className={cn("size-2 shrink-0 rounded-full", dotTone(component.status))} aria-hidden />
                <span>{componentLabel(component.component)}</span>
              </span>
              <span className="font-mono text-[11px] opacity-85">
                {statusLabel(component.status)}
                {component.failure_name ? ` · ${component.failure_name}` : ""}
              </span>
            </div>
          ))}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

/** Same four tone groups the dot strip uses, so one colour never means two
 * things: finished / in flight / failed / everything else. Counts come from
 * the platform 30-day `state_distribution` — the same window as
 * `change_order_total` and the success rate beside it (RCP-20261023:
 * previously this was aggregated in the browser from the caller's 20 most
 * recent orders, so the bar and the 89% had different denominators). */
function buildBreakdown(
  distribution: ChangeOrderStateDistribution | undefined,
  t: (key: string) => string,
): { key: string; label: string; tone: string; count: number | undefined; share: number }[] {
  const groups = [
    { key: "done", label: t("home.distribution.breakdownDone"), tone: "bg-tone-emerald", match: (s: ChangeOrderState) => s === "completed" },
    {
      key: "active",
      label: t("home.distribution.breakdownActive"),
      tone: "bg-tone-indigo",
      match: (s: ChangeOrderState) =>
        s === "stage_approval_active" ||
        s === "stage_execution_pending" ||
        s === "scheduled" ||
        s === "running",
    },
    {
      key: "failed",
      label: t("home.distribution.breakdownFailed"),
      tone: "bg-tone-red",
      match: (s: ChangeOrderState) =>
        s === "failed" || s === "partial_failed" || s === "rejected",
    },
    {
      key: "other",
      label: t("home.distribution.breakdownOther"),
      tone: "bg-tone-stone",
      match: () => true,
      remainder: true,
    },
  ];
  const stateCount = (state: ChangeOrderState): number => distribution?.[state] ?? 0;
  const counts = groups.slice(0, 3).map((group) => sumStates(stateCount, group.match));
  const total = distribution === undefined ? 0 : sumStates(stateCount, () => true);
  const values = [...counts, Math.max(total - counts.reduce((sum, n) => sum + n, 0), 0)];
  return groups.map((group, index) => ({
    key: group.key,
    label: group.label,
    tone: group.tone,
    // `undefined` (query pending or failed) stays undefined instead of becoming
    // 0: a zero here is a factual claim about the platform window, and the page
    // must not assert one it has not loaded. CountUp renders "—" for undefined.
    count: distribution === undefined ? undefined : (values[index] ?? 0),
    share: total === 0 ? 0 : ((values[index] ?? 0) / total) * 100,
  }));
}

/** The 18 frozen ChangeOrderState values, taken from the generated enum so a
 * contract addition cannot silently drop out of the distribution sums. */
const CHANGE_ORDER_STATES = Object.values(ChangeOrderState);

/** Sum the 18-key distribution over the states a group claims. The four
 * groups partition ChangeOrderState, so the counts add up to the platform
 * window total — the same number as `change_order_total`. */
function sumStates(
  stateCount: (state: ChangeOrderState) => number,
  matches: (state: ChangeOrderState) => boolean,
): number {
  let sum = 0;
  for (const state of CHANGE_ORDER_STATES) {
    if (matches(state)) sum += stateCount(state);
  }
  return sum;
}

export default function WorkspaceHomePage() {
  const { t } = useTranslation();
  const { user } = useSession();

  const dashboard = useMyDashboardQuery(true);
  const operations = useOperationsDashboardQuery(DASHBOARD_WINDOW_DAYS, true);
  const recentOrders = useMyChangeOrders(true, {}, { limit: RECENT_ORDER_LIMIT });
  const health = useSystemHealthQuery(true);

  const reviewFlows = useCurrentUserFlows(FlowType.change_review);
  const queryFlows = useCurrentUserFlows(FlowType.query_access);
  const announcement = useCurrentAnnouncementQuery(true);

  const hasFlowGrants =
    (reviewFlows.data?.items.length ?? 0) > 0 || (queryFlows.data?.items.length ?? 0) > 0;
  const isZeroPermission =
    user !== null &&
    !user.can_access_admin &&
    reviewFlows.isSuccess &&
    queryFlows.isSuccess &&
    !hasFlowGrants;

  const me = dashboard.data;
  const ops = operations.data;

  const completed = ops?.completed_total ?? 0;
  const failed = ops?.failed_total ?? 0;
  const partial = ops?.partial_failed_total ?? 0;
  const executed = completed + failed + partial;
  const successRate = executed === 0 ? null : Math.round((completed / executed) * 100);
  const orderTotal = ops?.change_order_total ?? 0;
  const ddl = ops?.ddl_statement_total ?? 0;
  const dml = ops?.dml_statement_total ?? 0;
  const ddlRatio = ddl + dml === 0 ? 0 : Math.round((ddl / (ddl + dml)) * 100);
  const delta = ops === undefined ? null : weekOverWeek(ops.order_trend);
  const orders = recentOrders.data?.items ?? [];
  const recentRows = orders.slice(0, RECENT_ROW_COUNT);

  const breakdown = buildBreakdown(ops?.state_distribution, t);
  return (
    <div className="flex flex-col gap-5" data-testid="workspace-page">

      {isZeroPermission && (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <Hourglass />
            </EmptyMedia>
            <EmptyTitle>{t("states.waitingForAdminTitle")}</EmptyTitle>
            <EmptyDescription>{t("states.waitingForAdminDesc")}</EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}

      {/* Row 1 — actionable counters (reference: 42 / 8 / 27). */}
      <div className="grid gap-5 sm:grid-cols-3">
        <KpiCard
          icon={<Stamp className="size-4" />}
          label={t("home.kpi.pendingApproval")}
          value={me?.pending_approval_count}
          to="/approvals/changes"
          testId="home-kpi-pending-approval"
          tone="bg-tone-violet/12 text-tone-violet"
        />
        <KpiCard
          icon={<Clock3 className="size-4" />}
          label={t("home.kpi.pendingExecution")}
          value={me?.pending_execution_count}
          to="/changes/mine?scope=execution"
          testId="home-kpi-pending-execution"
          tone="bg-tone-amber/12 text-tone-amber"
        />
        <KpiCard
          icon={<CircleAlert className="size-4" />}
          label={t("home.kpi.blockedReview")}
          value={me?.blocked_review_count}
          to="/changes/mine"
          testId="home-kpi-blocked-review"
          tone="bg-tone-orange/12 text-tone-orange"
        />
      </div>

      <div className="grid gap-5 xl:grid-cols-12">
          {/* Reference "Product insight": title + timestamp + art slot, then
              two stat blocks that each carry a bar glyph. */}
          <Panel
            className="flex min-w-0 flex-col justify-between gap-5 order-1 xl:col-span-4 xl:col-start-1 xl:row-start-1"
            data-testid="home-order-overview"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="flex flex-col gap-1">
                <h2 className="text-lg font-semibold">{t("home.overview.title")}</h2>
                <p className="text-muted-foreground truncate text-[13px]">
                  {ops === undefined
                    ? ""
                    : t("home.overview.window", {
                        from: ops.meta.window_start,
                        to: ops.meta.window_end,
                        zone: ops.meta.system_timezone,
                      })}
                </p>
              </div>
              {/* The reference's product photo sits here; ours is the brand
                  mark on a tinted tile — same 56px slot, no stock imagery. */}
              <span
                className="bg-chart-brand/12 text-chart-brand flex size-14 shrink-0 items-center justify-center rounded-lg"
                aria-hidden
              >
                <Terminal className="size-6" />
              </span>
            </div>

            <div className="border-border border-t" />

            <div className="flex items-end justify-between gap-3">
              <div className="flex flex-col gap-1">
                <span className="text-muted-foreground text-[13px]">
                  {t("home.overview.orders")}
                </span>
                <div className="flex flex-wrap items-baseline gap-2">
                  <CountUp
                    value={ops === undefined ? undefined : orderTotal}
                    format={formatCompactCount}
                    className="text-3xl font-semibold tabular-nums"
                  />
                  {delta !== null && (
                    // Direction must be visible without reading the sign: a
                    // falling week in success-green with an up arrow reads as
                    // growth at a glance (review F7).
                    <span
                      className={cn(
                        "inline-flex items-center gap-0.5 text-[13px] font-medium",
                        delta < 0
                          ? "text-destructive"
                          : delta === 0
                            ? "text-muted-foreground"
                            : "text-success",
                      )}
                    >
                      {delta < 0 ? (
                        <TrendingDown className="size-3.5" aria-hidden />
                      ) : (
                        <TrendingUp className="size-3.5" aria-hidden />
                      )}
                      {delta > 0 ? `+${String(delta)}%` : `${String(delta)}%`}
                    </span>
                  )}
                </div>
                <span className="text-muted-foreground text-[12px]">
                  {delta === null
                    ? t("home.overview.compareEmpty")
                    : t("home.overview.weekOverWeek")}
                </span>
              </div>
              {ops === undefined ? (
                <Skeleton className="h-14 w-24" aria-hidden />
              ) : (
                <MiniBars points={ops.order_trend} />
              )}
            </div>

            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1">
                <span className="text-muted-foreground text-[13px]">
                  {t("home.overview.statements")}
                </span>
                <CountUp
                  value={ops === undefined ? undefined : ddl + dml}
                  format={formatCompactCount}
                  className="text-3xl font-semibold tabular-nums"
                />
              </div>
              <div className="flex flex-col gap-2">
                <div className="bg-muted flex h-2.5 w-full overflow-hidden rounded-full">
                  <span
                    className="bg-chart-brand h-full rounded-l-full"
                    style={{ width: `${String(ddlRatio)}%` }}
                  />
                  <span
                    className="bg-chart-sky h-full rounded-r-full"
                    style={{ width: `${String(100 - ddlRatio)}%` }}
                  />
                </div>
                <div className="text-muted-foreground flex items-center justify-between text-[13px]">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="bg-chart-brand size-2 rounded-full" aria-hidden />
                    {t("home.overview.ddl")}
                    <CountUp
                      value={ops === undefined ? undefined : ddl}
                      format={formatCompactCount}
                      className="text-foreground tabular-nums"
                    />
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="bg-chart-sky size-2 rounded-full" aria-hidden />
                    {t("home.overview.dml")}
                    <CountUp
                      value={ops === undefined ? undefined : dml}
                      format={formatCompactCount}
                      className="text-foreground tabular-nums"
                    />
                  </span>
                </div>
              </div>
            </div>
          </Panel>

          {/* Reference "Total Earning": headline + comparison, then three rows
              with an app chip, a label pair and a progress bar. */}
          <Panel
            className="flex min-w-0 flex-1 flex-col gap-4 order-3 xl:col-span-4 xl:col-start-1 xl:row-start-2"
            data-testid="home-recent-orders"
          >
            <div className="flex items-start justify-between gap-3">
              <h2 className="text-lg font-semibold">{t("home.recent.title")}</h2>
              <Link
                to="/changes/mine"
                aria-label={t("home.recent.viewAll")}
                className="text-muted-foreground hover:text-foreground -mt-1 rounded-md p-1"
              >
                <Ellipsis className="size-4" aria-hidden />
              </Link>
            </div>

            <div className="flex flex-col gap-1">
              <div className="flex flex-wrap items-baseline gap-2">
                <CountUp
                  value={me === undefined ? undefined : me.submitted_order_count}
                  format={formatCompactCount}
                  className="text-3xl font-semibold tabular-nums"
                  testId="home-recent-in-flight"
                />
              </div>
              <p className="text-muted-foreground text-[13px]">
                {t("home.recent.inFlight")}
              </p>
            </div>

            <ul className="flex flex-col gap-4">
              {recentRows.length === 0 && (
                <li className="text-muted-foreground py-3 text-sm">
                  {t("home.recent.empty")}
                </li>
              )}
              {recentRows.map((order) => (
                <li key={order.id}>
                  <Link
                    to={`/changes/orders/${order.id}`}
                    className="group/row flex items-center gap-3"
                  >
                    <span
                      className={cn(
                        "flex size-10 shrink-0 items-center justify-center rounded-lg",
                        stateChip(order.state),
                      )}
                      aria-hidden
                    >
                      <Terminal className="size-4" />
                    </span>
                    <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span className="group-hover/row:text-foreground truncate text-sm font-medium">
                        {order.title ?? order.display_number}
                      </span>
                      <span className="text-muted-foreground truncate text-[13px]">
                        {order.display_number}
                      </span>
                    </div>
                    <div className="flex w-24 shrink-0 flex-col items-end gap-1.5">
                      <OrderStateBadge state={order.state} />
                      <span className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
                        <span
                          className="bg-chart-brand block h-full rounded-full"
                          style={{ width: `${String(Math.round(stageRatio(order) * 100))}%` }}
                        />
                      </span>
                    </div>
                  </Link>
                </li>
              ))}
            </ul>
          </Panel>
          {/* Owner ruling 2026-10-23: this slot now carries the system
              announcement (markdown, sanitized server-side) instead of the
              operator identity block. The nested ring panel stays, so the
              reference's right-hand stat column survives the swap. */}
          <AnnouncementPanel
            publication={announcement.data ?? null}
            health={health.data}
            completed={ops?.completed_total}
            orderTotal={ops?.change_order_total}
            className="order-2 xl:col-span-8 xl:col-start-5 xl:row-start-1"
          />

          {/* Reference "Sales plan": oversized rate on the left, description
              and the dot strip on the right. The dots are the latest orders
              coloured by state — same strip, real data. */}
          <Panel
            className="grid min-w-0 gap-6 lg:grid-cols-[minmax(0,0.8fr)_minmax(0,1.7fr)] order-4 xl:col-span-8 xl:col-start-5 xl:row-start-2"
            data-testid="home-distribution"
          >
            <div className="flex flex-col gap-2">
              <h2 className="text-lg font-semibold">{t("home.distribution.title")}</h2>
              <div className="flex items-end">
                <CountUp
                  value={successRate === null ? undefined : successRate}
                  format={formatPercent}
                  className="text-[4rem] leading-none font-medium tracking-tight tabular-nums"
                />
              </div>
              <p className="text-muted-foreground text-[13px] leading-relaxed">
                {ops === undefined
                  ? t("home.distribution.captionUnknown")
                  : t("home.distribution.caption", { completed, executed })}
              </p>
              {/* The four operator numbers that used to sit in the announcement
                  slot keep their place in the reference's stat column — here as
                  a compact 2x2 so the rate card has no dead space. */}
              <div className="border-border/70 mt-3 grid grid-cols-2 gap-x-4 gap-y-3 border-t pt-4">
                <MiniStat
                  label={t("home.metrics.approvalP50")}
                  value={ops?.approval_duration_p50_ms}
                  format={durationFormatter(ops?.approval_duration_p50_ms)}
                />
                <MiniStat
                  label={t("home.metrics.approvalP95")}
                  value={ops?.approval_duration_p95_ms}
                  format={durationFormatter(ops?.approval_duration_p95_ms)}
                />
                <MiniStat
                  label={t("home.metrics.queryExecutions")}
                  value={ops?.query_execution_total}
                  format={formatCompactCount}
                />
                <MiniStat
                  label={t("home.metrics.users")}
                  value={ops?.user_total}
                  format={formatCompactCount}
                />
              </div>
            </div>

            <div className="flex flex-col gap-4">
              {/* Place the View-all CTA in the section header so initial scroll y=0 clears the fixed bottom-right Assistant FAB. */}
              <div className="flex items-center justify-between gap-3">
                <h3 className="text-lg font-semibold">{t("home.distribution.legendTitle")}</h3>
                <Link
                  to="/changes/mine"
                  className={cn(buttonVariants({ className: "h-9 shrink-0 px-4" }))}
                >
                  {t("home.distribution.cta")}
                  <ArrowRight className="size-4" aria-hidden />
                </Link>
              </div>
              <p className="text-muted-foreground text-sm leading-relaxed">
                {ops === undefined
                  ? t("home.distribution.descriptionUnknown")
                  : t("home.distribution.description", { count: orderTotal })}
              </p>
              <div className="flex flex-col gap-2">
                <div className="bg-muted flex h-2 w-full gap-0.5 overflow-hidden rounded-full">
                  {breakdown.map((group) => (
                    <span
                      key={group.key}
                      className={cn("h-full first:rounded-l-full last:rounded-r-full", group.tone)}
                      style={{ width: `${String(group.share)}%` }}
                    />
                  ))}
                </div>
                <div
                  className="grid grid-cols-2 gap-x-6 gap-y-2 sm:grid-cols-4"
                  data-testid="home-distribution-breakdown"
                >
                  {breakdown.map((group) => (
                    <div key={group.key} className="flex flex-col gap-0.5">
                      <span className="text-muted-foreground inline-flex items-center gap-1.5 text-[13px]">
                        <span className={cn("size-2 rounded-full", group.tone)} aria-hidden />
                        {group.label}
                      </span>
                      <CountUp
                        value={group.count}
                        className="text-lg font-semibold tabular-nums"
                        testId={`home-distribution-count-${group.key}`}
                      />
                    </div>
                  ))}
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-3">
                {/* The dot strip remains the sequence of the user's own recent orders (not a platform aggregate); explain the sample scope via title
                    to avoid confusion with the platform-scoped four-segment counts above. */}
                <div
                  className="flex min-w-0 flex-1 flex-nowrap items-center gap-1.5 overflow-hidden"
                  aria-label={t("home.distribution.aria")}
                  title={t("home.distribution.dots", { count: orders.length })}
                >
                  {orders.map((order) => (
                    <span
                      key={order.id}
                      className={cn(
                        "size-3 shrink-0 rounded-full motion-safe:animate-bar-rise",
                        stateDot(order.state),
                      )}
                      title={`${order.display_number} · ${order.state}`}
                    />
                  ))}
                </div>
              </div>
            </div>
          </Panel>
      </div>
    </div>
  );
}
