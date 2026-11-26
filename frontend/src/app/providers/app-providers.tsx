import { QueryClientProvider } from "@tanstack/react-query";
import { useMemo, type ReactNode } from "react";
import { PositionedToaster } from "@/shared/components/shadcn-dashboard/sonner/toaster";
import { appToastManager } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { TooltipProvider } from "@/shared/components/ui/tooltip";
import { createQueryClient } from "./query-provider";
import { ThemeProvider } from "./theme-provider";
import { SessionProvider } from "@/features/auth/session-provider";
import { ErrorBoundary } from "./error-boundary";
import "@/shared/i18n";

export function AppProviders({ children }: { children: ReactNode }) {
  const queryClient = useMemo(() => createQueryClient(), []);
  return (
    <ErrorBoundary>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          {/* The header is a floating card pinned to the viewport top and
              carries the global controls at its right edge. PositionedToaster
              places top-right toasts at top-[68px] (below the 4px margin + 48px
              control row + gap) so feedback never overlaps the header actions. */}
          <PositionedToaster manager={appToastManager} position="top-right">
            <TooltipProvider>
              <SessionProvider>{children}</SessionProvider>
            </TooltipProvider>
          </PositionedToaster>
        </QueryClientProvider>
      </ThemeProvider>
    </ErrorBoundary>
  );
}
