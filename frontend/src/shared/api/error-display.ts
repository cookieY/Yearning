import {
  BUSINESS_ERROR_CATALOG,
} from "@/api/generated/projections/business-error-catalog";
import { OPERATION_ERROR_PROFILES } from "@/api/generated/projections/operation-error-profiles";
import { BusinessError, TransportError } from "@/shared/api/mutator";

/**
 * The single error-to-UI mapping (frontend PRD §2.41): business errors are
 * judged against the operation's generated error profile. An err_code the
 * operation never declares is a contract violation — the UI shows the safe
 * generic message plus the request_id and sends no client telemetry. Raw
 * error names and numeric codes never reach the screen; text comes from i18n
 * keys resolved by the caller.
 */

export interface ErrorDisplay {
  /** i18n key under `errors.` describing the failure. */
  messageKey: string;
  requestId: string | null;
  /** True when the err_code was outside the operation's declared profile. */
  contractViolation: boolean;
  /** Operator-facing upstream reason snapshot (§18.75-A) from the error
   * envelope's meta.detail — e.g. the probe target's HTTP status and error
   * body excerpt. Empty when the backend sent none. */
  detail: string | null;
  /** Field-level VALIDATION_FAILED reasons from the error envelope's
   * meta.fields (RCP-20261102-S008): request field name (snake_case) →
   * constraint messages. Empty object when the backend sent none. */
  fields: Record<string, string[]>;
}

const GENERIC_BUSINESS_KEY = "errors.generic.business";
const SAFE_GENERIC_KEY = "errors.generic.safe";

/** §18.75-A: the upstream reason travels in meta.detail (bounded key set).
 * A non-string value is ignored — the envelope never renders untyped data. */
function detailOf(error: BusinessError): string | null {
  const raw = error.meta?.["detail"];
  return typeof raw === "string" && raw !== "" ? raw : null;
}

/** RCP-20261102-S008: field-level VALIDATION_FAILED reasons travel in
 * meta.fields (field name → string[]). Non-conforming shapes are dropped
 * key-by-key — the envelope never renders untyped data. */
export function fieldsOf(error: BusinessError): Record<string, string[]> {
  const raw = error.meta?.["fields"];
  if (typeof raw !== "object" || raw === null) return {};
  const out: Record<string, string[]> = {};
  for (const [field, messages] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof field !== "string" || field === "") continue;
    if (!Array.isArray(messages)) continue;
    const strings = messages.filter((m): m is string => typeof m === "string" && m !== "");
    if (strings.length > 0) out[field] = strings;
  }
  return out;
}

function operationAllowedErrCodes(operationId: string): Set<number> | null {
  for (const profile of Object.values(OPERATION_ERROR_PROFILES.profiles)) {
    if (profile.operations.includes(operationId)) {
      return new Set(profile.err_codes);
    }
  }
  return null;
}

export function describeBusinessError(error: BusinessError, operationId: string): ErrorDisplay {
  const allowed = operationAllowedErrCodes(operationId);
  const declared = allowed !== null && allowed.has(error.err_code);
  if (!declared) {
    // Undeclared err_code: safe generic message + request_id, no telemetry.
    return {
      messageKey: SAFE_GENERIC_KEY, requestId: error.requestId,
      contractViolation: true, detail: null, fields: {},
    };
  }
  const entry = error.catalogEntry;
  return {
    messageKey: entry === null ? GENERIC_BUSINESS_KEY : `errors.${entry.name}`,
    requestId: error.requestId,
    contractViolation: false,
    detail: detailOf(error),
    fields: fieldsOf(error),
  };
}

export function describeTransportError(error: TransportError): ErrorDisplay {
  const status = error.problem.status;
  const requestId =
    "request_id" in error.problem && typeof error.problem.request_id === "string"
      ? error.problem.request_id
      : null;
  let messageKey: string;
  switch (status) {
    case 0:
      messageKey = "errors.transport.network";
      break;
    case 401:
      messageKey = "errors.transport.unauthenticated";
      break;
    case 403:
      messageKey = "errors.transport.forbidden";
      break;
    case 429:
      messageKey = "errors.transport.rateLimited";
      break;
    default:
      messageKey = "errors.transport.server";
  }
  return { messageKey, requestId, contractViolation: false, detail: null, fields: {} };
}

export function describeError(error: unknown, operationId: string): ErrorDisplay {
  if (error instanceof BusinessError) return describeBusinessError(error, operationId);
  if (error instanceof TransportError) return describeTransportError(error);
  return { messageKey: SAFE_GENERIC_KEY, requestId: null, contractViolation: false, detail: null, fields: {} };
}

/** Used by tests and callers that need the catalog name without rendering it. */
export function businessErrorName(errCode: number): string | null {
  return BUSINESS_ERROR_CATALOG.errors[String(errCode)]?.name ?? null;
}

/**
 * Resolves a stable catalog name to its numeric err_code so callers can
 * branch on special business outcomes without embedding numeric literals
 * in components (frontend PRD §4).
 */
export function businessErrCodeByName(name: string): number | null {
  for (const [code, entry] of Object.entries(BUSINESS_ERROR_CATALOG.errors)) {
    if (entry.name === name) return Number(code);
  }
  return null;
}
