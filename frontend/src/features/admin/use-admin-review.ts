import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
  createKnowledgeEntry,
  createRuleSet,
  deleteKnowledgeEntry,
  deleteRuleSet,
  evaluateKnowledgeEntry,
  listFlows,
  listKnowledgeEntries,
  listRuleSets,
  replaceKnowledgeEntry,
  replaceRuleSet,
} from "@/api/generated/client/administration/administration";
import type {
  KnowledgeEntryWrite,
  RuleSetWrite,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { PAGE_LIMIT, pageItems, useResourceMutation } from "@/features/admin/use-admin-mutation";
import { ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import { normalizePage } from "@/shared/lib/cursor-page";

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
  return useResourceMutation({
    mutationFn: (write: KnowledgeEntryWrite) => unwrapApi(createKnowledgeEntry(write)),
    invalidateKeys: [["admin", "knowledge-entries"]],
  });
}

export function useReplaceKnowledgeEntry() {
  return useResourceMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: KnowledgeEntryWrite }) =>
      unwrapApi(replaceKnowledgeEntry(id, write, ifMatchHeader(version))),
    invalidateKeys: [["admin", "knowledge-entries"]],
  });
}

export function useDeleteKnowledgeEntry() {
  return useResourceMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteKnowledgeEntry(id, ifMatchHeader(version));
      return true;
    },
    invalidateKeys: [["admin", "knowledge-entries"]],
  });
}

export function useEvaluateKnowledgeEntry() {
  return useResourceMutation({
    mutationFn: ({ id }: { id: string }) => unwrapApi(evaluateKnowledgeEntry(id)),
    invalidateKeys: [["admin", "knowledge-entries"]],
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
  return useResourceMutation({
    mutationFn: (write: RuleSetWrite) => unwrapApi(createRuleSet(write)),
    invalidateKeys: [["admin", "rule-sets"]],
  });
}

export function useReplaceRuleSet() {
  return useResourceMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: RuleSetWrite }) =>
      unwrapApi(replaceRuleSet(id, write, ifMatchHeader(version))),
    invalidateKeys: [["admin", "rule-sets"]],
  });
}

export function useDeleteRuleSet() {
  return useResourceMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteRuleSet(id, ifMatchHeader(version));
      return true;
    },
    invalidateKeys: [["admin", "rule-sets"]],
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
