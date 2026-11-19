import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { setupServer } from "msw/node";
import { authMockHandlers } from "@/shared/mock/auth-handlers";
import { setMockAuthBehavior } from "@/shared/mock/auth-scenario-store";

/**
 * The auth mock layer must speak the same contract as the real backend:
 * business envelopes over HTTP 200, Problem Details on the 401 path, and
 * session issuance exclusively through Set-Cookie headers — never through a
 * URL or web storage.
 */

const server = setupServer(...authMockHandlers());

beforeAll(() => {
  server.listen({ onUnhandledRequest: "bypass" });
});
afterEach(() => {
  server.resetHandlers();
});
afterAll(() => {
  server.close();
});

beforeEach(() => {
  setMockAuthBehavior("default");
});

async function post(path: string, body?: unknown, session?: string): Promise<Response> {
  return fetch(path, {
    method: "POST",
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...(session === undefined ? {} : { cookie: `yearning_session=${session}` }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

describe("authMockHandlers", () => {
  it("issues session and CSRF cookies on a successful login", async () => {
    const response = await post("https://yearning.test/api/v4/auth/login", {
      username: "henry",
      password: "fixture-pw",
    });
    expect(response.status).toBe(200);
    const body = (await response.json()) as { err_code: number; data: null };
    expect(body.err_code).toBe(0);
    const cookies = response.headers.getSetCookie();
    const joined = cookies.join("; ");
    expect(cookies).toHaveLength(2);
    expect(joined).toContain("yearning_session=");
    expect(joined).toContain("HttpOnly");
    expect(joined).toContain("yearning_csrf=");
  });

  it("answers 1101 for invalid credentials under the matching behavior", async () => {
    setMockAuthBehavior("invalid_credentials");
    const response = await post("https://yearning.test/api/v4/auth/login", {
      username: "henry",
      password: "wrong",
    });
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(1101);
  });

  it("answers 1102 with the reset-command message for the locked admin", async () => {
    setMockAuthBehavior("admin_locked");
    const response = await post("https://yearning.test/api/v4/auth/login", {
      username: "admin",
      password: "wrong",
    });
    const body = (await response.json()) as { err_code: number; message: string };
    expect(body.err_code).toBe(1102);
    expect(body.message).toContain("--reset-admin-password");
  });

  it("answers 1001 for a body without a username", async () => {
    const response = await post("https://yearning.test/api/v4/auth/login", { password: "x" });
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(1001);
  });

  it("returns the current user only while the session cookie is present", async () => {
    // Node's fetch keeps a cookie jar, so the anonymous leg is observed after
    // a real logout cleared the jar — the same way a browser behaves.
    await post("https://yearning.test/api/v4/auth/login", {
      username: "henry",
      password: "fixture-pw",
    });
    const withCookie = await fetch("https://yearning.test/api/v4/users/me");
    const okBody = (await withCookie.json()) as { err_code: number; data: { can_access_admin: boolean } };
    expect(okBody.err_code).toBe(0);
    expect(okBody.data.can_access_admin).toBe(false);

    await post("https://yearning.test/api/v4/auth/logout");
    const afterLogout = await fetch("https://yearning.test/api/v4/users/me");
    expect(afterLogout.status).toBe(401);
    expect(afterLogout.headers.get("content-type")).toContain("application/problem+json");
  });

  it("serves the admin capability only under the admin behavior", async () => {
    setMockAuthBehavior("admin");
    const response = await fetch("https://yearning.test/api/v4/users/me", {
      headers: { cookie: "yearning_session=mock-session-token" },
    });
    const body = (await response.json()) as { data: { can_access_admin: boolean } };
    expect(body.data.can_access_admin).toBe(true);
  });

  it("answers 401 on /users/me under the expired behavior even with a cookie", async () => {
    setMockAuthBehavior("expired");
    const response = await fetch("https://yearning.test/api/v4/users/me", {
      headers: { cookie: "yearning_session=mock-session-token" },
    });
    expect(response.status).toBe(401);
  });

  it("clears both cookies on logout", async () => {
    const response = await post("https://yearning.test/api/v4/auth/logout");
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(0);
    const cookies = response.headers.getSetCookie().join("; ");
    expect(cookies).toContain("yearning_session=;");
    expect(cookies).toContain("Max-Age=0");
  });

  it("authenticates the LDAP endpoint with the same cookie semantics", async () => {
    const response = await post("https://yearning.test/api/v4/auth/ldap/login", {
      username: "henry",
      password: "fixture-pw",
    });
    expect(response.status).toBe(200);
    const cookies = response.headers.getSetCookie().join("; ");
    expect(cookies).toContain("yearning_session=");
  });

  it("never emits the local-only 1102 on the LDAP endpoint (undeclared there)", async () => {
    setMockAuthBehavior("admin_locked");
    const response = await post("https://yearning.test/api/v4/auth/ldap/login", {
      username: "henry",
      password: "wrong",
    });
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(1101);
  });

  it("maps the invalid_credentials behavior onto the LDAP endpoint as 1101", async () => {
    setMockAuthBehavior("invalid_credentials");
    const response = await post("https://yearning.test/api/v4/auth/ldap/login", {
      username: "henry",
      password: "wrong",
    });
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(1101);
  });

  it("answers 1001 for an LDAP body without a username", async () => {
    const response = await post("https://yearning.test/api/v4/auth/ldap/login", {
      password: "x",
    });
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(1001);
  });

  it("changes the password for a valid body and clears the session cookies (§18.52)", async () => {
    const response = await post("https://yearning.test/api/v4/users/me/password", {
      old_password: "fixture-pw",
      new_password: "new-fixture-pw-12",
    }, "mock-session-token");
    expect(response.status).toBe(200);
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(0);
    // Mirrors the backend: every session is revoked on change.
    const cookies = response.headers.getSetCookie().join("; ");
    expect(cookies).toContain("yearning_session=;");
    expect(cookies).toContain("Max-Age=0");
  });

  it("answers 1001 on password change for a short new password", async () => {
    const response = await post("https://yearning.test/api/v4/users/me/password", {
      old_password: "fixture-pw",
      new_password: "short",
    }, "mock-session-token");
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(1001);
  });

  it("answers 1001 on password change for a missing old password", async () => {
    const response = await post("https://yearning.test/api/v4/users/me/password", {
      new_password: "new-fixture-pw-12",
    }, "mock-session-token");
    const body = (await response.json()) as { err_code: number };
    expect(body.err_code).toBe(1001);
  });

  it("answers 401 on password change without a session cookie", async () => {
    const response = await post("https://yearning.test/api/v4/users/me/password", {
      old_password: "fixture-pw",
      new_password: "new-fixture-pw-12",
    });
    expect(response.status).toBe(401);
  });

  // NOTE (review L5, 2026-11-04): these register cases exercise the MOCK's
  // success face. The real serve stack disables open registration with 403
  // — a recorded Mock/Real divergence (FE-F12 divergence list, see the
  // comment on the register handler). They pin mock behavior for UI flows,
  // not backend contract conformance.
  it("registers an open account with the contract's zero-permission face", async () => {
    const response = await post("https://yearning.test/api/v4/auth/register", {
      username: "newcomer",
      display_name: "New Colleague",
      email: "newcomer@yearning.io",
      password: "new-fixture-pw-12",
    });
    const body = (await response.json()) as {
      err_code: number;
      data: { username: string; display_name: string; email: string | null; is_builtin_admin: boolean };
    };
    expect(body.err_code).toBe(0);
    expect(body.data.username).toBe("newcomer");
    expect(body.data.display_name).toBe("New Colleague");
    expect(body.data.email).toBe("newcomer@yearning.io");
    expect(body.data.is_builtin_admin).toBe(false);
  });

  it("stores a null email when the optional field is not a string", async () => {
    const response = await post("https://yearning.test/api/v4/auth/register", {
      username: "newcomer",
      display_name: "New Colleague",
      email: 42,
      password: "new-fixture-pw-12",
    });
    const body = (await response.json()) as { data: { email: string | null } };
    expect(body.data.email).toBeNull();
  });

  it("rejects each malformed registration body with 1001", async () => {
    const cases: unknown[] = [
      { display_name: "x", password: "new-fixture-pw-12" },
      { username: 7, display_name: "x", password: "new-fixture-pw-12" },
      { username: "", display_name: "x", password: "new-fixture-pw-12" },
      { username: "newcomer", display_name: "", password: "new-fixture-pw-12" },
      { username: "newcomer", display_name: "x", password: "short" },
    ];
    for (const payload of cases) {
      const response = await post("https://yearning.test/api/v4/auth/register", payload);
      const body = (await response.json()) as { err_code: number };
      expect(body.err_code).toBe(1001);
    }
    // A non-JSON body is the same validation face.
    const unparseable = await fetch("https://yearning.test/api/v4/auth/register", {
      method: "POST",
      headers: { "Content-Type": "text/plain" },
      body: "not json",
    });
    expect(((await unparseable.json()) as { err_code: number }).err_code).toBe(1001);
  });

  it("redirects the registered OIDC provider and 401s unknown keys on start", async () => {
    const start = await fetch("https://yearning.test/api/v4/auth/oidc/corp-oidc/start", {
      redirect: "manual",
    });
    expect(start.status).toBe(302);
    expect(start.headers.get("location")).toBe("/mock/oidc/corp-oidc/start");

    const unknown = await fetch("https://yearning.test/api/v4/auth/oidc/other-idp/start");
    expect(unknown.status).toBe(401);
    const problem = (await unknown.json()) as { title: string; status: number };
    expect(problem.title).toBe("Unauthorized");
    expect(problem.status).toBe(401);
  });

  it("finishes the OIDC callback with a session cookie and 401s unknown keys", async () => {
    const finish = await fetch("https://yearning.test/api/v4/auth/oidc/corp-oidc/callback");
    const body = (await finish.json()) as { err_code: number };
    expect(body.err_code).toBe(0);
    expect(finish.headers.getSetCookie().join("; ")).toContain("yearning_session=");

    const unknown = await fetch("https://yearning.test/api/v4/auth/oidc/other-idp/callback");
    expect(unknown.status).toBe(401);
  });
});
