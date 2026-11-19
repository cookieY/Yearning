import { getReviewEventClient } from "@/shared/events/review-event-client";

/**
 * Lifecycle owner for the shared domain-event feed (frontend PRD §11 Phase F4: the
 * event client is a singleton and components never open their own stream).
 * The workspace page starts it on mount and stops it on unmount; the client's
 * resume point survives across pages, so a remount continues the feed without
 * re-consumed events.
 *
 * Transport selection: the api-conventions contract keeps HTTP polling as the
 * first-release baseline and the real backend stream wires in F12 — under
 * VITE_ENABLE_MOCK the stateful fixture transport drives the client so E2E
 * exercises the exact ingest/dedup/sequence semantics.
 */
let feed: Promise<void> | null = null;

export async function startReviewEvents(): Promise<void> {
  const client = getReviewEventClient();
  if (import.meta.env.VITE_ENABLE_MOCK === "true") {
    if (feed === null) {
      const { createMockEventTransport } = await import("@/shared/mock/review-fixture");
      // The client converges a stop() that races a remount (run() awaits the
      // previous loop before starting a fresh one), so a pending feed promise
      // must not suppress a restart after it resolves.
      feed = client.run((resume) => createMockEventTransport(resume)).finally(() => {
        if (feed === null) return;
        feed = null;
      });
    }
  }
}

export function stopReviewEvents(): void {
  feed = null;
  getReviewEventClient().stop();
}
