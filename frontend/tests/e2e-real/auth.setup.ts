import { test as setup } from "@playwright/test";
import {
  loginAdmin, loginSeedUser, statePath,
  type RealRole,
} from "./helpers/real-auth";

// One login per role per run (the backend rate-limits logins at 10
// attempts/user/minute): each role authenticates once here and the specs
// reuse the persisted storage state.
const ROLES: RealRole[] = ["admin", "submitter", "approver", "executor", "quser"];

for (const role of ROLES) {
  setup(`authenticate as ${role}`, async ({ page }) => {
    await page.addInitScript(() => {
      window.localStorage.setItem("yearning-locale", "zh-CN");
    });
    if (role === "admin") {
      await loginAdmin(page);
    } else {
      await loginSeedUser(page, role);
    }
    await page.context().storageState({ path: statePath(role) });
  });
}
