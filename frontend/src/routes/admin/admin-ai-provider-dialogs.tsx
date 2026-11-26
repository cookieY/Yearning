import { useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  AiProvider,
  AiProviderWrite,
  CreateAiProviderRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { ProviderModelField } from "@/features/admin/provider-model-field";
import { ProviderPricingPanel, ProviderUsagePanel } from "@/features/admin/provider-pricing";
import {
  useCreateAiProvider,
  useDeleteAiProvider,
  useReplaceAiProvider,
} from "@/features/admin/use-admin";
import {
  applyProfileTransition,
  configurationWrite,
  emptyProviderForm,
  paramFieldErrors,
  providerFormForEdit,
  type ProviderFormState,
} from "@/routes/admin/admin-ai-provider-form-model";
import {
  ProviderGenerationTabContent,
  type ProviderParamErrorState,
} from "@/routes/admin/admin-ai-provider-generation-tab";
import { describeError } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
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
import { NumberInput } from "@/shared/components/ui/number-input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsStack, TabsTrigger } from "@/shared/components/ui/tabs";

export function ProviderFormDialog({
  editing,
  onClose,
}: {
  editing: AiProvider | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  // Keyed per open by the parent: state initializes from the edited row.
  const [form, setForm] = useState<ProviderFormState>(() =>
    editing === null ? emptyProviderForm() : providerFormForEdit(editing),
  );
  const createMutation = useCreateAiProvider();
  const replaceMutation = useReplaceAiProvider();
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);
  const [errorDetail, setErrorDetail] = useState<string | null>(null);
  // S008 per-field parameter errors (target specific fields when pre-submit validation fails, without sending a request).
  const [paramErrors, setParamErrors] = useState<ProviderParamErrorState>({
    systemPromptProfile: null,
    maxTokens: null,
    contextWindow: null,
    temperature: null,
  });

  const mutation = editing === null ? createMutation : replaceMutation;

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    setErrorDetail(null);
    // Parameter field validation runs before mutation: invalid values are targeted inline without sending a request or triggering a probe.
    // paramErrors stores resolved text (after RCP-20261102-S008, server 1001 responses also carry field-level
    // meta.fields, so English constraint messages and localized copy share the same slot directly).
    const params = paramFieldErrors(form);
    if (params.maxTokens !== null || params.contextWindow !== null || params.temperature !== null) {
      setParamErrors({
        systemPromptProfile: null,
        maxTokens: params.maxTokens !== null ? t(params.maxTokens) : null,
        contextWindow: params.contextWindow !== null ? t(params.contextWindow) : null,
        temperature: params.temperature !== null ? t(params.temperature) : null,
      });
      return;
    }
    setParamErrors({ systemPromptProfile: null, maxTokens: null, contextWindow: null, temperature: null });
    const apiKey =
      form.apiKey === ""
        ? undefined
        : ({ value: form.apiKey });
    if (editing === null) {
      if (apiKey === undefined) {
        setErrorKey("errors.VALIDATION_FAILED");
        return;
      }
      const write = {
        name: form.name,
        provider_kind: form.providerKind,
        base_url: form.baseUrl,
        model_name: form.modelName,
        enabled: form.enabled,
        selection_priority: Number(form.selectionPriority),
        api_key: apiKey,
        configuration: configurationWrite(form, true),
      } as CreateAiProviderRequest;
      createMutation.mutate(write, {
        onSuccess: () => {
          appToast.success(t("admin.providers.createTitle"), {
            description: write.name,
          });
          onClose();
        },
        onError: (error) => {
          const display = describeError(error, "createAiProvider");
          setErrorKey(display.messageKey);
          setErrorRequestId(display.requestId);
          setErrorDetail(display.detail);
          setParamErrors({
            systemPromptProfile: display.fields.system_prompt_profile?.[0] ?? null,
            maxTokens: display.fields.max_tokens?.[0] ?? null,
            contextWindow: display.fields.context_window?.[0] ?? null,
            temperature: display.fields.temperature?.[0] ?? null,
          });
          appToast.error(t(display.messageKey));
        },
      });
    } else {
      const write: AiProviderWrite = {
        name: form.name,
        provider_kind: form.providerKind,
        base_url: form.baseUrl,
        model_name: form.modelName,
        enabled: form.enabled,
        selection_priority: Number(form.selectionPriority),
        ...(apiKey === undefined ? {} : { api_key: apiKey }),
        configuration: configurationWrite(form, false),
      };
      replaceMutation.mutate(
        { id: editing.id, version: editing.version, write },
        {
          onSuccess: () => {
            appToast.success(t("admin.providers.editTitle"), {
              description: write.name,
            });
            onClose();
          },
          onError: (error) => {
            const display = describeError(error, "replaceAiProvider");
            setErrorKey(display.messageKey);
            setErrorRequestId(display.requestId);
            setErrorDetail(display.detail);
            setParamErrors({
              systemPromptProfile: display.fields.system_prompt_profile?.[0] ?? null,
              maxTokens: display.fields.max_tokens?.[0] ?? null,
              contextWindow: display.fields.context_window?.[0] ?? null,
              temperature: display.fields.temperature?.[0] ?? null,
            });
            appToast.error(t(display.messageKey));
          },
        },
      );
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>
            {editing === null ? t("admin.providers.createTitle") : t("admin.providers.editTitle")}
          </DialogTitle>
          <DialogDescription>{t("admin.providers.formDescription")}</DialogDescription>
        </DialogHeader>
        {/* S008 three groups (owner 2026-10-31 FE package): Connection / Generation Parameters / Reference Pricing
            (rates + cost stats in the same group, PRD UI-01/02). Create mode has no provider id,
            so the pricing/usage group renders a placeholder notice. */}
        <Tabs defaultValue="connection">
          <TabsList className="-my-1 max-w-full overflow-x-auto overflow-y-hidden py-1">
            <TabsTrigger value="connection" data-testid="provider-tab-connection">
              {t("admin.providers.tabs.connection")}
            </TabsTrigger>
            <TabsTrigger value="generation" data-testid="provider-tab-generation">
              {t("admin.providers.tabs.generation")}
            </TabsTrigger>
            <TabsTrigger value="pricing" data-testid="provider-tab-pricing">
              {t("admin.providers.tabs.pricing")}
            </TabsTrigger>
          </TabsList>

          <TabsStack>
          <TabsContent value="connection" stacked>
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="provider-name">{t("admin.providers.name")}</Label>
            <Input
              id="provider-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              data-testid="provider-name"
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label>{t("admin.providers.kind")}</Label>
            {/* §18.68: provider kind switched to a controlled select — runtime Dial only has the OpenAI Compatible
                adapter (zero-branch passthrough of provider_kind), so the option set honestly reflects capability;
                future protocols can be added here. items provides the display mapping, with explicit SelectItem children
                (§18.48 final pattern; empty SelectContent = zero options in popup). */}
            <Select
              value={form.providerKind}
              onValueChange={(next) => {
                if (next !== null) setForm({ ...form, providerKind: next });
              }}
              items={{ openai_compatible: t("admin.providers.kindOpenaiCompatible") }}
            >
              <SelectTrigger data-testid="provider-kind" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="openai_compatible">
                  {t("admin.providers.kindOpenaiCompatible")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1 md:col-span-2">
            <Label htmlFor="provider-base-url">{t("admin.providers.baseUrl")}</Label>
            <Input
              id="provider-base-url"
              value={form.baseUrl}
              onChange={(event) => { setForm({ ...form, baseUrl: event.target.value }); }}
              placeholder="https://api.example.com/v1"
              data-testid="provider-base-url"
            />
            <p className="text-muted-foreground text-xs" data-testid="provider-base-url-hint">
              {t("admin.providers.baseUrlHint")}
            </p>
          </div>
          {/* §18.76-B: API Key moved above Model — catalog autocomplete depends on both being present,
              so visual order matches the filling order. */}
          <div className="flex flex-col gap-1 md:col-span-2">
            <Label htmlFor="provider-api-key">{t("admin.providers.apiKey")}</Label>
            <PasswordInput
              id="provider-api-key"
              value={form.apiKey}
              onChange={(event) => { setForm({ ...form, apiKey: event.target.value }); }}
              autoComplete="new-password"
              placeholder={
                editing !== null && editing.api_key_configured
                  ? t("admin.providers.apiKeyKeepPlaceholder")
                  : ""
              }
              data-testid="provider-api-key"
            />
            {editing !== null ? (
              <p className="text-muted-foreground text-xs">
                {t("admin.providers.apiKeyConfigured", {
                  state: editing.api_key_configured
                    ? t("admin.providers.configured")
                    : t("admin.providers.notConfigured"),
                })}
              </p>
            ) : null}
          </div>
          <ProviderModelField
            modelName={form.modelName}
            baseUrl={form.baseUrl}
            apiKey={form.apiKey}
            editingProviderId={editing?.id ?? null}
            onModelChange={(nextModel) => {
              setForm((prev) => applyProfileTransition(prev, nextModel, prev.systemPromptProfile, editing === null));
            }}
          />
          <div className="flex flex-col gap-1">
            <Label htmlFor="provider-priority">{t("admin.providers.priority")}</Label>
            <NumberInput
              id="provider-priority"
              min={1}
              max={100}
              value={form.selectionPriority}
              onChange={(event) => { setForm({ ...form, selectionPriority: event.target.value }); }}
              data-testid="provider-priority"
            />
            <p className="text-muted-foreground text-xs">{t("admin.providers.priorityHint")}</p>
          </div>
          <div className="flex items-center gap-2 md:col-span-2">
            <input
              type="checkbox"
              id="provider-enabled"
              checked={form.enabled}
              onChange={(event) => { setForm({ ...form, enabled: event.target.checked }); }}
              className="size-4"
              data-testid="provider-enabled"
            />
            <Label htmlFor="provider-enabled" className="cursor-pointer">
              {t("admin.providers.enabled")}
            </Label>
          </div>
          </div>
          </TabsContent>

          <TabsContent value="generation" stacked>
            <ProviderGenerationTabContent
              form={form}
              setForm={setForm}
              paramErrors={paramErrors}
              setParamErrors={setParamErrors}
              isCreate={editing === null}
            />
          </TabsContent>

          {/* Pricing/usage panel content is long and its table is wide: absolute positioning detaches it from grid size calculation so height is determined
              by the Connection / Generation Parameters panels, scrolling within that fixed height without stretching the dialog
              when switching tabs (owner 2026-11-03). */}
          <TabsContent value="pricing" stacked className="relative">
            {/* Unified scrollbars (owner 2026-11-03): pricing/usage panel scrolls with
                ScrollArea overlay thumb (was native overflow-auto). */}
            {editing === null ? (
              <p className="text-muted-foreground text-sm" data-testid="provider-pricing-create-hint">
                {t("admin.providers.pricing.empty")}
              </p>
            ) : (
              <ScrollArea className="absolute inset-0">
                <div className="flex flex-col gap-5 pr-4">
                  <ProviderPricingPanel providerId={editing.id} />
                  <ProviderUsagePanel providerId={editing.id} />
                </div>
              </ScrollArea>
            )}
          </TabsContent>
          </TabsStack>
        </Tabs>

        {errorKey !== null ? (
          <Alert variant="destructive" data-testid="provider-form-error">
            <AlertTitle>{t(errorKey)}</AlertTitle>
            <AlertDescription>
              {/* §18.75-A: display upstream refusal snapshot verbatim (meta.detail, capped at
                  300 chars server-side) so operators can immediately tell "wrong URL / invalid key / unsupported model"
                  apart instead of seeing only a generic unavailable notice. */}
              {errorDetail !== null ? (
                <p className="mb-2 break-all font-mono" data-testid="provider-form-error-detail">
                  {errorDetail}
                </p>
              ) : null}
              {errorRequestId !== null ? `request_id: ${errorRequestId}` : null}
            </AlertDescription>
          </Alert>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton pending={mutation.isPending} onClick={submit} data-testid="provider-submit">
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function ProviderDeleteDialog({
  provider,
  onClose,
}: {
  provider: AiProvider;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const deleteMutation = useDeleteAiProvider();
  const [errorKey, setErrorKey] = useState<string | null>(null);
  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("admin.providers.deleteTitle")}</DialogTitle>
          <DialogDescription>{t("admin.providers.deleteDescription", { name: provider.name })}</DialogDescription>
        </DialogHeader>
        {errorKey !== null ? (
          <Alert variant="destructive" data-testid="provider-delete-error">
            <AlertTitle>{t(errorKey)}</AlertTitle>
            <AlertDescription>{errorKey === "errors.generic.business" ? t("admin.providers.deleteErrorHint") : null}</AlertDescription>
          </Alert>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={deleteMutation.isPending}
            onClick={() => {
              deleteMutation.mutate(
                { id: provider.id, version: provider.version },
                {
                  onSuccess: () => {
                    appToast.success(t("common.delete"), {
                      description: provider.name,
                    });
                    onClose();
                  },
                  onError: (error) => {
                    const display = describeError(error, "deleteAiProvider");
                    setErrorKey(display.messageKey);
                    appToast.error(t(display.messageKey));
                  },
                },
              );
            }}
            data-testid="provider-delete-confirm"
          >
            {t("common.delete")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
