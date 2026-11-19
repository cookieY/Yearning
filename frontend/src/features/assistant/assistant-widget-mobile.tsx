import type { CSSProperties, ReactElement } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/shared/lib/utils";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/shared/components/ui/sheet";
import { AssistantPanel } from "./assistant-panel";

export interface AssistantWidgetMobileProps {
  open: boolean;
  isExpanded: boolean;
  onOpenChange: (nextOpen: boolean) => void;
  onToggleExpand: () => void;
  triggerButton: ReactElement;
  style?: CSSProperties;
}

export function AssistantWidgetMobile({
  open,
  isExpanded,
  onOpenChange,
  onToggleExpand,
  triggerButton,
  style,
}: AssistantWidgetMobileProps) {
  const { t } = useTranslation();

  return (
    <div
      className="fixed right-4 bottom-4 z-40"
      style={style}
      data-testid="database-assistant-widget-mobile"
    >
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetTrigger render={triggerButton} />
        <SheetContent
          side="bottom"
          className={cn(
            "rounded-t-2xl p-0 pb-[env(safe-area-inset-bottom)]",
            isExpanded ? "h-[96svh] max-h-[96svh]" : "h-[88svh] max-h-[88svh]",
          )}
          data-testid="database-assistant-mobile-sheet"
          data-expanded={isExpanded ? "true" : "false"}
        >
          <SheetHeader className="sr-only">
            <SheetTitle>{t("assistant.panelTitle")}</SheetTitle>
            <SheetDescription>{t("assistant.emptyDescription")}</SheetDescription>
          </SheetHeader>
          <AssistantPanel
            open={open}
            isExpanded={isExpanded}
            onToggleExpand={onToggleExpand}
          />
        </SheetContent>
      </Sheet>
    </div>
  );
}
