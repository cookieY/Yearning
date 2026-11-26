import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
  createDatasource,
  deleteDatasource,
  getDatasourceCapabilities,
  getDatasourceDiagnosticsConfig,
  listDatasources,
  replaceDatasource,
  testDatasourceConnection,
  updateDatasourceDiagnosticsConfig,
} from "@/api/generated/client/administration/administration";
import { getTask } from "@/api/generated/client/tasks/tasks";
import type {
  DatasourceWrite,
  UpdateDatasourceDiagnosticsConfigRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { PAGE_LIMIT, useResourceMutation } from "@/features/admin/use-admin-mutation";
import { isBusinessErrorCode } from "@/shared/api/error-display";
import { ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import { normalizePage } from "@/shared/lib/cursor-page";

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
        if (isBusinessErrorCode(error, "RESOURCE_NOT_FOUND")) {
          return null;
        }
        throw error;
      }
    },
    enabled: enabled && datasourceId !== "",
    retry: false,
    refetchInterval: (queryInstance) =>
      queryInstance.state.status === "error" ? false : queryInstance.state.data === null ? 800 : false,
  });
}

export function useCreateDatasource() {
  return useResourceMutation({
    mutationFn: (write: DatasourceWrite) => unwrapApi(createDatasource(write)),
    invalidateKeys: [["admin", "datasources"]],
  });
}

export function useReplaceDatasource() {
  return useResourceMutation({
    mutationFn: ({ id, version, write }: { id: string; version: number; write: DatasourceWrite }) =>
      unwrapApi(replaceDatasource(id, write, ifMatchHeader(version))),
    invalidateKeys: (updated) => [
      ["admin", "datasources"],
      ["admin", "datasources", updated.id],
    ],
  });
}

export function useDeleteDatasource() {
  return useResourceMutation({
    mutationFn: async ({ id, version }: { id: string; version: number }) => {
      await deleteDatasource(id, ifMatchHeader(version));
      return true;
    },
    invalidateKeys: [["admin", "datasources"]],
  });
}

export function useTestDatasourceConnection() {
  return useResourceMutation({
    mutationFn: ({ id, purpose }: { id: string; purpose: "review" | "query" | "execution" }) =>
      unwrapApi(testDatasourceConnection(id, { purpose })),
    invalidateKeys: (task) => [["admin", "tasks", task.id]],
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
  return useResourceMutation({
    mutationFn: (input: {
      datasourceId: string;
      version: number;
      body: UpdateDatasourceDiagnosticsConfigRequest;
    }) =>
      unwrapApi(updateDatasourceDiagnosticsConfig(input.datasourceId, input.body, ifMatchHeader(input.version))),
    invalidateKeys: (_data, variables) => [
      ["admin", "datasources", variables.datasourceId, "diagnostics-config"],
      ["admin", "diagnostics"],
    ],
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
      if (queryInstance.state.status === "error") return false;
      const state = queryInstance.state.data?.state;
      return state === "queued" || state === "running" ? 500 : false;
    },
  });
}
