import { useTranslation } from "react-i18next";
import { Skeleton } from "@/shared/components/ui/skeleton";

/** Small entry-safe skeleton: error/retry UI remains available without a new chunk. */
export function PageLoadingState({ label }: { label?: string }) {
  const { t } = useTranslation();
  const text = label ?? t("states.loading");
  return (
    <div role="status" aria-label={text} aria-busy="true" data-loading-variant="page" className="flex w-full flex-col gap-5 px-5 py-5">
      <span className="sr-only">{text}</span>
      <div aria-hidden="true" className="flex flex-col gap-5">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-5 w-40" />
        {[0, 1, 2].map((row) => <Skeleton key={row} className="h-4 w-3/4" />)}
      </div>
    </div>
  );
}
