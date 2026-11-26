import { describe, expect, it } from "vitest";
import type { z } from "zod";
import manifest from "@/api/generated/contract-manifest.json";
import * as zodSchemas from "@/api/generated/zod/yearningV4HTTPAPI";

interface RecordedIndex {
  contract_version: number;
  openapi_sha: string;
  zod_runtime_sha256: string;
  count: number;
  entries: Array<{
    operation_id: string;
    case_id: string;
    file: string;
    http_status: number;
    err_code: number | null;
  }>;
}

interface RecordedFixture {
  contract_version: number;
  openapi_sha: string;
  zod_runtime_sha256: string;
  operation_id: string;
  case_id: string;
  http_status: number;
  err_code: number | null;
  response: unknown;
}

const SECRET_VALUE_RE = /(f12seed-pw|f12-stub|Bearer\s+[A-Za-z0-9._-]{8,}|sk-[A-Za-z0-9]{12,})/;
const capitalize = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1);

function isZodSchema(value: unknown): value is z.ZodType {
  return value !== null && typeof value === "object" && typeof (value as { safeParse?: unknown }).safeParse === "function";
}

const indexModules = import.meta.glob<RecordedIndex>("./recorded/index.json", { eager: true, import: "default" });
const fixtureModules = import.meta.glob<RecordedFixture>("./recorded/*/*.json", { eager: true, import: "default" });

describe("recorded real-backend fixtures replay verification (Stage 3.5 / L2)", () => {
  it("matches pinned openapi_sha, scrubs all secrets, and validates against generated zod schemas", () => {
    const index = indexModules["./recorded/index.json"];
    expect(index, "missing src/shared/mock/recorded/index.json — run F12_RECORD_FIXTURES=1").toBeDefined();
    if (!index) return;

    expect(index.openapi_sha).toBe(manifest.sources.openapi.sha256);
    expect(index.zod_runtime_sha256).toBe(manifest.outputs.zod_runtime_schemas.sha256);
    expect(index.entries.length).toBeGreaterThan(20);

    for (const entry of index.entries) {
      const fixture = fixtureModules[`./recorded/${entry.file}`];
      expect(fixture, `missing fixture ${entry.file}`).toBeDefined();
      if (!fixture) continue;

      expect(SECRET_VALUE_RE.test(JSON.stringify(fixture)), `secret leakage detected in ${entry.file}`).toBe(false);
      expect(fixture.openapi_sha).toBe(manifest.sources.openapi.sha256);
      expect(fixture.operation_id).toBe(entry.operation_id);

      if (fixture.http_status === 200) {
        const schemaKey = `${capitalize(fixture.operation_id)}Response`;
        const schema = (zodSchemas as Record<string, unknown>)[schemaKey];
        if (isZodSchema(schema)) {
          const parsed = schema.safeParse(fixture.response);
          const issues = parsed.success
            ? []
            : parsed.error.issues.slice(0, 6).map((issue) => `${issue.path.map(String).join(".") || "<root>"}: ${issue.message}`);
          expect(issues, `schema drift in recorded fixture ${entry.file}: ${issues.join("; ")}`).toEqual([]);
        }
      }
    }
  });
});
