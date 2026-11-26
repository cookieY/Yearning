import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
  ArrowRight,
  CircleAlert,
  Clock3,
  Ellipsis,
  Hourglass,
  Stamp,
  Terminal,
  TrendingDown,
  TrendingUp,
} from "lucide-react";
import { getSystemHealthDashboard } from "@/api/generated/client/administration/administration";
import { unwrapApi } from "@/shared/api/mutator";
import { FlowType } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { listCurrentUserFlows } from "@/api/generated/client/change-drafts/change-drafts";
import { useSession } from "@/features/auth/session-provider";
import { OrderStateBadge } from "@/features/orders/order-state-badge";
import { useMyChangeOrders } from "@/features/orders/use-orders";
import { CountUp } from "@/shared/components/count-up";
import { buttonVariants } from "@/shared/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/shared/components/ui/empty";
import { Skeleton } from "@/shared/components/ui/skeleton";
import { cn } from "@/shared/lib/utils";
import { formatCompactCount } from "@/shared/lib/format";
import { useCurrentAnnouncementQuery } from "@/features/announcements/use-current-announcement";
import {
  useMyDashboardQuery,
  useOperationsDashboardQuery,
} from "@/features/dashboard/use-dashboard-queries";
import {
  buildBreakdown,
  durationFormatter,
  formatPercent,
  stageRatio,
  stateChip,
  stateDot,
  weekOverWeek,
} from "./workspace-home-metrics";
import {
  AnnouncementPanel,
  KpiCard,
  MiniBars,
  MiniStat,
  Panel,
} from "./workspace-home-panels";

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
