import { render } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  Table,
  TableBody,
  TableCell,
  TableRow,
} from "@/shared/components/ui/table"

import { useFlipReorder } from "./use-flip-reorder"

/** Vertical offset per row key, mutated between renders to fake a reorder. */
let tops: Record<string, number> = {}
/** Height the container reports; growing it fakes a list that gained rows. */
let containerHeight = 600
type Flight = { cancel: () => void }
const flights: Array<{ cancel: ReturnType<typeof vi.fn> }> = []
const animate = vi.fn<(keyframes: unknown, options: unknown) => Flight>()

beforeEach(() => {
  tops = {}
  containerHeight = 600
  flights.length = 0
  animate.mockReset()
  animate.mockImplementation(() => {
    const flight = { cancel: vi.fn() }
    flights.push(flight)
    return flight
  })
  Object.defineProperty(HTMLElement.prototype, "animate", {
    configurable: true,
    writable: true,
    value: animate,
  })
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
    function (this: HTMLElement) {
      const top =
        this.dataset.rowKey === undefined ? 0 : (tops[this.dataset.rowKey] ?? 0)
      // The container (no data-row-key) reports its height: the hook uses it as
      // the upper bound for a plausible reorder.
      const height = this.dataset.rowKey === undefined ? containerHeight : 32
      return {
        top,
        bottom: top + height,
        height,
        left: 0,
        right: 100,
        width: 100,
        x: 0,
        y: top,
        toJSON: () => ({}),
      }
    }
  )
})

afterEach(() => {
  vi.restoreAllMocks()
  delete (HTMLElement.prototype as { animate?: unknown }).animate
})

function Harness({ keys }: { keys: string[] }) {
  const ref = useFlipReorder<HTMLDivElement>(keys.join("|"))
  return (
    <div ref={ref}>
      {keys.map((key) => (
        <div key={key} data-row-key={key} />
      ))}
    </div>
  )
}

/** The list can disappear (empty state, tab switch) while the page stays mounted. */
function ToggleHarness({ keys, mount }: { keys: string[]; mount: boolean }) {
  const ref = useFlipReorder<HTMLDivElement>(JSON.stringify(keys))
  if (!mount) {
    return null
  }
  return (
    <div ref={ref}>
      {keys.map((key) => (
        <div key={key} data-row-key={key} />
      ))}
    </div>
  )
}

/** Real table components: proves the ref reaches the rendered <tbody>. */
function TableHarness({ keys }: { keys: string[] }) {
  const ref = useFlipReorder<HTMLTableSectionElement>(`table:${keys.join("|")}`)
  return (
    <Table>
      <TableBody ref={ref}>
        {keys.map((key) => (
          <TableRow key={key} data-row-key={key}>
            <TableCell>{key}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

function setMotionPreference(matches: boolean) {
  vi.spyOn(window, "matchMedia").mockImplementation(
    (query: string) =>
      ({
        matches: query.includes("prefers-reduced-motion") ? matches : false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })
  )
}

describe("useFlipReorder", () => {
  it("does not animate the first render, where there is no previous position", () => {
    setMotionPreference(false)
    tops = { a: 0, b: 32 }
    render(<Harness keys={["a", "b"]} />)
    expect(animate).not.toHaveBeenCalled()
  })

  it("animates a row by the distance it moved between renders", () => {
    setMotionPreference(false)
    tops = { a: 0, b: 32, c: 64 }
    const { rerender } = render(<Harness keys={["a", "b", "c"]} />)

    // "a" leaves: c moves up from 64 to 32, b keeps its slot.
    tops = { b: 32, c: 32 }
    rerender(<Harness keys={["b", "c"]} />)

    expect(animate).toHaveBeenCalledTimes(1)
    const [keyframes, options] = animate.mock.calls[0] as [
      Array<{ transform: string }>,
      { duration: number; easing: string },
    ]
    expect(keyframes[0]?.transform).toBe("translateY(32px)")
    expect(options.duration).toBe(200)
    expect(options.easing).toBe("cubic-bezier(0.23, 1, 0.32, 1)")
  })

  it("stays still when the user prefers reduced motion", () => {
    setMotionPreference(true)
    tops = { a: 0, b: 32, c: 64 }
    const { rerender } = render(<Harness keys={["a", "b", "c"]} />)

    tops = { b: 32, c: 32 }
    rerender(<Harness keys={["b", "c"]} />)

    expect(animate).not.toHaveBeenCalled()
  })

  it("ignores movement smaller than a visible nudge", () => {
    setMotionPreference(false)
    tops = { a: 0, b: 32 }
    const { rerender } = render(<Harness keys={["a", "b"]} />)

    // Same slots, one pixel of layout jitter: nothing should move on screen.
    tops = { a: 1, b: 33 }
    rerender(<Harness keys={["b", "a"]} />)

    expect(animate).not.toHaveBeenCalled()
  })

  it("animates rows rendered through the real table components", () => {
    setMotionPreference(false)
    tops = { a: 0, b: 32, c: 64 }
    const { rerender } = render(<TableHarness keys={["a", "b", "c"]} />)

    tops = { b: 32, c: 32 }
    rerender(<TableHarness keys={["b", "c"]} />)

    expect(animate).toHaveBeenCalledTimes(1)
    // The animated element must be the row itself, i.e. the ref was really
    // attached to <tbody> and the selector matched data-row-key.
    expect((animate.mock.contexts[0] as HTMLElement).tagName).toBe("TR")
  })

  it("keeps recording positions while reduced motion is on", () => {
    // Seed with motion on: if the suppressed pass below swallowed the record,
    // the last render would then animate a 32px jump and fail here.
    setMotionPreference(false)
    tops = { a: 0, b: 32 }
    const { rerender } = render(<Harness keys={["a", "b"]} />)

    setMotionPreference(true)
    tops = { b: 0 }
    rerender(<Harness keys={["b"]} />)
    expect(animate).not.toHaveBeenCalled()

    setMotionPreference(false)
    tops = { b: 0, c: 32 }
    rerender(<Harness keys={["b", "c"]} />)
    expect(animate).not.toHaveBeenCalled()
  })

  it("cancels its own in-flight animation before measuring again", () => {
    setMotionPreference(false)
    tops = { a: 0, b: 32 }
    const { rerender } = render(<Harness keys={["a", "b"]} />)

    tops = { b: 0 }
    rerender(<Harness keys={["b"]} />)
    expect(flights).toHaveLength(1)
    expect(flights[0]?.cancel).not.toHaveBeenCalled()

    // A second change inside the window: the stale transform must be undone
    // first, otherwise the row is measured mid-flight.
    tops = { b: 0, c: 32 }
    rerender(<Harness keys={["b", "c"]} />)
    expect(flights[0]?.cancel).toHaveBeenCalledTimes(1)
  })

  it("lands a row instead of gliding it when the shift exceeds the container", () => {
    setMotionPreference(false)
    // 600px container, rows 32px apart: a row 660px up is a different list, not
    // the same row in a new slot (that is what a filter swap looks like).
    tops = { a: 0, b: 700 }
    const { rerender } = render(<Harness keys={["a", "b"]} />)

    // "a" leaves; "b" would have to travel 660px — past the 600px container.
    tops = { b: 40 }
    rerender(<Harness keys={["b"]} />)
    expect(animate).not.toHaveBeenCalled()
  })

  it("also lands the row when a cleared filter widens the list", () => {
    setMotionPreference(false)
    // Three rows visible: a row pushed further than this is a different list.
    containerHeight = 96
    tops = { c: 0 }
    const { rerender } = render(<Harness keys={["c"]} />)

    // The filter is cleared: twenty rows come back and "c" now sits five slots
    // down. The bound must stay the narrower list, otherwise those 160px slip
    // under the wider container's height and the row glides across the list.
    containerHeight = 640
    tops = { x: 0, c: 160 }
    rerender(<Harness keys={["x", "c"]} />)
    expect(animate).not.toHaveBeenCalled()
  })

  it("forgets positions measured before the list unmounted", () => {
    setMotionPreference(false)
    tops = { a: 0, b: 32 }
    const { rerender } = render(<ToggleHarness keys={["a", "b"]} mount />)

    // Unmount while the page stays mounted: the effect clears on the next key.
    rerender(<ToggleHarness keys={[]} mount={false} />)

    // Remounted with rows that moved a lot: stale coordinates would animate.
    tops = { a: 320, b: 0 }
    rerender(<ToggleHarness keys={["b", "a"]} mount />)
    expect(animate).not.toHaveBeenCalled()
  })
})
