import { type RefObject, useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQueryClient } from "@tanstack/react-query";
import type {
  QueryResultPage,
  QuerySession,
  QuerySessionCapability,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { describeError } from "@/shared/api/error-display";
import { describeQueryErrorText } from "@/features/query/query-error-text";
import {
  buildResultTabLabels,
  extractQueryTarget,
  resolveStatementSchema,
  splitSqlStatements,
} from "@/features/query/query-sql-utils";
import { maskedMapFrom, type ResultTabState } from "@/features/query/result-grid";
import {
  DISPLAY_PAGE_SIZE,
  fetchPage,
  runSelect,
  useSessionColumns,
} from "@/features/query/use-query-domain";

export interface HistoryEntry {
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

export function useQueryExecutionTabs({
  sessionId,
  authenticated,
  row,
  capability,
  datasourceId,
  schemaName,
  sql,
  selectedSqlRef,
}: {
  sessionId: string;
  authenticated: boolean;
  row: QuerySession | null;
  capability: QuerySessionCapability | null;
  datasourceId: string;
  schemaName: string;
  sql: string;
  selectedSqlRef: RefObject<string>;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [running, setRunning] = useState(false);
  const [runError, setRunError] = useState<string | null>(null);
  const [tabs, setTabs] = useState<ResultTabState[]>([]);
  const [activeTab, setActiveTab] = useState<string>("");
  const [continuationErrors, setContinuationErrors] = useState<Record<string, string | null>>({});
  const [history, setHistory] = useState<HistoryEntry[]>([]);
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

  // Refresh the masked column map for tabs whose mask set hasn't populated yet.
  useEffect(() => {
    if (columnsQuery.data === undefined) return;
    setTabs((current) =>
      current.map((item) => (item.maskedByName.size === 0 ? { ...item, maskedByName } : item)),
    );
  }, [activeTab, columnsQuery.data, maskedByName]);

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

  const pushHistory = (entry: HistoryEntry) => {
    setHistory((current) => [entry, ...current].slice(0, HISTORY_LIMIT));
  };

  // After an execution lands, remaining server cursor pages drain in the background
  // up to AUTO_LOAD_ROW_LIMIT so result pagination operates purely on loaded rows.
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
            item.executionId === executionId
              ? { ...item, rows: [...item.rows, ...page.rows], nextCursor, exhausted }
              : item,
          ),
        );
        setContinuationErrors((current) => ({ ...current, [executionId]: null }));
        if (exhausted) return;
        cursor = nextCursor;
      } catch (error) {
        setContinuationErrors((current) => ({
          ...current,
          [executionId]: describeQueryErrorText(describeError(error, "fetchQueryResultPage")),
        }));
        return;
      }
    }
  };

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
          nextCursor:
            ((page as { page?: { next_cursor?: string | null } }).page?.next_cursor as string | null) ?? null,
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
      setRunError(describeQueryErrorText(describeError(error, "executeSelect")));
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

  const resetTargetAndError = useCallback(() => {
    setLastQueriedTarget({ schema: "", table: "" });
    setRunError(null);
  }, []);

  return {
    running,
    runError,
    tabs,
    activeTab,
    setActiveTab,
    continuationErrors,
    effectiveHistory,
    run,
    resetTargetAndError,
  };
}
