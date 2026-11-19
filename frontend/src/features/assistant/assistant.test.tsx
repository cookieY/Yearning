import type { ReactNode } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import "@/shared/i18n";
import type {
  AssistantSession,
  AssistantSessionDetail,
  AssistantSqlDraft,
  ChangeOrder,
  Flow,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import * as assistantApi from "@/api/generated/client/ai-assistant/ai-assistant";
import * as changeDraftsApi from "@/api/generated/client/change-drafts/change-drafts";
import { AssistantMarkdown } from "./assistant-markdown";
import { AssistantPanel, unwrapAssistantReplyContent } from "./assistant-panel";
import { AssistantWidget } from "./assistant-widget";
import { SqlDraftCard } from "./sql-draft-card";
import {
  dispatchSSEFrame,
  extractAuthorizedDatasources,
  streamAssistantMessageSSE,
} from "./use-assistant";
import { describeError } from "@/shared/api/error-display";
import { BusinessError, TransportError } from "@/shared/api/mutator";
import i18n from "@/shared/i18n";

vi.mock("@/api/generated/client/ai-assistant/ai-assistant", () => ({
  listAssistantSessions: vi.fn(),
  createAssistantSession: vi.fn(),
  getAssistantSession: vi.fn(),
  deleteAssistantSession: vi.fn(),
  sendAssistantMessage: vi.fn(),
  getSendAssistantMessageUrl: (sessionId: string) =>
    `/api/v1/assistant/sessions/${sessionId}/messages`,
  cancelAssistantMessageStream: vi.fn(),
  precheckAssistantSqlDraft: vi.fn(),
  submitAssistantSqlDraftOrder: vi.fn(),
}));

vi.mock("@/api/generated/client/change-drafts/change-drafts", () => ({
  listCurrentUserFlows: vi.fn(),
}));

const SAMPLE_FLOW: Flow = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Prod Order Flow",
  flow_type: "change_review",
  enabled: true,
  status: "enabled",
  version: 1,
  created_at: "2026-11-07T00:00:00Z",
  updated_at: "2026-11-07T00:00:00Z",
  stages: [
    {
      position: 1,
      datasource_id: "22222222-2222-4222-8222-222222222222",
      datasource_name: "prod-mysql-warehouse",
      approval_steps: [],
      execution_actors: [],
    },
  ],
};

const SAMPLE_SESSION: AssistantSession = {
  id: "33333333-3333-4333-8333-333333333333",
  user_id: "99999999-9999-4999-8999-999999999999",
  title: "Insert warehouse record session",
  state: "active",
  active_datasource_id: "22222222-2222-4222-8222-222222222222",
  active_schema_name: "warehouse_db",
  active_flow_id: SAMPLE_FLOW.id,
  selected_draft_id: "44444444-4444-4444-8444-444444444444",
  compacted_through_seq: 0,
  version: 2,
  expires_at: "2026-12-07T00:00:00Z",
  created_at: "2026-11-07T00:00:00Z",
  updated_at: "2026-11-07T00:00:00Z",
};

const SAMPLE_DRAFT: AssistantSqlDraft = {
  id: "44444444-4444-4444-8444-444444444444",
  session_id: SAMPLE_SESSION.id,
  message_id: "55555555-5555-4555-8555-555555555555",
  revision: 1,
  datasource_id: "22222222-2222-4222-8222-222222222222",
  datasource_name: "prod-mysql-warehouse",
  engine: "mysql",
  schema_name: "warehouse_db",
  flow_id: SAMPLE_FLOW.id,
  statement_kind: "insert",
  risk_category: "dml",
  sql_text:
    "INSERT INTO `warehouse_db`.`depots` (`depot_code`, `city`) VALUES ('WH-SH-01', 'Shanghai')",
  sql_hmac: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
  referenced_tables: ["depots"],
  assumptions: ["id is an auto-increment primary key and does not need to be specified explicitly"],
  semantic_warnings: ["Please verify the unique index constraint on depot_code"],
  next_steps: ["Run the formal AI precheck before confirming change order submission"],
  submission_state: "draft",
  ephemeral_run_id: null,
  submitted_order_id: null,
  submitted_order_display_no: null,
  version: 1,
  created_at: "2026-11-07T00:00:00Z",
  updated_at: "2026-11-07T00:00:00Z",
};

function renderWithProviders(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Database AI Assistant (FE-F13-AI-ASSISTANT)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });
    vi.mocked(changeDraftsApi.listCurrentUserFlows).mockResolvedValue({
      items: [SAMPLE_FLOW],
    } as never);
    vi.mocked(assistantApi.getAssistantSession).mockResolvedValue({
      session: SAMPLE_SESSION,
      messages: [],
      drafts: [],
    } as never);
  });

  it("renders the dots-02 floating trigger and opens the assistant panel on click", async () => {
    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [],
      page: { has_more: false, next_cursor: null },
    } as never);

    renderWithProviders(<AssistantWidget />);
    const trigger = screen.getByTestId("database-assistant-trigger");
    expect(trigger).toBeInTheDocument();
    const dotImg = screen.getByRole("img", { name: "\u6253\u5f00\u6570\u636e\u5e93 AI \u52a9\u624b" });
    expect(dotImg).toHaveStyle({ width: "120px", height: "120px" });

    fireEvent.click(trigger);
    expect(await screen.findByTestId("database-assistant-panel")).toBeInTheDocument();
    expect(screen.getByTestId("assistant-starter-platform_help")).toBeInTheDocument();
    expect(screen.getByTestId("assistant-starter-sql_optimize")).toBeInTheDocument();
    expect(screen.getByTestId("assistant-starter-sql_generate")).toBeInTheDocument();

    // Window expand & restore toggle
    const popover = screen.getByTestId("database-assistant-desktop-popover");
    expect(popover).toHaveAttribute("data-expanded", "false");
    const expandBtn = screen.getByTestId("assistant-expand-toggle");
    fireEvent.click(expandBtn);
    expect(popover).toHaveAttribute("data-expanded", "true");
    fireEvent.click(expandBtn);
    expect(popover).toHaveAttribute("data-expanded", "false");
  });

  it("supports dragging the floating assistant trigger without toggling the panel on release", async () => {
    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [],
      page: { has_more: false, next_cursor: null },
    } as never);

    renderWithProviders(<AssistantWidget />);
    const widget = screen.getByTestId("database-assistant-widget-desktop");
    const trigger = screen.getByTestId("database-assistant-trigger");

    expect(widget.style.transform).toBe("");
    expect(trigger).toHaveAttribute("data-dragging", "false");

    // Start dragging from (900, 700) -> (720, 580) (dx = -180, dy = -120)
    fireEvent.pointerDown(trigger, {
      pointerId: 1,
      pointerType: "mouse",
      button: 0,
      clientX: 900,
      clientY: 700,
    });
    // Small sub-threshold move (< 6px) should not enter dragging state
    fireEvent.pointerMove(trigger, {
      pointerId: 1,
      clientX: 898,
      clientY: 699,
    });
    expect(trigger).toHaveAttribute("data-dragging", "false");

    fireEvent.pointerMove(trigger, {
      pointerId: 1,
      clientX: 720,
      clientY: 580,
    });
    expect(trigger).toHaveAttribute("data-dragging", "true");
    expect(widget.style.transform).toBe("translate3d(-180px, -120px, 0)");

    fireEvent.pointerUp(trigger, {
      pointerId: 1,
      clientX: 720,
      clientY: 580,
    });
    expect(trigger).toHaveAttribute("data-dragging", "false");

    // Synthetic click fired by the browser after pointerup must be suppressed
    fireEvent.click(trigger);
    expect(screen.queryByTestId("database-assistant-panel")).not.toBeInTheDocument();

    // Window resize re-clamps the offset within viewport bounds
    fireEvent(window, new Event("resize"));
    expect(widget.style.transform).toBe("translate3d(-180px, -120px, 0)");

    // Subsequent normal click opens the assistant panel at the new position
    fireEvent.click(trigger);
    expect(await screen.findByTestId("database-assistant-panel")).toBeInTheDocument();
  });

  it("populates starter prompt when clicking empty-state quick starters and guards IME composition on Enter", async () => {
    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [],
      page: { has_more: false, next_cursor: null },
    } as never);
    vi.mocked(assistantApi.createAssistantSession).mockResolvedValue(
      SAMPLE_SESSION as never,
    );
    vi.mocked(assistantApi.sendAssistantMessage).mockResolvedValue({
      session: SAMPLE_SESSION,
      user_message: {
        id: "77777777-7777-4777-8777-777777777777",
        session_id: SAMPLE_SESSION.id,
        sequence: 1,
        role: "user",
        capability: "sql_generate",
        state: "completed",
        content: "Help me generate an INSERT statement",
        citations: [],
        sql_draft: null,
        failure_code: null,
        created_at: "2026-11-07T00:00:00Z",
      },
      assistant_message: {
        id: "55555555-5555-4555-8555-555555555555",
        session_id: SAMPLE_SESSION.id,
        sequence: 2,
        role: "assistant",
        capability: "sql_generate",
        state: "completed",
        content: "SQL draft generated.",
        citations: [],
        sql_draft: SAMPLE_DRAFT,
        failure_code: null,
        created_at: "2026-11-07T00:00:00Z",
      },
      sql_draft: SAMPLE_DRAFT,
      context_compacted: false,
    } as never);

    renderWithProviders(<AssistantPanel open={true} />);

    const genStarter = await screen.findByTestId("assistant-starter-sql_generate");
    fireEvent.click(genStarter);

    const input = screen.getByTestId<HTMLTextAreaElement>("assistant-prompt-input");
    expect(input.value).toContain("SQL");

    // Pressing Enter while IME is composing should NOT send
    fireEvent.keyDown(input, {
      key: "Enter",
      code: "Enter",
      isComposing: true,
    });
    expect(assistantApi.createAssistantSession).not.toHaveBeenCalled();

    // Pressing Enter when NOT composing sends the message
    fireEvent.keyDown(input, {
      key: "Enter",
      code: "Enter",
      isComposing: false,
    });

    await waitFor(() => {
      expect(assistantApi.createAssistantSession).toHaveBeenCalledTimes(1);
      expect(assistantApi.sendAssistantMessage).toHaveBeenCalledTimes(1);
    });
  });

  it("renders restored SQL draft card, runs formal AI precheck, and confirms order submission", async () => {
    const detail: AssistantSessionDetail = {
      session: SAMPLE_SESSION,
      drafts: [SAMPLE_DRAFT],
      messages: [
        {
          id: "77777777-7777-4777-8777-777777777777",
          session_id: SAMPLE_SESSION.id,
          sequence: 1,
          role: "user",
          capability: "sql_generate",
          state: "completed",
          content: "Insert Shanghai warehouse record",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
        },
        {
          id: "55555555-5555-4555-8555-555555555555",
          session_id: SAMPLE_SESSION.id,
          sequence: 2,
          role: "assistant",
          capability: "sql_generate",
          state: "completed",
          content: "Generated and restored SQL draft from table schema.",
          citations: [
            {
              citation_id: "help-order-submission",
              title: "Change Order and Formal AI Precheck Guide",
              source_ref: "submission-gate",
              route_path: "/changes/orders",
            },
          ],
          sql_draft: SAMPLE_DRAFT,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
        },
      ],
    };

    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [SAMPLE_SESSION],
      page: { has_more: false, next_cursor: null },
    } as never);
    vi.mocked(assistantApi.getAssistantSession).mockResolvedValue(detail as never);
    vi.mocked(assistantApi.precheckAssistantSqlDraft).mockResolvedValue({
      draft: {
        ...SAMPLE_DRAFT,
        submission_state: "precheck_ready",
        ephemeral_run_id: "66666666-6666-4666-8666-666666666666",
      },
      ephemeral_run_id: "66666666-6666-4666-8666-666666666666",
      gate_passed: true,
      gate_reason_codes: [],
    } as never);
    vi.mocked(assistantApi.submitAssistantSqlDraftOrder).mockResolvedValue({
      id: "88888888-8888-4888-8888-888888888888",
      display_number: "YO-20261107-0001",
    } as unknown as ChangeOrder as never);

    renderWithProviders(
      <SqlDraftCard
        sessionId={SAMPLE_SESSION.id}
        draft={SAMPLE_DRAFT}
        flows={[SAMPLE_FLOW]}
        isSelected={true}
        onSelectDraft={() => {}}
      />,
    );

    expect(
      screen.getByTestId(`assistant-sql-code-${SAMPLE_DRAFT.id}`).textContent,
    ).toContain("INSERT INTO `warehouse_db`.`depots`");
    expect(
      screen.getByTestId(`assistant-draft-warnings-${SAMPLE_DRAFT.id}`).textContent,
    ).toContain("depot_code");
    expect(
      screen.queryByTestId(`assistant-draft-open-submit-${SAMPLE_DRAFT.id}`),
    ).not.toBeInTheDocument();

    // Run formal precheck to transition draft -> precheck_ready
    fireEvent.click(screen.getByTestId(`assistant-draft-precheck-${SAMPLE_DRAFT.id}`));
    await waitFor(() => {
      expect(assistantApi.precheckAssistantSqlDraft).toHaveBeenCalledWith(
        SAMPLE_SESSION.id,
        SAMPLE_DRAFT.id,
        expect.objectContaining({
          flow_id: SAMPLE_FLOW.id,
          revision: SAMPLE_DRAFT.revision,
          sql_hmac: SAMPLE_DRAFT.sql_hmac,
        }),
        expect.anything(),
      );
    });

    // Open submit confirmation dialog and submit
    const openSubmitBtn = await screen.findByTestId(
      `assistant-draft-open-submit-${SAMPLE_DRAFT.id}`,
    );
    fireEvent.click(openSubmitBtn);
    const titleInput = await screen.findByTestId(
      `assistant-submit-title-${SAMPLE_DRAFT.id}`,
    );
    fireEvent.change(titleInput, { target: { value: "Add Shanghai warehouse record" } });

    const confirmBtn = screen.getByTestId(`assistant-submit-confirm-${SAMPLE_DRAFT.id}`);
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      expect(assistantApi.submitAssistantSqlDraftOrder).toHaveBeenCalledWith(
        SAMPLE_SESSION.id,
        SAMPLE_DRAFT.id,
        expect.objectContaining({
          flow_id: SAMPLE_FLOW.id,
          run_id: "66666666-6666-4666-8666-666666666666",
          revision: SAMPLE_DRAFT.revision,
          sql_hmac: SAMPLE_DRAFT.sql_hmac,
          title: "Add Shanghai warehouse record",
        }),
        expect.anything(),
      );
    });

    expect(
      await screen.findByTestId(`assistant-draft-order-banner-${SAMPLE_DRAFT.id}`),
    ).toHaveTextContent("YO-20261107-0001");
  });

  it("renders assistant replies as structured Markdown (headings, lists, code blocks, inline code, tables)", async () => {
    const markdownReply = [
      "### Index and Performance Analysis",
      "",
      "Based on the **execution plan** and `depots` table schema, the following issues were found:",
      "",
      "1. **Full Table Scan**: missing composite index on `depot_code`",
      "   - Scanned rows up to `120000`",
      "2. **Table Lookup Overhead**: recommend covering frequently queried columns",
      "",
      "| Metric | Before | Expected After |",
      "| --- | --- | --- |",
      "| Scanned Rows | 120000 | 1 |",
      "",
      "```sql",
      "CREATE INDEX idx_depots_code ON depots (depot_code);",
      "```",
    ].join("\n");

    const detail: AssistantSessionDetail = {
      session: SAMPLE_SESSION,
      drafts: [],
      messages: [
        {
          id: "77777777-7777-4777-8777-777777777777",
          session_id: SAMPLE_SESSION.id,
          sequence: 1,
          role: "user",
          capability: "sql_optimize",
          state: "completed",
          content: "Help me analyze why the depots query is slow",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
        },
        {
          id: "55555555-5555-4555-8555-555555555555",
          session_id: SAMPLE_SESSION.id,
          sequence: 2,
          role: "assistant",
          capability: "sql_optimize",
          state: "completed",
          content: markdownReply,
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
        },
      ],
    };

    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [SAMPLE_SESSION],
      page: { has_more: false, next_cursor: null },
    } as never);
    vi.mocked(assistantApi.getAssistantSession).mockResolvedValue(detail as never);

    renderWithProviders(<AssistantPanel open={true} />);

    const mdContainer = await screen.findByTestId("assistant-markdown");
    expect(mdContainer.querySelector("h4")).toHaveTextContent("Index and Performance Analysis");
    expect(mdContainer.querySelector("ol")).not.toBeNull();
    expect(mdContainer.querySelector("table")).not.toBeNull();
    expect(mdContainer.querySelector("pre code")).toHaveTextContent(
      "CREATE INDEX idx_depots_code ON depots (depot_code);",
    );

    // Verify Shadcn AI Elements Trace (Reasoning + Tool calls)
    const trace = screen.getByTestId(
      "assistant-trace-55555555-5555-4555-8555-555555555555",
    );
    expect(trace).toHaveTextContent("\u5df2\u5b8c\u6210\u601d\u8003\u4e0e\u63a8\u7406");
    expect(trace).toHaveTextContent("privacy_session.anonymize_and_gate");
    expect(trace).toHaveTextContent("catalog_inspector.load_schema_metadata");
  });

  it("unwraps truncated JSON envelopes into clean Markdown without leaking raw JSON keys", async () => {
    const truncatedJson =
      '{"out_of_scope":false,"resolved_capability":"platform_help","assistant_reply":"### SQL Performance Diagnostics Guide\\n\\n1. Click **SQL Diagnostics** in the left navigation\\n2. Select an authorized datasource and time range","cited_article_ids":["help-sql';

    const detail: AssistantSessionDetail = {
      session: SAMPLE_SESSION,
      drafts: [],
      messages: [
        {
          id: "99999999-1111-4111-8111-111111111111",
          session_id: SAMPLE_SESSION.id,
          sequence: 1,
          role: "assistant",
          capability: "platform_help",
          state: "completed",
          content: truncatedJson,
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
        },
      ],
    };

    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [SAMPLE_SESSION],
      page: { has_more: false, next_cursor: null },
    } as never);
    vi.mocked(assistantApi.getAssistantSession).mockResolvedValue(detail as never);

    renderWithProviders(<AssistantPanel open={true} />);

    const mdContainer = await screen.findByTestId("assistant-markdown");
    expect(mdContainer).not.toHaveTextContent("out_of_scope");
    expect(mdContainer.querySelector("h4")).toHaveTextContent(
      "SQL Performance Diagnostics Guide",
    );
  });

  it("maps all 7 canonical assistant business error codes (6001-6007) to localized i18n messages", () => {
    const codes = [6001, 6002, 6003, 6004, 6005, 6006, 6007];
    for (const code of codes) {
      const err = new BusinessError({
        err_code: code,
        message: "assistant error",
        request_id: "req-assistant-test",
        retryable: false,
      });
      const opId =
        code === 6005 ? "submitAssistantSqlDraftOrder" : "sendAssistantMessage";
      const display = describeError(err, opId);
      expect(display.contractViolation).toBe(false);
      expect(display.messageKey).toMatch(/^errors\.ASSISTANT_/);
      const translated = i18n.t(display.messageKey);
      expect(translated).not.toBe(display.messageKey);
      expect(translated.length).toBeGreaterThan(5);
    }
  });

  it("supports the /clear slash command to clear conversation context without calling sendAssistantMessage", async () => {
    const freshSession: AssistantSession = {
      ...SAMPLE_SESSION,
      id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      selected_draft_id: null,
      version: 1,
    };
    const detailWithHistory: AssistantSessionDetail = {
      session: SAMPLE_SESSION,
      drafts: [],
      messages: [
        {
          id: "77777777-7777-4777-8777-777777777777",
          session_id: SAMPLE_SESSION.id,
          sequence: 1,
          role: "user",
          capability: "platform_help",
          state: "completed",
          content: "First question in history",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
        },
        {
          id: "55555555-5555-4555-8555-555555555555",
          session_id: SAMPLE_SESSION.id,
          sequence: 2,
          role: "assistant",
          capability: "platform_help",
          state: "completed",
          content: "First answer in history",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:01Z",
        },
      ],
    };

    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [SAMPLE_SESSION],
      page: { has_more: false, next_cursor: null },
    } as never);
    vi.mocked(assistantApi.getAssistantSession).mockImplementation((id: string) => {
      if (id === freshSession.id) {
        return Promise.resolve({
          session: freshSession,
          messages: [],
          drafts: [],
        } as never);
      }
      return Promise.resolve(detailWithHistory as never);
    });
    vi.mocked(assistantApi.deleteAssistantSession).mockResolvedValue(undefined as never);
    vi.mocked(assistantApi.createAssistantSession).mockResolvedValue(freshSession as never);

    renderWithProviders(<AssistantPanel open={true} />);

    expect(
      await screen.findByTestId("assistant-message-77777777-7777-4777-8777-777777777777"),
    ).toBeInTheDocument();

    const input = screen.getByTestId<HTMLTextAreaElement>("assistant-prompt-input");
    fireEvent.change(input, { target: { value: "/cl" } });

    // Slash command menu appears when typing /cl
    expect(screen.getByTestId("assistant-slash-command-menu")).toBeInTheDocument();

    // Tab autocompletes /cl to /clear
    fireEvent.keyDown(input, { key: "Tab", code: "Tab" });
    expect(input.value).toBe("/clear");

    // Pressing Enter executes /clear: deletes current session, creates fresh session, never calls sendAssistantMessage
    fireEvent.keyDown(input, { key: "Enter", code: "Enter", isComposing: false });

    await waitFor(() => {
      expect(assistantApi.deleteAssistantSession).toHaveBeenCalledWith(
        SAMPLE_SESSION.id,
        expect.anything(),
      );
      expect(assistantApi.createAssistantSession).toHaveBeenCalledTimes(1);
    });
    expect(assistantApi.sendAssistantMessage).not.toHaveBeenCalled();
    expect(input.value).toBe("");
    expect(await screen.findByTestId("assistant-empty-state")).toBeInTheDocument();
  });

  it("auto-scrolls the message viewport to the latest message when opening the assistant dialog", async () => {
    const detailWithMultipleTurns: AssistantSessionDetail = {
      session: SAMPLE_SESSION,
      drafts: [],
      messages: [
        {
          id: "77777777-7777-4777-8777-777777777771",
          session_id: SAMPLE_SESSION.id,
          sequence: 1,
          role: "user",
          capability: "platform_help",
          state: "completed",
          content: "Earliest user message",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:00Z",
        },
        {
          id: "55555555-5555-4555-8555-555555555552",
          session_id: SAMPLE_SESSION.id,
          sequence: 2,
          role: "assistant",
          capability: "platform_help",
          state: "completed",
          content: "Earliest assistant reply",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:01Z",
        },
        {
          id: "77777777-7777-4777-8777-777777777773",
          session_id: SAMPLE_SESSION.id,
          sequence: 3,
          role: "user",
          capability: "platform_help",
          state: "completed",
          content: "Latest user message",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:02Z",
        },
        {
          id: "55555555-5555-4555-8555-555555555554",
          session_id: SAMPLE_SESSION.id,
          sequence: 4,
          role: "assistant",
          capability: "platform_help",
          state: "completed",
          content: "Latest assistant reply",
          citations: [],
          sql_draft: null,
          failure_code: null,
          created_at: "2026-11-07T00:00:03Z",
        },
      ],
    };

    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [SAMPLE_SESSION],
      page: { has_more: false, next_cursor: null },
    } as never);
    vi.mocked(assistantApi.getAssistantSession).mockResolvedValue(
      detailWithMultipleTurns as never,
    );

    const originalScrollHeight = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "scrollHeight",
    );
    const originalScrollTo = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "scrollTo",
    );
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
      configurable: true,
      get: () => 1280,
    });
    Object.defineProperty(HTMLElement.prototype, "scrollTo", {
      configurable: true,
      value: function (
        this: HTMLElement,
        optionsOrX?: ScrollToOptions | number,
        y?: number,
      ) {
        if (typeof optionsOrX === "number") {
          this.scrollTop = y ?? 0;
        } else if (
          typeof optionsOrX === "object" &&
          typeof optionsOrX.top === "number"
        ) {
          this.scrollTop = optionsOrX.top;
        }
      },
    });

    try {
      renderWithProviders(<AssistantPanel open={true} />);

      await screen.findByTestId(
        "assistant-message-55555555-5555-4555-8555-555555555554",
      );
      const openViewport = screen.getByTestId("assistant-message-viewport");

      // Ensure no historical user message carries data-scroll-anchor="true"
      const firstMsgItem = screen
        .getByTestId("assistant-message-77777777-7777-4777-8777-777777777771")
        .closest("[data-slot='message-scroller-item']");
      expect(firstMsgItem).toHaveAttribute("data-scroll-anchor", "false");

      await waitFor(() => {
        expect(openViewport.scrollTop).toBe(1280);
      });
    } finally {
      if (originalScrollHeight !== undefined) {
        Object.defineProperty(
          HTMLElement.prototype,
          "scrollHeight",
          originalScrollHeight,
        );
      } else {
        delete (HTMLElement.prototype as unknown as Record<string, unknown>)
          .scrollHeight;
      }
      if (originalScrollTo !== undefined) {
        Object.defineProperty(
          HTMLElement.prototype,
          "scrollTo",
          originalScrollTo,
        );
      } else {
        delete (HTMLElement.prototype as unknown as Record<string, unknown>)
          .scrollTo;
      }
    }
  });

  it("covers extractAuthorizedDatasources, dispatchSSEFrame, and streamAssistantMessageSSE branches", async () => {
    const sampleUserMessage = {
      id: "77777777-7777-4777-8777-777777777771",
      session_id: SAMPLE_SESSION.id,
      seq: 1,
      role: "user" as const,
      intent: "sql_generate" as const,
      state: "completed" as const,
      content: "Insert warehouse record",
      datasource_id: SAMPLE_SESSION.active_datasource_id,
      schema_name: SAMPLE_SESSION.active_schema_name,
      execution_plan_summary: null,
      out_of_scope: false,
      citations: [],
      sql_draft: null,
      error_code: null,
      error_message: null,
      request_id: null,
      created_at: "2026-11-07T00:00:00Z",
      updated_at: "2026-11-07T00:00:00Z",
    };
    const mockReply = {
      ...sampleUserMessage,
      id: "55555555-5555-4555-8555-555555555555",
      seq: 2,
      role: "assistant" as const,
      content: "Generated draft.",
      sql_draft: SAMPLE_DRAFT,
    };

    // 1. extractAuthorizedDatasources
    const datasources = extractAuthorizedDatasources([
      SAMPLE_FLOW,
      {
        ...SAMPLE_FLOW,
        id: "flow-query-access",
        name: "Query Access Flow",
        flow_type: "query_access",
        stages: [],
        query_capabilities: [
          {
            datasource_id: "22222222-2222-4222-8222-222222222222",
            datasource_name: "prod-mysql-warehouse",
            can_query: true,
            can_export: false,
          },
          {
            datasource_id: "33333333-3333-4333-8333-333333333333",
            datasource_name: "analytics-pg-replica",
            can_query: true,
            can_export: false,
          },
        ],
      },
    ]);
    expect(datasources).toHaveLength(2);
    expect(datasources[0]?.flowId).toBe(SAMPLE_FLOW.id);
    expect(datasources[1]?.flowId).toBeNull();

    // 2. dispatchSSEFrame branches
    const onProgress = vi.fn();
    const onTool = vi.fn();
    const onReasoning = vi.fn();
    const onDelta = vi.fn();
    const callbacks = { onProgress, onTool, onReasoning, onDelta };

    expect(dispatchSSEFrame("event: progress\ndata: \n", callbacks)).toBeNull();
    expect(dispatchSSEFrame("event: progress\ndata: {bad-json\n", callbacks)).toBeNull();
    expect(
      dispatchSSEFrame(
        `event: progress\ndata: ${JSON.stringify({ phase: "thinking", message_id: "m1" })}`,
        callbacks,
      ),
    ).toBeNull();
    expect(onProgress).toHaveBeenCalledWith({ phase: "thinking", message_id: "m1" });

    expect(
      dispatchSSEFrame(
        `event: tool\ndata: ${JSON.stringify({
          tool: "schema_lookup",
          status: "running",
          capability: "ddl",
          schema: "warehouse_db",
          tables: 3,
          engine: "mysql",
          statement_kind: "insert",
          count: 1,
        })}`,
        callbacks,
      ),
    ).toBeNull();
    expect(onTool).toHaveBeenCalledWith({
      tool: "schema_lookup",
      status: "running",
      capability: "ddl",
      schema: "warehouse_db",
      tables: 3,
      engine: "mysql",
      statement_kind: "insert",
      count: 1,
    });

    expect(
      dispatchSSEFrame(
        `event: tool\ndata: ${JSON.stringify({ tool: "schema_lookup", status: "completed" })}`,
        callbacks,
      ),
    ).toBeNull();
    expect(onTool).toHaveBeenLastCalledWith({
      tool: "schema_lookup",
      status: "completed",
      capability: undefined,
      schema: undefined,
      tables: undefined,
      engine: undefined,
      statement_kind: undefined,
      count: undefined,
    });

    expect(
      dispatchSSEFrame(
        `event: reasoning\ndata: ${JSON.stringify({ delta: "thinking step", status: "running" })}`,
        callbacks,
      ),
    ).toBeNull();
    expect(onReasoning).toHaveBeenCalledWith({
      delta: "thinking step",
      status: "running",
    });

    expect(
      dispatchSSEFrame(
        `event: reasoning\ndata: ${JSON.stringify({ status: "completed" })}`,
        callbacks,
      ),
    ).toBeNull();
    expect(onReasoning).toHaveBeenLastCalledWith({
      delta: undefined,
      status: "completed",
    });

    expect(
      dispatchSSEFrame(
        `event: delta\ndata: ${JSON.stringify({ delta: "hello " })}`,
        callbacks,
      ),
    ).toBeNull();
    expect(onDelta).toHaveBeenCalledWith("hello ");

    expect(
      dispatchSSEFrame(`event: unknown\ndata: ${JSON.stringify({ foo: "bar" })}`, callbacks),
    ).toBeNull();

    await expect(
      Promise.resolve().then(() =>
        dispatchSSEFrame(
          `event: error\ndata: ${JSON.stringify({
            err_code: 6002,
            message: "stream interrupted",
            request_id: "req-err-1",
            retryable: true,
            meta: { detail: "timeout" },
          })}`,
          callbacks,
        ),
      ),
    ).rejects.toThrow(BusinessError);

    const doneResult = dispatchSSEFrame(
      `event: done\ndata: ${JSON.stringify({
        session: SAMPLE_SESSION,
        user_message: sampleUserMessage,
        assistant_message: mockReply,
      })}`,
      callbacks,
    );
    expect(doneResult?.assistant_message.id).toBe(mockReply.id);

    // 3. streamAssistantMessageSSE with forceFetch: true
    const originalFetch = globalThis.fetch;
    try {
      const encoder = new TextEncoder();
      const ssePayload = [
        `event: progress\ndata: ${JSON.stringify({ phase: "context" })}\n\n`,
        `event: delta\ndata: ${JSON.stringify({ delta: "chunk-1" })}\n\n`,
        `event: done\ndata: ${JSON.stringify({
          session: SAMPLE_SESSION,
          user_message: sampleUserMessage,
          assistant_message: mockReply,
        })}`,
      ];

      globalThis.fetch = vi.fn().mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers({ "content-type": "text/event-stream" }),
        body: new ReadableStream({
          start(controller) {
            for (const chunk of ssePayload) {
              controller.enqueue(encoder.encode(chunk));
            }
            controller.close();
          },
        }),
      });

      const streamed = await streamAssistantMessageSSE({
        sessionId: SAMPLE_SESSION.id,
        body: { content: "hi" },
        callbacks,
        forceFetch: true,
      });
      expect(streamed.assistant_message.id).toBe(mockReply.id);

      // JSON fallback success
      globalThis.fetch = vi.fn().mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers({ "content-type": "application/json" }),
        body: null,
        json: () =>
          Promise.resolve({
            err_code: 0,
            data: {
              session: SAMPLE_SESSION,
              user_message: sampleUserMessage,
              assistant_message: mockReply,
            },
          }),
      });
      const jsonFallback = await streamAssistantMessageSSE({
        sessionId: SAMPLE_SESSION.id,
        body: { content: "hi" },
        callbacks,
        forceFetch: true,
      });
      expect(jsonFallback.assistant_message.id).toBe(mockReply.id);

      // JSON fallback business error
      globalThis.fetch = vi.fn().mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers({ "content-type": "application/json" }),
        body: null,
        json: () =>
          Promise.resolve({
            err_code: 6002,
            message: "upstream failed",
            request_id: "req-json-err",
            retryable: true,
            meta: { reason: "timeout" },
          }),
      });
      await expect(
        streamAssistantMessageSSE({
          sessionId: SAMPLE_SESSION.id,
          body: { content: "hi" },
          callbacks,
          forceFetch: true,
        }),
      ).rejects.toThrow(BusinessError);

      // HTTP 401 unauthorized + problem JSON
      globalThis.fetch = vi.fn().mockResolvedValueOnce({
        ok: false,
        status: 401,
        statusText: "Unauthorized",
        headers: new Headers({ "content-type": "application/problem+json" }),
        json: () =>
          Promise.resolve({
            title: "unauthorized",
            status: 401,
            detail: "expired",
          }),
      });
      await expect(
        streamAssistantMessageSSE({
          sessionId: SAMPLE_SESSION.id,
          body: { content: "hi" },
          callbacks,
          forceFetch: true,
        }),
      ).rejects.toThrow(TransportError);

      // HTTP 502 non-JSON transport error
      globalThis.fetch = vi.fn().mockResolvedValueOnce({
        ok: false,
        status: 502,
        statusText: "Bad Gateway",
        headers: new Headers({ "content-type": "text/plain" }),
        json: () => Promise.reject(new Error("not json")),
      });
      await expect(
        streamAssistantMessageSSE({
          sessionId: SAMPLE_SESSION.id,
          body: { content: "hi" },
          callbacks,
          forceFetch: true,
        }),
      ).rejects.toThrow(TransportError);

      // Network rejection
      globalThis.fetch = vi.fn().mockRejectedValueOnce(new TypeError("network down"));
      await expect(
        streamAssistantMessageSSE({
          sessionId: SAMPLE_SESSION.id,
          body: { content: "hi" },
          callbacks,
          forceFetch: true,
        }),
      ).rejects.toThrow(TransportError);

      // Incomplete SSE stream without done event
      globalThis.fetch = vi.fn().mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers({ "content-type": "text/event-stream" }),
        body: new ReadableStream({
          start(controller) {
            controller.enqueue(
              encoder.encode(`event: delta\ndata: ${JSON.stringify({ delta: "part" })}\n\n`),
            );
            controller.close();
          },
        }),
      });
      await expect(
        streamAssistantMessageSSE({
          sessionId: SAMPLE_SESSION.id,
          body: { content: "hi" },
          callbacks,
          forceFetch: true,
        }),
      ).rejects.toThrow(TransportError);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("renders all AssistantMarkdown block & inline tokens and unwrapAssistantReplyContent branches", () => {
    expect(unwrapAssistantReplyContent("plain text")).toBe("plain text");
    expect(
      unwrapAssistantReplyContent(JSON.stringify({ assistant_reply: "unwrapped json" })),
    ).toBe("unwrapped json");
    expect(
      unwrapAssistantReplyContent('{"assistant_reply": "partial\\nline\\t\\"quote\\"\\r'),
    ).toContain("partial\nline\t\"quote\"");

    const markdownSample = [
      "# Main Heading",
      "### Sub Heading",
      "Paragraph with **bold**, *italic*, `inline_code`, and [Workspace Link](/workspace).",
      "---",
      "> Important note inside blockquote",
      "- First bullet item",
      "  Sub-item line under first bullet",
      "1. Numbered item",
      "```sql",
      "SELECT * FROM orders;",
      "```",
      "```",
      "plain code block without lang",
      "```",
    ].join("\n");

    const { rerender } = render(
      <MemoryRouter>
        <AssistantMarkdown content={markdownSample} isStreaming={true} />
      </MemoryRouter>,
    );
    expect(screen.getByText("Main Heading")).toBeInTheDocument();
    expect(screen.getByText("Sub Heading")).toBeInTheDocument();
    expect(screen.getByText("bold")).toBeInTheDocument();
    expect(screen.getByText("italic")).toBeInTheDocument();
    expect(screen.getByText("inline_code")).toBeInTheDocument();
    expect(screen.getByText("Workspace Link")).toBeInTheDocument();
    expect(screen.getByText(/Important note inside blockquote/)).toBeInTheDocument();
    expect(screen.getByText(/Sub-item line under first bullet/)).toBeInTheDocument();
    expect(screen.getByTestId("assistant-markdown")).toHaveTextContent(
      "SELECT * FROM orders;",
    );

    rerender(
      <MemoryRouter>
        <AssistantMarkdown content="" isStreaming={true} />
      </MemoryRouter>,
    );
    rerender(
      <MemoryRouter>
        <AssistantMarkdown content="" isStreaming={false} />
      </MemoryRouter>,
    );
  });

  it("covers AssistantPanel citations, session create/delete, intent & execution plan toggles, and mobile AssistantWidget", async () => {
    const allCitationIds = [
      "help-query-access",
      "help-change-orders",
      "help-ai-precheck",
      "help-approvals",
      "help-execution-verify",
      "help-rollback-orders",
      "help-error-codes",
      "help-permission-model",
      "help-sql-diagnostics",
      "help-admin-governance",
      "help-admin-review-rules",
      "help-admin-notifications-audit",
      "help-custom-unknown",
    ];
    vi.mocked(assistantApi.listAssistantSessions).mockResolvedValue({
      items: [SAMPLE_SESSION],
      page: { has_more: false, next_cursor: null },
    } as never);
    vi.mocked(assistantApi.createAssistantSession).mockResolvedValue({
      ...SAMPLE_SESSION,
      id: "new-session-id",
    } as never);
    vi.mocked(assistantApi.deleteAssistantSession).mockResolvedValue(undefined as never);
    vi.mocked(assistantApi.getAssistantSession).mockResolvedValue({
      session: {
        ...SAMPLE_SESSION,
        compacted_through_seq: 2,
      },
      drafts: [SAMPLE_DRAFT],
      messages: [
        {
          id: "msg-user-1",
          session_id: SAMPLE_SESSION.id,
          seq: 1,
          role: "user",
          capability: "platform_help",
          state: "completed",
          content: "Show platform docs",
          datasource_id: null,
          schema_name: null,
          execution_plan_summary: null,
          out_of_scope: false,
          citations: [],
          sql_draft: null,
          error_code: null,
          error_message: null,
          request_id: null,
          created_at: "2026-11-07T08:01:00Z",
          updated_at: "2026-11-07T08:01:00Z",
        },
        {
          id: "msg-all-citations",
          session_id: SAMPLE_SESSION.id,
          seq: 2,
          role: "assistant",
          capability: "platform_help",
          state: "completed",
          content: "Here are all platform help links.",
          datasource_id: null,
          schema_name: null,
          execution_plan_summary: null,
          out_of_scope: false,
          citations: allCitationIds.map((id, idx) => ({
            citation_id: id,
            title: `Fallback Title ${String(idx)}`,
            source_ref: `docs/ref-${String(idx)}.md`,
            route_path: idx % 2 === 0 ? "/workspace" : null,
          })),
          sql_draft: null,
          error_code: null,
          error_message: null,
          request_id: null,
          created_at: "2026-11-07T08:02:00Z",
          updated_at: "2026-11-07T08:02:00Z",
        },
        {
          id: "msg-failed-state",
          session_id: SAMPLE_SESSION.id,
          seq: 3,
          role: "assistant",
          capability: "sql_optimize",
          state: "failed",
          content: "",
          datasource_id: null,
          schema_name: null,
          execution_plan_summary: null,
          out_of_scope: false,
          error_code: 6002,
          error_message: "AI upstream failed",
          request_id: "req-fail-3",
          citations: [],
          sql_draft: null,
          created_at: "2026-11-07T08:03:00Z",
          updated_at: "2026-11-07T08:03:00Z",
        },
        {
          id: "msg-cancelled-state",
          session_id: SAMPLE_SESSION.id,
          seq: 4,
          role: "assistant",
          capability: "sql_generate",
          state: "cancelled",
          content: "Partial output before cancel",
          datasource_id: null,
          schema_name: null,
          execution_plan_summary: null,
          out_of_scope: false,
          error_code: null,
          error_message: null,
          request_id: null,
          citations: [],
          sql_draft: SAMPLE_DRAFT,
          created_at: "2026-11-07T08:04:00Z",
          updated_at: "2026-11-07T08:04:00Z",
        },
      ],
    } as never);

    renderWithProviders(<AssistantPanel open={true} />);

    const replyBubble = await screen.findByTestId(
      "assistant-reply-bubble-msg-all-citations",
    );
    expect(replyBubble).toHaveTextContent("Here are all platform help links.");
    expect(screen.getByTestId("assistant-compacted-marker")).toBeInTheDocument();
    expect(screen.getByTestId("assistant-retry-message-msg-failed-state")).toBeInTheDocument();

    // Intent selector & execution plan toggle
    fireEvent.click(screen.getByTestId("assistant-intent-sql_optimize"));
    fireEvent.click(screen.getByTestId("assistant-toggle-plan-button"));
    const execPlanInput = await screen.findByTestId("assistant-execution-plan-textarea");
    fireEvent.change(execPlanInput, { target: { value: "id=1 SIMPLE orders ALL" } });

    const schemaInput = screen.getByTestId("assistant-schema-input");
    fireEvent.change(schemaInput, { target: { value: "analytics_db" } });

    // Create new session
    fireEvent.click(screen.getByTestId("assistant-new-session-button"));
    await waitFor(() => {
      expect(assistantApi.createAssistantSession).toHaveBeenCalled();
    });

    // Delete current session
    fireEvent.click(screen.getByTestId("assistant-delete-session-button"));
    await waitFor(() => {
      expect(assistantApi.deleteAssistantSession).toHaveBeenCalled();
    });

    // Test mobile viewport AssistantWidget
    const origInnerWidth = window.innerWidth;
    Object.defineProperty(window, "innerWidth", {
      writable: true,
      configurable: true,
      value: 375,
    });
    try {
      renderWithProviders(<AssistantWidget />);
      expect(
        await screen.findByTestId("database-assistant-widget-mobile"),
      ).toBeInTheDocument();
      const mobileTriggers = screen.getAllByTestId("database-assistant-trigger");
      const mobileTrigger = mobileTriggers.at(-1);
      if (!mobileTrigger) throw new Error("missing mobile trigger");
      fireEvent.pointerEnter(mobileTrigger);
      fireEvent.focus(mobileTrigger);
      fireEvent.click(mobileTrigger);
      expect(
        await screen.findByTestId("database-assistant-mobile-sheet"),
      ).toBeInTheDocument();
    } finally {
      Object.defineProperty(window, "innerWidth", {
        writable: true,
        configurable: true,
        value: origInnerWidth,
      });
    }

    // Also exercise streaming callbacks (onTool, onReasoning, onProgress, onDelta), retry button, and SqlDraftCard copy/select/error branches
    vi.mocked(assistantApi.sendAssistantMessage).mockImplementationOnce(
      (_sessionId, _body, options) => {
        const cb = (options as unknown as { callbacks?: Parameters<typeof dispatchSSEFrame>[1] })
          .callbacks;
        cb?.onTool?.({
          tool: "privacy_session.anonymize_and_gate",
          status: "running",
        });
        cb?.onTool?.({
          tool: "privacy_session.anonymize_and_gate",
          status: "completed",
        });
        cb?.onTool?.({
          tool: "knowledge_corpus.retrieve_articles",
          status: "running",
        });
        cb?.onTool?.({
          tool: "catalog_inspector.load_schema_metadata",
          status: "completed",
        });
        cb?.onTool?.({
          tool: "sql_validator.restore_and_verify_ast",
          status: "completed",
        });
        cb?.onReasoning?.({ status: "running", delta: "Analyzing schema..." });
        cb?.onReasoning?.({ status: "completed" });
        cb?.onProgress?.({ phase: "validating_and_restoring" });
        cb?.onDelta?.("Streamed reply text");
        return Promise.resolve({
          session: SAMPLE_SESSION,
          context_compacted: true,
          user_message: {
            id: "msg-u-stream",
            session_id: SAMPLE_SESSION.id,
            seq: 5,
            role: "user",
            intent: "sql_optimize",
            state: "completed",
            content: "Optimize query",
            datasource_id: SAMPLE_SESSION.active_datasource_id,
            schema_name: "analytics_db",
            execution_plan_summary: "id=1 SIMPLE orders ALL",
            out_of_scope: false,
            citations: [],
            sql_draft: null,
            error_code: null,
            error_message: null,
            request_id: null,
            created_at: "2026-11-07T08:05:00Z",
            updated_at: "2026-11-07T08:05:00Z",
          },
          assistant_message: {
            id: "msg-a-stream",
            session_id: SAMPLE_SESSION.id,
            seq: 6,
            role: "assistant",
            intent: "sql_optimize",
            state: "completed",
            content: "Streamed reply text",
            datasource_id: SAMPLE_SESSION.active_datasource_id,
            schema_name: "analytics_db",
            execution_plan_summary: null,
            out_of_scope: false,
            citations: [],
            sql_draft: SAMPLE_DRAFT,
            error_code: null,
            error_message: null,
            request_id: null,
            created_at: "2026-11-07T08:05:01Z",
            updated_at: "2026-11-07T08:05:01Z",
          },
        } as never);
      },
    );

    const promptInput = screen.getAllByTestId("assistant-prompt-input")[0];
    const sendButton = screen.getAllByTestId("assistant-send-button")[0];
    const newSessionBtn = screen.getAllByTestId("assistant-new-session-button")[0];
    const deleteSessionBtn = screen.getAllByTestId("assistant-delete-session-button")[0];
    if (!promptInput || !sendButton || !newSessionBtn || !deleteSessionBtn) {
      throw new Error("missing assistant controls");
    }
    fireEvent.change(promptInput, { target: { value: "Optimize query" } });
    fireEvent.click(sendButton);
    await waitFor(() => {
      expect(assistantApi.sendAssistantMessage).toHaveBeenCalled();
    });

    // Trigger sendAssistantMessage error + error retry button, createSession error, deleteSession error
    vi.mocked(assistantApi.sendAssistantMessage).mockRejectedValueOnce(
      new BusinessError({
        err_code: 6002,
        message: "upstream failed",
        request_id: "req-send-err",
        retryable: true,
      }),
    );
    fireEvent.change(promptInput, { target: { value: "Retry me" } });
    fireEvent.click(sendButton);
    const errRetryBtn = await screen.findByTestId("assistant-error-retry-button");
    fireEvent.click(errRetryBtn);

    vi.mocked(assistantApi.createAssistantSession).mockRejectedValueOnce(
      new BusinessError({
        err_code: 6001,
        message: "session limit",
        request_id: "req-create-err",
        retryable: false,
      }),
    );
    fireEvent.click(newSessionBtn);
    await waitFor(() => {
      expect(assistantApi.createAssistantSession).toHaveBeenCalledTimes(2);
    });

    vi.mocked(assistantApi.deleteAssistantSession).mockRejectedValueOnce(
      new BusinessError({
        err_code: 6005,
        message: "delete conflict",
        request_id: "req-del-err",
        retryable: false,
      }),
    );
    fireEvent.click(deleteSessionBtn);
    await waitFor(() => {
      expect(assistantApi.deleteAssistantSession).toHaveBeenCalledTimes(2);
    });

    // Test SqlDraftCard copy, select, and precheck error branch
    vi.mocked(assistantApi.precheckAssistantSqlDraft).mockRejectedValueOnce(
      new BusinessError({
        err_code: 6004,
        message: "precheck failed",
        request_id: "req-precheck-err",
        retryable: false,
      }),
    );
    const onSelectDraft = vi.fn();
    renderWithProviders(
      <SqlDraftCard
        sessionId={SAMPLE_SESSION.id}
        draft={{
          ...SAMPLE_DRAFT,
          datasource_name: null,
        }}
        flows={[SAMPLE_FLOW]}
        isSelected={false}
        onSelectDraft={onSelectDraft}
      />,
    );
    const selectBtn = screen.getAllByTestId(`assistant-select-draft-${SAMPLE_DRAFT.id}`).at(-1);
    const copyBtn = screen.getAllByTestId(`assistant-copy-sql-${SAMPLE_DRAFT.id}`).at(-1);
    const precheckBtn = screen.getAllByTestId(`assistant-draft-precheck-${SAMPLE_DRAFT.id}`).at(-1);
    if (!selectBtn || !copyBtn || !precheckBtn) {
      throw new Error("missing draft card buttons");
    }
    fireEvent.click(selectBtn);
    expect(onSelectDraft).toHaveBeenCalledWith(SAMPLE_DRAFT.id);

    fireEvent.click(copyBtn);

    fireEvent.click(precheckBtn);
    expect(
      await screen.findByTestId(`assistant-draft-error-${SAMPLE_DRAFT.id}`),
    ).toBeInTheDocument();
  });
});
