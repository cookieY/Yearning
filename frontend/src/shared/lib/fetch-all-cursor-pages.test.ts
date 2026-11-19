import { describe, expect, it } from "vitest";
import { fetchAllCursorPages } from "./fetch-all-cursor-pages";

describe("fetchAllCursorPages", () => {
  it("concatenates every page until the cursor is exhausted", async () => {
    const cursors: Array<string | null> = [];
    const items = await fetchAllCursorPages((after) => {
      cursors.push(after);
      return Promise.resolve(
        after === null
          ? {
            items: [1, 2],
            page: { has_more: true, next_cursor: "page-2" },
          }
          : {
            items: [3],
            page: { has_more: false, next_cursor: null },
          },
      );
    });

    expect(items).toEqual([1, 2, 3]);
    expect(cursors).toEqual([null, "page-2"]);
  });

  it("fails instead of looping when the server repeats a cursor", async () => {
    await expect(
      fetchAllCursorPages(() =>
        Promise.resolve({
          items: [],
          page: { has_more: true, next_cursor: "same-cursor" },
        }),
      ),
    ).rejects.toThrow("repeated cursor");
  });

  it("fails when has_more is true without a next cursor", async () => {
    await expect(
      fetchAllCursorPages(() =>
        Promise.resolve({
          items: [],
          page: { has_more: true, next_cursor: null },
        }),
      ),
    ).rejects.toThrow("missing next cursor");
  });
});
