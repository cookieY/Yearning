// S007 notification rules view (FE-F10-NOTIFICATION-CENTER): rule CRUD + preview.
// Rule = name + enabled + flow scope (empty = all) + one trigger event + one target role + one channel;
// preview only resolves targets and renders fixed copy, never sending or mutating state. Event vocabulary comes from
// NotificationRuleEventType (AI precheck family is structurally excluded — the generated type is the vocabulary).
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Plus, Trash2 } from "lucide-react";
import type {
  NotificationChannel,
  NotificationRule,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useDeleteNotificationRule,
  useNotificationChannels,
  useNotificationRules,
} from "@/features/admin/use-admin";
import { RuleFormDialog } from "@/routes/admin/admin-notification-rule-dialog";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { EmptyState } from "@/shared/components/status/empty-state";
import { LoadingState } from "@/shared/components/status/loading-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { ErrorState } from "@/shared/components/status/status-components";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";

export function RulesView({ isAdmin }: { isAdmin: boolean }) {
  const { t } = useTranslation();
  const rulesQuery = useNotificationRules(isAdmin);
  const channelsQuery = useNotificationChannels(isAdmin, { limit: 100 });
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationRule | null>(null);

  const rules = rulesQuery.data?.items ?? [];
  const channels = channelsQuery.data?.items ?? [];

  return (
    <PageLayout data-testid="admin-notification-rules">
      {rulesQuery.isPending && <LoadingState variant="page" />}
      {rulesQuery.error !== null && (
        <ErrorState error={rulesQuery.error} operationId="listNotificationRules" onRetry={() => void rulesQuery.refetch()} />
      )}
      {rulesQuery.data !== undefined && (
        <Card>
          <CardHeader className="border-b pb-3">
            <CardTitle>{t("adminRules.title")}</CardTitle>
            <CardDescription>{t("adminRules.description")}</CardDescription>
            {/* Create Rule sits at the far right of the header row (owner round 4 ruling, consistent with the notification channels card). */}
            {isAdmin && (
              <CardAction>
                <Button onClick={() => { setCreateOpen(true); }} data-testid="admin-rules-create">
                  <Plus />
                  {t("adminRules.create")}
                </Button>
              </CardAction>
            )}
          </CardHeader>
          <CardContent className="px-0!">
            {rules.length === 0 ? (
              <EmptyState title={t("adminRules.empty")} testId="admin-rules-empty" />
            ) : (
            <Table data-testid="admin-rules-table">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("adminRules.column.name")}</TableHead>
                  <TableHead>{t("adminRules.column.event")}</TableHead>
                  <TableHead>{t("adminRules.column.target")}</TableHead>
                  <TableHead>{t("adminRules.column.channel")}</TableHead>
                  <TableHead>{t("adminRules.column.template")}</TableHead>
                  <TableHead>{t("adminRules.column.enabled")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rules.map((rule) => {
                  const channel = channels.find((c: NotificationChannel) => c.id === rule.channel_id);
                  const hasCustomTemplate = typeof rule.message_template === "string" && rule.message_template.trim() !== "";
                  return (
                    <TableRow key={rule.id} data-testid={`admin-rule-row-${rule.id}`}>
                      <TableCell>{rule.name}</TableCell>
                      <TableCell className="font-mono text-xs">{rule.event_type}</TableCell>
                      <TableCell>{t(`adminRules.target.${rule.target_kind}`)}</TableCell>
                      <TableCell>{channel?.name ?? t("adminRules.channelMissing")}</TableCell>
                      <TableCell>
                        <StatusBadge tone={hasCustomTemplate ? "sky" : "stone"}>
                          {hasCustomTemplate ? t("adminRules.templateModeCustom") : t("adminRules.templateModeDefault")}
                        </StatusBadge>
                      </TableCell>
                      <TableCell>
                        <StatusBadge tone={rule.enabled ? "emerald" : "stone"}>
                          {rule.enabled ? t("common.enabled") : t("common.disabled")}
                        </StatusBadge>
                      </TableCell>
                      <TableCell className="text-right">
                        <Button variant="ghost" size="sm" onClick={() => { setEditing(rule); }} data-testid={`admin-rule-edit-${rule.id}`}>
                          <Pencil />
                        </Button>
                        <RuleDeleteButton rule={rule} />
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            )}
          </CardContent>
        </Card>
      )}

      {createOpen ? (
        <RuleFormDialog key="create" editing={null} onClose={() => { setCreateOpen(false); }} />
      ) : null}
      {editing !== null ? (
        <RuleFormDialog key={editing.id} editing={editing} onClose={() => { setEditing(null); }} />
      ) : null}
    </PageLayout>
  );
}

function RuleDeleteButton({ rule }: { rule: NotificationRule }) {
  const { t } = useTranslation();
  const deleteRule = useDeleteNotificationRule();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);
  return (
    <>
      <Button variant="ghost" size="sm" onClick={() => { setConfirmOpen(true); }} data-testid={`admin-rule-delete-${rule.id}`}>
        <Trash2 />
      </Button>
      <Dialog open={confirmOpen} onOpenChange={(next) => { if (!next) setConfirmOpen(false); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("adminRules.deleteTitle", { name: rule.name })}</DialogTitle>
            <DialogDescription>{t("adminRules.deleteDescription")}</DialogDescription>
          </DialogHeader>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="rule-delete-error">
              <AlertTitle>{t("adminRules.deleteFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setConfirmOpen(false); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              pending={deleteRule.isPending}
              onClick={() => {
                deleteRule.mutate(
                  { ruleId: rule.id, version: rule.version },
                  {
                    onSuccess: () => {
                      appToast.success(t("common.delete"), { description: rule.name });
                      setConfirmOpen(false);
                    },
                    onError: (error) => {
                      const errText = describeErrorText(describeError(error, "deleteNotificationRule"));
                      setErrorText(errText);
                      appToast.error(errText);
                    },
                  },
                );
              }}
              data-testid="rule-delete-confirm"
            >
              {t("common.delete")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
