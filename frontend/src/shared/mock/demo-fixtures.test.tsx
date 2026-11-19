import { describe, expect, it, beforeEach, afterAll, beforeAll } from "vitest";
import { setupServer } from "msw/node";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "@/shared/i18n";
import {
  assistantFixtureHandlers,
  resetAssistantFixture,
} from "@/shared/mock/assistant-fixture";
import {
  diagnosticsFixtureHandlers,
  resetDiagnosticsFixture,
} from "@/shared/mock/diagnostics-fixture";
import { DemoBanner } from "@/app/shell/demo-banner";
import { CATALOG_DATASOURCES } from "@/shared/mock/flow-catalog";
import { readStoredIdentity, setMockIdentity } from "@/shared/mock/identity-store";
import { clearMockSuperSession } from "@/shared/dev/mock-super";

const server = setupServer(
  ...assistantFixtureHandlers(),
  ...diagnosticsFixtureHandlers(),
);

describe("Demo Mode MSW fixtures (AI Assistant & SQL Diagnostics)", () => {
  beforeAll(() => {
    server.listen({ onUnhandledRequest: "error" });
  });

  beforeEach(() => {
    resetAssistantFixture();
    resetDiagnosticsFixture();
    clearMockSuperSession();
    setMockIdentity("henry");
  });

  afterAll(() => {
    server.close();
  });

  it("serves seeded assistant sessions, SSE streaming messages, draft precheck and submission", async () => {
    const listRes = await fetch("http://localhost/api/v4/ai-assistant/sessions");
    const listJson = (await listRes.json()) as {
      err_code: number;
      data: { items: Array<{ id: string; title: string }> };
    };
    expect(listJson.err_code).toBe(0);
    expect(listJson.data.items.length).toBe(1);
    const seedSessionId = listJson.data.items[0]?.id ?? "";
    expect(seedSessionId).not.toBe("");

    const detailRes = await fetch(
      `http://localhost/api/v4/ai-assistant/sessions/${seedSessionId}`,
    );
    const detailJson = (await detailRes.json()) as {
      err_code: number;
      data: { messages: unknown[]; drafts: Array<{ id: string; sql_hmac: string }> };
    };
    expect(detailJson.err_code).toBe(0);
    expect(detailJson.data.messages.length).toBe(2);
    expect(detailJson.data.drafts.length).toBe(1);

    const sseRes = await fetch(
      `http://localhost/api/v4/ai-assistant/sessions/${seedSessionId}/messages`,
      {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
        },
        body: JSON.stringify({
          content: "Please optimize my slow index query",
          capability_hint: "sql_optimize",
        }),
      },
    );
    expect(sseRes.headers.get("content-type")).toContain("text/event-stream");
    const sseText = await sseRes.text();
    expect(sseText).toContain("event: progress");
    expect(sseText).toContain("event: tool");
    expect(sseText).toContain("event: delta");
    expect(sseText).toContain("event: done");

    const draft = detailJson.data.drafts[0];
    if (!draft) throw new Error("expected seeded assistant draft");
    const precheckRes = await fetch(
      `http://localhost/api/v4/ai-assistant/sessions/${seedSessionId}/drafts/${draft.id}/prechecks`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          flow_id: "4f6f1a2b-0000-4000-8000-00000000d201",
          revision: 1,
          sql_hmac: draft.sql_hmac,
        }),
      },
    );
    const precheckJson = (await precheckRes.json()) as {
      err_code: number;
      data: { ephemeral_run_id: string; draft: { submission_state: string } };
    };
    expect(precheckJson.err_code).toBe(0);
    expect(precheckJson.data.draft.submission_state).toBe("precheck_ready");

    const submitRes = await fetch(
      `http://localhost/api/v4/ai-assistant/sessions/${seedSessionId}/drafts/${draft.id}/order-submissions`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          flow_id: "4f6f1a2b-0000-4000-8000-00000000d201",
          run_id: precheckJson.data.ephemeral_run_id,
          revision: 1,
          sql_hmac: draft.sql_hmac,
          title: "Add composite index on orders",
        }),
      },
    );
    const submitJson = (await submitRes.json()) as {
      err_code: number;
      data: { display_number: string };
    };
    expect(submitJson.err_code).toBe(0);
    expect(submitJson.data.display_number).toBe("YR-20260901-000001");
  });

  it("creates and deletes assistant sessions and supports platform help replies", async () => {
    const createRes = await fetch("http://localhost/api/v4/ai-assistant/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: "Workflow Help" }),
    });
    const created = (await createRes.json()) as {
      err_code: number;
      data: { id: string };
    };
    expect(created.err_code).toBe(0);

    const msgRes = await fetch(
      `http://localhost/api/v4/ai-assistant/sessions/${created.data.id}/messages`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          content: "How does the approval workflow work?",
          capability_hint: "platform_help",
        }),
      },
    );
    const msgJson = (await msgRes.json()) as {
      err_code: number;
      data: { assistant_message: { capability: string }; sql_draft: null };
    };
    expect(msgJson.err_code).toBe(0);
    expect(msgJson.data.assistant_message.capability).toBe("platform_help");
    expect(msgJson.data.sql_draft).toBeNull();

    const delRes = await fetch(
      `http://localhost/api/v4/ai-assistant/sessions/${created.data.id}`,
      { method: "DELETE" },
    );
    const delJson = (await delRes.json()) as { err_code: number };
    expect(delJson.err_code).toBe(0);
  });

  it("serves SQL diagnostics datasources, overview, statement detail, AI report and optimization submission", async () => {
    const dsId = CATALOG_DATASOURCES[0]?.id ?? "";
    expect(dsId).not.toBe("");

    const dsListRes = await fetch("http://localhost/api/v4/admin/diagnostics/datasources");
    const dsListJson = (await dsListRes.json()) as {
      err_code: number;
      data: { items: Array<{ id: string }> };
    };
    expect(dsListJson.err_code).toBe(0);
    expect(dsListJson.data.items.length).toBeGreaterThanOrEqual(3);

    const overviewRes = await fetch(
      `http://localhost/api/v4/admin/diagnostics/datasources/${dsId}/overview`,
    );
    const overviewJson = (await overviewRes.json()) as {
      err_code: number;
      data: { statements: Array<{ statement_id: string }> };
    };
    expect(overviewJson.err_code).toBe(0);
    const stmtId = overviewJson.data.statements[0]?.statement_id ?? "";
    expect(stmtId).not.toBe("");

    const snapRes = await fetch(
      `http://localhost/api/v4/admin/diagnostics/datasources/${dsId}/snapshots`,
      { method: "POST" },
    );
    const snapJson = (await snapRes.json()) as { err_code: number; data: { probe_state: string } };
    expect(snapJson.err_code).toBe(0);
    expect(snapJson.data.probe_state).toBe("ready");

    const detailRes = await fetch(
      `http://localhost/api/v4/admin/diagnostics/datasources/${dsId}/statements/${stmtId}`,
    );
    const detailJson = (await detailRes.json()) as {
      err_code: number;
      data: { latest_ai_report: { report_id: string } | null };
    };
    expect(detailJson.err_code).toBe(0);
    expect(detailJson.data.latest_ai_report).not.toBeNull();

    const reportRes = await fetch(
      `http://localhost/api/v4/admin/diagnostics/datasources/${dsId}/statements/${stmtId}/ai-reports`,
      { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
    );
    const reportJson = (await reportRes.json()) as {
      err_code: number;
      data: { report_id: string; submission_state: string };
    };
    expect(reportJson.err_code).toBe(0);
    const reportId = reportJson.data.report_id;

    const precheckRes = await fetch(
      `http://localhost/api/v4/admin/diagnostics/datasources/${dsId}/ai-reports/${reportId}/prechecks`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: "4f6f1a2b-0000-4000-8000-00000000d201" }),
      },
    );
    const precheckJson = (await precheckRes.json()) as {
      err_code: number;
      data: { run_id: string; report: { submission_state: string } };
    };
    expect(precheckJson.err_code).toBe(0);
    expect(precheckJson.data.report.submission_state).toBe("prechecked");

    const submitRes = await fetch(
      `http://localhost/api/v4/admin/diagnostics/datasources/${dsId}/ai-reports/${reportId}/order-submissions`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          flow_id: "4f6f1a2b-0000-4000-8000-00000000d201",
          ephemeral_run_id: precheckJson.data.run_id,
          title: "Index optimization order",
        }),
      },
    );
    const submitJson = (await submitRes.json()) as {
      err_code: number;
      data: { order_display_no: string; report: { submission_state: string } };
    };
    expect(submitJson.err_code).toBe(0);
    expect(submitJson.data.report.submission_state).toBe("submitted");

    const cfgPutRes = await fetch(
      `http://localhost/api/v4/admin/datasources/${dsId}/diagnostics-config`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled: true, interval_seconds: 120, top_n: 150 }),
      },
    );
    const cfgPutJson = (await cfgPutRes.json()) as {
      err_code: number;
      data: { interval_seconds: number; top_n: number };
    };
    expect(cfgPutJson.err_code).toBe(0);
    expect(cfgPutJson.data.interval_seconds).toBe(120);
    expect(cfgPutJson.data.top_n).toBe(150);
  });

  it("renders DemoBanner and switches demo personas", () => {
    const queryClient = new QueryClient();
    render(
      <QueryClientProvider client={queryClient}>
        <DemoBanner />
      </QueryClientProvider>,
    );
    expect(screen.getByTestId("demo-mode-banner")).toBeInTheDocument();

    const buttons = screen.getAllByRole("button");
    const fullAccessBtn = buttons[0];
    const approverBtn = buttons[2];
    const executorBtn = buttons[3];
    if (!fullAccessBtn || !approverBtn || !executorBtn) {
      throw new Error("expected demo persona buttons");
    }
    fireEvent.click(approverBtn);
    expect(readStoredIdentity()).toBe("reviewer-a");

    fireEvent.click(executorBtn);
    expect(readStoredIdentity()).toBe("executor-a");

    fireEvent.click(fullAccessBtn);
    expect(readStoredIdentity()).toBe("henry");
  });
});
