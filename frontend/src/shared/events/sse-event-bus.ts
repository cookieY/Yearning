import { useEffect, useRef } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useSession } from "@/features/auth/session-provider";

/**
 * Global real-time SSE event broadcast bus (RCP-20261118-REALTIME-SSE-BROADCAST).
 *
 * Maintains a single authenticated EventSource connection to
 * `/api/v4/events/stream` for the logged-in shell. Incoming events carry only
 * lightweight metadata notifications and map directly to TanStack Query
 * invalidations so pages re-read authoritative HTTP resources.
 */

export const QUERY_SSE_EVENT_TYPES = [
  "io.yearning.v4.query_session.state_changed",
  "io.yearning.v4.query_session.closed",
  "io.yearning.v4.query_grant.activated",
  "io.yearning.v4.query_grant.revoked",
  "io.yearning.v4.query_access.state_changed",
  "io.yearning.v4.query_access.requested",
  "io.yearning.v4.query_access.withdrawn",
  "io.yearning.v4.query_grant.relinquished",
  "io.yearning.v4.query_execution.state_changed",
] as const;

export const ORDER_SSE_EVENT_TYPES = [
  "io.yearning.v4.change_order.submitted",
  "io.yearning.v4.change_order.state_changed",
  "io.yearning.v4.change_order.step_activated",
  "io.yearning.v4.approval.decided",
  "io.yearning.v4.execution.state_changed",
  "io.yearning.v4.execution.verification_recorded",
] as const;

const FLOW_SSE_EVENT_TYPES = [
  "io.yearning.v4.flow.updated",
  "io.yearning.v4.user.deleted",
] as const;

const SSE_RESYNC_EVENT_TYPE = "resync" as const;

const QUERY_EVENT_SET = new Set<string>(QUERY_SSE_EVENT_TYPES);
const ORDER_EVENT_SET = new Set<string>(ORDER_SSE_EVENT_TYPES);
const FLOW_EVENT_SET = new Set<string>(FLOW_SSE_EVENT_TYPES);

const REALTIME_ALL_DOMAIN_KEYS: readonly (readonly string[])[] = [
  ["query"],
  ["change-orders"],
  ["change-order"],
  ["change-order-timeline"],
  ["change-order-comments"],
  ["change-draft"],
  ["execution-attempt"],
  ["execution-statements"],
  ["current-user-flows"],
  ["auth", "flows"],
  ["admin", "flows"],
  ["dashboard"],
];

const QUERY_DOMAIN_KEYS: readonly (readonly string[])[] = [
  ["query"],
  ["dashboard"],
];

const ORDER_DOMAIN_KEYS: readonly (readonly string[])[] = [
  ["change-orders"],
  ["change-order"],
  ["change-order-timeline"],
  ["change-order-comments"],
  ["execution-attempt"],
  ["execution-statements"],
  ["dashboard"],
];

const FLOW_DOMAIN_KEYS: readonly (readonly string[])[] = [
  ["current-user-flows"],
  ["auth", "flows"],
  ["query"],
  ["admin", "flows"],
  ["change-draft"],
  ["dashboard"],
];

const MAX_SEEN_IDS = 2048;
const MAX_PUBLISHED_SEEN_IDS = 512;

export function buildEventStreamUrl(): string {
  const rawBase: string = import.meta.env.VITE_API_BASE_URL ?? "";
  const base = rawBase.trim();
  const path = "/api/v4/events/stream";
  if (base === "") return path;
  return `${base.replace(/\/$/, "")}${path}`;
}

function rememberEventId(seenIds: Set<string>, id: string): boolean {
  if (id === "") return true;
  if (seenIds.has(id)) return false;
  if (seenIds.size >= MAX_SEEN_IDS) {
    const oldest = seenIds.values().next();
    if (!oldest.done) {
      seenIds.delete(oldest.value);
    }
  }
  seenIds.add(id);
  return true;
}

function invalidateKeyPrefixes(
  queryClient: QueryClient,
  prefixes: readonly (readonly string[])[],
): void {
  for (const queryKey of prefixes) {
    void queryClient.invalidateQueries({ queryKey: [...queryKey] });
  }
}

function invalidateAllRealtimeDomains(queryClient: QueryClient): void {
  invalidateKeyPrefixes(queryClient, REALTIME_ALL_DOMAIN_KEYS);
}

export interface SseDomainNotification {
  id: string;
  type: string;
  subject?: string;
  data?: unknown;
}

const sseListeners = new Map<string, Set<(event: SseDomainNotification) => void>>();
const publishedSeenIds = new Set<string>();

function rememberEventIdCapped(seenIds: Set<string>, id: string, maxEntries: number): boolean {
  if (id === "") return true;
  if (seenIds.has(id)) return false;
  if (seenIds.size >= maxEntries) {
    const oldest = seenIds.values().next();
    if (!oldest.done) {
      seenIds.delete(oldest.value);
    }
  }
  seenIds.add(id);
  return true;
}

function emitToSseListeners(event: SseDomainNotification): void {
  const exact = sseListeners.get(event.type);
  if (exact !== undefined) {
    for (const listener of [...exact]) {
      listener(event);
    }
  }
  const wildcard = sseListeners.get("*");
  if (wildcard !== undefined) {
    for (const listener of [...wildcard]) {
      listener(event);
    }
  }
}

/**
 * Subscribe to decoded live SSE notifications by CloudEvent `type` (or `"*"` for all types).
 */
export function subscribeSseEvent(
  eventType: string,
  listener: (event: SseDomainNotification) => void,
): () => void {
  let bucket = sseListeners.get(eventType);
  if (bucket === undefined) {
    bucket = new Set();
    sseListeners.set(eventType, bucket);
  }
  bucket.add(listener);
  return () => {
    const active = sseListeners.get(eventType);
    if (active === undefined) return;
    active.delete(listener);
    if (active.size === 0) {
      sseListeners.delete(eventType);
    }
  };
}

/**
 * Subscribe to decoded SSE notifications in a React component without re-subscribing
 * on every render. Pass `null` as `eventType` to disable the subscription.
 */
export function useSseEvent(
  eventType: string | null,
  handler: (event: SseDomainNotification) => void,
): void {
  const handlerRef = useRef(handler);
  useEffect(() => {
    handlerRef.current = handler;
  });
  useEffect(() => {
    if (eventType === null) return;
    return subscribeSseEvent(eventType, (event) => {
      handlerRef.current(event);
    });
  }, [eventType]);
}

/**
 * Publish a domain notification into in-app SSE subscribers with bounded `id`
 * deduplication (used by mock fixtures and tests). Returns `false` when a
 * non-empty `event.id` was already seen.
 */
export function publishSseEvent(event: SseDomainNotification): boolean {
  if (!rememberEventIdCapped(publishedSeenIds, event.id, MAX_PUBLISHED_SEEN_IDS)) {
    return false;
  }
  emitToSseListeners(event);
  return true;
}

/** Clears test-side `publishSseEvent` deduplication state between test cases. */
export function resetSseEventBusForTest(): void {
  publishedSeenIds.clear();
}

export function matchesEntitySubject(
  subject: string | undefined,
  pluralPrefix: string,
  singularPrefix: string,
  id: string,
): boolean {
  return (
    subject === undefined ||
    subject === "" ||
    subject === `${pluralPrefix}/${id}` ||
    subject === `${singularPrefix}:${id}`
  );
}

function parseSsePayload(rawData: string, lastEventId: string): SseDomainNotification {
  let id = lastEventId;
  let type = "";
  let subject: string | undefined;
  let data: unknown;

  if (rawData.trim() !== "") {
    try {
      const parsed: unknown = JSON.parse(rawData);
      if (typeof parsed === "object" && parsed !== null) {
        const record = parsed as Record<string, unknown>;
        if (typeof record.id === "string" && record.id !== "") {
          id = record.id;
        }
        if (typeof record.type === "string") {
          type = record.type;
        }
        if (typeof record.subject === "string" && record.subject !== "") {
          subject = record.subject;
        }
        if ("data" in record) {
          data = record.data;
        }
      }
    } catch {
      // Non-JSON payloads (such as plain-text resync frames) are tolerated safely.
    }
  }

  return { id, type, subject, data };
}

function dispatchSseEvent(
  eventType: string,
  event: Event,
  seenIds: Set<string>,
  queryClient: QueryClient,
): void {
  const msg = event as Partial<MessageEvent<unknown>>;
  const rawData = typeof msg.data === "string" ? msg.data : "";
  const lastEventId = typeof msg.lastEventId === "string" ? msg.lastEventId : "";
  const payload = parseSsePayload(rawData, lastEventId);
  const resolvedType = eventType === "message" ? payload.type : eventType;
  const notification: SseDomainNotification = { ...payload, type: resolvedType };

  if (resolvedType === SSE_RESYNC_EVENT_TYPE) {
    if (!rememberEventId(seenIds, payload.id)) return;
    invalidateAllRealtimeDomains(queryClient);
    emitToSseListeners(notification);
    return;
  }

  if (QUERY_EVENT_SET.has(resolvedType)) {
    if (!rememberEventId(seenIds, payload.id)) return;
    // The ["query"] prefix matches ["query", "session", id] as well as all
    // actor-scoped query reads (grants, access-requests, orders, sessions).
    invalidateKeyPrefixes(queryClient, QUERY_DOMAIN_KEYS);
    emitToSseListeners(notification);
    return;
  }

  if (ORDER_EVENT_SET.has(resolvedType)) {
    if (!rememberEventId(seenIds, payload.id)) return;
    invalidateKeyPrefixes(queryClient, ORDER_DOMAIN_KEYS);
    emitToSseListeners(notification);
    return;
  }

  if (FLOW_EVENT_SET.has(resolvedType)) {
    if (!rememberEventId(seenIds, payload.id)) return;
    invalidateKeyPrefixes(queryClient, FLOW_DOMAIN_KEYS);
    emitToSseListeners(notification);
    return;
  }

  if (resolvedType !== "" && rememberEventId(seenIds, payload.id)) {
    emitToSseListeners(notification);
  }
}

export function useSseEventBus(authenticated: boolean, queryClient: QueryClient): void {
  useEffect(() => {
    if (!authenticated) return;
    if (typeof window === "undefined" || typeof window.EventSource !== "function") return;

    const seenIds = new Set<string>();
    let needsResyncOnReconnect = false;
    let closed = false;

    const source = new window.EventSource(buildEventStreamUrl(), {
      withCredentials: true,
    });

    source.onopen = () => {
      if (closed) return;
      if (needsResyncOnReconnect) {
        needsResyncOnReconnect = false;
        invalidateAllRealtimeDomains(queryClient);
      }
    };

    source.onerror = () => {
      if (closed) return;
      needsResyncOnReconnect = true;
    };

    source.onmessage = (event) => {
      if (closed) return;
      dispatchSseEvent("message", event, seenIds, queryClient);
    };

    const namedEventTypes: readonly string[] = [
      ...QUERY_SSE_EVENT_TYPES,
      ...ORDER_SSE_EVENT_TYPES,
      ...FLOW_SSE_EVENT_TYPES,
      SSE_RESYNC_EVENT_TYPE,
    ];

    const listeners: Array<[string, EventListener]> = namedEventTypes.map((eventType) => {
      const listener: EventListener = (event) => {
        if (closed) return;
        dispatchSseEvent(eventType, event, seenIds, queryClient);
      };
      source.addEventListener(eventType, listener);
      return [eventType, listener];
    });

    return () => {
      closed = true;
      source.onopen = null;
      source.onerror = null;
      source.onmessage = null;
      for (const [eventType, listener] of listeners) {
        source.removeEventListener(eventType, listener);
      }
      source.close();
    };
  }, [authenticated, queryClient]);
}

export function GlobalEventBridge(): null {
  const session = useSession();
  const queryClient = useQueryClient();
  useSseEventBus(session.status === "authenticated", queryClient);
  return null;
}
