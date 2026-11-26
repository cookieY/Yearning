import { VALID_CHANGE_ORDER_STATES } from "@/shared/mock/request-validation-states";

/**
 * Validates `GET /api/v4/change-orders` query parameters matching
 * `backend/internal/httpapi/handlers_b9.go:HandleListOrders`.
 */
export function validateListOrdersParams(url: URL): {
  err_code: number;
  message: string;
} | null {
  const rawState = (url.searchParams.get("state") ?? "").trim();
  if (rawState !== "" && !VALID_CHANGE_ORDER_STATES.has(rawState)) {
    return { err_code: 1001, message: "Invalid order state filter" };
  }

  const q = (url.searchParams.get("q") ?? "").trim();
  if (q !== "" && Array.from(q).length > 128) {
    return { err_code: 1001, message: "Invalid search keyword filter" };
  }

  const ds = (url.searchParams.get("datasource") ?? "").trim();
  if (ds !== "" && Array.from(ds).length > 128) {
    return { err_code: 1001, message: "Invalid datasource filter" };
  }

  const rawFrom = (url.searchParams.get("submitted_from") ?? "").trim();
  if (rawFrom !== "" && !isValidDateOnly(rawFrom)) {
    return { err_code: 1001, message: "Invalid submitted_from date filter" };
  }

  const rawTo = (url.searchParams.get("submitted_to") ?? "").trim();
  if (rawTo !== "" && !isValidDateOnly(rawTo)) {
    return { err_code: 1001, message: "Invalid submitted_to date filter" };
  }

  const rawLimit = url.searchParams.get("limit");
  if (rawLimit !== null && rawLimit.trim() !== "") {
    if (!/^\d+$/.test(rawLimit.trim())) {
      return { err_code: 1001, message: "Invalid limit parameter" };
    }
    const n = Number(rawLimit.trim());
    if (!Number.isSafeInteger(n) || n < 1 || n > 200) {
      return { err_code: 1001, message: "Invalid limit parameter" };
    }
  }

  return null;
}

function isValidDateOnly(value: string): boolean {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) {
    return false;
  }
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  if (month < 1 || month > 12 || day < 1 || day > 31) {
    return false;
  }
  const dt = new Date(Date.UTC(year, month - 1, day));
  return (
    dt.getUTCFullYear() === year &&
    dt.getUTCMonth() === month - 1 &&
    dt.getUTCDate() === day
  );
}
