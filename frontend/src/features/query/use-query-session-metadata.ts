import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import {
  extractSqlMetadataReferences,
  type SqlCompletionCatalog,
} from "@/features/review/sql-completions";
import {
  listQuerySessionColumns,
  listQuerySessionSchemas,
  listQuerySessionTables,
} from "@/api/generated/client/query-sessions/query-sessions";
import { unwrapApi } from "@/shared/api/mutator";
import type {
  DatasourceColumn,
  DatasourceTable,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";

export const METADATA_STALE_TIME = 5 * 60_000;

export function useSessionSchemas(sessionId: string, datasourceId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["query", "session", sessionId, "schemas", datasourceId],
    queryFn: async () => {
      // F12 RCP-20261017 Direction A (#010 family): metadata read surface is an {items} envelope.
      const response = await unwrapApi(
        listQuerySessionSchemas(sessionId, {
          datasource_id: datasourceId,
        }),
      );
      return response.items;
    },
    enabled: enabled && sessionId !== "" && datasourceId !== "",
    staleTime: METADATA_STALE_TIME,
  });
}

export function useSessionTables(
  sessionId: string,
  datasourceId: string,
  schemaName: string,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["query", "session", sessionId, "tables", datasourceId, schemaName],
    queryFn: async () => {
      // F12 RCP-20261017 Direction A (#010 family): metadata read surface is an {items} envelope.
      const response = await unwrapApi(
        listQuerySessionTables(sessionId, {
          datasource_id: datasourceId,
          schema_name: schemaName,
        }),
      );
      return response.items;
    },
    enabled: enabled && sessionId !== "" && datasourceId !== "" && schemaName !== "",
    staleTime: METADATA_STALE_TIME,
  });
}

export function useSessionColumns(
  sessionId: string,
  datasourceId: string,
  schemaName: string,
  tableName: string,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["query", "session", sessionId, "columns", datasourceId, schemaName, tableName],
    queryFn: async () => {
      // F12 RCP-20261017 Direction A (#010 family): metadata read surface is an {items} envelope.
      const response = await unwrapApi(
        listQuerySessionColumns(sessionId, {
          datasource_id: datasourceId,
          schema_name: schemaName,
          table_name: tableName,
        }),
      );
      return response.items;
    },
    enabled:
      enabled && sessionId !== "" && datasourceId !== "" && schemaName !== "" && tableName !== "",
    staleTime: METADATA_STALE_TIME,
  });
}

const MAX_ON_DEMAND_SCHEMAS = 5;
const MAX_ON_DEMAND_TABLES = 10;
const FAILED_METADATA_COOLDOWN_MS = 30_000;

function readCachedTablesBySchema(
  queryClient: QueryClient,
  sessionId: string,
  datasourceId: string,
): Map<string, DatasourceTable[]> {
  const result = new Map<string, DatasourceTable[]>();
  const entries = queryClient.getQueriesData<DatasourceTable[]>({
    queryKey: ["query", "session", sessionId, "tables", datasourceId],
  });
  for (const [queryKey, data] of entries) {
    if (!Array.isArray(data)) continue;
    const schemaName = queryKey[5];
    if (typeof schemaName === "string" && schemaName !== "") {
      result.set(schemaName, data);
    }
  }
  return result;
}

function buildCompletionCatalogFromCache(
  queryClient: QueryClient,
  sessionId: string,
  datasourceId: string,
  activeSchema: string,
  schemas: string[],
): SqlCompletionCatalog {
  const tables: SqlCompletionCatalog["tables"] = [];
  const seenTableKeys = new Set<string>();
  const pushTable = (schema: string | null, name: string) => {
    const key = `${schema ?? ""}\u0000${name}`;
    if (seenTableKeys.has(key)) return;
    seenTableKeys.add(key);
    tables.push({ schema, name });
  };

  const tablesBySchema = readCachedTablesBySchema(queryClient, sessionId, datasourceId);
  const activeTables = activeSchema !== "" ? (tablesBySchema.get(activeSchema) ?? []) : [];
  // Expose active-schema tables both unqualified (`users`) and schema-qualified (`app.users`).
  for (const table of activeTables) {
    pushTable(null, table.table_name);
  }
  for (const [schema, rows] of tablesBySchema.entries()) {
    for (const table of rows) {
      pushTable(schema, table.table_name);
    }
  }

  const columns: SqlCompletionCatalog["columns"] = [];
  const seenColumnKeys = new Set<string>();
  const columnEntries = queryClient.getQueriesData<DatasourceColumn[]>({
    queryKey: ["query", "session", sessionId, "columns", datasourceId],
  });
  for (const [queryKey, cols] of columnEntries) {
    if (!Array.isArray(cols)) continue;
    const schemaName = queryKey[5];
    const tableName = queryKey[6];
    if (typeof schemaName !== "string" || typeof tableName !== "string") continue;
    for (const col of cols) {
      const key = `${schemaName}\u0000${tableName}\u0000${col.column_name}`;
      if (seenColumnKeys.has(key)) continue;
      seenColumnKeys.add(key);
      columns.push({ schema: schemaName, table: tableName, name: col.column_name });
    }
  }

  return { schemas, tables, columns };
}

export interface SessionCompletionResult {
  catalog: SqlCompletionCatalog;
  resolveCompletionCatalog: (ctx: {
    qualifier?: string;
    sql: string;
  }) => Promise<SqlCompletionCatalog>;
}

/**
 * Lazy, cache-backed SQL completion catalog for the query workspace:
 * - Eagerly fetches only the active schema's table list (zero multi-schema or
 *   multi-table fan-out on session entry or datasource switch).
 * - Automatically reuses any schemas/tables/columns already expanded in the
 *   left metadata tree via TanStack Query's shared cache.
 * - Lazily fetches non-active schema tables only when referenced via `<schema>.`
 *   or `FROM/JOIN <schema>.<table>`, and lazily fetches single-table columns only
 *   when referenced via `<table_or_alias>.` or `FROM/JOIN <table>`.
 */
export function useSessionCompletionCatalog(
  sessionId: string,
  datasourceId: string,
  activeSchema: string,
  schemas: string[],
  enabled: boolean,
  sql = "",
): SessionCompletionResult {
  const queryClient = useQueryClient();
  const [cacheVersion, setCacheVersion] = useState(0);
  const failedAtRef = useRef<Map<string, number>>(new Map());

  // Reset negative cooldowns when switching session or datasource.
  useEffect(() => {
    failedAtRef.current.clear();
  }, [datasourceId, sessionId]);

  // Eagerly fetch ONLY the active schema's tables (shared queryKey with MetadataTree).
  useSessionTables(sessionId, datasourceId, activeSchema, enabled && activeSchema !== "");

  // Observe cache updates from both MetadataTree expansions and on-demand fetches.
  useEffect(() => {
    if (!enabled || sessionId === "" || datasourceId === "") return;
    return queryClient.getQueryCache().subscribe((event) => {
      if (event.type !== "updated" && event.type !== "removed") return;
      if (event.type === "updated" && event.action.type !== "success") return;
      const rawKey: unknown = event.query.queryKey;
      if (!Array.isArray(rawKey)) return;
      if (
        rawKey[0] === "query" &&
        rawKey[1] === "session" &&
        rawKey[2] === sessionId &&
        (rawKey[3] === "tables" || rawKey[3] === "columns") &&
        rawKey[4] === datasourceId
      ) {
        setCacheVersion((current) => current + 1);
      }
    });
  }, [datasourceId, enabled, queryClient, sessionId]);

  const ensureSchemaTables = useCallback(
    async (schemaName: string): Promise<DatasourceTable[]> => {
      if (!enabled || sessionId === "" || datasourceId === "" || schemaName === "") return [];
      const failKey = `tables:${schemaName}`;
      const lastFailed = failedAtRef.current.get(failKey);
      if (lastFailed !== undefined && Date.now() - lastFailed < FAILED_METADATA_COOLDOWN_MS) {
        return [];
      }
      try {
        return await queryClient.query({
          queryKey: ["query", "session", sessionId, "tables", datasourceId, schemaName],
          queryFn: async () => {
            const response = await unwrapApi(
              listQuerySessionTables(sessionId, {
                datasource_id: datasourceId,
                schema_name: schemaName,
              }),
            );
            return response.items;
          },
          staleTime: METADATA_STALE_TIME,
          retry: false,
        });
      } catch {
        failedAtRef.current.set(failKey, Date.now());
        return [];
      }
    },
    [datasourceId, enabled, queryClient, sessionId],
  );

  const ensureTableColumns = useCallback(
    async (schemaName: string, tableName: string): Promise<DatasourceColumn[]> => {
      if (
        !enabled ||
        sessionId === "" ||
        datasourceId === "" ||
        schemaName === "" ||
        tableName === ""
      ) {
        return [];
      }
      const failKey = `columns:${schemaName}.${tableName}`;
      const lastFailed = failedAtRef.current.get(failKey);
      if (lastFailed !== undefined && Date.now() - lastFailed < FAILED_METADATA_COOLDOWN_MS) {
        return [];
      }
      try {
        return await queryClient.query({
          queryKey: ["query", "session", sessionId, "columns", datasourceId, schemaName, tableName],
          queryFn: async () => {
            const response = await unwrapApi(
              listQuerySessionColumns(sessionId, {
                datasource_id: datasourceId,
                schema_name: schemaName,
                table_name: tableName,
              }),
            );
            return response.items;
          },
          staleTime: METADATA_STALE_TIME,
          retry: false,
        });
      } catch {
        failedAtRef.current.set(failKey, Date.now());
        return [];
      }
    },
    [datasourceId, enabled, queryClient, sessionId],
  );

  const resolveCompletionCatalog = useCallback(
    async (ctx: { qualifier?: string; sql: string }): Promise<SqlCompletionCatalog> => {
      if (!enabled || sessionId === "" || datasourceId === "") {
        return { schemas, tables: [], columns: [] };
      }

      const refs = extractSqlMetadataReferences(ctx.sql);
      const schemaByLower = new Map<string, string>();
      for (const schema of schemas) {
        schemaByLower.set(schema.toLowerCase(), schema);
      }

      // 1. Lazily load tables for any known schema explicitly referenced via `<schema>.` or `FROM/JOIN <schema>.<table>`.
      const schemasToFetch: string[] = [];
      const seenSchemaFetch = new Set<string>();
      const considerSchema = (candidate: string | undefined) => {
        if (candidate === undefined || candidate === "") return;
        const matched = schemaByLower.get(candidate.toLowerCase());
        if (matched === undefined || seenSchemaFetch.has(matched)) return;
        seenSchemaFetch.add(matched);
        schemasToFetch.push(matched);
      };
      considerSchema(ctx.qualifier);
      for (const s of refs.schemas) considerSchema(s);
      for (const q of refs.qualifiers) considerSchema(q);

      if (schemasToFetch.length > 0) {
        await Promise.all(
          schemasToFetch.slice(0, MAX_ON_DEMAND_SCHEMAS).map((schema) => ensureSchemaTables(schema)),
        );
      }

      // 2. Lazily load columns ONLY for explicitly referenced tables that exist in cached schema tables.
      const tablesBySchema = readCachedTablesBySchema(queryClient, sessionId, datasourceId);
      const findCachedTable = (
        explicitSchema: string | null,
        tableIdent: string,
      ): { schema: string; table: string } | null => {
        const lowerTable = tableIdent.toLowerCase();
        const searchSchemas: string[] = [];
        if (explicitSchema !== null && explicitSchema !== "") {
          const matched = schemaByLower.get(explicitSchema.toLowerCase()) ?? explicitSchema;
          searchSchemas.push(matched);
        } else {
          if (activeSchema !== "") searchSchemas.push(activeSchema);
          for (const cachedSchema of tablesBySchema.keys()) {
            if (cachedSchema !== activeSchema) searchSchemas.push(cachedSchema);
          }
        }
        for (const schema of searchSchemas) {
          const rows = tablesBySchema.get(schema);
          if (rows === undefined) continue;
          const hit = rows.find((row) => row.table_name.toLowerCase() === lowerTable);
          if (hit !== undefined) {
            return { schema, table: hit.table_name };
          }
        }
        return null;
      };

      const tablesToFetch: Array<{ schema: string; table: string }> = [];
      const seenTableFetch = new Set<string>();
      const considerTable = (explicitSchema: string | null, tableIdent: string) => {
        const resolved = findCachedTable(explicitSchema, tableIdent);
        if (resolved === null) return;
        const key = `${resolved.schema}\u0000${resolved.table}`;
        if (seenTableFetch.has(key)) return;
        seenTableFetch.add(key);
        tablesToFetch.push(resolved);
      };

      for (const ref of refs.tables) {
        considerTable(ref.schema, ref.table);
      }

      const considerQualifierAsTable = (qualifier: string | undefined) => {
        if (qualifier === undefined || qualifier === "") return;
        const aliasRef = refs.aliases[qualifier.toLowerCase()];
        if (aliasRef !== undefined) {
          considerTable(aliasRef.schema, aliasRef.table);
          return;
        }
        considerTable(null, qualifier);
      };
      considerQualifierAsTable(ctx.qualifier);
      for (const q of refs.qualifiers) considerQualifierAsTable(q);

      if (tablesToFetch.length > 0) {
        await Promise.all(
          tablesToFetch
            .slice(0, MAX_ON_DEMAND_TABLES)
            .map(({ schema, table }) => ensureTableColumns(schema, table)),
        );
      }

      return buildCompletionCatalogFromCache(
        queryClient,
        sessionId,
        datasourceId,
        activeSchema,
        schemas,
      );
    },
    [
      activeSchema,
      datasourceId,
      enabled,
      ensureSchemaTables,
      ensureTableColumns,
      queryClient,
      schemas,
      sessionId,
    ],
  );

  // Lazily resolve schemas/tables referenced by the live SQL text (FROM/JOIN or `<ident>.`).
  useEffect(() => {
    if (!enabled || sessionId === "" || datasourceId === "" || sql.trim() === "") return;
    const refs = extractSqlMetadataReferences(sql);
    if (refs.schemas.length === 0 && refs.tables.length === 0 && refs.qualifiers.length === 0) {
      return;
    }
    const timer = window.setTimeout(() => {
      void resolveCompletionCatalog({ sql });
    }, 100);
    return () => {
      window.clearTimeout(timer);
    };
  }, [cacheVersion, datasourceId, enabled, resolveCompletionCatalog, sessionId, sql]);

  const catalog = useMemo<SqlCompletionCatalog>(() => {
    void cacheVersion;
    if (!enabled || sessionId === "" || datasourceId === "") {
      return { schemas, tables: [], columns: [] };
    }
    return buildCompletionCatalogFromCache(
      queryClient,
      sessionId,
      datasourceId,
      activeSchema,
      schemas,
    );
  }, [activeSchema, cacheVersion, datasourceId, enabled, queryClient, schemas, sessionId]);

  return { catalog, resolveCompletionCatalog };
}
