import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  confirmDiagnosticOrderLink,
  diagnoseStatementWithAi,
  getDiagnosticStatementDetail,
  getDiagnosticsOverview,
  listDiagnosticDatasources,
  precheckDiagnosticOptimizationDraft,
  submitDiagnosticOptimizationOrder,
  triggerManualDiagnosticsSnapshot,
} from "@/api/generated/client/administration/administration";
import { listCurrentUserFlows } from "@/api/generated/client/change-drafts/change-drafts";
import type {
  ConfirmDiagnosticOrderLinkRequest,
  DiagnoseStatementWithAiRequest,
  DiagnosticAiReport,
  DiagnosticDatasourceSummary,
  DiagnosticOrderLink,
  DiagnosticStatementDetail,
  DiagnosticsOverview,
  Flow,
  GetDiagnosticsOverviewParams,
  PrecheckDiagnosticOptimizationDraftRequest,
  PrecheckDiagnosticOptimizationDraftResponse,
  SubmitDiagnosticOptimizationOrderRequest,
  SubmitDiagnosticOptimizationOrderResponse,
  TriggerManualDiagnosticsSnapshotResponse,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { randomUUID } from "@/shared/lib/uuid";

export function useDiagnosticDatasources(enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "diagnostics", "datasources"],
    queryFn: async () => {
      const response = (await listDiagnosticDatasources()) as unknown as {
        items?: DiagnosticDatasourceSummary[];
      };
      return response.items ?? [];
    },
    enabled,
  });
}

export function useDiagnosticsOverview(
  datasourceId: string,
  params: GetDiagnosticsOverviewParams & { rangeMinutes?: number },
  enabled: boolean,
) {
  const { rangeMinutes = 60, ...apiParams } = params;
  return useQuery({
    queryKey: [
      "admin",
      "diagnostics",
      datasourceId,
      "overview",
      rangeMinutes,
      apiParams.from ?? "",
      apiParams.to ?? "",
      apiParams.compare_from ?? "",
      apiParams.compare_to ?? "",
      apiParams.sort_by ?? "total_exec_time_ms",
      apiParams.limit ?? 200,
    ],
    queryFn: async () => {
      const effectiveParams: GetDiagnosticsOverviewParams = { ...apiParams };
      if (!effectiveParams.from && !effectiveParams.to && rangeMinutes !== 60) {
        const now = Date.now();
        effectiveParams.to = new Date(now).toISOString();
        effectiveParams.from = new Date(now - rangeMinutes * 60_000).toISOString();
      }
      const response = await getDiagnosticsOverview(datasourceId, effectiveParams);
      return response as unknown as DiagnosticsOverview;
    },
    placeholderData: keepPreviousData,
    enabled: enabled && datasourceId !== "",
  });
}

export function useTriggerManualDiagnosticsSnapshot() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (datasourceId: string) => {
      const response = await triggerManualDiagnosticsSnapshot(datasourceId);
      return response as unknown as TriggerManualDiagnosticsSnapshotResponse;
    },
    onSuccess: (_data, datasourceId) => {
      void queryClient.invalidateQueries({
        queryKey: ["admin", "diagnostics", datasourceId],
      });
      void queryClient.invalidateQueries({
        queryKey: ["admin", "diagnostics", "datasources"],
      });
    },
  });
}

export function useDiagnosticStatementDetail(
  datasourceId: string,
  statementId: string | null,
  params: { from?: string; to?: string },
  enabled: boolean,
) {
  return useQuery({
    queryKey: [
      "admin",
      "diagnostics",
      datasourceId,
      "statements",
      statementId ?? "",
      params.from ?? "",
      params.to ?? "",
    ],
    queryFn: async () => {
      const response = await getDiagnosticStatementDetail(
        datasourceId,
        statementId ?? "",
        params,
      );
      return response as unknown as DiagnosticStatementDetail;
    },
    enabled: enabled && datasourceId !== "" && statementId !== null && statementId !== "",
  });
}

export function useConfirmDiagnosticOrderLink() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      datasourceId: string;
      statementId: string;
      body: ConfirmDiagnosticOrderLinkRequest;
    }) => {
      const response = await confirmDiagnosticOrderLink(
        input.datasourceId,
        input.statementId,
        input.body,
      );
      return response as unknown as DiagnosticOrderLink;
    },
    onSuccess: (_data, variables) => {
      void queryClient.invalidateQueries({
        queryKey: ["admin", "diagnostics", variables.datasourceId, "statements", variables.statementId],
      });
    },
  });
}

export function useDiagnoseStatementWithAi() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      datasourceId: string;
      statementId: string;
      body?: DiagnoseStatementWithAiRequest;
    }) => {
      const response = await diagnoseStatementWithAi(
        input.datasourceId,
        input.statementId,
        input.body ?? {},
      );
      return response as unknown as DiagnosticAiReport;
    },
    onSuccess: (_data, variables) => {
      void queryClient.invalidateQueries({
        queryKey: ["admin", "diagnostics", variables.datasourceId, "statements", variables.statementId],
      });
    },
  });
}

export function usePrecheckDiagnosticOptimizationDraft() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      datasourceId: string;
      statementId: string;
      reportId: string;
      body: PrecheckDiagnosticOptimizationDraftRequest;
    }) => {
      const response = await precheckDiagnosticOptimizationDraft(
        input.datasourceId,
        input.reportId,
        input.body,
        {
          headers: {
            "Idempotency-Key": `diag-precheck-${randomUUID()}`,
          },
        },
      );
      return response as unknown as PrecheckDiagnosticOptimizationDraftResponse;
    },
    onSuccess: (_data, variables) => {
      void queryClient.invalidateQueries({
        queryKey: ["admin", "diagnostics", variables.datasourceId, "statements", variables.statementId],
      });
    },
  });
}

export function useSubmitDiagnosticOptimizationOrder() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      datasourceId: string;
      statementId: string;
      reportId: string;
      body: SubmitDiagnosticOptimizationOrderRequest;
    }) => {
      const response = await submitDiagnosticOptimizationOrder(
        input.datasourceId,
        input.reportId,
        input.body,
        {
          headers: {
            "Idempotency-Key": `diag-order-${randomUUID()}`,
          },
        },
      );
      return response as unknown as SubmitDiagnosticOptimizationOrderResponse;
    },
    onSuccess: (_data, variables) => {
      void queryClient.invalidateQueries({
        queryKey: ["admin", "diagnostics", variables.datasourceId, "statements", variables.statementId],
      });
      void queryClient.invalidateQueries({ queryKey: ["change-orders"] });
    },
  });
}

export function useDiagnosticChangeFlows(enabled: boolean) {
  return useQuery({
    queryKey: ["current-user-flows", "diagnostics-change-review"],
    queryFn: async () => {
      const page = (await listCurrentUserFlows({ flow_type: "change_review" })) as unknown as {
        items?: Flow[];
      };
      return page.items ?? [];
    },
    enabled,
  });
}
