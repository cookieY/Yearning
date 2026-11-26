import type {
  AiProvider,
  AiProviderWrite,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  resolvePromptProfile,
  type PromptProfileSetting,
} from "@/features/admin/prompt-profile";

export interface ProviderFormState {
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

export function applyProfileTransition(
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

export function emptyProviderForm(): ProviderFormState {
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
export function configurationWrite(form: ProviderFormState, isCreate: boolean): AiProviderWrite["configuration"] {
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
export function paramFieldErrors(form: ProviderFormState): {
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

export function providerFormForEdit(provider: AiProvider): ProviderFormState {
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
