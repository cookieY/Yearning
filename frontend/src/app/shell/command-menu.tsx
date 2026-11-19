import { useMemo, useRef, useState, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { Search } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { useSession } from "@/features/auth/session-provider";
import { sessionRoleFromCurrentUser } from "@/shared/session/session";
import { cn } from "@/shared/lib/utils";
import { filterNavGroups, NAV_GROUPS, type NavItem } from "./nav-model";

interface Entry {
  item: NavItem;
  label: string;
  groupLabel: string;
}

/**
 * Header command menu (owner request 2026-10-23, supersedes the earlier
 * template-parity exclusion of the demo's menu search in spec §5): typing
 * filters the first-level navigation and Enter jumps to the page. Entries are
 * derived from NAV_GROUPS, so the menu can never drift from the sidebar —
 * including the admin-only groups, which stay filtered by the same session
 * role rule.
 *
 * The body is a separate component on purpose: Base UI unmounts the popup
 * when the dialog closes, so mounting the body only while open resets the
 * query and the highlight without an effect that writes state during render.
 * An earlier version keyed `DialogContent` instead, which remounted the popup
 * mid-transition and left it stuck on screen after Escape, backdrop clicks and
 * navigation (owner bug report 2026-10-23).
 */
export function CommandMenu({
  open,
  onOpenChange,
  returnFocusRef,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  returnFocusRef?: RefObject<HTMLElement | null>;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        finalFocus={returnFocusRef}
        showCloseButton={false}
        className="gap-0 rounded-xl! p-2! sm:max-w-2xl"
        aria-describedby={undefined}
      >
        <DialogTitle className="sr-only">{t("shell.command.title")}</DialogTitle>
        <CommandMenuBody
          onClose={() => {
            onOpenChange(false);
          }}
        />
      </DialogContent>
    </Dialog>
  );
}

function CommandMenuBody({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { user } = useSession();
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const listRef = useRef<HTMLUListElement | null>(null);

  const entries = useMemo<Entry[]>(() => {
    const groups = filterNavGroups(NAV_GROUPS, sessionRoleFromCurrentUser(user));
    return groups.flatMap((group) =>
      group.items.map((item) => ({
        item,
        label: t(item.labelKey),
        groupLabel: t(group.labelKey),
      })),
    );
  }, [t, user]);

  const needle = query.trim().toLowerCase();
  const results = useMemo(
    () =>
      needle === ""
        ? entries
        : entries.filter(
            (entry) =>
              entry.label.toLowerCase().includes(needle) ||
              entry.item.to.toLowerCase().includes(needle),
          ),
    [entries, needle],
  );

  // The list shrinks under the keyboard cursor; clamping at render keeps the
  // highlight valid without a state-syncing effect.
  const highlight = Math.min(activeIndex, Math.max(results.length - 1, 0));

  const run = (entry: Entry | undefined) => {
    if (entry === undefined) return;
    onClose();
    void navigate(entry.item.to);
  };

  const active = results[highlight];
  return (
    <>
      <div className="border-border flex items-center gap-2.5 rounded-lg border px-3.5">
        <Search className="text-muted-foreground size-4 shrink-0" aria-hidden />
        <Input
          autoFocus
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setActiveIndex(0);
          }}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown") {
              event.preventDefault();
              setActiveIndex(Math.min(highlight + 1, results.length - 1));
            } else if (event.key === "ArrowUp") {
              event.preventDefault();
              setActiveIndex(Math.max(highlight - 1, 0));
            } else if (event.key === "Enter") {
              event.preventDefault();
              run(active);
            }
          }}
          onBlur={() => {
            // Arrow keys keep focus in the input; a click elsewhere should
            // not leave a stale highlight behind.
            setActiveIndex(0);
          }}
          placeholder={t("shell.command.placeholder")}
          aria-label={t("shell.command.placeholder")}
          role="combobox"
          aria-expanded
          aria-controls="command-menu-list"
          aria-activedescendant={
            active === undefined ? undefined : `command-menu-item-${String(highlight)}`
          }
          className="h-12 border-0 bg-transparent px-0 text-sm shadow-none focus-visible:border-0 focus-visible:ring-0 dark:bg-transparent"
        />
        <kbd className="text-muted-foreground border-border hidden shrink-0 rounded border px-1.5 py-0.5 font-mono text-[10px] sm:inline-flex">
          {t("shell.command.hint")}
        </kbd>
      </div>
      <p className="text-muted-foreground px-3 pt-4 pb-2 text-[11px] font-medium tracking-wide uppercase">
        {needle === ""
          ? t("shell.command.suggestions")
          : t("shell.command.results", { count: results.length })}
      </p>
      {/* Unified scrollbars (owner 2026-11-03): command list scrolling switched to ScrollArea overlay
          thumb; height cap is placed on the viewport (see scroll-area.tsx comment). */}
      <ScrollArea viewportClassName="max-h-[min(26rem,55vh)]">
        <ul
          ref={listRef}
          id="command-menu-list"
          role="listbox"
          aria-label={t("shell.command.title")}
          className="pb-1"
        >
        {results.length === 0 && (
          <li className="text-muted-foreground px-2 py-6 text-center text-sm">
            {t("shell.command.empty")}
          </li>
        )}
        {results.map((entry, index) => {
          const Icon = entry.item.icon;
          return (
            <li key={entry.item.to}>
              <button
                type="button"
                id={`command-menu-item-${String(index)}`}
                data-active={index === highlight}
                role="option"
                aria-selected={index === highlight}
                onMouseMove={() => {
                  setActiveIndex(index);
                }}
                onClick={() => {
                  run(entry);
                }}
                className={cn(
                  "text-muted-foreground flex h-11 w-full cursor-pointer items-center gap-3 rounded-lg px-3 text-left text-sm font-semibold transition-colors duration-100 ease-out",
                  index === highlight && "bg-muted text-foreground",
                )}
              >
                <Icon className="size-4 shrink-0" aria-hidden />
                <span className="truncate">{entry.label}</span>
                <span className="text-muted-foreground ml-auto shrink-0 text-[11px]">
                  {entry.groupLabel}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      </ScrollArea>
    </>
  );
}
