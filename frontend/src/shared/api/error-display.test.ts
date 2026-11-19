import { describe, expect, it } from "vitest";
import {
  businessErrCodeByName,
  businessErrorName,
  describeBusinessError,
  describeError,
  describeTransportError,
} from "@/shared/api/error-display";
import { BusinessError, TransportError } from "@/shared/api/mutator";

const REQUEST_ID = "11111111-1111-4111-8111-111111111111";

function businessError(errCode: number): BusinessError {
  return new BusinessError({
    err_code: errCode,
    message: "server text",
    request_id: REQUEST_ID,
    retryable: false,
  });
}

describe("describeBusinessError", () => {
  it("maps a declared err_code to its catalog i18n key", () => {
    // login declares INVALID_CREDENTIALS in its generated error profile
    const display = describeBusinessError(businessError(1101), "login");
    expect(display.messageKey).toBe("errors.INVALID_CREDENTIALS");
    expect(display.requestId).toBe(REQUEST_ID);
    expect(display.contractViolation).toBe(false);
  });

  it("sends undeclared err_code outcomes to the safe generic path with the request_id", () => {
    // 2011 AI_PROVIDER_FAILED is real but never declared for login
    const display = describeBusinessError(businessError(2011), "login");
    expect(display.messageKey).toBe("errors.generic.safe");
    expect(display.requestId).toBe(REQUEST_ID);
    expect(display.contractViolation).toBe(true);
  });

  it("treats an unknown err_code as a contract violation, never a success signal", () => {
    const display = describeBusinessError(businessError(9999), "getCurrentUser");
    expect(display.messageKey).toBe("errors.generic.safe");
    expect(display.contractViolation).toBe(true);
  });

  it("falls back to the generic business key for a declared-but-uncatalogued code", () => {
    // getCurrentUser declares no_business_error → every err_code is a violation
    const display = describeBusinessError(businessError(1001), "getCurrentUser");
    expect(display.contractViolation).toBe(true);
  });
});

describe("describeTransportError", () => {
  it("maps each transport status class to its i18n key and keeps the request_id", () => {
    const problem = {
      type: "about:blank",
      title: "session_expired",
      status: 401,
      detail: "expired",
      request_id: REQUEST_ID,
    };
    const display = describeTransportError(new TransportError(problem));
    expect(display.messageKey).toBe("errors.transport.unauthenticated");
    expect(display.requestId).toBe(REQUEST_ID);
  });

  it("maps forbidden and rate-limited statuses to their own keys", () => {
    const forbidden = describeTransportError(
      new TransportError({ title: "permission_denied", status: 403, detail: "no" }),
    );
    expect(forbidden.messageKey).toBe("errors.transport.forbidden");
    const limited = describeTransportError(
      new TransportError({ title: "rate_limited", status: 429, detail: "slow down" }),
    );
    expect(limited.messageKey).toBe("errors.transport.rateLimited");
  });

  it("treats an operation outside every profile as fully undeclared", () => {
    const display = describeBusinessError(businessError(1001), "noSuchOperation");
    expect(display.messageKey).toBe("errors.generic.safe");
    expect(display.contractViolation).toBe(true);
  });

  it("reports network failures without fabricating a request id", () => {
    const display = describeTransportError(
      new TransportError({ title: "network_error", status: 0, detail: "down" }),
    );
    expect(display.messageKey).toBe("errors.transport.network");
    expect(display.requestId).toBeNull();
  });
});

describe("describeError", () => {
  it("routes unknown throwables to the safe generic message", () => {
    const display = describeError(new Error("boom"), "login");
    expect(display.messageKey).toBe("errors.generic.safe");
    expect(display.requestId).toBeNull();
  });
});

describe("catalog helpers", () => {
  it("resolves stable names and codes without numeric literals in components", () => {
    expect(businessErrorName(1101)).toBe("INVALID_CREDENTIALS");
    expect(businessErrCodeByName("ADMIN_PASSWORD_LOCKED")).toBe(1102);
    expect(businessErrCodeByName("NOT_A_REAL_ERROR")).toBeNull();
  });
});

// §18.75: probe failure (2011) and unique conflict (1005) on Provider write surfaces must show specific
// copy rather than the safe-generic fallback — regression lock from real integration where DeepSeek probe errors were masked.
describe("provider surfaces show specific messages (§18.75)", () => {
  it("maps 2011 on createAiProvider to the AI provider failure key", () => {
    const display = describeBusinessError(
      new BusinessError({ err_code: 2011, message: "AI_PROVIDER_FAILED", request_id: "r", retryable: true }),
      "createAiProvider",
    );
    expect(display.contractViolation).toBe(false);
    expect(display.messageKey).toBe("errors.AI_PROVIDER_FAILED");
  });

  it("maps 1005 on replaceAiProvider to the already-exists key", () => {
    const display = describeBusinessError(
      new BusinessError({ err_code: 1005, message: "RESOURCE_ALREADY_EXISTS", request_id: "r", retryable: false }),
      "replaceAiProvider",
    );
    expect(display.contractViolation).toBe(false);
    expect(display.messageKey).toBe("errors.RESOURCE_ALREADY_EXISTS");
  });
});

// §18.75-A addition (owner ruling: error messages must be specific): upstream refusal reason in meta.detail
// is passed through to ErrorDisplay.detail; non-string values are ignored.
describe("provider detail passthrough (§18.75-A)", () => {
  it("surfaces meta.detail on a declared 2011", () => {
    const display = describeBusinessError(
      new BusinessError({
        err_code: 2011,
        message: "AI_PROVIDER_FAILED",
        request_id: "r",
        retryable: true,
        meta: { detail: "provider: status 400: Model Not Exist" },
      }),
      "createAiProvider",
    );
    expect(display.detail).toBe("provider: status 400: Model Not Exist");
  });

  it("ignores non-string detail and stays null without meta", () => {
    const plain = describeBusinessError(
      new BusinessError({ err_code: 2011, message: "x", request_id: "r", retryable: true }),
      "createAiProvider",
    );
    expect(plain.detail).toBeNull();
    const weird = describeBusinessError(
      new BusinessError({
        err_code: 2011, message: "x", request_id: "r", retryable: true, meta: { detail: 42 },
      }),
      "createAiProvider",
    );
    expect(weird.detail).toBeNull();
  });
});

// RCP-20261102-S008 addition: field-level refusal reasons for VALIDATION_FAILED are passed through meta.fields;
// non-conforming shapes are dropped per key so the envelope never renders untyped data; contract violation paths
// (undeclared err_code) never expose fields.
describe("validation fields passthrough (RCP-20261102-S008)", () => {
  it("surfaces meta.fields on a declared 1001", () => {
    const display = describeBusinessError(
      new BusinessError({
        err_code: 1001,
        message: "VALIDATION_FAILED",
        request_id: "r",
        retryable: false,
        meta: {
          detail: "timezone must be a valid IANA name",
          fields: { timezone: ["must be a valid IANA name"], input_price: ["must be a decimal"] },
        },
      }),
      "createAiProviderRateVersion",
    );
    expect(display.fields).toEqual({
      timezone: ["must be a valid IANA name"],
      input_price: ["must be a decimal"],
    });
  });

  it("drops non-conforming field entries key-by-key", () => {
    const display = describeBusinessError(
      new BusinessError({
        err_code: 1001,
        message: "VALIDATION_FAILED",
        request_id: "r",
        retryable: false,
        meta: {
          fields: {
            timezone: "must be a valid IANA name", // string, not array — dropped
            input_price: ["ok", 42, ""], // non-string/empty entries dropped, valid one kept
            cached_read_price: [], // empty array — key dropped
            "": ["empty key"], // empty key — dropped
          },
        },
      }),
      "createAiProviderRateVersion",
    );
    expect(display.fields).toEqual({ input_price: ["ok"] });
  });

  it("stays an empty fields map without meta and hides fields on contract violations", () => {
    const plain = describeBusinessError(
      new BusinessError({ err_code: 1001, message: "x", request_id: "r", retryable: false }),
      "createAiProviderRateVersion",
    );
    expect(plain.fields).toEqual({});
    // 1001 on an operation that never declares it: safe generic path only
    // (getCurrentUser declares no_business_error — existing precedent above).
    const undeclared = describeBusinessError(
      new BusinessError({
        err_code: 1001, message: "x", request_id: "r", retryable: false,
        meta: { fields: { any: ["never shown"] } },
      }),
      "getCurrentUser",
    );
    expect(undeclared.contractViolation).toBe(true);
    expect(undeclared.fields).toEqual({});
  });
});
