import { HttpResponse, http } from "msw";
import type { DefaultBodyType, HttpHandler } from "msw";
import type {
  AssistantCapability,
  AssistantCitation,
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
import { CATALOG_DATASOURCES, CATALOG_FLOWS } from "@/shared/mock/flow-catalog";
import { currentIdentityId } from "@/shared/mock/identity-store";

/**
 * Stateful browser MSW fixture backing the Database AI Assistant panel
 * (`/api/v4/ai-assistant/*`) for local mock development and the public
 * pure-frontend interactive demo. Supports realistic SSE streaming frames
 * (`progress`, `tool`, `reasoning`, `delta`, `done`) plus SQL draft precheck
 * and change-order handoff.
 */

const ASSISTANT_REQUEST_ID = "56565656-5656-4656-8656-565656565656";
const SEED_SESSION_ID = "a1111111-1111-4111-8111-111111110001";
const SEED_USER_MSG_ID = "a1111111-1111-4111-8111-111111110101";
const SEED_ASSISTANT_MSG_ID = "a1111111-1111-4111-8111-111111110102";
const SEED_DRAFT_ID = "a1111111-1111-4111-8111-111111110201";
const SEED_SUBMITTED_ORDER_ID = "7e6f1a2b-0000-4000-8000-900000000001";
const DEFAULT_DS = CATALOG_DATASOURCES[0];
const DEFAULT_FLOW = CATALOG_FLOWS[0];

const DEMO_HMAC =
  "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";

interface AssistantSessionRecord {
  session: AssistantSession;
  messages: AssistantMessage[];
  drafts: AssistantSqlDraft[];
}

interface AssistantWorld {
  sessions: Map<string, AssistantSessionRecord>;
  seq: number;
}

function nowIso(): string {
  return new Date().toISOString();
}

function nextUuid(prefix: string): string {
  world.seq += 1;
  const tail = String(world.seq).padStart(12, "0");
  return `${prefix}-1111-4111-8111-${tail}`;
}

function successEnvelope(data: DefaultBodyType) {
  return {
    err_code: 0,
    message: "ok",
    data,
    request_id: ASSISTANT_REQUEST_ID,
  };
}

function businessError(errCode: number, message: string) {
  return {
    err_code: errCode,
    message,
    data: null,
    request_id: ASSISTANT_REQUEST_ID,
    retryable: false,
  };
}

function buildSeedWorld(): AssistantWorld {
  const createdAt = "2026-11-19T09:15:00Z";
  const replyAt = "2026-11-19T09:15:06Z";
  const expiresAt = "2026-12-19T09:15:00Z";
  const dsId = DEFAULT_DS?.id ?? "4f6f1a2b-0000-4000-8000-00000000c101";
  const dsName = DEFAULT_DS?.name ?? "orders-primary";
  const flowId = DEFAULT_FLOW?.id ?? "4f6f1a2b-0000-4000-8000-00000000d201";

  const seedDraft: AssistantSqlDraft = {
    id: SEED_DRAFT_ID,
    session_id: SEED_SESSION_ID,
    message_id: SEED_ASSISTANT_MSG_ID,
    revision: 1,
    datasource_id: dsId,
    datasource_name: dsName,
    flow_id: flowId,
    engine: "mysql",
    schema_name: "orders_db",
    statement_kind: "create_index",
    risk_category: "ddl",
    sql_text:
      "CREATE INDEX idx_orders_customer_status_created\n  ON orders_db.orders (customer_id, status, created_at);",
    sql_hmac: DEMO_HMAC,
    referenced_tables: ["orders"],
    assumptions: [
      "orders_db.orders runs on InnoDB (MySQL 8.0) with ALGORITHM=INPLACE online DDL supported.",
      "Filtering predicates consistently lead with customer_id and status before ordering by created_at DESC.",
    ],
    semantic_warnings: [
      "Verify write throughput headroom on orders_db.orders before executing during peak traffic.",
    ],
    next_steps: [
      "Run the formal AI precheck to verify deterministic rules and online DDL guardrails.",
      "Submit the verified draft directly into the authorized change review workflow.",
    ],
    submission_state: "draft",
    ephemeral_run_id: null,
    submitted_order_id: null,
    submitted_order_display_no: null,
    version: 1,
    created_at: replyAt,
    updated_at: replyAt,
  };

  const seedSession: AssistantSession = {
    id: SEED_SESSION_ID,
    user_id: currentIdentityId(),
    title: "Optimize orders lookup & composite index",
    state: "active",
    active_datasource_id: dsId,
    active_flow_id: flowId,
    active_schema_name: "orders_db",
    selected_draft_id: SEED_DRAFT_ID,
    compacted_through_seq: 0,
    expires_at: expiresAt,
    version: 2,
    created_at: createdAt,
    updated_at: replyAt,
  };

  const userMsg: AssistantMessage = {
    id: SEED_USER_MSG_ID,
    session_id: SEED_SESSION_ID,
    sequence: 1,
    role: "user",
    capability: "sql_optimize",
    state: "completed",
    content:
      "Our order history query `SELECT id, total_cents, status FROM orders WHERE customer_id = ? AND status = 'paid' ORDER BY created_at DESC LIMIT 20` is scanning 180k rows. Can you draft a safe covering index?",
    citations: [],
    sql_draft: null,
    failure_code: null,
    created_at: createdAt,
  };

  const assistantMsg: AssistantMessage = {
    id: SEED_ASSISTANT_MSG_ID,
    session_id: SEED_SESSION_ID,
    sequence: 2,
    role: "assistant",
    capability: "sql_optimize",
    state: "completed",
    content: [
      "### Root Cause Analysis",
      "The existing table only indexes `PRIMARY (id)` and `idx_created_at (created_at)`. Filtering by `customer_id` and `status` triggers a broad range scan plus `Using filesort`.",
      "",
      "### Recommended Index Strategy",
      "1. Place equality predicates (`customer_id`, `status`) first, followed by the sort key (`created_at`) so InnoDB serves `ORDER BY created_at DESC LIMIT 20` via backward index scan with zero filesort.",
      "2. Expected reduction: examined rows drop from **~180,000** to **<= 20** per execution.",
      "",
      "I prepared an online `CREATE INDEX` draft card below. You can run the **Formal AI Precheck** and advance it straight into a governed Change Order.",
    ].join("\n"),
    citations: [
      {
        citation_id: "cit-schema-orders",
        title: "Schema Metadata: orders_db.orders",
        source_ref: "schema://orders_db/orders",
        route_path: "/query",
      },
      {
        citation_id: "cit-guide-index-policy",
        title: "Yearning v4 Online DDL & Index Review Guidelines",
        source_ref: "docs://review/online-ddl",
        route_path: "/changes",
      },
    ],
    sql_draft: seedDraft,
    failure_code: null,
    created_at: replyAt,
  };

  const sessions = new Map<string, AssistantSessionRecord>();
  sessions.set(SEED_SESSION_ID, {
    session: seedSession,
    messages: [userMsg, assistantMsg],
    drafts: [seedDraft],
  });

  return { sessions, seq: 100 };
}

let world: AssistantWorld = buildSeedWorld();

export function resetAssistantFixture(): void {
  world = buildSeedWorld();
}

function resolveCapability(body: SendAssistantMessageRequest): AssistantCapability {
  if (body.capability_hint) return body.capability_hint;
  const lower = body.content.toLowerCase();
  if (
    lower.includes("how") ||
    lower.includes("approval") ||
    lower.includes("workflow") ||
    lower.includes("permission") ||
    lower.includes("flow")
  ) {
    return "platform_help";
  }
  if (
    lower.includes("slow") ||
    lower.includes("index") ||
    lower.includes("explain") ||
    lower.includes("optim") ||
    lower.includes("scan")
  ) {
    return "sql_optimize";
  }
  return "sql_generate";
}

function buildSimulatedReply(
  capability: AssistantCapability,
  record: AssistantSessionRecord,
  body: SendAssistantMessageRequest,
  assistantMsgId: string,
): {
  reasoning: string;
  toolName: string;
  content: string;
  citations: AssistantCitation[];
  draft: AssistantSqlDraft | null;
} {
  const dsId =
    body.datasource_id ??
    record.session.active_datasource_id ??
    DEFAULT_DS?.id ??
    "4f6f1a2b-0000-4000-8000-00000000c101";
  const ds = CATALOG_DATASOURCES.find((item) => item.id === dsId) ?? DEFAULT_DS;
  const dsName = ds?.name ?? "orders-primary";
  const engine = ds?.engine ?? "mysql";
  const schemaName = body.schema_name ?? record.session.active_schema_name ?? "orders_db";
  const flowId = body.flow_id ?? record.session.active_flow_id ?? DEFAULT_FLOW?.id ?? null;
  const ts = nowIso();

  if (capability === "platform_help") {
    return {
      toolName: "knowledge_lookup",
      reasoning:
        "Searching embedded Yearning v4 governance handbook for workflow stages, AI review gates, and execution controls.",
      content: [
        "### Yearning v4 Change Governance Workflow",
        "1. **Draft & AI Precheck**: Write or generate SQL in the Change Workbench, then run an ephemeral AI + deterministic rule precheck before submitting.",
        "2. **Multi-Stage Approval**: Each Change Flow defines ordered datasource stages with frozen approvers and execution actors.",
        "3. **Safe Execution & Rollback**: Approved stages execute with sanitized statement ledgers, optional `gh-ost` / `pt-osc` online DDL guardrails, and automatic DML rollback capture.",
        "",
        "You can inspect all active orders in **Changes** or explore slow query candidates in **Admin → SQL Diagnostics**.",
      ].join("\n"),
      citations: [
        {
          citation_id: "cit-help-workflow",
          title: "Change Review & Approval Workflow",
          source_ref: "help://change-workflow",
          route_path: "/changes",
        },
        {
          citation_id: "cit-help-diagnostics",
          title: "SQL Performance Diagnostics Overview",
          source_ref: "help://sql-diagnostics",
          route_path: "/admin/diagnostics",
        },
      ],
      draft: null,
    };
  }

  if (capability === "sql_optimize") {
    const draftId = nextUuid("a2222222");
    const draft: AssistantSqlDraft = {
      id: draftId,
      session_id: record.session.id,
      message_id: assistantMsgId,
      revision: 1,
      datasource_id: dsId,
      datasource_name: dsName,
      flow_id: flowId,
      engine,
      schema_name: schemaName,
      statement_kind: "create_index",
      risk_category: "ddl",
      sql_text:
        `CREATE INDEX idx_orders_merchant_created\n  ON ${schemaName}.orders (merchant_id, created_at DESC);`,
      sql_hmac: DEMO_HMAC,
      referenced_tables: ["orders"],
      assumptions: [
        `Target schema ${schemaName} contains the orders table with merchant_id cardinality > 5%.`,
        "Non-blocking online index build is permitted in the active review policy.",
      ],
      semantic_warnings: [
        "Confirm no redundant composite index already starts with (merchant_id, created_at).",
      ],
      next_steps: [
        "Click Run Formal AI Precheck on the SQL draft card below.",
        "Review findings and submit directly into the authorized Change Flow.",
      ],
      submission_state: "draft",
      ephemeral_run_id: null,
      submitted_order_id: null,
      submitted_order_display_no: null,
      version: 1,
      created_at: ts,
      updated_at: ts,
    };
    return {
      toolName: "schema_inspect",
      reasoning:
        "Inspecting table metadata, selectivity estimates, and index definitions in the authorized schema without accessing raw business rows.",
      content: [
        "### Query Optimization Summary",
        "- **Bottleneck**: Leading predicate lacks a matching composite index, causing full index/table scan and temporary sort buffer usage.",
        "- **Fix**: Add a narrow composite index aligned with the equality filter + sort order, and avoid `SELECT *` on wide columns.",
        "",
        "A ready-to-precheck DDL draft is attached below.",
      ].join("\n"),
      citations: [
        {
          citation_id: "cit-opt-schema",
          title: `Schema Inspection: ${schemaName}.orders`,
          source_ref: `schema://${schemaName}/orders`,
          route_path: "/query",
        },
      ],
      draft,
    };
  }

  const draftId = nextUuid("a3333333");
  const draft: AssistantSqlDraft = {
    id: draftId,
    session_id: record.session.id,
    message_id: assistantMsgId,
    revision: 1,
    datasource_id: dsId,
    datasource_name: dsName,
    flow_id: flowId,
    engine,
    schema_name: schemaName,
    statement_kind: "alter_table",
    risk_category: "ddl",
    sql_text:
      `ALTER TABLE ${schemaName}.orders\n  ADD COLUMN fulfillment_channel VARCHAR(32) NOT NULL DEFAULT 'standard'\n  COMMENT 'Order fulfillment channel identifier';`,
    sql_hmac: DEMO_HMAC,
    referenced_tables: ["orders"],
    assumptions: [
      `Target datasource ${dsName} supports instant/online column addition with a constant DEFAULT.`,
      "Column comment satisfies Yearning v4 deterministic DDL audit rules.",
    ],
    semantic_warnings: [
      "Confirm downstream ETL pipelines tolerate the new non-null column with default value.",
    ],
    next_steps: [
      "Run Formal AI Precheck to validate against the selected flow rule set.",
      "Submit to generate a governed Change Order with frozen review evidence.",
    ],
    submission_state: "draft",
    ephemeral_run_id: null,
    submitted_order_id: null,
    submitted_order_display_no: null,
    version: 1,
    created_at: ts,
    updated_at: ts,
  };

  return {
    toolName: "schema_inspect",
    reasoning:
      "Resolving authorized datasource schema metadata and synthesizing a policy-compliant SQL draft with explicit column defaults and comments.",
    content: [
      "### Generated Governed SQL Draft",
      "I inspected the authorized schema metadata and drafted a policy-compliant DDL statement with an explicit `NOT NULL DEFAULT` value and column `COMMENT` so it passes Yearning v4's deterministic audit rules cleanly.",
      "",
      "Use the draft card below to run **Formal AI Precheck** or hand off straight into a **Change Order**.",
    ].join("\n"),
    citations: [
      {
        citation_id: "cit-gen-schema",
        title: `Authorized Schema: ${schemaName}`,
        source_ref: `schema://${schemaName}`,
        route_path: "/query",
      },
    ],
    draft,
  };
}

function encodeSseFrame(event: string, payload: unknown): Uint8Array {
  return new TextEncoder().encode(
    `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`,
  );
}

const pause = (ms: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, ms));

export function assistantFixtureHandlers(): HttpHandler[] {
  return [
    http.get("*/ai-assistant/sessions", () => {
      const items = [...world.sessions.values()]
        .map((entry) => entry.session)
        .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
      return HttpResponse.json(
        successEnvelope({
          items,
          page: { has_more: false, next_cursor: null },
        }),
      );
    }),

    http.post("*/ai-assistant/sessions", async ({ request }) => {
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
      world.sessions.set(id, { session, messages: [], drafts: [] });
      return HttpResponse.json(successEnvelope(session));
    }),

    http.get("*/ai-assistant/sessions/:sessionId", ({ params }) => {
      const sessionId = String(params.sessionId);
      const record = world.sessions.get(sessionId);
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

    http.delete("*/ai-assistant/sessions/:sessionId", ({ params }) => {
      const sessionId = String(params.sessionId);
      world.sessions.delete(sessionId);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post("*/ai-assistant/sessions/:sessionId/messages", async ({ params, request }) => {
      const sessionId = String(params.sessionId);
      const record = world.sessions.get(sessionId);
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
        headers: {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
        },
      });
    }),

    http.post(
      "*/ai-assistant/sessions/:sessionId/messages/:messageId/cancellation",
      () => HttpResponse.json(successEnvelope({ cancelled: true })),
    ),

    http.post(
      "*/ai-assistant/sessions/:sessionId/drafts/:draftId/prechecks",
      async ({ params, request }) => {
        const sessionId = String(params.sessionId);
        const draftId = String(params.draftId);
        const record = world.sessions.get(sessionId);
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
      "*/ai-assistant/sessions/:sessionId/drafts/:draftId/order-submissions",
      async ({ params, request }) => {
        const sessionId = String(params.sessionId);
        const draftId = String(params.draftId);
        const record = world.sessions.get(sessionId);
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
          stages: [],
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
