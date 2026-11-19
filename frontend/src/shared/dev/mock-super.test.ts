import { afterEach, describe, expect, it, vi } from "vitest";
import {
  SUPER_STORAGE_KEY,
  holdsSessionSlot,
  mockSuperSession,
} from "@/shared/dev/mock-super";

afterEach(() => {
  vi.unstubAllEnvs();
  window.localStorage.clear();
});

describe("mockSuperSession & holdsSessionSlot", () => {
  it("disables mock super mode and enforces exact actor UUID match when VITE_ENABLE_MOCK is not enabled", () => {
    vi.stubEnv("MODE", "development");
    vi.stubEnv("VITE_ENABLE_MOCK", "");
    window.localStorage.setItem(SUPER_STORAGE_KEY, "1");

    expect(mockSuperSession()).toBe(false);
    expect(holdsSessionSlot("approver-uuid", "admin-uuid")).toBe(false);
    expect(holdsSessionSlot("approver-uuid", "approver-uuid")).toBe(true);
  });

  it("enables mock super mode in development only when VITE_ENABLE_MOCK is true and honors localStorage override", () => {
    vi.stubEnv("MODE", "development");
    vi.stubEnv("VITE_ENABLE_MOCK", "true");

    expect(mockSuperSession()).toBe(true);
    expect(holdsSessionSlot("approver-uuid", "admin-uuid")).toBe(true);

    window.localStorage.setItem(SUPER_STORAGE_KEY, "0");
    expect(mockSuperSession()).toBe(false);
    expect(holdsSessionSlot("approver-uuid", "admin-uuid")).toBe(false);

    window.localStorage.setItem(SUPER_STORAGE_KEY, "1");
    expect(mockSuperSession()).toBe(true);
    expect(holdsSessionSlot("approver-uuid", "admin-uuid")).toBe(true);
  });
});
