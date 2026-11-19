import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import { resetAdminFixture } from "@/shared/mock/admin-fixture";
import {
  ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
  ADMIN_FIXTURE_GROUP_ID,
  ADMIN_FIXTURE_LDAP_ID,
  ADMIN_FIXTURE_MIGRATION_RUN_ID,
  ADMIN_FIXTURE_REVISION_PUBLISHED_ID,
  ADMIN_FIXTURE_USER_BLOCKED_ID,
  ADMIN_FIXTURE_USER_MEMBER_ID,
} from "@/shared/mock/admin-fixture";

/**
 * FE-F10 site-domain fixture contract tests: users (deletion impact and
 * blockers, P102/P105), permission groups (flow grants, no datasource
 * transfers), flows (dual write models, FLOW_REFERENCED delete refusal,
 * masking vocabulary normalization), announcements (append-only revisions
 * + single publication pointer, S005), notification channels (Outbox test
 * delivery, S003) and the migration review run (candidate confirmation
 * gating the APPROVE-phrase approval, M001).
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

beforeAll(() => {
  window.localStorage.setItem("yearning-mock-auth", "admin");
});

beforeEach(() => {
  resetAdminFixture();
});

describe("site fixture users", () => {
  it("previews deletion impact and refuses blocked deletions with the declared codes", async () => {
    const impact = await call<{ can_delete: boolean; blockers: { code: string }[] }>(
      "GET",
      `/admin/users/${ADMIN_FIXTURE_USER_BLOCKED_ID}/deletion-impact`,
    );
    expect(impact.err_code).toBe(0);
    expect(impact.data.can_delete).toBe(false);
    expect(impact.data.blockers.map((blocker) => blocker.code)).toContain("active_orders");

    const deleted = await call("DELETE", `/admin/users/${ADMIN_FIXTURE_USER_BLOCKED_ID}`, undefined, {
      "If-Match": '"1"',
    });
    expect(deleted.err_code).toBe(1104);
  });

  it("deletes an unreferenced user and removes group memberships", async () => {
    // Membership first: the member user starts in the seeded group.
    const groupsBefore = await call<{ items: { id: string; member_user_ids: string[] }[] }>(
      "GET",
      "/admin/permission-groups",
    );
    expect(
      groupsBefore.data.items.find((group) => group.id === ADMIN_FIXTURE_GROUP_ID)?.member_user_ids,
    ).toContain(ADMIN_FIXTURE_USER_MEMBER_ID);

    const deleted = await call("DELETE", `/admin/users/${ADMIN_FIXTURE_USER_MEMBER_ID}`, undefined, {
      "If-Match": '"1"',
    });
    expect(deleted.err_code).toBe(0);
    const groupsAfter = await call<{ items: { id: string; member_user_ids: string[] }[] }>(
      "GET",
      "/admin/permission-groups",
    );
    expect(
      groupsAfter.data.items.find((group) => group.id === ADMIN_FIXTURE_GROUP_ID)?.member_user_ids,
    ).not.toContain(ADMIN_FIXTURE_USER_MEMBER_ID);
  });

  it("refuses creating users with short passwords (VALIDATION_FAILED)", async () => {
    const created = await call("POST", "/admin/users", {
      username: "shortpw",
      display_name: "Short",
      email: null,
      password: "short",
    });
    expect(created.err_code).toBe(1001);
  });
});

describe("site fixture flows", () => {
  it("rejects delete while a permission group grants the flow (FLOW_REFERENCED)", async () => {
    const flows = await call<{ items: { id: string; flow_type: string }[] }>("GET", "/admin/flows");
    const queryFlow = flows.data.items.find((flow) => flow.flow_type === "query_access");
    expect(queryFlow).toBeDefined();
    const deleted = await call("DELETE", `/admin/flows/${queryFlow?.id ?? ""}`, undefined, { "If-Match": '"1"' });
    expect(deleted.err_code).toBe(1106);
  });

  it("normalizes and deduplicates the masking vocabulary by case folding", async () => {
    const flows = await call<{ items: { id: string; flow_type: string }[] }>("GET", "/admin/flows");
    const queryFlow = flows.data.items.find((flow) => flow.flow_type === "query_access");

    const written = await call<{ sensitive_columns: string[] }>(
      "PUT",
      `/admin/flows/${queryFlow?.id ?? ""}/datasources/4f6f1a2b-0000-4000-8000-00000000a001/masking-rules`,
      { sensitive_columns: ["Email", " EMAIL ", "phone"] },
    );
    expect(written.err_code).toBe(0);
    expect(written.data.sensitive_columns.sort()).toEqual(["email", "phone"]);
  });

  it("refuses masking rules on change_review flows", async () => {
    const flows = await call<{ items: { id: string; flow_type: string }[] }>("GET", "/admin/flows");
    const changeFlow = flows.data.items.find((flow) => flow.flow_type === "change_review");
    const listed = await call("GET", `/admin/flows/${changeFlow?.id ?? ""}/masking-rules`);
    expect(listed.err_code).toBe(1001);
  });

  it("rejects change flows without execution actors (FLOW_ACTOR_NODE_EMPTY)", async () => {
    const created = await call("POST", "/admin/flows", {
      name: "no-actors",
      flow_type: "change_review",
      enabled: true,
      stages: [
        {
          position: 1,
          datasource_id: "4f6f1a2b-0000-4000-8000-00000000a001",
          approval_steps: [{ position: 1, actors: [] }],
          execution_actors: [],
        },
      ],
    });
    expect(created.err_code).toBe(1108);
  });
});

describe("site fixture announcements", () => {
  it("serves the current publication to any authenticated session", async () => {
    const current = await call<{ revision: { id: string } } | null>("GET", "/announcements/current");
    expect(current.err_code).toBe(0);
    expect(current.data?.revision.id).toBe(
      ADMIN_FIXTURE_REVISION_PUBLISHED_ID,
    );
  });

  it("creates append-only revisions and moves the single publication pointer", async () => {
    const created = await call<{ id: string; revision_number: number }>(
      "POST",
      "/admin/announcement-revisions",
      { title: "Announcement v2", markdown_source: "# Update\\n\\nNew **maintenance window**." },
    );
    expect(created.err_code).toBe(0);
    expect(created.data.revision_number).toBeGreaterThan(1);

    const published = await call<{ revision: { id: string } }>(
      "PUT",
      "/admin/announcement-publication",
      { announcement_revision_id: created.data.id },
      { "If-Match": '"1"' },
    );
    expect(published.err_code).toBe(0);
    expect(published.data.revision.id).toBe(created.data.id);

    const current = await call("GET", "/announcements/current");
    expect((current.data as { revision: { id: string } }).revision.id).toBe(created.data.id);
  });
});

describe("site fixture notification channels", () => {
  it("drives a test delivery through the Outbox state machine", async () => {
    // F12 RCP-20261017 Direction A (No.015): test delivery returns an acceptance receipt
    // (task_id + channel_id); delivery state machine is observed via the deliveries list.
    const receipt = await call<{ task_id: string; channel_id: string }>(
      "POST",
      `/admin/notification-channels/${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}/test-deliveries`,
      {},
    );
    expect(receipt.err_code).toBe(0);
    expect(receipt.data.task_id).toBeTruthy();
    expect(receipt.data.channel_id).toBe(ADMIN_FIXTURE_CHANNEL_EMAIL_ID);
    // The Outbox retry ceiling is five attempts (S003).
    const listed = await call<{ items: { state: string; delivery_attempt_count: number }[] }>(
      "GET",
      "/admin/notification-deliveries",
    );
    expect(listed.data.items.length).toBeGreaterThan(0);
    expect(listed.data.items.every((row) => row.delivery_attempt_count <= 5)).toBe(true);
  });

  it("requires a secret on channel create", async () => {
    const created = await call("POST", "/admin/notification-channels", {
      kind: "email",
      name: "no-secret",
      enabled: false,
      configuration: {
        host: "smtp.test",
        port: 465,
        tls_mode: "required",
        username: "u",
        from_address: "u@test",
      },
    });
    expect(created.err_code).toBe(1001);
  });
});

describe("site fixture migration review", () => {
  it("gates approval behind per-candidate confirmation and the exact phrase", async () => {
    const early = await call(
      "POST",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}/approval`,
      { manifest_hash: "sha256:x", confirmation_phrase: "APPROVE wrong" },
      { "If-Match": '"1"' },
    );
    expect(early.err_code).toBe(1001);

    const run = await call<{
      version: number;
      candidates: { candidate_id: string; target_definition_hash: string }[];
      manifest_hash: string;
    }>("GET", `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}`);
    expect(run.err_code).toBe(0);

    // Each confirmation bumps the run version (optimistic lock), exactly
    // like the If-Match contract the workbench consumes.
    let version = run.data.version;
    for (const candidate of run.data.candidates) {
      const confirmed = await call(
        "PUT",
        `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}/candidate-mappings/${candidate.candidate_id}/confirmation`,
        { confirmed: true, target_definition_hash: candidate.target_definition_hash },
        { "If-Match": `"${String(version)}"` },
      );
      expect(confirmed.err_code).toBe(0);
      version += 1;
    }

    // A stale If-Match is refused with CONCURRENT_MODIFICATION before the
    // approval semantics run.
    const stale = await call(
      "POST",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}/approval`,
      { manifest_hash: run.data.manifest_hash, confirmation_phrase: `APPROVE ${ADMIN_FIXTURE_MIGRATION_RUN_ID}` },
      { "If-Match": `"${String(run.data.version)}"` },
    );
    expect(stale.err_code).toBe(1004);

    const approved = await call<{ state: string }>(
      "POST",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}/approval`,
      {
        manifest_hash: run.data.manifest_hash,
        confirmation_phrase: `APPROVE ${ADMIN_FIXTURE_MIGRATION_RUN_ID}`,
      },
      { "If-Match": `"${String(version)}"` },
    );
    expect(approved.err_code).toBe(0);
    expect(approved.data.state).toBe("approved");
  });
});

describe("site fixture identity providers", () => {
  it("keeps LDAP a singleton and never echoes secrets", async () => {
    const second = await call("POST", "/admin/identity-providers", {
      provider_key: "ldap-2",
      provider_kind: "ldap",
      display_name: "Second",
      enabled: false,
      configuration: {
        host: "ldap2.test",
        port: 636,
        transport: "ldaps",
        server_name: "ldap2.test",
        bind_dn: "",
        base_dn: "dc=x",
        user_filter: "(&(uid={username}))",
        username_attribute: "uid",
        display_name_attribute: "cn",
        email_attribute: "mail",
        connect_timeout_ms: 5000,
        bind_timeout_ms: 5000,
        search_timeout_ms: 5000,
      },
      bind_password: { value: "ldapsec-2" },
    });
    expect(second.err_code).toBe(1001);

    const providers = await call<{ items: Record<string, unknown>[] }>("GET", "/admin/identity-providers");
    const ldap = providers.data.items.find((provider) => provider.id === ADMIN_FIXTURE_LDAP_ID);
    expect(ldap).toBeDefined();
    expect(JSON.stringify(ldap)).not.toContain("ldapsec-1");
    expect(ldap?.secret_configured).toBe(true);
  });
});
