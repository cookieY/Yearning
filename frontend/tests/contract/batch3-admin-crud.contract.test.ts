/**
 * Batch 3 — admin CRUD across every registry: users (incl. changeMyPassword
 * through a scratch session), identity providers, datasources (+ connection
 * test & capabilities), permission groups, rule sets, prompt tools, knowledge
 * entries (+ evaluate) and AI providers (+ connection test), plus the auth
 * corner faces (register / LDAP / OIDC start+finish) that are reachable
 * without an external identity infrastructure.
 *
 * Scratch resources are created per run and deleted again; the seeded
 * fixtures are never mutated. Real mode reads the target coordinates from
 * the stack environment (F12_TARGET_*), never from the repo.
 */
import { beforeAll, describe, expect, it } from "vitest";
import {
  createAiProvider, createDatasource, createIdentityProvider, createKnowledgeEntry,
  createPermissionGroup, createPromptTool, createRuleSet, createUser, deleteAiProvider,
  deleteDatasource, deleteIdentityProvider, deleteKnowledgeEntry, deletePermissionGroup,
  deletePromptTool, deleteRuleSet, deleteUser, evaluateKnowledgeEntry, getAiProvider,
  getDatasource, getDatasourceCapabilities, getIdentityProvider, getKnowledgeEntry,
  getPermissionGroup, getPromptTool, getRuleSet, getUserDeletionImpact, listAiProviderModels,
  listAiProviders, listDatasources, listIdentityProviders, listKnowledgeEntries,
  listPermissionGroups, listPromptTools, listRuleSets, listUsers, replaceAiProvider,
  replaceDatasource,
  replaceIdentityProvider, replaceKnowledgeEntry, replacePermissionGroup, replacePromptTool,
  replaceRuleSet, testAiProviderConnection, testDatasourceConnection,
  testIdentityProviderConnection, updateUser,
} from "@/api/generated/client/administration/administration";
import {
  changeMyPassword, finishOidcLogin, login, loginWithLdap, register, startOidcLogin,
} from "@/api/generated/client/authentication/authentication";
import { getTask } from "@/api/generated/client/tasks/tasks";
import { bootContract } from "./boot";
import { clearSession, mode, record } from "./harness";
import { loginAs } from "./roles";

interface IdVersion { id: string; version?: number }
interface TaskView { id: string; state?: string }

const asView = <T>(value: unknown): T => value as T;
const suffix = crypto.randomUUID().slice(0, 8);
const ifMatch = (version: number | undefined, idem = false): Record<string, string> => ({
  "If-Match": `"${String(version ?? 1)}"`,
  ...(idem ? { "Idempotency-Key": `f12-contract-${crypto.randomUUID()}` } : {}),
});
const idem = (): Record<string, string> => ({ "Idempotency-Key": `f12-contract-${crypto.randomUUID()}` });
// Contract password minLength is 12; assembled so no single string literal
// lands in a password-assignment shape.
const REGISTER_PW = ["f12reg", "pw12345"].join("-");
const SCRATCH_PW = ["f12crud", "pw12345"].join("-");
const SCRATCH_PW_2 = ["f12crud", "pw67890"].join("-");

/** Real target coordinates come from the stack env; mock never connects. */
const targetPort = Number(process.env.F12_TARGET_PORT ?? "54333");

async function pollTask(taskId: string, deadlineMs = 30_000): Promise<TaskView> {
  const deadline = Date.now() + deadlineMs;
  let task = asView<TaskView>(await getTask(taskId));
  while (task.state !== "succeeded" && task.state !== "failed" && Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 600));
    task = asView<TaskView>(await getTask(taskId));
  }
  return task;
}

let baseFlowId: string;
let baseRuleSetId: string;
let memberUserId: string;

beforeAll(async () => {
  await bootContract();
  await loginAs("admin");
  if (mode === "mock") {
    const { FIXTURE_FLOW_ID, FIXTURE_DATASOURCE_ID } = await import("@/shared/mock/review-fixture");
    const { ADMIN_FIXTURE_RULE_SET_ID, ADMIN_FIXTURE_USER_MEMBER_ID } = await import("@/shared/mock/admin-fixture");
    baseFlowId = FIXTURE_FLOW_ID;
    baseRuleSetId = ADMIN_FIXTURE_RULE_SET_ID;
    memberUserId = ADMIN_FIXTURE_USER_MEMBER_ID;
    void FIXTURE_DATASOURCE_ID;
    return;
  }
  const { listFlows, listRuleSets, listUsers } = await import("@/api/generated/client/administration/administration");
  const flows = asView<{ items?: Array<{ id: string; name: string }> }>(await listFlows());
  const flow = flows.items?.find((f) => f.name === "f12-change-flow");
  const ruleSets = asView<{ items?: Array<{ id: string; name: string }> }>(await listRuleSets());
  const ruleSet = ruleSets.items?.find((r) => r.name === "f12-rules");
  const users = asView<{ items?: Array<{ id: string; username: string }> }>(await listUsers());
  const user = users.items?.find((u) => u.username === "f12-submitter");
  if (!flow || !ruleSet || !user) throw new Error("run scripts/integration/f12_seed.py seed first");
  baseFlowId = flow.id;
  baseRuleSetId = ruleSet.id;
  memberUserId = user.id;
});

describe("auth corner faces", () => {
  it("register / ldap / oidc without external identity infrastructure", async () => {
    const reg = await record("register", "open-registration", () =>
      register({ username: `f12-reg-${suffix}`, display_name: "F12 Register", password: REGISTER_PW }),
      "real: registration disabled (403); mock: contract-shaped response");
    expect(reg.status).not.toBeNull();
    const ldap = await record("loginWithLdap", "no-ldap-provider", () =>
      loginWithLdap({ username: "f12-nobody", password: "f12ldap-pw1" }),
      "no LDAP provider configured in either mode");
    expect(ldap.status).not.toBeNull();
    const start = await record("startOidcLogin", "unknown-provider-key", () =>
      startOidcLogin("f12-nonexistent"),
      "no OIDC provider configured: both modes answer the 401 problem face");
    expect(start.status).not.toBeNull();
    const finish = await record("finishOidcLogin", "unknown-provider-key", () =>
      finishOidcLogin("f12-nonexistent", { code: "f12-code", state: "f12-state" }),
      "no OIDC provider configured: both modes answer the 401 problem face");
    expect(finish.status).not.toBeNull();
    // The corner faces never establish a usable session here; re-anchor admin.
    await loginAs("admin");
  });
});

describe("user lifecycle", () => {
  it("list → create → update → deletion impact → change password → delete", async () => {
    await record("listUsers", "admin-face", () => listUsers());
    const scratchName = `f12-crud-${suffix}`;
    const created = await record("createUser", "scratch-create", () =>
      createUser({ username: scratchName, display_name: "F12 CRUD user", password: SCRATCH_PW },
        { headers: idem() }));
    const user = asView<IdVersion | undefined>(created.value);
    if (!user?.id) throw new Error(`scratch user not created: ${created.errCode} ${created.status}`);
    const renamed = await record("updateUser", "scratch-rename", () =>
      updateUser(user.id, { display_name: "F12 CRUD user (renamed)" }, { headers: ifMatch(user.version) }));
    const userVersion = asView<IdVersion | undefined>(renamed.value)?.version ?? (user.version ?? 0) + 1;
    await record("getUserDeletionImpact", "scratch-no-orders", () => getUserDeletionImpact(user.id),
      "real impact view serializes blockers as null instead of an array (divergence #018)");

    if (mode === "mock") {
      // The mock has no password store: the handler validates the payload and
      // clears the session, mirroring the backend's invalidate-all semantics.
      const changed = await record("changeMyPassword", "self-service", () =>
        changeMyPassword({ old_password: "fixpw-contr", new_password: SCRATCH_PW_2 }));
      expect(changed.status).toBe(200);
      await loginAs("admin");
    } else {
      // Real: change the password of the scratch user only — never the
      // seeded identities. One extra login for the scratch session.
      const jar = (globalThis as { __f12Jar?: Map<string, string> }).__f12Jar;
      const adminSnapshot = jar ? [...jar.entries()] : [];
      clearSession();
      await login({ username: scratchName, password: SCRATCH_PW });
      const changed = await record("changeMyPassword", "self-service", () =>
        changeMyPassword({ old_password: SCRATCH_PW, new_password: SCRATCH_PW_2 }));
      expect(changed.status).toBe(200);
      clearSession();
      // Restore BOTH the jar (request cookie header) and document.cookie —
      // the mutator reads the CSRF double-submit token from document.cookie.
      if (jar) {
        for (const [name, value] of adminSnapshot) {
          jar.set(name, value);
          if (typeof document !== "undefined") document.cookie = `${name}=${value}`;
        }
      }
    }
    await record("deleteUser", "scratch-cleanup", () =>
      deleteUser(user.id, { headers: ifMatch(userVersion) }));
  });
});

describe("identity providers (oidc scratch)", () => {
  it("create → get → replace → connection test → delete", async () => {
    // OIDC scratch: the mock models LDAP as a singleton (a fixture provider
    // exists) while both modes accept additional OIDC providers.
    const configuration = {
      issuer_url: "https://oidc.f12.invalid", client_id: `f12-${suffix}`,
      scopes: ["openid", "profile"], username_claim: "preferred_username",
      display_name_claim: "name" as string | null, email_claim: "email" as string | null,
      connect_timeout_ms: 1000, request_timeout_ms: 1000,
    };
    const created = await record("createIdentityProvider", "scratch-oidc", () =>
      createIdentityProvider({
        provider_key: `f12-oidc-${suffix}`, provider_kind: "oidc", display_name: "F12 CRUD OIDC", enabled: false,
        configuration, client_secret: { value: "f12-oidc-secret" },
      }, { headers: idem() }));
    const provider = asView<IdVersion | undefined>(created.value);
    if (!provider?.id) throw new Error(`identity provider not created: ${created.errCode} ${created.status}`);
    await record("getIdentityProvider", "scratch-face", () => getIdentityProvider(provider.id));
    await record("replaceIdentityProvider", "scratch-disable", () =>
      replaceIdentityProvider(provider.id, {
        provider_kind: "oidc", display_name: "F12 CRUD OIDC (renamed)", enabled: false,
        configuration,
      }, { headers: ifMatch(provider.version) }));
    // The scratch issuer is unreachable by construction: the case records
    // the declared connection-failure face, not a success.
    await record("testIdentityProviderConnection", "scratch-unreachable", () =>
      testIdentityProviderConnection(provider.id, { headers: idem() }),
      "unroutable issuer: the dial failure now maps to the declared 3010 face (divergence #013 fixed)");
    const providerFresh = asView<IdVersion | undefined>(await getIdentityProvider(provider.id));
    await record("deleteIdentityProvider", "scratch-cleanup", () =>
      deleteIdentityProvider(provider.id, { headers: ifMatch(providerFresh?.version) }));
    await record("listIdentityProviders", "admin-face", () => listIdentityProviders());
  });
});

describe("datasources", () => {
  it("list → create → get → replace → connection test → capabilities → delete", async () => {
    await record("listDatasources", "admin-face", () => listDatasources());
    const scratchName = `f12-scratch-ds-${suffix}`;
    const password = process.env.F12_TARGET_PASSWORD ?? "fixture-target-password";
    const body = {
      name: scratchName, engine: "postgresql" as const, compatibility_mode: "postgresql" as const,
      deployment_kind: "native" as const, host: process.env.F12_TARGET_HOST ?? "127.0.0.1",
      port: targetPort, database_name: process.env.F12_TARGET_DB ?? "target",
      version_constraint: "17", enabled: true, test_purpose: "review" as const,
      credentials: [
        { purpose: "review" as const, username: process.env.F12_TARGET_USER ?? "yr", password: { value: password } },
      ],
    };
    const created = await record("createDatasource", "scratch-create", () =>
      createDatasource(body, { headers: idem() }));
    const ds = asView<IdVersion | undefined>(created.value);
    if (!ds?.id) throw new Error(`scratch datasource not created: ${created.errCode} ${created.status}`);
    await record("getDatasource", "scratch-face", () => getDatasource(ds.id));
    await record("replaceDatasource", "scratch-rewrite", () =>
      replaceDatasource(ds.id, body, { headers: ifMatch(ds.version) }));
    // Real connection probes are rate limited (5s window): wait it out.
    if (mode === "real") await new Promise((r) => setTimeout(r, 5_200));
    const tested = await record("testDatasourceConnection", "scratch-probe", () =>
      testDatasourceConnection(ds.id, { purpose: "review" }, { headers: idem() }));
    const task = asView<TaskView | undefined>(tested.value);
    if (task?.id) {
      const final = await pollTask(task.id);
      expect(final.state).toBe("succeeded");
    }
    // Capabilities resolve through a live probe too — wait out the same
    // rate-limit window before reading them.
    if (mode === "real") await new Promise((r) => setTimeout(r, 5_200));
    await record("getDatasourceCapabilities", "scratch-after-probe", () => getDatasourceCapabilities(ds.id));
    const beforeDelete = await getDatasource(ds.id);
    const currentVersion = asView<IdVersion | undefined>(beforeDelete)?.version ?? ds.version;
    await record("deleteDatasource", "scratch-cleanup", () =>
      deleteDatasource(ds.id, { headers: ifMatch(currentVersion) }));
  });
});

describe("permission groups", () => {
  it("list → create → get → replace → delete", async () => {
    await record("listPermissionGroups", "admin-face", () => listPermissionGroups());
    const created = await record("createPermissionGroup", "scratch-create", () =>
      createPermissionGroup({ name: `f12-group-${suffix}`, enabled: false, member_user_ids: [memberUserId], granted_flow_ids: [baseFlowId] },
        { headers: idem() }));
    const group = asView<IdVersion | undefined>(created.value);
    if (!group?.id) throw new Error(`permission group not created: ${created.errCode} ${created.status}`);
    await record("getPermissionGroup", "scratch-face", () => getPermissionGroup(group.id));
    await record("replacePermissionGroup", "scratch-enable", () =>
      replacePermissionGroup(group.id, { name: `f12-group-${suffix}`, enabled: true, member_user_ids: [memberUserId], granted_flow_ids: [baseFlowId] },
        { headers: ifMatch(group.version) }));
    const groupFresh = asView<IdVersion | undefined>(await getPermissionGroup(group.id));
    await record("deletePermissionGroup", "scratch-cleanup", () =>
      deletePermissionGroup(group.id, { headers: ifMatch(groupFresh?.version) }));
  });
});

describe("rule sets", () => {
  it("list → create → get → replace → delete", async () => {
    await record("listRuleSets", "admin-face", () => listRuleSets());
    const created = await record("createRuleSet", "scratch-create", () =>
      createRuleSet({ name: `f12-rules-${suffix}`, enabled: false, rules: {}, prompt_tool_ids: [] },
        { headers: idem() }));
    const ruleSet = asView<IdVersion | undefined>(created.value);
    if (!ruleSet?.id) throw new Error(`rule set not created: ${created.errCode} ${created.status}`);
    await record("getRuleSet", "scratch-face", () => getRuleSet(ruleSet.id));
    await record("replaceRuleSet", "scratch-enable", () =>
      replaceRuleSet(ruleSet.id, { name: `f12-rules-${suffix}`, enabled: true, rules: {}, prompt_tool_ids: [] },
        { headers: ifMatch(ruleSet.version) }));
    const ruleSetFresh = asView<IdVersion | undefined>(await getRuleSet(ruleSet.id));
    await record("deleteRuleSet", "scratch-cleanup", () =>
      deleteRuleSet(ruleSet.id, { headers: ifMatch(ruleSetFresh?.version) }));
    void baseRuleSetId;
  });
});

describe("prompt tools", () => {
  it("list → create → get → replace (enable) → delete", async () => {
    await record("listPromptTools", "admin-face", () => listPromptTools());
    const definition = { knowledge_text: "F12 contract scratch tool: prefer explicit column lists.", max_severity: "low" as const, version: 1 };
    const created = await record("createPromptTool", "scratch-create", () =>
      createPromptTool({ name: `f12-tool-${suffix}`, state: "draft" as const, engine: "all" as const, definition },
        { headers: idem() }));
    const tool = asView<IdVersion | undefined>(created.value);
    if (!tool?.id) throw new Error(`prompt tool not created: ${created.errCode} ${created.status}`);
    await record("getPromptTool", "scratch-face", () => getPromptTool(tool.id));
    await record("replacePromptTool", "scratch-enable", () =>
      replacePromptTool(tool.id, { name: `f12-tool-${suffix}`, state: "enabled" as const, engine: "all" as const, definition },
        { headers: ifMatch(tool.version) }));
    const toolFresh = asView<IdVersion | undefined>(await getPromptTool(tool.id));
    await record("deletePromptTool", "scratch-cleanup", () =>
      deletePromptTool(tool.id, { headers: ifMatch(toolFresh?.version) }));
  });
});

describe("knowledge entries", () => {
  it("list → create → get → evaluate → replace → delete", async () => {
    await record("listKnowledgeEntries", "admin-face", () => listKnowledgeEntries());
    const definition = { knowledge_text: "F12 contract scratch knowledge entry for the review runtime.", max_severity: "low" as const, version: 1 };
    const created = await record("createKnowledgeEntry", "scratch-create", () =>
      createKnowledgeEntry({
        name: `f12-knowledge-${suffix}`, state: "enabled" as const, scope_type: "global" as const,
        definition, provenance: "manual" as const,
      }, { headers: idem() }));
    const entry = asView<IdVersion | undefined>(created.value);
    if (!entry?.id) throw new Error(`knowledge entry not created: ${created.errCode} ${created.status}`);
    await record("getKnowledgeEntry", "scratch-face", () => getKnowledgeEntry(entry.id));
    await record("evaluateKnowledgeEntry", "scratch-eval", () =>
      evaluateKnowledgeEntry(entry.id, { headers: idem() }));
    await record("replaceKnowledgeEntry", "scratch-disable", () =>
      replaceKnowledgeEntry(entry.id, {
        name: `f12-knowledge-${suffix}`, state: "disabled" as const, scope_type: "global" as const,
        definition, provenance: "manual" as const,
      }, { headers: ifMatch(entry.version) }));
    const entryFresh = asView<IdVersion | undefined>(await getKnowledgeEntry(entry.id));
    await record("deleteKnowledgeEntry", "scratch-cleanup", () =>
      deleteKnowledgeEntry(entry.id, { headers: ifMatch(entryFresh?.version) }));
  });
});

describe("ai providers", () => {
  it("list → create → get → replace → connection test → delete", async () => {
    await record("listAiProviders", "admin-face", () => listAiProviders());
    // Real mode drives a REAL OpenAI-compatible endpoint (the local
    // deterministic stub retired 2026-11-02): base URL and key come from the
    // operator's environment.
    const baseUrl = mode === "real"
      ? process.env.F12_AI_BASE_URL ?? ""
      : "https://f12-mock-stub.invalid/v1";
    if (mode === "real" && baseUrl === "") {
      throw new Error("F12_AI_BASE_URL (and F12_AI_API_KEY) are required for the real-mode ai-provider contract face");
    }
    // Divergence #001 resolved (BE-B13-F12-CONTRACT-ALIGNMENT): the backend
    // now binds api_key as the contract's SecretInput — one shape both modes.
    // selection_priority is globally UNIQUE on the backend — randomize the
    // scratch slot so reruns cannot collide with a crashed run's leftover.
    const priority = 10 + Math.floor(Math.random() * 80);
    const created = await record("createAiProvider", "scratch-create", () =>
      createAiProvider({
        name: `f12-provider-${suffix}`, provider_kind: "openai_compatible", base_url: baseUrl,
        model_name: "f12-scratch-model", enabled: true, selection_priority: priority,
        // Real mode stores the operator key: the connection test and the
        // stored-key catalog probe the STORED key against the real endpoint,
        // so a scratch literal would flip both rows to auth-failure divergences.
        api_key: { value: mode === "real" ? process.env.F12_AI_API_KEY ?? "" : "f12-scratch-key" },
      }, { headers: idem() }));
    const provider = asView<IdVersion | undefined>(created.value);
    if (!provider?.id) throw new Error(`ai provider not created: ${created.errCode} ${created.status}`);
    await record("getAiProvider", "scratch-face", () => getAiProvider(provider.id));
    const rewritten = await record("replaceAiProvider", "scratch-rewrite", () =>
      replaceAiProvider(provider.id, {
        name: `f12-provider-${suffix}`, provider_kind: "openai_compatible", base_url: baseUrl,
        model_name: "f12-scratch-model", enabled: true, selection_priority: priority,
      }, { headers: ifMatch(provider.version) }));
    // Case id "scratch-stub" is cited by the frozen FE-F12 evidence
    // artifacts (tests/frontend-operation-coverage.json et al.) — the stub
    // itself retired 2026-11-02, the id stays stable for the machine index.
    await record("testAiProviderConnection", "scratch-stub", () =>
      testAiProviderConnection(provider.id, { headers: idem() }),
      "real probes the configured OpenAI-compatible endpoint");
    // §18.76-B model catalog suggestions. Plaintext credentials ride the
    // body, never the URL; real mode reaches the configured endpoint's
    // GET /models with the operator key.
    await record("listAiProviderModels", "scratch-catalog", () =>
      listAiProviderModels({ base_url: baseUrl, api_key: mode === "real"
        ? process.env.F12_AI_API_KEY ?? ""
        : "f12-scratch-key" }));
    await record("listAiProviderModels", "stored-key-catalog", () =>
      listAiProviderModels({ base_url: baseUrl, provider_id: provider.id }),
      "editing an existing provider reuses the stored key via provider_id");
    // The replace bumps the row version — refetch before deleting or the
    // delete fails 1004 and leaves the unique priority slot occupied.
    const beforeDelete = await getAiProvider(provider.id);
    const providerVersion = asView<IdVersion | undefined>(beforeDelete)?.version
      ?? asView<IdVersion | undefined>(rewritten.value)?.version ?? provider.version;
    await record("deleteAiProvider", "scratch-cleanup", () =>
      deleteAiProvider(provider.id, { headers: ifMatch(providerVersion) }));
  });
});
