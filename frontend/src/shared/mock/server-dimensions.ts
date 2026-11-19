/**
 * Server-side mock dimensions for the vite dev-server node transport
 * (owner ruling 2026-11-04, MSW multi-tab Option 1): the browser MSW worker
 * answers every open tab from its own in-memory world, and the shared
 * service worker dispatches each request to whichever client answers
 * first — tabs randomly swapped worlds (comments appearing/disappearing).
 * With the node transport the ONE world lives in the dev server process
 * and every tab/browser/device shares it, like a real backend.
 *
 * The localStorage-backed dimension readers (identity-store,
 * scenario-store, auth-scenario-store, shared/dev/mock-super) fall back to
 * this store when no localStorage exists (node). Switching dimensions in
 * node mode goes through the dev-only `/__mock/dimensions` endpoint (see
 * vite.config.ts); the next page load propagates the change to every tab.
 * In browser/worker/vitest modes this module is inert.
 *
 * State lives on globalThis (Symbol.for registry), not module scope: the
 * vite dev server re-evaluates app modules on restarts while the msw
 * interceptors and the /__mock/dimensions endpoint can hold different
 * module instances — a module-scoped store would split the state and a
 * dimension flip would silently stop reaching the handlers.
 */

export interface MockServerDimensions {
  identity: string;
  superMode: boolean;
  scenario: string;
  authBehavior: string;
}

const DIMENSIONS_KEY = Symbol.for("yearning.mock.server-dimensions");

interface GlobalWithDimensions {
  [DIMENSIONS_KEY]?: MockServerDimensions;
}

function store(): MockServerDimensions {
  const globalTarget = globalThis as typeof globalThis & GlobalWithDimensions;
  if (globalTarget[DIMENSIONS_KEY] === undefined) {
    globalTarget[DIMENSIONS_KEY] = {
      identity: "henry",
      // Mirrors the dev default in shared/dev/mock-super.ts: the dev session
      // is super unless explicitly opted out.
      superMode: true,
      scenario: "ready",
      authBehavior: "default",
    };
  }
  return globalTarget[DIMENSIONS_KEY];
}

export function getServerDimensions(): MockServerDimensions {
  return { ...store() };
}

export function setServerDimensions(
  patch: Partial<MockServerDimensions>,
): MockServerDimensions {
  const dimensions = store();
  if (patch.identity !== undefined) dimensions.identity = patch.identity;
  if (patch.superMode !== undefined) dimensions.superMode = patch.superMode;
  if (patch.scenario !== undefined) dimensions.scenario = patch.scenario;
  if (patch.authBehavior !== undefined) dimensions.authBehavior = patch.authBehavior;
  return getServerDimensions();
}
