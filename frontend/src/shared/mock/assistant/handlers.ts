import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import type {
  AssistantMessage,
  AssistantSession,
  AssistantSessionDetail,
  AssistantSqlDraft,
  ChangeOrder,
  CreateAssistantSessionRequest,
  PrecheckAssistantSqlDraftRequest,
  PrecheckAssistantSqlDraftResponse,
  SendAssistantMessageRequest,
  SendAssistantMessageResponse,
  SubmitAssistantSqlDraftOrderRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { apiRoute } from "@/shared/mock/api-route";
import { currentIdentityId } from "@/shared/mock/identity-store";
import { nowIso } from "@/shared/mock/kit";
import {
  DEFAULT_DS,
  DEFAULT_FLOW,
  DEMO_HMAC,
  SEED_SUBMITTED_ORDER_ID,
  assistantState,
  buildSimulatedReply,
  businessError,
  nextUuid,
  resolveCapability,
  successEnvelope,
} from "./state";

function encodeSseFrame(event: string, payload: unknown): Uint8Array {
  return new TextEncoder().encode(
    `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`,
  );
}

const pause = (ms: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, ms));

export function assistantFixtureHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/ai-assistant/sessions"), () => {
      const items = [...assistantState.world.sessions.values()]
        .map((entry) => entry.session)
        .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
      return HttpResponse.json(
        successEnvelope({
          items,
          page: { has_more: false, next_cursor: null },
        }),
      );
    }),

    http.post(apiRoute("/ai-assistant/sessions"), async ({ request }) => {
      const body = (await request.json().catch(() => ({}))) as CreateAssistantSessionRequest;
      const id = nextUuid("a1111111");
      const ts = nowIso();
      const session: AssistantSession = {
        id,
        user_id: currentIdentityId(),
        title: body.title?.trim() || "New Database Assistant Session",
        state: "active",
        active_datasource_id:
          body.active_datasource_id ?? DEFAULT_DS?.id ?? null,
        active_flow_id: body.active_flow_id ?? DEFAULT_FLOW?.id ?? null,
        active_schema_name: body.active_schema_name ?? "orders_db",
        selected_draft_id: null,
        compacted_through_seq: 0,
        expires_at: "2026-12-31T00:00:00Z",
        version: 1,
        created_at: ts,
        updated_at: ts,
      };
      assistantState.world.sessions.set(id, { session, messages: [], drafts: [] });
      return HttpResponse.json(successEnvelope(session));
    }),

    http.get(apiRoute("/ai-assistant/sessions/:sessionId"), ({ params }) => {
      const sessionId = String(params.sessionId);
      const record = assistantState.world.sessions.get(sessionId);
      if (!record) {
        return HttpResponse.json(businessError(1002, "assistant session not found"));
      }
      const detail: AssistantSessionDetail = {
        session: record.session,
        messages: record.messages,
        drafts: record.drafts,
      };
      return HttpResponse.json(successEnvelope(detail));
    }),

    http.delete(apiRoute("/ai-assistant/sessions/:sessionId"), ({ params }) => {
      const sessionId = String(params.sessionId);
      assistantState.world.sessions.delete(sessionId);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post(apiRoute("/ai-assistant/sessions/:sessionId/messages"), async ({ params, request }) => {
      const sessionId = String(params.sessionId);
      const record = assistantState.world.sessions.get(sessionId);
      if (!record) {
        return HttpResponse.json(businessError(1002, "assistant session not found"));
      }
      const body = (await request.json().catch(() => null)) as SendAssistantMessageRequest | null;
      if (!body || !body.content || body.content.trim() === "") {
        return HttpResponse.json(businessError(1001, "message content is required"));
      }

      const capability = resolveCapability(body);
      const ts = nowIso();
      const userMsgId = nextUuid("a4444444");
      const assistantMsgId = nextUuid("a5555555");
      const nextSeq = record.messages.length + 1;

      const userMessage: AssistantMessage = {
        id: userMsgId,
        session_id: sessionId,
        sequence: nextSeq,
        role: "user",
        capability,
        state: "completed",
        content: body.content,
        citations: [],
        sql_draft: null,
        failure_code: null,
        created_at: ts,
      };

      const simulated = buildSimulatedReply(capability, record, body, assistantMsgId);
      const assistantMessage: AssistantMessage = {
        id: assistantMsgId,
        session_id: sessionId,
        sequence: nextSeq + 1,
        role: "assistant",
        capability,
        state: "completed",
        content: simulated.content,
        citations: simulated.citations,
        sql_draft: simulated.draft,
        failure_code: null,
        created_at: nowIso(),
      };

      if (record.messages.length === 0) {
        record.session = {
          ...record.session,
          title: body.content.trim().slice(0, 48),
        };
      }
      record.session = {
        ...record.session,
        active_datasource_id:
          body.datasource_id ?? record.session.active_datasource_id,
        active_flow_id: body.flow_id ?? record.session.active_flow_id,
        active_schema_name:
          body.schema_name ?? record.session.active_schema_name,
        selected_draft_id:
          simulated.draft?.id ?? record.session.selected_draft_id,
        version: record.session.version + 1,
        updated_at: assistantMessage.created_at,
      };
      record.messages = [...record.messages, userMessage, assistantMessage];
      if (simulated.draft !== null) {
        record.drafts = [...record.drafts, simulated.draft];
      }

      const donePayload: SendAssistantMessageResponse = {
        session: record.session,
        user_message: userMessage,
        assistant_message: assistantMessage,
        sql_draft: simulated.draft,
        context_compacted: false,
      };

      const accept = (request.headers.get("accept") ?? "").toLowerCase();
      if (!accept.includes("text/event-stream")) {
        return HttpResponse.json(successEnvelope(donePayload));
      }

      const stream = new ReadableStream<Uint8Array>({
        async start(controller) {
          try {
            controller.enqueue(
              encodeSseFrame("progress", {
                phase: "analyzing",
                message_id: assistantMsgId,
              }),
            );
            await pause(120);
            controller.enqueue(
              encodeSseFrame("tool", {
                tool: simulated.toolName,
                status: "running",
                capability,
                schema: record.session.active_schema_name ?? "orders_db",
              }),
            );
            await pause(180);
            controller.enqueue(
              encodeSseFrame("tool", {
                tool: simulated.toolName,
                status: "completed",
                capability,
                schema: record.session.active_schema_name ?? "orders_db",
                tables: 4,
              }),
            );
            controller.enqueue(
              encodeSseFrame("reasoning", {
                status: "running",
                delta: simulated.reasoning,
              }),
            );
            await pause(160);
            controller.enqueue(
              encodeSseFrame("reasoning", { status: "completed" }),
            );

            // Stream markdown content in crisp chunks for natural typewriter pacing.
            const chunks = simulated.content.match(/.{1,36}/gs) ?? [simulated.content];
            for (const chunk of chunks) {
              controller.enqueue(encodeSseFrame("delta", { delta: chunk }));
              await pause(24);
            }

            controller.enqueue(encodeSseFrame("done", donePayload));
            controller.close();
          } catch {
            controller.close();
          }
        },
      });

      return new HttpResponse(stream, {
        status: 200,
        headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" },
      });
    }),

    http.post(apiRoute("/ai-assistant/sessions/:sessionId/messages/:messageId/cancellation"), ({ params }) => {
      const record = assistantState.world.sessions.get(String(params.sessionId));
      const msg = record?.messages.find((m) => m.id === String(params.messageId));
      if (!msg) return HttpResponse.json(businessError(1002, record ? "assistant message not found" : "assistant session not found"));
      if (msg.state === "streaming") msg.state = "cancelled";
      return HttpResponse.json(successEnvelope(msg));
    }),

    http.post(
      apiRoute("/ai-assistant/sessions/:sessionId/drafts/:draftId/prechecks"),
      async ({ params, request }) => {
        const sessionId = String(params.sessionId);
        const draftId = String(params.draftId);
        const record = assistantState.world.sessions.get(sessionId);
        if (!record) {
          return HttpResponse.json(businessError(1002, "assistant session not found"));
        }
        const draftIndex = record.drafts.findIndex((item) => item.id === draftId);
        const existing = draftIndex === -1 ? undefined : record.drafts[draftIndex];
        if (!existing) {
          return HttpResponse.json(businessError(1002, "assistant draft not found"));
        }
        const body = (await request.json().catch(() => ({}))) as Partial<PrecheckAssistantSqlDraftRequest>;
        const runId = nextUuid("a6666666");
        const updatedDraft: AssistantSqlDraft = {
          ...existing,
          flow_id: body.flow_id ?? existing.flow_id,
          submission_state: "precheck_ready",
          ephemeral_run_id: runId,
          version: existing.version + 1,
          updated_at: nowIso(),
        };
        record.drafts[draftIndex] = updatedDraft;
        record.messages = record.messages.map((msg) =>
          msg.sql_draft?.id === draftId ? { ...msg, sql_draft: updatedDraft } : msg,
        );
        await pause(280);
        const response: PrecheckAssistantSqlDraftResponse = {
          draft: updatedDraft,
          ephemeral_run_id: runId,
          result_ref: `/review-runs/${runId}`,
        };
        return HttpResponse.json(successEnvelope(response));
      },
    ),

    http.post(
      apiRoute("/ai-assistant/sessions/:sessionId/drafts/:draftId/order-submissions"),
      async ({ params, request }) => {
        const sessionId = String(params.sessionId);
        const draftId = String(params.draftId);
        const record = assistantState.world.sessions.get(sessionId);
        if (!record) {
          return HttpResponse.json(businessError(1002, "assistant session not found"));
        }
        const draftIndex = record.drafts.findIndex((item) => item.id === draftId);
        const existing = draftIndex === -1 ? undefined : record.drafts[draftIndex];
        if (!existing) {
          return HttpResponse.json(businessError(1002, "assistant draft not found"));
        }
        const body = (await request.json().catch(() => ({}))) as Partial<SubmitAssistantSqlDraftOrderRequest>;
        const displayNo = "YR-20260901-000001";
        const updatedDraft: AssistantSqlDraft = {
          ...existing,
          submission_state: "order_submitted",
          submitted_order_id: SEED_SUBMITTED_ORDER_ID,
          submitted_order_display_no: displayNo,
          version: existing.version + 1,
          updated_at: nowIso(),
        };
        record.drafts[draftIndex] = updatedDraft;
        record.messages = record.messages.map((msg) =>
          msg.sql_draft?.id === draftId ? { ...msg, sql_draft: updatedDraft } : msg,
        );
        await pause(220);

        const order: ChangeOrder = {
          id: SEED_SUBMITTED_ORDER_ID,
          display_number: displayNo,
          submitter_user_id: currentIdentityId(),
          requested_scheduled_for: body.requested_scheduled_for ?? null,
          title: body.title?.trim() || "AI Assistant Optimization Order",
          state: "submitted",
          current_stage_position: 1,
          stages: [{ id: "a7777777-7777-4777-8777-000000000011", position: 1, datasource_name: "orders-mysql", state: "approval_active", approval_steps: [{ id: "a7777777-7777-4777-8777-000000000021", position: 1, state: "active", decided_at: null, actors: [] }], execution_actors: [] }],
          has_sql: true,
          sql_hash: DEMO_HMAC,
          snapshot_hash: DEMO_HMAC,
          manually_verified: false,
          version: 1,
          submitted_at: nowIso(),
          terminal_at: null,
        };
        return HttpResponse.json(successEnvelope(order));
      },
    ),
  ];
}
