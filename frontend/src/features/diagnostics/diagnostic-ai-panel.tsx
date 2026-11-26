import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { CheckCircle2, ChevronDown, Sparkles } from "lucide-react";
import type {
  DiagnosticAiReport,
  DiagnosticStatementDetail,
  Flow,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { LoadingState } from "@/shared/components/status/loading-state";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
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
} from "@/shared/components/ui/code-block";
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
  useDiagnoseStatementWithAi,
  useDiagnosticChangeFlows,
  usePrecheckDiagnosticOptimizationDraft,
  useSubmitDiagnosticOptimizationOrder,
} from "./use-diagnostics";

export function DiagnosticAiPanel({
  detail,
  readableStatementTitle,
}: {
  detail: DiagnosticStatementDetail;
  readableStatementTitle: string;
}) {
  const { t } = useTranslation();
  const diagnoseAi = useDiagnoseStatementWithAi();
  const precheckDraft = usePrecheckDiagnosticOptimizationDraft();
  const submitOrder = useSubmitDiagnosticOptimizationOrder();
  const flowsQuery = useDiagnosticChangeFlows(true);

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

  const eligibleFlowSelectItems = useMemo(
    () => Object.fromEntries(eligibleFlows.map((flow) => [flow.id, flow.name])),
    [eligibleFlows],
  );

  const [orderTitle, setOrderTitle] = useState<string>(
    () => `SQL Optimization: ${readableStatementTitle}`,
  );
  const [orderRemark, setOrderRemark] = useState<string>("");
  const [submissionError, setSubmissionError] = useState<string | null>(null);

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
  );
}
