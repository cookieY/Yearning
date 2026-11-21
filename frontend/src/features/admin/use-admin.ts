import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  approveLegacyMigrationRun,
  assessSettingsImpact,
  confirmLegacyMigrationCandidate,
  createAiProvider,
  createAiProviderRateVersion,
  createAnnouncementRevision,
  createDatasource,
  createFlow,
  createIdentityProvider,
  createKnowledgeEntry,
  createNotificationChannel,
  createNotificationRule,
  createNotificationTestDelivery,
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
  deleteNotificationRule,
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
  listAiProviderRateVersions,
  listAiProviders,
  listAiUsageCalls,
  listAiUsageSummary,
  listAnnouncementRevisions,
  listDatasources,
  listFlowMaskingRules,
  listFlows,
  listIdentityProviders,
  listKnowledgeEntries,
  listLegacyMigrationRuns,
  listNotificationChannels,
  listNotificationDeliveries,
  listNotificationRules,
  listPermissionGroups,
  listPromptTools,
  listRuleSets,
  listSettingsRevisions,
  listUsers,
  optimizePromptToolPrompt,
  previewNotificationRule,
  publishAnnouncementRevision,
  replaceAiProvider,
  replaceDatasource,
  replaceFlow,
  replaceFlowMaskingRule,
  replaceIdentityProvider,
  replaceKnowledgeEntry,
  replaceNotificationChannel,
  replaceNotificationRule,
  replacePermissionGroup,
  replacePromptTool,
  replaceRuleSet,
  replaceSettingsNamespace,
  runPromptToolTest,
  testAiProviderConnection,
  testDatasourceConnection,
  testIdentityProviderConnection,
  updateDatasourceDiagnosticsConfig,
  updateUser,
} from "@/api/generated/client/administration/administration";
import { getTask } from "@/api/generated/client/tasks/tasks";
import type {
  AiProviderRateVersion,
  AiProviderWrite,
  ConfirmLegacyMigrationCandidateRequest,
  CreateAiProviderRateVersionRequest,
  CreateAiProviderRequest,
  CreateIdentityProviderRequest,
  CreateNotificationChannelRequest,
  CreateUserRequest,
  DatasourceWrite,
  FlowMaskingRuleWrite,
  FlowWrite,
  KnowledgeEntryWrite,
  NotificationChannelWrite,
  NotificationRuleWrite,
  NotificationTestRequest,
  PermissionGroupWrite,
  PromptToolTestCase,
  PromptToolTestCaseResult,
  PromptToolWrite,
  PublishAnnouncementRequest,
  ReplaceIdentityProviderRequest,
  RuleSetWrite,
  SettingsValue,
  UpdateDatasourceDiagnosticsConfigRequest,
  UpdateUserRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { businessErrCodeByName } from "@/shared/api/error-display";
import { BusinessError, ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import { setSystemTimezone } from "@/shared/lib/timezones";

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

function pageItems<T>(data: { items?: T[] } | undefined | null): T[] {
  return data?.items ?? [];
}

function normalizePage<T>(
  data: { items?: T[]; page?: { has_more?: boolean; next_cursor?: string | null } } | undefined | null,
): { items: T[]; page: { has_more: boolean; next_cursor: string | null } } {
  return {
    items: data?.items ?? [],
    page: {
      has_more: data?.page?.has_more ?? false,
      next_cursor: data?.page?.next_cursor ?? null,
    },
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
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listDatasources({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
            engine: paging?.engine,
            compatibility_mode: paging?.compatibilityMode,
            credential_mode: paging?.credentialMode,
            referenced: paging?.referenced,
          }),
        ),
      ),
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
        return await unwrapApi(getDatasourceCapabilities(datasourceId));
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
    mutationFn: (write: DatasourceWrite) => unwrapApi(createDatasource(write)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "datasources"] });
    },
  });
}

export function useReplaceDatasource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: DatasourceWrite }) =>
      unwrapApi(replaceDatasource(id, write, ifMatchHeader(version))),
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
      await deleteDatasource(id, ifMatchHeader(version));
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
    mutationFn: ({ id, purpose }: { id: string; purpose: "review" | "query" | "execution" }) =>
      unwrapApi(testDatasourceConnection(id, { purpose })),
    onSuccess: (task) => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "tasks", task.id] });
    },
  });
}

export function useDatasourceDiagnosticsConfig(datasourceId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "datasources", datasourceId, "diagnostics-config"],
    queryFn: () => unwrapApi(getDatasourceDiagnosticsConfig(datasourceId)),
    enabled: enabled && datasourceId !== "",
  });
}

export function useUpdateDatasourceDiagnosticsConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      datasourceId: string;
      version: number;
      body: UpdateDatasourceDiagnosticsConfigRequest;
    }) =>
      unwrapApi(updateDatasourceDiagnosticsConfig(input.datasourceId, input.body, ifMatchHeader(input.version))),
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
    queryFn: () => unwrapApi(getTask(taskId)),
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
    queryFn: async () =>
      // §18.82: server-side cursor paging (default 20/page); ordered by selection_priority ascending.
      normalizePage(
        await unwrapApi(
          listAiProviders({
            q: q || undefined,
            limit: paging?.limit,
            after: paging?.after || undefined,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreateAiProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (write: CreateAiProviderRequest) => unwrapApi(createAiProvider(write)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-providers"] });
    },
  });
}

export function useReplaceAiProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: AiProviderWrite }) =>
      unwrapApi(replaceAiProvider(id, write, ifMatchHeader(version))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-providers"] });
    },
  });
}

export function useDeleteAiProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteAiProvider(id, ifMatchHeader(version));
      return true;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-providers"] });
    },
  });
}

export function useTestAiProviderConnection() {
  return useMutation({
    mutationFn: ({ id }: { id: string }) =>
      // RCP-20261017 #019: synchronous probe result {ok} — failures use business error 2011.
      unwrapApi(testAiProviderConnection(id)),
  });
}

// ---- settings --------------------------------------------------------------

export function useSettingsNamespace(namespace: "general" | "query" | "execution" | "ai-budget" | "branding", enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "settings", namespace],
    queryFn: async () => {
      const result = await unwrapApi(getSettingsNamespace(namespace));
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
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listSettingsRevisions(namespace, {
            limit: paging?.limit,
            after: paging?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useAssessSettingsImpact() {
  return useMutation({
    mutationFn: ({
      namespace,
      settings,
    }: {
      namespace: "general" | "query" | "execution" | "ai-budget" | "branding";
      settings: SettingsValue;
    }) => unwrapApi(assessSettingsImpact(namespace, { settings })),
  });
}

export function useReplaceSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      namespace,
      version,
      settings,
      impactToken,
    }: {
      namespace: "general" | "query" | "execution" | "ai-budget" | "branding";
      version: number;
      settings: SettingsValue;
      impactToken: string | null;
    }) =>
      unwrapApi(
        replaceSettingsNamespace(
          namespace,
          { settings, impact_token: impactToken },
          ifMatchHeader(version),
        ),
      ),
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
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listPromptTools({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreatePromptTool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (write: PromptToolWrite) => unwrapApi(createPromptTool(write)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "prompt-tools"] });
    },
  });
}

export function useReplacePromptTool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: PromptToolWrite }) =>
      unwrapApi(replacePromptTool(id, write, ifMatchHeader(version))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "prompt-tools"] });
    },
  });
}

export function useDeletePromptTool() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deletePromptTool(id, ifMatchHeader(version));
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
    mutationFn: (request: Parameters<typeof optimizePromptToolPrompt>[0]) =>
      unwrapApi(optimizePromptToolPrompt(request)),
  });
}

export function useGeneratePromptToolTestCases() {
  return useMutation({
    mutationFn: (request: Parameters<typeof generatePromptToolTestCases>[0]) =>
      unwrapApi(generatePromptToolTestCases(request)),
  });
}

export function useRunPromptToolTest() {
  return useMutation({
    mutationFn: (request: Parameters<typeof runPromptToolTest>[0]) =>
      unwrapApi(runPromptToolTest(request)),
  });
}

export type { PromptToolTestCase, PromptToolTestCaseResult };

// ---- knowledge entries ------------------------------------------------------

export function useKnowledgeEntries(enabled: boolean, paging?: { limit?: number; after?: string; q?: string }) {
  return useQuery({
    queryKey: ["admin", "knowledge-entries", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? ""],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listKnowledgeEntries({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreateKnowledgeEntry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (write: KnowledgeEntryWrite) => unwrapApi(createKnowledgeEntry(write)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "knowledge-entries"] });
    },
  });
}

export function useReplaceKnowledgeEntry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: KnowledgeEntryWrite }) =>
      unwrapApi(replaceKnowledgeEntry(id, write, ifMatchHeader(version))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "knowledge-entries"] });
    },
  });
}

export function useDeleteKnowledgeEntry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteKnowledgeEntry(id, ifMatchHeader(version));
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
    mutationFn: ({ id }: { id: string }) => unwrapApi(evaluateKnowledgeEntry(id)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "knowledge-entries"] });
    },
  });
}

// ---- rule sets ---------------------------------------------------------------

export function useRuleSets(enabled: boolean, paging?: { limit?: number; after?: string; q?: string }) {
  return useQuery({
    queryKey: ["admin", "rule-sets", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? ""],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listRuleSets({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreateRuleSet() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (write: RuleSetWrite) => unwrapApi(createRuleSet(write)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "rule-sets"] });
    },
  });
}

export function useReplaceRuleSet() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: RuleSetWrite }) =>
      unwrapApi(replaceRuleSet(id, write, ifMatchHeader(version))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "rule-sets"] });
    },
  });
}

export function useDeleteRuleSet() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteRuleSet(id, ifMatchHeader(version));
      return true;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "rule-sets"] });
    },
  });
}

/**
 * Flow list read for the rule-set impact preview: which change flows bind a
 * rule set (FlowWrite.rule_set_id).
 */
export function useFlowsForRuleSetImpact(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "flows", "rule-set-impact"],
    queryFn: async () => pageItems(await unwrapApi(listFlows({ limit: PAGE_LIMIT }))),
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
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listUsers({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
            kind: paging?.kind,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateUserRequest) => unwrapApi(createUser(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
  });
}

export function useUpdateUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { userId: string; version: number; body: UpdateUserRequest }) =>
      unwrapApi(updateUser(input.userId, input.body, ifMatchHeader(input.version))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
  });
}

export function useDeleteUser() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { userId: string; version: number }) => {
      await deleteUser(input.userId, ifMatchHeader(input.version));
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "users"] });
    },
  });
}

export function useDeletionImpact(userId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "users", userId, "deletion-impact"],
    queryFn: () => unwrapApi(getUserDeletionImpact(userId)),
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
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listPermissionGroups({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
            enabled: paging?.enabled,
            member_user_id: paging?.memberUserId,
            flow_id: paging?.flowId,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreatePermissionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: PermissionGroupWrite) => unwrapApi(createPermissionGroup(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "permission-groups"] });
    },
  });
}

export function useReplacePermissionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { groupId: string; version: number; body: PermissionGroupWrite }) =>
      unwrapApi(replacePermissionGroup(input.groupId, input.body, ifMatchHeader(input.version))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "permission-groups"] });
    },
  });
}

export function useDeletePermissionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { groupId: string; version: number }) => {
      await deletePermissionGroup(input.groupId, ifMatchHeader(input.version));
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
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listFlows({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
            flow_type: paging?.flowType,
            enabled: paging?.enabled,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreateFlow() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: FlowWrite) => unwrapApi(createFlow(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "flows"] });
    },
  });
}

export function useReplaceFlow() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { flowId: string; version: number; body: FlowWrite }) =>
      unwrapApi(replaceFlow(input.flowId, input.body, ifMatchHeader(input.version))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "flows"] });
    },
  });
}

export function useDeleteFlow() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { flowId: string; version: number }) => {
      await deleteFlow(input.flowId, ifMatchHeader(input.version));
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "flows"] });
    },
  });
}

export function useFlowMaskingRules(flowId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "flows", flowId, "masking-rules"],
    queryFn: async () => pageItems(await unwrapApi(listFlowMaskingRules(flowId))),
    enabled: enabled && flowId !== "",
  });
}

export function useReplaceMaskingRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      flowId: string;
      datasourceId: string;
      body: FlowMaskingRuleWrite;
    }) => unwrapApi(replaceFlowMaskingRule(input.flowId, input.datasourceId, input.body)),
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
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listAnnouncementRevisions({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useCreateAnnouncementRevision() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { title: string; markdown_source: string }) =>
      unwrapApi(createAnnouncementRevision(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "announcements"] });
    },
  });
}

export function usePublishAnnouncement() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { body: PublishAnnouncementRequest; publicationVersion: number }) =>
      unwrapApi(publishAnnouncementRevision(input.body, ifMatchHeader(input.publicationVersion))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "announcements"] });
      void queryClient.invalidateQueries({ queryKey: ["announcements", "current"] });
    },
  });
}

// ---- identity providers ------------------------------------------------------

export function useIdentityProviders(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "identity-providers", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listIdentityProviders({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useCreateIdentityProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateIdentityProviderRequest) => unwrapApi(createIdentityProvider(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "identity-providers"] });
    },
  });
}

export function useReplaceIdentityProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { providerId: string; version: number; body: ReplaceIdentityProviderRequest }) =>
      unwrapApi(replaceIdentityProvider(input.providerId, input.body, ifMatchHeader(input.version))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "identity-providers"] });
    },
  });
}

export function useDeleteIdentityProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { providerId: string; version: number }) => {
      await deleteIdentityProvider(input.providerId, ifMatchHeader(input.version));
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "identity-providers"] });
    },
  });
}

export function useTestIdentityProviderConnection() {
  return useMutation({
    mutationFn: (providerId: string) => unwrapApi(testIdentityProviderConnection(providerId)),
  });
}

// ---- notification channels ----------------------------------------------------

export function useNotificationChannels(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "notification-channels", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listNotificationChannels({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useNotificationDeliveries(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "notification-deliveries", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listNotificationDeliveries({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useNotificationRules(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "notification-rules"],
    queryFn: async () => normalizePage(await unwrapApi(listNotificationRules({ limit: PAGE_LIMIT }))),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useCreateNotificationRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: NotificationRuleWrite) => unwrapApi(createNotificationRule(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-rules"] });
    },
  });
}

export function useReplaceNotificationRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { ruleId: string; version: number; body: NotificationRuleWrite }) =>
      unwrapApi(replaceNotificationRule(input.ruleId, input.body, ifMatchHeader(input.version))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-rules"] });
    },
  });
}

export function useDeleteNotificationRule() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { ruleId: string; version: number }) =>
      unwrapApi(deleteNotificationRule(input.ruleId, ifMatchHeader(input.version))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-rules"] });
    },
  });
}

export function usePreviewNotificationRule() {
  return useMutation({
    mutationFn: (body: { rule: NotificationRuleWrite; sample_order_id: string | null }) =>
      unwrapApi(previewNotificationRule({ rule: body.rule, sample_order_id: body.sample_order_id })),
  });
}

export function useCreateNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateNotificationChannelRequest) => unwrapApi(createNotificationChannel(body)),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-channels"] });
    },
  });
}

export function useReplaceNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { channelId: string; version: number; body: NotificationChannelWrite }) =>
      unwrapApi(replaceNotificationChannel(input.channelId, input.body, ifMatchHeader(input.version))),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-channels"] });
    },
  });
}

export function useDeleteNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { channelId: string; version: number }) => {
      await deleteNotificationChannel(input.channelId, ifMatchHeader(input.version));
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "notification-channels"] });
    },
  });
}

export function useTestNotificationDelivery() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { channelId: string; body: NotificationTestRequest }) =>
      // Acceptance receipt (task_id + channel_id) — test delivery is an async task,
      // result polled via GET /tasks/{task_id} (2026-10-31 round 3).
      unwrapApi(createNotificationTestDelivery(input.channelId, input.body)),
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
      const response = await unwrapApi(listAiProviderRateVersions(providerId as string));
      return Array.isArray(response)
        ? response
        : pageItems<AiProviderRateVersion>(response);
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
      await createAiProviderRateVersion(input.providerId, input.body);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "ai-provider-rate-versions"] });
    },
  });
}

/** S008 usage call ledger. Contract filter params run_id/currency+provider_id
 * (RCP-20261102-S008)+limit(<=200)+after cursor: pagination and provider filtering both
 * take effect server-side. */
export function useAiUsageCalls(
  enabled: boolean,
  filter?: { providerId?: string; limit?: number; after?: string },
) {
  return useQuery({
    queryKey: ["admin", "ai-usage", "calls", filter?.providerId ?? "", filter?.after ?? "", filter?.limit ?? 0],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listAiUsageCalls({
            provider_id: filter?.providerId || undefined,
            limit: filter?.limit,
            after: filter?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
}

/** Test delivery task polling (owner 2026-10-31 round 3). */
export function useNotificationTestTask(taskId: string | null) {
  return useQuery({
    queryKey: ["admin", "notification-test-task", taskId],
    queryFn: () => unwrapApi(getTask(taskId as string)),
    enabled: taskId !== null,
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state === undefined || state === "queued" || state === "running" ? 1500 : false;
    },
  });
}

/** S008 reference pricing daily summary (consumed by AI Budget usage ring chart). */
export function useAiUsageSummaryToday(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "ai-usage", "summary", "today"],
    queryFn: () => {
      // Today window: local midnight <= sent < now (daily 00:00 reset in system timezone).
      const start = new Date();
      start.setHours(0, 0, 0, 0);
      return unwrapApi(
        listAiUsageSummary({
          from: start.toISOString(),
          to: new Date().toISOString(),
        }),
      );
    },
    enabled,
  });
}

// ---- migration review ----------------------------------------------------------

export function useMigrationRuns(enabled: boolean, paging?: { limit?: number; after?: string }) {
  const query = useQuery({
    queryKey: ["admin", "migrations", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listLegacyMigrationRuns({
            limit: paging?.limit,
            after: paging?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
  return { ...query, data: query.data ?? { items: [], page: { has_more: false, next_cursor: null } } };
}

export function useConfirmMigrationCandidate() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      runId: string;
      candidateId: string;
      version: number;
      body: ConfirmLegacyMigrationCandidateRequest;
    }) =>
      unwrapApi(
        confirmLegacyMigrationCandidate(
          input.runId,
          input.candidateId,
          input.body,
          ifMatchHeader(input.version),
        ),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "migrations"] });
    },
  });
}

export function useApproveMigrationRun() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      runId: string;
      version: number;
      body: { manifest_hash: string; confirmation_phrase: string };
    }) =>
      unwrapApi(
        approveLegacyMigrationRun(input.runId, input.body, ifMatchHeader(input.version)),
      ),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin", "migrations"] });
    },
  });
}
