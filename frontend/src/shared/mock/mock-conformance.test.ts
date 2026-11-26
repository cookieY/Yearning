import { HttpResponse, http } from "msw";
import { afterEach, describe, expect, it } from "vitest";
import { apiRoute, matchOpenApiRoute } from "@/shared/mock/api-route";
import { setMockAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { setMockIdentity } from "@/shared/mock/identity-store";
import { OPENAPI_ROUTES } from "@/shared/mock/openapi-routes.generated";
import { validateListOrdersParams } from "@/shared/mock/request-validation";
import { resetReviewFixture } from "@/shared/mock/review-fixture";
import { baseHandlers } from "@/shared/mock/scenarios";
import { withZodValidation } from "@/shared/mock/zod-guard";

afterEach(() => {
  setMockAuthBehavior("default");
  setMockIdentity("henry");
  resetReviewFixture();
});

describe("mock conformance & contract fidelity (Stage 3.2)", () => {
  it("enforces exact /api/v4 prefix and rejects wildcards (DEF-03)", () => {
    const ordersRe = apiRoute("/change-orders");
    expect(ordersRe.test("https://yearning.test/api/v4/change-orders")).toBe(true);
    expect(ordersRe.test("http://127.0.0.1:4173/api/v4/change-orders")).toBe(true);
    expect(ordersRe.test("https://yearning.test/change-orders")).toBe(false);
    expect(ordersRe.test("https://yearning.test/prefix/api/v4/change-orders")).toBe(false);
    expect(ordersRe.test("https://yearning.test/api/v4/change-orders/extra")).toBe(false);

    expect(() => apiRoute("*/change-orders")).toThrow();
    expect(() => apiRoute("/api/v4/change-orders")).toThrow();
    expect(() => apiRoute("/not-in-openapi-contract")).toThrow();

    expect(matchOpenApiRoute("GET", "https://yearning.test/api/v4/change-orders")?.operationId).toBe(
      "listChangeOrders",
    );
    expect(matchOpenApiRoute("GET", "https://yearning.test/change-orders")).toBeNull();
  });

  it("maps every handler registered by baseHandlers() to an OpenAPI route", () => {
    const sampleBase = "https://yearning.test/api/v4";
    const routes = OPENAPI_ROUTES.map((route) => ({
      method: route.method,
      sampleUrl: `${sampleBase}${route.path.replace(/:[^/]+/g, "11111111-1111-4111-8111-111111111111")}`,
    }));
    expect(routes.length).toBe(180);

    const handlers = baseHandlers();
    expect(handlers.length).toBeGreaterThan(100);
    for (const handler of handlers) {
      const pathPattern = handler.info.path;
      expect(pathPattern).toBeInstanceOf(RegExp);
      const method = String(handler.info.method).toUpperCase();
      const matchedAny = routes.some(
        (candidate) =>
          candidate.method === method &&
          pathPattern instanceof RegExp &&
          pathPattern.test(candidate.sampleUrl),
      );
      expect(matchedAny, `No OpenAPI route matches ${method} ${String(pathPattern)}`).toBe(true);
    }
  });

  it("validates GET /api/v4/change-orders query params symmetrically with Go handlers_b9 (DEF-04/DEF-05)", async () => {
    const longText = "x".repeat(129);
    const badUrls = [
      `https://yearning.test/api/v4/change-orders?q=${longText}`,
      `https://yearning.test/api/v4/change-orders?datasource=${longText}`,
      "https://yearning.test/api/v4/change-orders?submitted_from=2026-02-30",
      "https://yearning.test/api/v4/change-orders?submitted_to=invalid-date",
      "https://yearning.test/api/v4/change-orders?state=nonexistent_state",
      "https://yearning.test/api/v4/change-orders?limit=0",
      "https://yearning.test/api/v4/change-orders?limit=201",
    ];

    for (const rawUrl of badUrls) {
      expect(validateListOrdersParams(new URL(rawUrl))?.err_code).toBe(1001);
      const res = await fetch(rawUrl);
      const json = (await res.json()) as { err_code: number };
      expect(json.err_code).toBe(1001);
    }

    const okUrl =
      "https://yearning.test/api/v4/change-orders?limit=200&state=submitted&submitted_from=2026-01-01&submitted_to=2026-12-31&q=ok";
    expect(validateListOrdersParams(new URL(okUrl))).toBeNull();
    const okRes = await fetch(okUrl);
    const okJson = (await okRes.json()) as { err_code: number; data: { items: unknown[] } };
    expect(okJson.err_code).toBe(0);
    expect(Array.isArray(okJson.data.items)).toBe(true);
  });

  it("enforces 401 on expired session and 403 on non-admin routes", async () => {
    setMockAuthBehavior("expired");
    const meExpired = await fetch("https://yearning.test/api/v4/users/me");
    expect(meExpired.status).toBe(401);
    const adminExpired = await fetch("https://yearning.test/api/v4/admin/users");
    expect(adminExpired.status).toBe(401);

    setMockAuthBehavior("default");
    setMockIdentity("henry");
    const adminForbidden = await fetch("https://yearning.test/api/v4/admin/users");
    expect(adminForbidden.status).toBe(403);
  });

  it("validates business errors with meta: null / empty arrays cleanly (DEF-16)", async () => {
    const [okWrapped] = withZodValidation([
      http.get(apiRoute("/change-orders"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: [], page: { next_cursor: null, has_more: false } },
          request_id: "11111111-1111-4111-8111-111111111111",
        }),
      ),
    ]);
    const okResolver = (
      okWrapped as unknown as { resolver: (info: { request: Request }) => Promise<Response> }
    ).resolver;
    const validRes = await okResolver({
      request: new Request("https://yearning.test/api/v4/change-orders"),
    });
    expect(validRes.status).toBe(200);
  });

  it("throws immediately when withZodValidation observes a schema-violating payload (negative control)", async () => {
    const [badWrapped] = withZodValidation([
      http.get(apiRoute("/users/me"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { broken: true },
          request_id: "not-a-uuid",
        }),
      ),
    ]);
    const badResolver = (
      badWrapped as unknown as { resolver: (info: { request: Request }) => Promise<Response> }
    ).resolver;
    await expect(
      badResolver({ request: new Request("https://yearning.test/api/v4/users/me") }),
    ).rejects.toThrow(/\[zod-guard\].*GetCurrentUserResponse/);
  });

  it("rejects >=400 JSON error responses without application/problem+json or malformed RFC 7807 fields", async () => {
    const [plainJsonError, malformedProblemError] = withZodValidation([
      http.get(apiRoute("/users/me"), () =>
        HttpResponse.json(
          { type: "about:blank", title: "Unauthorized", status: 401, detail: "expired", request_id: "11111111-1111-4111-8111-111111111111" },
          { status: 401 },
        ),
      ),
      http.get(apiRoute("/admin/users"), () =>
        HttpResponse.json(
          { title: "Forbidden", status: 403 },
          { status: 403, headers: { "Content-Type": "application/problem+json" } },
        ),
      ),
    ]);
    const plainResolver = (
      plainJsonError as unknown as { resolver: (info: { request: Request }) => Promise<Response> }
    ).resolver;
    await expect(
      plainResolver({ request: new Request("https://yearning.test/api/v4/users/me") }),
    ).rejects.toThrow(/\[zod-guard\].*application\/problem\+json/);

    const malformedResolver = (
      malformedProblemError as unknown as { resolver: (info: { request: Request }) => Promise<Response> }
    ).resolver;
    await expect(
      malformedResolver({ request: new Request("https://yearning.test/api/v4/admin/users") }),
    ).rejects.toThrow(/\[zod-guard\].*Problem Details/);
  });

  it("enforces strict PEM base64, datasource format/probe failure, and review definition parity (MOCK-LENIENT retired)", async () => {
    setMockAuthBehavior("admin");
    const headers = { "Content-Type": "application/json", "X-CSRF-Token": "mock-csrf-token" };
    // MOCK-LENIENT-01: non-base64 PEM body rejected with 1001.
    const badPemRes = await fetch("https://yearning.test/api/v4/admin/datasources", {
      method: "POST",
      headers,
      body: JSON.stringify({
        name: "bad-pem-ds",
        engine: "postgresql",
        compatibility_mode: "postgresql",
        deployment_kind: "native",
        host: "10.0.0.50",
        port: 5432,
        enabled: true,
        credentials: [{ purpose: "review", username: "yr", password: { value: "pw-123" } }],
        tls: { ca_pem: { value: "-----BEGIN CERTIFICATE-----\nnot!base64!\n-----END CERTIFICATE-----\n" } },
      }),
    });
    expect(((await badPemRes.json()) as { err_code: number }).err_code).toBe(1001);

    // MOCK-LENIENT-02: invalid host / fractional port rejected 1001, and port >= 59000 loopback fails connection test.
    const badHostRes = await fetch("https://yearning.test/api/v4/admin/datasources", {
      method: "POST",
      headers,
      body: JSON.stringify({
        name: "bad-host-ds",
        engine: "postgresql",
        compatibility_mode: "postgresql",
        deployment_kind: "native",
        host: "bad host/path",
        port: 5432,
        enabled: true,
        credentials: [{ purpose: "review", username: "yr", password: { value: "pw-123" } }],
      }),
    });
    expect(((await badHostRes.json()) as { err_code: number }).err_code).toBe(1001);

    // MOCK-LENIENT-03: unsupported keys or whitespace-only knowledge_text rejected 1001.
    const badRuleRes = await fetch("https://yearning.test/api/v4/admin/prompt-tools", {
      method: "POST",
      headers,
      body: JSON.stringify({
        name: "bad-def-tool",
        state: "draft",
        engine: "all",
        definition: { knowledge_text: "   ", max_severity: "low", version: 1, unexpected_key: true },
      }),
    });
    expect(((await badRuleRes.json()) as { err_code: number }).err_code).toBe(1001);
  });
});
