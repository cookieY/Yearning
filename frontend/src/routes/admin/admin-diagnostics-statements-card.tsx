import { useTranslation } from "react-i18next";
import { ArrowUpDown, ChevronDown, Eye, Sparkles } from "lucide-react";
import type {
  DiagnosticsStatementSummary,
  GetDiagnosticsOverviewSortBy,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { formatReadableSqlLabel } from "@/features/diagnostics/diagnostics-overview-charts";
import { EmptyState } from "@/shared/components/status/empty-state";
import { Button } from "@/shared/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  highlightSqlLine,
} from "@/shared/components/ui/code-block";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";

export function DiagnosticsStatementsCard({
  sortedStatements,
  onSortChange,
  onSelectStatement,
}: {
  sortedStatements: DiagnosticsStatementSummary[];
  onSortChange: (sortBy: GetDiagnosticsOverviewSortBy) => void;
  onSelectStatement: (statementId: string) => void;
}) {
  const { t } = useTranslation();

  return (
    <Card data-testid="diag-statements-card">
      <CardHeader>
        <CardTitle>{t("diagnostics.table.title")}</CardTitle>
        <CardDescription>{t("diagnostics.table.description")}</CardDescription>
      </CardHeader>
      <CardContent className="px-0!">
        {sortedStatements.length === 0 ? (
          <EmptyState
            title={t("diagnostics.table.empty")}
            testId="diag-statements-empty"
          />
        ) : (
          <ScrollArea className="h-80" viewportClassName="h-80 pr-3">
            <Table data-testid="diag-statements-table">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("diagnostics.columns.fingerprint")}</TableHead>
                  <TableHead>{t("diagnostics.columns.schema")}</TableHead>
                  <TableHead>{t("diagnostics.columns.templatePreview")}</TableHead>
                  <TableHead>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        onSortChange("total_exec_time_ms");
                      }}
                      data-testid="diag-sort-total_exec_time_ms"
                    >
                      {t("diagnostics.metrics.totalExecTimeMs")}
                      <ArrowUpDown className="size-3.5" />
                    </Button>
                  </TableHead>
                  <TableHead>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        onSortChange("avg_exec_time_ms");
                      }}
                      data-testid="diag-sort-avg_exec_time_ms"
                    >
                      {t("diagnostics.metrics.avgExecTimeMs")}
                      <ArrowUpDown className="size-3.5" />
                    </Button>
                  </TableHead>
                  <TableHead>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        onSortChange("calls");
                      }}
                      data-testid="diag-sort-calls"
                    >
                      {t("diagnostics.metrics.calls")}
                      <ArrowUpDown className="size-3.5" />
                    </Button>
                  </TableHead>
                  <TableHead>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        onSortChange("rows_examined");
                      }}
                      data-testid="diag-sort-rows_examined"
                    >
                      {t("diagnostics.metrics.rowsExaminedOrBlksRead")}
                      <ArrowUpDown className="size-3.5" />
                    </Button>
                  </TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedStatements.map((stmt, idx) => {
                  const previewText =
                    stmt.template_preview ??
                    t(`diagnostics.templateStatus.${stmt.template_status}`);
                  const isLongPreview = previewText.length > 52;
                  return (
                    <TableRow
                      key={stmt.statement_id}
                      data-testid={`diag-statement-row-${stmt.statement_id}`}
                    >
                      <TableCell className="font-mono text-xs font-semibold">
                        {formatReadableSqlLabel({
                          templatePreview: stmt.template_preview,
                          databaseOrSchema: stmt.database_or_schema,
                          engineFingerprint: stmt.engine_fingerprint,
                          fallbackIndex: idx,
                          maxLength: 42,
                        })}
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {stmt.database_or_schema}
                      </TableCell>
                      <TableCell className="max-w-xs font-mono text-xs text-muted-foreground">
                        {isLongPreview ? (
                          <Collapsible>
                            <CollapsibleTrigger className="group hover:bg-muted/40 flex w-full cursor-pointer items-center justify-between gap-2 rounded px-1.5 py-1 text-left font-mono text-xs transition-colors hover:text-foreground">
                              <span className="max-w-60 truncate">
                                {stmt.template_preview
                                  ? highlightSqlLine(previewText)
                                  : previewText}
                              </span>
                              <ChevronDown className="size-3.5 shrink-0 text-muted-foreground transition-transform duration-200 ease-out group-data-panel-open:rotate-180" />
                            </CollapsibleTrigger>
                            <CollapsibleContent className="pt-1 pb-1">
                              <CodeBlock size="sm">
                                <CodeBlockActions>
                                  <CodeBlockCopyButton value={previewText} />
                                </CodeBlockActions>
                                <CodeBlockContent wrap>
                                  {previewText}
                                </CodeBlockContent>
                              </CodeBlock>
                            </CollapsibleContent>
                          </Collapsible>
                        ) : (
                          <span className="block max-w-xs truncate">
                            {stmt.template_preview
                              ? highlightSqlLine(previewText)
                              : previewText}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {stmt.total_exec_time_ms.toFixed(2)} ms
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {stmt.avg_exec_time_ms.toFixed(2)} ms
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {stmt.calls}
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {stmt.rows_examined_or_shared_blks_read}
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-1.5">
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => {
                              onSelectStatement(stmt.statement_id);
                            }}
                            data-testid={`diag-open-detail-${stmt.statement_id}`}
                          >
                            <Eye className="size-3.5" />
                            {t("diagnostics.actions.viewDetail")}
                          </Button>
                          <Button
                            variant="secondary"
                            size="sm"
                            onClick={() => {
                              onSelectStatement(stmt.statement_id);
                            }}
                            data-testid={`diag-open-ai-${stmt.statement_id}`}
                          >
                            <Sparkles className="size-3.5" />
                            {t("diagnostics.actions.aiAnalyze")}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </ScrollArea>
        )}
      </CardContent>
    </Card>
  );
}
