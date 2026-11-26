import type { RefObject } from "react";
import * as monaco from "monaco-editor/editor/editor.main.js";
import {
  buildSqlSuggestions,
  type SqlCompletionCatalog,
} from "./sql-completions";

/**
 * Monaco's theme parser only accepts hex colors, not oklch(). The SQL syntax
 * theme is the one sanctioned business-semantic exception to the Shadcn
 * Dashboard token system (UI spec §4), so the two palettes are the template
 * semantic tokens (—card, —primary, —muted-foreground, —accent, —success,
 * —warning) statically converted to hex at adoption time.
 */
const THEME_COLORS = {
  light: {
    background: "FAFBFD",
    foreground: "202733",
    keyword: "005DC2",
    operator: "44566D",
    string: "126F47",
    number: "A14417",
    comment: "576475",
    predefined: "633DBA",
    lineNumber: "596677",
    activeLineNumber: "005BBB",
    activeLine: "F4F8FF",
    selection: "E3EFFF",
    selectionInactive: "EDF2F8",
    findFill: "FFF1C8",
    findOutline: "936000",
    cursor: "0075EA",
    error: "D33145",
    warning: "B75B08",
  },
  dark: {
    primary: "e5e5e5",
    success: "00a63e",
    warning: "f0b100",
    mutedForeground: "a1a1a1",
    card: "171717",
    accent: "262626",
    keywordBlue: "75aec7",
    schemaTeal: "009689",
  },
} as const;

export function defineYearningThemes(resolved: "light" | "dark"): void {
  if (resolved === "light") {
    const palette = THEME_COLORS.light;
    monaco.editor.defineTheme("yearning-sql", {
      base: "vs",
      inherit: true,
      rules: [
        { token: "identifier", foreground: palette.foreground },
        { token: "keyword", foreground: palette.keyword },
        { token: "keyword.sql", foreground: palette.keyword },
        { token: "operator", foreground: palette.operator },
        { token: "operator.sql", foreground: palette.operator },
        { token: "string", foreground: palette.string },
        { token: "string.sql", foreground: palette.string },
        { token: "number", foreground: palette.number },
        { token: "comment", foreground: palette.comment, fontStyle: "italic" },
        { token: "predefined", foreground: palette.predefined },
        { token: "predefined.sql", foreground: palette.predefined },
      ],
      colors: {
        "editor.background": `#${palette.background}`,
        "editor.foreground": `#${palette.foreground}`,
        "editorLineNumber.foreground": `#${palette.lineNumber}`,
        "editorLineNumber.activeForeground": `#${palette.activeLineNumber}`,
        "editor.lineHighlightBackground": `#${palette.activeLine}`,
        "editor.selectionBackground": `#${palette.selection}`,
        "editor.inactiveSelectionBackground": `#${palette.selectionInactive}`,
        "editor.findMatchBackground": `#${palette.findFill}`,
        "editor.findMatchBorder": `#${palette.findOutline}`,
        "editorCursor.foreground": `#${palette.cursor}`,
        "editorError.foreground": `#${palette.error}`,
        "editorWarning.foreground": `#${palette.warning}`,
      },
    });
    return;
  }
  const palette = THEME_COLORS.dark;
  monaco.editor.defineTheme("yearning-sql", {
    base: "vs-dark",
    inherit: true,
    rules: [
      { token: "keyword", foreground: palette.keywordBlue },
      { token: "string", foreground: palette.success },
      { token: "number", foreground: palette.warning },
      { token: "comment", foreground: palette.mutedForeground, fontStyle: "italic" },
      { token: "operator", foreground: palette.keywordBlue },
      { token: "predefined", foreground: palette.schemaTeal },
    ],
    colors: {
      "editor.background": `#${palette.card}`,
      "editorLineNumber.foreground": `#${palette.mutedForeground}`,
      "editor.selectionBackground": `#${palette.accent}`,
    },
  });
}

export function registerSqlCompletionProvider(
  catalogRef: RefObject<SqlCompletionCatalog>,
  onResolveCompletionRef: RefObject<
    | ((ctx: {
        qualifier?: string;
        sql: string;
      }) => Promise<SqlCompletionCatalog | null | undefined>)
    | undefined
  >,
): monaco.IDisposable {
  return monaco.languages.registerCompletionItemProvider("sql", {
    triggerCharacters: [".", " ", "`"],
    async provideCompletionItems(model, position, _context, token) {
      const word = model.getWordUntilPosition(position);
      const range = {
        startLineNumber: position.lineNumber,
        endLineNumber: position.lineNumber,
        startColumn: word.startColumn,
        endColumn: word.endColumn,
      };
      const linePrefix = model.getValueInRange({
        startLineNumber: position.lineNumber,
        startColumn: 1,
        endLineNumber: position.lineNumber,
        endColumn: word.startColumn,
      });
      const dotMatch = /(?:[`"]?([A-Za-z0-9_$-]+)[`"]?\s*\.\s*)$/.exec(linePrefix);
      const qualifier = dotMatch?.[1];
      const sqlText = model.getValue();

      let activeCatalog = catalogRef.current;
      const resolver = onResolveCompletionRef.current;
      if (resolver !== undefined && qualifier !== undefined) {
        const qLower = qualifier.toLowerCase();
        const hasMatchingTables = activeCatalog.tables.some(
          (table) => table.schema !== null && table.schema.toLowerCase() === qLower,
        );
        const hasMatchingColumns = activeCatalog.columns.some(
          (column) => column.table.toLowerCase() === qLower,
        );
        if (!hasMatchingTables && !hasMatchingColumns) {
          try {
            const resolved = await resolver({ qualifier, sql: sqlText });
            if (token.isCancellationRequested) {
              return { suggestions: [] };
            }
            if (resolved != null) {
              activeCatalog = resolved;
              catalogRef.current = resolved;
            }
          } catch {
            // Ignore transient metadata fetch errors; fall back to local catalog.
          }
        }
      }

      const KIND_MAP = {
        keyword: monaco.languages.CompletionItemKind.Keyword,
        schema: monaco.languages.CompletionItemKind.Module,
        table: monaco.languages.CompletionItemKind.Class,
        column: monaco.languages.CompletionItemKind.Field,
      } as const;
      const SORT_PREFIX = {
        column: "0_",
        table: "1_",
        schema: "2_",
        keyword: "3_",
      } as const;
      const suggestions = buildSqlSuggestions(activeCatalog, {
        ...(qualifier === undefined ? {} : { qualifier }),
        sql: sqlText,
      }).map((item) => ({
        label: item.label,
        kind: KIND_MAP[item.kind],
        insertText: item.insertText,
        sortText: `${SORT_PREFIX[item.kind]}${item.label}`,
        range,
        detail: item.detail,
      }));
      return { suggestions };
    },
  });
}

export const IS_MAC_PLATFORM =
  typeof navigator !== "undefined" &&
  /Macintosh|Mac OS X|iPhone|iPad|iPod/i.test(navigator.userAgent);

export {
  execClipboardCommand,
  getAsyncClipboard,
  readClipboardText,
  writeClipboardText,
} from "@/shared/lib/clipboard";

export function resolveCopyTarget(
  editor: monaco.editor.IStandaloneCodeEditor,
  model: monaco.editor.ITextModel,
): { range: monaco.IRange; text: string } | null {
  const selection = editor.getSelection();
  if (selection !== null && !selection.isEmpty()) {
    return { range: selection, text: model.getValueInRange(selection) };
  }
  const lineNumber = selection?.startLineNumber ?? 1;
  const lineContent = model.getLineContent(lineNumber);
  if (lineContent !== "") {
    const range =
      lineNumber < model.getLineCount()
        ? { startLineNumber: lineNumber, startColumn: 1, endLineNumber: lineNumber + 1, endColumn: 1 }
        : { startLineNumber: lineNumber, startColumn: 1, endLineNumber: lineNumber, endColumn: model.getLineMaxColumn(lineNumber) };
    return { range, text: model.getValueInRange(range) };
  }
  const fullText = model.getValue();
  if (fullText !== "") {
    return { range: model.getFullModelRange(), text: fullText };
  }
  return null;
}
