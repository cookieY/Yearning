import type { Dispatch, SetStateAction } from "react";
import { useTranslation } from "react-i18next";
import { Undo2 } from "lucide-react";
import {
  resolvePromptProfile,
  type PromptProfileSetting,
} from "@/features/admin/prompt-profile";
import {
  applyProfileTransition,
  type ProviderFormState,
} from "@/routes/admin/admin-ai-provider-form-model";
import { Button } from "@/shared/components/ui/button";
import { Label } from "@/shared/components/ui/label";
import { NumberInput } from "@/shared/components/ui/number-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

export interface ProviderParamErrorState {
  systemPromptProfile: string | null;
  maxTokens: string | null;
  contextWindow: string | null;
  temperature: string | null;
}

export function ProviderGenerationTabContent({
  form,
  setForm,
  paramErrors,
  setParamErrors,
  isCreate,
}: {
  form: ProviderFormState;
  setForm: Dispatch<SetStateAction<ProviderFormState>>;
  paramErrors: ProviderParamErrorState;
  setParamErrors: Dispatch<SetStateAction<ProviderParamErrorState>>;
  isCreate: boolean;
}) {
  const { t } = useTranslation();

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
              setForm((prev) => applyProfileTransition(prev, prev.modelName, value, isCreate));
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
                applyProfileTransition(prev, prev.modelName, value as PromptProfileSetting, isCreate),
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
