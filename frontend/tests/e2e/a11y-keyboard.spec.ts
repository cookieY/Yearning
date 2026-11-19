import { expect, test } from "@playwright/test";
import { mockSession, setMockAuthBehavior } from "./helpers/auth";

// FE-F11 keyboard and focus audit (design spec §11: keyboard, focus order, focus ring,
// overlay focus restoration). These tests complement the axe sweep with behavioral
// checks axe cannot express: tab order, dialog focus traps, focus return
// after overlay close, visible focus indicators and keyboard-only overlay
// operation.
test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

test("login tab order reaches username, password and submit with visible rings", async ({
  page,
}) => {
  await page.goto("/login");
  await expect(page.getByLabel(/\u7528\u6237\u540d|Username/)).toBeVisible();

  // The brand mark may precede the form in tab order; walk until username.
  for (let i = 0; i < 5; i += 1) {
    await page.keyboard.press("Tab");
    if (await page.getByLabel(/\u7528\u6237\u540d|Username/).evaluate((el) => el === document.activeElement)) {
      break;
    }
  }
  await expect(page.getByLabel(/\u7528\u6237\u540d|Username/)).toBeFocused();
  const submit = page.getByRole("button", { name: /\u767b\u5f55|Sign in/ });
  // The submit is disabled on an empty form (not tabbable by design); fill
  // the required fields so the walk reaches an enabled submit.
  await page.getByLabel(/\u7528\u6237\u540d|Username/).fill("henry");
  await page.getByLabel(/\u5bc6\u7801|Password/).fill("fixture-pw");
  await page.keyboard.press("Tab");
  for (let i = 0; i < 4; i += 1) {
    if (await submit.evaluate((el) => el === document.activeElement)) {
      break;
    }
    await page.keyboard.press("Tab");
  }
  await expect(submit).toBeFocused();

  const ring = await submit.evaluate((element) => {
    const style = getComputedStyle(element);
    return { shadow: style.boxShadow, outline: style.outlineStyle };
  });
  // focus-visible must be visually expressed (design spec: focus ring).
  expect(ring.shadow !== "none" || ring.outline !== "none").toBe(true);
});

test("keyboard-only session opens, operates and dismisses the user sheet", async ({ page }) => {
  await mockSession(page, "default");
  await page.goto("/workspace");
  await expect(page.getByTestId("workspace-page")).toBeVisible();

  const trigger = page.getByTestId("user-menu-trigger");
  await expect(trigger).toHaveAttribute("aria-haspopup", "dialog");
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  await trigger.focus();
  await page.keyboard.press("Enter");
  // The user menu is the template Profile Sheet — a side dialog, so the
  // overlay contract (focus in, Escape out, focus restored) is the dialog's.
  const sheet = page.getByRole("dialog");
  await expect(sheet).toBeVisible();
  await expect(trigger).toHaveAttribute("aria-expanded", "true");
  const controls = await trigger.getAttribute("aria-controls");
  expect(controls).not.toBeNull();
  await expect(sheet).toHaveAttribute("id", controls ?? "");
  // Focus moves into the overlay (asynchronously with the open animation).
  await expect
    .poll(() =>
      page.evaluate(() => {
        const portal = document.querySelector('[data-slot="sheet-portal"]');
        const el = document.activeElement;
        if (portal !== null && el !== null) return portal.contains(el);
        const dialog = document.querySelector('[role="dialog"]');
        return dialog !== null && el !== null && dialog.contains(el);
      }),
    )
    .toBe(true);

  await page.keyboard.press("Escape");
  await expect(sheet).not.toBeVisible();
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  // Focus returns to the trigger (overlay focus restoration).
  await expect(trigger).toBeFocused();
});

test("the datasource edit dialog traps focus and returns it after close", async ({ page }) => {
  await mockSession(page, "admin");
  await page.goto("/admin/datasources");
  await expect(page.getByTestId(/ds-row-/).first()).toBeVisible();

  // Row actions live in the ⋯ kebab; the menu item opens the dialog and the
  // menu hands focus back to its trigger, so focus-return asserts on it.
  const kebab = page.getByTestId(/ds-actions-/).first();
  await kebab.click();
  await page.getByTestId(/ds-edit-/).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();

  // Tab walks stay inside the overlay for many stops (focus trap). Base UI
  // parks transient focus on hidden guards inside the portal — containment
  // root is the portal, not the [role=dialog] subtree — and recovers stray
  // focus asynchronously, so each stop settles like a real keypress.
  let escaped = false;
  for (let i = 0; i < 25; i += 1) {
    await page.keyboard.press("Tab");
    await page.waitForTimeout(80);
    const inside = await page.evaluate(() => {
      const el = document.activeElement;
      if (el === null) return false;
      const portal = document.querySelector('[data-slot="dialog-portal"]');
      return portal !== null && portal.contains(el);
    });
    if (!inside) {
      escaped = true;
      break;
    }
  }
  expect(escaped).toBe(false);

  await page.keyboard.press("Escape");
  await expect(dialog).not.toBeVisible();
  await expect(kebab).toBeFocused();
});

test("session loss mid-dialog never traps the keyboard in a dead overlay", async ({ page }) => {
  await mockSession(page, "admin");
  await page.goto("/admin/datasources");
  await expect(page.getByTestId(/ds-row-/).first()).toBeVisible();

  await page.getByTestId(/ds-actions-/).first().click();
  await page.getByTestId(/ds-edit-/).first().click();
  await expect(page.getByRole("dialog")).toBeVisible();

  await setMockAuthBehavior(page, "expired");
  await page.reload();
  await expect(page).toHaveURL(/\/login/);
  // Hard gate: the login form is interactive again — the keyboard is not
  // trapped in a dead overlay. (Where the browser parks focus after a
  // reload is browser-owned and not asserted here.)
  await expect(page.getByLabel(/\u7528\u6237\u540d|Username/)).toBeVisible();
  await expect(page.getByLabel(/\u5bc6\u7801|Password/)).toBeVisible();
});
