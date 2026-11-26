import { lazy, Suspense } from "react";
import type * as React from "react";
import { Outlet, useLocation, useOutlet } from "react-router";
import { AnimatePresence, motion } from "motion/react";
import { SidebarInset, SidebarProvider } from "@/shared/components/ui/sidebar";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { YearningSidebar } from "@/app/shell/yearning-sidebar";
import { AppHeader } from "@/app/shell/app-header";
import { AppFooter } from "@/app/shell/app-footer";
import { AssistantWidget } from "@/features/assistant/assistant-widget";
import { GlobalEventBridge } from "@/shared/events/sse-event-bus";

// Statically dead-code-eliminated in release builds (VITE_DEMO_MODE !== "true")
// so the demo banner and mock identity store never enter dist/.
const DemoBanner =
  import.meta.env.VITE_DEMO_MODE === "true"
    ? lazy(() =>
        import("@/app/shell/demo-banner").then((module) => ({
          default: module.DemoBanner,
        })),
      )
    : null;

const DevViewAsToolbar =
  import.meta.env.DEV && import.meta.env.MODE !== "test"
    ? lazy(() =>
        import("@/app/shell/dev-view-as-toolbar").then((module) => ({
          default: module.DevViewAsToolbar,
        })),
      )
    : null;

/**
 * Logged-in shell, independently written against the frozen template's
 * FullLayout structure: inset sidebar, sticky header, centered content
 * container with the page outlet, and the single global footer. The template
 * marketing widgets (upgrade card, external help links, demo banner content)
 * are excluded; the announcement banner joins when a real announcements
 * source is wired.
 */
export function FullLayout() {
  return (
    <SidebarProvider
      defaultOpen={true}
      style={{ "--sidebar-width-icon": "52px" } as React.CSSProperties}
    >
      <GlobalEventBridge />
      <YearningSidebar />
      {/* Owner ruling 2026-09-02: the nav stays put and only the body scrolls —
          the inset is capped to the viewport and the content column scrolls
          internally, so the sidebar and the nav can never drift out of
          alignment.
          Owner ruling 2026-10-23: the shell is flush with the viewport — the
          floating canvas (8px margin, rounded corners, hairline outline) is
          gone, so the header bar and the sidebar meet the page edges. */}
      <SidebarInset className="bg-background h-svh overflow-hidden md:peer-data-[variant=inset]:m-0!">
        {DevViewAsToolbar !== null ? (
          <Suspense fallback={null}>
            <DevViewAsToolbar />
          </Suspense>
        ) : null}
        {DemoBanner !== null ? (
          <Suspense fallback={null}>
            <DemoBanner />
          </Suspense>
        ) : null}
        <AppHeader />
        {/* Fill chain (owner ruling 2026-10-20): the column below the nav is
            a definite-height scroll container, so min-h-full resolves exactly
            and every page can stretch its content to the footer — the old
            min-h-[calc(100vh-140px)] approximation left dead space on tall
            windows and floated the footer up on short ones.
            Scrollbar (owner feedback 2026-11-03: do not expose native scrollbar on the right ->
            unify all scrollbars with the ScrollArea component): the shell column scrolls
            inside a ScrollArea overlay thumb instead of a native bar. */}
        <ScrollArea className="min-h-0 flex-1 p-4">
          {/* One measure for the whole shell (owner report 2026-10-23: the
              banner used to be wider than every row below it on wide screens).
              The header card uses the same 1536px measure, so both edges line
              up from 1440 through ultrawide instead of the body being capped
              while the bar kept spanning the full inset. */}
          <div className="mx-auto flex min-h-full w-full max-w-[1536px] flex-col pb-24">
            {/* Page-switch motion (owner ruling 2026-10-24): every route
             * change — including the settings tab navigation, whose five tabs
             * are route-level — runs a short rise+fade through a keyed
             * AnimatePresence over the outlet. mode="wait" keeps exactly one
             * page mounted at a time; only opacity/transform move and the
             * exit is quicker than the enter. */}
            <RouteTransition>
              <Outlet />
            </RouteTransition>
            <AppFooter />
          </div>
        </ScrollArea>
        <AssistantWidget />
      </SidebarInset>
    </SidebarProvider>
  );
}

/**
 * Keyed route-transition wrapper (owner ruling 2026-10-24). The outlet is
 * captured per location so AnimatePresence can keep the leaving page
 * rendered while it fades out; the wrapper preserves the shell's flex fill
 * chain (page stretch down to the footer).
 */
function RouteTransition({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  const capturedOutlet = useOutlet();
  // The settings area and the dual-tab approvals workspace own static tab bars:
  // their sibling routes share one transition key so the shell never re-animates
  // the header/tab bar between them — the pages animate their own content below
  // the header instead.
  const isApprovalQueueTab =
    location.pathname === "/approvals/changes" ||
    location.pathname === "/approvals/query-access";
  const transitionKey = location.pathname.startsWith("/admin/settings")
    ? "/admin/settings"
    : isApprovalQueueTab
      ? "/approvals-queue"
      : location.pathname;
  return (
    <AnimatePresence mode="wait" initial={false}>
      <motion.div
        key={transitionKey}
        initial={{ opacity: 0, y: 10 }}
        animate={{ opacity: 1, y: 0 }}
        exit={{ opacity: 0, y: -6 }}
        transition={{ duration: 0.16, ease: "easeOut" }}
        className="flex flex-1 flex-col"
      >
        {capturedOutlet ?? children}
      </motion.div>
    </AnimatePresence>
  );
}
