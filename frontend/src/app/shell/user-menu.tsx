import { lazy, Suspense, useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/shared/components/ui/button";
import { UserAvatar } from "@/shared/components/user-avatar";
import type { SessionUser } from "@/shared/session/session";
import { useSession } from "@/features/auth/session-provider";

const UserMenuSheet = lazy(() =>
  import("./user-menu-sheet").then((module) => ({ default: module.UserMenuSheet })),
);

/**
 * Header user menu, following the frozen template's profile-sheet structure
 * (identity block, menu list, footer) with Yearning content. The identity and
 * the badge come from the real server session (GET /users/me). Sign-out is a
 * real POST /auth/logout — the HttpOnly session cookie is revoked server-side
 * and the session cache is dropped before navigating to /login. The
 * profile action navigates to the settings-style profile page (FE-F10,
 * UI spec §5.1: avatar entry, never a sidebar item).
 */
export function UserMenu({ user: userOverride }: { user?: SessionUser }) {
  const { t } = useTranslation();
  // Read real session identity when no override is active (avatar shows logged-in user instead of brand placeholder Y).
  const session = useSession();
  const avatarName =
    userOverride?.displayName.trim() || session.user?.display_name?.trim() || session.user?.username.trim() || "Yearning";
  const [open, setOpen] = useState(false);
  const [requested, setRequested] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const panelId = useId();

  // First-open stutter fix (owner feedback 2026-11-04): the first click previously paid three costs at once:
  // "download chunk + mount sheet + open animation". Preload the sheet chunk before first interaction
  // (on idle, or immediately on hover/focus) so clicking only pays the open animation — sheet mounting itself stays
  // lazy (not rendered before requested), and release bundles do not eagerly load it.
  useEffect(() => {
    const preload = (): void => { void import("./user-menu-sheet"); };
    if (typeof window.requestIdleCallback === "function") {
      const handle = window.requestIdleCallback(preload, { timeout: 2000 });
      return () => { window.cancelIdleCallback(handle); };
    }
    const timer = window.setTimeout(preload, 600);
    return () => { window.clearTimeout(timer); };
  }, []);

  return (
    <>
      <Button
        ref={triggerRef}
        type="button"
        variant="ghost"
        size="icon"
        className="hover:bg-primary/5 h-10 w-10 cursor-pointer rounded-full"
        aria-label={t("shell.userMenu.open")}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        data-testid="user-menu-trigger"
        onPointerEnter={() => { setRequested(true); }}
        onFocus={() => { setRequested(true); }}
        onClick={() => {
          setRequested(true);
          setOpen(true);
        }}
      >
        {/* avatar-13 (owner 2026-10-31): deterministic name-based color palette + initials. */}
        <UserAvatar
          name={avatarName}
        />
      </Button>
      {requested && (
        <Suspense fallback={null}>
          <UserMenuSheet
            user={userOverride}
            open={open}
            onOpenChange={setOpen}
            returnFocusRef={triggerRef}
            panelId={panelId}
          />
        </Suspense>
      )}
    </>
  );
}
