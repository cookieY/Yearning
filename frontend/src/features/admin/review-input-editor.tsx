import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CircleAlert } from "lucide-react";
import { Label } from "@/shared/components/ui/label";
import { Textarea } from "@/shared/components/ui/textarea";
import { Badge } from "@/shared/components/ui/badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import type { ReviewInputDefinition } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Shared editor for the review-input definition (§18.66 five-field model:
 * name / state / engine live in the page form, this editor carries rule
 * content). The definition is {knowledge_text, max_severity, version}: the
 * finding key is system-derived per row and injected into the model context
 * together with the rule text — there is deliberately NO field for it (nor
 * for finding templates, categories or fix suggestions: the review model
 * authors those per finding at run time). There is also NO field for
 * executable behavior: no code, no HTTP endpoint, no database write. The
 * definition is the sole object the structured-output, privacy-scan and
 * injection-check eval gates act on.
 *
 * max_severity remains in the wire DTO for historical payload compatibility.
 * It is no longer a user-selectable policy: an enabled rule's actual
 * violation is always Critical and blocks submission.
 */

export interface ReviewInputEditorValue {
  definition: ReviewInputDefinition;
  parameters: Record<string, number | string | boolean | string[]>;
}

export function ReviewInputDefinitionEditor({
  value,
  onChange,
  disabled = false,
  headerAction,
}: {
  value: ReviewInputEditorValue;
  onChange: (next: ReviewInputEditorValue) => void;
  /** Locks every definition field — used for built-in skills whose
   * definition face is system-owned (only the state may be toggled). */
  disabled?: boolean;
  headerAction?: ReactNode;
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

  return (
    <div className="flex flex-col gap-3" data-testid="review-input-editor">
      <div className="flex flex-col gap-1.5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Label htmlFor="review-input-knowledge-text">{t("admin.reviewInput.knowledgeText")}</Label>
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
        <p className="text-muted-foreground text-xs">{t("admin.reviewInput.knowledgeTextHint")}</p>
      </div>
      {/* max_severity is accepted only for wire compatibility. The effective
       * severity of an enabled rule's actual violation is fixed at Critical. */}
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
    </div>
  );
}
