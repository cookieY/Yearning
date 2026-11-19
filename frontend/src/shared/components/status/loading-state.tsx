import { PageLoadingState } from "./page-loading-state";
import { useTranslation } from "react-i18next";
import { Spinner } from "@/shared/components/ui/spinner";
import { Skeleton } from "@/shared/components/ui/skeleton";
import { cn } from "@/shared/lib/utils";

export function LoadingState({ label, variant = "panel", columns = 4 }: {
  label?: string;
  variant?: "page" | "panel" | "table" | "inline";
  columns?: number;
}) {
  const { t } = useTranslation();
  const text = label ?? t("states.loading");
  if (variant === "page") return <PageLoadingState label={label} />;
  if (variant === "inline") {
    return <span role="status" aria-label={text} className="inline-flex items-center gap-2 text-[13px] text-muted-foreground"><Spinner aria-hidden />{text}</span>;
  }
  return (
    <div role="status" aria-label={text} aria-busy="true" data-loading-variant={variant} className={cn("flex w-full flex-col gap-5 py-5", variant !== "table" && "px-5")}>
      <span className="sr-only">{text}</span>
      <div aria-hidden="true" className="flex flex-col gap-5">
        {variant !== "table" && <Skeleton className="h-5 w-40" />}
        {Array.from({ length: variant === "table" ? 5 : 3 }, (_, row) => (
          <div key={row} className={cn("flex items-center gap-5", variant === "table" && "border-b border-border/60 px-5 pb-4")}>
            {Array.from({ length: variant === "table" ? Math.min(12, Math.max(1, columns)) : 1 }, (_, column) => (
              <Skeleton key={column} className={cn("h-4 min-w-0 flex-1", variant !== "table" && (row === 1 ? "max-w-[75%]" : "max-w-[90%]"))} />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
