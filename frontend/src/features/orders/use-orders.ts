import { useEffect } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  ChangeOrder,
  ChangeOrderState,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { listChangeDrafts } from "@/api/generated/client/change-drafts/change-drafts";
import {
  copyChangeOrderToDraft,
  createRollbackDraft,
  getChangeOrder,
  listChangeOrderTimeline,
  listChangeOrders,
  voidChangeOrder,
  withdrawChangeOrder,
} from "@/api/generated/client/change-orders/change-orders";
import { ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import {
  matchesEntitySubject,
  subscribeSseEvent,
  useSseEvent,
} from "@/shared/events/sse-event-bus";
import { useSession } from "@/features/auth/session-provider";
import { holdsSessionSlot } from "@/shared/dev/mock-super";
import { activeExecutorStageFor } from "@/features/orders/order-state";
import { randomUUID } from "@/shared/lib/uuid";

/**
 * Server-state hooks for the personal order pages (frontend PRD F6). Events
 * are notifications only — every handler re-reads the HTTP resource, so
 * at-least-once redelivery can never duplicate rows (acceptance gate:
 * list events produce no duplicates lives in the client dedup + full refetch, not in list
 * merging).
 */

/** Server-side list filters (RCP-20260831-ORDER-LIST-FILTER contract
 * surface). All filters narrow the submitter-scoped result on the server —
 * the UI never filters client-side, which would fight cursor paging and the
 * event-driven re-read semantics. */
export interface OrderListFilters {
  state?: ChangeOrderState;
  q?: string;
  datasource?: string;
  submitted_from?: string;
  submitted_to?: string;
}

const EMPTY_FILTERS: OrderListFilters = {};

function activeFilters(filters: OrderListFilters): OrderListFilters {
  return Object.fromEntries(
    Object.entries(filters).filter(([, value]) => value !== undefined && value !== ""),
  );
}

/** Presentation scope for the relation-scoped order read (F-UX-004):
 * - "submitter" (default, UI-spec §5.2 My Orders): only orders I submitted.
 * - "execution": orders where the current user is a frozen executor of an active
 *   execution stage (`stage_execution_pending`, `scheduled`, `running`, or
 *   `result_unknown`), so non-approver self-executors retain a clickable route
 *   to `/approvals/orders/:id` to execute, cancel a schedule or running attempt,
 *   or manually verify `result_unknown`. Both are presentation filters over the
 *   same relation-scoped read (submitter / frozen approver / frozen executor),
 *   never an authorization boundary. */
export type OrderListScope = "submitter" | "execution";

/** "execution" scope predicate (F-UX-004): active or unverified execution
 * stage (`stage_execution_pending`, `scheduled`, `running`, `result_unknown`)
 * where the current user is one of its frozen execution actors. */
function awaitingMyExecution(order: ChangeOrder, me: string): boolean {
  return activeExecutorStageFor(order, me) !== null;
}

export function useMyChangeOrders(
  enabled: boolean,
  filters: OrderListFilters = EMPTY_FILTERS,
  paging?: { limit: number; after?: string },
  scope: OrderListScope = "submitter",
) {
  const queryClient = useQueryClient();
  const session = useSession();
  const me = session.user?.id;
  const effective = activeFilters(filters);
  const query = useQuery({
    // The filter object joins the key so every filter change is a fresh
    // server read, never a stale-cache hit. keepPreviousData holds the last
    // rows (and keeps this hook's consumers mounted) while a first-time
    // filter combination is in flight. The user id joins the key so the
    // scope filter below never runs against a half-initialized session.
    queryKey: ["change-orders", "mine", me, scope, effective, paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      // The list endpoint is relation-scoped (submitter / frozen approval
      // actor / frozen execution actor — backend relationFilterSQL, consumed
      // by the approval queue on the same read). The My Orders tab is
      // submitter-scoped per UI-spec §5.2: a presentation filter on this
      // shared read, never an authorization boundary.
      const readPage = async (after?: string) => {
        const page = await unwrapApi(
          listChangeOrders({
            limit: paging?.limit ?? 50,
            after: after || undefined,
            ...effective,
          }),
        );
        return {
          items: page.items,
          page: page.page,
        };
      };
      if (me === undefined) return { items: [], page: { has_more: false, next_cursor: null } };
      if (scope === "execution") {
        // Review R1 M1: filtering AFTER cursor paging breaks the KPI→list
        // contract — the first page can hold zero matches while the
        // dashboard's pending_execution_count is >0. Drain the relation
        // read forward (bounded lookahead) and filter the accumulated set.
        const matches: ChangeOrder[] = [];
        let cursor: string | null = paging?.after || null;
        for (let guard = 0; guard < 20; guard += 1) {
          const page = await readPage(cursor ?? undefined);
          for (const order of page.items) {
            if (awaitingMyExecution(order, me)) matches.push(order);
          }
          if (!page.page.has_more || page.page.next_cursor === null) {
            return { items: matches, page: { has_more: false, next_cursor: null } };
          }
          cursor = page.page.next_cursor;
        }
        // Lookahead guard hit (limit×20 relation rows ahead of the cursor —
        // extreme for a to-do surface): hand the honest cursor back so the
        // pager's next read continues the drain from where this one stopped.
        return { items: matches, page: { has_more: true, next_cursor: cursor } };
      }
      const page = await readPage(paging?.after);
      return {
        // My Orders submitter-scope (§5.2): the demo super session holds all identity slots at once
        // (shared/dev/mock-super), so show all relation-scoped orders to facilitate integrated testing.
        items: page.items.filter((order) => holdsSessionSlot(order.submitter_user_id, me)),
        page: page.page,
      };
    },
    enabled: enabled && me !== undefined,
    placeholderData: keepPreviousData,
  });

  // The event client routes by exact aggregate subject, so the list page
  // subscribes to the subjects it has actually loaded. keepPreviousData holds
  // the previous read through a filter transition, so the subscription set
  // never drops while a new filter key loads — ingest() cannot advance past
  // undelivered events in that window. New orders arrive through normal
  // invalidation on navigation — the submission flow lands on the detail
  // page and back-navigation remounts this query.
  const orders = query.data?.items ?? [];
  const subscriptionKey = orders.map((order) => order.id).join("|");
  useEffect(() => {
    if (subscriptionKey === "") return;
    const targets = new Set(subscriptionKey.split("|"));
    return subscribeSseEvent("*", (event) => {
      for (const orderId of targets) {
        if (matchesEntitySubject(event.subject, "change-orders", "order", orderId)) {
          void queryClient.invalidateQueries({ queryKey: ["change-orders", "mine"] });
          return;
        }
      }
    });
  }, [subscriptionKey, queryClient]);

  return query;
}

/**
 * Datasource options for the filter select, taken from one unfiltered read
 * of the submitter's orders — filter interactions must not shrink the
 * option set.
 */
export function useOrderDatasourceOptions(enabled: boolean) {
  const session = useSession();
  const me = session.user?.id;
  return useQuery({
    queryKey: ["change-orders", "mine", "datasource-options", me],
    queryFn: async () => {
      const page = await unwrapApi(listChangeOrders({ limit: 200 }));
      if (me === undefined) return [];
      return [
        ...new Set(
          page.items
            .filter((order) => order.submitter_user_id === me)
            .flatMap((order) => order.stages.map((stage) => stage.datasource_name)),
        ),
      ];
    },
    enabled: enabled && me !== undefined,
  });
}

export function useChangeOrder(orderId: string) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["change-order", orderId],
    queryFn: () => unwrapApi(getChangeOrder(orderId)),
  });

  useSseEvent(
    orderId === "" ? null : "*",
    (event) => {
      if (!matchesEntitySubject(event.subject, "change-orders", "order", orderId)) {
        return;
      }
      void queryClient.invalidateQueries({ queryKey: ["change-order", orderId] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", orderId] });
    },
  );

  return query;
}

export function useChangeOrderTimeline(orderId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["change-order-timeline", orderId],
    queryFn: async () => {
      const page = await unwrapApi(listChangeOrderTimeline(orderId, { limit: 50 }));
      return page.items;
    },
    enabled: enabled && orderId !== "",
  });
}

function orderActionRequestInit(order: ChangeOrder | null | undefined): RequestInit {
  // Both headers are declared required on withdrawal/voidance (OpenAPI
  // IdempotencyKey parameter; order_lifecycle profile carries the
  // idempotency codes) — If-Match guards the aggregate version.
  return ifMatchHeader(order?.version, {
    "Idempotency-Key": `order-action-${randomUUID()}`,
  });
}

export function useWithdrawOrder(order: ChangeOrder | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (reason: string) =>
      unwrapApi(withdrawChangeOrder(order?.id as string, { reason }, orderActionRequestInit(order))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-orders", "mine"] });
    },
  });
}

export function useVoidOrder(order: ChangeOrder | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (reason: string) =>
      unwrapApi(voidChangeOrder(order?.id as string, { reason }, orderActionRequestInit(order))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-orders", "mine"] });
    },
  });
}

export function useCopyOrderToDraft(order: ChangeOrder | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { target_flow_id: string; title: string; description?: string }) =>
      unwrapApi(
        copyChangeOrderToDraft(order?.id as string, input, {
          headers: {
            "Idempotency-Key": `copy-draft-${randomUUID()}`,
          },
        }),
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-drafts"] });
    },
  });
}

export function useCreateRollbackDraft(order: ChangeOrder | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input?: { title?: string; description?: string }) =>
      unwrapApi(
        createRollbackDraft(order?.id ?? "", input ?? {}, {
          headers: {
            "Idempotency-Key": `rollback-draft-${randomUUID()}`,
          },
        }),
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-drafts"] });
    },
  });
}

/** Draft catalog for the My Orders split (migration contract §2: drafts and submitted
 * orders are separated). Reuses the draft list endpoint already exercised by F4. */
export function useMyDrafts(enabled: boolean) {
  return useQuery({
    queryKey: ["change-drafts", "mine"],
    queryFn: async () => {
      const page = await unwrapApi(listChangeDrafts({ limit: 50 }));
      // A submitted draft is terminal — its order lives in the orders card,
      // and the draft-phase vocabulary has no honest label for it.
      return page.items.filter((draft) => draft.state !== "submitted");
    },
    enabled,
  });
}
