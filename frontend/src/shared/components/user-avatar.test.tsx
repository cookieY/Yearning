import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  UserAvatar,
  avatarColorFor,
  avatarInitialsOf,
} from "@/shared/components/user-avatar";

/**
 * avatar-13 block contract (owner ruling 2026-10-31): the same name always
 * maps to the same palette swatch (deterministic 31-multiplier hash) and
 * initials are two letters — first two of a single word, first+last of
 * multiple words, CJK-safe.
 */
const PALETTE = [
  "avatar-swatch-orange",
  "avatar-swatch-amber",
  "avatar-swatch-teal",
  "avatar-swatch-blue",
  "avatar-swatch-sky",
];

describe("avatarColorFor", () => {
  it("is deterministic for the same name", () => {
    expect(avatarColorFor("henry")).toBe(avatarColorFor("henry"));
    expect(avatarColorFor("henry")).not.toBe(avatarColorFor("henrz"));
  });

  it("only ever returns palette swatches", () => {
    const names = [
      "a", "b", "c", "d", "e", "f", "g", "h",
      "reviewer-a", "executor-a", "\u5f20\u4f1f", "\u674e\u5a1c", "0", "42", "", "ⓧ",
    ];
    for (const name of names) {
      expect(PALETTE).toContain(avatarColorFor(name));
    }
  });

  it("covers the whole palette across distinct names", () => {
    const seen = new Set<string>();
    // Iterate enough names that every hash bucket is hit.
    for (let code = 65; code < 65 + 26 && seen.size < PALETTE.length; code += 1) {
      for (let repeat = 1; repeat <= 6; repeat += 1) {
        seen.add(avatarColorFor(String.fromCharCode(code).repeat(repeat)));
      }
    }
    expect([...seen].sort()).toEqual([...PALETTE].sort());
  });
});

describe("avatarInitialsOf", () => {
  it("falls back to '?' for blank names", () => {
    expect(avatarInitialsOf("")).toBe("?");
    expect(avatarInitialsOf("   ")).toBe("?");
    expect(avatarInitialsOf(" \t \n ")).toBe("?");
  });

  it("takes the first two characters of a single word", () => {
    expect(avatarInitialsOf("henry")).toBe("HE");
    expect(avatarInitialsOf("H")).toBe("H");
    expect(avatarInitialsOf("\u5f20\u4f1f\u660e")).toBe("\u5f20\u4f1f");
  });

  it("takes first+last initials of multi-word names, ignoring extra whitespace", () => {
    expect(avatarInitialsOf("Ada Lovelace")).toBe("AL");
    expect(avatarInitialsOf("  Grace   Brewster   Hopper  ")).toBe("GH");
    expect(avatarInitialsOf("\u738b \u5c0f \u660e")).toBe("\u738b\u660e");
  });
});

describe("UserAvatar", () => {
  it("renders the deterministic swatch with the initials and merges the className", () => {
    render(<UserAvatar name="Ada Lovelace" className="size-10" />);
    const avatar = screen.getByText("AL");
    expect(avatar).toHaveAttribute("aria-hidden");
    expect(avatar.className).toContain(avatarColorFor("Ada Lovelace"));
    expect(avatar.className).toContain("size-10");
    expect(avatar.className).toContain("rounded-full");
  });
});
