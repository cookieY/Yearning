import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";

/**
 * Settings area navigation (§18.47): the five namespace tabs shared by the
 * generic settings pages AND the standalone notifications page, so the tab
 * bar no longer disappears when landing on /admin/settings/notifications.
 * AI Budget moved to the Review Engine group (owner 2026-10-31) and is no longer a settings tab.
 */
export type SettingsRouteNamespace =
  | "general"
  | "query"
  | "execution"
  | "branding"
  | "notifications";

export function SettingsNavTabs({
  current,
  onSelect,
}: {
  current: SettingsRouteNamespace;
  onSelect?: (next: SettingsRouteNamespace) => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const selectTab = (next: SettingsRouteNamespace): void => {
    onSelect?.(next);
    if (next !== current) {
      void navigate(`/admin/settings/${next}`);
    }
  };
  return (
    <Tabs
      value={current}
      onValueChange={(next) => {
        if (
          next === "general" ||
          next === "query" ||
          next === "execution" ||
          next === "branding" ||
          next === "notifications"
        ) {
          selectTab(next as SettingsRouteNamespace);
        }
      }}
      className="w-full max-w-full"
    >
      <TabsList className="flex w-full justify-start overflow-x-auto overflow-y-hidden">
        <TabsTrigger value="general" data-testid={"settings-tab-general"} onClick={() => { selectTab("general"); }}>
          {t("adminSettings.tab.general")}
        </TabsTrigger>
        <TabsTrigger value="query" data-testid={"settings-tab-query"} onClick={() => { selectTab("query"); }}>
          {t("adminSettings.tab.query")}
        </TabsTrigger>
        <TabsTrigger value="execution" data-testid={"settings-tab-execution"} onClick={() => { selectTab("execution"); }}>
          {t("adminSettings.tab.execution")}
        </TabsTrigger>
        <TabsTrigger value="branding" data-testid={"settings-tab-branding"} onClick={() => { selectTab("branding"); }}>
          {t("adminSettings.tab.branding")}
        </TabsTrigger>
        <TabsTrigger value="notifications" data-testid={"settings-tab-notifications"} onClick={() => { selectTab("notifications"); }}>
          {t("adminSettings.tab.notifications")}
        </TabsTrigger>
      </TabsList>
    </Tabs>
  );
}
