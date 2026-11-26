import { useLayoutEffect, type RefObject } from "react";

export interface PortalAnchorOptions {
  /** Match the trigger element's bounding width (default true). */
  matchWidth?: boolean;
  /** Minimum width fallback when trigger rect is unavailable or matchWidth is false. */
  fallbackWidth?: number;
  /** Desired height used to decide upward flip when bottom room is tight (default 240). */
  preferredHeight?: number;
  /** Gap in pixels between trigger and popup (default 4). */
  gap?: number;
}

/**
 * Positions a `fixed` portaled popup element synchronously before paint relative
 * to `triggerRef`, flipping above the trigger when viewport bottom room is tight
 * and tracking capture-phase scroll and resize events while open.
 */
export function usePortalAnchor(
  activeKey: string | boolean,
  triggerRef: RefObject<HTMLElement | null>,
  popupRef: RefObject<HTMLElement | null>,
  options: PortalAnchorOptions = {},
): void {
  const {
    matchWidth = true,
    fallbackWidth = 280,
    preferredHeight = 240,
    gap = 4,
  } = options;

  useLayoutEffect(() => {
    if (!activeKey) return;

    const updatePosition = () => {
      const trigger = triggerRef.current;
      const popup = popupRef.current;
      if (trigger === null || popup === null) return;

      const rect = trigger.getBoundingClientRect();
      const vpW = window.innerWidth || 1024;
      const vpH = window.innerHeight || 768;
      const margin = 8;
      const targetWidth =
        matchWidth && rect.width > 0 ? rect.width : Math.max(fallbackWidth, rect.width);

      const spaceBelow = Math.max(0, vpH - rect.bottom - margin);
      const spaceAbove = Math.max(0, rect.top - margin);
      const flipUp =
        rect.height > 0 && spaceBelow < preferredHeight && spaceAbove > spaceBelow;

      const maxLeft = Math.max(margin, vpW - targetWidth - margin);
      const left = Math.max(margin, Math.min(rect.left, maxLeft));

      popup.style.position = "fixed";
      popup.style.left = `${String(Math.round(left))}px`;
      if (matchWidth && rect.width > 0) {
        popup.style.width = `${String(Math.round(rect.width))}px`;
      } else {
        popup.style.removeProperty("width");
      }

      if (flipUp) {
        const bottomOffset = Math.max(margin, vpH - rect.top + gap);
        popup.style.removeProperty("top");
        popup.style.bottom = `${String(Math.round(bottomOffset))}px`;
        popup.style.maxHeight = `${String(Math.max(120, Math.round(spaceAbove - gap)))}px`;
      } else {
        const top =
          rect.height > 0
            ? Math.min(vpH - margin - 40, Math.max(margin, rect.bottom + gap))
            : gap;
        popup.style.removeProperty("bottom");
        popup.style.top = `${String(Math.round(top))}px`;
        popup.style.maxHeight = `${String(Math.max(120, Math.round(vpH - top - margin)))}px`;
      }
    };

    updatePosition();
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
    };
  }, [activeKey, fallbackWidth, gap, matchWidth, popupRef, preferredHeight, triggerRef]);
}
