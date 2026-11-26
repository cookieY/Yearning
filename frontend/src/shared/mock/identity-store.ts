import { create } from "zustand";
import {
  DEV_VIEW_AS_HEADER,
  normalizeDevViewAsRole,
  type DevViewAsRole,
} from "@/shared/dev/dev-view-as";
import { getServerDimensions } from "@/shared/mock/server-dimensions";

/**
 * Mock identity dimension (D9, §18.44):
 * - `owner`: all-role real actor used by default in dev (`pnpm dev`) and static demo.
 * - `henry`: ordinary non-admin user/submitter (default in Vitest and Playwright E2E).
 * - `reviewer-a`: frozen approval-step actor.
 * - `executor-a`: frozen execution-step actor.
 *
 * Role checks never elevate privileges or bypass frozen actor matching.
 */
export type MockIdentity = "owner" | "henry" | "reviewer-a" | "executor-a";

export interface MockIdentityUser {
  id: string;
  username: string;
}

export const MOCK_IDENTITIES: Record<MockIdentity, MockIdentityUser> = {
  owner: { id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ad", username: "owner" },
  henry: { id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac", username: "henry" },
  "reviewer-a": { id: "0198d9cc-e65d-7b9d-a8aa-3c81945fa001", username: "reviewer-a" },
  "executor-a": { id: "0198d9cc-e65d-7b9d-a8aa-3c81945fb002", username: "executor-a" },
};

/** Stable role-slot ids for fixture seeding (frozen snapshots and flow templates). */
export const MOCK_IDENTITY_IDS = {
  owner: MOCK_IDENTITIES.owner.id,
  submitter: MOCK_IDENTITIES.henry.id,
  reviewer: MOCK_IDENTITIES["reviewer-a"].id,
  executor: MOCK_IDENTITIES["executor-a"].id,
} as const;

export const IDENTITY_STORAGE_KEY = "yearning-mock-identity";

export function isMockIdentity(raw: unknown): raw is MockIdentity {
  return raw === "owner" || raw === "henry" || raw === "reviewer-a" || raw === "executor-a";
}

export function defaultMockIdentity(): MockIdentity {
  return import.meta.env.MODE === "development" || import.meta.env.VITE_DEMO_MODE === "true"
    ? "owner"
    : "henry";
}

export function readStoredIdentity(): MockIdentity {
  // Node transport (vite dev middleware, 2026-11-04): no window — the
  // shared dimensions live in the dev server process. Guard on `window`,
  // not `localStorage`: Node >=22 ships a global localStorage.
  if (typeof window === "undefined") {
    const server = getServerDimensions().identity;
    return isMockIdentity(server) ? server : defaultMockIdentity();
  }
  const value = localStorage.getItem(IDENTITY_STORAGE_KEY);
  return isMockIdentity(value) ? value : defaultMockIdentity();
}

/** Identity of the current session, read per request. */
export function currentIdentity(): MockIdentityUser {
  return MOCK_IDENTITIES[readStoredIdentity()];
}

export function currentIdentityId(): string {
  return currentIdentity().id;
}

/** Username for a known fixture identity id; unknown ids stay explicit (`unknown-<id>`). */
export function identityUsernameOf(userId: string): string {
  const found = Object.values(MOCK_IDENTITIES).find((identity) => identity.id === userId);
  return found?.username ?? `unknown-${userId}`;
}

export const useMockIdentity = create<{ identity: MockIdentity }>(() => ({
  identity: defaultMockIdentity(),
}));

export function setMockIdentity(identity: MockIdentity): void {
  localStorage.setItem(IDENTITY_STORAGE_KEY, identity);
  useMockIdentity.setState({ identity });
  window.dispatchEvent(new CustomEvent("yearning:mock-identity"));
}

/** Read dev-only downgrade preset from the request header; ignored outside `DEV`. */
export function readDevViewAs(request?: Request): DevViewAsRole {
  if (!import.meta.env.DEV) return "none";
  return normalizeDevViewAsRole(request?.headers.get(DEV_VIEW_AS_HEADER));
}

/** True only when the active identity holds admin capability and is not downgraded via D18. */
export function effectiveCanAccessAdmin(behavior: string, request?: Request): boolean {
  if (behavior === "expired") return false;
  const baseAdmin = behavior === "admin" || readStoredIdentity() === "owner";
  if (!baseAdmin) return false;
  return readDevViewAs(request) === "none";
}

/** Strict identity equality check — never grants cross-user slots. */
export function holdsActorSlot(userId: string): boolean {
  return userId === currentIdentityId();
}

/** Approval slot check allowing dev-only D18 downgrade (`executor` / `user` hide approval slots). */
export function holdsApprovalSlot(userId: string, request?: Request): boolean {
  if (userId !== currentIdentityId()) return false;
  const viewAs = readDevViewAs(request);
  return viewAs !== "executor" && viewAs !== "user";
}

/** Execution slot check allowing dev-only D18 downgrade (`approver` / `user` hide execution slots). */
export function holdsExecutionSlot(userId: string, request?: Request): boolean {
  if (userId !== currentIdentityId()) return false;
  const viewAs = readDevViewAs(request);
  return viewAs !== "approver" && viewAs !== "user";
}
