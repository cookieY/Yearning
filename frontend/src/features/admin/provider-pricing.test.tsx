import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { http, HttpResponse } from "msw";
import { beforeAll, afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { stubSession as stubSharedSession } from "@/test/session";
import {
  ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
  resetAdminFixture,
} from "@/shared/mock/admin-fixture";
import { SessionProvider } from "@/features/auth/session-provider";
import {
  ProviderPricingPanel,
  ProviderUsagePanel,
} from "@/features/admin/provider-pricing";

/**
 * S008 reference pricing panel (FE package 2026-10-31, branch coverage supplement 2026-11-02): open/closed
 * presentation of rate version table, empty-value stripping and guards on create form, reference template
 * "fill empty values only without overwriting", window add/remove and 24:00 boundary, plus every rate_source
 * and unreported shape in cost stats/usage ledger. Data surface runs over shared MSW fixture (append-only list +
 * create closes previous open version), session probe pins admin per admin-pages precedent.
 */

const SESSION_USER_ID = "e2e-admin";

function stubSession(admin: boolean): void {
  stubSharedSession({ admin, id: SESSION_USER_ID, requestId: SESSION_USER_ID });
}

function renderPanel(ui: React.ReactElement): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/"]}>
        <SessionProvider>{ui}</SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function unmountAll(): void {
  cleanup();
}

beforeAll(() => {
  // Base UI Select pointer-capture stubs (admin-pages precedent).
  Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
  Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
  Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });
});

beforeEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "admin");
  stubSession(true);
  resetAdminFixture();
});

afterEach(() => {
  server.resetHandlers();
  resetAdminFixture();
  window.localStorage.removeItem("yearning-mock-auth");
  vi.useRealTimers();
});

async function openRateForm(): Promise<void> {
  await userEvent.click(await screen.findByTestId("rate-new-version"));
  expect(screen.getByTestId("provider-rate-form")).toBeVisible();
}

describe("ProviderPricingPanel", () => {
  it("renders the empty state, guards an empty effective-from, then creates an open-ended version", async () => {
    const created: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), async ({ request }) => {
        created.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ err_code: 1002, message: "not used", data: null, request_id: "x" });
      }),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    // Fresh fixture world: no versions yet → empty copy, no table.
    expect(await screen.findByTestId("provider-pricing-panel")).toBeVisible();
    expect(screen.queryByTestId("provider-rate-table")).not.toBeInTheDocument();

    await openRateForm();
    // Guard: empty effective_from surfaces the near-field error, no request.
    await userEvent.click(screen.getByTestId("rate-submit"));
    expect(await screen.findByTestId("rate-form-error")).toBeVisible();
    expect(created).toHaveLength(0);

    // Fill the form; empty cached prices must be omitted from the body.
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-12-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "2.34" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "5.67" } });
    fireEvent.change(screen.getByTestId("rate-cached-read-0"), { target: { value: "0.10" } });
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        created.push(body);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { id: "rv-new", provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID, ...body, version: 1, created_at: "2026-12-01T00:00:00Z" },
          request_id: "x",
        });
      }),
    );
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => expect(screen.queryByTestId("provider-rate-form")).not.toBeInTheDocument());
    expect(created).toHaveLength(1);
    const body = created[0] as { currency: string; effective_until?: string; windows: Array<Record<string, unknown>> };
    expect(body.currency).toBe("CNY");
    expect(body.effective_until).toBeUndefined();
    expect(body.windows[0]).toMatchObject({ start_minute: 0, end_minute: 1440, input_price: "2.34", output_price: "5.67", cached_read_price: "0.10" });
    expect(body.windows[0]).not.toHaveProperty("cached_write_price");
  });

  it("closes the previous open version when a second one lands, and renders both window shapes", async () => {
    // Date-aware badge anchors "currently effective / next effective" to wall-clock time: pin now for
    // determinism. Pinned at 2026-11-02: v1 (from 2026-12-01)/v2/v3 are all not yet effective.
    vi.useFakeTimers({ now: new Date("2026-11-02T12:00:00Z"), shouldAdvanceTime: true });
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    // Contract-required prices on every create (window input/output are
    // required by the OpenAPI window schema; client validation mirrors it).
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-12-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "2.34" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "5.67" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => expect(screen.queryByTestId("provider-rate-form")).not.toBeInTheDocument());

    // Second version with an explicit until + a bounded window; the fixture
    // closes v1 at v2's effective_from (append-only closure).
    await openRateForm();
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByTestId("rate-until"), { target: { value: "2027-06-30" } });
    fireEvent.change(screen.getByTestId("rate-window-start-0"), { target: { value: "09:30" } });
    fireEvent.change(screen.getByTestId("rate-window-end-0"), { target: { value: "18:00" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "1.00" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "2.00" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => expect(screen.queryByTestId("provider-rate-form")).not.toBeInTheDocument());

    // Third version without an until stays open — at the pinned now it is
    // still future, so nothing is currently effective yet.
    await openRateForm();
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2027-07-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "1.00" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "2.00" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => expect(screen.queryByTestId("provider-rate-form")).not.toBeInTheDocument());

    const table = await screen.findByTestId("provider-rate-table");
    // v1: closed by v2's from-date and keeps the full-day window text;
    // v2: bounded window with its explicit until.
    await waitFor(() => {
      const rows = within(table).getAllByTestId(/provider-rate-row-/);
      expect(rows).toHaveLength(3);
      expect(rows.some((row) => within(row).queryAllByText("00:00–24:00").length === 1)).toBe(true);
      expect(rows.some((row) => within(row).queryAllByText("09:30–18:00").length === 1)).toBe(true);
    });
    // v1 was auto-closed at v2's from-date; v2 carries its explicit until.
    expect(table.textContent).toContain("2027-01-01 → 2027-06-30");
    expect(table.textContent).toContain("2026-12-01 → 2027-01-01");
    // The open version renders the open-ended marker.
    expect(table.textContent).toContain("2027-07-01 → \u5f00\u653e");
    // Pinned now: every version is future; the earliest from (v1) carries
    // next-effective and NO version may carry currently-effective (the old open-ended-only
    // badge labeled not-yet-effective versions as current).
    expect(within(table).queryAllByText("\u5f53\u524d\u751f\u6548")).toHaveLength(0);
    expect(within(table).getAllByText("\u4e0b\u4e00\u4e2a\u751f\u6548")).toHaveLength(1);
    const rows = within(table).getAllByTestId(/provider-rate-row-/);
    const nextRow = rows.find((row) => within(row).queryAllByText("\u4e0b\u4e00\u4e2a\u751f\u6548").length > 0);
    expect(nextRow?.textContent).toContain("2026-12-01");
  });

  it("marks the now-spanning version as currently effective and the earliest future version as next effective", async () => {
    vi.useFakeTimers({ now: new Date("2026-11-02T12:00:00Z"), shouldAdvanceTime: true });
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "1.00" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "2.00" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => expect(screen.queryByTestId("provider-rate-form")).not.toBeInTheDocument());

    // v2 future: fixture closes v1 at v2's from — v1 still spans the pinned
    // now (2026-01-01 → 2027-01-01), so both badges coexist exactly once.
    await openRateForm();
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "1.00" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "2.00" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => expect(screen.queryByTestId("provider-rate-form")).not.toBeInTheDocument());

    const table = await screen.findByTestId("provider-rate-table");
    await waitFor(() => {
      expect(within(table).getAllByTestId(/provider-rate-row-/)).toHaveLength(2);
    });
    expect(within(table).getAllByText("\u5f53\u524d\u751f\u6548")).toHaveLength(1);
    expect(within(table).getAllByText("\u4e0b\u4e00\u4e2a\u751f\u6548")).toHaveLength(1);
    const rows = within(table).getAllByTestId(/provider-rate-row-/);
    const currentRow = rows.find((row) => within(row).queryAllByText("\u5f53\u524d\u751f\u6548").length > 0);
    expect(currentRow?.textContent).toContain("2026-01-01 → 2027-01-01");
    const nextRow = rows.find((row) => within(row).queryAllByText("\u4e0b\u4e00\u4e2a\u751f\u6548").length > 0);
    expect(nextRow?.textContent).toContain("2027-01-01 → \u5f00\u653e");
  });

  it("applies the global reference template only to empty fields, and only when prices are positive", async () => {
    // Zero prices (the default world): applying must leave everything
    // untouched — the >0 guard refuses to fabricate a reference price.
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    await userEvent.click(screen.getByTestId("rate-apply-template"));
    expect(screen.getByTestId("rate-input-price-0")).toHaveValue("");
    expect(screen.getByTestId("rate-output-price-0")).toHaveValue("");

    // A fresh panel with real reference prices fills empty fields only — a
    // manual value wins (query must see the override from its first fetch).
    unmountAll();
    const overrideFetches: number[] = [];
    server.use(
      http.get(apiRoute("/admin/settings/:namespace"), () => {
        overrideFetches.push(1);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            namespace: "ai-budget",
            schema_version: 1,
            settings: {
              enforced: false,
              currency: "CNY",
              daily_budget_minor: 10000,
              alert_threshold_percent: 80,
              input_price_per_million_minor: 234,
              output_price_per_million_minor: 567,
            },
            version: 1,
            updated_by: SESSION_USER_ID,
            updated_at: "2026-08-28T08:00:00Z",
          },
          request_id: SESSION_USER_ID,
        });
      }),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    await waitFor(() => { expect(overrideFetches.length).toBeGreaterThan(0); });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "9.99" } });
    await userEvent.click(screen.getByTestId("rate-apply-template"));
    expect(screen.getByTestId("rate-input-price-0")).toHaveValue("9.99");
    expect(screen.getByTestId("rate-output-price-0")).toHaveValue("5.67");
    // Second application now fills the still-empty input too.
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "" } });
    await userEvent.click(screen.getByTestId("rate-apply-template"));
    expect(screen.getByTestId("rate-input-price-0")).toHaveValue("2.34");
  });

  it("ignores the template when the settings namespace carries no reference prices", async () => {
    const settingsFetches: number[] = [];
    server.use(
      http.get(apiRoute("/admin/settings/:namespace"), () => {
        settingsFetches.push(1);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            namespace: "ai-budget",
            schema_version: 1,
            // Wrong shape on purpose: no *_price_per_million_minor keys.
            settings: { enforced: true, currency: "USD" },
            version: 1,
            updated_by: SESSION_USER_ID,
            updated_at: "2026-08-28T08:00:00Z",
          },
          request_id: SESSION_USER_ID,
        });
      }),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    // The no-op verdict only counts once the query has actually resolved —
    // clicking before the fetch lands would pass vacuously.
    await waitFor(() => { expect(settingsFetches.length).toBeGreaterThan(0); });
    await userEvent.click(screen.getByTestId("rate-apply-template"));
    expect(screen.getByTestId("rate-input-price-0")).toHaveValue("");
    expect(screen.getByTestId("rate-output-price-0")).toHaveValue("");
  });

  it("adds and removes windows with the 24:00 boundary and clamps past-midnight ends", async () => {
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    // Second window starts where the previous one ended (1440 % 1440 = 0);
    // the full-day end stays representable as "24:00" (text control, the
    // time input could never hold it). Created-body 1440 is asserted in
    // the first test above and the full-day regression below.
    await userEvent.click(screen.getByTestId("rate-add-window"));
    expect(screen.getByTestId("rate-window-start-1")).toHaveValue("00:00");
    expect(screen.getByTestId("rate-window-end-1")).toHaveValue("24:00");
    fireEvent.change(screen.getByTestId("rate-window-end-1"), { target: { value: "23:59" } });
    expect(screen.getByTestId("rate-window-end-1")).toHaveValue("23:59");
    // Past-boundary input clamps to the day bound; blank input is ignored
    // (no NaN minute lands in the draft, the shown value never changes).
    fireEvent.change(screen.getByTestId("rate-window-end-1"), { target: { value: "25:99" } });
    expect(screen.getByTestId("rate-window-end-1")).toHaveValue("24:00");
    fireEvent.change(screen.getByTestId("rate-window-end-1"), { target: { value: "" } });
    expect(screen.getByTestId("rate-window-end-1")).toHaveValue("24:00");
    fireEvent.change(screen.getByTestId("rate-window-start-1"), { target: { value: "" } });
    expect(screen.getByTestId("rate-window-start-1")).toHaveValue("00:00");
    // Two windows: each row carries a remove affordance; dropping one
    // leaves a single window with no affordance left.
    await userEvent.click(screen.getAllByRole("button", { name: "\u5220\u9664" })[0] as HTMLElement);
    expect(screen.queryByTestId("rate-window-1")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "\u5220\u9664" })).not.toBeInTheDocument();
  });

  it("switches the currency to USD through the real Select", async () => {
    const created: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        created.push(body);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { id: "rv-usd", provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID, ...body, version: 1, created_at: "2026-12-01T00:00:00Z" },
          request_id: "x",
        });
      }),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    await userEvent.click(screen.getByTestId("rate-currency"));
    const listbox = await screen.findByRole("listbox");
    await userEvent.click(within(listbox).getByText("USD"));
    expect(screen.getByTestId("rate-currency")).toHaveTextContent("USD");
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-12-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "2.34" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "5.67" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => {
      expect(created).toHaveLength(1);
    });
    expect(created[0]).toMatchObject({ currency: "USD" });
  });

  it("surfaces the backend rejection inside the form without closing it", async () => {
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), () =>
        HttpResponse.json({ err_code: 1001, message: "RATE_CURRENCY_MISMATCH", data: null, request_id: "req-77" }),
      ),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-12-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "2.34" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "5.67" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    const alert = await screen.findByTestId("rate-form-error");
    expect(alert).toBeVisible();
    expect(alert.textContent).toContain("\u7a0d\u540e\u91cd\u8bd5");
    expect(screen.getByTestId("provider-rate-form")).toBeInTheDocument();
  });

  it("places server meta.fields rejections next to the offending fields (RCP-20261102)", async () => {
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), () =>
        HttpResponse.json({
          err_code: 1001,
          message: "VALIDATION_FAILED",
          data: null,
          request_id: "req-88",
          retryable: false,
          meta: {
            detail: "timezone must be a valid IANA name",
            fields: {
              timezone: ["must be a valid IANA name"],
              input_price: ["price must be a decimal with at most 6 fraction digits"],
            },
          },
        }),
      ),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    // Client-valid values (the server is the only one that rejects here).
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-12-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "2.34" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "5.67" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    // The form stays open and each named field shows its server message with
    // aria-invalid — not a form-level generic alone.
    expect(screen.getByTestId("provider-rate-form")).toBeInTheDocument();
    const formText = await waitFor(() => {
      const tz = screen.getByTestId("rate-timezone");
      expect(tz).toHaveAttribute("aria-invalid", "true");
      return screen.getByTestId("provider-rate-form").textContent;
    });
    expect(formText).toContain("must be a valid IANA name");
    const price = screen.getByTestId("rate-input-price-0");
    expect(price).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByTestId("provider-rate-form").textContent)
      .toContain("price must be a decimal with at most 6 fraction digits");
  });
});

describe("ProviderUsagePanel", () => {
  it("renders the fixture summary and every call shape the world serves", async () => {
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    // Summary: USD fixture amount is a currency-unit decimal string ("3.67100000").
    const summary = await screen.findByTestId("provider-usage-summary");
    expect(within(summary).getAllByText("USD").length).toBe(1);
    expect(summary.textContent).toContain("3.671000");
    const calls = await screen.findByTestId("provider-usage-calls");
    // global_settings row: tokens reported, reasoning suffix, CNY currency.
    const rowA = within(calls).getByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000d001");
    expect(rowA.textContent).toContain("1200 / 380");
    expect(rowA.textContent).toContain("(R 210)");
    // Amount strings render verbatim in currency units (never rescaled).
    expect(rowA.textContent).toContain("0.00632100");
    expect(rowA.textContent).not.toContain("0.00000000");
    // none row: everything unreported -> dashes and uncalculable amount.
    const rowB = within(calls).getByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000d002");
    // Null and absent are the same honest shape: every unset field renders
    // its not-reported mark — never a literal "null", never a zero cost.
    expect(rowB.textContent).toContain("— / — / —");
    expect(rowB.textContent).toContain("\u672a\u8fd4\u56de / \u672a\u8fd4\u56de");
    expect(rowB.textContent).toContain("\u672a\u8ba1\u7b97");
    expect(rowB.textContent).not.toContain("null");
    expect(rowB.textContent).not.toContain("0.00000000");
  });

  it("renders rate-version sourced rows and the not-reported token forms", async () => {
    const row = {
      id: "0b1e2f3a-0000-4000-8000-00000000d003",
      review_run_id: "0b1e2f3a-0000-4000-8000-000000000001",
      stage_position: 2, model_turn: 1, call_index: 2,
      provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
      model_name: "glm-4.6", sent_at: "2026-11-01T08:00:00Z",
      reasoning_effort: "high", max_tokens: 8192, temperature: "0",
      rate_version_id: "rv-1", rate_window_id: null, rate_source: "rate_version",
      currency: undefined, input_tokens: 10, output_tokens: undefined,
      cached_tokens: 4, cache_write_tokens: undefined, reasoning_tokens: undefined,
      usage_reported: true, reference_amount: undefined,
      calc_basis: "full_detail",
    };
    const rowBase = {
      id: "0b1e2f3a-0000-4000-8000-00000000d004",
      review_run_id: "0b1e2f3a-0000-4000-8000-000000000001",
      stage_position: 2, model_turn: 1, call_index: 3,
      provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
      model_name: "glm-4.6", sent_at: "2026-11-01T08:00:00Z",
      reasoning_effort: null, max_tokens: null, temperature: null,
      rate_version_id: null, rate_window_id: null, rate_source: "global_settings",
      currency: "CNY", input_tokens: 1, output_tokens: 1,
      cached_tokens: null, cache_write_tokens: null, reasoning_tokens: null,
      usage_reported: true, reference_amount: "100000000",
      calc_basis: "base_only",
    };
    server.use(
      http.get(apiRoute("/admin/ai-usage/calls"), () =>
        HttpResponse.json({
          err_code: 0, message: "ok", request_id: "x",
          data: { items: [row, rowBase], page: { next_cursor: null, has_more: false } },
        }),
      ),
    );
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const calls = await screen.findByTestId("provider-usage-calls");
    const rowV = within(calls).getByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000d003");
    // No currency suffix when the call carries none; output token unreported.
    expect(rowV.textContent).toContain("10 / \u672a\u8fd4\u56de");
    expect(rowV.textContent).toContain("4 / \u672a\u8fd4\u56de");
    expect(rowV.textContent).toContain("\u672a\u8ba1\u7b97");
    expect(rowV.textContent).not.toContain("(CNY)");
  });

  it("shows the empty forms when no usage exists for the provider", async () => {
    server.use(
      http.get(apiRoute("/admin/ai-usage/summary"), () =>
        HttpResponse.json({ err_code: 0, message: "ok", data: [], request_id: "x" }),
      ),
      http.get(apiRoute("/admin/ai-usage/calls"), () =>
        HttpResponse.json({
          err_code: 0, message: "ok", request_id: "x",
          data: { items: [], page: { next_cursor: null, has_more: false } },
        }),
      ),
    );
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await waitFor(() => {
      expect(screen.queryByTestId("provider-usage-summary")).not.toBeInTheDocument();
      expect(screen.queryByTestId("provider-usage-calls")).not.toBeInTheDocument();
    });
    expect(await screen.findByTestId("provider-usage-panel")).toBeVisible();
  });
});

/**
 * S008 regression (RED): PRD 3.3/3.x currency and amount semantics. API reference_amount is
 * a decimal string in major currency units, not nano units; template must not silently import across currencies;
 * full-day window end must be a representable accessible boundary.
 */
function usageCallRow(id: string, amount: string): Record<string, unknown> {
  return {
    id,
    review_run_id: "0b1e2f3a-0000-4000-8000-000000000001",
    stage_position: 1, model_turn: 1, call_index: 1,
    provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
    model_name: "glm-4.6", sent_at: "2026-11-01T08:00:00Z",
    reasoning_effort: null, max_tokens: null, temperature: null,
    rate_version_id: null, rate_window_id: null, rate_source: "global_settings",
    currency: "CNY", input_tokens: 1, output_tokens: 1,
    cached_tokens: null, cache_write_tokens: null, reasoning_tokens: null,
    usage_reported: true, reference_amount: amount, calc_basis: "base_only",
  };
}

function stubUsage(summaryAmount: string, callAmounts: ReadonlyArray<string>): void {
  server.use(
    http.get(apiRoute("/admin/ai-usage/summary"), () =>
      HttpResponse.json({
        err_code: 0, message: "ok", request_id: "x",
        data: [{ currency: "CNY", reference_amount: summaryAmount, computable_calls: 1, calls: 1 }],
      }),
    ),
    http.get(apiRoute("/admin/ai-usage/calls"), () =>
      HttpResponse.json({
        err_code: 0, message: "ok", request_id: "x",
        data: {
          items: callAmounts.map((amount, i) => usageCallRow(`0b1e2f3a-0000-4000-8000-00000000e00${String(i)}`, amount)),
          page: { next_cursor: null, has_more: false },
        },
      }),
    ),
  );
}

function stubBudgetTemplate(currency: "CNY" | "USD"): void {
  server.use(
    http.get(apiRoute("/admin/settings/:namespace"), () =>
      HttpResponse.json({
        err_code: 0, message: "ok", request_id: SESSION_USER_ID,
        data: {
          namespace: "ai-budget", schema_version: 1,
          settings: {
            enforced: false, currency, daily_budget_minor: 10000, alert_threshold_percent: 80,
            input_price_per_million_minor: 234, output_price_per_million_minor: 567,
          },
          version: 1, updated_by: SESSION_USER_ID, updated_at: "2026-08-28T08:00:00Z",
        },
      }),
    ),
  );
}

describe("S008 regression: reference_amount is already in currency units", () => {
  it("shows small call amounts directly, not divided by 1e9", async () => {
    stubUsage("0.00632100", ["0.00632100"]);
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const calls = await screen.findByTestId("provider-usage-calls");
    const row = within(calls).getByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000e000");
    expect(row.textContent).toContain("0.00632100");
    expect(row.textContent).not.toContain("0.00000000");
  });

  it("shows large precise call amounts without Number precision loss", async () => {
    stubUsage("1.5", ["123456789012345678.12345678"]);
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const calls = await screen.findByTestId("provider-usage-calls");
    const row = within(calls).getByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000e000");
    expect(row.textContent).toContain("123456789012345678.12345678");
  });

  it("shows summary amounts directly in currency units (small value)", async () => {
    stubUsage("0.00632100", []);
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const summary = await screen.findByTestId("provider-usage-summary");
    expect(summary.textContent).toContain("0.006321");
    expect(summary.textContent).not.toContain("0.000000");
  });

  it("shows summary amounts directly in currency units (large precise value)", async () => {
    stubUsage("123456789012345678.123456", []);
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const summary = await screen.findByTestId("provider-usage-summary");
    expect(summary.textContent).toContain("123456789012345678.123456");
  });
});

describe("S008 regression: full-day window boundary", () => {
  it("renders an accessible, representable end boundary for the default 1440 and submits 1440", async () => {
    const created: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>;
        created.push(body);
        return HttpResponse.json({
          err_code: 0, message: "ok", request_id: "x",
          data: { id: "rv-fd", provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID, ...body, version: 1, created_at: "2026-12-01T00:00:00Z" },
        });
      }),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    const end = screen.getByTestId<HTMLInputElement>("rate-window-end-0");
    // type=time cannot hold "24:00": the browser sanitizes it to an empty,
    // unreadable control. The boundary must be visibly representable.
    expect(end.value).not.toBe("");
    const fullDayControl = screen.queryByRole("checkbox", { name: /24:00|\u5168\u5929|full/i })
      ?? screen.queryByRole("switch", { name: /24:00|\u5168\u5929|full/i });
    expect(end.value === "24:00" && end.type !== "time" ? true : fullDayControl !== null).toBe(true);

    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-12-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "2.34" } });
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "5.67" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => { expect(created).toHaveLength(1); });
    const windows = (created[0] as { windows: Array<Record<string, unknown>> }).windows;
    expect(windows[0]).toMatchObject({ start_minute: 0, end_minute: 1440 });
  });
});

describe("S008 regression: reference template respects currency", () => {
  async function applyTemplateWithDraftCurrency(templateCurrency: "CNY" | "USD", draftCurrency: "CNY" | "USD"): Promise<void> {
    const fetches: number[] = [];
    stubBudgetTemplate(templateCurrency);
    server.events.on("request:start", ({ request }) => {
      if (request.url.includes("/admin/settings/ai-budget")) fetches.push(1);
    });
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    if (draftCurrency === "USD") {
      await userEvent.click(screen.getByTestId("rate-currency"));
      await userEvent.click(within(await screen.findByRole("listbox")).getByText("USD"));
    }
    await waitFor(() => { expect(fetches.length).toBeGreaterThan(0); });
    await userEvent.click(screen.getByTestId("rate-apply-template"));
    server.events.removeAllListeners();
  }

  it("does not import CNY template prices into a USD draft and explains why", async () => {
    await applyTemplateWithDraftCurrency("CNY", "USD");
    expect(screen.getByTestId("rate-input-price-0")).toHaveValue("");
    expect(screen.getByTestId("rate-output-price-0")).toHaveValue("");
    expect(await screen.findByRole("alert")).toHaveTextContent(/\u5e01\u79cd|currency|CNY|USD/i);
  });

  it("does not import USD template prices into a CNY draft and explains why", async () => {
    await applyTemplateWithDraftCurrency("USD", "CNY");
    expect(screen.getByTestId("rate-input-price-0")).toHaveValue("");
    expect(screen.getByTestId("rate-output-price-0")).toHaveValue("");
    expect(await screen.findByRole("alert")).toHaveTextContent(/\u5e01\u79cd|currency|CNY|USD/i);
  });
});

describe("S008 regression additions: pagination, field errors, a11y, error states", () => {
  it("pages the calls stream server-side with the provider_id filter", async () => {
    const callsQueries: string[] = [];
    server.use(
      http.get(apiRoute("/admin/ai-usage/summary"), () =>
        HttpResponse.json({ err_code: 0, message: "ok", data: [], request_id: "x" }),
      ),
      http.get(apiRoute("/admin/ai-usage/calls"), ({ request }) => {
        const url = new URL(request.url);
        // RCP-20261102-S008: provider filtering executes server-side — request must carry
        // provider_id, and cross-provider rows are dropped by the "server" (mirroring the real backend).
        callsQueries.push(url.searchParams.get("provider_id") ?? "");
        const providerFilter = url.searchParams.get("provider_id");
        const serverFilters = (rows: ReturnType<typeof usageCallRow>[]): ReturnType<typeof usageCallRow>[] =>
          providerFilter === null ? rows : rows.filter((row) => String(row.provider_id) === providerFilter);
        const after = url.searchParams.get("after");
        const pageBody = after !== null && after !== ""
          ? {
              items: serverFilters([usageCallRow("0b1e2f3a-0000-4000-8000-00000000e012", "0.03")]),
              page: { next_cursor: null, has_more: false },
            }
          : {
              items: serverFilters([
                usageCallRow("0b1e2f3a-0000-4000-8000-00000000e010", "0.01"),
                { ...usageCallRow("0b1e2f3a-0000-4000-8000-00000000e011", "0.02"), provider_id: "provider-other" },
              ]),
              page: { next_cursor: "cursor-page-2", has_more: true },
            };
        return HttpResponse.json({ err_code: 0, message: "ok", request_id: "x", data: pageBody });
      }),
    );
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const calls = await screen.findByTestId("provider-usage-calls");
    // The panel asks the server for this provider's stream; the foreign
    // row in the fixture page is the server's responsibility now — the
    // panel renders what the server returned for THIS provider.
    expect(callsQueries[0]).toBe(ADMIN_FIXTURE_PROVIDER_PRIMARY_ID);
    expect(within(calls).getByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000e010")).toBeInTheDocument();
    expect(within(calls).queryByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000e011")).not.toBeInTheDocument();
    // has_more=true keeps the honest next affordance even with one visible row.
    await userEvent.click(screen.getByTestId("provider-usage-calls-next"));
    await waitFor(() => {
      expect(screen.getByTestId("provider-usage-row-0b1e2f3a-0000-4000-8000-00000000e012")).toBeInTheDocument();
    });
    // Stream exhausted: the next affordance disables (never fabricated rows).
    expect(screen.getByTestId("provider-usage-calls-next")).toBeDisabled();
    // Every page request carries the provider filter (cursor pages too).
    expect(callsQueries.every((value) => value === ADMIN_FIXTURE_PROVIDER_PRIMARY_ID)).toBe(true);
  });

  it("blocks invalid prices and until<from with per-field errors and no request", async () => {
    const created: Array<Record<string, unknown>> = [];
    server.use(
      http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), async ({ request }) => {
        created.push((await request.json()) as Record<string, unknown>);
        return HttpResponse.json({ err_code: 0, message: "ok", data: {}, request_id: "x" });
      }),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    fireEvent.change(screen.getByTestId("rate-from"), { target: { value: "2026-12-01" } });
    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "abc" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    expect(screen.getByTestId("rate-input-price-0")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByTestId("provider-rate-form").textContent).toContain("\u4ef7\u683c\u987b\u4e3a\u975e\u8d1f");
    expect(created).toHaveLength(0);

    fireEvent.change(screen.getByTestId("rate-input-price-0"), { target: { value: "2.34" } });
    fireEvent.change(screen.getByTestId("rate-until"), { target: { value: "2026-11-30" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    expect(screen.getByTestId("provider-rate-form").textContent).toContain("\u751f\u6548\u81f3\u4e0d\u80fd\u65e9\u4e8e");
    expect(created).toHaveLength(0);

    fireEvent.change(screen.getByTestId("rate-until"), { target: { value: "2027-06-30" } });
    // The billing zone is a Select (owner 2026-11-05), not free text: it can
    // only hold a listed zone, so pick one explicitly before the valid submit.
    const zoneTrigger = screen.getByTestId("rate-timezone");
    expect(zoneTrigger).toHaveAttribute("data-slot", "select-trigger");
    expect(zoneTrigger.tagName).not.toBe("INPUT");
    await userEvent.click(zoneTrigger);
    await userEvent.click(await screen.findByRole("option", { name: "UTC" }));
    expect(screen.getByTestId("rate-timezone")).toHaveTextContent("UTC");

    // Everything valid again: zero is a legal price and the create goes out.
    fireEvent.change(screen.getByTestId("rate-output-price-0"), { target: { value: "0" } });
    await userEvent.click(screen.getByTestId("rate-submit"));
    await waitFor(() => { expect(created).toHaveLength(1); });
    const windows = (created[0] as { windows: Array<Record<string, unknown>> }).windows;
    expect(windows[0]).toMatchObject({ input_price: "2.34", output_price: "0" });
    expect(created[0]).toMatchObject({ timezone: "UTC" });
  });

  it("exposes accessible names for currency, price and window-end controls", async () => {
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    await openRateForm();
    expect(screen.getByLabelText("\u5e01\u79cd")).toHaveAttribute("data-testid", "rate-currency");
    expect(screen.getByLabelText("\u8f93\u5165\u4ef7")).toHaveAttribute("data-testid", "rate-input-price-0");
    expect(screen.getByLabelText("\u8f93\u51fa\u4ef7")).toHaveAttribute("data-testid", "rate-output-price-0");
    expect(screen.getByLabelText("\u65f6\u6bb5 end")).toHaveAttribute("data-testid", "rate-window-end-0");
  });

  it("surfaces a rate-version query failure as an error state with request id", async () => {
    server.use(
      http.get(apiRoute("/admin/ai-providers/:providerId/rate-versions"), () =>
        HttpResponse.json({ err_code: 500, message: "boom", data: null, request_id: "rv-req-1" }),
      ),
    );
    renderPanel(<ProviderPricingPanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("\u8bf7\u6c42\u5931\u8d25");
    expect(screen.queryByTestId("provider-rate-table")).not.toBeInTheDocument();
  });

  it("surfaces a calls-query failure as an error state instead of an empty table", async () => {
    server.use(
      http.get(apiRoute("/admin/ai-usage/summary"), () =>
        HttpResponse.json({ err_code: 0, message: "ok", data: [], request_id: "x" }),
      ),
      http.get(apiRoute("/admin/ai-usage/calls"), () =>
        HttpResponse.json({ err_code: 500, message: "boom", data: null, request_id: "calls-req-1" }),
      ),
    );
    renderPanel(<ProviderUsagePanel providerId={ADMIN_FIXTURE_PROVIDER_PRIMARY_ID} />);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("\u8bf7\u6c42\u5931\u8d25");
    expect(screen.queryByTestId("provider-usage-calls")).not.toBeInTheDocument();
  });
});
