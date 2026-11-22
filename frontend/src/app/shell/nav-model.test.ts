import { describe, expect, it } from "vitest";
import { filterNavGroups, isNavItemActive, NAV_GROUPS } from "./nav-model";

describe("navigation IA baseline (spec §5.1)", () => {
  it("declares exactly the five authority groups in order", () => {
    expect(NAV_GROUPS.map((group) => group.labelKey)).toEqual([
      "nav.group.workspace",
      "nav.group.audit",
      "nav.group.query",
      "nav.group.admin",
      "nav.group.reviewEngine",
    ]);
    const adminItems = NAV_GROUPS.find((g) => g.labelKey === "nav.group.admin")?.items.map((i) => i.labelKey);
    expect(adminItems?.slice(0, 2)).toEqual(["nav.admin.diagnostics", "nav.admin.users"]);
    const reviewItems = NAV_GROUPS.find((g) => g.labelKey === "nav.group.reviewEngine")?.items.map((i) => i.labelKey);
    expect(reviewItems?.slice(0, 2)).toEqual(["nav.reviewEngine.providers", "nav.reviewEngine.skills"]);
  });

  it("has no AI group and no chat route — v4 has no chat shell", () => {
    // 2026-10-31 owner ruling: AI Budget moved into the Review Engine group (cost control sits next to the Provider
    // domain). This assertion preserves the original intent — no AI chat group, no chat route; it no longer forbids
    // "ai" in admin item names (AI Budget is a legitimate entry).
    expect(NAV_GROUPS.some((group) => group.labelKey.toLowerCase().includes("ai"))).toBe(false);
    const flat = NAV_GROUPS.flatMap((group) => group.items);
    expect(flat.some((item) => item.to.includes("chat"))).toBe(false);
  });

  it("admin and review-engine entries are admin-visible only", () => {
    for (const group of NAV_GROUPS.filter(
      (candidate) =>
        candidate.labelKey === "nav.group.admin" ||
        candidate.labelKey === "nav.group.reviewEngine",
    )) {
      for (const item of group.items) {
        expect(item.visibility).toBe("admin");
      }
    }
  });
});

describe("filterNavGroups", () => {
  it("hides every admin group from a plain user", () => {
    const visible = filterNavGroups(NAV_GROUPS, "user");
    const labelKeys = visible.map((group) => group.labelKey);
    expect(labelKeys).not.toContain("nav.group.admin");
    expect(labelKeys).not.toContain("nav.group.reviewEngine");
    expect(labelKeys).toEqual([
      "nav.group.workspace",
      "nav.group.audit",
      "nav.group.query",
    ]);
  });

  it("shows every group to admin", () => {
    const visible = filterNavGroups(NAV_GROUPS, "admin");
    expect(visible).toHaveLength(NAV_GROUPS.length);
  });

  it("drops a group entirely when all of its entries are filtered out", () => {
    const adminGroup = NAV_GROUPS.find(
      (candidate) => candidate.labelKey === "nav.group.admin",
    );
    if (!adminGroup) throw new Error("admin group missing from NAV_GROUPS");
    const singleAdminGroup = [
      { labelKey: "nav.group.admin", items: adminGroup.items },
    ];
    expect(filterNavGroups(singleAdminGroup, "user")).toEqual([]);
  });
});

describe("isNavItemActive", () => {
  const allItems = NAV_GROUPS.flatMap((group) => group.items);

  it.each<[string, string]>([
    ["/workspace", "nav.home"],
    ["/changes/mine", "nav.myOrders"],
    ["/changes/orders/ord-1", "nav.myOrders"],
    ["/changes/new", "nav.orderSubmit"],
    ["/changes/drafts/drf-1", "nav.orderSubmit"],
    ["/approvals/changes", "nav.orderApprovals"],
    ["/approvals/query-access", "nav.orderApprovals"],
    ["/approvals/orders/ord-1", "nav.orderApprovals"],
    ["/records", "nav.auditRecords"],
    ["/query", "nav.query"],
    ["/query/sessions/qs-1", "nav.query"],
    ["/admin/settings", "nav.admin.settings"],
    ["/admin/settings/general", "nav.admin.settings"],
    ["/admin/settings/execution/", "nav.admin.settings"],
    ["/admin/identity-providers", "nav.admin.settings"],
    ["/admin/identity-providers/oidc-1", "nav.admin.settings"],
    ["/admin/review-engine/ai-budget", "nav.reviewEngine.aiBudget"],
    ["/admin/rule-sets", "nav.reviewEngine.ruleSets"],
  ])("highlights exactly %s -> %s", (pathname, expectedLabelKey) => {
    const activeKeys = allItems
      .filter((item) => isNavItemActive(item, pathname))
      .map((item) => item.labelKey);
    expect(activeKeys).toEqual([expectedLabelKey]);
  });

  it.each([
    "/",
    "/profile",
    "/admin/settings-extra",
    "/admin/identity-providers-v2",
    "/query-other",
    "/records-backup",
    "/approvals-extra",
    "/changes/orders-old",
  ])("does not false-match sibling path %s on segment boundaries", (pathname) => {
    const activeKeys = allItems
      .filter((item) => isNavItemActive(item, pathname))
      .map((item) => item.labelKey);
    expect(activeKeys).toEqual([]);
  });
});
