import * as React from "react"
import { Select as SelectPrimitive } from "@base-ui/react/select"
import { useTranslation } from "react-i18next"

import { cn } from "@/shared/lib/utils"
import { ChevronDownIcon, CheckIcon, ChevronUpIcon, SearchIcon } from "lucide-react"

function collectSelectItems(
  node: React.ReactNode,
  out: Record<string, React.ReactNode>
): void {
  React.Children.forEach(node, (child) => {
    if (!React.isValidElement(child)) return
    const childProps = child.props as {
      value?: unknown
      children?: React.ReactNode
    }
    if (
      child.type === SelectItem &&
      (typeof childProps.value === "string" || typeof childProps.value === "number")
    ) {
      out[String(childProps.value)] = childProps.children
      return
    }
    if (childProps.children !== undefined) {
      collectSelectItems(childProps.children, out)
    }
  })
}

function extractSelectNodeText(node: React.ReactNode): string {
  if (typeof node === "string" || typeof node === "number") {
    return String(node)
  }
  if (Array.isArray(node)) {
    return node.map(extractSelectNodeText).join(" ")
  }
  if (React.isValidElement(node)) {
    const childProps = node.props as { children?: React.ReactNode }
    return extractSelectNodeText(childProps.children)
  }
  return ""
}

function hasCustomSearchInput(node: React.ReactNode): boolean {
  let found = false
  React.Children.forEach(node, (child) => {
    if (found || !React.isValidElement(child)) return
    const childProps = child.props as {
      children?: React.ReactNode
      "data-slot"?: string
      "data-testid"?: string
    }
    const typeName =
      typeof child.type === "string"
        ? child.type
        : (child.type as { displayName?: string; name?: string }).displayName ??
          (child.type as { name?: string }).name ??
          ""
    if (
      typeName === "input" ||
      typeName === "Input" ||
      childProps["data-slot"] === "input" ||
      (typeof childProps["data-testid"] === "string" &&
        childProps["data-testid"].includes("search"))
    ) {
      found = true
      return
    }
    if (childProps.children !== undefined) {
      if (hasCustomSearchInput(childProps.children)) {
        found = true
      }
    }
  })
  return found
}

function filterSelectChildren(
  node: React.ReactNode,
  query: string
): { nodes: React.ReactNode[]; matchCount: number } {
  const nodes: React.ReactNode[] = []
  let matchCount = 0

  React.Children.forEach(node, (child) => {
    if (!React.isValidElement(child)) {
      nodes.push(child)
      return
    }
    const childProps = child.props as {
      value?: unknown
      children?: React.ReactNode
    }
    if (child.type === SelectItem) {
      const valueText = String(childProps.value ?? "").toLowerCase()
      const labelText = extractSelectNodeText(childProps.children).toLowerCase()
      if (valueText.includes(query) || labelText.includes(query)) {
        nodes.push(child)
        matchCount += 1
      }
      return
    }
    if (childProps.children !== undefined) {
      const sub = filterSelectChildren(childProps.children, query)
      if (child.type === SelectGroup && sub.matchCount === 0) {
        return
      }
      matchCount += sub.matchCount
      nodes.push(React.cloneElement(child, undefined, ...sub.nodes))
      return
    }
    nodes.push(child)
  })

  return { nodes, matchCount }
}

function Select<Value, Multiple extends boolean | undefined = false>({
  items,
  children,
  ...props
}: SelectPrimitive.Root.Props<Value, Multiple>) {
  const resolvedItems = React.useMemo(() => {
    if (Array.isArray(items)) return items
    const derived: Record<string, React.ReactNode> = {}
    collectSelectItems(children, derived)
    if (items !== undefined) {
      return { ...derived, ...items }
    }
    return Object.keys(derived).length > 0 ? derived : undefined
  }, [children, items])

  return (
    <SelectPrimitive.Root items={resolvedItems} {...props}>
      {children}
    </SelectPrimitive.Root>
  )
}

function SelectGroup({ className, ...props }: SelectPrimitive.Group.Props) {
  return (
    <SelectPrimitive.Group
      data-slot="select-group"
      className={cn("scroll-my-1", className)}
      {...props}
    />
  )
}

function SelectValue({ className, ...props }: SelectPrimitive.Value.Props) {
  return (
    <SelectPrimitive.Value
      data-slot="select-value"
      className={cn("flex flex-1 text-left", className)}
      {...props}
    />
  )
}

function SelectTrigger({
  className,
  size = "default",
  children,
  ...props
}: SelectPrimitive.Trigger.Props & {
  size?: "sm" | "default"
}) {
  return (
    <SelectPrimitive.Trigger
      data-slot="select-trigger"
      data-size={size}
      className={cn(
        "flex w-fit items-center justify-between gap-1.5 rounded-md border border-input bg-transparent py-2 pr-2 pl-2.5 text-xs whitespace-nowrap transition-colors outline-none select-none focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-1 aria-invalid:ring-destructive/20 data-placeholder:text-muted-foreground data-[size=default]:h-8 data-[size=sm]:h-7 data-[size=sm]:rounded-md *:data-[slot=select-value]:line-clamp-1 *:data-[slot=select-value]:flex *:data-[slot=select-value]:items-center *:data-[slot=select-value]:gap-1.5 dark:bg-input/30 dark:hover:bg-input/50 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      {children}
      <SelectPrimitive.Icon
        render={
          <ChevronDownIcon className="pointer-events-none size-4 text-muted-foreground" />
        }
      />
    </SelectPrimitive.Trigger>
  )
}

function SelectSearchableBody({
  children,
  searchable,
}: {
  children: React.ReactNode
  searchable: boolean
}) {
  const { t } = useTranslation()
  const [search, setSearch] = React.useState("")
  const showBuiltInSearch = searchable && !hasCustomSearchInput(children)
  const query = search.trim().toLowerCase()

  const filtered = React.useMemo(() => {
    if (!showBuiltInSearch || query === "") {
      return { nodes: children, matchCount: -1 }
    }
    return filterSelectChildren(children, query)
  }, [children, query, showBuiltInSearch])

  const placeholder = t("common.selectSearchPlaceholder", {
    defaultValue: "Search options…",
  })

  return (
    <>
      {showBuiltInSearch && (
        <div
          data-slot="select-search"
          className="sticky top-0 z-20 flex items-center gap-1.5 border-b border-border/60 bg-popover px-2 py-1.5"
        >
          <SearchIcon
            className="size-3.5 shrink-0 text-muted-foreground"
            aria-hidden
          />
          <input
            type="search"
            data-slot="select-search-input"
            value={search}
            onChange={(event) => {
              setSearch(event.target.value)
            }}
            onKeyDown={(event) => {
              if (event.key !== "Escape") {
                event.stopPropagation()
              }
            }}
            placeholder={placeholder}
            aria-label={placeholder}
            className="w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground [&::-webkit-search-cancel-button]:hidden"
          />
        </div>
      )}
      <SelectScrollUpButton />
      <SelectPrimitive.List>
        {filtered.matchCount === 0 ? (
          <div className="px-2 py-3 text-center text-xs text-muted-foreground">
            {t("table.emptySearch", { defaultValue: "No matching options" })}
          </div>
        ) : (
          filtered.nodes
        )}
      </SelectPrimitive.List>
      <SelectScrollDownButton />
    </>
  )
}

function SelectContent({
  className,
  children,
  side = "bottom",
  sideOffset = 4,
  align = "center",
  alignOffset = 0,
  alignItemWithTrigger = false,
  searchable = true,
  ...props
}: SelectPrimitive.Popup.Props &
  Pick<
    SelectPrimitive.Positioner.Props,
    "align" | "alignOffset" | "side" | "sideOffset" | "alignItemWithTrigger"
  > & {
    searchable?: boolean
  }) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Positioner
        side={side}
        sideOffset={sideOffset}
        align={align}
        alignOffset={alignOffset}
        alignItemWithTrigger={alignItemWithTrigger}
        className="isolate z-50"
      >
        <SelectPrimitive.Popup
          data-slot="select-content"
          data-align-trigger={alignItemWithTrigger}
          className={cn("relative isolate z-50 max-h-(--available-height) w-(--anchor-width) min-w-36 origin-(--transform-origin) overflow-x-hidden overflow-y-auto rounded-md bg-popover text-popover-foreground ring-1 ring-foreground/10 duration-150 data-[align-trigger=true]:animate-none data-[side=bottom]:slide-in-from-top-2 data-[side=inline-end]:slide-in-from-left-2 data-[side=inline-start]:slide-in-from-right-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:duration-[100ms] data-closed:fade-out-0 data-closed:zoom-out-95", className )}
          {...props}
        >
          <SelectSearchableBody searchable={searchable}>
            {children}
          </SelectSearchableBody>
        </SelectPrimitive.Popup>
      </SelectPrimitive.Positioner>
    </SelectPrimitive.Portal>
  )
}

function SelectLabel({
  className,
  ...props
}: SelectPrimitive.GroupLabel.Props) {
  return (
    <SelectPrimitive.GroupLabel
      data-slot="select-label"
      className={cn("px-2 py-2 text-xs font-medium text-muted-foreground", className)}
      {...props}
    />
  )
}

function SelectItem({
  className,
  children,
  ...props
}: SelectPrimitive.Item.Props) {
  return (
    <SelectPrimitive.Item
      data-slot="select-item"
      className={cn(
        "relative flex w-full cursor-default items-center gap-2 rounded-md py-2 pr-8 pl-2 text-xs outline-hidden select-none focus:bg-accent focus:text-accent-foreground not-data-[variant=destructive]:focus:**:text-accent-foreground data-disabled:pointer-events-none data-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 *:[span]:last:flex *:[span]:last:items-center *:[span]:last:gap-2",
        className
      )}
      {...props}
    >
      <SelectPrimitive.ItemText className="flex flex-1 shrink-0 gap-2 whitespace-nowrap">
        {children}
      </SelectPrimitive.ItemText>
      <SelectPrimitive.ItemIndicator
        render={
          <span className="pointer-events-none absolute right-2 flex size-4 items-center justify-center" />
        }
      >
        <CheckIcon className="pointer-events-none" />
      </SelectPrimitive.ItemIndicator>
    </SelectPrimitive.Item>
  )
}

function SelectSeparator({
  className,
  ...props
}: SelectPrimitive.Separator.Props) {
  return (
    <SelectPrimitive.Separator
      data-slot="select-separator"
      className={cn("pointer-events-none -mx-1 h-px bg-border", className)}
      {...props}
    />
  )
}

function SelectScrollUpButton({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.ScrollUpArrow>) {
  return (
    <SelectPrimitive.ScrollUpArrow
      data-slot="select-scroll-up-button"
      className={cn(
        "top-0 z-10 flex w-full cursor-default items-center justify-center bg-popover py-1 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      <ChevronUpIcon
      />
    </SelectPrimitive.ScrollUpArrow>
  )
}

function SelectScrollDownButton({
  className,
  ...props
}: React.ComponentProps<typeof SelectPrimitive.ScrollDownArrow>) {
  return (
    <SelectPrimitive.ScrollDownArrow
      data-slot="select-scroll-down-button"
      className={cn(
        "bottom-0 z-10 flex w-full cursor-default items-center justify-center bg-popover py-1 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      <ChevronDownIcon
      />
    </SelectPrimitive.ScrollDownArrow>
  )
}

export {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectScrollDownButton,
  SelectScrollUpButton,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
}
