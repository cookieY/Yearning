import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { getMyDashboard } from "@/api/generated/client/dashboard/dashboard";
import { getOperationsDashboard } from "@/api/generated/client/administration/administration";
import type {
  MyDashboard,
  OperationsDashboard,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Dashboard reads (dashboard PRD §2/§3/§9). Both endpoints are authenticated
 * session reads: `/dashboard/me` returns the caller's own counters, while
 * `/admin/dashboard/operations` is a platform-wide aggregate that §18.71 opens
 * to every logged-in session (owner ruling 2026-10-16) — the route name is
 * historical, not an access control.
 *
 * The 60s auto-refresh default comes from the PRD; every page-level refresh
 * control reuses these query keys.
 */

export function useMyDashboardQuery(enabled: boolean) {
  return useQuery({
    queryKey: ["dashboard", "me"],
    queryFn: async () => (await getMyDashboard()) as unknown as MyDashboard,
    enabled,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });
}

export function useOperationsDashboardQuery(windowDays: number, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "dashboard", "operations", windowDays],
    queryFn: async () =>
      (await getOperationsDashboard({ window_days: windowDays })) as unknown as OperationsDashboard,
    enabled,
    // Window switches keep the previous chart on screen instead of
    // collapsing the card to a skeleton on every refetch.
    placeholderData: keepPreviousData,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });
}
