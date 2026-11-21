import type { RefObject } from "react";
import { useTranslation } from "react-i18next";
import { RotateCcw, Send, Square } from "lucide-react";
import type {
  AssistantCapability,
  SendAssistantMessageRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/shared/components/ui/input-group";
import { Spinner } from "@/shared/components/ui/spinner";
import { Textarea } from "@/shared/components/ui/textarea";

export interface AssistantComposerProps {
  promptInputRef: RefObject<HTMLTextAreaElement | null>;
  panelError: string | null;
  lastFailedPayload: SendAssistantMessageRequest | null;
  onRetryFailed: () => void;
  activeIntent: AssistantCapability;
  showExecutionPlan: boolean;
  onToggleExecutionPlan: () => void;
  executionPlanInput: string;
  onExecutionPlanChange: (value: string) => void;
  isSlashClearMatch: boolean;
  promptInput: string;
  onPromptInputChange: (value: string) => void;
  isBusy: boolean;
  onClearContext: () => void;
  onSend: () => void;
  onStopGenerating: () => void;
}

/** Renders the assistant error banner, optional execution plan field, `/clear` palette, and prompt composer. */
export function AssistantComposer({
  promptInputRef,
  panelError,
  lastFailedPayload,
  onRetryFailed,
  activeIntent,
  showExecutionPlan,
  onToggleExecutionPlan,
  executionPlanInput,
  onExecutionPlanChange,
  isSlashClearMatch,
  promptInput,
  onPromptInputChange,
  isBusy,
  onClearContext,
  onSend,
  onStopGenerating,
}: AssistantComposerProps) {
  const { t } = useTranslation();

  return (
    <>
      {/* Error Banner with Retry */}
      {panelError !== null && (
        <div className="px-3.5 pb-2" data-testid="assistant-error-banner">
          <Alert variant="destructive">
            <AlertDescription className="flex items-center justify-between gap-2 text-xs">
              <span>{panelError}</span>
              {lastFailedPayload !== null && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-6 px-2 text-xs"
                  onClick={onRetryFailed}
                  data-testid="assistant-error-retry-button"
                >
                  <RotateCcw data-icon="inline-start" aria-hidden="true" />
                  {t("assistant.retryTurn")}
                </Button>
              )}
            </AlertDescription>
          </Alert>
        </div>
      )}

      {/* Composer Footer */}
      <div className="border-border flex flex-col gap-2 border-t p-3">
        {activeIntent === "sql_optimize" && (
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs"
                onClick={onToggleExecutionPlan}
                data-testid="assistant-toggle-plan-button"
              >
                {showExecutionPlan
                  ? t("assistant.hideExecutionPlan")
                  : t("assistant.addExecutionPlan")}
              </Button>
            </div>
            {showExecutionPlan && (
              <Textarea
                value={executionPlanInput}
                onChange={(event) => {
                  onExecutionPlanChange(event.target.value);
                }}
                placeholder={t("assistant.executionPlanPlaceholder")}
                rows={2}
                className="font-mono text-xs"
                data-testid="assistant-execution-plan-textarea"
              />
            )}
          </div>
        )}

        {isSlashClearMatch && (
          <div
            className="border-border bg-popover flex items-center justify-between rounded-lg border p-1 shadow-sm"
            data-testid="assistant-slash-command-menu"
          >
            <button
              type="button"
              onClick={onClearContext}
              className="hover:bg-accent hover:text-accent-foreground flex w-full items-center justify-between rounded-md px-2.5 py-1.5 text-left text-xs transition-colors"
              data-testid="assistant-slash-command-clear"
            >
              <span className="flex items-center gap-2">
                <Badge variant="secondary" className="font-mono text-[11px]">
                  /clear
                </Badge>
                <span className="text-foreground">
                  {t("assistant.clearCommandDescription")}
                </span>
              </span>
              <span className="text-muted-foreground font-mono text-[10px]">
                Enter
              </span>
            </button>
          </div>
        )}

        <InputGroup>
          <InputGroupTextarea
            ref={promptInputRef}
            value={promptInput}
            onChange={(event) => {
              onPromptInputChange(event.target.value);
            }}
            onKeyDown={(event) => {
              if (event.key === "Tab" && isSlashClearMatch) {
                event.preventDefault();
                onPromptInputChange("/clear");
                return;
              }
              if (
                event.key === "Enter" &&
                !event.shiftKey &&
                !event.nativeEvent.isComposing
              ) {
                event.preventDefault();
                if (isSlashClearMatch) {
                  onClearContext();
                  return;
                }
                onSend();
              }
            }}
            placeholder={t("assistant.inputPlaceholder")}
            aria-label={t("assistant.inputPlaceholder")}
            rows={2}
            data-testid="assistant-prompt-input"
          />
          <InputGroupAddon align="block-end" className="justify-between">
            <span className="text-muted-foreground text-[11px]">
              {t("assistant.inputHint")}
            </span>
            <div className="flex items-center gap-1.5">
              {isBusy && (
                <InputGroupButton
                  type="button"
                  variant="outline"
                  size="xs"
                  onClick={onStopGenerating}
                  data-testid="assistant-stop-button"
                >
                  <Square data-icon="inline-start" aria-hidden="true" />
                  {t("assistant.stopGenerating")}
                </InputGroupButton>
              )}
              <InputGroupButton
                type="button"
                variant="default"
                size="xs"
                disabled={promptInput.trim() === "" || isBusy}
                onClick={onSend}
                data-testid="assistant-send-button"
              >
                {isBusy ? (
                  <Spinner data-icon="inline-start" aria-hidden="true" />
                ) : (
                  <Send data-icon="inline-start" aria-hidden="true" />
                )}
                {t("assistant.send")}
              </InputGroupButton>
            </div>
          </InputGroupAddon>
        </InputGroup>
      </div>
    </>
  );
}
