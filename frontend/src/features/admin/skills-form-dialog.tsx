import { useState } from "react";
import { useTranslation } from "react-i18next";
import { FlaskConical, Sparkles } from "lucide-react";
import type {
  PromptTool,
  PromptToolWrite,
  PromptToolWriteParameters,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { ReviewInputEditorValue } from "@/features/admin/review-input-editor";
import {
  type InputState,
  ReviewInputDialogFrame,
  ReviewInputStateSelect,
} from "@/features/admin/review-input-shared";
import { SkillTestDialog } from "@/features/admin/skills-test-dialog";
import {
  useCreatePromptTool,
  useOptimizePromptToolPrompt,
  useReplacePromptTool,
} from "@/features/admin/use-admin";
import { describeError } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

// Skill description max length (consistent with OpenAPI PromptToolWrite.description maxLength in code points).
const MAX_SKILL_DESCRIPTION = 100;

interface SkillFormState {
  name: string;
  description: string;
  state: InputState;
  engine: "all" | "mysql" | "postgresql";
  paramDrafts: Record<string, string>;
  editor: ReviewInputEditorValue;
}

// Builtin skill prompts are locked, while concrete {{variable}} values are tunable as typed drafts.
export function stringifyParamDrafts(params: PromptToolWriteParameters): Record<string, string> {
  const drafts: Record<string, string> = {};
  for (const [key, value] of Object.entries(params)) {
    if (typeof value === "boolean") drafts[key] = value ? "true" : "false";
    else if (Array.isArray(value)) drafts[key] = value.join(", ");
    else drafts[key] = String(value);
  }
  return drafts;
}

export function convertParamDrafts(
  drafts: Record<string, string>,
  template: PromptToolWriteParameters,
): PromptToolWriteParameters | null {
  const out: PromptToolWriteParameters = {};
  for (const [key, raw] of Object.entries(drafts)) {
    const sample = template[key];
    if (typeof sample === "number") {
      const parsed = Number(raw);
      if (raw.trim() === "" || !Number.isInteger(parsed) || parsed < 1 || parsed > 1e15) return null;
      out[key] = parsed;
    } else if (typeof sample === "boolean") {
      out[key] = raw === "true";
    } else if (Array.isArray(sample)) {
      const items = raw.split(",").map((item) => item.trim()).filter((item) => item !== "");
      if (items.length > 16 || items.some((item) => item.length > 64)) return null;
      out[key] = items;
    } else {
      if (raw.trim() === "" || raw.length > 128) return null;
      out[key] = raw;
    }
  }
  return out;
}

export function paramTypeKey(value: number | string | boolean | string[]): string {
  if (typeof value === "number") return "paramTypeNumber";
  if (typeof value === "boolean") return "paramTypeBoolean";
  if (Array.isArray(value)) return "paramTypeStringList";
  return "paramTypeString";
}

function initialSkillForm(tool: PromptTool | null): SkillFormState {
  if (tool === null) {
    return {
      name: "",
      description: "",
      state: "draft",
      engine: "all",
      paramDrafts: {},
      editor: {
        definition: { knowledge_text: "", max_severity: "critical", version: 1 },
        parameters: {},
      },
    };
  }
  return {
    name: tool.name,
    description: tool.description,
    state: tool.state,
    engine: tool.engine,
    paramDrafts: stringifyParamDrafts(tool.parameters ?? {}),
    editor: { definition: tool.definition, parameters: tool.parameters ?? {} },
  };
}

export function SkillFormDialog({
  editing,
  onClose,
}: {
  editing: PromptTool | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const editingBuiltin = editing !== null && editing.is_builtin;
  const createTool = useCreatePromptTool();
  const replaceTool = useReplacePromptTool();
  const optimizePrompt = useOptimizePromptToolPrompt();
  const [form, setForm] = useState<SkillFormState>(() => initialSkillForm(editing));
  const rowParams: PromptToolWriteParameters = editing !== null ? (editing.parameters ?? {}) : {};
  const [paramInvalid, setParamInvalid] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);
  const [skillTestOpen, setSkillTestOpen] = useState(false);

  const onWriteError = (operationId: string) => (error: unknown) => {
    const display = describeError(error, operationId);
    setErrorKey(display.messageKey);
    setErrorRequestId(display.requestId);
    appToast.error(t(display.messageKey));
  };

  const handleOptimizePrompt = () => {
    const seedText =
      form.editor.definition.knowledge_text.trim() ||
      form.description.trim() ||
      form.name.trim();
    const currentParams = convertParamDrafts(form.paramDrafts, rowParams) ?? {};
    optimizePrompt.mutate(
      {
        name: form.name.trim() || undefined,
        description: form.description.trim() || undefined,
        engine: form.engine,
        parameters: currentParams,
        knowledge_text: seedText,
      },
      {
        onSuccess: (res) => {
          setForm((prev) => ({
            ...prev,
            editor: {
              ...prev.editor,
              definition: {
                ...prev.editor.definition,
                knowledge_text: res.knowledge_text,
              },
            },
          }));
          appToast.success(t("admin.skills.optimizePrompt"));
        },
        onError: onWriteError("optimizePromptToolPrompt"),
      },
    );
  };

  const mutation = editing === null ? createTool : replaceTool;

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    const params = convertParamDrafts(form.paramDrafts, rowParams);
    if (params === null) {
      setParamInvalid(true);
      return;
    }
    setParamInvalid(false);
    const write: PromptToolWrite = {
      name: form.name,
      description: form.description,
      state: form.state,
      engine: form.engine,
      parameters: params,
      definition: form.editor.definition,
    };
    if (editing === null) {
      createTool.mutate(write, {
        onSuccess: () => {
          appToast.success(t("common.save"), { description: write.name });
          onClose();
        },
        onError: onWriteError("createPromptTool"),
      });
    } else {
      replaceTool.mutate(
        { id: editing.id, version: editing.version, write },
        {
          onSuccess: () => {
            appToast.success(t("common.save"), { description: write.name });
            onClose();
          },
          onError: onWriteError("replacePromptTool"),
        },
      );
    }
  };

  return (
    <ReviewInputDialogFrame
      title={t(editing === null ? "admin.skills.createTitle" : "admin.skills.editTitle")}
      configHash={editing?.config_hash ?? ""}
      notice={
        <>
          {editingBuiltin ? (
            <Alert data-testid="review-input-builtin-notice">
              <AlertTitle>{t("admin.skills.builtinDialogTitle")}</AlertTitle>
              <AlertDescription>{t("admin.skills.builtinDialogBody")}</AlertDescription>
            </Alert>
          ) : null}
          <p className="text-muted-foreground text-xs" data-testid="skills-eval-gate-hint">
            {t("admin.skills.evalGateHint")}
          </p>
        </>
      }
      fields={
        <>
          <div className="flex flex-col gap-1 md:col-span-2">
            <Label htmlFor="review-input-name">{t("admin.reviewInput.name")}</Label>
            <Input
              id="review-input-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              disabled={editingBuiltin}
              data-testid="review-input-name"
            />
          </div>
          <div className="flex flex-col gap-1 md:col-span-2">
            <Label htmlFor="review-input-description">{t("admin.skills.descriptionLabel")}</Label>
            <Input
              id="review-input-description"
              value={form.description}
              maxLength={MAX_SKILL_DESCRIPTION}
              onChange={(event) => { setForm({ ...form, description: event.target.value }); }}
              placeholder={t("admin.skills.descriptionPlaceholder")}
              disabled={editingBuiltin}
              data-testid="review-input-description"
            />
            <p className="text-muted-foreground text-xs" data-testid="review-input-description-count">
              {t("admin.skills.descriptionCount", { current: Array.from(form.description).length, max: MAX_SKILL_DESCRIPTION })}
            </p>
          </div>
          <ReviewInputStateSelect
            value={form.state}
            onChange={(state) => { setForm({ ...form, state }); }}
          />
          <div className="flex flex-col gap-1">
            <Label>{t("admin.skills.engine")}</Label>
            <Select
              value={form.engine}
              onValueChange={(next) => {
                if (next === null) return;
                setForm({ ...form, engine: next });
              }}
            >
              <SelectTrigger data-testid="review-input-engine" disabled={editingBuiltin}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">all</SelectItem>
                <SelectItem value="mysql">mysql</SelectItem>
                <SelectItem value="postgresql">postgresql</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </>
      }
      extraSection={
        Object.keys(rowParams).length > 0 ? (
          <div className="flex flex-col gap-2 rounded-lg border p-3" data-testid="review-input-params">
            <div className="flex flex-col gap-0.5">
              <Label>{t("admin.skills.parametersTitle")}</Label>
              <p className="text-muted-foreground text-xs">{t("admin.skills.parametersHint")}</p>
            </div>
            {Object.entries(rowParams).map(([key, sample]) => (
              <div key={key} className="flex flex-col gap-1">
                <Label htmlFor={`review-input-param-${key}`} className="font-mono text-xs">
                  {key} · {t(`admin.skills.${paramTypeKey(sample)}`)}
                </Label>
                {typeof sample === "boolean" ? (
                  <Checkbox
                    id={`review-input-param-${key}`}
                    checked={form.paramDrafts[key] === "true"}
                    onCheckedChange={(checked) => {
                      setParamInvalid(false);
                      setForm({ ...form, paramDrafts: { ...form.paramDrafts, [key]: checked ? "true" : "false" } });
                    }}
                    data-testid={`review-input-param-${key}`}
                  />
                ) : (
                  <Input
                    id={`review-input-param-${key}`}
                    value={form.paramDrafts[key] ?? ""}
                    type={typeof sample === "number" ? "number" : "text"}
                    min={typeof sample === "number" ? 1 : undefined}
                    step={typeof sample === "number" ? 1 : undefined}
                    onChange={(event) => {
                      setParamInvalid(false);
                      setForm({ ...form, paramDrafts: { ...form.paramDrafts, [key]: event.target.value } });
                    }}
                    data-testid={`review-input-param-${key}`}
                  />
                )}
              </div>
            ))}
            {paramInvalid ? (
              <p className="text-destructive text-xs" data-testid="review-input-param-error">
                {t("admin.skills.parametersInvalid")}
              </p>
            ) : null}
          </div>
        ) : null
      }
      editorValue={form.editor}
      onEditorChange={(editor) => { setForm({ ...form, editor }); }}
      editorDisabled={editingBuiltin}
      editorHeaderAction={
        <div className="flex flex-wrap items-center gap-2">
          {!editingBuiltin ? (
            <PendingButton
              size="xs"
              variant="outline"
              pending={optimizePrompt.isPending}
              disabled={
                form.editor.definition.knowledge_text.trim() === "" &&
                form.description.trim() === "" &&
                form.name.trim() === ""
              }
              onClick={handleOptimizePrompt}
              data-testid="skill-optimize-prompt"
            >
              <Sparkles className="size-3.5" aria-hidden="true" />
              {optimizePrompt.isPending
                ? t("admin.skills.optimizePromptPending")
                : t("admin.skills.optimizePrompt")}
            </PendingButton>
          ) : null}
          <Button
            type="button"
            size="xs"
            variant="outline"
            disabled={form.editor.definition.knowledge_text.trim() === ""}
            onClick={() => { setSkillTestOpen(true); }}
            data-testid="skill-test-open"
          >
            <FlaskConical className="size-3.5" aria-hidden="true" />
            {t("admin.skills.testSkill")}
          </Button>
        </div>
      }
      errorKey={errorKey}
      errorRequestId={errorRequestId}
      pending={mutation.isPending}
      onSubmit={submit}
      onClose={onClose}
      trailing={
        skillTestOpen ? (
          <SkillTestDialog
            name={form.name}
            description={form.description}
            engine={form.engine}
            parameters={convertParamDrafts(form.paramDrafts, rowParams) ?? {}}
            knowledgeText={form.editor.definition.knowledge_text}
            onClose={() => { setSkillTestOpen(false); }}
          />
        ) : null
      }
    />
  );
}
