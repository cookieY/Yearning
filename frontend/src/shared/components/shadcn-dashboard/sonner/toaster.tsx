"use client";

import { useEffect, type ReactNode } from "react";
import { CircleCheck, Info, Loader2, OctagonAlert, TriangleAlert } from "lucide-react";

import {
  Toast,
  ToastAction,
  ToastClose,
  ToastContent,
  ToastDescription,
  ToastPortal,
  ToastProvider,
  ToastTitle,
  ToastViewport,
  createToastManager,
  useToastManager,
} from "@/shared/components/ui/toast";
import { cn } from "@/shared/lib/utils";

/**
 * Shadcn Dashboard `sonner-08` block — toaster.tsx (vendored 2026-11-04,
 * owner ruling: represent order status using the Sonner 08 component). Only the import paths are
 * adapted to this repo's components.json aliases (components →
 * @/shared/components, lib/utils → @/shared/lib/utils).
 */

export type ToastPosition =
  | "top-left"
  | "top-center"
  | "top-right"
  | "bottom-left"
  | "bottom-center"
  | "bottom-right";

export type ToastData = {
  /** Replaces the default type icon. */
  icon?: ReactNode;
  /** Extra classes for the toast, such as a tinted border. */
  className?: string;
  /** Shows a close button. */
  closeButton?: boolean;
  /** Fully custom body, rendered instead of the title and description. */
  content?: ReactNode;
};

export type ToastManager = ReturnType<typeof createToastManager>;

export const DEFAULT_TOAST_TIMEOUT_MS = 4000;

export { createToastManager };

const viewportClass: Record<ToastPosition, string> = {
  "top-left": "top-[68px] bottom-auto sm:left-4 sm:right-auto",
  "top-center": "top-[68px] bottom-auto sm:inset-x-0 sm:mx-auto",
  "top-right": "top-[68px] bottom-auto",
  "bottom-left": "sm:left-4 sm:right-auto",
  "bottom-center": "sm:inset-x-0 sm:mx-auto",
  "bottom-right": "",
};

// The base toast stacks upward from the bottom. These overrides flip the stack for top positions.
const topToastClass = cn(
  "top-0 bottom-auto origin-top after:top-auto after:bottom-full",
  "[--offset-y:calc(var(--toast-offset-y)+(var(--toast-index)*var(--gap))+var(--toast-swipe-movement-y))]",
  "[transform:translateX(var(--toast-swipe-movement-x))_translateY(calc(var(--toast-swipe-movement-y)+(var(--toast-index)*var(--peek))+(var(--shrink)*var(--height))))_scale(var(--scale))]",
  "data-expanded:[transform:translateX(var(--toast-swipe-movement-x))_translateY(var(--offset-y))]",
  "data-starting-style:[transform:translateY(-150%)]",
  "data-ending-style:opacity-0 [&[data-ending-style]:not([data-limited]):not([data-swipe-direction])]:[transform:translateY(-150%)]",
);

const typeIcons: Record<string, ReactNode> = {
  success: <CircleCheck className="size-4 text-tone-teal" aria-hidden="true" />,
  info: <Info className="size-4 text-tone-sky" aria-hidden="true" />,
  warning: <TriangleAlert className="size-4 text-tone-amber" aria-hidden="true" />,
  error: <OctagonAlert className="size-4 text-destructive" aria-hidden="true" />,
  loading: <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden="true" />,
};

type ManagedToastItem = ReturnType<typeof useToastManager>["toasts"][number];

function ToastItem({
  item,
  top,
  onClose,
}: {
  item: ManagedToastItem;
  top: boolean;
  onClose: (toastId: string) => void;
}) {
  useEffect(() => {
    if (item.transitionStatus === "ending" || item.type === "loading") {
      return undefined;
    }
    const duration = item.timeout ?? DEFAULT_TOAST_TIMEOUT_MS;
    if (duration <= 0) {
      return undefined;
    }
    const timerId = window.setTimeout(() => {
      onClose(item.id);
    }, duration);
    return () => {
      window.clearTimeout(timerId);
    };
  }, [item.id, item.timeout, item.transitionStatus, item.type, onClose]);

  const data = (item.data ?? {}) as ToastData;
  const icon = data.icon ?? (item.type ? typeIcons[item.type] : null);

  return (
    <Toast
      toast={item}
      swipeDirection={top ? ["up", "right"] : ["down", "right"]}
      className={cn(top && topToastClass, data.className)}
    >
      <ToastContent className="items-start">
        {data.content ?? (
          <>
            {icon && <span className="mt-0.5 shrink-0">{icon}</span>}
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <ToastTitle />
              <ToastDescription />
            </div>
            <ToastAction />
            {data.closeButton && <ToastClose />}
          </>
        )}
      </ToastContent>
    </Toast>
  );
}

function ToastList({ top }: { top: boolean }) {
  const { toasts, close } = useToastManager();

  return toasts.map((item) => (
    <ToastItem key={item.id} item={item} top={top} onClose={close} />
  ));
}

// A toast stack with its own manager, so demos can pick a corner instead of the global bottom right Toaster.
export function PositionedToaster({
  manager,
  position = "top-right",
  timeout = DEFAULT_TOAST_TIMEOUT_MS,
  children,
}: {
  manager: ToastManager;
  position?: ToastPosition;
  timeout?: number;
  children?: ReactNode;
}) {
  return (
    <ToastProvider toastManager={manager} timeout={timeout}>
      {children}
      <ToastPortal>
        <ToastViewport className={viewportClass[position]}>
          <ToastList top={position.startsWith("top")} />
        </ToastViewport>
      </ToastPortal>
    </ToastProvider>
  );
}
