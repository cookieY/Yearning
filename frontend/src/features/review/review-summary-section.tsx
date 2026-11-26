import { useTranslation } from "react-i18next";
import type { ReviewRun } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

export type ReviewStageSummary = ReviewRun["stage_results"][number] & {
  summary: string;
  summary_locale: "zh-CN" | "en-US";
};

function hasReviewSummary(
  stage: ReviewRun["stage_results"][number],
): stage is ReviewStageSummary {
  return (
    typeof stage.summary === "string" &&
    stage.summary.trim() !== "" &&
    (stage.summary_locale === "zh-CN" || stage.summary_locale === "en-US")
  );
}

/** Extracts validated AI stage conclusions from a review run snapshot (defensive against null runtime arrays). */
export function extractReviewSummaries(run: ReviewRun | null): ReviewStageSummary[] {
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  return run?.stage_results?.filter(hasReviewSummary) ?? [];
}

/** Displays per-stage AI review conclusions verbatim with explicit summary locale tags. */
export function ReviewSummarySection({ summaries }: { summaries: ReviewStageSummary[] }) {
  const { t } = useTranslation();
  if (summaries.length === 0) return null;

  return (
    <section
      className="flex flex-col gap-3 rounded-md border p-3"
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
  );
}
