import { defineConfig } from "@playwright/test";

// E2E runs against the production build served by vite preview. The smoke
// suite asserts static structure; scenario-driven browser flows (MSW worker
// via VITE_ENABLE_MOCK builds) extend this suite from FE-F2 on
// (code-generation-policy.json mock_layer).
export default defineConfig({
  testDir: "./tests/e2e",
  // SEC-0012: csp-security.spec.ts needs the real Go backend (embedded
  // release SPA) as the document source — vite preview sends no CSP. The
  // spec runs via its own project (tests/e2e/csp.playwright.config.ts,
  // driver scripts/run-csp-e2e.mjs) and is excluded here so the mock
  // regression baseline never executes it against a CSP-less server.
  // The frame-budget spec measures rAF deltas, so it runs in its own
  // single-worker process (`pnpm test:frames`) instead of competing with the
  // parallel suite for the CPU: under contention the machine, not the motion,
  // decides whether p95 fits. The rest of the suite still runs in parallel.
  testIgnore: process.env.MOTION_FRAMES_ONLY
    ? /csp-security\.spec\.ts/
    : [/csp-security\.spec\.ts/, /motion-frames\.spec\.ts/],
  timeout: 30_000,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? "list" : "html",
  use: {
    baseURL: "http://localhost:4173",
    trace: "retain-on-failure",
  },
  // tests/screenshots/*.png are assertions, not by-products. The template keeps
  // their historical paths and names so the committed baseline set stays
  // canonical, and a capture can no longer "pass" by silently rewriting the
  // file it was supposed to compare against.
  snapshotPathTemplate: "{testDir}/../screenshots/{arg}{ext}",
  // Tolerance, honestly derived: `threshold: 0.2` (a Playwright default) already
  // treats any single pixel within 0.2 YIQ distance as identical, so the
  // 2–39 px / ≤6 per channel anti-aliasing wobble measured on the reference
  // platform produces *zero* diff pixels. `maxDiffPixelRatio: 0.01` therefore
  // does not absorb that noise — it absorbs what was not measured: anti-aliased
  // edges that occasionally cross the 0.2 boundary, and browser-build drift
  // (Playwright pins a chromium revision, and a dependency bump can change it;
  // the committed baselines are only valid for the pinned build). 12960 px on a
  // 1440×900 frame stays far tighter than a structural regression (a missing
  // sidebar, a shifted column or a broken theme each move several percent).
  //
  // Known blind spot this creates: a *uniform, low-amplitude* colour shift
  // (every channel inside 0.2 YIQ) passes at any area — the ratio cannot catch
  // it, and a chromium upgrade turns all 116 baselines red at once.
  //
  // `animations`, `caret` and `scale` below restate Playwright defaults; they
  // are written out so a future config edit cannot silently re-enable them,
  // not because this package calibrated them.
  //
  // Todo: a global template also covers `toMatchAriaSnapshot`, whose default
  // lives in a different path — any future aria snapshot would land in
  // tests/screenshots/*.yml. Nothing uses it today; scope the template before
  // adding one.
  //
  // Rendering platform: the baselines are produced *and* compared inside the
  // pinned Playwright image (`scripts/e2e-container.sh`; the tag tracks the
  // `@playwright/test` version above), and the CI e2e job runs that same image.
  // A host run on macOS rasterises text with different fonts and hinting — the
  // same page drifts by ~2 % of pixels there — so the set keeps exactly one
  // renderer instead of a `{platform}` split: two sets would drift
  // independently and neither would describe CI.
  expect: {
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.01,
      threshold: 0.2,
      animations: "disabled",
      caret: "hide",
      scale: "css",
    },
  },
  webServer: {
    // The e2e suite is mock-driven (code-generation-policy.json mock_layer):
    // the build inlines VITE_ENABLE_MOCK=true so the shared MSW worker backs
    // the session, flows and dashboard endpoints exactly as in vitest. Release
    // builds keep the flag off — mock code is a dynamic import and never part
    // of the default bundle. The release banner reads the backend snapshot
    // endpoint (RCP-20261020), backed by the MSW fixture below — tests never
    // leave localhost and the banner stays deterministic.
    command:
      "VITE_BUILD_OUT_DIR=dist-e2e VITE_ENABLE_MOCK=true pnpm build && "
      + "VITE_BUILD_OUT_DIR=dist-e2e pnpm preview --port 4173 --strictPort",
    url: "http://localhost:4173",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
    },
  ],
});
