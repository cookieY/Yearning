import { useState } from "react";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import type { QueryAccessRequest } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useWithdrawAccessRequest } from "@/features/query/use-query-domain";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
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
import { Textarea } from "@/shared/components/ui/textarea";

export interface QueryReasonActionDialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  description: string;
  reasonLabel: string;
  defaultReason: string;
  confirmLabel: string;
  failedTitle: string;
  submittedToast: string;
  operationId: string;
  pending: boolean;
  testIdPrefix: string;
  onConfirm: (reason: string) => Promise<void>;
}

/** Shared confirmation dialog with optional reason textarea for query access lifecycle actions. */
export function QueryReasonActionDialog({
  open,
  onClose,
  title,
  description,
  reasonLabel,
  defaultReason,
  confirmLabel,
  failedTitle,
  submittedToast,
  operationId,
  pending,
  testIdPrefix,
  onConfirm,
}: QueryReasonActionDialogProps) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  const [errorText, setErrorText] = useState<string | null>(null);

  const close = () => {
    setReason("");
    setErrorText(null);
    onClose();
  };

  const submit = async () => {
    setErrorText(null);
    try {
      await onConfirm(reason.trim() === "" ? defaultReason : reason.trim());
      toast.success(submittedToast);
      close();
    } catch (error) {
      setErrorText(describeErrorText(describeError(error, operationId)));
    }
  };

  const textareaId = `${testIdPrefix}-reason`;

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) close(); }}>
      <DialogContent data-testid={`${testIdPrefix}-dialog`}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2">
          <Label htmlFor={textareaId}>{reasonLabel}</Label>
          <Textarea
            id={textareaId}
            value={reason}
            onChange={(event) => { setReason(event.target.value); }}
            maxLength={4096}
            data-testid={textareaId}
          />
          {errorText !== null && (
            <Alert variant="destructive" data-testid={`${testIdPrefix}-error`}>
              <AlertTitle>{failedTitle}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={pending}
            onClick={() => { void submit(); }}
            data-testid={`${testIdPrefix}-confirm`}
          >
            {confirmLabel}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export interface WithdrawQueryAccessTarget {
  label: string;
  request: QueryAccessRequest;
}

export function WithdrawQueryAccessDialog({
  target,
  onClose,
  onWithdrawn,
}: {
  target: WithdrawQueryAccessTarget | null;
  onClose: () => void;
  onWithdrawn?: (updated: QueryAccessRequest) => void;
}) {
  const { t } = useTranslation();
  const withdraw = useWithdrawAccessRequest();

  return (
    <QueryReasonActionDialog
      open={target !== null}
      onClose={onClose}
      title={t("query.entry.withdrawTitle", { name: target?.label ?? "" })}
      description={t("query.entry.withdrawDescription")}
      reasonLabel={t("query.entry.withdrawReason")}
      defaultReason={t("query.entry.withdrawDefaultReason")}
      confirmLabel={t("query.entry.withdrawConfirm")}
      failedTitle={t("query.entry.withdrawFailed")}
      submittedToast={t("query.entry.withdrawSubmitted")}
      operationId="withdrawQueryAccessRequest"
      pending={withdraw.isPending}
      testIdPrefix="query-withdraw"
      onConfirm={async (reason) => {
        if (target === null) return;
        const updated = await withdraw.mutateAsync({
          requestId: target.request.id,
          version: target.request.version,
          reason,
        });
        onWithdrawn?.(updated);
      }}
    />
  );
}
