import { beforeEach, describe, expect, it } from "vitest";
import {
  applySettingsToBridge,
  executionWindowAllows,
  resetSettingsBridge,
  settingsBridge,
} from "@/shared/mock/settings-bridge";

/**
 * §18.47 settings bridge — pure branch coverage: the PUT-path integration
 * test only walks happy inputs, so the validation arms (null payloads,
 * non-boolean flags, malformed window entries) and the midnight-crossing
 * window math are pinned here directly against the exported seam.
 */

const WEDNESDAY_10AM = new Date("2026-10-14T10:00:00"); // a Wednesday
const THURSDAY_09H00 = new Date("2026-10-15T09:00:00"); // a Thursday
const THURSDAY_23H30 = new Date("2026-10-15T23:30:00"); // Thursday, late arm
const FRIDAY_00H30 = new Date("2026-10-16T00:30:00"); // Friday, early arm

describe("applySettingsToBridge validation arms", () => {
  beforeEach(() => {
    resetSettingsBridge();
  });

  it("ignores null and non-object payloads", () => {
    applySettingsToBridge("query", null);
    applySettingsToBridge("query", "yes");
    expect(settingsBridge.queryApprovalEnabled).toBe(true);
  });

  it("only flips query approval on a real boolean", () => {
    applySettingsToBridge("query", { approval_enabled: "false" });
    expect(settingsBridge.queryApprovalEnabled).toBe(true);
    applySettingsToBridge("query", { approval_enabled: false });
    expect(settingsBridge.queryApprovalEnabled).toBe(false);
  });

  it("accepts restriction without windows and filters malformed window entries", () => {
    applySettingsToBridge("execution", { restriction_enabled: true });
    expect(settingsBridge.executionRestrictionEnabled).toBe(true);
    expect(settingsBridge.executionWindows).toEqual([]);

    applySettingsToBridge("execution", {
      windows: [
        null,
        "not-a-window",
        { days_of_week: "wednesday", start_local_time: "09:00", end_local_time: "10:00" },
        { days_of_week: ["wednesday", 7], start_local_time: "09:00", end_local_time: "10:00" },
      ],
    });
    expect(settingsBridge.executionWindows).toEqual([
      { days_of_week: ["wednesday"], start_local_time: "09:00", end_local_time: "10:00" },
    ]);
  });
});

describe("executionWindowAllows window math", () => {
  beforeEach(() => {
    resetSettingsBridge();
  });

  it("allows everything while the restriction is off", () => {
    expect(executionWindowAllows(WEDNESDAY_10AM)).toBe(true);
  });

  it("rejects everything when enabled with no windows (backend empty-list semantics)", () => {
    applySettingsToBridge("execution", { restriction_enabled: true });
    expect(executionWindowAllows(WEDNESDAY_10AM)).toBe(false);
  });

  it("same-day window matches day and minute range", () => {
    applySettingsToBridge("execution", {
      restriction_enabled: true,
      windows: [{ days_of_week: ["Wednesday"], start_local_time: "09:00", end_local_time: "17:00" }],
    });
    expect(executionWindowAllows(WEDNESDAY_10AM)).toBe(true);
    expect(executionWindowAllows(THURSDAY_09H00)).toBe(false); // wrong day
  });

  it("malformed HHMM times disable the window instead of throwing", () => {
    applySettingsToBridge("execution", {
      restriction_enabled: true,
      windows: [{ days_of_week: ["wednesday"], start_local_time: "9:00", end_local_time: "25:00" }],
    });
    expect(executionWindowAllows(WEDNESDAY_10AM)).toBe(false);
  });

  it("midnight-crossing windows span the previous evening and the morning after", () => {
    applySettingsToBridge("execution", {
      restriction_enabled: true,
      windows: [{ days_of_week: ["thursday"], start_local_time: "23:00", end_local_time: "01:00" }],
    });
    // Thursday 23:30 — late side of the window on the listed day itself.
    expect(executionWindowAllows(THURSDAY_23H30)).toBe(true);
    // Friday 00:30 — early side, gated on the previous day (Thursday listed).
    expect(executionWindowAllows(FRIDAY_00H30)).toBe(true);
    // A window listing only sunday never matches a mid-week instant.
    applySettingsToBridge("execution", {
      restriction_enabled: true,
      windows: [{ days_of_week: ["sunday"], start_local_time: "23:00", end_local_time: "01:00" }],
    });
    expect(executionWindowAllows(THURSDAY_23H30)).toBe(false);
    // Outside both arms entirely.
    expect(executionWindowAllows(WEDNESDAY_10AM)).toBe(false);
  });
});
