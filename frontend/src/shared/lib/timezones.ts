import { useSyncExternalStore } from "react";

/** IANA zone choices for timezone pickers: the runtime's supported zones plus
 * UTC. The currently stored value is always kept selectable (even when the
 * runtime list lacks it) so a server-held zone is displayed rather than
 * blanked. The server stays the authority on whether a zone really exists. */
export function timezoneOptions(current: string): string[] {
  const zones = new Set<string>(["UTC"]);
  try {
    for (const zone of Intl.supportedValuesOf("timeZone")) zones.add(zone);
  } catch {
    ["Asia/Shanghai", "Asia/Tokyo", "Europe/London", "America/New_York"].forEach((zone) => zones.add(zone));
  }
  if (current.trim() !== "") zones.add(current);
  return [...zones].sort((left, right) => left.localeCompare(right));
}

const DEFAULT_SYSTEM_TIMEZONE = "Asia/Shanghai";
let activeSystemTimezone = DEFAULT_SYSTEM_TIMEZONE;
const listeners = new Set<() => void>();

export function setSystemTimezone(zone: string | null | undefined): void {
  const trimmed = (zone ?? "").trim();
  if (trimmed === "" || trimmed === activeSystemTimezone) return;
  activeSystemTimezone = trimmed;
  for (const listener of listeners) {
    listener();
  }
}

export function useSystemTimezone(): string {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    () => activeSystemTimezone,
    () => activeSystemTimezone,
  );
}

/**
 * Formats an ISO-8601 timestamp in the administrator-configured system timezone
 * (from general settings / GET /users/me `system_timezone`) as
 * `YYYY-MM-DD HH:mm:ss`.
 */
export function formatTimestamp(
  value: string | null | undefined,
  timeZone?: string,
): string {
  if (value === null || value === undefined || value.trim() === "") {
    return "—";
  }
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value.replace("T", " ").replace("Z", " UTC");
  }
  const targetZone = (timeZone ?? activeSystemTimezone).trim() || DEFAULT_SYSTEM_TIMEZONE;
  const formatWithZone = (zone: string): string => {
    const parts = new Intl.DateTimeFormat("en-CA", {
      timeZone: zone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    }).formatToParts(parsed);
    const map: Record<string, string> = {};
    for (const part of parts) {
      if (part.type !== "literal") {
        map[part.type] = part.value;
      }
    }
    const hour = map.hour === "24" ? "00" : (map.hour ?? "00");
    return `${map.year ?? "0000"}-${map.month ?? "01"}-${map.day ?? "01"} ${hour}:${map.minute ?? "00"}:${map.second ?? "00"}`;
  };
  try {
    return formatWithZone(targetZone);
  } catch {
    try {
      return formatWithZone(DEFAULT_SYSTEM_TIMEZONE);
    } catch {
      return formatWithZone("UTC");
    }
  }
}

const ISO_TIMESTAMP_IN_TEXT =
  /\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\b/g;

/**
 * Formats any embedded ISO-8601 timestamps inside a server watermark string
 * (e.g. `Yearning SQL Viewer: admin @ 2026-11-09T14:05:38Z`) using the active
 * system timezone (`YYYY-MM-DD HH:mm:ss`).
 */
export function formatWatermark(
  watermark: string | null | undefined,
  timeZone?: string,
): string {
  if (watermark === null || watermark === undefined || watermark === "") {
    return "";
  }
  return watermark.replace(ISO_TIMESTAMP_IN_TEXT, (iso) => formatTimestamp(iso, timeZone));
}

/**
 * Formats an instant (ISO timestamp or Date) as a YYYY-MM-DD calendar date key
 * in the active system timezone (general.system_timezone, defaulting to Asia/Shanghai).
 */
export function dateKeyInTimeZone(
  value: string | Date,
  timeZone?: string,
): string {
  const parsed = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return "";
  }
  const targetZone = (timeZone ?? activeSystemTimezone).trim() || DEFAULT_SYSTEM_TIMEZONE;
  const formatWithZone = (zone: string): string =>
    new Intl.DateTimeFormat("en-CA", {
      timeZone: zone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).format(parsed);
  try {
    return formatWithZone(targetZone);
  } catch {
    try {
      return formatWithZone(DEFAULT_SYSTEM_TIMEZONE);
    } catch {
      return formatWithZone("UTC");
    }
  }
}

