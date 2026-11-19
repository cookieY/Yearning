import { useState } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import type {
  AssistantSqlDraft,
  ChangeOrder,
  Flow,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  usePrecheckAssistantSqlDraft,
  useSubmitAssistantSqlDraftOrder,
} from "./use-assistant";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";
import {
  CodeBlock,
  CodeBlockContent,
} from "@/shared/components/ui/code-block";
import { Checkbox } from "@/shared/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Spinner } from "@/shared/components/ui/spinner";
import { Textarea } from "@/shared/components/ui/textarea";
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  Copy,
  Database,
  ExternalLink,
  Play,
  Send,
  ShieldAlert,
} from "lucide-react";

export interface SqlDraftCardProps {
  sessionId: string;
  draft: AssistantSqlDraft;
  flows: Flow[];
  isSelected: boolean;
  onSelectDraft: (draftId: string) => void;
}

export function SqlDraftCard({
  sessionId,
  draft,
  flows,
  isSelected,
  onSelectDraft,
}: SqlDraftCardProps) {
  const { t, i18n } = useTranslation();
  const [copied, setCopied] = useState(false);
  const [selectedFlowId, setSelectedFlowId] = useState<string>(
    draft.flow_id ?? flows[0]?.id ?? "",
  );
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [orderTitle, setOrderTitle] = useState("");
  const [orderDescription, setOrderDescription] = useState("");
  const [rollbackEnabled, setRollbackEnabled] = useState(true);
  const [actionError, setActionError] = useState<string | null>(null);
  const [precheckedDraft, setPrecheckedDraft] =
    useState<AssistantSqlDraft | null>(null);
  const [submittedOrder, setSubmittedOrder] = useState<ChangeOrder | null>(null);

  const precheckMutation = usePrecheckAssistantSqlDraft(sessionId);
  const submitOrderMutation = useSubmitAssistantSqlDraftOrder(sessionId);

  const activeDraft =
    precheckedDraft !== null &&
    precheckedDraft.id === draft.id &&
    precheckedDraft.revision === draft.revision &&
    precheckedDraft.sql_hmac === draft.sql_hmac
      ? precheckedDraft
      : draft;

  const effectiveFlowId =
    selectedFlowId || activeDraft.flow_id || flows[0]?.id || "";
  const isChangeStatement =
    activeDraft.risk_category === "dml" || activeDraft.risk_category === "ddl";
  const effectiveSubmissionState =
    submittedOrder !== null ? "order_submitted" : activeDraft.submission_state;
  const effectiveRunId = activeDraft.ephemeral_run_id;
  const effectiveOrderId = submittedOrder?.id ?? activeDraft.submitted_order_id;
  const effectiveOrderDisplayNo =
    submittedOrder?.display_number ?? activeDraft.submitted_order_display_no;

  const handleCopySql = async () => {
    try {
      await navigator.clipboard.writeText(activeDraft.sql_text);
      setCopied(true);
      appToast.info(t("assistant.draft.copied"));
      setTimeout(() => {
        setCopied(false);
      }, 1500);
    } catch {
      setCopied(false);
    }
  };

  const handleRunPrecheck = () => {
    if (!effectiveFlowId || precheckMutation.isPending) return;
    setActionError(null);
    precheckMutation.mutate(
      {
        sessionId,
        draftId: activeDraft.id,
        body: {
          flow_id: effectiveFlowId,
          revision: activeDraft.revision,
          sql_hmac: activeDraft.sql_hmac,
          output_locale: i18n.language.startsWith("en") ? "en-US" : "zh-CN",
        },
      },
      {
        onSuccess: (res) => {
          setPrecheckedDraft({
            ...res.draft,
            ephemeral_run_id: res.ephemeral_run_id,
          });
          appToast.success(t("assistant.draft.precheckAction"));
        },
        onError: (err) => {
          const display = describeError(err, "precheckAssistantSqlDraft");
          const msg = t(display.messageKey);
          setActionError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  const handleConfirmSubmitOrder = () => {
    const runId = effectiveRunId;
    if (
      !effectiveFlowId ||
      runId === null ||
      orderTitle.trim() === "" ||
      submitOrderMutation.isPending
    ) {
      return;
    }
    setActionError(null);
    submitOrderMutation.mutate(
      {
        sessionId,
        draftId: activeDraft.id,
        body: {
          flow_id: effectiveFlowId,
          run_id: runId,
          revision: activeDraft.revision,
          sql_hmac: activeDraft.sql_hmac,
          title: orderTitle.trim(),
          description: orderDescription.trim() === "" ? null : orderDescription.trim(),
          rollback_enabled: rollbackEnabled,
        },
      },
      {
        onSuccess: (order) => {
          setSubmittedOrder(order);
          setConfirmOpen(false);
          appToast.success(t("assistant.draft.confirmSubmitAction"), {
            description: `#${order.display_number}`,
          });
        },
        onError: (err) => {
          const display = describeError(err, "submitAssistantSqlDraftOrder");
          const msg = t(display.messageKey);
          setActionError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  return (
    <Card
      data-testid={`assistant-sql-draft-${draft.id}`}
      className="border-border bg-card w-full shadow-xs"
    >
      <CardHeader className="gap-1.5 pb-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant="outline" className="gap-1 font-mono text-xs">
              <Database aria-hidden="true" />
              {draft.datasource_name ?? draft.datasource_id.slice(0, 8)}
            </Badge>
            <Badge variant="secondary" className="font-mono text-xs uppercase">
              {draft.engine}
            </Badge>
            <Badge variant="outline" className="font-mono text-xs">
              {draft.schema_name}
            </Badge>
            <Badge variant="secondary" className="font-mono text-xs uppercase">
              {draft.statement_kind}
            </Badge>
          </div>
          <div className="flex items-center gap-1.5">
            <Badge variant={isSelected ? "default" : "outline"} className="text-xs">
              v{draft.revision}
            </Badge>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                onSelectDraft(draft.id);
              }}
              data-testid={`assistant-select-draft-${draft.id}`}
            >
              {isSelected ? t("assistant.draft.selected") : t("assistant.draft.useAsContext")}
            </Button>
          </div>
        </div>
        <CardTitle className="text-xs font-semibold">
          {t("assistant.draft.cardTitle")}
        </CardTitle>
        <CardDescription className="text-xs">
          {t(`assistant.draft.submissionState.${effectiveSubmissionState}`)}
        </CardDescription>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <div className="bg-muted/60 relative rounded-lg border p-3">
          <div className="mb-1.5 flex items-center justify-between gap-2">
            <span className="text-muted-foreground font-mono text-[11px]">
              HMAC: {draft.sql_hmac.slice(0, 12)}…
            </span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                void handleCopySql();
              }}
              data-testid={`assistant-copy-sql-${draft.id}`}
            >
              {copied ? (
                <Check data-icon="inline-start" aria-hidden="true" />
              ) : (
                <Copy data-icon="inline-start" aria-hidden="true" />
              )}
              {copied ? t("assistant.draft.copied") : t("assistant.draft.copySql")}
            </Button>
          </div>
          <CodeBlock size="sm" data-testid={`assistant-sql-code-${draft.id}`}>
            <CodeBlockContent wrap className="max-h-52">
              {draft.sql_text}
            </CodeBlockContent>
          </CodeBlock>
        </div>

        {draft.semantic_warnings.length > 0 && (
          <Alert variant="destructive" data-testid={`assistant-draft-warnings-${draft.id}`}>
            <AlertTriangle aria-hidden="true" />
            <AlertDescription className="flex flex-col gap-1 text-xs">
              <span className="font-semibold">
                {t("assistant.draft.semanticWarningsTitle")}
              </span>
              <ul className="list-disc pl-4">
                {draft.semantic_warnings.map((warning, idx) => (
                  <li key={idx}>{warning}</li>
                ))}
              </ul>
            </AlertDescription>
          </Alert>
        )}

        {draft.assumptions.length > 0 && (
          <div className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground font-medium">
              {t("assistant.draft.assumptionsTitle")}
            </span>
            <ul className="text-foreground/90 list-disc pl-4">
              {draft.assumptions.map((item, idx) => (
                <li key={idx}>{item}</li>
              ))}
            </ul>
          </div>
        )}

        {draft.next_steps.length > 0 && (
          <div className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground font-medium">
              {t("assistant.draft.nextStepsTitle")}
            </span>
            <ul className="text-foreground/90 list-disc pl-4">
              {draft.next_steps.map((step, idx) => (
                <li key={idx}>{step}</li>
              ))}
            </ul>
          </div>
        )}

        {actionError !== null && (
          <Alert variant="destructive" data-testid={`assistant-draft-error-${draft.id}`}>
            <ShieldAlert aria-hidden="true" />
            <AlertDescription className="text-xs">{actionError}</AlertDescription>
          </Alert>
        )}

        {effectiveSubmissionState === "order_submitted" &&
          effectiveOrderDisplayNo !== null && (
            <Alert data-testid={`assistant-draft-order-banner-${draft.id}`}>
              <CheckCircle2 aria-hidden="true" />
              <AlertDescription className="flex flex-wrap items-center justify-between gap-2 text-xs">
                <span>
                  {t("assistant.draft.orderSubmittedLabel", {
                    displayNo: effectiveOrderDisplayNo,
                  })}
                </span>
                {effectiveOrderId !== null && (
                  <Link
                    to={`/changes/orders/${effectiveOrderId}`}
                    className="text-primary inline-flex items-center gap-1 font-medium underline-offset-4 hover:underline"
                    data-testid={`assistant-draft-order-link-${draft.id}`}
                  >
                    {t("assistant.draft.viewOrder")}
                    <ExternalLink className="size-3.5" aria-hidden="true" />
                  </Link>
                )}
              </AlertDescription>
            </Alert>
          )}
      </CardContent>

      {isChangeStatement && effectiveSubmissionState !== "order_submitted" && (
        <CardFooter className="flex flex-wrap items-center justify-between gap-2 border-t pt-3">
          {flows.length > 0 && (
            <div className="flex min-w-40 items-center gap-2">
              <Select
                items={Object.fromEntries(flows.map((flow) => [flow.id, flow.name]))}
                value={effectiveFlowId}
                onValueChange={(value) => {
                  if (value !== null) setSelectedFlowId(value);
                }}
              >
                <SelectTrigger
                  className="h-8 text-xs"
                  aria-label={t("assistant.draft.selectFlow")}
                  data-testid={`assistant-draft-flow-select-${draft.id}`}
                >
                  <SelectValue placeholder={t("assistant.draft.selectFlow")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {flows.map((flow) => (
                      <SelectItem key={flow.id} value={flow.id}>
                        {flow.name}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={!effectiveFlowId || precheckMutation.isPending}
              onClick={handleRunPrecheck}
              data-testid={`assistant-draft-precheck-${draft.id}`}
            >
              {precheckMutation.isPending ? (
                <Spinner data-icon="inline-start" aria-hidden="true" />
              ) : (
                <Play data-icon="inline-start" aria-hidden="true" />
              )}
              {t("assistant.draft.runPrecheck")}
            </Button>

            {effectiveSubmissionState === "precheck_ready" &&
              effectiveRunId !== null && (
                <Button
                  type="button"
                  variant="default"
                  size="sm"
                  onClick={() => {
                    setConfirmOpen(true);
                  }}
                  data-testid={`assistant-draft-open-submit-${draft.id}`}
                >
                  <Send data-icon="inline-start" aria-hidden="true" />
                  {t("assistant.draft.openSubmitConfirm")}
                </Button>
              )}
          </div>
        </CardFooter>
      )}

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent data-testid={`assistant-submit-dialog-${draft.id}`}>
          <DialogHeader>
            <DialogTitle>{t("assistant.submitDialog.title")}</DialogTitle>
            <DialogDescription>
              {t("assistant.submitDialog.description")}
            </DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-3 py-2 text-xs">
            <div className="bg-muted/50 flex flex-wrap items-center gap-2 rounded-md border p-2.5">
              <Badge variant="outline">{draft.datasource_name ?? draft.datasource_id}</Badge>
              <Badge variant="secondary">{draft.engine}</Badge>
              <Badge variant="outline">{draft.schema_name}</Badge>
              <Badge variant="outline">v{draft.revision}</Badge>
            </div>

            <CodeBlock size="sm">
              <CodeBlockContent wrap className="max-h-36">
                {draft.sql_text}
              </CodeBlockContent>
            </CodeBlock>

            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`order-title-${draft.id}`}>
                {t("assistant.submitDialog.orderTitleLabel")}
              </Label>
              <Input
                id={`order-title-${draft.id}`}
                value={orderTitle}
                onChange={(event) => {
                  setOrderTitle(event.target.value);
                }}
                placeholder={t("assistant.submitDialog.orderTitlePlaceholder")}
                maxLength={200}
                data-testid={`assistant-submit-title-${draft.id}`}
              />
            </div>

            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`order-desc-${draft.id}`}>
                {t("assistant.submitDialog.orderDescriptionLabel")}
              </Label>
              <Textarea
                id={`order-desc-${draft.id}`}
                value={orderDescription}
                onChange={(event) => {
                  setOrderDescription(event.target.value);
                }}
                placeholder={t("assistant.submitDialog.orderDescriptionPlaceholder")}
                maxLength={4000}
                rows={2}
                data-testid={`assistant-submit-description-${draft.id}`}
              />
            </div>

            <div className="flex items-center gap-2">
              <Checkbox
                id={`order-rollback-${draft.id}`}
                checked={rollbackEnabled}
                onCheckedChange={(checked) => {
                  setRollbackEnabled(checked);
                }}
                data-testid={`assistant-submit-rollback-${draft.id}`}
              />
              <Label htmlFor={`order-rollback-${draft.id}`} className="text-xs">
                {t("assistant.submitDialog.rollbackEnabledLabel")}
              </Label>
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                setConfirmOpen(false);
              }}
            >
              {t("assistant.submitDialog.cancel")}
            </Button>
            <Button
              type="button"
              variant="default"
              disabled={orderTitle.trim() === "" || submitOrderMutation.isPending}
              onClick={handleConfirmSubmitOrder}
              data-testid={`assistant-submit-confirm-${draft.id}`}
            >
              {submitOrderMutation.isPending && (
                <Spinner data-icon="inline-start" aria-hidden="true" />
              )}
              {t("assistant.submitDialog.confirmSubmit")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
