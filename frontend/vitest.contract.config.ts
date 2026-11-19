import path from "node:path";
import { defineConfig } from "vitest/config";

// FE-F12 contract suite: the same generated client + zod runtime schemas
// against the MSW mock layer (default) or the real serve stack
// (F12_BASE_URL + VITE_API_BASE_URL set). Kept apart from the unit config so
// coverage thresholds and the component setup file do not apply.
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  define: {
    __RELEASE_LATEST_URL__: JSON.stringify(""),
    __SPONSOR_URL__: JSON.stringify("https://sponsor.example/"),
    __DOCS_URL__: JSON.stringify("https://docs.example/"),
    __SOURCE_URL__: JSON.stringify("https://source.example/"),
  },
  test: {
    environment: "jsdom",
    include: ["tests/contract/**/*.contract.test.ts"],
    testTimeout: 120_000,
    hookTimeout: 120_000,
    fileParallelism: false,
    sequence: { concurrent: false },
  },
});
