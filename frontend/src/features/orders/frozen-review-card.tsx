import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useOrderReviewFindings } from "@/features/orders/use-approvals";
import { EvidenceSheet } from "@/features/review/evidence-sheet";
import { FindingList } from "@/features/review/finding-list";
import { ReviewAllClear } from "@/features/review/review-all-clear";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { FileSearch } from "lucide-react";

/**
 * Frozen review display (R003 — reviewers reuse the exact accepted
 * submission review and cannot request a rerun). The findings come from the
 * stage review snapshots frozen at submission via
 * GET /change-orders/{id}/review-findings; evidence opens through the same
 * controlled reveal sheet as the submission workspace. Nothing here calls a
 * provider and there is no "re-run review" affordance (PRD F7 item 5) — a
 * controlled re-review request surface needs a backend endpoint the frozen
 * OpenAPI does not carry, so none is invented.
 */
export function FrozenReviewCard({ orderId, embedded = false }: { orderId: string; embedded?: boolean }) {
  const { t } = useTranslation();
  const findingsQuery = useOrderReviewFindings(orderId, true);
  const [evidenceFindings, setEvidenceFindings] = useState<ReviewFinding[]>([]);
  const [evidenceOpen, setEvidenceOpen] = useState(false);

  const body = (
    <>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <FileSearch className="size-4" aria-hidden />
          {t("orders.detail.frozenReview")}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {findingsQuery.isPending ? (
          <LoadingState />
        ) : findingsQuery.isError ? (
          <ErrorState
            error={findingsQuery.error}
            operationId="listOrderReviewFindings"
            onRetry={() => void findingsQuery.refetch()}
          />
        ) : (
          <>
            <p className="text-muted-foreground mb-3 text-xs">
              {t("orders.detail.frozenReviewNote")}
            </p>
            {findingsQuery.data.length === 0 ? (
              <ReviewAllClear />
            ) : (
              <FindingList
                state={{ status: "ready", findings: findingsQuery.data }}
                onOpenEvidence={(groupFindings) => {
                  setEvidenceFindings(groupFindings);
                  setEvidenceOpen(true);
                }}
              />
            )}
          </>
        )}
      </CardContent>
      <EvidenceSheet findings={evidenceFindings} open={evidenceOpen} onOpenChange={setEvidenceOpen} />
    </>
  );

  if (embedded) {
    return (
      <section
        data-testid="frozen-review-card"
        className="flex flex-col gap-(--card-spacing) py-(--card-spacing) [--card-spacing:--spacing(4)]"
      >
        {body}
      </section>
    );
  }
  return <Card data-testid="frozen-review-card">{body}</Card>;
}
