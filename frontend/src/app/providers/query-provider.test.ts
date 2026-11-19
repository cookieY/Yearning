import { describe, expect, it } from "vitest";
import { BusinessError, TransportError } from "@/shared/api/mutator";
import { createQueryClient } from "@/app/providers/query-provider";

function businessError(): BusinessError {
  return new BusinessError({
    err_code: 1002,
    message: "not found",
    request_id: "33333333-3333-4333-8333-333333333333",
    retryable: false,
  });
}

function transportError(status: number): TransportError {
  return new TransportError({
    title: status === 0 ? "network_error" : "http_error",
    status,
    detail: "failure",
  });
}

describe("createQueryClient retry policy", () => {
  const retry = createQueryClient().getDefaultOptions().queries?.retry as (
    failureCount: number,
    error: Error,
  ) => boolean;

  it("never retries typed business errors regardless of retryability", () => {
    expect(retry(0, businessError())).toBe(false);
  });

  it("never retries 4xx transport failures", () => {
    expect(retry(0, transportError(401))).toBe(false);
    expect(retry(0, transportError(403))).toBe(false);
  });

  it("retries network and 5xx transport failures up to twice", () => {
    expect(retry(0, transportError(0))).toBe(true);
    expect(retry(1, transportError(503))).toBe(true);
    expect(retry(2, transportError(503))).toBe(false);
  });
});
