import { useQuery } from "@tanstack/react-query";
import { getCurrentAnnouncement } from "@/api/generated/client/announcements/announcements";
import { unwrapApi } from "@/shared/api/mutator";

/**
 * The published system announcement, or null when none is live (announcement
 * PRD §4). The client renders the server-sanitized HTML as-is; the sanitizer
 * authority stays server-side.
 */
export function useCurrentAnnouncementQuery(enabled: boolean) {
  return useQuery({
    queryKey: ["announcements", "current"],
    queryFn: () => unwrapApi(getCurrentAnnouncement()),
    enabled,
    staleTime: 60_000,
  });
}
