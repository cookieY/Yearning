import { create } from "zustand";

/**
 * Development-only role-preview state (D18).
 *
 * Purely in-memory (no localStorage or sessionStorage) so perspective overrides
 * never leak to browser storage and only attenuate privileges on top of the
 * active authenticated identity. Production builds strip the UI loader and
 * request header injection via `import.meta.env.DEV`, and the Go backend rejects
 * or ignores the header outside development mode.
 */
export type DevViewAsRole = "none" | "approver" | "executor" | "user";

export const DEV_VIEW_AS_HEADER = "X-Yearning-Dev-View-As";

const VALID_PRESETS: ReadonlySet<DevViewAsRole> = new Set([
  "none",
  "approver",
  "executor",
  "user",
]);

const GLOBAL_KEY = "__YEARNING_DEV_VIEW_AS__";

function publishGlobal(role: DevViewAsRole): void {
  const target = globalThis as Record<string, unknown>;
  if (role === "none") {
    Reflect.deleteProperty(target, GLOBAL_KEY);
  } else {
    target[GLOBAL_KEY] = role;
  }
}

export function normalizeDevViewAsRole(raw: unknown): DevViewAsRole {
  if (typeof raw !== "string") return "none";
  const normalized = raw.trim().toLowerCase() as DevViewAsRole;
  return VALID_PRESETS.has(normalized) ? normalized : "none";
}

export const useDevViewAs = create<{ role: DevViewAsRole }>(() => ({
  role: "none",
}));

export function getDevViewAs(): DevViewAsRole {
  return useDevViewAs.getState().role;
}

export function setDevViewAs(next: string): DevViewAsRole {
  const safe = normalizeDevViewAsRole(next);
  publishGlobal(safe);
  useDevViewAs.setState({ role: safe });
  return safe;
}

export function resetDevViewAs(): void {
  publishGlobal("none");
  useDevViewAs.setState({ role: "none" });
}
