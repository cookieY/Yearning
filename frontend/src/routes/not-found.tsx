import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { FileQuestion } from "lucide-react";
import { EmptyState } from "@/shared/components/status/empty-state";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { Button } from "@/shared/components/ui/button";

export default function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <PageLayout className="min-h-screen items-center justify-center p-6">
      <div className="flex w-full max-w-xl flex-col gap-5">
        <PageHeader icon={FileQuestion} title={t("errors.notFound")} />
        <EmptyState
          title={t("errors.notFoundDesc")}
          action={<Button nativeButton={false} render={<Link to="/" />}>{t("errors.backHome")}</Button>}
        />
      </div>
    </PageLayout>
  );
}
