import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useLocation, useParams } from "react-router";
import { Bell, SlidersHorizontal } from "lucide-react";
import type {
  SettingsImpactAssessment,
  SettingsValue,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError, describeErrorText, isBusinessErrorCode } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Card, CardContent } from "@/shared/components/ui/card";
import { formatTimestamp } from "@/shared/lib/timezones";
import { DEFAULT_PRODUCT_NAME } from "@/shared/lib/branding";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { AdminIdentityProvidersContent } from "@/routes/admin/admin-identity-providers-page";
import { AdminNotificationsContent } from "@/routes/admin/admin-notifications-page";
import { ExecutionFields } from "@/routes/admin/admin-settings-execution-fields";
import {
  BrandingFields,
  GeneralFields,
  generalTimezoneOf,
  QueryFields,
  timezoneStructureOk,
} from "@/routes/admin/admin-settings-namespace-fields";
import {
  useAssessSettingsImpact,
  useIdentityProviders,
  useNotificationChannels,
  useNotificationDeliveries,
  useNotificationRules,
  useReplaceSettings,
  useSettingsNamespace,
} from "@/features/admin/use-admin";
import { SettingsNavTabs, type SettingsRouteNamespace } from "@/routes/admin/settings-tabs";

/**
 * Settings family (routes /admin/settings/{general,query,execution,branding,notifications}; §2
 * splits legacy /manager/setting into typed namespaces — S006: no RPC,
 * automated-task, query-limit or per-order-budget fields ever return). The F9
 * ai-budget page keeps its dedicated two-step confirmation flow; this
 * unified settings workspace serves the five settings tabs so the PageHeader
 * and SettingsNavTabs stay stationary while TransitionPanel animates the body.
 */

type SettingsPageNamespace = Exclude<SettingsRouteNamespace, "notifications">;

function normalizeSettingsForSubmit(
  namespace: SettingsPageNamespace,
  draft: SettingsValue,
): SettingsValue {
  if (namespace === "branding" && "product_name" in draft) {
    const normalizedName = draft.product_name.trim() || DEFAULT_PRODUCT_NAME;
    return {
      ...draft,
      product_name: normalizedName,
      login_description: draft.login_description.trim(),
    };
  }
  return draft;
}
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
  const [assessedSignature, setAssessedSignature] = useState<string | null>(null);
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
    const payload = normalizeSettingsForSubmit(namespace, draft);
    replaceMutation.mutate(
      {
        namespace,
        version: settingsQuery.data === undefined ? 1 : settingsQuery.data.version,
        settings: payload,
        impactToken,
      },
      {
        onSuccess: () => {
          setAssessedSignature(null);
          setDraft(payload);
          setSavedNote(true);
          appToast.success(t("common.save"));
        },
        onError: (error) => {
          if (isBusinessErrorCode(error, "PRECONDITION_REQUIRED")) {
            setAssessedSignature(null);
            if (impactToken === null) {
              assessMutation.mutate(
                { namespace, settings: payload },
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
                  const payload = normalizeSettingsForSubmit(namespace, draft);
                  assessMutation.mutate(
                    { namespace, settings: payload },
                    {
                      onSuccess: (next) => {
                        setAssessment(next);
                        setAssessedSignature(JSON.stringify(payload));
                      },
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
                onClick={() => {
                  const payload = normalizeSettingsForSubmit(namespace, draft);
                  const canUseAssessedToken =
                    assessment !== null &&
                    highImpact &&
                    assessedSignature === JSON.stringify(payload);
                  save(canUseAssessedToken ? assessment.impact_token : null);
                }}
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
