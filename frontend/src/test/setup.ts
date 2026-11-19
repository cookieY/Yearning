import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterAll, afterEach, beforeAll, vi } from "vitest";
import { server } from "./msw/server";
import { applyScenarioToServer } from "@/shared/mock/scenarios";

// jsdom's native matchMedia always reports false, which would leave motion
// animations (submission wizard) running on rAF inside jsdom. Override
// unconditionally: report prefers-reduced-motion as matching so
// reducedMotion:"user" skips animations everywhere in component tests, and
// keep other media queries false. Individual tests may still override with
// vi.stubGlobal as theme-provider.test.tsx does.
Object.defineProperty(window, "matchMedia", {
  writable: true,
  configurable: true,
  value: vi.fn().mockImplementation((query: string) => ({
    matches: query.includes("prefers-reduced-motion"),
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })),
});

// jsdom does not implement the Web Animations API on elements; base-ui's
// ScrollArea viewport calls Element.getAnimations() unguarded after mount.
// Stub it as an empty list so the shared ScrollArea renders in component
// tests without unhandled TypeErrors — but keep base-ui's own animations
// disabled: with getAnimations present, useAnimationsFinished would otherwise
// switch popups/dialogs to the async (rAF + promise) close path and unmount
// content a frame later than the synchronous jsdom behavior the tests assert.
if (typeof Element.prototype.getAnimations !== "function") {
  Object.defineProperty(Element.prototype, "getAnimations", {
    writable: true,
    configurable: true,
    value: () => [],
  });
}
(globalThis as { BASE_UI_ANIMATIONS_DISABLED?: boolean }).BASE_UI_ANIMATIONS_DISABLED = true;

// jsdom reports an en-US browser language; pin the locale so component tests
// assert against the default zh-CN vocabulary deterministically.
window.localStorage.setItem("yearning-locale", "zh-CN");

beforeAll(() => {
  server.listen({ onUnhandledRequest: "bypass" });
  applyScenarioToServer(server, "ready");
});
afterEach(() => {
  cleanup();
  server.resetHandlers();
});
afterAll(() => {
  server.close();
});
