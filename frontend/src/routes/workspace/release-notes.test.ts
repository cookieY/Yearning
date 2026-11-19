import { describe, expect, it } from "vitest";
import { summarizeReleaseNotes } from "./release-notes";

describe("summarizeReleaseNotes", () => {
  it("renders plain body text unchanged", () => {
    expect(summarizeReleaseNotes("Fixed several issues and optimized performance")).toBe("Fixed several issues and optimized performance");
  });

  it("strips markdown structure: headings, lists, emphasis, links, code", () => {
    const body = [
      "## What's Changed",
      "",
      "- **Engineering**: upgrade `vite` to 8.0",
      "- fix: query export truncation [GH-123](https://github.com/cookieY/Yearning/pull/123)",
      "",
      "1. First point",
      "> Blockquote",
    ].join("\n");
    const summary = summarizeReleaseNotes(body, 200);
    expect(summary).not.toMatch(/[#*`>[\](),]/);
    expect(summary).toContain("What's Changed");
    expect(summary).toContain("Engineering");
    expect(summary).toContain("upgrade vite to 8.0");
    expect(summary).toContain("GH-123");
    expect(summary).toContain("First point");
  });

  it("truncates at 50 characters with an ellipsis (owner ruling)", () => {
    const long = "x".repeat(80);
    const summary = summarizeReleaseNotes(long);
    expect(summary).toHaveLength(51);
    expect(summary.endsWith("…")).toBe(true);
  });

  it("keeps bodies at or under the limit intact", () => {
    expect(summarizeReleaseNotes("x".repeat(50))).toHaveLength(50);
  });
});
