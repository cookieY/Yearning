/**
 * FE-F12 contract harness: one generated client, one runtime schema, two
 * backends.
 *
 * Every case calls a generated client function (src/api/generated/client)
 * through the production mutator, so CSRF, envelope unwrapping and error
 * typing are exercised exactly as the pages do. The raw HTTP envelope is
 * captured at the fetch boundary and validated against the generated zod
 * `<OperationId>Response` schema — the very artifact whose SHA-256 is pinned
 * in contract-manifest.json (zod_runtime_schemas). The same test file runs
 * against the MSW mock layer (default) and against the real serve stack
 * (F12_BASE_URL set), producing one report per mode; scripts/contract-diff.mjs
 * joins them into the Mock/Real divergence report.
 *
 * Node has no cookie jar: the fetch wrapper keeps one and mirrors the
 * JS-readable CSRF cookie into jsdom's document.cookie so the mutator's
 * double-submit path runs unchanged. No product code is touched.
 */
import { createHash } from "node:crypto";
import { lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { afterAll } from "vitest";
import type { ZodTypeAny } from "zod";
import * as zodSchemas from "@/api/generated/zod/yearningV4HTTPAPI";
import { BusinessError, TransportError } from "@/shared/api/mutator";

export type Mode = "mock" | "real";
export const mode: Mode = process.env.F12_BASE_URL ? "real" : "mock";

export interface CaseRow {
  operation_id: string;
  case_id: string;
  mode: Mode;
  http_status: number | null;
  err_code: number | null;
  outcome: "success" | "business_error" | "transport_error" | "no_response";
  schema_valid: boolean | null;
  issues: string[];
  problem_title?: string;
  /** Known, accepted Mock/Real divergence (e.g. the mock does not model authz). */
  note?: string;
}

const rows: CaseRow[] = [];
interface Capture { status: number; body: unknown }
let lastCapture: Capture | null = null;
// Read through an accessor: the wrapper assigns asynchronously, so a direct
// read after `lastCapture = null` is narrowed to `never` by control flow.
const takeCapture = (): Capture | null => lastCapture;
const jar = new Map<string, string>();
// Exposed for the role session cache (tests/contract/roles.ts).
(globalThis as { __f12Jar?: Map<string, string> }).__f12Jar = jar;
let installed = false;

/**
 * Wrap the current global fetch (after MSW has patched it in mock mode, so
 * the wrapper sits in front of the interceptor): inject the cookie jar,
 * absorb Set-Cookie, capture the raw envelope.
 */
export function installTransport(): void {
  if (installed) return;
  installed = true;
  const inner = globalThis.fetch;
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    if (jar.size > 0 && !headers.has("cookie")) {
      headers.set("cookie", [...jar].map(([k, v]) => `${k}=${v}`).join("; "));
    }
    const response = await inner(input, { ...init, headers });
    absorbSetCookie(response.headers);
    const clone = response.clone();
    try {
      lastCapture = { status: response.status, body: await clone.json() };
    } catch {
      lastCapture = { status: response.status, body: null };
    }
    return response;
  }) as typeof fetch;
}

function syncDocumentCookie(name: string, value: string | null): void {
  if (typeof document === "undefined") return;
  document.cookie = value === null
    ? `${name}=; expires=Thu, 01 Jan 1970 00:00:00 GMT`
    : `${name}=${value}`;
}

function absorbSetCookie(headers: Headers): void {
  const list = typeof headers.getSetCookie === "function" ? headers.getSetCookie() : [];
  for (const raw of list) {
    const parts = raw.split(";");
    const pair = parts[0] ?? "";
    const attrs = parts.slice(1);
    const eq = pair.indexOf("=");
    if (eq < 0) continue;
    const name = pair.slice(0, eq).trim();
    const value = pair.slice(eq + 1).trim();
    const expired = attrs.some((a) => /^\s*max-age=0\s*$/i.test(a) || /expires=.*1970/i.test(a));
    if (expired || value === "") {
      jar.delete(name);
      syncDocumentCookie(name, null);
    } else {
      jar.set(name, value);
      syncDocumentCookie(name, value);
    }
  }
}

/** Drop the session (both modes) — the next login starts clean. */
export function clearSession(): void {
  jar.clear();
  syncDocumentCookie("yearning_session", null);
  syncDocumentCookie("yearning_csrf", null);
}

const capitalize = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1);

export function responseSchemaFor(operationId: string): ZodTypeAny | null {
  const key = `${capitalize(operationId)}Response`;
  const schema = (zodSchemas as Record<string, unknown>)[key];
  return schema && typeof (schema as ZodTypeAny).safeParse === "function" ? (schema as ZodTypeAny) : null;
}

function issuesOf(error: { issues: Array<{ path: PropertyKey[]; message: string }> }): string[] {
  return error.issues.slice(0, 6).map((i) => `${i.path.map(String).join(".") || "<root>"}: ${i.message}`);
}

/** err_code literal(s) declared by a schema node, recursing into nested unions. */
function declaredCodes(node: unknown, depth = 0): number[] {
  if (!node || typeof node !== "object" || depth > 4) return [];
  const def = (node as { def?: Record<string, unknown> }).def;
  if (!def) return [];
  if (def.type === "union" && Array.isArray(def.options)) {
    return (def.options as unknown[]).flatMap((o) => declaredCodes(o, depth + 1));
  }
  if (def.type === "literal") {
    const values = def.values ?? (def.value !== undefined ? [def.value] : []);
    return (values as unknown[]).filter((v): v is number => typeof v === "number");
  }
  return [];
}

/** err_code literal(s) declared by a union member's `err_code` field, if any. */
function memberErrCodes(option: ZodTypeAny): number[] {
  const shape = (option as unknown as { def?: { shape?: Record<string, unknown> } }).def?.shape;
  const field = shape?.err_code;
  if (!field) return [];
  const direct = declaredCodes(field);
  if (direct.length > 0) return direct;
  // object schemas built with .and() keep shapes on each side; scan shallowly.
  return declaredCodes(field);
}

/**
 * Explain a failed parse. The generated response schemas are unions
 * (success envelope | declared business errors); zod reports a bare
 * "Invalid input" at the root for unions. The envelope's err_code selects
 * the member deterministically; fall back to the closest member otherwise.
 */
export function explainParse(schema: ZodTypeAny, body: unknown): string[] {
  const parsed = schema.safeParse(body);
  if (parsed.success) return [];
  const def = schema as unknown as { def?: { options?: ZodTypeAny[] }; _def?: { options?: ZodTypeAny[] } };
  const options = def.def?.options ?? def._def?.options;
  if (!options || options.length === 0) return issuesOf(parsed.error);
  const errCode = (body as { err_code?: unknown } | null)?.err_code;
  if (typeof errCode === "number") {
    const index = options.findIndex((o) => memberErrCodes(o).includes(errCode));
    const option = index >= 0 ? options[index] : undefined;
    if (option) {
      const attempt = option.safeParse(body);
      if (attempt.success) return [];
      return issuesOf(attempt.error).map((i) => `union[${index}] ${i}`);
    }
    return [`no union member declares err_code ${errCode} (unlisted business error)`];
  }
  let best: { index: number; count: number; issues: string[] } | null = null;
  options.forEach((option, index) => {
    const attempt = option.safeParse(body);
    if (attempt.success) return;
    const count = attempt.error.issues.length;
    if (best === null || count < best.count) best = { index, count, issues: issuesOf(attempt.error) };
  });
  if (best === null) return issuesOf(parsed.error);
  const chosen: { index: number; count: number; issues: string[] } = best;
  return chosen.issues.map((i) => `union[${chosen.index}] ${i}`);
}

function problemShapeIssues(body: unknown): string[] {
  const issues: string[] = [];
  if (body === null || typeof body !== "object") return ["problem body is not an object"];
  const p = body as Record<string, unknown>;
  for (const key of ["type", "title", "status", "detail", "request_id"]) {
    if (!(key in p)) issues.push(`problem.${key} missing`);
  }
  if (typeof p.status !== "number") issues.push("problem.status not a number");
  return issues;
}

export interface CaseResult<T> {
  value?: T;
  error?: unknown;
  status: number | null;
  body: unknown;
  errCode: number | null;
}

/**
 * Run one generated-client call, capture its raw envelope and validate it
 * against the operation's zod response schema. Never throws for business or
 * transport errors — the case decides what it expects.
 */
export async function record<T>(
  operationId: string,
  caseId: string,
  invoke: () => Promise<T>,
  note?: string,
): Promise<CaseResult<T>> {
  lastCapture = null;
  const row: CaseRow = {
    operation_id: operationId, case_id: caseId, mode,
    http_status: null, err_code: null, outcome: "no_response",
    schema_valid: null, issues: [],
    ...(note ? { note } : {}),
  };
  const result: CaseResult<T> = { status: null, body: null, errCode: null };
  try {
    result.value = await invoke();
    row.outcome = "success";
  } catch (error) {
    result.error = error;
    if (error instanceof BusinessError) {
      row.outcome = "business_error";
      row.err_code = error.err_code;
      result.errCode = error.err_code;
    } else if (error instanceof TransportError) {
      row.outcome = "transport_error";
      row.problem_title = error.problem.title;
    } else {
      row.outcome = "transport_error";
      row.issues.push(`unexpected error: ${String(error)}`);
    }
  }
  const captured = takeCapture();
  if (captured) {
    row.http_status = captured.status;
    result.status = captured.status;
    result.body = captured.body;
    if (captured.status === 200) {
      const schema = responseSchemaFor(operationId);
      if (schema === null) {
        row.schema_valid = null;
        row.issues.push("no generated response schema");
      } else {
        const issues = explainParse(schema, captured.body);
        row.schema_valid = issues.length === 0;
        row.issues.push(...issues);
      }
      const envelope = captured.body as { err_code?: unknown } | null;
      if (envelope && typeof envelope.err_code === "number") {
        row.err_code = envelope.err_code;
        result.errCode = envelope.err_code;
      }
    } else if (captured.status >= 300) {
      const issues = problemShapeIssues(captured.body);
      row.schema_valid = issues.length === 0;
      row.issues.push(...issues);
    }
  }
  rows.push(row);
  return result;
}

/** Sorted-path directory digest, byte for byte the manifest's sha256_path. */
export function sha256Dir(dir: string): string {
  const files: string[] = [];
  const walk = (d: string): void => {
    for (const entry of readdirSync(d)) {
      const p = join(d, entry);
      if (lstatSync(p).isDirectory()) walk(p);
      else files.push(p);
    }
  };
  walk(dir);
  files.sort();
  const digest = createHash("sha256");
  for (const file of files) {
    digest.update(relative(dir, file).split("\\").join("/"));
    digest.update(Buffer.alloc(1, 0));
    digest.update(readFileSync(file));
    digest.update(Buffer.alloc(1, 0));
  }
  return digest.digest("hex");
}

export function reportPath(): string {
  return process.env.F12_REPORT_PATH ?? join(process.cwd(), "tests", "contract", ".reports", `${mode}.json`);
}

afterAll(() => {
  const zodDir = join(process.cwd(), "src", "api", "generated", "zod");
  const path = reportPath();
  // Each test file lands in its own process; merge on write so a multi-file
  // run accumulates one report per mode instead of the last file winning.
  // Files run sequentially (fileParallelism: false), so the read is stable.
  let merged: CaseRow[] = rows;
  try {
    const previous = JSON.parse(readFileSync(path, "utf8")) as { mode?: string; rows?: CaseRow[] };
    if (previous.mode === mode && Array.isArray(previous.rows)) {
      const byKey = new Map(previous.rows.map((r) => [`${r.operation_id}::${r.case_id}`, r]));
      for (const row of rows) byKey.set(`${row.operation_id}::${row.case_id}`, row);
      merged = [...byKey.values()];
    }
  } catch {
    // first writer
  }
  const report = {
    contract_version: 1,
    mode,
    base_url: process.env.F12_BASE_URL ?? "msw://generated-handlers+scenario:ready",
    runtime_schema_sha256: sha256Dir(zodDir),
    generated_at: new Date().toISOString(),
    case_count: merged.length,
    operation_count: new Set(merged.map((r) => r.operation_id)).size,
    rows: merged,
  };
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, JSON.stringify(report, null, 2) + "\n");
});
