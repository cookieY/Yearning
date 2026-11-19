import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { ChevronLeft, ChevronRight } from "lucide-react";

import { Button } from "@/shared/components/ui/button";
import { cn } from "@/shared/lib/utils";

const SIZE_OPTIONS = [10, 20, 50, 100];

/**
 * Backend-paging footer for list tables (owner ruling 2026-09-02; restyled
 * per the pagination-01 floating-pill block, owner ruling 2026-10-24): a
 * pill bar of circular prev/next chevrons around numbered page pills, with
 * the active page carried by a spring-slid primary pill. The backend pages
 * by cursor (PageInfo: next_cursor/has_more) so there is no total — the bar
 * shows the pages already walked plus at most one more (Next); jumping back
 * truncates the cursor stack (onJumpPage/goToPage). The busy dot keeps its
 * immediate-acknowledgement contract (same commit as the click) and the
 * rows-per-page picker stays on the right.
 */
export function TablePagination({
  page,
  hasMore,
  isFirst,
  onPrev,
  onNext,
  onJumpPage,
  pageSize,
  onPageSizeChange,
  testIdPrefix,
  busy = false,
  rowsOnPage,
}: {
  page: number;
  hasMore: boolean;
  isFirst: boolean;
  onPrev: () => void;
  onNext: () => void;
  /** Jump back to an already-visited page; forward is always onNext. */
  onJumpPage?: (page: number) => void;
  pageSize: number;
  onPageSizeChange: (size: number) => void;
  testIdPrefix: string;
  /**
   * True while the next/previous page is being fetched under TanStack Query's
   * `keepPreviousData`: the table keeps the old rows (no flash of an empty
   * page), so the only immediate acknowledgement of the click is here — the
   * control disables and the busy dot appears on the same commit as the click.
   */
  busy?: boolean;
  /**
   * Rows rendered on the current page. The speculative page+1 pill (and
   * Next) only make sense when the current page actually has rows: a page
   * that came back empty has no successor worth offering, no matter what
   * the page meta said (owner bug report 2026-10-24: empty table kept a
   * two-pill bar and an endless walk forward). When an already-walked page
   * turns out empty the footer walks itself back to page 1 instead of
   * anchoring its pills on a page that no longer exists.
   */
  rowsOnPage?: number;
}) {
  const { t } = useTranslation();

  const hasMoreRows = rowsOnPage === undefined ? hasMore : hasMore && rowsOnPage > 0;

  // Filters shrinking the result, an unresolvable cursor or a mid-session
  // identity switch can leave the walked page empty; go back to page 1
  // rather than offering pills for pages that no longer exist.
  useEffect(() => {
    if (rowsOnPage === 0 && page > 1 && !busy && onJumpPage !== undefined) {
      onJumpPage(1);
    }
  }, [rowsOnPage, page, busy, onJumpPage]);

  // Cursor paging knows the walked pages plus at most one more; the window
  // anchors to that edge so no trailing ellipsis is ever needed.
  const last = hasMoreRows ? page + 1 : page;
  const start = Math.max(1, Math.min(page - 2, last - 4));
  const end = Math.min(last, start + 4);
  const numbers: number[] = [];
  for (let pageNo = start; pageNo <= end; pageNo += 1) numbers.push(pageNo);
  const leadOne = start > 1;
  const leadEllipsis = start > 2;

  const goTo = (target: number) => {
    if (target === page) return;
    if (target === page + 1) {
      onNext();
      return;
    }
    if (target < page) onJumpPage?.(target);
  };
  const canJump = (target: number) =>
    target === page ||
    target === page + 1 ||
    (target < page && onJumpPage !== undefined);

  const pill = (pageNo: number) => {
    const active = pageNo === page;
    return (
      <span key={pageNo} className="relative">
        <button
          type="button"
          aria-label={t("table.pagination.page", { page: pageNo })}
          aria-current={active ? "page" : undefined}
          disabled={!canJump(pageNo) || busy}
          onClick={() => { goTo(pageNo); }}
          className={cn(
            "relative z-10 grid size-7 place-items-center rounded-md text-xs font-semibold tabular-nums transition-colors",
            active
              ? "bg-primary text-primary-foreground"
              : "text-muted-foreground hover:bg-muted hover:text-foreground disabled:pointer-events-none disabled:opacity-40",
          )}
        >
          {pageNo}
        </button>
      </span>
    );
  };

  return (
    <div
      className="flex flex-row flex-wrap items-center justify-between gap-3 px-4 pt-3 pb-1"
      data-testid={`${testIdPrefix}-pagination`}
      aria-busy={busy || undefined}
    >
      <nav
        aria-label={t("table.pagination.page", { page })}
        className="bg-card inline-flex items-center gap-1 rounded-lg border p-1"
      >
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="rounded-md"
          aria-label={t("table.pagination.prev")}
          disabled={isFirst || busy}
          onClick={onPrev}
        >
          <ChevronLeft aria-hidden />
        </Button>
        <span className="relative mx-0.5 flex items-center gap-0.5">
          {leadOne ? pill(1) : null}
          {leadEllipsis ? (
            <span className="grid size-7 place-items-center text-xs text-muted-foreground" aria-hidden>
              …
            </span>
          ) : null}
          {numbers.map((pageNo) => pill(pageNo))}
        </span>
        <span className="relative">
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="rounded-md"
            aria-label={t("table.pagination.next")}
            disabled={!hasMoreRows || busy}
            data-testid={`${testIdPrefix}-next`}
            onClick={onNext}
          >
            <ChevronRight aria-hidden />
          </Button>
          {busy ? (
            <span
              className="absolute -top-0.5 -right-0.5 size-1.5 animate-pulse rounded-full bg-primary"
              aria-hidden="true"
              data-testid={`${testIdPrefix}-busy`}
            />
          ) : null}
        </span>
      </nav>
      <span role="status" className="sr-only">
        {busy ? t("table.pagination.loading") : ""}
      </span>
      {/* Numbers show the position; the live region keeps the page count
          announced for screen readers (and pins the legacy testid). */}
      <p
        className="sr-only"
        data-testid={`${testIdPrefix}-page`}
        aria-live="polite"
      >
        {t("table.pagination.page", { page })}
      </p>
      {/* Rows-per-page shares the same flat segmented language as the nav bar. */}
      <div
        role="group"
        aria-label={t("table.pagination.rowsPerPage")}
        className="bg-card text-muted-foreground inline-flex items-center rounded-lg border p-1"
      >
        <span className="pl-2 pr-1 text-xs">{t("table.pagination.rowsPerPage")}</span>
        <span className="relative mx-0.5 flex items-center gap-0.5">
          {SIZE_OPTIONS.map((size) => {
            const active = size === pageSize;
            return (
              <span key={size} className="relative">
                <button
                  type="button"
                  aria-pressed={active}
                  data-testid={`${testIdPrefix}-page-size-${size}`}
                  onClick={() => { onPageSizeChange(size); }}
                  className={cn(
                    "relative z-10 grid size-7 place-items-center rounded-md text-xs font-semibold tabular-nums transition-colors",
                    active
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:bg-muted hover:text-foreground",
                  )}
                >
                  {size}
                </button>
              </span>
            );
          })}
        </span>
      </div>
    </div>
  );
}
