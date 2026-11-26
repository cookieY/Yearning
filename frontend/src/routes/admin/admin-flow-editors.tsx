import { useState } from "react";
import { useTranslation } from "react-i18next";
import { EyeOff, Plus, Trash2 } from "lucide-react";
import type { Flow, FlowMaskingRule, User } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useFlowMaskingRules,
  useReplaceMaskingRule,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Textarea } from "@/shared/components/ui/textarea";

export function ApprovalStepsEditor({
  steps,
  users,
  onChange,
  testIdPrefix,
}: {
  steps: { position: number; actorIds: string[] }[];
  users: User[];
  onChange: (steps: { position: number; actorIds: string[] }[]) => void;
  testIdPrefix: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <Label>{t("adminFlows.approvalSteps")}</Label>
        {steps.length < 10 && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => { onChange([...steps, { position: steps.length + 1, actorIds: [] }]); }}
            data-testid={`${testIdPrefix}-add-step`}
          >
            <Plus />
            {t("adminFlows.addStep")}
          </Button>
        )}
      </div>
      <p className="text-muted-foreground text-xs">{t("adminFlows.approvalStepsHint")}</p>
      {steps.map((step, stepIndex) => (
        <div key={stepIndex} className="flex flex-col gap-1 rounded-md border p-2" data-testid={`${testIdPrefix}-step-${String(stepIndex)}`}>
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium">{t("adminFlows.stepTitle", { index: step.position })}</span>
            <span className="flex items-center gap-2">
              <label className="flex cursor-pointer items-center gap-1 text-xs text-muted-foreground">
                <Checkbox
                  checked={step.actorIds.length > 0 && step.actorIds.length === users.length}
                  indeterminate={step.actorIds.length > 0 && step.actorIds.length < users.length}
                  onCheckedChange={() => {
                    const allSelected = step.actorIds.length > 0 && step.actorIds.length === users.length;
                    onChange(
                      steps.map((item, index) =>
                        index === stepIndex
                          ? { ...item, actorIds: allSelected ? [] : users.map((user) => user.id) }
                          : item,
                      ),
                    );
                  }}
                  data-testid={`${testIdPrefix}-step-${String(stepIndex)}-select-all`}
                />
                {t("adminFlows.selectAll")}
                <span className="tabular-nums" data-testid={`${testIdPrefix}-step-${String(stepIndex)}-select-all-count`}>
                  {String(step.actorIds.length)}/{String(users.length)}
                </span>
              </label>
              {steps.length > 1 && (
                <Button
                  size="icon"
                  variant="ghost"
                  onClick={() => { onChange(
                      steps
                        .filter((_, index) => index !== stepIndex)
                        .map((item, index) => ({ ...item, position: index + 1 })),
                    ); }
                  }
                  aria-label={t("adminFlows.stepRemove", { index: String(step.position) })}
                  data-testid={`${testIdPrefix}-step-remove-${String(stepIndex)}`}
                >
                  <Trash2 />
                </Button>
              )}
            </span>
          </div>
          <StepActorPicker
            step={step}
            users={users}
            onToggle={(userId, checked) => {
              onChange(
                steps.map((item, index) =>
                  index === stepIndex
                    ? {
                        ...item,
                        actorIds: checked
                          ? item.actorIds.includes(userId)
                            ? item.actorIds
                            : [...item.actorIds, userId]
                          : item.actorIds.filter((id) => id !== userId),
                      }
                    : item,
                ),
              );
            }}
            testIdPrefix={`${testIdPrefix}-step-${String(stepIndex)}`}
          />
        </div>
      ))}
    </div>
  );
}

/**
 * Per-step actor capsules with a client-side search (owner ruling
 * 2026-10-31): the actor roster can be long, so the capsules filter on a
 * name substring while selections persist — filtering only hides capsules,
 * it never changes step.actorIds. Wrapped in a fixed-height ScrollArea so
 * long rosters stay bounded instead of stretching the modal.
 */
function StepActorPicker({
  step,
  users,
  onToggle,
  testIdPrefix,
}: {
  step: { position: number; actorIds: string[] };
  users: User[];
  onToggle: (userId: string, checked: boolean) => void;
  testIdPrefix: string;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const normalized = query.trim().toLowerCase();
  const visibleUsers = normalized === ""
    ? users
    : users.filter((user) =>
        (user.display_name ?? user.username).toLowerCase().includes(normalized) ||
        user.username.toLowerCase().includes(normalized),
      );
  return (
    <div className="flex flex-col gap-1.5">
      <Input
        value={query}
        onChange={(event) => { setQuery(event.target.value); }}
        placeholder={t("adminFlows.actorSearch")}
        aria-label={t("adminFlows.actorSearch")}
        className="h-7 text-xs"
        maxLength={64}
        data-testid={`${testIdPrefix}-search`}
      />
      <ScrollArea viewportClassName="max-h-36">
        <div className="flex flex-wrap gap-1 pr-3">
          {visibleUsers.map((user) => {
            const checked = step.actorIds.includes(user.id);
            return (
              <label
                key={user.id}
                className={`flex cursor-pointer items-center gap-1 rounded-full border px-2 py-0.5 text-xs ${
                  checked ? "bg-secondary" : ""
                }`}
              >
                <Checkbox
                  checked={checked}
                  onCheckedChange={(value) => { onToggle(user.id, value); }}
                  data-testid={`${testIdPrefix}-actor-${user.id}`}
                />
                {user.display_name}
              </label>
            );
          })}
          {visibleUsers.length === 0 && (
            <span className="text-muted-foreground text-xs">{t("adminFlows.actorSearchEmpty")}</span>
          )}
        </div>
      </ScrollArea>
    </div>
  );
}

export function ActorPicker({
  label,
  users,
  selected,
  onChange,
  testIdPrefix,
}: {
  label: string;
  users: User[];
  selected: string[];
  onChange: (ids: string[]) => void;
  testIdPrefix: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Label>{label}</Label>
        <label className="flex cursor-pointer items-center gap-1 text-xs text-muted-foreground">
          <Checkbox
            checked={selected.length > 0 && selected.length === users.length}
            indeterminate={selected.length > 0 && selected.length < users.length}
            onCheckedChange={() => {
              onChange(selected.length === users.length ? [] : users.map((user) => user.id));
            }}
            data-testid={`${testIdPrefix}-select-all`}
          />
          {t("adminFlows.selectAll")}
          <span className="tabular-nums" data-testid={`${testIdPrefix}-select-all-count`}>
            {String(selected.length)}/{String(users.length)}
          </span>
        </label>
      </div>
      <StepActorPicker
        step={{ position: 0, actorIds: selected }}
        users={users}
        onToggle={(userId, checked) => {
          onChange(
            checked
              ? selected.includes(userId)
                ? selected
                : [...selected, userId]
              : selected.filter((id) => id !== userId),
          );
        }}
        testIdPrefix={testIdPrefix}
      />
    </div>
  );
}

export function MaskingRulesSection({ flow }: { flow: Flow }) {
  const { t } = useTranslation();
  const rulesQuery = useFlowMaskingRules(flow.id, true);
  const replaceRule = useReplaceMaskingRule();
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [savedFor, setSavedFor] = useState<Record<string, string>>({});
  const [errorText, setErrorText] = useState<string | null>(null);

  const fetched: FlowMaskingRule[] = rulesQuery.data ?? [];
  // Vocabulary is configured per datasource of the flow (OpenAPI: per datasource of the flow), but the backend only returns
  // existing rule rows; pad empty rows for unconfigured datasources here so the first rule can be created in the UI.
  const capabilities = flow.query_capabilities ?? [];
  const rules: { datasource_id: string; datasource_name?: string; sensitive_columns: string[] }[] = [
    ...capabilities.map((capability) => {
      const existing = fetched.find((rule) => rule.datasource_id === capability.datasource_id);
      return { datasource_id: capability.datasource_id, datasource_name: capability.datasource_name, sensitive_columns: existing?.sensitive_columns ?? [] };
    }),
    ...fetched.filter((rule) => !capabilities.some((capability) => capability.datasource_id === rule.datasource_id)),
  ];

  return (
    <div className="flex flex-col gap-2 rounded-md border p-3" data-testid="flow-masking-rules">
      <div className="flex items-center gap-2">
        <EyeOff className="size-4" />
        <Label>{t("adminFlows.maskingRules")}</Label>
      </div>
      <p className="text-muted-foreground text-xs">{t("adminFlows.maskingRulesHint")}</p>
      {rulesQuery.isPending && <LoadingState variant="inline" />}
      {rules.length > 0 && (
        <ScrollArea viewportClassName="max-h-48">
          <div className="flex flex-col gap-2 pr-3">
            {rules.map((rule) => {
              const value = draft[rule.datasource_id] ?? rule.sensitive_columns.join(", ");
              const savedValue = savedFor[rule.datasource_id] ?? rule.sensitive_columns.join(", ");
              const dirty = value !== savedValue;
              return (
                <div key={rule.datasource_id} className="flex flex-col gap-1">
                  <span className="font-mono text-xs">{rule.datasource_name ?? rule.datasource_id}</span>
                  <div className="flex items-center gap-2">
                    <Textarea
                      value={value}
                      onChange={(event) => { setDraft((current) => ({ ...current, [rule.datasource_id]: event.target.value })); }}
                      className="min-h-8 text-xs"
                      data-testid={`flow-masking-rule-${rule.datasource_id}`}
                    />
                    <PendingButton
                      size="sm"
                      variant="outline"
                      pending={replaceRule.isPending}
                      disabled={!dirty}
                      onClick={() => {
                        setErrorText(null);
                        const columns = value
                          .split(",")
                          .map((column) => column.trim())
                          .filter((column) => column !== "");
                        replaceRule.mutate(
                          { flowId: flow.id, datasourceId: rule.datasource_id, body: { sensitive_columns: columns } },
                          {
                            onSuccess: () => {
                              setSavedFor((current) => ({ ...current, [rule.datasource_id]: value }));
                              toast.success(t("common.savedToast"));
                            },
                            onError: (error) => {
                              const msg = describeErrorText(describeError(error, "replaceFlowMaskingRule"));
                              setErrorText(msg);
                              toast.error(msg);
                            },
                          },
                        );
                      }}
                      data-testid={`flow-masking-save-${rule.datasource_id}`}
                    >
                      {t("common.save")}
                    </PendingButton>
                  </div>
                </div>
              );
            })}
          </div>
        </ScrollArea>
      )}
      {errorText !== null && (
        <Alert variant="destructive">
          <AlertDescription>{errorText}</AlertDescription>
        </Alert>
      )}
    </div>
  );
}
