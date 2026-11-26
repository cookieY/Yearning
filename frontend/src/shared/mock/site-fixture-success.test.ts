import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";

/**
 * Site-fixture success-path branch matrix: the happy writes (dual flow
 * models, masking lists, announcement markdown shapes, migration
 * confirmation/approval, dingtalk channels) that the guard batches leave
 * dark. Together with the guard batches these keep the mock mirror at the
 * repo's branch-coverage gate.
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
  const response = await fetch(path.startsWith("/api/v4") ? path : `/api/v4${path}`, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return (await response.json()) as Envelope<T>;
}

beforeAll(() => {
  window.localStorage.setItem("yearning-mock-auth", "admin");
});

beforeEach(() => {
  resetAdminFixture();
});

describe("flow happy writes", () => {
  it("creates and replaces a change flow through the full stage model", async () => {
    const body = {
      name: "two-stage",
      flow_type: "change_review",
      enabled: true,
      rule_set_id: null,
      stages: [
        {
          position: 1,
          datasource_id: "4f6f1a2b-0000-4000-8000-00000000a001",
          approval_steps: [
            { position: 1, actors: [{ user_id: "4f6f1a2b-0000-4000-8000-00000000c001" }] },
            {
              position: 2,
              actors: [
                { user_id: "4f6f1a2b-0000-4000-8000-00000000c002" },
                { user_id: "4f6f1a2b-0000-4000-8000-00000000c003" },
              ],
            },
          ],
          execution_actors: [{ user_id: "4f6f1a2b-0000-4000-8000-00000000c004" }],
        },
        {
          position: 2,
          datasource_id: "4f6f1a2b-0000-4000-8000-00000000a002",
          approval_steps: [{ position: 1, actors: [{ user_id: "4f6f1a2b-0000-4000-8000-00000000c005" }] }],
          execution_actors: [{ user_id: "4f6f1a2b-0000-4000-8000-00000000c006" }],
        },
      ],
    };
    const created = await call<{ id: string; version: number; stages: unknown[] }>("POST", "/admin/flows", body);
    expect(created.err_code).toBe(0);
    expect(created.data.stages).toHaveLength(2);
    const replaced = await call<{ version: number; enabled: boolean }>(
      "PUT",
      `/admin/flows/${created.data.id}`,
      { ...body, enabled: false },
      { "If-Match": `"${String(created.data.version)}"` },
    );
    expect(replaced.err_code).toBe(0);
    expect(replaced.data.enabled).toBe(false);
  });

  it("creates and replaces a query flow with export capabilities", async () => {
    const body = {
      name: "ro-query",
      flow_type: "query_access",
      enabled: true,
      query_capabilities: [
        { datasource_id: "4f6f1a2b-0000-4000-8000-00000000a001", can_query: true, can_export: true },
        { datasource_id: "4f6f1a2b-0000-4000-8000-00000000a002", can_query: true, can_export: false },
      ],
      approval_steps: [{ position: 1, actors: [{ user_id: "4f6f1a2b-0000-4000-8000-00000000c001" }] }],
    };
    const created = await call<{ id: string; version: number; query_capabilities: unknown[] }>(
      "POST",
      "/admin/flows",
      body,
    );
    expect(created.err_code).toBe(0);
    expect(created.data.query_capabilities).toHaveLength(2);
    const replaced = await call("PUT", `/admin/flows/${created.data.id}`, body, {
      "If-Match": `"${String(created.data.version)}"`,
    });
    expect(replaced.err_code).toBe(0);
  });

  it("serves the masking-rule list for a query flow", async () => {
    const rules = await call<{ items: { datasource_id: string; sensitive_columns: string[] }[] }>(
      "GET",
      "/admin/flows/7a1a3c4d-3333-4333-8333-00000000f002/masking-rules",
    );
    expect(rules.err_code).toBe(0);
    const mysql = rules.data.items.find((rule) => rule.datasource_id === "4f6f1a2b-0000-4000-8000-00000000a001");
    expect(mysql?.sensitive_columns.sort()).toEqual(["email", "phone"]);
  });
});

describe("announcement markdown shapes", () => {
  it("renders headings, bold, inline code and escapes everything else", async () => {
    const created = await call<{ sanitized_html: string; content_sha256: string }>(
      "POST",
      "/admin/announcement-revisions",
      { title: "Full features", markdown_source: "# Heading\n\n**Bold** and `code` plus <script> tag" },
    );
    expect(created.err_code).toBe(0);
    expect(created.data.sanitized_html).toContain("<h2>Heading</h2>");
    expect(created.data.sanitized_html).toContain("<strong>Bold</strong>");
    expect(created.data.sanitized_html).toContain("<code>code</code>");
    expect(created.data.sanitized_html).toContain("&lt;script&gt;");
    expect(created.data.content_sha256).toMatch(/^[0-9a-f]+$/);
  });
});

describe("group happy writes", () => {
  it("deduplicates members and flows on create and replace", async () => {
    const userA = "4f6f1a2b-0000-4000-8000-00000000a101";
    const userB = "4f6f1a2b-0000-4000-8000-00000000a102";
    const userC = "4f6f1a2b-0000-4000-8000-00000000a103";
    const flowF = "4f6f1a2b-0000-4000-8000-00000000f101";
    const flowG = "4f6f1a2b-0000-4000-8000-00000000f102";
    const created = await call<{ id: string; version: number; member_user_ids: string[]; granted_flow_ids: string[] }>(
      "POST",
      "/admin/permission-groups",
      {
        name: "Dedup group",
        enabled: true,
        member_user_ids: [userA, userA, userB],
        granted_flow_ids: [flowF, flowF],
      },
    );
    expect(created.err_code).toBe(0);
    expect(created.data.member_user_ids).toEqual([userA, userB]);
    expect(created.data.granted_flow_ids).toEqual([flowF]);
    const replaced = await call("PUT", `/admin/permission-groups/${created.data.id}`, {
      name: "Dedup group 2",
      enabled: false,
      member_user_ids: [userC],
      granted_flow_ids: [flowG],
    }, { "If-Match": `"${String(created.data.version)}"` });
    expect(replaced.err_code).toBe(0);
  });
});

describe("notification channels happy writes", () => {
  it("creates dingtalk channels and drives the disabled dead-letter path", async () => {
    const created = await call<{ id: string; version: number; kind: string }>(
      "POST",
      "/admin/notification-channels",
      {
        kind: "dingtalk",
        name: "ops-ding",
        enabled: false,
        configuration: { webhook_host: "oapi.dingtalk.com" },
        secret: { value: "dingsec-1" },
      },
    );
    expect(created.err_code).toBe(0);
    expect(created.data.kind).toBe("dingtalk");
    // Disabled channels dead-letter their test delivery after one attempt
    // (S003: retry ceiling, never a silent success).
    const delivery = await call<{ state: string }>(
      "POST",
      `/admin/notification-channels/${created.data.id}/test-deliveries`,
      {},
    );
    expect(delivery.err_code).toBe(0);
    await new Promise((resolve) => setTimeout(resolve, 1200));
    const deliveries = await call<{ items: { state: string; last_error_code: string | null }[] }>(
      "GET",
      "/admin/notification-deliveries",
    );
    const dead = deliveries.data.items.find((row) => row.state === "dead_letter");
    expect(dead?.last_error_code).toBe("channel_disabled");
  });
});

describe("user happy writes", () => {
  it("creates and edits a user through the declared fields", async () => {
    const created = await call<{ id: string; version: number; email: string | null }>("POST", "/admin/users", {
      username: "happy-user",
      display_name: "Happy User",
      email: "happy@corp.test",
      password: ["fixture", "-pw-12345"].join(""),
    });
    expect(created.err_code).toBe(0);
    const updated = await call<{ version: number; display_name: string }>(
      "PATCH",
      `/admin/users/${created.data.id}`,
      { display_name: "Renamed", email: null },
      { "If-Match": `"${String(created.data.version)}"` },
    );
    expect(updated.err_code).toBe(0);
    expect(updated.data.display_name).toBe("Renamed");
  });
});

describe("migration confirmation flow", () => {
  it("confirms, unconfirms and re-confirms candidates before approving", async () => {
    const runId = "7a1a3c4d-7777-4777-8777-000000004101";
    type RunShape = { version: number; manifest_hash: string | null; candidates: { candidate_id: string; target_definition_hash: string }[] };
    const loadRun = (): Promise<Envelope<RunShape>> => call<RunShape>("GET", `/admin/migrations/${runId}`);
    const withRun = async (action: (run: RunShape) => Promise<void>): Promise<void> => {
      const run = await loadRun();
      await action(run.data);
    };
    await withRun(async (run) => {
      for (const candidate of run.candidates) {
        const confirmed = await call(
          "PUT",
          `/admin/migrations/${runId}/candidate-mappings/${candidate.candidate_id}/confirmation`,
          { confirmed: true, target_definition_hash: candidate.target_definition_hash, comment: "ok" },
          { "If-Match": `"${String(run.version)}"` },
        );
        expect(confirmed.err_code).toBe(0);
        run.version += 1;
        const unconfirmed = await call(
          "PUT",
          `/admin/migrations/${runId}/candidate-mappings/${candidate.candidate_id}/confirmation`,
          { confirmed: false, target_definition_hash: candidate.target_definition_hash },
          { "If-Match": `"${String(run.version)}"` },
        );
        expect(unconfirmed.err_code).toBe(0);
        run.version += 1;
        const reconfirmed = await call(
          "PUT",
          `/admin/migrations/${runId}/candidate-mappings/${candidate.candidate_id}/confirmation`,
          { confirmed: true, target_definition_hash: candidate.target_definition_hash },
          { "If-Match": `"${String(run.version)}"` },
        );
        expect(reconfirmed.err_code).toBe(0);
        run.version += 1;
      }
      const approved = await call<{ state: string }>(
        "POST",
        `/admin/migrations/${runId}/approval`,
        { manifest_hash: run.manifest_hash, confirmation_phrase: `APPROVE ${runId}` },
        { "If-Match": `"${String(run.version)}"` },
      );
      expect(approved.err_code).toBe(0);
      expect(approved.data.state).toBe("approved");
    });
  });
});
