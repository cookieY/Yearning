import i18next from "@/shared/i18n";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Ban,
  Copy,
  Database,
  Download,
  EyeOff,
  History,
  LogOut,
  Play,
  ShieldAlert,
  Table2,
  X,
} from "lucide-react";
import type { QueryGrant, QueryResultPage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { createQueryGrantRenewalRequest } from "@/api/generated/client/query-access/query-access";
import { useSession } from "@/features/auth/session-provider";
import { describeError, type ErrorDisplay } from "@/shared/api/error-display";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { ContextMenu } from "@/shared/components/context-menu";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardHeader } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/shared/components/ui/hover-card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { DatePicker } from "@/shared/components/ui/date-picker";
import { Label } from "@/shared/components/ui/label";
import { Progress } from "@/shared/components/ui/progress";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { Textarea } from "@/shared/components/ui/textarea";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { SqlEditorPanel } from "@/features/review/sql-editor-panel";
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  highlightSqlLine,
} from "@/shared/components/ui/code-block";
import { MetadataTree } from "@/features/query/metadata-tree";
import {
  buildResultTabLabels,
  extractQueryTarget,
  resolveStatementSchema,
  splitSqlStatements,
} from "@/features/query/query-sql-utils";
import { ResultGrid, maskedMapFrom, type ResultTabState } from "@/features/query/result-grid";
import {
  DISPLAY_PAGE_SIZE,
  EXPORT_PAGE_SIZE,
  fetchPage,
  runSelect,
  useCloseSession,
  useMyGrants,
  useQuerySession,
  useSessionColumns,
  useSessionCompletionCatalog,
  useSessionSchemas,
} from "@/features/query/use-query-domain";
import {
  downloadXlsx,
  exportExecutionToXlsx,
  type ExportProgress,
} from "@/features/query/xlsx-export";

/**
 * Query Workspace (route /query/sessions/:id; migration contract §5).
 *
 * The session's frozen capability set is the datasource scope: switching
 * datasources stays inside it (UI spec §7.5), the tree exposes schemas →
 * tables → columns through the metadata endpoints, and one editor serves
 * single-SELECT statements. Client-side single-SELECT detection is a hint
 * only — the server's Query Safety Check is authoritative (4007 renders
 * inline).
 *
 * Grant lifecycle surfaces: the banner shows the active grant's remaining
 * validity with renewal and relinquish entries; a revocation flips the
 * session state (via global SSE invalidation + in-flight 4004) into a
 * non-dismissible blocked notice carrying the reason. Revocation actor/time
 * are not part of the frozen read contract — the RCP-pending gap is recorded
 * in the migration contract §17; My Orders - Query Orders remains the
 * authoritative detail view.
 */

interface HistoryEntry {
  id: string;
  sql: string;
  schemaName: string;
  datasourceName: string;
  elapsedMs: number | null;
  at: string;
  failed: boolean;
}

const AUTO_LOAD_ROW_LIMIT = 100_000;

const HISTORY_LIMIT = 100;
const MAX_BATCH_STATEMENTS = 16;

export default function QueryWorkspacePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { sessionId = "" } = useParams();
  const session = useSession();
  const authenticated = session.status === "authenticated";

  const sessionQuery = useQuerySession(sessionId, authenticated);
  const grantsQuery = useMyGrants(authenticated);

  // A revocation reaches this page through global SSE invalidation; the
  // grants list (the reason source) must re-read at the same moment or the
  // notice falls back to the generic copy.
  useEffect(() => {
    if (sessionQuery.data?.state === "revoked") {
      void queryClient.invalidateQueries({ queryKey: ["query"] });
    }
  }, [queryClient, sessionQuery.data?.state]);
  const closeSession = useCloseSession();

  const row = sessionQuery.data ?? null;
  const [datasourceId, setDatasourceId] = useState<string>("");
  const [schemaName, setSchemaName] = useState("");
  const [sql, setSql] = useState("SELECT ");
  const selectedSqlRef = useRef("");
  const [hasSelection, setHasSelection] = useState(false);
  const [loadValue, setLoadValue] = useState<{ text: string; nonce: number } | null>(null);

  const handleSelectionChange = useCallback((nextSelected: string) => {
    selectedSqlRef.current = nextSelected;
    const nextHas = nextSelected.trim() !== "";
    setHasSelection((prev) => (prev === nextHas ? prev : nextHas));
  }, []);
  const [running, setRunning] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [tabs, setTabs] = useState<ResultTabState[]>([]);
  const [activeTab, setActiveTab] = useState<string>("");
  const [continuationErrors, setContinuationErrors] = useState<Record<string, string | null>>({});
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const [leftPanel, setLeftPanel] = useState<"tree" | "history">("tree");
  const [closeOpen, setCloseOpen] = useState(false);
  const [closeReason, setCloseReason] = useState("");
  const [exportContext, setExportContext] = useState<ExportContext | null>(null);

  // Default datasource: first capability once the session loads.
  useEffect(() => {
    if (row === null || row.capabilities.length === 0) return;
    setDatasourceId((current) => {
      const first = row.capabilities[0];
      if (current !== "" && row.capabilities.some((capability) => capability.datasource_id === current)) {
        return current;
      }
      return first === undefined ? current : first.datasource_id;
    });
  }, [row]);

  const capability = useMemo(
    () => row?.capabilities.find((item) => item.datasource_id === datasourceId) ?? null,
    [datasourceId, row],
  );

  // §18.48: the schema picker lists the session's live schemas for the
  // active datasource (same read the metadata tree uses) — typing is gone.
  const schemasQuery = useSessionSchemas(sessionId, datasourceId, row?.state === "active");
  const schemaNames = useMemo(
    () => (schemasQuery.data ?? []).map((schema) => schema.name),
    [schemasQuery.data],
  );

  // The masked-column cross reference: loaded for the active schema/table
  // selection is impractical per query, so the workspace consults the
  // columns endpoint for the queried table after each run (best-effort,
  // metadata is live while run masking is frozen server-side).
  const [lastQueriedTarget, setLastQueriedTarget] = useState<{ schema: string; table: string }>({
    schema: "",
    table: "",
  });
  const columnsQuery = useSessionColumns(
    sessionId,
    datasourceId,
    lastQueriedTarget.schema !== "" ? lastQueriedTarget.schema : schemaName,
    lastQueriedTarget.table,
    authenticated &&
      row?.state === "active" &&
      (lastQueriedTarget.schema !== "" || schemaName !== "") &&
      lastQueriedTarget.table !== "",
  );
  const maskedByName = useMemo(() => maskedMapFrom(columnsQuery.data), [columnsQuery.data]);

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

  // Renewal request (owner feedback 2026-11-04: the banner button must actually
  // work): POST /query-grants/{id}/renewal-requests starts a fresh approval
  // flow; the current grant keeps running until it expires. The new request
  // surfaces in My Orders - Query Orders like any access request.
  const [renewOpen, setRenewOpen] = useState(false);
  const [renewError, setRenewError] = useState<string | null>(null);
  const renewMutation = useMutation({
    mutationFn: (input: { requested_until: string; reason: string }) => {
      if (activeGrant === null) {
        // Unreachable through the UI (the button only renders with an active
        // grant); describeError maps this to the safe generic message.
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
      const msg = describeErrorText(describeError(error, "createQueryGrantRenewalRequest"));
      setRenewError(msg);
      toast.error(msg);
    },
  });

  const blocked = row !== null && row.state !== "active";
  // Per-datasource capability states (datasource_unavailable /
  // identity_changed) block runs and exports up-front with the reason
  // badge instead of surfacing only as a 3010 at execution time.
  const capabilityBlocked = capability !== null && capability.state !== "active";
  const completion = useSessionCompletionCatalog(
    sessionId,
    datasourceId,
    schemaName,
    schemaNames,
    authenticated && row?.state === "active" && !capabilityBlocked,
    sql,
  );
  // §18.48: right-click a table -> a small context menu with View Table Structure.
  const [inspectMenu, setInspectMenu] = useState<{
    datasourceId: string;
    schema: string;
    table: string;
    x: number;
    y: number;
  } | null>(null);
  const [inspectTarget, setInspectTarget] = useState<{
    datasourceId: string;
    schema: string;
    table: string;
  } | null>(null);
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
      setDatasourceId(nextDatasourceId);
      setSchemaName("");
      setLastQueriedTarget({ schema: "", table: "" });
      setRunError(null);
      setInspectMenu(null);
      setInspectTarget(null);
    },
    [datasourceId],
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
        setDatasourceId(targetDatasourceId);
        setLastQueriedTarget({ schema: "", table: "" });
        setRunError(null);
      }
      // Clicking a table also activates its schema (§18.48, legacy parity):
      // the run's schema/SQL pairing stays consistent without a second pick.
      setSchemaName(nextSchema);
    },
    [datasourceId, handleSelectionChange],
  );

  const pushHistory = (entry: HistoryEntry) => {
    setHistory((current) => [entry, ...current].slice(0, HISTORY_LIMIT));
  };

  const effectiveHistory = useMemo<HistoryEntry[]>(() => {
    const byId = new Map<string, HistoryEntry>();
    for (const entry of history) {
      byId.set(entry.id, entry);
    }
    const capNames = new Map<string, string>(
      (row?.capabilities ?? []).map((cap) => [cap.datasource_id, cap.datasource_name]),
    );
    for (const ex of row?.executions ?? []) {
      if (byId.has(ex.id)) continue;
      byId.set(ex.id, {
        id: ex.id,
        sql: ex.sql,
        schemaName: "",
        datasourceName: capNames.get(ex.datasource_id) ?? ex.datasource_id,
        elapsedMs: ex.elapsed_ms ?? null,
        at: ex.created_at,
        failed: ex.state !== "succeeded" && ex.state !== "running",
      });
    }
    return Array.from(byId.values())
      .sort((a, b) => (b.at > a.at ? 1 : b.at < a.at ? -1 : 0))
      .slice(0, HISTORY_LIMIT);
  }, [history, row?.capabilities, row?.executions]);

  const exportSchemaName = resolveStatementSchema(sql, schemaName);

  const run = async () => {
    const trimmedSelected = selectedSqlRef.current.trim();
    const sqlToRun = trimmedSelected !== "" ? trimmedSelected : sql;
    const statements = splitSqlStatements(sqlToRun);
    const batch =
      statements.length > 1 && statements.length <= MAX_BATCH_STATEMENTS
        ? statements
        : [sqlToRun];
    const batchItems = batch.map((stmt) => ({
      sql: stmt,
      schemaName: resolveStatementSchema(stmt, schemaName),
      target: extractQueryTarget(stmt),
    }));
    if (
      row === null ||
      capability === null ||
      batchItems.some((item) => item.schemaName === "")
    ) {
      setRunError(t("query.run.needSchema"));
      return;
    }
    const tabLabels = buildResultTabLabels(
      batchItems,
      (index) => t("query.result.tabTitle", { index: index + 1 }),
    );
    const firstItem = batchItems[0];
    setRunError(null);
    setRunning(true);
    if (firstItem !== undefined) {
      setLastQueriedTarget({
        schema: firstItem.schemaName,
        table: firstItem.target.table ?? "",
      });
    }
    try {
      const nextTabs: ResultTabState[] = [];
      let totalElapsedMs = 0;
      for (let i = 0; i < batchItems.length; i += 1) {
        const item = batchItems[i];
        if (item === undefined) continue;
        const page = await runSelect(sessionId, {
          datasource_id: datasourceId,
          schema_name: item.schemaName,
          sql: item.sql,
          timeout_ms: 30000,
          page_size: DISPLAY_PAGE_SIZE,
        });
        const tab: ResultTabState = {
          executionId: page.execution_id,
          title: tabLabels[i],
          columns: page.columns,
          rows: [...page.rows],
          nextCursor: ((page as { page?: { next_cursor?: string | null } }).page?.next_cursor as string | null) ?? null,
          elapsedMs: page.elapsed_ms ?? null,
          exhausted: !((page as { page?: { has_more?: boolean } }).page?.has_more ?? false),
          maskedByName: new Map<string, boolean>(),
        };
        totalElapsedMs += tab.elapsedMs ?? 0;
        nextTabs.push(tab);
      }
      const firstTab = nextTabs[0];
      if (firstTab !== undefined) {
        setTabs(nextTabs);
        setActiveTab(firstTab.executionId);
        for (const tab of nextTabs) {
          if (!tab.exhausted && tab.nextCursor !== null) {
            void drainCursor(tab.executionId, tab.nextCursor, tab.rows.length);
          }
        }
        pushHistory({
          id: firstTab.executionId,
          sql: sqlToRun,
          schemaName: firstItem?.schemaName ?? schemaName,
          datasourceName: capability.datasource_name,
          elapsedMs: totalElapsedMs,
          at: new Date().toISOString(),
          failed: false,
        });
        void queryClient.invalidateQueries({ queryKey: ["query", "session", sessionId] });
      }
    } catch (error) {
      setRunError(describeErrorText(describeError(error, "executeSelect")));
      pushHistory({
        id: `failed-${String(Date.now())}`,
        sql: sqlToRun,
        schemaName: firstItem?.schemaName ?? schemaName,
        datasourceName: capability.datasource_name,
        elapsedMs: null,
        at: new Date().toISOString(),
        failed: true,
      });
    } finally {
      setRunning(false);
    }
  };

  // §18.50 follow-up (owner ruling: legacy parity, pure frontend paging):
  // after an execution lands, its remaining server cursor pages drain
  // automatically in the background until exhausted — the pager then pages
  // entirely over the loaded rows and the user never sees a fetch step.
  // A generous ceiling keeps a pathological result from draining forever;
  // past it the tab simply stops growing (exhausted stays false).
  const drainCursor = async (executionId: string, fromCursor: string, rowsSoFar: number): Promise<void> => {
    let cursor: string | null = fromCursor;
    let total = rowsSoFar;
    while (cursor !== null && total < AUTO_LOAD_ROW_LIMIT) {
      try {
        const page: QueryResultPage = await fetchPage(executionId, cursor, "display");
        total += page.rows.length;
        const nextCursor =
          ((page as { page?: { next_cursor?: string | null } }).page?.next_cursor as string | null) ?? null;
        const exhausted = !((page as { page?: { has_more?: boolean } }).page?.has_more ?? false);
        setTabs((current) =>
          current.map((item) =>
            item.executionId === executionId ? { ...item, rows: [...item.rows, ...page.rows], nextCursor, exhausted } : item,
          ),
        );
        setContinuationErrors((current) => ({ ...current, [executionId]: null }));
        if (exhausted) return;
        cursor = nextCursor;
      } catch (error) {
        setContinuationErrors((current) => ({
          ...current,
          [executionId]: describeErrorText(describeError(error, "fetchQueryResultPage")),
        }));
        return;
      }
    }
  };

  // Refresh the masked column map for the active result tab's table when the
  // metadata response arrives (live vocabulary — see component doc).
  useEffect(() => {
    if (columnsQuery.data === undefined) return;
    setTabs((current) =>
      current.map((item) => (item.maskedByName.size === 0 ? { ...item, maskedByName } : item)),
    );
  }, [activeTab, columnsQuery.data, maskedByName]);

  if (sessionQuery.isPending) {
    return (
      <PageLayout>
        <PageHeader
          icon={Database}
          title={t("query.workspace.title", { name: t("query.entry.title") })}
        />
        <LoadingState variant="page" />
      </PageLayout>
    );
  }

  if (sessionQuery.error !== null) {
    return (
      <PageLayout data-testid="query-workspace-error">
        <PageHeader
          icon={Database}
          title={t("query.workspace.title", { name: t("query.entry.title") })}
        />
        <ErrorState
          error={sessionQuery.error}
          operationId="getQuerySession"
          onRetry={() => void sessionQuery.refetch()}
        />
      </PageLayout>
    );
  }

  if (row === null) {
    return (
      <PageLayout>
        <PageHeader
          icon={Database}
          title={t("query.workspace.title", { name: t("query.entry.title") })}
        />
        <ErrorState error={new Error("session not found")} operationId="getQuerySession" onRetry={() => { void navigate("/query"); }} />
      </PageLayout>
    );
  }

  return (
    <PageLayout
      className="h-[max(calc(100vh-7rem),40rem)] min-h-0 gap-3"
      data-testid="query-workspace"
    >
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
        <div
          className="text-muted-foreground flex flex-wrap items-center gap-2 text-xs"
          data-testid="query-grant-banner"
        >
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
        <Card className="flex min-h-0 min-w-0 flex-col">
          <CardHeader className="min-w-0 grid-cols-[minmax(0,1fr)] pb-2">
            <Tabs
              value={leftPanel}
              onValueChange={(value) => { setLeftPanel(value as "tree" | "history"); }}
              className="min-w-0 w-full"
            >
              <TabsList className="grid w-full grid-cols-2">
                <TabsTrigger value="tree" className="min-w-0 text-xs" data-testid="query-left-tab-tree">
                  {t("query.workspace.treeTab")}
                </TabsTrigger>
                <TabsTrigger value="history" className="min-w-0 text-xs" data-testid="query-left-tab-history">
                  <History className="size-3" />
                  {t("query.workspace.historyTab")}
                </TabsTrigger>
              </TabsList>
              <TransitionPanel
                activeIndex={leftPanel === "tree" ? 0 : 1}
                keepMounted
                className="min-h-0 min-w-0"
              >
                <div className="h-[calc(100vh-18rem)] min-h-0 min-w-0">
                  {row.state === "active" ? (
                    <MetadataTree
                      session={row}
                      activeDatasourceId={datasourceId}
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
                    />
                  ) : (
                    <p className="text-muted-foreground p-3 text-xs">{t("query.workspace.treeBlocked")}</p>
                  )}
                </div>
                <div className="min-w-0 overflow-hidden">
                  {/* Unified scrollbars (owner 2026-11-03): history list uses ScrollArea overlay thumb. */}
                  <ScrollArea className="h-[calc(100vh-18rem)] w-full min-w-0">
                    <div className="w-full min-w-0 overflow-hidden">
                      {effectiveHistory.length === 0 && (
                        <p className="text-muted-foreground p-3 text-xs">{t("query.workspace.historyEmpty")}</p>
                      )}
                      {effectiveHistory.map((entry) => (
                        <HoverCard key={entry.id}>
                          <HoverCardTrigger
                            delay={150}
                            closeDelay={100}
                            render={
                              <button
                                type="button"
                                onClick={() => {
                                  setSql(entry.sql);
                                  handleSelectionChange("");
                                  setLoadValue({ text: entry.sql, nonce: Date.now() });
                                }}
                                className="hover:bg-accent block w-full max-w-full truncate rounded px-2 py-1.5 text-left font-mono text-xs"
                                data-testid={`query-history-${entry.id}`}
                              />
                            }
                          >
                            <span className={entry.failed ? "text-destructive" : ""}>
                              {entry.failed ? entry.sql : highlightSqlLine(entry.sql)}
                            </span>
                          </HoverCardTrigger>
                          <HoverCardContent
                            side="right"
                            align="start"
                            className="w-96 max-w-[min(28rem,calc(100vw-2rem))]"
                            data-testid={`query-history-hover-${entry.id}`}
                          >
                            <CodeBlock size="sm">
                              <CodeBlockActions>
                                <CodeBlockCopyButton value={entry.sql} />
                              </CodeBlockActions>
                              <CodeBlockContent wrap className="max-h-60">
                                {entry.sql}
                              </CodeBlockContent>
                            </CodeBlock>
                          </HoverCardContent>
                        </HoverCard>
                      ))}
                    </div>
                  </ScrollArea>
                </div>
              </TransitionPanel>
            </Tabs>
          </CardHeader>
        </Card>

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
              {/* §18.48: UI-framework Select over the session's live schemas. */}
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

            {/* Fixed-height chain (F5 virtualization lesson): Monaco must
                measure a stable box or its overlays spill over the result
                area once results render and the flex space shrinks. */}
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
                if ("clipboard" in navigator) void navigator.clipboard.writeText(`"${inspectMenu.schema}"."${inspectMenu.table}"`);
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

      <Dialog open={inspectTarget !== null} onOpenChange={(next) => { if (!next) setInspectTarget(null); }}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="font-mono text-sm">
              {inspectTarget === null ? "" : `${inspectTarget.schema}.${inspectTarget.table}`}
            </DialogTitle>
            <DialogDescription>{t("query.structure.description")}</DialogDescription>
          </DialogHeader>
          {/* Unified scrollbars (owner 2026-11-03): structure table scrolls with ScrollArea overlay
              thumb; height cap sits on the viewport (see scroll-area.tsx comment). */}
          <ScrollArea data-testid="query-table-structure" viewportClassName="max-h-96">
            <div>
            {structureQuery.isPending && <LoadingState variant="table" columns={8} />}
            {structureQuery.error !== null && <p className="text-destructive p-2 text-xs">{t("query.tree.loadFailed")}</p>}
            {structureQuery.data !== undefined && (
              <Table density="compact">
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-10">#</TableHead>
                    <TableHead>{t("query.structure.column")}</TableHead>
                    <TableHead>{t("query.structure.type")}</TableHead>
                    <TableHead className="w-14">{t("query.structure.key")}</TableHead>
                    <TableHead className="w-20">{t("query.structure.nullable")}</TableHead>
                    <TableHead>{t("query.structure.default")}</TableHead>
                    <TableHead>{t("query.structure.extra")}</TableHead>
                    <TableHead>{t("query.structure.comment")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {structureQuery.data.map((column) => (
                    <TableRow key={column.column_name} data-testid={`query-structure-${column.column_name}`}>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">{column.ordinal}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {column.column_name}
                        {column.masked && <EyeOff className="ml-1 inline size-3 text-muted-foreground" aria-label={t("query.result.maskedColumns")} />}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{column.data_type}</TableCell>
                      <TableCell className="text-xs">
                        {column.column_key ? (
                          <Badge variant={column.column_key === "PRI" ? "default" : "secondary"} className="px-1.5 font-mono text-[10px]">
                            {column.column_key}
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-xs">{column.nullable ? "NULL" : "NOT NULL"}</TableCell>
                      <TableCell className="font-mono text-xs">{column.column_default || <span className="text-muted-foreground">—</span>}</TableCell>
                      <TableCell className="font-mono text-xs">{column.extra || <span className="text-muted-foreground">—</span>}</TableCell>
                      <TableCell className="text-xs">{column.comment || <span className="text-muted-foreground">—</span>}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
            </div>
          </ScrollArea>
        </DialogContent>
      </Dialog>

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
              // The dialog stays open; the mutation error renders via the
              // session re-read rather than a silent failure.
              toast.error(describeErrorText(describeError(error, "closeQuerySession")));
            });
        }}
        submitting={closeSession.isPending}
      />

      <RenewGrantDialog
        open={renewOpen}
        grant={activeGrant}
        submitting={renewMutation.isPending}
        error={renewError}
        onCancel={() => { setRenewOpen(false); }}
        onSubmit={(input) => {
          setRenewError(null);
          renewMutation.mutate(input);
        }}
      />

      <ExportDialog context={exportContext} sessionId={sessionId} onClose={() => { setExportContext(null); }} />
    </PageLayout>
  );
}

function CloseSessionDialog({
  open,
  reason,
  onReason,
  onCancel,
  onSubmit,
  submitting,
}: {
  open: boolean;
  reason: string;
  onReason: (value: string) => void;
  onCancel: () => void;
  onSubmit: () => void;
  submitting: boolean;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onCancel(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("query.exit.title")}</DialogTitle>
          <DialogDescription>{t("query.exit.description")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2">
          <Label htmlFor="query-exit-reason">{t("query.exit.reason")}</Label>
          <Textarea
            id="query-exit-reason"
            value={reason}
            onChange={(event) => { onReason(event.target.value); }}
            maxLength={4096}
            data-testid="query-exit-reason"
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={submitting}
            onClick={() => { onSubmit(); }}
            data-testid="query-exit-confirm"
          >
            {t("query.exit.confirm")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

interface ExportContext {
  datasourceId: string;
  schemaName: string;
  sql: string;
}

/** UX-006: today in local timezone (YYYY-MM-DD), used by the date picker to disable past dates. */
function todayLocal(): string {
  const now = new Date();
  return `${String(now.getFullYear())}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}

/**
 * Renewal request dialog: mirrors the access-request form (date + time + reason ->
 * wall-clock Z timestamp at minute granularity, OBS-009) and defaults the
 * target date to the current grant's expiry so a renewal extends it.
 */
function RenewGrantDialog({
  open,
  grant,
  submitting,
  error,
  onCancel,
  onSubmit,
}: {
  open: boolean;
  grant: QueryGrant | null;
  submitting: boolean;
  error: string | null;
  onCancel: () => void;
  onSubmit: (input: { requested_until: string; reason: string }) => void;
}) {
  const { t } = useTranslation();
  const defaultUntil = (): string => {
    // Review L3: the default must strictly extend the current grant. Derive
    // from the expiry's UTC calendar date + 1 day (OBS-009 wall-clock — a
    // west-of-UTC local date would otherwise land before the expiry once
    // the server re-anchors the wall-clock digits).
    const base =
      grant === null || grant.expires_at === null || grant.expires_at === undefined
        ? new Date()
        : new Date(Date.parse(grant.expires_at));
    const extended = new Date(base.getTime() + 24 * 60 * 60 * 1000);
    return `${String(extended.getUTCFullYear())}-${String(extended.getUTCMonth() + 1).padStart(2, "0")}-${String(extended.getUTCDate()).padStart(2, "0")}`;
  };
  const [until, setUntil] = useState("");
  const [untilTime, setUntilTime] = useState("23:59");
  const [reason, setReason] = useState("");
  const [armed, setArmed] = useState(false);
  if (open && !armed) {
    setArmed(true);
    setUntil(defaultUntil());
    setUntilTime("23:59");
    setReason("");
  }
  if (!open && armed) {
    setArmed(false);
  }
  const valid = until !== "" && untilTime !== "" && reason.trim() !== "";
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onCancel(); }}>
      <DialogContent data-testid="query-renew-dialog">
        <DialogHeader>
          <DialogTitle>{t("query.workspace.renewTitle")}</DialogTitle>
          <DialogDescription>{t("query.workspace.renewDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-renew-until">{t("query.apply.until")}</Label>
            <DatePicker
              value={until}
              onChange={(next) => { setUntil(next); }}
              time={untilTime}
              onTimeChange={(next) => { setUntilTime(next); }}
              timeLabel={t("query.apply.untilTime")}
              timeId="query-renew-until-time"
              timeTestId="query-renew-until-time"
              testId="query-renew-until"
              ariaLabel={t("query.apply.until")}
              disableBefore={todayLocal()}
              className="w-full"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-renew-reason">{t("query.apply.reason")}</Label>
            <Textarea
              id="query-renew-reason"
              value={reason}
              onChange={(event) => { setReason(event.target.value); }}
              maxLength={4096}
              data-testid="query-renew-reason"
            />
          </div>
          {error !== null && (
            <Alert variant="destructive" data-testid="query-renew-error">
              <AlertTitle>{t("query.workspace.renewFailed")}</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            onClick={() => {
              // OBS-009: wall-clock time (Z marker) at minute granularity, re-anchored server-side
              // in general.system_timezone.
              onSubmit({ requested_until: `${until}T${untilTime}:00Z`, reason });
            }}
            pending={submitting}
            disabled={!valid}
            data-testid="query-renew-submit"
          >
            {t("query.workspace.renewSubmit")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ExportDialog({
  context,
  sessionId,
  onClose,
}: {
  context: ExportContext | null;
  sessionId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [progress, setProgress] = useState<ExportProgress | null>(null);
  const [done, setDone] = useState<{ rows: number; executionId: string } | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const cancelledRef = useRef(false);
  const startedRef = useRef(false);

  // The dialog stays mounted (context=null hides it), so every close must
  // fully reset the run state — a stale startedRef would silently swallow
  // the next export and keep showing the previous success.
  // Closing cancels any still-running export loop and fully resets the
  // dialog run state (R2 H-3/L-9); the next context re-arms the refs.
  const closeExport = () => {
    cancelledRef.current = true;
    startedRef.current = false;
    setProgress(null);
    setDone(null);
    setFailure(null);
    onClose();
  };



  useEffect(() => {
    if (context === null) return;
    if (startedRef.current) return;
    startedRef.current = true;
    cancelledRef.current = false;
    cancelledRef.current = false;
    setProgress({ pagesRead: 0, rowsExported: 0, finished: false, failure: null });
    setDone(null);
    setFailure(null);
    // Server cursors are strictly forward and the frozen executeSelect
    // contract carries no purpose field — the first screen of an export
    // cannot itself be an export-purpose read. SEC-0031 tightening: the
    // export therefore opens its OWN execution with page_size=1, bounding
    // the un-enforced display read to a single row, and walks EVERYTHING
    // else through purpose=export continuations — each one re-authorizes
    // the frozen can_export server-side (4003) and writes the
    // query.export_page_read audit, the same caliber as the detail pages.
    // Results of 0..1 rows open no cursor (has_more=false) and have no
    // auditable read path under this contract; that single row is
    // equivalent to manual collection through display, which the PRD
    // already declares outside can_export's boundary.
    void (async () => {
      try {
        const first = await runSelect(sessionId, {
          datasource_id: context.datasourceId,
          schema_name: context.schemaName,
          sql: context.sql,
          timeout_ms: 30000,
          page_size: 1,
        });
        const executionId = first.execution_id;
        await exportExecutionToXlsx(
          first,
          (cursor) => fetchPage(executionId, cursor, "export", EXPORT_PAGE_SIZE),
          {
            onProgress: setProgress,
            onDone: (bytes, rows) => {
              downloadXlsx(bytes, `query-export-${executionId}.xlsx`);
              setDone({ rows, executionId });
            },
            onFailure: (message, rows) => {
              setFailure(t("query.export.failedWithRange", { message, rows }));
            },
          },
          () => cancelledRef.current,
        );
      } catch (error) {
        setFailure(describeErrorText(describeError(error, "executeSelect")));
      }
    })();
  }, [context, sessionId, t]);

  if (context === null) return null;

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent data-testid="query-export-dialog">
        <DialogHeader>
          <DialogTitle>{t("query.export.title")}</DialogTitle>
          <DialogDescription>{t("query.export.description")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {progress !== null && !done && failure === null && (
            <>
              <Progress value={progress.finished ? 100 : Math.min(95, progress.pagesRead * 10)} />
              <p className="text-muted-foreground text-xs" data-testid="query-export-progress">
                {t("query.export.progress", { pages: progress.pagesRead, rows: progress.rowsExported })}
              </p>
            </>
          )}
          {done !== null && (
            <p className="text-xs" data-testid="query-export-done">
              {t("query.export.done", { rows: done.rows })}
            </p>
          )}
          {done === null && progress === null && (
            <LoadingState variant="inline" />
          )}
          {failure !== null && (
            <Alert variant="destructive" data-testid="query-export-failure">
              <AlertDescription>{failure}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          {done === null && failure === null && (
            <Button
              variant="outline"
              onClick={() => {
                cancelledRef.current = true;
              }}
              data-testid="query-export-cancel"
            >
              <X />
              {t("query.export.cancel")}
            </Button>
          )}
          <Button onClick={closeExport} data-testid="query-export-close">{t("common.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

const CURSOR_REJECTION_KEYS: Record<string, string> = {
  undefined_table: "query.errors.undefinedTable",
  undefined_column: "query.errors.undefinedColumn",
  data_type_mismatch: "query.errors.dataTypeMismatch",
  parse_error: "query.errors.parseError",
  insufficient_privilege: "query.errors.insufficientPrivilege",
  statement_timeout: "errors.QUERY_TIMEOUT",
  query_cancelled: "query.errors.queryCancelled",
  connection_failed: "errors.DATASOURCE_UNAVAILABLE",
  authentication_failed: "errors.DATASOURCE_AUTH_FAILED",
};

function describeErrorText(display: ErrorDisplay): string {
  // Translated through the shared i18n bundle so users never see raw keys.
  // Map sanitized `database: cursor_rejected_<failure>` details on 4011 QUERY_STATEMENT_REJECTED
  // to specific localized messages so typos (e.g. missing table/column) never look like read-only rejections.
  const rawDetail = typeof display.detail === "string" ? display.detail.trim() : "";
  if (display.messageKey === "errors.QUERY_STATEMENT_REJECTED" && rawDetail.startsWith("database: cursor_rejected_")) {
    const failure = rawDetail.slice("database: cursor_rejected_".length);
    const mappedKey = CURSOR_REJECTION_KEYS[failure];
    if (mappedKey !== undefined) {
      const mapped = i18next.t(mappedKey);
      return display.requestId === null ? mapped : `${mapped} (${display.requestId})`;
    }
  }
  // BUG-010: unmapped driver diagnostic details carried in meta.detail remain appended for diagnosability.
  const detail = rawDetail !== "" ? ` ${rawDetail}` : "";
  const text = i18next.t(display.messageKey) + detail;
  return display.requestId === null ? text : `${text} (${display.requestId})`;
}
