"use client";

import { BellRing, CircleCheck, Clock, Info, OctagonAlert, TriangleAlert, type LucideIcon } from "lucide-react";

import { Button } from "@/shared/components/ui/button";
import { cn } from "@/shared/lib/utils";
import {
  DEFAULT_TOAST_TIMEOUT_MS,
  PositionedToaster,
  createToastManager,
  type ToastData,
} from "./toaster";

/**
 * Shadcn Dashboard `sonner-08` block — demo (vendored 2026-11-04, owner
 * ruling: represent order status using the Sonner 08 component). Only the import paths are adapted
 * to this repo's components.json aliases. The block's tone grammar (icon in a
 * tinted circle + tinted border + tinted button) is the visual baseline the
 * order-state chips reuse (features/orders/order-state-badge.tsx).
 */

// A local manager keeps these toasts top right, clear of chat widgets in the bottom corner.
const toast = createToastManager();

type Tone = {
  icon: LucideIcon;
  iconColor: string;
  badge: string;
  border: string;
  button: string;
};

// Each type gets its own icon in a tinted circle, a matching toast border, and a matching button.
const tones = {
  default: {
    icon: BellRing,
    iconColor: "text-muted-foreground",
    badge: "bg-muted",
    border: "",
    button: "",
  },
  success: {
    icon: CircleCheck,
    iconColor: "text-tone-teal",
    badge: "bg-tone-teal/15",
    border: "border-tone-teal/30",
    button: "border-tone-teal/30 bg-tone-teal/10 hover:bg-tone-teal/20",
  },
  info: {
    icon: Info,
    iconColor: "text-tone-sky",
    badge: "bg-tone-sky/15",
    border: "border-tone-sky/30",
    button: "border-tone-sky/30 bg-tone-sky/10 hover:bg-tone-sky/20",
  },
  warning: {
    icon: TriangleAlert,
    iconColor: "text-tone-amber",
    badge: "bg-tone-amber/20",
    border: "border-tone-amber/40",
    button: "border-tone-amber/40 bg-tone-amber/15 hover:bg-tone-amber/25",
  },
  error: {
    icon: OctagonAlert,
    iconColor: "text-destructive",
    badge: "bg-destructive/10",
    border: "border-destructive/30",
    button: "border-destructive/30 bg-destructive/10 hover:bg-destructive/15",
  },
  reminder: {
    icon: Clock,
    iconColor: "text-tone-sky",
    badge: "bg-tone-sky/15",
    border: "border-tone-sky/30",
    button: "border-tone-sky/30 bg-tone-sky/10 hover:bg-tone-sky/20",
  },
} satisfies Record<string, Tone>;

type ToneName = keyof typeof tones;

const toneData = (name: ToneName, extra?: Partial<ToastData>): ToastData => {
  const tone: Tone = tones[name];
  return {
    icon: (
      <span className={cn("flex size-7 items-center justify-center rounded-full", tone.badge)}>
        <tone.icon className={cn("size-4", tone.iconColor)} aria-hidden="true" />
      </span>
    ),
    className: tone.border,
    ...extra,
  };
};

export const appToastManager = toast;
export { toneData };

export type AppToastOptions = {
  description?: string;
  closeButton?: boolean;
  timeout?: number;
};

export const appToast = {
  add: toast.add.bind(toast),
  default: (title: string, options?: AppToastOptions | string) => {
    const resolved = typeof options === "string" ? { description: options } : options;
    return toast.add({
      title,
      description: resolved?.description,
      timeout: resolved?.timeout ?? DEFAULT_TOAST_TIMEOUT_MS,
      data: toneData("default", { closeButton: resolved?.closeButton ?? true }),
    });
  },
  info: (title: string, options?: AppToastOptions | string) => {
    const resolved = typeof options === "string" ? { description: options } : options;
    return toast.add({
      type: "info",
      title,
      description: resolved?.description,
      timeout: resolved?.timeout ?? DEFAULT_TOAST_TIMEOUT_MS,
      data: toneData("info", { closeButton: resolved?.closeButton ?? true }),
    });
  },
  success: (title: string, options?: AppToastOptions | string) => {
    const resolved = typeof options === "string" ? { description: options } : options;
    return toast.add({
      type: "success",
      title,
      description: resolved?.description,
      timeout: resolved?.timeout ?? DEFAULT_TOAST_TIMEOUT_MS,
      data: toneData("success", { closeButton: resolved?.closeButton ?? true }),
    });
  },
  warning: (title: string, options?: AppToastOptions | string) => {
    const resolved = typeof options === "string" ? { description: options } : options;
    return toast.add({
      type: "warning",
      title,
      description: resolved?.description,
      timeout: resolved?.timeout ?? DEFAULT_TOAST_TIMEOUT_MS,
      data: toneData("warning", { closeButton: resolved?.closeButton ?? true }),
    });
  },
  error: (title: string, options?: AppToastOptions | string) => {
    const resolved = typeof options === "string" ? { description: options } : options;
    return toast.add({
      type: "error",
      title,
      description: resolved?.description,
      timeout: resolved?.timeout ?? DEFAULT_TOAST_TIMEOUT_MS,
      data: toneData("error", { closeButton: resolved?.closeButton ?? true }),
    });
  },
};

const variants: { label: string; tone: ToneName; show: () => void }[] = [
  {
    label: "Default",
    tone: "default",
    show: () => toast.add({ title: "Event created", description: "Friday, Nov 7 at 10:00 AM", data: toneData("default") }),
  },
  {
    label: "Success",
    tone: "success",
    show: () => toast.add({ type: "success", title: "Changes saved", description: "Your profile is up to date.", data: toneData("success") }),
  },
  {
    label: "Info",
    tone: "info",
    show: () => toast.add({ type: "info", title: "New version available", description: "Refresh to get v2.4.0.", data: toneData("info") }),
  },
  {
    label: "Warning",
    tone: "warning",
    show: () => toast.add({ type: "warning", title: "Storage almost full", description: "You have used 92% of 10 GB.", data: toneData("warning") }),
  },
  {
    label: "Error",
    tone: "error",
    show: () => toast.add({ type: "error", title: "Payment failed", description: "Your card was declined.", data: toneData("error") }),
  },
  {
    label: "With close button",
    tone: "reminder",
    show: () =>
      toast.add({
        title: "Session expires soon",
        description: "You will be signed out in 5 minutes.",
        timeout: 8000,
        data: toneData("reminder", { closeButton: true }),
      }),
  },
];

const SonnerDemo = () => {
  return (
    <PositionedToaster manager={toast} position="top-right">
      <div className="flex max-w-md flex-wrap justify-center gap-2">
        {variants.map((variant) => {
          const tone: Tone = tones[variant.tone];
          return (
            <Button
              key={variant.label}
              variant="outline"
              onClick={variant.show}
              className={cn("text-foreground", tone.button)}
            >
              <tone.icon className={tone.iconColor} aria-hidden="true" />
              {variant.label}
            </Button>
          );
        })}
      </div>
    </PositionedToaster>
  );
};

export default SonnerDemo;
