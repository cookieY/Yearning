import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Bar,
  BarChart,
  Brush,
  CartesianGrid,
  Line,
  LineChart,
  XAxis,
  YAxis,
} from "recharts";
import { ChevronDown } from "lucide-react";
import type {
  DiagnosticsHeatmapSeries,
  DiagnosticsLoadTrendPoint,
  DiagnosticsNewHighLoadItem,
  DiagnosticsRegressionItem,
  DiagnosticsScatterPoint,
  DiagnosticsStatementSummary,
  DiagnosticsTopStatementItem,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import {
  formatAxisSqlLabel,
  formatReadableSqlLabel,
  formatShortTime,
  type TopMetricKey,
} from "./diagnostics-formatters";
import {
  DiagnosticsAnomalyTablesRow,
  DiagnosticsScatterAndHeatmapRow,
} from "./diagnostics-overview-panels";

export { formatReadableSqlLabel, formatShortTime, type TopMetricKey };

export interface DiagnosticsOverviewChartsProps {
  loadTrend: DiagnosticsLoadTrendPoint[];
  topStatements: DiagnosticsTopStatementItem[];
  scatterPoints: DiagnosticsScatterPoint[];
  heatmap: DiagnosticsHeatmapSeries[];
  regressions: DiagnosticsRegressionItem[];
  newHighLoad: DiagnosticsNewHighLoadItem[];
  statements?: DiagnosticsStatementSummary[];
  selectedStatementId?: string | null;
  showAccessibleTables: boolean;
  brushWindow: { startTime: string; endTime: string } | null;
  onBrushWindowChange: (window: { startTime: string; endTime: string } | null) => void;
  onSelectStatement: (statementId: string, openAi?: boolean) => void;
}

export function DiagnosticsOverviewCharts({
  loadTrend,
  topStatements,
  scatterPoints,
  heatmap,
  regressions,
  newHighLoad,
  statements = [],
  selectedStatementId = null,
  showAccessibleTables,
  brushWindow,
  onBrushWindowChange,
  onSelectStatement,
}: DiagnosticsOverviewChartsProps) {
  const { t } = useTranslation();
  const [topMetric, setTopMetric] = useState<TopMetricKey>("total_exec_time_ms");

  const topMetricSelectItems = useMemo<Record<TopMetricKey, string>>(
    () => ({
      total_exec_time_ms: t("diagnostics.metrics.totalExecTimeMs"),
      avg_exec_time_ms: t("diagnostics.metrics.avgExecTimeMs"),
      calls: t("diagnostics.metrics.calls"),
    }),
    [t],
  );

  const statementPreviewById = useMemo(() => {
    const map = new Map<
      string,
      { templatePreview: string | null; databaseOrSchema: string }
    >();
    for (const stmt of statements) {
      map.set(stmt.statement_id, {
        templatePreview: stmt.template_preview ?? null,
        databaseOrSchema: stmt.database_or_schema,
      });
    }
    for (const top of topStatements) {
      const existing = map.get(top.statement_id);
      if (!existing?.templatePreview && top.template_preview) {
        map.set(top.statement_id, {
          templatePreview: top.template_preview,
          databaseOrSchema: top.database_or_schema,
        });
      }
    }
    return map;
  }, [statements, topStatements]);

  const resolveReadableLabel = useCallback(
    (
      statementId: string,
      databaseOrSchema?: string | null,
      engineFingerprint?: string | null,
      directPreview?: string | null,
      fallbackIndex?: number,
      maxLength = 34,
    ): string => {
      const entry = statementPreviewById.get(statementId);
      return formatReadableSqlLabel({
        templatePreview: directPreview ?? entry?.templatePreview ?? null,
        databaseOrSchema: databaseOrSchema ?? entry?.databaseOrSchema ?? null,
        engineFingerprint,
        fallbackIndex,
        maxLength,
      });
    },
    [statementPreviewById],
  );

  const loadChartConfig = useMemo<ChartConfig>(
    () => ({
      total_exec_time_ms: {
        label: t("diagnostics.metrics.totalExecTimeMs"),
        color: "var(--chart-1)",
      },
      avg_exec_time_ms: {
        label: t("diagnostics.metrics.avgExecTimeMs"),
        color: "var(--chart-2)",
      },
      calls: {
        label: t("diagnostics.metrics.calls"),
        color: "var(--chart-3)",
      },
    }),
    [t],
  );

  const topChartConfig = useMemo<ChartConfig>(
    () => ({
      value: {
        label: topMetricSelectItems[topMetric],
        color: "var(--chart-1)",
      },
    }),
    [topMetric, topMetricSelectItems],
  );

  const sortedTopStatements = useMemo(() => {
    const copy = [...topStatements];
    copy.sort((a, b) => b[topMetric] - a[topMetric]);
    return copy.slice(0, 10).map((item, idx) => {
      const entry = statementPreviewById.get(item.statement_id);
      return {
        ...item,
        axisLabel: formatAxisSqlLabel({
          templatePreview: entry?.templatePreview ?? item.template_preview,
          databaseOrSchema: item.database_or_schema,
          index: idx,
          maxLength: 22,
        }),
        shortLabel: resolveReadableLabel(
          item.statement_id,
          item.database_or_schema,
          item.engine_fingerprint,
          item.template_preview,
          idx,
          32,
        ),
        value: item[topMetric],
      };
    });
  }, [resolveReadableLabel, statementPreviewById, topMetric, topStatements]);

  return (
    <div className="flex flex-col gap-4" data-testid="diag-overview-charts">
      {/* Row 1: SQL Load Trend + Top 10 Hot SQL */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card data-testid="diag-chart-load-trend">
          <CardHeader className="flex flex-row items-start justify-between gap-2">
            <div>
              <CardTitle>{t("diagnostics.charts.loadTrendTitle")}</CardTitle>
              <CardDescription>{t("diagnostics.charts.loadTrendDesc")}</CardDescription>
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              {loadTrend.length >= 2 ? (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    const first = loadTrend[0];
                    const last = loadTrend[loadTrend.length - 1];
                    if (first && last) {
                      onBrushWindowChange({
                        startTime: first.bucket_start,
                        endTime: last.bucket_end,
                      });
                    }
                  }}
                  data-testid="diag-brush-select-latest"
                >
                  {t("diagnostics.charts.filterWindowAction")}
                </Button>
              ) : null}
              {brushWindow !== null ? (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    onBrushWindowChange(null);
                  }}
                  data-testid="diag-brush-reset"
                >
                  {t("diagnostics.charts.clearBrushWindow")}
                </Button>
              ) : null}
            </div>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <ChartContainer config={loadChartConfig} className="h-56 w-full">
              <LineChart
                data={loadTrend}
                margin={{ top: 8, right: 12, left: 8, bottom: 4 }}
              >
                <CartesianGrid vertical={false} strokeDasharray="3 3" />
                <XAxis
                  dataKey="bucket_start"
                  tickFormatter={formatShortTime}
                  fontSize={11}
                  tickLine={false}
                  axisLine={false}
                  tickMargin={8}
                />
                <YAxis
                  fontSize={11}
                  tickLine={false}
                  axisLine={false}
                  tickMargin={6}
                />
                <ChartTooltip content={<ChartTooltipContent />} />
                <Line
                  type="monotone"
                  dataKey="total_exec_time_ms"
                  stroke="var(--color-total_exec_time_ms)"
                  strokeWidth={2}
                  dot={false}
                  isAnimationActive={false}
                />
                <Line
                  type="monotone"
                  dataKey="avg_exec_time_ms"
                  stroke="var(--color-avg_exec_time_ms)"
                  strokeWidth={2}
                  dot={false}
                  isAnimationActive={false}
                />
                {loadTrend.length > 1 ? (
                  <Brush
                    dataKey="bucket_start"
                    height={18}
                    travellerWidth={6}
                    fill="var(--muted)"
                    stroke="var(--border)"
                    tickFormatter={formatShortTime}
                    onChange={(range) => {
                      if (
                        typeof range.startIndex === "number" &&
                        typeof range.endIndex === "number"
                      ) {
                        const startPt = loadTrend[range.startIndex];
                        const endPt = loadTrend[range.endIndex];
                        if (startPt && endPt) {
                          onBrushWindowChange({
                            startTime: startPt.bucket_start,
                            endTime: endPt.bucket_end,
                          });
                        }
                      }
                    }}
                  />
                ) : null}
              </LineChart>
            </ChartContainer>
            {showAccessibleTables ? (
              <Table data-testid="diag-load-trend-a11y-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("diagnostics.columns.timeBucket")}</TableHead>
                    <TableHead>{t("diagnostics.metrics.totalExecTimeMs")}</TableHead>
                    <TableHead>{t("diagnostics.metrics.calls")}</TableHead>
                    <TableHead>{t("diagnostics.metrics.avgExecTimeMs")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {loadTrend.map((pt) => (
                    <TableRow key={pt.bucket_start}>
                      <TableCell className="font-mono text-xs">
                        {formatShortTime(pt.bucket_start)}
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {pt.total_exec_time_ms.toFixed(2)}
                      </TableCell>
                      <TableCell className="tabular-nums">{pt.calls}</TableCell>
                      <TableCell className="tabular-nums">
                        {pt.avg_exec_time_ms.toFixed(2)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : null}
          </CardContent>
        </Card>

        <Card data-testid="diag-chart-top10">
          <CardHeader className="flex flex-row items-start justify-between gap-2">
            <div>
              <CardTitle>{t("diagnostics.charts.top10Title")}</CardTitle>
              <CardDescription>{t("diagnostics.charts.top10Desc")}</CardDescription>
            </div>
            <Select
              items={topMetricSelectItems}
              value={topMetric}
              onValueChange={(next) => {
                if (next) setTopMetric(next);
              }}
            >
              <SelectTrigger
                className="w-40"
                data-testid="diag-top10-metric-select"
              >
                <SelectValue>{topMetricSelectItems[topMetric]}</SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="total_exec_time_ms">
                  {t("diagnostics.metrics.totalExecTimeMs")}
                </SelectItem>
                <SelectItem value="avg_exec_time_ms">
                  {t("diagnostics.metrics.avgExecTimeMs")}
                </SelectItem>
                <SelectItem value="calls">{t("diagnostics.metrics.calls")}</SelectItem>
              </SelectContent>
            </Select>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <ChartContainer config={topChartConfig} className="h-64 w-full">
              <BarChart
                data={sortedTopStatements}
                layout="vertical"
                margin={{ top: 8, right: 16, left: 8, bottom: 4 }}
              >
                <CartesianGrid horizontal={false} strokeDasharray="3 3" />
                <XAxis
                  type="number"
                  fontSize={11}
                  tickLine={false}
                  axisLine={false}
                  tickMargin={6}
                />
                <YAxis
                  dataKey="axisLabel"
                  type="category"
                  width={152}
                  interval={0}
                  fontSize={11}
                  tickLine={false}
                  axisLine={false}
                  tickMargin={6}
                />
                <ChartTooltip content={<ChartTooltipContent labelKey="shortLabel" />} />
                <Bar
                  dataKey="value"
                  fill="var(--color-value)"
                  radius={[0, 4, 4, 0]}
                  isAnimationActive={false}
                />
              </BarChart>
            </ChartContainer>
            <Collapsible defaultOpen className="border-t pt-1">
              <CollapsibleTrigger className="group hover:bg-muted/40 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-1.5 text-left text-xs font-medium text-foreground transition-colors">
                <span>
                  {t("diagnostics.charts.top10Title")} ({sortedTopStatements.length})
                </span>
                <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-out group-data-panel-open:rotate-180" />
              </CollapsibleTrigger>
              <CollapsibleContent className="pt-1 pb-1">
                <ScrollArea className="h-24" viewportClassName="h-24 pr-3">
                  <div
                    className="flex flex-wrap gap-1.5"
                    data-testid="diag-top10-quick-select"
                  >
                    {sortedTopStatements.map((item) => (
                      <Button
                        key={item.statement_id}
                        variant={
                          selectedStatementId === item.statement_id
                            ? "secondary"
                            : "outline"
                        }
                        size="sm"
                        className="font-mono text-xs"
                        onClick={() => {
                          onSelectStatement(item.statement_id);
                        }}
                        data-testid={`diag-top10-item-${item.statement_id}`}
                      >
                        {item.shortLabel} · {item.value.toFixed(1)}
                      </Button>
                    ))}
                  </div>
                </ScrollArea>
              </CollapsibleContent>
            </Collapsible>
          </CardContent>
        </Card>
      </div>

      <DiagnosticsScatterAndHeatmapRow
        scatterPoints={scatterPoints}
        heatmap={heatmap}
        selectedStatementId={selectedStatementId}
        resolveReadableLabel={resolveReadableLabel}
        onSelectStatement={onSelectStatement}
      />

      <DiagnosticsAnomalyTablesRow
        regressions={regressions}
        newHighLoad={newHighLoad}
        resolveReadableLabel={resolveReadableLabel}
        onSelectStatement={onSelectStatement}
      />
    </div>
  );
}
