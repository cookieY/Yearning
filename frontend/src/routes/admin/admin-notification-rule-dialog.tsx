import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Eye } from "lucide-react";
import type {
  NotificationRule,
  NotificationRuleEventType,
  NotificationRulePreview,
  NotificationRuleTargetKind,
  NotificationRuleWrite,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateNotificationRule,
  useFlows,
  useNotificationChannels,
  usePreviewNotificationRule,
  useReplaceNotificationRule,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
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
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Textarea } from "@/shared/components/ui/textarea";

const EVENT_TYPES: NotificationRuleEventType[] = [
  "change_order.submitted",
  "change_order.step_activated",
  "change_order.approval_decided",
  "change_order.stage_ready_for_execution",
  "change_order.rejected",
  "change_order.withdrawn",
  "change_order.voided",
  "execution.scheduled",
  "execution.schedule_cancelled",
  "execution.started",
  "execution.succeeded",
  "execution.failed",
  "execution.partial_failed",
  "execution.cancelled",
  "execution.partial_cancelled",
  "execution.result_unknown",
  "execution.blocked_datasource_unavailable",
  "execution.missed_schedule",
  "query_grant.revoked",
  "ai.budget_warning",
  "ai.budget_exhausted",
  "ai.provider_failover",
];

const TARGET_KINDS: NotificationRuleTargetKind[] = [
  "order_submitter",
  "stage_approvers",
  "stage_executors",
  "grant_requester",
  "builtin_admins",
];

const ORDER_PLACEHOLDERS = [
  "order_display_number",
  "order_id",
  "order_title",
  "submitter",
  "submitted_at",
  "order_state",
  "flow_name",
  "stage_name",
  "datasource_name",
  "approvers",
  "approved_by",
  "approved_at",
  "executors",
  "executed_by",
  "executed_at",
  "execution_started_at",
  "reason_code",
  "order_url",
] as const;

const GRANT_PLACEHOLDERS = [
  "grant_flow_name",
  "grant_datasources",
  "revoked_by",
  "revoked_at",
  "reason_code",
] as const;

const AI_PLACEHOLDERS = [
  "event_label",
  "occurred_at",
  "reason_code",
] as const;

function placeholdersForEvent(eventType: NotificationRuleEventType): readonly string[] {
  if (eventType === "query_grant.revoked") return GRANT_PLACEHOLDERS;
  if (eventType.startsWith("ai.")) return AI_PLACEHOLDERS;
  return ORDER_PLACEHOLDERS;
}

function buildDefaultMessageTemplate(
  eventType: NotificationRuleEventType,
  t: (key: string) => string,
): string {
  const headerKey = eventType.replace(".", "_");
  const header = t(`adminRules.templateHeader.${headerKey}`);
  if (eventType === "query_grant.revoked") {
    return [
      header,
      `${t("adminRules.placeholderLabels.grant_flow_name")}: {{grant_flow_name}}`,
      `${t("adminRules.placeholderLabels.grant_datasources")}: {{grant_datasources}}`,
      `${t("adminRules.placeholderLabels.revoked_by")}: {{revoked_by}}`,
      `${t("adminRules.placeholderLabels.revoked_at")}: {{revoked_at}}`,
      `${t("adminRules.placeholderLabels.reason_code")}: {{reason_code}}`,
    ].join("\n");
  }
  if (eventType.startsWith("ai.")) {
    return [
      header,
      `${t("adminRules.placeholderLabels.event_label")}: {{event_label}}`,
      `${t("adminRules.placeholderLabels.occurred_at")}: {{occurred_at}}`,
      `${t("adminRules.placeholderLabels.reason_code")}: {{reason_code}}`,
    ].join("\n");
  }
  return [
    `${header} ({{order_display_number}})`,
    `${t("adminRules.placeholderLabels.order_id")}: {{order_id}}`,
    `${t("adminRules.placeholderLabels.order_title")}: {{order_title}}`,
    `${t("adminRules.placeholderLabels.submitter")}: {{submitter}}`,
    `${t("adminRules.placeholderLabels.submitted_at")}: {{submitted_at}}`,
    `${t("adminRules.placeholderLabels.order_state")}: {{order_state}}`,
    `${t("adminRules.placeholderLabels.approvers")}: {{approvers}}`,
    `${t("adminRules.placeholderLabels.approved_by")}: {{approved_by}} ({{approved_at}})`,
    `${t("adminRules.placeholderLabels.executors")}: {{executors}}`,
    `${t("adminRules.placeholderLabels.executed_by")}: {{executed_by}} ({{executed_at}})`,
  ].join("\n");
}

const EMPTY_RULE: NotificationRuleWrite = {
  name: "",
  enabled: false,
  flow_id: null,
  event_type: "change_order.submitted",
  target_kind: "order_submitter",
  channel_id: "",
  message_template: null,
};

function initialRuleDraft(editing: NotificationRule | null): NotificationRuleWrite {
  if (editing === null) return EMPTY_RULE;
  return {
    name: editing.name,
    enabled: editing.enabled,
    flow_id: editing.flow_id,
    event_type: editing.event_type,
    target_kind: editing.target_kind,
    channel_id: editing.channel_id,
    message_template: editing.message_template ?? null,
  };
}

export function RuleFormDialog({
  editing,
  onClose,
}: {
  editing: NotificationRule | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const createRule = useCreateNotificationRule();
  const replaceRule = useReplaceNotificationRule();
  const previewRule = usePreviewNotificationRule();
  const channelsQuery = useNotificationChannels(true, { limit: 100 });
  const flowsQuery = useFlows(true);
  const flows = flowsQuery.data?.items ?? [];
  const channels = channelsQuery.data?.items ?? [];
  const [draft, setDraft] = useState<NotificationRuleWrite>(() => initialRuleDraft(editing));
  const [sampleOrder, setSampleOrder] = useState("");
  const [preview, setPreview] = useState<NotificationRulePreview | null>(null);
  const [errorText, setErrorText] = useState<string | null>(null);

  // Fall back to the first channel when no initial value is set (Base UI Select requires controlled values to match items).
  const effectiveChannelId = draft.channel_id || channels[0]?.id || "";
  const effectiveRule: NotificationRuleWrite = { ...draft, channel_id: effectiveChannelId };

  const runPreview = async () => {
    setErrorText(null);
    try {
      const result = await previewRule.mutateAsync({
        rule: effectiveRule,
        sample_order_id: sampleOrder === "" ? null : sampleOrder,
      });
      setPreview(result);
    } catch (error) {
      setErrorText(describeErrorText(describeError(error, "previewNotificationRule")));
    }
  };

  const submit = async () => {
    setErrorText(null);
    try {
      if (editing === null) {
        await createRule.mutateAsync(effectiveRule);
      } else {
        await replaceRule.mutateAsync({ ruleId: editing.id, version: editing.version, body: effectiveRule });
      }
      appToast.success(t("common.save"), { description: draft.name });
      onClose();
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, editing === null ? "createNotificationRule" : "replaceNotificationRule"),
      );
      setErrorText(errText);
      appToast.error(errText);
    }
  };

  const defaultTemplateText = buildDefaultMessageTemplate(draft.event_type, t);
  const availablePlaceholders = placeholdersForEvent(draft.event_type);
  const appendPlaceholder = (token: string) => {
    const snippet = `{{${token}}}`;
    const current = draft.message_template ?? "";
    const next = current === "" ? snippet : `${current} ${snippet}`;
    setDraft({ ...draft, message_template: next });
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing === null ? t("adminRules.createTitle") : t("adminRules.editTitle", { name: editing.name })}</DialogTitle>
          <DialogDescription>{t("adminRules.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="rule-name">{t("adminRules.column.name")}</Label>
            <Input id="rule-name" value={draft.name} onChange={(event) => { setDraft({ ...draft, name: event.target.value }); }} maxLength={128} data-testid="rule-name" />
          </div>
          <div className="flex items-center gap-2">
            <Checkbox
              id="rule-enabled"
              checked={draft.enabled}
              onCheckedChange={(checked) => { setDraft({ ...draft, enabled: checked }); }}
              data-testid="rule-enabled"
            />
            <Label htmlFor="rule-enabled" className="cursor-pointer">
              {t("adminRules.column.enabled")}
            </Label>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="rule-event">{t("adminRules.column.event")}</Label>
            <Select
              value={draft.event_type}
              onValueChange={(value) => { setDraft({ ...draft, event_type: value as NotificationRuleEventType }); }}
            >
              <SelectTrigger id="rule-event" className="w-full" data-testid="rule-event">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {EVENT_TYPES.map((eventType) => (
                  <SelectItem key={eventType} value={eventType}>
                    {t(`adminRules.event.${eventType}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="rule-flow">{t("adminRules.column.flow")}</Label>
            <Select
              value={draft.flow_id ?? ""}
              onValueChange={(value) => { setDraft({ ...draft, flow_id: value === "" ? null : value }); }}
            >
              <SelectTrigger id="rule-flow" className="w-full" data-testid="rule-flow">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">{t("adminRules.flow.all")}</SelectItem>
                {flows.map((flow) => (
                  <SelectItem key={flow.id} value={flow.id}>
                    {flow.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="rule-target">{t("adminRules.column.target")}</Label>
            <Select
              value={draft.target_kind}
              onValueChange={(value) => { setDraft({ ...draft, target_kind: value as NotificationRuleTargetKind }); }}
            >
              <SelectTrigger id="rule-target" className="w-full" data-testid="rule-target">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TARGET_KINDS.map((targetKind) => (
                  <SelectItem key={targetKind} value={targetKind}>
                    {t(`adminRules.target.${targetKind}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="rule-channel">{t("adminRules.column.channel")}</Label>
            <Select
              value={effectiveChannelId}
              onValueChange={(value) => { if (value !== null) { setDraft({ ...draft, channel_id: value }); } }}
            >
              <SelectTrigger id="rule-channel" className="w-full" data-testid="rule-channel">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {channels.map((channel) => (
                  <SelectItem key={channel.id} value={channel.id}>
                    {channel.name} ({channel.kind})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Label htmlFor="rule-message-template">{t("adminRules.templateLabel")}</Label>
              <div className="flex items-center gap-1.5">
                <Button
                  type="button"
                  variant="outline"
                  size="xs"
                  onClick={() => { setDraft({ ...draft, message_template: defaultTemplateText }); }}
                  data-testid="rule-template-fill-default"
                >
                  {t("adminRules.templateFillDefault")}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  disabled={!draft.message_template}
                  onClick={() => { setDraft({ ...draft, message_template: null }); }}
                  data-testid="rule-template-reset"
                >
                  {t("adminRules.templateResetDefault")}
                </Button>
              </div>
            </div>
            <Textarea
              id="rule-message-template"
              value={draft.message_template ?? ""}
              placeholder={defaultTemplateText}
              onChange={(event) => {
                const raw = event.target.value;
                setDraft({ ...draft, message_template: raw === "" ? null : raw });
              }}
              maxLength={2000}
              rows={4}
              className="font-mono text-xs"
              data-testid="rule-message-template"
            />
            <p className="text-muted-foreground text-xs">{t("adminRules.templateHint")}</p>
            <div className="rounded-md bg-muted/40 p-2">
              <p className="text-muted-foreground mb-1.5 text-xs font-medium">{t("adminRules.placeholdersTitle")}</p>
              <div className="flex flex-wrap gap-1" data-testid="rule-placeholder-chips">
                {availablePlaceholders.map((key) => (
                  <Button
                    key={key}
                    type="button"
                    variant="outline"
                    size="xs"
                    className="h-6 px-2 font-mono text-[11px]"
                    onClick={() => { appendPlaceholder(key); }}
                    data-testid={`rule-placeholder-chip-${key}`}
                  >
                    {t(`adminRules.placeholderLabels.${key}`)} · {`{{${key}}}`}
                  </Button>
                ))}
              </div>
            </div>
          </div>
          <div className="rounded-md border border-border p-3">
            <p className="text-sm font-medium">{t("adminRules.previewTitle")}</p>
            <p className="text-muted-foreground mt-1 text-xs">{t("adminRules.previewDescription")}</p>
            <div className="mt-2 flex gap-2">
              <Input
                placeholder={t("adminRules.previewSamplePlaceholder")}
                value={sampleOrder}
                onChange={(event) => { setSampleOrder(event.target.value); }}
                data-testid="rule-preview-sample"
              />
              <Button variant="outline" onClick={() => { void runPreview(); }} data-testid="rule-preview-run">
                <Eye />
                {t("adminRules.previewRun")}
              </Button>
            </div>
            {preview !== null && (
              <div className="mt-2" data-testid="rule-preview-result">
                <p className="text-xs text-muted-foreground">
                  {t("adminRules.previewTargets", { count: preview.targets.length })}
                </p>
                <ul className="mt-1 font-mono text-xs">
                  {preview.targets.map((target) => (
                    <li key={target.recipient_key}>
                      {target.recipient_key}
                      {target.masked_address ? ` · ${target.masked_address}` : ""}
                    </li>
                  ))}
                </ul>
                <p className="mt-2 rounded bg-muted p-2 text-xs whitespace-pre-wrap">{preview.message_preview}</p>
              </div>
            )}
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="rule-form-error">
              <AlertTitle>{t("adminRules.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={createRule.isPending || replaceRule.isPending}
            disabled={draft.name.trim() === "" || effectiveChannelId === ""}
            onClick={() => { void submit(); }}
            data-testid="rule-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
