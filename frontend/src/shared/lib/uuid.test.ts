import { afterEach, describe, expect, it, vi } from "vitest";

import { randomUUID } from "./uuid";

const V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

const realCrypto = globalThis.crypto;

describe("randomUUID", () => {
  afterEach(() => { vi.unstubAllGlobals(); });

  it("returns a v4 UUID", () => {
    expect(randomUUID()).toMatch(V4);
  });

  it("falls back to getRandomValues when crypto.randomUUID is missing (insecure context)", () => {
    vi.stubGlobal("crypto", {
      getRandomValues: (array: Uint8Array<ArrayBuffer>) => realCrypto.getRandomValues(array),
    });
    const a = randomUUID();
    const b = randomUUID();
    expect(a).toMatch(V4);
    expect(b).toMatch(V4);
    expect(a).not.toBe(b);
  });
});
