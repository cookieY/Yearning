import i18next from "@/shared/i18n";
import { describeErrorText, type ErrorDisplay } from "@/shared/api/error-display";
import { dateKeyInTimeZone } from "@/shared/lib/timezones";

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

const REQUESTED_UNTIL_MAX_PREFIX = "requested_until_exceeds_max_minutes:";

function formatMaxDuration(minutes: number): string {
  if (minutes % 1440 === 0) {
    return i18next.t("query.errors.durationDays", { count: minutes / 1440 });
  }
  if (minutes % 60 === 0) {
    return i18next.t("query.errors.durationHours", { count: minutes / 60 });
  }
  return i18next.t("query.errors.durationMinutes", { count: minutes });
}

function withRequestId(message: string, requestId: string | null): string {
  return requestId === null ? message : `${message} (${requestId})`;
}

/**
 * Formats backend query/cursor errors with localized hints for `cursor_rejected_*`
 * and query access duration validation details while falling back to the standard
 * error display formatter.
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
      return withRequestId(i18next.t(mappedKey), display.requestId);
    }
  }
  if (display.messageKey === "errors.VALIDATION_FAILED") {
    if (rawDetail === "requested_until_in_past") {
      return withRequestId(i18next.t("query.errors.requestedUntilInPast"), display.requestId);
    }
    if (rawDetail.startsWith(REQUESTED_UNTIL_MAX_PREFIX)) {
      const minutes = Number(rawDetail.slice(REQUESTED_UNTIL_MAX_PREFIX.length));
      if (Number.isInteger(minutes) && minutes > 0) {
        return withRequestId(
          i18next.t("query.errors.requestedUntilExceedsMax", {
            limit: formatMaxDuration(minutes),
          }),
          display.requestId,
        );
      }
    }
  }
  return describeErrorText(display, { detailSeparator: " " });
}

/** Today in general.system_timezone (YYYY-MM-DD), used by the date picker to disable past dates. */
export function todayLocal(): string {
  return dateKeyInTimeZone(new Date());
}
