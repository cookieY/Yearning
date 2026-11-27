import { lazy, Suspense } from "react";
import { useTranslation } from "react-i18next";
import type { SqlCompletionCatalog } from "./sql-completions";
import type { SqlEditorPanelProps } from "./sql-editor-panel";
import { Skeleton } from "@/shared/components/ui/skeleton";
import { cn } from "@/shared/lib/utils";

export type { SqlCompletionCatalog, SqlEditorPanelProps };

const LazySqlEditor = lazy(() =>
  import("@/features/review/sql-editor-panel").then((module) => ({
    default: module.SqlEditorPanel,
  })),
);

const EDITOR_SKELETON_WIDTHS = ["w-3/5", "w-4/5", "w-2/3", "w-3/4", "w-1/2"] as const;

/** Local height-matched placeholder while the lazy Monaco SQL editor chunk loads. */
export function SqlEditorSkeleton({
  className,
  label,
}: {
  className?: string;
  label?: string;
}) {
  const { t } = useTranslation();
  const text = label ?? t("states.loading");
  return (
    <div
      role="status"
      aria-busy="true"
      aria-label={text}
      data-slot="sql-editor-skeleton"
      className={cn(
        "min-h-[240px] h-[46vh] border-input bg-card flex flex-col gap-3 overflow-hidden rounded-md border p-4",
        className,
      )}
    >
      <span className="sr-only">{text}</span>
      <div aria-hidden="true" className="flex flex-col gap-3">
        {EDITOR_SKELETON_WIDTHS.map((widthClass, index) => (
          <div key={index} className="flex items-center gap-3">
            <Skeleton className="h-3.5 w-6 shrink-0" />
            <Skeleton className={cn("h-3.5", widthClass)} />
          </div>
        ))}
      </div>
    </div>
  );
}

/** Lazy wrapper around SqlEditorPanel so Monaco never blocks route-level mounting. */
export function LazySqlEditorPanel(props: SqlEditorPanelProps) {
  return (
    <Suspense fallback={<SqlEditorSkeleton className={props.className} />}>
      <LazySqlEditor {...props} />
    </Suspense>
  );
}
