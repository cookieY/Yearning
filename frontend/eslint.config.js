import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  {
    // src/api/generated/** is orval output: correctness is enforced by the
    // locked generator version and the regeneration zero-diff gate, not lint.
    // src/shared/components/ui/** and src/shared/hooks/** are registry
    // output written by the locked shadcn flow; their content is pinned by
    // registry-inventory.json (entry + landed file hashes), so lint rules
    // for hand-written code do not apply to them either.
    ignores: [
      "dist",
      // e2e / real-backend builds write to these dirs (.gitignore:84-85).
      // They are build output, not source: linting them only produces noise
      // from vendored MSW worker files.
      "dist-e2e",
      "dist-real",
      "coverage",
      "playwright-report",
      "test-results",
      "node_modules",
      "src/api/generated/**",
      "src/shared/components/ui/**",
      "src/shared/hooks/**",
      // SEC-0032: MSW tooling moved out of public/ — it is dev/test only and
      // ships through the vite msw-worker-dev-only plugin, never in a release
      // dist. Unminified vendor output stays outside lint scope.
      "src/shared/mock/mockServiceWorker.js",
    ],
  },
  {
    files: ["**/*.{ts,tsx}"],
    extends: [js.configs.recommended, ...tseslint.configs.strictTypeChecked],
    languageOptions: {
      ecmaVersion: 2023,
      globals: globals.browser,
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    plugins: {
      "react-hooks": reactHooks,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/consistent-type-imports": [
        "error",
        { prefer: "type-imports", fixStyle: "inline-type-imports" },
      ],
    },
  },
  {
    files: ["scripts/**/*.mjs", "eslint.config.js", "vite.config.ts"],
    extends: [js.configs.recommended, tseslint.configs.disableTypeChecked],
    languageOptions: { globals: globals.node },
  },
  {
    files: ["tests/e2e/**/*.ts", "tests/e2e-real/**/*.ts"],
    extends: [tseslint.configs.disableTypeChecked],
    languageOptions: { globals: globals.browser },
  },
  {
    // FE-F12 contract suite and build script config: non-product code, disable type-aware rules
    // (contract tests intentionally use as-unknown shape assertions mirroring use-admin.ts generated client
    // union type unwrapping).
    files: [
      "tests/contract/**/*.ts",
      "vitest.contract.config.ts",
      "playwright.real.config.ts",
    ],
    extends: [tseslint.configs.disableTypeChecked],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    rules: {
      // TypeScript cannot preserve null-narrowing across closures inside a polling loop (currentTask! pattern).
      "@typescript-eslint/no-non-null-assertion": "off",
    },
  }
);
