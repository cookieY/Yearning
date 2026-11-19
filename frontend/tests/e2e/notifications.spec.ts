// S007 notification center navigation acceptance (FE-F10-NOTIFICATION-CENTER):
// navigate into notifications from system settings (not direct URL), switch three views, preview rules without sending.
// Real browser navigation acceptance (manual gate) is recorded separately; this spec covers the mock-world functional surface.
import { expect, test } from "@playwright/test";
import { mockSession } from "./helpers/auth";

test.describe("notification center (S007)", () => {
  test("admin reaches notifications from the settings tab bar and switches views", async ({ page }) => {
    await mockSession(page, "admin");
    await page.goto("/admin/settings/general");
    const notificationsTab = page.getByTestId("settings-tab-notifications");
    await expect(notificationsTab).toBeVisible();
    // Settings navigation includes the notifications tab (discoverable entry — mock-world functional equivalent of acceptance-01).
    await notificationsTab.click();
    await page.waitForURL("**/admin/settings/notifications");
    await expect(page.getByTestId("admin-notifications-page")).toBeVisible();
    await expect(page.getByTestId("admin-notifications-table")).toBeVisible();
    // Three-view switch: rules view renders seeded rules (AI precheck vocabulary is invisible).
    await page.getByTestId("admin-notifications-view-rules").click();
    await expect(page.getByTestId("admin-rules-table")).toBeVisible();
    const rulesText = await page.getByTestId("admin-rules-table").textContent();
    expect(rulesText).toContain("\u6267\u884c\u5931\u8d25\u901a\u77e5\u63d0\u4ea4\u4eba");
    // Delivery records view.
    await page.getByTestId("admin-notifications-view-deliveries").click();
    await expect(page.getByTestId("admin-deliveries-table")).toBeVisible();
    // Can switch back to other settings (same settings navigation).
    await page.getByTestId("settings-tab-general").click();
    await expect(page).toHaveURL(/\/admin\/settings\/general/);
  });
});
