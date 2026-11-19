import type { ComponentProps, CSSProperties, ReactNode } from "react"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/shared/components/ui/sheet"

type SidebarMobileProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  side: "left" | "right"
  dir?: string
  rootProps: Omit<ComponentProps<"div">, "children" | "className" | "dir">
  children: ReactNode
}

/**
 * Mobile-only shell navigation. This module owns the Sheet import so opening
 * the navigation can load the dialog/portal stack without putting it in the
 * initial application bundle.
 */
export function SidebarMobile({
  open,
  onOpenChange,
  side,
  dir,
  rootProps,
  children,
}: SidebarMobileProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange} {...rootProps}>
      <SheetContent
        dir={dir}
        data-sidebar="sidebar"
        data-slot="sidebar"
        data-mobile="true"
        className="w-(--sidebar-width) bg-sidebar p-0 text-sidebar-foreground [&>button]:hidden"
        style={
          {
            "--sidebar-width": "18rem",
          } as CSSProperties
        }
        side={side}
      >
        <SheetHeader className="sr-only">
          <SheetTitle>Sidebar</SheetTitle>
          <SheetDescription>Displays the mobile sidebar.</SheetDescription>
        </SheetHeader>
        <div className="flex h-full w-full flex-col">{children}</div>
      </SheetContent>
    </Sheet>
  )
}
