import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import { ExternalLink, RotateCcw, Sparkles } from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import {
  clearMockSuperSession,
  mockSuperSession,
  setMockSuperSession,
} from "@/shared/dev/mock-super";
import {
  readStoredIdentity,
  setMockIdentity,
  type MockIdentity,
} from "@/shared/mock/identity-store";

type DemoPersona = "all" | MockIdentity;

function resolveInitialPersona(): DemoPersona {
  if (mockSuperSession()) return "all";
  return readStoredIdentity();
}

/**
 * Lightweight top bar shown exclusively in the public static interactive demo
 * build (`VITE_DEMO_MODE=true`). Lets visitors understand they are in a safe
 * browser-only sandbox, switch between full-access and separated governance
 * personas, or reset the local mock state with one click.
 */
export function DemoBanner() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [persona, setPersona] = useState<DemoPersona>(resolveInitialPersona);

  const roles: Array<{ id: DemoPersona; label: string }> = [
    { id: "all", label: t("demo.roleAll") },
    { id: "henry", label: t("demo.roleSubmitter") },
    { id: "reviewer-a", label: t("demo.roleReviewer") },
    { id: "executor-a", label: t("demo.roleExecutor") },
  ];

  const handleSelectPersona = (next: DemoPersona, label: string) => {
    if (next === "all") {
      setMockIdentity("henry");
      setMockSuperSession(true);
    } else {
      setMockSuperSession(false);
      setMockIdentity(next);
    }
    setPersona(next);
    void queryClient.invalidateQueries();
    appToast.success(t("demo.roleSwitched", { role: label }));
  };

  const handleResetDemo = () => {
    clearMockSuperSession();
    setMockIdentity("henry");
    window.location.reload();
  };

  return (
    <div
      role="status"
      data-testid="demo-mode-banner"
      className="bg-primary/10 border-primary/20 text-foreground mx-auto mt-1 flex w-[calc(100%-2rem)] max-w-[1536px] flex-wrap items-center justify-between gap-2 rounded-xl border px-3 py-1.5 text-xs"
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="bg-primary text-primary-foreground inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 font-medium">
          <Sparkles className="size-3" aria-hidden="true" />
          {t("demo.badge")}
        </span>
        <span className="text-muted-foreground truncate">{t("demo.notice")}</span>
      </div>

      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-muted-foreground hidden sm:inline">{t("demo.roleLabel")}:</span>
        <div className="bg-card border-border inline-flex items-center rounded-lg border p-0.5">
          {roles.map((role) => {
            const active = persona === role.id;
            return (
              <button
                key={role.id}
                type="button"
                onClick={() => {
                  handleSelectPersona(role.id, role.label);
                }}
                aria-pressed={active}
                className={
                  active
                    ? "bg-primary text-primary-foreground rounded-md px-2 py-0.5 font-medium transition-colors"
                    : "text-muted-foreground hover:text-foreground rounded-md px-2 py-0.5 transition-colors"
                }
              >
                {role.label}
              </button>
            );
          })}
        </div>

        <Button
          type="button"
          variant="outline"
          size="xs"
          onClick={handleResetDemo}
          className="h-6 gap-1 px-2 text-xs"
        >
          <RotateCcw className="size-3" aria-hidden="true" />
          {t("demo.reset")}
        </Button>

        <a
          href={__DOCS_URL__}
          target="_blank"
          rel="noopener noreferrer"
          className="text-primary hover:underline inline-flex items-center gap-1 px-1 font-medium"
        >
          {t("demo.docs")}
          <ExternalLink className="size-3" aria-hidden="true" />
        </a>
      </div>
    </div>
  );
}
