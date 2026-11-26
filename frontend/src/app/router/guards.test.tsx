import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { http } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { testProblemJson } from "@/test/envelope";
import { SESSION_USER_ID, stubSession } from "@/test/session";
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

function stubGuardUser(canAccessAdmin: boolean): void {
  stubSession({ admin: canAccessAdmin, displayName: "Henry" });
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
      http.get(apiRoute("/users/me"), () =>
        testProblemJson(500, "internal_error", "boom", SESSION_USER_ID),
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
    stubGuardUser(false);
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
    stubGuardUser(false);
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
    stubGuardUser(false);
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
    stubGuardUser(true);
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
