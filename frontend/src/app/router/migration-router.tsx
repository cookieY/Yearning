import { lazy } from "react";
import { createBrowserRouter, Navigate, Outlet } from "react-router";
import { BlankLayout } from "@/app/layouts/blank-layout";
import { AppHeader } from "@/app/shell/app-header";
import { Loadable } from "@/app/router/loadable";
import { RedirectIfAuthenticated, RequireSession } from "@/app/router/guards";

/**
 * Migration-mode router (migration contract §8 machine gate). Built ONLY
 * when the session declares migration review capability (the dedicated
 * migration review server answers /users/me with capabilities:
 * ["migration_review"]). Exactly three surfaces exist: the login page, the
 * migration workbench behind the session guard, and an explicit maintenance
 * page for every normal business deep link — the normal app router (with
 * no /admin/migrations route at all) stays the only tree in normal mode.
 */

const LoginPage = lazy(() => import("@/routes/login/login-page"));
const AdminMigrationsPage = lazy(() => import("@/routes/migrations/admin-migrations-page"));
const MigrationMaintenancePage = lazy(() => import("@/routes/migrations/maintenance-page"));

/** Minimal migration shell: header for identity/logout only — the normal
 * business navigation is deliberately absent in migration review mode. */
function MigrationLayout() {
  return (
    <div className="flex min-h-screen flex-col">
      <AppHeader />
      <main className="mx-auto w-full max-w-5xl flex-1 p-6">
        <Outlet />
      </main>
    </div>
  );
}

export function buildMigrationRouter() {
  return createBrowserRouter([
    {
      path: "/login",
      element: (
        <BlankLayout>
          <RedirectIfAuthenticated>{Loadable(LoginPage)}</RedirectIfAuthenticated>
        </BlankLayout>
      ),
    },
    {
      path: "/admin/migrations",
      element: (
        <RequireSession>
          <MigrationLayout />
        </RequireSession>
      ),
      children: [{ index: true, element: Loadable(AdminMigrationsPage) }],
    },
    // Every other path — including all normal business deep links — lands
    // on the explicit maintenance page (never a silent 404).
    { path: "*", element: Loadable(MigrationMaintenancePage) },
    { path: "/", element: <Navigate to="/admin/migrations" replace /> },
  ]);
}
