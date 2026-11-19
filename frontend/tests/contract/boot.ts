/**
 * Per-file boot for the contract suite: start MSW with the shared "ready"
 * scenario in mock mode, then install the capturing transport in front of
 * whatever fetch is now global (MSW's interceptor or Node's real fetch).
 */
import { installTransport, mode } from "./harness";

export async function bootContract(): Promise<void> {
  if (mode === "mock") {
    const { server } = await import("@/test/msw/server");
    const { applyScenarioToServer } = await import("@/shared/mock/scenarios");
    server.listen({ onUnhandledRequest: "error" });
    applyScenarioToServer(server, "ready");
  }
  installTransport();
}
