import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { useTranslation } from "react-i18next";
import { Dot } from "@/shared/components/dots-02";
import { useIsMobile } from "@/shared/hooks/use-mobile";
import { cn } from "@/shared/lib/utils";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/shared/components/ui/popover";

const AssistantPanel = lazy(() =>
  import("./assistant-panel").then((module) => ({
    default: module.AssistantPanel,
  })),
);

const AssistantWidgetMobile = lazy(() =>
  import("./assistant-widget-mobile").then((module) => ({
    default: module.AssistantWidgetMobile,
  })),
);

const DRAG_THRESHOLD_PX = 6;
const VIEWPORT_EDGE_PADDING_PX = 8;

interface DragOffset {
  x: number;
  y: number;
}

interface ActiveDragState {
  pointerId: number;
  startX: number;
  startY: number;
  originX: number;
  originY: number;
  moved: boolean;
}

function clampDragOffset(offset: DragOffset, isMobile: boolean): DragOffset {
  if (typeof window === "undefined") {
    return offset;
  }
  const size = isMobile ? 84 : 120;
  const anchorMargin = isMobile ? 16 : 20;
  const vw = window.innerWidth || 1024;
  const vh = window.innerHeight || 768;

  const minX = Math.min(0, -(vw - size - anchorMargin - VIEWPORT_EDGE_PADDING_PX));
  const maxX = Math.max(0, anchorMargin - VIEWPORT_EDGE_PADDING_PX);
  const minY = Math.min(0, -(vh - size - anchorMargin - VIEWPORT_EDGE_PADDING_PX));
  const maxY = Math.max(0, anchorMargin - VIEWPORT_EDGE_PADDING_PX);

  return {
    x: Math.min(maxX, Math.max(minX, Math.round(offset.x))),
    y: Math.min(maxY, Math.max(minY, Math.round(offset.y))),
  };
}

export function AssistantWidget() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [open, setOpen] = useState(false);
  const [isExpanded, setIsExpanded] = useState(false);
  const [offset, setOffset] = useState<DragOffset>({ x: 0, y: 0 });
  const [isDragging, setIsDragging] = useState(false);
  const dragStateRef = useRef<ActiveDragState | null>(null);
  const suppressClickRef = useRef(false);

  useEffect(() => {
    const preload = (): void => {
      void import("./assistant-panel");
    };
    if (typeof window.requestIdleCallback === "function") {
      const handle = window.requestIdleCallback(preload, { timeout: 2000 });
      return () => {
        window.cancelIdleCallback(handle);
      };
    }
    const timer = window.setTimeout(preload, 600);
    return () => {
      window.clearTimeout(timer);
    };
  }, []);

  useEffect(() => {
    const handleResize = (): void => {
      setOffset((prev) => clampDragOffset(prev, isMobile));
    };
    window.addEventListener("resize", handleResize);
    return () => {
      window.removeEventListener("resize", handleResize);
    };
  }, [isMobile]);

  const handleOpenChange = (nextOpen: boolean) => {
    if (suppressClickRef.current) {
      suppressClickRef.current = false;
      return;
    }
    setOpen(nextOpen);
  };

  const handleToggleExpand = () => {
    setIsExpanded((prev) => !prev);
  };

  const handlePointerDown = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.pointerType === "mouse" && event.button !== 0) {
      return;
    }
    suppressClickRef.current = false;
    dragStateRef.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      originX: offset.x,
      originY: offset.y,
      moved: false,
    };
    if (typeof event.currentTarget.setPointerCapture === "function") {
      try {
        event.currentTarget.setPointerCapture(event.pointerId);
      } catch {
        // Ignore pointer capture errors in synthetic test environments.
      }
    }
  };

  const handlePointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const state = dragStateRef.current;
    if (!state || state.pointerId !== event.pointerId) {
      return;
    }
    const dx = event.clientX - state.startX;
    const dy = event.clientY - state.startY;
    if (!state.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD_PX) {
      return;
    }
    if (!state.moved) {
      state.moved = true;
      suppressClickRef.current = true;
      setIsDragging(true);
    }
    setOffset(
      clampDragOffset(
        {
          x: state.originX + dx,
          y: state.originY + dy,
        },
        isMobile,
      ),
    );
  };

  const finishPointerDrag = (event: ReactPointerEvent<HTMLButtonElement>) => {
    const state = dragStateRef.current;
    if (!state || state.pointerId !== event.pointerId) {
      return;
    }
    if (typeof event.currentTarget.releasePointerCapture === "function") {
      try {
        event.currentTarget.releasePointerCapture(event.pointerId);
      } catch {
        // Ignore pointer capture release errors in synthetic test environments.
      }
    }
    dragStateRef.current = null;
    if (state.moved) {
      setIsDragging(false);
    }
  };

  const handleClickCapture = (event: ReactMouseEvent<HTMLButtonElement>) => {
    if (suppressClickRef.current) {
      suppressClickRef.current = false;
      event.preventDefault();
      event.stopPropagation();
    }
  };

  const floatingStyle =
    offset.x !== 0 || offset.y !== 0
      ? { transform: `translate3d(${String(offset.x)}px, ${String(offset.y)}px, 0)` }
      : undefined;

  const triggerButton = (
    <button
      type="button"
      aria-label={t("assistant.triggerLabel")}
      data-testid="database-assistant-trigger"
      data-dragging={isDragging ? "true" : "false"}
      onPointerEnter={() => {
        void import("./assistant-panel");
      }}
      onFocus={() => {
        void import("./assistant-panel");
      }}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={finishPointerDrag}
      onPointerCancel={finishPointerDrag}
      onClickCapture={handleClickCapture}
      className={cn(
        "focus-visible:ring-ring touch-none rounded-2xl drop-shadow-md transition-transform duration-150 ease-out outline-none select-none focus-visible:ring-2 focus-visible:ring-offset-2",
        isDragging
          ? "cursor-grabbing"
          : "cursor-grab active:scale-[0.97]",
      )}
    >
      <Dot
        size={isMobile ? 84 : 120}
        followCursor={!isMobile && !isDragging}
        aria-label={t("assistant.triggerLabel")}
        className="pointer-events-none"
      />
    </button>
  );

  if (isMobile) {
    return (
      <Suspense fallback={null}>
        <AssistantWidgetMobile
          open={open}
          isExpanded={isExpanded}
          onOpenChange={handleOpenChange}
          onToggleExpand={handleToggleExpand}
          triggerButton={triggerButton}
          style={floatingStyle}
        />
      </Suspense>
    );
  }

  return (
    <div
      className="fixed right-5 bottom-5 z-40"
      style={floatingStyle}
      data-testid="database-assistant-widget-desktop"
    >
      <Popover open={open} onOpenChange={handleOpenChange}>
        <PopoverTrigger render={triggerButton} />
        <PopoverContent
          align="end"
          sideOffset={12}
          className={cn(
            "origin-(--transform-origin) overflow-hidden rounded-xl p-0 shadow-xl transition-[opacity,scale,translate] duration-[240ms] ease-out data-open:animate-none data-closed:animate-none data-starting-style:translate-y-3 data-starting-style:scale-95 data-starting-style:opacity-0 data-ending-style:translate-y-2 data-ending-style:scale-95 data-ending-style:opacity-0 data-ending-style:duration-[160ms]",
            isExpanded
              ? "h-[calc(100svh-3rem)] max-h-[calc(100svh-3rem)] w-[960px] max-w-[calc(100vw-2rem)]"
              : "h-[min(720px,calc(100svh-3.5rem))] max-h-[calc(100svh-3.5rem)] w-[540px] max-w-[calc(100vw-2rem)]",
          )}
          data-testid="database-assistant-desktop-popover"
          data-expanded={isExpanded ? "true" : "false"}
        >
          <Suspense fallback={null}>
            <AssistantPanel
              open={open}
              isExpanded={isExpanded}
              onToggleExpand={handleToggleExpand}
            />
          </Suspense>
        </PopoverContent>
      </Popover>
    </div>
  );
}
