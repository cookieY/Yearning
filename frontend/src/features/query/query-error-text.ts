import i18next from "@/shared/i18n";
import { describeErrorText, type ErrorDisplay } from "@/shared/api/error-display";

const CURSOR_REJECTION_KEYS: Record<string, string> = {
  undefined_table: "query.errors.undefinedTable",
  undefined_column: "query.errors.undefinedColumn",
  data_type_mismatch: "query.errors.dataTypeMismatch",
  parse_error: "query.errors.parseError",
  insufficient_privilege: "query.errors.insufficientPrivilege",
  statement_timeout: "errors.QUERY_TIMEOUT",
  query_cancelled: "query.errors.queryCancelled",
  connection_failed: "errors.DATASOURCE_UNAVAILABLE",
  authentication_failed: "errors.DATASOURCE_AUTH_FAILED",
};

/**
 * Formats backend query/cursor errors with localized hints for `cursor_rejected_*`
 * sanitized database details while falling back to the standard error display formatter.
 */
export function describeQueryErrorText(display: ErrorDisplay): string {
  const rawDetail = typeof display.detail === "string" ? display.detail.trim() : "";
  if (
    display.messageKey === "errors.QUERY_STATEMENT_REJECTED" &&
    rawDetail.startsWith("database: cursor_rejected_")
  ) {
    const failure = rawDetail.slice("database: cursor_rejected_".length);
    const mappedKey = CURSOR_REJECTION_KEYS[failure];
    if (mappedKey !== undefined) {
      const mapped = i18next.t(mappedKey);
      return display.requestId === null ? mapped : `${mapped} (${display.requestId})`;
    }
  }
  return describeErrorText(display, { detailSeparator: " " });
}

/** Today in local timezone (YYYY-MM-DD), used by the date picker to disable past dates. */
export function todayLocal(): string {
  const now = new Date();
  return `${String(now.getFullYear())}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}
