import { useQuery } from "@tanstack/react-query";
import { getCurrentAnnouncement } from "@/api/generated/client/announcements/announcements";
import type { AnnouncementPublication } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * The published system announcement, or null when none is live (announcement
 * PRD §4). The client renders the server-sanitized HTML as-is; the sanitizer
 * authority stays server-side.
 */
export function useCurrentAnnouncementQuery(enabled: boolean) {
  return useQuery({
    queryKey: ["announcements", "current"],
    queryFn: async () =>
      (await getCurrentAnnouncement()) as unknown as AnnouncementPublication | null,
    enabled,
    staleTime: 60_000,
  });
}
