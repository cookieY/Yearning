import type { ReactNode } from "react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { KnowledgeEntry, PromptTool } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useDeleteKnowledgeEntry, useDeletePromptTool } from "@/features/admin/use-admin";
import {
  ReviewInputDefinitionEditor,
  type ReviewInputEditorValue,
} from "@/features/admin/review-input-editor";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/shared/components/ui/alert-dialog";
import { Button } from "@/shared/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

export const STATE_ORDER = ["draft", "enabled", "disabled"] as const;
export type InputState = (typeof STATE_ORDER)[number];

export function StateBadge({ state }: { state: string }) {
  const { t } = useTranslation();
  const tone = state === "enabled" ? "emerald" : state === "draft" ? "amber" : "stone";
  return (
    <StatusBadge tone={tone} data-testid="review-input-state">
      {t(`admin.reviewInput.state_${state}`)}
    </StatusBadge>
  );
}

export function ReviewInputStateSelect({
  value,
  onChange,
}: {
  value: InputState;
  onChange: (next: InputState) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-1">
      <Label>{t("admin.reviewInput.state")}</Label>
      <Select
        value={value}
        onValueChange={(next) => {
          if (next === null) return;
          onChange(next);
        }}
        items={Object.fromEntries(
          STATE_ORDER.map((state) => [state, t(`admin.reviewInput.state_${state}`)]),
        )}
      >
        <SelectTrigger data-testid="review-input-state-select">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {STATE_ORDER.map((state) => (
            <SelectItem key={state} value={state}>
              {t(`admin.reviewInput.state_${state}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

export function ReviewInputDialogFrame({
  title,
  configHash,
  notice,
  fields,
  extraSection,
  editorValue,
  onEditorChange,
  editorDisabled,
  editorHeaderAction,
  editorMode = "skill",
  submitDisabled = false,
  errorKey,
  errorRequestId,
  pending,
  onSubmit,
  onClose,
  trailing,
}: {
  title: string;
  configHash: string;
  notice?: ReactNode;
  fields: ReactNode;
  extraSection?: ReactNode;
  editorValue: ReviewInputEditorValue;
  onEditorChange: (next: ReviewInputEditorValue) => void;
  editorDisabled?: boolean;
  editorHeaderAction?: ReactNode;
  editorMode?: "skill" | "knowledge";
  submitDisabled?: boolean;
  errorKey: string | null;
  errorRequestId: string | null;
  pending: boolean;
  onSubmit: () => void;
  onClose: () => void;
  trailing?: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {t(editorMode === "knowledge" ? "admin.knowledge.formDescription" : "admin.reviewInput.formDescription")}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {/* Owner ruling: hash and invalidation impact notice belong to the editing context;
              hash is visible only in edit mode (not yet generated during creation). */}
          <Alert className="border-[var(--risk-warning)]/40" data-testid="review-input-outdated-note">
            <AlertTitle>{t("admin.reviewInput.outdatedNoteTitle")}</AlertTitle>
            <AlertDescription>{t("admin.reviewInput.outdatedNoteBody")}</AlertDescription>
          </Alert>
          {configHash !== "" ? (
            <div className="flex flex-col gap-1">
              <Label>{t("admin.reviewInput.configHash")}</Label>
              <p className="break-all font-mono text-xs" data-testid="review-input-hash">
                {configHash}
              </p>
            </div>
          ) : null}
          {notice}
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">{fields}</div>
          {extraSection}
          <ReviewInputDefinitionEditor
            value={editorValue}
            onChange={onEditorChange}
            disabled={editorDisabled}
            headerAction={editorHeaderAction}
            mode={editorMode}
          />
          {errorKey !== null ? (
            <Alert variant="destructive" data-testid="review-input-error">
              <AlertTitle>{t(errorKey)}</AlertTitle>
              <AlertDescription>
                {errorRequestId !== null ? `request_id: ${errorRequestId}` : null}
              </AlertDescription>
            </Alert>
          ) : null}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={pending}
            disabled={submitDisabled}
            onClick={onSubmit}
            data-testid="review-input-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
        {trailing}
      </DialogContent>
    </Dialog>
  );
}

export function DeleteReviewInputDialog({
  kind,
  target,
  onClose,
}: {
  kind: "skills" | "knowledge";
  target: PromptTool | KnowledgeEntry;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const isSkills = kind === "skills";
  const deleteTool = useDeletePromptTool();
  const deleteEntry = useDeleteKnowledgeEntry();
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const mutation = isSkills ? deleteTool : deleteEntry;
  return (
    <AlertDialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("admin.reviewInput.deleteTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {isSkills
              ? t("admin.reviewInput.deleteDescription", { name: target.name })
              : t("admin.knowledge.deleteDescription", { name: target.name })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {errorKey !== null ? (
          <Alert variant="destructive" data-testid="review-input-delete-error">
            <AlertTitle>{t(errorKey)}</AlertTitle>
            {isSkills ? (
              <AlertDescription>{t("admin.reviewInput.deleteErrorHint")}</AlertDescription>
            ) : null}
          </Alert>
        ) : null}
        <AlertDialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={mutation.isPending}
            onClick={() => {
              mutation.mutate(
                { id: target.id, version: target.version },
                {
                  onSuccess: () => {
                    appToast.success(t("common.delete"), { description: target.name });
                    onClose();
                  },
                  onError: (error) => {
                    const display = describeError(error, isSkills ? "deletePromptTool" : "deleteKnowledgeEntry");
                    setErrorKey(display.messageKey);
                    appToast.error(t(display.messageKey));
                  },
                },
              );
            }}
            data-testid="review-input-delete-confirm"
          >
            {t("common.delete")}
          </PendingButton>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
