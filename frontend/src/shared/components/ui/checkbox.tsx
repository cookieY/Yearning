import { Checkbox as CheckboxPrimitive } from "@base-ui/react/checkbox"
import { CheckIcon, MinusIcon } from "lucide-react"

import { cn } from "@/shared/lib/utils"

/**
 * §18.70: indeterminate state renders as a solid minus icon, clearly distinguished from checked checkmark —
 * master select-all checkboxes (§18.64 steps/executors, §18.44 dialog tri-state, etc.) previously drew a checkmark
 * when indeterminate too, making it visually indistinguishable from fully selected. Unchecked/checked rendering is unchanged
 * (Base UI sets only data-indeterminate when indeterminate, without data-checked/unchecked).
 */
function Checkbox({
  className,
  ...props
}: CheckboxPrimitive.Root.Props) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      className={cn(
        "peer size-4 shrink-0 rounded-[4px] border border-input bg-transparent transition-colors duration-100 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 data-[checked]:bg-primary data-[checked]:text-primary-foreground data-[checked]:border-primary data-[indeterminate]:bg-primary data-[indeterminate]:text-primary-foreground data-[indeterminate]:border-primary",
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator
        data-slot="checkbox-indicator"
        className="group/checkbox-indicator flex items-center justify-center text-current"
      >
        <CheckIcon className="size-3.5 group-data-[indeterminate]/checkbox-indicator:hidden" />
        <MinusIcon className="hidden size-3.5 group-data-[indeterminate]/checkbox-indicator:block" />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  )
}

export { Checkbox }
