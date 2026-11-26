import { HttpResponse } from "msw";
import type { DefaultBodyType } from "msw";

export interface SuccessEnvelopePayload<T = DefaultBodyType> {
  err_code: 0;
  message: "ok";
  data: T;
  request_id: string;
}

export interface BusinessErrorBody {
  err_code: number;
  message: string;
  data: null;
  request_id: string;
  retryable: boolean;
  meta?: Record<string, unknown>;
}

export interface ProblemBody {
  type: "about:blank";
  title: string;
  status: number;
  detail: string;
  request_id: string;
}

export interface CursorPagePayload<T> {
  items: T[];
  page: {
    next_cursor: string | null;
    has_more: boolean;
  };
}

export function nowIso(): string {
  return new Date().toISOString();
}

export function nowIsoSeconds(): string {
  return nowIso().replace(/\.\d{3}Z$/, "Z");
}

export function fixtureUuid(): string {
  const webCrypto = globalThis.crypto as
    | { randomUUID?: () => string; getRandomValues?: (arr: Uint8Array) => Uint8Array }
    | undefined;
  if (typeof webCrypto?.randomUUID === "function") {
    return webCrypto.randomUUID();
  }
  if (typeof webCrypto?.getRandomValues !== "function") {
    throw new Error("Web Crypto API is unavailable for fixture UUID generation");
  }
  const bytes = new Uint8Array(16);
  webCrypto.getRandomValues(bytes);
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export function formatSequenceUuid(prefix: string, seq: number): string {
  return `${prefix}-1111-4111-8111-${String(seq).padStart(12, "0")}`;
}

export function parseIfMatch(request: Request): string | null {
  const raw = request.headers.get("If-Match");
  return raw === null ? null : raw.replace(/^"|"$/g, "");
}

export function successEnvelope<T = DefaultBodyType>(
  data: T,
  requestId: string,
): SuccessEnvelopePayload<T> {
  return {
    err_code: 0,
    message: "ok",
    data,
    request_id: requestId,
  };
}

export function businessErrorPayload(
  errCode: number,
  message: string,
  requestId: string,
  meta?: Record<string, unknown>,
): BusinessErrorBody {
  return {
    err_code: errCode,
    message,
    data: null,
    request_id: requestId,
    retryable: false,
    ...(meta === undefined ? {} : { meta }),
  };
}

export function businessError(
  errCode: number,
  message: string,
  requestId: string,
  meta?: Record<string, unknown>,
): HttpResponse<DefaultBodyType> {
  return HttpResponse.json(businessErrorPayload(errCode, message, requestId, meta));
}

export function problem(
  status: number,
  title: string,
  detail: string,
  requestId: string,
): HttpResponse<DefaultBodyType> {
  const body: ProblemBody = {
    type: "about:blank",
    title,
    status,
    detail,
    request_id: requestId,
  };
  return HttpResponse.json(body, {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}

export function pageOf<T>(
  items: T[],
  limit: number | null,
  after: string | null,
  defaultLimit: number | null = null,
): CursorPagePayload<T> {
  const start =
    after === null || after === ""
      ? 0
      : (() => {
          const index = items.findIndex((item) => (item as { id?: string }).id === after);
          return index === -1 ? items.length : index + 1;
        })();
  const window = items.slice(start);
  const effectiveLimit = limit ?? defaultLimit;
  const slice = effectiveLimit === null ? window : window.slice(0, effectiveLimit);
  const last = slice.at(-1) as { id?: string } | undefined;
  const hasMore = start + slice.length < items.length;
  return {
    items: slice,
    page: {
      next_cursor: hasMore && last?.id !== undefined ? last.id : null,
      has_more: hasMore,
    },
  };
}

export function createMockResponder(requestId: string, defaultPageSize: number | null = null) {
  return {
    successEnvelope: <T = DefaultBodyType>(data: T) => successEnvelope(data, requestId),
    businessErrorPayload: (errCode: number, message: string, meta?: Record<string, unknown>) =>
      businessErrorPayload(errCode, message, requestId, meta),
    businessErrorResponse: (errCode: number, message: string, meta?: Record<string, unknown>) =>
      businessError(errCode, message, requestId, meta),
    problem: (status: number, title: string, detail: string) =>
      problem(status, title, detail, requestId),
    unauthenticated: () => problem(401, "session_expired", "no active session", requestId),
    pageOf: <T>(items: T[], limit: number | null, after: string | null) =>
      pageOf(items, limit, after, defaultPageSize),
  };
}
