import { act, render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { HttpResponse, http } from "msw";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { server } from "@/test/msw/server";
import { SessionProvider } from "@/features/auth/session-provider";
import {
  GlobalEventBridge,
  ORDER_SSE_EVENT_TYPES,
  QUERY_SSE_EVENT_TYPES,
  buildEventStreamUrl,
  publishSseEvent,
  resetSseEventBusForTest,
  subscribeSseEvent,
  useSseEvent,
  useSseEventBus,
} from "@/shared/events/sse-event-bus";

class MockEventSource {
  static instances: MockEventSource[] = [];

  readonly url: string;
  readonly init: EventSourceInit | undefined;
  readonly listeners = new Map<string, Set<EventListener>>();
  onopen: ((this: EventSource, ev: Event) => unknown) | null = null;
  onerror: ((this: EventSource, ev: Event) => unknown) | null = null;
  onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null;
  closed = false;
  readyState = 0;

  constructor(url: string | URL, eventSourceInitDict?: EventSourceInit) {
    this.url = String(url);
    this.init = eventSourceInitDict;
    MockEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListener): void {
    let set = this.listeners.get(type);
    if (set === undefined) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(listener);
  }

  removeEventListener(type: string, listener: EventListener): void {
    this.listeners.get(type)?.delete(listener);
  }

  close(): void {
    this.closed = true;
    this.readyState = 2;
  }

  emitOpen(): void {
    this.readyState = 1;
    this.onopen?.call(this as unknown as EventSource, new Event("open"));
  }

  emitError(): void {
    this.onerror?.call(this as unknown as EventSource, new Event("error"));
  }

  emitNamed(type: string, data: unknown, lastEventId = ""): void {
    const serialized = typeof data === "string" ? data : JSON.stringify(data);
    const event = new MessageEvent(type, {
      data: serialized,
      lastEventId,
    });
    const set = this.listeners.get(type);
    if (set === undefined) return;
    for (const listener of set) {
      listener(event);
    }
  }
}

function BusHarness({ authenticated }: { authenticated: boolean }) {
  const client = useQueryClientForTest();
  useSseEventBus(authenticated, client);
  return null;
}

let activeClient: QueryClient | null = null;

function useQueryClientForTest(): QueryClient {
  if (activeClient === null) {
    throw new Error("activeClient must be initialized");
  }
  return activeClient;
}

function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
}

function stubAuthenticatedUser(): void {
  server.use(
    http.get("*/users/me", () =>
      HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: {
          id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac",
          username: "henry",
          display_name: "henry",
          email: null,
          is_builtin_admin: false,
          version: 1,
          created_at: "2026-08-28T08:00:00Z",
          updated_at: "2026-08-28T08:00:00Z",
          can_access_admin: false,
        },
        request_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac",
      }),
    ),
  );
}

function stubAnonymousUser(): void {
  server.use(
    http.get("*/users/me", () =>
      HttpResponse.json(
        {
          type: "about:blank",
          title: "unauthorized",
          status: 401,
          detail: "authentication required",
          request_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac",
        },
        { status: 401 },
      ),
    ),
  );
}

describe("Global SSE event broadcast bus", () => {
  beforeEach(() => {
    MockEventSource.instances = [];
    vi.stubGlobal("EventSource", MockEventSource);
    activeClient = createTestQueryClient();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    activeClient = null;
  });

  it("respects VITE_API_BASE_URL when building the SSE stream URL", () => {
    expect(buildEventStreamUrl()).toBe("/api/v4/events/stream");
    vi.stubEnv("VITE_API_BASE_URL", "https://edge.yearning.test/v4/");
    expect(buildEventStreamUrl()).toBe("https://edge.yearning.test/v4/api/v4/events/stream");
  });

  it("mounts a single EventSource only when authenticated through GlobalEventBridge", async () => {
    stubAuthenticatedUser();
    const client = activeClient;
    if (client === null) throw new Error("missing client");

    const { unmount } = render(
      <QueryClientProvider client={client}>
        <SessionProvider>
          <GlobalEventBridge />
        </SessionProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(MockEventSource.instances).toHaveLength(1);
    });

    const source = MockEventSource.instances[0];
    expect(source?.url).toBe("/api/v4/events/stream");
    expect(source?.init).toEqual({ withCredentials: true });
    expect(source?.closed).toBe(false);

    unmount();
    expect(source?.closed).toBe(true);
  });

  it("does not open an EventSource when the session is anonymous", async () => {
    stubAnonymousUser();
    const client = activeClient;
    if (client === null) throw new Error("missing client");

    render(
      <QueryClientProvider client={client}>
        <SessionProvider>
          <GlobalEventBridge />
        </SessionProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(client.getQueryData(["auth", "session"])).toBeNull();
    });
    expect(MockEventSource.instances).toHaveLength(0);
  });

  it("invalidates query and dashboard caches on query revocation and session events and refetches active session query", async () => {
    const client = activeClient;
    if (client === null) throw new Error("missing client");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    const sessionFetcher = vi.fn(() => Promise.resolve({ id: "qs-1", state: "active" }));

    function SessionObserver() {
      useQuery({
        queryKey: ["query", "session", "qs-1"],
        queryFn: sessionFetcher,
      });
      return null;
    }

    render(
      <QueryClientProvider client={client}>
        <BusHarness authenticated={true} />
        <SessionObserver />
      </QueryClientProvider>,
    );

    await waitFor(() => {
      expect(sessionFetcher).toHaveBeenCalledTimes(1);
    });
    expect(MockEventSource.instances).toHaveLength(1);
    const source = MockEventSource.instances[0];
    if (source === undefined) throw new Error("missing EventSource");

    invalidateSpy.mockClear();

    act(() => {
      source.emitNamed("io.yearning.v4.query_grant.revoked", {
        id: "evt-query-1",
        type: "io.yearning.v4.query_grant.revoked",
        subject: "query-grants/g-1",
        aggregate_id: "g-1",
        state: "revoked",
        occurred_at: "2026-11-18T14:00:00Z",
      });
    });

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["query"] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["dashboard"] });
    expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: ["change-orders"] });

    await waitFor(() => {
      expect(sessionFetcher).toHaveBeenCalledTimes(2);
    });

    invalidateSpy.mockClear();
    act(() => {
      source.emitNamed("io.yearning.v4.query_session.closed", {
        id: "evt-query-2",
        type: "io.yearning.v4.query_session.closed",
        subject: "query-sessions/qs-1",
        aggregate_id: "qs-1",
        state: "closed",
        occurred_at: "2026-11-18T14:00:05Z",
      });
    });

    await waitFor(() => {
      expect(sessionFetcher).toHaveBeenCalledTimes(3);
    });
  });

  it("invalidates order, approval, execution, and dashboard caches on order domain events", () => {
    const client = activeClient;
    if (client === null) throw new Error("missing client");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    render(<BusHarness authenticated={true} />);
    const source = MockEventSource.instances[0];
    if (source === undefined) throw new Error("missing EventSource");

    ORDER_SSE_EVENT_TYPES.forEach((eventType, index) => {
      invalidateSpy.mockClear();
      act(() => {
        source.emitNamed(eventType, {
          id: `evt-order-${String(index)}`,
          type: eventType,
          subject: "change-orders/order-1",
          aggregate_id: "order-1",
          state: "approved",
          occurred_at: "2026-11-18T14:01:00Z",
        });
      });

      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["change-orders"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["change-order"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["change-order-timeline"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["execution-attempt"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["execution-statements"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["dashboard"] });
      expect(invalidateSpy).not.toHaveBeenCalledWith({ queryKey: ["query"] });
    });
  });

  it("registers listeners for every declared query domain event type", () => {
    const client = activeClient;
    if (client === null) throw new Error("missing client");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    render(<BusHarness authenticated={true} />);
    const source = MockEventSource.instances[0];
    if (source === undefined) throw new Error("missing EventSource");

    QUERY_SSE_EVENT_TYPES.forEach((eventType, index) => {
      invalidateSpy.mockClear();
      act(() => {
        source.emitNamed(eventType, {
          id: `evt-q-${String(index)}`,
          type: eventType,
          subject: "query-access-requests/req-1",
          aggregate_id: "req-1",
          state: "access_pending",
          occurred_at: "2026-11-18T14:02:00Z",
        });
      });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["query"] });
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["dashboard"] });
    });
  });

  it("deduplicates repeated event IDs and bounds the seen-ID cache", () => {
    const client = activeClient;
    if (client === null) throw new Error("missing client");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    render(<BusHarness authenticated={true} />);
    const source = MockEventSource.instances[0];
    if (source === undefined) throw new Error("missing EventSource");

    act(() => {
      source.emitNamed("io.yearning.v4.approval.decided", {
        id: "dup-evt-1",
        type: "io.yearning.v4.approval.decided",
      });
      source.emitNamed("io.yearning.v4.approval.decided", {
        id: "dup-evt-1",
        type: "io.yearning.v4.approval.decided",
      });
    });

    // Only the first delivery triggers invalidations (7 order/dashboard prefixes).
    expect(invalidateSpy).toHaveBeenCalledTimes(7);

    // Overflow past 2048 entries evicts the oldest ID while keeping recent IDs deduplicated.
    invalidateSpy.mockClear();
    act(() => {
      for (let i = 0; i < 2050; i += 1) {
        source.emitNamed("io.yearning.v4.query_access.requested", {
          id: `bounded-${String(i)}`,
        });
      }
    });
    const callsAfterOverflow = invalidateSpy.mock.calls.length;
    expect(callsAfterOverflow).toBe(2050 * 2);

    // Recent ID is still deduplicated; evicted "dup-evt-1" is accepted again.
    act(() => {
      source.emitNamed("io.yearning.v4.query_access.requested", {
        id: "bounded-2049",
      });
    });
    expect(invalidateSpy).toHaveBeenCalledTimes(callsAfterOverflow);

    act(() => {
      source.emitNamed("io.yearning.v4.query_access.requested", {
        id: "dup-evt-1",
      });
    });
    expect(invalidateSpy).toHaveBeenCalledTimes(callsAfterOverflow + 2);
  });

  it("invalidates all realtime domain keys on resync control event and on reconnect after error", () => {
    const client = activeClient;
    if (client === null) throw new Error("missing client");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    render(<BusHarness authenticated={true} />);
    const source = MockEventSource.instances[0];
    if (source === undefined) throw new Error("missing EventSource");

    // Initial open does not trigger a spurious resync.
    act(() => {
      source.emitOpen();
    });
    expect(invalidateSpy).not.toHaveBeenCalled();

    // Control event "resync" invalidates every domain prefix.
    act(() => {
      source.emitNamed("resync", { reason: "slow_consumer" });
    });
    const expectedKeys = [
      ["query"],
      ["change-orders"],
      ["change-order"],
      ["change-order-timeline"],
      ["change-order-comments"],
      ["change-draft"],
      ["execution-attempt"],
      ["execution-statements"],
      ["dashboard"],
    ];
    for (const queryKey of expectedKeys) {
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey });
    }

    // Error followed by reconnect open triggers a resync; subsequent open without error does not.
    invalidateSpy.mockClear();
    act(() => {
      source.emitError();
      source.emitOpen();
    });
    for (const queryKey of expectedKeys) {
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey });
    }

    invalidateSpy.mockClear();
    act(() => {
      source.emitOpen();
    });
    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("closes the EventSource on unmount and when authenticated flips to false", () => {
    const { rerender, unmount } = render(<BusHarness authenticated={true} />);
    expect(MockEventSource.instances).toHaveLength(1);
    const first = MockEventSource.instances[0];
    expect(first?.closed).toBe(false);

    rerender(<BusHarness authenticated={false} />);
    expect(first?.closed).toBe(true);

    rerender(<BusHarness authenticated={true} />);
    expect(MockEventSource.instances).toHaveLength(2);
    const second = MockEventSource.instances[1];
    expect(second?.closed).toBe(false);

    unmount();
    expect(second?.closed).toBe(true);
  });

  it("invalidates flow and dashboard caches when io.yearning.v4.flow.updated arrives", () => {
    const client = activeClient;
    if (client === null) throw new Error("missing client");
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    render(<BusHarness authenticated={true} />);
    const source = MockEventSource.instances[0];
    if (source === undefined) throw new Error("missing EventSource");

    act(() => {
      source.emitNamed("io.yearning.v4.flow.updated", {
        id: "evt-flow-1",
        type: "io.yearning.v4.flow.updated",
        subject: "flow:flow-1",
      });
    });

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["current-user-flows"] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["auth", "flows"] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["query"] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["admin", "flows"] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["dashboard"] });
  });

  it("delivers decoded SSE subject and payload to subscribeSseEvent listeners and deduplicates by id", () => {
    const received: Array<{ id: string; type: string; subject?: string; data?: unknown }> = [];
    const unsubscribe = subscribeSseEvent("io.yearning.v4.flow.updated", (evt) => {
      received.push(evt);
    });

    render(<BusHarness authenticated={true} />);
    const source = MockEventSource.instances[0];
    if (source === undefined) throw new Error("missing EventSource");

    act(() => {
      source.emitNamed("io.yearning.v4.flow.updated", {
        id: "evt-sub-1",
        type: "io.yearning.v4.flow.updated",
        subject: "flows/flow-9",
        data: { revision: 4 },
      });
      // Duplicate event id must not notify twice.
      source.emitNamed("io.yearning.v4.flow.updated", {
        id: "evt-sub-1",
        type: "io.yearning.v4.flow.updated",
        subject: "flows/flow-9",
        data: { revision: 4 },
      });
    });

    expect(received).toEqual([
      {
        id: "evt-sub-1",
        type: "io.yearning.v4.flow.updated",
        subject: "flows/flow-9",
        data: { revision: 4 },
      },
    ]);

    act(() => {
      publishSseEvent({
        id: "evt-pub-2",
        type: "io.yearning.v4.flow.updated",
        subject: "flow:flow-9",
      });
    });
    expect(received).toHaveLength(2);
    expect(received[1]?.subject).toBe("flow:flow-9");

    unsubscribe();
    act(() => {
      publishSseEvent({
        id: "evt-pub-3",
        type: "io.yearning.v4.flow.updated",
        subject: "flow:flow-9",
      });
    });
    expect(received).toHaveLength(2);
  });

  it("useSseEvent subscribes cleanly and publishSseEvent deduplicates by event id until reset", () => {
    resetSseEventBusForTest();
    const received: string[] = [];

    function HookProbe({ enabled }: { enabled: boolean }) {
      useSseEvent(enabled ? "io.yearning.v4.change_order.state_changed" : null, (event) => {
        received.push(event.id);
      });
      return null;
    }

    const { rerender, unmount } = render(<HookProbe enabled={true} />);

    act(() => {
      expect(
        publishSseEvent({
          id: "evt-dedupe-1",
          type: "io.yearning.v4.change_order.state_changed",
          subject: "change-orders/123",
        }),
      ).toBe(true);
      expect(
        publishSseEvent({
          id: "evt-dedupe-1",
          type: "io.yearning.v4.change_order.state_changed",
          subject: "change-orders/123",
        }),
      ).toBe(false);
    });
    expect(received).toEqual(["evt-dedupe-1"]);

    resetSseEventBusForTest();
    act(() => {
      expect(
        publishSseEvent({
          id: "evt-dedupe-1",
          type: "io.yearning.v4.change_order.state_changed",
          subject: "change-orders/123",
        }),
      ).toBe(true);
    });
    expect(received).toEqual(["evt-dedupe-1", "evt-dedupe-1"]);

    rerender(<HookProbe enabled={false} />);
    act(() => {
      publishSseEvent({
        id: "evt-dedupe-2",
        type: "io.yearning.v4.change_order.state_changed",
        subject: "change-orders/123",
      });
    });
    expect(received).toEqual(["evt-dedupe-1", "evt-dedupe-1"]);
    unmount();
  });
});
