import type { ReactElement } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HttpResponse, delay, http } from "msw";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import { SessionProvider } from "@/features/auth/session-provider";
import AdminAiBudgetPage from "@/routes/admin/admin-ai-budget-page";
import AdminAnnouncementsPage from "@/routes/admin/admin-announcements-page";
import AdminIdentityProvidersPage from "@/routes/admin/admin-identity-providers-page";
import AdminSettingsNamespacePage from "@/routes/admin/admin-settings-page";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";
import { apiRoute } from "@/shared/mock/api-route";
import { resetPublicBranding } from "@/shared/lib/branding";
import { BrandLogo } from "@/app/shell/brand-logo";
import { server } from "@/test/msw/server";
import { grantSession, SESSION_USER_ID } from "@/test/session";
import "@/shared/i18n";

function stubAdminSession(): void {
  grantSession();
}

function renderPage(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(
    <QueryClientProvider client={client}>
      <SessionProvider>
        <MemoryRouter>{ui}</MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

function renderSettingsNamespace(namespace: string, header?: ReactElement) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return render(
    <QueryClientProvider client={client}>
      <SessionProvider>
        <MemoryRouter initialEntries={[`/admin/settings/${namespace}`]}>
          {header}
          <Routes>
            <Route path="/admin/settings/:namespace" element={<AdminSettingsNamespacePage />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

beforeAll(() => {
  Object.defineProperty(Element.prototype, "hasPointerCapture", {
    value: () => false,
    configurable: true,
  });
  Object.defineProperty(Element.prototype, "scrollIntoView", {
    value: () => undefined,
    configurable: true,
  });
  Object.defineProperty(Element.prototype, "releasePointerCapture", {
    value: () => undefined,
    configurable: true,
  });
});

beforeEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "admin");
  resetAdminFixture();
  resetPublicBranding();
});

describe("admin settings visual consistency", () => {
  it("uses the shadcn checkbox and select primitives in the general namespace", async () => {
    stubAdminSession();
    renderSettingsNamespace("general");

    const page = await screen.findByTestId("admin-settings-general");
    expect(page).toHaveAttribute("data-slot", "page-layout");
    expect(screen.getByRole("heading", { level: 1, name: "\u901a\u7528\u8bbe\u7f6e" })).toBeVisible();
    const registration = await screen.findByTestId("settings-general-registration");
    expect(screen.getByRole("checkbox", { name: "\u5f00\u653e\u7528\u6237\u6ce8\u518c" })).toBe(registration);
    expect(registration).toHaveAttribute("data-slot", "checkbox");
    expect(registration.tagName).not.toBe("INPUT");
    expect(registration).not.toBeChecked();
    await userEvent.click(registration);
    await waitFor(() => expect(registration).toBeChecked());

    // Scheme 1 language boundary: general settings no longer expose a contradictory
    // global UI language picker; only system timezone lives here.
    expect(screen.queryByTestId("settings-general-locale")).toBeNull();
    const timezone = screen.getByTestId("settings-general-timezone");
    expect(screen.getByRole("combobox", { name: "\u7cfb\u7edf\u65f6\u533a" })).toBe(timezone);
    expect(timezone).toHaveAttribute("data-slot", "select-trigger");
    expect(timezone.tagName).not.toBe("INPUT");
    expect(timezone).toHaveTextContent("Asia/Shanghai");
    await userEvent.click(timezone);
    await userEvent.click(await screen.findByRole("option", { name: "UTC" }));
    expect(timezone).toHaveTextContent("UTC");
  });

  it("edits execution-window weekdays through a multi-select, never a comma list", async () => {
    stubAdminSession();
    server.use(
      http.get(apiRoute("/admin/settings/:namespace"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            namespace: "execution",
            schema_version: 1,
            settings: {
              restriction_enabled: true,
              windows: [{ days_of_week: ["monday"], start_local_time: "02:00", end_local_time: "06:00" }],
              osc: { tool: "none", defaults: {} },
            },
          },
          request_id: "x",
        }),
      ),
    );
    renderSettingsNamespace("execution");

    const weekdays = await screen.findByTestId("settings-execution-weekdays-0");
    expect(weekdays).toHaveAttribute("data-slot", "select-trigger");
    expect(weekdays.tagName).not.toBe("INPUT");
    expect(weekdays).toHaveTextContent("\u5468\u4e00");
    await userEvent.click(weekdays);
    await userEvent.click(await screen.findByRole("option", { name: "\u5468\u4e09" }));
    await waitFor(() => { expect(weekdays).toHaveTextContent("\u5468\u4e00\u3001\u5468\u4e09"); });
  });

  it("blocks a structurally invalid server-held timezone before any save (F-SET-001)", async () => {
    stubAdminSession();
    // The picker can only ever offer well-formed zones, so a malformed value
    // can arrive solely from the server — it stays visible and blocks saving
    // until the admin picks a valid zone.
    server.use(
      http.get(apiRoute("/admin/settings/:namespace"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            namespace: "general",
            schema_version: 1,
            settings: {
              registration_enabled: false,
              system_timezone: "Asia//Shanghai",
              default_locale: "zh-CN",
              public_base_url: "",
            },
            version: 1,
            updated_by: SESSION_USER_ID,
            updated_at: "2026-10-24T10:00:00Z",
          },
          request_id: "req-fset001-invalid",
        }),
      ),
    );
    renderSettingsNamespace("general");

    const trigger = await screen.findByTestId("settings-general-timezone");
    expect(trigger).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByTestId("settings-general-timezone-error")).toHaveTextContent("\u65f6\u533a\u4e0d\u5408\u6cd5");
    expect(screen.getByTestId("settings-timezone-blocked")).toBeInTheDocument();
    expect(screen.getByTestId("settings-save")).toBeDisabled();
    expect(screen.getByTestId("settings-assess")).toBeDisabled();

    await userEvent.click(trigger);
    await userEvent.click(await screen.findByRole("option", { name: "UTC" }));
    expect(trigger).toHaveAttribute("aria-invalid", "false");
    expect(screen.queryByTestId("settings-general-timezone-error")).toBeNull();
    expect(screen.queryByTestId("settings-timezone-blocked")).toBeNull();
    expect(screen.getByTestId("settings-save")).toBeEnabled();
  });

  it("surfaces the server-side rejection of a picked zone (F-SET-001)", async () => {
    stubAdminSession();
    server.use(
      http.put(apiRoute("/admin/settings/:namespace"), () =>
        HttpResponse.json(
          {
            err_code: 1001,
            message: "settings: validation failed",
            data: null,
            request_id: "req-fset001",
            retryable: false,
            meta: { detail: "system_timezone timezone" },
          },
          { status: 400 },
        ),
      ),
    );
    renderSettingsNamespace("general");

    const trigger = await screen.findByTestId("settings-general-timezone");
    await userEvent.click(trigger);
    await userEvent.click(await screen.findByRole("option", { name: "UTC" }));
    expect(trigger).toHaveAttribute("aria-invalid", "false");
    expect(screen.queryByTestId("settings-timezone-blocked")).toBeNull();

    await userEvent.click(screen.getByTestId("settings-save"));
    await waitFor(() => expect(screen.getByTestId("settings-error")).toBeVisible());
    expect(screen.queryByTestId("settings-saved-note")).toBeNull();
  });

  it("uses the structural page loading state on first load", async () => {
    stubAdminSession();
    server.use(
      http.get(apiRoute("/admin/identity-providers"), async () => {
        await delay(300);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [],
            page: { has_more: false, next_cursor: null },
          },
          request_id: "req-idp-loading",
        });
      }),
    );
    renderPage(<AdminIdentityProvidersPage />);

    const loading = await screen.findByRole("status");
    expect(loading).toHaveAttribute("data-loading-variant", "page");
    expect(loading.querySelector('[data-slot="skeleton"]')).not.toBeNull();
  });

  it("keeps the save label and exposes a spinner while a save is pending", async () => {
    stubAdminSession();
    renderSettingsNamespace("general");

    const registration = await screen.findByTestId("settings-general-registration");
    await userEvent.click(registration);
    server.use(
      http.put(apiRoute("/admin/settings/:namespace"), async ({ request }) => {
        const body = (await request.json()) as { settings: Record<string, unknown> };
        await delay(300);
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            namespace: "general",
            schema_version: 1,
            settings: body.settings,
            version: 2,
            updated_by: SESSION_USER_ID,
            updated_at: "2026-10-24T10:00:00Z",
          },
          request_id: "req-settings-save",
        });
      }),
    );

    const save = screen.getByTestId("settings-save");
    await userEvent.click(save);
    expect(save).toBeDisabled();
    expect(save).toHaveAttribute("aria-busy", "true");
    expect(save).toHaveTextContent("\u4fdd\u5b58");
    expect(save.querySelector('[data-slot="spinner"]')).not.toBeNull();
    await waitFor(
      () => { expect(save.querySelector('[data-slot="spinner"]')).toBeNull(); },
      { timeout: 4000 },
    );
  });

  it("uses an Empty composition for an empty announcement revision list", async () => {
    stubAdminSession();
    server.use(
      http.get(apiRoute("/admin/announcement-revisions"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [],
            page: { has_more: false, next_cursor: null },
          },
          request_id: "req-announcements-empty",
        }),
      ),
      http.get(apiRoute("/announcements/current"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: null,
          request_id: "req-current-empty",
        }),
      ),
    );
    renderPage(<AdminAnnouncementsPage />);

    const empty = await screen.findByTestId("admin-announcements-empty");
    expect(empty.closest('[data-slot="empty"]')).not.toBeNull();
    expect(within(empty).getByText("\u6682\u65e0\u516c\u544a\u4fee\u8ba2\u3002")).toHaveAttribute(
      "data-slot",
      "empty-title",
    );
  });
});

describe("settings-related pages use one page heading", () => {
  it.each([
    ["identity providers", () => renderPage(<AdminIdentityProvidersPage />), "\u8ba4\u8bc1Provider"],
    ["notifications", () => renderSettingsNamespace("notifications"), "\u901a\u77e5"],
    ["announcements", () => renderPage(<AdminAnnouncementsPage />), "\u516c\u544a\u7ba1\u7406"],
    ["AI budget", () => renderPage(<AdminAiBudgetPage />), "AI \u9884\u7b97"],
  ] as const)("renders a single level-one heading on %s", async (_name, mount, title) => {
    stubAdminSession();
    const view = mount();
    expect(await screen.findByRole("heading", { level: 1, name: title })).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    view.unmount();
  });
});

describe("provider and channel dialogs use shadcn form controls", () => {
  it("renders the LDAP transport and enabled state through shared primitives", async () => {
    stubAdminSession();
    renderPage(<AdminIdentityProvidersPage />);

    const ldapRow = await screen.findByTestId(/^admin-idp-row-/);
    await userEvent.click(within(ldapRow).getByTestId(/^admin-idp-edit-/));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByTestId("idp-ldap-transport")).toHaveAttribute(
      "data-slot",
      "select-trigger",
    );
    const transport = within(dialog).getByTestId("idp-ldap-transport");
    expect(transport.tagName).not.toBe("SELECT");
    await userEvent.click(transport);
    await userEvent.click(await screen.findByRole("option", { name: "starttls" }));
    expect(transport).toHaveTextContent("starttls");

    const enabled = within(dialog).getByTestId("idp-enabled");
    expect(enabled).toHaveAttribute("data-slot", "checkbox");
    expect(enabled.tagName).not.toBe("INPUT");
    const initialChecked = enabled.getAttribute("aria-checked");
    await userEvent.click(enabled);
    await waitFor(() => {
      expect(enabled.getAttribute("aria-checked")).not.toBe(initialChecked);
    });
  });

  it("renders channel kind, TLS and enabled through shared primitives", async () => {
    stubAdminSession();
    renderSettingsNamespace("notifications");
    await screen.findByTestId("admin-notifications-create");
    await userEvent.click(screen.getByTestId("admin-notifications-create"));
    const dialog = await screen.findByRole("dialog");

    const kind = within(dialog).getByTestId("channel-kind");
    expect(kind).toHaveAttribute("data-slot", "select-trigger");
    expect(kind.tagName).not.toBe("SELECT");
    expect(within(dialog).getByTestId("channel-tls")).toHaveAttribute(
      "data-slot",
      "select-trigger",
    );
    expect(within(dialog).getByTestId("channel-enabled")).toHaveAttribute(
      "data-slot",
      "checkbox",
    );
    const enabled = within(dialog).getByTestId("channel-enabled");
    const initialChecked = enabled.getAttribute("aria-checked");
    await userEvent.click(enabled);
    await waitFor(() => {
      expect(enabled.getAttribute("aria-checked")).not.toBe(initialChecked);
    });

    expect(within(dialog).getByLabelText("SMTP \u8ba4\u8bc1\u7528\u6237\u540d")).toHaveAttribute(
      "data-testid",
      "channel-username",
    );
    expect(within(dialog).getByLabelText("\u53d1\u4ef6\u4eba\u90ae\u7bb1\uff08From\uff09")).toHaveAttribute(
      "data-testid",
      "channel-from",
    );
    expect(within(dialog).getByTestId("channel-host")).toHaveAttribute("placeholder", "smtp.example.com");

    await userEvent.click(kind);
    await userEvent.click(await screen.findByRole("option", { name: "\u9489\u9489" }));
    expect(within(dialog).queryByTestId("channel-host")).not.toBeInTheDocument();
  });

  it("applies branding product_name and login_description immediately upon save", async () => {
    stubAdminSession();
    renderSettingsNamespace("branding", <BrandLogo />);
    const nameInput = await screen.findByTestId("settings-branding-name");
    const descInput = screen.getByTestId("settings-branding-description");
    await userEvent.clear(nameInput);
    await userEvent.type(nameInput, "CloudDB");
    await userEvent.clear(descInput);
    await userEvent.type(descInput, "Unified SQL control plane");
    await userEvent.click(screen.getByTestId("settings-save"));

    await waitFor(() => {
      expect(document.title).toBe("CloudDB");
      expect(screen.getByTestId("settings-saved-note")).toBeVisible();
    });
    const logoLink = screen.getByRole("link", { name: "Yearning" });
    expect(logoLink.querySelectorAll("img")).toHaveLength(2);
    expect(screen.queryByTestId("brand-product-name")).toBeNull();

    // Clearing both branding fields and saving restores canonical defaults.
    await userEvent.clear(nameInput);
    await userEvent.clear(descInput);
    const saveButton = screen.getByTestId("settings-save");
    expect(saveButton).toBeEnabled();
    await userEvent.click(saveButton);
    await waitFor(() => {
      expect(document.title).toBe("Yearning");
      expect(nameInput).toHaveValue("Yearning");
    });
    expect(descInput).toHaveValue("");
    expect(screen.queryByTestId("settings-error")).toBeNull();
  });

  it("localizes SMTP password and TLS labels and packages Feishu, Slack, and DingTalk secrets on submit", async () => {
    stubAdminSession();
    const submittedBodies: { kind: string; secret: { value?: string } | undefined }[] = [];
    server.use(
      http.post(apiRoute("/admin/notification-channels"), async ({ request }) => {
        const body = (await request.json()) as { kind: string; secret?: { value?: string } };
        submittedBodies.push({ kind: body.kind, secret: body.secret });
        return HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            id: "7a1a3c4d-4444-4444-8444-00000000c999",
            kind: body.kind,
            name: "test-ch",
            enabled: true,
            secret_configured: true,
            configuration: {},
            version: 1,
            created_at: "2026-10-24T10:00:00Z",
            updated_at: "2026-10-24T10:00:00Z",
          },
          request_id: "req-chan-create",
        });
      }),
    );

    renderSettingsNamespace("notifications");
    await screen.findByTestId("admin-notifications-create");
    await userEvent.click(screen.getByTestId("admin-notifications-create"));
    let dialog = await screen.findByRole("dialog");

    // 1. Email kind shows SMTP password label and localized TLS options.
    expect(within(dialog).getByLabelText("SMTP \u5bc6\u7801 / \u6388\u6743\u7801")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByTestId("channel-tls"));
    expect(await screen.findByRole("option", { name: /SSL\/TLS \u52a0\u5bc6\u8fde\u63a5/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /STARTTLS \u52a0\u5bc6/ })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("option", { name: /STARTTLS \u52a0\u5bc6/ }));
    expect(within(dialog).getByTestId("channel-tls")).toHaveTextContent(/STARTTLS \u52a0\u5bc6/);

    // 2. Feishu splits webhook + optional sign_secret and packages JSON envelope automatically.
    await userEvent.click(within(dialog).getByTestId("channel-kind"));
    await userEvent.click(await screen.findByRole("option", { name: "\u98de\u4e66" }));
    await userEvent.type(within(dialog).getByTestId("channel-name"), "feishu-plain");
    await userEvent.type(
      within(dialog).getByTestId("channel-webhook"),
      "https://open.feishu.cn/open-apis/bot/v2/hook/hook-1",
    );
    await userEvent.click(within(dialog).getByTestId("channel-form-submit"));
    await waitFor(() => { expect(submittedBodies).toHaveLength(1); });
    expect(submittedBodies[0]).toEqual({
      kind: "feishu",
      secret: { value: JSON.stringify({ webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/hook-1" }) },
    });

    // Re-open for Feishu with signing secret.
    await userEvent.click(screen.getByTestId("admin-notifications-create"));
    dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByTestId("channel-kind"));
    await userEvent.click(await screen.findByRole("option", { name: "\u98de\u4e66" }));
    await userEvent.type(within(dialog).getByTestId("channel-name"), "feishu-signed");
    await userEvent.type(
      within(dialog).getByTestId("channel-webhook"),
      "https://open.feishu.cn/open-apis/bot/v2/hook/hook-2",
    );
    await userEvent.type(within(dialog).getByTestId("channel-sign-secret"), "sec-123");
    await userEvent.click(within(dialog).getByTestId("channel-form-submit"));
    await waitFor(() => { expect(submittedBodies).toHaveLength(2); });
    expect(submittedBodies[1]).toEqual({
      kind: "feishu",
      secret: {
        value: JSON.stringify({
          webhook_url: "https://open.feishu.cn/open-apis/bot/v2/hook/hook-2",
          sign_secret: "sec-123",
        }),
      },
    });

    // 3. Slack shows Webhook label and submits raw URL.
    await userEvent.click(screen.getByTestId("admin-notifications-create"));
    dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByTestId("channel-kind"));
    await userEvent.click(await screen.findByRole("option", { name: "Slack" }));
    expect(within(dialog).getByLabelText("Webhook \u5730\u5740")).toBeInTheDocument();
    await userEvent.type(within(dialog).getByTestId("channel-name"), "slack-ops");
    await userEvent.type(within(dialog).getByTestId("channel-secret"), "https://hooks.slack.com/services/T00/B00/XX");
    await userEvent.click(within(dialog).getByTestId("channel-form-submit"));
    await waitFor(() => { expect(submittedBodies).toHaveLength(3); });
    expect(submittedBodies[2]).toEqual({
      kind: "slack",
      secret: { value: "https://hooks.slack.com/services/T00/B00/XX" },
    });

    // 4. DingTalk preserves bare token vs {access_token, sign_secret} envelope.
    await userEvent.click(screen.getByTestId("admin-notifications-create"));
    dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByTestId("channel-kind"));
    await userEvent.click(await screen.findByRole("option", { name: "\u9489\u9489" }));
    await userEvent.type(within(dialog).getByTestId("channel-name"), "ding-signed");
    await userEvent.type(within(dialog).getByTestId("channel-webhook"), "tok-abc");
    await userEvent.type(within(dialog).getByTestId("channel-sign-secret"), "SEC123");
    await userEvent.click(within(dialog).getByTestId("channel-form-submit"));
    await waitFor(() => { expect(submittedBodies).toHaveLength(4); });
    expect(submittedBodies[3]).toEqual({
      kind: "dingtalk",
      secret: { value: JSON.stringify({ access_token: "tok-abc", sign_secret: "SEC123" }) },
    });
  });

  it("updates default notification template language from /admin/settings/notifications", async () => {
    stubAdminSession();
    let capturedLocale: unknown = null;
    server.use(
      http.put(apiRoute("/admin/settings/:namespace"), async ({ request, params }) => {
        if (params.namespace === "general") {
          const body = (await request.json()) as { settings: { default_locale?: unknown } };
          capturedLocale = body.settings.default_locale;
          return HttpResponse.json({
            err_code: 0,
            message: "ok",
            data: {
              namespace: "general",
              schema_version: 1,
              settings: body.settings,
              version: 2,
              updated_by: SESSION_USER_ID,
              updated_at: "2026-10-24T10:00:00Z",
            },
            request_id: "req-notif-locale-save",
          });
        }
        return HttpResponse.json({ err_code: 0, message: "ok", data: null, request_id: "other" });
      }),
    );

    renderSettingsNamespace("notifications");
    const localeTrigger = await screen.findByTestId("notifications-default-locale");
    await waitFor(() => { expect(localeTrigger).toBeEnabled(); });
    expect(localeTrigger).toHaveTextContent("\u7b80\u4f53\u4e2d\u6587");

    await userEvent.click(localeTrigger);
    await userEvent.click(await screen.findByRole("option", { name: "English" }));
    await waitFor(() => {
      expect(capturedLocale).toBe("en-US");
    });
  });
});
