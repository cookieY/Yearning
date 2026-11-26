import { http, type HttpHandler } from "msw";
import type { z } from "zod";
import * as zodSchemas from "@/api/generated/zod/yearningV4HTTPAPI";
import { matchOpenApiRoute } from "@/shared/mock/api-route";

type LowerMethod = "get" | "post" | "put" | "patch" | "delete";

const METHOD_FACTORIES: Record<LowerMethod, typeof http.get> = {
  get: http.get,
  post: http.post,
  put: http.put,
  patch: http.patch,
  delete: http.delete,
};

function formatZodIssues(issues: ReadonlyArray<{ path: PropertyKey[]; message: string }>): string {
  return issues
    .slice(0, 6)
    .map((issue) => `${issue.path.map(String).join(".") || "<root>"}: ${issue.message}`)
    .join("; ");
}

function validateProblemDetails(body: unknown, label: string): void {
  if (typeof body !== "object" || body === null) {
    throw new Error(`[zod-guard] ${label} returned non-object Problem Details payload`);
  }
  const obj = body as Record<string, unknown>;
  if (
    typeof obj.type !== "string" ||
    typeof obj.title !== "string" ||
    typeof obj.status !== "number" ||
    typeof obj.detail !== "string"
  ) {
    throw new Error(`[zod-guard] ${label} failed RFC 7807 Problem Details shape check`);
  }
}

function unionMemberErrCodes(option: z.ZodType): number[] {
  const shape = (option as unknown as { def?: { shape?: Record<string, unknown> } }).def?.shape;
  const field = shape?.err_code as { def?: { type?: string; values?: unknown[]; value?: unknown; options?: unknown[] } } | undefined;
  if (!field?.def) return [];
  if (field.def.type === "literal") {
    const vals = field.def.values ?? (field.def.value !== undefined ? [field.def.value] : []);
    return vals.filter((v): v is number => typeof v === "number");
  }
  if (field.def.type === "union" && Array.isArray(field.def.options)) {
    return field.def.options.flatMap((o) => {
      const def = (o as { def?: { type?: string; values?: unknown[]; value?: unknown } }).def;
      if (def?.type !== "literal") return [];
      const vals = def.values ?? (def.value !== undefined ? [def.value] : []);
      return vals.filter((v): v is number => typeof v === "number");
    });
  }
  return [];
}

function formatSchemaFailure(schema: z.ZodType, body: unknown): string {
  const options = (schema as unknown as { def?: { options?: z.ZodType[] } }).def?.options;
  const errCode = (body as { err_code?: unknown } | null)?.err_code;
  if (options && typeof errCode === "number") {
    const match = options.find((opt) => unionMemberErrCodes(opt).includes(errCode));
    if (match) {
      const attempt = match.safeParse(body);
      if (!attempt.success) return formatZodIssues(attempt.error.issues);
    }
  }
  const parsed = schema.safeParse(body);
  return parsed.success ? "" : formatZodIssues(parsed.error.issues);
}

async function assertResponseMatchesContract(
  method: string,
  requestUrl: string,
  response: Response,
): Promise<void> {
  if (response.status >= 300 && response.status < 400) {
    return;
  }
  if (response.status === 204) {
    return;
  }

  const contentType = (response.headers.get("content-type") ?? "").toLowerCase();
  if (contentType.includes("text/event-stream") || !contentType.includes("json")) {
    return;
  }

  const pathname = new URL(requestUrl, "http://localhost").pathname;
  if (response.status >= 400) {
    if (!contentType.includes("problem+json")) {
      throw new Error(
        `[zod-guard] ${method} ${pathname} returned HTTP ${String(response.status)} with "${contentType}" instead of "application/problem+json"`,
      );
    }
    const body: unknown = await response.clone().json();
    validateProblemDetails(body, `${method} ${pathname}`);
    return;
  }

  if (response.status !== 200) {
    return;
  }

  const route = matchOpenApiRoute(method, requestUrl);
  if (route === null) {
    throw new Error(
      `[zod-guard] ${method} ${pathname} has no matching route in OPENAPI_ROUTES`,
    );
  }

  const schema = (zodSchemas as Record<string, unknown>)[route.zodResponse] as
    | z.ZodType
    | undefined;
  if (schema === undefined || typeof schema.safeParse !== "function") {
    throw new Error(
      `[zod-guard] missing generated Zod schema "${route.zodResponse}" for ${route.operationId}`,
    );
  }

  const body: unknown = await response.clone().json();
  const parsed = schema.safeParse(body);
  if (!parsed.success) {
    const detail = formatSchemaFailure(schema, body) || formatZodIssues(parsed.error.issues);
    throw new Error(
      `[zod-guard] ${method} ${pathname} (${route.operationId}) failed ${route.zodResponse}: ${detail}`,
    );
  }
}

/**
 * Wraps every MSW `HttpHandler` so HTTP 200 JSON responses are validated at
 * runtime against the Orval-generated Zod response schema and HTTP >=400
 * problem responses follow RFC 7807. Throws immediately on schema drift.
 */
export function withZodValidation(handlers: readonly HttpHandler[]): HttpHandler[] {
  return handlers.map((handler) => {
    const methodLower = String(handler.info.method).toLowerCase() as LowerMethod;
    const factory = (METHOD_FACTORIES as Partial<Record<LowerMethod, typeof http.get>>)[methodLower];
    if (factory === undefined) {
      throw new Error(`[zod-guard] unsupported HTTP method "${String(handler.info.method)}"`);
    }
    const originalResolver = (
      handler as unknown as {
        resolver: (info: Parameters<Parameters<typeof http.get>[1]>[0]) => Promise<Response | undefined> | Response | undefined;
      }
    ).resolver;

    return factory(handler.info.path, async (info) => {
      const result = await originalResolver(info);
      if (result instanceof Response) {
        await assertResponseMatchesContract(
          methodLower.toUpperCase(),
          info.request.url,
          result,
        );
      }
      return result;
    });
  });
}
