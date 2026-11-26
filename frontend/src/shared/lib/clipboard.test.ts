import { afterEach, describe, expect, it, vi } from "vitest";
import { writeClipboardText } from "./clipboard";

describe("writeClipboardText", () => {
  const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  const originalExecCommand = Object.getOwnPropertyDescriptor(document, "execCommand");

  afterEach(() => {
    if (originalClipboard) {
      Object.defineProperty(navigator, "clipboard", originalClipboard);
    } else {
      Reflect.deleteProperty(navigator, "clipboard");
    }
    if (originalExecCommand) {
      Object.defineProperty(document, "execCommand", originalExecCommand);
    } else {
      Reflect.deleteProperty(document, "execCommand");
    }
    vi.restoreAllMocks();
  });

  it("uses navigator.clipboard.writeText when available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    const ok = await writeClipboardText("SELECT 1;");
    expect(ok).toBe(true);
    expect(writeText).toHaveBeenCalledWith("SELECT 1;");
  });

  it("falls back to readonly textarea + execCommand copy on non-HTTPS LAN origins", async () => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: undefined,
    });
    const execCommand = vi.fn().mockImplementation((cmd: string) => {
      expect(cmd).toBe("copy");
      const active = document.activeElement as HTMLTextAreaElement | null;
      expect(active?.tagName).toBe("TEXTAREA");
      expect(active?.readOnly).toBe(true);
      expect(active?.value).toBe("SELECT balance FROM accounts WHERE balance >= 500;");
      return true;
    });
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      writable: true,
      value: execCommand,
    });

    const ok = await writeClipboardText("SELECT balance FROM accounts WHERE balance >= 500;");
    expect(ok).toBe(true);
    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(document.querySelectorAll("textarea")).toHaveLength(0);
  });

  it("returns false when both async clipboard and execCommand fail", async () => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockRejectedValue(new Error("denied")) },
    });
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      writable: true,
      value: vi.fn().mockReturnValue(false),
    });

    const ok = await writeClipboardText("SELECT 1;");
    expect(ok).toBe(false);
  });
});
