import type { ComponentProps, ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { CardHeader } from "@/shared/components/ui/card";
import { Spinner } from "@/shared/components/ui/spinner";
import { cn } from "@/shared/lib/utils";

/** Page rhythm and title hierarchy follow the workspace baseline (UI spec §7.8). */
export function PageLayout({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="page-layout" className={cn("flex min-w-0 flex-col gap-5", className)} {...props} />;
}

export function PageHeader({
  title, description, icon: Icon, actions, className, ...props
}: Omit<ComponentProps<"header">, "title"> & {
  title: ReactNode;
  description?: ReactNode;
  icon?: LucideIcon;
  actions?: ReactNode;
}) {
  return (
    <header data-slot="page-header" className={cn("flex min-w-0 flex-col gap-4 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between", className)} {...props}>
      <div className="flex min-w-0 flex-1 items-start gap-3 sm:items-center">
        {Icon && <div data-slot="page-icon" className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary"><Icon className="size-5" aria-hidden /></div>}
        <div className="flex min-w-0 flex-col gap-1.5">
          <h1 className="text-2xl leading-snug font-semibold tracking-tight [overflow-wrap:anywhere]">{title}</h1>
          {description && <p className="text-[13px] leading-relaxed text-muted-foreground">{description}</p>}
        </div>
      </div>
      {actions && <div data-slot="page-actions" className="flex max-w-full flex-wrap items-center gap-2 self-start sm:self-auto">{actions}</div>}
    </header>
  );
}

/** A Card header dedicated to data controls; search remains right aligned.
 * `fill` (owner feedback 2026-11-05) lets the children area stretch across
 * the header — for the ECS-style global search bar that replaces every other
 * filter control. The right slot then drops its `ml-auto` (the growing
 * children area pushes it to the edge instead). */
export function ListToolbar({ children, search, actions, busy = false, className, fill = false, ...props }: ComponentProps<"div"> & {
  search?: ReactNode;
  actions?: ReactNode;
  busy?: boolean;
  fill?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <CardHeader data-slot="list-toolbar" className={cn("flex flex-row flex-wrap items-center gap-3 rounded-none border-b", className)} {...props}>
      {children && <div className={cn("flex min-w-0 flex-wrap items-center gap-3", fill && "flex-1")}>{children}</div>}
      <div className={cn(
        "flex w-full min-w-0 flex-wrap items-center justify-end gap-2 sm:w-auto",
        fill ? "sm:flex-none" : "ml-auto sm:flex-1",
      )}>
        {busy && <span role="status" aria-label={t("states.refreshing")} className="flex shrink-0 items-center gap-2 text-muted-foreground"><Spinner aria-hidden /><span className="sr-only">{t("states.refreshing")}</span></span>}
        {search}
        {actions}
      </div>
    </CardHeader>
  );
}
