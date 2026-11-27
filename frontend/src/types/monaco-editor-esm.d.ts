// monaco-editor 0.56 exposes deep ESM entry points through its exports map
// ("./*.js": "./esm/vs/*.js"), but its type declarations for those subpaths
// are not resolvable by TypeScript's bundler mode. Bridge the entry point and
// minimal SQL side-effect contributions to the package's root type surface.
declare module "monaco-editor/editor/editor.api.js" {
  export * from "monaco-editor";
}

// Side-effect contributions for SQL editing, highlighting, suggest, and find.
declare module "monaco-editor/editor/browser/coreCommands.js";
declare module "monaco-editor/editor/common/standaloneStrings.js";
declare module "monaco-editor/editor/contrib/suggest/browser/suggestController.js";
declare module "monaco-editor/editor/contrib/snippet/browser/snippetController2.js";
declare module "monaco-editor/features/find/register.js";
declare module "monaco-editor/editor/contrib/clipboard/browser/clipboard.js";
declare module "monaco-editor/editor/contrib/bracketMatching/browser/bracketMatching.js";
declare module "monaco-editor/editor/contrib/comment/browser/comment.js";
declare module "monaco-editor/editor/contrib/folding/browser/folding.js";
declare module "monaco-editor/editor/contrib/indentation/browser/indentation.js";
declare module "monaco-editor/editor/contrib/linesOperations/browser/linesOperations.js";
declare module "monaco-editor/editor/contrib/wordOperations/browser/wordOperations.js";
declare module "monaco-editor/editor/contrib/multicursor/browser/multicursor.js";
declare module "monaco-editor/editor/contrib/readOnlyMessage/browser/contribution.js";
declare module "monaco-editor/languages/definitions/sql/register.js";
