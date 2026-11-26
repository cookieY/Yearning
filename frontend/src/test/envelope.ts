import { HttpResponse } from "msw";
import type { DefaultBodyType } from "msw";
import {
  businessErrorPayload,
  problem,
  successEnvelope,
  type BusinessErrorBody,
  type SuccessEnvelopePayload,
} from "@/shared/mock/kit";

export const TEST_REQUEST_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

export function testEnvelope<T = DefaultBodyType>(
  data: T,
  requestId: string = TEST_REQUEST_ID,
): SuccessEnvelopePayload<T> {
  return successEnvelope(data, requestId);
}

export function testEnvelopeJson(
  data: DefaultBodyType,
  requestId: string = TEST_REQUEST_ID,
): HttpResponse<DefaultBodyType> {
  return HttpResponse.json(testEnvelope(data, requestId));
}

export function testBusinessError(
  errCode: number,
  message: string,
  options?: {
    requestId?: string;
    retryable?: boolean;
    meta?: Record<string, unknown>;
  },
): BusinessErrorBody {
  const payload = businessErrorPayload(
    errCode,
    message,
    options?.requestId ?? TEST_REQUEST_ID,
    options?.meta,
  );
  return options?.retryable === undefined
    ? payload
    : { ...payload, retryable: options.retryable };
}

export function testBusinessErrorJson(
  errCode: number,
  message: string,
  options?: {
    requestId?: string;
    retryable?: boolean;
    meta?: Record<string, unknown>;
  },
): HttpResponse<DefaultBodyType> {
  return HttpResponse.json(testBusinessError(errCode, message, options));
}

export function testProblemJson(
  status: number,
  title: string,
  detail: string,
  requestId: string = TEST_REQUEST_ID,
): HttpResponse<DefaultBodyType> {
  return problem(status, title, detail, requestId);
}
