import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Ban, Copy, Database, Download, LogOut, Play, ShieldAlert, Table2 } from "lucide-react";
import type { QueryGrant } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { createQueryGrantRenewalRequest } from "@/api/generated/client/query-access/query-access";
import { useSession } from "@/features/auth/session-provider";
import { describeQueryErrorText } from "@/features/query/query-error-text";
import { resolveStatementSchema } from "@/features/query/query-sql-utils";
import {
  CloseSessionDialog,
  ExportDialog,
  RenewGrantDialog,
  TableStructureDialog,
  type ExportContext,
  type InspectTarget,
} from "@/features/query/query-workspace-dialogs";
import { QueryWorkspaceSidebar } from "@/features/query/query-workspace-sidebar";
import { ResultGrid } from "@/features/query/result-grid";
import {
  useCloseSession,
  useMyGrants,
  useQuerySession,
  useSessionColumns,
  useSessionCompletionCatalog,
  useSessionSchemas,
} from "@/features/query/use-query-domain";
import { useQueryExecutionTabs } from "@/features/query/use-query-execution-tabs";
import { SqlEditorPanel } from "@/features/review/sql-editor-panel";
import { describeError } from "@/shared/api/error-display";
import { ContextMenu } from "@/shared/components/context-menu";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { writeClipboardText } from "@/shared/lib/clipboard";
import { LoadingState } from "@/shared/components/status/loading-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent } from "@/shared/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";

/** Query Workspace (route /query/sessions/:id). */
export default function QueryWorkspacePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { sessionId = "" } = useParams();
  const session = useSession();
  const authenticated = session.status === "authenticated";

  const sessionQuery = useQuerySession(sessionId, authenticated);
  const grantsQuery = useMyGrants(authenticated);

  // When a revocation arrives via SSE, refetch grants to load the revocation reason.
  useEffect(() => {
    if (sessionQuery.data?.state === "revoked") {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
    }
  }, [queryClient, sessionQuery.data?.state]);
  const closeSession = useCloseSession();

  const row = sessionQuery.data ?? null;
  const [selectedDatasourceId, setSelectedDatasourceId] = useState<string>("");
  const datasourceId = useMemo(() => {
    if (row === null || row.capabilities.length === 0) return "";
    if (
      selectedDatasourceId !== "" &&
      row.capabilities.some((capability) => capability.datasource_id === selectedDatasourceId)
    ) {
      return selectedDatasourceId;
    }
    return row.capabilities[0]?.datasource_id ?? "";
  }, [row, selectedDatasourceId]);
  const [schemaName, setSchemaName] = useState("");
  const [sql, setSql] = useState("SELECT ");
  const selectedSqlRef = useRef("");
  const [hasSelection, setHasSelection] = useState(false);
  const [loadValue, setLoadValue] = useState<{ text: string; nonce: number } | null>(null);
  const [leftPanel, setLeftPanel] = useState<"tree" | "history">("tree");
  const [closeOpen, setCloseOpen] = useState(false);
  const [closeReason, setCloseReason] = useState("");
  const [exportContext, setExportContext] = useState<ExportContext | null>(null);
  const [renewOpen, setRenewOpen] = useState(false);
  const [renewError, setRenewError] = useState<string | null>(null);
  const [inspectMenu, setInspectMenu] = useState<{
    datasourceId: string;
    schema: string;
    table: string;
    x: number;
    y: number;
  } | null>(null);
  const [inspectTarget, setInspectTarget] = useState<InspectTarget | null>(null);

  const handleSelectionChange = useCallback((nextSelected: string) => {
    selectedSqlRef.current = nextSelected;
    const nextHas = nextSelected.trim() !== "";
    setHasSelection((prev) => (prev === nextHas ? prev : nextHas));
  }, []);

  const capability = useMemo(
    () => row?.capabilities.find((item) => item.datasource_id === datasourceId) ?? null,
    [datasourceId, row],
  );

  const schemasQuery = useSessionSchemas(sessionId, datasourceId, row?.state === "active");
  const schemaNames = useMemo(
    () => (schemasQuery.data ?? []).map((schema) => schema.name),
    [schemasQuery.data],
  );

  const {
    running,
    runError,
    tabs,
    activeTab,
    setActiveTab,
    continuationErrors,
    effectiveHistory,
    run,
    resetTargetAndError,
  } = useQueryExecutionTabs({
    sessionId,
    authenticated,
    row,
    capability,
    datasourceId,
    schemaName,
    sql,
    selectedSqlRef,
  });

  const activeGrant: QueryGrant | null = useMemo(() => {
    const active = (grantsQuery.data ?? []).filter((grant) => grant.state === "active");
    if (active.length === 0) return null;
    return active.reduce((latest, current) => (current.created_at > latest.created_at ? current : latest));
  }, [grantsQuery.data]);

  const revokedGrant = useMemo(() => {
    const revoked = (grantsQuery.data ?? []).filter((grant) => grant.state === "revoked");
    if (revoked.length === 0) return null;
    return revoked.reduce((latest, current) => (current.created_at > latest.created_at ? current : latest));
  }, [grantsQuery.data]);

  const renewMutation = useMutation({
    mutationFn: (input: { requested_until: string; reason: string }) => {
      if (activeGrant === null) {
        return Promise.reject(new Error("no active grant"));
      }
      return createQueryGrantRenewalRequest(activeGrant.id, input);
    },
    onSuccess: () => {
      toast.success(t("query.workspace.renewSubmitted"));
      setRenewOpen(false);
      void queryClient.invalidateQueries({ queryKey: ["query"] });
    },
    onError: (error) => {
      const msg = describeQueryErrorText(describeError(error, "createQueryGrantRenewalRequest"));
      setRenewError(msg);
      toast.error(msg);
    },
  });

  const blocked = row !== null && row.state !== "active";
  const capabilityBlocked = capability !== null && capability.state !== "active";
  const completion = useSessionCompletionCatalog(
    sessionId,
    datasourceId,
    schemaName,
    schemaNames,
    authenticated && row?.state === "active" && !capabilityBlocked,
    sql,
  );
  const structureQuery = useSessionColumns(
    sessionId,
    inspectTarget?.datasourceId ?? "",
    inspectTarget?.schema ?? "",
    inspectTarget?.table ?? "",
    inspectTarget !== null,
  );

  const handleDatasourceChange = useCallback(
    (nextDatasourceId: string) => {
      if (nextDatasourceId === datasourceId) return;
      setSelectedDatasourceId(nextDatasourceId);
      setSchemaName("");
      resetTargetAndError();
      setInspectMenu(null);
      setInspectTarget(null);
    },
    [datasourceId, resetTargetAndError],
  );

  const insertTemplate = useCallback(
    (nextSchema: string, tableName: string, targetDatasourceId?: string) => {
      const template = `SELECT *\nFROM ${nextSchema}.${tableName}\nLIMIT 100;`.replace(/;$/, "");
      setSql(template);
      handleSelectionChange("");
      setLoadValue({ text: template, nonce: Date.now() });
      if (
        targetDatasourceId !== undefined &&
        targetDatasourceId !== "" &&
        targetDatasourceId !== datasourceId
      ) {
        setSelectedDatasourceId(targetDatasourceId);
        resetTargetAndError();
      }
      setSchemaName(nextSchema);
    },
    [datasourceId, handleSelectionChange, resetTargetAndError],
  );

  const exportSchemaName = resolveStatementSchema(sql, schemaName);

  if (sessionQuery.isPending) {
    return (
      <PageLayout>
        <PageHeader icon={Database} title={t("query.workspace.title", { name: t("query.entry.title") })} />
        <LoadingState variant="page" />
      </PageLayout>
    );
  }

  if (sessionQuery.error !== null) {
    return (
      <PageLayout data-testid="query-workspace-error">
        <PageHeader icon={Database} title={t("query.workspace.title", { name: t("query.entry.title") })} />
        <ErrorState error={sessionQuery.error} operationId="getQuerySession" onRetry={() => void sessionQuery.refetch()} />
      </PageLayout>
    );
  }

  if (row === null) {
    return (
      <PageLayout>
        <PageHeader icon={Database} title={t("query.workspace.title", { name: t("query.entry.title") })} />
        <ErrorState error={new Error("session not found")} operationId="getQuerySession" onRetry={() => { void navigate("/query"); }} />
      </PageLayout>
    );
  }

  return (
    <PageLayout className="h-[max(calc(100vh-7rem),40rem)] min-h-0 gap-3" data-testid="query-workspace">
      <PageHeader
        icon={Database}
        title={t("query.workspace.title", {
          name: capability?.datasource_name ?? row.capabilities[0]?.datasource_name ?? t("query.entry.title"),
        })}
      />

      {row.state === "revoked" && (
        <Alert variant="destructive" data-testid="query-revoked-notice">
          <ShieldAlert />
          <AlertTitle>{t("query.workspace.revokedTitle")}</AlertTitle>
          <AlertDescription>
            {revokedGrant !== null && revokedGrant.revoked_reason !== null
              ? t("query.workspace.revokedReason", { reason: revokedGrant.revoked_reason })
              : t("query.workspace.revokedGeneric")}
            {t("query.workspace.revokedSeeMine")}
          </AlertDescription>
        </Alert>
      )}
      {row.state === "closed" && (
        <Alert data-testid="query-closed-notice">
          <Ban />
          <AlertTitle>{t("query.workspace.closedTitle")}</AlertTitle>
          <AlertDescription>{t("query.workspace.closedDescription")}</AlertDescription>
        </Alert>
      )}
      {(row.state === "expired" || row.state === "user_deleted") && (
        <Alert variant="destructive" data-testid="query-expired-notice">
          <ShieldAlert />
          <AlertTitle>{t("query.workspace.expiredTitle")}</AlertTitle>
          <AlertDescription>{t("query.workspace.expiredDescription")}</AlertDescription>
        </Alert>
      )}

      {activeGrant !== null && (
        <div className="text-muted-foreground flex flex-wrap items-center gap-2 text-xs" data-testid="query-grant-banner">
          <Badge variant="secondary">{t("query.workspace.grantActive")}</Badge>
          <span>
            {t("query.workspace.grantExpires", {
              time: activeGrant.expires_at === null || activeGrant.expires_at === undefined
                ? "—"
                : new Date(activeGrant.expires_at).toLocaleString(),
            })}
          </span>
          <Button
            size="sm"
            variant="outline"
            title={t("query.workspace.renewHint")}
            onClick={() => {
              setRenewError(null);
              setRenewOpen(true);
            }}
            data-testid="query-grant-renew"
          >
            {t("query.workspace.renew")}
          </Button>
        </div>
      )}

      <div className="grid min-h-0 flex-1 grid-cols-[280px_minmax(0,1fr)] gap-3">
        <QueryWorkspaceSidebar
          leftPanel={leftPanel}
          onLeftPanelChange={setLeftPanel}
          session={row}
          datasourceId={datasourceId}
          effectiveHistory={effectiveHistory}
          onSelectDatasource={handleDatasourceChange}
          onTableSelected={insertTemplate}
          onInspectTable={(schema, table, x, y, targetDatasourceId) => {
            setInspectTarget(null);
            setInspectMenu({
              datasourceId: targetDatasourceId ?? datasourceId,
              schema,
              table,
              x,
              y,
            });
          }}
          onSelectHistorySql={(nextSql) => {
            setSql(nextSql);
            handleSelectionChange("");
            setLoadValue({ text: nextSql, nonce: Date.now() });
          }}
        />

        <Card className="flex min-h-0 flex-col">
          <CardContent className="flex h-full min-h-0 flex-col gap-3 pt-4">
            <div className="flex flex-wrap items-center gap-2">
              {row.capabilities.length > 1 ? (
                <Select
                  value={datasourceId === "" ? null : datasourceId}
                  onValueChange={(value) => {
                    if (value !== null) handleDatasourceChange(value);
                  }}
                  items={Object.fromEntries(
                    row.capabilities.map((cap) => [cap.datasource_id, cap.datasource_name]),
                  )}
                  disabled={blocked}
                >
                  <SelectTrigger
                    className="h-8 w-44 font-mono text-xs"
                    aria-label={t("query.workspace.datasourcePlaceholder")}
                    data-testid="query-datasource-select"
                  >
                    <Database className="size-3 shrink-0" aria-hidden />
                    <SelectValue placeholder={t("query.workspace.datasourcePlaceholder")} />
                  </SelectTrigger>
                  <SelectContent>
                    {row.capabilities.map((cap) => (
                      <SelectItem key={cap.datasource_id} value={cap.datasource_id}>
                        {cap.datasource_name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Badge variant="outline" className="max-w-[240px] truncate font-mono text-xs">
                  <Database className="size-3" />
                  {capability?.datasource_name ?? datasourceId}
                </Badge>
              )}
              {capabilityBlocked && (
                <Badge variant="destructive" data-testid="query-capability-blocked">
                  {t(`query.capability.state.${(capability as { state: "datasource_unavailable" | "identity_changed" } | null)?.state ?? "datasource_unavailable"}`)}
                </Badge>
              )}
              <Select
                value={schemaName === "" ? null : schemaName}
                onValueChange={(value) => { setSchemaName(value ?? ""); }}
                items={Object.fromEntries(schemaNames.map((name) => [name, name]))}
                disabled={blocked}
              >
                <SelectTrigger
                  className="h-8 w-40 font-mono text-xs"
                  aria-label={t("query.workspace.schemaPlaceholder")}
                  data-testid="query-schema-input"
                >
                  <SelectValue placeholder={t("query.workspace.schemaPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {schemaNames.map((name) => (
                    <SelectItem key={name} value={name}>{name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <PendingButton
                size="sm"
                className="shrink-0"
                pending={running}
                onClick={() => void run()}
                disabled={blocked || capability === null || capabilityBlocked}
                data-testid="query-run"
              >
                <Play />
                {hasSelection ? t("query.run.executeSelected") : t("query.run.execute")}
              </PendingButton>
              <span className="text-muted-foreground min-w-0 truncate text-xs">{t("query.run.singleSelectHint")}</span>
              <div className="ml-auto flex shrink-0 items-center gap-2">
                {capability?.can_export === true && sql.trim() !== "" && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={blocked || capabilityBlocked || exportSchemaName === ""}
                    onClick={() => {
                      const trimmedSelected = selectedSqlRef.current.trim();
                      const sqlForExport = trimmedSelected !== "" ? trimmedSelected : sql;
                      const schemaForExport = resolveStatementSchema(sqlForExport, schemaName);
                      setExportContext({
                        datasourceId,
                        schemaName: schemaForExport !== "" ? schemaForExport : exportSchemaName,
                        sql: sqlForExport,
                      });
                    }}
                    data-testid="query-export"
                  >
                    <Download />
                    {t("query.export.button")}
                  </Button>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  disabled={blocked}
                  onClick={() => { setCloseOpen(true); }}
                  data-testid="query-exit"
                >
                  <LogOut />
                  {t("query.workspace.exit")}
                </Button>
              </div>
            </div>

            {/* Stable container height keeps Monaco overlays properly aligned when results mount below. */}
            <div className="h-[clamp(180px,30vh,280px)] shrink-0">
              <SqlEditorPanel
                className="h-full min-h-0"
                value={sql}
                onChange={setSql}
                onSelectionChange={handleSelectionChange}
                onRunShortcut={() => {
                  if (!blocked && !capabilityBlocked && capability !== null && !running) {
                    void run();
                  }
                }}
                loadValue={loadValue}
                completionCatalog={completion.catalog}
                onResolveCompletion={completion.resolveCompletionCatalog}
                readOnly={blocked}
                data-testid="query-sql-editor"
              />
            </div>

            {runError !== null && (
              <Alert variant="destructive" className="py-2" data-testid="query-run-error">
                <AlertDescription>{runError}</AlertDescription>
              </Alert>
            )}

            <div className="flex min-h-0 flex-1 flex-col">
              {tabs.length === 0 ? (
                <div className="text-muted-foreground flex flex-1 items-center justify-center rounded-md border border-dashed text-xs">
                  {t("query.result.empty")}
                </div>
              ) : tabs.length === 1 && tabs[0] !== undefined ? (
                <div className="min-h-0 flex-1">
                  <ResultGrid
                    tab={tabs[0]}
                    continuationError={continuationErrors[tabs[0].executionId] ?? null}
                  />
                </div>
              ) : (
                <Tabs value={activeTab} onValueChange={setActiveTab} className="flex min-h-0 flex-1 flex-col">
                  <TabsList className="-my-1 w-full overflow-x-auto overflow-y-hidden py-1">
                    {tabs.map((tab, index) => (
                      <TabsTrigger key={tab.executionId} value={tab.executionId} className="font-mono text-xs">
                        {tab.title ?? t("query.result.tabTitle", { index: index + 1 })}
                      </TabsTrigger>
                    ))}
                  </TabsList>
                  <TransitionPanel
                    activeIndex={Math.max(0, tabs.findIndex((tab) => tab.executionId === activeTab))}
                    className="flex min-h-0 flex-1 flex-col"
                    panelClassName="flex min-h-0 flex-1 flex-col"
                  >
                    {tabs.map((tab) => (
                      <div key={tab.executionId} className="min-h-0 flex-1">
                        <ResultGrid
                          tab={tab}
                          continuationError={continuationErrors[tab.executionId] ?? null}
                        />
                      </div>
                    ))}
                  </TransitionPanel>
                </Tabs>
              )}
            </div>
          </CardContent>
        </Card>
      </div>

      {inspectMenu !== null && (
        <ContextMenu
          position={{ x: inspectMenu.x, y: inspectMenu.y }}
          onClose={() => { setInspectMenu(null); }}
          overlayTestId="query-inspect-overlay"
          panelTestId="query-inspect-panel"
          items={[
            {
              icon: Copy,
              label: t("query.tree.copyName"),
              testId: "query-inspect-copy",
              onClick: () => {
                void writeClipboardText(`"${inspectMenu.schema}"."${inspectMenu.table}"`).then((ok) => {
                  if (ok) {
                    toast.info(t("common.copied"));
                  } else {
                    toast.error(t("common.copyFailed"));
                  }
                });
              },
            },
            {
              icon: Table2,
              label: t("query.tree.viewStructure"),
              testId: "query-inspect-open",
              onClick: () => {
                setInspectTarget({
                  datasourceId: inspectMenu.datasourceId,
                  schema: inspectMenu.schema,
                  table: inspectMenu.table,
                });
              },
            },
          ]}
        />
      )}

      <TableStructureDialog
        target={inspectTarget}
        isPending={structureQuery.isPending}
        error={structureQuery.error}
        columns={structureQuery.data}
        onClose={() => { setInspectTarget(null); }}
      />

      <CloseSessionDialog
        open={closeOpen}
        reason={closeReason}
        onReason={setCloseReason}
        onCancel={() => { setCloseOpen(false); }}
        onSubmit={() => {
          void closeSession
            .mutateAsync({ sessionId, reason: closeReason })
            .then(() => {
              toast.info(t("query.exit.confirm"));
              setCloseOpen(false);
              void navigate("/query");
            })
            .catch((error: unknown) => {
              toast.error(describeQueryErrorText(describeError(error, "closeQuerySession")));
            });
        }}
        submitting={closeSession.isPending}
      />

      {renewOpen ? (
        <RenewGrantDialog
          key={activeGrant?.id ?? "none"}
          grant={activeGrant}
          submitting={renewMutation.isPending}
          error={renewError}
          onCancel={() => { setRenewOpen(false); }}
          onSubmit={(input) => {
            setRenewError(null);
            renewMutation.mutate(input);
          }}
        />
      ) : null}

      {exportContext !== null ? (
        <ExportDialog
          key={`${exportContext.datasourceId}:${exportContext.schemaName}:${exportContext.sql}`}
          context={exportContext}
          sessionId={sessionId}
          onClose={() => { setExportContext(null); }}
        />
      ) : null}
    </PageLayout>
  );
}
