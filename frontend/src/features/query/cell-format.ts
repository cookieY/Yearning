/**
 * Pure JSON cell formatter shared between the virtualized query result grid
 * and the streaming OOXML Web Worker serializer (never "[object Object]").
 */
export function stringifyCell(value: unknown): string {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    return String(value);
  }
  return JSON.stringify(value);
}
