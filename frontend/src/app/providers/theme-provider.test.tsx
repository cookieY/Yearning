import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ThemeProvider, useTheme, type Theme } from "@/app/providers/theme-provider";

const matchMediaMock = vi.fn();

function ThemeProbe({ onTheme }: { onTheme: (theme: Theme, resolved: "light" | "dark") => void }) {
  const { theme, resolvedTheme, setTheme } = useTheme();
  return (
    <button
      type="button"
      onClick={() => {
        setTheme("light");
        onTheme("light", resolvedTheme);
      }}
    >
      {`${theme}:${resolvedTheme}`}
    </button>
  );
}

describe("ThemeProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.className = "";
    matchMediaMock.mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() });
    vi.stubGlobal("matchMedia", matchMediaMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("defaults to system, resolves light for a light scheme, and applies the html class", () => {
    render(
      <ThemeProvider>
        <ThemeProbe onTheme={vi.fn()} />
      </ThemeProvider>,
    );
    expect(screen.getByRole("button").textContent).toBe("system:light");
    expect(document.documentElement.classList.contains("light")).toBe(true);
  });

  it("resolves system to dark when the media query matches and follows changes", () => {
    matchMediaMock.mockReturnValue({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() });
    render(
      <ThemeProvider>
        <ThemeProbe onTheme={vi.fn()} />
      </ThemeProvider>,
    );
    expect(screen.getByRole("button").textContent).toBe("system:dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("persists an explicit choice and re-resolves", () => {
    matchMediaMock.mockReturnValue({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() });
    const onTheme = vi.fn();
    render(
      <ThemeProvider>
        <ThemeProbe onTheme={onTheme} />
      </ThemeProvider>,
    );
    act(() => {
      screen.getByRole("button").click();
    });
    expect(localStorage.getItem("vite-ui-theme")).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(document.documentElement.classList.contains("light")).toBe(true);
  });
});
