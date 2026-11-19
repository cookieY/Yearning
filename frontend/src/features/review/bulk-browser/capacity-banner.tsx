import { useTranslation } from "react-i18next";
import {
  FINGERPRINT_COMPLEXITY_WARNING_UNIQUE,
  FINGERPRINT_MAX_UNIQUE,
} from "@/features/review/bulk-constants";
import type { SqlDigest } from "@/features/review/bulk-import/sql-digest";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { TriangleAlert } from "lucide-react";

/**
 * Batch capacity summary with the contract complexity thresholds (frontend
 * PRD F5 items 1/8, sql-fingerprint.json): file size, statement count, unique
 * fingerprints, target tables and coverage. Pre-review the numbers come from
 * the local digest and are labelled as a browser-side estimate; once a run
 * exists the authoritative server counts take precedence. Above 200 unique
 * fingerprints the workspace shows the complexity warning, above 1,000 the
 * split guidance.
 */

export interface CapacityBannerProps {
  digest: SqlDigest | null;
  /** Authoritative run numbers (null before the first review). */
  serverStatementCount: number | null;
  serverGroupCount: number | null;
}

export function CapacityBanner({
  digest,
  serverStatementCount,
  serverGroupCount,
}: CapacityBannerProps) {
  const { t } = useTranslation();

  const statementCount = serverStatementCount ?? digest?.statementCount ?? null;
  const groupCount = serverGroupCount ?? digest?.groupCount ?? null;
  // Per-figure labeling: the groups figure is a local estimate whenever the
  // server has not reported one, even if the statement count is authoritative.
  const groupsAreEstimates = serverGroupCount === null;

  return (
    <div className="space-y-2" data-testid="capacity-banner">
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm sm:grid-cols-5">
        <div>
          <dt className="text-muted-foreground text-xs">{t("precheck.bulk.capacity.size")}</dt>
          <dd data-testid="capacity-size">
            {digest === null ? "—" : `${(digest.sizeBytes / (1024 * 1024)).toFixed(2)} MiB`}
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">{t("precheck.bulk.capacity.statements")}</dt>
          <dd data-testid="capacity-statements">{statementCount ?? "—"}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">{t("precheck.bulk.capacity.groups")}</dt>
          <dd data-testid="capacity-groups">
            {groupCount ?? "—"}
            {groupsAreEstimates && groupCount !== null && (
              <span className="text-muted-foreground ml-1 text-xs">
                {t("precheck.bulk.capacity.localEstimate")}
              </span>
            )}
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">{t("precheck.bulk.capacity.tables")}</dt>
          <dd data-testid="capacity-tables">{digest?.tableCount ?? "—"}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">{t("precheck.bulk.capacity.coverage")}</dt>
          <dd data-testid="capacity-coverage">
            {digest === null ? "—" : `${String(Math.round(digest.coverageRatio * 100))}%`}
          </dd>
        </div>
      </dl>

      {groupCount !== null && groupCount > FINGERPRINT_MAX_UNIQUE && (
        <Alert variant="destructive" data-testid="bulk-split-guidance">
          <TriangleAlert className="size-4" aria-hidden />
          <AlertTitle>
            {t("precheck.bulk.capacity.splitTitle", { count: groupCount, limit: FINGERPRINT_MAX_UNIQUE })}
          </AlertTitle>
          <AlertDescription>
            {t("precheck.bulk.capacity.splitDescription", {
              limit: FINGERPRINT_MAX_UNIQUE,
              warning: FINGERPRINT_COMPLEXITY_WARNING_UNIQUE,
            })}
          </AlertDescription>
        </Alert>
      )}
      {groupCount !== null && groupCount > FINGERPRINT_COMPLEXITY_WARNING_UNIQUE && groupCount <= FINGERPRINT_MAX_UNIQUE && (
        <Alert data-testid="bulk-complexity-warning">
          <TriangleAlert className="size-4" aria-hidden />
          <AlertTitle>
            {t("precheck.bulk.capacity.warningTitle", { count: groupCount, limit: FINGERPRINT_COMPLEXITY_WARNING_UNIQUE })}
          </AlertTitle>
          <AlertDescription>
            {t("precheck.bulk.capacity.warningDescription")}
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}
