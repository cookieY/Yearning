import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ChevronsDownUp, ChevronsUpDown } from "lucide-react";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import { highlightSqlLine } from "@/shared/components/ui/code-block";
import { ScrollArea } from "@/shared/components/ui/scroll-area";

/**
 * Frozen-SQL file viewer — shadcn-dashboard collapsible-08 "Expandable
 * Code" block adapted per owner ruling 2026-10-31, restyled flush after the
 * first round feedback ("styling still does not match Collapsible 08" and "no syntax
 * highlighting"): the card itself plays the block's single surface — slim mono
 * header bar (file label + total line count, border-b), numbered mono code
 * body, gradient fade while collapsed, and the border-t show-more footer
 * trigger. No nested border/rounding: the surrounding Card is the block's
 * outer box, so this component renders full-bleed sections.
 *
 * SQL syntax highlighting follows the block's token vocabulary (strings /
 * keywords / numbers / punctuation) with the palette expressed as dedicated
 * scan-safe classes (.code-token-*, global.css) instead of raw tailwind
 * color utilities. Punctuation stays in the muted ink, same as the block.
 *
 * Line gutter is widened to w-12/tabular-nums over the block's w-8: bulk
 * submissions routinely run past 99 lines and the gutter must not overlap.
 * With rest.length === 0 (short statements) the trigger is omitted — header
 * + all lines, no dead footer.
 */
const PREVIEW_LINES = 5;

export function SqlFileViewer({
  value,
  "data-testid": testId,
}: {
  value: string;
  "data-testid"?: string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const lines = value.split("\n");
  const preview = lines.slice(0, PREVIEW_LINES);
  const rest = lines.slice(PREVIEW_LINES);

  const renderLines = (list: string[], offset: number): ReactNode =>
    list.map((line, index) => (
      <div key={index + offset} className="flex">
        <span className="w-12 shrink-0 pr-3 text-right text-muted-foreground/60 select-none tabular-nums">
          {index + offset + 1}
        </span>
        <span className="whitespace-pre text-foreground/90">
          {line ? highlightSqlLine(line) : " "}
        </span>
      </div>
    ));

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="w-full"
      data-testid={testId}
    >
      <div className="flex items-center justify-between border-b border-border px-3 py-2">
        <span className="font-mono text-xs text-muted-foreground">
          {t("orders.sql.fileLabel")}
        </span>
        <span
          className="text-xs text-muted-foreground tabular-nums"
          data-testid="order-sql-line-count"
        >
          {t("orders.sql.lineCount", { count: lines.length })}
        </span>
      </div>
      {/* Unified scrollbars (owner 2026-11-03): horizontal scrolling of the SQL file uses ScrollArea overlay
          thumb. The gradient fade mask must stay pinned to the container edge and not scroll horizontally with the content, so it sits
          on the relative layer outside ScrollArea.
          owner 2026-11-04: expanding all lines must not stretch the viewer unboundedly — viewport height cap
          is 500px, and overflow scrolls inside the viewer; max-h is placed on the viewport per scroll-area.tsx
          comment so clamping is reliable. */}
      <div className="relative font-mono text-xs">
        <ScrollArea className="py-2" viewportClassName="max-h-[500px]">
          <div className="w-max">
            {renderLines(preview, 0)}
            {rest.length > 0 && (
              <CollapsibleContent>{renderLines(rest, PREVIEW_LINES)}</CollapsibleContent>
            )}
          </div>
        </ScrollArea>
        {!open && rest.length > 0 && (
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-x-0 bottom-0 h-10 bg-linear-to-t from-card to-transparent"
          />
        )}
      </div>
      {rest.length > 0 && (
        <CollapsibleTrigger
          className="flex w-full items-center justify-center gap-1.5 border-t border-border py-2 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring"
          data-testid="order-sql-more-trigger"
        >
          {open ? (
            <ChevronsDownUp className="size-3.5" aria-hidden="true" />
          ) : (
            <ChevronsUpDown className="size-3.5" aria-hidden="true" />
          )}
          {open
            ? t("orders.sql.collapse")
            : t("orders.sql.showMore", { count: rest.length })}
        </CollapsibleTrigger>
      )}
    </Collapsible>
  );
}
