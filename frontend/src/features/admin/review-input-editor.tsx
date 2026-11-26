import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CircleAlert, Info } from "lucide-react";
import { Label } from "@/shared/components/ui/label";
import { Textarea } from "@/shared/components/ui/textarea";
import { Badge } from "@/shared/components/ui/badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import type {
  KnowledgeEntryDefinition,
  KnowledgeEntryDefinitionWriteMaxSeverity,
  ReviewInputDefinition,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";

export const KNOWLEDGE_ADVISORY_SEVERITIES = ["info", "low", "medium"] as const;

export function isAdvisorySeverity(
  value: string,
): value is KnowledgeEntryDefinitionWriteMaxSeverity {
  return value === "info" || value === "low" || value === "medium";
}

/**
 * Shared editor for review-input definitions:
 * - mode="skill": Prompt Tool / Review Skill (mandatory Critical blocking policy, non-downgradeable).
 * - mode="knowledge": Internal Experience (scoped non-blocking advisory severity: info | low | medium).
 */

export interface ReviewInputEditorValue {
  definition: ReviewInputDefinition | KnowledgeEntryDefinition;
  parameters: Record<string, number | string | boolean | string[]>;
}

export function ReviewInputDefinitionEditor({
  value,
  onChange,
  disabled = false,
  headerAction,
  mode = "skill",
}: {
  value: ReviewInputEditorValue;
  onChange: (next: ReviewInputEditorValue) => void;
  /** Locks every definition field — used for built-in skills whose
   * definition face is system-owned (only the state may be toggled). */
  disabled?: boolean;
  headerAction?: ReactNode;
  mode?: "skill" | "knowledge";
}) {
  const { t } = useTranslation();
  const { definition } = value;

  const updateKnowledgeText = (knowledgeText: string) => {
    if (disabled) return;
    onChange({
      ...value,
      definition: { ...definition, knowledge_text: knowledgeText },
    });
  };

  const updateKnowledgeSeverity = (maxSeverity: KnowledgeEntryDefinitionWriteMaxSeverity) => {
    if (disabled) return;
    onChange({
      ...value,
      definition: { ...definition, max_severity: maxSeverity },
    });
  };

  const legacyBlockingSeverity = mode === "knowledge" && !isAdvisorySeverity(definition.max_severity);

  return (
    <div className="flex flex-col gap-3" data-testid="review-input-editor">
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Label htmlFor="review-input-knowledge-text">
            {t(mode === "knowledge" ? "admin.knowledge.knowledgeText" : "admin.reviewInput.knowledgeText")}
          </Label>
          {headerAction}
        </div>
        <Textarea
          id="review-input-knowledge-text"
          rows={6}
          value={definition.knowledge_text}
          onChange={(event) => { updateKnowledgeText(event.target.value); }}
          disabled={disabled}
          data-testid="review-input-knowledge-text"
        />
        <p className="text-muted-foreground text-xs">
          {t(mode === "knowledge" ? "admin.knowledge.knowledgeTextHint" : "admin.reviewInput.knowledgeTextHint")}
        </p>
      </div>
      {mode === "knowledge" ? (
        <div className="flex flex-col gap-2">
          <div className="flex flex-col gap-1">
            <Label>{t("admin.knowledge.advisorySeverityLabel")}</Label>
            <Select
              value={isAdvisorySeverity(definition.max_severity) ? definition.max_severity : undefined}
              onValueChange={(next) => {
                if (next !== null && isAdvisorySeverity(next)) {
                  updateKnowledgeSeverity(next);
                }
              }}
              items={Object.fromEntries(
                KNOWLEDGE_ADVISORY_SEVERITIES.map((sev) => [sev, t(`admin.reviewInput.severity_${sev}`)]),
              )}
              disabled={disabled}
            >
              <SelectTrigger data-testid="knowledge-max-severity">
                <SelectValue>
                  {isAdvisorySeverity(definition.max_severity)
                    ? t(`admin.reviewInput.severity_${definition.max_severity}`)
                    : t("admin.knowledge.selectAdvisorySeverity")}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {KNOWLEDGE_ADVISORY_SEVERITIES.map((sev) => (
                  <SelectItem key={sev} value={sev}>
                    {t(`admin.reviewInput.severity_${sev}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {legacyBlockingSeverity ? (
            <Alert variant="destructive" data-testid="knowledge-legacy-severity-warning">
              <CircleAlert aria-hidden="true" />
              <AlertTitle>{t("admin.knowledge.legacySeverityTitle")}</AlertTitle>
              <AlertDescription>
                {t("admin.knowledge.legacySeverityHint", {
                  severity: t(`admin.reviewInput.severity_${definition.max_severity}`),
                })}
              </AlertDescription>
            </Alert>
          ) : null}
          <Alert data-testid="knowledge-advisory-policy">
            <Info aria-hidden="true" />
            <AlertTitle className="flex flex-wrap items-center gap-2">
              {t("admin.knowledge.advisoryPolicyTitle")}
              {isAdvisorySeverity(definition.max_severity) ? (
                <Badge variant="secondary" data-testid="knowledge-advisory-severity-badge">
                  {t(`admin.reviewInput.severity_${definition.max_severity}`)}
                </Badge>
              ) : null}
            </AlertTitle>
            <AlertDescription>{t("admin.knowledge.advisoryPolicyHint")}</AlertDescription>
          </Alert>
        </div>
      ) : (
        /* max_severity is accepted only for wire compatibility. The effective
         * severity of an enabled skill's actual violation is fixed at Critical. */
        <Alert data-testid="review-input-severity-policy">
          <CircleAlert aria-hidden="true" />
          <AlertTitle className="flex flex-wrap items-center gap-2">
            {t("admin.reviewInput.severityPolicyTitle")}
            <Badge variant="destructive" data-testid="review-input-critical-severity">
              {t("admin.reviewInput.severity_critical")}
            </Badge>
          </AlertTitle>
          <AlertDescription>{t("admin.reviewInput.severityPolicyHint")}</AlertDescription>
        </Alert>
      )}
    </div>
  );
}
