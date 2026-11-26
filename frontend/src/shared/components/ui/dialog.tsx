"use client"

import * as React from "react"
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog"

import { cn } from "@/shared/lib/utils"
import { Button } from "@/shared/components/ui/button"
import { ScrollArea } from "@/shared/components/ui/scroll-area"
import { XIcon } from "lucide-react"

function Dialog({ ...props }: DialogPrimitive.Root.Props) {
  return <DialogPrimitive.Root data-slot="dialog" {...props} />
}

function DialogTrigger({ ...props }: DialogPrimitive.Trigger.Props) {
  return <DialogPrimitive.Trigger data-slot="dialog-trigger" {...props} />
}

function DialogPortal({ ...props }: DialogPrimitive.Portal.Props) {
  return <DialogPrimitive.Portal data-slot="dialog-portal" {...props} />
}

function DialogClose({ ...props }: DialogPrimitive.Close.Props) {
  return <DialogPrimitive.Close data-slot="dialog-close" {...props} />
}

function DialogOverlay({
  className,
  ...props
}: DialogPrimitive.Backdrop.Props) {
  return (
    <DialogPrimitive.Backdrop
      data-slot="dialog-overlay"
      className={cn(
        "fixed inset-0 isolate z-50 bg-black/40 duration-150 ease-out data-open:animate-in data-open:fade-in-0 data-open:duration-[180ms] data-closed:animate-out data-closed:fade-out-0 data-closed:duration-[120ms]",
        className
      )}
      {...props}
    />
  )
}

function DialogContent({
  className,
  viewportClassName,
  children,
  showCloseButton = true,
  initialFocus,
  ref,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Popup> & {
  showCloseButton?: boolean
  viewportClassName?: string
}) {
  const popupRef = React.useRef<HTMLDivElement>(null)
  const setPopupRef = React.useCallback(
    (node: HTMLDivElement | null) => {
      popupRef.current = node
      if (typeof ref === "function") {
        ref(node)
      } else if (ref !== undefined && ref !== null) {
        ref.current = node
      }
    },
    [ref]
  )
  const headerChildren: React.ReactNode[] = []
  const bodyChildren: React.ReactNode[] = []
  const footerChildren: React.ReactNode[] = []
  React.Children.forEach(children, (child) => {
    if (React.isValidElement(child) && child.type === DialogHeader) {
      headerChildren.push(child)
    } else if (React.isValidElement(child) && child.type === DialogFooter) {
      footerChildren.push(child)
    } else {
      bodyChildren.push(child)
    }
  })

  const hasHeader = headerChildren.length > 0
  const hasFooter = footerChildren.length > 0
  const viewportMaxHeight =
    hasHeader && hasFooter
      ? "max-h-[calc(500px-10.5rem)]"
      : hasHeader
        ? "max-h-[calc(500px-6.5rem)]"
        : hasFooter
          ? showCloseButton
            ? "max-h-[calc(500px-7.75rem)]"
            : "max-h-[calc(500px-7rem)]"
          : showCloseButton
            ? "max-h-[calc(500px-3.75rem)]"
            : "max-h-[calc(500px-3rem)]"

  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Popup
        ref={setPopupRef}
        data-slot="dialog-content"
        /* Default initialFocus to the popup container: Base UI FloatingFocusManager
           sets preventScroll: true only when focusing floatingFocusElement itself,
           keeping ScrollArea scrolled to top=0 when top notices or disabled fields
           push the first tabbable control below the fold (while still honoring
           autoFocus children, explicit initialFocus, focus trapping, and Tab). */
        initialFocus={initialFocus ?? popupRef}
        className={cn(
          /* Unified scrollbars (owner 2026-11-03): panel becomes a flex column embedding ScrollArea,
             scrolling tall content with an overlay thumb instead of callers passing overflow-y-auto and exposing
             a native bar. The height cap sits on the viewport rather than the panel/root: inside an auto-height chain,
             height:100% collapses to auto (see scroll-area.tsx comment), so only max-h on the viewport itself
             clamps reliably.
             owner 2026-11-04: all dialogs cap height at 500px — clamped at both panel and viewport;
             overflowing content scrolls inside the panel.
             owner 2026-11-09: render DialogHeader in the fixed card title area at the top of the dialog
             (outside ScrollArea) aligned with the close button, and keep negative right margin (-mr-3)
             plus pr-6 on the scrollable body so the scrollbar never crowds form fields.
             owner 2026-11-19: render top-level DialogFooter outside ScrollArea at the bottom so primary actions
             stay pinned and never scroll away with tall dialog bodies. */
          "fixed top-1/2 left-1/2 z-50 flex max-h-[500px] w-full max-w-[calc(100%-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 rounded-lg bg-popover p-6 text-xs/relaxed text-popover-foreground ring-1 ring-foreground/10 duration-150 ease-out outline-none sm:max-w-sm data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-open:duration-[180ms] data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95 data-closed:duration-[120ms]",
          showCloseButton && !hasHeader && "pt-9",
          className
        )}
        {...props}
      >
        {headerChildren}
        <ScrollArea
          className={cn("min-h-0 flex-1", showCloseButton && "-mr-3")}
          viewportClassName={viewportClassName ?? viewportMaxHeight}
        >
          <div className={cn("flex flex-col gap-4", showCloseButton ? "pr-6" : "pr-3")}>
            {bodyChildren}
          </div>
        </ScrollArea>
        {footerChildren}
        {showCloseButton && (
          <DialogPrimitive.Close
            data-slot="dialog-close"
            render={
              <Button
                variant="ghost"
                className="absolute top-4 right-4"
                size="icon-sm"
              />
            }
          >
            <XIcon
            />
            <span className="sr-only">Close</span>
          </DialogPrimitive.Close>
        )}
      </DialogPrimitive.Popup>
    </DialogPortal>
  )
}

function DialogHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="dialog-header"
      className={cn("flex shrink-0 flex-col gap-1 pr-8 text-left", className)}
      {...props}
    />
  )
}

function DialogFooter({
  className,
  showCloseButton = false,
  children,
  ...props
}: React.ComponentProps<"div"> & {
  showCloseButton?: boolean
}) {
  return (
    <div
      data-slot="dialog-footer"
      className={cn(
        "flex shrink-0 flex-col-reverse gap-2 sm:flex-row sm:justify-end",
        className
      )}
      {...props}
    >
      {children}
      {showCloseButton && (
        <DialogPrimitive.Close render={<Button variant="outline" />}>
          Close
        </DialogPrimitive.Close>
      )}
    </div>
  )
}

function DialogTitle({ className, ...props }: DialogPrimitive.Title.Props) {
  return (
    <DialogPrimitive.Title
      data-slot="dialog-title"
      className={cn("text-sm font-medium", className)}
      {...props}
    />
  )
}

function DialogDescription({
  className,
  ...props
}: DialogPrimitive.Description.Props) {
  return (
    <DialogPrimitive.Description
      data-slot="dialog-description"
      className={cn(
        "text-xs/relaxed text-muted-foreground *:[a]:underline *:[a]:underline-offset-3 *:[a]:hover:text-foreground",
        className
      )}
      {...props}
    />
  )
}

export {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogOverlay,
  DialogPortal,
  DialogTitle,
  DialogTrigger,
}
