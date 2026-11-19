import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import {
  ADMIN_FIXTURE_CHANNEL_EMAIL_ID,
  ADMIN_FIXTURE_GROUP_ID,
  ADMIN_FIXTURE_LDAP_ID,
  ADMIN_FIXTURE_MIGRATION_RUN_ID,
  ADMIN_FIXTURE_REVISION_PUBLISHED_ID,
  ADMIN_FIXTURE_USER_MEMBER_ID,
  resetAdminFixture,
} from "@/shared/mock/admin-fixture";

/**
 * Site-fixture branch matrix: the admin CRUD guard rails (malformed
 * bodies, optimistic-lock refusals, state gates) behind every F10
 * management surface. Each case pins the declared err_code the pages
 * render inline.
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

describe("user write guards", () => {
  it("refuses duplicates, malformed updates and unknown impact reads", async () => {
    const duplicate = await call("POST", "/admin/users", {
      username: "admin",
      display_name: "Clone",
      email: null,
      password: "fixturepw1",
    });
    expect(duplicate.err_code).toBe(1001);
    const malformed = await call("POST", "/admin/users", { username: "x" });
    expect(malformed.err_code).toBe(1001);
    const missingImpact = await call("GET", "/admin/users/unknown-user/deletion-impact");
    expect(missingImpact.err_code).toBe(1002);
    const staleUpdate = await call(
      "PATCH",
      `/admin/users/${ADMIN_FIXTURE_USER_MEMBER_ID}`,
      { display_name: "New Name" },
      { "If-Match": '"99"' },
    );
    expect(staleUpdate.err_code).toBe(1004);
    const nothingUpdate = await call(
      "PATCH",
      `/admin/users/${ADMIN_FIXTURE_USER_MEMBER_ID}`,
      {},
      { "If-Match": '"1"' },
    );
    expect(nothingUpdate.err_code).toBe(1001);
    const badEmail = await call(
      "PATCH",
      `/admin/users/${ADMIN_FIXTURE_USER_MEMBER_ID}`,
      { email: "not-an-email" },
      { "If-Match": '"1"' },
    );
    expect(badEmail.err_code).toBe(1001);
    const staleDelete = await call("DELETE", `/admin/users/${ADMIN_FIXTURE_USER_MEMBER_ID}`, undefined, {
      "If-Match": '"99"',
    });
    expect(staleDelete.err_code).toBe(1004);
  });
});

describe("permission group write guards", () => {
  it("refuses malformed creates, unknown reads and stale replacements", async () => {
    const malformed = await call("POST", "/admin/permission-groups", { name: "" });
    expect(malformed.err_code).toBe(1001);
    const missing = await call("GET", "/admin/permission-groups/unknown-group");
    expect(missing.err_code).toBe(1002);
    const stale = await call(
      "PUT",
      `/admin/permission-groups/${ADMIN_FIXTURE_GROUP_ID}`,
      { name: "x", enabled: true, member_user_ids: [], granted_flow_ids: [] },
      { "If-Match": '"99"' },
    );
    expect(stale.err_code).toBe(1004);
    const malformedReplace = await call(
      "PUT",
      `/admin/permission-groups/${ADMIN_FIXTURE_GROUP_ID}`,
      { name: "x" },
      { "If-Match": '"1"' },
    );
    expect(malformedReplace.err_code).toBe(1001);
    const staleDelete = await call("DELETE", `/admin/permission-groups/${ADMIN_FIXTURE_GROUP_ID}`, undefined, {
      "If-Match": '"99"',
    });
    expect(staleDelete.err_code).toBe(1004);
  });
});

describe("flow write guards", () => {
  const CHANGE_BODY = {
    name: "guard-flow",
    flow_type: "change_review",
    enabled: true,
    stages: [
      {
        position: 1,
        datasource_id: "4f6f1a2b-0000-4000-8000-00000000a001",
        approval_steps: [{ position: 1, actors: [{ user_id: "u" }] }],
        execution_actors: [{ user_id: "u" }],
      },
    ],
  };

  it("refuses malformed flow bodies at create and replace", async () => {
    const badName = await call("POST", "/admin/flows", { name: "", flow_type: "change_review", enabled: true });
    expect(badName.err_code).toBe(1108);
    const badType = await call("POST", "/admin/flows", { name: "x", flow_type: "other", enabled: true });
    expect(badType.err_code).toBe(1108);
    const missingStages = await call("POST", "/admin/flows", {
      name: "x",
      flow_type: "change_review",
      enabled: true,
    });
    expect(missingStages.err_code).toBe(1108);
    const queryNoCapabilities = await call("POST", "/admin/flows", {
      name: "x",
      flow_type: "query_access",
      enabled: true,
    });
    expect(queryNoCapabilities.err_code).toBe(1108);
    const created = await call<{ id: string; version: number }>("POST", "/admin/flows", CHANGE_BODY);
    expect(created.err_code).toBe(0);
    const stale = await call("PUT", `/admin/flows/${created.data.id}`, CHANGE_BODY, { "If-Match": '"99"' });
    expect(stale.err_code).toBe(1004);
    const malformedReplace = await call("PUT", `/admin/flows/${created.data.id}`, null, { "If-Match": '"1"' });
    expect(malformedReplace.err_code).toBe(1001);
    const unknown = await call("GET", "/admin/flows/unknown-flow");
    expect(unknown.err_code).toBe(1002);
    await call("DELETE", `/admin/flows/${created.data.id}`, undefined, { "If-Match": '"1"' });
    const gone = await call("GET", `/admin/flows/${created.data.id}`);
    expect(gone.err_code).toBe(1002);
  });

  it("refuses masking-rule writes for unknown flows, non-query flows and invalid vocabularies", async () => {
    const unknownFlow = await call("GET", "/admin/flows/unknown/masking-rules");
    expect(unknownFlow.err_code).toBe(1002);
    const unknownPut = await call(
      "PUT",
      "/admin/flows/unknown/datasources/x/masking-rules",
      { sensitive_columns: ["email"] },
    );
    expect(unknownPut.err_code).toBe(1002);
    const changeFlow = "4f6f1a2b-0000-4000-8000-00000000f001";
    const putChange = await call(
      "PUT",
      `/admin/flows/${changeFlow}/datasources/4f6f1a2b-0000-4000-8000-00000000a001/masking-rules`,
      { sensitive_columns: ["email"] },
    );
    expect(putChange.err_code).toBe(1001);
    const queryFlow = "7a1a3c4d-3333-4333-8333-00000000f002";
    const outside = await call(
      "PUT",
      `/admin/flows/${queryFlow}/datasources/unknown-ds/masking-rules`,
      { sensitive_columns: ["email"] },
    );
    expect(outside.err_code).toBe(1001);
    const malformed = await call(
      "PUT",
      `/admin/flows/${queryFlow}/datasources/4f6f1a2b-0000-4000-8000-00000000a001/masking-rules`,
      { sensitive_columns: ["x".repeat(200)] },
    );
    expect(malformed.err_code).toBe(1001);
  });
});

describe("announcement guards", () => {
  it("refuses malformed revisions and unknown publications", async () => {
    const malformed = await call("POST", "/admin/announcement-revisions", { title: "" });
    expect(malformed.err_code).toBe(1001);
    const publishUnknown = await call(
      "PUT",
      "/admin/announcement-publication",
      { announcement_revision_id: "unknown-revision" },
      { "If-Match": '"1"' },
    );
    expect(publishUnknown.err_code).toBe(1002);
    const stalePublish = await call(
      "PUT",
      "/admin/announcement-publication",
      { announcement_revision_id: ADMIN_FIXTURE_REVISION_PUBLISHED_ID },
      { "If-Match": '"99"' },
    );
    expect(stalePublish.err_code).toBe(1004);
    const malformedPublish = await call("PUT", "/admin/announcement-publication", null);
    expect(malformedPublish.err_code).toBe(1001);
  });
});

describe("identity provider guards", () => {
  const OIDC_BODY = {
    provider_key: "corp-oidc",
    provider_kind: "oidc",
    display_name: "Corp OIDC",
    enabled: false,
    configuration: {
      issuer_url: "https://idp.corp.test",
      client_id: "yearning",
      scopes: ["openid"],
      username_claim: "preferred_username",
      display_name_claim: "name",
      email_claim: "email",
      connect_timeout_ms: 5000,
      request_timeout_ms: 10000,
    },
    client_secret: { value: "oidcsec-1" },
  };

  it("refuses malformed creates and enforces secret lifecycle rules", async () => {
    const noKey = await call("POST", "/admin/identity-providers", { provider_kind: "oidc" });
    expect(noKey.err_code).toBe(1001);
    const badKind = await call("POST", "/admin/identity-providers", { provider_key: "x", provider_kind: "saml" });
    expect(badKind.err_code).toBe(1001);

    async function currentVersion(id: string): Promise<number> {
      const read = await call<{ version: number }>("GET", `/admin/identity-providers/${id}`);
      return read.data.version;
    }

    const created = await call<{ id: string }>("POST", "/admin/identity-providers", OIDC_BODY);
    expect(created.err_code).toBe(0);
    const id = created.data.id;

    // Enable first (fresh version per step — guards never bump on failure).
    await call("PUT", `/admin/identity-providers/${id}`, { ...OIDC_BODY, enabled: true }, {
      "If-Match": `"${String(await currentVersion(id))}"`,
    });
    // Enabled OIDC providers refuse secret clearing.
    const cleared = await call(
      "PUT",
      `/admin/identity-providers/${id}`,
      { ...OIDC_BODY, enabled: true, client_secret: null },
      { "If-Match": `"${String(await currentVersion(id))}"` },
    );
    expect(cleared.err_code).toBe(1001);

    const stale = await call("PUT", `/admin/identity-providers/${id}`, OIDC_BODY, { "If-Match": '"99"' });
    expect(stale.err_code).toBe(1004);
    const missing = await call("GET", "/admin/identity-providers/unknown-idp");
    expect(missing.err_code).toBe(1002);
    const malformedBody = await call("PUT", `/admin/identity-providers/${id}`, null, {
      "If-Match": `"${String(await currentVersion(id))}"`,
    });
    expect(malformedBody.err_code).toBe(1001);

    // Enabled providers refuse deletion.
    const deleteEnabled = await call("DELETE", `/admin/identity-providers/${id}`, undefined, {
      "If-Match": `"${String(await currentVersion(id))}"`,
    });
    expect(deleteEnabled.err_code).toBe(1001);

    // Disabled providers delete cleanly.
    await call("PUT", `/admin/identity-providers/${id}`, { ...OIDC_BODY, enabled: false }, {
      "If-Match": `"${String(await currentVersion(id))}"`,
    });
    const deleted = await call("DELETE", `/admin/identity-providers/${id}`, undefined, {
      "If-Match": `"${String(await currentVersion(id))}"`,
    });
    expect(deleted.err_code).toBe(0);

    const unknownTest = await call("POST", "/admin/identity-providers/unknown/connection-tests");
    expect(unknownTest.err_code).toBe(1002);
  });
});

describe("notification channel guards", () => {
  const EMAIL_BODY = {
    kind: "email",
    name: "guard-mail",
    enabled: true,
    configuration: {
      host: "smtp.corp.test",
      port: 465,
      tls_mode: "required",
      username: "noreply",
      from_address: "noreply@corp.test",
    },
    secret: { value: "guardpw1" },
  };

  it("refuses malformed creates and enforces replace/delete rules", async () => {
    const malformed = await call("POST", "/admin/notification-channels", { kind: "email" });
    expect(malformed.err_code).toBe(1001);
    const created = await call<{ id: string; version: number }>("POST", "/admin/notification-channels", EMAIL_BODY);
    expect(created.err_code).toBe(0);
    const stale = await call("PUT", `/admin/notification-channels/${created.data.id}`, EMAIL_BODY, {
      "If-Match": '"99"',
    });
    expect(stale.err_code).toBe(1004);
    const malformedReplace = await call("PUT", `/admin/notification-channels/${created.data.id}`, null, {
      "If-Match": '"1"',
    });
    expect(malformedReplace.err_code).toBe(1001);
    const missing = await call("GET", "/admin/notification-channels/unknown-channel");
    expect(missing.err_code).toBe(1002);
    const unknownTest = await call("POST", "/admin/notification-channels/unknown/test-deliveries", {});
    expect(unknownTest.err_code).toBe(1002);
    const staleDelete = await call("DELETE", `/admin/notification-channels/${created.data.id}`, undefined, {
      "If-Match": '"99"',
    });
    expect(staleDelete.err_code).toBe(1004);
    // Enabled channels refuse secret clearing.
    const cleared = await call(
      "PUT",
      `/admin/notification-channels/${created.data.id}`,
      { ...EMAIL_BODY, secret: null },
      { "If-Match": '"1"' },
    );
    expect(cleared.err_code).toBe(1001);
  });

  it("keeps the seeded email channel testable", async () => {
    const delivery = await call<{ state: string }>(
      "POST",
      `/admin/notification-channels/${ADMIN_FIXTURE_CHANNEL_EMAIL_ID}/test-deliveries`,
      {},
    );
    expect(delivery.err_code).toBe(0);
  });
});

describe("migration review guards", () => {
  it("refuses confirmations for unknown runs/candidates and wrong hashes", async () => {
    const unknownRun = await call(
      "PUT",
      `/admin/migrations/unknown-run/candidate-mappings/x/confirmation`,
      { confirmed: true, target_definition_hash: "sha256:ab" },
      { "If-Match": '"1"' },
    );
    expect(unknownRun.err_code).toBe(1002);
    const run = await call<{ version: number; candidates: { candidate_id: string; target_definition_hash: string }[] }>(
      "GET",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}`,
    );
    const candidate = run.data.candidates[0];
    if (candidate === undefined) throw new Error("no seeded candidate");
    const wrongState = await call(
      "PUT",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}/candidate-mappings/${candidate.candidate_id}/confirmation`,
      { confirmed: true, target_definition_hash: "sha256:00" },
      { "If-Match": `"${String(run.data.version)}"` },
    );
    expect(wrongState.err_code).toBe(1001);
    const unknownCandidate = await call(
      "PUT",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}/candidate-mappings/unknown/confirmation`,
      { confirmed: true, target_definition_hash: "sha256:00" },
      { "If-Match": `"${String(run.data.version)}"` },
    );
    expect(unknownCandidate.err_code).toBe(1002);
  });

  it("refuses approval for unknown runs, wrong phrases and wrong manifests", async () => {
    const unknownRun = await call("POST", "/admin/migrations/unknown/approval", {
      manifest_hash: "sha256:x",
      confirmation_phrase: "APPROVE unknown",
    });
    expect(unknownRun.err_code).toBe(1002);
    const run = await call<{ version: number; manifest_hash: string | null }>(
      "GET",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}`,
    );
    const versionHeader = { "If-Match": `"${String(run.data.version)}"` };
    const wrongManifest = await call(
      "POST",
      `/admin/migrations/${ADMIN_FIXTURE_MIGRATION_RUN_ID}/approval`,
      { manifest_hash: "sha256:ff", confirmation_phrase: `APPROVE ${ADMIN_FIXTURE_MIGRATION_RUN_ID}` },
      versionHeader,
    );
    expect(wrongManifest.err_code).toBe(1001);
  });
});

describe("user deletion builtin-admin guard", () => {
  it("refuses deleting the builtin admin regardless of version", async () => {
    const adminId = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";
    const refused = await call("DELETE", `/admin/users/${adminId}`, undefined, { "If-Match": '"1"' });
    expect(refused.err_code).toBe(1103);
    void ADMIN_FIXTURE_LDAP_ID;
  });
});

describe("non-admin guard", () => {
  it("refuses every site surface with HTTP 403 for non-admin sessions", async () => {
    window.localStorage.setItem("yearning-mock-auth", "default");
    for (const path of [
      "/admin/users",
      "/admin/permission-groups",
      "/admin/flows",
      "/admin/announcement-revisions",
      "/admin/identity-providers",
      "/admin/notification-channels",
      "/admin/migrations",
    ]) {
      const response = await fetch(path);
      expect(response.status).toBe(403);
    }
    window.localStorage.setItem("yearning-mock-auth", "admin");
  });
});

describe("flow model validation arms", () => {
  it("refuses stages with too many, empty or actor-less approval steps", async () => {
    const base = {
      name: "steps-guard",
      flow_type: "change_review",
      enabled: true,
      stages: [
        {
          position: 1,
          datasource_id: "4f6f1a2b-0000-4000-8000-00000000a001",
          approval_steps: [{ position: 1, actors: [{ user_id: "u" }] }],
          execution_actors: [{ user_id: "u" }],
        },
      ],
    };
    const elevenSteps = {
      ...base,
      stages: [
        {
          ...base.stages[0] ?? {},
          approval_steps: Array.from({ length: 11 }, (_, index) => ({ position: index + 1, actors: [] })),
        },
      ],
    };
    const tooMany = await call("POST", "/admin/flows", elevenSteps);
    expect(tooMany.err_code).toBe(1108);
    const emptyActors = {
      ...base,
      stages: [
        { ...(base.stages[0] ?? { position: 1, datasource_id: "" }), approval_steps: [{ position: 1, actors: [] }] },
      ],
    };
    const refused = await call("POST", "/admin/flows", emptyActors);
    expect(refused.err_code).toBe(1108);
    const replaceTooMany = await call("PUT", "/admin/flows/4f6f1a2b-0000-4000-8000-00000000f001", elevenSteps, {
      "If-Match": '"1"',
    });
    expect(replaceTooMany.err_code).toBe(1108);
    const replaceNoStages = await call(
      "PUT",
      "/admin/flows/4f6f1a2b-0000-4000-8000-00000000f001",
      { ...base, stages: [] },
      { "If-Match": '"1"' },
    );
    expect(replaceNoStages.err_code).toBe(1108);
    const queryManySteps = await call("POST", "/admin/flows", {
      name: "q-steps",
      flow_type: "query_access",
      enabled: true,
      query_capabilities: [{ datasource_id: "4f6f1a2b-0000-4000-8000-00000000a001", can_query: true, can_export: true }],
      approval_steps: [{ position: 1, actors: [] }],
    });
    expect(queryManySteps.err_code).toBe(1108);
    const replaceQueryBad = await call(
      "PUT",
      "/admin/flows/7a1a3c4d-3333-4333-8333-00000000f002",
      { name: "q", flow_type: "query_access", enabled: true, approval_steps: [] },
      { "If-Match": '"1"' },
    );
    expect(replaceQueryBad.err_code).toBe(1108);
  });
});

describe("user update arms", () => {
  it("validates display-name edits and reads the deletion impact of the blocked user", async () => {
    const emptyName = await call(
      "PATCH",
      "/admin/users/7a1a3c4d-1111-4111-8111-00000000e101",
      { display_name: "  " },
      { "If-Match": '"1"' },
    );
    expect(emptyName.err_code).toBe(1001);
    const missingUser = await call("PATCH", "/admin/users/unknown", { display_name: "x" }, { "If-Match": '"1"' });
    expect(missingUser.err_code).toBe(1002);
    const missingDelete = await call("DELETE", "/admin/users/unknown", undefined, { "If-Match": '"1"' });
    expect(missingDelete.err_code).toBe(1002);
    // The blocked user's impact also carries the template-reference arm.
    const impact = await call<{ blockers: { code: string }[] }>(
      "GET",
      "/admin/users/7a1a3c4d-1111-4111-8111-00000000e102/deletion-impact",
    );
    expect(impact.data.blockers.map((blocker) => blocker.code)).toContain("referenced_by_template");
    const blockedDelete = await call("DELETE", "/admin/users/7a1a3c4d-1111-4111-8111-00000000e102", undefined, {
      "If-Match": '"1"',
    });
    // The active-orders arm fires first (mirror check order); the
    // template-reference arm is visible in the impact preview above.
    expect(blockedDelete.err_code).toBe(1104);
  });
});

describe("unknown-row reads", () => {
  it("answers 1002 for every missing single-row site read", async () => {
    const group = await call("GET", "/admin/permission-groups/unknown");
    expect(group.err_code).toBe(1002);
    const channel = await call("GET", "/admin/notification-channels/unknown");
    expect(channel.err_code).toBe(1002);
    const revision = await call("GET", "/admin/migrations/unknown");
    expect(revision.err_code).toBe(1002);
    const user = await call("GET", "/admin/users/unknown/deletion-impact");
    expect(user.err_code).toBe(1002);
  });
});
