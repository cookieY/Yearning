"use client";

import {
  useRef,
  useState,
  type CSSProperties,
  type MouseEvent,
  type ReactNode,
} from "react";

import { cn } from "@/shared/lib/utils";

export type ShineBorderProps = {
  children: ReactNode;
  className?: string;
  /** Ring thickness in px (the spotlight gradient shows through it). */
  borderWidth?: number;
  /** Optional opacity transition duration override in milliseconds. */
  duration?: number;
  /** Semantic gradient utility classes for the mouse-following spotlight. */
  gradient?: string;
};

/**
 * Mouse-spotlight border (`shine-border-03` block, owner ruling 2026-10-24 &
 * 2026-11-11): a multi-stop horizontal gradient sits beneath the card surface
 * and is revealed only through a localized cursor-tracking radial mask, while
 * an unhovered fallback keeps a crisp 1px flat border at rest.
 */
export function ShineBorder({
  children,
  className,
  borderWidth = 2,
  duration,
  gradient = "from-shine-from via-shine-via to-shine-to",
}: ShineBorderProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({ x: 0, y: 0 });
  const [opacity, setOpacity] = useState(0);

  const handleMouseMove = (event: MouseEvent<HTMLDivElement>) => {
    const rect = containerRef.current?.getBoundingClientRect();
    if (rect === undefined) return;
    setPosition({
      x: event.clientX - rect.left,
      y: event.clientY - rect.top,
    });
  };

  const spotlightMask = `radial-gradient(400px circle at ${String(position.x)}px ${String(position.y)}px, black 10%, transparent 60%)`;

  return (
    <div
      ref={containerRef}
      data-slot="shine-border"
      onMouseMove={handleMouseMove}
      onMouseEnter={() => {
        setOpacity(1);
      }}
      onMouseLeave={() => {
        setOpacity(0);
      }}
      className={cn(
        "bg-card relative overflow-hidden rounded-2xl p-(--bw)",
        className,
      )}
      style={{ "--bw": `${String(borderWidth)}px` } as CSSProperties}
    >
      {/* Default flat hairline border fallback when not hovered */}
      <div
        aria-hidden
        className="border-border pointer-events-none absolute inset-0 rounded-[inherit] border"
      />

      {/* Mouse-following spotlight ring */}
      <div
        aria-hidden
        data-slot="shine-border-spotlight"
        className={cn(
          "pointer-events-none absolute inset-0 rounded-[inherit] bg-linear-to-r transition-opacity duration-300",
          gradient,
        )}
        style={{
          opacity,
          ...(duration !== undefined
            ? { transitionDuration: `${String(duration)}ms` }
            : {}),
          WebkitMaskImage: spotlightMask,
          maskImage: spotlightMask,
        }}
      />

      {/* Content Layer */}
      <div className="bg-card relative z-10 h-full rounded-[calc(var(--radius-2xl)-var(--bw))]">
        {children}
      </div>
    </div>
  );
}
