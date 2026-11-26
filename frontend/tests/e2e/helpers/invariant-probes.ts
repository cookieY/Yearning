import { expect, type Locator, type Page } from "@playwright/test";
import { mockSession, setMockIdentity, type MockAuthBehavior, type MockIdentity } from "./auth";

export type AuditLocale = "zh-CN" | "en-US";
export interface AuditViewport {
  width: number;
  height: number;
}

export async function bootstrapAuditPage(
  page: Page,
  opts: {
    viewport: AuditViewport;
    locale: AuditLocale;
    session?: MockAuthBehavior;
    identity?: MockIdentity;
    scenario?: string | null;
  },
): Promise<void> {
  await page.setViewportSize(opts.viewport);
  await page.addInitScript(
    `window.localStorage.setItem('yearning-locale', '${opts.locale}');` +
      (opts.scenario ? `window.localStorage.setItem('yearning-mock-scenario', '${opts.scenario}');` : ""),
  );
  await mockSession(page, opts.session ?? "admin");
  await setMockIdentity(page, opts.identity ?? "henry");
}

export async function assertPageGeometry(page: Page, routeLabel: string): Promise<void> {
  const result = await page.evaluate(() => {
    const vpW = window.innerWidth;
    const scrollW = document.documentElement.scrollWidth;
    const overlaps: string[] = [];

    const controlGroups = document.querySelectorAll(
      '[data-slot="page-actions"], [data-slot="list-toolbar"], header',
    );
    for (const group of controlGroups) {
      const children = Array.from(
        group.querySelectorAll<HTMLElement>("button, input, a, [role='combobox']"),
      ).filter((el) => {
        const s = getComputedStyle(el);
        const r = el.getBoundingClientRect();
        return s.display !== "none" && s.visibility !== "hidden" && r.width > 0 && r.height > 0;
      });
      for (let i = 0; i < children.length; i += 1) {
        const a = children[i];
        if (a === undefined) continue;
        const ra = a.getBoundingClientRect();
        for (let j = i + 1; j < children.length; j += 1) {
          const b = children[j];
          if (b === undefined || a.contains(b) || b.contains(a)) continue;
          const rb = b.getBoundingClientRect();
          const ix = Math.max(0, Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left));
          const iy = Math.max(0, Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top));
          if (ix > 1 && iy > 1) {
            overlaps.push(
              `${a.dataset.testid ?? a.tagName} vs ${b.dataset.testid ?? b.tagName}`,
            );
          }
        }
      }
    }

    const badCards: string[] = [];
    for (const card of document.querySelectorAll<HTMLElement>('[data-slot="card"]')) {
      if (card.closest("[hidden]") !== null) continue;
      const r = card.getBoundingClientRect();
      const s = getComputedStyle(card);
      if (s.display === "none" || s.visibility === "hidden") continue;
      if (r.width <= 0 || r.right > vpW + 2) {
        badCards.push(`${card.dataset.testid ?? "card"} w=${String(r.width)} right=${String(r.right)}`);
      }
    }

    return { vpW, scrollW, overlaps, badCards };
  });

  expect(result.scrollW, `${routeLabel}: horizontal overflow`).toBeLessThanOrEqual(result.vpW + 1);
  expect(result.overlaps, `${routeLabel}: overlapping controls`).toEqual([]);
  expect(result.badCards, `${routeLabel}: card geometry clipped or collapsed`).toEqual([]);
}

export async function assertOverlayInvariants(
  _page: Page,
  overlay: Locator,
  label: string,
): Promise<void> {
  await expect(overlay, `${label}: overlay should be visible`).toBeVisible({ timeout: 5_000 });
  const diag = await overlay.evaluate((el) => {
    const r = el.getBoundingClientRect();
    const vpW = window.innerWidth;
    const vpH = window.innerHeight;
    const clippedBy: string[] = [];

    let cur: HTMLElement | null = el.parentElement;
    while (cur !== null && cur !== document.documentElement) {
      const cs = getComputedStyle(cur);
      const ov = `${cs.overflowX}:${cs.overflowY}`;
      if (/(hidden|clip|scroll|auto)/.test(ov)) {
        const ar = cur.getBoundingClientRect();
        if (
          ar.top > r.top + 1 ||
          ar.bottom < r.bottom - 1 ||
          ar.left > r.left + 1 ||
          ar.right < r.right - 1
        ) {
          clippedBy.push(`${cur.tagName}.${cur.className.slice(0, 40)} (${ov})`);
        }
      }
      cur = cur.parentElement;
    }

    const cx = Math.min(vpW - 2, Math.max(2, r.left + r.width / 2));
    const cy = Math.min(vpH - 2, Math.max(2, r.top + r.height / 2));
    const topHit = document.elementFromPoint(cx, cy);
    const hitInside = topHit !== null && (el.contains(topHit) || topHit.contains(el));

    return {
      rect: { top: r.top, left: r.left, right: r.right, bottom: r.bottom, width: r.width, height: r.height },
      vpW,
      vpH,
      clippedBy,
      hitInside,
      hitTag: topHit ? `${topHit.tagName}.${String(topHit.getAttribute("data-testid") ?? "")}` : "none",
    };
  });

  expect(diag.rect.width, `${label}: width > 0`).toBeGreaterThan(0);
  expect(diag.rect.height, `${label}: height > 0`).toBeGreaterThan(0);
  expect(diag.rect.top, `${label}: top >= 0`).toBeGreaterThanOrEqual(-1);
  expect(diag.rect.left, `${label}: left >= 0`).toBeGreaterThanOrEqual(-1);
  expect(diag.rect.right, `${label}: right <= vpW`).toBeLessThanOrEqual(diag.vpW + 1);
  expect(diag.rect.bottom, `${label}: bottom <= vpH`).toBeLessThanOrEqual(diag.vpH + 1);
  expect(diag.clippedBy, `${label}: ancestor overflow clipping`).toEqual([]);
  expect(diag.hitInside, `${label}: elementFromPoint obscured by ${diag.hitTag}`).toBe(true);
}

export async function installShiftTracker(page: Page): Promise<void> {
  await page.evaluate(() => {
    const win = window as unknown as {
      __invariantShifts?: number;
      __invariantShiftObs?: PerformanceObserver;
    };
    win.__invariantShiftObs?.disconnect();
    win.__invariantShifts = 0;
    const obs = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const ls = entry as PerformanceEntry & { value?: number };
        if (typeof ls.value === "number") {
          win.__invariantShifts = (win.__invariantShifts ?? 0) + ls.value;
        }
      }
    });
    obs.observe({ type: "layout-shift", buffered: false });
    win.__invariantShiftObs = obs;
  });
}

export async function readShiftScore(page: Page): Promise<number> {
  return page.evaluate(() => {
    const win = window as unknown as { __invariantShifts?: number };
    return win.__invariantShifts ?? 0;
  });
}

export async function collectVisibleOverlayTriggers(
  page: Page,
  coveredTestIds: Set<string>,
): Promise<string[]> {
  return page.evaluate((covered) => {
    const seenPrefixes = new Set(covered);
    const triggers = document.querySelectorAll<HTMLElement>(
      '[aria-haspopup], [data-slot="popover-trigger"], [data-slot="select-trigger"], [data-slot="dropdown-menu-trigger"]',
    );
    const uncovered: string[] = [];
    for (const el of triggers) {
      const s = getComputedStyle(el);
      const r = el.getBoundingClientRect();
      if (s.display === "none" || s.visibility === "hidden" || r.width === 0 || r.height === 0) {
        continue;
      }
      if ((el as HTMLButtonElement).disabled || el.getAttribute("aria-disabled") === "true") {
        continue;
      }
      const key =
        el.dataset.testid ??
        el.getAttribute("aria-label") ??
        el.id ??
        `${el.tagName}:${el.textContent.trim().slice(0, 24)}`;
      const matched = Array.from(seenPrefixes).some(
        (prefix) => key === prefix || key.startsWith(prefix),
      );
      if (!matched) {
        uncovered.push(key);
        seenPrefixes.add(key);
      }
    }
    return uncovered;
  }, Array.from(coveredTestIds));
}
