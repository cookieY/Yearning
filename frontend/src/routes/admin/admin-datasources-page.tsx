import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Database, Plus } from "lucide-react";
import type { Datasource } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useDatasources } from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import {
  DatasourceDiagnosticsConfigDialog,
  DeleteConfirmDialog,
} from "@/routes/admin/admin-datasource-dialogs";
import { DatasourceFormDialog } from "@/routes/admin/admin-datasource-form-dialog";
import { DatasourceRow } from "@/routes/admin/admin-datasource-table-rows";
import {
  filterBySearchTerms,
  GlobalSearchBar,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { EmptyState } from "@/shared/components/status/empty-state";
import { LoadingState } from "@/shared/components/status/loading-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent } from "@/shared/components/ui/card";
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";

/** Closed set of engines: search condition options and the server-side engine filter param share the same mapping
 * (RCP-20261105-ADMIN-LIST-FIELD-FILTERS). */
const ENGINE_VALUES = {
  mysql: "mysql", postgresql: "postgresql", tidb: "tidb", oceanbase: "oceanbase", polardb: "polardb",
} as const;

export default function AdminDatasourcesPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const pager = useCursorPage(20);
  const [terms, setTerms] = useState<SearchTerm[]>([]);
  const [live, setLive] = useState<SearchTerm | null>(null);
  const effectiveTerms = useMemo(
    () => (live === null ? terms : [...terms, live]),
    [live, terms],
  );
  // §18.73 + RCP-20261105: free text / name / endpoint go through server-side q (q covers name/host);
  // engine and credential mode are valued server-side column filters (engine / credential_mode),
  // performing cross-page narrowing on the server. Composite display strings like flow reference counts are no longer searchable fields.
  const serverQ = useMemo(
    () =>
      [...effectiveTerms]
        .reverse()
        .find((term) => term.field === null || term.field === "name" || term.field === "endpoint")
        ?.text.trim() ?? "",
    [effectiveTerms],
  );
  const engineParam = useMemo<(typeof ENGINE_VALUES)[keyof typeof ENGINE_VALUES] | undefined>(() => {
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "engine" && candidate.text.trim() !== "");
    return term === undefined ? undefined : ENGINE_VALUES[term.text as keyof typeof ENGINE_VALUES];
  }, [effectiveTerms]);
  const credentialModeParam = useMemo<"read_write" | "read_only" | undefined>(() => {
    const MODES = { [t("admin.datasources.credModeReadWrite")]: "read_write", [t("admin.datasources.credModeReadOnly")]: "read_only" } as const;
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "credentialStatus" && candidate.text.trim() !== "");
    return term === undefined ? undefined : MODES[term.text as keyof typeof MODES];
  }, [effectiveTerms, t]);

  const query = useDatasources(enabled, {
    limit: pager.pageSize,
    after: pager.after,
    q: serverQ || undefined,
    engine: engineParam,
    credentialMode: credentialModeParam,
  });
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Datasource | null>(null);
  const [deleting, setDeleting] = useState<Datasource | null>(null);
  const [configuringDiagnostics, setConfiguringDiagnostics] = useState<Datasource | null>(null);
  // Expanded capability row: datasource id + the probe task id that keyed
  // the latest capabilities fetch (a re-test starts a fresh key).
  const [probe, setProbe] = useState<{ datasourceId: string; taskId: string } | null>(null);
  const searchAccessors = useMemo<SearchAccessor<Datasource>[]>(
    () => [
      {
        key: "name",
        label: t("admin.datasources.name"),
        value: (ds) => `${ds.name} ${ds.enabled ? "" : t("admin.datasources.disabled")}`,
      },
      {
        // Engines form a closed set: options map directly to the server-side engine filter parameter (RCP-20261105).
        key: "engine",
        label: t("admin.datasources.engine"),
        value: (ds) => ds.engine,
        options: Object.keys(ENGINE_VALUES).map((engine) => ({ value: engine, label: engine })),
      },
      {
        key: "endpoint",
        label: t("admin.datasources.endpoint"),
        value: (ds) =>
          `${ds.host}:${String(ds.port)} ${
            ds.tls_verified
              ? t("admin.datasources.tlsVerifiedBadge")
              : t("admin.datasources.tlsPlaintextBadge")
          }`,
      },
      {
        key: "credentialStatus",
        label: t("admin.datasources.credentialStatus"),
        value: (ds) =>
          ds.credential_status.execution === true
            ? `read_write ${t("admin.datasources.credModeReadWrite")}`
            : `read_only ${t("admin.datasources.credModeReadOnly")}`,
        options: [
          { value: "read_write", label: t("admin.datasources.credModeReadWrite") },
          { value: "read_only", label: t("admin.datasources.credModeReadOnly") },
        ],
      },
    ],
    [t],
  );
  const hasSearchFilter = effectiveTerms.some((term) => term.text.trim() !== "");
  const datasources = filterBySearchTerms(
    query.data?.items ?? [],
    effectiveTerms,
    searchAccessors,
  );
  // Tween whole-row position changes when search/pagination alters the row set; capability probe rows carry no data-row-key and do not participate.
  const datasourcesBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(datasources.map((datasource) => datasource.id)),
  );

  return (
    <PageLayout>
      <PageHeader
        title={t("admin.datasources.title")}
        description={t("admin.datasources.description")}
        icon={Database}
      />
      <Card className="overflow-visible">
        <ListToolbar
          busy={query.isFetching && !query.isPending}
          fill
          actions={
            <Button
              onClick={() => {
                setEditing(null);
                setDialogOpen(true);
              }}
              data-testid="ds-create"
            >
              <Plus /> {t("admin.datasources.createTitle")}
            </Button>
          }
        >
          {/* Owner feedback 2026-11-05: ECS-style multi-field search box on
              the left, create action on the right inside the toolbar. */}
          <GlobalSearchBar
            fields={searchAccessors}
            terms={terms}
            onTermsChange={(next) => {
              setTerms(next);
              pager.reset();
            }}
            onLiveChange={(next) => {
              setLive(next);
              pager.reset();
            }}
            placeholder={t("admin.datasources.searchPlaceholder")}
            testId="admin-datasources-search"
            className="w-full min-w-0 flex-1"
          />
        </ListToolbar>
        <CardContent className="px-0!">
          {query.isPending ? (
            <LoadingState variant="table" columns={6} />
          ) : query.isError ? (
            <ErrorState error={query.error} operationId="listDatasources" onRetry={() => void query.refetch()} />
          ) : datasources.length === 0 && hasSearchFilter ? (
            <EmptyState
              title={t("table.emptySearch")}
              filtered
              testId="admin-datasources-search-empty"
            />
          ) : datasources.length === 0 ? (
            <EmptyState
              title={t("admin.datasources.emptyTitle")}
              description={t("admin.datasources.emptyDescription")}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("admin.datasources.name")}</TableHead>
                  <TableHead>{t("admin.datasources.engine")}</TableHead>
                  <TableHead>{t("admin.datasources.endpoint")}</TableHead>
                  <TableHead>{t("admin.datasources.credentialStatus")}</TableHead>
                  <TableHead>{t("admin.datasources.references")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody ref={datasourcesBodyRef} stale={query.isFetching && query.isPlaceholderData}>
                {datasources.map((datasource) => (
                  <DatasourceRow
                    key={datasource.id}
                    datasource={datasource}
                    probeKey={probe !== null && probe.datasourceId === datasource.id ? probe.taskId : null}
                    onCloseProbe={() => {
                      setProbe((current) => (current?.datasourceId === datasource.id ? null : current));
                    }}
                    onEdit={() => {
                      setEditing(datasource);
                      setDialogOpen(true);
                    }}
                    onDelete={() => { setDeleting(datasource); }}
                    onConfigureDiagnostics={() => { setConfiguringDiagnostics(datasource); }}
                    onTestStarted={(taskId) => { setProbe({ datasourceId: datasource.id, taskId }); }}
                  />
                ))}
              </TableBody>
            </Table>
          )}
          {!query.isPending && !query.isError ? (
            <TablePagination
              busy={query.isFetching && query.isPlaceholderData}
              page={pager.pageNo}
              hasMore={query.data.page.has_more}
              isFirst={pager.isFirst}
              onPrev={pager.goPrev}
              onJumpPage={pager.goToPage}
              onNext={() => {
                pager.pushCursor(query.data.page.next_cursor);
              }}
              pageSize={pager.pageSize}
              onPageSizeChange={pager.setPageSize}
              rowsOnPage={query.data.items.length}
              testIdPrefix="datasources"
            />
          ) : null}
        </CardContent>
      </Card>
      {dialogOpen ? (
        <DatasourceFormDialog
          key={editing?.id ?? "new"}
          editing={editing}
          onClose={() => {
            setDialogOpen(false);
            setEditing(null);
          }}
        />
      ) : null}
      {deleting !== null ? (
        <DeleteConfirmDialog
          key={deleting.id}
          datasource={deleting}
          onClose={() => { setDeleting(null); }}
        />
      ) : null}
      {configuringDiagnostics !== null ? (
        <DatasourceDiagnosticsConfigDialog
          key={configuringDiagnostics.id}
          datasource={configuringDiagnostics}
          onClose={() => { setConfiguringDiagnostics(null); }}
        />
      ) : null}
    </PageLayout>
  );
}
