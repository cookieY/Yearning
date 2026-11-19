/** Owner ruling 2026-09-02: large counts compact to K/M/B so big values
 * never blow up the stat-card layout — 1000 renders as 1K, 1000000 as 1M,
 * 1000000000 as 1B. Values below 1000 render unchanged. */
export function formatCompactCount(value: number): string {
  if (value >= 1e9) return String(Math.floor(value / 1e9)) + "B";
  if (value >= 1e6) return String(Math.floor(value / 1e6)) + "M";
  if (value >= 1e3) return String(Math.floor(value / 1e3)) + "K";
  return String(value);
}
