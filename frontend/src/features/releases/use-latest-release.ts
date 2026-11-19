import { useQuery } from "@tanstack/react-query";
import type { ReleaseSnapshot } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Latest release snapshot served by this instance's backend (startup + daily
 * upstream refresh). Shared by the home banner and the shell header's release
 * cluster, so both read one cache entry instead of fetching twice.
 */
export function useLatestReleaseQuery(enabled: boolean) {
  return useQuery({
    queryKey: ["releases", "latest"],
    queryFn: async () => {
      const { getLatestRelease } = await import(
        "@/api/generated/client/dashboard/dashboard"
      );
      return (await getLatestRelease()) as unknown as ReleaseSnapshot | null;
    },
    enabled,
    // Backend snapshot refreshes daily server-side; retain a 6-hour client
    // stale window without window-focus refetches so the shell fetches once.
    staleTime: 6 * 60 * 60 * 1000,
    refetchOnWindowFocus: false,
    retry: false,
  });
}
