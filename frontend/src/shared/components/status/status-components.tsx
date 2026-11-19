import { useTranslation } from "react-i18next";
import { AlertCircle, RotateCw } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { describeError } from "@/shared/api/error-display";

/**
 * Shared page-level status components (frontend PRD §2.15): every page shows
 * loading, error and no-permission states through these pieces instead of
 * re-rolling per-page markup. Error text always comes from i18n; the raw
 * err_code name is never rendered. The request_id is the only opaque value
 * displayed, so users can quote it to operators without telemetry being sent.
 */

export interface ErrorStateProps {
  error: unknown;
  /** OpenAPI operationId whose generated error profile judges business errors. */
  operationId: string;
  onRetry?: () => void;
}

export function ErrorState({ error, operationId, onRetry }: ErrorStateProps) {
  const { t } = useTranslation();
  const display = describeError(error, operationId);
  return (
    <Alert variant="destructive" className="rounded-xl">
      <AlertCircle />
      <AlertTitle>{t("states.errorTitle")}</AlertTitle>
      <AlertDescription>
        <span>{t(display.messageKey)}</span>
        {display.requestId !== null && (
          <span className="mt-1 block font-mono text-xs">
            {t("errors.requestId")}: {display.requestId}
          </span>
        )}
      </AlertDescription>
      {onRetry !== undefined && (
        <Button variant="outline" size="sm" className="mt-2" onClick={onRetry}>
          <RotateCw aria-hidden />
          {t("errors.retry")}
        </Button>
      )}
    </Alert>
  );
}
