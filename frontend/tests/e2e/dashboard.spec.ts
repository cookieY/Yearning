import { expect, test } from "@playwright/test";
import { mockSession } from "./helpers/auth";

// Owner ruling 2026-10-23 ("promote the new home page to replace the old one"): /workspace renders
// the reference-derived dashboard — three actionable counters, the order
// overview, the announcement beside the completion ring, the recent orders and
// the execution-success panel. §18.71 (owner ruling 2026-10-16) keeps the
// platform statistics readable by every authenticated session, so the
// zero-permission home renders the same operations numbers.

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("yearning-locale", "zh-CN");
  });
});

test("admin home renders the counters, announcement and platform statistics", async ({ page }) => {
  await mockSession(page, "admin");
  await page.goto("/workspace");
  await expect(page.getByTestId("workspace-page")).toBeVisible();

  // Row 1 — the caller's own counters (/dashboard/me fixture).
  await expect(page.getByTestId("home-kpi-pending-approval")).toHaveText("1");
  await expect(page.getByTestId("home-kpi-pending-execution")).toHaveText("1");
  await expect(page.getByTestId("home-kpi-blocked-review")).toHaveText("1");

  // Operations aggregates: order total, the mini bar chart and the DDL/DML split.
  const overview = page.getByTestId("home-order-overview");
  await expect(overview).toContainText("47");
  await expect(page.getByTestId("home-mini-bars")).toBeVisible();

  // The announcement slot carries the published notice, sanitized server-side.
  await expect(page.getByTestId("home-announcement-title")).toHaveText("\u5b63\u5ea6\u7ef4\u62a4\u7a97\u53e3\u516c\u544a");
  await expect(page.getByTestId("home-announcement-body")).toContainText("\u672c\u5b63\u5ea6\u7ef4\u62a4\u5b89\u6392");

  // Completion ring + execution success panel with the state strip.
  await expect(page.getByTestId("home-completion-ring")).toBeVisible();
  await expect(page.getByTestId("home-distribution")).toContainText("89%");
  await expect(page.getByTestId("home-recent-orders")).toBeVisible();
});

test("zero-permission home still renders the platform statistics (§18.71)", async ({ page }) => {
  const operationsCalls: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/admin/dashboard/operations")) {
      operationsCalls.push(request.url());
    }
  });
  await mockSession(page, "default");
  await page.goto("/workspace");
  await expect(page.getByTestId("workspace-page")).toBeVisible();
  await expect(page.getByTestId("home-kpi-pending-approval")).toBeVisible();
  await expect(page.getByTestId("home-announcement")).toBeVisible();
  await expect(page.getByTestId("home-order-overview")).toContainText("47");
  expect(operationsCalls.length).toBeGreaterThan(0);
});

test("the manual refresh control refetches the personal dashboard", async ({ page }) => {
  await mockSession(page, "admin");
  let meCalls = 0;
  page.on("request", (request) => {
    if (request.url().includes("/dashboard/me")) {
      meCalls += 1;
    }
  });
  await page.goto("/workspace");
  await expect(page.getByTestId("workspace-page")).toBeVisible();
  const before = meCalls;
  await page.getByTestId("header-refresh").click();
  await expect.poll(() => meCalls).toBeGreaterThan(before);
});
