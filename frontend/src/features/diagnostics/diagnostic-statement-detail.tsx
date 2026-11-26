import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, X } from "lucide-react";
import type { DiagnosticStatementDetail } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { formatTimestamp } from "@/shared/lib/timezones";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
} from "@/shared/components/ui/code-block";
import { formatReadableSqlLabel } from "./diagnostics-overview-charts";
import { DiagnosticAiPanel } from "./diagnostic-ai-panel";
import { DiagnosticStatementCharts } from "./diagnostic-statement-charts";

export interface DiagnosticStatementDetailPanelProps {
  detail: DiagnosticStatementDetail;
  autoFocusAi?: boolean;
  onClose: () => void;
}

export function DiagnosticStatementDetailPanel({
  detail,
  onClose,
}: DiagnosticStatementDetailPanelProps) {
  const { t } = useTranslation();

  const readableStatementTitle = useMemo(
    () =>
      formatReadableSqlLabel({
        templatePreview: detail.template_sql ?? detail.summary.template_preview,
        databaseOrSchema: detail.database_or_schema,
        engineFingerprint: detail.engine_fingerprint,
        maxLength: 68,
      }),
    [
      detail.database_or_schema,
      detail.engine_fingerprint,
      detail.summary.template_preview,
      detail.template_sql,
    ],
  );

  return (
    <div className="flex flex-col gap-5" data-testid="diag-statement-detail-panel">
      <div className="flex flex-row items-start justify-between gap-4 border-b pb-3">
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-mono text-base font-semibold text-foreground">
              {readableStatementTitle}
            </h3>
            <Badge variant="outline">{detail.database_or_schema}</Badge>
            <Badge variant="secondary" data-testid="diag-detail-coverage-mode">
              {t(`diagnostics.coverageMode.${detail.coverage_mode}`)}
            </Badge>
          </div>
          <p className="text-muted-foreground text-xs" data-testid="diag-detail-window-scope">
            {t("diagnostics.detail.windowScope", {
              from: formatTimestamp(detail.from),
              to: formatTimestamp(detail.to),
              engine: `${detail.engine}/${detail.compatibility_mode}`,
            })}
          </p>
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={onClose}
          data-testid="diag-detail-close-btn"
        >
          <X className="size-4" />
          {t("common.close")}
        </Button>
      </div>

      <div className="flex flex-col gap-5">
        <DiagnosticAiPanel
          detail={detail}
          readableStatementTitle={readableStatementTitle}
        />

        {/* Normalized SQL Template Section with Collapsible & Fixed-Height ScrollArea */}
        <Collapsible defaultOpen className="rounded-md border px-2">
          <CollapsibleTrigger className="group hover:bg-muted/40 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-2.5 text-left transition-colors">
            <div className="flex items-center gap-2">
              <span className="font-semibold text-xs text-foreground">
                {t("diagnostics.detail.templateSqlTitle")}
              </span>
              <Badge variant="outline" data-testid="diag-detail-template-status">
                {t(`diagnostics.templateStatus.${detail.template_status}`)}
              </Badge>
            </div>
            <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-out group-data-panel-open:rotate-180" />
          </CollapsibleTrigger>
          <CollapsibleContent className="px-1 pb-3">
            {detail.template_sql ? (
              <CodeBlock size="sm" data-testid="diag-detail-template-sql">
                <CodeBlockActions>
                  <CodeBlockCopyButton value={detail.template_sql} />
                </CodeBlockActions>
                <CodeBlockContent wrap className="max-h-36">
                  {detail.template_sql}
                </CodeBlockContent>
              </CodeBlock>
            ) : (
              <Alert data-testid="diag-detail-template-unavailable">
                <AlertDescription>
                  {detail.template_note ?? t("diagnostics.detail.templateUnavailableNote")}
                </AlertDescription>
              </Alert>
            )}
          </CollapsibleContent>
        </Collapsible>

        <DiagnosticStatementCharts detail={detail} />
      </div>
    </div>
  );
}
