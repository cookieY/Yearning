import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSession } from "@/features/auth/session-provider";
import {
  closeQuerySession,
  createQuerySession,
  executeSelect,
  fetchQueryResultPage,
  getQuerySession,
  listQuerySessions,
} from "@/api/generated/client/query-sessions/query-sessions";
import {
  createQueryAccessRequest,
  decideQueryAccess,
  listQueryAccessRequests,
  listQueryGrants,
  listQueryOrders,
  relinquishQueryGrant,
  revokeQueryGrant,
  withdrawQueryAccessRequest,
} from "@/api/generated/client/query-access/query-access";
import { listCurrentUserFlows } from "@/api/generated/client/change-drafts/change-drafts";
import { useIdempotentMutation } from "@/shared/api/idempotency";
import { idempotencyKeyHeader, ifMatchIdempotencyHeader, unwrapApi } from "@/shared/api/mutator";
import { EMPTY_PAGE } from "@/shared/lib/cursor-page";
import type {
  ApprovalDecisionRequest,
  PageInfo,
  CreateQueryAccessRequest as CreateQueryAccessRequestBody,
  CreateQuerySession as CreateQuerySessionBody,
  ExecuteQueryRequest,
  Flow,
  ListQueryAccessRequestsScope,
  QueryAccessRequest,
  QueryAccessRequestState,
  QueryGrant,
  QueryOrderEntry,
  QueryResultPage,
  QuerySession,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Server-state hooks for the query domain (work package FE-F10). Query
 * reads are relation-scoped server-side (owner / frozen reviewer), so the
 * hooks never filter — they consume exactly what the declared OpenAPI
 * returns. Mutations carry If-Match (row version) and the Idempotency-Key
 * header exactly where the frozen OpenAPI declares it.
 */

function itemsOf<T>(data: { items?: T[] } | undefined | null): T[] {
  return data?.items ?? [];
}

/** Relation-scoped reads are user-bound server-side; keying by the session
 * user keeps a mid-session identity flip from serving the previous user's
 * cached lists (F6 mine-tab precedent). */
function useSessionUserKey(): string {
  return useSession().user?.id ?? "anonymous";
}

// ---- flow catalog / entry ------------------------------------------------

export function useMyQueryFlows(enabled: boolean) {
  return useQuery({
    queryKey: ["query", "actor", useSessionUserKey(), "my-flows"],
    queryFn: async () =>
      itemsOf<Flow>(await unwrapApi(listCurrentUserFlows({ flow_type: "query_access" }))),
    enabled,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
}

// ---- access requests & grants -------------------------------------------

export function useMyAccessRequests(
  enabled: boolean,
  paging?: {
    limit?: number;
    after?: string;
    state?: QueryAccessRequestState;
    scope?: ListQueryAccessRequestsScope;
  },
) {
  const query = useQuery({
    queryKey: [
      "query", "actor", useSessionUserKey(), "access-requests",
      paging?.after ?? "", paging?.limit ?? 0, paging?.state ?? "", paging?.scope ?? "",
    ],
    queryFn: async () => {
      // §18.82: server-side cursor paging (default 20/page) — consumers
      // read {items, page}; table pages render the footer from page.
      // RCP-20261021: state & scope filtered server-side (approvals pending view).
      const response = await unwrapApi(
        listQueryAccessRequests({
          limit: paging?.limit,
          after: paging?.after || undefined,
          state: paging?.state || undefined,
          scope: paging?.scope || undefined,
        }),
      );
      return {
        items: itemsOf<QueryAccessRequest>(response),
        page: response.page,
      };
    },
    enabled,
    // Keep previous page during pagination/filtering: table does not flash empty, and footer can show in-progress feedback.
    placeholderData: keepPreviousData,
  });
  // §18.82: data is always defined (empty page fallback while pending), so consumers need no null check.
  return { ...query, data: query.data ?? EMPTY_PAGE };
}

export function useMyGrants(enabled: boolean) {
  return useQuery({
    queryKey: ["query", "actor", useSessionUserKey(), "grants"],
    queryFn: async () => itemsOf<QueryGrant>(await unwrapApi(listQueryGrants({ limit: 100 }))),
    enabled,
  });
}

export function useCreateAccessRequest() {
  const queryClient = useQueryClient();
  return useIdempotentMutation("query-access", {
    mutationFn: (body: CreateQueryAccessRequestBody, idempotencyKey) =>
      unwrapApi(
        createQueryAccessRequest(body, {
          headers: idempotencyKeyHeader(idempotencyKey),
        }),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useDecideAccessRequest() {
  const queryClient = useQueryClient();
  return useIdempotentMutation("query-decide", {
    mutationFn: (
      input: {
        requestId: string;
        version: number;
        body: ApprovalDecisionRequest;
      },
      idempotencyKey,
    ) =>
      unwrapApi(
        decideQueryAccess(
          input.requestId,
          input.body,
          ifMatchIdempotencyHeader(input.version, idempotencyKey),
        ),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useWithdrawAccessRequest() {
  const queryClient = useQueryClient();
  return useIdempotentMutation("query-withdraw", {
    mutationFn: (
      input: { requestId: string; version: number; reason: string },
      idempotencyKey,
    ) =>
      unwrapApi(
        withdrawQueryAccessRequest(
          input.requestId,
          { reason: input.reason },
          ifMatchIdempotencyHeader(input.version, idempotencyKey),
        ),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useRevokeGrant() {
  const queryClient = useQueryClient();
  return useIdempotentMutation("query-revoke", {
    mutationFn: (input: { grantId: string; version: number; reason: string }, idempotencyKey) =>
      unwrapApi(
        revokeQueryGrant(
          input.grantId,
          { reason: input.reason },
          ifMatchIdempotencyHeader(input.version, idempotencyKey),
        ),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useRelinquishGrant() {
  const queryClient = useQueryClient();
  return useIdempotentMutation("query-relinquish", {
    mutationFn: (input: { grantId: string; version: number; reason: string }, idempotencyKey) =>
      unwrapApi(
        relinquishQueryGrant(
          input.grantId,
          { reason: input.reason },
          ifMatchIdempotencyHeader(input.version, idempotencyKey),
        ),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

// ---- combined query orders (RCP-20261021) --------------------------------

/** Combined Query Orders view: requests + sessions merged server-side with unified keyset pagination. */
export function useQueryOrders(enabled: boolean, paging?: { limit?: number; after?: string }) {
  const query = useQuery({
    queryKey: [
      "query", "actor", useSessionUserKey(), "orders",
      paging?.after ?? "", paging?.limit ?? 0,
    ],
    queryFn: async () => {
      const response = await unwrapApi(
        listQueryOrders({
          limit: paging?.limit,
          after: paging?.after || undefined,
        }),
      );
      return {
        items: itemsOf<QueryOrderEntry>(response),
        page: response.page,
      };
    },
    enabled,
    // Keep previous page during pagination: combined view does not flash empty, and footer shows in-progress feedback.
    placeholderData: keepPreviousData,
  });
  // §18.82: data is always defined (empty page fallback while pending), so consumers need no null check.
  return { ...query, data: query.data ?? EMPTY_PAGE };
}

// ---- sessions ------------------------------------------------------------

export function useMySessions(enabled: boolean, paging?: { limit?: number; after?: string }) {
  const query = useQuery({
    queryKey: [
      "query", "actor", useSessionUserKey(), "sessions",
      paging?.after ?? "", paging?.limit ?? 0,
    ],
    queryFn: async () => {
      const response = await unwrapApi(
        listQuerySessions({
          limit: paging?.limit,
          after: paging?.after || undefined,
        }),
      );
      return {
        items: itemsOf<QuerySession>(response),
        page: response.page,
      };
    },
    enabled,
  });
  // §18.82: data is always defined (empty page fallback while pending), so consumers need no null check.
  return { ...query, data: query.data ?? EMPTY_PAGE };
}

export function useQuerySession(sessionId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["query", "session", sessionId],
    queryFn: () => unwrapApi(getQuerySession(sessionId)),
    enabled: enabled && sessionId !== "",
    retry: false,
  });
}

export function useCreateSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateQuerySessionBody) => unwrapApi(createQuerySession(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

export function useCloseSession() {
  const queryClient = useQueryClient();
  return useIdempotentMutation("query-close", {
    mutationFn: (input: { sessionId: string; reason: string }, idempotencyKey) =>
      unwrapApi(
        closeQuerySession(input.sessionId, { reason: input.reason }, {
          headers: idempotencyKeyHeader(idempotencyKey),
        }),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
}

// ---- metadata ------------------------------------------------------------

export {
  METADATA_STALE_TIME,
  useSessionSchemas,
  useSessionTables,
  useSessionColumns,
  useSessionCompletionCatalog,
  type SessionCompletionResult,
} from "./use-query-session-metadata";

// ---- executions & cursor pages ------------------------------------------

export const DISPLAY_PAGE_SIZE = 1000;
export const EXPORT_PAGE_SIZE = 1000;

export function runSelect(
  sessionId: string,
  body: ExecuteQueryRequest,
): Promise<QueryResultPage> {
  return unwrapApi(executeSelect(sessionId, body));
}

export function fetchPage(
  executionId: string,
  cursor: string,
  purpose: "display" | "export",
  pageSize?: number,
): Promise<QueryResultPage> {
  // pageSize is the RCP-20260901-QUERY-PAGE-SIZE-PARAM override (1..5000,
  // applies to this and subsequent fetches server-side); omit it to keep
  // the size the cursor was opened with (display pages 500, export pages
  // opened small — see the workspace export SEC-0031 note).
  return unwrapApi(
    fetchQueryResultPage(executionId, {
      cursor,
      purpose,
      page_size: pageSize,
    }),
  );
}

/** Page-info helper for callers that only need the continuation token. */
export function nextCursorOf(page: QueryResultPage): string | null {
  return ((page as { page?: PageInfo }).page?.next_cursor as string | null) ?? null;
}
