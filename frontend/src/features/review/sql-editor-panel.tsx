import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ClipboardPaste, Copy, Scissors, TextSelect } from "lucide-react";

import { ContextMenu, type ContextMenuItem, type ContextMenuState } from "@/shared/components/context-menu";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { cn } from "@/shared/lib/utils";
// editor.main (not editor.api): the api entry omits the editor
// contributions — the suggest widget, find, etc. — which the completion
// provider depends on. All languages ship here too; the chunk stays lazy.
import * as monaco from "monaco-editor/editor/editor.main.js";
// SQL is a monarch-highlighted basic language: tokenization runs on the main
// thread and only the general editor worker is needed. Bundled workers only —
// no runtime CDN or floating remote loading (UI spec §14).
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";
import { useTheme } from "@/app/providers/theme-provider";

// Register the SQL language definition (monaco 0.56 layout) for monarch
// tokenization.
import "monaco-editor/languages/definitions/sql/register.js";

/**
 * Monaco SQL editor panel (frontend PRD F4; UI spec §7.4). The editor is the
 * SQL workspace core: bundled workers, one instance per mount with a single
 * unified dispose path (legacy baseline finding #12 — no leaked providers or
 * models), and a controlled `value` bridge for programmatic loads (reveal).
 * The syntax theme is the one sanctioned business-semantic exception to the
 * Shadcn Dashboard token system; container, toolbar and borders stay on
 * standard components.
 */

let environmentConfigured = false;
function configureWorkers(): void {
  if (environmentConfigured) return;
  environmentConfigured = true;
  self.MonacoEnvironment = {
    getWorker() {
      return new EditorWorker();
    },
  };
}

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

function defineYearningThemes(resolved: "light" | "dark"): void {
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

/** Optional schema/table/column catalog feeding the SQL completion provider.
 * Schemas come from the frozen flow's schema mappings; tables and columns
 * require a metadata read surface the submission journey does not have yet
 * (contract: metadata endpoints are query-session scoped only), so the
 * catalog ships empty there and lights up when a read surface exists. */
import {
  buildSqlSuggestions,
  type SqlCompletionCatalog,
} from "./sql-completions";

export type { SqlCompletionCatalog };

const EMPTY_CATALOG: SqlCompletionCatalog = { schemas: [], tables: [], columns: [] };

const IS_MAC_PLATFORM =
  typeof navigator !== "undefined" &&
  /Macintosh|Mac OS X|iPhone|iPad|iPod/i.test(navigator.userAgent);

function getAsyncClipboard(): Clipboard | undefined {
  if (typeof navigator === "undefined") return undefined;
  return (navigator as { clipboard?: Clipboard }).clipboard;
}

function execClipboardCommand(command: "copy" | "paste"): boolean {
  if (typeof document === "undefined") return false;
  try {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- non-HTTPS LAN fallback when navigator.clipboard is unavailable
    return document.execCommand(command);
  } catch {
    return false;
  }
}

async function writeClipboardText(text: string): Promise<boolean> {
  const clipboard = getAsyncClipboard();
  if (clipboard !== undefined && typeof clipboard.writeText === "function") {
    try {
      await clipboard.writeText(text);
      return true;
    } catch {
      // Fall back to execCommand when Clipboard API rejects (e.g., permission denied).
    }
  }
  if (typeof document === "undefined") return false;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "-9999px";
  textarea.style.left = "-9999px";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.focus();
  textarea.select();
  try {
    return execClipboardCommand("copy");
  } finally {
    document.body.removeChild(textarea);
  }
}

async function readClipboardText(): Promise<string | null> {
  const clipboard = getAsyncClipboard();
  if (clipboard !== undefined && typeof clipboard.readText === "function") {
    try {
      return await clipboard.readText();
    } catch {
      return null;
    }
  }
  return null;
}

function resolveCopyTarget(
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

export interface SqlEditorPanelProps {
  value: string;
  onChange: (sql: string) => void;
  readOnly?: boolean;
  /** Programmatic value loads (reveal); keeps the user's undo stack intact. */
  loadValue?: { text: string; nonce: number } | null;
  onLocate?: null | { target: string; nonce: number };
  /** Schema/table/column suggestions for the completion provider. */
  completionCatalog?: SqlCompletionCatalog;
  /** Optional lazy resolver invoked by Monaco when a `<schema>.` or `<table_or_alias>.`
   * qualifier misses the local catalog cache. */
  onResolveCompletion?: (ctx: {
    qualifier?: string;
    sql: string;
  }) => Promise<SqlCompletionCatalog | null | undefined>;
  /** Invoked whenever the active text selection changes (empty string when collapsed). */
  onSelectionChange?: (selectedText: string) => void;
  /** Optional shortcut handler bound to Cmd/Ctrl+Enter inside the editor. */
  onRunShortcut?: () => void;
  /** Size overrides from the host page (default 46vh stays for other users). */
  className?: string;
  "data-testid"?: string;
}

export function SqlEditorPanel({
  value,
  onChange,
  readOnly = false,
  loadValue = null,
  onLocate = null,
  completionCatalog = EMPTY_CATALOG,
  onResolveCompletion,
  onSelectionChange,
  onRunShortcut,
  className,
  "data-testid": testId,
}: SqlEditorPanelProps) {
  const { t } = useTranslation();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const onChangeRef = useRef(onChange);
  const onSelectionChangeRef = useRef(onSelectionChange);
  const onRunShortcutRef = useRef(onRunShortcut);
  const { resolvedTheme } = useTheme();
  const valueRef = useRef(value);
  const suppressChangeRef = useRef(false);
  const [contextMenu, setContextMenu] = useState<ContextMenuState | null>(null);
  const modKey = IS_MAC_PLATFORM ? "\u2318" : "Ctrl+";

  // Keep the handler/value mirrors current without touching refs during
  // render (react-hooks/refs): declared before the editor mount effect so
  // the first creation reads fresh values.
  useEffect(() => {
    onChangeRef.current = onChange;
    onSelectionChangeRef.current = onSelectionChange;
    onRunShortcutRef.current = onRunShortcut;
    valueRef.current = value;
  });

  // The provider reads the catalog and resolver through refs so hot-reloads
  // and late catalog arrivals are always honored by the live editor instance;
  // the registration is per-panel and disposed with it (no leaked providers).
  const catalogRef = useRef(completionCatalog);
  const onResolveCompletionRef = useRef(onResolveCompletion);
  useEffect(() => {
    catalogRef.current = completionCatalog;
    onResolveCompletionRef.current = onResolveCompletion;
  }, [completionCatalog, onResolveCompletion]);

  useEffect(() => {
    configureWorkers();
    defineYearningThemes(resolvedTheme);
    const providerDisposable = monaco.languages.registerCompletionItemProvider("sql", {
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
    return () => { providerDisposable.dispose(); };
  }, [resolvedTheme]);

  useEffect(() => {
    if (containerRef.current === null) return;
    const editor = monaco.editor.create(containerRef.current, {
      value: valueRef.current,
      language: "sql",
      theme: "yearning-sql",
      automaticLayout: true,
      minimap: { enabled: false },
      // The app's registered mono face (Geist Mono variable font, global.css)
      // keeps the editor on the same typographic system as UI mono texts.
      fontFamily: '"Geist Mono", "SFMono-Regular", "JetBrains Mono", Menlo, monospace',
      fontSize: 14,
      lineHeight: 22,
      scrollBeyondLastLine: false,
      wordWrap: "on",
      renderWhitespace: "none",
      selectionHighlight: false,
      occurrencesHighlight: "off",
      quickSuggestions: { other: true, comments: false, strings: false },
      suggestOnTriggerCharacters: true,
      // Keep Monaco on the classic textarea input pipeline so native copy/paste/
      // select-all shortcuts, OS Edit menus, and CJK IME behave consistently on
      // macOS/Windows/Linux and across non-HTTPS LAN origins.
      editContext: false,
      // Render our localized ContextMenu with Copy/Cut/Paste/Select All instead
      // of Monaco's English Shadow-DOM menu (which omits Paste over HTTP and
      // omits Select All everywhere).
      contextmenu: false,
      fixedOverflowWidgets: true,
      readOnly,
    });
    editorRef.current = editor;
    const disposable = editor.onDidChangeModelContent(() => {
      if (suppressChangeRef.current) return;
      const model = editor.getModel();
      if (model !== null) onChangeRef.current(model.getValue());
    });
    const selectionDisposable = editor.onDidChangeCursorSelection(() => {
      const handler = onSelectionChangeRef.current;
      if (handler === undefined) return;
      const model = editor.getModel();
      const selection = editor.getSelection();
      if (model === null || selection === null || selection.isEmpty()) {
        handler("");
        return;
      }
      handler(model.getValueInRange(selection));
    });
    const selectAllInEditor = () => {
      const model = editor.getModel();
      if (model === null) return;
      editor.setSelection(model.getFullModelRange());
    };
    const selectAllActionDisposable = editor.addAction({
      id: "yearning.sql.selectAll",
      label: "Select All",
      keybindings: [
        monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyA,
        monaco.KeyMod.WinCtrl | monaco.KeyCode.KeyA,
      ],
      run: selectAllInEditor,
    });
    const keyDownDisposable = editor.onKeyDown((event) => {
      const { browserEvent } = event;
      if (
        (browserEvent.metaKey || browserEvent.ctrlKey) &&
        !browserEvent.altKey &&
        !browserEvent.shiftKey &&
        (browserEvent.code === "KeyA" || browserEvent.key.toLowerCase() === "a")
      ) {
        event.preventDefault();
        event.stopPropagation();
        selectAllInEditor();
      }
    });
    const contextMenuDisposable = editor.onContextMenu((event) => {
      event.event.preventDefault();
      event.event.stopPropagation();
      editor.focus();
      if (event.target.position !== null) {
        const position = event.target.position;
        const selections = editor.getSelections() ?? [];
        const insideSelection = selections.some(
          (sel) => !sel.isEmpty() && sel.containsPosition(position),
        );
        if (!insideSelection) {
          editor.setPosition(position);
        }
      }
      setContextMenu({ x: event.event.posx, y: event.event.posy });
    });
    const runActionDisposable = editor.addAction({
      id: "yearning.sql.runShortcut",
      label: "Run SQL",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter],
      run: () => {
        onRunShortcutRef.current?.();
      },
    });
    return () => {
      runActionDisposable.dispose();
      contextMenuDisposable.dispose();
      keyDownDisposable.dispose();
      selectAllActionDisposable.dispose();
      selectionDisposable.dispose();
      disposable.dispose();
      editor.getModel()?.dispose();
      editor.dispose();
      editorRef.current = null;
    };
    // The editor mounts once per panel lifetime; prop changes flow through
    // the dedicated effects below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const editor = editorRef.current;
    if (editor === null) return;
    if (editor.getOption(monaco.editor.EditorOption.readOnly) !== readOnly) {
      editor.updateOptions({ readOnly });
    }
  }, [readOnly]);

  // Programmatic loads (e.g. revealed SQL) replace the model content without
  // emitting a user-change event back into the store.
  useEffect(() => {
    const editor = editorRef.current;
    const model = editor?.getModel();
    if (editor == null || model == null || loadValue === null) return;
    if (model.getValue() === loadValue.text) return;
    suppressChangeRef.current = true;
    try {
      model.pushEditOperations(
        [],
        [{ range: model.getFullModelRange(), text: loadValue.text }],
        () => null,
      );
      onSelectionChangeRef.current?.("");
    } finally {
      suppressChangeRef.current = false;
    }
  }, [loadValue]);

  // Statement locating: select the first occurrence of the target snippet.
  useEffect(() => {
    const editor = editorRef.current;
    const model = editor?.getModel();
    if (editor == null || model == null || onLocate === null) return;
    const text = model.getValue();
    const index = text.indexOf(onLocate.target);
    if (index < 0) return;
    const start = model.getPositionAt(index);
    const end = model.getPositionAt(index + onLocate.target.length);
    editor.setSelection({ startLineNumber: start.lineNumber, startColumn: start.column, endLineNumber: end.lineNumber, endColumn: end.column });
    editor.revealRangeInCenter({ startLineNumber: start.lineNumber, startColumn: start.column, endLineNumber: end.lineNumber, endColumn: end.column });
    editor.focus();
  }, [onLocate]);

  const handleCopy = useCallback(() => {
    const editor = editorRef.current;
    const model = editor?.getModel();
    if (editor == null || model == null) return;
    const target = resolveCopyTarget(editor, model);
    if (target === null) {
      editor.focus();
      return;
    }
    void writeClipboardText(target.text).then((ok) => {
      if (editorRef.current !== editor) return;
      editor.focus();
      if (!ok) {
        appToast.error(
          t("sqlEditor.contextMenu.copyFailed", { shortcut: `${modKey}C` }),
        );
      }
    });
  }, [modKey, t]);

  const handleCut = useCallback(() => {
    const editor = editorRef.current;
    const model = editor?.getModel();
    if (editor == null || model == null || readOnly) return;
    const selection = editor.getSelection();
    let range: monaco.IRange | null = null;
    if (selection !== null && !selection.isEmpty()) {
      range = selection;
    } else {
      const lineNumber = selection?.startLineNumber ?? 1;
      if (model.getLineContent(lineNumber) !== "") {
        range =
          lineNumber < model.getLineCount()
            ? { startLineNumber: lineNumber, startColumn: 1, endLineNumber: lineNumber + 1, endColumn: 1 }
            : { startLineNumber: lineNumber, startColumn: 1, endLineNumber: lineNumber, endColumn: model.getLineMaxColumn(lineNumber) };
      }
    }
    if (range === null) {
      editor.focus();
      return;
    }
    const text = model.getValueInRange(range);
    const targetRange = range;
    void writeClipboardText(text).then((ok) => {
      if (editorRef.current !== editor) return;
      if (ok) {
        editor.pushUndoStop();
        editor.executeEdits("yearning.cut", [{ range: targetRange, text: "" }]);
        editor.pushUndoStop();
      } else {
        appToast.error(
          t("sqlEditor.contextMenu.copyFailed", { shortcut: `${modKey}X` }),
        );
      }
      editor.focus();
    });
  }, [modKey, readOnly, t]);

  const handlePaste = useCallback(() => {
    const editor = editorRef.current;
    if (editor === null || readOnly) return;
    const fallbackExecPaste = (): void => {
      if (editorRef.current !== editor) return;
      editor.focus();
      if (!execClipboardCommand("paste")) {
        appToast.info(
          t("sqlEditor.contextMenu.pasteShortcutHint", { shortcut: `${modKey}V` }),
        );
      }
    };
    const clipboard = getAsyncClipboard();
    if (clipboard === undefined || typeof clipboard.readText !== "function") {
      fallbackExecPaste();
      return;
    }
    void readClipboardText().then((text) => {
      if (editorRef.current !== editor) return;
      if (text !== null) {
        const selection = editor.getSelection();
        const pos = editor.getPosition();
        const targetRange: monaco.IRange = selection ?? {
          startLineNumber: pos?.lineNumber ?? 1,
          startColumn: pos?.column ?? 1,
          endLineNumber: pos?.lineNumber ?? 1,
          endColumn: pos?.column ?? 1,
        };
        editor.pushUndoStop();
        editor.executeEdits("yearning.paste", [
          { range: targetRange, text, forceMoveMarkers: true },
        ]);
        editor.pushUndoStop();
        editor.focus();
        return;
      }
      fallbackExecPaste();
    });
  }, [modKey, readOnly, t]);

  const handleSelectAll = useCallback(() => {
    const editor = editorRef.current;
    const model = editor?.getModel();
    if (editor == null || model == null) return;
    editor.focus();
    editor.setSelection(model.getFullModelRange());
  }, []);

  const contextMenuItems: ContextMenuItem[] = [
    {
      icon: Copy,
      label: t("sqlEditor.contextMenu.copy"),
      shortcut: `${modKey}C`,
      testId: "sql-editor-menu-copy",
      onClick: handleCopy,
    },
    ...(readOnly
      ? []
      : [
          {
            icon: Scissors,
            label: t("sqlEditor.contextMenu.cut"),
            shortcut: `${modKey}X`,
            testId: "sql-editor-menu-cut",
            onClick: handleCut,
          },
          {
            icon: ClipboardPaste,
            label: t("sqlEditor.contextMenu.paste"),
            shortcut: `${modKey}V`,
            testId: "sql-editor-menu-paste",
            onClick: handlePaste,
          },
        ]),
    {
      icon: TextSelect,
      label: t("sqlEditor.contextMenu.selectAll"),
      shortcut: `${modKey}A`,
      testId: "sql-editor-menu-select-all",
      onClick: handleSelectAll,
    },
  ];

  return (
    <>
      <div
        ref={containerRef}
        className={cn(
          "min-h-[240px] h-[46vh] border-input rounded-md border overflow-hidden",
          className,
        )}
        data-testid={testId}
        data-slot="sql-editor-panel"
      />
      {contextMenu !== null && (
        <ContextMenu
          position={contextMenu}
          items={contextMenuItems}
          onClose={() => { setContextMenu(null); }}
          overlayTestId="sql-editor-context-menu-overlay"
          panelTestId="sql-editor-context-menu"
        />
      )}
    </>
  );
}

/** Hard height cap for the read-only viewer (§18.45): bulk reveals scroll
 * inside the cap; Monaco virtualizes line rendering, so a hundred-thousand
 * line reveal costs the same DOM as a one-liner. */
export const SQL_READONLY_MAX_HEIGHT = 320;

/**
 * Read-only SQL viewer for revealed plaintext (order detail, §18.45 owner
 * request): the same Monaco surface as the editor — SQL highlighting, line
 * numbers and code folding for bulk statements — in a container that grows
 * with the content up to the hard cap so no reveal can stretch the page.
 * Deliberately not an editor prop: no change events, no completion provider,
 * no context menu — it is a viewer, and the reveal plaintext stays in
 * component memory exactly as before.
 */
export function SqlReadOnlyViewer({
  value,
  "data-testid": testId,
}: {
  value: string;
  "data-testid"?: string;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const { resolvedTheme } = useTheme();

  useEffect(() => {
    configureWorkers();
    defineYearningThemes(resolvedTheme);
  }, [resolvedTheme]);

  useEffect(() => {
    const container = containerRef.current;
    if (container === null) return;
    const fit = (): void => {
      const editor = editorRef.current;
      if (editor === null) return;
      const height = Math.min(editor.getContentHeight(), SQL_READONLY_MAX_HEIGHT);
      container.style.height = `${String(height)}px`;
    };
    const editor = monaco.editor.create(container, {
      value,
      language: "sql",
      theme: "yearning-sql",
      automaticLayout: true,
      minimap: { enabled: false },
      fontSize: 13,
      lineHeight: 20,
      scrollBeyondLastLine: false,
      wordWrap: "on",
      renderWhitespace: "none",
      readOnly: true,
      lineNumbersMinChars: 3,
      renderLineHighlight: "none",
      overviewRulerLanes: 0,
      contextmenu: false,
      stickyScroll: { enabled: false },
      guides: { indentation: false },
      padding: { top: 10, bottom: 10 },
      scrollbar: { verticalScrollbarSize: 8, horizontalScrollbarSize: 8, useShadows: false },
    });
    editorRef.current = editor;
    const sizeDisposable = editor.onDidContentSizeChange(fit);
    fit();
    return () => {
      sizeDisposable.dispose();
      editor.getModel()?.dispose();
      editor.dispose();
      editorRef.current = null;
    };
    // The viewer mounts once per reveal; a value swap flows through the
    // effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Defensive value sync (the card remounts the viewer per reveal, but a
  // same-mount swap must still land): read-only models never emit the
  // user-change path, so a plain setValue is enough.
  useEffect(() => {
    const editor = editorRef.current;
    const model = editor?.getModel();
    if (editor == null || model == null || model.getValue() === value) return;
    model.setValue(value);
  }, [value]);

  return (
    <div
      ref={containerRef}
      style={{ height: SQL_READONLY_MAX_HEIGHT }}
      className="border-input overflow-hidden rounded-md border"
      data-testid={testId}
      data-slot="sql-readonly-viewer"
    />
  );
}
