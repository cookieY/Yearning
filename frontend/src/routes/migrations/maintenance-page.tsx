import { useTranslation } from "react-i18next";
import { Construction } from "lucide-react";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent } from "@/shared/components/ui/card";

/**
 * Maintenance page for the migration-mode router (migration contract §8:
 * migration mode registers only login, logout, and /admin/migrations; any normal business deep link lands on
 * an explicit maintenance page). This is NOT a 404: the route exists but the whole normal
 * business surface is unavailable while the server runs in migration review
 * mode (503 on every business API).
 */
export default function MigrationMaintenancePage() {
  const { t } = useTranslation();
  return (
    <PageLayout className="min-h-screen items-center justify-center p-6" data-testid="migration-maintenance">
      <div className="flex w-full max-w-xl flex-col gap-5">
        <PageHeader
          icon={Construction}
          title={t("migrationMode.maintenanceTitle")}
          description={t("migrationMode.maintenanceDescription")}
        />
        <Card>
          <CardContent>
            <Button onClick={() => { window.location.href = "/admin/migrations"; }} data-testid="migration-maintenance-goto">
              {t("migrationMode.gotoWorkbench")}
            </Button>
          </CardContent>
        </Card>
      </div>
    </PageLayout>
  );
}
