import { motion } from "motion/react";
import { cn } from "@/shared/lib/utils";
import { DOT_BASE_SIZE, DOT_EYE_SPRING, useDotEyeMotion } from "@/shared/components/use-dot-eye-motion";

/**
 * Shadcn Dashboard `dots-03` (Scalloped): a scalloped dot with caret eyes that
 * blink and glance around, the body giving a small wobble every few seconds.
 * Structure, geometry and motion are the block's own; the only changes are an
 * `aria-label` prop (the block hard-codes an English label) and dropping the
 * demo wrapper. Reduced-motion users get the static dot.
 */

export interface DotProps {
  className?: string;
  /** Eyes track the pointer instead of glancing around on their own. */
  followCursor?: boolean;
  size?: number;
  "aria-label"?: string;
}

export function Dot({ className, followCursor = false, size = DOT_BASE_SIZE, "aria-label": ariaLabel }: DotProps) {
  const { ref, reduceMotion, look, blink } = useDotEyeMotion(followCursor);

  const bodyAnimation = reduceMotion ? undefined : { rotate: [0, 15, 0] };
  const bodyTransition = {
    duration: 0.6,
    ease: "easeInOut",
    repeat: Infinity,
    repeatDelay: 2,
  } as const;

  return (
    <div
      ref={ref}
      role="img"
      aria-label={ariaLabel}
      style={{ width: size, height: size }}
      className={cn("relative", className)}
    >
      <motion.svg
        viewBox="0 0 100 100"
        animate={bodyAnimation}
        transition={bodyTransition}
        aria-hidden="true"
        className="fill-all-clear-mascot stroke-all-clear-mascot size-full origin-center"
      >
        <circle cx="50" cy="50" r="32" />
        <circle cx="80.0" cy="50.0" r="11" />
        <circle cx="76.0" cy="65.0" r="11" />
        <circle cx="65.0" cy="76.0" r="11" />
        <circle cx="50.0" cy="80.0" r="11" />
        <circle cx="35.0" cy="76.0" r="11" />
        <circle cx="24.0" cy="65.0" r="11" />
        <circle cx="20.0" cy="50.0" r="11" />
        <circle cx="24.0" cy="35.0" r="11" />
        <circle cx="35.0" cy="24.0" r="11" />
        <circle cx="50.0" cy="20.0" r="11" />
        <circle cx="65.0" cy="24.0" r="11" />
        <circle cx="76.0" cy="35.0" r="11" />
      </motion.svg>
      <div
        className="absolute top-1/2 left-1/2 flex size-24 items-center justify-center"
        style={{ transform: `translate(-50%, -50%) scale(${String(size / DOT_BASE_SIZE)})` }}
      >
        <motion.div
          className="flex items-center gap-2"
          animate={{ x: look.x * 6, y: look.y * 5, scaleY: blink ? 0.1 : 1 }}
          transition={{ ...DOT_EYE_SPRING, scaleY: { duration: 0.1 } }}
        >
          <svg
            viewBox="0 0 10 10"
            aria-hidden="true"
            className="stroke-assistant-mascot-eye size-3 fill-none"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M2 7L5 3L8 7" />
          </svg>
          <svg
            viewBox="0 0 10 10"
            aria-hidden="true"
            className="stroke-assistant-mascot-eye size-3 fill-none"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M2 7L5 3L8 7" />
          </svg>
        </motion.div>
      </div>
    </div>
  );
}
