import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Layers, Pencil, Plus, Trash2 } from "lucide-react";
import { Checkbox } from "@/shared/components/ui/checkbox";
import type { RuleSet, RuleSetWrite } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateRuleSet,
  useDeleteRuleSet,
  useFlowsForRuleSetImpact,
  usePromptTools,
  useReplaceRuleSet,
  useRuleSets,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { renderSkillDescription } from "@/features/admin/skill-description";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { formatTimestamp } from "@/shared/lib/timezones";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { Card, CardContent } from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuGroup, DropdownMenuItem, DropdownMenuLabel } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";

/**
 * Rule Set Combination Management (route /admin/rule-sets; migration contract §2 maps the
 * legacy /manager/rules here). In v4 a rule set is a PURE prompt-tool
 * combination — rule_sets.rules is structurally an empty object and the old
 * ~50-knob DML/DDL catalog has no v4 equivalent (the built-in lexical checks
 * ship as builtin skills; the legacy rule_id-per-datasource binding is gone).
 * The page therefore edits exactly: name, enabled, prompt_tool_ids.
 *
 * Impact preview (deliverable): editing a rule set re-hashes its config; every
 * unsubmitted review referencing the old hash becomes outdated while
 * submitted orders keep their frozen snapshot. The dialog renders the
 * reverse visibility — which flows currently bind this rule set (binding
 * itself happens on the flow editor, F10) — and the hash-change warning.
 * Draft skills cannot be bound (UI spec §5.3) — the picker marks them.
 */

interface RuleSetFormState {
  name: string;
  enabled: boolean;
  toolIds: string[];
}

function RuleSetFormDialog({
  editing,
  onClose,
}: {
  editing: RuleSet | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const session = useSession();
  const toolsQuery = usePromptTools(session.user?.can_access_admin === true);
  const flowsQuery = useFlowsForRuleSetImpact(session.user?.can_access_admin === true);
  const createMutation = useCreateRuleSet();
  const replaceMutation = useReplaceRuleSet();
  // Keyed per open by the parent: state initializes from the edited row.
  const [form, setForm] = useState<RuleSetFormState>(() =>
    editing === null
      ? { name: "", enabled: true, toolIds: [] }
      : { name: editing.name, enabled: editing.enabled, toolIds: [...editing.prompt_tool_ids] },
  );
  // §18.80: add client-side search (name + description substring) and select-all to the combination list for
  // efficient skill selection across long skill catalogs.
  const [toolSearch, setToolSearch] = useState("");
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);

  const keyword = toolSearch.trim().toLowerCase();

  const allTools = toolsQuery.data?.items ?? [];
  // §18.80 addendum 1: search shares the same source as display — matching against the dynamically rendered description text.
  const renderedDescription = (tool: (typeof allTools)[number]): string =>
    renderSkillDescription(tool.description, tool.parameters ?? {});
  const visibleTools = keyword === ""
    ? allTools
    : allTools.filter((tool) =>
        tool.name.toLowerCase().includes(keyword) ||
        renderedDescription(tool).toLowerCase().includes(keyword),
      );
  const bindableVisibleIds = visibleTools
    .filter((tool) => tool.state !== "draft")
    .map((tool) => tool.id);
  const selectedVisibleCount = bindableVisibleIds.filter((id) => form.toolIds.includes(id)).length;
  const allVisibleSelected = bindableVisibleIds.length > 0 && selectedVisibleCount === bindableVisibleIds.length;
  const someVisibleSelected = selectedVisibleCount > 0 && selectedVisibleCount < bindableVisibleIds.length;

  const toggleAllVisible = () => {
    setForm((current) => {
      if (allVisibleSelected) {
        return {
          ...current,
          toolIds: current.toolIds.filter((id) => !bindableVisibleIds.includes(id)),
        };
      }
      return {
        ...current,
        toolIds: [...new Set([...current.toolIds, ...bindableVisibleIds])],
      };
    });
  };

  const bindingFlows = useMemo(
    () =>
      editing === null
        ? []
        : (flowsQuery.data ?? []).filter((flow) => flow.rule_set_id === editing.id),
    [editing, flowsQuery.data],
  );
  const bindingWillChange = useMemo(
    () => editing !== null && JSON.stringify([...editing.prompt_tool_ids].sort()) !== JSON.stringify([...form.toolIds].sort()),
    [editing, form.toolIds],
  );

  const toggleTool = (toolId: string) => {
    setForm((current) => ({
      ...current,
      toolIds: current.toolIds.includes(toolId)
        ? current.toolIds.filter((id) => id !== toolId)
        : [...current.toolIds, toolId],
    }));
  };

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    const write: RuleSetWrite = {
      name: form.name,
      enabled: form.enabled,
      // Pure prompt-tool combination: the rules object is structurally
      // empty in v4 (ai-review-production PRD 9.3.1).
      rules: {},
      prompt_tool_ids: form.toolIds,
    };
    if (editing === null) {
      createMutation.mutate(write, {
        onSuccess: () => {
          toast.success(t("common.createdToast"), { description: form.name });
          onClose();
        },
        onError: (error) => {
          const display = describeError(error, "createRuleSet");
          setErrorKey(display.messageKey);
          setErrorRequestId(display.requestId);
          toast.error(t(display.messageKey));
        },
      });
    } else {
      replaceMutation.mutate(
        { id: editing.id, version: editing.version, write },
        {
          onSuccess: () => {
            toast.success(t("common.savedToast"), { description: form.name });
            onClose();
          },
          onError: (error) => {
            const display = describeError(error, "replaceRuleSet");
            setErrorKey(display.messageKey);
            setErrorRequestId(display.requestId);
            toast.error(t(display.messageKey));
          },
        },
      );
    }
  };

  const mutation = editing === null ? createMutation : replaceMutation;

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      {/* §18.80: responsive width — xl was too narrow on full screens to read combination details, widened to
          3xl/4xl tiers. */}
      <DialogContent className="max-h-[720px] sm:max-w-3xl lg:max-w-4xl [&>[data-slot=scroll-area]>[data-slot=scroll-area-viewport]]:max-h-none">
        <DialogHeader>
          <DialogTitle>
            {editing === null ? t("admin.ruleSets.createTitle") : t("admin.ruleSets.editTitle")}
          </DialogTitle>
          <DialogDescription>{t("admin.ruleSets.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rule-set-name">{t("admin.ruleSets.name")}</Label>
              <Input
                id="rule-set-name"
                value={form.name}
                onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
                data-testid="rule-set-name"
              />
            </div>
            <div className="flex items-center gap-2">
              <Checkbox
                id="rule-set-enabled"
                checked={form.enabled}
                onCheckedChange={(checked) => { setForm({ ...form, enabled: checked }); }}
                data-testid="rule-set-enabled"
              />
              <Label htmlFor="rule-set-enabled" className="cursor-pointer">
                {t("admin.ruleSets.enabled")}
              </Label>
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label>{t("admin.ruleSets.toolsTitle")}</Label>
            {toolsQuery.isPending ? (
              <LoadingState variant="inline" />
            ) : (
              <>
                {/* §18.80: select-all (indeterminate state reuses §18.70 rendering) + client-side search;
                    select-all only acts on bindable skills in the current filtered result. */}
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <TableSearchInput
                    value={toolSearch}
                    onChange={setToolSearch}
                    placeholder={t("admin.ruleSets.toolsSearchPlaceholder")}
                    testId="rule-set-tools-search"
                  />
                  <label className="flex cursor-pointer items-center gap-2 text-sm" data-testid="rule-set-tools-select-all-row">
                    <Checkbox
                      checked={allVisibleSelected}
                      indeterminate={someVisibleSelected}
                      onCheckedChange={toggleAllVisible}
                      disabled={bindableVisibleIds.length === 0}
                      data-testid="rule-set-tools-select-all"
                    />
                    {t("admin.ruleSets.selectAll")}
                    <span className="text-muted-foreground text-xs tabular-nums" data-testid="rule-set-tools-select-all-count">
                      {selectedVisibleCount}/{bindableVisibleIds.length}
                    </span>
                  </label>
                </div>
                <ScrollArea className="rounded-lg border" viewportClassName="h-[280px] p-2">
                  <div className="flex flex-col gap-1.5" data-testid="rule-set-tools">
                    {visibleTools.length === 0 ? (
                      <EmptyState title={t("table.emptySearch")} filtered testId="rule-set-tools-search-empty" />
                    ) : (
                      visibleTools.map((tool) => {
                        const bindable = tool.state === "enabled" || tool.state === "disabled";
                        const selected = form.toolIds.includes(tool.id);
                        return (
                          <label
                            key={tool.id}
                            className={`flex items-center justify-between gap-2 rounded-lg border p-2.5 ${
                              bindable ? "cursor-pointer" : "cursor-not-allowed opacity-60"
                            }`}
                            data-testid={`rule-set-tool-option-${tool.id}`}
                          >
                            <span className="flex min-w-0 flex-col gap-0.5">
                              <span className="flex items-center gap-2">
                                <Checkbox
                                  checked={selected}
                                  onCheckedChange={() => { if (bindable) toggleTool(tool.id); }}
                                  disabled={!bindable}
                                />
                                <span className="text-sm font-medium">{tool.name}</span>
                              </span>
                              {/* §18.80 owner ruling: replace hash display with skill description —
                                  understand each skill's purpose before checking it while editing; {{param}} placeholders
                                  render dynamically from current inline values (addendum 1). */}
                              {tool.description ? (
                                <span className="text-muted-foreground truncate pl-6 text-xs">
                                  {renderedDescription(tool)}
                                </span>
                              ) : null}
                            </span>
                            <span className="flex shrink-0 items-center gap-1.5">
                              <Badge variant={tool.state === "enabled" ? "default" : "secondary"}>
                                {t(`admin.reviewInput.state_${tool.state}`)}
                              </Badge>
                              {bindable ? null : (
                                <Badge variant="destructive">{t("admin.ruleSets.draftNotBindable")}</Badge>
                              )}
                            </span>
                          </label>
                        );
                      })
                    )}
                  </div>
                </ScrollArea>
              </>
            )}
          </div>
          {editing !== null ? (
            <div className="rounded-lg border p-3" data-testid="rule-set-impact-preview">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-sm font-medium">{t("admin.ruleSets.impactTitle")}</p>
                <Badge variant={bindingFlows.length > 0 ? "secondary" : "outline"}>
                  {t("admin.ruleSets.boundFlows", { count: bindingFlows.length })}
                </Badge>
              </div>
              <p className="text-muted-foreground mt-1 text-xs">{t("admin.ruleSets.impactHashNote")}</p>
              {/* §18.59: display affected flows as a grid of tag badges (previously a comma-separated string). */}
              <div className="mt-2 flex flex-wrap gap-1.5">
                {bindingFlows.length === 0 ? (
                  <span className="text-muted-foreground text-xs">{t("admin.ruleSets.noBoundFlows")}</span>
                ) : (
                  bindingFlows.map((flow) => (
                    <Badge key={flow.id} variant="secondary" className="max-w-56">
                      <span className="truncate">{flow.name}</span>
                    </Badge>
                  ))
                )}
              </div>
              {bindingWillChange ? (
                <p className="mt-1 text-xs text-[var(--risk-warning)]">
                  {t("admin.ruleSets.bindingChangeNote")}
                </p>
              ) : null}
            </div>
          ) : null}
          {errorKey !== null ? (
            <Alert variant="destructive" data-testid="rule-set-error">
              <AlertTitle>{t(errorKey)}</AlertTitle>
              <AlertDescription>
                {errorRequestId !== null ? `request_id: ${errorRequestId}` : null}
              </AlertDescription>
            </Alert>
          ) : null}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton pending={mutation.isPending} onClick={submit} data-testid="rule-set-submit">
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function AdminRuleSetsPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const pager = useCursorPage(20);
  const [search, setSearch] = useState("");
  const query = useRuleSets(enabled, { limit: pager.pageSize, after: pager.after, q: search.trim() || undefined });
  const flowsQuery = useFlowsForRuleSetImpact(enabled);
  const [editing, setEditing] = useState<RuleSet | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  // §18.73: search goes through server-side q; keyword is used only to distinguish empty-state copy.
  const keyword = search.trim();
  // Keyword filtering runs client-side; tween position changes of surviving rows when the row set changes.
  const visibleRuleSets = (query.data?.items ?? []).filter(
    (ruleSet: RuleSet) => keyword === "" || ruleSet.name.toLowerCase().includes(keyword),
  );
  const ruleSetBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(visibleRuleSets.map((ruleSet: RuleSet) => ruleSet.id)),
  );
  const [deleting, setDeleting] = useState<RuleSet | null>(null);
  const deleteMutation = useDeleteRuleSet();
  const [deleteErrorKey, setDeleteErrorKey] = useState<string | null>(null);


  return (
    <PageLayout>
      <PageHeader
        title={t("admin.ruleSets.title")}
        description={t("admin.ruleSets.description")}
        icon={Layers}
        actions={
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
            data-testid="rule-set-create"
          >
            <Plus /> {t("admin.ruleSets.createTitle")}
          </Button>
        }
      />
      <Card>
        <ListToolbar
          busy={query.isFetching && !query.isPending}
          fill
        >
          <TableSearchInput
            value={search}
            onChange={(value) => {
              setSearch(value);
              pager.reset();
            }}
            testId="admin-rule-sets-search"
          />
        </ListToolbar>
        <CardContent className="px-0!">
          {query.isPending ? (
            <LoadingState variant="table" columns={4} />
          ) : query.isError ? (
            <ErrorState error={query.error} operationId="listRuleSets" onRetry={() => void query.refetch()} />
          ) : query.data.items.length === 0 && keyword !== "" ? (
            <EmptyState
              title={t("table.emptySearch")}
              filtered
              testId="admin-rule-sets-search-empty"
            />
          ) : query.data.items.length === 0 ? (
            <EmptyState
              title={t("admin.ruleSets.emptyTitle")}
              description={t("admin.ruleSets.emptyDescription")}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("admin.ruleSets.name")}</TableHead>
                  <TableHead>{t("admin.reviewInput.configHash")}</TableHead>
                  {/* §18.59 owner ruling: remove referenced-flows / skill-combination columns from the table
                      (reference count still drives delete disabling; skill details live in the edit dialog). */}
                  <TableHead>{t("admin.ruleSets.createdAtColumn")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody ref={ruleSetBodyRef} stale={query.isFetching && query.isPlaceholderData}>
                {visibleRuleSets.map((ruleSet: RuleSet) => {
                  const boundFlows = (flowsQuery.data ?? []).filter(
                    (flow) => flow.rule_set_id === ruleSet.id,
                  );
                  return (
                    <TableRow key={ruleSet.id} data-testid={`rule-set-row-${ruleSet.id}`} data-row-key={ruleSet.id}>
                      <TableCell className="font-semibold text-foreground">
                        {ruleSet.name}
                        {ruleSet.enabled ? null : (
                          <StatusBadge tone="stone" className="ml-2">
                            {t("admin.ruleSets.disabled")}
                          </StatusBadge>
                        )}
                      </TableCell>
                      <TableCell className="max-w-80 truncate font-mono" data-testid="rule-set-hash">
                        {ruleSet.config_hash}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums" data-testid={`rule-set-created-${ruleSet.id}`}>
                        {formatTimestamp(ruleSet.created_at)}
                      </TableCell>
                      <TableCell className="text-right">
                        <TableRowActions testId={`rule-set-actions-${ruleSet.id}`}>
                          <DropdownMenuItem
                            onClick={() => {
                              setEditing(ruleSet);
                              setDialogOpen(true);
                            }}
                            data-testid={`rule-set-edit-${ruleSet.id}`}
                          >
                            <Pencil /> {t("common.edit")}
                          </DropdownMenuItem>
                          {/* §18.59: disable deletion for referenced rule sets — reason rendered as a trailing
                              muted line inside the menu (tooltips do not enter menu portals). */}
                          {boundFlows.length > 0 ? (
                            <>
                              <DropdownMenuItem disabled data-testid={`rule-set-delete-${ruleSet.id}`}>
                                <Trash2 /> {t("common.delete")}
                              </DropdownMenuItem>
                              <DropdownMenuGroup><DropdownMenuLabel>{t("admin.ruleSets.deleteBlockedHint")}</DropdownMenuLabel></DropdownMenuGroup>
                            </>
                          ) : (
                            <DropdownMenuItem
                              variant="destructive"
                              disabled={deleteMutation.isPending}
                              onClick={() => { setDeleting(ruleSet); }}
                              data-testid={`rule-set-delete-${ruleSet.id}`}
                            >
                              <Trash2 /> {t("common.delete")}
                            </DropdownMenuItem>
                          )}
                        </TableRowActions>
                      </TableCell>
                    </TableRow>
                  );
                })}
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
              testIdPrefix="rulesets"
            />
          ) : null}
        </CardContent>
      </Card>
      {dialogOpen ? (
        <RuleSetFormDialog
          key={editing?.id ?? "new"}
          editing={editing}
          onClose={() => {
            setDialogOpen(false);
            setEditing(null);
          }}
        />
      ) : null}
      {deleting !== null ? (
        <Dialog open onOpenChange={(next) => { if (!next) setDeleting(null); }}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{t("admin.ruleSets.deleteTitle")}</DialogTitle>
              <DialogDescription>
                {t("admin.ruleSets.deleteDescription", { name: deleting.name })}
              </DialogDescription>
            </DialogHeader>
            {deleteErrorKey !== null ? (
              <Alert variant="destructive" data-testid="rule-set-delete-error">
                <AlertTitle>{t(deleteErrorKey)}</AlertTitle>
                <AlertDescription>{t("admin.reviewInput.deleteErrorHint")}</AlertDescription>
              </Alert>
            ) : null}
            <DialogFooter>
              <Button variant="outline" onClick={() => { setDeleting(null); }}>
                {t("common.cancel")}
              </Button>
              <PendingButton
                variant="destructive"
                pending={deleteMutation.isPending}
                onClick={() => {
                  deleteMutation.mutate(
                    { id: deleting.id, version: deleting.version },
                    {
                      onSuccess: () => {
                        toast.success(t("common.delete"), { description: deleting.name });
                        setDeleting(null);
                      },
                      onError: (error) => {
                        const display = describeError(error, "deleteRuleSet");
                        setDeleteErrorKey(display.messageKey);
                        toast.error(t(display.messageKey));
                      },
                    },
                  );
                }}
                data-testid="rule-set-delete-confirm"
              >
                {t("common.delete")}
              </PendingButton>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}
    </PageLayout>
  );
}
