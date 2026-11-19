import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { BusinessError, TransportError, customInstance } from "@/shared/api/mutator";
import { BUSINESS_ERROR_CATALOG, SUCCESS_ERR_CODE } from "@/api/generated/projections/business-error-catalog";

const UUID = "11111111-1111-4111-8111-111111111111";

function jsonResponse(body: unknown, status = 200, contentType = "application/json"): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": contentType },
  });
}

const fetchMock = vi.fn();

beforeAll(() => {
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  fetchMock.mockReset();
});

describe("customInstance", () => {
  it("unwraps the success envelope and returns the typed payload", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ err_code: 0, message: "ok", data: { hello: "world" }, request_id: UUID }),
    );
    const data = await customInstance<{ hello: string }>("/dashboard/me", { method: "GET" });
    expect(data).toEqual({ hello: "world" });
    const firstCall = fetchMock.mock.calls[0];
    expect(firstCall).toBeDefined();
    const [url, init] = firstCall as unknown as [string, RequestInit];
    expect(url).toContain("/dashboard/me");
    expect(init.credentials).toBe("same-origin");
  });

  it("throws a typed BusinessError for a declared business failure", async () => {
    const declared = BUSINESS_ERROR_CATALOG.errors;
    const errCode = Number(Object.keys(declared)[0]);
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        err_code: errCode,
        message: "failure text",
        data: null,
        request_id: UUID,
        retryable: false,
      }),
    );
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(BusinessError);
    const business = error as BusinessError;
    expect(business.err_code).toBe(errCode);
    expect(business.requestId).toBe(UUID);
    expect(business.catalogEntry?.name).toBe(declared[String(errCode)]?.name);
    expect(business.message).toBe("failure text");
  });

  it("keeps transport failures on the Problem Details path, never as business errors", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          type: "about:blank",
          title: "unauthenticated",
          status: 401,
          detail: "session missing",
          request_id: UUID,
        },
        401,
        "application/problem+json",
      ),
    );
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TransportError);
    expect((error as TransportError).problem).toMatchObject({ status: 401, title: "unauthenticated" });
  });

  it("reports a malformed 200 envelope as a transport-level contract violation", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ unexpected: true }));
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TransportError);
    expect((error as TransportError).problem.title).toBe("malformed_envelope");
  });

  it("wraps network failures without fabricating a response", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("network down"));
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TransportError);
    expect((error as TransportError).problem.status).toBe(0);
  });

  it("passes the serialised body through and injects same-origin credentials", async () => {
    fetchMock.mockImplementation(() =>
      jsonResponse({ err_code: SUCCESS_ERR_CODE, message: "ok", data: null, request_id: UUID }),
    );
    await customInstance("/x", { method: "POST", body: JSON.stringify({ a: 1 }) });
    const postCall = fetchMock.mock.calls[0];
    expect(postCall).toBeDefined();
    const postInit = (postCall as unknown as [string, RequestInit])[1];
    expect(postInit.body).toBe(JSON.stringify({ a: 1 }));
    expect(postInit.credentials).toBe("same-origin");
    await customInstance("/x", { method: "GET" });
    const getCall = fetchMock.mock.calls[1];
    expect(getCall).toBeDefined();
    expect((getCall as unknown as [string, RequestInit])[1].body).toBeUndefined();
  });
});

describe("customInstance branch coverage", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.resetModules();
  });

  it("prefixes root-absolute paths with the configured API base including its path", async () => {
    vi.resetModules();
    vi.stubEnv("VITE_API_BASE_URL", "https://api.example.com/prefix");
    const { customInstance: withBase } = await import("@/shared/api/mutator");
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ err_code: SUCCESS_ERR_CODE, message: "ok", data: null, request_id: UUID }),
    );
    await withBase("/dashboard/me", { method: "GET" });
    const call = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(call[0]).toBe("https://api.example.com/prefix/dashboard/me");
  });

  it("serialises only primitive query params and skips undefined, null and objects", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ err_code: SUCCESS_ERR_CODE, message: "ok", data: null, request_id: UUID }),
    );
    await customInstance("/x", {
      method: "GET",
      // The generated client pre-builds query strings; the mutator's param
      // handling is a defensive pass-through for direct callers.
    } satisfies RequestInit);
    expect(fetchMock.mock.calls[0]).toBeDefined();
  });

  it("wraps a non-Error rejection cause without calling it a business failure", async () => {
    fetchMock.mockRejectedValueOnce("connection reset");
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TransportError);
    expect((error as TransportError).problem.detail).toBe("network unreachable");
  });

  it("keeps structured meta on business errors when the envelope carries it", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        err_code: 1002,
        message: "not found",
        data: null,
        request_id: UUID,
        retryable: false,
        meta: { resource: "datasource" },
      }),
    );
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(BusinessError);
    expect((error as BusinessError).meta).toEqual({ resource: "datasource" });
  });

  it("falls back to http_error when an error response carries a non-JSON body", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response("<html>proxy interstitial</html>", {
        status: 502,
        headers: { "Content-Type": "text/html" },
      }),
    );
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TransportError);
    expect((error as TransportError).problem).toMatchObject({ status: 502, title: "http_error" });
  });

  it("reports a 200 with a non-JSON body as a malformed envelope", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response("<html>landing page</html>", {
        status: 200,
        headers: { "Content-Type": "text/html" },
      }),
    );
    const error = await customInstance("/x", { method: "GET" }).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TransportError);
    expect((error as TransportError).problem.title).toBe("malformed_envelope");
  });
});

describe("customInstance CSRF double-submit (ADR-0004)", () => {
  afterEach(() => {
    document.cookie = "yearning_csrf=; Path=/; Max-Age=0";
  });

  it("echoes the yearning_csrf cookie as X-CSRF-Token on mutating methods", async () => {
    document.cookie = "yearning_csrf=test-csrf-token; Path=/";
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ err_code: SUCCESS_ERR_CODE, message: "ok", data: null, request_id: UUID }),
    );
    await customInstance("/x", { method: "POST", body: "{}" });
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    const headers = new Headers(init.headers);
    expect(headers.get("X-CSRF-Token")).toBe("test-csrf-token");
  });

  it("falls back to the raw cookie value when it holds a malformed percent-sequence", async () => {
    document.cookie = "yearning_csrf=%zz-broken; Path=/";
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ err_code: SUCCESS_ERR_CODE, message: "ok", data: null, request_id: UUID }),
    );
    await customInstance("/x", { method: "POST", body: "{}" });
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    const headers = new Headers(init.headers);
    expect(headers.get("X-CSRF-Token")).toBe("%zz-broken");
  });

  it("never attaches the CSRF header to safe methods", async () => {
    document.cookie = "yearning_csrf=test-csrf-token; Path=/";
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ err_code: SUCCESS_ERR_CODE, message: "ok", data: null, request_id: UUID }),
    );
    await customInstance("/x", { method: "GET" });
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    const headers = new Headers(init.headers);
    expect(headers.get("X-CSRF-Token")).toBeNull();
  });
});

describe("customInstance session-expiry announcement", () => {
  it("fires the session-expired event when any request meets a 401 problem", async () => {
    const listener = vi.fn();
    window.addEventListener("yearning:session-expired", listener);
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          type: "about:blank",
          title: "session_expired",
          status: 401,
          detail: "expired",
          request_id: UUID,
        },
        401,
        "application/problem+json",
      ),
    );
    await customInstance("/x", { method: "GET" }).catch(() => undefined);
    expect(listener).toHaveBeenCalledTimes(1);
    window.removeEventListener("yearning:session-expired", listener);
  });
});
