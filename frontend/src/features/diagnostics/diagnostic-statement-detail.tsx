import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import {
  CartesianGrid,
  Line,
  LineChart,
  XAxis,
  YAxis,
} from "recharts";
import { CheckCircle2, ChevronDown, Link2, Sparkles, X } from "lucide-react";
import type {
  ConfirmDiagnosticOrderLinkRequestRelationKind,
  DiagnosticAiReport,
  DiagnosticStatementDetail,
  Flow,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { formatTimestamp } from "@/shared/lib/timezones";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { LoadingState } from "@/shared/components/status/loading-state";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
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
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { PendingButton } from "@/shared/components/pending-button";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
} from "@/shared/components/ui/code-block";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import {
  useConfirmDiagnosticOrderLink,
  useDiagnoseStatementWithAi,
  useDiagnosticChangeFlows,
  usePrecheckDiagnosticOptimizationDraft,
  useSubmitDiagnosticOptimizationOrder,
} from "./use-diagnostics";
import { formatReadableSqlLabel, formatShortTime } from "./diagnostics-overview-charts";

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
  const confirmOrderLink = useConfirmDiagnosticOrderLink();
  const diagnoseAi = useDiagnoseStatementWithAi();
  const precheckDraft = usePrecheckDiagnosticOptimizationDraft();
  const submitOrder = useSubmitDiagnosticOptimizationOrder();
  const flowsQuery = useDiagnosticChangeFlows(true);

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

  const [aiError, setAiError] = useState<string | null>(null);
  const [liveReport, setLiveReport] = useState<DiagnosticAiReport | null>(null);
  const activeReport = liveReport ?? detail.latest_ai_report;

  // Flows authorized for this datasource
  const eligibleFlows = useMemo<Flow[]>(() => {
    const all = flowsQuery.data ?? [];
    const matching = all.filter((flow) =>
      (flow.stages ?? []).some((stage) => stage.datasource_id === detail.datasource_id),
    );
    return matching.length > 0 ? matching : all;
  }, [detail.datasource_id, flowsQuery.data]);

  const [selectedFlowId, setSelectedFlowId] = useState<string>("");
  const effectiveFlowId =
    selectedFlowId ||
    activeReport?.flow_id ||
    eligibleFlows[0]?.id ||
    "";

  const effectiveCandidateSql = activeReport?.candidate_sql ?? "";

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

  const eligibleFlowSelectItems = useMemo(
    () => Object.fromEntries(eligibleFlows.map((flow) => [flow.id, flow.name])),
    [eligibleFlows],
  );

  const [orderTitle, setOrderTitle] = useState<string>(
    () => `SQL Optimization: ${readableStatementTitle}`,
  );
  const [orderRemark, setOrderRemark] = useState<string>("");
  const [submissionError, setSubmissionError] = useState<string | null>(null);

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

  const handleRunAiDiagnosis = async () => {
    setAiError(null);
    try {
      const report = await diagnoseAi.mutateAsync({
        datasourceId: detail.datasource_id,
        statementId: detail.statement_id,
        body: {
          from: detail.from,
          to: detail.to,
        },
      });
      setLiveReport(report);
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, "diagnoseStatementWithAi"),
      );
      setAiError(errText);
    }
  };

  const handlePrecheck = async (report: DiagnosticAiReport) => {
    setSubmissionError(null);
    try {
      const res = await precheckDraft.mutateAsync({
        datasourceId: detail.datasource_id,
        statementId: detail.statement_id,
        reportId: report.report_id,
        body: {
          flow_id: effectiveFlowId,
          candidate_sql: effectiveCandidateSql,
        },
      });
      setLiveReport(res.report);
      appToast.success(t("diagnostics.ai.precheckSuccess"));
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, "precheckDiagnosticOptimizationDraft"),
      );
      setSubmissionError(errText);
      appToast.error(errText);
    }
  };

  const handleSubmitOrder = async (report: DiagnosticAiReport) => {
    setSubmissionError(null);
    try {
      const res = await submitOrder.mutateAsync({
        datasourceId: detail.datasource_id,
        statementId: detail.statement_id,
        reportId: report.report_id,
        body: {
          flow_id: effectiveFlowId,
          ephemeral_run_id: report.ephemeral_run_id ?? "",
          title: orderTitle.trim(),
          remark: orderRemark.trim() || null,
        },
      });
      setLiveReport(res.report);
      appToast.success(t("diagnostics.ai.submitOrderButton"), {
        description: res.report.submitted_order_display_no ?? undefined,
      });
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, "submitDiagnosticOptimizationOrder"),
      );
      setSubmissionError(errText);
      appToast.error(errText);
    }
  };

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
        {/* AI Performance Diagnosis & Order Advancement Section (placed at the top of the detail panel) */}
        <Card data-testid="diag-ai-panel">
          <CardHeader className="flex flex-row items-center justify-between gap-2">
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                <Sparkles className="size-4" />
                {t("diagnostics.ai.title")}
              </CardTitle>
              <CardDescription>{t("diagnostics.ai.description")}</CardDescription>
            </div>
            <PendingButton
              pending={diagnoseAi.isPending}
              onClick={() => void handleRunAiDiagnosis()}
              data-testid="diag-run-ai-btn"
            >
              <Sparkles className="size-4" />
              {t("diagnostics.ai.runButton")}
            </PendingButton>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {aiError ? (
              <Alert variant="destructive" data-testid="diag-ai-error">
                <AlertDescription>{aiError}</AlertDescription>
              </Alert>
            ) : null}

            {diagnoseAi.isPending ? (
              <div data-testid="diag-ai-loading">
                <LoadingState variant="panel" />
              </div>
            ) : activeReport ? (
              <div className="flex flex-col gap-4" data-testid="diag-ai-report">
                {/* 4 Structured Sections inside Collapsible with Fixed-Height ScrollArea */}
                <div className="flex flex-col divide-y rounded-md border">
                  <Collapsible defaultOpen className="px-2">
                    <CollapsibleTrigger className="group hover:bg-muted/40 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-2.5 text-left text-xs font-semibold text-foreground transition-colors">
                      <span>
                        {t("diagnostics.ai.observedFactsTitle")} &{" "}
                        {t("diagnostics.ai.hypothesesTitle")}
                      </span>
                      <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-out group-data-panel-open:rotate-180" />
                    </CollapsibleTrigger>
                    <CollapsibleContent className="px-1 pb-3">
                      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                        {/* 1. Observed Statistical Facts */}
                        <div
                          className="flex flex-col gap-2 rounded-md border p-3"
                          data-testid="diag-ai-observed-facts"
                        >
                          <h4 className="font-semibold text-sm">
                            {t("diagnostics.ai.observedFactsTitle")}
                          </h4>
                          <ScrollArea className="max-h-44" viewportClassName="max-h-44 pr-3">
                            <ul className="flex flex-col gap-1 text-xs">
                              {activeReport.observed_facts.map((fact, idx) => (
                                <li
                                  key={`${fact.metric}-${String(idx)}`}
                                  className="flex justify-between gap-2"
                                >
                                  <span className="text-muted-foreground">{fact.metric}</span>
                                  <span className="font-mono font-medium">
                                    {fact.value} ({fact.window})
                                  </span>
                                </li>
                              ))}
                            </ul>
                          </ScrollArea>
                        </div>

                        {/* 2. Root-Cause Hypotheses */}
                        <div
                          className="flex flex-col gap-2 rounded-md border p-3"
                          data-testid="diag-ai-hypotheses"
                        >
                          <h4 className="font-semibold text-sm">
                            {t("diagnostics.ai.hypothesesTitle")}
                          </h4>
                          <ScrollArea className="max-h-44" viewportClassName="max-h-44 pr-3">
                            <ul className="flex flex-col gap-2 text-xs">
                              {activeReport.root_cause_hypotheses.map((hyp, idx) => (
                                <li key={String(idx)} className="flex flex-col gap-0.5">
                                  <div className="flex items-center justify-between gap-2">
                                    <span className="font-medium">{hyp.hypothesis}</span>
                                    <Badge variant="outline">{hyp.confidence}</Badge>
                                  </div>
                                  <span className="text-muted-foreground">
                                    {hyp.evidence_basis}
                                  </span>
                                </li>
                              ))}
                            </ul>
                          </ScrollArea>
                        </div>
                      </div>
                    </CollapsibleContent>
                  </Collapsible>

                  {/* 3. Optimization Recommendations */}
                  <Collapsible defaultOpen className="px-2">
                    <CollapsibleTrigger className="group hover:bg-muted/40 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-2.5 text-left text-xs font-semibold text-foreground transition-colors">
                      <span>
                        {t("diagnostics.ai.recommendationsTitle")} (
                        {activeReport.optimization_recommendations.length})
                      </span>
                      <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-out group-data-panel-open:rotate-180" />
                    </CollapsibleTrigger>
                    <CollapsibleContent className="px-2 pb-3 pt-1">
                      <div data-testid="diag-ai-recommendations">
                        <ScrollArea className="max-h-56" viewportClassName="max-h-56 pr-3">
                          <div className="flex flex-col gap-3">
                            {activeReport.optimization_recommendations.map((rec, idx) => (
                              <div key={String(idx)} className="flex flex-col gap-1 text-xs">
                                <div className="flex items-center gap-2">
                                  <Badge variant="secondary">{rec.kind}</Badge>
                                  <span className="font-semibold">{rec.title}</span>
                                </div>
                                <p className="text-muted-foreground">{rec.rationale}</p>
                                {rec.candidate_sql ? (
                                  <CodeBlock size="sm">
                                    <CodeBlockActions>
                                      <CodeBlockCopyButton value={rec.candidate_sql} />
                                    </CodeBlockActions>
                                    <CodeBlockContent wrap>
                                      {rec.candidate_sql}
                                    </CodeBlockContent>
                                  </CodeBlock>
                                ) : null}
                              </div>
                            ))}
                          </div>
                        </ScrollArea>
                      </div>
                    </CollapsibleContent>
                  </Collapsible>

                  {/* 4. Verification Steps */}
                  <Collapsible defaultOpen className="px-2">
                    <CollapsibleTrigger className="group hover:bg-muted/40 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-2.5 text-left text-xs font-semibold text-foreground transition-colors">
                      <span>
                        {t("diagnostics.ai.verificationStepsTitle")} (
                        {activeReport.verification_steps.length})
                      </span>
                      <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-out group-data-panel-open:rotate-180" />
                    </CollapsibleTrigger>
                    <CollapsibleContent className="px-2 pb-3 pt-1">
                      <div data-testid="diag-ai-verification-steps">
                        <ScrollArea className="max-h-40" viewportClassName="max-h-40 pr-3">
                          <ol className="list-decimal space-y-1 pl-4 text-xs">
                            {activeReport.verification_steps.map((step, idx) => (
                              <li key={String(idx)}>{step}</li>
                            ))}
                          </ol>
                        </ScrollArea>
                      </div>
                    </CollapsibleContent>
                  </Collapsible>
                </div>

                {/* Change Order Advancement Gate */}
                {activeReport.candidate_sql ? (
                  <div
                    className="flex flex-col gap-3 rounded-md border bg-muted/20 p-4"
                    data-testid="diag-order-advancement"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <h4 className="font-semibold text-sm">
                        {t("diagnostics.ai.advanceOrderTitle")}
                      </h4>
                      <Badge variant="outline" data-testid="diag-ai-submission-state">
                        {t(`diagnostics.submissionState.${activeReport.submission_state}`)}
                      </Badge>
                    </div>

                    <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                      <div className="flex flex-col gap-1">
                        <Label htmlFor="diag-order-flow-select">
                          {t("diagnostics.ai.selectFlowLabel")}
                        </Label>
                        <Select
                          items={eligibleFlowSelectItems}
                          value={effectiveFlowId}
                          onValueChange={(next) => {
                            if (next) setSelectedFlowId(next);
                          }}
                        >
                          <SelectTrigger
                            id="diag-order-flow-select"
                            data-testid="diag-order-flow-select"
                          >
                            <SelectValue
                              placeholder={t("diagnostics.ai.selectFlowPlaceholder")}
                            >
                              {eligibleFlowSelectItems[effectiveFlowId] ??
                                t("diagnostics.ai.selectFlowPlaceholder")}
                            </SelectValue>
                          </SelectTrigger>
                          <SelectContent>
                            {eligibleFlows.map((flow) => (
                              <SelectItem key={flow.id} value={flow.id}>
                                {flow.name}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>

                      <div className="flex flex-col gap-1">
                        <Label htmlFor="diag-order-title-input">
                          {t("diagnostics.ai.orderTitleLabel")}
                        </Label>
                        <Input
                          id="diag-order-title-input"
                          value={orderTitle}
                          onChange={(e) => {
                            setOrderTitle(e.target.value);
                          }}
                          data-testid="diag-order-title-input"
                        />
                      </div>
                    </div>

                    <div className="flex flex-col gap-1">
                      <Label htmlFor="diag-candidate-sql-textarea">
                        {t("diagnostics.ai.candidateSqlLabel")}
                      </Label>
                      <CodeBlock
                        id="diag-candidate-sql-textarea"
                        size="sm"
                        data-testid="diag-candidate-sql-textarea"
                      >
                        <CodeBlockActions>
                          <CodeBlockCopyButton value={effectiveCandidateSql} />
                        </CodeBlockActions>
                        <CodeBlockContent wrap className="max-h-36">
                          {effectiveCandidateSql}
                        </CodeBlockContent>
                      </CodeBlock>
                    </div>

                    <div className="flex flex-col gap-1">
                      <Label htmlFor="diag-order-remark-input">
                        {t("diagnostics.ai.orderRemarkLabel")}
                      </Label>
                      <Input
                        id="diag-order-remark-input"
                        value={orderRemark}
                        onChange={(e) => {
                          setOrderRemark(e.target.value);
                        }}
                        data-testid="diag-order-remark-input"
                      />
                    </div>

                    {submissionError ? (
                      <Alert variant="destructive" data-testid="diag-order-submission-error">
                        <AlertDescription>{submissionError}</AlertDescription>
                      </Alert>
                    ) : null}

                    {activeReport.submitted_order_display_no ? (
                      <Alert data-testid="diag-submitted-order-alert">
                        <CheckCircle2 className="size-4" />
                        <AlertTitle data-testid="diag-submitted-order-no">
                          {t("diagnostics.ai.orderSubmittedTitle", {
                            displayNo: activeReport.submitted_order_display_no,
                          })}
                        </AlertTitle>
                        <AlertDescription>
                          {activeReport.submitted_order_id ? (
                            <Link
                              to={`/changes/orders/${activeReport.submitted_order_id}`}
                              className="underline"
                              data-testid="diag-submitted-order-link"
                            >
                              {t("diagnostics.ai.viewSubmittedOrder")}
                            </Link>
                          ) : null}
                        </AlertDescription>
                      </Alert>
                    ) : (
                      <div className="flex flex-wrap items-center gap-2">
                        <PendingButton
                          variant="outline"
                          pending={precheckDraft.isPending}
                          disabled={!effectiveFlowId || !effectiveCandidateSql.trim()}
                          onClick={() => void handlePrecheck(activeReport)}
                          data-testid="diag-precheck-btn"
                        >
                          {t("diagnostics.ai.runPrecheckButton")}
                        </PendingButton>
                        <PendingButton
                          pending={submitOrder.isPending}
                          disabled={
                            !effectiveFlowId ||
                            activeReport.submission_state !== "prechecked" ||
                            !activeReport.ephemeral_run_id ||
                            !orderTitle.trim()
                          }
                          onClick={() => void handleSubmitOrder(activeReport)}
                          data-testid="diag-submit-order-btn"
                        >
                          {t("diagnostics.ai.submitOrderButton")}
                        </PendingButton>
                      </div>
                    )}
                  </div>
                ) : null}
              </div>
            ) : (
              <p className="text-muted-foreground text-xs" data-testid="diag-ai-empty">
                {t("diagnostics.ai.emptyPrompt")}
              </p>
            )}
          </CardContent>
        </Card>

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

        {/* 3 Detail Charts (1 chart per row) */}
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
      </div>
    </div>
  );
}
