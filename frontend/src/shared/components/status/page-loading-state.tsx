import { useTranslation } from "react-i18next";
import { Skeleton } from "@/shared/components/ui/skeleton";

/** Small entry-safe skeleton: error/retry UI remains available without a new chunk. */
export function PageLoadingState({ label }: { label?: string }) {
  const { t } = useTranslation();
  const text = label ?? t("states.loading");
  return (
    <div
      role="status"
      aria-label={text}
      aria-busy="true"
      data-loading-variant="page"
      className="flex w-full min-w-0 flex-col gap-5"
    >
      <span className="sr-only">{text}</span>
      <div aria-hidden="true" className="flex items-center gap-3">
        <Skeleton className="size-9 shrink-0 rounded-lg" />
        <div className="flex min-w-0 flex-1 flex-col gap-2">
          <Skeleton className="h-7 w-48 max-w-[60%]" />
          <Skeleton className="h-4 w-72 max-w-[85%]" />
        </div>
      </div>
      <div aria-hidden="true" className="rounded-xl border border-border bg-card">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border/60 p-5">
          <Skeleton className="h-9 w-64 max-w-full" />
          <Skeleton className="h-9 w-28 shrink-0" />
        </div>
        <div className="flex flex-col divide-y divide-border/60 px-5">
          {[0, 1, 2, 3, 4].map((row) => (
            <div key={row} className="flex items-center gap-4 py-4">
              <Skeleton className="h-4 flex-[2]" />
              <Skeleton className="h-4 flex-[3]" />
              <Skeleton className="hidden h-4 flex-[2] sm:block" />
              <Skeleton className="h-4 w-20 shrink-0" />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
