import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { ShieldBan } from "lucide-react";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { Button } from "@/shared/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
} from "@/shared/components/ui/empty";

/** Forbidden state page (403): presented when the server denies access. */
export default function ForbiddenPage() {
  const { t } = useTranslation();
  return (
    <PageLayout className="min-h-screen items-center justify-center p-6">
      <div className="flex w-full max-w-xl flex-col gap-5">
        <PageHeader icon={ShieldBan} title={t("states.forbiddenTitle")} />
        <Empty>
          <EmptyHeader>
            <EmptyDescription>{t("states.forbiddenDesc")}</EmptyDescription>
          </EmptyHeader>
          <Button nativeButton={false} render={<Link to="/workspace" />}>{t("errors.backHome")}</Button>
        </Empty>
      </div>
    </PageLayout>
  );
}
