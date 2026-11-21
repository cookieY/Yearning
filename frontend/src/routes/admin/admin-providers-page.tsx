import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Bot, Cpu, Info, KeyRound, ListOrdered, Pencil, PlugZap, Plus, Trash2, Undo2 } from "lucide-react";
import type {
  AiProvider,
  AiProviderWrite,
  CreateAiProviderRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { listAiProviderModels } from "@/api/generated/client/administration/administration";
import {
  useAiProviders,
  useCreateAiProvider,
  useDeleteAiProvider,
  useReplaceAiProvider,
  useTestAiProviderConnection,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { describeError } from "@/shared/api/error-display";
import { unwrapApi } from "@/shared/api/mutator";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { Card } from "@/shared/components/ui/card";
import { cn } from "@/shared/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Input } from "@/shared/components/ui/input";
import { NumberInput } from "@/shared/components/ui/number-input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { Tabs, TabsContent, TabsList, TabsStack, TabsTrigger } from "@/shared/components/ui/tabs";
import { ProviderPricingPanel, ProviderUsagePanel } from "@/features/admin/provider-pricing";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Label } from "@/shared/components/ui/label";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { PendingButton } from "@/shared/components/pending-button";
import {
  resolvePromptProfile,
  type PromptProfileSetting,
} from "@/features/admin/prompt-profile";

/**
 * Provider Management (route /admin/review-engine/providers; UI spec §5.3 puts
 * provider administration in the review-engine group). The list renders the
 * primary/backup order exactly as the runtime consumes it —
 * selection_priority ascending (backend provider/chain.go:3), index 0 is
 * the primary. The API key is write-only: create requires it once, replace
 * keeps the stored key unless a new one is typed, and every read face
 * exposes only api_key_configured. The privacy/output contract hashes are
 * NOT form fields: the platform derives both from code constants
 * (provider contract_hash.go) and pins them per row at write time —
 * identical fingerprints across providers are what authorizes fallback
 * (chain.go gate) (R008/R005 boundary: no per-order budget here — that
 * is the ai-budget settings namespace).
 */

interface ProviderFormState {
  name: string;
  providerKind: string;
  baseUrl: string;
  modelName: string;
  enabled: boolean;
  selectionPriority: string;
  apiKey: string;
  // S008 generation parameters: string state holds three states — "" = provider default / auto-detect (omitted), while temperature
  // "0" is an explicit zero (still sent as 0 after Number conversion, never collapsed into default).
  systemPromptProfile: PromptProfileSetting;
  reasoningEffort: "" | "low" | "medium" | "high";
  maxTokens: string;
  contextWindow: string;
  temperature: string;
  /** Initial parameter snapshot: per-field undo (Revert Changes) restores to this; separated from "Use Provider Default"
      (clearing the value) semantics (deliverable: separate reverting edits from using provider defaults). */
  initialParams: {
    systemPromptProfile: PromptProfileSetting;
    reasoningEffort: "" | "low" | "medium" | "high";
    maxTokens: string;
    contextWindow: string;
    temperature: string;
  };
}

const PROFILE_DEFAULT_PARAMS: Record<"default" | "san", { maxTokens: string; contextWindow: string }> = {
  default: { maxTokens: "4096", contextWindow: "128000" },
  san: { maxTokens: "4096", contextWindow: "8192" },
};

function applyProfileTransition(
  prev: ProviderFormState,
  nextModelName: string,
  nextProfileSetting: PromptProfileSetting,
  isCreate: boolean,
): ProviderFormState {
  const prevProfile = resolvePromptProfile(prev.modelName, prev.systemPromptProfile).profile;
  const nextProfile = resolvePromptProfile(nextModelName, nextProfileSetting).profile;
  if (prevProfile === nextProfile) {
    return { ...prev, modelName: nextModelName, systemPromptProfile: nextProfileSetting };
  }
  const prevDefaults = PROFILE_DEFAULT_PARAMS[prevProfile];
  const nextDefaults = PROFILE_DEFAULT_PARAMS[nextProfile];
  const fallback = isCreate
    ? PROFILE_DEFAULT_PARAMS.default
    : { maxTokens: prev.initialParams.maxTokens, contextWindow: prev.initialParams.contextWindow };

  const resolveField = (currentValue: string, prevDefault: string, nextDefault: string, revertValue: string): string => {
    if (nextProfile === "san") {
      return currentValue === "" || currentValue === prevDefault ? nextDefault : currentValue;
    }
    if (currentValue === prevDefault || (isCreate && currentValue === "")) {
      return revertValue;
    }
    return currentValue;
  };

  return {
    ...prev,
    modelName: nextModelName,
    systemPromptProfile: nextProfileSetting,
    maxTokens: resolveField(prev.maxTokens, prevDefaults.maxTokens, nextDefaults.maxTokens, fallback.maxTokens),
    contextWindow: resolveField(
      prev.contextWindow,
      prevDefaults.contextWindow,
      nextDefaults.contextWindow,
      fallback.contextWindow,
    ),
  };
}

function emptyProviderForm(): ProviderFormState {
  return {
    name: "",
    providerKind: "openai_compatible",
    baseUrl: "",
    modelName: "",
    enabled: true,
    selectionPriority: "10",
    apiKey: "",
    systemPromptProfile: "",
    reasoningEffort: "",
    maxTokens: PROFILE_DEFAULT_PARAMS.default.maxTokens,
    contextWindow: PROFILE_DEFAULT_PARAMS.default.contextWindow,
    temperature: "",
    initialParams: {
      systemPromptProfile: "",
      reasoningEffort: "",
      maxTokens: PROFILE_DEFAULT_PARAMS.default.maxTokens,
      contextWindow: PROFILE_DEFAULT_PARAMS.default.contextWindow,
      temperature: "",
    },
  };
}

/**
 * S008 parameter write semantics (M2): if all user-governed fields keep their initial values -> omit the entire
 * configuration key (retaining stored config); if any field is modified (or on Create with non-empty defaults) ->
 * supply an explicit object for full replacement, where keys left empty clear that user override (i.e. "Use Provider
 * Default" / "Auto-detect"). When effective profile is "san", reasoning_effort is hidden and omitted so SAN requests
 * never carry an unsupported reasoning_effort setting. A trimmed "0" for temperature is preserved as explicit 0.
 */
function configurationWrite(form: ProviderFormState, isCreate: boolean): AiProviderWrite["configuration"] {
  const initial = form.initialParams;
  const effectiveProfile = resolvePromptProfile(form.modelName, form.systemPromptProfile).profile;
  const effectiveReasoningEffort = effectiveProfile === "san" ? "" : form.reasoningEffort;
  const hasCreateValues =
    isCreate &&
    (form.systemPromptProfile !== "" ||
      effectiveReasoningEffort !== "" ||
      form.maxTokens !== "" ||
      form.contextWindow !== "" ||
      form.temperature !== "");
  const touched =
    hasCreateValues ||
    form.systemPromptProfile !== initial.systemPromptProfile ||
    effectiveReasoningEffort !== initial.reasoningEffort ||
    form.maxTokens !== initial.maxTokens ||
    form.contextWindow !== initial.contextWindow ||
    form.temperature !== initial.temperature;
  if (!touched) return undefined;
  const configuration: NonNullable<AiProviderWrite["configuration"]> = {};
  if (form.systemPromptProfile !== "") configuration.system_prompt_profile = form.systemPromptProfile;
  if (effectiveReasoningEffort !== "") configuration.reasoning_effort = effectiveReasoningEffort;
  if (form.maxTokens !== "") {
    const parsed = Number(form.maxTokens);
    if (Number.isFinite(parsed) && form.maxTokens.trim() !== "") configuration.max_tokens = parsed;
  }
  if (form.contextWindow !== "") {
    const parsed = Number(form.contextWindow);
    if (Number.isFinite(parsed) && form.contextWindow.trim() !== "") configuration.context_window = parsed;
  }
  if (form.temperature !== "") {
    const parsed = Number(form.temperature);
    if (Number.isFinite(parsed) && form.temperature.trim() !== "") configuration.temperature = parsed;
  }
  return configuration;
}

/**
 * S008 parameter field validation (PRD §6 "save errors target specific fields"; backend
 * ValidateProviderConfiguration returns a generic 1001 without field names, so field targeting mirrors the contract
 * client-side): empty = provider default (unvalidated); max_tokens must be an integer 256..200000;
 * context_window must be a positive integer 1..2147483647;
 * temperature must be a number 0..2 ("0" is an explicit zero and valid). Returns an i18n message key.
 */
function paramFieldErrors(form: ProviderFormState): {
  maxTokens: string | null;
  contextWindow: string | null;
  temperature: string | null;
} {
  const parsedMax = form.maxTokens.trim() === "" ? null : Number(form.maxTokens);
  const parsedContext = form.contextWindow.trim() === "" ? null : Number(form.contextWindow);
  const parsedTemp = form.temperature.trim() === "" ? null : Number(form.temperature);
  return {
    maxTokens:
      parsedMax === null || (Number.isInteger(parsedMax) && parsedMax >= 256 && parsedMax <= 200000)
        ? null
        : "admin.providers.gen.maxTokensInvalid",
    contextWindow:
      parsedContext === null || (Number.isInteger(parsedContext) && parsedContext >= 1 && parsedContext <= 2147483647)
        ? null
        : "admin.providers.gen.contextWindowInvalid",
    temperature:
      parsedTemp === null || (Number.isFinite(parsedTemp) && parsedTemp >= 0 && parsedTemp <= 2)
        ? null
        : "admin.providers.gen.temperatureInvalid",
  };
}

function providerFormForEdit(provider: AiProvider): ProviderFormState {
  return {
    name: provider.name,
    providerKind: provider.provider_kind,
    baseUrl: provider.base_url,
    modelName: provider.model_name,
    enabled: provider.enabled,
    selectionPriority: String(provider.selection_priority),
    // Never prefilled: an empty key field on edit means "keep the stored
    // key" (gate: Secrets are never prefilled).
    apiKey: "",
    // S008 full parameter backfill: stored values take precedence, unconfigured = provider default / auto-detect (empty string).
    systemPromptProfile: provider.configuration?.system_prompt_profile ?? "",
    reasoningEffort: provider.configuration?.reasoning_effort ?? "",
    maxTokens: provider.configuration?.max_tokens != null ? String(provider.configuration.max_tokens) : "",
    contextWindow: provider.configuration?.context_window != null ? String(provider.configuration.context_window) : "",
    temperature: provider.configuration?.temperature != null ? String(provider.configuration.temperature) : "",
    initialParams: {
      systemPromptProfile: provider.configuration?.system_prompt_profile ?? "",
      reasoningEffort: provider.configuration?.reasoning_effort ?? "",
      maxTokens: provider.configuration?.max_tokens != null ? String(provider.configuration.max_tokens) : "",
      contextWindow: provider.configuration?.context_window != null ? String(provider.configuration.context_window) : "",
      temperature: provider.configuration?.temperature != null ? String(provider.configuration.temperature) : "",
    },
  };
}

function ProviderFormDialog({
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
  const [paramErrors, setParamErrors] = useState<{
    systemPromptProfile: string | null;
    maxTokens: string | null;
    contextWindow: string | null;
    temperature: string | null;
  }>({
    systemPromptProfile: null,
    maxTokens: null,
    contextWindow: null,
    temperature: null,
  });

  // §18.76-B: model catalog autocomplete. Automatically fetches once base_url + api_key are present (or reusing stored
  // key in edit mode); 400ms debounce + sequence counter prevent stale writebacks. The catalog is only a suggestion list
  // and the input always allows manual entry; fetch failures silently degrade to manual entry (the probe still surfaces
  // the real error on save). Results are cached by key and loading is derived from key mismatch — no synchronous
  // setState inside the effect (react-hooks/set-state-in-effect).
  const catalogKey = `${form.baseUrl.trim()}\u0000${form.apiKey.trim()}\u0000${editing?.id ?? ""}`;
  const canFetchCatalog = form.baseUrl.trim() !== "" && (form.apiKey.trim() !== "" || editing !== null);
  const [catalog, setCatalog] = useState<{
    key: string;
    state: "done" | "failed";
    items: string[];
  } | null>(null);
  const catalogSeq = useRef(0);
  const [suggestOpen, setSuggestOpen] = useState(false);
  useEffect(() => {
    if (!canFetchCatalog) return;
    const seq = ++catalogSeq.current;
    const timer = setTimeout(() => {
      void unwrapApi(
        listAiProviderModels({
          base_url: form.baseUrl.trim(),
          ...(form.apiKey.trim() !== "" ? { api_key: form.apiKey.trim() } : {}),
          ...(form.apiKey.trim() === "" && editing !== null ? { provider_id: editing.id } : {}),
        }),
      )
        .then((res) => {
          if (catalogSeq.current !== seq) return;
          setCatalog({ key: catalogKey, state: "done", items: res.items });
        })
        .catch(() => {
          if (catalogSeq.current !== seq) return;
          setCatalog({ key: catalogKey, state: "failed", items: [] });
        });
    }, 400);
    return () => {
      clearTimeout(timer);
      catalogSeq.current += 1;
    };
  }, [catalogKey, canFetchCatalog, editing, form.baseUrl, form.apiKey]);
  const catalogResult = catalog !== null && catalog.key === catalogKey ? catalog : null;
  const catalogView = !canFetchCatalog
    ? "idle"
    : catalogResult === null
      ? "loading"
      : catalogResult.state;
  // tsc does not preserve narrowing across JSX clauses, so materialize the items array first.
  const catalogItems = catalogView === "done" ? catalogResult?.items ?? [] : [];

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

  const resolvedProfile = resolvePromptProfile(form.modelName, form.systemPromptProfile);
  const modelDisplay = form.modelName.trim() === "" ? "—" : form.modelName.trim();
  const resolvedProfileMessage =
    resolvedProfile.source === "explicit"
      ? resolvedProfile.profile === "san"
        ? t("admin.providers.gen.promptProfileResolvedExplicitSan")
        : t("admin.providers.gen.promptProfileResolvedExplicitDefault")
      : resolvedProfile.profile === "san"
        ? t("admin.providers.gen.promptProfileResolvedAutoSan", { model: modelDisplay })
        : t("admin.providers.gen.promptProfileResolvedAutoDefault", { model: modelDisplay });

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
          <div className="flex flex-col gap-1">
            <Label htmlFor="provider-model">{t("admin.providers.model")}</Label>
            {/* §18.76-B live verification fix: relative wraps only the input (so the top-full panel hangs directly below
                the input without drifting with the hint line); open/close follows input focus/blur, options select via
                onMouseDown (preventDefault stops blur from swallowing the click first), and selecting collapses the panel. */}
            <div className="relative">
              <Input
                id="provider-model"
                value={form.modelName}
                onChange={(event) => {
                  const nextModel = event.target.value;
                  setForm((prev) => applyProfileTransition(prev, nextModel, prev.systemPromptProfile, editing === null));
                }}
                onFocus={() => { setSuggestOpen(true); }}
                onBlur={() => { setSuggestOpen(false); }}
                data-testid="provider-model"
              />
              {suggestOpen && catalogItems.length > 0 ? (
                /* Unified scrollbars (owner 2026-11-03): model suggestion dropdown uses ScrollArea overlay thumb. */
                <ScrollArea
                  className="absolute top-full left-0 right-0 z-50 mt-1 rounded-md border bg-popover"
                  viewportClassName="max-h-48"
                  data-testid="provider-model-options"
                >
                  <div>
                  {catalogItems.map((model) => (
                    <button
                      key={model}
                      type="button"
                      className="w-full px-2.5 py-1.5 text-left text-xs hover:bg-accent"
                      onMouseDown={(event) => {
                        event.preventDefault();
                        setForm((prev) => applyProfileTransition(prev, model, prev.systemPromptProfile, editing === null));
                        setSuggestOpen(false);
                      }}
                      data-testid={`provider-model-option-${model}`}
                    >
                      {model}
                    </button>
                  ))}
                  </div>
                </ScrollArea>
              ) : null}
            </div>
            <p className="text-muted-foreground text-xs">{t("admin.providers.modelCatalogHint")}</p>
            {/* Status row reserves a fixed single-line height: showing or hiding the failure hint does not shift layout (loading no longer shows hint copy, owner 2026-11-03),
                so opening the dialog no longer jitters from this line appearing/disappearing (owner 2026-11-03). */}
            <div className="min-h-4">
              {catalogView === "failed" ? (
                <p className="text-muted-foreground text-xs" data-testid="provider-model-catalog-failed">
                  {t("admin.providers.modelCatalogFailed")}
                </p>
              ) : null}
            </div>
          </div>
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
          <div className="flex flex-col gap-4" data-testid="provider-generation-panel">
            <p className="text-muted-foreground text-xs">{t("admin.providers.gen.hint")}</p>
            <div className="flex flex-col gap-1">
              <Label htmlFor="provider-gen-prompt-profile">{t("admin.providers.gen.promptProfile")}</Label>
              <div className="flex items-center gap-2">
                <Select
                  value={form.systemPromptProfile === "" ? "auto" : form.systemPromptProfile}
                  onValueChange={(next) => {
                    if (next === null) return;
                    const value: PromptProfileSetting = next === "auto" ? "" : (next as "default" | "san");
                    setParamErrors((current) => ({ ...current, systemPromptProfile: null }));
                    setForm((prev) => applyProfileTransition(prev, prev.modelName, value, editing === null));
                  }}
                >
                  <SelectTrigger
                    id="provider-gen-prompt-profile"
                    data-testid="provider-gen-prompt-profile"
                    aria-invalid={paramErrors.systemPromptProfile !== null}
                    className="min-w-0 flex-1"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="auto">{t("admin.providers.gen.promptProfileAuto")}</SelectItem>
                    <SelectItem value="default">{t("admin.providers.gen.promptProfileDefault")}</SelectItem>
                    <SelectItem value="san">{t("admin.providers.gen.promptProfileSan")}</SelectItem>
                  </SelectContent>
                </Select>
                <ParamRevertButton
                  dirty={form.systemPromptProfile !== form.initialParams.systemPromptProfile}
                  initial={form.initialParams.systemPromptProfile}
                  testId="provider-gen-prompt-profile-revert"
                  onRevert={(value) => {
                    setParamErrors((current) => ({ ...current, systemPromptProfile: null }));
                    setForm((prev) =>
                      applyProfileTransition(prev, prev.modelName, value as PromptProfileSetting, editing === null),
                    );
                  }}
                />
              </div>
              <p className="text-muted-foreground text-xs" data-testid="provider-gen-prompt-profile-status">
                {resolvedProfileMessage}
              </p>
              {paramErrors.systemPromptProfile !== null && (
                <p className="text-destructive text-xs" data-testid="provider-gen-prompt-profile-error">
                  {paramErrors.systemPromptProfile}
                </p>
              )}
            </div>
            {resolvedProfile.profile !== "san" && (
              <div className="flex flex-col gap-1">
                <Label htmlFor="provider-gen-reasoning">{t("admin.providers.gen.reasoningEffort")}</Label>
                <div className="flex items-center gap-2">
                  <Select
                    value={form.reasoningEffort === "" ? "default" : form.reasoningEffort}
                    onValueChange={(next) => {
                      if (next === null) return;
                      const value = next === "default" ? "" : (next as "low" | "medium" | "high");
                      setForm({ ...form, reasoningEffort: value });
                    }}
                  >
                    <SelectTrigger id="provider-gen-reasoning" data-testid="provider-gen-reasoning" className="w-48">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="default">{t("admin.providers.gen.reasoningEffortDefault")}</SelectItem>
                      <SelectItem value="low">low</SelectItem>
                      <SelectItem value="medium">medium</SelectItem>
                      <SelectItem value="high">high</SelectItem>
                    </SelectContent>
                  </Select>
                  <ParamRevertButton
                    dirty={form.reasoningEffort !== form.initialParams.reasoningEffort}
                    initial={form.initialParams.reasoningEffort}
                    onRevert={(value) => { setForm({ ...form, reasoningEffort: value as ProviderFormState["reasoningEffort"] }); }}
                  />
                </div>
              </div>
            )}
            <div className="flex flex-col gap-1">
              <Label htmlFor="provider-gen-max-tokens">{t("admin.providers.gen.maxTokens")}</Label>
              <div className="flex items-center gap-2">
                <NumberInput
                  id="provider-gen-max-tokens"
                  min={256}
                  max={200000}
                  value={form.maxTokens}
                  onChange={(event) => {
                    setParamErrors((current) => ({ ...current, maxTokens: null }));
                    setForm({ ...form, maxTokens: event.target.value });
                  }}
                  placeholder={t("admin.providers.gen.reasoningEffortDefault")}
                  aria-invalid={paramErrors.maxTokens !== null}
                  data-testid="provider-gen-max-tokens"
                  className="font-mono"
                  wrapperClassName="w-60 shrink-0"
                />
                <span className="text-muted-foreground text-xs">{t("admin.providers.gen.maxTokensRange")}</span>
                <ParamRevertButton
                  dirty={form.maxTokens !== form.initialParams.maxTokens}
                  initial={form.initialParams.maxTokens}
                  onRevert={(value) => { setForm({ ...form, maxTokens: value }); }}
                />
              </div>
              {paramErrors.maxTokens !== null && (
                <p className="text-destructive text-xs" data-testid="provider-gen-max-tokens-error">
                  {paramErrors.maxTokens}
                </p>
              )}
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="provider-gen-context-window">{t("admin.providers.gen.contextWindow")}</Label>
              <div className="flex items-center gap-2">
                <NumberInput
                  id="provider-gen-context-window"
                  min={1}
                  max={2147483647}
                  value={form.contextWindow}
                  onChange={(event) => {
                    setParamErrors((current) => ({ ...current, contextWindow: null }));
                    setForm({ ...form, contextWindow: event.target.value });
                  }}
                  placeholder={t("admin.providers.gen.contextWindowUnspecified")}
                  aria-invalid={paramErrors.contextWindow !== null}
                  data-testid="provider-gen-context-window"
                  className="font-mono"
                  wrapperClassName="w-60 shrink-0"
                />
                <span className="text-muted-foreground text-xs">{t("admin.providers.gen.contextWindowHint")}</span>
                <ParamRevertButton
                  dirty={form.contextWindow !== form.initialParams.contextWindow}
                  initial={form.initialParams.contextWindow}
                  onRevert={(value) => { setForm({ ...form, contextWindow: value }); }}
                  testId="provider-gen-context-window-revert"
                />
              </div>
              {paramErrors.contextWindow !== null && (
                <p className="text-destructive text-xs" data-testid="provider-gen-context-window-error">
                  {paramErrors.contextWindow}
                </p>
              )}
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="provider-gen-temperature">{t("admin.providers.gen.temperature")}</Label>
              <div className="flex items-center gap-2">
                <NumberInput
                  id="provider-gen-temperature"
                  min={0}
                  max={2}
                  step={0.1}
                  value={form.temperature}
                  onChange={(event) => {
                    setParamErrors((current) => ({ ...current, temperature: null }));
                    setForm({ ...form, temperature: event.target.value });
                  }}
                  placeholder={t("admin.providers.gen.reasoningEffortDefault")}
                  aria-invalid={paramErrors.temperature !== null}
                  data-testid="provider-gen-temperature"
                  className="font-mono"
                  wrapperClassName="w-60 shrink-0"
                />
                <span className="text-muted-foreground text-xs">{t("admin.providers.gen.temperatureRange")}</span>
                <ParamRevertButton
                  dirty={form.temperature !== form.initialParams.temperature}
                  initial={form.initialParams.temperature}
                  onRevert={(value) => { setForm({ ...form, temperature: value }); }}
                />
              </div>
              {paramErrors.temperature !== null && (
                <p className="text-destructive text-xs" data-testid="provider-gen-temperature-error">
                  {paramErrors.temperature}
                </p>
              )}
            </div>
          </div>
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

/** Per-field undo (restores initial snapshot); separated from "Use Provider Default" (clearing the value). */
function ParamRevertButton({
  dirty,
  initial,
  onRevert,
  testId = "provider-gen-revert",
}: {
  dirty: boolean;
  initial: string;
  onRevert: (initial: string) => void;
  testId?: string;
}) {
  const { t } = useTranslation();
  if (!dirty) return null;
  return (
    <Button
      type="button"
      size="sm"
      variant="ghost"
      onClick={() => { onRevert(initial); }}
      data-testid={testId}
      aria-label={t("admin.providers.gen.revert")}
      title={t("admin.providers.gen.revert")}
    >
      <Undo2 aria-hidden />
    </Button>
  );
}

function ProviderCard({
  provider,
  ordinal,
  onEdit,
  onAskDelete,
}: {
  provider: AiProvider;
  ordinal: number;
  onEdit: () => void;
  onAskDelete: () => void;
}) {
  const { t } = useTranslation();
  const testMutation = useTestAiProviderConnection();
  // RCP-20261017 item 019: connection test returns a synchronous {ok} result — pending/success/failure states
  // render directly; failure carries request_id and upstream refusal reason (same channel as §18.75-A), with no
  // Task polling path anymore (the old Task shape left success silent and failure without detail).
  const [testOk, setTestOk] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorDetail, setErrorDetail] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);

  return (
    <div
      data-testid={`provider-row-${provider.id}`}
      data-row-key={provider.id}
      className="flex min-w-0 flex-col gap-2.5 rounded-xl border border-border bg-card p-4"
    >
      <div className="flex min-w-0 items-center gap-2.5">
        <div
          className={cn(
            "flex size-8 shrink-0 items-center justify-center rounded-lg",
            provider.enabled ? "bg-tone-emerald/10 text-tone-emerald" : "bg-muted text-muted-foreground",
          )}
        >
          <Cpu className="size-4" aria-hidden />
        </div>
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground" title={provider.name}>{provider.name}</span>
        <div className="shrink-0">
          <TableRowActions testId={`provider-actions-${provider.id}`}>
            <DropdownMenuItem
              disabled={testMutation.isPending}
              onClick={() => {
                setTestOk(false);
                setErrorKey(null);
                setErrorDetail(null);
                setErrorRequestId(null);
                testMutation.mutate(
                  { id: provider.id },
                  {
                    onSuccess: () => {
                      setTestOk(true);
                      appToast.success(t("admin.providers.testOk"), {
                        description: provider.name,
                      });
                    },
                    onError: (error) => {
                      const display = describeError(error, "testAiProviderConnection");
                      setErrorKey(display.messageKey);
                      setErrorDetail(display.detail);
                      setErrorRequestId(display.requestId);
                      appToast.error(t(display.messageKey));
                    },
                  },
                );
              }}
              data-testid={`provider-test-${provider.id}`}
            >
              <PlugZap /> {t("admin.datasources.testConnection")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={onEdit} data-testid={`provider-edit-${provider.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onClick={onAskDelete} data-testid={`provider-delete-${provider.id}`}>
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </TableRowActions>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant={ordinal === 0 ? "default" : "outline"} className="h-5 px-2" data-testid={`provider-role-${provider.id}`}>
          {ordinal === 0 ? t("admin.providers.primary") : t("admin.providers.backup")}
        </Badge>
        <StatusBadge
          tone={provider.enabled ? "emerald" : "stone"}
          data-testid={`provider-enabled-${provider.id}`}
        >
          {provider.enabled ? t("admin.providers.enabled") : t("admin.providers.disabled")}
        </StatusBadge>
        <StatusBadge
          tone={provider.api_key_configured ? "emerald" : "red"}
          data-testid={`provider-apikey-${provider.id}`}
        >
          <KeyRound className="mr-1 size-3" />
          {provider.api_key_configured
            ? t("admin.providers.configured")
            : t("admin.providers.notConfigured")}
        </StatusBadge>
      </div>
      <div className="flex min-w-0 flex-col gap-0.5 font-mono text-xs">
        <span className="truncate text-foreground/90" title={provider.model_name}>{provider.model_name}</span>
        <span className="truncate text-muted-foreground" title={provider.base_url}>{provider.base_url}</span>
        <span className="flex min-w-0 items-baseline justify-between gap-2 text-muted-foreground">
          <span className="truncate" title={provider.provider_kind}>{provider.provider_kind}</span>
          <span className="shrink-0 tabular-nums">{t("admin.providers.priority")} {provider.selection_priority}</span>
        </span>
      </div>
      {testMutation.isPending ? (
        <p className="text-muted-foreground text-xs" data-testid={`provider-test-running-${provider.id}`}>
          {t("admin.datasources.testRunning")}
        </p>
      ) : null}
      {testOk && !testMutation.isPending ? (
        <p className="text-xs text-[var(--risk-safe)]" data-testid={`provider-test-ok-${provider.id}`}>
          {t("admin.providers.testOk")}
        </p>
      ) : null}
      {errorKey !== null ? (
        <div>
          <p className="text-xs text-destructive">{t(errorKey)}</p>
          {errorDetail !== null ? (
            <p className="break-all font-mono text-xs text-destructive" data-testid={`provider-test-error-detail-${provider.id}`}>
              {errorDetail}
            </p>
          ) : null}
          {errorRequestId !== null ? (
            <p className="text-muted-foreground text-xs">request_id: {errorRequestId}</p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function ProviderDeleteDialog({
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

export default function AdminProvidersPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const [search, setSearch] = useState("");
  // §18.82: backend cursor pagination (default 20 rows/page); search changes reset to page 1.
  const pager = useCursorPage(20);
  const query = useAiProviders(enabled, search.trim() || undefined, {
    limit: pager.pageSize,
    after: pager.after,
  });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const providersLoaded = !query.isPending && !query.isError;
  const [editing, setEditing] = useState<AiProvider | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  // §18.73: search goes through server-side q (name / BaseURL / model substring); keyword is used only to
  // distinguish empty-state copy.
  const keyword = search.trim();
  const providers = query.data?.items ?? [];
  // §18.73: search narrows server-side; tween whole-row position changes via FLIP when the row set changes.
  const providersBodyRef = useFlipReorder<HTMLDivElement>(
    JSON.stringify(providers.map((provider) => provider.id)),
  );
  const [deleting, setDeleting] = useState<AiProvider | null>(null);

  return (
    <PageLayout>
      {/* 2026-11-04 regression back to shared visual language: the custom in-Card header from 22e79fd dropped
          data-slot="page-header" (24px h1) and the ListToolbar slot,
          caught by the visual-consistency gate in CI; restore the PageHeader shell
          while keeping the notification area and Provider card grid intact. */}
      <PageHeader
        title={t("admin.providers.title")}
        description={t("admin.providers.description")}
        icon={Bot}
        actions={
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
            data-testid="provider-create"
          >
            <Plus /> {t("admin.providers.createTitle")}
          </Button>
        }
      />
      <Card data-testid="admin-providers-panel" className="gap-0 py-0">
        <div className="grid grid-cols-1 gap-3 border-b border-border p-5 md:grid-cols-2">
          <div className="flex items-start gap-3 rounded-xl border border-tone-sky/30 bg-tone-sky/5 p-3.5 text-[13px] text-tone-sky">
            <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
            <div className="flex flex-col gap-1">
              <p className="text-sm font-medium">{t("admin.providers.notice.keyTitle")}</p>
              <p className="leading-relaxed">{t("admin.providers.notice.keyBody")}</p>
            </div>
          </div>
          <div className="flex items-start gap-3 rounded-xl border border-border p-3.5 text-[13px] text-muted-foreground">
            <ListOrdered className="mt-0.5 size-4 shrink-0" aria-hidden />
            <p className="leading-relaxed">{t("admin.providers.notice.orderBody")}</p>
          </div>
        </div>
        <ListToolbar
          className="pt-5"
          busy={query.isFetching && query.isPlaceholderData}
          fill
        >
          <TableSearchInput
            value={search}
            onChange={(value) => {
              setSearch(value);
              pager.reset();
            }}
            testId="admin-providers-search"
          />
        </ListToolbar>
        <section className="flex flex-col gap-4 p-5">
          {query.isPending ? (
            <LoadingState variant="panel" />
          ) : query.isError ? (
            <ErrorState error={query.error} operationId="listAiProviders" onRetry={() => void query.refetch()} />
          ) : providers.length === 0 && keyword !== "" ? (
            <EmptyState
              title={t("table.emptySearch")}
              filtered
              testId="admin-providers-search-empty"
            />
          ) : providers.length === 0 ? (
            <EmptyState
              title={t("admin.providers.emptyTitle")}
              description={t("admin.providers.emptyDescription")}
            />
          ) : (
            <div
              ref={providersBodyRef}
              aria-busy={query.isFetching && query.isPlaceholderData}
              className={cn(
                "grid grid-cols-1 gap-3 transition-opacity duration-150 ease-out sm:grid-cols-2 lg:grid-cols-4",
                query.isFetching && query.isPlaceholderData && "opacity-60",
              )}
            >
              {providers.map((provider, index) => (
                <ProviderCard
                  key={provider.id}
                  provider={provider}
                  ordinal={index}
                  onEdit={() => {
                    setEditing(provider);
                    setDialogOpen(true);
                  }}
                  onAskDelete={() => { setDeleting(provider); }}
                />
              ))}
            </div>
          )}
        </section>
        {providersLoaded ? (
          <TablePagination
            busy={query.isFetching && query.isPlaceholderData}
            page={pager.pageNo}
            hasMore={query.data.page.has_more}
            isFirst={pager.isFirst}
            onPrev={pager.goPrev}
            onJumpPage={pager.goToPage}
            onNext={() => { pager.pushCursor(query.data.page.next_cursor); }}
            pageSize={pager.pageSize}
            onPageSizeChange={pager.setPageSize}
            rowsOnPage={query.data.items.length}
            testIdPrefix="providers"
          />
        ) : null}
      </Card>
      {deleting !== null ? (
        <ProviderDeleteDialog
          key={deleting.id}
          provider={deleting}
          onClose={() => { setDeleting(null); }}
        />
      ) : null}
      {dialogOpen ? (
        <ProviderFormDialog
          key={editing?.id ?? "new"}
          editing={editing}
          onClose={() => {
            setDialogOpen(false);
            setEditing(null);
          }}
        />
      ) : null}
    </PageLayout>
  );
}
