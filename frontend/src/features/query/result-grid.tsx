import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useVirtualizer } from "@tanstack/react-virtual";
import { EyeOff } from "lucide-react";
import { Badge } from "@/shared/components/ui/badge";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/shared/components/ui/tooltip";
import type { QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { stringifyCell } from "./cell-format";

/**
 * Virtualized result grid (UI spec §7.5): rows arrive through the server
 * cursor and render in a fixed-height virtualized window — never all at
 * once. There is no total-row display anywhere (Q005): the footer states
 * the rows ALREADY READ and offers cursor continuation, honestly reporting
 * interruption or exhaustion instead of a fabricated total.
 *
 * §18.50 (owner ruling: legacy parity, pure frontend paging): executions
 * drain their server cursor pages in the background until exhausted, so
 * the grid pages ENTIRELY over the loaded rows — the user never sees a
 * fetch step. Columns are drag-resizable; oversized strings ellipsize and
 * reveal their full content through a tooltip.
 */

export interface ResultTabState {
  executionId: string;
  /** Display label for multi-result tabs (e.g. "demo.accounts", "demo.accounts 2"). */
  title?: string;
  columns: QueryResultPage["columns"];
  rows: unknown[][];
  nextCursor: string | null;
  elapsedMs: number | null;
  exhausted: boolean;
  /** Live metadata masked flags by column name (from the columns endpoint). */
  maskedByName: Map<string, boolean>;
}

interface ResultGridProps {
  tab: ResultTabState;
  continuationError: string | null;
}

const LONG_CELL_THRESHOLD = 64;
const DEFAULT_COL_WIDTH = 160;
const MIN_COL_WIDTH = 64;
const MAX_COL_WIDTH = 960;
const DEFAULT_PAGE_SIZE = 100;

export function ResultGrid({ tab, continuationError }: ResultGridProps) {
  const { t } = useTranslation();
  const parentRef = useRef<HTMLDivElement>(null);
  const [pageNo, setPageNo] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_PAGE_SIZE);
  const [widths, setWidths] = useState<Record<string, number>>({});
  const dragRef = useRef<{ column: string; startX: number; startWidth: number } | null>(null);

  // A grid instance sticks to one execution tab; if the tab prop ever
  // swaps, paging and the resized widths restart honestly.
  const tabKeyRef = useRef(tab.executionId);
  if (tabKeyRef.current !== tab.executionId) {
    tabKeyRef.current = tab.executionId;
    setPageNo(1);
    setWidths({});
  }

  const startRow = (pageNo - 1) * pageSize;
  const visibleRows = tab.rows.slice(startRow, startRow + pageSize);
  const virtualizer = useVirtualizer({
    count: visibleRows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 32,
    overscan: 10,
  });

  const loadedPages = Math.max(1, Math.ceil(tab.rows.length / pageSize));
  const canNext = pageNo < loadedPages;

  const virtualItems = virtualizer.getVirtualItems();
  const firstItem = virtualItems[0];
  const lastItem = virtualItems[virtualItems.length - 1];
  const padBottom =
    lastItem === undefined ? 0 : Math.max(0, virtualizer.getTotalSize() - lastItem.end);

  const widthOf = (name: string): number => widths[name] ?? DEFAULT_COL_WIDTH;
  const tableWidth = tab.columns.reduce((sum, column) => sum + widthOf(column.name), 0);

  /* v8 ignore start: real pointer geometry only — jsdom cannot drive the
     drag pipeline; the resize behavior is gated by the e2e drag assertions
     (query-results.spec) and real-browser verification. */
  const onResizeStart = (event: React.PointerEvent<HTMLSpanElement>, column: string): void => {
    dragRef.current = { column, startX: event.clientX, startWidth: widthOf(column) };
    event.currentTarget.setPointerCapture(event.pointerId);
  };
  const onResizeMove = (event: React.PointerEvent<HTMLSpanElement>): void => {
    const drag = dragRef.current;
    if (drag === null) return;
    const next = Math.min(MAX_COL_WIDTH, Math.max(MIN_COL_WIDTH, drag.startWidth + (event.clientX - drag.startX)));
    setWidths((current) => ({ ...current, [drag.column]: next }));
  };
  const onResizeEnd = (event: React.PointerEvent<HTMLSpanElement>): void => {
    dragRef.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
  };
  /* v8 ignore stop */

  return (
    <TooltipProvider>
      <div className="flex h-full min-h-0 flex-col gap-2" data-testid={`query-result-${tab.executionId}`}>
        <div className="text-muted-foreground flex flex-wrap items-center gap-2 text-xs">
          <span data-testid={`query-result-elapsed-${tab.executionId}`}>
            {tab.elapsedMs === null ? "" : t("query.result.elapsed", { ms: tab.elapsedMs })}
          </span>
          <span data-testid={`query-result-loaded-${tab.executionId}`}>
            {t("query.result.rowsLoaded", { count: tab.rows.length })}
          </span>
          {[...tab.maskedByName.entries()].filter(([, masked]) => masked).length > 0 && (
            <Badge variant="secondary" className="gap-0.5">
              <EyeOff className="size-2.5" />
              {t("query.result.maskedColumns")}
            </Badge>
          )}
        </div>
        {/* Keyboard-scrollable region (WCAG 2.1.1/2.1.3, axe
            scrollable-region-focusable): the virtualized grid is the page's
            primary scroll surface, so it must be reachable by Tab. Owner
            feedback 2026-11-04 (scrollbars must use ScrollArea) + the 2026-11-03
            ScrollArea ruling: the shared ScrollArea paints both scrollbars;
            the virtualizer keeps its ref on the real scroll element — the
            ScrollArea viewport. */}
        <ScrollArea
          className="min-h-0 flex-1 rounded-md border"
          viewportClassName="focus-visible:ring-ring/50 focus-visible:outline-none focus-visible:ring-1"
          viewportProps={{
            ref: parentRef,
            role: "region",
            tabIndex: 0,
            "aria-label": t("query.result.gridLabel"),
          }}
        >
          <table
            className="w-full text-xs tabular-nums"
            style={{ tableLayout: "fixed", minWidth: `${String(tableWidth)}px` }}
            data-testid={`query-result-table-${tab.executionId}`}
          >
            <thead className="bg-muted sticky top-0 z-10 text-muted-foreground shadow-[0_1px_0_0_var(--border)]">
              <tr>
                {tab.columns.map((column) => (
                  <th
                    key={column.name}
                    className="bg-muted sticky top-0 z-10 relative border-b border-r px-3 py-1.5 text-left font-medium tracking-wide"
                    style={{ width: `${String(widthOf(column.name))}px` }}
                    title={column.type}
                  >
                    <span className="inline-flex items-center gap-1 truncate">
                      {column.name}
                      {tab.maskedByName.get(column.name) === true && (
                        <EyeOff className="size-2.5 text-muted-foreground" aria-label={t("query.result.maskedColumns")} />
                      )}
                    </span>
                    <span
                      role="separator"
                      aria-orientation="vertical"
                      aria-label={t("query.result.resizeColumn", { name: column.name })}
                      onPointerDown={(event) => { onResizeStart(event, column.name); }}
                      onPointerMove={onResizeMove}
                      onPointerUp={onResizeEnd}
                      onPointerCancel={onResizeEnd}
                      className="hover:bg-primary/40 absolute inset-y-0 right-0 w-1.5 cursor-col-resize touch-none"
                      data-testid={`query-col-resize-${column.name}`}
                    />
                  </th>
                ))}
              </tr>
            </thead>
            {/* v8 ignore start: the virtualizer paints zero rows under jsdom
                (no layout) — spacer/row/cell branches below are gated by the
                e2e suite (query-results.spec), never by unit coverage. */}
            <tbody>
              {/*
                Spacer rows keep the real scroll geometry (the virtualizer
                renders only the window; without spacers the sticky header
                overlaps the first row and the scrollbar collapses — the
                §18.50 layout bug).
              */}
              {firstItem !== undefined && firstItem.start > 0 && (
                <tr aria-hidden style={{ height: `${String(firstItem.start)}px` }} />
              )}
              {virtualItems.map((virtualRow) => {
                const row = visibleRows[virtualRow.index];
                if (row === undefined) return null;
                const globalIndex = startRow + virtualRow.index;
                return (
                  <tr
                    key={virtualRow.key}
                    className="border-b hover:bg-accent/40"
                    style={{ height: `${String(virtualRow.size)}px` }}
                    data-row={globalIndex}
                  >
                    {row.map((cell, cellIndex) => {
                      const columnName = tab.columns[cellIndex]?.name ?? String(cellIndex);
                      const text = cell === null || cell === undefined ? "" : stringifyCell(cell);
                      return (
                        <td key={cellIndex} className="overflow-hidden border-r px-3">
                          {text.length > LONG_CELL_THRESHOLD ? (
                            <Tooltip>
                              <TooltipTrigger className="block max-w-full truncate text-left" data-testid={`query-cell-${columnName}-${String(globalIndex)}`}>
                                {text}
                              </TooltipTrigger>
                              <TooltipContent className="max-w-lg break-all" data-testid={`query-cell-tip-${columnName}-${String(globalIndex)}`}>
                                {text}
                              </TooltipContent>
                            </Tooltip>
                          ) : (
                            <span className="block truncate">{text}</span>
                          )}
                        </td>
                      );
                    })}
                  </tr>
                );
              })}
              {padBottom > 0 && <tr aria-hidden style={{ height: `${String(padBottom)}px` }} />}
            </tbody>
            {/* v8 ignore stop */}
          </table>
        </ScrollArea>
        {/* Clear the full 120x120 fixed bottom-right Assistant FAB hitbox when the query workspace fills the viewport height. */}
        <div className="flex flex-wrap items-center gap-2 md:pr-32">
          {/* Pure frontend paging (§18.50 follow-up): background draining
              grows the loaded rows; the pager never fetches. */}
          {tab.exhausted && (
            <span className="text-muted-foreground text-xs" data-testid={`query-result-exhausted-${tab.executionId}`}>
              {t("query.result.exhausted")}
            </span>
          )}
          {continuationError !== null && (
            <span className="text-destructive text-xs" data-testid={`query-result-continuation-error-${tab.executionId}`}>
              {continuationError}
            </span>
          )}
          <div className="ml-auto">
            <TablePagination
              page={pageNo}
              hasMore={canNext}
              isFirst={pageNo <= 1}
              onPrev={() => { setPageNo((current) => Math.max(1, current - 1)); }}
              onNext={() => { setPageNo((current) => current + 1); }}
              onJumpPage={(target) => { setPageNo(target); }}
              pageSize={pageSize}
              onPageSizeChange={(size) => { setPageSize(size); setPageNo(1); }}
              rowsOnPage={visibleRows.length}
              testIdPrefix="query-result"
            />
          </div>
        </div>
      </div>
    </TooltipProvider>
  );
}

/** Column-name → masked map from the live metadata endpoint response. */
export function maskedMapFrom(columns: { column_name: string; masked: boolean }[] | undefined): Map<string, boolean> {
  return new Map((columns ?? []).map((column) => [column.column_name, column.masked]));
}
