"use client";

import {
  useLayoutEffect,
  useRef,
  type HTMLAttributes,
  type ReactNode,
} from "react";
import type { MotionProps, Transition, Variant } from "motion/react";

import { usePrefersReducedMotion } from "@/shared/lib/use-prefers-reduced-motion";
import { cn } from "@/shared/lib/utils";

/** The tabs-02 enter/center/exit trio, parameterized for reuse. */
export const TAB_PANEL_VARIANTS = {
  enter: { opacity: 0.45, y: 8 },
  center: { opacity: 1, y: 0 },
  exit: { opacity: 0.45, y: -8 },
} satisfies { enter: Variant; center: Variant; exit: Variant };

const DEFAULT_DURATION_MS = 200;
const INTERRUPTED_DURATION_MS = 150;
const TAB_EASING = "cubic-bezier(0.23, 1, 0.32, 1)";

type TransitionPanelProps = {
  children: ReactNode[];
  className?: string;
  panelClassName?: string;
  transition?: Transition;
  activeIndex: number;
  variants?: { enter: Variant; center: Variant; exit: Variant };
  keepMounted?: boolean;
} & MotionProps &
  Omit<HTMLAttributes<HTMLDivElement>, keyof MotionProps | "children">;

/**
 * Animated panel switcher for controlled tab surfaces (tabs-02 reference
 * block, owner ruling 2026-10-24): on tab switch, the active panel rises and
 * fades in with a synchronous pre-paint WAAPI animation using compositor-only
 * opacity + transform (motion discipline rules 1/2/3/4/5).
 */
export function TransitionPanel({
  children,
  className,
  panelClassName,
  transition,
  activeIndex,
  keepMounted = false,
}: TransitionPanelProps) {
  const reducedMotion = usePrefersReducedMotion();
  const panelRef = useRef<HTMLDivElement | null>(null);
  const prevIndexRef = useRef(activeIndex);
  const animationRef = useRef<Animation | null>(null);

  const durationMs =
    typeof transition?.duration === "number"
      ? Math.min(240, Math.round(transition.duration * 1000))
      : DEFAULT_DURATION_MS;

  useLayoutEffect(() => {
    if (prevIndexRef.current === activeIndex) {
      return;
    }
    prevIndexRef.current = activeIndex;

    const wasRunning = animationRef.current?.playState === "running";
    animationRef.current?.cancel();
    animationRef.current = null;

    const node = panelRef.current;
    if (reducedMotion || node === null || typeof node.animate !== "function") {
      return;
    }

    const startOpacity = wasRunning ? 0.72 : 0.45;
    const startOffsetPx = wasRunning ? 5 : 8;
    const effectiveDuration = wasRunning
      ? Math.min(durationMs, INTERRUPTED_DURATION_MS)
      : durationMs;

    animationRef.current = node.animate(
      [
        { opacity: startOpacity, transform: `translateY(${String(startOffsetPx)}px)` },
        { opacity: 1, transform: "translateY(0px)" },
      ],
      {
        duration: effectiveDuration,
        easing: TAB_EASING,
      },
    );
  }, [activeIndex, durationMs, reducedMotion]);

  useLayoutEffect(() => {
    return () => {
      animationRef.current?.cancel();
    };
  }, []);

  return (
    <div className={cn("relative", className)}>
      <div ref={panelRef} className={panelClassName}>
        {keepMounted
          ? children.map((child, index) => (
              <div key={index} hidden={index !== activeIndex}>
                {child}
              </div>
            ))
          : children[activeIndex]}
      </div>
    </div>
  );
}
