import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { HttpResponse, http } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import { server } from "@/test/msw/server";
import "@/shared/i18n";
import { SessionProvider } from "@/features/auth/session-provider";
import {
  RedirectIfAuthenticated,
  RequireAdminCapability,
  RequireSession,
} from "./guards";

/**
 * Guards read the session fact from GET /users/me only. Anonymous visitors go
 * to /login, authenticated ones leave /login for the workspace, and admin
 * surfaces require the server-declared can_access_admin capability.
 *
 * Isolation note: a 401 response that lands after a test finished would fire
 * the session-expired event into the NEXT test's provider, so tests whose
 * session query ends unauthenticated let the network settle before returning.
 */

const UUID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

function userEnvelope(canAccessAdmin: boolean) {
  return HttpResponse.json({
    err_code: 0,
    message: "ok",
    data: {
      id: UUID,
      username: "henry",
      display_name: "Henry",
      email: null,
      is_builtin_admin: canAccessAdmin,
      version: 1,
      created_at: "2026-08-28T08:00:00Z",
      updated_at: "2026-08-28T08:00:00Z",
      can_access_admin: canAccessAdmin,
    },
    request_id: UUID,
  });
}

function renderGuard(ui: React.ReactElement, routePath: string, initialPath: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialPath]}>
        <SessionProvider>
          <Routes>
            <Route path="/workspace" element={<div>workspace-page</div>} />
            <Route path="/403" element={<div>forbidden-page</div>} />
            <Route path={routePath} element={ui} />
          </Routes>
        </SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** Lets in-flight session requests settle so events never cross tests. */
function settle(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 150));
}

beforeEach(() => {
  server.resetHandlers();
});

describe("RequireSession", () => {
  it("redirects anonymous visitors to /login", async () => {
    renderGuard(
      <RequireSession>
        <div>protected</div>
      </RequireSession>,
      "/guarded",
      "/guarded",
    );
    // No /login route is mounted; the redirect leaves the probe unmounted.
    expect(screen.queryByText("protected")).not.toBeInTheDocument();
    await settle();
  });

  it("renders a retryable degraded panel when the probe fails for non-auth reasons", async () => {
    server.use(
      http.get(
        "*/users/me",
        () =>
          HttpResponse.json(
            {
              type: "about:blank",
              title: "internal_error",
              status: 500,
              detail: "boom",
              request_id: UUID,
            },
            { status: 500, headers: { "Content-Type": "application/problem+json" } },
          ),
      ),
    );
    renderGuard(
      <RequireSession>
        <div>protected</div>
      </RequireSession>,
      "/guarded",
      "/guarded",
    );
    // Transport text instead of a login redirect — the user is not signed out.
    expect(
      await screen.findByText("\u670d\u52a1\u6682\u65f6\u4e0d\u53ef\u7528\uff0c\u8bf7\u7a0d\u540e\u91cd\u8bd5\u3002", {}, { timeout: 3000 }),
    ).toBeVisible();
    expect(screen.getByText(/\u8bf7\u6c42\u6807\u8bc6/)).toBeVisible();
    await settle();
  });

  it("admits authenticated visitors", async () => {
    server.use(http.get("*/users/me", () => userEnvelope(false)));
    renderGuard(
      <RequireSession>
        <div>protected</div>
      </RequireSession>,
      "/guarded",
      "/guarded",
    );
    // First MSW round-trip in a file can take a while; network-bound finds
    // get a patient timeout instead of the 1s default.
    expect(await screen.findByText("protected", {}, { timeout: 3000 })).toBeVisible();
  });
});

describe("RedirectIfAuthenticated", () => {
  it("sends an authenticated visitor away from /login", async () => {
    server.use(http.get("*/users/me", () => userEnvelope(false)));
    renderGuard(
      <RedirectIfAuthenticated>
        <div>login-form</div>
      </RedirectIfAuthenticated>,
      "/login",
      "/login",
    );
    expect(await screen.findByText("workspace-page", {}, { timeout: 3000 })).toBeVisible();
  });

  it("keeps an anonymous visitor on /login", async () => {
    renderGuard(
      <RedirectIfAuthenticated>
        <div>login-form</div>
      </RedirectIfAuthenticated>,
      "/login",
      "/login",
    );
    expect(await screen.findByText("login-form", {}, { timeout: 3000 })).toBeVisible();
    await settle();
  });
});

describe("RequireAdminCapability", () => {
  it("sends users without the server capability to /403", async () => {
    server.use(http.get("*/users/me", () => userEnvelope(false)));
    renderGuard(
      <RequireAdminCapability>
        <div>admin-page</div>
      </RequireAdminCapability>,
      "/admin",
      "/admin",
    );
    expect(await screen.findByText("forbidden-page", {}, { timeout: 3000 })).toBeVisible();
  });

  it("admits users with the server capability", async () => {
    server.use(http.get("*/users/me", () => userEnvelope(true)));
    renderGuard(
      <RequireAdminCapability>
        <div>admin-page</div>
      </RequireAdminCapability>,
      "/admin",
      "/admin",
    );
    expect(await screen.findByText("admin-page", {}, { timeout: 3000 })).toBeVisible();
  });
});
