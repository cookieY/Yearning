import {
  BUSINESS_ERROR_CATALOG,
  SUCCESS_ERR_CODE,
} from "@/api/generated/projections/business-error-catalog";

/**
 * The single sanctioned response unwrapper (frontend implementation PRD §4):
 * HTTP 200 envelopes are parsed, err_code is checked once here, and pages,
 * hooks and components never compare numeric literals themselves.
 * Transport and security failures (non-2xx, problem+json, network) take the
 * separate TransportError path and are never disguised as business errors.
 * Signature follows the orval 8 fetch-client mutator convention: the generated
 * call site passes the fully built URL plus a RequestInit (body pre-serialised).
 */

export interface ProblemDetails {
  type: string;
  title: string;
  status: number;
  detail: string;
  instance?: string;
  request_id: string;
}

export class TransportError extends Error {
  readonly problem: ProblemDetails | { title: string; status: number; detail: string };

  constructor(problem: TransportError["problem"]) {
    super(problem.detail || problem.title);
    this.name = "TransportError";
    this.problem = problem;
  }
}

export interface BusinessErrorPayload {
  err_code: number;
  message: string;
  request_id: string;
  retryable: boolean;
  meta?: Record<string, unknown>;
}

export class BusinessError extends Error {
  readonly err_code: number;
  readonly name: string;
  readonly requestId: string;
  readonly retryable: boolean;
  readonly meta?: Record<string, unknown>;

  constructor(payload: BusinessErrorPayload) {
    super(payload.message);
    this.name = "BusinessError";
    this.err_code = payload.err_code;
    this.requestId = payload.request_id;
    this.retryable = payload.retryable;
    if (payload.meta !== undefined) {
      this.meta = payload.meta;
    }
  }

  /** Stable contract identifier such as VALIDATION_FAILED, never shown raw in the UI. */
  get catalogEntry() {
    return BUSINESS_ERROR_CATALOG.errors[String(this.err_code)] ?? null;
  }
}

const API_BASE_URL: string = import.meta.env.VITE_API_BASE_URL ?? "";

/**
 * Session cookies are issued by the backend as `yearning_session` (HttpOnly)
 * plus a JS-readable `yearning_csrf` double-submit cookie (ADR-0004). Mutating
 * requests must echo the CSRF value in the X-CSRF-Token header; the token
 * never enters URLs or web storage (acceptance gate).
 */
const CSRF_COOKIE_NAME = "yearning_csrf";
const CSRF_HEADER_NAME = "X-CSRF-Token";
const MUTATING_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export function readCsrfToken(): string | null {
  if (typeof document === "undefined") return null;
  const prefix = `${CSRF_COOKIE_NAME}=`;
  for (const entry of document.cookie.split(";")) {
    const trimmed = entry.trim();
    if (trimmed.startsWith(prefix)) {
      const raw = trimmed.slice(prefix.length);
      try {
        return decodeURIComponent(raw);
      } catch {
        // A malformed percent-sequence must not break every mutating request.
        return raw;
      }
    }
  }
  return null;
}

/**
 * Fired when the server answers 401 on a request. The session layer owns the
 * reaction (cache reset, redirect); the mutator only reports the fact once.
 */
export const SESSION_EXPIRED_EVENT = "yearning:session-expired";

function announceSessionExpired(): void {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
}

function buildUrl(url: string): string {
  // orval emits root-absolute paths; a deployment behind a path-prefixed
  // reverse proxy sets VITE_API_BASE_URL to that full prefix.
  if (API_BASE_URL === "") return url;
  return `${API_BASE_URL.replace(/\/$/, "")}${url}`;
}

export function resolveActivePlatformLocale(): "zh-CN" | "en-US" {
  if (typeof document !== "undefined") {
    const htmlLang = document.documentElement.lang.trim().toLowerCase();
    if (htmlLang.startsWith("en")) {
      return "en-US";
    }
  }
  return "zh-CN";
}

export const customInstance = async <T>(url: string, options?: RequestInit): Promise<T> => {
  const headers = new Headers(options?.headers);
  const method = (options?.method ?? "GET").toUpperCase();
  if (!headers.has("Accept-Language")) {
    headers.set("Accept-Language", resolveActivePlatformLocale());
  }
  if (MUTATING_METHODS.has(method)) {
    const csrfToken = readCsrfToken();
    if (csrfToken !== null && !headers.has(CSRF_HEADER_NAME)) {
      headers.set(CSRF_HEADER_NAME, csrfToken);
    }
  }

  let response: Response;
  try {
    response = await fetch(buildUrl(url), {
      ...options,
      method,
      headers,
      credentials: "same-origin",
    });
  } catch (cause) {
    throw new TransportError({
      title: "network_error",
      status: 0,
      detail: cause instanceof Error ? cause.message : "network unreachable",
    });
  }

  if (!response.ok) {
    // Transport and security failures stay on the Problem Details path.
    const problem = (await response.json().catch(() => null)) as ProblemDetails | null;
    if (response.status === 401) {
      // Session expiry is a global fact, not a per-page error: every consumer
      // must see the anonymous state, so announce it before throwing.
      announceSessionExpired();
    }
    throw new TransportError(
      problem && typeof problem.title === "string" && typeof problem.status === "number"
        ? problem
        : { title: "http_error", status: response.status, detail: response.statusText },
    );
  }

  let raw: unknown;
  try {
    raw = await response.json();
  } catch {
    // A 200 with a non-JSON body (proxy interstitial, redirect landing page)
    // is a transport-level contract violation, never a business result.
    throw new TransportError({
      title: "malformed_envelope",
      status: response.status,
      detail: "response body is not JSON",
    });
  }
  const envelope = raw as {
    err_code?: number;
    message?: string;
    data?: unknown;
    request_id?: string;
    retryable?: boolean;
    meta?: Record<string, unknown>;
  };

  if (envelope.err_code === SUCCESS_ERR_CODE) {
    return envelope.data as T;
  }

  if (
    typeof envelope.err_code === "number" &&
    typeof envelope.request_id === "string" &&
    typeof envelope.retryable === "boolean"
  ) {
    throw new BusinessError({
      err_code: envelope.err_code,
      message: envelope.message ?? "",
      request_id: envelope.request_id,
      retryable: envelope.retryable,
      ...(envelope.meta === undefined ? {} : { meta: envelope.meta }),
    });
  }

  // A 200 body that is neither a success envelope nor a declared business
  // error is a contract violation: surface a safe generic error, never guess.
  throw new TransportError({
    title: "malformed_envelope",
    status: response.status,
    detail: "response envelope does not match the OpenAPI contract",
  });
};

export default customInstance;
