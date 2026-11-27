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

export function buildApiUrl(url: string): string {
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

export function buildRequestHeaders(method: string, extraHeaders?: HeadersInit): Headers {
  const headers = new Headers(extraHeaders);
  const normalizedMethod = method.toUpperCase();
  if (!headers.has("Accept-Language")) {
    headers.set("Accept-Language", resolveActivePlatformLocale());
  }
  if (import.meta.env.DEV) {
    const viewAs = (globalThis as { __YEARNING_DEV_VIEW_AS__?: unknown }).__YEARNING_DEV_VIEW_AS__;
    if (
      (viewAs === "approver" || viewAs === "executor" || viewAs === "user") &&
      !headers.has("X-Yearning-Dev-View-As")
    ) {
      headers.set("X-Yearning-Dev-View-As", viewAs);
    }
  }
  if (MUTATING_METHODS.has(normalizedMethod)) {
    const csrfToken = readCsrfToken();
    if (csrfToken !== null && !headers.has(CSRF_HEADER_NAME)) {
      headers.set(CSRF_HEADER_NAME, csrfToken);
    }
  }
  return headers;
}

export async function throwFromErrorResponse(response: Response): Promise<never> {
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

export async function executeFetch(url: string, options?: RequestInit): Promise<Response> {
  const method = (options?.method ?? "GET").toUpperCase();
  const headers = buildRequestHeaders(method, options?.headers);
  let response: Response;
  try {
    response = await fetch(buildApiUrl(url), {
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
    return await throwFromErrorResponse(response);
  }
  return response;
}

export async function unwrapEnvelopeResponse<T>(response: Response): Promise<T> {
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
}

export function parseStreamBusinessError(
  parsed: Record<string, unknown>,
  fallbackRequestId = "sse",
): BusinessError {
  return new BusinessError({
    err_code: typeof parsed.err_code === "number" ? parsed.err_code : 5000,
    message: typeof parsed.message === "string" ? parsed.message : "",
    request_id: typeof parsed.request_id === "string" ? parsed.request_id : fallbackRequestId,
    retryable: Boolean(parsed.retryable),
    ...(typeof parsed.meta === "object" && parsed.meta !== null
      ? { meta: parsed.meta as Record<string, unknown> }
      : {}),
  });
}

export const customInstance = async <T>(url: string, options?: RequestInit): Promise<T> => {
  const response = await executeFetch(url, options);
  return unwrapEnvelopeResponse<T>(response);
};

type SuccessEnvelope<R> = Extract<R, { err_code: 0 }>;

type NormalizePage<D> = D extends { items: readonly unknown[]; page: infer P }
  ? Omit<D, "items" | "page"> & {
      items: NonNullable<D["items"]>[number][];
      page: P;
    }
  : D;

/**
 * Extracts the `data` payload type from a generated OpenAPI success/business-error
 * union (`customInstance` unwraps `err_code === 0` at runtime and throws
 * `BusinessError` on non-zero envelopes) and normalizes `Page & { items?: T[] }`
 * intersections so array elements keep their contract element type `T`.
 */
export type UnwrapEnvelope<R> = NormalizePage<
  [SuccessEnvelope<R>] extends [never]
    ? R extends { data: infer D }
      ? D
      : R
    : SuccessEnvelope<R> extends { data: infer D }
      ? D
      : SuccessEnvelope<R>
>;

/**
 * Typed bridge aligning generated client promises (typed as full OpenAPI envelopes)
 * with `customInstance`'s runtime unwrapping of `envelope.data`.
 */
export function unwrapApi<R>(promise: Promise<R>): Promise<UnwrapEnvelope<R>> {
  return promise as unknown as Promise<UnwrapEnvelope<R>>;
}

/** Builds optimistic-concurrency `RequestInit` headers carrying a quoted `If-Match` row version. */
export function ifMatchHeader(
  version: number | undefined,
  extraHeaders?: Record<string, string>,
): RequestInit {
  return {
    headers: {
      ...extraHeaders,
      "If-Match": `"${String(version ?? 1)}"`,
    },
  };
}

function generateIdempotencyToken(): string {
  const webCrypto = globalThis.crypto as
    | { randomUUID?: () => string; getRandomValues?: (arr: Uint8Array) => Uint8Array }
    | undefined;
  if (typeof webCrypto?.randomUUID === "function") {
    return webCrypto.randomUUID();
  }
  if (typeof webCrypto?.getRandomValues === "function") {
    const bytes = new Uint8Array(16);
    webCrypto.getRandomValues(bytes);
    bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
    bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
    const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  }
  return `${String(Date.now())}-${Math.random().toString(16).slice(2)}`;
}

/** Mints a fresh `Idempotency-Key` token with an optional domain prefix. */
export function newIdempotencyKey(prefix?: string): string {
  const token = generateIdempotencyToken();
  return prefix === undefined || prefix === "" ? token : `${prefix}-${token}`;
}

/** Wraps an already-minted per-operation `Idempotency-Key` into a header map. */
export function idempotencyKeyHeader(
  idempotencyKey: string,
  extraHeaders?: Record<string, string>,
): Record<string, string> {
  return {
    ...extraHeaders,
    "Idempotency-Key": idempotencyKey,
  };
}

/** Builds a header map carrying a newly minted `Idempotency-Key`. */
export function idempotencyHeaders(
  prefix?: string,
  extraHeaders?: Record<string, string>,
): Record<string, string> {
  return idempotencyKeyHeader(newIdempotencyKey(prefix), extraHeaders);
}

/** Combines `If-Match` optimistic locking with a caller-supplied operation `Idempotency-Key`. */
export function ifMatchIdempotencyHeader(
  version: number | undefined,
  idempotencyKey: string,
  extraHeaders?: Record<string, string>,
): RequestInit {
  return ifMatchHeader(version, idempotencyKeyHeader(idempotencyKey, extraHeaders));
}

/** Extracts and trims the operation idempotency header from a request. */
export function readIdempotencyKeyHeader(request: Request): string {
  return request.headers.get("Idempotency-Key")?.trim() ?? "";
}

export default customInstance;
