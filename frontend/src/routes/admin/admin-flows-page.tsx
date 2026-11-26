import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Pencil, Plus, Trash2, Workflow } from "lucide-react";
import type { Flow } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { FlowFormDialog } from "@/routes/admin/admin-flow-form-dialog";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { Card, CardContent } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import {
  useDeleteFlow,
  useFlows,
} from "@/features/admin/use-admin";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";

/**
 * Flow Management (route /admin/flows; migration contract §2 maps legacy
 * /manager/flow here, §3 field mapping). Two STRICTLY separate forms:
 * - Change flow: Name / Enabled / one Rule Set -> 1..N serial Stages, each Stage binding one Datasource
 *   + Schema Mapping + 1..10 Approval Steps + Executors (user UUIDs).
 * - Query flow: Name / Enabled -> Datasources x (query/export capabilities) -> 1..10 Access Approval Steps.
 *   Must NOT expose SQL, Rule Set, or Executor fields (§3).
 * The old flow editor's step-type model (submit/approve/execute mixed types, max 5
 * audit layers) is gone: v4 stages carry the execution actors and approval
 * steps uniformly. Editing shows the "does not affect submitted orders" note (§3). Query
 * flows additionally expose the per-datasource sensitive-column vocabulary
 * (Q006 admin face; <=256 literal columns, case-fold normalized server-side).
 */

export default function AdminFlowsPage() {
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const pager = useCursorPage(20);
  const [terms, setTerms] = useState<SearchTerm[]>([]);
  const [live, setLive] = useState<SearchTerm | null>(null);
  const effectiveTerms = useMemo(
    () => (live === null ? terms : [...terms, live]),
    [live, terms],
  );
  // §18.73 + RCP-20261105: name / free text go through server-side q; flow type and enabled state are valued
  // server-side column filters (flow_type / enabled), performing cross-page narrowing on the server.
  // "Topology" is a localized composite display string and is no longer a searchable field.
  const serverQ = useMemo(
    () =>
      [...effectiveTerms]
        .reverse()
        .find((term) => term.field === null || term.field === "name")
        ?.text.trim() ?? "",
    [effectiveTerms],
  );
  const flowTypeParam = useMemo<"change_review" | "query_access" | undefined>(() => {
    const TYPES = { [t("adminFlows.kind.change")]: "change_review", [t("adminFlows.kind.query")]: "query_access" } as const;
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "kind" && candidate.text.trim() !== "");
    return term === undefined ? undefined : TYPES[term.text as keyof typeof TYPES];
  }, [effectiveTerms, t]);
  const enabledParam = useMemo<boolean | undefined>(() => {
    const LABELS = { [t("adminFlows.enabled")]: true, [t("adminFlows.disabled")]: false } as const;
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "enabled" && candidate.text.trim() !== "");
    return term === undefined ? undefined : LABELS[term.text as keyof typeof LABELS];
  }, [effectiveTerms, t]);

  const flowsQuery = useFlows(isAdmin, {
    limit: pager.pageSize,
    after: pager.after,
    q: serverQ || undefined,
    flowType: flowTypeParam,
    enabled: enabledParam,
  });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const flowsLoaded = !flowsQuery.isPending && !flowsQuery.isError;
  const [editing, setEditing] = useState<Flow | null>(null);
  const [createType, setCreateType] = useState<"change_review" | "query_access" | null>(null);

  const searchAccessors = useMemo<SearchAccessor<Flow>[]>(
    () => [
      {
        key: "name",
        label: t("adminFlows.column.name"),
        value: (flow) => flow.name,
      },
      {
        key: "kind",
        label: t("adminFlows.column.kind"),
        value: (flow) =>
          flow.flow_type === "query_access"
            ? `query_access ${t("adminFlows.kind.query")}`
            : `change ${t("adminFlows.kind.change")}`,
        options: [
          { value: "change", label: t("adminFlows.kind.change") },
          { value: "query_access", label: t("adminFlows.kind.query") },
        ],
      },
      {
        key: "enabled",
        label: t("adminFlows.column.enabled"),
        value: (flow) =>
          flow.enabled ? `enabled ${t("adminFlows.enabled")}` : `disabled ${t("adminFlows.disabled")}`,
        options: [
          { value: "enabled", label: t("adminFlows.enabled") },
          { value: "disabled", label: t("adminFlows.disabled") },
        ],
      },
    ],
    [t],
  );
  const hasSearchFilter = effectiveTerms.some((term) => term.text.trim() !== "");
  const flows = filterBySearchTerms(
    flowsQuery.data?.items ?? [],
    effectiveTerms,
    searchAccessors,
  );
  // Search narrows server-side/client-side; tween whole-row position changes when the row set changes.
  const flowsBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(flows.map((flow) => flow.id)),
  );

  return (
    <PageLayout data-testid="admin-flows-page">
      <PageHeader
        title={t("adminFlows.title")}
        description={t("adminFlows.description")}
        icon={Workflow}
      />

      <Card className="overflow-visible">
        <ListToolbar
          busy={flowsQuery.isFetching && !flowsQuery.isPending}
          fill
          actions={
            <>
              <Button variant="outline" onClick={() => { setCreateType("change_review"); }} data-testid="admin-flows-create-change">
                <Plus />
                {t("adminFlows.createChange")}
              </Button>
              <Button onClick={() => { setCreateType("query_access"); }} data-testid="admin-flows-create-query">
                <Plus />
                {t("adminFlows.createQuery")}
              </Button>
            </>
          }
        >
          {/* Owner feedback 2026-11-05: ECS-style multi-field search box on
              the left, create actions on the right inside the toolbar. */}
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
            placeholder={t("adminFlows.searchPlaceholder")}
            testId="admin-flows-search"
            className="w-full min-w-0 flex-1"
          />
        </ListToolbar>
        <CardContent className="px-0!">
          {flowsQuery.isPending ? (
            <LoadingState variant="table" columns={5} />
          ) : flowsQuery.error !== null ? (
            <ErrorState error={flowsQuery.error} operationId="listFlows" onRetry={() => void flowsQuery.refetch()} />
          ) : (
            <>
              {flows.length === 0 ? (
                <EmptyState
                  title={hasSearchFilter ? t("table.emptySearch") : t("adminFlows.empty")}
                  filtered={hasSearchFilter}
                  testId="admin-flows-empty"
                />
              ) : (
              <Table data-testid="admin-flows-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("adminFlows.column.name")}</TableHead>
                    <TableHead>{t("adminFlows.column.kind")}</TableHead>
                    <TableHead>{t("adminFlows.column.enabled")}</TableHead>
                    <TableHead>{t("adminFlows.column.structure")}</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody ref={flowsBodyRef} stale={flowsQuery.isFetching && flowsQuery.isPlaceholderData}>
                  {flows.map((flow) => (
                    <FlowRow key={flow.id} flow={flow} onEdit={() => { setEditing(flow); }} />
                  ))}
                </TableBody>
              </Table>
              )}
              {flowsLoaded ? (
                <TablePagination
                  busy={flowsQuery.isFetching && flowsQuery.isPlaceholderData}
                  page={pager.pageNo}
                  hasMore={flowsQuery.data.page.has_more}
                  isFirst={pager.isFirst}
                  onPrev={pager.goPrev}
                  onJumpPage={pager.goToPage}
                  onNext={() => {
                    pager.pushCursor(flowsQuery.data.page.next_cursor);
                  }}
                  pageSize={pager.pageSize}
                  onPageSizeChange={pager.setPageSize}
                  rowsOnPage={flowsQuery.data.items.length}
                  testIdPrefix="flows"
                />
              ) : null}
            </>
          )}
        </CardContent>
      </Card>

      {createType !== null || editing !== null ? (
        <FlowFormDialog
          key={editing?.id ?? `create-${createType ?? "change_review"}`}
          flowType={editing?.flow_type ?? createType ?? "change_review"}
          editing={editing}
          onClose={() => {
            setEditing(null);
            setCreateType(null);
          }}
        />
      ) : null}
    </PageLayout>
  );
}

function FlowRow({ flow, onEdit }: { flow: Flow; onEdit: () => void }) {
  const { t } = useTranslation();
  const deleteFlow = useDeleteFlow();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);

  const isQuery = flow.flow_type === "query_access";

  return (
    <>
      <TableRow data-testid={`admin-flow-row-${flow.id}`} data-row-key={flow.id}>
        <TableCell className="font-semibold text-foreground">{flow.name}</TableCell>
        <TableCell>
          <Badge variant="outline">
            {isQuery ? t("adminFlows.kind.query") : t("adminFlows.kind.change")}
          </Badge>
        </TableCell>
        <TableCell>
          <StatusBadge tone={flow.enabled ? "emerald" : "stone"}>
            {flow.enabled ? t("adminFlows.enabled") : t("adminFlows.disabled")}
          </StatusBadge>
        </TableCell>
        <TableCell className="text-muted-foreground tabular-nums">
          {isQuery
            ? t("adminFlows.structure.query", {
                datasources: flow.query_capabilities?.length ?? 0,
                steps: flow.approval_steps?.length ?? 0,
              })
            : t("adminFlows.structure.change", {
                stages: flow.stages?.length ?? 0,
              })}
        </TableCell>
        <TableCell className="text-right">
          <TableRowActions testId={`admin-flow-actions-${flow.id}`}>
            <DropdownMenuItem onClick={onEdit} data-testid={`admin-flow-edit-${flow.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              onClick={() => {
                setErrorText(null);
                setConfirmOpen(true);
              }}
              data-testid={`admin-flow-delete-${flow.id}`}
            >
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </TableRowActions>
        </TableCell>
      </TableRow>

      <Dialog open={confirmOpen} onOpenChange={(next) => { if (!next) setConfirmOpen(false); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("adminFlows.delete.title", { name: flow.name })}</DialogTitle>
            <DialogDescription>{t("adminFlows.delete.description")}</DialogDescription>
          </DialogHeader>
          {errorText !== null && (
            <Alert variant="destructive">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setConfirmOpen(false); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant="destructive"
              pending={deleteFlow.isPending}
              onClick={() => {
                deleteFlow.mutate(
                  { flowId: flow.id, version: flow.version },
                  {
                    onSuccess: () => {
                      toast.success(t("common.delete"), { description: flow.name });
                      setConfirmOpen(false);
                    },
                    onError: (error) => {
                      const msg = describeErrorText(describeError(error, "deleteFlow"));
                      setErrorText(msg);
                      toast.error(msg);
                    },
                  },
                );
              }}
              data-testid="admin-flow-delete-confirm"
            >
              {t("adminFlows.delete.confirm")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
