import { useEffect, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  ChangeOrder,
  ExecutionAttempt,
  ExecutionSchedule,
  ExecutionVerificationRequest,
  OscOverrides,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  cancelExecutionAttempt,
  cancelExecutionSchedule,
  createExecutionAttempt,
  createExecutionSchedule,
  createExecutionVerification,
  getExecutionAttempt,
  listExecutionAttemptStatements,
} from "@/api/generated/client/change-orders/change-orders";
import { ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import { matchesEntitySubject, useSseEvent } from "@/shared/events/sse-event-bus";
import { isAttemptTerminalState } from "@/features/orders/order-state";
import { randomUUID } from "@/shared/lib/uuid";

/**
 * Server-state hooks for the execution workspace (frontend PRD F8, W006,
 * E001–E007). The contract has no list-attempts read: the attempt id enters
 * the page through the creation response and stays in page state (F12 will
 * consume the same response), so every attempt read here is keyed by an
 * explicitly known id. All execution-domain events ride the order subject
 * (backend emitOrderEvent), which the order hooks already subscribe to —
 * attempt/statement queries only need invalidation on that same subject.
 */

const LIVE_POLL_MS = 500;

/** While an attempt is live the queries poll on the HTTP-polling baseline
 * (api-contracts): intermediate preflight/running transitions publish no
 * domain event of their own, only begin and the terminal outcome do — and a
 * settled cancellation publishes none at all (backend record.go), so the
 * order aggregate refreshes on the same tick. */
export function useExecutionAttempt(attemptId: string, orderId: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["execution-attempt", attemptId],
    queryFn: () => unwrapApi(getExecutionAttempt(attemptId)),
    enabled: enabled && attemptId !== "",
    refetchInterval: (queryInstance) => {
      const state = queryInstance.state.data?.state;
      return state !== undefined && !isAttemptTerminalState(state) ? LIVE_POLL_MS : false;
    },
  });

  // A settled cancellation publishes no domain event (backend record.go:
  // "completed cancellation emits no domain event; status is surfaced via API and read models") — while the attempt is
  // live, every poll tick also refreshes the order aggregate so the page
  // converges on the terminal fate without relying on events.
  const attemptState = query.data?.state;
  const live = attemptState !== undefined && !isAttemptTerminalState(attemptState);
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", orderId] });
    }, LIVE_POLL_MS);
    return () => {
      clearInterval(timer);
    };
  }, [live, orderId, queryClient]);
  // The live window closes the moment the attempt reads terminal; one final
  // order refresh captures the settled fate even when the last tick raced it
  // (a settled cancellation publishes no event to bridge the gap).
  const wasLive = useRef(false);
  useEffect(() => {
    if (live) {
      wasLive.current = true;
      return;
    }
    if (!wasLive.current) return;
    wasLive.current = false;
    void queryClient.invalidateQueries({ queryKey: ["change-order", orderId] });
  }, [live, orderId, queryClient]);

  // Events are notifications: the attempt and its statements re-read on every
  // execution fact published for the order (backend emitOrderEvent subject).
  useSseEvent(
    orderId === "" ? null : "*",
    (event) => {
      if (!matchesEntitySubject(event.subject, "change-orders", "order", orderId)) {
        return;
      }
      void queryClient.invalidateQueries({ queryKey: ["execution-attempt", attemptId] });
      void queryClient.invalidateQueries({ queryKey: ["execution-statements", attemptId] });
    },
  );

  return query;
}

/** Statement ledger for a known attempt. `poll` is the caller's "attempt is
 * live" knowledge — statements re-read on the polling baseline only while the
 * attempt is still progressing. */
export function useExecutionStatements(attemptId: string, enabled: boolean, poll: boolean) {
  return useQuery({
    queryKey: ["execution-statements", attemptId],
    queryFn: async () => {
      const page = await unwrapApi(listExecutionAttemptStatements(attemptId, { limit: 100 }));
      return page.items;
    },
    enabled: enabled && attemptId !== "",
    refetchInterval: poll && enabled && attemptId !== "" ? LIVE_POLL_MS : false,
  });
}

function orderActionRequestInit(order: ChangeOrder | null | undefined): RequestInit {
  return ifMatchHeader(order?.version, {
    "Idempotency-Key": `execution-${randomUUID()}`,
  });
}

export function useCreateExecutionAttempt(
  order: ChangeOrder | null,
  onCreated: (attemptId: string) => void,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { ddl_execution_mode?: "gh_ost" | "online_ddl"; osc_overrides?: OscOverrides }) =>
      unwrapApi(
        createExecutionAttempt(
          order?.id as string,
          Object.keys(input).length > 0 ? input : undefined,
          orderActionRequestInit(order),
        ),
      ),
    onSuccess: (attempt) => {
      onCreated(attempt.id);
      void queryClient.invalidateQueries({ queryKey: ["change-order", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", order?.id] });
    },
  });
}

export function useCancelExecutionAttempt(
  attempt: ExecutionAttempt | null,
  orderId: string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (reason: string) =>
      cancelExecutionAttempt(
        attempt?.id as string,
        { reason },
        ifMatchHeader(attempt?.version, {
          "Idempotency-Key": `execution-cancel-${randomUUID()}`,
        }),
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", orderId] });
      void queryClient.invalidateQueries({ queryKey: ["execution-attempt", attempt?.id] });
      void queryClient.invalidateQueries({ queryKey: ["execution-statements", attempt?.id] });
    },
  });
}

export function useCreateExecutionVerification(
  attempt: ExecutionAttempt | null,
  orderId: string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ExecutionVerificationRequest) =>
      createExecutionVerification(
        attempt?.id as string,
        input,
        ifMatchHeader(attempt?.version, {
          // If-Match guards the attempt version (backend verifyOnce), the
          // idempotency key makes the verdict retry-safe (execution_verify
          // profile).
          "Idempotency-Key": `execution-verify-${randomUUID()}`,
        }),
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", orderId] });
      void queryClient.invalidateQueries({ queryKey: ["execution-attempt", attempt?.id] });
      void queryClient.invalidateQueries({ queryKey: ["execution-statements", attempt?.id] });
    },
  });
}

export function useCreateExecutionSchedule(order: ChangeOrder | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (scheduledFor: string): Promise<ExecutionSchedule> =>
      unwrapApi(
        createExecutionSchedule(
          order?.id as string,
          { scheduled_for: scheduledFor },
          orderActionRequestInit(order),
        ),
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", order?.id] });
    },
  });
}

/** Cancel the live schedule (owner ruling 2026-10-30): any frozen executor,
 * order-keyed command (the schedule id never crosses a refresh), order back
 * to stage_execution_pending — re-schedule or execute immediately. */
export function useCancelExecutionSchedule(order: ChangeOrder | null) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (): Promise<ExecutionSchedule> =>
      unwrapApi(cancelExecutionSchedule(order?.id as string, orderActionRequestInit(order))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["change-order", order?.id] });
      void queryClient.invalidateQueries({ queryKey: ["change-order-timeline", order?.id] });
    },
  });
}
