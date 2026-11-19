import { act, renderHook } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { usePrefersReducedMotion } from "./use-prefers-reduced-motion"

const QUERY = "(prefers-reduced-motion: reduce)"

function controllableMedia(initial: boolean) {
  let matches = initial
  const listeners = new Set<() => void>()
  const media = {
    get matches() {
      return matches
    },
    media: QUERY,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: (_type: string, listener: () => void) => {
      listeners.add(listener)
    },
    removeEventListener: (_type: string, listener: () => void) => {
      listeners.delete(listener)
    },
    dispatchEvent: vi.fn(),
  } as unknown as MediaQueryList

  return {
    media,
    set(next: boolean) {
      matches = next
      for (const listener of listeners) {
        listener()
      }
    },
  }
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe("usePrefersReducedMotion", () => {
  it("reports the preference from the shared test setup (reduce on)", () => {
    const { result } = renderHook(() => usePrefersReducedMotion())

    expect(result.current).toBe(true)
  })

  it("reports motion allowed when the query does not match", () => {
    const { media } = controllableMedia(false)
    vi.spyOn(window, "matchMedia").mockReturnValue(media)

    const { result } = renderHook(() => usePrefersReducedMotion())

    expect(result.current).toBe(false)
  })

  it("follows a change in the system preference", () => {
    const controls = controllableMedia(false)
    vi.spyOn(window, "matchMedia").mockReturnValue(controls.media)

    const { result } = renderHook(() => usePrefersReducedMotion())
    expect(result.current).toBe(false)

    act(() => {
      controls.set(true)
    })

    expect(result.current).toBe(true)
  })
})
