import { LoadingState } from "@/shared/components/status/loading-state";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronRight, Database, EyeOff, Server, Table2 } from "lucide-react";
import type {
  QuerySession,
  QuerySessionCapability,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/shared/components/ui/collapsible";
import { Input } from "@/shared/components/ui/input";
import { Badge } from "@/shared/components/ui/badge";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  useSessionColumns,
  useSessionSchemas,
  useSessionTables,
} from "@/features/query/use-query-domain";

/**
 * Frozen-scope metadata tree (UI spec §7.5): datasource → schema → table,
 * lazy per level through the session metadata endpoints. Tables insert a
 * SELECT template into the editor; expanding a table loads its column list
 * where masked columns render the Masked badge from the LIVE vocabulary — the
 * per-run masking itself is frozen server-side at execution time.
 */

interface MetadataTreeProps {
  session: QuerySession;
  activeDatasourceId: string;
  onSelectDatasource?: (datasourceId: string) => void;
  onTableSelected: (schemaName: string, tableName: string, datasourceId?: string) => void;
  /** §18.48 legacy parity: right-click a table → view its structure. */
  onInspectTable?: (
    schemaName: string,
    tableName: string,
    x: number,
    y: number,
    datasourceId?: string,
  ) => void;
}

export function MetadataTree({
  session,
  activeDatasourceId,
  onSelectDatasource,
  onTableSelected,
  onInspectTable,
}: MetadataTreeProps) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState("");
  const [expandedDatasources, setExpandedDatasources] = useState<Record<string, boolean>>({});
  const [expandedSchemas, setExpandedSchemas] = useState<Set<string>>(new Set());
  const [expandedTables, setExpandedTables] = useState<Set<string>>(new Set());

  const multiDatasource = session.capabilities.length > 1;
  const filterText = filter.trim().toLowerCase();

  const toggleSchema = (dsId: string, schemaName: string) => {
    const key = `${dsId}:${schemaName}`;
    setExpandedSchemas((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const toggleTable = (dsId: string, schemaName: string, tableName: string) => {
    const key = `${dsId}:${schemaName}.${tableName}`;
    setExpandedTables((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col gap-2" data-testid="query-metadata-tree">
      <Input
        value={filter}
        onChange={(event) => { setFilter(event.target.value); }}
        placeholder={t("query.tree.searchPlaceholder")}
        data-testid="query-tree-filter"
      />
      {/* Unified scrollbars (owner 2026-11-03): tree scrolling uses ScrollArea overlay thumb. */}
      <ScrollArea className="min-h-0 min-w-0 flex-1">
        <div className="w-full min-w-0 overflow-hidden pr-1">
          {multiDatasource ? (
            session.capabilities.map((cap) => {
              const expanded =
                expandedDatasources[cap.datasource_id] ??
                cap.datasource_id === activeDatasourceId;
              return (
                <DatasourceNode
                  key={cap.datasource_id}
                  session={session}
                  capability={cap}
                  active={cap.datasource_id === activeDatasourceId}
                  expanded={expanded}
                  expandedSchemas={expandedSchemas}
                  expandedTables={expandedTables}
                  filterText={filterText}
                  onToggle={(nextOpen) => {
                    setExpandedDatasources((current) => ({
                      ...current,
                      [cap.datasource_id]: nextOpen,
                    }));
                    if (nextOpen && cap.datasource_id !== activeDatasourceId) {
                      onSelectDatasource?.(cap.datasource_id);
                    }
                  }}
                  onToggleSchema={(schemaName) => { toggleSchema(cap.datasource_id, schemaName); }}
                  onToggleTable={(schemaName, tableName) => {
                    toggleTable(cap.datasource_id, schemaName, tableName);
                  }}
                  onTableSelected={(schemaName, tableName) => {
                    onTableSelected(schemaName, tableName, cap.datasource_id);
                  }}
                  onInspectTable={
                    onInspectTable === undefined
                      ? undefined
                      : (schemaName, tableName, x, y) => {
                          onInspectTable(schemaName, tableName, x, y, cap.datasource_id);
                        }
                  }
                />
              );
            })
          ) : (
            <DatasourceSchemaList
              sessionId={session.id}
              datasourceId={activeDatasourceId}
              enabled={session.state === "active"}
              expandedSchemas={expandedSchemas}
              expandedTables={expandedTables}
              filterText={filterText}
              onToggleSchema={(schemaName) => { toggleSchema(activeDatasourceId, schemaName); }}
              onToggleTable={(schemaName, tableName) => {
                toggleTable(activeDatasourceId, schemaName, tableName);
              }}
              onTableSelected={(schemaName, tableName) => {
                onTableSelected(schemaName, tableName);
              }}
              onInspectTable={
                onInspectTable === undefined
                  ? undefined
                  : (schemaName, tableName, x, y) => {
                      onInspectTable(schemaName, tableName, x, y);
                    }
              }
            />
          )}
        </div>
      </ScrollArea>
    </div>
  );
}

function DatasourceNode({
  session,
  capability,
  active,
  expanded,
  expandedSchemas,
  expandedTables,
  filterText,
  onToggle,
  onToggleSchema,
  onToggleTable,
  onTableSelected,
  onInspectTable,
}: {
  session: QuerySession;
  capability: QuerySessionCapability;
  active: boolean;
  expanded: boolean;
  expandedSchemas: Set<string>;
  expandedTables: Set<string>;
  filterText: string;
  onToggle: (nextOpen: boolean) => void;
  onToggleSchema: (schemaName: string) => void;
  onToggleTable: (schemaName: string, tableName: string) => void;
  onTableSelected: (schemaName: string, tableName: string) => void;
  onInspectTable?: (schemaName: string, tableName: string, x: number, y: number) => void;
}) {
  const { t } = useTranslation();
  const unavailable = capability.state !== "active";

  return (
    <Collapsible open={expanded} onOpenChange={onToggle}>
      <CollapsibleTrigger
        aria-expanded={expanded}
        className={`group hover:bg-accent flex w-full cursor-pointer items-center gap-1 rounded px-1 py-1 text-left text-sm ${
          active ? "bg-accent/60 font-medium text-foreground" : "text-foreground"
        }`}
        data-testid={`query-tree-datasource-${capability.datasource_id}`}
      >
        <ChevronRight className="size-3.5 shrink-0 transition-transform duration-200 ease-out group-data-panel-open:rotate-90" />
        <Server className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate font-mono text-xs">{capability.datasource_name}</span>
        {unavailable && (
          <Badge variant="destructive" className="ml-auto shrink-0 text-[10px]">
            {t(`query.capability.state.${capability.state}`)}
          </Badge>
        )}
      </CollapsibleTrigger>
      <CollapsibleContent className="ml-3">
        {unavailable ? (
          <p className="text-muted-foreground px-2 py-2 text-xs">
            {t(`query.capability.state.${capability.state}`)}
          </p>
        ) : (
          <DatasourceSchemaList
            sessionId={session.id}
            datasourceId={capability.datasource_id}
            enabled={session.state === "active" && expanded}
            expandedSchemas={expandedSchemas}
            expandedTables={expandedTables}
            filterText={filterText}
            onToggleSchema={onToggleSchema}
            onToggleTable={onToggleTable}
            onTableSelected={onTableSelected}
            onInspectTable={onInspectTable}
          />
        )}
      </CollapsibleContent>
    </Collapsible>
  );
}

function DatasourceSchemaList({
  sessionId,
  datasourceId,
  enabled,
  expandedSchemas,
  expandedTables,
  filterText,
  onToggleSchema,
  onToggleTable,
  onTableSelected,
  onInspectTable,
}: {
  sessionId: string;
  datasourceId: string;
  enabled: boolean;
  expandedSchemas: Set<string>;
  expandedTables: Set<string>;
  filterText: string;
  onToggleSchema: (schemaName: string) => void;
  onToggleTable: (schemaName: string, tableName: string) => void;
  onTableSelected: (schemaName: string, tableName: string) => void;
  onInspectTable?: (schemaName: string, tableName: string, x: number, y: number) => void;
}) {
  const { t } = useTranslation();
  const schemasQuery = useSessionSchemas(sessionId, datasourceId, enabled);
  const schemas = schemasQuery.data ?? [];

  return (
    <>
      {schemasQuery.isPending && <LoadingState variant="inline" />}
      {schemasQuery.error !== null && (
        <p className="text-destructive px-2 py-4 text-xs">{t("query.tree.loadFailed")}</p>
      )}
      {!schemasQuery.isPending &&
        schemas.map((schema) => (
          <SchemaNode
            key={`${datasourceId}:${schema.name}`}
            sessionId={sessionId}
            datasourceId={datasourceId}
            schemaName={schema.name}
            expanded={expandedSchemas.has(`${datasourceId}:${schema.name}`)}
            expandedTables={expandedTables}
            filterText={filterText}
            onToggle={() => { onToggleSchema(schema.name); }}
            onToggleTable={(tableName) => { onToggleTable(schema.name, tableName); }}
            onTableSelected={onTableSelected}
            onInspectTable={onInspectTable}
          />
        ))}
    </>
  );
}

interface SchemaNodeProps {
  sessionId: string;
  datasourceId: string;
  schemaName: string;
  expanded: boolean;
  expandedTables: Set<string>;
  filterText: string;
  onToggle: () => void;
  onToggleTable: (tableName: string) => void;
  onTableSelected: (schemaName: string, tableName: string) => void;
  onInspectTable?: (schemaName: string, tableName: string, x: number, y: number) => void;
}

function SchemaNode({
  sessionId,
  datasourceId,
  schemaName,
  expanded,
  expandedTables,
  filterText,
  onToggle,
  onToggleTable,
  onTableSelected,
  onInspectTable,
}: SchemaNodeProps) {
  const { t } = useTranslation();
  const tablesQuery = useSessionTables(sessionId, datasourceId, schemaName, expanded);
  const visibleTables = useMemo(() => {
    const tables = tablesQuery.data ?? [];
    return filterText === ""
      ? tables
      : tables.filter((table) => table.table_name.toLowerCase().includes(filterText));
  }, [filterText, tablesQuery.data]);

  return (
    <Collapsible open={expanded} onOpenChange={onToggle}>
      <CollapsibleTrigger
        aria-expanded={expanded}
        className="group hover:bg-accent flex w-full cursor-pointer items-center gap-1 rounded px-1 py-1 text-left text-sm"
        data-testid={`query-tree-schema-${schemaName}`}
      >
        <ChevronRight className="size-3.5 shrink-0 transition-transform duration-200 ease-out group-data-panel-open:rotate-90" />
        <Database className="size-3.5 text-muted-foreground" />
        <span className="truncate font-mono text-xs">{schemaName}</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="ml-4">
        {tablesQuery.isPending && <LoadingState variant="inline" />}
        {visibleTables.map((table) => {
          const key = `${datasourceId}:${schemaName}.${table.table_name}`;
          const tableExpanded = expandedTables.has(key);
          return (
            <Collapsible
              key={table.table_name}
              open={tableExpanded}
              onOpenChange={() => {
                onToggleTable(table.table_name);
              }}
            >
              <div className="flex items-center">
                <CollapsibleTrigger
                  aria-expanded={tableExpanded}
                  className="group hover:bg-accent cursor-pointer rounded p-0.5"
                  data-testid={`query-tree-expand-${schemaName}-${table.table_name}`}
                >
                  <ChevronRight className="size-3 shrink-0 transition-transform duration-200 ease-out group-data-panel-open:rotate-90" />
                </CollapsibleTrigger>
                <button
                  type="button"
                  onClick={() => { onTableSelected(schemaName, table.table_name); }}
                  onContextMenu={(event) => {
                    if (onInspectTable === undefined) return;
                    event.preventDefault();
                    onInspectTable(schemaName, table.table_name, event.clientX, event.clientY);
                  }}
                  title={t("query.tree.insertTemplate")}
                  className="hover:bg-accent flex min-w-0 flex-1 cursor-pointer items-center gap-1 rounded px-1 py-1 text-left text-sm"
                  data-testid={`query-tree-table-${schemaName}-${table.table_name}`}
                >
                  <Table2 className="size-3.5 text-muted-foreground" />
                  <span className="truncate font-mono text-xs">{table.table_name}</span>
                  {table.relation_kind !== "table" && (
                    <Badge variant="outline" className="ml-auto text-[11px]">
                      {table.relation_kind}
                    </Badge>
                  )}
                </button>
              </div>
              <CollapsibleContent>
                <ColumnList
                  sessionId={sessionId}
                  datasourceId={datasourceId}
                  schemaName={schemaName}
                  tableName={table.table_name}
                />
              </CollapsibleContent>
            </Collapsible>
          );
        })}
      </CollapsibleContent>
    </Collapsible>
  );
}

function ColumnList({
  sessionId,
  datasourceId,
  schemaName,
  tableName,
}: {
  sessionId: string;
  datasourceId: string;
  schemaName: string;
  tableName: string;
}) {
  const { t } = useTranslation();
  const columnsQuery = useSessionColumns(sessionId, datasourceId, schemaName, tableName, true);
  const columns = columnsQuery.data ?? [];
  return (
    <div className="ml-6 border-l pl-2" data-testid={`query-tree-columns-${schemaName}-${tableName}`}>
      {columnsQuery.isPending && <LoadingState variant="inline" />}
      {columns.map((column) => (
        <div key={column.column_name} className="flex items-center gap-1 py-0.5">
          <span className="text-muted-foreground truncate font-mono text-xs">{column.column_name}</span>
          <span className="text-muted-foreground/70 shrink-0 font-mono text-[11px]">{column.data_type}</span>
          {column.masked && (
            <Badge variant="secondary" className="ml-auto shrink-0 gap-0.5 text-[11px]">
              <EyeOff className="size-2.5" />
              {t("query.tree.masked")}
            </Badge>
          )}
        </div>
      ))}
    </div>
  );
}
