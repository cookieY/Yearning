import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it } from "vitest";
import { HttpResponse, http } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { testBusinessErrorJson, testEnvelope, testProblemJson } from "@/test/envelope";
import { SESSION_USER_ID } from "@/test/session";
import "@/shared/i18n";
import { resetPublicBranding } from "@/shared/lib/branding";
import { SessionProvider } from "@/features/auth/session-provider";
import LoginPage from "./login-page";

/**
 * The login screen is contract-driven: visible methods follow
 * GET /auth/providers, business failures map through the generated error
 * profile, and the admin lock-out shows the server-side reset command.
 */

function envelope(data: unknown) {
  return testEnvelope(data, SESSION_USER_ID);
}

function businessError(errCode: number, message: string) {
  return testBusinessErrorJson(errCode, message, { requestId: SESSION_USER_ID });
}

function renderLogin() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <SessionProvider>
          <LoginPage />
        </SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  server.resetHandlers();
  resetPublicBranding();
});

describe("LoginPage", () => {
  it("renders the local form for the default provider set", async () => {
    renderLogin();
    expect(await screen.findByLabelText("\u7528\u6237\u540d")).toBeVisible();
    expect(screen.getByLabelText("\u5bc6\u7801")).toBeVisible();
    // The richer default mock advertises LDAP and one OIDC provider.
    expect(screen.getByRole("button", { name: "\u672c\u5730\u8d26\u53f7" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "LDAP" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("link", { name: "\u4f7f\u7528 \u4f01\u4e1a OIDC \u767b\u5f55" })).toBeVisible();
  });

  it("shows a mapped INVALID_CREDENTIALS message, never the raw error name", async () => {
    server.use(http.post(apiRoute("/auth/login"), () => businessError(1101, "invalid credentials")));
    renderLogin();
    await userEvent.type(await screen.findByLabelText("\u7528\u6237\u540d"), "henry");
    await userEvent.type(screen.getByLabelText("\u5bc6\u7801"), "wrong-password");
    await userEvent.click(screen.getByRole("button", { name: "\u767b\u5f55" }));
    expect(await screen.findByText("\u7528\u6237\u540d\u6216\u5bc6\u7801\u4e0d\u6b63\u786e\u3002", {}, { timeout: 3000 })).toBeVisible();
    expect(screen.getByText(/\u8bf7\u6c42\u6807\u8bc6/)).toBeVisible();
  });

  it("shows the server reset command when the admin account is locked", async () => {
    server.use(
      http.post(apiRoute("/auth/login"), () =>
        businessError(1102, "admin locked; run ./Yearning --reset-admin-password"),
      ),
    );
    renderLogin();
    await userEvent.type(await screen.findByLabelText("\u7528\u6237\u540d"), "admin");
    await userEvent.type(screen.getByLabelText("\u5bc6\u7801"), "wrong-password");
    await userEvent.click(screen.getByRole("button", { name: "\u767b\u5f55" }));
    expect(await screen.findByText("\u8d85\u7ea7\u7ba1\u7406\u5458\u5df2\u9501\u5b9a", {}, { timeout: 3000 })).toBeVisible();
    expect(screen.getByText("./Yearning --reset-admin-password")).toBeVisible();
  });

  it("sends the safe generic error for an err_code outside the login profile", async () => {
    // 4001 QUERY_GRANT_REQUIRED is real but never declared for login.
    server.use(http.post(apiRoute("/auth/login"), () => businessError(4001, "grant required")));
    renderLogin();
    await userEvent.type(await screen.findByLabelText("\u7528\u6237\u540d"), "henry");
    await userEvent.type(screen.getByLabelText("\u5bc6\u7801"), "fixture-pw");
    await userEvent.click(screen.getByRole("button", { name: "\u767b\u5f55" }));
    expect(await screen.findByText(/\u8d85\u51fa\u63a5\u53e3\u58f0\u660e\u7684\u9519\u8bef\u8303\u56f4/, {}, { timeout: 3000 })).toBeVisible();
    expect(screen.getByText(/\u8bf7\u6c42\u6807\u8bc6/)).toBeVisible();
  });

  it("submits via the LDAP endpoint on an LDAP-only deployment without a switcher", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        HttpResponse.json(envelope({ local: false, ldap: true, oidc: [] })),
      ),
    );
    let localHits = 0;
    let ldapCredentials: unknown = null;
    server.use(
      http.post(apiRoute("/auth/login"), () => {
        localHits += 1;
        return HttpResponse.json(envelope(null));
      }),
      http.post(apiRoute("/auth/ldap/login"), async ({ request }) => {
        ldapCredentials = (await request.json()) as unknown;
        return HttpResponse.json(envelope(null));
      }),
    );
    renderLogin();
    await userEvent.type(await screen.findByLabelText("\u7528\u6237\u540d", {}, { timeout: 3000 }), "henry");
    await userEvent.type(screen.getByLabelText("\u5bc6\u7801"), "fixture-pw");
    await userEvent.click(screen.getByRole("button", { name: "\u767b\u5f55" }));
    await waitFor(() => {
      expect(ldapCredentials).not.toBeNull();
    });
    expect(localHits).toBe(0);
    expect(ldapCredentials).toEqual({ username: "henry", password: "fixture-pw" });
  });

  it("offers and submits the LDAP mode when the server enables it", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        HttpResponse.json(envelope({ local: true, ldap: true, oidc: [] })),
      ),
    );
    let ldapCredentials: unknown = null;
    server.use(
      http.post(apiRoute("/auth/ldap/login"), async ({ request }) => {
        ldapCredentials = (await request.json()) as unknown;
        return HttpResponse.json(envelope(null));
      }),
    );
    renderLogin();
    await userEvent.click(await screen.findByRole("button", { name: "LDAP" }));
    await userEvent.type(screen.getByLabelText("\u7528\u6237\u540d"), "henry");
    await userEvent.type(screen.getByLabelText("\u5bc6\u7801"), "fixture-pw");
    await userEvent.click(screen.getByRole("button", { name: "\u767b\u5f55" }));
    await waitFor(() => {
      expect(ldapCredentials).not.toBeNull();
    });
    expect(ldapCredentials).toEqual({ username: "henry", password: "fixture-pw" });
  });

  it("hides the credential form on an OIDC-only deployment", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        HttpResponse.json(
          envelope({
            local: false,
            ldap: false,
            oidc: [{ key: "corp", label: "Corp SSO", start_url: "/oidc/start" }],
          }),
        ),
      ),
    );
    renderLogin();
    expect(
      await screen.findByRole("link", { name: "\u4f7f\u7528 Corp SSO \u767b\u5f55" }, { timeout: 3000 }),
    ).toBeVisible();
    expect(screen.queryByLabelText("\u7528\u6237\u540d")).not.toBeInTheDocument();
  });

  it("explains itself when the server offers no login method", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        HttpResponse.json(envelope({ local: false, ldap: false, oidc: [] })),
      ),
    );
    renderLogin();
    expect(await screen.findByText("\u767b\u5f55\u65b9\u5f0f\u4e0d\u53ef\u7528", {}, { timeout: 3000 })).toBeVisible();
    expect(screen.queryByLabelText("\u7528\u6237\u540d")).not.toBeInTheDocument();
  });

  it("filters OIDC providers whose start_url is not an http(s) target", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        HttpResponse.json(
          envelope({
            local: true,
            ldap: false,
            oidc: [{ key: "evil", label: "Evil", start_url: "javascript:alert(1)" }],
          }),
        ),
      ),
    );
    renderLogin();
    await screen.findByLabelText("\u7528\u6237\u540d");
    expect(screen.queryByRole("link", { name: /Evil/ })).not.toBeInTheDocument();
  });

  it("shows a retryable error state when the provider list fails", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        testProblemJson(500, "internal_error", "boom", SESSION_USER_ID),
      ),
    );
    renderLogin();
    expect(await screen.findByText("\u670d\u52a1\u6682\u65f6\u4e0d\u53ef\u7528\uff0c\u8bf7\u7a0d\u540e\u91cd\u8bd5\u3002", {}, { timeout: 3000 })).toBeVisible();
    expect(screen.getByText(/\u8bf7\u6c42\u6807\u8bc6/)).toBeVisible();
  });

  it("lists OIDC providers returned by the server", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        HttpResponse.json(
          envelope({
            local: true,
            ldap: false,
            oidc: [{ key: "corp", label: "Corp SSO", start_url: "/api/v4/auth/oidc/corp/start" }],
          }),
        ),
      ),
    );
    renderLogin();
    const link = await screen.findByRole("link", { name: "\u4f7f\u7528 Corp SSO \u767b\u5f55" }, { timeout: 3000 });
    expect(link).toHaveAttribute("href", "/api/v4/auth/oidc/corp/start");
  });

  it("submits credentials to the login operation and succeeds", async () => {
    let received: unknown = null;
    server.use(
      http.post(apiRoute("/auth/login"), async ({ request }) => {
        received = (await request.json()) as unknown;
        return HttpResponse.json(envelope(null), {
          headers: new Headers({
            "Set-Cookie": "yearning_session=e2e; Path=/; HttpOnly; SameSite=Lax",
          }),
        });
      }),
    );
    renderLogin();
    await userEvent.type(await screen.findByLabelText("\u7528\u6237\u540d"), "henry");
    await userEvent.type(screen.getByLabelText("\u5bc6\u7801"), "fixture-pw");
    await userEvent.click(screen.getByRole("button", { name: "\u767b\u5f55" }));
    await waitFor(() => {
      expect(received).not.toBeNull();
    });
    expect(received).toEqual({ username: "henry", password: "fixture-pw" });
  });

  it("renders the AGPL license notice and source link on the login screen", async () => {
    renderLogin();
    expect(await screen.findByText("AGPL-3.0 Licensed | Copyright © 2017-present Henry Yee")).toBeVisible();
    const sourceLink = screen.getByRole("link", { name: "\u6e90\u7801" });
    expect(sourceLink).toHaveAttribute("href", "https://source.example/");
    expect(sourceLink).toHaveAttribute("rel", "noreferrer noopener");
  });

  it("applies custom product_name and login_description from GET /auth/providers", async () => {
    server.use(
      http.get(apiRoute("/auth/providers"), () =>
        HttpResponse.json(
          envelope({
            local: true,
            ldap: false,
            oidc: [],
            branding: {
              product_name: "AcmeSQL",
              login_description: "Internal DB portal",
            },
          }),
        ),
      ),
    );
    renderLogin();
    expect(await screen.findByText("\u767b\u5f55 AcmeSQL")).toBeVisible();
    expect(document.title).toBe("AcmeSQL");
    expect(screen.getByTestId("login-description")).toHaveTextContent("Internal DB portal");
    expect(screen.getByText(/AcmeSQL v4/)).toBeVisible();
    const logoLink = screen.getByRole("link", { name: "Yearning" });
    expect(logoLink.querySelectorAll("img")).toHaveLength(2);
    expect(screen.queryByTestId("brand-product-name")).toBeNull();
  });
});
