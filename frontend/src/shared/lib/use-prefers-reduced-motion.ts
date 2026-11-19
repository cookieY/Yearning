import * as React from "react"

const QUERY = "(prefers-reduced-motion: reduce)"

function readPreference(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia(QUERY).matches
    : false
}

/**
 * Reads the user's reduced-motion preference reactively.
 *
 * CSS transitions are already neutralised globally in `global.css`, but motion
 * driven from JavaScript (recharts' draw animation) can't read that media
 * query, so it asks here instead.
 *
 * The name follows recharts' internal `usePrefersReducedMotion`, so the chart's
 * own gating is described with the same words as the library's. It deliberately
 * does not shadow `motion/react`'s `useReducedMotion`: this module has no motion
 * dependency, so importing it never pulls framer-motion into a chunk.
 */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = React.useState(readPreference)

  React.useEffect(() => {
    if (typeof window.matchMedia !== "function") {
      return
    }
    const media = window.matchMedia(QUERY)
    const onChange = () => {
      setReduced(media.matches)
    }
    onChange()
    media.addEventListener("change", onChange)
    return () => {
      media.removeEventListener("change", onChange)
    }
  }, [])

  return reduced
}
