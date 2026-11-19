import { expect, test } from "@playwright/test";

import { mockSession } from "./helpers/auth";

// Real enter/exit asymmetry gate: tw-animate publishes the two durations as CSS
// animations on the popup element, so the ratio is read from the browser instead
// of trusted from class strings. One interval probe is armed before opening so
// neither the 160ms entry nor the 110ms exit can be missed by a round-trip.
test("popover enter:exit duration ratio stays between 1.2 and 2.0", async ({ page }) => {
  await mockSession(page, "default");
  // The /changes/mine date pickers are gone (owner feedback 2026-11-05: the
  // ECS-style global search bar absorbed every filter control, its date
  // values come from an inline calendar — no popover). The renewal dialog
  // carries the shared DatePicker now, inside the query-session world that
  // seeds the fixture grant.
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-mock-scenario", "query-session");
  });
  await page.goto("/query/sessions/qs-fixture-active");
  await expect(page.getByTestId("query-workspace")).toBeVisible({ timeout: 15_000 });
  await page.getByTestId("query-grant-renew").click();

  const trigger = page.getByTestId("query-renew-until");
  await expect(trigger).toBeVisible({ timeout: 15_000 });

  await page.evaluate(() => {
    const probe = { enter: [] as number[], exit: [] as number[], done: false };
    (window as unknown as { __animProbe: typeof probe }).__animProbe = probe;
    const sample = (suffix: "enter" | "exit"): number[] =>
      [...document.querySelectorAll("[data-slot='popover-content']")].flatMap((element) =>
        element
          .getAnimations()
          .filter((animation): animation is CSSAnimation => animation instanceof CSSAnimation)
          .filter((animation) => animation.animationName.includes(suffix))
          .map((animation) => Number(animation.effect?.getTiming().duration ?? 0))
          .filter((duration) => duration > 0),
      );
    const interval = window.setInterval(() => {
      if (probe.enter.length === 0) probe.enter = sample("enter");
      if (probe.exit.length === 0) probe.exit = sample("exit");
      if (probe.enter.length > 0 && probe.exit.length > 0) {
        probe.done = true;
        window.clearInterval(interval);
      }
    }, 10);
    window.setTimeout(() => {
      probe.done = true;
      window.clearInterval(interval);
    }, 3_000);
  });

  await trigger.click();
  const content = page.locator("[data-slot='popover-content']").first();
  await expect(content).toBeVisible();
  await expect
    .poll(() => page.evaluate(() => (window as unknown as { __animProbe: { enter: number[] } }).__animProbe.enter.length), {
      timeout: 1_000,
    })
    .toBeGreaterThan(0);

  // Close by toggling the trigger: Escape would bubble to the host dialog and
  // unmount the popover before its exit animation can play.
  await trigger.click();
  await expect(content).toBeHidden({ timeout: 2_000 });
  await expect
    .poll(() => page.evaluate(() => (window as unknown as { __animProbe: { exit: number[] } }).__animProbe.exit.length), {
      timeout: 1_000,
    })
    .toBeGreaterThan(0);

  const probe = await page.evaluate(() =>
    (window as unknown as { __animProbe: { enter: number[]; exit: number[] } }).__animProbe,
  );
  const enterMs = Math.max(...probe.enter);
  const exitMs = Math.min(...probe.exit);
  const ratio = enterMs / exitMs;
  expect(ratio, `enter ${enterMs}ms / exit ${exitMs}ms`).toBeGreaterThanOrEqual(1.2);
  expect(ratio, `enter ${enterMs}ms / exit ${exitMs}ms`).toBeLessThanOrEqual(2);
});
