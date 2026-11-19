import { useMemo } from "react";
import { RouterProvider } from "react-router";
import { useSession } from "@/features/auth/session-provider";
import { toSessionUser } from "@/shared/session/session";
import { appRouter } from "@/app/router/router";
import { buildMigrationRouter } from "@/app/router/migration-router";

/**
 * Mode-selecting router host (migration contract §8): the session decides
 * which tree exists. Normal mode renders the standard app router — the
 * migration workbench is NOT registered there ("normal operation mode must not register or bundle
 * a navigable migration workbench entry"). When the server declares migration review mode
 * (/users/me capabilities includes "migration_review"), the minimal
 * migration router takes over: login, /admin/migrations and an explicit
 * maintenance page for every business deep link. The router instance is
 * rebuilt once per mode flip; React Router remounts, which is acceptable
 * because a mode switch is a different server deployment, not an in-app
 * navigation.
 */
export function AppRouter() {
  const { user } = useSession();
  const migrationReview = user === null ? false : toSessionUser(user).migrationReview;
  const migrationRouter = useMemo(
    () => (migrationReview ? buildMigrationRouter() : null),
    [migrationReview],
  );
  return <RouterProvider router={migrationRouter ?? appRouter} />;
}
