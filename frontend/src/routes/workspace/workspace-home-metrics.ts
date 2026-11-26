import { ChangeOrderState } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type {
  ChangeOrder,
  ChangeOrderStateDistribution,
  DailyMetricPoint,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  ORDER_STATE_TONE_CLASS,
  ORDER_STATE_TONE_DOT,
  orderStateTone,
} from "@/features/orders/order-state";

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
export function durationFormatter(target: number | undefined): (value: number) => string {
  if (target === undefined) return DURATION_FORMATTERS.missing;
  if (target < 1000) return DURATION_FORMATTERS.ms;
  if (target < 60_000) return DURATION_FORMATTERS.seconds;
  return DURATION_FORMATTERS.minutes;
}

/** Module-level so CountUp's effect dependency stays stable across renders. */
export function formatPercent(percent: number): string {
  return `${String(percent)}%`;
}

/** Recent 7 days against the 7 days before them. The server's `order_trend`
 * is `group by day`, so days with zero submissions are **absent**, not zero —
 * slicing the last 14 array entries would silently compare 14 orders-days
 * against a different span. Rebuild the calendar first (missing day = 0),
 * then take the two weeks. A window without a predecessor still renders
 * nothing instead of +0%. */
const DAY_MS = 24 * 60 * 60 * 1000;

export function weekOverWeek(points: DailyMetricPoint[]): number | null {
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
export function stageRatio(order: ChangeOrder): number {
  const total = order.stages.length;
  if (total === 0) return 0;
  return Math.min(1, (order.current_stage_position ?? 1) / total);
}

/** The home reuses the shared order-state tone vocabulary, so a state has
 * one colour across every surface (badge, strip dot, legend) and that colour
 * is theme-specific — never a second local map. */
export const stateDot = (state: ChangeOrder["state"]) => ORDER_STATE_TONE_DOT[orderStateTone(state)];
export const stateChip = (state: ChangeOrder["state"]) => ORDER_STATE_TONE_CLASS[orderStateTone(state)];

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

/** Same four tone groups the dot strip uses, so one colour never means two
 * things: finished / in flight / failed / everything else. Counts come from
 * the platform 30-day `state_distribution` — the same window as
 * `change_order_total` and the success rate beside it (RCP-20261023:
 * previously this was aggregated in the browser from the caller's 20 most
 * recent orders, so the bar and the 89% had different denominators). */
export function buildBreakdown(
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
