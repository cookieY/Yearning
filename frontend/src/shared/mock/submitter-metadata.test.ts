import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  FIXTURE_DATASOURCE_ID,
  FIXTURE_FLOW_ID,
  resetReviewFixture,
} from "./review-fixture";
import {
  CATALOG_DATASOURCES,
  CATALOG_FLOWS,
} from "./flow-catalog";
import { setMockAuthBehavior } from "./auth-scenario-store";

/**
 * Submitter metadata projection mock gates (RCP-20260904): the three
 * /users/me/flows/{flow}/datasources/{ds}/metadata/* handlers answer items
 * only for the granted pair, 2014 without enumerating existence, and the
 * tables/columns faces filter by the requested schema/table.
 */

const CATALOG_DS = CATALOG_DATASOURCES[0]?.id ?? "";

async function get(path: string): Promise<{ status: number; body: Record<string, unknown> }> {
  const response = await fetch(`https://yearning.test${path}`);
  return { status: response.status, body: (await response.json()) as Record<string, unknown> };
}

describe("submitter metadata mock (§18.42)", () => {
  beforeEach(() => {
    setMockAuthBehavior("admin");
    resetReviewFixture();
  });

  afterEach(() => {
    resetReviewFixture();
  });

  it("serves schemas/tables/columns for the default flow's stage datasource", async () => {
    const schemas = await get(`/users/me/flows/${FIXTURE_FLOW_ID}/datasources/${FIXTURE_DATASOURCE_ID}/metadata/schemas`);
    expect(schemas.body.err_code).toBe(0);
    expect((schemas.body.data as { items: Array<{ name: string }> }).items.map((entry) => entry.name)).toContain("app");

    const tables = await get(
      `/users/me/flows/${FIXTURE_FLOW_ID}/datasources/${FIXTURE_DATASOURCE_ID}/metadata/tables?schema_name=app`,
    );
    const tableNames = (tables.body.data as { items: Array<{ table_name: string }> }).items.map((entry) => entry.table_name);
    expect(tableNames).toContain("orders");
    expect(tableNames).toContain("users");

    const columns = await get(
      `/users/me/flows/${FIXTURE_FLOW_ID}/datasources/${FIXTURE_DATASOURCE_ID}/metadata/columns?schema_name=app&table_name=users`,
    );
    const columnRows = (columns.body.data as { items: Array<{ column_name: string; masked: boolean }> }).items;
    expect(columnRows.map((entry) => entry.column_name)).toContain("email");
    expect(columnRows.find((entry) => entry.column_name === "email")?.masked).toBe(true);
  });

  it("serves the demo world for a catalog flow's stage datasource", async () => {
    const catalogFlow = CATALOG_FLOWS.find((entry) => entry.stages.some((stage) => stage.datasource_id === CATALOG_DS));
    expect(catalogFlow).toBeDefined();
    const catalogFlowId = catalogFlow?.id ?? "";
    const fallbackDs = CATALOG_DATASOURCES[5]?.id ?? "";
    const schemas = await get(`/users/me/flows/${catalogFlowId}/datasources/${CATALOG_DS}/metadata/schemas`);
    expect(schemas.body.err_code).toBe(0);
    // Unknown catalog datasources fall back to the deterministic demo world.
    const fallback = await get(`/users/me/flows/${catalogFlowId}/datasources/${fallbackDs}/metadata/tables?schema_name=public`);
    expect((fallback.body.data as { items: Array<{ table_name: string }> }).items.map((entry) => entry.table_name)).toContain("demo_table");
    const fallbackColumns = await get(`/users/me/flows/${catalogFlowId}/datasources/${fallbackDs}/metadata/columns?schema_name=public&table_name=demo_table`);
    expect((fallbackColumns.body.data as { items: Array<{ column_name: string }> }).items.map((entry) => entry.column_name)).toContain("id");
  });

  it("answers 2014 without enumerating for non-granted pairs", async () => {
    // Foreign datasource on a granted flow.
    const foreignDs = await get(
      `/users/me/flows/${FIXTURE_FLOW_ID}/datasources/99999999-0000-4000-8000-000000000009/metadata/schemas`,
    );
    expect(foreignDs.body.err_code).toBe(2014);
    // Foreign flow id on a granted datasource.
    const foreignFlow = await get(
      `/users/me/flows/99999999-0000-4000-8000-000000000009/datasources/${FIXTURE_DATASOURCE_ID}/metadata/schemas`,
    );
    expect(foreignFlow.body.err_code).toBe(2014);
    // Catalog flow asking for the default flow's datasource misses the
    // per-flow stage binding.
    const catalogFlowId = CATALOG_FLOWS[0]?.id ?? "";
    const wrongStage = await get(
      `/users/me/flows/${catalogFlowId}/datasources/${FIXTURE_DATASOURCE_ID}/metadata/schemas`,
    );
    expect(wrongStage.body.err_code).toBe(2014);
  });

  it("treats missing query params as empty filters", async () => {
    // tables without schema_name: the '' request misses every schema → 1002.
    const tablesNoParam = await get(
      `/users/me/flows/${FIXTURE_FLOW_ID}/datasources/${FIXTURE_DATASOURCE_ID}/metadata/tables`,
    );
    expect(tablesNoParam.body.err_code).toBe(1002);
    // columns without params: empty filters yield an empty item list.
    const columnsNoParam = await get(
      `/users/me/flows/${FIXTURE_FLOW_ID}/datasources/${FIXTURE_DATASOURCE_ID}/metadata/columns`,
    );
    expect(columnsNoParam.body.err_code).toBe(0);
    expect((columnsNoParam.body.data as { items: unknown[] }).items).toEqual([]);
  });

  it("answers 1002 when the requested schema does not exist", async () => {
    const missing = await get(
      `/users/me/flows/${FIXTURE_FLOW_ID}/datasources/${FIXTURE_DATASOURCE_ID}/metadata/tables?schema_name=nonexistent`,
    );
    expect(missing.body.err_code).toBe(1002);
  });
});
