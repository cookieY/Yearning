import { fireEvent, within, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SessionProvider } from "@/features/auth/session-provider";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HttpResponse, http } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { grantSession } from "@/test/session";
import i18n from "@/shared/i18n";
import {
  FIXTURE_FLOW_ID,
  FIXTURE_OWNER_ID,
  fixtureRunIds,
  resetReviewFixture,
} from "@/shared/mock/review-fixture";
import { setMockAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { useDraftEditorStore } from "@/features/review/draft-editor-store";
import ChangesNewPage from "./changes-new-page";
import DraftWorkspacePage from "./draft-workspace-page";

/**
 * Submission wizard create-mode gates (§18.34) under the ephemeral precheck
 * contract (RCP-20261101): the AI precheck button sends the CURRENT editor SQL to
 * POST /review-runs verbatim — it never adopts a draft, never saves SQL;
 * Save and Return stays the only writer (adoption + SQL PUT); bulk import lands
 * SQL locally; submission posts /change-orders with {sql, run_id, metadata}.
 */

// jsdom cannot drive ProseMirror's contenteditable; the editor has its own
// unit test, so the wizard test swaps in a textarea with the same contract.
vi.mock("@/shared/components/markdown-editor", () => ({
  MarkdownEditor: ({ value, onChange, id, "data-testid": testId }: {
    value: string;
    onChange: (value: string) => void;
    id?: string;
    "data-testid"?: string;
  }) => (
    <textarea id={id} data-testid={testId} value={value} onChange={(event) => { onChange(event.target.value); }} />
  ),
}));

vi.mock("@/features/review/sql-editor-panel", () => ({
  SqlEditorPanel: ({
    value,
    onChange,
    readOnly = false,
    ...rest
  }: {
    value: string;
    onChange: (sql: string) => void;
    readOnly?: boolean;
    "data-testid"?: string;
  }) => (
    <textarea
      aria-label="SQL Editor Stub"
      value={value}
      readOnly={readOnly}
      onChange={(event) => {
        onChange(event.target.value);
      }}
      data-testid={rest["data-testid"]}
    />
  ),
}));

function renderWizard(initialEntry: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <SessionProvider>
        <MemoryRouter initialEntries={[initialEntry]}>
          <Routes>
            <Route path="/changes/new" element={<ChangesNewPage />} />
            <Route path="/changes/drafts/:draftId" element={<DraftWorkspacePage />} />
            <Route path="/changes/mine" element={<p data-testid="mine-page">mine</p>} />
            <Route path="/changes/orders/:orderId" element={<p data-testid="order-detail-stub">order</p>} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

function pickImportFile(content: string): void {
  const input = screen.getByTestId("bulk-import-input");
  const file = new File([content], "bulk.sql", { type: "text/plain" });
  Object.defineProperty(input, "files", {
    value: [file],
    configurable: true,
  });
  fireEvent.change(input);
}

function gate(): { promise: Promise<void>; open: () => void } {
  let open!: () => void;
  const promise = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { promise, open };
}

interface PrecheckCapture {
  bodies: Array<{ flow_id?: string; draft_id?: string; sql?: string }>;
  languages: Array<string | null>;
}

/** Captures POST /review-runs requests (server.use layers above the stateful
 * fixture: the override wins and progression is not needed for these tests). */
function capturePrechecks(): PrecheckCapture {
  const capture: PrecheckCapture = { bodies: [], languages: [] };
  server.use(
    http.post(apiRoute("/review-runs"), async ({ request }) => {
      capture.bodies.push((await request.json()) as PrecheckCapture["bodies"][number]);
      capture.languages.push(request.headers.get("Accept-Language"));
      return HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: { run_id: crypto.randomUUID(), result_ref: `/review-runs/${crypto.randomUUID()}` },
        request_id: FIXTURE_OWNER_ID,
      });
    }),
  );
  return capture;
}

beforeEach(() => {
  server.resetHandlers();
  resetReviewFixture();
  // The review fixture answers /users/me/flows only for the admin behavior
  // (the e2e mockSession(page, "admin") equivalent).
  setMockAuthBehavior("admin");
  grantSession();
});

afterEach(async () => {
  await i18n.changeLanguage("zh-CN");
});

describe("SubmissionWizard create mode (ephemeral precheck)", () => {
  it("prechecks the current editor SQL without adopting a draft", async () => {
    const capture = capturePrechecks();
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    // One-page layout: the editor is live from the start — no draft gate.
    expect(await screen.findByTestId("sql-editor")).toBeVisible();

    // No flow picked: the acting operation surfaces the near-field error.
    fireEvent.click(screen.getByTestId("run-review"));
    expect(screen.getByTestId("wizard-step-error").textContent).toContain("\u5ba1\u6838\u6d41\u7a0b");

    // Flow picked: the precheck sends the editor SQL verbatim — no draft is
    // created, no SQL is written (RCP-20261101). The title gate belongs to
    // Save and Return / Submit, not to the precheck.
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    const runReview = screen.getByTestId("run-review");
    fireEvent.click(runReview);
    fireEvent.click(runReview);
    await waitFor(() => {
      expect(capture.bodies).toHaveLength(1);
    });
    expect(capture.bodies[0]).toEqual({
      flow_id: FIXTURE_FLOW_ID,
      sql: "UPDATE orders SET status = 1;",
    });
    expect(capture.languages).toEqual(["zh-CN"]);
    expect(screen.getByTestId("changes-new-page")).toBeVisible();
    // Precheck never adopts a draft: the page carries no draft id, and the drafts list stays empty.
    expect(screen.getByTestId("changes-new-page").getAttribute("data-draft-id")).toBeNull();
    const drafts = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: unknown[] };
    };
    expect(drafts.data.items).toHaveLength(0);
  });

  it("sends the page language with the AI review request", async () => {
    await i18n.changeLanguage("en-US");
    const capture = capturePrechecks();

    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(await screen.findByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    fireEvent.click(screen.getByTestId("run-review"));

    await waitFor(() => {
      expect(capture.languages).toEqual(["en-US"]);
    });
  });

  it("keeps bulk import local — no server write on the import path", async () => {
    let sqlPutCalls = 0;
    server.use(
      http.put(apiRoute("/change-drafts/:draftId/sql"), () => {
        sqlPutCalls += 1;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: null,
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));

    fireEvent.click(screen.getByTestId("open-bulk-import"));
    expect(await screen.findByTestId("bulk-import-dialog")).toBeVisible();
    pickImportFile("SELECT 1;\n".repeat(1001));
    expect(await screen.findByTestId("bulk-import-ok")).toBeVisible();
    fireEvent.click(screen.getByTestId("bulk-import-confirm"));

    // The import lands in local state only: the virtualized browser reads
    // the local digest; no draft, no SQL PUT (RCP-20261101).
    expect(await screen.findByTestId("bulk-browser")).toBeVisible();
    await waitFor(() => {
      expect(screen.getByTestId("capacity-statements")).toHaveTextContent("1001");
    });
    expect(screen.queryByTestId("sql-editor")).not.toBeInTheDocument();
    expect(sqlPutCalls).toBe(0);
    const drafts = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: unknown[] };
    };
    expect(drafts.data.items).toHaveLength(0);
  });

  it("submits draftless with {sql, run_id, metadata} after a passing precheck", async () => {
    const submissions: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/change-orders"), async ({ request }) => {
        submissions.push((await request.json()) as Record<string, unknown>);
        // The success path only reads {id, display_number} for navigation.
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { id: "0d5e1f2b-0000-4000-8000-00000000de01", display_number: "YR-20261101-000001" },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "\u76f4\u63d0\u5de5\u5355" },
    });
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1 WHERE user_id = 42;" },
    });
    // Real fixture precheck: the run reaches ready on its own timers.
    fireEvent.click(screen.getByTestId("run-review"));
    await waitFor(() => {
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });

    fireEvent.click(screen.getByTestId("submit-draft"));
    expect(await screen.findByTestId("submit-confirm-dialog")).toBeVisible();
    fireEvent.click(screen.getByTestId("submit-confirm-accept"));
    await waitFor(() => {
      expect(screen.getByTestId("order-detail-stub")).toBeVisible();
    });
    expect(submissions).toHaveLength(1);
    expect(submissions[0]?.flow_id).toBe(FIXTURE_FLOW_ID);
    expect(submissions[0]?.sql).toBe("UPDATE orders SET status = 1 WHERE user_id = 42;");
    expect(submissions[0]?.run_id).toBe(fixtureRunIds().at(-1));
    expect(submissions[0]?.draft_id).toBeUndefined();
    expect(submissions[0]?.metadata).toEqual({ title: "\u76f4\u63d0\u5de5\u5355", rollback_enabled: true });
  });

  it("carries the desired execution time onto the implicitly created draft (§18.56)", async () => {
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "\u5b9a\u65f6\u5de5\u5355" },
    });
    // DateTimePicker (§18.56 addendum 2 desktop picker style): click input to open panel -> click ISO day ->
    // click bottom "Time" button to switch to hour/minute columns -> pick 09/30 -> Confirm.
    fireEvent.click(screen.getByTestId("draft-requested-for"));
    const tomorrow = new Date(Date.now() + 24 * 3600 * 1000);
    const iso = `${String(tomorrow.getFullYear())}-${String(tomorrow.getMonth() + 1).padStart(2, "0")}-${String(tomorrow.getDate()).padStart(2, "0")}`;
    fireEvent.click(await screen.findByRole("button", { name: iso }));
    fireEvent.click(screen.getByTestId("draft-requested-for-time-toggle"));
    fireEvent.click(within(screen.getByTestId("dtp-hours")).getByRole("button", { name: "09" }));
    fireEvent.click(within(screen.getByTestId("dtp-minutes")).getByRole("button", { name: "30" }));
    fireEvent.click(screen.getByTestId("draft-requested-for-confirm"));
    expect(screen.queryByTestId("draft-requested-for-panel")).toBeNull();
    expect(screen.getByTestId("draft-requested-for")).toHaveValue("2026-10-16 09:30".replace("2026-10-16", iso));
    // Save Draft stays the only adopter: it opts into the draft implicitly.
    fireEvent.click(screen.getByTestId("wizard-save-draft"));
    await waitFor(() => {
      expect(screen.getByTestId("mine-page")).toBeVisible();
    });
    const drafts = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: Array<{ requested_scheduled_for: string | null }> };
    };
    expect(drafts.data.items[0]?.requested_scheduled_for).not.toBeNull();
  });
});

describe("SubmissionWizard in-flight SQL saves", () => {
  it("prechecks the current editor text without saving it (precheck does not piggyback a save)", async () => {
    // owner ruling 2026-10-31 + RCP-20261101: precheck sends current editor SQL verbatim for review,
    // never writing SQL; new local text in the editor is preserved intact.
    const created = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u8fd0\u884c\u4e0d\u4fdd\u5b58" }),
      })
    ).json()) as {
      data: {
        id: string;
        version: number;
        revision: number;
        has_sql: boolean;
        [key: string]: unknown;
      };
    };
    const draft = created.data;
    const precheckBodies: Array<{ sql?: string; draft_id?: string }> = [];
    let sqlPutCalls = 0;
    server.use(
      http.put(apiRoute("/change-drafts/:draftId/sql"), () => {
        sqlPutCalls += 1;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { ...draft, has_sql: true },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
      http.post(apiRoute("/review-runs"), async ({ request }) => {
        precheckBodies.push((await request.json()) as { sql?: string; draft_id?: string });
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { run_id: crypto.randomUUID(), result_ref: "/review-runs/x" },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard(`/changes/drafts/${draft.id}`);
    const editor = await screen.findByTestId("sql-editor");
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 2;" },
    });
    fireEvent.click(screen.getByTestId("run-review"));

    await waitFor(() => {
      expect(precheckBodies).toHaveLength(1);
    });
    // Sent verbatim for review: request body SQL = current editor text, carrying the draft anchor id.
    expect(precheckBodies[0]?.sql).toBe("UPDATE orders SET status = 2;");
    expect(precheckBodies[0]?.draft_id).toBe(draft.id);
    expect(sqlPutCalls).toBe(0);
    expect(editor).toHaveValue("UPDATE orders SET status = 2;");
  });

  it("disables the precheck button immediately while the start is in flight (owner priority)", async () => {
    const created = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u9884\u5ba1\u4e0d\u634e\u5e26\u4fdd\u5b58" }),
      })
    ).json()) as {
      data: {
        id: string;
        version: number;
        revision: number;
        has_sql: boolean;
        [key: string]: unknown;
      };
    };
    const draft = created.data;
    const runStarted = gate();
    const releaseRun = gate();
    let precheckCalls = 0;
    server.use(
      http.post(apiRoute("/review-runs"), async () => {
        precheckCalls += 1;
        runStarted.open();
        await releaseRun.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { run_id: crypto.randomUUID(), result_ref: "/review-runs/x" },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard(`/changes/drafts/${draft.id}`);
    const editor = await screen.findByTestId("sql-editor");
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    const runReview = screen.getByTestId("run-review");
    fireEvent.click(runReview);

    // While a run is in flight (gate not yet open), the button must immediately enter pending state: spinner + disabled,
    // instead of appearing frozen for several seconds and causing duplicate clicks.
    await runStarted.promise;
    expect(runReview).toBeDisabled();
    expect(runReview).toHaveAttribute("aria-busy", "true");
    // Duplicate clicks within the window must be swallowed: precheck may only be initiated once.
    fireEvent.click(runReview);

    releaseRun.open();
    await waitFor(() => {
      expect(precheckCalls).toBe(1);
    });
    await waitFor(() => {
      expect(screen.getByTestId("run-review")).toBeEnabled();
    });
  });

  it("sends the click-time SQL from create mode and keeps text typed during the flight", async () => {
    // Draftless create mode: precheck directly sends local SQL; text typed while the POST is in flight stays in
    // the editor (neither lost nor triggering any write), but is not part of the current review payload.
    const runStarted = gate();
    const releaseRun = gate();
    const precheckBodies: Array<{ sql?: string; draft_id?: string; flow_id?: string }> = [];
    server.use(
      http.post(apiRoute("/review-runs"), async ({ request }) => {
        precheckBodies.push((await request.json()) as { sql?: string; draft_id?: string; flow_id?: string });
        runStarted.open();
        await releaseRun.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { run_id: crypto.randomUUID(), result_ref: "/review-runs/x" },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    const editor = await screen.findByTestId("sql-editor");
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    fireEvent.click(screen.getByTestId("run-review"));

    await runStarted.promise;
    expect(screen.getByTestId("run-review")).toBeDisabled();
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 2;" },
    });
    releaseRun.open();

    await waitFor(() => {
      expect(precheckBodies).toHaveLength(1);
    });
    expect(precheckBodies[0]?.sql).toBe("UPDATE orders SET status = 1;");
    expect(precheckBodies[0]?.draft_id).toBeUndefined();
    expect(editor).toHaveValue("UPDATE orders SET status = 2;");
    const drafts = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: unknown[] };
    };
    expect(drafts.data.items).toHaveLength(0);
  });

  it("does not leave the editor when SQL changes while save-and-return is in flight", async () => {
    const created = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u4fdd\u5b58\u5e76\u8fd4\u56de\u7ade\u6001" }),
      })
    ).json()) as {
      data: {
        id: string;
        version: number;
        revision: number;
        has_sql: boolean;
        [key: string]: unknown;
      };
    };
    const draft = created.data;
    const saveStarted = gate();
    const releaseSave = gate();
    server.use(
      http.put(apiRoute("/change-drafts/:draftId/sql"), async ({ request }) => {
        await request.json();
        saveStarted.open();
        await releaseSave.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            ...draft,
            state: "draft",
            version: draft.version + 1,
            revision: draft.revision + 1,
            has_sql: true,
          },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard(`/changes/drafts/${draft.id}`);
    const editor = await screen.findByTestId("sql-editor");
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));

    await saveStarted.promise;
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 2;" },
    });
    releaseSave.open();

    await waitFor(() => {
      expect(editor).toHaveValue("UPDATE orders SET status = 2;");
    });
    expect(screen.queryByTestId("mine-page")).not.toBeInTheDocument();
  });

  it("does not leave while metadata is saving if the editor changes again", async () => {
    const created = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u5143\u6570\u636e\u4fdd\u5b58\u7ade\u6001" }),
      })
    ).json()) as {
      data: {
        id: string;
        version: number;
        revision: number;
        has_sql: boolean;
        [key: string]: unknown;
      };
    };
    const draft = created.data;
    const metadataStarted = gate();
    const releaseMetadata = gate();
    server.use(
      http.patch(apiRoute("/change-drafts/:draftId"), async ({ request }) => {
        await request.json();
        metadataStarted.open();
        await releaseMetadata.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            ...draft,
            state: "draft",
            version: draft.version + 2,
            revision: draft.revision + 1,
            has_sql: true,
          },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard(`/changes/drafts/${draft.id}`);
    const editor = await screen.findByTestId("sql-editor");
    await waitFor(() => {
      expect(screen.getByTestId("draft-title-input")).toHaveValue("\u5143\u6570\u636e\u4fdd\u5b58\u7ade\u6001");
    });
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));

    await metadataStarted.promise;
    fireEvent.change(editor, {
      target: { value: "UPDATE orders SET status = 2;" },
    });
    releaseMetadata.open();

    await waitFor(() => {
      expect(editor).toHaveValue("UPDATE orders SET status = 2;");
    });
    expect(screen.queryByTestId("mine-page")).not.toBeInTheDocument();
  });

  it("does not leave when metadata changes while its patch is in flight", async () => {
    const created = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u5143\u6570\u636e PATCH \u5feb\u7167" }),
      })
    ).json()) as {
      data: {
        id: string;
        version: number;
        revision: number;
        has_sql: boolean;
        [key: string]: unknown;
      };
    };
    const draft = created.data;
    const metadataStarted = gate();
    const releaseMetadata = gate();
    let patchedMetadata: { title?: string; description?: string | null } = {};
    server.use(
      http.patch(apiRoute("/change-drafts/:draftId"), async ({ request }) => {
        patchedMetadata = (await request.json()) as {
          title?: string;
          description?: string | null;
        };
        metadataStarted.open();
        await releaseMetadata.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            ...draft,
            state: "draft",
            version: draft.version + 1,
            revision: draft.revision + 1,
            has_sql: true,
          },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard(`/changes/drafts/${draft.id}`);
    const title = await screen.findByTestId("draft-title-input");
    await waitFor(() => {
      expect(title).toHaveValue("\u5143\u6570\u636e PATCH \u5feb\u7167");
    });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));

    await metadataStarted.promise;
    expect(patchedMetadata.title).toBe("\u5143\u6570\u636e PATCH \u5feb\u7167");
    fireEvent.change(title, {
      target: { value: "PATCH \u671f\u95f4\u7684\u65b0\u6807\u9898" },
    });
    releaseMetadata.open();

    await waitFor(() => {
      expect(title).toHaveValue("PATCH \u671f\u95f4\u7684\u65b0\u6807\u9898");
    });
    await waitFor(() => {
      expect(screen.getByTestId("wizard-save-draft")).toBeEnabled();
    });
    expect(screen.queryByTestId("mine-page")).not.toBeInTheDocument();
  });

  it("keeps metadata edited during implicit adoption before the first draft query", async () => {
    const created = await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          flow_id: FIXTURE_FLOW_ID,
          title: "adoption metadata \u5feb\u7167",
          description: "POST \u65f6\u7684\u8bf4\u660e",
        }),
      })
    ).json() as {
      data: {
        id: string;
        version: number;
        revision: number;
        title: string;
        description: string | null;
        requested_scheduled_for: string | null;
        [key: string]: unknown;
      };
    };
    const createStarted = gate();
    const releaseCreate = gate();
    let postedMetadata: {
      title?: string;
      description?: string | null;
      requested_scheduled_for?: string | null;
    } = {};
    server.use(
      http.post(apiRoute("/change-drafts"), async ({ request }) => {
        postedMetadata = (await request.json()) as typeof postedMetadata;
        createStarted.open();
        await releaseCreate.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: created.data,
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "adoption metadata \u5feb\u7167" },
    });
    fireEvent.change(screen.getByTestId("draft-description-input"), {
      target: { value: "POST \u65f6\u7684\u8bf4\u660e" },
    });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));

    await createStarted.promise;
    expect(postedMetadata.title).toBe("adoption metadata \u5feb\u7167");
    expect(postedMetadata.description).toBe("POST \u65f6\u7684\u8bf4\u660e");
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "POST \u671f\u95f4\u4fee\u6539\u540e\u7684\u6807\u9898" },
    });
    fireEvent.change(screen.getByTestId("draft-description-input"), {
      target: { value: "POST \u671f\u95f4\u4fee\u6539\u540e\u7684\u8bf4\u660e" },
    });
    fireEvent.click(screen.getByTestId("draft-requested-for"));
    const tomorrow = new Date(Date.now() + 24 * 3600 * 1000);
    const iso = `${String(tomorrow.getFullYear())}-${String(tomorrow.getMonth() + 1).padStart(2, "0")}-${String(tomorrow.getDate()).padStart(2, "0")}`;
    fireEvent.click(await screen.findByRole("button", { name: iso }));
    fireEvent.click(screen.getByTestId("draft-requested-for-time-toggle"));
    fireEvent.click(within(screen.getByTestId("dtp-hours")).getByRole("button", { name: "09" }));
    fireEvent.click(within(screen.getByTestId("dtp-minutes")).getByRole("button", { name: "30" }));
    fireEvent.click(screen.getByTestId("draft-requested-for-confirm"));
    const requestedFor = `${iso} 09:30`;
    releaseCreate.open();

    await waitFor(() => {
      expect(screen.getByTestId("draft-title-input")).toHaveValue("POST \u671f\u95f4\u4fee\u6539\u540e\u7684\u6807\u9898");
    });
    expect(screen.getByTestId("draft-description-input")).toHaveValue("POST \u671f\u95f4\u4fee\u6539\u540e\u7684\u8bf4\u660e");
    expect(screen.getByTestId("draft-requested-for")).toHaveValue(requestedFor);
    expect(screen.getByTestId("wizard-save-draft")).toBeEnabled();
    expect(screen.queryByTestId("mine-page")).not.toBeInTheDocument();
  });

  it("blocks acting actions while the adoption save is in flight (Save and Return is the sole adopter)", async () => {
    const saveStarted = gate();
    const releaseSave = gate();
    const savedBodies: string[] = [];
    server.use(
      http.put(apiRoute("/change-drafts/:draftId/sql"), async ({ request, params }) => {
        const body = (await request.json()) as { sql: string };
        savedBodies.push(body.sql);
        saveStarted.open();
        await releaseSave.promise;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: String(params.draftId),
            state: "draft",
            version: 2,
            revision: 2,
            has_sql: true,
          },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "\u963b\u65ad adoption \u5e76\u53d1\u52a8\u4f5c" },
    });
    fireEvent.change(await screen.findByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));

    await saveStarted.promise;
    expect(screen.getByTestId("run-review")).toBeDisabled();
    expect(screen.getByTestId("wizard-save-draft")).toBeDisabled();
    expect(screen.getByTestId("wizard-save-draft")).toHaveAttribute("aria-busy", "true");
    expect(screen.getByTestId("open-bulk-import")).toBeDisabled();
    expect(screen.queryByTestId("submit-draft")).not.toBeInTheDocument();

    releaseSave.open();
    await waitFor(() => {
      expect(screen.getByTestId("mine-page")).toBeVisible();
    });
    expect(savedBodies).toEqual(["UPDATE orders SET status = 1;"]);
  });

  it("retries a failed adoption PUT against the adopted draft instead of creating another", async () => {
    let putCalls = 0;
    server.use(
      http.put(apiRoute("/change-drafts/:draftId/sql"), async ({ request, params }) => {
        putCalls += 1;
        if (putCalls === 1) {
          return HttpResponse.json(
            {
              err_code: 5030,
              message: "temporary save failure",
              data: null,
              request_id: FIXTURE_OWNER_ID,
            },
            { status: 500 },
          );
        }
        const body = (await request.json()) as { sql: string };
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: String(params.draftId),
            owner_user_id: FIXTURE_OWNER_ID,
            flow_id: FIXTURE_FLOW_ID,
            title: "adoption PUT \u91cd\u8bd5",
            description: null,
            requested_scheduled_for: null,
            revision: 2,
            state: "draft",
            has_sql: true,
            sql_size_bytes: body.sql.length,
            statement_count: 1,
            review_run_id: null,
            version: 2,
            created_at: "2026-08-28T08:00:00Z",
            updated_at: "2026-08-28T08:00:00Z",
          },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "adoption PUT \u91cd\u8bd5" },
    });
    fireEvent.change(await screen.findByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1;" },
    });
    // Save and Return is the sole adopter (RCP-20261101): stays on the page when the initial PUT fails.
    fireEvent.click(screen.getByTestId("wizard-save-draft"));
    expect(await screen.findByTestId("wizard-action-error")).toBeVisible();

    const page = screen.getByTestId("changes-new-page");
    const adoptedId = page.getAttribute("data-draft-id");
    expect(adoptedId).not.toBeNull();
    const drafts = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: Array<{ id: string; title: string }> };
    };
    expect(drafts.data.items).toHaveLength(1);
    expect(drafts.data.items[0]).toMatchObject({
      id: adoptedId,
      title: "adoption PUT \u91cd\u8bd5",
    });

    await waitFor(() => {
      expect(screen.getByTestId("wizard-save-draft")).toBeEnabled();
    });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));
    await waitFor(() => {
      expect(putCalls).toBe(2);
    });
    await waitFor(() => {
      expect(screen.getByTestId("mine-page")).toBeVisible();
    });
    const afterRetry = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: Array<{ id: string }> };
    };
    expect(afterRetry.data.items).toHaveLength(1);
  });
});

describe("SubmissionWizard metadata completions (§18.42)", () => {
  it("serves the stage datasource metadata endpoints for the granted flow", async () => {
    // Wizard consumption needs the editor mounted with real suggestions —
    // an e2e concern; here we pin the mock contract the wizard consumes:
    // the three metadata reads answer 200 with items for the granted pair
    // and 2014 for a non-granted datasource.
    const grantedDatasource = "4f6f1a2b-0000-4000-8000-000000000002";
    const base = `/api/v4/users/me/flows/${FIXTURE_FLOW_ID}/datasources/${grantedDatasource}/metadata`;
    interface MetadataPage {
      data: { items: Array<Record<string, string>> };
      err_code: number;
    }
    const schemas = (await (await fetch(`${base}/schemas`)).json()) as MetadataPage;
    expect(schemas.data.items.map((entry) => entry.name)).toContain("app");
    const tables = (await (
      await fetch(`${base}/tables?schema_name=app`)
    ).json()) as MetadataPage;
    expect(tables.data.items.map((entry) => entry.table_name)).toContain("orders");
    const columns = (await (
      await fetch(`${base}/columns?schema_name=app&table_name=orders`)
    ).json()) as MetadataPage;
    expect(columns.data.items.map((entry) => entry.column_name)).toContain("user_id");
    // A foreign datasource never becomes enumerable, even for a granted flow.
    const denied = (await (
      await fetch(
        `/api/v4/users/me/flows/${FIXTURE_FLOW_ID}/datasources/99999999-0000-4000-8000-000000000009/metadata/schemas`,
      )
    ).json()) as MetadataPage;
    expect(denied.err_code).toBe(2014);
  });
});

describe("SubmissionWizard remote metadata prefetch removal", () => {
  it("never prefetches stage datasource schemas, tables, or columns in create or draft mode", async () => {
    const metadataRequests: string[] = [];
    const recordMetadataHit = ({ request }: { request: Request }) => {
      metadataRequests.push(request.url);
      return HttpResponse.json({ err_code: 0, message: "ok", data: { items: [] } });
    };
    server.use(
      http.get(apiRoute("/users/me/flows/:flowId/datasources/:datasourceId/metadata/schemas"), recordMetadataHit),
      http.get(apiRoute("/users/me/flows/:flowId/datasources/:datasourceId/metadata/tables"), recordMetadataHit),
      http.get(apiRoute("/users/me/flows/:flowId/datasources/:datasourceId/metadata/columns"), recordMetadataHit),
    );

    // 1. Create mode: selecting a flow and typing SQL never triggers metadata prefetch.
    const createView = renderWizard("/changes/new");
    fireEvent.click(await screen.findByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    const createEditor = await screen.findByTestId("sql-editor");
    expect(createEditor).toBeVisible();
    fireEvent.change(createEditor, {
      target: { value: "UPDATE orders SET status = 1 WHERE id = 1;" },
    });
    await new Promise((resolve) => { setTimeout(resolve, 250); });
    expect(metadataRequests).toEqual([]);
    createView.unmount();

    // 2. Draft mode: opening an existing draft never triggers metadata prefetch either.
    const created = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u65e0\u8fdc\u7a0b\u5143\u6570\u636e\u9884\u62c9\u53d6" }),
      })
    ).json()) as { data: { id: string } };

    renderWizard(`/changes/drafts/${created.data.id}`);
    const draftEditor = await screen.findByTestId("sql-editor");
    expect(draftEditor).toBeVisible();
    await new Promise((resolve) => { setTimeout(resolve, 250); });
    expect(metadataRequests).toEqual([]);
  });
});

describe("SubmissionWizard flow picker search", () => {
  it("filters flow cards by title, shows a search empty state and keeps the selection", async () => {
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();

    // Substring filter: the order keyword matches only the catalog order flows.
    fireEvent.change(screen.getByTestId("wizard-flow-search"), {
      target: { value: "\u8ba2\u5355" },
    });
    const cards = screen.getAllByTestId(/^use-flow-/);
    expect(cards.length).toBeGreaterThan(0);
    expect(cards.every((card) => card.textContent.includes("\u8ba2\u5355"))).toBe(true);

    // No match: the dedicated empty state replaces the grid.
    fireEvent.change(screen.getByTestId("wizard-flow-search"), {
      target: { value: "\u4e0d\u5b58\u5728\u7684\u6d41\u7a0b" },
    });
    expect(screen.getByTestId("wizard-flow-search-empty")).toBeVisible();

    // Clearing restores the full catalog with the selection preserved.
    fireEvent.change(screen.getByTestId("wizard-flow-search"), {
      target: { value: "" },
    });
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("wizard-flow-search"), {
      target: { value: "\u6307\u6807" },
    });
    fireEvent.change(screen.getByTestId("wizard-flow-search"), { target: { value: "" } });
    expect(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`).getAttribute("aria-pressed")).toBe("true");
  });
});

describe("SubmissionWizard draft mode metadata", () => {
  it("shows the frozen flow inline and saves metadata edits in place", async () => {
    const create = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u521d\u59cb\u6807\u9898" }),
      })
    ).json()) as { data: { id: string } };

    renderWizard(`/changes/drafts/${create.data.id}`);
    // One-page layout: the frozen flow and the metadata fields share the
    // page with the editor — no step navigation to reach them.
    expect(await screen.findByTestId("sql-editor")).toBeVisible();
    expect(await screen.findByTestId("wizard-flow-frozen")).toBeVisible();
    const title = screen.getByTestId("draft-title-input");
    expect(title).toHaveValue("\u521d\u59cb\u6807\u9898");

    // An empty title is rejected near-field; a real edit is persisted by
    // Save and Return (owner 2026-10-24: Save Info folded away), which then leaves
    // for the drafts list.
    fireEvent.change(title, { target: { value: "" } });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));
    expect(screen.getByTestId("wizard-title-error")).toBeVisible();
    fireEvent.change(title, { target: { value: "\u6539\u540e\u7684\u6807\u9898" } });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));
    await waitFor(() => {
      expect(screen.getByTestId("mine-page")).toBeVisible();
    });
    const drafts = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: Array<{ title: string }> };
    };
    expect(drafts.data.items[0]?.title).toBe("\u6539\u540e\u7684\u6807\u9898");
  });

  it("shows the saved draft title in the summary card on reopen (F-UX-001)", async () => {
    const create = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u5df2\u4fdd\u5b58\u6807\u9898\u8349\u7a3f" }),
      })
    ).json()) as { data: { id: string } };

    renderWizard(`/changes/drafts/${create.data.id}`);
    expect(await screen.findByTestId("wizard-summary")).toBeVisible();
    // Reopen draft: summary "Title" shows the saved title, not "Unfilled".
    const summary = screen.getByTestId("wizard-summary");
    await waitFor(() => {
      expect(summary.textContent).toContain("\u5df2\u4fdd\u5b58\u6807\u9898\u8349\u7a3f");
    });
    // Assert only the title row itself — "SQL Scale" legitimately shows "Unfilled" when there is no SQL.
    expect(summary.textContent).not.toContain("\u6807\u9898\u672a\u586b\u5199");
  });

  it("keeps the summary dock blocked while no precheck exists, then leaves via Save Draft", async () => {
    const create = (await (
      await fetch("/api/v4/change-drafts", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ flow_id: FIXTURE_FLOW_ID, title: "\u65e0SQL\u8349\u7a3f" }),
      })
    ).json()) as { data: { id: string } };

    renderWizard(`/changes/drafts/${create.data.id}`);
    expect(await screen.findByTestId("sql-editor")).toBeVisible();

    // The summary card stays mounted without legacy submission hints/blockers while submit-draft lives inside the precheck modal.
    const summary = await screen.findByTestId("wizard-summary");
    expect(summary).toBeVisible();
    expect(summary.textContent).not.toContain("\u63d0\u4ea4\u524d\u8bf7\u6838\u5bf9\u4ee5\u4e0b\u4fe1\u606f");
    expect(screen.queryByTestId("submit-draft")).not.toBeInTheDocument();
    expect(screen.queryByTestId("submission-readiness")).not.toBeInTheDocument();
    expect(screen.queryByTestId("gate-blockers")).not.toBeInTheDocument();
    expect(screen.getByTestId("wizard-summary-precheck")).toBeVisible();

    // Editing SQL then Save and Return: the SQL persists implicitly on the way
    // out (owner 2026-10-24: the explicit save button is gone).
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1 WHERE user_id = 42;" },
    });
    fireEvent.click(screen.getByTestId("wizard-save-draft"));
    await waitFor(() => {
      expect(screen.getByTestId("mine-page")).toBeVisible();
    });
    const drafts = (await (await fetch("/api/v4/change-drafts")).json()) as {
      data: { items: Array<{ has_sql: boolean }> };
    };
    expect(drafts.data.items[0]?.has_sql).toBe(true);
  });

  it("shows runDraftReview SQL_PARSE_FAILED location detail only inside precheck-dialog and keeps modal switching mutually exclusive", async () => {
    server.use(
      http.post(apiRoute("/review-runs"), () =>
        HttpResponse.json({
          err_code: 2003,
          message: "SQL_PARSE_FAILED",
          data: null,
          request_id: FIXTURE_OWNER_ID,
          retryable: false,
          meta: {
            detail: "line 2, col 8 (offset 17, statement #2, dialect postgresql/postgresql): unclassifiable byte 0x60",
          },
        }),
      ),
    );
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "SELECT 1;\nSELECT `x`;" },
    });
    fireEvent.click(screen.getByTestId("run-review"));

    const startErr = await screen.findByTestId("precheck-start-error");
    expect(startErr).toHaveTextContent("line 2, col 8");
    expect(startErr).toHaveTextContent("unclassifiable byte 0x60");
    expect(screen.queryByTestId("wizard-action-error")).not.toBeInTheDocument();
  });

  it("closes precheck-dialog via close button, reopens via view-precheck-results, and never stacks sibling dialogs", async () => {
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "modal transition" },
    });
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1 WHERE user_id = 42;" },
    });

    fireEvent.click(screen.getByTestId("run-review"));
    expect(await screen.findByTestId("precheck-dialog")).toBeVisible();
    await waitFor(() => {
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });

    fireEvent.click(screen.getByTestId("precheck-dialog-close"));
    await waitFor(() => {
      expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();
    });
    fireEvent.click(screen.getByTestId("view-precheck-results"));
    expect(await screen.findByTestId("precheck-dialog")).toBeVisible();

    fireEvent.click(screen.getByTestId("submit-draft"));
    expect(await screen.findByTestId("submit-confirm-dialog")).toBeVisible();
    expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId("submit-confirm-cancel"));
    expect(await screen.findByTestId("precheck-dialog")).toBeVisible();
    expect(screen.queryByTestId("submit-confirm-dialog")).not.toBeInTheDocument();
  });

  it("pins precheck-dialog footer outside scroll-area and transitions gradient progress tones", async () => {
    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    fireEvent.click(screen.getByTestId(`use-flow-${FIXTURE_FLOW_ID}`));
    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "pinned footer and gradient progress" },
    });
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "UPDATE orders SET status = 1 WHERE user_id = 42;" },
    });

    fireEvent.click(screen.getByTestId("run-review"));
    const dialog = await screen.findByTestId("precheck-dialog");
    expect(dialog).toHaveClass("max-h-[min(680px,78vh)]", "sm:max-w-[56rem]");
    expect(screen.queryByTestId("review-in-progress")).not.toBeInTheDocument();
    expect(screen.queryByTestId("submission-readiness")).not.toBeInTheDocument();
    const closeBtn = screen.getByTestId("precheck-dialog-close");
    const submitBtn = screen.getByTestId("submit-draft");

    // Footer actions must sit in DialogFooter outside the ScrollArea viewport so they remain fixed while scrolling.
    const footer = closeBtn.closest('[data-slot="dialog-footer"]');
    expect(footer).not.toBeNull();
    expect(submitBtn.closest('[data-slot="dialog-footer"]')).toBe(footer);
    expect(footer?.closest('[data-slot="scroll-area"]')).toBeNull();
    expect(dialog.querySelector('[data-slot="scroll-area"]')).not.toBeNull();

    // Progress bar carries animated in-flight attributes and settles to a terminal gradient tone upon completion.
    const progress = screen.getByTestId("precheck-progress");
    expect(progress).toHaveClass("precheck-progress");
    expect(progress).toHaveAttribute("data-progress-active", "true");

    await waitFor(() => {
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });

    const completedProgress = screen.getByTestId("precheck-progress");
    expect(completedProgress).toHaveAttribute("data-progress-active", "false");
    expect(completedProgress).toHaveAttribute("data-progress-tone", "passed");

    // Clicking the backdrop or pressing Escape must not close precheck-dialog; only explicit close controls may dismiss it.
    const overlay = document.querySelector('[data-slot="dialog-overlay"]');
    expect(overlay).not.toBeNull();
    if (overlay !== null) {
      fireEvent.pointerDown(overlay);
      fireEvent.mouseDown(overlay);
      fireEvent.click(overlay);
    }
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.getByTestId("precheck-dialog")).toBeVisible();

    const headerClose = dialog.querySelector<HTMLButtonElement>('[data-slot="dialog-close"]');
    expect(headerClose).not.toBeNull();
    if (headerClose !== null) {
      fireEvent.click(headerClose);
    }
    await waitFor(() => {
      expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();
    });
  });

  it("resets stale submitted state so revisiting /changes/new never mounts a read-only editor", async () => {
    useDraftEditorStore.getState().markServerState("submitted");

    renderWizard("/changes/new");
    expect(await screen.findByTestId("wizard-flow-picker")).toBeVisible();
    const editor = screen.getByTestId<HTMLTextAreaElement>("sql-editor");
    expect(editor.readOnly).toBe(false);
    expect(screen.getByTestId("run-review")).toBeEnabled();
    expect(useDraftEditorStore.getState().serverState).toBeNull();
  });
});

describe("SubmissionWizard lazy target database selector", () => {
  interface TargetSelectorMockOptions {
    flowId: string;
    flowName: string;
    datasourceId: string;
    datasourceName: string;
    engine: "mysql" | "postgresql";
    databaseName: string | null;
    schemas: string[];
    displayNumber: string;
    schemaGate?: { resolve: () => void };
  }

  function setupTargetSelectorMocks(options: TargetSelectorMockOptions) {
    Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
    Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
    Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });

    let schemaFetches = 0;
    const reviewPayloads: Array<Record<string, unknown>> = [];
    const orderPayloads: Array<Record<string, unknown>> = [];
    const runId = crypto.randomUUID();

    server.use(
      http.get(apiRoute("/users/me/flows"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [
              {
                id: options.flowId,
                name: options.flowName,
                flow_type: "change_review",
                enabled: true,
                status: "enabled",
                rule_set_id: null,
                stages: [
                  {
                    position: 1,
                    datasource_id: options.datasourceId,
                    datasource_name: options.datasourceName,
                    datasource_engine: options.engine,
                    datasource_compatibility_mode: options.engine,
                    datasource_database_name: options.databaseName,
                    approval_steps: [{ position: 1, actors: [{ user_id: FIXTURE_OWNER_ID }] }],
                    execution_actors: [{ user_id: FIXTURE_OWNER_ID }],
                  },
                ],
                version: 1,
                created_at: "2026-08-01T00:00:00Z",
                updated_at: "2026-08-01T00:00:00Z",
              },
            ],
            next_cursor: null,
          },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
      http.get(apiRoute("/users/me/flows/:flowId/datasources/:datasourceId/metadata/schemas"), async () => {
        schemaFetches += 1;
        if (options.schemaGate !== undefined) {
          const gateRef = options.schemaGate;
          await new Promise<void>((resolve) => {
            gateRef.resolve = resolve;
          });
        }
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: options.schemas.map((name) => ({ name })) },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
      http.post(apiRoute("/review-runs"), async ({ request }) => {
        reviewPayloads.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { run_id: runId, result_ref: `/review-runs/${runId}` },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
      http.get(apiRoute("/review-runs/:runId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: runId,
            draft_id: "",
            draft_revision: 0,
            state: "ready",
            statement_count: 1,
            fingerprint_group_count: 1,
            stage_results: [],
            gate: { passed: true, reason_codes: [] },
            failure_code: null,
            version: 1,
            created_at: "2026-08-01T00:00:00Z",
            started_at: "2026-08-01T00:00:00Z",
            finished_at: "2026-08-01T00:00:01Z",
          },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
      http.get(apiRoute("/review-runs/:runId/findings"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: [], next_cursor: null },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
      http.post(apiRoute("/change-orders"), async ({ request }) => {
        orderPayloads.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: crypto.randomUUID(),
            display_number: options.displayNumber,
            state: "stage_approval_active",
          },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    return {
      reviewPayloads,
      orderPayloads,
      getSchemaFetches: () => schemaFetches,
    };
  }

  const noDefaultFlowId = "4f6f1a2b-0000-4000-8000-00000000f901";
  const noDefaultDsId = "4f6f1a2b-0000-4000-8000-00000000d901";

  it("fetches schemas only on dropdown open, requires a MySQL target DB, carries target_database to review & submit, and invalidates on change", async () => {
    const schemaGate: { resolve: () => void } = { resolve: () => undefined };
    const { reviewPayloads, orderPayloads, getSchemaFetches } = setupTargetSelectorMocks({
      flowId: noDefaultFlowId,
      flowName: "MySQL Without Default DB",
      datasourceId: noDefaultDsId,
      datasourceName: "mysql-no-default",
      engine: "mysql",
      databaseName: null,
      schemas: ["app", "analytics"],
      displayNumber: "YR-20261124-000001",
      schemaGate,
    });

    renderWizard("/changes/new");
    fireEvent.click(await screen.findByTestId(`use-flow-${noDefaultFlowId}`));
    expect(await screen.findByTestId("wizard-target-database")).toBeVisible();
    expect(getSchemaFetches()).toBe(0);

    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "lazy target db submission" },
    });
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "UPDATE app.orders SET status = 1 WHERE id = 1;" },
    });

    // Without selecting a target DB, AI precheck runs directly without target_database.
    await userEvent.click(screen.getByTestId("run-review"));
    await screen.findByTestId("precheck-dialog");
    await waitFor(() => {
      expect(reviewPayloads).toHaveLength(1);
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });
    expect(screen.queryByTestId("wizard-step-error")).not.toBeInTheDocument();
    expect(reviewPayloads[0]?.target_database).toBeUndefined();
    expect(getSchemaFetches()).toBe(0);

    await userEvent.click(screen.getByTestId("precheck-dialog-close"));
    await waitFor(() => {
      expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();
    });

    // Opening the dropdown triggers lazy metadata/schemas fetch and renders loading state in the popup portal (never shifting page flow).
    await userEvent.click(screen.getByTestId("wizard-target-database"));
    await waitFor(() => {
      expect(getSchemaFetches()).toBe(1);
      expect(screen.getByTestId("wizard-target-database")).toHaveAttribute("aria-expanded", "true");
      expect(screen.getByTestId("wizard-target-database-loading")).toBeInTheDocument();
    });
    expect(
      screen.getByTestId("wizard-target-database-section").contains(
        screen.getByTestId("wizard-target-database-loading"),
      ),
    ).toBe(false);
    schemaGate.resolve();
    await userEvent.click(await screen.findByRole("option", { name: "app" }));
    await waitFor(() => {
      expect(screen.getByTestId("wizard-summary-target-database")).toHaveTextContent("app");
      expect(screen.getByTestId("wizard-summary-precheck")).toHaveTextContent("\u5df2\u4fee\u6539");
    });

    // Precheck now sends target_database: "app".
    await userEvent.click(screen.getByTestId("run-review"));
    await screen.findByTestId("precheck-dialog");
    await waitFor(() => {
      expect(reviewPayloads).toHaveLength(2);
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });
    expect(reviewPayloads[1]?.target_database).toBe("app");

    // Switching target database after precheck marks precheck stale/outdated.
    await userEvent.click(screen.getByTestId("precheck-dialog-close"));
    await waitFor(() => {
      expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();
    });
    await userEvent.click(screen.getByTestId("wizard-target-database"));
    await waitFor(() => {
      expect(screen.getByTestId("wizard-target-database")).toHaveAttribute("aria-expanded", "true");
    });
    await userEvent.click(await screen.findByRole("option", { name: "analytics" }));
    await waitFor(() => {
      expect(screen.getByTestId("wizard-summary-target-database")).toHaveTextContent("analytics");
      expect(screen.getByTestId("wizard-summary-precheck")).toHaveTextContent("\u5df2\u4fee\u6539");
    });

    // Switch back to the reviewed DB so submission carries target_database: "app".
    await userEvent.click(screen.getByTestId("wizard-target-database"));
    await waitFor(() => {
      expect(screen.getByTestId("wizard-target-database")).toHaveAttribute("aria-expanded", "true");
    });
    await userEvent.click(await screen.findByRole("option", { name: "app" }));
    await userEvent.click(screen.getByTestId("view-precheck-results"));
    await waitFor(() => {
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    });
    await userEvent.click(screen.getByTestId("submit-draft"));
    await userEvent.click(await screen.findByTestId("submit-confirm-accept"));

    await waitFor(() => {
      expect(orderPayloads).toHaveLength(1);
      expect(orderPayloads[0]?.target_database).toBe("app");
    });
  });

  it("shows PG schema notice (not Database), never prefills datasource_database_name, and clears/submits optional target_database", async () => {
    const pgFlowId = "4f6f1a2b-0000-4000-8000-00000000f902";
    const pgDsId = "4f6f1a2b-0000-4000-8000-00000000d902";
    const { reviewPayloads, orderPayloads, getSchemaFetches } = setupTargetSelectorMocks({
      flowId: pgFlowId,
      flowName: "PG Change Flow",
      datasourceId: pgDsId,
      datasourceName: "prod-pg",
      engine: "postgresql",
      databaseName: "target_db",
      schemas: ["public", "analytics"],
      displayNumber: "YR-20261124-000002",
    });

    renderWizard("/changes/new");
    fireEvent.click(await screen.findByTestId(`use-flow-${pgFlowId}`));
    expect(await screen.findByTestId("wizard-target-database")).toBeVisible();
    expect(screen.getByTestId("wizard-pg-schema-notice")).toBeVisible();
    expect(screen.getByTestId("wizard-target-database-hint")).toHaveTextContent("target_db");
    expect(screen.getByTestId("wizard-target-database-hint")).toHaveTextContent("Schema");
    // datasource_database_name ("target_db") must never be prefilled as PG Target Schema.
    expect(screen.getByTestId("wizard-summary-target-database")).toHaveTextContent("search_path");
    expect(screen.queryByTestId("wizard-target-database-clear")).not.toBeInTheDocument();
    expect(getSchemaFetches()).toBe(0);

    fireEvent.change(screen.getByTestId("draft-title-input"), {
      target: { value: "pg target schema test" },
    });
    fireEvent.change(screen.getByTestId("sql-editor"), {
      target: { value: "UPDATE analytics.events SET note = 'ok' WHERE id = 1;" },
    });

    await userEvent.click(screen.getByTestId("wizard-target-database"));
    await waitFor(() => {
      expect(getSchemaFetches()).toBe(1);
    });
    await userEvent.click(await screen.findByRole("option", { name: "analytics" }));
    await waitFor(() => {
      expect(screen.getByTestId("wizard-summary-target-database")).toHaveTextContent("analytics");
      expect(screen.getByTestId("wizard-target-database-clear")).toBeVisible();
    });

    await userEvent.click(screen.getByTestId("run-review"));
    await screen.findByTestId("precheck-dialog");
    await waitFor(() => {
      expect(reviewPayloads).toHaveLength(1);
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });
    expect(reviewPayloads[0]?.target_database).toBe("analytics");

    await userEvent.click(screen.getByTestId("precheck-dialog-close"));
    await waitFor(() => {
      expect(screen.queryByTestId("precheck-dialog")).not.toBeInTheDocument();
    });

    // Clearing PG schema returns summary to default search_path and omits target_database on precheck + submit.
    await userEvent.click(screen.getByTestId("wizard-target-database-clear"));
    await waitFor(() => {
      expect(screen.getByTestId("wizard-summary-target-database")).toHaveTextContent("search_path");
      expect(screen.queryByTestId("wizard-target-database-clear")).not.toBeInTheDocument();
    });

    await userEvent.click(screen.getByTestId("run-review"));
    await screen.findByTestId("precheck-dialog");
    await waitFor(() => {
      expect(reviewPayloads).toHaveLength(2);
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });
    expect(reviewPayloads[1]?.target_database).toBeUndefined();

    await userEvent.click(screen.getByTestId("submit-draft"));
    await userEvent.click(await screen.findByTestId("submit-confirm-accept"));
    await waitFor(() => {
      expect(orderPayloads).toHaveLength(1);
      expect(orderPayloads[0]?.target_database).toBeUndefined();
    });
  });

  it("clears saved target_database on an existing draft across precheck (null), save (null), and submit (null)", async () => {
    const pgFlowId = "4f6f1a2b-0000-4000-8000-00000000f903";
    const pgDsId = "4f6f1a2b-0000-4000-8000-00000000d903";
    const savedDraftId = "4f6f1a2b-0000-4000-8000-00000000a903";
    const sqlText = "UPDATE analytics.events SET note = 'ok' WHERE id = 1;";
    const patchPayloads: Array<Record<string, unknown>> = [];
    let currentTargetDb: string | null = "analytics";
    let draftVersion = 2;

    const { reviewPayloads, orderPayloads } = setupTargetSelectorMocks({
      flowId: pgFlowId,
      flowName: "PG Draft Flow",
      datasourceId: pgDsId,
      datasourceName: "prod-pg",
      engine: "postgresql",
      databaseName: "target_db",
      schemas: ["public", "analytics"],
      displayNumber: "YR-20261124-000003",
    });

    server.use(
      http.get(apiRoute("/change-drafts/:draftId"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: savedDraftId,
            owner_user_id: FIXTURE_OWNER_ID,
            flow_id: pgFlowId,
            flow_version_seen: 1,
            title: "Saved PG draft",
            description: null,
            requested_scheduled_for: null,
            rollback_enabled: true,
            target_database: currentTargetDb,
            ticket_type: "normal",
            revision: 1,
            state: "draft",
            has_sql: true,
            sql_size_bytes: sqlText.length,
            statement_count: 1,
            review_run_id: null,
            version: draftVersion,
            created_at: "2026-08-01T00:00:00Z",
            updated_at: "2026-08-01T00:00:00Z",
          },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
      http.post(apiRoute("/change-drafts/:draftId/sql-reveals"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            reveal_id: crypto.randomUUID(),
            sql: sqlText,
            watermark: "henry@yearning.io",
            valid_until: "2026-12-01T00:00:00Z",
          },
          request_id: FIXTURE_OWNER_ID,
        }),
      ),
      http.patch(apiRoute("/change-drafts/:draftId"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        patchPayloads.push(body);
        if ("target_database" in body) {
          currentTargetDb = (body.target_database as string | null) ?? null;
        }
        draftVersion += 1;
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: savedDraftId,
            owner_user_id: FIXTURE_OWNER_ID,
            flow_id: pgFlowId,
            flow_version_seen: 1,
            title: typeof body.title === "string" ? body.title : "Saved PG draft",
            description: null,
            requested_scheduled_for: null,
            rollback_enabled: true,
            target_database: currentTargetDb,
            ticket_type: "normal",
            revision: 1,
            state: "draft",
            has_sql: true,
            sql_size_bytes: sqlText.length,
            statement_count: 1,
            review_run_id: null,
            version: draftVersion,
            created_at: "2026-08-01T00:00:00Z",
            updated_at: "2026-08-01T00:00:00Z",
          },
          request_id: FIXTURE_OWNER_ID,
        });
      }),
    );

    renderWizard(`/changes/drafts/${savedDraftId}`);
    expect(await screen.findByTestId("wizard-target-database-clear")).toBeVisible();
    expect(screen.getByTestId("wizard-summary-target-database")).toHaveTextContent("analytics");
    await waitFor(() => {
      expect(screen.getByTestId("sql-editor")).toHaveValue(sqlText);
    });

    await userEvent.click(screen.getByTestId("wizard-target-database-clear"));
    await waitFor(() => {
      expect(screen.getByTestId("wizard-summary-target-database")).toHaveTextContent("search_path");
      expect(screen.queryByTestId("wizard-target-database-clear")).not.toBeInTheDocument();
    });

    // AI precheck explicitly sends target_database: null (no DB mutation, never falling back to saved "analytics").
    await userEvent.click(screen.getByTestId("run-review"));
    await screen.findByTestId("precheck-dialog");
    await waitFor(() => {
      expect(reviewPayloads).toHaveLength(1);
      expect(screen.getByTestId("submit-draft")).toBeEnabled();
    }, { timeout: 4_000 });
    expect("target_database" in (reviewPayloads[0] ?? {})).toBe(true);
    expect(reviewPayloads[0]?.target_database).toBeNull();
    expect(patchPayloads).toHaveLength(0);

    // Submitting patches the draft with target_database: null and posts target_database: null on /change-orders.
    await userEvent.click(screen.getByTestId("submit-draft"));
    await userEvent.click(await screen.findByTestId("submit-confirm-accept"));
    await waitFor(() => {
      expect(orderPayloads).toHaveLength(1);
    });
    expect(patchPayloads).toHaveLength(1);
    expect("target_database" in (patchPayloads[0] ?? {})).toBe(true);
    expect(patchPayloads[0]?.target_database).toBeNull();
    expect("target_database" in (orderPayloads[0] ?? {})).toBe(true);
    expect(orderPayloads[0]?.target_database).toBeNull();
  });
});
