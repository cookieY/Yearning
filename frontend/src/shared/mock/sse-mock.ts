import { http, HttpResponse } from "msw";

export interface MockSseEventPayload {
  id: string;
  type: string;
  subject?: string;
  aggregate_id?: string;
  state?: string;
  occurred_at: string;
}

type SseSubscriber = (payload: MockSseEventPayload) => void;

const subscribers = new Set<SseSubscriber>();
let nextEventSeq = 1;

/**
 * Publishes a lightweight SSE notification to active browser EventSource
 * subscribers. When no subscribers exist (such as in Node Vitest suites),
 * returns immediately without allocating timers.
 */
export function emitMockSseEvent(
  type: string,
  detail: { subject?: string; aggregate_id?: string; state?: string } = {},
): void {
  if (subscribers.size === 0) return;

  const payload: MockSseEventPayload = {
    id: `mock-evt-${String(nextEventSeq)}`,
    type,
    occurred_at: new Date().toISOString(),
    ...detail,
  };
  nextEventSeq += 1;

  // Yield briefly so the triggering fetch completes and caller-side mock
  // identity swaps in E2E specs settle before TanStack Query re-reads.
  setTimeout(() => {
    for (const notify of subscribers) {
      notify(payload);
    }
  }, 50);
}

/**
 * MSW handlers backing `GET /api/v4/events/stream` for browser E2E runs so
 * `GlobalEventBridge` stays connected and receives live invalidations.
 */
export function sseMockHandlers() {
  return [
    http.get("*/events/stream", () => {
      const encoder = new TextEncoder();
      let subscriber: SseSubscriber | null = null;

      const stream = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(encoder.encode(": connected\n\n"));

          subscriber = (payload) => {
            const frame =
              `id: ${payload.id}\n` +
              `event: ${payload.type}\n` +
              `data: ${JSON.stringify(payload)}\n\n`;
            try {
              controller.enqueue(encoder.encode(frame));
            } catch {
              if (subscriber !== null) {
                subscribers.delete(subscriber);
                subscriber = null;
              }
            }
          };

          subscribers.add(subscriber);
        },
        cancel() {
          if (subscriber !== null) {
            subscribers.delete(subscriber);
            subscriber = null;
          }
        },
      });

      return new HttpResponse(stream, {
        status: 200,
        headers: {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
        },
      });
    }),
  ];
}
