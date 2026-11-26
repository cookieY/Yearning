import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import {
  currentIdentity,
  currentIdentityId,
  holdsApprovalSlot,
} from "@/shared/mock/identity-store";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import {
  businessError,
  emit,
  orderPublic,
  pageOf,
  recordOrderEvent,
  successEnvelope,
  world,
} from "./state";
import type { FixtureComment } from "./types";

export function reviewApprovalHandlers(): HttpHandler[] {
  return [
    http.post(apiRoute("/change-orders/:orderId/approval-decisions"), async ({ request, params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      if (request.headers.get("If-Match") !== `"${String(order.version)}"`) {
        return businessError(1004, "order changed elsewhere");
      }
      const body = (await request.json()) as { decision: string; comment?: string };
      if (body.decision !== "approve" && body.decision !== "reject") {
        return businessError(1001, "decision must be approve or reject");
      }
      if (order.state !== "stage_approval_active") {
        return businessError(1010, `decision is not legal from state ${order.state}`);
      }
      const stage = order.stages.find((candidate) => candidate.state === "approval_active");
      const step = stage?.approval_steps.find((candidate) => candidate.state === "active");
      if (stage === undefined || step === undefined) {
        return businessError(1010, "no active approval step");
      }
      if (!step.actors.some((actor) => holdsApprovalSlot(actor.id, request))) {
        return businessError(3001, "current user is not a frozen reviewer of this step");
      }
      if (step.decided_at !== null) {
        return businessError(3002, "approval step already decided");
      }
      const maxPosition = Math.max(...stage.approval_steps.map((candidate) => candidate.position));
      const isNonFinalApprove = body.decision === "approve" && step.position < maxPosition;
      const hasNextPending =
        !isNonFinalApprove ||
        stage.approval_steps.some(
          (candidate) => candidate.position === step.position + 1 && candidate.state === "pending",
        );
      if (!hasNextPending) {
        return businessError(1004, "approval step chain broken");
      }
      const decidedAt = now();
      step.decided_at = decidedAt;
      step.state = body.decision === "approve" ? "approved" : "rejected";
      order.version += 1;
      const from = order.state;
      const decisionSummary = `审批人 ${currentIdentity().username} ${body.decision === "approve" ? "通过" : "拒绝"}阶段 ${String(stage.position)} 审批步 ${String(step.position)}`;
      const decidedComment = body.comment?.trim() ? body.comment : null;
      const decidedEmit = (): void => {
        recordOrderEvent(order, "change_order.approval_decided", "user", currentIdentityId(), decisionSummary, stage.position, null, decidedComment);
        emit(
          `change-orders/${order.id}`,
          "io.yearning.v4.change_order.approval_decided",
          {
            aggregate_id: order.id,
            stage_id: stage.id,
            step_id: step.id,
            reviewer_user_id: currentIdentityId(),
            decision: body.decision,
            aggregate_version: order.version,
          },
          { kind: "user", user_id: currentIdentityId() },
        );
      };
      if (body.decision === "approve") {
        if (isNonFinalApprove) {
          const next = stage.approval_steps.find(
            (candidate) => candidate.position === step.position + 1 && candidate.state === "pending",
          );
          if (next === undefined) return businessError(1004, "approval step chain broken");
          next.state = "active";
          decidedEmit();
          recordOrderEvent(order, "change_order.approval_step_activated", "system", null, `阶段 ${String(stage.position)} 审批步 ${String(next.position)} 开始审批`, stage.position, null);
        } else {
          stage.state = "execution_pending";
          order.state = "stage_execution_pending";
          recordOrderEvent(order, "change_order.stage_ready_for_execution", "system", null, `阶段 ${String(stage.position)} 终步通过，工单等待执行（执行需冻结执行人另点执行）`, stage.position, order.state);
          emit(
            `change-orders/${order.id}`,
            "io.yearning.v4.change_order.state_changed",
            {
              aggregate_id: order.id,
              from,
              to: order.state,
              reason_code: "final_step_approved",
              aggregate_version: order.version,
            },
            { kind: "system" },
          );
          decidedEmit();
        }
      } else {
        for (const candidate of stage.approval_steps) {
          if (candidate.state === "pending" || candidate.state === "active") {
            candidate.state = "skipped";
            candidate.decided_at = decidedAt;
          }
        }
        stage.state = "cancelled";
        order.state = "rejected";
        order.terminal_at = decidedAt;
        decidedEmit();
        recordOrderEvent(order, "change_order.rejected", "user", currentIdentityId(), "审批拒绝：任一拒绝立即拒绝整单", stage.position, order.state);
        emit(
          `change-orders/${order.id}`,
          "io.yearning.v4.change_order.state_changed",
          {
            aggregate_id: order.id,
            from,
            to: order.state,
            reason_code: "approval_rejected",
            aggregate_version: order.version,
          },
          { kind: "user", user_id: currentIdentityId() },
        );
      }
      return HttpResponse.json(successEnvelope(orderPublic(order, request)));
    }),

    http.get(apiRoute("/change-orders/:orderId/comments"), ({ params, request }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get("limit") ?? "50");
      const after = url.searchParams.get("after");
      const entries = [...(world.orderComments.get(order.id) ?? [])].reverse();
      return HttpResponse.json(successEnvelope(pageOf(entries, limit, after)));
    }),

    http.post(apiRoute("/change-orders/:orderId/comments"), async ({ request, params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      const body = (await request.json()) as { content: string };
      if (body.content === "" || body.content.length > 4096) {
        return businessError(1001, "content must be 1..4096 characters");
      }
      const comment: FixtureComment = {
        id: uuid(),
        order_id: order.id,
        author_user_id: currentIdentityId(),
        author_display_name: currentIdentity().username,
        content: body.content,
        occurred_at: now(),
      };
      const entries = world.orderComments.get(order.id) ?? [];
      entries.push(comment);
      world.orderComments.set(order.id, entries);
      return HttpResponse.json(successEnvelope(comment));
    }),

    http.get(apiRoute("/change-orders/:orderId/review-findings"), ({ params, request }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get("limit") ?? "50");
      const after = url.searchParams.get("after");
      const findings =
        order.review_run_id === null ? [] : (world.findings.get(order.review_run_id) ?? []);
      return HttpResponse.json(successEnvelope(pageOf(findings, limit, after)));
    }),

    http.post(apiRoute("/change-orders/:orderId/sql-reveals"), async ({ request, params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      const body = (await request.json()) as { purpose?: string };
      const purpose = typeof body.purpose === "string" ? body.purpose : "";
      if (purpose === "" || purpose.length > 256) {
        return businessError(1001, "purpose is required (1..256)");
      }
      const revealId = uuid();
      return HttpResponse.json(
        successEnvelope({
          reveal_id: revealId,
          sql: order.sql_text,
          watermark: `Yearning SQL Viewer: ${currentIdentity().username} @ ${now()}`,
          valid_until: new Date(Date.now() + 5 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z"),
        }),
      );
    }),

    http.post(apiRoute("/change-orders/:orderId/sql-copy-events"), async ({ request, params }) => {
      const order = world.orders.get(String(params.orderId));
      if (order === undefined) return businessError(1002, "order not found");
      const body = (await request.json()) as { source_reveal_id?: string };
      if (body.source_reveal_id === undefined || body.source_reveal_id === "") {
        return businessError(1001, "source_reveal_id is required");
      }
      return HttpResponse.json(successEnvelope(null));
    }),
  ];
}
