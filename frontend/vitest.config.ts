import path from "node:path";
import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  // Mirrors the vite.config.ts defines: unit suites render the workspace
  // page and the shell footer, which read the inlined GitHub-release
  // endpoint and footer links. Tests run with the endpoint disabled (empty
  // string) — they must never hit network.
  define: {
    __RELEASE_LATEST_URL__: JSON.stringify(""),
    __SPONSOR_URL__: JSON.stringify("https://sponsor.example/"),
    __DOCS_URL__: JSON.stringify("https://docs.example/"),
    __SOURCE_URL__: JSON.stringify("https://source.example/"),
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    // Shared CI runners execute the jsdom suites several times slower than a
    // dev laptop; the default 5s timeout produced chronic flake on the
    // char-by-char typing and double-precheck flows. 20s bounds a genuine
    // hang without relaxing any assertion.
    testTimeout: 20_000,
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      include: ["src/**"],
      exclude: [
        "src/api/generated/**",
        "src/shared/components/ui/**",
        "src/**/*.d.ts",
        "src/**/*.css",
        "src/assets/**",
        // Assembly and entry layers with no decision logic. The shell and its
        // components are FE-F2 deliverables and carry their own unit tests;
        // only thin entry/assembly files stay outside the 80% gate.
        // Logic-bearing modules (api, i18n, theme, providers' policy code,
        // mock scenarios) stay inside the 80% gate.
        "src/main.tsx",
        "src/app/router/**",
        "src/app/layouts/**",
        "src/routes/**",
        "src/app/providers/app-providers.tsx",
        "src/shared/mock/**",
        // Browser-only Monaco integration shell: the real editor cannot run
        // under jsdom (layout APIs). The Playwright precheck suite exercises
        // it for real (mount, typing, explicit theme surfaces); lifecycle
        // dispose is guarded by the unified mount effect.
        "src/features/review/sql-editor-panel.tsx",
      ],
      thresholds: {
        // Shared pure functions and state logic carry the highest bar
        // (frontend implementation PRD §21); the generated layer is excluded.
        lines: 80,
        functions: 80,
        statements: 80,
        branches: 80,
      },
    },
  },
});
