import type { ReactNode } from "react";
import { Navigate, useLocation } from "react-router";
import { useSession } from "@/features/auth/session-provider";
import { ErrorState } from "@/shared/components/status/status-components";
import { PageLoadingState } from "@/shared/components/status/page-loading-state";

/**
 * Route guards (frontend PRD §10.3). Authorization stays server-side: the
 * guards only read the session fact from GET /users/me — an anonymous visitor
 * is sent to /login, and admin surfaces check the server-declared
 * `can_access_admin` capability, never a client-side role rule. A session
 * probe that failed for non-authentication reasons (network, 5xx) shows a
 * retryable degraded state instead of pretending the user signed out.
 */

function SessionUnavailable() {
  const { retry, probeError } = useSession();
  // The real probe failure (network, 5xx, malformed envelope) drives the
  // transport text and request_id; the retry action re-runs the probe.
  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <div className="w-full max-w-md">
        <ErrorState error={probeError} operationId="getCurrentUser" onRetry={retry} />
      </div>
    </div>
  );
}

function useGuardSession(): "loading" | "anonymous" | "authenticated" | "unavailable" {
  const { status } = useSession();
  return status;
}

export function RequireSession({ children }: { children: ReactNode }) {
  const status = useGuardSession();
  const location = useLocation();
  if (status === "loading") {
    return (
      <div className="mx-auto w-full max-w-[1536px] p-4">
        <PageLoadingState />
      </div>
    );
  }
  if (status === "unavailable") {
    return <SessionUnavailable />;
  }
  if (status === "anonymous") {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}

export function RedirectIfAuthenticated({ children }: { children: ReactNode }) {
  const status = useGuardSession();
  const location = useLocation();
  const { user } = useSession();
  if (status === "loading") {
    return (
      <div className="mx-auto w-full max-w-[1536px] p-4">
        <PageLoadingState />
      </div>
    );
  }
  if (status === "unavailable") {
    return <SessionUnavailable />;
  }
  if (status === "authenticated") {
    const from: unknown = location.state;
    let target =
      typeof from === "object" && from !== null && "from" in from && typeof from.from === "string"
        ? from.from
        : "/workspace";
    // UX-002: fall back to workspace when the redirect target is an admin surface and the current session lacks admin capability,
    // instead of routing through /403.
    if (target.startsWith("/admin") && !user?.can_access_admin) {
      target = "/workspace";
    }
    return <Navigate to={target} replace />;
  }
  return <>{children}</>;
}

export function RequireAdminCapability({ children }: { children: ReactNode }) {
  const { user } = useSession();
  const status = useGuardSession();
  const location = useLocation();
  if (status === "loading") {
    return <PageLoadingState />;
  }
  if (status === "unavailable") {
    return <SessionUnavailable />;
  }
  if (status === "anonymous") {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  if (!user?.can_access_admin) {
    return <Navigate to="/403" replace />;
  }
  return <>{children}</>;
}
