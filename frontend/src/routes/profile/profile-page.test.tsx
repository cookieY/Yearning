import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { HttpResponse, http } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import { server } from "@/test/msw/server";
import { SessionProvider } from "@/features/auth/session-provider";
import "@/shared/i18n";
import ProfilePage from "@/routes/profile/profile-page";

/**
 * §18.52 self-service password change on the profile security tab: local
 * accounts get the three-field form (client validation, contract errors,
 * session-expired redirect on success — the server revokes every session);
 * ldap/oidc accounts get the identity-source note instead.
 */

const SESSION_USER_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

function meOverride(authProvider: string | undefined): void {
  server.use(
    http.get("*/users/me", () =>
      HttpResponse.json({
        err_code: 0,
        message: "ok",
        data: {
          id: SESSION_USER_ID,
          username: "henry",
          display_name: "henry",
          email: null,
          is_builtin_admin: false,
          version: 1,
          created_at: "2026-08-28T08:00:00Z",
          updated_at: "2026-08-28T08:00:00Z",
          can_access_admin: false,
          ...(authProvider === undefined ? {} : { auth_provider: authProvider }),
        },
        request_id: SESSION_USER_ID,
      }),
    ),
  );
}

function renderProfile(): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <SessionProvider>
        <MemoryRouter initialEntries={["/profile"]}>
          <Routes>
            <Route path="/profile" element={<ProfilePage />} />
            <Route path="/login" element={<div data-testid="login-route-stub" />} />
          </Routes>
        </MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

async function openSecurityTab(): Promise<void> {
  await userEvent.click(screen.getByTestId("profile-tab-security"));
}

beforeEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "default");
});

describe("profile security tab password change (§18.52)", () => {
  it("blocks submit until the fields satisfy the client policy", async () => {
    meOverride(undefined);
    renderProfile();
    expect(await screen.findByRole("heading", { level: 1, name: "\u4e2a\u4eba\u4e2d\u5fc3" })).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    await openSecurityTab();

    const submit = screen.getByTestId("profile-change-password-submit");
    expect(submit).toBeDisabled();

    await userEvent.type(screen.getByTestId("profile-old-password"), "old-fixture-pw");
    await userEvent.type(screen.getByTestId("profile-new-password"), "new-fixture-pw-12");
    await userEvent.type(screen.getByTestId("profile-confirm-password"), "different-fixture");
    expect(await screen.findByTestId("profile-confirm-mismatch")).toBeVisible();
    expect(submit).toBeDisabled();

    await userEvent.clear(screen.getByTestId("profile-confirm-password"));
    await userEvent.type(screen.getByTestId("profile-confirm-password"), "new-fixture-pw-12");
    expect(screen.queryByTestId("profile-confirm-mismatch")).toBeNull();
    expect(submit).toBeEnabled();
  });

  it("returns to the login route after a successful change", async () => {
    meOverride(undefined);
    server.use(
      http.post("*/users/me/password", () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: null,
          request_id: SESSION_USER_ID,
        }),
      ),
    );
    renderProfile();
    await openSecurityTab();

    await userEvent.type(screen.getByTestId("profile-old-password"), "old-fixture-pw");
    await userEvent.type(screen.getByTestId("profile-new-password"), "new-fixture-pw-12");
    await userEvent.type(screen.getByTestId("profile-confirm-password"), "new-fixture-pw-12");
    await userEvent.click(screen.getByTestId("profile-change-password-submit"));

    expect(await screen.findByTestId("login-route-stub")).toBeVisible();
  });

  it("renders the dedicated copy when the old password is wrong (1101)", async () => {
    meOverride(undefined);
    server.use(
      http.post("*/users/me/password", () =>
        HttpResponse.json({
          err_code: 1101,
          message: "\u7528\u6237\u540d\u6216\u5bc6\u7801\u9519\u8bef",
          data: null,
          request_id: SESSION_USER_ID,
          retryable: false,
        }),
      ),
    );
    renderProfile();
    await openSecurityTab();

    await userEvent.type(screen.getByTestId("profile-old-password"), "wrong-old-pw");
    await userEvent.type(screen.getByTestId("profile-new-password"), "new-fixture-pw-12");
    await userEvent.type(screen.getByTestId("profile-confirm-password"), "new-fixture-pw-12");
    await userEvent.click(screen.getByTestId("profile-change-password-submit"));

    const alert = await screen.findByTestId("profile-change-password-error");
    expect(alert.textContent).toContain("\u539f\u5bc6\u7801\u4e0d\u6b63\u786e");
  });

  it("shows the identity-source note instead of the form for an oidc account", async () => {
    meOverride("oidc");
    renderProfile();
    await openSecurityTab();

    expect(await screen.findByTestId("profile-security-external-note")).toBeVisible();
    expect(screen.getByTestId("profile-security-external-note").textContent).toContain("OIDC");
    expect(screen.queryByTestId("profile-old-password")).toBeNull();
    expect(screen.queryByTestId("profile-change-password-submit")).toBeNull();
  });
});
