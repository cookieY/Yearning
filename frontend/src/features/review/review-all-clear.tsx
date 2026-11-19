import { useTranslation } from "react-i18next";
import { Dot } from "@/shared/components/dots-03";

/**
 * "Everything is fine" state of a clean frozen review: the Shadcn Dashboard
 * `dots-03` face (caret eyes read as content), then the verdict and its note.
 * Follows the pointer with its eyes so it feels alive without needing a loop
 * of its own.
 */
export function ReviewAllClear() {
  const { t } = useTranslation();
  return (
    <div
      className="flex flex-col items-center gap-3 py-6 text-center"
      role="status"
      data-testid="findings-empty-all"
    >
      {/* Decorative: the verdict below says the same thing in text. */}
      <div aria-hidden="true" data-testid="review-all-clear-face">
        <Dot size={88} followCursor />
      </div>
      <p className="text-sm font-semibold">{t("precheck.findings.emptyAll")}</p>
      <p className="text-muted-foreground text-xs">{t("precheck.findings.emptyAllDescription")}</p>
    </div>
  );
}
