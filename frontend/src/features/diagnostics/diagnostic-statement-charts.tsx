import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CartesianGrid,
  Line,
  LineChart,
  XAxis,
  YAxis,
} from "recharts";
import { Link2 } from "lucide-react";
import type {
  ConfirmDiagnosticOrderLinkRequestRelationKind,
  DiagnosticStatementDetail,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Badge } from "@/shared/components/ui/badge";
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
import { Label } from "@/shared/components/ui/label";
import { PendingButton } from "@/shared/components/pending-button";
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
import { useConfirmDiagnosticOrderLink } from "./use-diagnostics";
import { formatShortTime } from "./diagnostics-formatters";

export function DiagnosticStatementCharts({
  detail,
}: {
  detail: DiagnosticStatementDetail;
}) {
  const { t } = useTranslation();
  const confirmOrderLink = useConfirmDiagnosticOrderLink();

  const [selectedOrderId, setSelectedOrderId] = useState<string>(
    () =>
      detail.order_comparison.linked_change_order_id ??
      detail.order_comparison.candidate_orders_in_window[0]?.change_order_id ??
      "",
  );
  const [relationKind, setRelationKind] =
    useState<ConfirmDiagnosticOrderLinkRequestRelationKind>("same_fingerprint");
  const [orderLinkError, setOrderLinkError] = useState<string | null>(null);
  const [orderLinkConfirmedNo, setOrderLinkConfirmedNo] = useState<string | null>(
    detail.order_comparison.linked_order_display_no,
  );

  const candidateOrderSelectItems = useMemo(
    () =>
      Object.fromEntries(
        detail.order_comparison.candidate_orders_in_window.map((ord) => [
          ord.change_order_id,
          `${ord.display_no} · ${ord.title}`,
        ]),
      ),
    [detail.order_comparison.candidate_orders_in_window],
  );

  const relationKindSelectItems = useMemo<
    Record<ConfirmDiagnosticOrderLinkRequestRelationKind, string>
  >(
    () => ({
      same_fingerprint: t("diagnostics.relationKind.same_fingerprint"),
      rewritten_fingerprint_confirmed: t(
        "diagnostics.relationKind.rewritten_fingerprint_confirmed",
      ),
      index_or_schema_change: t(
        "diagnostics.relationKind.index_or_schema_change",
      ),
    }),
    [t],
  );

  const trendConfig = useMemo<ChartConfig>(
    () => ({
      avg_exec_time_ms: {
        label: t("diagnostics.metrics.avgExecTimeMs"),
        color: "var(--chart-1)",
      },
      calls: {
        label: t("diagnostics.metrics.calls"),
        color: "var(--chart-2)",
      },
    }),
    [t],
  );

  const costConfig = useMemo<ChartConfig>(
    () => ({
      rows_examined_or_shared_blks_read: {
        label: t("diagnostics.metrics.rowsExaminedOrBlksRead"),
        color: "var(--chart-1)",
      },
      rows_sent_or_rows_returned: {
        label: t("diagnostics.metrics.rowsSentOrReturned"),
        color: "var(--chart-2)",
      },
      lock_time_ms_or_temp_blks_written: {
        label: t("diagnostics.metrics.lockTimeOrTempBlks"),
        color: "var(--chart-3)",
      },
    }),
    [t],
  );

  const handleConfirmOrderLink = async () => {
    setOrderLinkError(null);
    try {
      const link = await confirmOrderLink.mutateAsync({
        datasourceId: detail.datasource_id,
        statementId: detail.statement_id,
        body: {
          change_order_id: selectedOrderId,
          relation_kind: relationKind,
        },
      });
      setOrderLinkConfirmedNo(link.order_display_no);
      appToast.success(t("diagnostics.detail.confirmOrderLink"), {
        description: link.order_display_no,
      });
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, "confirmDiagnosticOrderLink"),
      );
      setOrderLinkError(errText);
      appToast.error(errText);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      {/* Detail Chart 1: Aligned Avg Latency & Calls Trend */}
      <Card data-testid="diag-detail-chart-trend">
        <CardHeader>
          <CardTitle className="text-sm">
            {t("diagnostics.detail.trendChartTitle")}
          </CardTitle>
          <CardDescription className="text-xs">
            {t("diagnostics.detail.trendChartDesc")}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <ChartContainer config={trendConfig} className="h-36 w-full">
            <LineChart
              data={detail.trend_series}
              syncId="diag-stmt-detail"
              margin={{ top: 4, right: 8, left: 4, bottom: 2 }}
            >
              <CartesianGrid vertical={false} strokeDasharray="3 3" />
              <XAxis
                dataKey="bucket_start"
                tickFormatter={formatShortTime}
                fontSize={10}
                tickLine={false}
                axisLine={false}
                tickMargin={6}
              />
              <YAxis fontSize={10} tickLine={false} axisLine={false} tickMargin={4} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Line
                type="monotone"
                dataKey="avg_exec_time_ms"
                stroke="var(--color-avg_exec_time_ms)"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
            </LineChart>
          </ChartContainer>
          <ChartContainer config={trendConfig} className="h-32 w-full">
            <LineChart
              data={detail.trend_series}
              syncId="diag-stmt-detail"
              margin={{ top: 4, right: 8, left: 4, bottom: 2 }}
            >
              <CartesianGrid vertical={false} strokeDasharray="3 3" />
              <XAxis
                dataKey="bucket_start"
                tickFormatter={formatShortTime}
                fontSize={10}
                tickLine={false}
                axisLine={false}
                tickMargin={6}
              />
              <YAxis fontSize={10} tickLine={false} axisLine={false} tickMargin={4} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Line
                type="monotone"
                dataKey="calls"
                stroke="var(--color-calls)"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
            </LineChart>
          </ChartContainer>
        </CardContent>
      </Card>

      {/* Detail Chart 2: Execution Cost Trend */}
      <Card data-testid="diag-detail-chart-cost">
        <CardHeader>
          <CardTitle className="text-sm">
            {t("diagnostics.detail.costChartTitle")}
          </CardTitle>
          <CardDescription className="text-xs">
            {t("diagnostics.detail.costChartDesc")}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <ChartContainer config={costConfig} className="h-44 w-full">
            <LineChart
              data={detail.cost_series}
              syncId="diag-stmt-detail"
              margin={{ top: 4, right: 8, left: 4, bottom: 2 }}
            >
              <CartesianGrid vertical={false} strokeDasharray="3 3" />
              <XAxis
                dataKey="bucket_start"
                tickFormatter={formatShortTime}
                fontSize={10}
                tickLine={false}
                axisLine={false}
                tickMargin={6}
              />
              <YAxis fontSize={10} tickLine={false} axisLine={false} tickMargin={4} />
              <ChartTooltip content={<ChartTooltipContent />} />
              <Line
                type="monotone"
                dataKey="rows_examined_or_shared_blks_read"
                stroke="var(--color-rows_examined_or_shared_blks_read)"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
              <Line
                type="monotone"
                dataKey="lock_time_ms_or_temp_blks_written"
                stroke="var(--color-lock_time_ms_or_temp_blks_written)"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
            </LineChart>
          </ChartContainer>
          <div
            className="flex flex-col gap-1 text-muted-foreground text-xs"
            data-testid="diag-detail-cost-notes"
          >
            {Object.entries(detail.cost_unit_notes).map(([key, note]) => (
              <p key={key}>
                <span className="font-mono font-medium text-foreground">{key}:</span>{" "}
                {note}
              </p>
            ))}
          </div>
        </CardContent>
      </Card>

      {/* Detail Chart 3: Before/After Order Optimization Comparison */}
      <Card data-testid="diag-detail-chart-comparison">
        <CardHeader>
          <CardTitle className="text-sm">
            {t("diagnostics.detail.comparisonTitle")}
          </CardTitle>
          <CardDescription className="text-xs">
            {t("diagnostics.detail.comparisonDesc")}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {detail.order_comparison.insufficient_samples ? (
            <Badge
              variant="outline"
              className="w-fit"
              data-testid="diag-comparison-insufficient-badge"
            >
              {t("diagnostics.insufficientSamples")}
            </Badge>
          ) : null}
          {orderLinkConfirmedNo ? (
            <p
              className="text-xs font-medium text-foreground"
              data-testid="diag-confirmed-order-no"
            >
              {t("diagnostics.detail.linkedOrderLabel")}: {orderLinkConfirmedNo}
            </p>
          ) : null}
          {detail.order_comparison.before_window &&
          detail.order_comparison.after_window ? (
            <Table data-testid="diag-comparison-windows-table">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("diagnostics.columns.window")}</TableHead>
                  <TableHead>{t("diagnostics.metrics.calls")}</TableHead>
                  <TableHead>{t("diagnostics.metrics.avgExecTimeMs")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow>
                  <TableCell className="text-xs">
                    {t("diagnostics.detail.beforeOrder")}
                  </TableCell>
                  <TableCell className="tabular-nums text-xs">
                    {detail.order_comparison.before_window.calls}
                  </TableCell>
                  <TableCell className="tabular-nums text-xs">
                    {detail.order_comparison.before_window.avg_exec_time_ms.toFixed(2)} ms
                  </TableCell>
                </TableRow>
                <TableRow>
                  <TableCell className="text-xs">
                    {t("diagnostics.detail.afterOrder")}
                  </TableCell>
                  <TableCell className="tabular-nums text-xs">
                    {detail.order_comparison.after_window.calls}
                  </TableCell>
                  <TableCell className="tabular-nums text-xs">
                    {detail.order_comparison.after_window.avg_exec_time_ms.toFixed(2)} ms
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          ) : null}

          {detail.order_comparison.candidate_orders_in_window.length > 0 ? (
            <div className="flex flex-col gap-2 border-t pt-2">
              <Label className="text-xs">
                {t("diagnostics.detail.candidateOrderSelect")}
              </Label>
              <Select
                items={candidateOrderSelectItems}
                value={selectedOrderId}
                onValueChange={(next) => {
                  if (next) setSelectedOrderId(next);
                }}
              >
                <SelectTrigger data-testid="diag-candidate-order-select">
                  <SelectValue>
                    {candidateOrderSelectItems[selectedOrderId]}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {detail.order_comparison.candidate_orders_in_window.map((ord) => (
                    <SelectItem key={ord.change_order_id} value={ord.change_order_id}>
                      {ord.display_no} · {ord.title}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Select
                items={relationKindSelectItems}
                value={relationKind}
                onValueChange={(next) => {
                  if (next) setRelationKind(next);
                }}
              >
                <SelectTrigger data-testid="diag-order-relation-select">
                  <SelectValue>
                    {relationKindSelectItems[relationKind]}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="same_fingerprint">
                    {t("diagnostics.relationKind.same_fingerprint")}
                  </SelectItem>
                  <SelectItem value="rewritten_fingerprint_confirmed">
                    {t("diagnostics.relationKind.rewritten_fingerprint_confirmed")}
                  </SelectItem>
                  <SelectItem value="index_or_schema_change">
                    {t("diagnostics.relationKind.index_or_schema_change")}
                  </SelectItem>
                </SelectContent>
              </Select>
              <PendingButton
                variant="outline"
                size="sm"
                pending={confirmOrderLink.isPending}
                disabled={!selectedOrderId}
                onClick={() => void handleConfirmOrderLink()}
                data-testid="diag-confirm-order-link-btn"
              >
                <Link2 className="size-3.5" />
                {t("diagnostics.detail.confirmOrderLink")}
              </PendingButton>
              {orderLinkError ? (
                <p className="text-destructive text-xs">{orderLinkError}</p>
              ) : null}
            </div>
          ) : (
            <p className="text-muted-foreground text-xs">
              {t("diagnostics.detail.noCandidateOrders")}
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
