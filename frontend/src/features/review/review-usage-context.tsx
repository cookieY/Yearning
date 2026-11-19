import { useTranslation } from "react-i18next";
import type { ReviewRunUsage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Button } from "@/shared/components/ui/button";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/shared/components/ui/hover-card";
import { Progress } from "@/shared/components/ui/progress";

/**
 * AI usage of a review run, as the shadcn AI Elements `Context` component: an
 * outline button that opens a Hover Card with a Progress bar and the token
 * rows. The run carries no context-window limit and no reasoning tokens, so
 * there is no "% of context" to show (and none is invented); the bar instead
 * shows how the run's tokens split between input and output, and the card
 * keeps the exact figures — the earlier one-line text was an audit detail, so
 * it must stay exact, not abbreviated.
 */

function formatTokens(value: number): string {
  if (value < 1000) return String(value);
  return `${(value / 1000).toFixed(1).replace(/\.0$/, "")}k`;
}

export function ReviewUsageContext({ usage }: { usage: ReviewRunUsage }) {
  const { t, i18n } = useTranslation();
  const total = usage.input_tokens + usage.output_tokens;
  const inputShare = total === 0 ? 0 : Math.round((usage.input_tokens / total) * 100);
  const exact = new Intl.NumberFormat(i18n.language);
  const rows = [
    { key: "input", label: t("precheck.review.usage.input"), value: usage.input_tokens, share: inputShare },
    { key: "output", label: t("precheck.review.usage.output"), value: usage.output_tokens, share: total === 0 ? 0 : 100 - inputShare },
  ];

  return (
    <HoverCard>
      <HoverCardTrigger
        delay={10}
        closeDelay={100}
        render={<Button variant="outline" size="sm" className="w-fit" />}
        data-testid="review-usage"
      >
        {t("precheck.review.usage.trigger", { total: formatTokens(total) })}
      </HoverCardTrigger>
      <HoverCardContent className="flex w-72 flex-col gap-3" data-testid="review-usage-card">
        <div className="flex items-center justify-between gap-3 text-sm">
          <span className="min-w-0 truncate font-medium" title={usage.model_name}>
            {usage.model_name}
          </span>
          <span className="text-muted-foreground shrink-0 tabular-nums">
            {exact.format(total)} tokens
          </span>
        </div>
        <Progress value={inputShare} aria-label={t("precheck.review.usage.inputShare")} />
        <div className="flex flex-col gap-1 text-sm">
          {rows.map((row) => (
            <div key={row.key} className="flex justify-between gap-3" data-testid={`review-usage-${row.key}`}>
              <span className="text-muted-foreground">{row.label}</span>
              <span className="tabular-nums">
                {exact.format(row.value)}
                <span className="text-muted-foreground"> · {row.share}%</span>
              </span>
            </div>
          ))}
          <div className="flex justify-between gap-3" data-testid="review-usage-provider">
            <span className="text-muted-foreground">{t("precheck.review.usage.provider")}</span>
            <span>{usage.provider_name}</span>
          </div>
        </div>
      </HoverCardContent>
    </HoverCard>
  );
}
