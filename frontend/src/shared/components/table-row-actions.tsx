import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { MoreHorizontal } from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/shared/components/ui/dropdown-menu";

/**
 * Trailing row-action kebab — the table baseline of spec §203 (consolidate inline row actions
 * into a trailing `⋮` menu), now realized per the 2026-10-24 reference ruling: one ⋯
 * glyph per row instead of a strip of labeled buttons. Pages keep the exact
 * same handlers and testids — they move onto the menu items, so only the
 * affordance collapses.
 */
export function TableRowActions({
  label,
  testId,
  children,
}: {
  /** Accessible name of the ⋯ trigger; defaults to the actions column title. */
  label?: string;
  testId?: string;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex justify-end">
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={label ?? t("common.actions")}
              data-testid={testId}
            >
              <MoreHorizontal aria-hidden />
            </Button>
          }
        />
        {/* w-40 beats the anchor-width default so labels like Test Connection never
            wrap; items themselves stay text-xs from the menu primitive. */}
        <DropdownMenuContent align="end" className="w-44">
          {children}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
