import { defineConfig } from "@playwright/test";

// SEC-0012 independent CSP browser E2E project. Deliberately NOT wired into
// playwright.config.ts (mock suite) or playwright.real.config.ts: both serve
// documents from `vite preview`, which sends no CSP, so the CSP spec can only
// pass against the Go backend built with the embedded release SPA
// (`go build -tags assets`). The document source is started by the driver
// script on a random loopback port — hence no webServer here.
//
// Run (builds FE + backend, starts serve, runs tests/e2e/csp-security.spec.ts):
//
//   cd frontend && node scripts/run-csp-e2e.mjs
//
// Required environment (set by the driver):
//   CSP_E2E_BASE_URL      origin of the embedded-assets serve instance
//   F12_ADMIN_PASSWORD    builtin admin password from the f12 stack workdir
//   F12_SEED_USER_PASSWORD seed users' password (default f12seed-pw12345)
//
// testMatch pins this config to the CSP spec only; the mock config ignores the
// spec (testIgnore) so the regression baseline stays intact. workers=1 keeps
// the backend login rate limit (10 attempts/user/minute) far away.
export default defineConfig({
  // Relative paths resolve against this config file's directory (tests/e2e),
  // not the frontend root — "." is the spec directory itself.
  testDir: ".",
  testMatch: /csp-security\.spec\.ts/,
  timeout: 90_000,
  retries: 0,
  reporter: "list",
  workers: 1,
  use: {
    baseURL: requireBaseURL(),
    trace: "retain-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
    },
  ],
});

function requireBaseURL(): string {
  const value = process.env.CSP_E2E_BASE_URL;
  if (!value) {
    throw new Error(
      "CSP_E2E_BASE_URL is required — run via `node scripts/run-csp-e2e.mjs` "
      + "(it starts the Go serve with embedded assets and exports the origin)",
    );
  }
  return value;
}
