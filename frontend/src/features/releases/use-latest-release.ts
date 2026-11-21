import { useQuery } from "@tanstack/react-query";
import { getLatestRelease } from "@/api/generated/client/dashboard/dashboard";
import { unwrapApi } from "@/shared/api/mutator";

/**
 * Latest release snapshot served by this instance's backend (startup + daily
 * upstream refresh). Shared by the home banner and the shell header's release
 * cluster, so both read one cache entry instead of fetching twice.
 */
export function useLatestReleaseQuery(enabled: boolean) {
  return useQuery({
    queryKey: ["releases", "latest"],
    queryFn: () => unwrapApi(getLatestRelease()),
    enabled,
    // Backend snapshot refreshes daily server-side; retain a 6-hour client
    // stale window without window-focus refetches so the shell fetches once.
    staleTime: 6 * 60 * 60 * 1000,
    refetchOnWindowFocus: false,
    retry: false,
  });
}
