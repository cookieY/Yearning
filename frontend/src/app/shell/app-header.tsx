import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ArrowRight, Search, Tags } from "lucide-react";
import { SidebarTrigger } from "@/shared/components/ui/sidebar";
import { Button } from "@/shared/components/ui/button";
import { Spinner } from "@/shared/components/ui/spinner";
import { useLatestReleaseQuery } from "@/features/releases/use-latest-release";
import { BrandLogo } from "./brand-logo";
import { ThemeToggle } from "./theme-toggle";
import { LanguageToggle } from "./language-toggle";
import { UserMenu } from "./user-menu";

const CommandMenu = lazy(() => import("./command-menu").then((module) => ({ default: module.CommandMenu })));

/**
 * Shell header. Owner ruling 2026-10-23: the bar is a separate surface from
 * the page content — its own border, radius and inset — instead of a
 * full-bleed strip glued to the content by a bottom border, and it carries
 * the command-menu trigger (the reference template's menu search, which
 * spec §5 had excluded until this ruling).
 */
export function AppHeader() {
  const { t } = useTranslation();
  const [commandOpen, setCommandOpen] = useState(false);
  const [commandRequested, setCommandRequested] = useState(false);
  const commandReturnFocus = useRef<HTMLElement | null>(null);
  // Defer the popup code until first use, then retain its mounted dialog so
  // close transitions and focus restoration stay owned by Base UI.
  const openCommandMenu = (trigger: HTMLElement) => {
    commandReturnFocus.current = trigger;
    setCommandRequested(true);
    setCommandOpen(true);
  };

  // Release status lives in the bar, left of the theme toggle (owner request
  // 2026-10-23), so the workspace pages no longer repeat a version row of
  // their own. The query is shared with any page that asks for the same keys.
  const release = useLatestReleaseQuery(true);
  const releaseSnapshot = release.data ?? null;
  const currentVersion = releaseSnapshot?.current_version ?? null;
  const hasUpdate =
    currentVersion !== null && currentVersion !== releaseSnapshot?.tag_name;

  // ⌘K / Ctrl+K anywhere in the shell toggles the menu; the listener ignores
  // repeats so a held key cannot strobe the dialog open and closed.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() !== "k" || !(event.metaKey || event.ctrlKey)) return;
      event.preventDefault();
      if (event.repeat) return;
      if (!commandOpen) commandReturnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      setCommandRequested(true);
      setCommandOpen((open) => !open);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [commandOpen]);

  return (
    <header className="bg-card relative mx-auto mt-1 mb-0 w-[calc(100%-2rem)] max-w-[1536px] rounded-xl border border-border">
      {/* Floating card (restored 2026-10-23): the shell canvas is flush, the
          bar itself stays a rounded card. `calc(100%-2rem)` + the shared
          1536px cap keep it on the same band as the body column below, so the
          two still share both edges at every width. */}
      <nav
        aria-label={t("shell.headerNav")}
        className="flex items-center gap-2 px-3 py-2 md:h-12 md:py-0"
      >
        <div className="flex items-center gap-2">
          {/* §18.59: align breakpoint with sidebar (ui/sidebar displays at md:flex) — previously lg:hidden
              double-rendered with the sidebar logo between 768~1024px; changing to md:hidden makes them strictly complementary. */}
          <div className="shrink-0 md:hidden">
            <BrandLogo className="max-w-24" />
          </div>
          <SidebarTrigger
            className="btn-circle-hover"
            aria-label={t("shell.toggleSidebar")}
          />
        </div>

        {/* Reference-style search trigger: it looks like a field but is a
            button. The real input lives in the dialog, so the shell never
            steals a keystroke the page below is using. */}
        <Button
          variant="ghost"
          onClick={(event) => {
            openCommandMenu(event.currentTarget);
          }}
          aria-label={t("shell.command.open")}
          aria-keyshortcuts="Control+K Meta+K"
          data-testid="header-command-trigger"
          className="text-muted-foreground ml-1 hidden h-9 w-80 cursor-pointer justify-start gap-2 rounded-md border-0 bg-transparent px-2.5 font-medium hover:bg-transparent hover:text-muted-foreground md:flex lg:w-96"
        >
          <Search className="size-4" aria-hidden />
          <span className="truncate">{t("shell.command.placeholder")}</span>
          <kbd className="border-border/70 ml-auto rounded border px-1.5 py-0.5 font-mono text-[10px]">
            ⌘K
          </kbd>
        </Button>

        <div className="ml-auto flex shrink-0 items-center gap-1">
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={(event) => {
              openCommandMenu(event.currentTarget);
            }}
            aria-label={t("shell.command.open")}
            className="md:hidden"
          >
            <Search className="size-4" />
          </Button>

          {/* Release cluster. Owner report 2026-10-23: it read as a foreign
              widget beside the theme/language controls, so it now matches them
              exactly — foreground (pure white in dark), 13px at the same
              optical size as their 20px glyphs. */}
          <div
            className="text-foreground mr-1 hidden items-center gap-2.5 text-[13px] font-medium xl:flex"
            data-testid="header-release"
          >
            <span className="inline-flex items-center gap-1.5 font-mono tabular-nums">
              <Tags className="size-4" aria-hidden />
              {currentVersion ?? "—"}
            </span>
            <span className="bg-muted-foreground/40 size-1 rounded-full" aria-hidden />
            <span className={hasUpdate ? "text-success" : undefined}>
              {hasUpdate
                ? t("shell.release.hasUpdate", { latest: releaseSnapshot?.tag_name ?? "" })
                : t("shell.release.upToDate")}
            </span>
            {releaseSnapshot !== null && (
              <>
                <span className="bg-muted-foreground/40 size-1 rounded-full" aria-hidden />
                <a
                  href={releaseSnapshot.html_url}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="hover:text-primary inline-flex items-center gap-1 underline-offset-4 hover:underline"
                >
                  {t("shell.release.notes")}
                  <ArrowRight className="size-3.5" aria-hidden />
                </a>
              </>
            )}
          </div>

          <ThemeToggle />
          <LanguageToggle />
          <UserMenu />
        </div>
      </nav>
      {commandRequested && (
        <Suspense fallback={<Spinner role="status" aria-label={t("states.loading")} className="absolute top-4 right-4" />}>
          <CommandMenu open={commandOpen} onOpenChange={setCommandOpen} returnFocusRef={commandReturnFocus} />
        </Suspense>
      )}
    </header>
  );
}
