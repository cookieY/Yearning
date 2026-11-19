"use client"

import { Collapsible as CollapsiblePrimitive } from "@base-ui/react/collapsible"

import { cn } from "@/shared/lib/utils"

/**
 * Collapsible primitive (shadcn-dashboard collapsible-08 block, owner ruling
 * 2026-10-31) backed by Base UI — the project standard, not the upstream
 * Radix build. Panel keeps unmounted-when-closed semantics by default
 * (keepMounted is available on CollapsibleContent when a surface needs it).
 */

function Collapsible({ ...props }: CollapsiblePrimitive.Root.Props) {
  return <CollapsiblePrimitive.Root data-slot="collapsible" {...props} />
}

function CollapsibleTrigger({
  className,
  ...props
}: CollapsiblePrimitive.Trigger.Props) {
  return (
    <CollapsiblePrimitive.Trigger
      data-slot="collapsible-trigger"
      className={cn(
        "cursor-pointer transition-[color,background-color,transform] duration-150 ease-out motion-safe:active:scale-[0.99]",
        className
      )}
      {...props}
    />
  )
}

function CollapsibleContent({
  className,
  ...props
}: CollapsiblePrimitive.Panel.Props) {
  return (
    <CollapsiblePrimitive.Panel
      data-slot="collapsible-content"
      className={cn(
        "origin-top overflow-hidden transition-[transform,opacity] duration-200 ease-out outline-none data-starting-style:-translate-y-1 data-starting-style:scale-y-[0.98] data-starting-style:opacity-0 data-starting-style:duration-200 data-ending-style:-translate-y-1 data-ending-style:scale-y-[0.98] data-ending-style:opacity-0 data-ending-style:duration-[130ms]",
        className
      )}
      {...props}
    />
  )
}

export { Collapsible, CollapsibleTrigger, CollapsibleContent }
