import { afterEach, describe, expect, it } from "vitest";
import { dateKeyInTimeZone, formatTimestamp, setSystemTimezone } from "./timezones";

describe("timezones fallback and formatting", () => {
  afterEach(() => {
    setSystemTimezone("Asia/Shanghai");
  });

  it("falls back consistently to Asia/Shanghai for empty or invalid timezones", () => {
    const iso = "2026-11-24T17:30:00Z";
    expect(formatTimestamp(iso)).toBe("2026-11-25 01:30:00");
    expect(formatTimestamp(iso, "")).toBe("2026-11-25 01:30:00");
    expect(formatTimestamp(iso, "Mars/Olympus_Mons")).toBe("2026-11-25 01:30:00");

    expect(dateKeyInTimeZone(iso)).toBe("2026-11-25");
    expect(dateKeyInTimeZone(iso, "")).toBe("2026-11-25");
    expect(dateKeyInTimeZone(iso, "Mars/Olympus_Mons")).toBe("2026-11-25");
  });

  it("formats explicit valid IANA timezones accurately", () => {
    const iso = "2026-11-24T17:30:00Z";
    expect(formatTimestamp(iso, "UTC")).toBe("2026-11-24 17:30:00");
    expect(dateKeyInTimeZone(iso, "UTC")).toBe("2026-11-24");

    setSystemTimezone("America/Los_Angeles");
    expect(formatTimestamp(iso)).toBe("2026-11-24 09:30:00");
    expect(dateKeyInTimeZone(iso)).toBe("2026-11-24");
  });
});
