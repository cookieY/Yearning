import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ClipboardPaste, Copy, Scissors, TextSelect } from "lucide-react";

import { ContextMenu, type ContextMenuItem, type ContextMenuState } from "@/shared/components/context-menu";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { cn } from "@/shared/lib/utils";
// Trimmed editor.api + minimal SQL contributions (suggest, find, SQL monarch
// grammar, and basic editing actions), omitting TypeScript/LSP/other grammars.
// The chunk still loads lazily via LazySqlEditorPanel.
import * as monaco from "monaco-editor/editor/editor.api.js";
// SQL is a monarch-highlighted basic language: tokenization runs on the main
// thread and only the general editor worker is needed. Bundled workers only —
// no runtime CDN or floating remote loading (UI spec §14).
import EditorWorker from "monaco-editor/editor/editor.worker.js?worker";
import { useTheme } from "@/app/providers/theme-provider";
import "./monaco-sql-contribs";

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

/** Optional schema/table/column catalog feeding the SQL completion provider.
 * Schemas come from the frozen flow's schema mappings; tables and columns
 * require a metadata read surface the submission journey does not have yet
 * (contract: metadata endpoints are query-session scoped only), so the
 * catalog ships empty there and lights up when a read surface exists. */
import type { SqlCompletionCatalog } from "./sql-completions";
import {
  IS_MAC_PLATFORM,
  defineYearningThemes,
  execClipboardCommand,
  getAsyncClipboard,
  readClipboardText,
  registerSqlCompletionProvider,
  resolveCopyTarget,
  writeClipboardText,
} from "./sql-editor-support";

export type { SqlCompletionCatalog };

const EMPTY_CATALOG: SqlCompletionCatalog = { schemas: [], tables: [], columns: [] };

export interface SqlEditorPanelProps {
  value: string;
  onChange: (sql: string) => void;
  readOnly?: boolean;
  /** Programmatic value loads (reveal); keeps the user's undo stack intact. */
  loadValue?: { text: string; nonce: number } | null;
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
    const providerDisposable = registerSqlCompletionProvider(catalogRef, onResolveCompletionRef);
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
