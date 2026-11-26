import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Bell, Plus } from "lucide-react";
import type {
  NotificationChannel,
  NotificationDelivery,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { ChannelFormDialog } from "@/routes/admin/admin-notification-channel-dialogs";
import { ChannelRow } from "@/routes/admin/admin-notification-channel-row";
import { RulesView } from "@/routes/admin/admin-notification-rules-view";
import {
  useNotificationChannels,
  useNotificationDeliveries,
  useNotificationRules,
  useReplaceSettings,
  useSettingsNamespace,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import type { StatusTone } from "@/shared/lib/status-tone";
import { formatTimestamp } from "@/shared/lib/timezones";
import { Button } from "@/shared/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Label } from "@/shared/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";

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
  const channelsLoaded = !channelsQuery.isPending && !channelsQuery.error;
  const deliveriesQuery = useNotificationDeliveries(isAdmin);
  useNotificationRules(isAdmin);
  const generalSettingsQuery = useSettingsNamespace("general", isAdmin);
  const replaceSettings = useReplaceSettings();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationChannel | null>(null);

  const channels = channelsQuery.data?.items ?? [];
  const deliveries = deliveriesQuery.data?.items ?? [];
  const generalSettingsData = generalSettingsQuery.data;
  const templateLocale: "zh-CN" | "en-US" =
    generalSettingsData !== undefined &&
    "default_locale" in generalSettingsData.settings &&
    generalSettingsData.settings.default_locale === "en-US"
      ? "en-US"
      : "zh-CN";
  const templateLocaleLabels: Record<"zh-CN" | "en-US", string> = {
    "zh-CN": t("adminNotifications.templateLocale.zhCN"),
    "en-US": t("adminNotifications.templateLocale.enUS"),
  };
  // Channels table uses cursor pagination and create/delete changes its row set; deliveries table updates via polling only.
  const channelsBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(channels.map((channel) => channel.id)),
  );

  return (
    <div className="flex flex-col gap-5">
      {/* tabs-02 reference (owner ruling 2026-10-24): UI library Tabs + TransitionPanel
          switching animation (slide + blur), replacing the hand-rolled button group. */}
      <div className="flex flex-wrap items-center justify-between gap-3">
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

        <div className="flex flex-wrap items-center gap-2">
          <Label htmlFor="notifications-default-locale" className="text-muted-foreground whitespace-nowrap text-xs">
            {t("adminNotifications.templateLocale.label")}
          </Label>
          <Select
            value={templateLocale}
            items={templateLocaleLabels}
            disabled={generalSettingsData === undefined || replaceSettings.isPending}
            onValueChange={(value) => {
              if (value !== "zh-CN" && value !== "en-US") return;
              if (generalSettingsData === undefined || value === templateLocale) return;
              replaceSettings.mutate(
                {
                  namespace: "general",
                  version: generalSettingsData.version,
                  settings: { ...generalSettingsData.settings, default_locale: value },
                  impactToken: null,
                },
                {
                  onSuccess: () => {
                    appToast.success(t("adminNotifications.templateLocale.saved"));
                  },
                  onError: (error) => {
                    appToast.error(describeErrorText(describeError(error, "replaceSettingsNamespace")));
                  },
                },
              );
            }}
          >
            <SelectTrigger
              id="notifications-default-locale"
              className="h-8 w-36 text-xs"
              data-testid="notifications-default-locale"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="zh-CN">{templateLocaleLabels["zh-CN"]}</SelectItem>
              <SelectItem value="en-US">{templateLocaleLabels["en-US"]}</SelectItem>
            </SelectContent>
          </Select>
          <span className="text-muted-foreground text-xs">
            {t("adminNotifications.templateLocale.hint")}
          </span>
        </div>
      </div>

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
                  {(
                    [
                      "adminNotifications.column.event",
                      "adminNotifications.column.channelCol",
                      "adminNotifications.column.target",
                      "adminNotifications.column.state",
                      "adminNotifications.column.attempts",
                      "adminNotifications.column.nextAttempt",
                      "adminNotifications.column.lastError",
                      "adminNotifications.column.createdAt",
                    ] as const
                  ).map((key) => (
                    <TableHead key={key}>{t(key)}</TableHead>
                  ))}
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

      {createOpen && view === "channels" ? (
        <ChannelFormDialog key="create" editing={null} onClose={() => { setCreateOpen(false); }} />
      ) : null}
      {editing !== null ? (
        <ChannelFormDialog key={editing.id} editing={editing} onClose={() => { setEditing(null); }} />
      ) : null}
    </div>
  );
}
