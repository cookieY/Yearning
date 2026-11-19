import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HttpResponse, http } from "msw";
import { server } from "@/test/msw/server";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import "@/shared/i18n";
import { EvidenceSheet } from "./evidence-sheet";

/**
 * The evidence sheet is the sensitive-data boundary component (migration
 * contract §6): normalized facts always render; the raw payload only shows
 * after an explicit audited reveal, carries the server watermark, survives
 * in memory only (wiped on close) and copies exclusively through the
 * copy-audit API. The 7-day retention countdown stays visible.
 */

const EVIDENCE_ID = "4f6f1a2b-0000-4000-8000-00000000be01";
const FINDING_ID = "4f6f1a2b-0000-4000-8000-00000000f101";

function finding(): ReviewFinding {
  return {
    id: FINDING_ID,
    stage_position: 1,
    fingerprint_group_id: null,
    category: "performance",
    severity: "medium",
    title: "Missing index",
    message: "Statement `SELECT 1` cannot hit an index.",
    suggestion: null,
    model_confidence: null,
    evidence_ids: [EVIDENCE_ID],
  };
}

function envelope(data: unknown) {
  return { err_code: 0, message: "ok", data, request_id: FINDING_ID };
}

function renderSheet(open: boolean) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <EvidenceSheet
        findings={[finding()]}
        open={open}
        onOpenChange={() => {}}
      />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  server.resetHandlers();
  server.use(
    http.get("*/review-findings/:findingId/evidence", () =>
      HttpResponse.json(
        envelope([
          {
            id: EVIDENCE_ID,
            source_kind: "tool_call",
            source_reference: "built-in:mysql.table_stats",
            fact_status: "known",
            normalized_fact: { object_type: "table", object_name: "orders" },
            has_raw_payload: true,
            raw_payload_expires_at: new Date(
              Date.now() + 6 * 24 * 3600 * 1000,
            ).toISOString(),
            collected_at: new Date().toISOString(),
          },
        ]),
      ),
    ),
  );
});

describe("EvidenceSheet", () => {
  it("groups evidence under per-finding headers for multiple findings (2026-10-31)", async () => {
    const secondEvidenceId = "4f6f1a2b-0000-4000-8000-00000000be02";
    const secondFinding: ReviewFinding = {
      ...finding(),
      id: "4f6f1a2b-0000-4000-8000-00000000f102",
      title: "Target table does not exist",
    };
    server.use(
      http.get("*/review-findings/:findingId/evidence", ({ params }) =>
        HttpResponse.json(
          envelope(
            params.findingId === FINDING_ID
              ? [
                  {
                    id: EVIDENCE_ID,
                    source_kind: "tool_call",
                    source_reference: "built-in:mysql.table_stats",
                    fact_status: "known",
                    normalized_fact: { object_type: "table", object_name: "orders" },
                    has_raw_payload: false,
                    raw_payload_expires_at: null,
                    collected_at: new Date().toISOString(),
                  },
                ]
              : [
                  {
                    id: secondEvidenceId,
                    source_kind: "tool_call",
                    source_reference: "built-in:pg.schema_metadata",
                    fact_status: "known",
                    normalized_fact: { object_type: "table", object_name: "x" },
                    has_raw_payload: false,
                    raw_payload_expires_at: null,
                    collected_at: new Date().toISOString(),
                  },
                ],
          ),
        ),
      ),
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <EvidenceSheet
          findings={[finding(), secondFinding]}
          open
          onOpenChange={() => {}}
        />
      </QueryClientProvider>,
    );
    // One heading group per finding in the exact order passed; evidence items are grouped under their respective finding.
    const headers = await screen.findAllByTestId("evidence-finding-header");
    expect(headers).toHaveLength(2);
    expect(headers[0]).toHaveTextContent("Missing index");
    expect(headers[1]).toHaveTextContent("Target table does not exist");
    const items = screen.getAllByTestId("evidence-item");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("built-in:mysql.table_stats");
    expect(items[1]).toHaveTextContent("built-in:pg.schema_metadata");
  });

  it("renders a per-finding header even when a group member carries no evidence", async () => {
    server.use(
      http.get("*/review-findings/:findingId/evidence", ({ params }) =>
        HttpResponse.json(
          envelope(
            params.findingId === FINDING_ID
              ? [
                  {
                    id: EVIDENCE_ID,
                    source_kind: "tool_call",
                    source_reference: "built-in:mysql.table_stats",
                    fact_status: "known",
                    normalized_fact: { object_type: "table", object_name: "orders" },
                    has_raw_payload: false,
                    raw_payload_expires_at: null,
                    collected_at: new Date().toISOString(),
                  },
                ]
              : [],
          ),
        ),
      ),
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <EvidenceSheet
          findings={[finding(), { ...finding(), id: "4f6f1a2b-0000-4000-8000-00000000f103", title: "Finding without evidence" }]}
          open
          onOpenChange={() => {}}
        />
      </QueryClientProvider>,
    );
    const headers = await screen.findAllByTestId("evidence-finding-header");
    expect(headers).toHaveLength(2);
    // Only the first finding carries evidence items; findings without evidence keep only their heading group (never borrowing another finding's evidence).
    expect(screen.getAllByTestId("evidence-item")).toHaveLength(1);
    expect(headers[1]).toHaveTextContent("Finding without evidence");
  });


  it("renders normalized evidence without any raw payload", async () => {
    renderSheet(true);
    expect(await screen.findByText("built-in:mysql.table_stats")).toBeVisible();
    expect(screen.getByTestId("evidence-item").textContent).toContain("orders");
    // Raw content only appears after the explicit reveal.
    expect(screen.queryByTestId(`raw-view-${EVIDENCE_ID}`)).toBeNull();
  });

  it("shows the retention countdown while the raw payload exists", async () => {
    renderSheet(true);
    const item = await screen.findByTestId("evidence-item");
    expect(item.textContent).toContain("\u4fdd\u7559\u671f");
    expect(item.textContent).toMatch(/\u7ea6 \d \u5929/);
  });

  it("reveals the raw payload with the server watermark after an explicit action", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("*/review-evidence/:evidenceId/raw-reveals", () =>
        HttpResponse.json(
          envelope({
            reveal_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9e01".replace("r", "9"),
            content_type: "application/json",
            raw_payload: { tool: "table_stats", note: "raw-marker" },
            watermark: "henry · 2026-08-30T10:00:00Z · evidence:1",
            valid_until: new Date(Date.now() + 600_000).toISOString(),
          }),
        ),
      ),
    );
    renderSheet(true);
    await screen.findByText("built-in:mysql.table_stats");
    await user.click(screen.getByTestId(`reveal-raw-${EVIDENCE_ID}`));
    const rawView = await screen.findByTestId(`raw-view-${EVIDENCE_ID}`);
    expect(rawView.textContent).toContain("raw-marker");
    // The dynamic watermark identifies the actor, server time and resource.
    expect(rawView.textContent).toContain("henry ·");
    expect(rawView.textContent).toContain("evidence:1");
  });

  it("copies only through the audit API and reports the audited call", async () => {
    const user = userEvent.setup();
    const clipboardWrite = vi
      .spyOn(navigator.clipboard, "writeText")
      .mockResolvedValue(undefined);
    let copyAuditCalls = 0;
    server.use(
      http.post("*/review-evidence/:evidenceId/raw-reveals", () =>
        HttpResponse.json(
          envelope({
            reveal_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
            content_type: "application/json",
            raw_payload: { note: "raw-marker" },
            watermark: "henry · server-time",
            valid_until: new Date(Date.now() + 600_000).toISOString(),
          }),
        ),
      ),
      http.post("*/review-evidence/:evidenceId/raw-copy-events", () => {
        copyAuditCalls += 1;
        return HttpResponse.json(envelope(null));
      }),
    );
    renderSheet(true);
    await screen.findByText("built-in:mysql.table_stats");
    await user.click(screen.getByTestId(`reveal-raw-${EVIDENCE_ID}`));
    await screen.findByTestId(`raw-view-${EVIDENCE_ID}`);
    await user.click(screen.getByTestId(`copy-raw-${EVIDENCE_ID}`));
    await waitFor(() => {
      expect(copyAuditCalls).toBe(1);
    });
    // The clipboard write carried the revealed plaintext.
    expect(clipboardWrite).toHaveBeenCalledWith(expect.stringContaining("raw-marker"));
    // Success stays silent: no error alert for a fully audited copy.
    expect(screen.queryByTestId("evidence-copy-error")).toBeNull();
  });

  it("audits only after the clipboard write resolved (SEC-0035 ordering)", async () => {
    const user = userEvent.setup();
    let copyAuditCalls = 0;
    // A deferred clipboard write proves the ordering: the audit endpoint
    // stays untouched while the write is in flight; resolving it releases
    // the audit request.
    let resolveWrite: () => void = () => {};
    const writeGate = new Promise<void>((resolve) => {
      resolveWrite = resolve;
    });
    vi.spyOn(navigator.clipboard, "writeText").mockImplementation(() =>
      writeGate.then(() => undefined),
    );
    server.use(
      http.post("*/review-evidence/:evidenceId/raw-reveals", () =>
        HttpResponse.json(
          envelope({
            reveal_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
            content_type: "application/json",
            raw_payload: { note: "raw-marker" },
            watermark: "henry · server-time",
            valid_until: new Date(Date.now() + 600_000).toISOString(),
          }),
        ),
      ),
      http.post("*/review-evidence/:evidenceId/raw-copy-events", () => {
        copyAuditCalls += 1;
        return HttpResponse.json(envelope(null));
      }),
    );
    renderSheet(true);
    await screen.findByText("built-in:mysql.table_stats");
    await user.click(screen.getByTestId(`reveal-raw-${EVIDENCE_ID}`));
    await screen.findByTestId(`raw-view-${EVIDENCE_ID}`);
    await user.click(screen.getByTestId(`copy-raw-${EVIDENCE_ID}`));
    expect(copyAuditCalls).toBe(0);
    resolveWrite();
    await waitFor(() => {
      expect(copyAuditCalls).toBe(1);
    });
  });

  it("surfaces an audit failure after the copy instead of swallowing it", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    server.use(
      http.post("*/review-evidence/:evidenceId/raw-reveals", () =>
        HttpResponse.json(
          envelope({
            reveal_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
            content_type: "application/json",
            raw_payload: { note: "raw-marker" },
            watermark: "henry · server-time",
            valid_until: new Date(Date.now() + 600_000).toISOString(),
          }),
        ),
      ),
      http.post("*/review-evidence/:evidenceId/raw-copy-events", () =>
        HttpResponse.json({
          err_code: 1002,
          message: "evidence not found",
          data: null,
          request_id: FINDING_ID,
          retryable: false,
        }),
      ),
    );
    renderSheet(true);
    await screen.findByText("built-in:mysql.table_stats");
    await user.click(screen.getByTestId(`reveal-raw-${EVIDENCE_ID}`));
    await screen.findByTestId(`raw-view-${EVIDENCE_ID}`);
    await user.click(screen.getByTestId(`copy-raw-${EVIDENCE_ID}`));
    const alert = await screen.findByTestId("evidence-copy-error");
    expect(alert.textContent).toContain("\u590d\u5236\u5ba1\u8ba1\u4e0a\u62a5\u5931\u8d25");
    expect(alert.textContent).toContain(FINDING_ID);
  });

  it("surfaces a clipboard failure and sends no audit for a copy that never happened", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("denied"));
    let copyAuditCalls = 0;
    server.use(
      http.post("*/review-evidence/:evidenceId/raw-reveals", () =>
        HttpResponse.json(
          envelope({
            reveal_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
            content_type: "application/json",
            raw_payload: { note: "raw-marker" },
            watermark: "henry · server-time",
            valid_until: new Date(Date.now() + 600_000).toISOString(),
          }),
        ),
      ),
      http.post("*/review-evidence/:evidenceId/raw-copy-events", () => {
        copyAuditCalls += 1;
        return HttpResponse.json(envelope(null));
      }),
    );
    renderSheet(true);
    await screen.findByText("built-in:mysql.table_stats");
    await user.click(screen.getByTestId(`reveal-raw-${EVIDENCE_ID}`));
    await screen.findByTestId(`raw-view-${EVIDENCE_ID}`);
    await user.click(screen.getByTestId(`copy-raw-${EVIDENCE_ID}`));
    const alert = await screen.findByTestId("evidence-copy-error");
    expect(alert.textContent).toContain("\u590d\u5236\u5931\u8d25");
    // The plaintext never reached the clipboard — there is nothing to audit.
    await waitFor(() => {
      expect(copyAuditCalls).toBe(0);
    });
  });

  it("wipes the revealed plaintext when the sheet closes", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("*/review-evidence/:evidenceId/raw-reveals", () =>
        HttpResponse.json(
          envelope({
            reveal_id: "0198d9cc-e65d-7b9d-a8aa-3c81945f9901",
            content_type: "application/json",
            raw_payload: { note: "raw-marker" },
            watermark: "henry",
            valid_until: new Date(Date.now() + 600_000).toISOString(),
          }),
        ),
      ),
    );
    const { rerender } = renderSheet(true);
    await screen.findByText("built-in:mysql.table_stats");
    await user.click(screen.getByTestId(`reveal-raw-${EVIDENCE_ID}`));
    await screen.findByTestId(`raw-view-${EVIDENCE_ID}`);
    // Closing unmounts the open sheet (open=false) — the component wipes its
    // in-memory plaintext, so nothing raw survives a reopen.
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <EvidenceSheet findings={[finding()]} open={false} onOpenChange={() => {}} />
      </QueryClientProvider>,
    );
    expect(screen.queryByTestId(`raw-view-${EVIDENCE_ID}`)).toBeNull();
  });

  it("shows the mapped error with request id when the evidence query fails", async () => {
    server.use(
      http.get("*/review-findings/:findingId/evidence", () =>
        HttpResponse.json(
          {
            err_code: 1002,
            message: "not found",
            data: null,
            request_id: "99999999-9999-4999-8999-999999999999",
            retryable: false,
          },
        ),
      ),
    );
    renderSheet(true);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("99999999-9999-4999-8999-999999999999");
  });

  it("states when a finding has no linked evidence at all", async () => {
    server.use(
      http.get("*/review-findings/:findingId/evidence", () =>
        HttpResponse.json(envelope([])),
      ),
    );
    renderSheet(true);
    expect(await screen.findByText("\u8be5\u98ce\u9669\u53d1\u73b0\u6682\u65e0\u5173\u8054\u8bc1\u636e\u3002")).toBeVisible();
  });

  it("shows only normalized evidence after the raw payload expired", async () => {
    server.use(
      http.get("*/review-findings/:findingId/evidence", () =>
        HttpResponse.json(
          envelope([
            {
              id: EVIDENCE_ID,
              source_kind: "tool_call",
              source_reference: "built-in:mysql.table_stats",
              fact_status: "known",
              normalized_fact: {},
              has_raw_payload: false,
              raw_payload_expires_at: null,
              collected_at: new Date().toISOString(),
            },
          ]),
        ),
      ),
    );
    renderSheet(true);
    const item = await screen.findByTestId("evidence-item");
    expect(item.textContent).not.toContain("\u89e3\u5bc6\u67e5\u770b\u539f\u59cb\u6570\u636e");
  });
});
