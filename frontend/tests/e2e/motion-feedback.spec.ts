import { expect, test, type Locator, type Page } from "@playwright/test";

import { mockSession } from "./helpers/auth";

// Feedback gate for the "feedback within 100ms" motion criterion.
//
// Two dimensions are measured:
//
//   1. Press feedback on the login submit button. The probe is armed *before*
//      `mouse.down()`, so the page-clock origin is the real pointerdown event
//      rather than a `performance.now()` sampled after a driver round-trip.
//      The first frame whose computed scale/background differs is recorded, and
//      the transition list is read inside that same rAF turn — reading it after
//      the promise resolves races the 100ms transition and can come back empty.
//   2. Cursor paging/search on a real table. The mock layer can hold a response
//      for `window.__YEARNING_MOCK_LATENCY__` ms (test-only), which makes the placeholder
//      window observable: the footer must show the busy dot and disable Next on
//      the same commit as the click, while the previous rows stay visible.
//
// Reduced motion is covered separately: the press must keep a colour
// acknowledgement inside 100ms and must not scale (spatial feedback is dropped).
interface PressProbe {
  firstScale: number;
  firstColor: number;
  endScale: string;
  transitions: { property: string; duration: number }[];
  declared: { property: string; duration: number }[];
}

async function armPressProbe(page: Page, selector: string): Promise<void> {
  await page.evaluate((sel) => {
    const el = document.querySelector(sel) as HTMLElement | null;
    if (el === null) throw new Error(`missing press target: ${sel}`);
    const state = {
      pressStart: null as number | null,
      firstScale: -1,
      firstColor: -1,
      endScale: "",
      transitions: [] as { property: string; duration: number }[],
      declared: (() => {
        // The declared transition is the contract; the live animation list is
        // only an observation. A loaded runner can paint its first frame after
        // a 100ms transition has already finished, so the live list comes back
        // empty even though the CSS is correct.
        const computed = getComputedStyle(el);
        const properties = computed.transitionProperty.split(",").map((entry) => entry.trim());
        const durations = computed.transitionDuration.split(",").map((entry) => entry.trim());
        return properties.map((property, index) => {
          const raw = durations[index] ?? "0s";
          const value = Number.parseFloat(raw);
          return { property, duration: raw.endsWith("ms") ? value : value * 1000 };
        });
      })(),
      beforeColor: "",
      framesAfterChange: 0,
      settled: false,
    };
    (window as unknown as { __press: typeof state }).__press = state;
    (window as unknown as { __pressDone: Promise<typeof state> }).__pressDone = new Promise((resolve) => {
      el.addEventListener("pointerdown", (event) => {
        // Sample the pre-active colour at the real pointerdown, not at arm
        // time: the pointer arrived after a hover transition, and comparing
        // against the base colour would let the hover tail satisfy firstColor.
        state.beforeColor = getComputedStyle(el).backgroundColor;
        // `event.timeStamp` shares the document timeline origin with
        // `Animation.startTime`, so the measured latency is "when the
        // transition began", not "when the next frame happened to paint".
        // On a loaded runner frames can be 100ms apart; sampling the first
        // changed frame measured the frame cadence, not the feedback.
        state.pressStart = event.timeStamp;
      }, { capture: true, once: true });
      const tick = (): void => {
        const now = performance.now();
        const scale = getComputedStyle(el).scale;
        state.endScale = scale;
        if (state.pressStart !== null) {
          const running = el
            .getAnimations()
            .filter((animation): animation is CSSTransition => animation instanceof CSSTransition);
          if (!state.settled) {
            // Freeze the list the first time a transition is visible: the
            // 100ms transition is over well before the settle window closes,
            // so re-reading it every frame ends with an empty list.
            if (state.transitions.length === 0) {
              state.transitions = running.map((animation) => ({
                property: animation.transitionProperty,
                duration: Number(animation.effect?.getTiming().duration ?? 0),
              }));
            }
            const scaleTransition = running.find((animation) => animation.transitionProperty === "scale");
            const colorTransition = running.find((animation) => animation.transitionProperty === "background-color");
            if (scaleTransition !== undefined && typeof scaleTransition.startTime === "number" && state.firstScale < 0) {
              state.firstScale = scaleTransition.startTime - state.pressStart;
            }
            if (colorTransition !== undefined && typeof colorTransition.startTime === "number" && state.firstColor < 0) {
              state.firstColor = colorTransition.startTime - state.pressStart;
            }
          }
          const changed = state.firstScale >= 0 || state.firstColor >= 0;
          if (changed) state.framesAfterChange += 1;
          // Release the promise once the feedback has been observed for a few
          // frames (the transition list is captured in the first of them).
          if (state.framesAfterChange >= 3 || now - state.pressStart > 2000) {
            state.settled = true;
            resolve(state);
            return;
          }
        }
        requestAnimationFrame(tick);
      };
      requestAnimationFrame(tick);
    });
  }, selector);
}

async function pressAndRead(page: Page, button: Locator): Promise<PressProbe> {
  const box = await button.boundingBox();
  expect(box).not.toBeNull();
  if (box === null) throw new Error("button has no box");
  await armPressProbe(page, "form button[type=submit]");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  // Let the 100ms hover transition settle so the pointerdown comparison only
  // sees the press change.
  await page.waitForTimeout(160);
  await page.mouse.down();
  const result = await page.evaluate(() =>
    (window as unknown as { __pressDone: Promise<PressProbe> }).__pressDone,
  );
  await page.mouse.up();
  await page.mouse.move(0, 0);
  return result;
}

interface BusyProbe {
  triggerAt: number | null;
  busyAt: number | null;
}

// Measure the acknowledgement in the page clock. The previous implementation
// compared `Date.now()` across a Playwright driver round-trip and allowed
// 150ms while the criterion says 100ms, so a 120ms regression stayed green.
async function armBusyProbe(page: Page, busyTestId: string, triggerTestId: string): Promise<void> {
  await page.evaluate(({ busyTestId, triggerTestId }) => {
    const state = { triggerAt: null as number | null, busyAt: null as number | null };
    (window as unknown as { __busyProbe: typeof state }).__busyProbe = state;
    const trigger = document.querySelector(`[data-testid="${triggerTestId}"]`);
    if (trigger === null) throw new Error(`missing busy trigger: ${triggerTestId}`);
    const record = (): void => {
      if (state.triggerAt === null) state.triggerAt = performance.now();
    };
    trigger.addEventListener("pointerdown", record, { capture: true });
    trigger.addEventListener("input", record, { capture: true });
    const check = (): void => {
      if (state.busyAt === null && document.querySelector(`[data-testid="${busyTestId}"]`) !== null) {
        state.busyAt = performance.now();
      }
    };
    new MutationObserver(check).observe(document.body, { subtree: true, childList: true });
    check();
  }, { busyTestId, triggerTestId });
}

async function readBusyProbe(page: Page): Promise<BusyProbe> {
  return page.evaluate(() => (window as unknown as { __busyProbe: BusyProbe }).__busyProbe);
}

test("press feedback starts and settles within 100ms", async ({ page }) => {
  await page.goto("/login");
  await page.locator("#username").fill("demo");
  await page.locator("#password").fill("not-a-real-password");

  const button = page.locator("form button[type=submit]");
  await expect(button).toBeEnabled();
  const probe = await pressAndRead(page, button);

  expect(probe.firstScale, "scale feedback never started").toBeGreaterThanOrEqual(0);
  expect(probe.firstScale).toBeLessThan(100);
  const scaleTransition =
    probe.transitions.find((entry) => entry.property === "scale") ??
    probe.declared.find((entry) => entry.property === "scale");
  expect(scaleTransition, "pressed state must be a scale transition").toBeDefined();
  expect(scaleTransition?.duration).toBeLessThanOrEqual(100);
});

test("reduced motion keeps colour feedback and drops the scale jump", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/login");
  await page.locator("#username").fill("demo");
  await page.locator("#password").fill("not-a-real-password");

  const button = page.locator("form button[type=submit]");
  await expect(button).toBeEnabled();
  const probe = await pressAndRead(page, button);

  expect(probe.firstColor, "reduced motion must keep a colour acknowledgement").toBeGreaterThanOrEqual(0);
  expect(probe.firstColor).toBeLessThan(100);
  const endScale = probe.endScale === "none" ? 1 : Number.parseFloat(probe.endScale);
  expect(endScale).toBe(1);
  expect(probe.transitions.some((entry) => entry.property === "scale")).toBe(false);
});

test("paging and search acknowledge within 100ms while old rows stay", async ({ page }) => {
  await page.addInitScript(() => {
    (window as unknown as { __YEARNING_MOCK_LATENCY__?: number }).__YEARNING_MOCK_LATENCY__ = 300;
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
  await mockSession(page, "admin");
  await page.goto("/admin/datasources");

  const rows = page.locator('[data-testid^="ds-row-"]');
  await expect(rows.first()).toBeVisible({ timeout: 20_000 });

  await page.getByTestId("datasources-page-size-10").click();
  await expect(rows).toHaveCount(10, { timeout: 5_000 });

  const next = page.getByTestId("datasources-next");
  await expect(next).toBeEnabled();
  await expect(page.getByTestId("datasources-busy")).toBeHidden();
  await armBusyProbe(page, "datasources-busy", "datasources-next");
  await next.click();
  await expect(page.getByTestId("datasources-busy")).toBeVisible({ timeout: 2_000 });
  const pagingProbe = await readBusyProbe(page);
  expect(pagingProbe.triggerAt, "pointerdown was not observed").not.toBeNull();
  expect(pagingProbe.busyAt, "busy dot was not observed").not.toBeNull();
  const pagingDelay = (pagingProbe.busyAt ?? 0) - (pagingProbe.triggerAt ?? 0);
  expect(pagingDelay).toBeGreaterThanOrEqual(0);
  expect(pagingDelay).toBeLessThan(100);
  await expect(next).toBeDisabled();
  await expect(rows).toHaveCount(10);
  await expect(page.getByTestId("datasources-busy")).toBeHidden({ timeout: 5_000 });

  await expect(page.getByTestId("datasources-busy")).toBeHidden();
  await armBusyProbe(page, "datasources-busy", "admin-datasources-search");
  await page.getByTestId("admin-datasources-search").fill("prod");
  await expect(page.getByTestId("datasources-busy")).toBeVisible({ timeout: 2_000 });
  const searchProbe = await readBusyProbe(page);
  expect(searchProbe.triggerAt, "input event was not observed").not.toBeNull();
  expect(searchProbe.busyAt, "busy dot was not observed").not.toBeNull();
  const searchDelay = (searchProbe.busyAt ?? 0) - (searchProbe.triggerAt ?? 0);
  expect(searchDelay).toBeGreaterThanOrEqual(0);
  expect(searchDelay).toBeLessThan(100);
  await expect(page.getByTestId("datasources-busy")).toBeHidden({ timeout: 5_000 });
  await expect(rows.first()).toBeVisible();
});

test("reduced motion toasts keep their stacking offset without a transform transition", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await mockSession(page, "admin");
  await page.goto("/admin/settings/identity");

  const trigger = page.locator('[data-testid^="admin-idp-test-"]').first();
  await expect(trigger).toBeVisible();

  const toasts = page.locator("[data-sonner-toast]");
  await trigger.click();
  await expect(toasts).toHaveCount(1);
  await page.waitForTimeout(200);
  await trigger.click();
  await expect(toasts).toHaveCount(2);

  const stacked = await toasts.evaluateAll((els) =>
    els.map((el) => {
      const rect = el.getBoundingClientRect();
      return { top: rect.top, transition: getComputedStyle(el).transitionProperty };
    }),
  );
  const [first, second] = stacked;
  if (first === undefined || second === undefined) throw new Error("expected two stacked toasts");
  // Sonner uses the inline `--y` transform as the stacked-toast offset. A
  // reduced-motion override may disable the *transition* on transform, but it
  // must not remove the transform itself, or every toast lands on top of the
  // previous one.
  expect(second.top - first.top).toBeGreaterThan(8);
  expect(first.transition).not.toContain("transform");
});
