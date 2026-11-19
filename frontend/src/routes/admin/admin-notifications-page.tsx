import i18next from "@/shared/i18n";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Bell, CheckCircle2, FlaskConical, Pencil, Plus, Trash2 } from "lucide-react";
import type {
  DingTalkNotificationConfig as DingTalkConfig,
  EmailNotificationConfig as EmailConfig,
  FeishuNotificationConfig as FeishuConfig,
  SlackNotificationConfig as SlackConfig,
  NotificationChannel,
  NotificationChannelKind,
  NotificationDelivery,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { SettingsNavTabs } from "@/routes/admin/settings-tabs";
import { RulesView } from "@/routes/admin/admin-notification-rules-view";
import { useNotificationRules } from "@/features/admin/use-admin";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import type { StatusTone } from "@/shared/lib/status-tone";
import { formatTimestamp } from "@/shared/lib/timezones";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { NumberInput } from "@/shared/components/ui/number-input";
import { Label } from "@/shared/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { Spinner } from "@/shared/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import {
  useCreateNotificationChannel,
  useDeleteNotificationChannel,
  useNotificationChannels,
  useNotificationDeliveries,
  useNotificationTestTask,
  useReplaceNotificationChannel,
  useTestNotificationDelivery,
} from "@/features/admin/use-admin";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";

/**
 * Notification Settings (route /admin/settings/notifications; S003). Channels are the
 * Outbox-driven email/DingTalk senders — secrets are never prefilled
 * (secret_configured presence only; create requires a secret, replace keeps
 * or replaces). The deliveries table shows the honest Outbox state machine
 * (queued → sending → succeeded/failed, <=5 attempts then dead_letter);
 * test deliveries go through the same Outbox loop, not a side channel.
 */

function deliveryStateTone(state: NotificationDelivery["state"]): StatusTone {
  switch (state) {
    case "queued":
      return "sky";
    case "sending":
      return "indigo";
    case "succeeded":
      return "emerald";
    case "failed":
      return "rose";
    case "dead_letter":
      return "red";
    default:
      return "stone";
  }
}

function describeErrorText(display: { messageKey: string; requestId: string | null }): string {
  // Translated through the shared i18n bundle so users never see raw keys.
  const text = i18next.t(display.messageKey);
  return display.requestId === null ? text : `${text} (${display.requestId})`;
}

interface ChannelDraft {
  kind: NotificationChannelKind;
  name: string;
  enabled: boolean;
  host: string;
  port: number;
  tlsMode: "required" | "starttls";
  username: string;
  fromAddress: string;
  secret: string;
  // DingTalk-specific: signing secret (required when robot security setting = signature mode); combined with the Webhook
  // into an {access_token, sign_secret} envelope (matching senders.SendDingTalk).
  signSecret: string;
}

const EMPTY_CHANNEL: ChannelDraft = {
  kind: "email",
  name: "",
  enabled: false,
  host: "",
  port: 465,
  tlsMode: "required",
  username: "",
  fromAddress: "",
  secret: "",
  signSecret: "",
};

// DingTalk robot credential = access_token query/path segment in the webhook URL (senders.SendDingTalk).
// Allows pasting either the full Webhook URL (extracting the access_token param) or a bare token directly.
function dingTalkToken(raw: string): string {
  const text = raw.trim();
  try {
    const url = new URL(text);
    const token = url.searchParams.get("access_token");
    if (token !== null && token !== "") return token;
  } catch {
    // Treat non-URL input as a bare access_token.
  }
  return text;
}

// DingTalk credential envelope: when signing secret is filled, use the {access_token, sign_secret} envelope (backend signed
// mode); otherwise send a bare token (compatible with existing channels and unsigned robots).
function dingTalkSecretValue(token: string, signSecret: string): string {
  const sign = signSecret.trim();
  if (sign === "") return token;
  return JSON.stringify({ access_token: token, sign_secret: sign });
}

export default function AdminNotificationsPage() {
  const { t } = useTranslation();

  return (
    <PageLayout data-testid="admin-notifications-page">
      <PageHeader
        title={t("adminNotifications.title")}
        description={t("adminNotifications.description")}
        icon={Bell}
      />
      <SettingsNavTabs current="notifications" />
      <AdminNotificationsContent />
    </PageLayout>
  );
}

export function AdminNotificationsContent() {
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  // S007 three views: switch Channels / Rules / Deliveries on the same page (without inventing new routes).
  const [view, setView] = useState<"channels" | "rules" | "deliveries">("channels");
  const [deliveryStateFilter, setDeliveryStateFilter] = useState<"all" | NotificationDelivery["state"]>("all");
  const pager = useCursorPage(20);
  const channelsQuery = useNotificationChannels(isAdmin, { limit: pager.pageSize, after: pager.after });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const channelsLoaded = !channelsQuery.isPending && !channelsQuery.isError;
  const deliveriesQuery = useNotificationDeliveries(isAdmin);
  useNotificationRules(isAdmin);
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationChannel | null>(null);

  const channels = channelsQuery.data?.items ?? [];
  const deliveries = deliveriesQuery.data?.items ?? [];
  // Channels table uses cursor pagination and create/delete changes its row set; deliveries table updates via polling only.
  const channelsBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(channels.map((channel) => channel.id)),
  );

  return (
    <div className="flex flex-col gap-5">
      {/* tabs-02 reference (owner ruling 2026-10-24): UI library Tabs + TransitionPanel
          switching animation (slide + blur), replacing the hand-rolled button group. */}
      <Tabs
        value={view}
        onValueChange={(next) => {
          if (next === "channels" || next === "rules" || next === "deliveries") setView(next as "channels" | "rules" | "deliveries");
        }}
      >
        <TabsList aria-label={t("adminNotifications.views")}>
          {(
            [
              ["channels", t("adminNotifications.channels")],
              ["rules", t("adminNotifications.rules")],
              ["deliveries", t("adminNotifications.deliveries")],
            ] as const
          ).map(([key, label]) => (
            <TabsTrigger
              key={key}
              value={key}
              data-testid={`admin-notifications-view-${key}`}
            >
              {label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      <TransitionPanel activeIndex={view === "channels" ? 0 : view === "rules" ? 1 : 2}>
        <div className="flex flex-col gap-5">
          {channelsQuery.isPending && <LoadingState variant="page" />}
          {channelsQuery.error !== null && (
            <ErrorState error={channelsQuery.error} operationId="listNotificationChannels" onRetry={() => void channelsQuery.refetch()} />
          )}

          {!channelsQuery.isPending && channelsQuery.error === null && (
            <Card>
          <CardHeader className="border-b pb-3">
            <CardTitle className="flex items-center gap-2">
              <Bell className="size-4" />
              {t("adminNotifications.channels")}
            </CardTitle>
            <CardDescription>{t("adminNotifications.channelsDescription")}</CardDescription>
            {/* Create Channel sits at the far right of the header row (owner round 4 ruling, consistent with other table cards). */}
            <CardAction>
              <Button onClick={() => { setCreateOpen(true); }} data-testid="admin-notifications-create">
                <Plus />
                {t("adminNotifications.create")}
              </Button>
            </CardAction>
          </CardHeader>
          <CardContent className="px-0!">
            {channels.length === 0 ? (
              <EmptyState title={t("adminNotifications.empty")} testId="admin-notifications-empty" />
            ) : (
              <>
              <Table data-testid="admin-notifications-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("adminNotifications.column.kind")}</TableHead>
                    <TableHead>{t("adminNotifications.column.name")}</TableHead>
                    <TableHead>{t("adminNotifications.column.enabled")}</TableHead>
                    <TableHead>{t("adminNotifications.column.secret")}</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody ref={channelsBodyRef} stale={channelsQuery.isFetching && channelsQuery.isPlaceholderData}>
                  {channels.map((channel) => (
                    <ChannelRow key={channel.id} channel={channel} onEdit={() => { setEditing(channel); }} />
                  ))}
                </TableBody>
              </Table>
              </>
            )}
              {channelsLoaded ? (
                <TablePagination
                  busy={channelsQuery.isFetching && channelsQuery.isPlaceholderData}
                  page={pager.pageNo}
                  hasMore={channelsQuery.data.page.has_more}
                  isFirst={pager.isFirst}
                  onPrev={pager.goPrev}
                  onJumpPage={pager.goToPage}
                  onNext={() => {
                    pager.pushCursor(channelsQuery.data.page.next_cursor);
                  }}
                  pageSize={pager.pageSize}
                  onPageSizeChange={pager.setPageSize}
                  rowsOnPage={channelsQuery.data.items.length}
                  testIdPrefix="notifications"
                />
              ) : null}
          </CardContent>
          </Card>
          )}
        </div>

        <div className="flex flex-col gap-5">
          <RulesView isAdmin={isAdmin} />
        </div>

        <div className="flex flex-col gap-5">
        <Card>
        <CardHeader className="border-b pb-3">
          <CardTitle>{t("adminNotifications.deliveries")}</CardTitle>
          <CardDescription>{t("adminNotifications.deliveriesDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="px-0!">
          <div className="mb-3 flex items-center gap-2 px-6">
            <Label htmlFor="delivery-state-filter" className="text-sm text-muted-foreground">
              {t("adminNotifications.filter.state")}
            </Label>
            <Select
              value={deliveryStateFilter}
              onValueChange={(value) => { if (value !== null) { setDeliveryStateFilter(value); } }}
            >
              <SelectTrigger id="delivery-state-filter" className="w-40" data-testid="delivery-state-filter">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t("adminNotifications.filter.all")}</SelectItem>
                <SelectItem value="queued">{t("adminNotifications.deliveryState.queued")}</SelectItem>
                <SelectItem value="sending">{t("adminNotifications.deliveryState.sending")}</SelectItem>
                <SelectItem value="succeeded">{t("adminNotifications.deliveryState.succeeded")}</SelectItem>
                <SelectItem value="failed">{t("adminNotifications.deliveryState.failed")}</SelectItem>
                <SelectItem value="dead_letter">{t("adminNotifications.deliveryState.dead_letter")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {deliveriesQuery.isPending ? (
            <LoadingState variant="table" columns={8} />
          ) : deliveriesQuery.error !== null ? (
            <ErrorState
              error={deliveriesQuery.error}
              operationId="listNotificationDeliveries"
              onRetry={() => void deliveriesQuery.refetch()}
            />
          ) : deliveries.length === 0 ? (
            <EmptyState
              title={t("adminNotifications.deliveries")}
              description={t("adminNotifications.deliveriesDescription")}
            />
          ) : (
            <Table data-testid="admin-deliveries-table">
              <TableHeader>
                <TableRow>
                  <TableHead>{t("adminNotifications.column.event")}</TableHead>
                  <TableHead>{t("adminNotifications.column.channelCol")}</TableHead>
                  <TableHead>{t("adminNotifications.column.target")}</TableHead>
                  <TableHead>{t("adminNotifications.column.state")}</TableHead>
                  <TableHead>{t("adminNotifications.column.attempts")}</TableHead>
                  <TableHead>{t("adminNotifications.column.nextAttempt")}</TableHead>
                  <TableHead>{t("adminNotifications.column.lastError")}</TableHead>
                  <TableHead>{t("adminNotifications.column.createdAt")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {deliveries
                  .filter((delivery) => deliveryStateFilter === "all" || delivery.state === deliveryStateFilter)
                  .map((delivery) => (
                  <TableRow key={delivery.id} data-testid={`admin-delivery-row-${delivery.id}`}>
                    <TableCell className="font-mono text-xs">{delivery.event_type}</TableCell>
                    <TableCell>{delivery.channel_name}</TableCell>
                    <TableCell className="font-mono text-xs">{delivery.masked_target}</TableCell>
                    <TableCell>
                      <StatusBadge tone={deliveryStateTone(delivery.state)}>
                        {t(`adminNotifications.deliveryState.${delivery.state}`)}
                      </StatusBadge>
                    </TableCell>
                    <TableCell className="text-base font-semibold tabular-nums text-foreground">
                      {delivery.delivery_attempt_count}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {formatTimestamp(delivery.next_attempt_at)}
                    </TableCell>
                    <TableCell className="text-muted-foreground font-mono">
                      {delivery.last_error_code ?? "—"}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {formatTimestamp(delivery.created_at)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
        </div>
      </TransitionPanel>

      <ChannelFormDialog open={createOpen && view === "channels"} editing={null} onClose={() => { setCreateOpen(false); }} />
      <ChannelFormDialog open={editing !== null} editing={editing} onClose={() => { setEditing(null); }} />
    </div>
  );
}

function ChannelRow({ channel, onEdit }: { channel: NotificationChannel; onEdit: () => void }) {
  const { t } = useTranslation();
  const deleteChannel = useDeleteNotificationChannel();
  const testDelivery = useTestNotificationDelivery();
  // 2026-10-31 round 3: test delivery is an async task — the acceptance receipt task_id is polled via
  // useNotificationTestTask until terminal state, showing inline whether delivery succeeded.
  const [testTaskId, setTestTaskId] = useState<string | null>(null);
  const testTaskQuery = useNotificationTestTask(testTaskId);
  const rulesQuery = useNotificationRules(true);
  const referencedRules = (rulesQuery.data?.items ?? []).filter((rule) => rule.channel_id === channel.id);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);

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
              onClick={() => { testDelivery.mutate(
                  { channelId: channel.id, body: {} },
                  {
                    onSuccess: (receipt) => {
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
                ); }
              }
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
      {errorText !== null && (
        <TableRow>
          <TableCell colSpan={5}>
            <Alert variant="destructive">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          </TableCell>
        </TableRow>
      )}
      {testTaskId !== null && (
        <TableRow>
          <TableCell colSpan={5} data-testid={`admin-channel-test-result-${channel.id}`}>
            {(() => {
              // Terminal state = succeeded/failed/cancelled; query error or non-terminal = in progress (consumed asynchronously by Worker;
              // real backend test deliveries are not written to the deliveries table, so results live only on the task surface).
              const state = testTaskQuery.data?.state;
              if (state === "succeeded") {
                return (
                  <div className="flex items-center gap-2 text-xs text-[var(--risk-safe)]">
                    <CheckCircle2 className="size-4" aria-hidden />
                    {t("adminNotifications.testResult.succeeded")}
                  </div>
                );
              }
              if (state === "failed" || state === "cancelled") {
                const failureName = testTaskQuery.data?.error?.title ?? testTaskQuery.error?.message;
                return (
                  <div className="flex items-center gap-2 text-xs text-destructive">
                    <AlertTriangle className="size-4" aria-hidden />
                    {failureName !== undefined && failureName !== ""
                      ? t("adminNotifications.testResult.failedWithReason", { reason: failureName })
                      : t("adminNotifications.testResult.failed")}
                  </div>
                );
              }
              return (
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Spinner className="size-4" aria-hidden />
                  {t("adminNotifications.testResult.running")}
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
              onClick={() => { deleteChannel.mutate(
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
                ); }
              }
            >
              {t("common.delete")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function ChannelFormDialog({
  open,
  editing,
  onClose,
}: {
  open: boolean;
  editing: NotificationChannel | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const createChannel = useCreateNotificationChannel();
  const replaceChannel = useReplaceNotificationChannel();
  const [draft, setDraft] = useState<ChannelDraft>(EMPTY_CHANNEL);
  const [openFor, setOpenFor] = useState<string | null>(null);
  const [errorText, setErrorText] = useState<string | null>(null);

  const formKey = editing?.id ?? "create";
  if (open && openFor !== formKey) {
    setOpenFor(formKey);
    setErrorText(null);
    if (editing === null) {
      setDraft(EMPTY_CHANNEL);
    } else {
      const configuration = editing.configuration as unknown as Record<string, string | number>;
      setDraft({
        kind: editing.kind,
        name: editing.name,
        enabled: editing.enabled,
        host: String(configuration.host ?? ""),
        port: Number(configuration.port ?? 465),
        tlsMode: configuration.tls_mode as "required" | "starttls",
        username: String(configuration.username ?? ""),
        fromAddress: String(configuration.from_address ?? ""),
        secret: "",
        signSecret: "",
      });
    }
  }
  if (!open && openFor !== null) setOpenFor(null);

  const submit = async () => {
    setErrorText(null);
    // DingTalk: input accepts either a full Webhook URL or a bare token, normalized on submit to
    // access_token per backend contract; if a signing secret is provided, assemble an {access_token, sign_secret} envelope.
    const dingToken = draft.kind === "dingtalk" ? dingTalkToken(draft.secret) : "";
    const secretValue = draft.kind === "dingtalk"
      ? dingTalkSecretValue(dingToken, draft.signSecret)
      : draft.secret;
    const configuration: EmailConfig | DingTalkConfig | FeishuConfig | SlackConfig =
      draft.kind === "dingtalk"
        ? { webhook_host: "oapi.dingtalk.com" }
        : draft.kind === "feishu"
          ? {}
          : draft.kind === "slack"
            ? {}
            : {
                host: draft.host,
                port: draft.port,
                tls_mode: draft.tlsMode,
                username: draft.username,
                from_address: draft.fromAddress,
              };
    if (editing === null && (draft.kind === "dingtalk" ? dingToken : draft.secret) === "") {
      setErrorText(
        draft.kind === "dingtalk"
          ? t("adminNotifications.webhookRequired")
          : t("adminNotifications.secretRequired"),
      );
      return;
    }
    try {
      if (editing === null) {
        await createChannel.mutateAsync({
          kind: draft.kind,
          name: draft.name,
          enabled: draft.enabled,
          configuration,
          secret: { value: secretValue },
        });
      } else {
        await replaceChannel.mutateAsync({
          channelId: editing.id,
          version: editing.version,
          body: {
            kind: draft.kind,
            name: draft.name,
            enabled: draft.enabled,
            configuration,
            secret: secretValue === "" ? undefined : { value: secretValue },
          },
        });
      }
      appToast.success(t("common.save"), { description: draft.name });
      onClose();
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, editing === null ? "createNotificationChannel" : "replaceNotificationChannel"),
      );
      setErrorText(errText);
      appToast.error(errText);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing === null ? t("adminNotifications.createTitle") : t("adminNotifications.editTitle", { name: editing.name })}</DialogTitle>
          <DialogDescription>{t("adminNotifications.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="channel-kind">{t("adminNotifications.column.kind")}</Label>
            <Select
              value={draft.kind}
              disabled={editing !== null}
              onValueChange={(value) => {
                if (value !== "email" && value !== "dingtalk" && value !== "feishu" && value !== "slack") return;
                setDraft({ ...draft, kind: value });
              }}
            >
              <SelectTrigger
                id="channel-kind"
                className="w-48"
                data-testid="channel-kind"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="email">email</SelectItem>
                <SelectItem value="dingtalk">dingtalk</SelectItem>
                <SelectItem value="feishu">feishu</SelectItem>
                <SelectItem value="slack">slack</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="channel-name">{t("adminNotifications.column.name")}</Label>
            <Input id="channel-name" value={draft.name} onChange={(event) => { setDraft({ ...draft, name: event.target.value }); }} maxLength={128} data-testid="channel-name" />
          </div>
          <div className="flex items-center gap-2">
            <Checkbox
              id="channel-enabled"
              checked={draft.enabled}
              onCheckedChange={(checked) => { setDraft({ ...draft, enabled: checked }); }}
              data-testid="channel-enabled"
            />
            <Label htmlFor="channel-enabled" className="cursor-pointer">
              {t("adminNotifications.column.enabled")}
            </Label>
          </div>
          {draft.kind === "email" && (
            <>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-host">{t("adminNotifications.field.host")}</Label>
                <Input id="channel-host" value={draft.host} onChange={(event) => { setDraft({ ...draft, host: event.target.value }); }} data-testid="channel-host" />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-port">{t("adminNotifications.field.port")}</Label>
                <NumberInput id="channel-port" min={1} max={65535} value={draft.port} onChange={(event) => { setDraft({ ...draft, port: Number(event.target.value) }); }} data-testid="channel-port" />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-tls">{t("adminNotifications.field.tls")}</Label>
                <Select
                  value={draft.tlsMode}
                  onValueChange={(value) => {
                    if (value !== "required" && value !== "starttls") return;
                    setDraft({ ...draft, tlsMode: value });
                  }}
                >
                  <SelectTrigger
                    id="channel-tls"
                    className="w-48"
                    data-testid="channel-tls"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="required">required</SelectItem>
                    <SelectItem value="starttls">starttls</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-username">{t("adminNotifications.field.username")}</Label>
                <Input id="channel-username" value={draft.username} onChange={(event) => { setDraft({ ...draft, username: event.target.value }); }} data-testid="channel-username" />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-from">{t("adminNotifications.field.from")}</Label>
                <Input id="channel-from" type="email" value={draft.fromAddress} onChange={(event) => { setDraft({ ...draft, fromAddress: event.target.value }); }} data-testid="channel-from" />
              </div>
            </>
          )}
          {draft.kind === "dingtalk" ? (
            // DingTalk credential = robot Webhook URL (containing access_token) + optional signing secret,
            // rendered as two separate inputs; on submit, parse access_token and assemble the envelope as needed
            // (matching senders.SendDingTalk).
            <>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-webhook">{t("adminNotifications.field.webhook")}</Label>
                <PasswordInput
                  id="channel-webhook"
                  value={draft.secret}
                  onChange={(event) => { setDraft({ ...draft, secret: event.target.value }); }}
                  autoComplete="new-password"
                  data-testid="channel-webhook"
                />
                <p className="text-muted-foreground text-xs">
                  {editing === null
                    ? t("adminNotifications.field.webhookCreateHint")
                    : t("adminNotifications.field.webhookEditHint")}
                </p>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-sign-secret">{t("adminNotifications.field.signSecret")}</Label>
                <PasswordInput
                  id="channel-sign-secret"
                  value={draft.signSecret}
                  onChange={(event) => { setDraft({ ...draft, signSecret: event.target.value }); }}
                  autoComplete="new-password"
                  data-testid="channel-sign-secret"
                />
                <p className="text-muted-foreground text-xs">
                  {t("adminNotifications.field.signSecretHint")}
                </p>
              </div>
            </>
          ) : (
            <div className="flex flex-col gap-2">
              <Label htmlFor="channel-secret">{t("adminNotifications.field.secret")}</Label>
              <PasswordInput
                id="channel-secret"
                value={draft.secret}
                onChange={(event) => { setDraft({ ...draft, secret: event.target.value }); }}
                autoComplete="new-password"
                data-testid="channel-secret"
              />
              <p className="text-muted-foreground text-xs">
                {draft.kind === "feishu" || draft.kind === "slack"
                  ? editing === null
                    ? t("adminNotifications.field.secretWebhookCreateHint")
                    : t("adminNotifications.field.secretWebhookEditHint")
                  : editing === null
                    ? t("adminNotifications.field.secretCreateHint")
                    : t("adminNotifications.field.secretEditHint")}
              </p>
            </div>
          )}
          {errorText !== null && (
            <Alert variant="destructive" data-testid="channel-form-error">
              <AlertTitle>{t("adminNotifications.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={createChannel.isPending || replaceChannel.isPending}
            disabled={draft.name.trim() === ""}
            onClick={() => void submit()}
            data-testid="channel-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
