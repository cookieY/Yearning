import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { Eye } from "lucide-react";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import {
  setDevViewAs,
  useDevViewAs,
  type DevViewAsRole,
} from "@/shared/dev/dev-view-as";

const PRESETS: Array<{ id: DevViewAsRole; labelKey: string }> = [
  { id: "none", labelKey: "shell.devViewAs.own" },
  { id: "approver", labelKey: "shell.devViewAs.approver" },
  { id: "executor", labelKey: "shell.devViewAs.executor" },
  { id: "user", labelKey: "shell.devViewAs.user" },
];

/**
 * Dev-only role downgrade toolbar (D18).
 * Dynamically imported behind `import.meta.env.DEV` in FullLayout so it is
 * completely eliminated from production bundles.
 */
export function DevViewAsToolbar() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const current = useDevViewAs((state) => state.role);

  const handleChange = (next: DevViewAsRole, label: string): void => {
    const applied = setDevViewAs(next);
    void queryClient.invalidateQueries();
    appToast.info(t("shell.devViewAs.switched", { role: label }));
    if (applied !== next) {
      appToast.warning(t("shell.devViewAs.downgradeOnly"));
    }
  };

  return (
    <div
      role="region"
      aria-label={t("shell.devViewAs.label")}
      data-testid="dev-view-as-toolbar"
      className="bg-[var(--risk-warning)]/10 border-[var(--risk-warning)]/30 text-foreground mx-auto mt-1 flex w-[calc(100%-2rem)] max-w-[1536px] flex-wrap items-center justify-between gap-2 rounded-xl border px-3 py-1.5 text-xs"
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-[var(--risk-warning)]/15 px-2 py-0.5 font-medium text-[var(--risk-warning)]">
          <Eye className="size-3" aria-hidden="true" />
          {t("shell.devViewAs.badge")}
        </span>
        <span className="text-muted-foreground truncate">{t("shell.devViewAs.notice")}</span>
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-muted-foreground hidden sm:inline">{t("shell.devViewAs.label")}:</span>
        <div className="bg-card border-border inline-flex items-center rounded-lg border p-0.5">
          {PRESETS.map((preset) => {
            const active = current === preset.id;
            const label = t(preset.labelKey);
            return (
              <button
                key={preset.id}
                type="button"
                onClick={() => {
                  handleChange(preset.id, label);
                }}
                aria-pressed={active}
                data-testid={`dev-view-as-${preset.id}`}
                className={
                  active
                    ? "bg-primary text-primary-foreground rounded-md px-2 py-0.5 font-medium transition-colors"
                    : "text-muted-foreground hover:text-foreground rounded-md px-2 py-0.5 transition-colors"
                }
              >
                {label}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
