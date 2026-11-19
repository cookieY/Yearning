import { afterAll, describe, expect, it } from "vitest";
import { ensureMockNodeServer } from "./node";

/**
 * The node mock transport must be a process-level singleton (owner ruling
 * 2026-11-04): msw's interceptors do not reliably detach on close(), so two
 * live servers would both answer every request. The store lives on
 * globalThis via Symbol.for — the same registry the vite dev-server plugin
 * reads — so this test exercises the real create-or-return semantics.
 */

const KEY = Symbol.for("yearning.mock.node-server");

describe("ensureMockNodeServer", () => {
  it("returns the same listening server instance on every call", () => {
    const first = ensureMockNodeServer();
    const second = ensureMockNodeServer();
    expect(second).toBe(first);
    // The singleton stays registered for the process (dev server + vitest).
    expect((globalThis as Record<symbol, unknown>)[KEY]).toBe(first);
  });

  // Leave the process registry exactly as this file found it, so a future
  // change to worker/isolation reuse cannot inherit our interceptor
  // (review L2, 2026-11-04).
  afterAll(() => {
    const registry = globalThis as Record<symbol, unknown>;
    const server = registry[KEY] as { close: () => void } | undefined;
    server?.close();
    Reflect.deleteProperty(registry, KEY);
  });
});
