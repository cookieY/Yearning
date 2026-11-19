import { create } from "zustand";
import { holdsSessionSlot, mockSuperSession } from "@/shared/dev/mock-super";
import { getServerDimensions } from "@/shared/mock/server-dimensions";

/**
 * Mock identity dimension (owner ruling 2026-10-13 on issue #1/§18.44): the
 * single-account demo collapsed submitter, frozen reviewers and executors
 * into one user, so the submitter's own order detail grew the approval
 * decision card. The mock now models three deterministic identities, read
 * per request exactly like the auth-behavior and scenario dimensions:
 * henry submits (and owns the seeded drafts), reviewer-a holds every frozen
 * approval-step actor slot, executor-a holds every execution-actor slot.
 */
export type MockIdentity = "henry" | "reviewer-a" | "executor-a";

export interface MockIdentityUser {
  id: string;
  username: string;
}

export const MOCK_IDENTITIES: Record<MockIdentity, MockIdentityUser> = {
  henry: { id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac", username: "henry" },
  "reviewer-a": { id: "0198d9cc-e65d-7b9d-a8aa-3c81945fa001", username: "reviewer-a" },
  "executor-a": { id: "0198d9cc-e65d-7b9d-a8aa-3c81945fb002", username: "executor-a" },
};

/** Stable role-slot ids for fixture seeding (frozen snapshots and flow
 * templates). Submitter stays the henry identity. */
export const MOCK_IDENTITY_IDS = {
  submitter: MOCK_IDENTITIES.henry.id,
  reviewer: MOCK_IDENTITIES["reviewer-a"].id,
  executor: MOCK_IDENTITIES["executor-a"].id,
} as const;

export const IDENTITY_STORAGE_KEY = "yearning-mock-identity";

export function readStoredIdentity(): MockIdentity {
  // Node transport (vite dev middleware, 2026-11-04): no window — the
  // shared dimensions live in the dev server process. Guard on `window`,
  // not `localStorage`: Node ≥22 ships a global localStorage, so the
  // typeof-localStorage probe is false-negative there.
  if (typeof window === "undefined") {
    const server = getServerDimensions().identity;
    return server === "reviewer-a" || server === "executor-a" ? server : "henry";
  }
  const value = localStorage.getItem(IDENTITY_STORAGE_KEY);
  return value === "reviewer-a" || value === "executor-a" ? value : "henry";
}

/** Identity of the current session, read per request so tests and the e2e
 * helper can flip it without re-mounting the worker or re-logging-in. */
export function currentIdentity(): MockIdentityUser {
  return MOCK_IDENTITIES[readStoredIdentity()];
}

export function currentIdentityId(): string {
  return currentIdentity().id;
}

/** Username for a known fixture identity id — frozen actor snapshots and
 * execution-actor lists render names; unknown ids fall back to "henry". */
export function identityUsernameOf(userId: string): string {
  const found = Object.values(MOCK_IDENTITIES).find((identity) => identity.id === userId);
  return found?.username ?? "henry";
}

export const useMockIdentity = create<{ identity: MockIdentity }>(() => ({
  identity: "henry",
}));

export function setMockIdentity(identity: MockIdentity): void {
  localStorage.setItem(IDENTITY_STORAGE_KEY, identity);
  useMockIdentity.setState({ identity });
  window.dispatchEvent(new CustomEvent("yearning:mock-identity"));
}

/**
 * Mock super mode (owner ruling 2026-11-03: grant the mock account all permissions so testing does not require
 * switching accounts): the dev-browser session is treated as holding every actor slot
 * and every admin capability, so one account can submit, review, execute and
 * open the admin area without flipping the identity/auth-behavior dimensions.
 * The flag and default live in shared/dev/mock-super.ts (single source of
 * truth shared with the app's client-side actor checks); the failure faces of
 * the auth-behavior dimension (expired / invalid_credentials / admin_locked)
 * keep their semantics even in super mode.
 */
export function mockSuperMode(): boolean {
  return mockSuperSession();
}

/**
 * Actor-slot check used by every fixture guard: does the current session user
 * hold this frozen actor/owner slot? Super mode answers yes for all of them;
 * otherwise the exact identity match applies (§18.44 three-identity split).
 */
export function holdsActorSlot(userId: string): boolean {
  return holdsSessionSlot(userId, currentIdentityId());
}
