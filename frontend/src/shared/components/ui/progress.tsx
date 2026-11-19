"use client"

import { Progress as ProgressPrimitive } from "@base-ui/react/progress"

import { cn } from "@/shared/lib/utils"

function Progress({
  className,
  children,
  value,
  min = 0,
  max = 100,
  style,
  ...props
}: ProgressPrimitive.Root.Props) {
  // The indicator is laid out at full width and scaled with a compositor-only
  // transform, so progress updates never tween `width` (which would relayout
  // the track on every animation frame).
  //
  // The percentage must mirror Base UI's own normalisation (ProgressRoot.js:
  // `valueToPercent` + clamp) instead of assuming 0..100: a non-finite value or
  // an inverted range would otherwise emit an invalid `scale` (falling back to
  // a full bar) or a negative one (a strip growing from the wrong edge).
  // Bounds are normalised too: `min = -Infinity` or `max <= min` would make
  // the percentage NaN/negative, which lands as an invalid `scale` (full bar)
  // while Base UI still reports a clamped aria value — a visual/ARIA mismatch.
  // Such input is treated as an indeterminate bar instead.
  const validRange = Number.isFinite(min) && Number.isFinite(max) && max > min
  const percent =
    validRange && typeof value === "number" && Number.isFinite(value)
      ? Math.min(100, Math.max(0, ((value - min) / (max - min)) * 100))
      : 0
  // 4 decimals keep the CSS variable free of float artifacts like
  // 0.35000000000000003 (sub-pixel anyway, but noisy in snapshots and string
  // assertions).
  const scale = Math.round(percent * 100) / 10_000

  return (
    <ProgressPrimitive.Root
      value={validRange ? (value ?? null) : null}
      min={validRange ? min : 0}
      max={validRange ? max : 100}
      data-slot="progress"
      style={{ "--progress-scale": String(scale), ...style } as unknown as React.CSSProperties}
      className={cn("flex flex-wrap gap-3", className)}
      {...props}
    >
      {children}
      <ProgressTrack>
        <ProgressIndicator />
      </ProgressTrack>
    </ProgressPrimitive.Root>
  )
}

function ProgressTrack({ className, ...props }: ProgressPrimitive.Track.Props) {
  return (
    <ProgressPrimitive.Track
      className={cn(
        "relative flex h-1 w-full items-center overflow-x-hidden rounded-full bg-muted",
        className
      )}
      data-slot="progress-track"
      {...props}
    />
  )
}

function ProgressIndicator({
  className,
  style,
  ...props
}: ProgressPrimitive.Indicator.Props) {
  return (
    <ProgressPrimitive.Indicator
      data-slot="progress-indicator"
      // Base UI sets an inline `width: <percentage>%`; overriding it to 100%
      // here is what turns the fill into a transform instead of a layout box.
      style={{ width: "100%", ...style }}
      className={cn(
        "h-full w-full origin-left scale-x-(--progress-scale,0) bg-primary transition-[scale] duration-300 ease-out rtl:origin-right",
        className
      )}
      {...props}
    />
  )
}

function ProgressLabel({ className, ...props }: ProgressPrimitive.Label.Props) {
  return (
    <ProgressPrimitive.Label
      className={cn("text-xs", className)}
      data-slot="progress-label"
      {...props}
    />
  )
}

function ProgressValue({ className, ...props }: ProgressPrimitive.Value.Props) {
  return (
    <ProgressPrimitive.Value
      className={cn(
        "ml-auto text-xs text-muted-foreground tabular-nums",
        className
      )}
      data-slot="progress-value"
      {...props}
    />
  )
}

export {
  Progress,
  ProgressTrack,
  ProgressIndicator,
  ProgressLabel,
  ProgressValue,
}
