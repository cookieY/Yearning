import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  approveLegacyMigrationRun,
  assessSettingsImpact,
  confirmLegacyMigrationCandidate,
  createAnnouncementRevision,
  createFlow,
  createNotificationChannel,
  createNotificationRule,
  createNotificationTestDelivery,
  deleteFlow,
  deleteNotificationChannel,
  deleteNotificationRule,
  getSettingsNamespace,
  listAnnouncementRevisions,
  listFlowMaskingRules,
  listFlows,
  listLegacyMigrationRuns,
  listNotificationChannels,
  listNotificationDeliveries,
  listNotificationRules,
  listSettingsRevisions,
  previewNotificationRule,
  publishAnnouncementRevision,
  replaceFlow,
  replaceFlowMaskingRule,
  replaceNotificationChannel,
  replaceNotificationRule,
  replaceSettingsNamespace,
} from "@/api/generated/client/administration/administration";
import { getTask } from "@/api/generated/client/tasks/tasks";
import type {
  ConfirmLegacyMigrationCandidateRequest,
  CreateNotificationChannelRequest,
  FlowMaskingRuleWrite,
  FlowWrite,
  NotificationChannelWrite,
  NotificationRuleWrite,
  NotificationTestRequest,
  PublishAnnouncementRequest,
  SettingsValue,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { PAGE_LIMIT, pageItems, useResourceMutation } from "@/features/admin/use-admin-mutation";
import { ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import { EMPTY_PAGE, normalizePage } from "@/shared/lib/cursor-page";
import { setPublicBranding } from "@/shared/lib/branding";
import { setSystemTimezone } from "@/shared/lib/timezones";

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
      } else if (namespace === "branding") {
        setPublicBranding(result.settings as { product_name?: string; login_description?: string } | undefined);
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
    onSuccess: (data, variables) => {
      queryClient.setQueryData(["admin", "settings", variables.namespace], data);
      if (
        variables.namespace === "general" &&
        typeof (variables.settings as { system_timezone?: unknown }).system_timezone === "string"
      ) {
        setSystemTimezone((variables.settings as { system_timezone: string }).system_timezone);
      } else if (variables.namespace === "branding") {
        setPublicBranding(variables.settings as { product_name?: string; login_description?: string });
        void queryClient.invalidateQueries({ queryKey: ["auth", "providers"] });
      }
      void queryClient.invalidateQueries({ queryKey: ["admin", "settings", variables.namespace] });
      void queryClient.invalidateQueries({ queryKey: ["admin", "settings", variables.namespace, "revisions"] });
      void queryClient.invalidateQueries({ queryKey: ["auth", "session"] });
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
  return useResourceMutation({
    mutationFn: (body: FlowWrite) => unwrapApi(createFlow(body)),
    invalidateKeys: [["admin", "flows"]],
    when: "settled",
  });
}

export function useReplaceFlow() {
  return useResourceMutation({
    mutationFn: (input: { flowId: string; version: number; body: FlowWrite }) =>
      unwrapApi(replaceFlow(input.flowId, input.body, ifMatchHeader(input.version))),
    invalidateKeys: [["admin", "flows"]],
    when: "settled",
  });
}

export function useDeleteFlow() {
  return useResourceMutation({
    mutationFn: async (input: { flowId: string; version: number }) => {
      await deleteFlow(input.flowId, ifMatchHeader(input.version));
    },
    invalidateKeys: [["admin", "flows"]],
    when: "settled",
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
  return useResourceMutation({
    mutationFn: (input: {
      flowId: string;
      datasourceId: string;
      body: FlowMaskingRuleWrite;
    }) => unwrapApi(replaceFlowMaskingRule(input.flowId, input.datasourceId, input.body)),
    invalidateKeys: (_data, variables) => [["admin", "flows", variables.flowId, "masking-rules"]],
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
  return useResourceMutation({
    mutationFn: (body: { title: string; markdown_source: string }) =>
      unwrapApi(createAnnouncementRevision(body)),
    invalidateKeys: [["admin", "announcements"]],
    when: "settled",
  });
}

export function usePublishAnnouncement() {
  return useResourceMutation({
    mutationFn: (input: { body: PublishAnnouncementRequest; publicationVersion: number }) =>
      unwrapApi(publishAnnouncementRevision(input.body, ifMatchHeader(input.publicationVersion))),
    invalidateKeys: [
      ["admin", "announcements"],
      ["announcements", "current"],
    ],
    when: "settled",
  });
}

// ---- notification channels & rules -----------------------------------------

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
  return useResourceMutation({
    mutationFn: (body: NotificationRuleWrite) => unwrapApi(createNotificationRule(body)),
    invalidateKeys: [["admin", "notification-rules"]],
    when: "settled",
  });
}

export function useReplaceNotificationRule() {
  return useResourceMutation({
    mutationFn: (input: { ruleId: string; version: number; body: NotificationRuleWrite }) =>
      unwrapApi(replaceNotificationRule(input.ruleId, input.body, ifMatchHeader(input.version))),
    invalidateKeys: [["admin", "notification-rules"]],
    when: "settled",
  });
}

export function useDeleteNotificationRule() {
  return useResourceMutation({
    mutationFn: (input: { ruleId: string; version: number }) =>
      unwrapApi(deleteNotificationRule(input.ruleId, ifMatchHeader(input.version))),
    invalidateKeys: [["admin", "notification-rules"]],
    when: "settled",
  });
}

export function usePreviewNotificationRule() {
  return useMutation({
    mutationFn: (body: { rule: NotificationRuleWrite; sample_order_id: string | null }) =>
      unwrapApi(previewNotificationRule({ rule: body.rule, sample_order_id: body.sample_order_id })),
  });
}

export function useCreateNotificationChannel() {
  return useResourceMutation({
    mutationFn: (body: CreateNotificationChannelRequest) => unwrapApi(createNotificationChannel(body)),
    invalidateKeys: [["admin", "notification-channels"]],
    when: "settled",
  });
}

export function useReplaceNotificationChannel() {
  return useResourceMutation({
    mutationFn: (input: { channelId: string; version: number; body: NotificationChannelWrite }) =>
      unwrapApi(replaceNotificationChannel(input.channelId, input.body, ifMatchHeader(input.version))),
    invalidateKeys: [["admin", "notification-channels"]],
    when: "settled",
  });
}

export function useDeleteNotificationChannel() {
  return useResourceMutation({
    mutationFn: async (input: { channelId: string; version: number }) => {
      await deleteNotificationChannel(input.channelId, ifMatchHeader(input.version));
    },
    invalidateKeys: [["admin", "notification-channels"]],
    when: "settled",
  });
}

export function useTestNotificationDelivery() {
  return useResourceMutation({
    mutationFn: (input: { channelId: string; body: NotificationTestRequest }) =>
      // Acceptance receipt (task_id + channel_id) — test delivery is an async task,
      // result polled via GET /tasks/{task_id} (2026-10-31 round 3).
      unwrapApi(createNotificationTestDelivery(input.channelId, input.body)),
    invalidateKeys: [["admin", "notification-deliveries"]],
    when: "settled",
  });
}

/** Test delivery task polling (owner 2026-10-31 round 3). */
export function useNotificationTestTask(taskId: string | null) {
  return useQuery({
    queryKey: ["admin", "notification-test-task", taskId],
    queryFn: () => unwrapApi(getTask(taskId as string)),
    enabled: taskId !== null,
    refetchInterval: (query) => {
      if (query.state.status === "error") return false;
      const state = query.state.data?.state;
      return state === undefined || state === "queued" || state === "running" ? 1500 : false;
    },
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
  return { ...query, data: query.data ?? EMPTY_PAGE };
}

export function useConfirmMigrationCandidate() {
  return useResourceMutation({
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
    invalidateKeys: [["admin", "migrations"]],
    when: "settled",
  });
}

export function useApproveMigrationRun() {
  return useResourceMutation({
    mutationFn: (input: {
      runId: string;
      version: number;
      body: { manifest_hash: string; confirmation_phrase: string };
    }) =>
      unwrapApi(
        approveLegacyMigrationRun(input.runId, input.body, ifMatchHeader(input.version)),
      ),
    invalidateKeys: [["admin", "migrations"]],
    when: "settled",
  });
}
