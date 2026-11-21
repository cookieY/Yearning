import { useEffect, useRef, useState } from "react";
import { useReducedMotion } from "motion/react";

export const DOT_EYE_SPRING = { type: "spring", stiffness: 220, damping: 18 } as const;
export const DOT_BASE_SIZE = 96;

function randomBetween(min: number, max: number): number {
  return Math.random() * (max - min) + min;
}

/**
 * Shared idle glance, pointer-tracking, and blink state for Shadcn Dashboard
 * mascot dots (`dots-02` and `dots-03`), honoring `useReducedMotion()`.
 */
export function useDotEyeMotion(followCursor: boolean) {
  const reduceMotion = useReducedMotion();
  const ref = useRef<HTMLDivElement>(null);
  const [look, setLook] = useState({ x: 0, y: 0 });
  const [blink, setBlink] = useState(false);

  useEffect(() => {
    if (reduceMotion || followCursor) return;
    let timer: ReturnType<typeof setTimeout>;
    const glance = () => {
      const center = Math.random() < 0.25;
      setLook(
        center
          ? { x: 0, y: 0 }
          : { x: randomBetween(-1, 1), y: randomBetween(-0.7, 0.7) },
      );
      timer = setTimeout(glance, randomBetween(1200, 2800));
    };
    timer = setTimeout(glance, 800);
    return () => {
      clearTimeout(timer);
    };
  }, [reduceMotion, followCursor]);

  useEffect(() => {
    if (reduceMotion || !followCursor) return;
    const onMove = (event: PointerEvent) => {
      const rect = ref.current?.getBoundingClientRect();
      if (!rect) return;
      const dx = event.clientX - (rect.left + rect.width / 2);
      const dy = event.clientY - (rect.top + rect.height / 2);
      const distance = Math.hypot(dx, dy) || 1;
      const strength = Math.min(distance / 160, 1);
      setLook({ x: (dx / distance) * strength, y: (dy / distance) * strength });
    };
    window.addEventListener("pointermove", onMove);
    return () => {
      window.removeEventListener("pointermove", onMove);
    };
  }, [reduceMotion, followCursor]);

  useEffect(() => {
    if (reduceMotion) return;
    let timer: ReturnType<typeof setTimeout>;
    const schedule = () => {
      timer = setTimeout(() => {
        setBlink(true);
        timer = setTimeout(() => {
          setBlink(false);
          schedule();
        }, 140);
      }, randomBetween(2000, 5000));
    };
    schedule();
    return () => {
      clearTimeout(timer);
    };
  }, [reduceMotion]);

  return { ref, reduceMotion, look, blink };
}
