import { useId } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ArrowRight, Ellipsis, Megaphone } from "lucide-react";
import type {
  AnnouncementPublication,
  DailyMetricPoint,
  SystemHealthDashboard,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { CountUp } from "@/shared/components/count-up";
import { Badge } from "@/shared/components/ui/badge";
import { Card } from "@/shared/components/ui/card";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Skeleton } from "@/shared/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/shared/components/ui/tooltip";
import { ANNOUNCEMENT_PROSE } from "@/shared/lib/announcement-prose";
import { formatCompactCount } from "@/shared/lib/format";
import { formatTimestamp } from "@/shared/lib/timezones";
import { cn } from "@/shared/lib/utils";
import { formatPercent } from "./workspace-home-metrics";

/** Panel: the reference's card anatomy (20px padding, 12px radius, hairline
 * ring) without the shared card's 16px rhythm, so this page can hold the
 * reference's larger type scale without changing every other screen. */
export function Panel({ className, ...props }: React.ComponentProps<typeof Card>) {
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
export function MiniBars({ points }: { points: DailyMetricPoint[] }) {
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
export function KpiCard({
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
export function MiniStat({
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

/** Announcement slot (owner ruling 2026-10-23: "this slot should display the system announcement"). It keeps
 * the reference card's anatomy — a header block, a divided body, and the
 * nested ring panel on the right — but the header now speaks for the notice
 * instead of an operator identity, and the body is the server-sanitized
 * markdown render. Long notices scroll inside the card so the grid row height
 * stays tied to the left column rather than to the announcement length. */
export function AnnouncementPanel({
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
