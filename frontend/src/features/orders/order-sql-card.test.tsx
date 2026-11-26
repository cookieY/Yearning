import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HttpResponse, http } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { testBusinessErrorJson, testEnvelope } from "@/test/envelope";
import "@/shared/i18n";
import { OrderSqlCard } from "./order-sql-card";

/**
 * SEC-0035 copy-audit regression: the clipboard write is issued inside the
 * click's user-activation window, the audit request fires only after the
 * plaintext actually reached the clipboard, and both failure paths surface
 * through the inline error alert — no silent swallowing.
 *
 * The revealed plaintext renders through the collapsible-08 file viewer
 * (sql-file-viewer): mono header with the total line count, numbered lines,
 * first rows visible, remainder behind the show-more trigger.
 */

const ORDER_ID = "0d6f1a2b-0000-4000-8000-000000000701";
const REVEAL_ID = "0d6f1a2b-0000-4000-8000-00000000a701";
const REQUEST_ID = "0d6f1a2b-0000-4000-8000-000000000702";

function envelope(data: unknown) {
  return testEnvelope(data, REQUEST_ID);
}

function revealResponse(sql = "UPDATE orders SET status = 1;") {
  return envelope({
    reveal_id: REVEAL_ID,
    sql,
    watermark: "Yearning SQL Viewer: henry @ 2026-10-21T00:00:00Z",
    valid_until: "2026-10-21T00:05:00Z",
  });
}

function renderCard() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <OrderSqlCard orderId={ORDER_ID} />
    </QueryClientProvider>,
  );
}

async function reveal(user: ReturnType<typeof userEvent.setup>, sql?: string) {
  server.use(
    http.post(apiRoute("/change-orders/:orderId/sql-reveals"), () =>
      HttpResponse.json(revealResponse(sql)),
    ),
  );
  renderCard();
  await user.click(await screen.findByTestId("reveal-order-sql"));
  await screen.findByTestId("order-sql-view");
}

beforeEach(() => {
  server.resetHandlers();
});

describe("OrderSqlCard copy audit (SEC-0035)", () => {
  it("writes the clipboard first and audits only after the write resolved", async () => {
    const user = userEvent.setup();
    let copyAuditCalls = 0;
    const seenBodies: string[] = [];
    // A deferred clipboard write proves the ordering: while the write is in
    // flight the audit endpoint must stay untouched; resolving it releases
    // the audit request.
    let resolveWrite: () => void = () => {};
    const writeGate = new Promise<void>((resolve) => {
      resolveWrite = resolve;
    });
    const clipboardWrite = vi
      .spyOn(navigator.clipboard, "writeText")
      .mockImplementation(() => writeGate.then(() => undefined));
    server.use(
      http.post(apiRoute("/change-orders/:orderId/sql-copy-events"), async ({ request }) => {
        copyAuditCalls += 1;
        seenBodies.push(await request.text());
        return HttpResponse.json(envelope(null));
      }),
    );

    await reveal(user);
    await user.click(screen.getByTestId("copy-order-sql"));
    // Clipboard write happened immediately; the audit waits for it.
    expect(clipboardWrite).toHaveBeenCalledWith("UPDATE orders SET status = 1;");
    expect(copyAuditCalls).toBe(0);
    resolveWrite();
    await waitFor(() => {
      expect(copyAuditCalls).toBe(1);
    });
    // The audit references the reveal it came from — never the SQL content.
    expect(seenBodies[0]).toContain(REVEAL_ID);
    expect(seenBodies[0]).not.toContain("UPDATE orders");
    // Nothing failed: no error alert.
    expect(screen.queryByTestId("order-sql-error")).toBeNull();
  });

  it("surfaces an audit failure after the copy instead of swallowing it", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    server.use(
      http.post(apiRoute("/change-orders/:orderId/sql-copy-events"), () =>
        testBusinessErrorJson(1002, "order not found", { requestId: REQUEST_ID }),
      ),
    );

    await reveal(user);
    await user.click(screen.getByTestId("copy-order-sql"));
    const alert = await screen.findByTestId("order-sql-error");
    expect(alert.textContent).toContain("\u590d\u5236\u5ba1\u8ba1\u4e0a\u62a5\u5931\u8d25");
    expect(alert.textContent).toContain(REQUEST_ID);
  });

  it("surfaces a clipboard failure and sends no audit for a copy that never happened", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("denied"));
    let copyAuditCalls = 0;
    server.use(
      http.post(apiRoute("/change-orders/:orderId/sql-copy-events"), () => {
        copyAuditCalls += 1;
        return HttpResponse.json(envelope(null));
      }),
    );

    await reveal(user);
    await user.click(screen.getByTestId("copy-order-sql"));
    const alert = await screen.findByTestId("order-sql-error");
    expect(alert.textContent).toContain("\u590d\u5236\u5931\u8d25");
    // The plaintext never reached the clipboard — there is nothing to audit.
    await waitFor(() => {
      expect(copyAuditCalls).toBe(0);
    });
  });
});

describe("OrderSqlCard frozen-SQL file viewer (collapsible-08)", () => {
  const PREVIEW = 5;
  const longSql = Array.from({ length: 8 }, (_, i) => `SELECT ${String(i + 1)};`).join("\n");

  it("shows the file header with the total line count and keeps only the preview lines mounted", async () => {
    const user = userEvent.setup();
    await reveal(user, longSql);
    const viewer = screen.getByTestId("order-sql-readonly");
    // Line text is split into multiple token spans by syntax highlighting; full-line assertions concatenate textContent.
    const codeText = () => viewer.textContent;
    expect(codeText()).toContain("SELECT 1;");
    expect(codeText()).toContain(`SELECT ${String(PREVIEW)};`);
    // The collapsible panel is unmounted while closed: line 6+ must not be
    // in the DOM at all, not merely hidden.
    expect(codeText()).not.toContain("SELECT 6;");
    expect(codeText()).not.toContain("SELECT 8;");
    expect(screen.getByTestId("order-sql-line-count").textContent).toBe("8 \u884c");
    expect(screen.getByTestId("order-sql-more-trigger").textContent).toContain("\u5c55\u5f00\u5269\u4f59 3 \u884c");
    // Syntax highlighting (collapsible-08 token vocabulary): keywords/numbers use dedicated classes, punctuation uses
    // muted — without introducing raw palette utility classes.
    expect(viewer.querySelector(".code-token-keyword")?.textContent).toBe("SELECT");
    expect(viewer.querySelector(".code-token-number")?.textContent).toBe("1");
  });

  it("expands the remaining lines on the show-more trigger and collapses back", async () => {
    const user = userEvent.setup();
    await reveal(user, longSql);
    const viewer = screen.getByTestId("order-sql-readonly");
    const codeText = () => viewer.textContent;
    const trigger = screen.getByTestId("order-sql-more-trigger");
    await user.click(trigger);
    expect(codeText()).toContain("SELECT 6;");
    expect(codeText()).toContain("SELECT 8;");
    expect(trigger.textContent).toContain("\u6536\u8d77");
    await user.click(trigger);
    expect(codeText()).not.toContain("SELECT 6;");
    expect(trigger.textContent).toContain("\u5c55\u5f00\u5269\u4f59 3 \u884c");
  });

  it("omits the collapsible chrome entirely for short statements", async () => {
    const user = userEvent.setup();
    await reveal(user);
    expect(screen.getByTestId("order-sql-line-count").textContent).toBe("1 \u884c");
    expect(screen.getByTestId("order-sql-readonly").textContent).toContain(
      "UPDATE orders SET status = 1;",
    );
    expect(screen.queryByTestId("order-sql-more-trigger")).toBeNull();
  });
});
