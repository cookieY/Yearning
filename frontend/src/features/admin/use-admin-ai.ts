import { keepPreviousData, useMutation, useQuery } from "@tanstack/react-query";
import {
  createAiProvider,
  createAiProviderRateVersion,
  createPromptTool,
  deleteAiProvider,
  deletePromptTool,
  generatePromptToolTestCases,
  listAiProviderRateVersions,
  listAiProviders,
  listAiUsageCalls,
  listAiUsageSummary,
  listPromptTools,
  optimizePromptToolPrompt,
  replaceAiProvider,
  replacePromptTool,
  runPromptToolTest,
  testAiProviderConnection,
} from "@/api/generated/client/administration/administration";
import type {
  AiProviderRateVersion,
  AiProviderWrite,
  CreateAiProviderRateVersionRequest,
  CreateAiProviderRequest,
  PromptToolTestCase,
  PromptToolTestCaseResult,
  PromptToolWrite,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { PAGE_LIMIT, pageItems, useResourceMutation } from "@/features/admin/use-admin-mutation";
import { ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import { normalizePage } from "@/shared/lib/cursor-page";

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
  return useResourceMutation({
    mutationFn: (write: CreateAiProviderRequest) => unwrapApi(createAiProvider(write)),
    invalidateKeys: [["admin", "ai-providers"]],
  });
}

export function useReplaceAiProvider() {
  return useResourceMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: AiProviderWrite }) =>
      unwrapApi(replaceAiProvider(id, write, ifMatchHeader(version))),
    invalidateKeys: [["admin", "ai-providers"]],
  });
}

export function useDeleteAiProvider() {
  return useResourceMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteAiProvider(id, ifMatchHeader(version));
      return true;
    },
    invalidateKeys: [["admin", "ai-providers"]],
  });
}

export function useTestAiProviderConnection() {
  return useMutation({
    mutationFn: ({ id }: { id: string }) =>
      // RCP-20261017 #019: synchronous probe result {ok} — failures use business error 2011.
      unwrapApi(testAiProviderConnection(id)),
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
  return useResourceMutation({
    mutationFn: (write: PromptToolWrite) => unwrapApi(createPromptTool(write)),
    invalidateKeys: [["admin", "prompt-tools"]],
  });
}

export function useReplacePromptTool() {
  return useResourceMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: PromptToolWrite }) =>
      unwrapApi(replacePromptTool(id, write, ifMatchHeader(version))),
    invalidateKeys: [["admin", "prompt-tools"]],
  });
}

export function useDeletePromptTool() {
  return useResourceMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deletePromptTool(id, ifMatchHeader(version));
      return true;
    },
    invalidateKeys: [
      ["admin", "prompt-tools"],
      ["admin", "rule-sets"],
    ],
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
  return useResourceMutation({
    mutationFn: async (input: {
      providerId: string;
      body: CreateAiProviderRateVersionRequest;
    }) => {
      await createAiProviderRateVersion(input.providerId, input.body);
    },
    invalidateKeys: [["admin", "ai-provider-rate-versions"]],
    when: "settled",
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
