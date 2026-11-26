import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, CheckCircle2, FlaskConical, Pencil, Trash2 } from "lucide-react";
import type { NotificationChannel } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useDeleteNotificationChannel,
  useNotificationRules,
  useNotificationTestTask,
  useTestNotificationDelivery,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { TableRowErrorRow } from "@/shared/components/table-row-error-row";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { Spinner } from "@/shared/components/ui/spinner";
import { TableCell, TableRow } from "@/shared/components/ui/table";

function channelFromAddress(channel: NotificationChannel): string {
  if (channel.kind !== "email") return "";
  const cfg = channel.configuration;
  if ("from_address" in cfg && typeof cfg.from_address === "string") {
    return cfg.from_address.trim();
  }
  return "";
}

export function ChannelRow({ channel, onEdit }: { channel: NotificationChannel; onEdit: () => void }) {
  const { t } = useTranslation();
  const { user } = useSession();
  const deleteChannel = useDeleteNotificationChannel();
  const testDelivery = useTestNotificationDelivery();
  // 2026-10-31 round 3: test delivery is an async task — the acceptance receipt task_id is polled via
  // useNotificationTestTask until terminal state, showing inline whether delivery succeeded.
  const [testTaskId, setTestTaskId] = useState<string | null>(null);
  const testTaskQuery = useNotificationTestTask(testTaskId);
  const rulesQuery = useNotificationRules(true);
  const referencedRules = (rulesQuery.data?.items ?? []).filter((rule) => rule.channel_id === channel.id);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [testDialogOpen, setTestDialogOpen] = useState(false);
  const [testRecipient, setTestRecipient] = useState("");
  const [errorText, setErrorText] = useState<string | null>(null);

  const triggerTestDelivery = (recipient?: string) => {
    const trimmedRecipient = recipient?.trim();
    testDelivery.mutate(
      {
        channelId: channel.id,
        body: trimmedRecipient !== undefined && trimmedRecipient !== "" ? { recipient: trimmedRecipient } : {},
      },
      {
        onSuccess: (receipt) => {
          setTestDialogOpen(false);
          setTestTaskId(receipt.task_id);
          setErrorText(null);
          appToast.info(t("adminNotifications.test"), {
            description: channel.name,
          });
        },
        onError: (error) => {
          const errText = describeErrorText(describeError(error, "createNotificationTestDelivery"));
          setErrorText(errText);
          appToast.error(errText);
        },
      },
    );
  };

  const trimmedTestRecipient = testRecipient.trim();
  const testRecipientInvalid = trimmedTestRecipient === "" || !trimmedTestRecipient.includes("@");

  return (
    <>
      <TableRow data-row-key={channel.id} data-testid={`admin-channel-row-${channel.id}`}>
        <TableCell>
          <Badge variant="outline">{channel.kind}</Badge>
        </TableCell>
        <TableCell className="font-semibold text-foreground">{channel.name}</TableCell>
        <TableCell>
          <StatusBadge tone={channel.enabled ? "emerald" : "stone"}>
            {channel.enabled ? t("adminNotifications.enabled") : t("adminNotifications.disabled")}
          </StatusBadge>
        </TableCell>
        <TableCell>
          <StatusBadge tone={channel.secret_configured ? "emerald" : "red"}>
            {channel.secret_configured ? t("adminNotifications.secretConfigured") : t("adminNotifications.secretMissing")}
          </StatusBadge>
        </TableCell>
        <TableCell className="text-right">
          <TableRowActions testId={`admin-channel-actions-${channel.id}`}>
            <DropdownMenuItem
              disabled={testDelivery.isPending}
              aria-busy={testDelivery.isPending || undefined}
              onClick={() => {
                if (channel.kind === "email") {
                  const defaultRecipient = (user?.email ?? "").trim() || channelFromAddress(channel);
                  setTestRecipient(defaultRecipient);
                  setErrorText(null);
                  setTestDialogOpen(true);
                  return;
                }
                triggerTestDelivery();
              }}
              data-testid={`admin-channel-test-${channel.id}`}
            >
              {testDelivery.isPending ? <Spinner aria-hidden /> : <FlaskConical />}
              {t("adminNotifications.test")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={onEdit} data-testid={`admin-channel-edit-${channel.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onClick={() => { setConfirmOpen(true); }} data-testid={`admin-channel-delete-${channel.id}`}>
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </TableRowActions>
        </TableCell>
      </TableRow>
      {errorText !== null && !testDialogOpen ? (
        <TableRowErrorRow
          colSpan={5}
          summary={errorText}
          testId={`admin-channel-error-${channel.id}`}
          copyTestId={`admin-channel-error-copy-${channel.id}`}
          closeTestId={`admin-channel-error-close-${channel.id}`}
          onClose={() => { setErrorText(null); }}
        />
      ) : null}
      {testTaskId !== null && (
        <TableRow>
          <TableCell colSpan={5} className="whitespace-normal" data-testid={`admin-channel-test-result-${channel.id}`}>
            {(() => {
              // Terminal state = succeeded/failed/cancelled; query error stops polling and surfaces a sanitized reason
              // instead of leaving the spinner active (consumed asynchronously by Worker).
              if (testTaskQuery.isError) {
                const reason = describeErrorText(describeError(testTaskQuery.error, "getTask"));
                return (
                  <div className="flex items-start gap-2 text-xs text-destructive">
                    <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
                    <span className="min-w-0 break-words">
                      {t("adminNotifications.testResult.failedWithReason", { reason })}
                    </span>
                  </div>
                );
              }
              const state = testTaskQuery.data?.state;
              if (state === "succeeded") {
                return (
                  <div className="flex items-center gap-2 text-xs text-[var(--risk-safe)]">
                    <CheckCircle2 className="size-4 shrink-0" aria-hidden />
                    <span className="min-w-0 break-words">{t("adminNotifications.testResult.succeeded")}</span>
                  </div>
                );
              }
              if (state === "failed" || state === "cancelled") {
                const failureName = testTaskQuery.data?.error?.title;
                return (
                  <div className="flex items-start gap-2 text-xs text-destructive">
                    <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
                    <span className="min-w-0 break-words">
                      {failureName !== undefined && failureName !== ""
                        ? t("adminNotifications.testResult.failedWithReason", { reason: failureName })
                        : t("adminNotifications.testResult.failed")}
                    </span>
                  </div>
                );
              }
              return (
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Spinner className="size-4 shrink-0" aria-hidden />
                  <span className="min-w-0 break-words">{t("adminNotifications.testResult.running")}</span>
                </div>
              );
            })()}
          </TableCell>
        </TableRow>
      )}

      <Dialog open={confirmOpen} onOpenChange={(next) => { if (!next) setConfirmOpen(false); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("adminNotifications.delete.title", { name: channel.name })}</DialogTitle>
            <DialogDescription>{t("adminNotifications.delete.description")}</DialogDescription>
            {referencedRules.length > 0 && (
              <DialogDescription data-testid="channel-referenced-rules">
                {t("adminNotifications.referencedRules", { count: referencedRules.length, names: referencedRules.map((rule) => rule.name).join(t("common.listSeparator")) })}
              </DialogDescription>
            )}
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setConfirmOpen(false); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant="destructive"
              pending={deleteChannel.isPending}
              onClick={() => {
                deleteChannel.mutate(
                  { channelId: channel.id, version: channel.version },
                  {
                    onSuccess: () => {
                      appToast.success(t("common.delete"), {
                        description: channel.name,
                      });
                      setConfirmOpen(false);
                    },
                    onError: (error) => {
                      const errText = describeErrorText(describeError(error, "deleteNotificationChannel"));
                      appToast.error(errText);
                    },
                  },
                );
              }}
            >
              {t("common.delete")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={testDialogOpen} onOpenChange={(next) => { if (!next) setTestDialogOpen(false); }}>
        <DialogContent data-testid="channel-test-email-dialog">
          <DialogHeader>
            <DialogTitle>{t("adminNotifications.testEmailDialog.title", { name: channel.name })}</DialogTitle>
            <DialogDescription>{t("adminNotifications.testEmailDialog.description")}</DialogDescription>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={(event) => {
              event.preventDefault();
              if (testDelivery.isPending || testRecipientInvalid) return;
              triggerTestDelivery(trimmedTestRecipient);
            }}
          >
            <div className="flex flex-col gap-2">
              <Label htmlFor={`channel-test-recipient-${channel.id}`}>
                {t("adminNotifications.testEmailDialog.recipientLabel")}
              </Label>
              <Input
                id={`channel-test-recipient-${channel.id}`}
                type="email"
                value={testRecipient}
                placeholder={t("adminNotifications.testEmailDialog.recipientPlaceholder")}
                onChange={(event) => { setTestRecipient(event.target.value); }}
                data-testid="channel-test-recipient"
              />
              <p className="text-muted-foreground text-xs">
                {t("adminNotifications.testEmailDialog.recipientHint")}
              </p>
            </div>
            {errorText !== null && (
              <Alert variant="destructive" data-testid="channel-test-email-error">
                <AlertDescription>{errorText}</AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => { setTestDialogOpen(false); }}>
                {t("common.cancel")}
              </Button>
              <PendingButton
                type="submit"
                pending={testDelivery.isPending}
                disabled={testRecipientInvalid}
                data-testid="channel-test-submit"
              >
                {t("adminNotifications.testEmailDialog.submit")}
              </PendingButton>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
