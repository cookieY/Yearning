import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useLocation, useParams } from "react-router";
import { Bell, SlidersHorizontal } from "lucide-react";
import type {
  ExecutionWindowDaysOfWeekItem,
  SettingsImpactAssessment,
  SettingsValue,
  OscOverrides,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { BusinessError } from "@/shared/api/mutator";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent } from "@/shared/components/ui/card";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Input } from "@/shared/components/ui/input";
import { NumberInput } from "@/shared/components/ui/number-input";
import { formatTimestamp, timezoneOptions } from "@/shared/lib/timezones";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { Label } from "@/shared/components/ui/label";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { AdminIdentityProvidersContent } from "@/routes/admin/admin-identity-providers-page";
import { AdminNotificationsContent } from "@/routes/admin/admin-notifications-page";
import {
  useAssessSettingsImpact,
  useIdentityProviders,
  useNotificationChannels,
  useNotificationDeliveries,
  useNotificationRules,
  useReplaceSettings,
  useSettingsNamespace,
} from "@/features/admin/use-admin";

/**
 * Settings family (routes /admin/settings/{general,query,execution,branding,notifications}; §2
 * splits legacy /manager/setting into typed namespaces — S006: no RPC,
 * automated-task, query-limit or per-order-budget fields ever return). The F9
 * ai-budget page keeps its dedicated two-step confirmation flow; this
 * unified settings workspace serves the five settings tabs so the PageHeader
 * and SettingsNavTabs stay stationary while TransitionPanel animates the body.
 */

import { SettingsNavTabs, type SettingsRouteNamespace } from "@/routes/admin/settings-tabs";

type SettingsPageNamespace = Exclude<SettingsRouteNamespace, "notifications">;
const ALL_SETTINGS_NAMESPACES: SettingsRouteNamespace[] = [
  "general",
  "query",
  "execution",
  "branding",
  "notifications",
];

export default function AdminSettingsNamespacePage() {
  const { namespace: paramNamespace } = useParams();
  const location = useLocation();
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;

  // Pre-warm all five settings tabs so switching tabs never flashes a
  // skeleton LoadingState on first visit or rapid tab switching.
  useSettingsNamespace("general", isAdmin);
  useIdentityProviders(isAdmin, { limit: 20, after: undefined });
  useSettingsNamespace("query", isAdmin);
  useSettingsNamespace("execution", isAdmin);
  useSettingsNamespace("branding", isAdmin);
  useNotificationChannels(isAdmin, { limit: 20, after: undefined });
  useNotificationRules(isAdmin);
  useNotificationDeliveries(isAdmin);

  const rawNamespace =
    paramNamespace ?? (location.pathname.endsWith("/notifications") ? "notifications" : "");
  const routeNamespace: SettingsRouteNamespace = ALL_SETTINGS_NAMESPACES.includes(
    rawNamespace as SettingsRouteNamespace,
  )
    ? (rawNamespace as SettingsRouteNamespace)
    : "general";
  const [prevRouteNamespace, setPrevRouteNamespace] = useState<SettingsRouteNamespace>(routeNamespace);
  const [activeTab, setActiveTab] = useState<SettingsRouteNamespace>(routeNamespace);
  if (prevRouteNamespace !== routeNamespace) {
    setPrevRouteNamespace(routeNamespace);
    setActiveTab(routeNamespace);
  }

  if (!ALL_SETTINGS_NAMESPACES.includes(rawNamespace as SettingsRouteNamespace)) {
    return <Navigate to="/admin/settings/general" replace />;
  }
  const current = activeTab;
  const isNotifications = current === "notifications";

  return (
    <PageLayout data-testid={isNotifications ? "admin-notifications-page" : `admin-settings-${current}`}>
      <PageHeader
        title={isNotifications ? t("adminNotifications.title") : t(`adminSettings.${current}.title`)}
        description={
          isNotifications
            ? t("adminNotifications.description")
            : t(`adminSettings.${current}.description`)
        }
        icon={isNotifications ? Bell : SlidersHorizontal}
      />
      <SettingsNavTabs current={current} onSelect={setActiveTab} />
      <TransitionPanel activeIndex={ALL_SETTINGS_NAMESPACES.indexOf(current)}>
        <NamespaceSettings key="general" namespace="general" />
        <NamespaceSettings key="query" namespace="query" />
        <NamespaceSettings key="execution" namespace="execution" />
        <NamespaceSettings key="branding" namespace="branding" />
        <AdminNotificationsContent key="notifications" />
      </TransitionPanel>
    </PageLayout>
  );
}

function NamespaceSettings({ namespace }: { namespace: SettingsPageNamespace }) {
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const settingsQuery = useSettingsNamespace(namespace, isAdmin);
  const assessMutation = useAssessSettingsImpact();
  const replaceMutation = useReplaceSettings();
  const [draftFor, setDraftFor] = useState<number | null>(() => settingsQuery.data?.version ?? null);
  const [draft, setDraft] = useState<SettingsValue | null>(() =>
    settingsQuery.data === undefined ? null : structuredClone(settingsQuery.data.settings),
  );
  const [assessment, setAssessment] = useState<SettingsImpactAssessment | null>(null);
  const [errorText, setErrorText] = useState<string | null>(null);
  const [savedNote, setSavedNote] = useState(false);

  // Draft initialization keyed by the loaded namespace version: a remount
  // or a reload after save re-seeds the form exactly once, without a
  // setState-in-effect cascade. The saved note and the (auto-generated)
  // assessment intentionally survive the post-save re-seed — both hide on
  // their own once the form goes dirty again; wiping them here made every
  // successful save look like a silent no-op (§18.47 follow-up).
  if (settingsQuery.data !== undefined && draftFor !== settingsQuery.data.version) {
    setDraftFor(settingsQuery.data.version);
    setDraft(structuredClone(settingsQuery.data.settings));
  }

  const dirty = useMemo(() => {
    if (draft === null || settingsQuery.data === undefined) return false;
    return JSON.stringify(draft) !== JSON.stringify(settingsQuery.data.settings);
  }, [draft, settingsQuery.data]);

  const highImpact = assessment?.impact.level === "high";

  // High-impact saves need a one-time impact token. Rather than making the
  // user run the assess ceremony by hand, a PRECONDITION_REQUIRED (1011)
  // answer triggers exactly one automatic assess → retry cycle with the
  // fresh token (server-driven: the client never guesses impact levels);
  // the resulting assessment renders so the effects are still shown.
  const save = (impactToken: string | null): void => {
    if (draft === null) return;
    setErrorText(null);
    replaceMutation.mutate(
      {
        namespace,
        version: settingsQuery.data === undefined ? 1 : settingsQuery.data.version,
        settings: draft,
        impactToken,
      },
      {
        onSuccess: () => {
          setSavedNote(true);
          appToast.success(t("common.save"));
        },
        onError: (error) => {
          if (error instanceof BusinessError && error.err_code === 1011 && impactToken === null) {
            assessMutation.mutate(
              { namespace, settings: draft },
              {
                onSuccess: (next) => {
                  setAssessment(next);
                  save(next.impact_token);
                },
                onError: (assessError) => {
                  const errText = describeErrorText(describeError(assessError, "assessSettingsImpact"));
                  setErrorText(errText);
                  appToast.error(errText);
                },
              },
            );
            return;
          }
          const errText = describeErrorText(describeError(error, "replaceSettingsNamespace"));
          setErrorText(errText);
          appToast.error(errText);
        },
      },
    );
  };

  return (
    <div className="flex flex-col gap-5">
      {settingsQuery.isPending && <LoadingState variant="panel" columns={2} />}
      {settingsQuery.error !== null && (
        <ErrorState
          error={settingsQuery.error}
          operationId="getSettingsNamespace"
          onRetry={() => void settingsQuery.refetch()}
        />
      )}

      {draft !== null && (
        <Card>
          {/* §18.55: the redundant inner card header (Settings Form / Edit per declared Schema)
              was removed by owner ruling — the page header already names the
              namespace. */}
          <CardContent className="flex flex-col gap-4">
            {namespace === "general" && <GeneralFields draft={draft} setDraft={setDraft} />}
            {namespace === "query" && <QueryFields draft={draft} setDraft={setDraft} />}
            {namespace === "execution" && <ExecutionFields draft={draft} setDraft={setDraft} />}
            {namespace === "branding" && <BrandingFields draft={draft} setDraft={setDraft} />}

            {assessment !== null && (
              <div className="flex flex-col gap-2 rounded-md border p-3" data-testid="settings-assessment">
                <div className="flex items-center gap-2 text-sm">
                  <Badge variant={highImpact ? "destructive" : "secondary"}>
                    {t("adminSettings.assessment.level", { level: assessment.impact.level })}
                  </Badge>
                  <span className="text-muted-foreground text-xs">
                    {t("adminSettings.assessment.expiresAt", {
                      time: formatTimestamp(assessment.expires_at),
                    })}
                  </span>
                </div>
                <ul className="text-muted-foreground list-disc pl-4 text-xs">
                  {assessment.impact.effects.map((effect, index) => (
                    <li key={index}>
                      {effect.kind} × {effect.count} — {effect.consequence}
                    </li>
                  ))}
                </ul>
              </div>
            )}

            {savedNote && !dirty && (
              <p className="text-xs" data-testid="settings-saved-note">
                {t("adminSettings.saved")}
              </p>
            )}
            {errorText !== null && (
              <Alert variant="destructive" data-testid="settings-error">
                <AlertTitle>{t("adminSettings.saveFailed")}</AlertTitle>
                <AlertDescription>{errorText}</AlertDescription>
              </Alert>
            )}

            {namespace === "general" && !timezoneStructureOk(generalTimezoneOf(draft)) && (
              <Alert variant="destructive" data-testid="settings-timezone-blocked">
                <AlertTitle>{t("adminSettings.saveFailed")}</AlertTitle>
                <AlertDescription>{t("adminSettings.general.timezoneInvalid")}</AlertDescription>
              </Alert>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <PendingButton
                variant="outline"
                disabled={!dirty || assessMutation.isPending || (namespace === "general" && !timezoneStructureOk(generalTimezoneOf(draft)))}
                pending={assessMutation.isPending}
                onClick={() => {
                  assessMutation.mutate(
                    { namespace, settings: draft },
                    {
                      onSuccess: (next) => { setAssessment(next); },
                      onError: (error) => {
                        setErrorText(describeErrorText(describeError(error, "assessSettingsImpact")));
                      },
                    },
                  );
                }}
                data-testid="settings-assess"
              >
                {t("adminSettings.assess")}
              </PendingButton>
              <PendingButton
                disabled={!dirty || replaceMutation.isPending || assessMutation.isPending || (namespace === "general" && !timezoneStructureOk(generalTimezoneOf(draft)))}
                pending={replaceMutation.isPending}
                onClick={() => { save(assessment !== null && highImpact ? assessment.impact_token : null); }}
                data-testid="settings-save"
              >
                {t("common.save")}
              </PendingButton>
            </div>
          </CardContent>
        </Card>
      )}
      {namespace === "general" && <AdminIdentityProvidersContent embedded />}
    </div>
  );
}

type DraftSetter = (next: SettingsValue) => void;

/** F-SET-001: structural timezone validation (matching backend structural rules — non-empty segments, character set,
 * not "Local", length; whether the IANA zone name actually exists is authoritatively decided and surfaced by the server). */
function timezoneStructureOk(value: string): boolean {
  if (value === "" || value.length > 64 || value === "Local") return false;
  if (value.split("/").some((segment) => segment === "")) return false;
  return /^[A-Za-z0-9_+/-]+$/.test(value);
}

/** `SettingsValue` is a five-namespace union type; the general panel guard only reads
 * the timezone field when namespace==="general", safely narrowing via the `in` operator. */
function generalTimezoneOf(draft: SettingsValue): string {
  return "system_timezone" in draft ? draft.system_timezone : "";
}

function GeneralFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("registration_enabled" in draft)) return null;
  return (
    <>
      <div className="flex items-center gap-2">
        <Checkbox
          id="settings-general-registration"
          checked={draft.registration_enabled}
          onCheckedChange={(checked) => { setDraft({ ...draft, registration_enabled: checked }); }}
          data-testid="settings-general-registration"
        />
        <Label htmlFor="settings-general-registration" className="cursor-pointer">
          {t("adminSettings.general.registration")}
        </Label>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-general-timezone">{t("adminSettings.general.timezone")}</Label>
        {/* Owner feedback 2026-11-05: the timezone is picked, not typed. */}
        <Select
          value={draft.system_timezone}
          onValueChange={(value) => {
            if (typeof value !== "string") return;
            setDraft({ ...draft, system_timezone: value });
          }}
        >
          <SelectTrigger
            id="settings-general-timezone"
            className="w-72"
            aria-invalid={!timezoneStructureOk(draft.system_timezone)}
            data-testid="settings-general-timezone"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <ScrollArea viewportClassName="max-h-60">
              {timezoneOptions(draft.system_timezone).map((zone) => (
                <SelectItem key={zone} value={zone}>
                  {zone}
                </SelectItem>
              ))}
            </ScrollArea>
          </SelectContent>
        </Select>
        {!timezoneStructureOk(draft.system_timezone) && (
          <p className="text-destructive text-xs" data-testid="settings-general-timezone-error">
            {t("adminSettings.general.timezoneInvalid")}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-general-locale">{t("adminSettings.general.locale")}</Label>
        {/* The system default locale is a server setting (settings-namespaces
            `general.default_locale`); the header switcher only changes this
            browser's session, so the admin editor must stay. */}
        <Select
          value={draft.default_locale}
          onValueChange={(value) => {
            if (value !== "zh-CN" && value !== "en-US") return;
            setDraft({ ...draft, default_locale: value });
          }}
        >
          <SelectTrigger
            id="settings-general-locale"
            className="w-48"
            data-testid="settings-general-locale"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="zh-CN">zh-CN</SelectItem>
            <SelectItem value="en-US">en-US</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </>
  );
}

function QueryFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("approval_enabled" in draft)) return null;
  return (
    <>
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <Checkbox
            id="settings-query-approval"
            checked={draft.approval_enabled}
            onCheckedChange={(checked) => { setDraft({ ...draft, approval_enabled: checked }); }}
            data-testid="settings-query-approval"
          />
          <Label htmlFor="settings-query-approval" className="cursor-pointer">
            {t("adminSettings.query.approval")}
          </Label>
        </div>
        <p className="text-muted-foreground text-xs">{t("adminSettings.query.approvalHint")}</p>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-query-timeout">{t("adminSettings.query.timeout")}</Label>
        <NumberInput
          id="settings-query-timeout"
          min={1}
          max={300000}
          value={draft.default_timeout_ms}
          onChange={(event) => { setDraft({ ...draft, default_timeout_ms: Number(event.target.value) }); }}
          data-testid="settings-query-timeout"
        />
        <p className="text-muted-foreground text-xs">{t("adminSettings.query.timeoutHint")}</p>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-query-duration">{t("adminSettings.query.maxDuration")}</Label>
        <NumberInput
          id="settings-query-duration"
          min={1}
          max={525600}
          value={draft.maximum_approved_access_duration_minutes}
          onChange={(event) => { setDraft({ ...draft, maximum_approved_access_duration_minutes: Number(event.target.value) }); }
          }
          data-testid="settings-query-duration"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-query-max-rows">{t("adminSettings.query.maxResultRows")}</Label>
        <NumberInput
          id="settings-query-max-rows"
          min={1}
          max={100000}
          value={draft.max_result_rows}
          onChange={(event) => { setDraft({ ...draft, max_result_rows: Number(event.target.value) }); }}
          data-testid="settings-query-max-rows"
        />
        <p className="text-muted-foreground text-xs">{t("adminSettings.query.maxResultRowsHint")}</p>
      </div>
      <Alert>
        <AlertDescription data-testid="settings-query-no-limit">{t("adminSettings.query.noLimitNote")}</AlertDescription>
      </Alert>
    </>
  );
}

const WEEKDAYS: readonly ExecutionWindowDaysOfWeekItem[] = [
  "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
];

type OscStringKey = "max_load" | "critical_load";
type OscNumericKey =
  | "chunk_size"
  | "dml_batch_size"
  | "max_lag"
  | "nice_ratio"
  | "cut_over_lock_timeout_seconds"
  | "retries"
  | "critical_load_interval_millis"
  | "critical_load_hibernate_seconds"
  | "heartbeat_interval_millis"
  | "exponential_backoff_max_interval";
type OscBooleanKey =
  | "exact_rowcount"
  | "concurrent_rowcount"
  | "ok_to_drop_table"
  | "timestamp_old_table"
  | "cut_over_exponential_backoff"
  | "attempt_instant_ddl";

function ExecutionFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("restriction_enabled" in draft)) return null;

  /** Strip empty values: all keys on OscOverrides are optional, so empty strings are omitted (pattern would reject "").
   * exec captures the `in`-narrowed ExecutionSettings (parameter narrowing does not persist across closures). */
  const exec = draft;
  function setOscStringDefault(key: OscStringKey, raw: string): void {
    const trimmed = raw.trim();
    const previous: OscOverrides = exec.osc.defaults;
    const { [key]: _removed, ...rest } = previous;
    const base: OscOverrides = rest;
    if (trimmed !== "") base[key] = trimmed;
    setDraft({ ...exec, osc: { ...exec.osc, defaults: base } });
  }
  function setOscNumericDefault(key: OscNumericKey, raw: string): void {
    const trimmed = raw.trim();
    const previous: OscOverrides = exec.osc.defaults;
    const { [key]: _removed, ...rest } = previous;
    const base: OscOverrides = rest;
    const parsed = Number(trimmed);
    if (trimmed !== "" && !Number.isNaN(parsed)) base[key] = parsed;
    setDraft({ ...exec, osc: { ...exec.osc, defaults: base } });
  }
  function setOscBooleanDefault(key: OscBooleanKey, next: boolean | undefined): void {
    const previous: OscOverrides = exec.osc.defaults;
    const { [key]: _removed, ...rest } = previous;
    const base: OscOverrides = rest;
    if (next !== undefined) base[key] = next;
    setDraft({ ...exec, osc: { ...exec.osc, defaults: base } });
  }
  return (
    <>
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <Checkbox
            id="settings-execution-restriction"
            checked={draft.restriction_enabled}
            onCheckedChange={(checked) => { setDraft({ ...draft, restriction_enabled: checked }); }}
            data-testid="settings-execution-restriction"
          />
          <Label htmlFor="settings-execution-restriction" className="cursor-pointer">
            {t("adminSettings.execution.restriction")}
          </Label>
        </div>
        <p className="text-muted-foreground text-xs">{t("adminSettings.execution.restrictionHint")}</p>
      </div>
      <div className="flex flex-col gap-2">
        <Label>{t("adminSettings.execution.windows")}</Label>
        <p className="text-muted-foreground text-xs">{t("adminSettings.execution.windowsHint")}</p>
        <div className="flex flex-col gap-1">
          {draft.windows.map((window, index) => (
            <div key={index} className="flex items-center gap-2 text-xs">
              <Input
                value={window.start_local_time}
                onChange={(event) => {
                  const windows = [...draft.windows];
                  windows[index] = { ...window, start_local_time: event.target.value };
                  setDraft({ ...draft, windows });
                }}
                className="h-8 font-mono text-xs"
                placeholder="HH:MM"
              />
              <span>→</span>
              <Input
                value={window.end_local_time}
                onChange={(event) => {
                  const windows = [...draft.windows];
                  windows[index] = { ...window, end_local_time: event.target.value };
                  setDraft({ ...draft, windows });
                }}
                className="h-8 font-mono text-xs"
                placeholder="HH:MM"
              />
              {/* Owner feedback 2026-11-05: weekdays are picked from the seven
                  fixed values (multi-select), not typed as a comma list. */}
              <Select
                multiple
                value={window.days_of_week}
                onValueChange={(next) => {
                  const picked = new Set<string>(next);
                  const windows = [...draft.windows];
                  windows[index] = {
                    ...window,
                    days_of_week: WEEKDAYS.filter((day) => picked.has(day)),
                  };
                  setDraft({ ...draft, windows });
                }}
              >
                <SelectTrigger
                  className="h-8 w-56 text-xs"
                  aria-label={t("adminSettings.execution.weekdays")}
                  data-testid={`settings-execution-weekdays-${String(index)}`}
                >
                  <SelectValue placeholder={t("adminSettings.execution.weekdays")}>
                    {(selected: string[]) =>
                      selected.length === 0
                        ? t("adminSettings.execution.weekdays")
                        : WEEKDAYS.filter((day) => selected.includes(day))
                            .map((day) => t(`adminSettings.execution.weekday.${day}`))
                            .join(t("adminSettings.execution.weekdaySeparator"))
                    }
                  </SelectValue>
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false}>
                  {WEEKDAYS.map((day) => (
                    <SelectItem key={day} value={day}>
                      {t(`adminSettings.execution.weekday.${day}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => { setDraft({ ...draft, windows: draft.windows.filter((_, i) => i !== index) }); }}
              >
                ×
              </Button>
            </div>
          ))}
          {draft.windows.length < 14 && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => { setDraft({
                  ...draft,
                  windows: [
                    ...draft.windows,
                    { days_of_week: ["monday"] as ExecutionWindowDaysOfWeekItem[], start_local_time: "02:00", end_local_time: "06:00" },
                  ],
                }); }
              }
              data-testid="settings-execution-add-window"
            >
              {t("adminSettings.execution.addWindow")}
            </Button>
          )}
        </div>
      </div>
      <div className="flex flex-col gap-2">
        <Label>{t("adminSettings.execution.oscTool")}</Label>
        {/* §18.59: Base UI Select + expand typed parameter inputs when gh-ost is selected
            (five OscOverrides keys declared in contract; empty values are omitted). */}
        <Select
          value={draft.osc.tool}
          onValueChange={(value) => { setDraft({ ...draft, osc: { ...draft.osc, tool: value ?? "none" } }); }}
          items={{ none: "none", "gh-ost": "gh-ost" }}
        >
          <SelectTrigger
            className="h-9 w-40 text-sm"
            aria-label={t("adminSettings.execution.oscTool")}
            data-testid="settings-execution-osc"
          >
            <SelectValue placeholder="none" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">none</SelectItem>
            <SelectItem value="gh-ost">gh-ost</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">{t("adminSettings.execution.oscHint")}</p>
        {draft.osc.tool === "gh-ost" && (
          <div className="mt-1 flex flex-col gap-4 rounded-md border p-3" data-testid="settings-osc-defaults">
            <p className="text-muted-foreground text-xs">{t("adminSettings.execution.oscDefaultsHint")}</p>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionLoad")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "max_load", labelKey: "adminSettings.execution.oscMaxLoad", placeholder: "Threads_running=50", testId: "settings-osc-max-load" },
                  { key: "critical_load", labelKey: "adminSettings.execution.oscCriticalLoad", placeholder: "Threads_running=100", testId: "settings-osc-critical-load" },
                ] as const).map((field) => (
                  <OscTextField
                    key={field.key}
                    label={t(field.labelKey)}
                    placeholder={field.placeholder}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscStringDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
                {([
                  { key: "critical_load_interval_millis", labelKey: "adminSettings.execution.oscCriticalLoadInterval", testId: "settings-osc-critical-load-interval" },
                  { key: "critical_load_hibernate_seconds", labelKey: "adminSettings.execution.oscCriticalLoadHibernate", testId: "settings-osc-critical-load-hibernate" },
                ] as const).map((field) => (
                  <OscNumberField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscNumericDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionCopy")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "chunk_size", labelKey: "adminSettings.execution.oscChunkSize", testId: "settings-osc-chunk-size" },
                  { key: "dml_batch_size", labelKey: "adminSettings.execution.oscDmlBatchSize", testId: "settings-osc-dml-batch-size" },
                  { key: "max_lag", labelKey: "adminSettings.execution.oscMaxLag", testId: "settings-osc-max-lag" },
                  { key: "nice_ratio", labelKey: "adminSettings.execution.oscNiceRatio", testId: "settings-osc-nice-ratio" },
                ] as const).map((field) => (
                  <OscNumberField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscNumericDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionCutover")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "cut_over_lock_timeout_seconds", labelKey: "adminSettings.execution.oscCutOverTimeout", testId: "settings-osc-cut-over-timeout" },
                  { key: "retries", labelKey: "adminSettings.execution.oscRetries", testId: "settings-osc-retries" },
                  { key: "heartbeat_interval_millis", labelKey: "adminSettings.execution.oscHeartbeatInterval", testId: "settings-osc-heartbeat-interval" },
                  { key: "exponential_backoff_max_interval", labelKey: "adminSettings.execution.oscBackoffMaxInterval", testId: "settings-osc-backoff-max-interval" },
                ] as const).map((field) => (
                  <OscNumberField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscNumericDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionSwitches")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "exact_rowcount", labelKey: "adminSettings.execution.oscExactRowcount", testId: "settings-osc-exact-rowcount" },
                  { key: "concurrent_rowcount", labelKey: "adminSettings.execution.oscConcurrentRowcount", testId: "settings-osc-concurrent-rowcount" },
                  { key: "ok_to_drop_table", labelKey: "adminSettings.execution.oscOkToDropTable", testId: "settings-osc-ok-to-drop-table" },
                  { key: "timestamp_old_table", labelKey: "adminSettings.execution.oscTimestampOldTable", testId: "settings-osc-timestamp-old-table" },
                  { key: "cut_over_exponential_backoff", labelKey: "adminSettings.execution.oscCutOverExpBackoff", testId: "settings-osc-cut-over-exp-backoff" },
                  { key: "attempt_instant_ddl", labelKey: "adminSettings.execution.oscAttemptInstantDdl", testId: "settings-osc-attempt-instant-ddl" },
                ] as const).map((field) => (
                  <OscBooleanField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscBooleanDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>
          </div>
        )}
      </div>
    </>
  );
}

function OscTextField({ label, placeholder, value, onChange, testId }: {
  label: string; placeholder: string; value: string | undefined; onChange: (next: string) => void; testId: string;
}) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={testId} className="text-xs">{label}</Label>
      <Input id={testId} value={value ?? ""} placeholder={placeholder} onChange={(event) => { onChange(event.target.value); }} className="h-8 font-mono text-xs" data-testid={testId} />
    </div>
  );
}

function OscNumberField({ label, value, onChange, testId }: {
  label: string; value: number | undefined; onChange: (next: string) => void; testId: string;
}) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={testId} className="text-xs">{label}</Label>
      <NumberInput id={testId} value={value === undefined ? "" : String(value)} onChange={(event) => { onChange(event.target.value); }} className="h-8 text-xs" data-testid={testId} />
    </div>
  );
}

function OscBooleanField({ label, value, onChange, testId }: {
  label: string; value: boolean | undefined; onChange: (next: boolean | undefined) => void; testId: string;
}) {
  const { t } = useTranslation();
  const strVal = value === true ? "true" : value === false ? "false" : "default";
  return (
    <div className="flex flex-col gap-1">
      <Label className="text-xs">{label}</Label>
      <Select
        value={strVal}
        onValueChange={(next) => {
          if (next === "true") onChange(true);
          else if (next === "false") onChange(false);
          else onChange(undefined);
        }}
        items={{
          default: t("adminSettings.execution.oscTriStateDefault"),
          true: t("adminSettings.execution.oscTriStateEnabled"),
          false: t("adminSettings.execution.oscTriStateDisabled"),
        }}
      >
        <SelectTrigger className="h-8 text-xs" aria-label={label} data-testid={testId}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">{t("adminSettings.execution.oscTriStateDefault")}</SelectItem>
          <SelectItem value="true">{t("adminSettings.execution.oscTriStateEnabled")}</SelectItem>
          <SelectItem value="false">{t("adminSettings.execution.oscTriStateDisabled")}</SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}

function BrandingFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("product_name" in draft)) return null;
  return (
    <>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-branding-name">{t("adminSettings.branding.productName")}</Label>
        <Input
          id="settings-branding-name"
          value={draft.product_name}
          onChange={(event) => { setDraft({ ...draft, product_name: event.target.value }); }}
          maxLength={64}
          data-testid="settings-branding-name"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-branding-description">{t("adminSettings.branding.loginDescription")}</Label>
        <Input
          id="settings-branding-description"
          value={draft.login_description}
          onChange={(event) => { setDraft({ ...draft, login_description: event.target.value }); }}
          maxLength={500}
          data-testid="settings-branding-description"
        />
      </div>
      <p className="text-muted-foreground text-xs">{t("adminSettings.branding.assetsNote")}</p>
    </>
  );
}
