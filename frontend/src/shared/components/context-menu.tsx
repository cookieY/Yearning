"use client";

import React, { useEffect } from "react";
import { AnimatePresence, motion } from "motion/react";
import type { LucideIcon } from "lucide-react";
import { Separator } from "@/shared/components/ui/separator";
import { Kbd } from "@/shared/components/ui/kbd";

/**
 * Animated context menu (shadcn-dashboard context-menu-01 block, owner
 * ruling 2026-10-31: use this block to polish the right-click "View Table Structure" menu in the query workspace).
 * Controlled variant of the registry demo: the trigger surface keeps its
 * own onContextMenu state and renders this menu at the pointer position —
 * fade+scale panel, per-item stagger and hover slide, icon chips, optional
 * Kbd shortcut (omitted when not bound — no fake shortcuts), Separator
 * before danger items. Esc and click-outside close; the panel is clamped
 * to the viewport.
 */

export interface ContextMenuItem {
  icon: LucideIcon;
  label: string;
  /** Optional keyboard hint — render only when a real binding exists. */
  shortcut?: string;
  danger?: boolean;
  testId?: string;
  onClick?: () => void;
}

export interface ContextMenuState {
  x: number;
  y: number;
}

export function ContextMenu({
  position,
  items,
  onClose,
  overlayTestId,
  panelTestId,
}: {
  position: ContextMenuState;
  items: ContextMenuItem[];
  onClose: () => void;
  overlayTestId?: string;
  panelTestId?: string;
}) {
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => { window.removeEventListener("keydown", onKey); };
  }, [onClose]);

  const width = 208; // w-52
  const left = Math.min(position.x, Math.max(window.innerWidth - width - 8, 8));
  const top = Math.min(position.y, Math.max(window.innerHeight - items.length * 44 - 24, 8));

  return (
    <AnimatePresence>
      <>
        <div
          className="fixed inset-0 z-40"
          onClick={onClose}
          onContextMenu={(event) => { event.preventDefault(); onClose(); }}
          data-testid={overlayTestId}
        />
        <motion.div
          initial={{ opacity: 0, scale: 0.95 }}
          animate={{ opacity: 1, scale: 1 }}
          exit={{ opacity: 0, scale: 0.95 }}
          transition={{ duration: 0.15 }}
          className="bg-popover text-popover-foreground fixed z-50 w-52 overflow-hidden rounded-2xl border border-border/80 p-1.5"
          style={{ left, top }}
          data-testid={panelTestId}
        >
          {items.map((item, index) => {
            const Icon = item.icon;
            return (
              <React.Fragment key={item.label}>
                {item.danger && index > 0 && <Separator className="my-1.5" />}
                <motion.button
                  type="button"
                  initial={{ opacity: 0, x: -10 }}
                  animate={{ opacity: 1, x: 0 }}
                  transition={{ delay: index * 0.05 }}
                  whileHover={{ x: 4 }}
                  className="text-foreground hover:bg-muted dark:hover:bg-muted/50 flex w-full cursor-pointer items-center justify-between rounded-xl px-2.5 py-2 text-left text-xs font-medium transition-colors"
                  onClick={() => {
                    item.onClick?.();
                    onClose();
                  }}
                  data-testid={item.testId}
                >
                  <div className="flex items-center gap-2.5">
                    <span className="flex h-7 w-7 items-center justify-center rounded-lg border">
                      <Icon className="size-3.5" aria-hidden />
                    </span>
                    {item.label}
                  </div>
                  {item.shortcut !== undefined && item.shortcut !== "" && (
                    <Kbd className="border-border bg-muted text-muted-foreground rounded-md border px-1.5 py-0.5 font-mono text-[10px] font-medium">
                      {item.shortcut}
                    </Kbd>
                  )}
                </motion.button>
              </React.Fragment>
            );
          })}
        </motion.div>
      </>
    </AnimatePresence>
  );
}
