import { afterEach, describe, expect, it, vi } from "vitest";
import {
  syncDimensionsFromServer,
  unregisterMockWorker,
} from "./client-dimensions";
import { IDENTITY_STORAGE_KEY } from "./identity-store";
import { SCENARIO_STORAGE_KEY } from "./scenario-store";
import { AUTH_BEHAVIOR_STORAGE_KEY } from "./auth-scenario-store";
import { SUPER_STORAGE_KEY } from "@/shared/dev/mock-super";

/**
 * The client half of the node mock transport: unregister the legacy service
 * worker, then one-way boot sync of the server dimensions into localStorage.
 * Every guard (endpoint missing, non-ok response, malformed payloads) must
 * leave the tab's own state intact.
 */

function stubFetch(implementation: () => Promise<unknown>) {
  const fetchMock = vi.fn(implementation);
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.localStorage.clear();
});

describe("unregisterMockWorker", () => {
  it("does nothing when the browser has no service worker container", async () => {
    // jsdom has no navigator.serviceWorker: the guard branch itself.
    await expect(unregisterMockWorker()).resolves.toBeUndefined();
  });

  it("unregisters every legacy registration", async () => {
    const unregister = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", {
      serviceWorker: {
        getRegistrations: vi.fn().mockResolvedValue([
          { unregister },
          { unregister },
        ]),
      },
    });
    await unregisterMockWorker();
    expect(unregister).toHaveBeenCalledTimes(2);
  });

  it("is a no-op when no legacy registration exists", async () => {
    vi.stubGlobal("navigator", {
      serviceWorker: {
        getRegistrations: vi.fn().mockResolvedValue([]),
      },
    });
    await expect(unregisterMockWorker()).resolves.toBeUndefined();
  });
});

describe("syncDimensionsFromServer", () => {
  it("writes every accepted dimension into localStorage", async () => {
    stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            identity: "reviewer-a",
            superMode: false,
            scenario: "orders-busy",
            authBehavior: "admin_locked",
          }),
      }),
    );
    await syncDimensionsFromServer();
    expect(window.localStorage.getItem(IDENTITY_STORAGE_KEY)).toBe("reviewer-a");
    expect(window.localStorage.getItem(SUPER_STORAGE_KEY)).toBe("0");
    expect(window.localStorage.getItem(SCENARIO_STORAGE_KEY)).toBe("orders-busy");
    expect(window.localStorage.getItem(AUTH_BEHAVIOR_STORAGE_KEY)).toBe("admin_locked");
  });

  it("accepts each documented identity but rejects unknown ones", async () => {
    for (const identity of ["henry", "reviewer-a", "executor-a"]) {
      stubFetch(() =>
        Promise.resolve({ ok: true, json: () => Promise.resolve({ identity }) }),
      );
      await syncDimensionsFromServer();
      expect(window.localStorage.getItem(IDENTITY_STORAGE_KEY)).toBe(identity);
    }
    window.localStorage.setItem(IDENTITY_STORAGE_KEY, "henry");
    stubFetch(() =>
      Promise.resolve({ ok: true, json: () => Promise.resolve({ identity: "attacker" }) }),
    );
    await syncDimensionsFromServer();
    expect(window.localStorage.getItem(IDENTITY_STORAGE_KEY)).toBe("henry");
  });

  it("only accepts a boolean superMode, a string scenario and a string authBehavior", async () => {
    window.localStorage.setItem(SUPER_STORAGE_KEY, "1");
    window.localStorage.setItem(SCENARIO_STORAGE_KEY, "ready");
    window.localStorage.setItem(AUTH_BEHAVIOR_STORAGE_KEY, "default");
    stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            superMode: "yes",
            scenario: 42,
            authBehavior: null,
          }),
      }),
    );
    await syncDimensionsFromServer();
    expect(window.localStorage.getItem(SUPER_STORAGE_KEY)).toBe("1");
    expect(window.localStorage.getItem(SCENARIO_STORAGE_KEY)).toBe("ready");
    expect(window.localStorage.getItem(AUTH_BEHAVIOR_STORAGE_KEY)).toBe("default");
  });

  it("maps a true superMode to the stored flag form", async () => {
    stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ superMode: true }),
      }),
    );
    await syncDimensionsFromServer();
    expect(window.localStorage.getItem(SUPER_STORAGE_KEY)).toBe("1");
  });

  it("keeps the tab state when the endpoint answers non-ok", async () => {
    window.localStorage.setItem(SCENARIO_STORAGE_KEY, "orders-busy");
    stubFetch(() => Promise.resolve({ ok: false, json: () => Promise.resolve({}) }));
    await syncDimensionsFromServer();
    expect(window.localStorage.getItem(SCENARIO_STORAGE_KEY)).toBe("orders-busy");
  });

  it("keeps the tab state when the endpoint is absent or throws", async () => {
    window.localStorage.setItem(SCENARIO_STORAGE_KEY, "orders-busy");
    stubFetch(() => Promise.reject(new Error("no endpoint")));
    await syncDimensionsFromServer();
    expect(window.localStorage.getItem(SCENARIO_STORAGE_KEY)).toBe("orders-busy");
  });
});
