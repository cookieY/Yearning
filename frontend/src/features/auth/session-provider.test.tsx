import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it } from "vitest";
import { server } from "@/test/msw/server";
import { HttpResponse, http } from "msw";
import "@/shared/i18n";
import { SESSION_EXPIRED_EVENT } from "@/shared/api/mutator";
import { SessionProvider, useSession } from "./session-provider";

/**
 * Session state is a server question: /users/me with no session cookie
 * answers 401 (anonymous), a success envelope answers authenticated, and
 * logout clears the cached session.
 */

const UUID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

function currentUserEnvelope(canAccessAdmin: boolean) {
  return {
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
  };
}

function Probe() {
  const session = useSession();
  return (
    <div>
      <span data-testid="status">{session.status}</span>
      <span data-testid="displayName">{session.user?.display_name ?? "none"}</span>
      <button type="button" onClick={() => void session.logout()}>
        logout
      </button>
    </div>
  );
}

function renderProbe() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <SessionProvider>
          <Probe />
        </SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  server.resetHandlers();
});

describe("SessionProvider", () => {
  it("reports anonymous when /users/me answers 401", async () => {
    renderProbe();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("anonymous"));
    expect(screen.getByTestId("displayName")).toHaveTextContent("none");
  });

  it("reports authenticated with the server identity and admin capability", async () => {
    server.use(http.get("*/users/me", () => HttpResponse.json(currentUserEnvelope(true))));
    renderProbe();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    expect(screen.getByTestId("displayName")).toHaveTextContent("Henry");
  });

  it("reports a retryable unavailable state for non-authentication failures", async () => {
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
    renderProbe();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("unavailable"));
  });

  it("flips an authenticated session to anonymous when the mutator announces expiry", async () => {
    let expired = false;
    server.use(
      http.get("*/users/me", () => {
        if (!expired) return HttpResponse.json(currentUserEnvelope(false));
        return HttpResponse.json(
          {
            type: "about:blank",
            title: "session_expired",
            status: 401,
            detail: "expired",
            request_id: UUID,
          },
          { status: 401, headers: { "Content-Type": "application/problem+json" } },
        );
      }),
    );
    renderProbe();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    // A background request discovers the expiry and the mutator announces it;
    // the provider must react without any refetch loop.
    expired = true;
    await act(async () => {
      window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
      await Promise.resolve();
    });
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("anonymous"));
    // Exactly one 401 happened: no refetch storm after the announcement.
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(screen.getByTestId("status")).toHaveTextContent("anonymous");
  });

  it("clears the session after logout", async () => {
    server.use(http.get("*/users/me", () => HttpResponse.json(currentUserEnvelope(false))));
    renderProbe();
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("authenticated"));
    await userEvent.click(screen.getByRole("button", { name: "logout" }));
    await waitFor(() => expect(screen.getByTestId("status")).toHaveTextContent("anonymous"));
  });
});
