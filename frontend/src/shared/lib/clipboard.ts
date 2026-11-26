/**
 * Canonical clipboard helper supporting both secure contexts (Async Clipboard API)
 * and non-HTTPS LAN HTTP origins via a hidden read-only textarea + execCommand("copy")
 * fallback (ENG-001 shared primitive).
 *
 * Callers must inspect the resolved boolean rather than assuming rejection on failure;
 * SEC-0035 audited copy flows must only record an audit event when this resolves to true.
 */

export function getAsyncClipboard(): Clipboard | undefined {
  if (typeof navigator === "undefined") return undefined;
  return (navigator as { clipboard?: Clipboard }).clipboard;
}

export function execClipboardCommand(command: "copy" | "paste"): boolean {
  if (typeof document === "undefined") return false;
  try {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- non-HTTPS LAN fallback when navigator.clipboard is unavailable
    return document.execCommand(command);
  } catch {
    return false;
  }
}

export async function writeClipboardText(text: string): Promise<boolean> {
  const clipboard = getAsyncClipboard();
  if (clipboard !== undefined && typeof clipboard.writeText === "function") {
    try {
      await clipboard.writeText(text);
      return true;
    } catch {
      // Fall back to execCommand when Clipboard API rejects (e.g., permission denied or non-secure HTTP).
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

export async function readClipboardText(): Promise<string | null> {
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
