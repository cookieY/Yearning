import { useTranslation } from "react-i18next";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Badge } from "@/shared/components/ui/badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Spinner } from "@/shared/components/ui/spinner";
import { EmptyState } from "@/shared/components/status/empty-state";
import { LoadingState } from "@/shared/components/status/loading-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { ChevronDown, CircleAlert, FileSearch, Lightbulb, Link2, TriangleAlert } from "lucide-react";
import { useSeverityFilter } from "./use-draft-workspace";
import { FindingText } from "./finding-text";
import { knownFailureKey } from "./review-status-card";
import { ReviewSummarySection, type ReviewStageSummary } from "./review-summary-section";
import { SeverityDistribution } from "./severity-distribution";

/**
 * Structured finding list (migration contract §4 item 5): severity filter,
 * stage/group context, suggestion and evidence. Only the contract fields
 * render — model reasoning is not part of the API surface and never appears
 * in the UI (PRD F4: do not display chain-of-thought).
 *
 * owner page feedback 2026-11-04 (card hierarchy): no longer wraps itself in a Card — both
 * callers (frozen review results, submission wizard precheck) are already embedded inside an outer card, so the list itself is
 * just a header row (risk findings + severity select) plus finding items, saving one layer of card chrome.
 * Round 3 on the same day: the numeric severity distribution row (headless) hangs directly below the header row, rendered
 * from the same source in both frozen review results and the submission wizard.
 */

const SEVERITY_BADGE: Record<ReviewFinding["severity"], { key: string; className: string }> = {
  // F12 #009: info is a non-blocking informational level (same visual tier as low, does not elevate max severity).
  info: { key: "precheck.severity.info", className: "text-muted-foreground" },
  low: { key: "precheck.severity.low", className: "text-muted-foreground" },
  medium: { key: "precheck.severity.medium", className: "text-[var(--risk-warning)]" },
  high: { key: "precheck.severity.high", className: "text-[var(--risk-high)]" },
  critical: { key: "precheck.severity.critical", className: "text-[var(--risk-critical)]" },
};

/**
 * Findings are only an empty result after a complete terminal review.
 * Keeping all query/lifecycle states explicit prevents pending, failed, or
 * incomplete reviews from being presented as "no risks".
 */
export type FindingListState =
  | { status: "idle" }
  | { status: "waiting" }
  | { status: "stale" }
  | { status: "expired" }
  | { status: "loading" }
  | { status: "error"; error: unknown; operationId?: string }
  | { status: "incomplete"; findings: ReviewFinding[]; failureCode?: string | null }
  | { status: "ready"; findings: ReviewFinding[] };

export interface FindingListProps {
  state: FindingListState;
  summaries?: ReviewStageSummary[];
  hideIncompleteNotice?: boolean;
  onOpenEvidence: (findings: ReviewFinding[]) => void;
  onRetry?: () => void;
}

/** The closed category vocabulary from api/contracts/ai-review-output.schema.json. */
const CATEGORY_KEYS: Record<string, string> = {
  correctness: "precheck.category.correctness",
  performance: "precheck.category.performance",
  availability: "precheck.category.availability",
  security: "precheck.category.security",
  governance: "precheck.category.governance",
  compatibility: "precheck.category.compatibility",
  operability: "precheck.category.operability",
};

/**
 * Presentation-layer grouping (owner ruling 2026-10-31): findings bound to
 * the same server-provided fingerprint group render as ONE card led by the
 * highest severity; the remaining same-group findings stay fully visible as
 * secondary sub-items (title + suggestion up front, technical message and
 * evidence one expand away). Nothing is deleted — this is the sanctioned
 * display-layer grouping, not a semantic merge; backend-authoritative
 * merging is proposed separately (RCP-20261031-FINDING-CONDITION-MERGE).
 */
const SEVERITY_RANK: Record<ReviewFinding["severity"], number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
  info: 0,
};

type FindingUnit =
  | { kind: "single"; finding: ReviewFinding }
  | { kind: "group"; primary: ReviewFinding; others: ReviewFinding[] };

function buildUnits(findings: ReviewFinding[]): FindingUnit[] {
  const members = new Map<string, ReviewFinding[]>();
  for (const finding of findings) {
    const groupId = finding.fingerprint_group_id;
    if (groupId == null || groupId === "") continue;
    const list = members.get(groupId) ?? [];
    list.push(finding);
    members.set(groupId, list);
  }
  const units: FindingUnit[] = [];
  const emitted = new Set<string>();
  for (const finding of findings) {
    const groupId = finding.fingerprint_group_id;
    if (groupId == null || groupId === "") {
      units.push({ kind: "single", finding });
      continue;
    }
    if (emitted.has(groupId)) continue;
    emitted.add(groupId);
    const sorted = [...(members.get(groupId) ?? [])].sort(
      (a, b) => SEVERITY_RANK[b.severity] - SEVERITY_RANK[a.severity],
    );
    const [top, ...rest] = sorted;
    if (top !== undefined && sorted.length >= 2) {
      units.push({ kind: "group", primary: top, others: rest });
    } else {
      units.push({ kind: "single", finding });
    }
  }
  return units;
}

export function FindingList({
  state,
  summaries = [],
  hideIncompleteNotice = false,
  onOpenEvidence,
  onRetry,
}: FindingListProps) {
  const { t } = useTranslation();
  const { severity, setSeverity, visible } = useSeverityFilter();
  const findings =
    state.status === "ready" || state.status === "incomplete" ? state.findings : null;
  const visibleFindings = findings === null ? [] : visible(findings);

  return (
    <section className="flex flex-col gap-3" data-testid="finding-list">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h4 className="text-sm font-semibold">{t("precheck.findings.title")}</h4>
        {findings !== null && findings.length > 0 && (
          <Select
            value={severity}
            onValueChange={(value) => {
              setSeverity(value as "all" | ReviewFinding["severity"]);
            }}
            items={{
              all: t("precheck.findings.filterAll"),
              info: t("precheck.severity.info"),
              low: t("precheck.severity.low"),
              medium: t("precheck.severity.medium"),
              high: t("precheck.severity.high"),
              critical: t("precheck.severity.critical"),
            }}
          >
            <SelectTrigger className="w-32" aria-label={t("precheck.findings.filter")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("precheck.findings.filterAll")}</SelectItem>
              <SelectItem value="info">{t("precheck.severity.info")}</SelectItem>
              <SelectItem value="low">{t("precheck.severity.low")}</SelectItem>
              <SelectItem value="medium">{t("precheck.severity.medium")}</SelectItem>
              <SelectItem value="high">{t("precheck.severity.high")}</SelectItem>
              <SelectItem value="critical">{t("precheck.severity.critical")}</SelectItem>
            </SelectContent>
          </Select>
        )}
      </div>
      {summaries.length > 0 && (state.status === "ready" || state.status === "incomplete") && (
        <ReviewSummarySection summaries={summaries} />
      )}
      {findings !== null && findings.length > 0 && <SeverityDistribution findings={findings} />}
      <div className="flex flex-col gap-3">
        {state.status === "idle" && (
          <EmptyState
            title={t("precheck.findings.idleTitle")}
            description={t("precheck.findings.idleDescription")}
            testId="findings-idle"
          />
        )}
        {state.status === "waiting" && (
          <Alert
            role="status"
            aria-live="polite"
            aria-atomic="true"
            data-testid="findings-waiting"
          >
            <Spinner aria-hidden />
            <AlertTitle>{t("precheck.findings.waitingTitle")}</AlertTitle>
            <AlertDescription>{t("precheck.findings.waitingDescription")}</AlertDescription>
          </Alert>
        )}
        {state.status === "stale" && (
          <Alert data-testid="findings-stale">
            <CircleAlert className="size-4" aria-hidden />
            <AlertTitle>{t("precheck.findings.staleTitle")}</AlertTitle>
            <AlertDescription>{t("precheck.findings.staleDescription")}</AlertDescription>
          </Alert>
        )}
        {state.status === "expired" && (
          <Alert data-testid="findings-expired">
            <CircleAlert className="size-4" aria-hidden />
            <AlertTitle>{t("precheck.findings.expiredTitle")}</AlertTitle>
            <AlertDescription>{t("precheck.findings.expiredDescription")}</AlertDescription>
          </Alert>
        )}
        {state.status === "loading" && (
          <div data-testid="findings-loading">
            <LoadingState variant="inline" label={t("precheck.findings.loading")} />
          </div>
        )}
        {state.status === "error" && (
          <div data-testid="findings-error">
            <ErrorState
              error={state.error}
              operationId={state.operationId ?? "listReviewRunFindings"}
              onRetry={onRetry}
            />
          </div>
        )}
        {state.status === "incomplete" && !hideIncompleteNotice && (
          <Alert data-testid="findings-incomplete">
            <TriangleAlert className="text-[var(--risk-warning)]" aria-hidden />
            <AlertTitle>{t("precheck.findings.incompleteTitle")}</AlertTitle>
            <AlertDescription className="flex flex-col gap-1.5">
              {state.failureCode != null && (
                <p
                  className="font-medium text-[var(--exec-failed)]"
                  data-testid="findings-incomplete-reason"
                >
                  {t(`precheck.failure.${knownFailureKey(state.failureCode)}`)}
                </p>
              )}
              <p>
                {state.findings.length > 0
                  ? t("precheck.findings.incompleteDescription")
                  : t("precheck.findings.incompleteEmptyDescription")}
              </p>
            </AlertDescription>
          </Alert>
        )}
        {state.status === "incomplete" &&
          hideIncompleteNotice &&
          state.findings.length === 0 &&
          summaries.length === 0 && (
            <EmptyState
              title={t("precheck.findings.noneGeneratedTitle")}
              testId="findings-none-generated"
            />
          )}
        {state.status === "ready" && state.findings.length === 0 && (
          <EmptyState
            title={t("precheck.findings.emptyAll")}
            description={t("precheck.findings.emptyAllDescription")}
            testId="findings-empty-all"
          />
        )}
        {findings !== null && findings.length > 0 && visibleFindings.length === 0 && (
          <EmptyState
            filtered
            title={t("precheck.findings.emptyFiltered")}
            testId="findings-empty"
          />
        )}
        {findings !== null && visibleFindings.length > 0
          ? buildUnits(visibleFindings).map((unit) => {
              const finding = unit.kind === "group" ? unit.primary : unit.finding;
              const badge = SEVERITY_BADGE[finding.severity];
              const hasEvidence =
                finding.evidence_ids.length > 0 ||
                (unit.kind === "group" &&
                  unit.others.some((sub) => sub.evidence_ids.length > 0));
              return (
                <article
                  key={finding.id}
                  className="rounded-md border p-3 text-sm"
                  data-testid="finding-item"
                  data-severity={finding.severity}
                >
                  <header className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline" className={badge.className}>
                      <span aria-hidden className="bg-current size-1.5 shrink-0 rounded-full" />
                      {t(badge.key)}
                    </Badge>
                    <Badge variant="secondary">{t(categoryKey(finding.category))}</Badge>
                    <span className="text-muted-foreground text-xs">
                      {t("precheck.stagePath.stage", { position: finding.stage_position })}
                    </span>
                  </header>
                  <h4 className="mt-2 leading-snug font-semibold break-words">
                    <FindingText text={finding.title} />
                  </h4>
                  <p className="mt-1.5 leading-relaxed break-words">
                    <FindingText text={finding.message} />
                  </p>
                  {finding.suggestion != null && finding.suggestion !== "" && (
                    <div
                      className="bg-muted/40 mt-2 rounded-r-md border-l-2 border-primary p-2.5"
                      data-testid="finding-suggestion"
                    >
                      <p className="flex items-center gap-1.5 text-xs font-medium">
                        <Lightbulb className="size-3.5" aria-hidden />
                        {t("precheck.findings.suggestion")}
                      </p>
                      <div className="mt-1 leading-relaxed break-words">
                        <FindingText text={finding.suggestion} />
                      </div>
                    </div>
                  )}
                  {unit.kind === "group" && (
                    <section className="mt-2.5" data-testid="finding-group-others">
                      <p className="text-muted-foreground flex items-center gap-1 text-xs font-medium">
                        <Link2 className="size-3" aria-hidden />
                        {t("precheck.findings.groupOthers")}
                      </p>
                      <div className="mt-1.5 space-y-2.5">
                        {unit.others.map((sub) => {
                          const subBadge = SEVERITY_BADGE[sub.severity];
                          return (
                            <div
                              key={sub.id}
                              className="border-t pt-2.5"
                              data-testid="finding-sub-item"
                              data-severity={sub.severity}
                            >
                              <div className="flex flex-wrap items-center gap-1.5">
                                <Badge variant="outline" className={subBadge.className}>
                                  <span aria-hidden className="bg-current size-1.5 shrink-0 rounded-full" />
                                  {t(subBadge.key)}
                                </Badge>
                                <Badge variant="secondary">{t(categoryKey(sub.category))}</Badge>
                              </div>
                              <h5 className="mt-1 text-[13px] leading-snug font-medium break-words">
                                <FindingText text={sub.title} />
                              </h5>
                                {sub.suggestion != null && sub.suggestion !== "" && (
                                <p className="mt-1 text-[13px] leading-relaxed break-words">
                                  <span className="font-medium">
                                    {t("precheck.findings.suggestionPrefix")}
                                  </span>
                                  <FindingText text={sub.suggestion} />
                                </p>
                              )}
                              <Collapsible className="mt-1" data-testid="finding-sub-detail">
                                <CollapsibleTrigger className="group text-muted-foreground hover:text-foreground inline-flex cursor-pointer items-center gap-1 text-xs">
                                  <ChevronDown className="size-3.5 transition-transform duration-200 ease-out group-data-panel-open:rotate-180" aria-hidden />
                                  {t("precheck.findings.subDetail")}
                                </CollapsibleTrigger>
                                <CollapsibleContent keepMounted>
                                  <p className="mt-1 text-[13px] leading-relaxed break-words">
                                    <FindingText text={sub.message} />
                                  </p>
                                </CollapsibleContent>
                              </Collapsible>
                            </div>
                          );
                        })}
                      </div>
                    </section>
                  )}
                  {hasEvidence && (
                    <footer className="mt-2 flex flex-wrap gap-2">
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => { onOpenEvidence([finding, ...(unit.kind === "group" ? unit.others : [])]); }}
                      >
                        <FileSearch className="size-3.5" aria-hidden />
                        {t("precheck.evidence.open")}
                      </Button>
                    </footer>
                  )}
                </article>
              );
            })
          : null}
      </div>
    </section>
  );
}

function categoryKey(category: string): string {
  return CATEGORY_KEYS[category] ?? "precheck.category.other";
}
