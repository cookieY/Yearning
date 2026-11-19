import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "@/shared/i18n";

const toastInfo = vi.fn();
const toastError = vi.fn();

vi.mock("@/shared/components/shadcn-dashboard/sonner/sonner-08", () => ({
  appToast: {
    info: (...args: unknown[]) => { toastInfo(...args); },
    error: (...args: unknown[]) => { toastError(...args); },
    success: vi.fn(),
  },
}));

vi.mock("@/app/providers/theme-provider", () => ({
  useTheme: () => ({ resolvedTheme: "light" as const }),
}));

vi.mock("monaco-editor/editor/editor.worker.js?worker", () => ({
  default: vi.fn(),
}));

vi.mock("monaco-editor/languages/definitions/sql/register.js", () => ({}));

interface MockModel {
  getValue: () => string;
  getValueInRange: (range: { startLineNumber: number; startColumn: number; endLineNumber: number; endColumn: number }) => string;
  getFullModelRange: () => { startLineNumber: number; startColumn: number; endLineNumber: number; endColumn: number };
  getLineCount: () => number;
  getLineContent: (line: number) => string;
  getLineMaxColumn: (line: number) => number;
  pushEditOperations: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
}

interface MockSelection {
  startLineNumber: number;
  startColumn: number;
  endLineNumber: number;
  endColumn: number;
  isEmpty: () => boolean;
  containsPosition: (pos: { lineNumber: number; column: number }) => boolean;
}

const monacoState = {
  lastCreateOptions: null as Record<string, unknown> | null,
  contextMenuHandler: null as ((event: {
    event: { preventDefault: () => void; stopPropagation: () => void; posx: number; posy: number };
    target: { position: { lineNumber: number; column: number } | null };
  }) => void) | null,
  text: "SELECT 1;\nSELECT 2;",
  selection: null as MockSelection | null,
  focus: vi.fn(),
  setPosition: vi.fn(),
  setSelection: vi.fn(),
  pushUndoStop: vi.fn(),
  executeEdits: vi.fn(),
};

function makeSelection(
  startLineNumber: number,
  startColumn: number,
  endLineNumber: number,
  endColumn: number,
): MockSelection {
  return {
    startLineNumber,
    startColumn,
    endLineNumber,
    endColumn,
    isEmpty: () => startLineNumber === endLineNumber && startColumn === endColumn,
    containsPosition: (pos) =>
      pos.lineNumber >= startLineNumber &&
      pos.lineNumber <= endLineNumber &&
      (pos.lineNumber !== startLineNumber || pos.column >= startColumn) &&
      (pos.lineNumber !== endLineNumber || pos.column <= endColumn),
  };
}

vi.mock("monaco-editor/editor/editor.main.js", () => {
  const model: MockModel = {
    getValue: () => monacoState.text,
    getValueInRange: () => monacoState.text,
    getFullModelRange: () => ({
      startLineNumber: 1,
      startColumn: 1,
      endLineNumber: 2,
      endColumn: 10,
    }),
    getLineCount: () => monacoState.text.split("\n").length,
    getLineContent: (line: number) => monacoState.text.split("\n")[line - 1] ?? "",
    getLineMaxColumn: (line: number) => (monacoState.text.split("\n")[line - 1]?.length ?? 0) + 1,
    pushEditOperations: vi.fn(),
    dispose: vi.fn(),
  };

  return {
    KeyMod: { CtrlCmd: 2048, WinCtrl: 256 },
    KeyCode: { KeyA: 31, Enter: 3 },
    languages: {
      CompletionItemKind: { Keyword: 1, Module: 2, Class: 3, Field: 4 },
      registerCompletionItemProvider: () => ({ dispose: vi.fn() }),
    },
    editor: {
      EditorOption: { readOnly: 91 },
      defineTheme: vi.fn(),
      create: (_el: HTMLElement, options: Record<string, unknown>) => {
        monacoState.lastCreateOptions = options;
        return {
          getModel: () => model,
          getSelection: () => monacoState.selection,
          getSelections: () => (monacoState.selection === null ? [] : [monacoState.selection]),
          getPosition: () => ({ lineNumber: 1, column: 1 }),
          getOption: () => Boolean(options.readOnly),
          updateOptions: vi.fn(),
          focus: () => { monacoState.focus(); },
          setPosition: (pos: unknown) => { monacoState.setPosition(pos); },
          setSelection: (sel: unknown) => { monacoState.setSelection(sel); },
          pushUndoStop: () => { monacoState.pushUndoStop(); },
          executeEdits: (source: string, edits: Array<{ range: unknown; text: string }>) => {
            monacoState.executeEdits(source, edits);
            return true;
          },
          onDidChangeModelContent: () => ({ dispose: vi.fn() }),
          onDidChangeCursorSelection: () => ({ dispose: vi.fn() }),
          onKeyDown: () => ({ dispose: vi.fn() }),
          onContextMenu: (cb: typeof monacoState.contextMenuHandler) => {
            monacoState.contextMenuHandler = cb;
            return { dispose: vi.fn() };
          },
          addAction: () => ({ dispose: vi.fn() }),
          revealRangeInCenter: vi.fn(),
          dispose: vi.fn(),
        };
      },
    },
  };
});

import { SqlEditorPanel } from "./sql-editor-panel";

function openContextMenu(pos = { lineNumber: 1, column: 3 }): void {
  act(() => {
    monacoState.contextMenuHandler?.({
      event: {
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
        posx: 120,
        posy: 140,
      },
      target: { position: pos },
    });
  });
}

describe("SqlEditorPanel clipboard and context menu", () => {
  const originalClipboard = navigator.clipboard;

  beforeEach(() => {
    monacoState.lastCreateOptions = null;
    monacoState.contextMenuHandler = null;
    monacoState.text = "SELECT 1;\nSELECT 2;";
    monacoState.selection = makeSelection(1, 1, 2, 10);
    monacoState.focus.mockReset();
    monacoState.setPosition.mockReset();
    monacoState.setSelection.mockReset();
    monacoState.pushUndoStop.mockReset();
    monacoState.executeEdits.mockReset();
    toastInfo.mockReset();
    toastError.mockReset();
  });

  afterEach(() => {
    Object.defineProperty(navigator, "clipboard", {
      value: originalClipboard,
      configurable: true,
    });
  });

  it("configures Monaco with editContext disabled and custom localized context menu", () => {
    render(<SqlEditorPanel value="SELECT 1;" onChange={vi.fn()} data-testid="sql-editor" />);
    expect(monacoState.lastCreateOptions).toMatchObject({
      editContext: false,
      contextmenu: false,
      fixedOverflowWidgets: true,
    });
  });

  it("supports copy, cut, paste, and select-all from the right-click context menu", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const readText = vi.fn().mockResolvedValue("SELECT 42;");
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText, readText },
      configurable: true,
    });

    render(<SqlEditorPanel value={monacoState.text} onChange={vi.fn()} data-testid="sql-editor" />);

    openContextMenu();
    expect(await screen.findByTestId("sql-editor-context-menu")).toBeInTheDocument();
    // Clicking inside an active selection preserves it instead of collapsing the caret.
    expect(monacoState.setPosition).not.toHaveBeenCalled();

    fireEvent.click(screen.getByTestId("sql-editor-menu-copy"));
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith("SELECT 1;\nSELECT 2;");
    });

    openContextMenu();
    fireEvent.click(await screen.findByTestId("sql-editor-menu-cut"));
    await waitFor(() => {
      expect(monacoState.executeEdits).toHaveBeenCalledWith(
        "yearning.cut",
        expect.arrayContaining([expect.objectContaining({ text: "" })]),
      );
    });

    openContextMenu();
    fireEvent.click(await screen.findByTestId("sql-editor-menu-paste"));
    await waitFor(() => {
      expect(readText).toHaveBeenCalled();
      expect(monacoState.executeEdits).toHaveBeenCalledWith(
        "yearning.paste",
        expect.arrayContaining([expect.objectContaining({ text: "SELECT 42;", forceMoveMarkers: true })]),
      );
    });

    openContextMenu();
    fireEvent.click(await screen.findByTestId("sql-editor-menu-select-all"));
    expect(monacoState.setSelection).toHaveBeenCalledWith({
      startLineNumber: 1,
      startColumn: 1,
      endLineNumber: 2,
      endColumn: 10,
    });
  });

  it("shows a shortcut hint toast when paste is invoked without async clipboard access", async () => {
    Object.defineProperty(navigator, "clipboard", {
      value: undefined,
      configurable: true,
    });

    render(<SqlEditorPanel value={monacoState.text} onChange={vi.fn()} data-testid="sql-editor" />);
    openContextMenu();
    fireEvent.click(await screen.findByTestId("sql-editor-menu-paste"));

    expect(toastInfo).toHaveBeenCalledTimes(1);
    expect(monacoState.executeEdits).not.toHaveBeenCalled();
  });

  it("omits cut and paste items when readOnly is true", async () => {
    render(<SqlEditorPanel value={monacoState.text} onChange={vi.fn()} readOnly data-testid="sql-editor" />);
    openContextMenu();
    expect(await screen.findByTestId("sql-editor-menu-copy")).toBeInTheDocument();
    expect(screen.getByTestId("sql-editor-menu-select-all")).toBeInTheDocument();
    expect(screen.queryByTestId("sql-editor-menu-cut")).toBeNull();
    expect(screen.queryByTestId("sql-editor-menu-paste")).toBeNull();
  });
});
