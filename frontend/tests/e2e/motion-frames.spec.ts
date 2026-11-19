import { existsSync, mkdirSync, writeFileSync } from "node:fs";

import { expect, test, type Locator, type Page } from "@playwright/test";

import { mockSession } from "./helpers/auth";

// A retry would hide exactly the intermittent budget miss this gate exists to
// catch, so the frame spec opts out of the suite-wide CI retry.
test.describe.configure({ retries: 0 });

// Frame-budget gate for the interactions the motion packages own.
//
// The measurement is deliberately narrow: it records the animation frames that
// belong to one target element (a CSS transition with a named property, or a
// CSS animation with a named keyframe), opens the window on the click and
// closes it when that very animation reports `finished`. It does not sample
// "the document" — a page-wide counter is satisfied by any unrelated transition
// (the sidebar case once recorded 15 running animations, only ~2 of which were
// the sidebar's own), so it would stay green after deleting the transition it
// claims to protect.
//
// Budget: a 60Hz frame is 16.7ms, a 120Hz frame 8.3ms. `budget` is
// `max(16.7, 2 × idle median)`, i.e. one *missed* frame at the display refresh
// rate, capped at 33.4ms so a throttled or overloaded machine cannot widen the
// gate until it is vacuous. `p95` must fit the budget (at most one frame in
// twenty may be late). The tail is asserted on `max`, not on `p99`: with 25–40
// frames per window `ceil(0.99 * n) - 1` is always the last index, so `p99` is
// literally the maximum here and calling it a quantile that is "stable across
// runs" would be false. A single overrunning frame is tolerated up to
// `TAIL_FACTOR` budget multiples (one stall); a sustained stall fails p95.`p99`
// stays in the JSON as a recorded number, not a claim.

interface FrameSample {
  interaction: string;
  refresh_hz: number;
  budget_ms: number;
  frames: number;
  running_animations: number;
  p50: number;
  p95: number;
  p99: number;
  max: number;
}

interface MotionExpectation {
  /** Element that owns (or contains) the transition/animation under test. */
  target: Locator;
  /** `CSSTransition.transitionProperty` values that count, e.g. ["opacity"]. */
  transitionProperties?: string[];
  /** `CSSAnimation.animationName` substrings that count, e.g. ["enter"]. */
  animationNames?: string[];
  /** Keyframe properties that count for WAAPI animations, e.g. ["transform"]. */
  keyframeProperties?: string[];
}

const FLOOR_BUDGET_MS = 16.7;
const MAX_BUDGET_MS = FLOOR_BUDGET_MS * 2;
const MIN_FRAMES = 6;
const TAIL_FACTOR = 3;

const round = (value: number): number => Math.round(value * 10) / 10;

function percentile(values: number[], ratio: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const rawIndex =
    ratio < 0.99 ? Math.floor(ratio * (sorted.length - 1)) : Math.ceil(ratio * sorted.length) - 1;
  const index = Math.min(sorted.length - 1, Math.max(0, rawIndex));
  return sorted[index] ?? 0;
}

interface RecordedAnimationEvent {
  kind: "transition" | "animation";
  name: string;
  target: EventTarget | null;
  startTime: number;
  endTime: number | null;
}

async function installRecorder(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const frames: { t: number; d: number }[] = [];
    const animEvents: RecordedAnimationEvent[] = [];
    window.__frames = frames;
    window.__animEvents = animEvents;
    let last = performance.now();
    const tick = (now: number) => {
      frames.push({ t: now, d: now - last });
      last = now;
      if (frames.length > 2400) frames.splice(0, frames.length - 2400);
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);

    const recordStart = (kind: "transition" | "animation", name: string, target: EventTarget | null) => {
      animEvents.push({ kind, name, target, startTime: performance.now(), endTime: null });
      if (animEvents.length > 400) animEvents.splice(0, animEvents.length - 400);
    };
    const recordEnd = (kind: "transition" | "animation", name: string, target: EventTarget | null) => {
      const now = performance.now();
      for (let i = animEvents.length - 1; i >= 0; i--) {
        const entry = animEvents[i];
        if (entry && entry.kind === kind && entry.name === name && entry.target === target && entry.endTime === null) {
          entry.endTime = now;
          break;
        }
      }
    };
    document.addEventListener("transitionrun", (e) => recordStart("transition", e.propertyName, e.target), true);
    document.addEventListener("transitionend", (e) => recordEnd("transition", e.propertyName, e.target), true);
    document.addEventListener("transitioncancel", (e) => recordEnd("transition", e.propertyName, e.target), true);
    document.addEventListener("animationstart", (e) => recordStart("animation", e.animationName, e.target), true);
    document.addEventListener("animationend", (e) => recordEnd("animation", e.animationName, e.target), true);
    document.addEventListener("animationcancel", (e) => recordEnd("animation", e.animationName, e.target), true);
  });
}

async function idleMedianDelta(page: Page): Promise<number> {
  await page.waitForTimeout(150);
  const deltas = await page.evaluate(() => window.__frames.slice(-40).map((frame) => frame.d));
  return percentile(deltas, 0.5);
}

async function settleFinitePageAnimations(page: Page): Promise<void> {
  await page.evaluate(async () => {
    const finite = document.getAnimations().filter((animation) => {
      if (animation.playState !== "running") return false;
      const endTime = animation.effect?.getComputedTiming().endTime;
      return typeof endTime === "number" && Number.isFinite(endTime);
    });
    await Promise.all(finite.map((animation) => animation.finished.catch(() => undefined)));
  });
}

interface MotionWindowResult {
  /** Animations on the target that matched the expectation. */
  count: number;
  /** Earliest document-timeline start of a matched animation, if known. */
  startTime: number | null;
  /** Page-clock instant at which the matched animation finished. */
  endTime: number;
}

/**
 * Waits inside the page (via rAF + native `animation.finished`, with zero CDP
 * polling during the motion window) for the target's animation to start and
 * complete, then returns the window boundaries in a single round-trip.
 */
async function awaitAnimationWindow(
  target: Locator,
  expectation: MotionExpectation,
  since: number,
): Promise<MotionWindowResult> {
  const spec = {
    transitionProperties: expectation.transitionProperties ?? [],
    animationNames: expectation.animationNames ?? [],
    keyframeProperties: expectation.keyframeProperties ?? [],
    since,
  };
  return target.evaluate(async (element, spec) => {
    const findLiveMatches = () =>
      element.getAnimations({ subtree: true }).filter((animation) => {
        if (animation instanceof CSSTransition) {
          return spec.transitionProperties.includes(animation.transitionProperty);
        }
        if (animation instanceof CSSAnimation) {
          return spec.animationNames.some((name) => animation.animationName.includes(name));
        }
        const effect = animation.effect;
        if (!(effect instanceof KeyframeEffect)) return false;
        return effect.getKeyframes().some((frame) =>
          spec.keyframeProperties.some((property) => {
            const value = (frame as Record<string, unknown>)[property];
            return typeof value === "string" && value !== "none" && !value.endsWith("(0px)");
          }),
        );
      });

    const findRecordedMatches = () =>
      (window.__animEvents ?? []).filter((entry) => {
        if (entry.startTime < spec.since) return false;
        if (!(entry.target instanceof Node) || !element.contains(entry.target)) return false;
        if (entry.kind === "transition") {
          return spec.transitionProperties.includes(entry.name);
        }
        return spec.animationNames.some((name) => entry.name.includes(name));
      });

    let matches = findLiveMatches();
    const deadline = performance.now() + 500;
    while (matches.length === 0 && findRecordedMatches().length === 0 && performance.now() < deadline) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      matches = findLiveMatches();
    }

    if (matches.length > 0) {
      const starts = matches
        .map((animation) => animation.startTime)
        .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
      const startTime = starts.length > 0 ? Math.min(...starts) : performance.now();
      await Promise.all(matches.map((animation) => animation.finished.catch(() => undefined)));
      return {
        count: matches.length,
        startTime,
        endTime: performance.now(),
      };
    }

    const recorded = findRecordedMatches();
    if (recorded.length > 0) {
      const starts = recorded.map((entry) => entry.startTime);
      while (recorded.some((entry) => entry.endTime === null) && performance.now() < deadline + 500) {
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      }
      const ends = recorded
        .map((entry) => entry.endTime)
        .filter((value): value is number => typeof value === "number" && Number.isFinite(value));
      return {
        count: recorded.length,
        startTime: Math.min(...starts),
        endTime: ends.length > 0 ? Math.max(...ends) : performance.now(),
      };
    }

    return { count: 0, startTime: null, endTime: performance.now() };
  }, spec);
}

async function measure(
  page: Page,
  interaction: string,
  expectation: MotionExpectation,
  action: () => Promise<void>,
  assertTargetReached: () => Promise<void>,
): Promise<FrameSample> {
  // Page-entry charts and counts can still be compositing when data renders.
  // They are not the interaction under test; sampling through them turns the
  // gate into a generic load test and produced 33-50ms outliers in CI. Wait
  // only for finite animations so intentional infinite spinners never hang it.
  await settleFinitePageAnimations(page);
  const idleMedian = await idleMedianDelta(page);
  // Locked 120Hz displays have idleMedian ~8.3ms; variable-refresh (ProMotion)
  // or 60Hz headless compositors run animations on the 60Hz tier (33.4ms budget).
  const budget =
    idleMedian <= 9.5
      ? Math.min(MAX_BUDGET_MS, Math.max(FLOOR_BUDGET_MS, idleMedian * 2))
      : MAX_BUDGET_MS;
  const refreshHz = Math.round(1000 / Math.max(idleMedian, 1));

  // The window opens just before the click is dispatched, so the frames of the
  // commit that starts the transition are inside it (the click itself is not a
  // frame). It closes when the target's own animation reports `finished`.
  const start = await page.evaluate(() => performance.now());
  await action();
  const animWindow = await awaitAnimationWindow(expectation.target, expectation, start);
  await assertTargetReached();
  // The click's handler, dialog close and data commit are inside the wall-clock
  // window but outside the animation. Start the frame window after the animation's
  // own document-timeline start so the gate judges the motion, not the work that
  // scheduled it.
  const windowStart = animWindow.startTime === null ? start : Math.max(start, animWindow.startTime);
  const end = animWindow.endTime;

  const deltas = await page.evaluate(
    ([windowStart, windowEnd, minFrames]: [number, number, number]) => {
      const allInWindow = window.__frames
        .filter((frame) => frame.t >= windowStart && frame.t <= windowEnd)
        .map((frame) => frame.d);
      // Drop the initial commit and portal-layout frames when enough animation frames remain.
      const skip = Math.min(2, Math.max(0, allInWindow.length - minFrames));
      return allInWindow.slice(skip);
    },
    [windowStart, end, MIN_FRAMES] as [number, number, number],
  );

  return {
    interaction,
    refresh_hz: refreshHz,
    budget_ms: round(budget),
    frames: deltas.length,
    running_animations: animWindow.count,
    p50: round(percentile(deltas, 0.5)),
    p95: round(percentile(deltas, 0.95)),
    p99: round(percentile(deltas, 0.99)),
    max: round(deltas.length > 0 ? Math.max(...deltas) : 0),
  };
}

const samples: FrameSample[] = [];

function expectWithinBudget(sample: FrameSample): void {
  expect(sample.running_animations, `${sample.interaction}: no matching transition/animation was running`).toBeGreaterThan(0);
  expect(sample.frames, `${sample.interaction}: too few frames in the window`).toBeGreaterThanOrEqual(MIN_FRAMES);
  expect(sample.p95, `${sample.interaction}: p95 ${sample.p95}ms over budget ${sample.budget_ms}ms`).toBeLessThanOrEqual(sample.budget_ms);
  expect(sample.max, `${sample.interaction}: worst frame ${sample.max}ms over tail guard ${sample.budget_ms * TAIL_FACTOR}ms`).toBeLessThanOrEqual(sample.budget_ms * TAIL_FACTOR);
  samples.push(sample);
  persist();
}

test.beforeEach(async ({ page }) => {
  await installRecorder(page);
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
  await mockSession(page, "default");
});

function persist() {
  mkdirSync("tests/performance", { recursive: true });
  const report = {
    generated_at: new Date().toISOString(),
    floor_budget_ms: FLOOR_BUDGET_MS,
    cap_budget_ms: MAX_BUDGET_MS,
    measured: samples,
  };
  const body = `${JSON.stringify(report, null, 2)}\n`;
  writeFileSync("tests/performance/motion-frames.json", body);
  // `tests/performance/` is gitignored, so this file never dirties the tree.
  //
  // Mirroring into the parent repository is opt-in and off by default: it used
  // to write `artifacts/development/FE-F11-MOTION-FINISH/tests/motion-frames.json`
  // unconditionally, which turned every later `pnpm test:e2e` run into an
  // uncommitted modification of an *already completed* package's committed
  // evidence (found during FE-F11-MOTION-SPATIAL). Refreshing that record is a
  // deliberate act: set MOTION_FRAMES_MIRROR=1 while regenerating that package.
  if (process.env.MOTION_FRAMES_MIRROR === "1") {
    const evidence = "../artifacts/development/FE-F11-MOTION-FINISH/tests/motion-frames.json";
    if (existsSync("../artifacts/development/FE-F11-MOTION-FINISH")) {
      writeFileSync(evidence, body);
    }
  }
}

test.afterAll(() => {
  if (samples.length > 0) persist();
});

test("sidebar collapse transitions its own gap width", async ({ page }) => {
  test.setTimeout(60_000);
  await page.goto("/workspace");
  await expect(page.getByTestId("home-kpi-pending-approval")).toBeVisible({ timeout: 15_000 });

  const trigger = page.locator("[data-slot='sidebar-trigger']").first();
  const gap = page.locator("[data-slot='sidebar-gap']").first();
  const sample = await measure(
    page,
    "sidebar-collapse",
    { target: gap, transitionProperties: ["width"] },
    async () => {
      await trigger.click();
    },
    async () => {
      await expect(page.locator("[data-state='collapsed']").first()).toBeAttached();
    },
  );

  expectWithinBudget(sample);
});

test("popover entry plays its own enter keyframes", async ({ page }) => {
  test.setTimeout(60_000);
  // The /changes/mine date pickers are gone (owner feedback 2026-11-05: the
  // global search bar's date values come from an inline calendar). The
  // renewal dialog carries the shared DatePicker popover now, inside the
  // query-session world that seeds the fixture grant.
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-scenario", "query-session");
  });
  await page.goto("/query/sessions/qs-fixture-active");
  await expect(page.getByTestId("query-workspace")).toBeVisible({ timeout: 15_000 });
  await page.getByTestId("query-grant-renew").click();
  const trigger = page.getByTestId("query-renew-until");
  await expect(trigger).toBeVisible({ timeout: 15_000 });

  const content = page.locator("[data-slot='popover-content']").first();
  const sample = await measure(
    page,
    "popover-enter",
    { target: content, animationNames: ["enter"] },
    async () => {
      await trigger.click();
    },
    async () => {
      await expect(content).toBeVisible();
    },
  );

  expectWithinBudget(sample);
});

test("mobile sidebar sheet transitions its own opacity and translate", async ({ page }) => {
  test.setTimeout(60_000);
  // 390px is below the 768px mobile breakpoint; at desktop widths the same
  // trigger collapses the sidebar and no Sheet is ever mounted.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/workspace");
  await expect(page.getByTestId("home-kpi-pending-approval")).toBeVisible();

  // Two triggers exist in the tree (desktop rail + mobile header); only the
  // visible one flips `openMobile` at this width. The mobile sidebar reuses the
  // Sheet primitive but rebrands the root: `data-slot="sidebar"` +
  // `data-mobile="true"` (sheet.tsx's own `data-slot="sheet-content"` is
  // overridden by sidebar.tsx).
  const trigger = page.locator("[data-slot='sidebar-trigger']:visible").first();
  const sheet = page.locator("[data-slot='sidebar'][data-mobile='true']").first();
  const sample = await measure(
    page,
    "sheet-enter",
    { target: sheet, transitionProperties: ["opacity", "translate"] },
    async () => {
      await trigger.click();
    },
    async () => {
      await expect(sheet).toBeVisible();
    },
  );

  expectWithinBudget(sample);
});

test("dialog entry stays inside the frame budget", async ({ page }) => {
  test.setTimeout(60_000);
  await mockSession(page, "admin");
  await page.goto("/admin/datasources");

  // The delete confirmation is the smallest dialog on the page, but it uses the
  // same Radix Dialog, scroll lock and enter animation as every other modal; a
  // form-heavy dialog's first paint is app work, not motion, so it is not a
  // stable frame gate target.
  // analytics-pg: the unreferenced row whose ⋯ delete is enabled.
  const triggerRow = page.locator("tr", { hasText: "analytics-pg" });
  await expect(triggerRow).toBeVisible({ timeout: 15_000 });
  await triggerRow.getByTestId(/ds-actions-/).click();
  const deleteItem = page.getByTestId(/^ds-delete-/).first();
  await expect(deleteItem).toBeVisible({ timeout: 15_000 });
  const dialog = page.locator("[data-slot='dialog-content']").first();
  const sample = await measure(
    page,
    "dialog-enter",
    { target: dialog, animationNames: ["enter"] },
    async () => {
      await deleteItem.click();
    },
    async () => {
      await expect(dialog).toBeVisible();
    },
  );

  expectWithinBudget(sample);
  await page.keyboard.press("Escape");
});

test("progress fill is a scaled box, never an animated width", async ({ page }) => {
  test.setTimeout(120_000);
  // The review bulk-import dialog reads the file in chunks and paints a real
  // <Progress> while it does, so this asserts the component in the browser
  // instead of re-reading its class strings.
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-scenario", "review-ready");
  });
  await mockSession(page, "admin");
  await page.goto("/changes/new");
  await expect(page.getByTestId("changes-new-page")).toBeVisible({ timeout: 15_000 });
  await page.getByTestId("use-flow-4f6f1a2b-0000-4000-8000-000000000001").click();
  await page.getByTestId("draft-title-input").fill("motion frames - progress");
  // Implicit drafts: bulk import adopts the draft silently; the page root
  // carries the id and the editor swaps to the virtualized browser.
  await page.getByTestId("open-bulk-import").click();
  await expect(page.getByTestId("bulk-import-dialog")).toBeVisible();
  // ~20 MB keeps the chunk loop on screen long enough to inspect a live frame.
  const statement = "UPDATE orders SET status = 1 WHERE id = 1;\n";
  const sql = statement.repeat(Math.ceil((20 * 1024 * 1024) / statement.length));
  await page.getByTestId("bulk-import-input").setInputFiles({
    name: "progress.sql",
    mimeType: "text/plain",
    buffer: Buffer.from(sql, "utf-8"),
  });

  const indicator = page.locator("[data-slot='progress-indicator']").first();
  await expect(indicator).toBeVisible({ timeout: 30_000 });

  const box = await indicator.evaluate((element) => {
    const style = getComputedStyle(element);
    const track = element.parentElement as HTMLElement;
    return {
      transitionProperty: style.transitionProperty,
      width: Number.parseFloat(style.width),
      trackWidth: track.getBoundingClientRect().width,
    };
  });

  // This is the regression this package exists to prevent: a `width`
  // transition relayouts the track on every frame.
  expect(box.transitionProperty).toContain("scale");
  expect(box.transitionProperty).not.toContain("width");
  expect(
    Math.abs(box.width - box.trackWidth),
    `indicator width ${box.width}px should fill the ${box.trackWidth}px track (the visible fill comes from scale)`,
  ).toBeLessThan(1);

  await page.getByTestId("bulk-import-cancel").click().catch(() => {});
});

declare global {
  interface Window {
    __frames: { t: number; d: number }[];
    __animEvents?: RecordedAnimationEvent[];
  }
}

test("button press stays inside the frame budget", async ({ page }) => {
  test.setTimeout(60_000);
  // Measure on the workspace header, not the login brand panel: the login
  // decoration runs three large blurred glows on infinite loops, so even an
  // idle login page has a p95 above the two-frame budget. The press feedback
  // itself is the same shared Button primitive.
  await page.goto("/workspace");
  const button = page.getByTestId("header-refresh");
  await expect(button).toBeEnabled({ timeout: 15_000 });

  const box = await button.boundingBox();
  expect(box).not.toBeNull();
  if (box === null) throw new Error("button has no box");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);

  const sample = await measure(
    page,
    "button-press",
    { target: button, transitionProperties: ["scale"] },
    async () => {
      await page.mouse.down();
    },
    async () => {
      await expect
        .poll(async () => {
          const value = await button.evaluate((element) => getComputedStyle(element).scale);
          return value === "none" ? 1 : Number.parseFloat(value);
        })
        .toBeLessThan(1);
    },
  );
  await page.mouse.up();

  expectWithinBudget(sample);
});

test("row reorder stays inside the frame budget", async ({ page }) => {
  test.setTimeout(60_000);
  await mockSession(page, "admin");
  await page.goto("/admin/datasources");

  // analytics-pg: the unreferenced row whose ⋯ delete is enabled (referenced
  // rows keep a disabled item).
  const row = page.locator("tr", { hasText: "analytics-pg" });
  await expect(row).toBeVisible({ timeout: 15_000 });
  const name = (await row.locator("td").first().innerText()).trim();
  const body = page.locator("tbody").first();

  // Open the confirmation before the window: a Playwright click waits for the
  // post-commit work, so starting the window at the delete button would let the
  // 200ms reorder finish before the first sample.
  await row.getByTestId(/ds-actions-/).click();
  await page.getByTestId(/^ds-delete-/).first().click();
  const sample = await measure(
    page,
    "table-reorder",
    { target: body, keyframeProperties: ["transform"] },
    async () => {
      await page.getByTestId("ds-delete-confirm").click();
    },
    async () => {
      await expect(body).not.toContainText(name);
    },
  );

  expectWithinBudget(sample);
});
