import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
  CartesianGrid,
  Scatter,
  ScatterChart,
  XAxis,
  YAxis,
} from "recharts";
import { ChevronDown } from "lucide-react";
import type {
  DiagnosticsHeatmapSeries,
  DiagnosticsNewHighLoadItem,
  DiagnosticsRegressionItem,
  DiagnosticsScatterPoint,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/shared/components/ui/chart";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { formatShortTime, heatmapToneClass } from "./diagnostics-formatters";

export type ResolveReadableStatementLabel = (
  statementId: string,
  databaseOrSchema?: string | null,
  engineFingerprint?: string | null,
  directPreview?: string | null,
  fallbackIndex?: number,
  maxLength?: number,
) => string;

export function DiagnosticsScatterAndHeatmapRow({
  scatterPoints,
  heatmap,
  selectedStatementId,
  resolveReadableLabel,
  onSelectStatement,
}: {
  scatterPoints: DiagnosticsScatterPoint[];
  heatmap: DiagnosticsHeatmapSeries[];
  selectedStatementId: string | null;
  resolveReadableLabel: ResolveReadableStatementLabel;
  onSelectStatement: (statementId: string, openAi?: boolean) => void;
}) {
  const { t } = useTranslation();

  const scatterChartConfig = useMemo<ChartConfig>(
    () => ({
      scatter: {
        label: t("diagnostics.charts.scatterTitle"),
        color: "var(--chart-2)",
      },
    }),
    [t],
  );

  const maxHeatmapTotalMs = useMemo(() => {
    let maxVal = 0;
    for (const row of heatmap) {
      for (const cell of row.buckets) {
        if (cell.total_exec_time_ms > maxVal) {
          maxVal = cell.total_exec_time_ms;
        }
      }
    }
    return maxVal > 0 ? maxVal : 1;
  }, [heatmap]);

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card data-testid="diag-chart-scatter">
        <CardHeader>
          <CardTitle>{t("diagnostics.charts.scatterTitle")}</CardTitle>
          <CardDescription>{t("diagnostics.charts.scatterDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <ChartContainer config={scatterChartConfig} className="h-56 w-full">
            <ScatterChart margin={{ top: 8, right: 16, left: 8, bottom: 4 }}>
              <CartesianGrid strokeDasharray="3 3" />
              <XAxis
                type="number"
                dataKey="calls"
                name={t("diagnostics.metrics.calls")}
                fontSize={11}
                tickLine={false}
                axisLine={false}
                tickMargin={8}
              />
              <YAxis
                type="number"
                dataKey="avg_exec_time_ms"
                name={t("diagnostics.metrics.avgExecTimeMs")}
                fontSize={11}
                tickLine={false}
                axisLine={false}
                tickMargin={6}
              />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Scatter
                name={t("diagnostics.charts.scatterTitle")}
                data={scatterPoints}
                fill="var(--color-scatter)"
                isAnimationActive={false}
              />
            </ScatterChart>
          </ChartContainer>
          <Collapsible defaultOpen className="border-t pt-1">
            <CollapsibleTrigger className="group hover:bg-muted/40 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-1.5 text-left text-xs font-medium text-foreground transition-colors">
              <span>
                {t("diagnostics.charts.scatterTitle")} ({Math.min(scatterPoints.length, 8)})
              </span>
              <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-out group-data-panel-open:rotate-180" />
            </CollapsibleTrigger>
            <CollapsibleContent className="pt-1 pb-1">
              <ScrollArea className="h-24" viewportClassName="h-24 pr-3">
                <div className="flex flex-wrap gap-1.5" data-testid="diag-scatter-items">
                  {scatterPoints.slice(0, 8).map((pt, idx) => (
                    <Button
                      key={pt.statement_id}
                      variant={
                        selectedStatementId === pt.statement_id
                          ? "secondary"
                          : "ghost"
                      }
                      size="sm"
                      className="font-mono text-xs"
                      onClick={() => {
                        onSelectStatement(pt.statement_id);
                      }}
                      data-testid={`diag-scatter-item-${pt.statement_id}`}
                    >
                      {resolveReadableLabel(
                        pt.statement_id,
                        pt.database_or_schema,
                        pt.engine_fingerprint,
                        null,
                        idx,
                        28,
                      )}{" "}
                      ({pt.calls}× / {pt.avg_exec_time_ms.toFixed(1)}ms)
                    </Button>
                  ))}
                </div>
              </ScrollArea>
            </CollapsibleContent>
          </Collapsible>
        </CardContent>
      </Card>

      <Card data-testid="diag-chart-heatmap">
        <CardHeader>
          <CardTitle>{t("diagnostics.charts.heatmapTitle")}</CardTitle>
          <CardDescription>{t("diagnostics.charts.heatmapDesc")}</CardDescription>
        </CardHeader>
        <CardContent>
          {heatmap.length === 0 ? (
            <p className="text-muted-foreground text-xs">
              {t("diagnostics.charts.emptyChart")}
            </p>
          ) : (
            <ScrollArea className="h-64" viewportClassName="h-64 pr-3">
              <div className="overflow-x-auto">
                <Table data-testid="diag-heatmap-grid">
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("diagnostics.columns.templatePreview")}</TableHead>
                      {(heatmap[0]?.buckets ?? []).map((cell) => (
                        <TableHead
                          key={cell.bucket_start}
                          className="text-center font-mono text-xs"
                        >
                          {formatShortTime(cell.bucket_start)}
                        </TableHead>
                      ))}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {heatmap.map((series, idx) => (
                      <TableRow key={series.statement_id}>
                        <TableCell>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="font-mono text-xs"
                            onClick={() => {
                              onSelectStatement(series.statement_id);
                            }}
                            data-testid={`diag-heatmap-row-${series.statement_id}`}
                          >
                            {resolveReadableLabel(
                              series.statement_id,
                              series.database_or_schema,
                              series.engine_fingerprint,
                              null,
                              idx,
                              32,
                            )}
                          </Button>
                        </TableCell>
                        {series.buckets.map((cell) => {
                          const ratio = cell.total_exec_time_ms / maxHeatmapTotalMs;
                          return (
                            <TableCell key={cell.bucket_start} className="p-1 text-center">
                              <button
                                type="button"
                                onClick={() => {
                                  onSelectStatement(series.statement_id);
                                }}
                                className={`w-full rounded px-1.5 py-1 font-mono text-xs tabular-nums ${heatmapToneClass(
                                  ratio,
                                  cell.unobserved_below_top_n,
                                )}`}
                                title={
                                  cell.unobserved_below_top_n
                                    ? t("diagnostics.coverageMode.unobserved_below_top_n")
                                    : `${cell.total_exec_time_ms.toFixed(1)} ms (${String(cell.calls)}×)`
                                }
                              >
                                {cell.unobserved_below_top_n
                                  ? "—"
                                  : `${cell.total_exec_time_ms.toFixed(0)}ms`}
                              </button>
                            </TableCell>
                          );
                        })}
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </ScrollArea>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export function DiagnosticsAnomalyTablesRow({
  regressions,
  newHighLoad,
  resolveReadableLabel,
  onSelectStatement,
}: {
  regressions: DiagnosticsRegressionItem[];
  newHighLoad: DiagnosticsNewHighLoadItem[];
  resolveReadableLabel: ResolveReadableStatementLabel;
  onSelectStatement: (statementId: string, openAi?: boolean) => void;
}) {
  const { t } = useTranslation();

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card data-testid="diag-chart-regressions">
        <CardHeader>
          <CardTitle>{t("diagnostics.charts.regressionsTitle")}</CardTitle>
          <CardDescription>{t("diagnostics.charts.regressionsDesc")}</CardDescription>
        </CardHeader>
        <CardContent>
          {regressions.length === 0 ? (
            <p className="text-muted-foreground text-xs">
              {t("diagnostics.charts.emptyChart")}
            </p>
          ) : (
            <ScrollArea className="h-56" viewportClassName="h-56 pr-3">
              <Table data-testid="diag-regressions-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("diagnostics.columns.templatePreview")}</TableHead>
                    <TableHead>{t("diagnostics.columns.baselineWindow")}</TableHead>
                    <TableHead>{t("diagnostics.columns.currentWindow")}</TableHead>
                    <TableHead>{t("diagnostics.columns.deltaAvgMs")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {regressions.map((item, idx) => (
                    <TableRow key={item.statement_id}>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="font-mono text-xs"
                          onClick={() => {
                            onSelectStatement(item.statement_id);
                          }}
                          data-testid={`diag-regression-row-${item.statement_id}`}
                        >
                          {resolveReadableLabel(
                            item.statement_id,
                            item.database_or_schema,
                            item.engine_fingerprint,
                            null,
                            idx,
                            36,
                          )}
                        </Button>
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {item.baseline_avg_ms.toFixed(2)} ms ({item.baseline_calls}×)
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {item.current_avg_ms.toFixed(2)} ms ({item.current_calls}×)
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <span>
                            {item.delta_avg_ms >= 0 ? "+" : ""}
                            {item.delta_avg_ms.toFixed(2)} ms
                          </span>
                          {item.insufficient_samples ? (
                            <Badge
                              variant="outline"
                              data-testid={`diag-regression-insufficient-${item.statement_id}`}
                            >
                              {t("diagnostics.insufficientSamples")}
                            </Badge>
                          ) : null}
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </ScrollArea>
          )}
        </CardContent>
      </Card>

      <Card data-testid="diag-chart-new-high-load">
        <CardHeader>
          <CardTitle>{t("diagnostics.charts.newHighLoadTitle")}</CardTitle>
          <CardDescription>{t("diagnostics.charts.newHighLoadDesc")}</CardDescription>
        </CardHeader>
        <CardContent>
          {newHighLoad.length === 0 ? (
            <p className="text-muted-foreground text-xs">
              {t("diagnostics.charts.emptyChart")}
            </p>
          ) : (
            <ScrollArea className="h-56" viewportClassName="h-56 pr-3">
              <Table data-testid="diag-new-high-load-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("diagnostics.columns.templatePreview")}</TableHead>
                    <TableHead>{t("diagnostics.metrics.totalExecTimeMs")}</TableHead>
                    <TableHead>{t("diagnostics.metrics.calls")}</TableHead>
                    <TableHead>{t("diagnostics.columns.unobservedNote")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {newHighLoad.map((item, idx) => (
                    <TableRow key={item.statement_id}>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="font-mono text-xs"
                          onClick={() => {
                            onSelectStatement(item.statement_id);
                          }}
                          data-testid={`diag-new-high-load-row-${item.statement_id}`}
                        >
                          {resolveReadableLabel(
                            item.statement_id,
                            item.database_or_schema,
                            item.engine_fingerprint,
                            null,
                            idx,
                            36,
                          )}
                        </Button>
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {item.current_total_ms.toFixed(2)} ms
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {item.current_calls}× ({item.current_avg_ms.toFixed(2)} ms)
                      </TableCell>
                      <TableCell
                        className="text-muted-foreground text-xs"
                        data-testid={`diag-unobserved-note-${item.statement_id}`}
                      >
                        {item.unobserved_note}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </ScrollArea>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
