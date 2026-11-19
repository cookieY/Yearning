import { afterEach, describe, expect, it } from "vitest";
import { getMyDashboard } from "@/api/generated/client/dashboard/dashboard";
import { BusinessError, TransportError } from "@/shared/api/mutator";
import { applyScenarioToServer } from "@/shared/mock/scenarios";
import { DEMO_REQUEST_ID } from "@/shared/mock/scenarios";
import { server } from "@/test/msw/server";

// Machine gate: Mock supports ready, blocked, running and error scenarios
// (frontend implementation PRD §8) through the generated client, the shared
// mutator and the orval-generated MSW base handlers.
// Server lifecycle (listen/reset/close) is owned by src/test/setup.ts.
describe("mock scenarios through the generated client", () => {
  afterEach(() => {
    server.resetHandlers();
  });

  it("ready: unwraps the typed dashboard payload", async () => {
    applyScenarioToServer(server, "ready");
    const data = (await getMyDashboard()) as unknown as Record<string, unknown>;
    // The mutator returns envelope.data only; the payload is the MyDashboard
    // object, never the envelope itself.
    expect(typeof data.draft_count).toBe("number");
    expect(typeof data.refreshed_at).toBe("string");
    expect(data.err_code).toBeUndefined();
  });

  it("blocked: surfaces a declared business error with catalog identity", async () => {
    applyScenarioToServer(server, "blocked");
    const error = await getMyDashboard().catch((e: unknown) => e);
    expect(error).toBeInstanceOf(BusinessError);
    const business = error as BusinessError;
    expect(business.catalogEntry?.name).toBe("RESOURCE_NOT_FOUND");
    expect(business.retryable).toBe(false);
    expect(business.requestId).toBe(DEMO_REQUEST_ID);
  });

  it("running: delays before the typed payload (measurable latency)", async () => {
    applyScenarioToServer(server, "running");
    const startedAt = Date.now();
    const data = (await getMyDashboard()) as unknown as Record<string, unknown>;
    expect(Date.now() - startedAt).toBeGreaterThanOrEqual(1000);
    expect(typeof data.draft_count).toBe("number");
  });

  it("error: answers on the Problem Details path as a TransportError", async () => {
    applyScenarioToServer(server, "error");
    const error = await getMyDashboard().catch((e: unknown) => e);
    expect(error).toBeInstanceOf(TransportError);
    expect((error as TransportError).problem).toMatchObject({
      status: 503,
      title: "service_unavailable",
    });
  });
});
