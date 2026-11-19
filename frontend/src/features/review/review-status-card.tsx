import { ReviewUsageContext } from "@/features/review/review-usage-context";
import { StatusBadge } from "@/shared/components/status/status-badge";
import type { StatusTone } from "@/shared/lib/status-tone";
import { useTranslation } from "react-i18next";
import type { ReviewRun } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Badge } from "@/shared/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { Separator } from "@/shared/components/ui/separator";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import {
  CheckCircle2,
  CircleAlert,
  CircleDashed,
  Loader2,
  OctagonAlert,
  TriangleAlert,
} from "lucide-react";
import type { RunPhase } from "./run-state";

/**
 * Review status and progress card (migration contract §4 items 3/4): the
 * observable run state, per-stage results, statement/fingerprint counts.
 * Every phase is expressed with text, icon and semantic color — color alone
 * never carries the state (UI spec §15).
 */

const PHASE_BADGE: Record<RunPhase, { key: string; icon: typeof Loader2; className: string; spin: boolean }> = {
  idle: { key: "precheck.phase.idle", icon: CircleDashed, className: "text-muted-foreground", spin: false },
  queued: { key: "precheck.phase.queued", icon: Loader2, className: "text-muted-foreground", spin: true },
  running: { key: "precheck.phase.running", icon: Loader2, className: "text-primary", spin: true },
  ready: { key: "precheck.phase.ready", icon: CheckCircle2, className: "text-[var(--risk-safe)]", spin: false },
  blocked: { key: "precheck.phase.blocked", icon: OctagonAlert, className: "text-[var(--risk-critical)]", spin: false },
  partial: { key: "precheck.phase.partial", icon: TriangleAlert, className: "text-[var(--risk-high)]", spin: false },
  failed: { key: "precheck.phase.failed", icon: OctagonAlert, className: "text-[var(--exec-failed)]", spin: false },
  outdated: { key: "precheck.phase.outdated", icon: TriangleAlert, className: "text-[var(--exec-partial-failed)]", spin: false },
};

const STAGE_TONE: Record<string, StatusTone> = { pending: "stone", running: "indigo", passed: "emerald", blocked: "red", partial: "orange", failed: "red" };

const STAGE_BADGE: Record<string, { key: string; className: string }> = {
  pending: { key: "precheck.stage.pending", className: "text-muted-foreground" },
  running: { key: "precheck.stage.running", className: "text-primary" },
  passed: { key: "precheck.stage.passed", className: "text-[var(--risk-safe)]" },
  blocked: { key: "precheck.stage.blocked", className: "text-[var(--risk-critical)]" },
  partial: { key: "precheck.stage.partial", className: "text-[var(--risk-high)]" },
  failed: { key: "precheck.stage.failed", className: "text-[var(--exec-failed)]" },
};

function stageBadgeOf(state: string): { key: string; className: string } {
  const badge = STAGE_BADGE[state];
  if (badge !== undefined) return badge;
  return { key: "precheck.stage.pending", className: "text-muted-foreground" };
}

export interface ReviewStatusCardProps {
  phase: RunPhase;
  run: ReviewRun | null;
  hasRunId: boolean;
  isPending: boolean;
  /** Background refetching: keep content mounted when cached data exists, expressing fetch status only via aria-busy
   * and the spinning phase badge without inserting a loading block (owner 2026-11-04 zero precheck jitter). */
  isFetching?: boolean;
  isError: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Sits inside another card: drop this card's own surface and padding. */
  flat?: boolean;
}

export function ReviewStatusCard({
  phase,
  run,
  hasRunId,
  isPending,
  isFetching = false,
  isError,
  error,
  onRetry,
  flat = false,
}: ReviewStatusCardProps) {
  const { t } = useTranslation();
  const badge = isError
    ? {
        key: "precheck.review.errorBadge",
        icon: CircleAlert,
        className: "text-destructive",
        spin: false,
      }
    : hasRunId && (isPending || run === null) && phase === "idle"
      ? {
          key: "precheck.review.loadingBadge",
          icon: Loader2,
          className: "text-primary",
          spin: true,
        }
      : PHASE_BADGE[phase];
  const Icon = badge.icon;
  // RCP-20261101 ephemeral Run: queued observations before group completion may carry null fields —
  // stage_results:null was observed at runtime (despite non-null type declaration), so guard on the consumer side;
  // route-level crashes are unacceptable.
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  const summaries = run?.stage_results?.filter(hasReviewSummary) ?? [];

  return (
    <Card
      className={
        flat
          ? "gap-3 rounded-none border-0! bg-transparent py-0 shadow-none! ring-0 [--card-spacing:0px]"
          : undefined
      }
      data-testid="review-status"
    >
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Icon className={`size-4 ${badge.spin ? "animate-spin" : ""}`} aria-hidden />
          {t("precheck.review.title")}
          <Badge variant="outline" className={badge.className}>
            {t(badge.key)}
          </Badge>
        </CardTitle>
        <CardDescription>{t("precheck.review.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3" aria-busy={isFetching || undefined}>
        {!hasRunId && <EmptyState title={t("precheck.review.noRun")} />}
        {hasRunId && isError && (
          <div data-testid="review-status-error">
            <ErrorState
              error={error}
              operationId="getReviewRun"
              onRetry={onRetry}
            />
          </div>
        )}
        {hasRunId && !isError && run === null && (
          <div data-testid="review-status-loading">
            <LoadingState variant="inline" label={t("precheck.review.loading")} />
          </div>
        )}
        {/* owner 2026-11-04 (zero jitter during precheck): background refetch no longer inserts/removes a loading
            block — flipping isFetching on each poll refetch previously made content jump up and down repeatedly
            (and toggled the scrollbar when page height was near viewport height). When cached data exists, the table
            stays mounted and fetching is expressed solely through aria-busy and the phase badge (spinning while running),
            without changing any layout height. */}
        {hasRunId && !isError && run !== null && (
          <>
            <div className="text-sm" data-testid="review-counts">
              {t("precheck.review.statements", { count: run.statement_count })} ·{" "}
              {t("precheck.review.fingerprints", { count: run.fingerprint_group_count })}
            </div>
            {run.usage != null && <ReviewUsageContext usage={run.usage} />}
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("precheck.review.stage")}</TableHead>
                  <TableHead>{t("precheck.review.state")}</TableHead>
                  <TableHead>{t("precheck.review.findings")}</TableHead>
                  <TableHead>{t("precheck.review.evidence")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {/* Same as above: runtime defense against null (type not declared nullable). */}
                {/* eslint-disable-next-line @typescript-eslint/no-unnecessary-condition */}
                {(run.stage_results ?? []).map((stage) => {
                  const stageBadge = stageBadgeOf(stage.state);
                  return (
                    <TableRow key={stage.stage_position}>
                      <TableCell className="font-semibold text-foreground">{t("precheck.stagePath.stage", { position: stage.stage_position })}</TableCell>
                      <TableCell>
                        <StatusBadge tone={STAGE_TONE[stage.state] ?? "stone"}>
                          {t(stageBadge.key)}
                        </StatusBadge>
                      </TableCell>
                      <TableCell className="font-semibold text-foreground">{stage.finding_count}</TableCell>
                      <TableCell className="font-semibold text-foreground">{stage.evidence_count}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            {summaries.length > 0 && (
              <>
                <Separator />
                <section
                  className="flex flex-col gap-3"
                  aria-label={t("precheck.review.summary")}
                  data-testid="review-summary"
                >
                  <h3 className="text-sm font-semibold text-foreground">
                    {t("precheck.review.summary")}
                  </h3>
                  <dl className="flex flex-col gap-3">
                    {summaries.map((stage) => (
                      <div className="flex flex-col gap-1" key={stage.stage_position}>
                        <dt className="text-xs font-medium text-muted-foreground">
                          {t("precheck.stagePath.stage", { position: stage.stage_position })}
                        </dt>
                        <dd
                          className="text-sm leading-6 text-foreground"
                          lang={stage.summary_locale}
                        >
                          {stage.summary}
                        </dd>
                      </div>
                    ))}
                  </dl>
                </section>
              </>
            )}
            {run.failure_code != null && (
              <p className="text-sm text-[var(--exec-failed)]" data-testid="review-failure">
                {t(`precheck.failure.${knownFailureKey(run.failure_code)}`)}
              </p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function hasReviewSummary(
  stage: ReviewRun["stage_results"][number],
): stage is ReviewRun["stage_results"][number] & {
  summary: string;
  summary_locale: "zh-CN" | "en-US";
} {
  return (
    typeof stage.summary === "string" &&
    stage.summary.trim() !== "" &&
    (stage.summary_locale === "zh-CN" || stage.summary_locale === "en-US")
  );
}

/** failure_code is an open string; only known outcomes get specific copy. */
export function knownFailureKey(code: string): string {
  switch (code) {
    case "provider_unavailable":
    case "AI_PROVIDER_FAILED":
      return "provider_unavailable";
    case "budget_exhausted":
    case "AI_BUDGET_EXCEEDED":
      return "budget_exhausted";
    case "timed_out":
    case "cancelled":
      return "timed_out";
    case "REVIEW_SCHEMA_INVALID":
      return "schema_invalid";
    case "REVIEW_COVERAGE_INCOMPLETE":
      return "coverage_incomplete";
    case "SQL_PARSE_FAILED":
      return "sql_parse_failed";
    case "SQL_FINGERPRINT_FAILED":
    case "SQL_ANONYMIZATION_FAILED":
      return "sql_analysis_failed";
    case "SQL_BATCH_TOO_LARGE":
    case "TOO_MANY_FINGERPRINTS":
      return "sql_limit_exceeded";
    default:
      return "generic";
  }
}
