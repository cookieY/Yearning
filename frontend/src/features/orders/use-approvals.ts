import { useEffect } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  ChangeOrder,
  ChangeOrderComment,
  ReviewFinding,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  createChangeOrderComment,
  decideChangeOrder,
  listChangeOrderComments,
  listChangeOrders,
  listOrderReviewFindings,
  recordOrderSqlCopy,
  revealOrderSql,
} from "@/api/generated/client/change-orders/change-orders";
import { getReviewEventClient, useDomainEvent } from "@/shared/events/review-event-client";
import { useSession } from "@/features/auth/session-provider";
import { holdsSessionSlot } from "@/shared/dev/mock-super";
import { stageApprovalSteps } from "@/features/orders/approval-steps";
import { fetchAllCursorPages, type CursorPage } from "@/shared/lib/fetch-all-cursor-pages";
import { randomUUID } from "@/shared/lib/uuid";

/**
 * Server-state hooks for the approval workspace (frontend PRD F7). The
 * reviewer consumes exactly the frozen submission review (R003): every hook
 * here is a read or an approval-domain write — nothing creates a Review Run,
 * and no transfer/delegation surface exists (W004: the frozen order cannot
 * be edited or reassigned, not even by admin).
 */

/**
 * Approval queue: the relation-scoped order list narrowed to orders where
 * the current user is a frozen actor of the currently active approval step
 * (W003 peer approval). The narrowing is presentation-layer only — the backend
 * re-checks the frozen-actor membership on every decision (3001) and the
 * single-effective-decision constraint (3002) stays authoritative.
 */
export function useApprovalQueue(enabled: boolean) {
  const queryClient = useQueryClient();
  const session = useSession();
  const me = session.user?.id;
  const query = useQuery({
    // The user id joins the key and gates the read: the queue is only
    // meaningful for an authenticated session, and the filter inside the
    // queryFn never runs with a half-initialized identity.
    queryKey: ["change-orders", "approval-queue", me],
    queryFn: async () => {
      // §18.58 owner ruling: the queue lists EVERY order involving the user
      // as a frozen approver — decided steps stay visible. No server-side
      // `state` narrowing (the relation is about the user, not the order's
      // lifecycle); the frozen-actor narrowing stays client-side
      // (presentation only). Boundary: the 200-row newest-first cap is
      // documented in the migration contract §14.3.
      const page = (await listChangeOrders({
        limit: 200,
      })) as unknown as {
        items: ChangeOrder[];
      };
      if (me === undefined) return [];
      return page.items.filter((order) =>
        order.stages.some((stage) =>
          stageApprovalSteps(stage).some((step) => step.actors.some((actor) => holdsSessionSlot(actor.id, me))),
        ),
      );
    },
    enabled: enabled && me !== undefined,
  });

  // Peer decisions arrive as aggregate events; re-reading the relation list
  // is the only state update (events are notifications, api/events/README).
  const orders = query.data;
  const subscriptionKey =
    orders === undefined ? "" : orders.map((order) => `${order.id}:${String(order.version)}`).join("|");
  useEffect(() => {
    if (subscriptionKey === "") return;
    const client = getReviewEventClient();
    const unsubscribes = (orders ?? []).map((order) =>
      client.subscribe(`change-orders/${order.id}`, () => {
        void queryClient.invalidateQueries({ queryKey: ["change-orders", "approval-queue"] });
      }),
    );
    return () => {
      for (const unsubscribe of unsubscribes) unsubscribe();
    };
  }, [subscriptionKey, orders, queryClient]);

  return query;
}

export interface ApprovalDecisionInput {
  decision: "approve" | "reject";
  comment: string;
}

export function useApprovalDecision(order: ChangeOrder | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ApprovalDecisionInput) =>
      decideChangeOrder(
        order?.id as string,
        { decision: input.decision, comment: input.comment },
        {
          // Both headers are declared required on approval-decisions —
          // If-Match guards the aggregate version, Idempotency-Key makes the
          // decision retry-safe (order_decision profile).
          headers: {
            "If-Match": `"${String(order?.version ?? 1)}"`,
            "Idempotency-Key": `order-decision-${randomUUID()}`,
          },
        },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-orders", "approval-queue"] });
      void queryClient.invalidateQueries({ queryKey: ["change-orders", "mine"] });
    },
  });
}

export function useOrderComments(orderId: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["change-order-comments", orderId],
    queryFn: async () => {
      const page = (await listChangeOrderComments(orderId, { limit: 50 })) as unknown as {
        items: ChangeOrderComment[];
      };
      return page.items;
    },
    enabled: enabled && orderId !== "",
  });

  useDomainEvent(
    orderId === "" ? null : `change-orders/${orderId}`,
    "any",
    () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order-comments", orderId] });
    },
  );

  return query;
}

export function useCreateOrderComment(orderId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (content: string) =>
      createChangeOrderComment(
        orderId,
        { content },
        { headers: { "Idempotency-Key": `order-comment-${randomUUID()}` } },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order-comments", orderId] });
    },
  });
}

/** Frozen submission findings for the approval page (R003 reuse — this reads
 * the stage review snapshots frozen at submission; it never starts a run). */
export function useOrderReviewFindings(orderId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["change-order-review-findings", orderId],
    queryFn: async () =>
      fetchAllCursorPages(async (after) =>
        (await listOrderReviewFindings(orderId, {
          limit: 200,
          ...(after === null ? {} : { after }),
        })) as unknown as CursorPage<ReviewFinding>,
      ),
    enabled: enabled && orderId !== "",
  });
}

export interface OrderSqlReveal {
  revealId: string;
  sql: string;
  watermark: string;
  validUntil: string;
}

/**
 * Order-side SQL plaintext reveal (PRD F7 item 1 — the reviewer judges the
 * exact frozen SQL Revision). Same controlled-reveal contract as the draft
 * workspace: per-view authorization (sensitive_reveal profile), watermark,
 * 5-minute validity, plaintext kept in component memory only and copy events
 * audited server-side without content.
 */
export function useRevealOrderSql(orderId: string) {
  return useMutation({
    mutationFn: async (purpose: string) => {
      const data = (await revealOrderSql(orderId, { purpose })) as unknown as {
        reveal_id: string;
        sql: string;
        watermark: string;
        valid_until: string;
      };
      return {
        revealId: data.reveal_id,
        sql: data.sql,
        watermark: data.watermark,
        validUntil: data.valid_until,
      } satisfies OrderSqlReveal;
    },
  });
}

export function useRecordOrderSqlCopy(orderId: string) {
  return useMutation({
    mutationFn: (sourceRevealId: string) =>
      recordOrderSqlCopy(orderId, { source_reveal_id: sourceRevealId }),
  });
}
