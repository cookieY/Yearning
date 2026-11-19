import * as React from "react"
import { Minus, Plus } from "lucide-react"

import { Button } from "@/shared/components/ui/button"
import { Input } from "@/shared/components/ui/input"
import { cn } from "@/shared/lib/utils"

type NumberInputProps = Omit<React.ComponentProps<"input">, "type" | "ref"> & {
  decrementLabel?: string
  incrementLabel?: string
  /** Classes for the outer −/input/+ row (e.g. a fixed width); `className` targets the input. */
  wrapperClassName?: string
}

// Number field following shadcn-dashboard input-07 ("Input with controls"):
// outline −/+ buttons flank a centered input. The native browser spin arrows
// are hidden. Stepping delegates to the DOM's stepUp/stepDown so min/max/step
// (including decimal steps) keep native semantics, then dispatches an input
// event so callers' onChange handlers (event.target.value) fire exactly as
// they do for typed edits.
function NumberInput({
  className,
  disabled,
  readOnly,
  decrementLabel = "Decrease",
  incrementLabel = "Increase",
  wrapperClassName,
  ...props
}: NumberInputProps) {
  const inputRef = React.useRef<HTMLInputElement>(null)

  const step = (direction: "up" | "down") => {
    const el = inputRef.current
    if (!el) return
    try {
      if (direction === "up") el.stepUp()
      else el.stepDown()
    } catch {
      return
    }
    el.dispatchEvent(new Event("input", { bubbles: true }))
  }

  const locked = disabled || readOnly

  return (
    <div data-slot="number-input" className={cn("flex w-full min-w-0 gap-2", wrapperClassName)}>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="shrink-0"
        aria-label={decrementLabel}
        tabIndex={-1}
        disabled={locked}
        onClick={() => { step("down"); }}
      >
        <Minus className="h-4 w-4" />
      </Button>
      <Input
        {...props}
        ref={inputRef}
        type="number"
        disabled={disabled}
        readOnly={readOnly}
        className={cn(
          "bg-background text-center h-8 [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none",
          className
        )}
      />
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="shrink-0"
        aria-label={incrementLabel}
        tabIndex={-1}
        disabled={locked}
        onClick={() => { step("up"); }}
      >
        <Plus className="h-4 w-4" />
      </Button>
    </div>
  )
}

export { NumberInput }
