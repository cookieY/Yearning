import { describe, expect, it } from "vitest";

/**
 * SEC-0032 regression guard: the MSW service worker is dev/test tooling and
 * must never ship in a release dist. Everything under public/ is copied into
 * every build unconditionally, so the worker lives at
 * src/shared/mock/mockServiceWorker.js and reaches a URL only through the
 * vite `msw-worker-dev-only` plugin (dev middleware always; build emission
 * only under VITE_ENABLE_MOCK — the e2e webServer build). These placement
 * invariants are the unit-level half of the guard; the behavioral half is the
 * real release build whose dist/ must not contain the file. A stray
 * `msw init public/` fails here and in check-assets.mjs.
 */

const devOnlyWorker = import.meta.glob("./mockServiceWorker.js", {
  eager: true,
  query: "?raw",
  import: "default",
});

const publicFiles = import.meta.glob("../../../public/**", {
  eager: true,
  query: "?url",
  import: "default",
});

const packageJson = import.meta.glob("../../../package.json", {
  eager: true,
  query: "?raw",
  import: "default",
});

describe("MSW service worker placement (SEC-0032)", () => {
  it("keeps the worker at the dev-only source the vite plugin reads", () => {
    const worker = devOnlyWorker["./mockServiceWorker.js"];
    expect(worker).toBeDefined();
    // The genuine unmodified `msw init` artifact, not a stub.
    expect(worker).toContain("Mock Service Worker.");
    expect(worker).toContain("INTEGRITY_CHECKSUM");
  });

  it("keeps the worker version in lockstep with the installed msw package", () => {
    const worker = devOnlyWorker["./mockServiceWorker.js"] ?? "";
    const pkg = JSON.parse(packageJson["../../../package.json"] ?? "{}") as {
      // msw is dev/test tooling, so it lives under devDependencies.
      devDependencies: Record<string, string>;
    };
    const match = worker.match(/PACKAGE_VERSION = '([^']+)'/);
    expect(match?.[1]).toBe(pkg.devDependencies["msw"]);
  });

  it("never keeps the worker under public/ — public/* ships in every build", () => {
    const shipped = Object.keys(publicFiles);
    expect(shipped.some((file) => file.endsWith("/mockServiceWorker.js"))).toBe(false);
  });
});
