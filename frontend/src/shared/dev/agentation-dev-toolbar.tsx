import { Agentation } from "agentation";
import { Check, Files } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  formatAllAgentationMarkdown,
  loadAllAgentationAnnotations,
} from "./agentation-cleanup";
import { writeClipboardText } from "@/shared/lib/clipboard";

const PASSTHROUGH_ATTR = "data-feedback-toolbar";
const PASSTHROUGH_VALUE = "alt-passthrough";
const NATIVE_INJECTED_ATTR = "data-agentation-copy-all";
const NATIVE_STYLE_ID = "agentation-copy-all-style";

interface NativeToolbarSlot {
  container: HTMLElement;
  buttonClass: string;
  tooltipClass: string;
}

/**
 * Installs a `window` capture-phase listener so `Option (Alt) + Click` passes
 * straight through Agentation to the underlying app without closing the
 * annotation toolbar.
 */
function installAltClickPassthrough(): () => void {
  const clearPassthrough = (root: HTMLElement) => {
    if (root.getAttribute(PASSTHROUGH_ATTR) === PASSTHROUGH_VALUE) {
      root.removeAttribute(PASSTHROUGH_ATTR);
    }
  };

  const onCaptureMouseDown = (event: MouseEvent) => {
    if (!event.altKey) {
      return;
    }
    const root = document.getElementById("root");
    if (root === null) {
      return;
    }
    const target = event.target;
    if (!(target instanceof Node) || !root.contains(target)) {
      return;
    }
    root.setAttribute(PASSTHROUGH_ATTR, PASSTHROUGH_VALUE);
    queueMicrotask(() => {
      clearPassthrough(root);
    });
  };

  const onCaptureClick = (event: MouseEvent) => {
    if (!event.altKey) {
      return;
    }
    const root = document.getElementById("root");
    if (root === null) {
      return;
    }
    const rawTarget = event.target;
    if (!(rawTarget instanceof Node) || !root.contains(rawTarget)) {
      return;
    }
    const dispatchTarget =
      rawTarget instanceof Element ? rawTarget : rawTarget.parentElement;
    if (dispatchTarget === null) {
      return;
    }

    // Stop the original Alt+Click so browsers do not download <a href> targets
    // and React Router does not ignore the click via isModifiedEvent(altKey).
    event.preventDefault();
    event.stopPropagation();

    root.setAttribute(PASSTHROUGH_ATTR, PASSTHROUGH_VALUE);
    try {
      dispatchTarget.dispatchEvent(
        new MouseEvent("click", {
          bubbles: true,
          cancelable: true,
          detail: event.detail,
          screenX: event.screenX,
          screenY: event.screenY,
          clientX: event.clientX,
          clientY: event.clientY,
          button: event.button,
          buttons: event.buttons,
          altKey: false,
          ctrlKey: event.ctrlKey,
          metaKey: event.metaKey,
          shiftKey: event.shiftKey,
        }),
      );
    } finally {
      clearPassthrough(root);
    }
  };

  const onBubble = () => {
    const root = document.getElementById("root");
    if (root !== null) {
      clearPassthrough(root);
    }
  };

  window.addEventListener("mousedown", onCaptureMouseDown, true);
  window.addEventListener("click", onCaptureClick, true);
  window.addEventListener("mousedown", onBubble, false);
  window.addEventListener("click", onBubble, false);

  return () => {
    window.removeEventListener("mousedown", onCaptureMouseDown, true);
    window.removeEventListener("click", onCaptureClick, true);
    window.removeEventListener("mousedown", onBubble, false);
    window.removeEventListener("click", onBubble, false);
    const root = document.getElementById("root");
    if (root !== null) {
      clearPassthrough(root);
    }
  };
}

function getAgentationShadowRoot(): ShadowRoot | null {
  return document.querySelector("agentation-toolbar")?.shadowRoot ?? null;
}

function useCurrentPathname(onBeforeChange: () => void): string {
  const [pathname, setPathname] = useState(() => window.location.pathname);

  useEffect(() => {
    const sync = () => {
      onBeforeChange();
      setPathname(window.location.pathname);
    };

    // eslint-disable-next-line @typescript-eslint/unbound-method -- Restored cleanly on unmount.
    const originalPushState = window.history.pushState;
    // eslint-disable-next-line @typescript-eslint/unbound-method -- Restored cleanly on unmount.
    const originalReplaceState = window.history.replaceState;

    window.history.pushState = function pushState(...args) {
      originalPushState.apply(this, args);
      sync();
    };
    window.history.replaceState = function replaceState(...args) {
      originalReplaceState.apply(this, args);
      sync();
    };
    window.addEventListener("popstate", sync);

    return () => {
      window.history.pushState = originalPushState;
      window.history.replaceState = originalReplaceState;
      window.removeEventListener("popstate", sync);
    };
  }, [onBeforeChange]);

  return pathname;
}

/**
 * Mounts a host container right after Agentation's native Copy button inside
 * `<agentation-toolbar>`'s open Shadow DOM so our cross-page Copy All button
 * inherits the exact native pill styling and tooltip behavior.
 * Also re-opens feedback mode automatically when `shouldReopenRef.current` is
 * set (e.g. after an Option+Click route change).
 */
function useNativeToolbarSlot(
  pathname: string,
  shouldReopenRef: { current: boolean },
): NativeToolbarSlot | null {
  const [slot, setSlot] = useState<NativeToolbarSlot | null>(null);

  useEffect(() => {
    let mountedContainer: HTMLElement | null = null;
    let shadowObserver: MutationObserver | null = null;

    const ensureSlot = () => {
      const shadow = getAgentationShadowRoot();
      if (shadow === null) {
        return;
      }

      if (shadowObserver === null) {
        shadowObserver = new MutationObserver(() => {
          ensureSlot();
        });
        shadowObserver.observe(shadow, { childList: true, subtree: true });
      }

      if (shouldReopenRef.current) {
        const launcher = shadow.querySelector('button[aria-label="Start feedback mode"]');
        if (launcher instanceof HTMLButtonElement) {
          shouldReopenRef.current = false;
          launcher.click();
        }
      }

      if (shadow.getElementById(NATIVE_STYLE_ID) === null) {
        const style = document.createElement("style");
        style.id = NATIVE_STYLE_ID;
        style.textContent = `
          [class*="toolbarContainer"][class*="expanded"]:has([${NATIVE_INJECTED_ATTR}="true"]) {
            width: max-content !important;
          }
        `;
        shadow.appendChild(style);
      }

      const toolbar = shadow.querySelector("[data-feedback-toolbar]");
      if (!(toolbar instanceof HTMLElement)) {
        return;
      }
      const existing = toolbar.querySelector(`[${NATIVE_INJECTED_ATTR}="true"]`);
      if (existing instanceof HTMLElement) {
        return;
      }
      const copyButton = toolbar.querySelector('button[aria-label^="Copy"]');
      if (!(copyButton instanceof HTMLButtonElement)) {
        return;
      }
      const copyWrapper = copyButton.parentElement;
      if (copyWrapper === null) {
        return;
      }
      const tooltipEl = copyWrapper.querySelector("span");
      const container = document.createElement("div");
      container.setAttribute(NATIVE_INJECTED_ATTR, "true");
      container.className = copyWrapper.className;
      copyWrapper.insertAdjacentElement("afterend", container);
      mountedContainer = container;

      const baseButtonClass = copyButton.className.split(" ")[0] ?? "";
      setSlot({
        container,
        buttonClass: baseButtonClass,
        tooltipClass: tooltipEl?.className ?? "",
      });
    };

    ensureSlot();
    const bodyObserver = new MutationObserver(() => {
      ensureSlot();
    });
    bodyObserver.observe(document.body, { childList: true, subtree: true });

    return () => {
      bodyObserver.disconnect();
      shadowObserver?.disconnect();
      if (mountedContainer !== null) {
        mountedContainer.remove();
        mountedContainer = null;
      }
      setSlot(null);
    };
  }, [pathname, shouldReopenRef]);

  return slot;
}

/**
 * Dev-only wrapper around `<Agentation />` adding:
 * 1. Route-aware `key={pathname}` so SPA `history.pushState` navigation
 *    switches Agentation's active `localStorage` bucket per page, while
 *    preserving active feedback mode across route transitions.
 * 2. Cross-page aggregated copy injected directly into Agentation's native
 *    toolbar pill (plus `Alt+Shift+C`).
 * 3. `Option (Alt) + Click` passthrough so links/buttons remain clickable
 *    while annotation mode is active.
 */
export function AgentationDevToolbar() {
  const shouldReopenRef = useRef(false);
  const captureActiveBeforeRouteChange = useCallback(() => {
    const shadow = getAgentationShadowRoot();
    if (shadow !== null && shadow.querySelector('button[aria-expanded="true"]') !== null) {
      shouldReopenRef.current = true;
    }
  }, []);

  const pathname = useCurrentPathname(captureActiveBeforeRouteChange);
  const slot = useNativeToolbarSlot(pathname, shouldReopenRef);
  const [status, setStatus] = useState<string | null>(null);

  const handleCopyAll = useCallback(async () => {
    const pages = loadAllAgentationAnnotations();
    if (pages.length === 0) {
      setStatus("No annotations");
      return;
    }
    const markdown = formatAllAgentationMarkdown(pages);
    const total = pages.reduce((sum, page) => sum + page.annotations.length, 0);
    const ok = await writeClipboardText(markdown);
    setStatus(ok ? `Copied ${String(pages.length)}p · ${String(total)} items` : "Copy failed");
  }, []);

  useEffect(() => {
    return installAltClickPassthrough();
  }, []);

  useEffect(() => {
    if (status === null) {
      return;
    }
    const timer = window.setTimeout(() => {
      setStatus(null);
    }, 2200);
    return () => {
      window.clearTimeout(timer);
    };
  }, [status]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.altKey && event.shiftKey && event.key.toLowerCase() === "c") {
        event.preventDefault();
        void handleCopyAll();
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === "f") {
        window.requestAnimationFrame(() => {
          const active = getAgentationShadowRoot()?.activeElement;
          if (active instanceof HTMLElement) {
            active.blur();
          }
        });
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [handleCopyAll]);

  return (
    <>
      <Agentation key={pathname} />
      {slot !== null &&
        createPortal(
          <>
            <button
              type="button"
              className={slot.buttonClass}
              onClick={(event) => {
                event.stopPropagation();
                void handleCopyAll();
              }}
              aria-label="Copy all pages feedback"
              title="Copy all pages annotations (Alt + Shift + C) · Hold Option + Click to pass through"
            >
              {status !== null && status.startsWith("Copied ") ? (
                <Check size={18} strokeWidth={2} />
              ) : (
                <Files size={18} strokeWidth={1.8} />
              )}
            </button>
            <span className={slot.tooltipClass}>
              {status ?? "Copy All Pages (Alt+Shift+C)"}
            </span>
          </>,
          slot.container,
        )}
    </>
  );
}

export { installAltClickPassthrough };
