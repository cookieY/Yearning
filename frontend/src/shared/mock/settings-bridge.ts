/**
 * Settings → domain behavior bridge (§18.47, owner ruling 2026-10-14): the
 * admin settings fixture and the query/execution domain fixtures own
 * separate mock worlds, so a saved settings row used to change nothing
 * downstream — the forms "lacked actual function". The admin PUT now writes
 * the effective knobs through this tiny shared module (module state, reset
 * naturally on every page load exactly like the fixture worlds), and the
 * domain handlers read it: query approval disabled opens the Q002 direct
 * path, and the execution window restriction rejects begins/schedules with
 * OUTSIDE_EXECUTION_WINDOW (3008) — mirroring the backend preflight
 * (checkExecutionWindow / checkExecutionWindowAt: enabled with an empty
 * window list rejects everything, a malformed window entry is ignored, and
 * the window is a rate limiter, not a security boundary).
 */

export interface MockExecutionWindow {
  days_of_week: string[];
  start_local_time: string;
  end_local_time: string;
}

interface SettingsBridgeState {
  queryApprovalEnabled: boolean;
  executionRestrictionEnabled: boolean;
  executionWindows: MockExecutionWindow[];
}

const STORAGE_KEY = "yearning-mock-settings-bridge";

function readInitial(): SettingsBridgeState {
  // Persisted like the scenario/auth/identity dimensions: the mock world
  // resets on every page load, but the owner's saved settings knob must
  // survive a refresh (§18.47 follow-up 4).
  const fallback: SettingsBridgeState = {
    queryApprovalEnabled: true,
    executionRestrictionEnabled: false,
    executionWindows: [],
  };
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return fallback;
    const parsed = JSON.parse(raw) as Partial<SettingsBridgeState>;
    return {
      queryApprovalEnabled: typeof parsed.queryApprovalEnabled === "boolean" ? parsed.queryApprovalEnabled : true,
      executionRestrictionEnabled:
        typeof parsed.executionRestrictionEnabled === "boolean" ? parsed.executionRestrictionEnabled : false,
      executionWindows: Array.isArray(parsed.executionWindows) ? parsed.executionWindows : [],
    };
  } catch {
    return fallback;
  }
}

export const settingsBridge: SettingsBridgeState = readInitial();

function persist(): void {
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        queryApprovalEnabled: settingsBridge.queryApprovalEnabled,
        executionRestrictionEnabled: settingsBridge.executionRestrictionEnabled,
        executionWindows: settingsBridge.executionWindows,
      }),
    );
  } catch {
    // Storage unavailable (non-browser): module state only.
  }
}

/** Written by the admin settings PUT; unknown keys are ignored. */
export function applySettingsToBridge(namespace: string, settings: unknown): void {
  if (settings === null || typeof settings !== "object") return;
  const record = settings as Record<string, unknown>;
  if (namespace === "query" && typeof record.approval_enabled === "boolean") {
    settingsBridge.queryApprovalEnabled = record.approval_enabled;
    persist();
  }
  if (namespace === "execution") {
    if (typeof record.restriction_enabled === "boolean") {
      settingsBridge.executionRestrictionEnabled = record.restriction_enabled;
    }
    if (Array.isArray(record.windows)) {
      const windows: MockExecutionWindow[] = [];
      for (const entry of record.windows) {
        if (entry === null || typeof entry !== "object") continue;
        const window = entry as Partial<MockExecutionWindow>;
        if (
          Array.isArray(window.days_of_week) &&
          typeof window.start_local_time === "string" &&
          typeof window.end_local_time === "string"
        ) {
          windows.push({
            days_of_week: window.days_of_week.filter((day): day is string => typeof day === "string"),
            start_local_time: window.start_local_time,
            end_local_time: window.end_local_time,
          });
        }
      }
      settingsBridge.executionWindows = windows;
      persist();
    }
  }
}

const DAY_KEYS = ["sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"] as const;

function parseHHMM(value: string): number | null {
  const match = /^([01][0-9]|2[0-3]):([0-5][0-9])$/.exec(value);
  if (match === null) return null;
  return Number(match[1]) * 60 + Number(match[2]);
}

function windowContains(window: MockExecutionWindow, at: Date): boolean {
  const start = parseHHMM(window.start_local_time);
  const end = parseHHMM(window.end_local_time);
  if (start === null || end === null) return false;
  const day = DAY_KEYS[at.getDay()];
  const minutes = at.getHours() * 60 + at.getMinutes();
  const dayAllowed = (days: string[]): boolean => days.some((entry) => entry.toLowerCase() === day);
  // start ≤ end: same-day window; start > end: crosses midnight (backend
  // windowContains semantics).
  if (start <= end) return dayAllowed(window.days_of_week) && minutes >= start && minutes <= end;
  if (minutes >= start) return dayAllowed(window.days_of_week);
  if (minutes <= end) {
    const previous = DAY_KEYS[(at.getDay() + 6) % 7];
    return window.days_of_week.some((entry) => entry.toLowerCase() === previous);
  }
  return false;
}

/** Test seam: vitest worlds mutate the bridge via PUT handlers; the browser
 * persists across page loads via localStorage instead of resetting. */
export function resetSettingsBridge(): void {
  settingsBridge.queryApprovalEnabled = true;
  settingsBridge.executionRestrictionEnabled = false;
  settingsBridge.executionWindows = [];
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Storage unavailable: module state reset above.
  }
}

/** Whether an execution may start / be scheduled at the given instant. */
export function executionWindowAllows(at: Date): boolean {
  if (!settingsBridge.executionRestrictionEnabled) return true;
  return settingsBridge.executionWindows.some((window) => windowContains(window, at));
}
