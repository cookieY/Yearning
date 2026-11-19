import { useEffect } from "react";
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

export const FLOW_SSE_EVENT_TYPES = [
  "io.yearning.v4.flow.updated",
  "io.yearning.v4.user.deleted",
] as const;

export const SSE_RESYNC_EVENT_TYPE = "resync" as const;

const QUERY_EVENT_SET = new Set<string>(QUERY_SSE_EVENT_TYPES);
const ORDER_EVENT_SET = new Set<string>(ORDER_SSE_EVENT_TYPES);
const FLOW_EVENT_SET = new Set<string>(FLOW_SSE_EVENT_TYPES);

const REALTIME_ALL_DOMAIN_KEYS: readonly (readonly string[])[] = [
  ["query"],
  ["change-orders"],
  ["change-order"],
  ["change-order-timeline"],
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
  ["execution-attempt"],
  ["execution-statements"],
  ["dashboard"],
];

const FLOW_DOMAIN_KEYS: readonly (readonly string[])[] = [
  ["current-user-flows"],
  ["auth", "flows"],
  ["query"],
  ["admin", "flows"],
  ["dashboard"],
];

const MAX_SEEN_IDS = 2048;

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

export function invalidateAllRealtimeDomains(queryClient: QueryClient): void {
  invalidateKeyPrefixes(queryClient, REALTIME_ALL_DOMAIN_KEYS);
}

interface ParsedSsePayload {
  id: string;
  type: string;
}

function parseSsePayload(rawData: string, lastEventId: string): ParsedSsePayload {
  let id = lastEventId;
  let type = "";

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
      }
    } catch {
      // Non-JSON payloads (such as plain-text resync frames) are tolerated safely.
    }
  }

  return { id, type };
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

  if (resolvedType === SSE_RESYNC_EVENT_TYPE) {
    if (!rememberEventId(seenIds, payload.id)) return;
    invalidateAllRealtimeDomains(queryClient);
    return;
  }

  if (QUERY_EVENT_SET.has(resolvedType)) {
    if (!rememberEventId(seenIds, payload.id)) return;
    // The ["query"] prefix matches ["query", "session", id] as well as all
    // actor-scoped query reads (grants, access-requests, orders, sessions).
    invalidateKeyPrefixes(queryClient, QUERY_DOMAIN_KEYS);
    return;
  }

  if (ORDER_EVENT_SET.has(resolvedType)) {
    if (!rememberEventId(seenIds, payload.id)) return;
    invalidateKeyPrefixes(queryClient, ORDER_DOMAIN_KEYS);
    return;
  }

  if (FLOW_EVENT_SET.has(resolvedType)) {
    if (!rememberEventId(seenIds, payload.id)) return;
    invalidateKeyPrefixes(queryClient, FLOW_DOMAIN_KEYS);
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
