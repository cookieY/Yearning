import * as React from "react"

/** Shared --ease-out, so reorder motion matches the rest of the surface. */
const EASING = "cubic-bezier(0.23, 1, 0.32, 1)"
const DURATION = 200
/** Below this the row did not visibly move; animating it would only add work. */
const MOVED = 2

/**
 * FLIP for table rows that reorder in place (a pending item being approved
 * shifts every row below it up). Rows opt in with `data-row-key`; the key is
 * what maps a row's previous position to its new one, so a list rendered with
 * `key={index}` cannot use this — there is no identity to follow.
 *
 * Positions are measured relative to the container, which makes the delta
 * invariant to scrolling between renders.
 *
 * A shift longer than the list itself is not "this row moved up a few places":
 * that is a filter or search swapping in a different set of rows, and gliding a
 * row across the whole list reads as a bug. Those rows land where they belong
 * instead. The bound takes the smaller of the previous and current heights, so
 * clearing a filter (a list growing) is suppressed as well as narrowing one —
 * otherwise the same row would glide back across a wider container.
 *
 * A hidden container reports height 0, which suppresses every tween. None of
 * the current call sites renders its table inside a hidden element.
 */
export function useFlipReorder<Container extends HTMLElement>(orderKey: string) {
  const ref = React.useRef<Container | null>(null)
  const positions = React.useRef(new Map<string, number>())
  const running = React.useRef<Animation[]>([])
  /** Height of the list at the previous pass, for the two-directional bound. */
  const previousHeight = React.useRef<number | null>(null)

  React.useLayoutEffect(() => {
    const container = ref.current
    if (container === null) {
      // The list was unmounted (empty state, tab switch). Keeping the map would
      // make the next render animate from coordinates measured before the
      // container went away, which is how you get a row flying in from nowhere.
      positions.current.clear()
      previousHeight.current = null
      return
    }

    // A change inside the 200ms window would be measured mid-flight:
    // getBoundingClientRect() already includes the running transform, so both
    // the recorded positions and the next delta would be wrong and the row
    // would jump before it moved. Cancelling is synchronous and restores the
    // laid-out position immediately. Only animations this hook started are
    // cancelled — a subtree sweep would permanently kill any CSS animation a
    // row happens to use (a spinner, a pulse).
    for (const animation of running.current) {
      animation.cancel()
    }
    running.current = []

    const containerRect = container.getBoundingClientRect()
    const containerTop = containerRect.top
    const bound =
      previousHeight.current === null
        ? containerRect.height
        : Math.min(previousHeight.current, containerRect.height)
    const next = new Map<string, number>()
    const moved: Array<{ row: HTMLElement; delta: number }> = []

    for (const row of container.querySelectorAll<HTMLElement>("[data-row-key]")) {
      const key = row.dataset.rowKey
      if (key === undefined) {
        continue
      }
      const top = row.getBoundingClientRect().top - containerTop
      next.set(key, top)

      const previous = positions.current.get(key)
      if (previous === undefined) {
        continue
      }
      const delta = previous - top
      if (Math.abs(delta) < MOVED || Math.abs(delta) > bound) {
        continue
      }
      // Animating rows that are outside the viewport buys nothing the user can
      // see and multiplies the composited layers during the tween. A long page
      // (the default 20-row admin table) otherwise pushes the animation off the
      // frame budget on software rasterisers.
      const rowRect = row.getBoundingClientRect()
      if (rowRect.bottom <= 0 || rowRect.top >= window.innerHeight) {
        continue
      }
      moved.push({ row, delta })
    }

    positions.current = next
    previousHeight.current = containerRect.height

    if (moved.length === 0) {
      return
    }
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return
    }

    running.current = moved.map(({ row, delta }) =>
      row.animate(
        [
          { transform: `translateY(${String(delta)}px)` },
          { transform: "translateY(0)" },
        ],
        { duration: DURATION, easing: EASING }
      )
    )
  }, [orderKey])

  return ref
}
