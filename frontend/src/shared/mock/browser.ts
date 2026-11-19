import { setupWorker } from "msw/browser";
import { applyScenarioToWorker, baseHandlers } from "@/shared/mock/scenarios";
import { readStoredScenario } from "@/shared/mock/scenario-store";

/**
 * Browser MSW worker for mock-driven development and e2e (VITE_ENABLE_MOCK).
 * The initial scenario comes from localStorage (scenario-store); switching is
 * applied live via the yearning:mock-scenario event dispatched by the store.
 */
export async function startMockWorker(): Promise<void> {
  const worker = setupWorker(...baseHandlers());
  await worker.start({ onUnhandledRequest: "bypass", quiet: false });
  applyScenarioToWorker(worker, readStoredScenario());
  window.addEventListener("yearning:mock-scenario", () => {
    applyScenarioToWorker(worker, readStoredScenario());
  });
}
