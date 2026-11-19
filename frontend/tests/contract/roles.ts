/**
 * Shared role switching for the contract suite. Mock mode drives the MSW
 * identity/auth-behavior stores exactly as Playwright does; real mode logs
 * in with the identities seeded by scripts/integration/f12_seed.py.
 *
 * Real mode caches one live session per role (cookie snapshot): the backend
 * rate-limits logins (429) and locks accounts after five failures, and the
 * suite switches roles constantly — replaying a cached session keeps the
 * login count at one per role per run.
 */
import { chmodSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { login } from "@/api/generated/client/authentication/authentication";
import { clearSession, mode } from "./harness";

export type Role = "admin" | "submitter" | "approver" | "executor" | "quser";

const REAL_USERNAMES: Record<Role, string> = {
  admin: "admin",
  submitter: "f12-submitter",
  approver: "f12-approver",
  executor: "f12-executor",
  quser: "f12-quser",
};

const MOCK_IDENTITY: Record<Role, "henry" | "reviewer-a" | "executor-a"> = {
  admin: "henry",
  submitter: "henry",
  approver: "reviewer-a",
  executor: "executor-a",
  quser: "henry",
};

export function realPassword(role: Role): string {
  const value = role === "admin"
    ? process.env.F12_ADMIN_PASSWORD
    : process.env.F12_SEED_USER_PASSWORD ?? "f12seed-pw12345";
  if (!value) throw new Error("F12_ADMIN_PASSWORD is required in real mode");
  return value;
}

export function usernameFor(role: Role): string {
  return mode === "real" ? REAL_USERNAMES[role] : MOCK_IDENTITY[role];
}

// Session cache: cookie name→value pairs per role. Real mode additionally
// shares the cache across test files through a temp file — each vitest file
// is its own process, and one login per role per RUN keeps the backend's
// login rate limiter (10 attempts/user/minute) far out of reach.
const sessions = new Map<Role, ReadonlyArray<[string, string]>>();
const SESSION_CACHE_TTL_MS = 20 * 60_000;

interface SharedSessionEntry { at: number; jar: Array<[string, string]> }
const sharedCachePath = (): string | null =>
  mode === "real" ? `${tmpdir()}/f12-contract-sessions.json` : null;

function readSharedSessions(): Map<string, SharedSessionEntry> {
  const path = sharedCachePath();
  if (path === null) return new Map();
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8")) as Record<string, SharedSessionEntry>;
    return new Map(Object.entries(parsed));
  } catch {
    return new Map();
  }
}

function writeSharedSessions(map: Map<string, SharedSessionEntry>): void {
  const path = sharedCachePath();
  if (path === null) return;
  // The file holds live session cookies: keep it owner-only (the stack's own
  // secret discipline — see f12_stack.py's 0600 keyring).
  writeFileSync(path, JSON.stringify(Object.fromEntries(map), null, 2), { mode: 0o600 });
  try {
    chmodSync(path, 0o600);
  } catch {
    // chmod is belt-and-braces for pre-existing files; the write mode covers new ones.
  }
}

function snapshotJar(): Array<[string, string]> {
  const jar = (globalThis as { __f12Jar?: Map<string, string> }).__f12Jar;
  return jar ? [...jar.entries()] : [];
}

function restoreJar(pairs: ReadonlyArray<[string, string]>): void {
  clearSession();
  const jar = (globalThis as { __f12Jar?: Map<string, string> }).__f12Jar;
  if (!jar) return;
  for (const [name, value] of pairs) {
    jar.set(name, value);
    if (typeof document !== "undefined") {
      document.cookie = `${name}=${value}`;
    }
  }
}

/** Drop the cached session (after logout or an explicit invalidation). */
export function invalidateSession(role: Role): void {
  sessions.delete(role);
  const shared = readSharedSessions();
  if (shared.delete(role)) writeSharedSessions(shared);
}

export async function loginAs(role: Role): Promise<void> {
  if (mode === "mock") {
    clearSession();
    const { setMockAuthBehavior } = await import("@/shared/mock/auth-scenario-store");
    setMockAuthBehavior(role === "admin" ? "admin" : "default");
    localStorage.setItem("yearning-mock-identity", MOCK_IDENTITY[role]);
    await login({ username: MOCK_IDENTITY[role], password: "fixpw-contr" });
    return;
  }
  const cached = sessions.get(role);
  if (cached) {
    restoreJar(cached);
    return;
  }
  const shared = readSharedSessions();
  const entry = shared.get(role);
  if (entry && Date.now() - entry.at < SESSION_CACHE_TTL_MS) {
    const jarSnapshot = entry.jar as ReadonlyArray<[string, string]>;
    sessions.set(role, jarSnapshot);
    restoreJar(jarSnapshot);
    return;
  }
  clearSession();
  await login({ username: REAL_USERNAMES[role], password: realPassword(role) });
  const snapshot = snapshotJar();
  sessions.set(role, snapshot);
  shared.set(role, { at: Date.now(), jar: [...snapshot] });
  writeSharedSessions(shared);
}

export function anonymous(): void {
  clearSession();
}
