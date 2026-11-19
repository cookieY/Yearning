import { expect, test } from "@playwright/test";

import { mockSession } from "./helpers/auth";

// FLIP coverage in the browser: the hook is unit-tested, but this proves a real
// table wires it and that a genuine reorder (one row deleted, the rows below
// moving up) produces a 200ms transform animation on the surviving <tr>. A
// keyword filter that swaps in a different set of rows is intentionally
// suppressed by the hook's bound, so it cannot be used as the trigger here.
test("deleting a row FLIPs the rows below it", async ({ page }) => {
  await mockSession(page, "admin");
  await page.goto("/admin/datasources");

  const rows = page.locator("tbody tr");
  await expect(rows.first()).toBeVisible({ timeout: 15_000 });
  expect(await rows.count()).toBeGreaterThan(3);

  // analytics-pg: the unreferenced row whose ⋯ delete is enabled.
  const row = page.locator("tr", { hasText: "analytics-pg" });
  const name = (await row.locator("td").first().innerText()).trim();
  await row.getByTestId(/ds-actions-/).click();
  await page.getByTestId(/^ds-delete-/).first().click();
  await page.getByTestId("ds-delete-confirm").click();

  await page.waitForFunction(
    () => {
      const body = document.querySelector("tbody");
      if (body === null) return false;
      return body.getAnimations({ subtree: true }).some((animation) => {
        const effect = animation.effect as KeyframeEffect | null;
        if (effect === null) return false;
        return effect.getKeyframes().some((frame) => {
          const transform = (frame as Keyframe).transform;
          return typeof transform === "string" && transform !== "none" && !transform.includes("translateY(0px)");
        });
      });
    },
    null,
    { timeout: 3_000 },
  );

  const durations = await page.evaluate(() => {
    const body = document.querySelector("tbody");
    if (body === null) return [];
    return body.getAnimations({ subtree: true }).map((animation) =>
      Number(animation.effect?.getTiming().duration ?? 0),
    );
  });
  expect(durations).toContain(200);
  await expect(page.locator("tbody")).not.toContainText(name, { timeout: 5_000 });
});
