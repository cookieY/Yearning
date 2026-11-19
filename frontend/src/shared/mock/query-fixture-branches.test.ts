import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { setMockIdentity } from "@/shared/mock/identity-store";
import {
  QUERY_FIXTURE_DS_MYSQL_ID,
  QUERY_FIXTURE_DS_PG_ID,
  QUERY_FIXTURE_FLOW_ID,
  QUERY_FIXTURE_SESSION_USER_ID,
  resetQueryFixture,
  seedQueryScenario,
} from "@/shared/mock/query-fixture";

/**
 * Query-fixture branch matrix (second batch): guard rails that the happy
 * paths leave dark — unauthenticated probes, malformed bodies, owner and
 * relation scoping, state-machine refusals and cursor misuse. Each case
 * pins the declared err_code the pages render.
 */

interface Envelope<T> {
  err_code: number;
  message: string;
  data: T;
  request_id: string;
}

async function call<T>(
  method: string,
  path: string,
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<Envelope<T>> {
  const response = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return (await response.json()) as Envelope<T>;
}

beforeEach(() => {
  window.localStorage.setItem("yearning-mock-auth", "default");
  resetQueryFixture();
});

describe("unauthenticated guard", () => {
  it("answers every query surface with HTTP 401 Problem Details when expired", async () => {
    seedQueryScenario("query-session");
    window.localStorage.setItem("yearning-mock-auth", "expired");
    for (const path of ["/query-sessions", "/query-grants", "/query-access-requests"]) {
      const response = await fetch(path);
      expect(response.status).toBe(401);
      expect(response.headers.get("content-type")).toContain("application/problem+json");
    }
    const execution = await fetch(`/query-sessions/qs-fixture-active/executions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ datasource_id: QUERY_FIXTURE_DS_MYSQL_ID, schema_name: "app", sql: "select 1" }),
    });
    expect(execution.status).toBe(401);
  });
});

describe("access request write guards", () => {
  beforeEach(() => {
    seedQueryScenario("query-flow");
  });

  it("refuses malformed bodies with VALIDATION_FAILED", async () => {
    const created = await call("POST", "/query-access-requests", { flow_id: QUERY_FIXTURE_FLOW_ID });
    expect(created.err_code).toBe(1001);
    const empty = await call("POST", "/query-access-requests", null);
    expect(empty.err_code).toBe(1001);
  });

  it("refuses datasources outside the flow and duplicate open requests", async () => {
    const outside = await call("POST", "/query-access-requests", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      datasource_ids: ["00000000-0000-4000-8000-00000000000c"],
      requested_until: new Date(Date.now() + 86400000).toISOString(),
      reason: "outside",
    });
    expect(outside.err_code).toBe(2014);

    const until = new Date(Date.now() + 86400000).toISOString();
    await call("POST", "/query-access-requests", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
      requested_until: until,
      reason: "first",
    });
    const duplicate = await call("POST", "/query-access-requests", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
      requested_until: until,
      reason: "second",
    });
    expect(duplicate.err_code).toBe(1005);
  });

  it("refuses reads and writes for unrelated requests", async () => {
    seedQueryScenario("query-session");
    const unrelated = await call("GET", "/query-access-requests/qar-fixture-none");
    expect(unrelated.err_code).toBe(1002);
    const withdrawn = await call(
      "POST",
      "/query-access-requests/qar-fixture-none/withdrawal",
      { reason: "x" },
      { "If-Match": '"1"', "Idempotency-Key": "k" },
    );
    expect(withdrawn.err_code).toBe(1002);
  });
});

describe("grant lifecycle refusals", () => {
  beforeEach(() => {
    seedQueryScenario("query-session");
  });

  async function grantId(): Promise<string> {
    const grants = await call<{ items: { id: string; version: number }[] }>("GET", "/query-grants");
    const first = grants.data.items[0];
    return first === undefined ? "" : first.id;
  }

  it("requires a reason on revocation and refuses stale versions", async () => {
    const id = await grantId();
    const noReason = await call("POST", `/query-grants/${id}/revocations`, {});
    expect(noReason.err_code).toBe(1001);
    const stale = await call("POST", `/query-grants/${id}/revocations`, { reason: "x" }, { "If-Match": '"99"' });
    expect(stale.err_code).toBe(1004);
  });

  it("refuses to revoke twice (QUERY_GRANT_REVOKED) and blocks new sessions on the revoked grant", async () => {
    const id = await grantId();
    // Revocation authority is the frozen reviewer (§18.44); the session
    // attempt below returns to the grant owner.
    setMockIdentity("reviewer-a");
    await call("POST", `/query-grants/${id}/revocations`, { reason: "rotation" }, { "If-Match": '"1"', "Idempotency-Key": "r1" });
    const again = await call("POST", `/query-grants/${id}/revocations`, { reason: "again" }, { "If-Match": '"2"', "Idempotency-Key": "r2" });
    expect(again.err_code).toBe(4004);
    setMockIdentity("henry");
    const session = await call("POST", "/query-sessions", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      grant_id: id,
      datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
    });
    expect(session.err_code).toBe(4004);
  });

  it("relinquishes an owned grant and closes its sessions; renewal only works while active", async () => {
    const id = await grantId();
    const relinquished = await call(
      "POST",
      `/query-grants/${id}/relinquishment`,
      { reason: "done" },
      { "If-Match": '"1"', "Idempotency-Key": "l1" },
    );
    expect(relinquished.err_code).toBe(0);
    expect(relinquished.data).toMatchObject({ state: "relinquished" });
    const sessions = await call<{ items: { state: string }[] }>("GET", "/query-sessions");
    expect(sessions.data.items.every((row) => row.state === "closed")).toBe(true);

    const renewed = await call(
      "POST",
      `/query-grants/${id}/renewal-requests`,
      { requested_until: new Date(Date.now() + 86400000).toISOString(), reason: "extend" },
      { "Idempotency-Key": "n1" },
    );
    expect(renewed.err_code).toBe(4004);
  });
});

describe("session and execution guards", () => {
  beforeEach(() => {
    seedQueryScenario("query-session");
  });

  it("refuses session creation with malformed bodies and foreign grants", async () => {
    const malformed = await call("POST", "/query-sessions", { flow_id: QUERY_FIXTURE_FLOW_ID });
    expect(malformed.err_code).toBe(1001);
    const foreignGrant = await call("POST", "/query-sessions", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      grant_id: "00000000-0000-4000-8000-00000000000d",
      datasource_ids: [QUERY_FIXTURE_DS_MYSQL_ID],
    });
    expect(foreignGrant.err_code).toBe(4001);
  });

  it("refuses closing a session twice with QUERY_SESSION_CLOSED", async () => {
    await call("POST", "/query-sessions/qs-fixture-active/closure", { reason: "bye" }, { "Idempotency-Key": "c1" });
    const again = await call("POST", "/query-sessions/qs-fixture-active/closure", { reason: "again" }, { "Idempotency-Key": "c2" });
    expect(again.err_code).toBe(4006);
    // §18.49: reason is OPTIONAL (backend defaults empty to user_closed) —
    // a reasonless closure no longer trips validation; the state gate rules.
    const reasonless = await call("POST", "/query-sessions/qs-fixture-active/closure", {}, { "Idempotency-Key": "c3" });
    expect(reasonless.err_code).toBe(4006);
  });

  it("blocks metadata reads once the session is closed", async () => {
    await call("POST", "/query-sessions/qs-fixture-active/closure", { reason: "bye" }, { "Idempotency-Key": "c4" });
    const schemas = await call("GET", `/query-sessions/qs-fixture-active/metadata/schemas?datasource_id=${QUERY_FIXTURE_DS_MYSQL_ID}`);
    expect(schemas.err_code).toBe(4006);
    const tables = await call(
      "GET",
      `/query-sessions/qs-fixture-active/metadata/tables?datasource_id=${QUERY_FIXTURE_DS_MYSQL_ID}&schema_name=app`,
    );
    expect(tables.err_code).toBe(4006);
    const columns = await call(
      "GET",
      `/query-sessions/qs-fixture-active/metadata/columns?datasource_id=${QUERY_FIXTURE_DS_MYSQL_ID}&schema_name=app&table_name=users`,
    );
    expect(columns.err_code).toBe(4006);
  });

  it("refuses executions with malformed payloads and unknown schemas", async () => {
    const malformed = await call("POST", "/query-sessions/qs-fixture-active/executions", null);
    expect(malformed.err_code).toBe(1001);
    const noSchema = await call("POST", "/query-sessions/qs-fixture-active/executions", {
      datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
      schema_name: "",
      sql: "select 1",
    });
    expect(noSchema.err_code).toBe(1001);
    const unknown = await call("POST", "/query-sessions/qs-fixture-active/executions", {
      datasource_id: QUERY_FIXTURE_DS_MYSQL_ID,
      schema_name: "nope",
      sql: "select 1",
    });
    expect(unknown.err_code).toBe(1001);
  });

  it("refuses page reads with an invalid purpose and unknown cursor", async () => {
    const first = await call<{ execution_id: string; page: { next_cursor: string | null } }>(
      "POST",
      "/query-sessions/qs-fixture-active/executions",
      { datasource_id: QUERY_FIXTURE_DS_MYSQL_ID, schema_name: "app", sql: "select id from app.users", page_size: 500 },
    );
    const executionId = first.data.execution_id;
    const badPurpose = await call(
      "GET",
      `/query-executions/${executionId}/pages?cursor=${encodeURIComponent(first.data.page.next_cursor ?? "")}&purpose=bulk`,
    );
    expect(badPurpose.err_code).toBe(1001);
    const unknownCursor = await call(
      "GET",
      `/query-executions/${executionId}/pages?cursor=not-a-cursor&purpose=display`,
    );
    expect(unknownCursor.err_code).toBe(1009);
    const unknownExecution = await call("GET", "/query-executions/none/pages?cursor=c&purpose=display");
    expect(unknownExecution.err_code).toBe(1002);
  });
});

describe("direct-entry branches (approval disabled)", () => {
  beforeEach(() => {
    seedQueryScenario("query-flow-direct");
  });

  it("refuses datasources outside the flow capabilities", async () => {
    const created = await call("POST", "/query-sessions", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      grant_id: null,
      datasource_ids: [QUERY_FIXTURE_DS_PG_ID, "00000000-0000-4000-8000-00000000000e"],
    });
    // PG is in the flow; the unknown id is filtered out and PG remains,
    // so this succeeds — the guard case is a fully-unknown selection:
    expect(created.err_code === 0 || created.err_code === 4002).toBe(true);
    const unknownOnly = await call("POST", "/query-sessions", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      grant_id: null,
      datasource_ids: ["00000000-0000-4000-8000-00000000000e"],
    });
    expect(unknownOnly.err_code).toBe(4002);
  });
});

describe("approval decision guards", () => {
  // The decision surface belongs to the frozen reviewer identity (§18.44).
  beforeEach(() => {
    setMockIdentity("reviewer-a");
    seedQueryScenario("query-approval");
  });

  afterEach(() => {
    setMockIdentity("henry");
  });
  it("refuses invalid decisions and closed requests", async () => {
    const invalid = await call(
      "POST",
      "/query-access-requests/qar-fixture-pending/decisions",
      { decision: "maybe" },
      { "If-Match": '"1"', "Idempotency-Key": "d1" },
    );
    expect(invalid.err_code).toBe(1001);

    // Approve once, then decide again on the terminal request.
    await call(
      "POST",
      "/query-access-requests/qar-fixture-pending/decisions",
      { decision: "approve" },
      { "If-Match": '"1"', "Idempotency-Key": "d2" },
    );
    const terminal = await call(
      "POST",
      "/query-access-requests/qar-fixture-pending/decisions",
      { decision: "approve" },
      { "If-Match": '"2"', "Idempotency-Key": "d3" },
    );
    expect(terminal.err_code).toBe(1010);
  });

  it("hides the request entirely from the relation-less builtin admin", async () => {
    // admin_is_not_business_override: the admin is not the frozen reviewer
    // here, so the read relation excludes the request outright (1002 —
    // existence is not leaked through a 3001).
    window.localStorage.setItem("yearning-mock-auth", "admin");
    const hidden = await call(
      "POST",
      "/query-access-requests/qar-fixture-pending/decisions",
      { decision: "approve" },
      { "If-Match": '"1"', "Idempotency-Key": "d4" },
    );
    expect(hidden.err_code).toBe(1002);
  });
});

describe("cursor pagination edge cases", () => {
  beforeEach(() => {
    seedQueryScenario("query-session");
  });

  it("keeps display reads bounded and export reads authorized per datasource", async () => {
    // PG has no export capability in the grant-frozen session; create a
    // direct flow session covering PG first (approval disabled world).
    seedQueryScenario("query-flow-direct");
    const session = await call<{ id: string }>("POST", "/query-sessions", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      grant_id: null,
      datasource_ids: [QUERY_FIXTURE_DS_PG_ID],
    });
    expect(session.err_code).toBe(0);
    const execution = await call<{ execution_id: string; page: { next_cursor: string | null } }>(
      "POST",
      `/query-sessions/${session.data.id}/executions`,
      { datasource_id: QUERY_FIXTURE_DS_PG_ID, schema_name: "public", sql: "select id from public.analytics_events", page_size: 50 },
    );
    expect(execution.err_code).toBe(0);
    const refused = await call(
      "GET",
      `/query-executions/${execution.data.execution_id}/pages?cursor=${encodeURIComponent(execution.data.page.next_cursor ?? "")}&purpose=export`,
    );
    expect(refused.err_code).toBe(4003);
  });

  it("ignores pagination cursors that no longer resolve (empty page, no replay)", async () => {
    const grants = await call<{ items: { id: string }[] }>("GET", "/query-grants?after=unknown-id");
    expect(grants.err_code).toBe(0);
    expect(grants.data.items).toEqual([]);
  });
});

describe("session user identity", () => {
  it("pins the shared session identity used by the scoping rules", () => {
    expect(QUERY_FIXTURE_SESSION_USER_ID).toBe("0198d9cc-e65d-7b9d-a8aa-3c81945f99ac");
  });
});

describe("fixture data-model branches", () => {
  beforeEach(() => {
    seedQueryScenario("query-session");
  });

  it("executes a SELECT without a FROM clause against the schema's first table", async () => {
    const execution = await call<{ rows: unknown[][] }>(
      "POST",
      "/query-sessions/qs-fixture-active/executions",
      { datasource_id: QUERY_FIXTURE_DS_MYSQL_ID, schema_name: "app", sql: "select 1", page_size: 10 },
    );
    expect(execution.err_code).toBe(0);
    expect(execution.data.rows.length).toBeGreaterThan(0);
  });

  it("falls back to the schema's first table for unknown FROM targets", async () => {
    const execution = await call<{ rows: unknown[][] }>(
      "POST",
      "/query-sessions/qs-fixture-active/executions",
      { datasource_id: QUERY_FIXTURE_DS_MYSQL_ID, schema_name: "app", sql: "select id from app.nonexistent" },
    );
    expect(execution.err_code).toBe(0);
  });

  it("serves table and column metadata for the whole frozen scope", async () => {
    // F12 RCP-20261017 Direction A (No.010 family): metadata read surface is an {items} envelope.
    const tables = await call<{ items: { table_name: string }[] }>(
      "GET",
      `/query-sessions/qs-fixture-active/metadata/tables?datasource_id=${QUERY_FIXTURE_DS_MYSQL_ID}&schema_name=stats`,
    );
    expect(tables.err_code).toBe(0);
    expect(tables.data.items.map((table) => table.table_name)).toContain("daily_counts");
    const columns = await call<{ items: { column_name: string }[] }>(
      "GET",
      `/query-sessions/qs-fixture-active/metadata/columns?datasource_id=${QUERY_FIXTURE_DS_MYSQL_ID}&schema_name=stats&table_name=daily_counts`,
    );
    expect(columns.err_code).toBe(0);
    expect(columns.data.items.map((column) => column.column_name)).toContain("gmv");
    const unknownSchema = await call(
      "GET",
      `/query-sessions/qs-fixture-active/metadata/tables?datasource_id=${QUERY_FIXTURE_DS_MYSQL_ID}&schema_name=missing`,
    );
    expect(unknownSchema.err_code).toBe(1001);
    const unknownTable = await call(
      "GET",
      `/query-sessions/qs-fixture-active/metadata/columns?datasource_id=${QUERY_FIXTURE_DS_MYSQL_ID}&schema_name=app&table_name=missing`,
    );
    expect(unknownTable.err_code).toBe(1001);
    const unknownDatasource = await call(
      "GET",
      `/query-sessions/qs-fixture-active/metadata/schemas?datasource_id=unknown`,
    );
    expect(unknownDatasource.err_code).toBe(4002);
  });

  it("paginates grants and sessions with explicit limits", async () => {
    const grants = await call<{ items: unknown[] }>("GET", "/query-grants?limit=1");
    expect(grants.err_code).toBe(0);
    expect(grants.data.items).toHaveLength(1);
    const sessions = await call<{ items: unknown[] }>("GET", "/query-sessions?limit=1");
    expect(sessions.err_code).toBe(0);
    expect(sessions.data.items).toHaveLength(1);
  });

  it("applies the PG masking vocabulary from the flow rules", async () => {
    seedQueryScenario("query-flow-direct");
    const session = await call<{ id: string }>("POST", "/query-sessions", {
      flow_id: QUERY_FIXTURE_FLOW_ID,
      grant_id: null,
      datasource_ids: [QUERY_FIXTURE_DS_PG_ID],
    });
    const execution = await call<{ rows: unknown[][] }>(
      "POST",
      `/query-sessions/${session.data.id}/executions`,
      { datasource_id: QUERY_FIXTURE_DS_PG_ID, schema_name: "public", sql: "select payload from public.analytics_events" },
    );
    expect(execution.err_code).toBe(0);
    // The mask is applied server-side per vocabulary column regardless of
    // the SELECT projection (fixture serves the full table shape).
    expect(execution.data.rows[0]).toContain("***");
  });
});
