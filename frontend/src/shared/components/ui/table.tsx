import * as React from "react"

import { cn } from "@/shared/lib/utils"

function Table({ className, density = "comfortable", ...props }: React.ComponentProps<"table"> & { density?: "comfortable" | "compact" }) {
  return (
    <div
      data-slot="table-container"
      // `overflow-y: clip` instead of the implicit `auto`: rows are translated
      // during a FLIP reorder and an auto/scroll pairing would flash a
      // scrollbar (reflowing the table width) on every 200ms animation.
      className="relative w-full overflow-x-auto overflow-y-clip"
    >
      <table
        data-slot="table"
        data-density={density}
        className={cn(
          // Owner table reference: readable 14px body with 24px status capsules.
          "w-full caption-bottom text-sm leading-5 [&_[data-slot=badge]]:h-6 [&_[data-slot=badge]]:px-2.5 [&_[data-slot=badge]]:text-[13px] [&_[data-slot=badge]]:font-semibold",
          density === "compact" && "[&_[data-slot=table-row]]:h-9 [&_[data-slot=table-head]]:h-9 [&_[data-slot=table-head]]:px-3 [&_[data-slot=table-cell]]:px-3 [&_[data-slot=table-cell]]:py-2 [&_[data-slot=badge]]:h-5 [&_[data-slot=badge]]:px-2 [&_[data-slot=badge]]:text-xs",
          className
        )}
        {...props}
      />
    </div>
  )
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
  return (
    <thead
      data-slot="table-header"
      // table-01 reference: the header row is inert (no hover wash) and its
      // own border is the card's header divider.
      className={cn("bg-muted/25 [&_tr]:h-10 [&_tr]:border-b [&_tr]:hover:bg-transparent", className)}
      {...props}
    />
  )
}

function TableBody({
  className,
  stale = false,
  ...props
}: React.ComponentProps<"tbody"> & { stale?: boolean }) {
  return (
    <tbody
      data-slot="table-body"
      // While a keepPreviousData page is being replaced the rows on screen are
      // stale: mark them busy and make them inert so a click cannot act on the
      // previous page's row. The fetch is usually short; the footer keeps the
      // 100ms busy dot visible.
      aria-busy={stale || undefined}
      className={cn(
        "[&_tr:last-child]:border-0",
        stale && "pointer-events-none",
        className,
      )}
      {...props}
    />
  )
}

function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) {
  return (
    <tfoot
      data-slot="table-footer"
      className={cn(
        "border-t bg-muted/50 font-semibold [&>tr]:last:border-b-0",
        className
      )}
      {...props}
    />
  )
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
  return (
    <tr
      data-slot="table-row"
      className={cn(
        "h-11 border-b border-border/70 transition-colors duration-100 ease-out hover:bg-muted/30 has-aria-expanded:bg-muted/50 active:bg-muted/50 data-[state=selected]:bg-muted",
        className
      )}
      {...props}
    />
  )
}

function TableHead({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      data-slot="table-head"
      // Reference ruling 2026-10-24 (all tables ≥95% parity): the header is
      // quiet: muted color, medium weight and a 40px band.
      className={cn(
        "h-10 px-5 py-2 text-left align-middle text-[13px] font-medium tracking-wide whitespace-nowrap text-muted-foreground [&:has([role=checkbox])]:pr-0",
        className
      )}
      {...props}
    />
  )
}

function TableCell({ className, ...props }: React.ComponentProps<"td">) {
  return (
    <td
      data-slot="table-cell"
      // Primary identifiers opt into foreground/semibold at the column;
      // supporting values inherit the quiet tone and aligned digits.
      className={cn(
        "px-5 py-2 align-middle text-muted-foreground whitespace-nowrap tabular-nums [&:has([role=checkbox])]:pr-0",
        className
      )}
      {...props}
    />
  )
}

function TableCaption({
  className,
  ...props
}: React.ComponentProps<"caption">) {
  return (
    <caption
      data-slot="table-caption"
      className={cn("mt-4 text-xs text-muted-foreground", className)}
      {...props}
    />
  )
}

export {
  Table,
  TableHeader,
  TableBody,
  TableFooter,
  TableHead,
  TableRow,
  TableCell,
  TableCaption,
}
