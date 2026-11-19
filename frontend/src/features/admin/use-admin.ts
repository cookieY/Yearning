import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { setSystemTimezone } from "@/shared/lib/timezones";
import {
  assessSettingsImpact,
  createAiProvider,
  createDatasource,
  createFlow,
  createIdentityProvider,
  createKnowledgeEntry,
  createNotificationChannel,
  createPermissionGroup,
  createPromptTool,
  createRuleSet,
  createUser,
  deleteAiProvider,
  deleteDatasource,
  deleteFlow,
  deleteIdentityProvider,
  deleteKnowledgeEntry,
  deleteNotificationChannel,
  deletePermissionGroup,
  deletePromptTool,
  deleteRuleSet,
  deleteUser,
  evaluateKnowledgeEntry,
  generatePromptToolTestCases,
  getDatasourceCapabilities,
  getDatasourceDiagnosticsConfig,
  getSettingsNamespace,
  getUserDeletionImpact,
  listAiProviders,
  listAnnouncementRevisions,
  listAuditEvents,
  listDatasources,
  listFlows,
  listFlowMaskingRules,
  listIdentityProviders,
  listKnowledgeEntries,
  listNotificationChannels,
  listNotificationRules,
  createNotificationRule,
  replaceNotificationRule,
  deleteNotificationRule,
  previewNotificationRule,
  listNotificationDeliveries,
  listPermissionGroups,
  listPromptTools,
  listRuleSets,
  listSettingsRevisions,
  listUsers,
  optimizePromptToolPrompt,
  replaceAiProvider,
  replaceDatasource,
  replaceFlow,
  replaceIdentityProvider,
  replaceKnowledgeEntry,
  replaceNotificationChannel,
  replacePermissionGroup,
  replacePromptTool,
  replaceRuleSet,
  replaceSettingsNamespace,
  replaceFlowMaskingRule,
  runPromptToolTest,
  testAiProviderConnection,
  testDatasourceConnection,
  testIdentityProviderConnection,
  updateDatasourceDiagnosticsConfig,
  updateUser,
} from "@/api/generated/client/administration/administration";
import {
  approveLegacyMigrationRun,
  confirmLegacyMigrationCandidate,
  getLegacyMigrationRun,
  listLegacyMigrationRuns,
} from "@/api/generated/client/administration/administration";
import {
  createAnnouncementRevision,
  publishAnnouncementRevision,
} from "@/api/generated/client/administration/administration";
import { getTask } from "@/api/generated/client/tasks/tasks";
import { BusinessError } from "@/shared/api/mutator";
import { businessErrCodeByName } from "@/shared/api/error-display";
import type {
  AiProvider,
  AiProviderWrite,
  AnnouncementRevision,
  AuditEvent,
  ConfirmLegacyMigrationCandidateRequest,
  CreateAiProviderRequest,
  CreateIdentityProviderRequest,
  CreateNotificationChannelRequest,
  NotificationChannelWrite,
  CreateUserRequest,
  Datasource,
  DatasourceCapabilities,
  DatasourceDiagnosticsConfig,
  DatasourceWrite,
  Flow,
  FlowMaskingRule,
  FlowMaskingRuleWrite,
  FlowWrite,
  IdentityProvider,
  KnowledgeEntry,
  KnowledgeEntryEvaluation,
  KnowledgeEntryWrite,
  LegacyMigrationCandidate,
  LegacyMigrationRun,
  NotificationChannel,
  NotificationDelivery,
  PermissionGroup,
  PromptTool,
  PermissionGroupWrite,
  PromptToolWrite,
  OptimizePromptToolPromptRequest,
  OptimizePromptToolPromptResponse,
  GeneratePromptToolTestCasesRequest,
  GeneratePromptToolTestCasesResponse,
  PromptToolTestCase,
  RunPromptToolTestRequest,
  RunPromptToolTestResponse,
  PromptToolTestCaseResult,
  PublishAnnouncementRequest,
  ReplaceIdentityProviderRequest,
  RuleSet,
  RuleSetWrite,
  NotificationRule,
  NotificationRulePreview,
  NotificationRuleWrite,
  SettingsImpactAssessment,
  SettingsRevision,
  SettingsValue,
  Task,
  UpdateDatasourceDiagnosticsConfigRequest,
  UpdateUserRequest,
  User,
  UserDeletionImpact,
  AiProviderRateVersion,
  CreateAiProviderRateVersionRequest,
  AiCallUsage,
  AiUsageSummary,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Server-state hooks for the review-admin management surfaces (frontend PRD
 * F9 / work package FE-F9-REVIEW-ADMIN). Every hook consumes a declared
 * OpenAPI operation; mutations carry If-Match (row version). Admin-domain
 * objects change rarely — no live event feed exists for them, so mutations
 * invalidate their list/read keys directly. The Idempotency-Key header is
 * not declared for admin operations, so none is sent (migration contract
 * §16.6 records the header-vs-profile drift).
 */

const PAGE_LIMIT = 200;

function pageItems<T>(data: { items?: T[]; page?: { has_more: boolean } }): T[] {
  return data.items ?? [];
}

function ifMatch(version: number | undefined): Record<string, string> {
  // The frozen OpenAPI declares the Idempotency-Key header only on
  // order/query-domain mutations (profile-vs-header drift recorded in the
  // migration contract §16.6) — admin replacements carry If-Match only.
  return {
    "If-Match": `"${String(version ?? 1)}"`,
  };
}

// ---- datasources ---------------------------------------------------------

export function useDatasources(enabled: boolean, paging?: {
  limit?: number;
  after?: string;
  q?: string;
  /** RCP-20261105 per-column filters: all are narrowing conditions, ANDed together and combined with q. */
  engine?: "mysql" | "postgresql" | "tidb" | "oceanbase" | "polardb";
  compatibilityMode?: "mysql" | "postgresql";
  credentialMode?: "read_write" | "read_only";
  referenced?: boolean;
}) {
  return useQuery({
    queryKey: [
      "admin", "datasources", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? "",
      paging?.engine ?? "", paging?.compatibilityMode ?? "", paging?.credentialMode ?? "",
      paging?.referenced === undefined ? "" : String(paging.referenced),
    ],
    queryFn: async () => {
      const response = await listDatasources({
        limit: paging?.limit ?? PAGE_LIMIT,
        after: paging?.after || undefined,
        q: paging?.q || undefined,
        engine: paging?.engine,
        compatibility_mode: paging?.compatibilityMode,
        credential_mode: paging?.credentialMode,
        referenced: paging?.referenced,
      });
      return response as unknown as { items: Datasource[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useDatasourceCapabilities(datasourceId: string, probeKey: string, enabled: boolean) {
  return useQuery({
    // The probe key (the running/finished connection-test task id) makes a
    // re-test fetch fresh facts instead of replaying the previous ones —
    // the query only polls while the probe has not materialized data yet.
    queryKey: ["admin", "datasources", datasourceId, "capabilities", probeKey],
    queryFn: async () => {
      try {
        const response = await getDatasourceCapabilities(datasourceId);
        return response as unknown as DatasourceCapabilities;
      } catch (error) {
        // A missing probe (1002 before the first connection test) is the
        // empty state of this view, not a failure — the query keeps
        // polling until a probe materializes the capabilities.
        if (error instanceof BusinessError && error.err_code === businessErrCodeByName("RESOURCE_NOT_FOUND")) {
          return null;
        }
        throw error;
      }
    },
    enabled: enabled && datasourceId !== "",
    retry: false,
    refetchInterval: (queryInstance) => (queryInstance.state.data === null ? 800 : false),
  });
}

export function useCreateDatasource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (write: DatasourceWrite) => {
      const response = await createDatasource(write);
      return response as unknown as Datasource;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "datasources"] });
    },
  });
}

export function useReplaceDatasource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version, write }: { id: string; version: number; write: DatasourceWrite }) => {
      const response = await replaceDatasource(id, write, { headers: ifMatch(version) });
      return response as unknown as Datasource;
    },
    onSuccess: (updated) => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "datasources"] });
      void queryClient.invalidateQueries({ queryKey: ["admin", "datasources", updated.id] });
    },
  });
}

export function useDeleteDatasource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteDatasource(id, { headers: ifMatch(version) });
      return true;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "datasources"] });
    },
  });
}

export function useTestDatasourceConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, purpose }: { id: string; purpose: "review" | "query" | "execution" }) => {
      const response = await testDatasourceConnection(id, { purpose });
      return response as unknown as Task;
    },
    onSuccess: (task) => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "tasks", task.id] });
    },
  });
}

export function useDatasourceDiagnosticsConfig(datasourceId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "datasources", datasourceId, "diagnostics-config"],
    queryFn: async () => {
      const response = await getDatasourceDiagnosticsConfig(datasourceId);
      return response as unknown as DatasourceDiagnosticsConfig;
    },
    enabled: enabled && datasourceId !== "",
  });
}

export function useUpdateDatasourceDiagnosticsConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      datasourceId: string;
      version: number;
      body: UpdateDatasourceDiagnosticsConfigRequest;
    }) => {
      const response = await updateDatasourceDiagnosticsConfig(input.datasourceId, input.body, {
        headers: ifMatch(input.version),
      });
      return response as unknown as DatasourceDiagnosticsConfig;
    },
    onSuccess: (_data, variables) => {
      void queryClient.invalidateQueries({
        queryKey: ["admin", "datasources", variables.datasourceId, "diagnostics-config"],
      });
      void queryClient.invalidateQueries({ queryKey: ["admin", "diagnostics"] });
    },
  });
}

export function useAdminTask(taskId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "tasks", taskId],
    queryFn: async () => {
      const response = await getTask(taskId);
      return response as unknown as Task;
    },
    enabled: enabled && taskId !== "",
    // Terminal task states stop the polling; the capability refetch happens
    // through the succeeded-task effect on the page.
    refetchInterval: (queryInstance) => {
      const state = queryInstance.state.data?.state;
      return state === "queued" || state === "running" ? 500 : false;
    },
  });
}

// ---- ai providers ---------------------------------------------------------

export function useAiProviders(
  enabled: boolean,
  q?: string,
  paging?: { limit?: number; after?: string },
) {
  return useQuery({
    queryKey: ["admin", "ai-providers", q ?? "", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      // §18.82: server-side cursor paging (default 20/page) — the hook
      // returns the full paged envelope for the table footer.
      const response = (await listAiProviders({
        q: q || undefined,
        limit: paging?.limit,
        after: paging?.after || undefined,
      })) as unknown as {
        items?: AiProvider[];
        page?: { has_more: boolean; next_cursor: string | null };
      };
      // Ordered by selection_priority ascending so index 0 is the primary
      // provider (backend provider/chain.go:3).
      return {
        items: pageItems<AiProvider>(response),
        page: response.page ?? { has_more: false, next_cursor: null },
      };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useCreateAiProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (write: CreateAiProviderRequest) => {
      const response = await createAiProvider(write);
      return response as unknown as AiProvider;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-providers"] });
    },
  });
}

export function useReplaceAiProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version, write }: { id: string; version: number; write: AiProviderWrite }) => {
      const response = await replaceAiProvider(id, write, { headers: ifMatch(version) });
      return response as unknown as AiProvider;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-providers"] });
    },
  });
}

export function useDeleteAiProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteAiProvider(id, { headers: ifMatch(version) });
      return true;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-providers"] });
    },
  });
}

export function useTestAiProviderConnection() {
  return useMutation({
    mutationFn: async ({ id }: { id: string }) => {
      const response = await testAiProviderConnection(id);
      // RCP-20261017 #019: synchronous probe result {ok} — no longer a Task, no polling;
      // failures use the business error member (2011 + upstream refusal detail).
      return response as unknown as { ok: boolean };
    },
  });
}

// ---- settings --------------------------------------------------------------

export function useSettingsNamespace(namespace: "general" | "query" | "execution" | "ai-budget" | "branding", enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "settings", namespace],
    queryFn: async () => {
      const response = await getSettingsNamespace(namespace);
      const result = response as unknown as {
        namespace: string;
        schema_version: number;
        settings: SettingsValue;
        version: number;
        updated_by: string;
        updated_at: string;
      };
      if (
        namespace === "general" &&
        typeof (result.settings as { system_timezone?: unknown } | undefined)?.system_timezone === "string"
      ) {
        setSystemTimezone((result.settings as { system_timezone: string }).system_timezone);
      }
      return result;
    },
    enabled,
  });
}

export function useSettingsRevisions(
  namespace: "general" | "query" | "execution" | "ai-budget" | "branding",
  enabled: boolean,
  paging?: { limit?: number; after?: string },
) {
  return useQuery({
    queryKey: ["admin", "settings", namespace, "revisions", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      // §18.82: server-side cursor paging (default 20/page).
      const response = await listSettingsRevisions(namespace, {
        limit: paging?.limit,
        after: paging?.after || undefined,
      });
      return {
        items: pageItems<SettingsRevision>(response as unknown as { items?: SettingsRevision[] }),
        page:
          (response as unknown as { page?: { has_more: boolean; next_cursor: string | null } })
            .page ?? { has_more: false, next_cursor: null },
      };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useAssessSettingsImpact() {
  return useMutation({
    mutationFn: async ({
      namespace,
      settings,
    }: {
      namespace: "general" | "query" | "execution" | "ai-budget" | "branding";
      settings: SettingsValue;
    }) => {
      const response = await assessSettingsImpact(namespace, { settings });
      return response as unknown as SettingsImpactAssessment;
    },
  });
}

export function useReplaceSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      namespace,
      version,
      settings,
      impactToken,
    }: {
      namespace: "general" | "query" | "execution" | "ai-budget" | "branding";
      version: number;
      settings: SettingsValue;
      impactToken: string | null;
    }) => {
      const response = await replaceSettingsNamespace(
        namespace,
        { settings, impact_token: impactToken },
        { headers: ifMatch(version) },
      );
      return response as unknown as { version: number; settings: SettingsValue };
    },
    onSuccess: (_data, variables) => {
      if (
        variables.namespace === "general" &&
        typeof (variables.settings as { system_timezone?: unknown }).system_timezone === "string"
      ) {
        setSystemTimezone((variables.settings as { system_timezone: string }).system_timezone);
      }
      void queryClient.invalidateQueries({ queryKey: ["admin", "settings", variables.namespace] });
      void queryClient.invalidateQueries({ queryKey: ["admin", "settings", variables.namespace, "revisions"] });
      void queryClient.invalidateQueries({ queryKey: ["auth", "session"] });
    },
  });
}

// ---- prompt tools ----------------------------------------------------------

export function usePromptTools(enabled: boolean, paging?: { limit?: number; after?: string; q?: string }) {
  return useQuery({
    queryKey: ["admin", "prompt-tools", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? ""],
    queryFn: async () => {
      const response = await listPromptTools({ limit: paging?.limit ?? PAGE_LIMIT, after: paging?.after || undefined, q: paging?.q || undefined });
      return response as unknown as { items: PromptTool[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useCreatePromptTool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (write: PromptToolWrite) => {
      const response = await createPromptTool(write);
      return response as unknown as PromptTool;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "prompt-tools"] });
    },
  });
}

export function useReplacePromptTool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version, write }: { id: string; version: number; write: PromptToolWrite }) => {
      const response = await replacePromptTool(id, write, { headers: ifMatch(version) });
      return response as unknown as PromptTool;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "prompt-tools"] });
    },
  });
}

export function useDeletePromptTool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deletePromptTool(id, { headers: ifMatch(version) });
      return true;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "prompt-tools"] });
      void queryClient.invalidateQueries({ queryKey: ["admin", "rule-sets"] });
    },
  });
}

export function useOptimizePromptToolPrompt() {
  return useMutation({
    mutationFn: async (request: OptimizePromptToolPromptRequest) => {
      const response = await optimizePromptToolPrompt(request);
      return response as unknown as OptimizePromptToolPromptResponse;
    },
  });
}

export function useGeneratePromptToolTestCases() {
  return useMutation({
    mutationFn: async (request: GeneratePromptToolTestCasesRequest) => {
      const response = await generatePromptToolTestCases(request);
      return response as unknown as GeneratePromptToolTestCasesResponse;
    },
  });
}

export function useRunPromptToolTest() {
  return useMutation({
    mutationFn: async (request: RunPromptToolTestRequest) => {
      const response = await runPromptToolTest(request);
      return response as unknown as RunPromptToolTestResponse;
    },
  });
}

export type { PromptToolTestCase, PromptToolTestCaseResult };

// ---- knowledge entries ------------------------------------------------------

export function useKnowledgeEntries(enabled: boolean, paging?: { limit?: number; after?: string; q?: string }) {
  return useQuery({
    queryKey: ["admin", "knowledge-entries", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? ""],
    queryFn: async () => {
      const response = await listKnowledgeEntries({ limit: paging?.limit ?? PAGE_LIMIT, after: paging?.after || undefined, q: paging?.q || undefined });
      return response as unknown as { items: KnowledgeEntry[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useCreateKnowledgeEntry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (write: KnowledgeEntryWrite) => {
      const response = await createKnowledgeEntry(write);
      return response as unknown as KnowledgeEntry;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "knowledge-entries"] });
    },
  });
}

export function useReplaceKnowledgeEntry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version, write }: { id: string; version: number; write: KnowledgeEntryWrite }) => {
      const response = await replaceKnowledgeEntry(id, write, { headers: ifMatch(version) });
      return response as unknown as KnowledgeEntry;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "knowledge-entries"] });
    },
  });
}

export function useDeleteKnowledgeEntry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteKnowledgeEntry(id, { headers: ifMatch(version) });
      return true;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "knowledge-entries"] });
    },
  });
}

export function useEvaluateKnowledgeEntry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id }: { id: string }) => {
      const response = await evaluateKnowledgeEntry(id);
      return response as unknown as KnowledgeEntryEvaluation;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "knowledge-entries"] });
    },
  });
}

// ---- rule sets ---------------------------------------------------------------

export function useRuleSets(enabled: boolean, paging?: { limit?: number; after?: string; q?: string }) {
  return useQuery({
    queryKey: ["admin", "rule-sets", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? ""],
    queryFn: async () => {
      const response = await listRuleSets({ limit: paging?.limit ?? PAGE_LIMIT, after: paging?.after || undefined, q: paging?.q || undefined });
      return response as unknown as { items: RuleSet[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useCreateRuleSet() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (write: RuleSetWrite) => {
      const response = await createRuleSet(write);
      return response as unknown as RuleSet;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "rule-sets"] });
    },
  });
}

export function useReplaceRuleSet() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version, write }: { id: string; version: number; write: RuleSetWrite }) => {
      const response = await replaceRuleSet(id, write, { headers: ifMatch(version) });
      return response as unknown as RuleSet;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "rule-sets"] });
    },
  });
}

export function useDeleteRuleSet() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteRuleSet(id, { headers: ifMatch(version) });
      return true;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "rule-sets"] });
    },
  });
}

/**
 * Flow list read for the rule-set impact preview: which change flows bind a
 * rule set (FlowWrite.rule_set_id). The binding itself happens on the flow
 * editor (F10) — this page only renders the reverse visibility.
 */
export function useFlowsForRuleSetImpact(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "flows", "rule-set-impact"],
    queryFn: async () => {
      const response = await listFlows({ limit: PAGE_LIMIT });
      return pageItems<{
        id: string;
        name: string;
        flow_type: string;
        enabled: boolean;
        rule_set_id: string | null;
      }>(response as unknown as { items?: Array<{ id: string; name: string; flow_type: string; enabled: boolean; rule_set_id: string | null }> });
    },
    enabled,
  });
}


// ===========================================================================
// FE-F10 site domains: users, permission groups, flows, masking rules,
// announcements, audit events, identity providers, notification channels
// and migration review runs. Mutations carry If-Match; the admin guard is
// the server's can_access_admin.
// ===========================================================================

// ---- users ---------------------------------------------------------------

export function useUsers(enabled: boolean, paging?: {
  limit?: number;
  after?: string;
  q?: string;
  /** RCP-20261105 per-column filter: kind narrows by account type, ANDed with q. */
  kind?: "builtin_admin" | "local_user";
}) {
  return useQuery({
    queryKey: ["admin", "users", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? "", paging?.kind ?? ""],
    queryFn: async () => {
      const response = await listUsers({
        limit: paging?.limit ?? PAGE_LIMIT,
        after: paging?.after || undefined,
        q: paging?.q || undefined,
        kind: paging?.kind,
      });
      return response as unknown as { items: User[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useCreateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateUserRequest) => {
      return (await createUser(body)) as unknown as User;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
  });
}

export function useUpdateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string; version: number; body: UpdateUserRequest }) => {
      return (await updateUser(input.userId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as User;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
  });
}

export function useDeleteUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string; version: number }) => {
      await deleteUser(input.userId, { headers: ifMatch(input.version) });
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
  });
}

export function useDeletionImpact(userId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "users", userId, "deletion-impact"],
    queryFn: async () => {
      return (await getUserDeletionImpact(userId)) as unknown as UserDeletionImpact;
    },
    enabled: enabled && userId !== "",
  });
}

// ---- permission groups ---------------------------------------------------

export function usePermissionGroups(enabled: boolean, paging?: {
  limit?: number;
  after?: string;
  q?: string;
  /** RCP-20261105 per-column filters: enabled / member / granted flow, all narrowing and ANDed together. */
  enabled?: boolean;
  memberUserId?: string;
  flowId?: string;
}) {
  return useQuery({
    queryKey: [
      "admin", "permission-groups", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? "",
      paging?.enabled === undefined ? "" : String(paging.enabled), paging?.memberUserId ?? "", paging?.flowId ?? "",
    ],
    queryFn: async () => {
      const response = await listPermissionGroups({
        limit: paging?.limit ?? PAGE_LIMIT,
        after: paging?.after || undefined,
        q: paging?.q || undefined,
        enabled: paging?.enabled,
        member_user_id: paging?.memberUserId,
        flow_id: paging?.flowId,
      });
      return response as unknown as { items: PermissionGroup[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useCreatePermissionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: PermissionGroupWrite) => {
      return (await createPermissionGroup(body)) as unknown as PermissionGroup;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "permission-groups"] });
    },
  });
}

export function useReplacePermissionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { groupId: string; version: number; body: PermissionGroupWrite }) => {
      return (await replacePermissionGroup(input.groupId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as PermissionGroup;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "permission-groups"] });
    },
  });
}

export function useDeletePermissionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { groupId: string; version: number }) => {
      await deletePermissionGroup(input.groupId, { headers: ifMatch(input.version) });
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "permission-groups"] });
    },
  });
}

// ---- flows (full model + masking rules) -----------------------------------

export function useFlows(enabled: boolean, paging?: {
  limit?: number;
  after?: string;
  q?: string;
  /** RCP-20261105 per-column filters: flow_type / enabled narrowing, ANDed with q. */
  flowType?: "change_review" | "query_access";
  enabled?: boolean;
}) {
  return useQuery({
    queryKey: [
      "admin", "flows", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? "",
      paging?.flowType ?? "", paging?.enabled === undefined ? "" : String(paging.enabled),
    ],
    queryFn: async () => {
      const response = await listFlows({
        limit: paging?.limit ?? PAGE_LIMIT,
        after: paging?.after || undefined,
        q: paging?.q || undefined,
        flow_type: paging?.flowType,
        enabled: paging?.enabled,
      });
      return response as unknown as { items: Flow[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    placeholderData: keepPreviousData,
    enabled,
  });
}
export function useCreateFlow() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: FlowWrite) => {
      return (await createFlow(body)) as unknown as Flow;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "flows"] });
    },
  });
}

export function useReplaceFlow() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { flowId: string; version: number; body: FlowWrite }) => {
      return (await replaceFlow(input.flowId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as Flow;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "flows"] });
    },
  });
}

export function useDeleteFlow() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { flowId: string; version: number }) => {
      await deleteFlow(input.flowId, { headers: ifMatch(input.version) });
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "flows"] });
    },
  });
}

export function useFlowMaskingRules(flowId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "flows", flowId, "masking-rules"],
    queryFn: async () => {
      // Contract data is an {items} envelope rather than a bare array; the previous cast crashed .map in the edit dialog on the real backend.
      const response = (await listFlowMaskingRules(flowId)) as unknown as { items?: FlowMaskingRule[] };
      return response.items ?? [];
    },
    enabled: enabled && flowId !== "",
  });
}

export function useReplaceMaskingRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      flowId: string;
      datasourceId: string;
      body: FlowMaskingRuleWrite;
    }) => {
      return (await replaceFlowMaskingRule(input.flowId, input.datasourceId, input.body));
    },
    onSettled: (_data, error, variables) => {
      if (error === null) {
        void queryClient.invalidateQueries({
          queryKey: ["admin", "flows", variables.flowId, "masking-rules"],
        });
      }
    },
  });
}

// ---- announcements ---------------------------------------------------------

export function useAnnouncementRevisions(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "announcements", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      const response = await listAnnouncementRevisions({ limit: paging?.limit ?? PAGE_LIMIT, after: paging?.after || undefined });
      return response as unknown as { items: AnnouncementRevision[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
}
export function useCreateAnnouncementRevision() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: { title: string; markdown_source: string }) => {
      return (await createAnnouncementRevision(body)) as unknown as AnnouncementRevision;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "announcements"] });
    },
  });
}

export function usePublishAnnouncement() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { body: PublishAnnouncementRequest; publicationVersion: number }) => {
      return (await publishAnnouncementRevision(input.body, {
        headers: ifMatch(input.publicationVersion),
      })) as unknown as AnnouncementRevision;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "announcements"] });
      void queryClient.invalidateQueries({ queryKey: ["announcements", "current"] });
    },
  });
}

// ---- audit events -----------------------------------------------------------

export function useAuditEvents(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "audit-events"],
    queryFn: async () => {
      const response = await listAuditEvents({ limit: PAGE_LIMIT });
      return pageItems<AuditEvent>(response as unknown as { items?: AuditEvent[] });
    },
    enabled,
  });
}

// ---- identity providers ------------------------------------------------------

export function useIdentityProviders(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "identity-providers", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      const response = await listIdentityProviders({ limit: paging?.limit ?? PAGE_LIMIT, after: paging?.after || undefined });
      return response as unknown as { items: IdentityProvider[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
}
export function useCreateIdentityProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateIdentityProviderRequest) => {
      return (await createIdentityProvider(body)) as unknown as IdentityProvider;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "identity-providers"] });
    },
  });
}

export function useReplaceIdentityProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { providerId: string; version: number; body: ReplaceIdentityProviderRequest }) => {
      return (await replaceIdentityProvider(input.providerId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as IdentityProvider;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "identity-providers"] });
    },
  });
}

export function useDeleteIdentityProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { providerId: string; version: number }) => {
      await deleteIdentityProvider(input.providerId, { headers: ifMatch(input.version) });
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "identity-providers"] });
    },
  });
}

export function useTestIdentityProviderConnection() {
  return useMutation({
    mutationFn: async (providerId: string) => {
      return (await testIdentityProviderConnection(providerId)) as unknown as Task;
    },
  });
}

// ---- notification channels ----------------------------------------------------

export function useNotificationChannels(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "notification-channels", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      const response = await listNotificationChannels({ limit: paging?.limit ?? PAGE_LIMIT, after: paging?.after || undefined });
      return response as unknown as { items: NotificationChannel[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
}
export function useNotificationDeliveries(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "notification-deliveries", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      const response = await listNotificationDeliveries({ limit: paging?.limit ?? PAGE_LIMIT, after: paging?.after || undefined });
      return response as unknown as { items: NotificationDelivery[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
}
export function useNotificationRules(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "notification-rules"],
    queryFn: async () => {
      const response = await listNotificationRules({ limit: PAGE_LIMIT });
      return response as unknown as { items: NotificationRule[]; page: { has_more: boolean; next_cursor: string | null } };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
}
export function useCreateNotificationRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: NotificationRuleWrite) => {
      return (await createNotificationRule(body)) as unknown as NotificationRule;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-rules"] });
    },
  });
}
export function useReplaceNotificationRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { ruleId: string; version: number; body: NotificationRuleWrite }) => {
      return (await replaceNotificationRule(input.ruleId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as NotificationRule;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-rules"] });
    },
  });
}
export function useDeleteNotificationRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { ruleId: string; version: number }) => {
      return (await deleteNotificationRule(input.ruleId, {
        headers: ifMatch(input.version),
      })) as unknown as null;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-rules"] });
    },
  });
}
export function usePreviewNotificationRule() {
  return useMutation({
    mutationFn: async (body: { rule: NotificationRuleWrite; sample_order_id: string | null }) => {
      return (await previewNotificationRule({ rule: body.rule, sample_order_id: body.sample_order_id })) as unknown as NotificationRulePreview;
    },
  });
}
export function useCreateNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: CreateNotificationChannelRequest) => {
      return (await createNotificationChannel(body)) as unknown as NotificationChannel;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-channels"] });
    },
  });
}

export function useReplaceNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { channelId: string; version: number; body: NotificationChannelWrite }) => {
      return (await replaceNotificationChannel(input.channelId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as NotificationChannel;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-channels"] });
    },
  });
}

export function useDeleteNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { channelId: string; version: number }) => {
      await deleteNotificationChannel(input.channelId, { headers: ifMatch(input.version) });
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-channels"] });
    },
  });
}

export function useTestNotificationDelivery() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { channelId: string; body: unknown }) => {
      const { createNotificationTestDelivery } = await import(
        "@/api/generated/client/administration/administration"
      );
      // Acceptance receipt (task_id + channel_id) — test delivery is an async task,
      // result polled via GET /tasks/{task_id} (2026-10-31 round 3).
      return (await createNotificationTestDelivery(input.channelId, input.body as never)) as unknown as {
        task_id: string;
        channel_id: string;
      };
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-deliveries"] });
    },
  });
}

// ---- S008 Generation Parameters / Reference Pricing (FE package 2026-10-31) -----------------

export function useAiProviderRateVersions(providerId: string | null, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "ai-provider-rate-versions", providerId],
    queryFn: async () => {
      const { listAiProviderRateVersions } = await import(
        "@/api/generated/client/administration/administration"
      );
      const response = await listAiProviderRateVersions(providerId as string);
      return pageItems<AiProviderRateVersion>(
        response as unknown as { items?: AiProviderRateVersion[] },
      );
    },
    enabled: enabled && providerId !== null,
    placeholderData: keepPreviousData,
  });
}

export function useCreateAiProviderRateVersion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      providerId: string;
      body: CreateAiProviderRateVersionRequest;
    }) => {
      const { createAiProviderRateVersion } = await import(
        "@/api/generated/client/administration/administration"
      );
      await createAiProviderRateVersion(input.providerId, input.body);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-provider-rate-versions"] });
    },
  });
}

/** S008 usage call ledger. Contract filter params run_id/currency+provider_id
 * (RCP-20261102-S008)+limit(<=200)+after cursor: pagination and provider filtering both
 * take effect server-side (page.has_more = honest boundary of this filtered stream; dropping the page envelope
 * would silently hide overflow rows). */
export function useAiUsageCalls(
  enabled: boolean,
  filter?: { providerId?: string; limit?: number; after?: string },
) {
  return useQuery({
    queryKey: ["admin", "ai-usage", "calls", filter?.providerId ?? "", filter?.after ?? "", filter?.limit ?? 0],
    queryFn: async () => {
      const { listAiUsageCalls } = await import(
        "@/api/generated/client/administration/administration"
      );
      const response = await listAiUsageCalls({
        provider_id: filter?.providerId || undefined,
        limit: filter?.limit,
        after: filter?.after || undefined,
      });
      return response as unknown as {
        items: AiCallUsage[];
        page: { has_more: boolean; next_cursor: string | null };
      };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
}

/** Test delivery task polling (owner 2026-10-31 round 3: page must show whether delivery succeeded).
 * The real backend records test delivery as an async_task (queued/running -> succeeded/failed),
 * and the page polls every 1.5s until terminal state; error/loading states are rendered by the caller via pending. */
export function useNotificationTestTask(taskId: string | null) {
  return useQuery({
    queryKey: ["admin", "notification-test-task", taskId],
    queryFn: async () => {
      const { getTask } = await import("@/api/generated/client/tasks/tasks");
      const task = (await getTask(taskId as string)) as unknown as {
        id: string;
        kind: string;
        state: "queued" | "running" | "succeeded" | "failed" | "cancelled";
        error: { title?: string } | null;
      };
      return task;
    },
    enabled: taskId !== null,
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state === undefined || state === "queued" || state === "running" ? 1500 : false;
    },
  });
}

/** S008 reference pricing daily summary (consumed by AI Budget usage ring chart). reference_amount is a
 * fixed-point decimal string in major currency units (OpenAPI: bounded decimal in currency units),
 * not nano units; callers present it directly in major units without floating-point scaling. */
export function useAiUsageSummaryToday(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "ai-usage", "summary", "today"],
    queryFn: async () => {
      const { customInstance } = await import("@/shared/api/mutator");
      // Today window: local midnight <= sent < now (daily 00:00 reset in system timezone).
      const start = new Date();
      start.setHours(0, 0, 0, 0);
      const from = start.toISOString();
      const to = new Date().toISOString();
      return customInstance<AiUsageSummary[]>(
        `/api/v4/admin/ai-usage/summary?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
      );
    },
    enabled,
  });
}

// ---- migration review ----------------------------------------------------------

export function useMigrationRuns(enabled: boolean, paging?: { limit?: number; after?: string }) {
  const query = useQuery({
    queryKey: ["admin", "migrations", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () => {
      // §18.82: server-side cursor paging (default 20/page).
      const response = await listLegacyMigrationRuns({
        limit: paging?.limit,
        after: paging?.after || undefined,
      });
      return {
        items: pageItems<LegacyMigrationRun>(
          response as unknown as { items?: LegacyMigrationRun[] },
        ),
        page:
          (response as unknown as { page?: { has_more: boolean; next_cursor: string | null } })
            .page ?? { has_more: false, next_cursor: null },
      };
    },
    enabled,
    placeholderData: keepPreviousData,
  });
  return { ...query, data: query.data ?? { items: [], page: { has_more: false, next_cursor: null } } };
}

// Empty-page fallback: wrapper guarantees data is always defined (empty page while pending),
// so consumers do not need null checks on data (§18.82).

export function useMigrationRun(runId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "migrations", runId],
    queryFn: async () => {
      return (await getLegacyMigrationRun(runId)) as unknown as LegacyMigrationRun;
    },
    enabled: enabled && runId !== "",
  });
}

export function useConfirmMigrationCandidate() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      runId: string;
      candidateId: string;
      version: number;
      body: ConfirmLegacyMigrationCandidateRequest;
    }) => {
      return (await confirmLegacyMigrationCandidate(input.runId, input.candidateId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as LegacyMigrationCandidate;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "migrations"] });
    },
  });
}

export function useApproveMigrationRun() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      runId: string;
      version: number;
      body: { manifest_hash: string; confirmation_phrase: string };
    }) => {
      return (await approveLegacyMigrationRun(input.runId, input.body, {
        headers: ifMatch(input.version),
      })) as unknown as LegacyMigrationRun;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "migrations"] });
    },
  });
}
