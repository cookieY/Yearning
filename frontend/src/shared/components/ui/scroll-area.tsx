"use client"

import type * as React from "react"
import { ScrollArea as ScrollAreaPrimitive } from "@base-ui/react/scroll-area"

import { cn } from "@/shared/lib/utils"

/**
 * Frozen template kit (ui-template-baseline 6f99c0b) scroll-area, copied
 * verbatim except the cn import path. YearningX extensions (owner ruling
 * 2026-11-03: use ScrollArea for all scrollbars):
 * 1. A horizontal ScrollBar is rendered alongside the vertical one — base-ui
 *    unmounts a Scrollbar whose axis has no overflow (keepMounted defaults to
 *    false), so surfaces that only scroll vertically never paint a horizontal
 *    track.
 * 2. `viewportClassName` targets the viewport: the viewport's `size-full`
 *    percentage height only resolves against a definite-height containing
 *    block. When the Root sits in an auto-height (content-sized, possibly
 *    max-h-clamped) flex chain — dialogs, dropdown panels, capped lists —
 *    height:100% falls back to auto and the viewport grows past the Root.
 *    Putting the height cap (`max-h-*`) directly on the viewport via this
 *    prop instead of on the Root keeps such surfaces clipped-and-scrolling.
 * 3. `viewportProps` spreads onto the Viewport — surfaces that hand the
 *    viewport to libraries (virtualizers need the real scroll element as
 *    their ref) or own a11y semantics on the scroll region itself
 *    (role/tabIndex, WCAG 2.1.1) pass them here instead of the Root.
 */
function ScrollArea({
  className,
  viewportClassName,
  viewportProps,
  style,
  children,
  ...props
}: ScrollAreaPrimitive.Root.Props & {
  viewportClassName?: string
  viewportProps?: Omit<
    React.ComponentProps<typeof ScrollAreaPrimitive.Viewport>,
    "className" | "children"
  >
}) {
  const mergedClassName = cn("relative", className)
  const positionMatch = /(?:^|\s)(absolute|fixed|sticky)(?:\s|$)/.exec(mergedClassName)
  const resolvedStyle =
    positionMatch !== null && typeof style !== "function"
      ? { position: positionMatch[1] as React.CSSProperties["position"], ...style }
      : style
  return (
    <ScrollAreaPrimitive.Root
      data-slot="scroll-area"
      className={mergedClassName}
      style={resolvedStyle}
      {...props}
    >
      <ScrollAreaPrimitive.Viewport
        data-slot="scroll-area-viewport"
        className={cn(
          "relative size-full rounded-[inherit] transition-color-shadow outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-1",
          viewportClassName
        )}
        {...viewportProps}
      >
        {children}
      </ScrollAreaPrimitive.Viewport>
      <ScrollBar />
      <ScrollBar orientation="horizontal" />
      <ScrollAreaPrimitive.Corner />
    </ScrollAreaPrimitive.Root>
  )
}

function ScrollBar({
  className,
  orientation = "vertical",
  ...props
}: ScrollAreaPrimitive.Scrollbar.Props) {
  return (
    <ScrollAreaPrimitive.Scrollbar
      data-slot="scroll-area-scrollbar"
      data-orientation={orientation}
      orientation={orientation}
      className={cn(
        "cn-scroll-area-scrollbar flex touch-none p-px transition-colors select-none data-horizontal:h-2.5 data-horizontal:flex-col data-horizontal:border-t data-horizontal:border-t-transparent data-vertical:h-full data-vertical:w-2.5 data-vertical:border-l data-vertical:border-l-transparent",
        className
      )}
      {...props}
    >
      <ScrollAreaPrimitive.Thumb
        data-slot="scroll-area-thumb"
        className="cn-scroll-area-thumb relative flex-1 rounded-full bg-border"
      />
    </ScrollAreaPrimitive.Scrollbar>
  )
}

export { ScrollArea, ScrollBar }
