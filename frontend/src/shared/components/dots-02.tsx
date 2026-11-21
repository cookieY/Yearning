import { motion } from "motion/react";
import { cn } from "@/shared/lib/utils";
import { DOT_BASE_SIZE, DOT_EYE_SPRING, useDotEyeMotion } from "@/shared/components/use-dot-eye-motion";

/**
 * Shadcn Dashboard `dots-02`: a warm amber character with two top ear-like bumps
 * and round eyes that blink and glance around (or track the pointer when
 * `followCursor` is enabled), giving a gentle jump every few seconds.
 * Structure, geometry and motion match the original block; palette classes map
 * to semantic tokens (`fill-tone-amber stroke-tone-amber` and `bg-foreground`)
 * to satisfy the raw-color gate across light and dark themes, and
 * `useReducedMotion()` provides a static fallback.
 */

export interface DotProps {
  className?: string;
  followCursor?: boolean;
  size?: number;
  "aria-label"?: string;
}

export function Dot({
  className,
  followCursor = false,
  size = DOT_BASE_SIZE,
  "aria-label": ariaLabel = "Animated dot",
}: DotProps) {
  const { ref, reduceMotion, look, blink } = useDotEyeMotion(followCursor);

  const jump = (size / DOT_BASE_SIZE) * 10;
  const bodyAnimation = reduceMotion ? undefined : { y: [0, -jump, 0] };
  const bodyTransition = {
    duration: 0.45,
    ease: "easeOut",
    repeat: Infinity,
    repeatDelay: 3,
  } as const;

  return (
    <motion.div
      ref={ref}
      animate={bodyAnimation}
      transition={bodyTransition}
      role="img"
      aria-label={ariaLabel}
      style={{ width: size, height: size }}
      className={cn("relative", className)}
    >
      <svg
        viewBox="0 0 100 100"
        aria-hidden="true"
        className="fill-assistant-mascot stroke-assistant-mascot size-full"
      >
        <rect x="18" y="30" width="64" height="52" rx="24" />
        <circle cx="31" cy="30" r="12" />
        <circle cx="69" cy="30" r="12" />
      </svg>
      <div
        className={cn(
          "absolute top-1/2 left-1/2 flex size-24 items-center justify-center",
          "pt-2",
        )}
        style={{
          transform: `translate(-50%, -50%) scale(${String(size / DOT_BASE_SIZE)})`,
        }}
      >
        <motion.div
          className="flex items-center gap-2.5"
          animate={{
            x: look.x * 6,
            y: look.y * 5,
            scaleY: blink ? 0.1 : 1,
          }}
          transition={{ ...DOT_EYE_SPRING, scaleY: { duration: 0.1 } }}
        >
          <div className="bg-assistant-mascot-eye size-2.5 rounded-full" />
          <div className="bg-assistant-mascot-eye size-2.5 rounded-full" />
        </motion.div>
      </div>
    </motion.div>
  );
}

export default function Dots02() {
  return <Dot followCursor />;
}
