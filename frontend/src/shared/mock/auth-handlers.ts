import { HttpResponse, http } from "msw";
import type { DefaultBodyType, HttpHandler } from "msw";
import { readStoredAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { currentIdentity, mockSuperMode } from "@/shared/mock/identity-store";

/**
 * Hand-written MSW handlers for the authentication endpoints, layered on the
 * orval base mocks (code-generation-policy mock_layer: vitest and Playwright
 * share these). They implement the same contract as the real backend:
 * HTTP 200 business envelopes for domain outcomes, Problem Details for the
 * 401 transport path, and session issuance through Set-Cookie headers —
 * `yearning_session` HttpOnly + `yearning_csrf` double-submit (ADR-0004).
 * The mock never puts a token in a URL or in web storage.
 */

const MOCK_REQUEST_ID = "44444444-4444-4444-8444-444444444444";
const SESSION_COOKIE = "yearning_session";
const CSRF_COOKIE = "yearning_csrf";
const MOCK_SESSION_TOKEN = "mock-session-token";
const MOCK_CSRF_TOKEN = "mock-csrf-token";

const NOW = "2026-08-28T08:00:00Z";

// Consistent with minLength of ChangeMyPasswordRequest.new_password in the contract.
const MIN_NEW_PASSWORD_LENGTH = 12;

function successEnvelope(data: DefaultBodyType) {
  return { err_code: 0, message: "ok", data, request_id: MOCK_REQUEST_ID };
}

function businessError(errCode: number, message: string) {
  return {
    err_code: errCode,
    message,
    data: null,
    request_id: MOCK_REQUEST_ID,
    retryable: false,
  };
}

function problem(status: number, title: string, detail: string): HttpResponse<DefaultBodyType> {
  return HttpResponse.json(
    { type: "about:blank", title, status, detail, request_id: MOCK_REQUEST_ID },
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}

function sessionCookieHeaders(): Headers {
  const headers = new Headers();
  headers.append(
    "Set-Cookie",
    `${SESSION_COOKIE}=${MOCK_SESSION_TOKEN}; Path=/; HttpOnly; SameSite=Lax`,
  );
  headers.append("Set-Cookie", `${CSRF_COOKIE}=${MOCK_CSRF_TOKEN}; Path=/; SameSite=Lax`);
  return headers;
}

function clearedSessionCookieHeaders(): Headers {
  const headers = new Headers();
  headers.append(
    "Set-Cookie",
    `${SESSION_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0`,
  );
  headers.append("Set-Cookie", `${CSRF_COOKIE}=; Path=/; SameSite=Lax; Max-Age=0`);
  return headers;
}

// Demo-mode in-memory session flag: public static demos (VITE_DEMO_MODE=true)
// start pre-authenticated so visitors enter the workspace immediately even when
// browsers ignore HttpOnly Set-Cookie headers synthesized by a Service Worker.
// Normal mock dev, Vitest and Playwright E2E leave VITE_DEMO_MODE unset.
let demoSessionActive = import.meta.env.VITE_DEMO_MODE === "true";

export function setDemoSessionActive(active: boolean): void {
  demoSessionActive = active;
}

// Reads the parsed request cookies — msw populates these from the Cookie
// header in Node and from the service-worker request in the browser, where
// the raw header is not always observable.
function hasSession(cookies: Record<string, string>): boolean {
  if (import.meta.env.VITE_DEMO_MODE === "true" && demoSessionActive) {
    return true;
  }
  return typeof cookies[SESSION_COOKIE] === "string" && cookies[SESSION_COOKIE] !== "";
}

function mockUser(canAccessAdmin: boolean) {
  // Identity dimension (§18.44): /users/me answers with the stored mock
  // identity — henry by default; e2e and manual testing flip it to act as
  // the frozen reviewer or the executor without a second login.
  const identity = currentIdentity();
  return {
    id: identity.id,
    username: identity.username,
    email: null,
    is_builtin_admin: canAccessAdmin,
    version: 1,
    created_at: NOW,
    updated_at: NOW,
    can_access_admin: canAccessAdmin,
    // §18.52: all mock identities are local accounts; the ldap/oidc branch of
    // the profile security tab is covered by tests overriding /users/me.
    auth_provider: "local",
  };
}

function userEnvelope(canAccessAdmin: boolean) {
  return HttpResponse.json(successEnvelope(mockUser(canAccessAdmin)));
}

function unauthenticated() {
  return problem(401, "session_expired", "no active session");
}

/** Which login form submissions succeed, and who the session belongs to. */
function loginOutcome(behavior: string): { ok: boolean; canAccessAdmin: boolean } {
  switch (behavior) {
    case "invalid_credentials":
    case "admin_locked":
      return { ok: false, canAccessAdmin: false };
    case "admin":
      return { ok: true, canAccessAdmin: true };
    default:
      return { ok: true, canAccessAdmin: false };
  }
}

function credentialProblem(behavior: string, endpoint: "local" | "ldap"): HttpResponse<DefaultBodyType> | null {
  if (behavior === "invalid_credentials") {
    return HttpResponse.json(businessError(1101, "invalid credentials"));
  }
  if (behavior === "admin_locked") {
    // 1102 ADMIN_PASSWORD_LOCKED is declared only for the local login
    // operation (auth_external_login = [1001, 1101, 1002]); the LDAP endpoint
    // reports the generic credential failure instead of an undeclared code.
    if (endpoint === "ldap") {
      return HttpResponse.json(businessError(1101, "invalid credentials"));
    }
    return HttpResponse.json(
      businessError(1102, "admin locked; run ./Yearning --reset-admin-password"),
    );
  }
  return null;
}

export function authMockHandlers(): HttpHandler[] {
  return [
    http.post("*/auth/login", async ({ request }) => {
      const behavior = readStoredAuthBehavior();
      const failure = credentialProblem(behavior, "local");
      if (failure !== null) return failure;
      const body = (await request.json().catch(() => null)) as { username?: string } | null;
      if (body === null || body.username === undefined || body.username === "") {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
      const { ok } = loginOutcome(behavior);
      if (!ok) {
        return HttpResponse.json(businessError(1101, "invalid credentials"));
      }
      if (import.meta.env.VITE_DEMO_MODE === "true") {
        demoSessionActive = true;
      }
      return HttpResponse.json(successEnvelope(null), { headers: sessionCookieHeaders() });
    }),

    http.post("*/auth/ldap/login", async ({ request }) => {
      const behavior = readStoredAuthBehavior();
      const failure = credentialProblem(behavior, "ldap");
      if (failure !== null) return failure;
      const body = (await request.json().catch(() => null)) as { username?: string } | null;
      if (body === null || body.username === undefined || body.username === "") {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
      const { ok } = loginOutcome(behavior);
      if (!ok) {
        return HttpResponse.json(businessError(1101, "invalid credentials"));
      }
      if (import.meta.env.VITE_DEMO_MODE === "true") {
        demoSessionActive = true;
      }
      return HttpResponse.json(successEnvelope(null), { headers: sessionCookieHeaders() });
    }),

    http.post("*/auth/logout", () => {
      if (import.meta.env.VITE_DEMO_MODE === "true") {
        demoSessionActive = false;
      }
      return HttpResponse.json(successEnvelope(null), { headers: clearedSessionCookieHeaders() });
    }),

    // Open registration. The mock answers with the contract's success face
    // (an active user with zero workflow permissions); the real serve stack
    // currently disables registration with 403 — a recorded Mock/Real
    // divergence of the FE-F12 contract suite, not a mock defect.
    http.post("*/auth/register", async ({ request }) => {
      const body = (await request.json().catch(() => null)) as {
        username?: unknown; display_name?: unknown; email?: unknown; password?: unknown;
      } | null;
      if (
        body === null || typeof body.username !== "string" || body.username === "" ||
        typeof body.display_name !== "string" || body.display_name === "" ||
        typeof body.password !== "string" || body.password.length < 12
      ) {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
      const now = new Date().toISOString().replace(/\.\d{3}Z$/, "Z");
      return HttpResponse.json(successEnvelope({
        id: crypto.randomUUID(), username: body.username, display_name: body.display_name,
        email: typeof body.email === "string" ? body.email : null,
        is_builtin_admin: false, version: 1, created_at: now, updated_at: now,
      }));
    }),

    // OIDC start/finish. The mock cannot redirect to a real IdP; an unknown
    // provider key answers with the same problem face the serve stack shows
    // (401 problem — the route mounts behind the session middleware with no
    // provider registered), so both modes record the problem shape.
    http.get("*/auth/oidc/:providerKey/start", ({ params }) => {
      if (String(params.providerKey) !== "corp-oidc") {
        return HttpResponse.json(
          { type: "about:blank", title: "Unauthorized", status: 401, detail: "no oidc provider", request_id: MOCK_REQUEST_ID },
          { status: 401 },
        );
      }
      return new HttpResponse(null, {
        status: 302,
        headers: { Location: "/mock/oidc/corp-oidc/start" },
      });
    }),

    http.get("*/auth/oidc/:providerKey/callback", ({ params }) => {
      if (String(params.providerKey) !== "corp-oidc") {
        return HttpResponse.json(
          { type: "about:blank", title: "Unauthorized", status: 401, detail: "no oidc provider", request_id: MOCK_REQUEST_ID },
          { status: 401 },
        );
      }
      if (import.meta.env.VITE_DEMO_MODE === "true") {
        demoSessionActive = true;
      }
      return HttpResponse.json(successEnvelope(null), { headers: sessionCookieHeaders() });
    }),

    // §18.52 self-service password change. Demo semantics: mock has no real password store (login accepts any credentials,
    // controlled by the auth-behavior dimension), so validate input per contract only — old_password non-empty,
    // new_password >= 12 otherwise 1001; on success clear the session cookie, mirroring the backend semantics where
    // "all sessions are invalidated after password change and the client returns to login". Real old-password check / 1109
    // is covered by backend contract tests.
    http.post("*/users/me/password", async ({ request, cookies }) => {
      const behavior = readStoredAuthBehavior();
      if (behavior === "expired" || !hasSession(cookies)) {
        return unauthenticated();
      }
      const body = (await request.json().catch(() => null)) as
        | { old_password?: unknown; new_password?: unknown }
        | null;
      if (
        body === null ||
        typeof body.old_password !== "string" ||
        body.old_password === "" ||
        typeof body.new_password !== "string" ||
        body.new_password.length < MIN_NEW_PASSWORD_LENGTH
      ) {
        return HttpResponse.json(businessError(1001, "validation failed"));
      }
      if (import.meta.env.VITE_DEMO_MODE === "true") {
        demoSessionActive = false;
      }
      return HttpResponse.json(successEnvelope(null), { headers: clearedSessionCookieHeaders() });
    }),

    http.get("*/auth/providers", () =>
      HttpResponse.json(
        successEnvelope({
          local: true,
          ldap: true,
          oidc: [
            {
              key: "corp-oidc",
              label: "企业 OIDC",
              start_url: "/mock/oidc/corp-oidc/start",
            },
          ],
        }),
      ),
    ),

    http.get("*/users/me", ({ cookies }) => {
      const behavior = readStoredAuthBehavior();
      if (behavior === "expired" || !hasSession(cookies)) {
        return unauthenticated();
      }
      // Super mode (owner 2026-11-03): the dev-browser session carries the
      // admin capability on the default face too, without flipping the
      // auth-behavior dimension (the expired face already returned above).
      const isAdmin = behavior === "admin" || mockSuperMode();
      return userEnvelope(isAdmin);
    }),

    // NOTE: GET /users/me/flows is owned by the stateful review fixture
    // (shared/mock/review-fixture.ts), which derives the zero-permission
    // empty page from the auth behavior so FE-F3's waiting state and FE-F4's
    // granted workspace share one source of truth.
  ];
}
