import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

const LAYOUT_NO_RESTRICTED_SYNTAX = [
  {
    selector:
      "JSXAttribute[name.name='className'] Literal[value=/\\babsolute\\b.*\\bz-50\\b|\\bz-50\\b.*\\babsolute\\b/]",
    message:
      "Portal overlay menus with Base UI Popover/Select/DropdownMenu or createPortal + usePortalAnchor instead of naked absolute z-50 positioning.",
  },
  {
    selector:
      "JSXAttribute[name.name='className'] TemplateElement[value.raw=/\\babsolute\\b.*\\bz-50\\b|\\bz-50\\b.*\\babsolute\\b/]",
    message:
      "Portal overlay menus with Base UI Popover/Select/DropdownMenu or createPortal + usePortalAnchor instead of naked absolute z-50 positioning.",
  },
  {
    selector:
      "JSXElement[openingElement.name.name='TableCell'] MemberExpression[property.name='detail']",
    message:
      "Render long error details in a colSpan TableRowErrorRow below the row instead of inside a TableCell.",
  },
  {
    selector:
      "JSXElement[openingElement.name.name='TableCell'] Identifier[name='testErrorDetail']",
    message:
      "Render long error details in a colSpan TableRowErrorRow below the row instead of inside a TableCell.",
  },
];

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
      "playwright.invariants.config.ts",
    ],
    extends: [tseslint.configs.disableTypeChecked],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    rules: {
      // TypeScript cannot preserve null-narrowing across closures inside a polling loop (currentTask! pattern).
      "@typescript-eslint/no-non-null-assertion": "off",
    },
  },
  {
    // Stage 2 Step 2.13 (A3) + Stage 3A (D21): forbid `as unknown as` type escapes and naked overlay / TableCell detail anti-patterns.
    files: ["src/features/**/*.{ts,tsx}", "src/routes/**/*.{ts,tsx}"],
    ignores: ["**/*.test.*", "**/*.spec.*", "**/*.worker.ts"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "TSAsExpression > TSAsExpression[typeAnnotation.type='TSUnknownKeyword']",
          message: "Fix generated/mutator typing instead of using 'as unknown as' casts in feature/route code.",
        },
        ...LAYOUT_NO_RESTRICTED_SYNTAX,
      ],
    },
  },
  {
    // Stage 3A (D21): apply layout guards to shared components without overriding the feature/route rule set.
    files: ["src/shared/components/**/*.{ts,tsx}"],
    ignores: ["**/*.test.*", "**/*.spec.*"],
    rules: {
      "no-restricted-syntax": ["error", ...LAYOUT_NO_RESTRICTED_SYNTAX],
    },
  },
  {
    // Stage 2 Step 2.13 (A3): forbid naked fetch() outside shared API transport, SSE stream, and dev dimension sync.
    files: ["src/**/*.{ts,tsx}"],
    ignores: [
      "**/*.test.*",
      "**/*.spec.*",
      "src/shared/api/mutator.ts",
      "src/features/assistant/use-assistant.ts",
      "src/shared/mock/client-dimensions.ts",
    ],
    rules: {
      "no-restricted-globals": [
        "error",
        {
          name: "fetch",
          message: "Use the generated API client and shared/api/mutator.ts instead of calling fetch() directly.",
        },
      ],
    },
  }
);
