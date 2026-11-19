import { describe, expect, it } from "vitest";
import { formatCompactCount } from "./format";

describe("formatCompactCount", () => {
  it("renders values below 1000 unchanged", () => {
    expect(formatCompactCount(0)).toBe("0");
    expect(formatCompactCount(47)).toBe("47");
    expect(formatCompactCount(999)).toBe("999");
  });

  it("compacts thousands to K (owner example: 1000 → 1K)", () => {
    expect(formatCompactCount(1000)).toBe("1K");
    expect(formatCompactCount(12345)).toBe("12K");
    expect(formatCompactCount(23981)).toBe("23K");
  });

  it("compacts millions to M (owner example: 1000000 → 1M)", () => {
    expect(formatCompactCount(1000000)).toBe("1M");
    expect(formatCompactCount(13500000)).toBe("13M");
  });

  it("compacts billions to B", () => {
    expect(formatCompactCount(1000000000)).toBe("1B");
    expect(formatCompactCount(2500000000)).toBe("2B");
  });
});
