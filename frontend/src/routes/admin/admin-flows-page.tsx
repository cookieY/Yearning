import i18next from "@/shared/i18n";
import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { ArrowDown, ArrowUp, Database, EyeOff, Pencil, Plus, Trash2, Workflow } from "lucide-react";
import { Checkbox } from "@/shared/components/ui/checkbox";
import type { Flow, FlowMaskingRule, User } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
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
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { Textarea } from "@/shared/components/ui/textarea";
import {
  useCreateFlow,
  useDatasources,
  useDeleteFlow,
  useFlows,
  useFlowMaskingRules,
  useReplaceFlow,
  useReplaceMaskingRule,
  useRuleSets,
  useUsers,
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

function describeErrorText(display: { messageKey: string; requestId: string | null }): string {
  // Translated through the shared i18n bundle so users never see raw keys.
  const text = i18next.t(display.messageKey);
  return display.requestId === null ? text : `${text} (${display.requestId})`;
}

interface StageDraft {
  position: number;
  datasourceId: string;
  approvalSteps: { position: number; actorIds: string[] }[];
  executionActorIds: string[];
}

interface FlowFormState {
  name: string;
  enabled: boolean;
  ruleSetId: string;
  stages: StageDraft[];
  queryCapabilities: { datasourceId: string; canExport: boolean }[];
  approvalSteps: { position: number; actorIds: string[] }[];
}

const EMPTY_FORM: FlowFormState = {
  name: "",
  enabled: true,
  ruleSetId: "",
  stages: [],
  queryCapabilities: [],
  approvalSteps: [],
};

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
            ? t("adminFlows.kind.query")
            : t("adminFlows.kind.change"),
        options: [
          { label: t("adminFlows.kind.change") },
          { label: t("adminFlows.kind.query") },
        ],
      },
      {
        key: "enabled",
        label: t("adminFlows.column.enabled"),
        value: (flow) =>
          flow.enabled ? t("adminFlows.enabled") : t("adminFlows.disabled"),
        options: [
          { label: t("adminFlows.enabled") },
          { label: t("adminFlows.disabled") },
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

      <FlowFormDialog
        open={createType !== null || editing !== null}
        flowType={editing?.flow_type ?? createType ?? "change_review"}
        editing={editing}
        onClose={() => {
          setEditing(null);
          setCreateType(null);
        }}
      />
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

function FlowFormDialog({
  open,
  flowType,
  editing,
  onClose,
}: {
  open: boolean;
  flowType: "change_review" | "query_access";
  editing: Flow | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const datasourcesQuery = useDatasources(isAdmin);
  const usersQuery = useUsers(isAdmin);
  const ruleSetsQuery = useRuleSets(isAdmin);
  const createFlow = useCreateFlow();
  const replaceFlow = useReplaceFlow();

  const [form, setForm] = useState<FlowFormState>(EMPTY_FORM);
  const [openFor, setOpenFor] = useState<string | null>(null);
  const [errorText, setErrorText] = useState<string | null>(null);
  const [queryDsSearch, setQueryDsSearch] = useState("");
  const [stageDsSearchByIndex, setStageDsSearchByIndex] = useState<Record<number, string>>({});

  const formKey = editing?.id ?? `create-${flowType}`;
  if (open && openFor !== formKey) {
    setOpenFor(formKey);
    setQueryDsSearch("");
    setStageDsSearchByIndex({});
    setForm(
      editing === null
        ? { ...EMPTY_FORM }
        : {
            name: editing.name,
            enabled: editing.enabled,
            ruleSetId: editing.rule_set_id ?? "",
            stages: (editing.stages ?? []).map((stage) => ({
              position: stage.position,
              datasourceId: stage.datasource_id,
              approvalSteps: stage.approval_steps.map((step) => ({
                position: step.position,
                actorIds: step.actors.map((actor) => actor.user_id),
              })),
              executionActorIds: stage.execution_actors.map((actor) => actor.user_id),
            })),
            queryCapabilities: (editing.query_capabilities ?? []).map((capability) => ({
              datasourceId: capability.datasource_id,
              canExport: capability.can_export,
            })),
            approvalSteps: (editing.approval_steps ?? []).map((step) => ({
              position: step.position,
              actorIds: step.actors.map((actor) => actor.user_id),
            })),
          },
    );
    setErrorText(null);
  }
  if (!open && openFor !== null) setOpenFor(null);

  const datasources = useMemo(() => datasourcesQuery.data?.items ?? [], [datasourcesQuery.data]);
  const users = usersQuery.data?.items ?? [];
  const ruleSets = ruleSetsQuery.data?.items ?? [];

  // BUG-005 Option A: frontend mirror of the same-engine validation (backed by authoritative backend check).
  const engineMismatch = useMemo(() => {
    if (flowType !== "change_review") return false;
    const engines = form.stages
      .map((stage) => datasources.find((ds) => ds.id === stage.datasourceId))
      .map((ds) => (ds ? `${ds.engine}/${ds.compatibility_mode}` : ""));
    return engines.length >= 2 && engines.some((e) => e !== engines[0]);
  }, [datasources, flowType, form]);

  const valid = useMemo(() => {
    if (form.name.trim() === "") return false;
    if (flowType === "change_review") {
      if (form.ruleSetId === "") return false;
      if (engineMismatch) return false;
      return (
        form.stages.length >= 1 &&
        form.stages.every((stage) => {
          const ds = datasources.find((item) => item.id === stage.datasourceId);
          return (
            stage.datasourceId !== "" &&
            ds !== undefined &&
            ds.enabled &&
            stage.approvalSteps.length >= 1 &&
            stage.approvalSteps.length <= 10 &&
            stage.approvalSteps.every((step) => step.actorIds.length >= 1) &&
            stage.executionActorIds.length >= 1
          );
        })
      );
    }
    return (
      form.queryCapabilities.length >= 1 &&
      form.queryCapabilities.every((capability) => {
        const ds = datasources.find((item) => item.id === capability.datasourceId);
        return ds !== undefined && ds.enabled;
      }) &&
      form.approvalSteps.length >= 1 &&
      form.approvalSteps.length <= 10 &&
      form.approvalSteps.every((step) => step.actorIds.length >= 1)
    );
  }, [datasources, engineMismatch, flowType, form]);

  const missingFlowItems = useMemo(() => {
    const items: string[] = [];
    if (form.name.trim() === "") {
      items.push(t("adminFlows.column.name"));
    }
    if (flowType === "change_review") {
      if (form.ruleSetId === "") {
        items.push(t("adminFlows.ruleSet"));
      }
      if (form.stages.length < 1) {
        items.push(t("adminFlows.missingStages"));
      }
      for (const stage of form.stages) {
        const index = String(stage.position);
        const ds = datasources.find((item) => item.id === stage.datasourceId);
        if (stage.datasourceId === "" || ds === undefined) {
          items.push(t("adminFlows.missingStageDatasource", { index }));
        } else if (!ds.enabled) {
          items.push(t("adminFlows.missingStageDisabledDatasource", { index }));
        }
        const stepsValid =
          stage.approvalSteps.length >= 1 &&
          stage.approvalSteps.length <= 10 &&
          stage.approvalSteps.every((step) => step.actorIds.length >= 1);
        if (!stepsValid) {
          items.push(t("adminFlows.missingStageApprovalSteps", { index }));
        }
        if (stage.executionActorIds.length < 1) {
          items.push(t("adminFlows.missingStageExecutors", { index }));
        }
      }
    } else {
      if (form.queryCapabilities.length < 1) {
        items.push(t("adminFlows.missingQueryDatasources"));
      } else if (
        form.queryCapabilities.some((capability) => {
          const ds = datasources.find((item) => item.id === capability.datasourceId);
          return ds === undefined || !ds.enabled;
        })
      ) {
        items.push(t("adminFlows.missingQueryDisabledDatasource"));
      }
      const stepsValid =
        form.approvalSteps.length >= 1 &&
        form.approvalSteps.length <= 10 &&
        form.approvalSteps.every((step) => step.actorIds.length >= 1);
      if (!stepsValid) {
        items.push(t("adminFlows.missingQueryApprovalSteps"));
      }
    }
    return items;
  }, [datasources, flowType, form, t]);

  const submit = async () => {
    setErrorText(null);
    const body =
      flowType === "change_review"
        ? {
            name: form.name,
            flow_type: flowType,
            enabled: form.enabled,
            rule_set_id: form.ruleSetId === "" ? null : form.ruleSetId,
            stages: form.stages.map((stage) => ({
              position: stage.position,
              datasource_id: stage.datasourceId,
              approval_steps: stage.approvalSteps.map((step) => ({
                position: step.position,
                actors: step.actorIds.map((user_id) => ({ user_id })),
              })),
              execution_actors: stage.executionActorIds.map((user_id) => ({ user_id })),
            })),
          }
        : {
            name: form.name,
            flow_type: flowType,
            enabled: form.enabled,
            query_capabilities: form.queryCapabilities.map((capability) => ({
              datasource_id: capability.datasourceId,
              can_query: true as const,
              can_export: capability.canExport,
            })),
            approval_steps: form.approvalSteps.map((step) => ({
              position: step.position,
              actors: step.actorIds.map((user_id) => ({ user_id })),
            })),
          };
    try {
      if (editing === null) {
        await createFlow.mutateAsync(body);
      } else {
        await replaceFlow.mutateAsync({ flowId: editing.id, version: editing.version, body });
      }
      toast.success(editing === null ? t("common.createdToast") : t("common.savedToast"), { description: form.name });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, editing === null ? "createFlow" : "replaceFlow"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent
        className="max-h-[min(760px,86vh)] sm:max-w-3xl"
        viewportClassName="max-h-[calc(min(760px,86vh)-10.5rem)]"
      >
        <DialogHeader>
          <DialogTitle>
            {editing === null
              ? flowType === "change_review"
                ? t("adminFlows.createChange")
                : t("adminFlows.createQuery")
              : t("adminFlows.editTitle", { name: editing.name })}
          </DialogTitle>
          <DialogDescription>{t("adminFlows.formDescription")}</DialogDescription>
        </DialogHeader>

        <Alert>
          <AlertDescription>{t("adminFlows.noRetroactiveNote")}</AlertDescription>
        </Alert>

        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="flow-name">
              {t("adminFlows.column.name")}
              <span className="text-destructive" aria-hidden>*</span>
            </Label>
            <Input
              id="flow-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              maxLength={128}
              aria-required="true"
              data-testid="flow-name"
            />
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <Checkbox
              checked={form.enabled}
              onCheckedChange={(checked) => { setForm({ ...form, enabled: checked }); }}
              data-testid="flow-enabled"
            />
            {t("adminFlows.column.enabled")}
          </label>

          {flowType === "change_review" ? (
            <>
              <div className="flex flex-col gap-2">
                <Label>
                  {t("adminFlows.ruleSet")}
                  <span className="text-destructive" aria-hidden>*</span>
                </Label>
                {/* §18.59: Base UI Select (owner ruling "must use UI components"). */}
                <Select
                  value={form.ruleSetId === "" ? null : form.ruleSetId}
                  onValueChange={(value) => { setForm({ ...form, ruleSetId: value ?? "" }); }}
                  items={Object.fromEntries(ruleSets.map((ruleSet) => [ruleSet.id, ruleSet.name]))}
                >
                  <SelectTrigger
                    className="h-9 w-full text-sm"
                    aria-label={t("adminFlows.ruleSet")}
                    aria-required="true"
                    data-testid="flow-rule-set"
                  >
                    <SelectValue placeholder={t("adminFlows.noRuleSet")} />
                  </SelectTrigger>
                  <SelectContent>
                    {ruleSets.map((ruleSet) => (
                      <SelectItem key={ruleSet.id} value={ruleSet.id}>
                        {ruleSet.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs" data-testid="flow-rule-set-hint">
                  {t("adminFlows.ruleSetHint")}
                </p>
                {!ruleSetsQuery.isPending && ruleSets.length === 0 && (
                  <Alert variant="destructive" data-testid="flow-rule-set-empty-warning">
                    <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
                      <span>{t("adminFlows.ruleSetEmptyWarning")}</span>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          onClose();
                          void navigate("/admin/rule-sets");
                        }}
                        data-testid="flow-go-to-rule-sets"
                      >
                        {t("adminFlows.goToRuleSets")}
                      </Button>
                    </AlertDescription>
                  </Alert>
                )}
              </div>

              <div className="flex flex-col gap-3">
                <div className="flex items-center justify-between">
                  <Label>
                    {t("adminFlows.stages")}
                    <span className="text-destructive" aria-hidden>*</span>
                  </Label>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => { setForm((current) => ({
                        ...current,
                        stages: [
                          ...current.stages,
                          {
                            position: current.stages.length + 1,
                            datasourceId: "",
                            approvalSteps: [{ position: 1, actorIds: [] }],
                            executionActorIds: [],
                          },
                        ],
                      })); }
                    }
                    data-testid="flow-add-stage"
                  >
                    <Plus />
                    {t("adminFlows.addStage")}
                  </Button>
                </div>
                {form.stages.map((stage, stageIndex) => (
                  <div key={stageIndex} className="flex flex-col gap-2 rounded-md border p-3" data-testid={`flow-stage-${String(stageIndex)}`}>
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-sm font-medium">{t("adminFlows.stageTitle", { index: stage.position })}</span>
                      <div className="flex gap-1">
                        <Button
                          size="icon"
                          variant="ghost"
                          disabled={stageIndex === 0}
                          onClick={() => { setForm((current) => {
                              const upper = current.stages[stageIndex - 1];
                              const lower = current.stages[stageIndex];
                              if (upper === undefined || lower === undefined) return current;
                              const stages = [...current.stages];
                              stages[stageIndex - 1] = lower;
                              stages[stageIndex] = upper;
                              return { ...current, stages: stages.map((item, index) => ({ ...item, position: index + 1 })) };
                            }); }
                          }
                          aria-label={t("adminFlows.stageUp", { index: String(stage.position) })}
                          data-testid={`flow-stage-up-${String(stageIndex)}`}
                        >
                          <ArrowUp />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          disabled={stageIndex === form.stages.length - 1}
                          onClick={() => { setForm((current) => {
                              const upper = current.stages[stageIndex];
                              const lower = current.stages[stageIndex + 1];
                              if (upper === undefined || lower === undefined) return current;
                              const stages = [...current.stages];
                              stages[stageIndex] = lower;
                              stages[stageIndex + 1] = upper;
                              return { ...current, stages: stages.map((item, index) => ({ ...item, position: index + 1 })) };
                            }); }
                          }
                          aria-label={t("adminFlows.stageDown", { index: String(stage.position) })}
                          data-testid={`flow-stage-down-${String(stageIndex)}`}
                        >
                          <ArrowDown />
                        </Button>
                        <Button
                          size="icon"
                          variant="ghost"
                          onClick={() => { setForm((current) => ({
                              ...current,
                              stages: current.stages
                                .filter((_, index) => index !== stageIndex)
                                .map((item, index) => ({ ...item, position: index + 1 })),
                            })); }
                          }
                          aria-label={t("adminFlows.stageRemove", { index: String(stage.position) })}
                          data-testid={`flow-stage-remove-${String(stageIndex)}`}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </div>
                    <div className="flex flex-col gap-2">
                      <Label>
                        {t("adminFlows.stageDatasource")}
                        <span className="text-destructive" aria-hidden>*</span>
                      </Label>
                      <Select
                        value={stage.datasourceId === "" ? null : stage.datasourceId}
                        onValueChange={(value) => { setForm((current) => {
                            const stages = [...current.stages];
                            stages[stageIndex] = { ...stage, datasourceId: value ?? "" };
                            return { ...current, stages };
                          }); }
                        }
                        items={Object.fromEntries(datasources.map((datasource) => [datasource.id, datasource.name]))}
                      >
                        <SelectTrigger
                          className="h-9 w-full text-sm"
                          aria-label={t("adminFlows.stageDatasource", { index: String(stage.position) })}
                          data-testid={`flow-stage-datasource-${String(stageIndex)}`}
                        >
                          <SelectValue placeholder={t("adminFlows.pickDatasource")} />
                        </SelectTrigger>
                        <SelectContent>
                          <div className="p-1">
                            <Input
                              value={stageDsSearchByIndex[stageIndex] ?? ""}
                              onChange={(event) => {
                                const val = event.target.value;
                                setStageDsSearchByIndex((prev) => ({ ...prev, [stageIndex]: val }));
                              }}
                              onKeyDown={(event) => {
                                event.stopPropagation();
                              }}
                              placeholder={t("adminFlows.datasourceSearch")}
                              aria-label={t("adminFlows.datasourceSearch")}
                              className="h-7 text-xs"
                              maxLength={64}
                              data-testid={`flow-stage-datasource-search-${String(stageIndex)}`}
                            />
                          </div>
                          {(() => {
                            const stageDsQuery = (stageDsSearchByIndex[stageIndex] ?? "").trim().toLowerCase();
                            const visibleStageDatasources =
                              stageDsQuery === ""
                                ? datasources
                                : datasources.filter((datasource) =>
                                    datasource.name.toLowerCase().includes(stageDsQuery) ||
                                    datasource.engine.toLowerCase().includes(stageDsQuery),
                                  );
                            if (visibleStageDatasources.length === 0) {
                              return (
                                <div className="text-muted-foreground px-2 py-1.5 text-xs">
                                  {t("adminFlows.datasourceSearchEmpty")}
                                </div>
                              );
                            }
                            return visibleStageDatasources.map((datasource) => (
                              <SelectItem key={datasource.id} value={datasource.id}>
                                {datasource.enabled
                                  ? datasource.name
                                  : `${datasource.name} (${t("adminFlows.disabled")})`}
                              </SelectItem>
                            ));
                          })()}
                        </SelectContent>
                      </Select>
                      {(() => {
                        const selectedDs = datasources.find((item) => item.id === stage.datasourceId);
                        if (selectedDs === undefined || selectedDs.enabled) return null;
                        return (
                          <p
                            className="text-destructive text-xs"
                            data-testid={`flow-stage-datasource-disabled-${String(stageIndex)}`}
                          >
                            {t("adminFlows.disabledDatasourceWarning", { name: selectedDs.name })}
                          </p>
                        );
                      })()}
                    </div>
                    <ApprovalStepsEditor
                      steps={stage.approvalSteps}
                      users={users}
                      onChange={(steps) => { setForm((current) => {
                          const stages = [...current.stages];
                          stages[stageIndex] = { ...stage, approvalSteps: steps };
                          return { ...current, stages };
                        }); }
                      }
                      testIdPrefix={`flow-stage-${String(stageIndex)}`}
                    />
                    <ActorPicker
                      label={t("adminFlows.executionActors")}
                      users={users}
                      selected={stage.executionActorIds}
                      onChange={(ids) => { setForm((current) => {
                          const stages = [...current.stages];
                          stages[stageIndex] = { ...stage, executionActorIds: ids };
                          return { ...current, stages };
                        }); }
                      }
                      testIdPrefix={`flow-stage-${String(stageIndex)}-exec`}
                    />
                  </div>
                ))}
              </div>
            </>
          ) : (
            <>
              <div className="flex flex-col gap-2">
                <Label>{t("adminFlows.queryDatasources")}</Label>
                <p className="text-muted-foreground text-xs">{t("adminFlows.queryDatasourcesHint")}</p>
                <div className="flex flex-col gap-1.5 rounded-md border p-2">
                  <Input
                    value={queryDsSearch}
                    onChange={(event) => { setQueryDsSearch(event.target.value); }}
                    placeholder={t("adminFlows.datasourceSearch")}
                    aria-label={t("adminFlows.datasourceSearch")}
                    className="h-7 text-xs"
                    maxLength={64}
                    data-testid="flow-query-ds-search"
                  />
                  {(() => {
                    const normalizedDsQuery = queryDsSearch.trim().toLowerCase();
                    const visibleQueryDatasources =
                      normalizedDsQuery === ""
                        ? datasources
                        : datasources.filter((datasource) =>
                            datasource.name.toLowerCase().includes(normalizedDsQuery) ||
                            datasource.engine.toLowerCase().includes(normalizedDsQuery),
                          );
                    if (visibleQueryDatasources.length === 0) {
                      return (
                        <span className="text-muted-foreground px-1 py-1 text-xs">
                          {t("adminFlows.datasourceSearchEmpty")}
                        </span>
                      );
                    }
                    return visibleQueryDatasources.map((datasource) => {
                      const selected = form.queryCapabilities.find((capability) => capability.datasourceId === datasource.id);
                      return (
                        <div key={datasource.id} className="flex items-center gap-2 px-1 py-1 text-sm">
                          <Checkbox
                            checked={selected !== undefined}
                            onCheckedChange={(checked) => {
                              setForm((current) => ({
                                ...current,
                                queryCapabilities: checked
                                  ? [...current.queryCapabilities, { datasourceId: datasource.id, canExport: false }]
                                  : current.queryCapabilities.filter((capability) => capability.datasourceId !== datasource.id),
                              }));
                            }}
                            data-testid={`flow-query-ds-${datasource.id}`}
                          />
                          <Database className="size-3.5 text-muted-foreground" />
                          {datasource.name}
                          {selected !== undefined && (
                            <label className="ml-auto flex cursor-pointer items-center gap-1 text-xs">
                              <Checkbox
                                checked={selected.canExport}
                                onCheckedChange={(checked) => {
                                  setForm((current) => ({
                                    ...current,
                                    queryCapabilities: current.queryCapabilities.map((capability) =>
                                      capability.datasourceId === datasource.id
                                        ? { ...capability, canExport: checked }
                                        : capability,
                                    ),
                                  }));
                                }}
                                data-testid={`flow-query-export-${datasource.id}`}
                              />
                              {t("adminFlows.canExport")}
                            </label>
                          )}
                        </div>
                      );
                    });
                  })()}
                </div>
              </div>
              <ApprovalStepsEditor
                steps={form.approvalSteps}
                users={users}
                onChange={(steps) => { setForm({ ...form, approvalSteps: steps }); }}
                testIdPrefix="flow-query"
              />
              {editing !== null && <MaskingRulesSection flow={editing} />}
            </>
          )}

          {errorText !== null && (
            <Alert variant="destructive" data-testid="flow-form-error">
              <AlertTitle>{t("adminFlows.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          {!valid && missingFlowItems.length > 0 && (
            <p className="text-destructive text-xs font-medium" data-testid="flow-form-invalid-hint">
              {t("adminFlows.formMissingSummary", { items: missingFlowItems.join(", ") })}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {engineMismatch && (
            <Alert variant="destructive" data-testid="flow-engine-mismatch">
              <AlertDescription>{t("adminFlows.engineMismatch")}</AlertDescription>
            </Alert>
          )}
          <PendingButton
            pending={createFlow.isPending || replaceFlow.isPending}
            disabled={!valid}
            onClick={() => void submit()}
            data-testid="flow-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ApprovalStepsEditor({
  steps,
  users,
  onChange,
  testIdPrefix,
}: {
  steps: { position: number; actorIds: string[] }[];
  users: User[];
  onChange: (steps: { position: number; actorIds: string[] }[]) => void;
  testIdPrefix: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <Label>{t("adminFlows.approvalSteps")}</Label>
        {steps.length < 10 && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => { onChange([...steps, { position: steps.length + 1, actorIds: [] }]); }}
            data-testid={`${testIdPrefix}-add-step`}
          >
            <Plus />
            {t("adminFlows.addStep")}
          </Button>
        )}
      </div>
      <p className="text-muted-foreground text-xs">{t("adminFlows.approvalStepsHint")}</p>
      {steps.map((step, stepIndex) => (
        <div key={stepIndex} className="flex flex-col gap-1 rounded-md border p-2" data-testid={`${testIdPrefix}-step-${String(stepIndex)}`}>
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium">{t("adminFlows.stepTitle", { index: step.position })}</span>
            <span className="flex items-center gap-2">
              <label className="flex cursor-pointer items-center gap-1 text-xs text-muted-foreground">
                <Checkbox
                  checked={step.actorIds.length > 0 && step.actorIds.length === users.length}
                  indeterminate={step.actorIds.length > 0 && step.actorIds.length < users.length}
                  onCheckedChange={() => {
                    const allSelected = step.actorIds.length > 0 && step.actorIds.length === users.length;
                    onChange(
                      steps.map((item, index) =>
                        index === stepIndex
                          ? { ...item, actorIds: allSelected ? [] : users.map((user) => user.id) }
                          : item,
                      ),
                    );
                  }}
                  data-testid={`${testIdPrefix}-step-${String(stepIndex)}-select-all`}
                />
                {t("adminFlows.selectAll")}
                <span className="tabular-nums" data-testid={`${testIdPrefix}-step-${String(stepIndex)}-select-all-count`}>
                  {String(step.actorIds.length)}/{String(users.length)}
                </span>
              </label>
              {steps.length > 1 && (
                <Button
                  size="icon"
                  variant="ghost"
                  onClick={() => { onChange(
                      steps
                        .filter((_, index) => index !== stepIndex)
                        .map((item, index) => ({ ...item, position: index + 1 })),
                    ); }
                  }
                  aria-label={t("adminFlows.stepRemove", { index: String(step.position) })}
                  data-testid={`${testIdPrefix}-step-remove-${String(stepIndex)}`}
                >
                  <Trash2 />
                </Button>
              )}
            </span>
          </div>
          <StepActorPicker
            step={step}
            users={users}
            onToggle={(userId, checked) => {
              onChange(
                steps.map((item, index) =>
                  index === stepIndex
                    ? {
                        ...item,
                        actorIds: checked
                          ? [...item.actorIds, userId]
                          : item.actorIds.filter((id) => id !== userId),
                      }
                    : item,
                ),
              );
            }}
            testIdPrefix={`${testIdPrefix}-step-${String(stepIndex)}`}
          />
        </div>
      ))}
    </div>
  );
}

/**
 * Per-step actor capsules with a client-side search (owner ruling
 * 2026-10-31): the actor roster can be long, so the capsules filter on a
 * name substring while selections persist — filtering only hides capsules,
 * it never changes step.actorIds.
 */
function StepActorPicker({
  step,
  users,
  onToggle,
  testIdPrefix,
}: {
  step: { position: number; actorIds: string[] };
  users: User[];
  onToggle: (userId: string, checked: boolean) => void;
  testIdPrefix: string;
}) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const normalized = query.trim().toLowerCase();
  const visibleUsers = normalized === ""
    ? users
    : users.filter((user) =>
        (user.display_name ?? user.username).toLowerCase().includes(normalized) ||
        user.username.toLowerCase().includes(normalized),
      );
  return (
    <div className="flex flex-col gap-1.5">
      <Input
        value={query}
        onChange={(event) => { setQuery(event.target.value); }}
        placeholder={t("adminFlows.actorSearch")}
        aria-label={t("adminFlows.actorSearch")}
        className="h-7 text-xs"
        maxLength={64}
        data-testid={`${testIdPrefix}-search`}
      />
      <div className="flex flex-wrap gap-1">
        {visibleUsers.map((user) => {
          const checked = step.actorIds.includes(user.id);
          return (
            <label
              key={user.id}
              className={`flex cursor-pointer items-center gap-1 rounded-full border px-2 py-0.5 text-xs ${
                checked ? "bg-secondary" : ""
              }`}
            >
              <Checkbox
                checked={checked}
                onCheckedChange={(value) => { onToggle(user.id, value); }}
                data-testid={`${testIdPrefix}-actor-${user.id}`}
              />
              {user.display_name}
            </label>
          );
        })}
        {visibleUsers.length === 0 && (
          <span className="text-muted-foreground text-xs">{t("adminFlows.actorSearchEmpty")}</span>
        )}
      </div>
    </div>
  );
}

function ActorPicker({
  label,
  users,
  selected,
  onChange,
  testIdPrefix,
}: {
  label: string;
  users: User[];
  selected: string[];
  onChange: (ids: string[]) => void;
  testIdPrefix: string;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Label>{label}</Label>
        <label className="flex cursor-pointer items-center gap-1 text-xs text-muted-foreground">
          <Checkbox
            checked={selected.length > 0 && selected.length === users.length}
            indeterminate={selected.length > 0 && selected.length < users.length}
            onCheckedChange={() => {
              onChange(selected.length === users.length ? [] : users.map((user) => user.id));
            }}
            data-testid={`${testIdPrefix}-select-all`}
          />
          {t("adminFlows.selectAll")}
          <span className="tabular-nums" data-testid={`${testIdPrefix}-select-all-count`}>
            {String(selected.length)}/{String(users.length)}
          </span>
        </label>
      </div>
      <StepActorPicker
        step={{ position: 0, actorIds: selected }}
        users={users}
        onToggle={(userId, checked) => {
          onChange(
            checked ? [...selected, userId] : selected.filter((id) => id !== userId),
          );
        }}
        testIdPrefix={testIdPrefix}
      />
    </div>
  );
}

function MaskingRulesSection({ flow }: { flow: Flow }) {
  const { t } = useTranslation();
  const rulesQuery = useFlowMaskingRules(flow.id, true);
  const replaceRule = useReplaceMaskingRule();
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [savedFor, setSavedFor] = useState<Record<string, string>>({});
  const [errorText, setErrorText] = useState<string | null>(null);

  const fetched: FlowMaskingRule[] = rulesQuery.data ?? [];
  // Vocabulary is configured per datasource of the flow (OpenAPI: per datasource of the flow), but the backend only returns
  // existing rule rows; pad empty rows for unconfigured datasources here so the first rule can be created in the UI.
  const capabilities = flow.query_capabilities ?? [];
  const rules: { datasource_id: string; datasource_name?: string; sensitive_columns: string[] }[] = [
    ...capabilities.map((capability) => {
      const existing = fetched.find((rule) => rule.datasource_id === capability.datasource_id);
      return { datasource_id: capability.datasource_id, datasource_name: capability.datasource_name, sensitive_columns: existing?.sensitive_columns ?? [] };
    }),
    ...fetched.filter((rule) => !capabilities.some((capability) => capability.datasource_id === rule.datasource_id)),
  ];

  return (
    <div className="flex flex-col gap-2 rounded-md border p-3" data-testid="flow-masking-rules">
      <div className="flex items-center gap-2">
        <EyeOff className="size-4" />
        <Label>{t("adminFlows.maskingRules")}</Label>
      </div>
      <p className="text-muted-foreground text-xs">{t("adminFlows.maskingRulesHint")}</p>
      {rulesQuery.isPending && <LoadingState variant="inline" />}
      {rules.map((rule) => {
        const value = draft[rule.datasource_id] ?? rule.sensitive_columns.join(", ");
        const savedValue = savedFor[rule.datasource_id] ?? rule.sensitive_columns.join(", ");
        const dirty = value !== savedValue;
        return (
          <div key={rule.datasource_id} className="flex flex-col gap-1">
            <span className="font-mono text-xs">{rule.datasource_name ?? rule.datasource_id}</span>
            <div className="flex items-center gap-2">
              <Textarea
                value={value}
                onChange={(event) => { setDraft((current) => ({ ...current, [rule.datasource_id]: event.target.value })); }}
                className="min-h-8 text-xs"
                data-testid={`flow-masking-rule-${rule.datasource_id}`}
              />
              <PendingButton
                size="sm"
                variant="outline"
                pending={replaceRule.isPending}
                disabled={!dirty}
                onClick={() => {
                  setErrorText(null);
                  const columns = value
                    .split(",")
                    .map((column) => column.trim())
                    .filter((column) => column !== "");
                  replaceRule.mutate(
                    { flowId: flow.id, datasourceId: rule.datasource_id, body: { sensitive_columns: columns } },
                    {
                      onSuccess: () => {
                        setSavedFor((current) => ({ ...current, [rule.datasource_id]: value }));
                        toast.success(t("common.savedToast"));
                      },
                      onError: (error) => {
                        const msg = describeErrorText(describeError(error, "replaceFlowMaskingRule"));
                        setErrorText(msg);
                        toast.error(msg);
                      },
                    },
                  );
                }}
                data-testid={`flow-masking-save-${rule.datasource_id}`}
              >
                {t("common.save")}
              </PendingButton>
            </div>
          </div>
        );
      })}
      {errorText !== null && (
        <Alert variant="destructive">
          <AlertDescription>{errorText}</AlertDescription>
        </Alert>
      )}
    </div>
  );
}
