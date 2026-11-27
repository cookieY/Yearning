import { StrictMode } from "react";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { SqlCompletionCatalog } from "@/features/review/sql-completions";

type ResolveCompletionFn = (ctx: {
  qualifier?: string;
  sql: string;
}) => Promise<SqlCompletionCatalog | null | undefined>;

const sqlCompletionCapture: {
  catalog: SqlCompletionCatalog | undefined;
  resolve: ResolveCompletionFn | undefined;
} = {
  catalog: undefined,
  resolve: undefined,
};

function resetSqlCompletionCapture(): void {
  sqlCompletionCapture.catalog = undefined;
  sqlCompletionCapture.resolve = undefined;
}

function getCapturedResolver(): ResolveCompletionFn | undefined {
  return sqlCompletionCapture.resolve;
}

// jsdom cannot host the lazy Monaco chunk; the established stub swaps in a
// controlled textarea (draft-workspace test precedent).
vi.mock("@/features/review/sql-editor-panel", () => ({
  SqlEditorPanel: ({
    value,
    onChange,
    onSelectionChange,
    completionCatalog,
    onResolveCompletion,
    ...rest
  }: {
    value: string;
    onChange: (sql: string) => void;
    onSelectionChange?: (selectedText: string) => void;
    completionCatalog?: SqlCompletionCatalog;
    onResolveCompletion?: ResolveCompletionFn;
    readOnly?: boolean;
    "data-testid"?: string;
  }) => {
    sqlCompletionCapture.catalog = completionCatalog;
    sqlCompletionCapture.resolve = onResolveCompletion;
    return (
      <textarea
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
        onSelect={(event) => {
          const el = event.currentTarget;
          const selected = el.value.slice(el.selectionStart, el.selectionEnd);
          onSelectionChange?.(selected);
        }}
        readOnly={rest.readOnly === true}
        data-testid={rest["data-testid"] ?? "sql-editor-stub"}
      />
    );
  },
}));
import userEvent from "@testing-library/user-event";
import { openRowActionsAt } from "@/test/row-actions";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { HttpResponse, http } from "msw";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { SESSION_USER_ID, stubSession } from "@/test/session";
import { OPERATION_ERROR_PROFILES } from "@/api/generated/projections/operation-error-profiles";
import {
  QUERY_FIXTURE_DS_MYSQL_ID,
  QUERY_FIXTURE_DS_PG_ID,
  QUERY_FIXTURE_FLOW_ID,
  resetQueryFixture,
  seedQueryScenario,
} from "@/shared/mock/query-fixture";
import { MOCK_IDENTITY_IDS, setMockIdentity } from "@/shared/mock/identity-store";
import { SessionProvider } from "@/features/auth/session-provider";
import i18next from "@/shared/i18n";
import QueryEntryPage from "@/routes/query/query-entry-page";
import QueryWorkspacePage from "@/routes/query/query-workspace-page";
import { ExportDialog } from "@/features/query/query-workspace-dialogs";
import { ApprovalsWorkspacePage } from "@/routes/approvals/approval-queue-page";

/**
 * Query-domain component tests (popup-free). Gates covered: query allows single SELECT only
 * (server 4007 rendered inline, client hint never replaces server check), unbounded-total pagination (rows read rather than
 * total count), non-dismissible blocking notice on Grant revocation, admin has no query read surface (only grant revocation),
 * and key states of the unified Query Orders view.
 */

const REVIEWER_USER_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945fa001";

function renderAt(path: string): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <SessionProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/query" element={<QueryEntryPage />} />
            <Route path="/query/sessions/:sessionId" element={<QueryWorkspacePage />} />
            <Route path="/approvals/query-access" element={<ApprovalsWorkspacePage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

beforeAll(() => {
  Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
  Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
  Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });
});

beforeEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "default");
  resetQueryFixture();
});

/**
 * §18.48: schema selection through the real UI. The Base UI Select popup
 * never mounts under jsdom (F9 popup-isolation reality), so tests pick the
 * schema the way users increasingly do: click the table in the metadata
 * tree — that activates its schema and drops a template, which the caller
 * then overwrites with its own SQL (the editor is cleared here).
 */
async function chooseSchema(name: string): Promise<void> {
  const schemaNode = await screen.findByTestId(`query-tree-schema-${name}`);
  await userEvent.click(schemaNode);
  const tables = await screen.findAllByTestId(new RegExp(`^query-tree-table-${name}-`));
  const table = tables[0];
  if (table === undefined) throw new Error(`no tables rendered under schema ${name}`);
  await userEvent.click(table);
  await userEvent.clear(screen.getByTestId("query-sql-editor"));
}

describe("gate: query allows single SELECT only with unbounded-total pagination", () => {
  it("renders the server's 4007 refusal inline and never blocks client-side only", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    // The editor is a lazy Monaco chunk in jsdom; set the SQL through the
    // textarea the SqlEditorPanel renders as its fallback area.
    const textarea = screen.getByTestId("query-sql-editor");
    await userEvent.type(textarea, "update app.users set username = 'x'");
    await userEvent.click(screen.getByTestId("query-run"));
    expect(await screen.findByTestId("query-run-error")).toHaveTextContent("\u4ec5\u5141\u8bb8SELECT\u8bed\u53e5");
  });

  it("reports rows loaded from the cursor, never a fabricated total", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    const textarea = screen.getByTestId("query-sql-editor");
    await userEvent.type(textarea, "select id from app.users");
    await userEvent.click(screen.getByTestId("query-run"));
    const loaded = await screen.findByTestId(/query-result-loaded-/);
    // §18.50 follow-up: the background drain loads the whole result —
    // the honest loaded count lands on the full 1200, still no total.
    await waitFor(() => {
      expect(loaded).toHaveTextContent("\u5df2\u8bfb\u53d6 1200 \u884c");
    });
    // No total-row display anywhere on the workspace (Q005).
    expect(screen.getByTestId("query-workspace").textContent).not.toContain("\u5171 ");
  });

  it("marks vocabulary columns as masked in the result header", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    const textarea = screen.getByTestId("query-sql-editor");
    await userEvent.type(textarea, "select id, email from app.users");
    await userEvent.click(screen.getByTestId("query-run"));
    await screen.findByTestId(/query-result-loaded-/);
    expect(await screen.findByText("\u5df2\u6309\u654f\u611f\u5b57\u6bb5\u7b56\u7565\u8131\u654f")).toBeVisible();
  });

  it("shows SHOW FULL FIELDS parity columns in the structure dialog (§18.51)", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    fireEvent.contextMenu(screen.getByTestId("query-tree-table-app-users"));
    // context-menu-01 pattern (owner 2026-10-31 round 7): animated panel + Copy Table Name + View Table Structure.
    await screen.findByTestId("query-inspect-panel");
    expect(screen.getByTestId("query-inspect-copy")).toBeInTheDocument();
    await userEvent.click(await screen.findByTestId("query-inspect-open"));
    const dialog = await screen.findByTestId("query-table-structure");
    expect(dialog).toBeVisible();
    // Key / Default / Extra / Comment — aligned with legacy SHOW FULL FIELDS (empty values render as "—").
    expect(within(dialog).getByText("PRI")).toBeVisible();
    expect(within(dialog).getByText("auto_increment")).toBeVisible();
    expect(within(dialog).getByText("CURRENT_TIMESTAMP")).toBeVisible();
    expect(within(dialog).getByText("\u4e3b\u952e")).toBeVisible();
    expect(within(dialog).getByText("\u6ce8\u518c\u65f6\u95f4")).toBeVisible();
    // Regular columns outside the key column render empty placeholders without column shifting.
    const usernameRow = within(dialog).getByTestId("query-structure-username");
    expect(usernameRow).toHaveTextContent("\u7528\u6237\u540d");
    expect(usernameRow.textContent).not.toContain("PRI");
  });

  it("syncs sql state when clicking a table in the metadata tree so running without typing executes the template", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    await userEvent.click(screen.getByTestId("query-tree-table-app-users"));
    expect(screen.getByTestId("query-sql-editor")).toHaveValue("SELECT *\nFROM app.users\nLIMIT 100");
    await userEvent.click(screen.getByTestId("query-run"));
    await screen.findByTestId(/query-result-loaded-/);
    expect(screen.queryByTestId("query-run-error")).not.toBeInTheDocument();
  });

  it("only shows multiple result tabs when multiple SELECT statements are executed at the same time", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    const textarea = screen.getByTestId("query-sql-editor");

    // Single statement run twice: replaces the result instead of accumulating tabs.
    await userEvent.type(textarea, "select id from app.users limit 1");
    await userEvent.click(screen.getByTestId("query-run"));
    await screen.findByTestId(/query-result-loaded-/);
    await userEvent.click(screen.getByTestId("query-run"));
    await screen.findByTestId(/query-result-loaded-/);
    expect(screen.queryByRole("tab", { name: "\u7ed3\u679c 1" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "\u7ed3\u679c 2" })).not.toBeInTheDocument();

    // Multiple statements in one batch: renders schema.table tabs and increments duplicates.
    await userEvent.clear(textarea);
    await userEvent.type(
      textarea,
      "select id from app.users limit 1; select id from app.orders limit 1; select id from app.users limit 2",
    );
    await userEvent.click(screen.getByTestId("query-run"));
    expect(await screen.findByRole("tab", { name: "app.users" })).toBeVisible();
    expect(await screen.findByRole("tab", { name: "app.orders" })).toBeVisible();
    expect(await screen.findByRole("tab", { name: "app.users 2" })).toBeVisible();
  });

  it("allows running schema-qualified SQL without picking a schema and runs only selected SQL when a selection exists", async () => {
    seedQueryScenario("query-session");
    stubSession(false);

    const executedPayloads: Array<{ schema_name: string; sql: string }> = [];
    server.use(
      http.post(apiRoute("/query-sessions/:sessionId/executions"), async ({ request }) => {
        const body = (await request.json()) as { schema_name: string; sql: string };
        executedPayloads.push({ schema_name: body.schema_name, sql: body.sql });
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            execution_id: `exec-${String(executedPayloads.length)}`,
            columns: [{ name: "id", type: "bigint" }],
            rows: [[1]],
            page: { next_cursor: null, has_more: false },
            elapsed_ms: 4,
          },
          request_id: `req-${String(executedPayloads.length)}`,
          retryable: false,
        });
      }),
    );

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    const textarea = screen.getByTestId<HTMLTextAreaElement>("query-sql-editor");

    // Without picking a schema, unqualified SQL still requires a schema selection.
    await userEvent.clear(textarea);
    await userEvent.type(textarea, "SELECT * FROM users");
    await userEvent.click(screen.getByTestId("query-run"));
    expect(await screen.findByTestId("query-run-error")).toHaveTextContent(
      i18next.t("query.run.needSchema"),
    );
    expect(executedPayloads).toHaveLength(0);

    // Schema-qualified SQL runs without selecting a schema in the dropdown.
    const multiSql = "SELECT * FROM app.users LIMIT 1; SELECT * FROM app.orders LIMIT 2";
    await userEvent.clear(textarea);
    await userEvent.type(textarea, multiSql);
    await userEvent.click(screen.getByTestId("query-run"));
    expect(await screen.findByRole("tab", { name: "app.users" })).toBeVisible();
    expect(await screen.findByRole("tab", { name: "app.orders" })).toBeVisible();
    expect(executedPayloads).toEqual([
      { schema_name: "app", sql: "SELECT * FROM app.users LIMIT 1" },
      { schema_name: "app", sql: "SELECT * FROM app.orders LIMIT 2" },
    ]);

    // Selecting only the second statement switches the run button and executes only that snippet.
    const secondStart = multiSql.indexOf("SELECT * FROM app.orders");
    textarea.setSelectionRange(secondStart, multiSql.length);
    fireEvent.select(textarea);
    expect(screen.getByTestId("query-run")).toHaveTextContent(i18next.t("query.run.executeSelected"));
    await userEvent.click(screen.getByTestId("query-run"));
    await waitFor(() => {
      expect(executedPayloads).toHaveLength(3);
    });
    expect(executedPayloads[2]).toEqual({
      schema_name: "app",
      sql: "SELECT * FROM app.orders LIMIT 2",
    });
  });

  it("maps cursor_rejected_data_type_mismatch and cursor_rejected_undefined_column to unambiguous localized copy", async () => {
    seedQueryScenario("query-session");
    stubSession(false);

    let failure = "data_type_mismatch";
    server.use(
      http.post(apiRoute("/query-sessions/:sessionId/executions"), () =>
        HttpResponse.json({
          err_code: 4011,
          message: "QUERY_STATEMENT_REJECTED",
          data: null,
          request_id: `req-${failure}`,
          retryable: false,
          meta: { detail: `database: cursor_rejected_${failure}` },
        }),
      ),
    );

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    const textarea = screen.getByTestId("query-sql-editor");
    await userEvent.clear(textarea);
    await userEvent.type(textarea, "SELECT * FROM app.users WHERE id = 'abc'");
    await userEvent.click(screen.getByTestId("query-run"));

    const mismatchAlert = await screen.findByTestId("query-run-error");
    expect(mismatchAlert).toHaveTextContent(i18next.t("query.errors.dataTypeMismatch"));
    expect(mismatchAlert).toHaveTextContent("(req-data_type_mismatch)");

    failure = "undefined_column";
    await userEvent.click(screen.getByTestId("query-run"));
    await waitFor(() => {
      expect(screen.getByTestId("query-run-error")).toHaveTextContent("(req-undefined_column)");
    });
    expect(screen.getByTestId("query-run-error")).toHaveTextContent(
      i18next.t("query.errors.undefinedColumn"),
    );
  });
});

describe("gate: admin revocation is user-perceptible and admin cannot view queries", () => {
  it("shows the non-dismissible revocation notice with the reason after the grant is revoked", async () => {
    seedQueryScenario("query-revoked");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    const notice = await screen.findByTestId("query-revoked-notice", undefined, { timeout: 8000 });
    expect(notice).toHaveTextContent("\u8bbf\u95ee\u8d44\u683c\u5df2\u88ab\u64a4\u9500");
    expect(notice).toHaveTextContent("least-privilege rotation");
    // The blocked workspace disables running and exits remain the only path.
    expect(screen.getByTestId("query-run")).toBeDisabled();
  });

  it("renders the reviewer revocation surface with the terminating semantics copy and hides it from non-reviewer grantees", async () => {
    seedQueryScenario("query-session");
    // Non-reviewer grantee (henry) cannot revoke their own grant; GrantRevocationCard stays empty.
    setMockIdentity("henry");
    stubSession(false);
    renderAt("/approvals/query-access");
    expect(await screen.findByRole("heading", { level: 1, name: "\u67e5\u8be2\u8bbf\u95ee\u5ba1\u6279" })).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.queryByTestId("query-access-grants-table")).not.toBeInTheDocument();

    // Frozen reviewer (reviewer-a) sees the active grant and revocation dialog.
    cleanup();
    setMockIdentity("reviewer-a");
    stubSession({
      id: REVIEWER_USER_ID,
      username: "reviewer-a",
      admin: false,
      requestId: SESSION_USER_ID,
    });
    renderAt("/approvals/query-access");
    const grantsTable = await screen.findByTestId("query-access-grants-table");
    await openRowActionsAt(within(grantsTable).getByTestId(/query-grant-actions-/));
    await userEvent.click(screen.getByTestId(/query-access-revoke-/));
    const dialog = await screen.findByTestId("query-access-revoke-dialog");
    expect(dialog).toHaveTextContent("\u7ec8\u6b62\u8be5\u6388\u6743\u4e0b\u5168\u90e8\u67e5\u8be2\u4f1a\u8bdd");
    // Revocation is terminating access, never reading it.
    expect(dialog.textContent).not.toContain("\u67e5\u770b\u67e5\u8be2");
    setMockIdentity("henry");
  });

  it("grants apply to the builtin admin identity too — no implicit override, no exclusion (§18.48)", async () => {
    seedQueryScenario("query-session");
    stubSession(true);
    // The builtin-admin ACTOR comes from the behavior dimension (the /users/me
    // stub alone keeps the henry identity, which owns the seeded session).
    window.localStorage.setItem("yearning-mock-auth", "admin");
    renderAt("/query");
    // A granted world serves the flow catalog to the admin identity exactly
    // like to a regular user (permission-group semantics, backend
    // flowGranted has no admin exclusion)…
    await screen.findByTestId(/query-flow-apply-/);
    // …but the admin ACTOR owns no grant, so under the owner 2026-10-31
    // ruling (no grant → no enter button) the catalog row offers apply —
    // enter is hidden exactly as for any ungranted user.
    expect(screen.queryByTestId(/query-flow-enter-/)).toBeNull();
    // …while the admin still owns no query objects of its own (no session
    // rows — it never went through the request/approval chain).
    await waitFor(() => {
      expect(screen.queryByTestId(/query-session-/)).toBeNull();
    });
  });
});

describe("query entry mode branches (server-driven)", () => {
  it("reuses the active session for the same grant+datasources instead of creating a new one (owner 2026-10-31)", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    const creates: string[] = [];
    server.events.on("request:start", ({ request }) => {
      if (request.method === "POST" && new URL(request.url).pathname.endsWith("/query-sessions")) {
        creates.push(request.url);
      }
    });
    renderAt("/query");
    await userEvent.click(await screen.findByTestId(/query-flow-enter-/));
    // Reuse seeded active session 6f0f2b3c-5555-4555-8555-000000000101: enter workspace directly with zero new POST requests.
    await screen.findByTestId("query-workspace");
    expect(creates).toHaveLength(0);
    server.events.removeAllListeners();
  });

  it("displays bound grant expiry on active sessions and extends the existing session on renewal approval", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    const sessionId = "6f0f2b3c-5555-4555-8555-000000000101";

    renderAt("/query");
    const initialExpiry = await screen.findByTestId(`query-session-expiry-${sessionId}`);
    expect(initialExpiry.textContent).toMatch(/^\u622a\u6b62\u65f6\u95f4\uff1a/);
    const initialExpiryText = initialExpiry.textContent;

    const grantsRes = (await (await fetch("/api/v4/query-grants")).json()) as {
      data: { items: { id: string; state: string }[] };
    };
    const activeGrantId = grantsRes.data.items.find((g) => g.state === "active")?.id;
    expect(activeGrantId).toBeDefined();

    const extendedUntil = new Date(Date.now() + 10 * 24 * 60 * 60 * 1000).toISOString();
    const renewRes = (await (
      await fetch(`/api/v4/query-grants/${activeGrantId ?? ""}/renewal-requests`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": "renew-rebind-session-1",
        },
        body: JSON.stringify({ requested_until: extendedUntil, reason: "extend active session" }),
      })
    ).json()) as { err_code: number; data: { id: string } };
    expect(renewRes.err_code).toBe(0);

    setMockIdentity("reviewer-a");
    const approveRes = (await (
      await fetch(`/api/v4/query-access-requests/${renewRes.data.id}/decisions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          "Idempotency-Key": "approve-rebind-session-1",
        },
        body: JSON.stringify({ decision: "approve" }),
      })
    ).json()) as { err_code: number; data: { grant_id: string } };
    expect(approveRes.err_code).toBe(0);
    const newGrantId = approveRes.data.grant_id;
    setMockIdentity("henry");

    const sessionsRes = (await (await fetch("/api/v4/query-sessions")).json()) as {
      data: { items: { id: string; grant_id: string; state: string }[] };
    };
    const activeSessions = sessionsRes.data.items.filter((row) => row.state === "active");
    expect(activeSessions).toHaveLength(1);
    expect(activeSessions[0]?.id).toBe(sessionId);
    expect(activeSessions[0]?.grant_id).toBe(newGrantId);

    cleanup();
    const creates: string[] = [];
    server.events.on("request:start", ({ request }) => {
      if (request.method === "POST" && new URL(request.url).pathname.endsWith("/query-sessions")) {
        creates.push(request.url);
      }
    });
    renderAt("/query");
    const updatedExpiry = await screen.findByTestId(`query-session-expiry-${sessionId}`);
    expect(updatedExpiry.textContent).toMatch(/^\u622a\u6b62\u65f6\u95f4\uff1a/);
    expect(updatedExpiry.textContent).not.toBe(initialExpiryText);

    await userEvent.click(await screen.findByTestId(/query-flow-enter-/));
    await screen.findByTestId("query-workspace");
    expect(creates).toHaveLength(0);
    server.events.removeAllListeners();
  });

  it("hides the enter button and offers apply when review is required without a grant (owner 2026-10-31 round 6)", async () => {
    seedQueryScenario("query-flow");
    stubSession(false);
    renderAt("/query");
    expect(await screen.findByRole("heading", { level: 1, name: "\u67e5\u8be2" })).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    // No eligibility (and the flow has approval steps = eligibility requires approval): do not show "Enter Query",
    // show "Request Access" directly — no longer requiring a 4001 round-trip first (§18.49 server-driven handling
    // remains as a fallback when a grant expires mid-flight).
    expect(await screen.findByTestId(/query-flow-apply-/)).toBeVisible();
    expect(screen.queryByTestId(/query-flow-enter-/)).toBeNull();
  });

  it("opens the access-request dialog without any SQL input (Q001)", async () => {
    seedQueryScenario("query-flow");
    stubSession(false);
    renderAt("/query");
    // owner 2026-10-31 round 6: Request Access is shown directly when ineligible (Enter button is hidden).
    await userEvent.click(await screen.findByTestId(/query-flow-apply-/));
    const dialog = await screen.findByTestId("query-access-request-dialog");
    expect(dialog).toBeInTheDocument();
    // No SQL input exists on the application form (Q001) — the reason
    // textarea is the only multiline input.
    expect(dialog.querySelectorAll("textarea")).toHaveLength(1);
    expect(dialog.textContent).toContain("\u4e0d\u5ba1\u6279\u5177\u4f53SQL");
  });

  it("replaces the apply button with a disabled pending button when the flow already has a pending access request", async () => {
    seedQueryScenario("query-flow");
    stubSession(false);
    await fetch("/api/v4/query-access-requests", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        flow_id: QUERY_FIXTURE_FLOW_ID,
        datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
        requested_until: "2026-12-31T23:59:00Z",
        reason: "troubleshooting",
      }),
    });
    renderAt("/query");
    const pendingBtn = await screen.findByTestId(/query-flow-pending-/);
    expect(pendingBtn).toBeVisible();
    expect(pendingBtn).toBeDisabled();
    expect(pendingBtn).toHaveTextContent("\u5ba1\u6279\u4e2d");
    expect(screen.queryByTestId(/query-flow-apply-/)).toBeNull();

    // Requester can withdraw the pending request directly from the flow card.
    const withdrawBtn = await screen.findByTestId(/query-flow-withdraw-/);
    expect(withdrawBtn).toBeVisible();
    expect(withdrawBtn).toBeEnabled();
    await userEvent.click(withdrawBtn);

    const dialog = await screen.findByTestId("query-withdraw-dialog");
    await userEvent.type(within(dialog).getByTestId("query-withdraw-reason"), "no longer needed");
    await userEvent.click(within(dialog).getByTestId("query-withdraw-confirm"));

    await waitFor(() => {
      expect(screen.queryByTestId("query-withdraw-dialog")).toBeNull();
    });
    expect(await screen.findByTestId(/query-flow-apply-/)).toBeVisible();
    expect(screen.queryByTestId(/query-flow-pending-/)).toBeNull();
    expect(screen.queryByTestId(/query-flow-withdraw-/)).toBeNull();
  });

  it("isolates pending requests by flow_id when two flows share overlapping datasource sets", async () => {
    seedQueryScenario("query-flow");
    stubSession(false);
    const flowA = QUERY_FIXTURE_FLOW_ID;
    const flowB = "00000000-0000-4000-8000-00000000f002";
    const ds1 = QUERY_FIXTURE_DS_MYSQL_ID;
    const ds2 = "00000000-0000-4000-8000-00000000d002";

    server.use(
      http.get(apiRoute("/users/me/flows"), () =>
        HttpResponse.json({
          err_code: 0,
          name: "OK",
          message: "ok",
          request_id: "req-two-flows",
          data: {
            items: [
              {
                id: flowA,
                name: "f12-query-flow",
                flow_type: "query_access",
                enabled: true,
                status: "enabled",
                rule_set_id: null,
                approval_steps: [{ position: 1, actors: [{ user_id: "00000000-0000-4000-8000-000000000002" }] }],
                query_capabilities: [{ datasource_id: ds1, datasource_name: "pg-main", can_query: true, can_export: true }],
                version: 1,
                created_at: "2026-08-01T00:00:00Z",
                updated_at: "2026-08-01T00:00:00Z",
              },
              {
                id: flowB,
                name: "f12-owner-query-flow",
                flow_type: "query_access",
                enabled: true,
                status: "enabled",
                rule_set_id: null,
                approval_steps: [{ position: 1, actors: [{ user_id: "00000000-0000-4000-8000-000000000002" }] }],
                query_capabilities: [
                  { datasource_id: ds1, datasource_name: "pg-main", can_query: true, can_export: true },
                  { datasource_id: ds2, datasource_name: "mysql-main", can_query: true, can_export: true },
                ],
                version: 1,
                created_at: "2026-08-01T00:00:00Z",
                updated_at: "2026-08-01T00:00:00Z",
              },
            ],
          },
        }),
      ),
    );

    // Submit a pending request on flowA only (whose datasource [ds1] is a subset of flowB [ds1, ds2]).
    await fetch("/api/v4/query-access-requests", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        flow_id: flowA,
        datasource_ids: [ds1],
        requested_until: "2026-12-31T23:59:00Z",
        reason: "only flow A",
      }),
    });

    renderAt("/query");
    expect(await screen.findByTestId(`query-flow-pending-${flowA}`)).toBeDisabled();
    expect(await screen.findByTestId(`query-flow-withdraw-${flowA}`)).toBeEnabled();
    // flowB must stay in "Apply Access" state and must NOT borrow flowA's pending request.
    expect(await screen.findByTestId(`query-flow-apply-${flowB}`)).toBeEnabled();
    expect(screen.queryByTestId(`query-flow-pending-${flowB}`)).toBeNull();
    expect(screen.queryByTestId(`query-flow-withdraw-${flowB}`)).toBeNull();
  });

  it("renders 3010 DATASOURCE_UNAVAILABLE inline from operation-error-profiles when query access create hits an unreachable datasource and allows retry", async () => {
    for (const profile of [
      "query_access_create",
      "query_grant_renew",
      "query_session_create",
      "query_metadata",
      "query_execute",
      "submitter_metadata",
    ] as const) {
      expect(OPERATION_ERROR_PROFILES.profiles[profile]?.err_codes).toContain(3010);
    }

    seedQueryScenario("query-flow");
    stubSession(false);
    let postCount = 0;
    server.use(
      http.post(apiRoute("/query-access-requests"), () => {
        postCount += 1;
        if (postCount === 1) {
          return HttpResponse.json({
            err_code: 3010,
            name: "DATASOURCE_UNAVAILABLE",
            message: "Query request rejected",
            data: null,
            request_id: "req-3010-boot",
            retryable: false,
          });
        }
        return undefined;
      }),
    );

    renderAt("/query");
    await userEvent.click(await screen.findByTestId(/query-flow-apply-/));
    const dialog = await screen.findByTestId("query-access-request-dialog");

    // Pick datasource, future expiration date from Calendar, and reason.
    await userEvent.click(within(dialog).getByTestId(`query-apply-ds-${QUERY_FIXTURE_DS_MYSQL_ID}`));
    await userEvent.click(within(dialog).getByTestId("query-apply-until"));
    const calendar = document.querySelector('[data-slot="calendar"]');
    if (calendar === null) throw new Error("expected calendar popover to mount");
    const enabledDays = Array.from(calendar.querySelectorAll<HTMLButtonElement>("button:not([disabled])")).filter(
      (btn) => /^\d+$/.test(btn.textContent.trim()),
    );
    const futureDay = enabledDays[enabledDays.length - 1];
    if (futureDay === undefined) throw new Error("expected enabled day button in calendar");
    await userEvent.click(futureDay);
    await userEvent.type(within(dialog).getByTestId("query-apply-reason"), "on-demand probe check");

    // First submit returns 200 OK business error 3010: renders mapped copy, never 500 or contract-violation text.
    const submitBtn = within(dialog).getByTestId("query-apply-submit");
    await userEvent.click(submitBtn);
    const alert = await within(dialog).findByTestId("query-apply-error");
    expect(alert).toHaveTextContent("\u76ee\u6807\u6570\u636e\u6e90\u4e0d\u53ef\u7528\uff0c\u64cd\u4f5c\u88ab\u6302\u8d77\u3002 (req-3010-boot)");
    expect(submitBtn).toBeEnabled();

    // Retry immediately after datasource becomes reachable succeeds and transitions flow to pending.
    await userEvent.click(submitBtn);
    await waitFor(() => {
      expect(screen.queryByTestId("query-access-request-dialog")).toBeNull();
    });
    expect(await screen.findByTestId(/query-flow-pending-/)).toBeDisabled();
    expect(postCount).toBe(2);
  });
});

describe("review R1 fixes", () => {
  it("H-1: editing an OIDC provider without retyping the secret keeps it (no empty replacement)", async () => {
    const AdminIdentityProvidersPage = (
      await import("@/routes/admin/admin-identity-providers-page")
    ).default;
    const { resetAdminFixture } = await import("@/shared/mock/admin-fixture");
    resetAdminFixture();
    window.localStorage.setItem("yearning-mock-auth", "admin");
    // Seed an OIDC provider so the table carries a real OIDC row.
    const seedResponse = await fetch("/api/v4/admin/identity-providers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        provider_key: "corp-oidc",
        provider_kind: "oidc",
        display_name: "Corp OIDC",
        enabled: false,
        configuration: {
          issuer_url: "https://idp.corp.test",
          client_id: "yearning",
          scopes: ["openid"],
          username_claim: "preferred_username",
          display_name_claim: "name",
          email_claim: "email",
          connect_timeout_ms: 5000,
          request_timeout_ms: 10000,
        },
        client_secret: { value: "oidcsec-1" },
      }),
    });
    const seeded = (await seedResponse.json()) as { err_code: number; data: { id: string } };
    expect(seeded.err_code).toBe(0);
    stubSession(true);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    cleanup();
    render(
      <QueryClientProvider client={client}>
        <SessionProvider>
          <MemoryRouter>
            <AdminIdentityProvidersPage />
          </MemoryRouter>
        </SessionProvider>
      </QueryClientProvider>,
    );
    const oidcRow = await screen.findByTestId(`admin-idp-row-${seeded.data.id}`);
    // Row actions collapsed into the ⋯ kebab; edit opens the provider form.
    await openRowActionsAt(within(oidcRow).getByTestId(/admin-idp-actions-/));
    await userEvent.click(screen.getByTestId(`admin-idp-edit-${seeded.data.id}`));
    const secret = await screen.findByTestId("idp-secret");
    expect((secret as HTMLInputElement).value).toBe("");
    // Save without retyping the secret: the replace payload omits
    // client_secret (keep) and the fixture would reject {value:""} outright.
    await userEvent.click(screen.getByTestId("idp-form-submit"));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    // The provider survived with its secret still configured.
    const after = (await (
      await fetch(`/api/v4/admin/identity-providers/${seeded.data.id}`)
    ).json()) as { err_code: number; data: { secret_configured: boolean } };
    expect(after.err_code).toBe(0);
    expect(after.data.secret_configured).toBe(true);
    expect(JSON.stringify(after.data)).not.toContain("oidcsec-1");
  });

  it("H-2: deciding a pending request keeps it visible in the queue with updated state and hides decision actions via cache invalidation", async () => {
    seedQueryScenario("query-approval");
    // The queue and its decision surface belong to the frozen reviewer
    // identity (§18.44) — henry is only the requester and sees no row.
    // The handlers read the identity dimension (localStorage), so both the
    // session stub and the store must say reviewer-a.
    setMockIdentity("reviewer-a");
    stubSession({
      id: REVIEWER_USER_ID,
      username: "reviewer-a",
      admin: false,
      requestId: SESSION_USER_ID,
    });
    window.localStorage.setItem("yearning-mock-auth", "default");
    renderAt("/approvals/query-access");
    const row = await screen.findByTestId("query-access-pending-6f0f2b3c-5555-4555-8555-000000000201");
    await openRowActionsAt(within(row).getByTestId("query-access-actions-6f0f2b3c-5555-4555-8555-000000000201"));
    await userEvent.click(screen.getByTestId("query-access-approve-6f0f2b3c-5555-4555-8555-000000000201"));
    await userEvent.click(screen.getByTestId("query-access-decision-confirm"));
    // The invalidated list re-reads: the decided row remains in the table with
    // its updated state (Grant Active) and its decision actions removed.
    await waitFor(
      () => {
        const updatedRow = screen.getByTestId("query-access-pending-6f0f2b3c-5555-4555-8555-000000000201");
        expect(updatedRow).toBeVisible();
        expect(updatedRow).toHaveTextContent("\u6388\u6743\u751f\u6548");
        expect(within(updatedRow).queryByTestId("query-access-actions-6f0f2b3c-5555-4555-8555-000000000201")).not.toBeInTheDocument();
      },
      { timeout: 4000 },
    );
  });

  it("hides requester-only pending requests from /approvals/query-access and tab badge while preserving dual-role and non-active-step behavior", async () => {
    seedQueryScenario("query-flow");
    // 1. Non-approver requester (henry) creates a pending request whose frozen approver is reviewer-a.
    setMockIdentity("henry");
    stubSession(false);
    window.localStorage.setItem("yearning-mock-auth", "default");
    const createRes = await fetch("/api/v4/query-access-requests", {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": "req-only-test-1" },
      body: JSON.stringify({
        flow_id: QUERY_FIXTURE_FLOW_ID,
        datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
        requested_until: "2026-12-31T00:00:00Z",
        reason: "requester-only visibility check",
      }),
    });
    const created = (await createRes.json()) as { err_code: number; data: { id: string; can_decide: boolean } };
    expect(created.err_code).toBe(0);
    expect(created.data.can_decide).toBe(false);

    // Requester visits /approvals/query-access: sees empty approval queue and NO badge or decision actions.
    renderAt("/approvals/query-access");
    expect(await screen.findByTestId("query-access-pending-empty")).toBeVisible();
    expect(screen.queryByTestId(`query-access-pending-${created.data.id}`)).not.toBeInTheDocument();
    expect(screen.queryByTestId(`query-access-actions-${created.data.id}`)).not.toBeInTheDocument();
    expect(screen.queryByTestId("approvals-tab-query-badge")).not.toBeInTheDocument();

    // 2. Same-identity dual-role actor (owner is both requester and Step 1 frozen approver):
    // sees own pending row in /approvals/query-access with can_decide=true, decision actions, and badge "1".
    cleanup();
    seedQueryScenario("query-flow");
    setMockIdentity("owner");
    stubSession({
      id: MOCK_IDENTITY_IDS.owner,
      username: "owner",
      admin: false,
      requestId: MOCK_IDENTITY_IDS.owner,
    });
    const dualCreateRes = await fetch("/api/v4/query-access-requests", {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": "dual-role-test-1" },
      body: JSON.stringify({
        flow_id: QUERY_FIXTURE_FLOW_ID,
        datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
        requested_until: "2026-12-31T00:00:00Z",
        reason: "dual-role self-approval visibility check",
      }),
    });
    const dualCreated = (await dualCreateRes.json()) as { err_code: number; data: { id: string; can_decide: boolean } };
    expect(dualCreated.err_code).toBe(0);
    expect(dualCreated.data.can_decide).toBe(true);
    renderAt("/approvals/query-access");
    const dualRow = await screen.findByTestId(`query-access-pending-${dualCreated.data.id}`);
    expect(dualRow).toBeVisible();
    expect(within(dualRow).getByTestId(`query-access-actions-${dualCreated.data.id}`)).toBeVisible();
    expect(await screen.findByTestId("approvals-tab-query-badge")).toHaveTextContent("1");

    // 3. Non-active-step reviewer (can_decide=false in scope=reviewer response): sees the row with no approve/reject menu and no badge.
    cleanup();
    server.use(
      http.get(apiRoute("/query-access-requests"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          request_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9999",
          data: {
            items: [
              {
                id: "6f0f2b3c-5555-4555-8555-000000000299",
                requester_user_id: SESSION_USER_ID,
                applicant_name: "henry",
                state: "access_pending",
                datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
                datasource_names: ["analytics-mysql"],
                grant_id: null,
                version: 1,
                created_at: "2026-11-26T10:00:00Z",
                can_decide: false,
              },
            ],
            page: { limit: 20, has_more: false, next_cursor: null },
          },
        }),
      ),
    );
    setMockIdentity("reviewer-a");
    stubSession({
      id: REVIEWER_USER_ID,
      username: "reviewer-a",
      admin: false,
      requestId: SESSION_USER_ID,
    });
    renderAt("/approvals/query-access");
    const step2Row = await screen.findByTestId("query-access-pending-6f0f2b3c-5555-4555-8555-000000000299");
    expect(step2Row).toBeVisible();
    expect(within(step2Row).getByTestId("query-access-detail-6f0f2b3c-5555-4555-8555-000000000299")).toBeVisible();
    expect(within(step2Row).queryByTestId("query-access-actions-6f0f2b3c-5555-4555-8555-000000000299")).not.toBeInTheDocument();
    expect(screen.queryByTestId("approvals-tab-query-badge")).not.toBeInTheDocument();
  });

  it("displays requester application reason in the queue table, detail sheet, and decision dialog", async () => {
    seedQueryScenario("query-approval");
    setMockIdentity("reviewer-a");
    stubSession({
      id: REVIEWER_USER_ID,
      username: "reviewer-a",
      admin: false,
      requestId: SESSION_USER_ID,
    });
    window.localStorage.setItem("yearning-mock-auth", "default");
    renderAt("/approvals/query-access");

    const row = await screen.findByTestId("query-access-pending-6f0f2b3c-5555-4555-8555-000000000201");
    expect(within(row).getByTestId("query-access-reason-6f0f2b3c-5555-4555-8555-000000000201")).toHaveTextContent(
      "month-end reconciliation",
    );

    // Search filters by application reason.
    fireEvent.change(screen.getByTestId("query-access-search"), {
      target: { value: "nonexistent-reason-filter" },
    });
    expect(await screen.findByTestId("query-access-search-empty")).toBeVisible();
    fireEvent.change(screen.getByTestId("query-access-search"), {
      target: { value: "reconciliation" },
    });
    expect(screen.getByTestId("query-access-pending-6f0f2b3c-5555-4555-8555-000000000201")).toBeVisible();
    fireEvent.change(screen.getByTestId("query-access-search"), { target: { value: "" } });

    const liveRow = await screen.findByTestId("query-access-pending-6f0f2b3c-5555-4555-8555-000000000201");

    // 1. Direct row-action path opens decision dialog with requester's reason without opening the detail sheet.
    await openRowActionsAt(within(liveRow).getByTestId("query-access-actions-6f0f2b3c-5555-4555-8555-000000000201"));
    await userEvent.click(screen.getByTestId("query-access-approve-6f0f2b3c-5555-4555-8555-000000000201"));
    expect(screen.queryByTestId("query-access-detail-sheet")).not.toBeInTheDocument();
    const directDialog = await screen.findByTestId("query-access-decision-dialog");
    expect(within(directDialog).getByTestId("query-access-decision-reason")).toHaveTextContent(
      "month-end reconciliation",
    );
    await userEvent.click(within(directDialog).getByRole("button", { name: "\u53d6\u6d88" }));
    await waitFor(() => {
      expect(screen.queryByTestId("query-access-decision-dialog")).not.toBeInTheDocument();
    });

    // 2. Detail button opens the detail sheet with full reason & validity, and transitions to decision dialog.
    await userEvent.click(within(liveRow).getByTestId("query-access-detail-6f0f2b3c-5555-4555-8555-000000000201"));
    const detailSheet = await screen.findByTestId("query-access-detail-sheet");
    expect(within(detailSheet).getByTestId("query-access-detail-reason")).toHaveTextContent(
      "month-end reconciliation",
    );
    await userEvent.click(within(detailSheet).getByTestId("query-access-detail-reject"));
    await waitFor(() => {
      expect(screen.queryByTestId("query-access-detail-sheet")).not.toBeInTheDocument();
    });
    const sheetDecisionDialog = await screen.findByTestId("query-access-decision-dialog");
    expect(within(sheetDecisionDialog).getByTestId("query-access-decision-reason")).toHaveTextContent(
      "month-end reconciliation",
    );
  });

  it("consolidates renewed active grants of the same flow into a single latest row and revokes the chain together", async () => {
    seedQueryScenario("query-session");
    setMockIdentity("henry");
    stubSession(false);

    const grantsBefore = (await (await fetch("/api/v4/query-grants")).json()) as {
      data: { items: { id: string; state: string }[] };
    };
    const initialGrantId = grantsBefore.data.items.find((grant) => grant.state === "active")?.id;
    expect(initialGrantId).toBeDefined();

    const extendedUntil = new Date(Date.now() + 14 * 24 * 60 * 60 * 1000).toISOString();
    const renewRes = (await (
      await fetch(`/api/v4/query-grants/${initialGrantId ?? ""}/renewal-requests`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": "renew-consolidate-1",
        },
        body: JSON.stringify({ requested_until: extendedUntil, reason: "consolidate renewal check" }),
      })
    ).json()) as { err_code: number; data: { id: string } };
    expect(renewRes.err_code).toBe(0);

    setMockIdentity("reviewer-a");
    stubSession({
      id: REVIEWER_USER_ID,
      username: "reviewer-a",
      admin: false,
      requestId: SESSION_USER_ID,
    });
    window.localStorage.setItem("yearning-mock-auth", "default");

    const approveRes = (await (
      await fetch(`/api/v4/query-access-requests/${renewRes.data.id}/decisions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          "Idempotency-Key": "approve-consolidate-1",
        },
        body: JSON.stringify({ decision: "approve" }),
      })
    ).json()) as { err_code: number; data: { grant_id: string } };
    expect(approveRes.err_code).toBe(0);
    const latestGrantId = approveRes.data.grant_id;
    expect(latestGrantId).not.toBe(initialGrantId);

    renderAt("/approvals/query-access");
    const grantsTable = await screen.findByTestId("query-access-grants-table");
    expect(within(grantsTable).getAllByTestId(/^query-access-grant-/)).toHaveLength(1);
    expect(within(grantsTable).getByTestId(`query-access-grant-${latestGrantId}`)).toBeVisible();
    expect(within(grantsTable).queryByTestId(`query-access-grant-${initialGrantId ?? ""}`)).toBeNull();

    // Revoking the consolidated row revokes the entire active chain so the superseded grant does not reappear.
    await openRowActionsAt(within(grantsTable).getByTestId(`query-grant-actions-${latestGrantId}`));
    await userEvent.click(screen.getByTestId(`query-access-revoke-${latestGrantId}`));
    const revokeDialog = await screen.findByTestId("query-access-revoke-dialog");
    await userEvent.type(within(revokeDialog).getByTestId("query-access-revoke-reason"), "cleanup chain");
    await userEvent.click(within(revokeDialog).getByTestId("query-access-revoke-confirm"));

    await waitFor(() => {
      expect(screen.queryByTestId("query-access-grants-table")).not.toBeInTheDocument();
    });
  });

  it("keeps broader-datasource grants separate from narrower grants on the same flow during consolidation and revocation", async () => {
    seedQueryScenario("query-flow");
    setMockIdentity("henry");
    stubSession(false);

    const broadUntil = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString();
    const broadReqRes = (await (
      await fetch("/api/v4/query-access-requests", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": "broad-grant-req-1",
        },
        body: JSON.stringify({
          flow_id: QUERY_FIXTURE_FLOW_ID,
          datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID, QUERY_FIXTURE_DS_PG_ID],
          requested_until: broadUntil,
          reason: "both mysql and pg",
        }),
      })
    ).json()) as { err_code: number; data: { id: string } };
    expect(broadReqRes.err_code).toBe(0);

    setMockIdentity("reviewer-a");
    stubSession({
      id: REVIEWER_USER_ID,
      username: "reviewer-a",
      admin: false,
      requestId: SESSION_USER_ID,
    });
    window.localStorage.setItem("yearning-mock-auth", "default");

    const broadApprove = (await (
      await fetch(`/api/v4/query-access-requests/${broadReqRes.data.id}/decisions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          "Idempotency-Key": "approve-broad-1",
        },
        body: JSON.stringify({ decision: "approve" }),
      })
    ).json()) as { err_code: number; data: { grant_id: string } };
    expect(broadApprove.err_code).toBe(0);

    setMockIdentity("henry");
    stubSession(false);
    const narrowUntil = new Date(Date.now() + 14 * 24 * 60 * 60 * 1000).toISOString();
    const narrowReqRes = (await (
      await fetch("/api/v4/query-access-requests", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": "narrow-grant-req-1",
        },
        body: JSON.stringify({
          flow_id: QUERY_FIXTURE_FLOW_ID,
          datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
          requested_until: narrowUntil,
          reason: "mysql only later expiry",
        }),
      })
    ).json()) as { err_code: number; data: { id: string } };
    expect(narrowReqRes.err_code).toBe(0);

    setMockIdentity("reviewer-a");
    stubSession({
      id: REVIEWER_USER_ID,
      username: "reviewer-a",
      admin: false,
      requestId: SESSION_USER_ID,
    });
    const narrowApprove = (await (
      await fetch(`/api/v4/query-access-requests/${narrowReqRes.data.id}/decisions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "If-Match": '"1"',
          "Idempotency-Key": "approve-narrow-1",
        },
        body: JSON.stringify({ decision: "approve" }),
      })
    ).json()) as { err_code: number; data: { grant_id: string } };
    expect(narrowApprove.err_code).toBe(0);
    const broadGrantId = broadApprove.data.grant_id;
    const narrowGrantId = narrowApprove.data.grant_id;

    renderAt("/approvals/query-access");
    const grantsTable = await screen.findByTestId("query-access-grants-table");
    expect(within(grantsTable).getAllByTestId(/^query-access-grant-/)).toHaveLength(2);
    expect(within(grantsTable).getByTestId(`query-access-grant-${broadGrantId}`)).toBeVisible();
    expect(within(grantsTable).getByTestId(`query-access-grant-${narrowGrantId}`)).toBeVisible();

    // Revoking the narrower grant leaves the broader {MYSQL, PG} grant active and visible.
    await openRowActionsAt(within(grantsTable).getByTestId(`query-grant-actions-${narrowGrantId}`));
    await userEvent.click(screen.getByTestId(`query-access-revoke-${narrowGrantId}`));
    const revokeDialog = await screen.findByTestId("query-access-revoke-dialog");
    await userEvent.type(within(revokeDialog).getByTestId("query-access-revoke-reason"), "revoke narrow only");
    await userEvent.click(within(revokeDialog).getByTestId("query-access-revoke-confirm"));

    await waitFor(() => {
      const updatedTable = screen.getByTestId("query-access-grants-table");
      expect(within(updatedTable).getAllByTestId(/^query-access-grant-/)).toHaveLength(1);
      expect(within(updatedTable).getByTestId(`query-access-grant-${broadGrantId}`)).toBeVisible();
    });
  });

  // The identity dimension is localStorage-backed and setup.ts never clears
  // storage between tests — a reviewer-a leak would 404 every later henry
  // read in this file (the export test reads the henry-owned session).
  afterEach(() => {
    setMockIdentity("henry");
  });
});

describe("review R2 fix L-8: export dialog reset at the component level", () => {
  it("runs a second export after closing the dialog on the same page mount", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");

    const downloads: string[] = [];
    const downloadSpy = vi.spyOn(
      await import("@/features/query/xlsx-export"),
      "downloadXlsx",
    );
    downloadSpy.mockImplementation((_bytes, filename) => {
      downloads.push(filename);
    });

    const type = async (sql: string) => {
      const editor = screen.getByTestId("query-sql-editor");
      await userEvent.clear(editor);
      await userEvent.type(editor, sql);
    };
    await type("select id from app.users limit 1");
    await userEvent.click(screen.getByTestId("query-run"));
    await screen.findByTestId(/query-result-loaded-/);

    // First export completes and downloads.
    await userEvent.click(screen.getByTestId("query-export"));
    await screen.findByTestId("query-export-done", undefined, { timeout: 8000 });
    expect(downloads).toHaveLength(1);

    // Close, then export again on the SAME mount: the reset effect must
    // let the second run start (the R2/L-8 regression the pure-function
    // test could not lock).
    await userEvent.click(screen.getByTestId("query-export-close"));
    await waitFor(() => {
      expect(screen.queryByTestId("query-export-dialog")).not.toBeInTheDocument();
    });
    await userEvent.click(screen.getByTestId("query-export"));
    await screen.findByTestId("query-export-done", undefined, { timeout: 8000 });
    expect(downloads).toHaveLength(2);

    downloadSpy.mockRestore();
  });
});

// SEC-0031: the workspace export is held to the same caliber as the
// continuation pages — the entry is gated on can_export and every bulk data
// read goes through purpose=export (the read the server answers with the
// query.export_page_read audit and the frozen 4003 re-authorization).
describe("SEC-0031: workspace export caliber (can_export gate + audited export reads)", () => {
  afterEach(() => {
    server.events.removeAllListeners();
    setMockIdentity("henry");
  });

  it("cannot trigger an export when the session capability denies can_export", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    // Same active session, frozen capability without export: the workspace
    // entry must not render, so there is no in-product export path at all.
    server.use(
      http.get(apiRoute("/query-sessions/:sessionId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "6f0f2b3c-5555-4555-8555-000000000101",
            user_id: SESSION_USER_ID,
            state: "active",
            capabilities: [
              {
                datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
                datasource_name: "analytics-mysql",
                state: "active",
                can_query: true,
                can_export: false,
              },
            ],
            created_at: "2026-08-28T08:00:00Z",
          },
          request_id: "6f0f2b3c-5555-4555-8555-000000000101",
        }),
      ),
    );
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    await userEvent.type(screen.getByTestId("query-sql-editor"), "select id from app.users");
    // SQL is ready and the session is active — the absence is purely the
    // can_export gate, not a missing-prerequisite state.
    expect(screen.queryByTestId("query-export")).not.toBeInTheDocument();
  });

  it("routes export data through audited purpose=export reads with a one-row first screen", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");

    const exportReads: URL[] = [];
    const executePageSizes: number[] = [];
    server.events.on("request:start", (event) => {
      const { request } = event;
      const url = new URL(request.url);
      if (url.pathname.endsWith("/pages") && url.searchParams.get("purpose") === "export") {
        exportReads.push(url);
      }
      if (request.method === "POST" && url.pathname.endsWith("/executions")) {
        void request.clone().json().then((body) => {
          executePageSizes.push((body as { page_size?: number }).page_size ?? 0);
        });
      }
    });

    const downloadSpy = vi.spyOn(
      await import("@/features/query/xlsx-export"),
      "downloadXlsx",
    );
    downloadSpy.mockImplementation(() => {});

    await userEvent.type(screen.getByTestId("query-sql-editor"), "select id from app.users");
    await userEvent.click(screen.getByTestId("query-run"));
    await screen.findByTestId(/query-result-loaded-/);
    await userEvent.click(screen.getByTestId("query-export"));
    await screen.findByTestId("query-export-done", undefined, { timeout: 8000 });

    // Done (and therefore the download) is only reachable after audited
    // export-purpose reads — can_export is re-authorized on each one
    // server-side before any byte is written to a file.
    expect(exportReads.length).toBeGreaterThan(0);
    expect(exportReads.every((url) => url.searchParams.get("purpose") === "export")).toBe(true);
    // The frozen executeSelect contract has no purpose field, so the export
    // execution opens its cursor with page_size=1: the unaudited display
    // first screen is bounded to a single row while every bulk page is an
    // export read (last POST on this page is the export execution's).
    expect(executePageSizes.length).toBeGreaterThan(0);
    expect(executePageSizes[executePageSizes.length - 1]).toBe(1);

    downloadSpy.mockRestore();
  });
});

describe("query renewal dialog — shadcn DatePicker with embedded time (owner 2026-11-05)", () => {
  afterEach(() => {
    server.events.removeAllListeners();
  });

  it("embeds the time input inside the shadcn DatePicker popover and submits requested_until", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    const submittedPayloads: {
      path: string;
      body: Record<string, unknown>;
      idempotencyKey: string | null;
    }[] = [];
    server.events.on("request:start", ({ request }) => {
      const path = new URL(request.url).pathname;
      if (request.method === "POST" && path.endsWith("/renewal-requests")) {
        const idempotencyKey = request.headers.get("Idempotency-Key");
        void request.clone().json().then((body) => {
          submittedPayloads.push({
            path,
            body: body as Record<string, unknown>,
            idempotencyKey,
          });
        });
      }
    });

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await userEvent.click(await screen.findByTestId("query-grant-renew"));
    const dialog = await screen.findByTestId("query-renew-dialog");
    expect(dialog).toBeInTheDocument();

    // Standalone time input is gone before the DatePicker popover opens.
    expect(screen.queryByTestId("query-renew-until-time")).toBeNull();

    const trigger = screen.getByTestId("query-renew-until");
    expect(trigger.textContent).toMatch(/^\d{4}-\d{2}-\d{2} 23:59$/);

    // Open the DatePicker popover: both the shadcn Calendar and the time
    // InputGroupInput mount inside the same popover.
    await userEvent.click(trigger);
    expect(document.querySelector('[data-slot="calendar"]')).not.toBeNull();
    const timeInput = await screen.findByTestId("query-renew-until-time");
    expect(timeInput.closest('[data-slot="input-group"]')).not.toBeNull();
    fireEvent.change(timeInput, { target: { value: "18:30" } });
    expect(trigger.textContent).toMatch(/^\d{4}-\d{2}-\d{2} 18:30$/);

    await userEvent.type(screen.getByTestId("query-renew-reason"), "extend analytics window");
    await userEvent.click(screen.getByTestId("query-renew-submit"));

    await waitFor(() => {
      expect(submittedPayloads).toHaveLength(1);
    });
    expect(submittedPayloads[0]?.path).toMatch(/\/query-grants\/[0-9a-f-]+\/renewal-requests$/);
    expect(submittedPayloads[0]?.idempotencyKey).toMatch(/^query-renew-/);
    expect(submittedPayloads[0]?.body.reason).toBe("extend analytics window");
    expect(String(submittedPayloads[0]?.body.requested_until)).toMatch(/^\d{4}-\d{2}-\d{2}T18:30:00Z$/);
    await waitFor(() => {
      expect(screen.queryByTestId("query-renew-dialog")).not.toBeInTheDocument();
    });
  });

  it("maps requested_until validation details into localized max-duration and past-time hints", async () => {
    seedQueryScenario("query-session");
    stubSession(false);
    let detail = "requested_until_exceeds_max_minutes:43200";
    server.use(
      http.post(apiRoute("/query-grants/:grantId/renewal-requests"), () =>
        HttpResponse.json({
          err_code: 1001,
          message: "VALIDATION_FAILED",
          data: null,
          request_id: "req-renew-limit",
          retryable: false,
          meta: { detail },
        }),
      ),
    );

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await userEvent.click(await screen.findByTestId("query-grant-renew"));
    await screen.findByTestId("query-renew-dialog");
    await userEvent.type(screen.getByTestId("query-renew-reason"), "extend 60 days");
    await userEvent.click(screen.getByTestId("query-renew-submit"));

    const maxAlert = await screen.findByTestId("query-renew-error");
    expect(maxAlert).toHaveTextContent(i18next.t("query.errors.requestedUntilExceedsMax", { limit: "30 \u5929" }));
    expect(maxAlert).toHaveTextContent("(req-renew-limit)");

    detail = "requested_until_in_past";
    await userEvent.click(screen.getByTestId("query-renew-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("query-renew-error")).toHaveTextContent(i18next.t("query.errors.requestedUntilInPast"));
    });
  });

  it("restores query execution history after a full page refresh via GET /query-sessions/:id", async () => {
    seedQueryScenario("query-session");
    stubSession(false);

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    await userEvent.type(screen.getByTestId("query-sql-editor"), "select id, username from app.users");
    await userEvent.click(screen.getByTestId("query-run"));
    await screen.findByTestId(/query-result-loaded-/);

    // Simulate a browser refresh: unmount and render with a fresh QueryClient.
    cleanup();
    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await userEvent.click(screen.getByTestId("query-left-tab-history"));
    const historyRows = await screen.findAllByTestId(/^query-history-(?!hover-)/);
    expect(historyRows[0]).toHaveTextContent("select id, username from app.users");
  });

  it("maps sanitized 4011 cursor_rejected_* details to specific messages and retains unmapped details", async () => {
    seedQueryScenario("query-session");
    stubSession(false);

    let mode: "undefined_table" | "driver_error" = "undefined_table";
    server.use(
      http.post(apiRoute("/query-sessions/:sessionId/executions"), () => {
        const detail =
          mode === "undefined_table"
            ? "database: cursor_rejected_undefined_table"
            : "database: cursor_rejected_driver_error";
        return HttpResponse.json({
          err_code: 4011,
          message: "QUERY_STATEMENT_REJECTED",
          data: null,
          request_id: `req-${mode}`,
          retryable: false,
          meta: { detail },
        });
      }),
    );

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    await chooseSchema("app");
    await userEvent.type(screen.getByTestId("query-sql-editor"), "select * from app.missing_table");
    await userEvent.click(screen.getByTestId("query-run"));

    const undefinedAlert = await screen.findByTestId("query-run-error");
    expect(undefinedAlert).toHaveTextContent(i18next.t("query.errors.undefinedTable"));
    expect(undefinedAlert).toHaveTextContent("(req-undefined_table)");
    expect(undefinedAlert.textContent).not.toContain("cursor_rejected_undefined_table");
    expect(undefinedAlert.textContent).not.toContain(i18next.t("errors.QUERY_STATEMENT_REJECTED"));

    mode = "driver_error";
    await userEvent.click(screen.getByTestId("query-run"));
    await waitFor(() => {
      expect(screen.getByTestId("query-run-error")).toHaveTextContent("(req-driver_error)");
    });
    const driverAlert = screen.getByTestId("query-run-error");
    expect(driverAlert).toHaveTextContent(i18next.t("errors.QUERY_STATEMENT_REJECTED"));
    expect(driverAlert).toHaveTextContent("database: cursor_rejected_driver_error");
  });

  it("lazily loads only activeSchema tables by default and fetches other schemas/columns strictly on demand with deduplication", async () => {
    resetSqlCompletionCapture();
    seedQueryScenario("query-session");
    stubSession(false);

    const tableRequests: string[] = [];
    const columnRequests: string[] = [];
    server.use(
      http.get(apiRoute("/query-sessions/:sessionId/metadata/schemas"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          request_id: "req-lazy-schemas",
          retryable: false,
          data: { items: [{ name: "app" }, { name: "reporting" }] },
        }),
      ),
      http.get(apiRoute("/query-sessions/:sessionId/metadata/tables"), ({ request }) => {
        const schemaName = new URL(request.url).searchParams.get("schema_name") ?? "";
        tableRequests.push(schemaName);
        const items =
          schemaName === "reporting"
            ? [{ schema_name: "reporting", table_name: "daily_metrics", relation_kind: "table" }]
            : [
                { schema_name: "app", table_name: "users", relation_kind: "table" },
                { schema_name: "app", table_name: "orders", relation_kind: "table" },
              ];
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          request_id: "req-lazy-tables",
          retryable: false,
          data: { items },
        });
      }),
      http.get(apiRoute("/query-sessions/:sessionId/metadata/columns"), ({ request }) => {
        const url = new URL(request.url);
        const schemaName = url.searchParams.get("schema_name") ?? "";
        const tableName = url.searchParams.get("table_name") ?? "";
        columnRequests.push(`${schemaName}.${tableName}`);
        const items =
          tableName === "daily_metrics"
            ? [{ schema_name: "reporting", table_name: "daily_metrics", column_name: "metric_date", ordinal: 1, data_type: "date", nullable: false, masked: false }]
            : [{ schema_name: "app", table_name: "users", column_name: "email", ordinal: 1, data_type: "varchar", nullable: false, masked: true }];
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          request_id: "req-lazy-columns",
          retryable: false,
          data: { items },
        });
      }),
    );

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");
    // Before selecting any activeSchema, zero table or column fan-out requests fire.
    await new Promise((resolve) => { setTimeout(resolve, 150); });
    expect(tableRequests).toEqual([]);
    expect(columnRequests).toEqual([]);

    // Selecting activeSchema "app" fetches ONLY "app" tables — never "reporting" and never columns.
    await chooseSchema("app");
    await waitFor(() => {
      expect(sqlCompletionCapture.catalog?.schemas).toEqual(["app", "reporting"]);
      expect(sqlCompletionCapture.catalog?.tables).toEqual(
        expect.arrayContaining([
          { schema: null, name: "users" },
          { schema: "app", name: "users" },
          { schema: "app", name: "orders" },
        ]),
      );
    });
    expect(tableRequests).toEqual(["app"]);
    expect(columnRequests).toEqual([]);
    expect(sqlCompletionCapture.catalog?.columns).toEqual([]);

    // Typing FROM app.users triggers a single lazy column fetch for app.users only (not orders).
    const editor = screen.getByTestId("query-sql-editor");
    await userEvent.clear(editor);
    await userEvent.type(editor, "SELECT u. FROM app.users u");
    await waitFor(() => {
      expect(sqlCompletionCapture.catalog?.columns).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ schema: "app", table: "users", name: "email" }),
        ]),
      );
    });
    expect(columnRequests).toEqual(["app.users"]);

    // Calling Monaco's onResolveCompletion for `<other_schema>.` fetches "reporting" tables once and deduplicates.
    const resolveCompletion = getCapturedResolver();
    if (resolveCompletion === undefined) throw new Error("missing onResolveCompletion");
    const [firstResolved, secondResolved] = await Promise.all([
      resolveCompletion({ qualifier: "reporting", sql: "SELECT * FROM reporting." }),
      resolveCompletion({ qualifier: "reporting", sql: "SELECT * FROM reporting." }),
    ]);
    expect(firstResolved?.tables).toEqual(
      expect.arrayContaining([{ schema: "reporting", name: "daily_metrics" }]),
    );
    expect(secondResolved?.tables).toEqual(
      expect.arrayContaining([{ schema: "reporting", name: "daily_metrics" }]),
    );
    expect(tableRequests).toEqual(["app", "reporting"]);
    expect(columnRequests).toEqual(["app.users"]);
  });

  it("lists every granted datasource in both the metadata tree and toolbar selector and switches cleanly", async () => {
    seedQueryScenario("query-session");
    stubSession(false);

    // Return both analytics-mysql and analytics-pg on the active session.
    server.use(
      http.get(apiRoute("/query-sessions/:sessionId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          request_id: "req-multi-ds",
          retryable: false,
          data: {
            id: "6f0f2b3c-5555-4555-8555-000000000101",
            flow_id: QUERY_FIXTURE_FLOW_ID,
            grant_id: null,
            state: "active",
            created_at: "2026-09-01T07:30:00Z",
            closed_at: null,
            capabilities: [
              {
                datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
                datasource_name: "analytics-mysql",
                state: "active",
                can_query: true,
                can_export: true,
              },
              {
                datasource_id: QUERY_FIXTURE_DS_PG_ID,
                datasource_name: "analytics-pg",
                state: "active",
                can_query: true,
                can_export: false,
              },
            ],
            executions: [],
          },
        }),
      ),
      http.get(apiRoute("/query-sessions/:sessionId/metadata/schemas"), ({ request }) => {
        const dsId = new URL(request.url).searchParams.get("datasource_id");
        const items = dsId === QUERY_FIXTURE_DS_PG_ID ? [{ name: "public" }] : [{ name: "app" }];
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          request_id: "req-multi-ds-schemas",
          retryable: false,
          data: { items },
        });
      }),
    );

    renderAt("/query/sessions/6f0f2b3c-5555-4555-8555-000000000101");
    await screen.findByTestId("query-workspace");

    // Both analytics-mysql and analytics-pg appear as top-level datasource nodes in the left tree.
    const dsButtons = await screen.findAllByTestId(/^query-tree-datasource-/);
    expect(dsButtons).toHaveLength(2);
    expect(dsButtons.map((node) => node.textContent)).toEqual([
      expect.stringContaining("analytics-mysql"),
      expect.stringContaining("analytics-pg"),
    ]);

    // Toolbar exposes the datasource selector initialized to analytics-mysql.
    const dsSelect = screen.getByTestId("query-datasource-select");
    expect(dsSelect).toHaveTextContent("analytics-mysql");

    // Select schema on analytics-mysql, then switch to analytics-pg via the left tree.
    await chooseSchema("app");
    const pgNode = dsButtons[1];
    if (pgNode === undefined) throw new Error("missing pg datasource node");
    await userEvent.click(pgNode);

    await waitFor(() => {
      expect(screen.getByTestId("query-datasource-select")).toHaveTextContent("analytics-pg");
    });
    // Expanding analytics-pg loads its PostgreSQL schemas (public) and resets the stale schema pick.
    expect(await screen.findByTestId("query-tree-schema-public")).toBeInTheDocument();
    expect(screen.getByTestId("query-schema-input")).not.toHaveTextContent("app");

    // Switching back via the toolbar selector restores analytics-mysql cleanly.
    await userEvent.click(screen.getByTestId("query-datasource-select"));
    await userEvent.click(await screen.findByRole("option", { name: "analytics-mysql" }));
    await waitFor(() => {
      expect(screen.getByTestId("query-datasource-select")).toHaveTextContent("analytics-mysql");
    });
  });

  it("fires a single execution and download under StrictMode and locale changes, and suppresses late download on cancel or close", async () => {
    const xlsxModule = await import("@/features/query/xlsx-export");
    const downloadSpy = vi.spyOn(xlsxModule, "downloadXlsx").mockImplementation(() => {});
    const exportCtx = {
      datasourceId: QUERY_FIXTURE_DS_MYSQL_ID,
      schemaName: "app",
      sql: "select id from app.users limit 1",
    };

    try {
      let executeCalls = 0;
      let resolveFirst!: () => void;
      server.use(
        http.post(apiRoute("/query-sessions/:id/executions"), async () => {
          executeCalls += 1;
          await new Promise<void>((resolve) => {
            resolveFirst = resolve;
          });
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              execution_id: "exec-strict-1",
              columns: [{ name: "id", type: "int" }],
              rows: [[1]],
              page: { next_cursor: null, has_more: false },
              elapsed_ms: 5,
            },
            request_id: "req-strict-1",
          });
        }),
      );

      const view = render(
        <StrictMode>
          <ExportDialog context={exportCtx} sessionId="6f0f2b3c-5555-4555-8555-000000000101" onClose={() => {}} />
        </StrictMode>,
      );
      await waitFor(() => {
        expect(executeCalls).toBe(1);
      });
      await i18next.changeLanguage("en-US");
      resolveFirst();
      await screen.findByTestId("query-export-done");
      expect(executeCalls).toBe(1);
      expect(downloadSpy).toHaveBeenCalledTimes(1);
      view.unmount();
      await i18next.changeLanguage("zh-CN");

      downloadSpy.mockClear();
      let resolveCancelRun!: () => void;
      server.use(
        http.post(apiRoute("/query-sessions/:id/executions"), async () => {
          await new Promise<void>((resolve) => {
            resolveCancelRun = resolve;
          });
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              execution_id: "exec-cancel-1",
              columns: [{ name: "id", type: "int" }],
              rows: [[1]],
              page: { next_cursor: null, has_more: false },
              elapsed_ms: 5,
            },
            request_id: "req-cancel-1",
          });
        }),
      );
      const cancelView = render(
        <ExportDialog context={exportCtx} sessionId="6f0f2b3c-5555-4555-8555-000000000101" onClose={() => {}} />,
      );
      await userEvent.click(await screen.findByTestId("query-export-cancel"));
      resolveCancelRun();
      await screen.findByTestId("query-export-failure");
      expect(downloadSpy).not.toHaveBeenCalled();
      cancelView.unmount();

      downloadSpy.mockClear();
      let resolveCloseRun!: () => void;
      const onClose = vi.fn();
      server.use(
        http.post(apiRoute("/query-sessions/:id/executions"), async () => {
          await new Promise<void>((resolve) => {
            resolveCloseRun = resolve;
          });
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              execution_id: "exec-close-1",
              columns: [{ name: "id", type: "int" }],
              rows: [[1]],
              page: { next_cursor: null, has_more: false },
              elapsed_ms: 5,
            },
            request_id: "req-close-1",
          });
        }),
      );
      const closeView = render(
        <ExportDialog context={exportCtx} sessionId="6f0f2b3c-5555-4555-8555-000000000101" onClose={onClose} />,
      );
      await userEvent.click(await screen.findByTestId("query-export-close"));
      expect(onClose).toHaveBeenCalledTimes(1);
      closeView.unmount();
      resolveCloseRun();
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(downloadSpy).not.toHaveBeenCalled();
    } finally {
      await i18next.changeLanguage("zh-CN");
      downloadSpy.mockRestore();
    }
  });
});
