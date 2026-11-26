import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import {
  Activity,
  Camera,
  TableProperties,
} from "lucide-react";
import type {
  DiagnosticsStatementSummary,
  GetDiagnosticsOverviewSortBy,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { DiagnosticStatementDetailPanel } from "@/features/diagnostics/diagnostic-statement-detail";
import { DiagnosticsOverviewCharts } from "@/features/diagnostics/diagnostics-overview-charts";
import {
  useDiagnosticDatasources,
  useDiagnosticStatementDetail,
  useDiagnosticsOverview,
  useTriggerManualDiagnosticsSnapshot,
} from "@/features/diagnostics/use-diagnostics";
import { DiagnosticsStatementsCard } from "@/routes/admin/admin-diagnostics-statements-card";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { formatTimestamp } from "@/shared/lib/timezones";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Card,
  CardContent,
} from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

type TimeRangePreset = "15m" | "1h" | "6h" | "24h" | "7d";

const PRESET_MINUTES: Record<TimeRangePreset, number> = {
  "15m": 15,
  "1h": 60,
  "6h": 360,
  "24h": 1440,
  "7d": 10080,
};

export default function AdminDiagnosticsPage() {
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const [searchParams, setSearchParams] = useSearchParams();

  const datasourcesQuery = useDiagnosticDatasources(isAdmin);
  const datasources = useMemo(
    () => datasourcesQuery.data ?? [],
    [datasourcesQuery.data],
  );

  const urlDatasourceId = searchParams.get("datasource_id") ?? "";
  const urlStatementId = searchParams.get("statement_id") ?? "";
  const urlRange = (searchParams.get("range") ?? "1h") as TimeRangePreset;
  const activeRange: TimeRangePreset =
    urlRange in PRESET_MINUTES ? urlRange : "1h";

  const activeDatasourceId = useMemo(() => {
    if (urlDatasourceId && datasources.some((ds) => ds.id === urlDatasourceId)) {
      return urlDatasourceId;
    }
    const readyCollected = datasources.find(
      (ds) =>
        ds.diagnostics_enabled &&
        ds.probe_state === "ready" &&
        ds.last_collected_at !== null,
    );
    if (readyCollected) {
      return readyCollected.id;
    }
    const readyEnabled = datasources.find(
      (ds) => ds.diagnostics_enabled && ds.probe_state === "ready",
    );
    if (readyEnabled) {
      return readyEnabled.id;
    }
    return datasources[0]?.id ?? urlDatasourceId;
  }, [datasources, urlDatasourceId]);

  const datasourceSelectItems = useMemo(
    () =>
      Object.fromEntries(
        datasources.map((ds) => [ds.id, `${ds.name} (${ds.engine})`]),
      ),
    [datasources],
  );

  const timeRangeSelectItems = useMemo<Record<TimeRangePreset, string>>(
    () => ({
      "15m": t("diagnostics.range.15m"),
      "1h": t("diagnostics.range.1h"),
      "6h": t("diagnostics.range.6h"),
      "24h": t("diagnostics.range.24h"),
      "7d": t("diagnostics.range.7d"),
    }),
    [t],
  );

  const [sortBy, setSortBy] =
    useState<GetDiagnosticsOverviewSortBy>("total_exec_time_ms");
  const [showAccessibleTables, setShowAccessibleTables] = useState(false);
  const [brushWindow, setBrushWindow] = useState<{
    startTime: string;
    endTime: string;
  } | null>(null);
  const [snapshotNotice, setSnapshotNotice] = useState<string | null>(null);
  const [snapshotError, setSnapshotError] = useState<string | null>(null);

  const handleSelectStatement = (statementId: string) => {
    updateUrlParams({ statement_id: statementId });
  };

  const overviewQuery = useDiagnosticsOverview(
    activeDatasourceId,
    {
      rangeMinutes: PRESET_MINUTES[activeRange],
      from: brushWindow?.startTime,
      to: brushWindow?.endTime,
      sort_by: sortBy,
      limit: 200,
    },
    isAdmin && activeDatasourceId !== "",
  );

  const detailQuery = useDiagnosticStatementDetail(
    activeDatasourceId,
    urlStatementId || null,
    {
      from: brushWindow?.startTime,
      to: brushWindow?.endTime,
    },
    isAdmin && activeDatasourceId !== "" && urlStatementId !== "",
  );

  const triggerSnapshot = useTriggerManualDiagnosticsSnapshot();

  const updateUrlParams = (patch: {
    datasource_id?: string | null;
    statement_id?: string | null;
    range?: TimeRangePreset | null;
  }) => {
    const next = new URLSearchParams(searchParams);
    if (patch.datasource_id !== undefined) {
      if (patch.datasource_id) next.set("datasource_id", patch.datasource_id);
      else next.delete("datasource_id");
    }
    if (patch.statement_id !== undefined) {
      if (patch.statement_id) next.set("statement_id", patch.statement_id);
      else next.delete("statement_id");
    }
    if (patch.range !== undefined) {
      if (patch.range) next.set("range", patch.range);
      else next.delete("range");
    }
    setSearchParams(next, { replace: true });
  };

  const handleManualSnapshot = async () => {
    if (!activeDatasourceId) return;
    setSnapshotError(null);
    setSnapshotNotice(null);
    try {
      const snap = await triggerSnapshot.mutateAsync(activeDatasourceId);
      const notice = t("diagnostics.snapshotSuccess", {
        count: snap.statement_count,
        durationMs: snap.duration_ms,
      });
      setSnapshotNotice(notice);
      appToast.success(notice);
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, "triggerManualDiagnosticsSnapshot"),
      );
      setSnapshotError(errText);
      appToast.error(errText);
    }
  };

  const sortedStatements = useMemo<DiagnosticsStatementSummary[]>(() => {
    const list = [...(overviewQuery.data?.statements ?? [])];
    list.sort((a, b) => {
      if (sortBy === "avg_exec_time_ms") return b.avg_exec_time_ms - a.avg_exec_time_ms;
      if (sortBy === "calls") return b.calls - a.calls;
      if (sortBy === "rows_examined") {
        return (
          b.rows_examined_or_shared_blks_read - a.rows_examined_or_shared_blks_read
        );
      }
      return b.total_exec_time_ms - a.total_exec_time_ms;
    });
    return list;
  }, [overviewQuery.data?.statements, sortBy]);

  return (
    <PageLayout data-testid="admin-diagnostics-page">
      <PageHeader
        title={t("diagnostics.title")}
        description={t("diagnostics.description")}
        icon={Activity}
      />

      {/* Top Control Header */}
      <Card data-testid="diag-header-card">
        <CardContent className="flex flex-col gap-3 pt-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-3">
              {/* Datasource Switcher */}
              <div className="flex items-center gap-2">
                <span className="text-muted-foreground text-xs font-medium">
                  {t("diagnostics.datasourceLabel")}
                </span>
                <Select
                  items={datasourceSelectItems}
                  value={activeDatasourceId}
                  onValueChange={(next) => {
                    if (next) {
                      setBrushWindow(null);
                      updateUrlParams({
                        datasource_id: next,
                        statement_id: null,
                      });
                    }
                  }}
                >
                  <SelectTrigger
                    className="w-56"
                    data-testid="diag-datasource-select"
                  >
                    <SelectValue placeholder={t("diagnostics.selectDatasource")}>
                      {datasourceSelectItems[activeDatasourceId] ??
                        t("diagnostics.selectDatasource")}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {datasources.map((ds) => (
                      <SelectItem key={ds.id} value={ds.id}>
                        {ds.name} ({ds.engine})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>

              {/* Time Range Selector */}
              <div className="flex items-center gap-2">
                <span className="text-muted-foreground text-xs font-medium">
                  {t("diagnostics.timeRangeLabel")}
                </span>
                <Select
                  items={timeRangeSelectItems}
                  value={activeRange}
                  onValueChange={(next) => {
                    if (next && next in PRESET_MINUTES) {
                      setBrushWindow(null);
                      updateUrlParams({ range: next });
                    }
                  }}
                >
                  <SelectTrigger
                    className="w-36"
                    data-testid="diag-time-range-select"
                  >
                    <SelectValue>{timeRangeSelectItems[activeRange]}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="15m">{t("diagnostics.range.15m")}</SelectItem>
                    <SelectItem value="1h">{t("diagnostics.range.1h")}</SelectItem>
                    <SelectItem value="6h">{t("diagnostics.range.6h")}</SelectItem>
                    <SelectItem value="24h">{t("diagnostics.range.24h")}</SelectItem>
                    <SelectItem value="7d">{t("diagnostics.range.7d")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              {/* Coverage & Status Badges */}
              {overviewQuery.data ? (
                <div className="flex flex-wrap items-center gap-1.5">
                  <Badge variant="secondary" data-testid="diag-coverage-badge">
                    {t(`diagnostics.coverageMode.${overviewQuery.data.coverage_mode}`)}
                  </Badge>
                  <Badge variant="outline" data-testid="diag-probe-badge">
                    {t(`diagnostics.probeState.${overviewQuery.data.probe_state}`)}
                  </Badge>
                  <Badge variant="outline" data-testid="diag-circuit-badge">
                    {t(`diagnostics.circuitState.${overviewQuery.data.state}`)}
                  </Badge>
                  <span
                    className="text-muted-foreground text-xs"
                    data-testid="diag-last-collected"
                  >
                    {t("diagnostics.lastCollectedAt", {
                      time: overviewQuery.data.last_collected_at
                        ? formatTimestamp(overviewQuery.data.last_collected_at)
                        : "—",
                    })}
                  </span>
                </div>
              ) : null}
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setShowAccessibleTables((prev) => !prev);
                }}
                data-testid="diag-toggle-table-metrics"
              >
                <TableProperties className="size-4" />
                {t("diagnostics.toggleAccessibleTables")}
              </Button>
              <PendingButton
                size="sm"
                pending={triggerSnapshot.isPending}
                disabled={!activeDatasourceId}
                onClick={() => void handleManualSnapshot()}
                data-testid="diag-manual-snapshot-btn"
              >
                <Camera className="size-4" />
                {t("diagnostics.manualSnapshotButton")}
              </PendingButton>
            </div>
          </div>

          {snapshotNotice ? (
            <Alert data-testid="diag-snapshot-notice">
              <AlertDescription>{snapshotNotice}</AlertDescription>
            </Alert>
          ) : null}
          {snapshotError ? (
            <Alert variant="destructive" data-testid="diag-snapshot-error">
              <AlertDescription>{snapshotError}</AlertDescription>
            </Alert>
          ) : null}
          {overviewQuery.data && overviewQuery.data.probe_state !== "ready" ? (
            <Alert data-testid="diag-probe-status-banner">
              <AlertTitle>
                {t(`diagnostics.probeState.${overviewQuery.data.probe_state}`)}
              </AlertTitle>
              <AlertDescription>
                {t(`diagnostics.probeHint.${overviewQuery.data.probe_state}`)}
              </AlertDescription>
            </Alert>
          ) : null}
        </CardContent>
      </Card>

      {datasourcesQuery.isPending ? (
        <LoadingState variant="table" columns={6} />
      ) : datasourcesQuery.isError ? (
        <ErrorState
          error={datasourcesQuery.error}
          operationId="listDiagnosticDatasources"
          onRetry={() => void datasourcesQuery.refetch()}
        />
      ) : datasources.length === 0 ? (
        <EmptyState
          title={t("diagnostics.emptyDatasourcesTitle")}
          description={t("diagnostics.emptyDatasourcesDesc")}
          testId="diag-empty-datasources"
        />
      ) : overviewQuery.isPending ? (
        <LoadingState variant="table" columns={6} />
      ) : overviewQuery.isError ? (
        <ErrorState
          error={overviewQuery.error}
          operationId="getDiagnosticsOverview"
          onRetry={() => void overviewQuery.refetch()}
        />
      ) : (
        <>
          {/* Selected SQL Statement Detail Dialog Modal */}
          <Dialog
            open={Boolean(urlStatementId)}
            onOpenChange={(open) => {
              if (!open) {
                updateUrlParams({ statement_id: null });
              }
            }}
          >
            <DialogContent
              showCloseButton={false}
              className="flex h-[88vh] max-h-[88vh] flex-col overflow-hidden p-4 sm:max-w-5xl [&>[data-slot=scroll-area]]:h-full [&>[data-slot=scroll-area]>[data-slot=scroll-area-viewport]]:max-h-[calc(88vh-2rem)]!"
              data-testid="diag-statement-detail-dialog"
            >
              <DialogTitle className="sr-only">
                {t("diagnostics.title")}
              </DialogTitle>
              {detailQuery.isPending ? (
                <LoadingState variant="inline" />
              ) : detailQuery.isError ? (
                <ErrorState
                  error={detailQuery.error}
                  operationId="getDiagnosticStatementDetail"
                  onRetry={() => void detailQuery.refetch()}
                />
              ) : (
                <DiagnosticStatementDetailPanel
                  key={detailQuery.data.statement_id}
                  detail={detailQuery.data}
                  onClose={() => {
                    updateUrlParams({ statement_id: null });
                  }}
                />
              )}
            </DialogContent>
          </Dialog>

          {/* 6 Overview Charts */}
          <DiagnosticsOverviewCharts
            loadTrend={overviewQuery.data.load_trend}
            topStatements={overviewQuery.data.top_statements}
            scatterPoints={overviewQuery.data.scatter_points}
            heatmap={overviewQuery.data.heatmap}
            regressions={overviewQuery.data.regressions}
            newHighLoad={overviewQuery.data.new_high_load}
            statements={overviewQuery.data.statements}
            selectedStatementId={urlStatementId || null}
            showAccessibleTables={showAccessibleTables}
            brushWindow={brushWindow}
            onBrushWindowChange={setBrushWindow}
            onSelectStatement={(statementId) => {
              handleSelectStatement(statementId);
            }}
          />

          <DiagnosticsStatementsCard
            sortedStatements={sortedStatements}
            onSortChange={setSortBy}
            onSelectStatement={handleSelectStatement}
          />
        </>
      )}
    </PageLayout>
  );
}
