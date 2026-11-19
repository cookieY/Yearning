export interface CursorPage<T> {
  items?: T[];
  page: {
    next_cursor: string | null;
    has_more: boolean;
  };
}

/**
 * Reads every page of a cursor-backed collection. Consumers that filter or
 * aggregate client-side must start from the complete collection; keeping only
 * the first page would make the filter silently incomplete.
 */
export async function fetchAllCursorPages<T>(
  loadPage: (after: string | null) => Promise<CursorPage<T>>,
): Promise<T[]> {
  const seenCursors = new Set<string>();
  let after: string | null = null;
  let items: T[] = [];

  for (;;) {
    const page = await loadPage(after);
    items = [...items, ...(page.items ?? [])];
    if (!page.page.has_more) return items;

    const nextCursor = page.page.next_cursor;
    if (nextCursor === null || nextCursor === "") {
      throw new Error("cursor pagination reported has_more with a missing next cursor");
    }
    if (seenCursors.has(nextCursor)) {
      throw new Error("cursor pagination returned a repeated cursor");
    }
    seenCursors.add(nextCursor);
    after = nextCursor;
  }
}
