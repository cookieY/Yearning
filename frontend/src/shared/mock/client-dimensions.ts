import { IDENTITY_STORAGE_KEY, isMockIdentity } from "@/shared/mock/identity-store";
import { SCENARIO_STORAGE_KEY } from "@/shared/mock/scenario-store";
import { AUTH_BEHAVIOR_STORAGE_KEY } from "@/shared/mock/auth-scenario-store";

/**
 * Client side of the node mock transport (owner ruling 2026-11-04, MSW multi-tab
 * Option 1): unregister the legacy MSW service worker so it cannot answer
 * requests before the dev-server middleware, then one-way boot sync of the
 * shared server dimensions into this tab's localStorage.
 *
 * Switching dimensions in node mode goes through the dev-only
 * `/__mock/dimensions` endpoint (GET/POST, see vite.config.ts); the next page
 * load propagates it to every tab. Failures (endpoint absent — worker
 * transport or plain dev without the mock) leave the tab's own state intact.
 */

const DIMENSION_ENDPOINT = "/__mock/dimensions";

export async function unregisterMockWorker(): Promise<void> {
  if (!("serviceWorker" in navigator)) return;
  const registrations = await navigator.serviceWorker.getRegistrations();
  await Promise.all(registrations.map((registration) => registration.unregister()));
}

export async function syncDimensionsFromServer(): Promise<void> {
  try {
    const response = await fetch(DIMENSION_ENDPOINT, { credentials: "same-origin" });
    if (!response.ok) return;
    const dimensions = (await response.json()) as {
      identity?: unknown;
      scenario?: unknown;
      authBehavior?: unknown;
    };
    if (isMockIdentity(dimensions.identity)) {
      localStorage.setItem(IDENTITY_STORAGE_KEY, dimensions.identity);
    }
    if (typeof dimensions.scenario === "string") {
      localStorage.setItem(SCENARIO_STORAGE_KEY, dimensions.scenario);
    }
    if (typeof dimensions.authBehavior === "string") {
      localStorage.setItem(AUTH_BEHAVIOR_STORAGE_KEY, dimensions.authBehavior);
    }
  } catch {
    // Endpoint absent — keep the tab's own dimension state.
  }
}
