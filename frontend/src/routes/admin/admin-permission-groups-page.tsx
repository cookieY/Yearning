import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Pencil, Plus, ShieldCheck, Trash2, X } from "lucide-react";
import { Checkbox } from "@/shared/components/ui/checkbox";
import type { Flow, PermissionGroup, User } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { TableSearchInput } from "@/shared/components/table-search-input";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import {
  useCreatePermissionGroup,
  useDeletePermissionGroup,
  useFlows,
  usePermissionGroups,
  useReplacePermissionGroup,
  useUsers,
} from "@/features/admin/use-admin";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";

/**
 * Permission Group Management (route /admin/permission-groups; migration contract §2 maps
 * legacy /manager/policy here, §3 field mapping: the three legacy
 * per-datasource transfer fields are DELETED — a group grants
 * members plus change/query FLOWS (P101: flows, never datasources or
 * workflow actors). The per-user effective permission preview derives from
 * the groups' server facts client-side (member ∪ granted flows). There is
 * deliberately no reviewer/executor authorization entry here.
 */

interface GroupFormState {
  name: string;
  enabled: boolean;
  memberIds: string[];
  flowIds: string[];
}

export default function AdminPermissionGroupsPage() {
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
  // §18.73 + RCP-20261105: group name / free text go through server-side q; enabled / member / granted flow are
  // valued server-side column filters (enabled / member_user_id / flow_id), performing cross-page narrowing
  // on the server. Member and granted-flow options come from the loaded user/flow catalogs.
  const serverQ = useMemo(
    () =>
      [...effectiveTerms]
        .reverse()
        .find((term) => term.field === null || term.field === "name")
        ?.text.trim() ?? "",
    [effectiveTerms],
  );
  const usersQuery = useUsers(isAdmin);
  const flowsQuery = useFlows(isAdmin);
  const users = useMemo(() => usersQuery.data?.items ?? [], [usersQuery.data?.items]);
  const flows = useMemo(() => flowsQuery.data?.items ?? [], [flowsQuery.data?.items]);
  const userById = useMemo(() => new Map(users.map((user) => [user.id, user])), [users]);
  const flowById = useMemo(() => new Map(flows.map((flow) => [flow.id, flow])), [flows]);
  const enabledParam = useMemo<boolean | undefined>(() => {
    const LABELS = { [t("adminGroups.enabled")]: true, [t("adminGroups.disabled")]: false } as const;
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "enabled" && candidate.text.trim() !== "");
    return term === undefined ? undefined : LABELS[term.text as keyof typeof LABELS];
  }, [effectiveTerms, t]);
  const memberUserIdParam = useMemo<string | undefined>(() => {
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "members" && candidate.text.trim() !== "");
    if (term === undefined) return undefined;
    return users.find((user) => user.username === term.text.trim())?.id;
  }, [effectiveTerms, users]);
  const flowIdParam = useMemo<string | undefined>(() => {
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "flows" && candidate.text.trim() !== "");
    if (term === undefined) return undefined;
    return flows.find((flow) => flow.name === term.text.trim())?.id;
  }, [effectiveTerms, flows]);
  const groupsQuery = usePermissionGroups(isAdmin, {
    limit: pager.pageSize,
    after: pager.after,
    q: serverQ || undefined,
    enabled: enabledParam,
    memberUserId: memberUserIdParam,
    flowId: flowIdParam,
  });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const groupsLoaded = !groupsQuery.isPending && !groupsQuery.isError;
  const [editing, setEditing] = useState<PermissionGroup | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [previewUser, setPreviewUser] = useState<string>("");

  const rawGroups = useMemo(() => groupsQuery.data?.items ?? [], [groupsQuery.data?.items]);

  const searchAccessors = useMemo<SearchAccessor<PermissionGroup>[]>(
    () => [
      {
        key: "name",
        label: t("adminGroups.column.name"),
        value: (group) => group.name,
      },
      {
        key: "enabled",
        label: t("adminGroups.column.enabled"),
        value: (group) =>
          group.enabled ? t("adminGroups.enabled") : t("adminGroups.disabled"),
        options: [
          { label: t("adminGroups.enabled") },
          { label: t("adminGroups.disabled") },
        ],
      },
      {
        key: "members",
        label: t("adminGroups.column.members"),
        value: (group) =>
          group.member_user_ids
            .map((id) => userById.get(id)?.username ?? id)
            .join(" "),
        // Member options = loaded usernames; selecting one maps to the server-side member_user_id filter.
        options: users.map((user) => ({ label: user.username })),
      },
      {
        key: "flows",
        label: t("adminGroups.column.flows"),
        value: (group) =>
          group.granted_flow_ids
            .map((id) => flowById.get(id)?.name ?? id)
            .join(" "),
        options: flows.map((flow) => ({ label: flow.name })),
      },
    ],
    [flowById, t, userById, users, flows],
  );
  const hasSearchFilter = effectiveTerms.some((term) => term.text.trim() !== "");
  const groups = filterBySearchTerms(rawGroups, effectiveTerms, searchAccessors);
  // Search narrows server-side/client-side; tween whole-row position changes when the row set changes.
  const groupsBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(groups.map((group) => group.id)),
  );

  const preview = useMemo(() => {
    if (previewUser === "") return null;
    const membership = rawGroups.filter((group) => group.member_user_ids.includes(previewUser) && group.enabled);
    const grantedFlowIds = new Set(membership.flatMap((group) => group.granted_flow_ids));
    return { groups: membership, flows: [...grantedFlowIds].map((id) => flowById.get(id)).filter((flow) => flow !== undefined) };
  }, [flowById, rawGroups, previewUser]);

  const loadError = groupsQuery.error ?? usersQuery.error ?? flowsQuery.error;

  return (
    <PageLayout data-testid="admin-groups-page">
      <PageHeader
        title={t("adminGroups.title")}
        description={t("adminGroups.description")}
        icon={ShieldCheck}
      />

      {loadError === null && !groupsQuery.isPending && (
          <Card>
            <CardHeader className="pb-3">
              <CardTitle>{t("adminGroups.preview.title")}</CardTitle>
              <CardDescription>{t("adminGroups.preview.description")}</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {/* Owner feedback 2026-11-05: the previewed user is picked from the
                  loaded users (value = user id, label = username), not typed;
                  popup height is capped at max-h-60 via ScrollArea, and a
                  cancel button next to the Select clears the selection. */}
              <div className="flex flex-wrap items-center gap-2">
                <Select
                  value={previewUser === "" ? null : previewUser}
                  onValueChange={(next) => { setPreviewUser(next ?? ""); }}
                  items={Object.fromEntries(users.map((user) => [user.id, user.username]))}
                >
                  <SelectTrigger className="w-72" aria-label={t("adminGroups.preview.title")} data-testid="admin-groups-preview-input">
                    <SelectValue placeholder={t("adminGroups.preview.placeholder")} />
                  </SelectTrigger>
                  <SelectContent alignItemWithTrigger={false}>
                    <ScrollArea viewportClassName="max-h-60">
                      {users.map((user) => (
                        <SelectItem key={user.id} value={user.id}>
                          {user.username}
                        </SelectItem>
                      ))}
                    </ScrollArea>
                  </SelectContent>
                </Select>
                {previewUser !== "" && (
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() => { setPreviewUser(""); }}
                    data-testid="admin-groups-preview-clear"
                  >
                    <X />
                    {t("common.cancel")}
                  </Button>
                )}
              </div>
              {preview !== null && (
                <div className="flex flex-col gap-2 rounded-md border p-3 text-sm" data-testid="admin-groups-preview-result">
                  <p className="text-xs">
                    <span className="text-muted-foreground">{t("adminGroups.preview.user")}: </span>
                    {userById.get(previewUser)?.username ?? previewUser}
                  </p>
                  <div className="flex flex-wrap gap-1">
                    {preview.groups.length === 0 && (
                      <span className="text-muted-foreground text-xs">{t("adminGroups.preview.noGroups")}</span>
                    )}
                    {preview.groups.map((group) => (
                      <Badge key={group.id} variant="outline">
                        {group.name}
                      </Badge>
                    ))}
                  </div>
                  <div className="flex flex-wrap gap-1">
                    {preview.flows.length === 0 && (
                      <span className="text-muted-foreground text-xs">{t("adminGroups.preview.noFlows")}</span>
                    )}
                    {preview.flows.map((flow) => (
                      <Badge key={flow.id} variant="secondary">
                        {flow.name}
                      </Badge>
                    ))}
                  </div>
                </div>
              )}
            </CardContent>
          </Card>
      )}

      <Card className="overflow-visible">
        <ListToolbar
          busy={groupsQuery.isFetching && !groupsQuery.isPending}
          fill
          actions={
            <Button onClick={() => { setCreateOpen(true); }} data-testid="admin-groups-create">
              <Plus />
              {t("adminGroups.create")}
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
            placeholder={t("adminGroups.searchPlaceholder")}
            testId="admin-groups-search"
            className="w-full min-w-0 flex-1"
          />
        </ListToolbar>
        <CardContent className="px-0!">
          {loadError !== null ? (
            <ErrorState
              error={loadError}
              operationId="listPermissionGroups"
              onRetry={() => {
                void groupsQuery.refetch();
                void usersQuery.refetch();
                void flowsQuery.refetch();
              }}
            />
          ) : groupsQuery.isPending ? (
            <LoadingState variant="table" columns={5} />
          ) : (
            <>
              {groups.length === 0 ? (
                <EmptyState
                  title={hasSearchFilter ? t("table.emptySearch") : t("adminGroups.empty")}
                  filtered={hasSearchFilter}
                  testId="admin-groups-empty"
                />
              ) : (
                <Table data-testid="admin-groups-table">
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("adminGroups.column.name")}</TableHead>
                      <TableHead>{t("adminGroups.column.enabled")}</TableHead>
                      <TableHead>{t("adminGroups.column.members")}</TableHead>
                      <TableHead>{t("adminGroups.column.flows")}</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody ref={groupsBodyRef} stale={groupsQuery.isFetching && groupsQuery.isPlaceholderData}>
                    {groups.map((group) => (
                      <GroupRow key={group.id} group={group} userById={userById} flowById={flowById} onEdit={() => { setEditing(group); }} />
                    ))}
                  </TableBody>
                </Table>
              )}
              {groupsLoaded ? (
                <TablePagination
                  busy={groupsQuery.isFetching && groupsQuery.isPlaceholderData}
                  page={pager.pageNo}
                  hasMore={groupsQuery.data.page.has_more}
                  isFirst={pager.isFirst}
                  onPrev={pager.goPrev}
                  onJumpPage={pager.goToPage}
                  onNext={() => {
                    pager.pushCursor(groupsQuery.data.page.next_cursor);
                  }}
                  pageSize={pager.pageSize}
                  onPageSizeChange={pager.setPageSize}
                  rowsOnPage={groupsQuery.data.items.length}
                  testIdPrefix="groups"
                />
              ) : null}
            </>
          )}
        </CardContent>
      </Card>

      {createOpen ? (
        <GroupFormDialog
          key="create"
          editing={null}
          users={users}
          flows={flows}
          onClose={() => { setCreateOpen(false); }}
        />
      ) : null}
      {editing !== null ? (
        <GroupFormDialog
          key={editing.id}
          editing={editing}
          users={users}
          flows={flows}
          onClose={() => { setEditing(null); }}
        />
      ) : null}
    </PageLayout>
  );
}

function GroupRow({
  group,
  userById,
  flowById,
  onEdit,
}: {
  group: PermissionGroup;
  userById: Map<string, User>;
  flowById: Map<string, Flow>;
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  const deleteGroup = useDeletePermissionGroup();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);

  return (
    <>
      <TableRow data-testid={`admin-group-row-${group.id}`} data-row-key={group.id}>
        <TableCell className="font-semibold text-foreground">{group.name}</TableCell>
        <TableCell>
          <StatusBadge tone={group.enabled ? "emerald" : "stone"}>
            {group.enabled ? t("adminGroups.enabled") : t("adminGroups.disabled")}
          </StatusBadge>
        </TableCell>
        <TableCell className="text-muted-foreground max-w-56 truncate text-xs">
          {group.member_user_ids
            .map((id) => userById.get(id)?.username ?? id)
            .join(", ") || "—"}
        </TableCell>
        <TableCell className="text-muted-foreground max-w-56 truncate text-xs">
          {group.granted_flow_ids
            .map((id) => flowById.get(id)?.name ?? id)
            .join(", ") || "—"}
        </TableCell>
        <TableCell className="text-right">
          <TableRowActions testId={`admin-group-actions-${group.id}`}>
            <DropdownMenuItem onClick={onEdit} data-testid={`admin-group-edit-${group.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              onClick={() => {
                setErrorText(null);
                setConfirmOpen(true);
              }}
              data-testid={`admin-group-delete-${group.id}`}
            >
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </TableRowActions>
        </TableCell>
      </TableRow>

      <Dialog open={confirmOpen} onOpenChange={(next) => { if (!next) setConfirmOpen(false); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("adminGroups.delete.title", { name: group.name })}</DialogTitle>
            <DialogDescription>{t("adminGroups.delete.description")}</DialogDescription>
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
              pending={deleteGroup.isPending}
              onClick={() => {
                deleteGroup.mutate(
                  { groupId: group.id, version: group.version },
                  {
                    onSuccess: () => {
                      toast.success(t("common.delete"), { description: group.name });
                      setConfirmOpen(false);
                    },
                    onError: (error) => {
                      const msg = describeErrorText(describeError(error, "deletePermissionGroup"));
                      setErrorText(msg);
                      toast.error(msg);
                    },
                  },
                );
              }}
              data-testid="admin-group-delete-confirm"
            >
              {t("adminGroups.delete.confirm")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function GroupFormDialog({
  editing,
  users,
  flows,
  onClose,
}: {
  editing: PermissionGroup | null;
  users: User[];
  flows: Flow[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const createGroup = useCreatePermissionGroup();
  const replaceGroup = useReplacePermissionGroup();
  const [form, setForm] = useState<GroupFormState>(() =>
    editing === null
      ? { name: "", enabled: true, memberIds: [], flowIds: [] }
      : {
          name: editing.name,
          enabled: editing.enabled,
          memberIds: [...editing.member_user_ids],
          flowIds: [...editing.granted_flow_ids],
        },
  );
  const [errorText, setErrorText] = useState<string | null>(null);
  const [memberSearch, setMemberSearch] = useState("");
  const [flowSearch, setFlowSearch] = useState("");

  const toggle = (list: "memberIds" | "flowIds", id: string) => {
    setForm((current) => ({
      ...current,
      [list]: current[list].includes(id) ? current[list].filter((item) => item !== id) : [...current[list], id],
    }));
  };

  // Select-all applies to the FILTERED rows only (§18.46): checking it unions
  // the visible ids into the selection, unchecking removes exactly the
  // visible ones — rows hidden by the search keep their state untouched.
  const toggleAll = (list: "memberIds" | "flowIds", visibleIds: string[]) => {
    setForm((current) => {
      const selected = new Set(current[list]);
      const allVisibleSelected = visibleIds.length > 0 && visibleIds.every((id) => selected.has(id));
      for (const id of visibleIds) {
        if (allVisibleSelected) selected.delete(id);
        else selected.add(id);
      }
      return { ...current, [list]: [...selected] };
    });
  };

  const memberKeyword = memberSearch.trim().toLowerCase();
  const visibleUsers =
    memberKeyword === ""
      ? users
      : users.filter(
          (user) =>
            (user.display_name ?? "").toLowerCase().includes(memberKeyword) ||
            user.username.toLowerCase().includes(memberKeyword),
        );
  const flowKeyword = flowSearch.trim().toLowerCase();
  const visibleFlows =
    flowKeyword === "" ? flows : flows.filter((flow) => flow.name.toLowerCase().includes(flowKeyword));
  const visibleMemberIds = visibleUsers.map((user) => user.id);
  const visibleFlowIds = visibleFlows.map((flow) => flow.id);
  const selectedVisibleMembers = visibleMemberIds.filter((id) => form.memberIds.includes(id)).length;
  const selectedVisibleFlows = visibleFlowIds.filter((id) => form.flowIds.includes(id)).length;

  const submit = async () => {
    setErrorText(null);
    const body = {
      name: form.name,
      enabled: form.enabled,
      member_user_ids: form.memberIds,
      granted_flow_ids: form.flowIds,
    };
    try {
      if (editing === null) {
        await createGroup.mutateAsync(body);
      } else {
        await replaceGroup.mutateAsync({ groupId: editing.id, version: editing.version, body });
      }
      toast.success(editing === null ? t("common.createdToast") : t("common.savedToast"), { description: form.name });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, editing === null ? "createPermissionGroup" : "replacePermissionGroup"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing === null ? t("adminGroups.createTitle") : t("adminGroups.editTitle", { name: editing.name })}</DialogTitle>
          <DialogDescription>{t("adminGroups.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="group-name">{t("adminGroups.column.name")}</Label>
            <Input
              id="group-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              maxLength={128}
              data-testid="group-name"
            />
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <Checkbox
              checked={form.enabled}
              onCheckedChange={(checked) => { setForm({ ...form, enabled: checked }); }}
              data-testid="group-enabled"
            />
            {t("adminGroups.column.enabled")}
          </label>

          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Label>{t("adminGroups.members")}</Label>
              <TableSearchInput
                value={memberSearch}
                onChange={setMemberSearch}
                placeholder={t("adminGroups.memberSearchPlaceholder")}
                testId="group-member-search"
              />
            </div>
            <label className="flex cursor-pointer items-center gap-2 border-b pb-2 text-sm" data-testid="group-member-select-all-row">
              <Checkbox
                checked={visibleMemberIds.length > 0 && selectedVisibleMembers === visibleMemberIds.length}
                indeterminate={selectedVisibleMembers > 0 && selectedVisibleMembers < visibleMemberIds.length}
                onCheckedChange={() => { toggleAll("memberIds", visibleMemberIds); }}
                data-testid="group-member-select-all"
              />
              {t("adminGroups.selectAll")}
              <span className="text-muted-foreground text-xs tabular-nums" data-testid="group-member-select-all-count">
                {String(selectedVisibleMembers)}/{String(visibleMemberIds.length)}
              </span>
            </label>
            {/* Unified scrollbars (owner 2026-11-03): member/flow selection lists use ScrollArea
                overlay thumb; height cap sits on the viewport (see scroll-area.tsx comment). */}
            <ScrollArea className="rounded-md border p-2" viewportClassName="max-h-48">
              <div className="pr-3">
              {visibleUsers.length === 0 && (
                <p className="text-muted-foreground p-2 text-xs">{t("table.emptySearch")}</p>
              )}
              {visibleUsers.map((user) => (
                <label key={user.id} className="flex cursor-pointer items-center gap-2 px-1 py-1 text-sm">
                  <Checkbox
                    checked={form.memberIds.includes(user.id)}
                    onCheckedChange={() => { toggle("memberIds", user.id); }}
                    data-testid={`group-member-${user.id}`}
                  />
                  {user.display_name}
                  <span className="text-muted-foreground font-mono text-xs">{user.username}</span>
                </label>
              ))}
              </div>
            </ScrollArea>
          </div>

          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Label>{t("adminGroups.flows")}</Label>
              <TableSearchInput
                value={flowSearch}
                onChange={setFlowSearch}
                placeholder={t("adminGroups.flowSearchPlaceholder")}
                testId="group-flow-search"
              />
            </div>
            <p className="text-muted-foreground text-xs">{t("adminGroups.flowsHint")}</p>
            <label className="flex cursor-pointer items-center gap-2 border-b pb-2 text-sm" data-testid="group-flow-select-all-row">
              <Checkbox
                checked={visibleFlowIds.length > 0 && selectedVisibleFlows === visibleFlowIds.length}
                indeterminate={selectedVisibleFlows > 0 && selectedVisibleFlows < visibleFlowIds.length}
                onCheckedChange={() => { toggleAll("flowIds", visibleFlowIds); }}
                data-testid="group-flow-select-all"
              />
              {t("adminGroups.selectAll")}
              <span className="text-muted-foreground text-xs tabular-nums" data-testid="group-flow-select-all-count">
                {String(selectedVisibleFlows)}/{String(visibleFlowIds.length)}
              </span>
            </label>
            <ScrollArea className="rounded-md border p-2" viewportClassName="max-h-48">
              <div className="pr-3">
              {visibleFlows.length === 0 && (
                <p className="text-muted-foreground p-2 text-xs">
                  {flowKeyword !== "" ? t("table.emptySearch") : t("adminGroups.noFlows")}
                </p>
              )}
              {visibleFlows.map((flow) => (
                <label key={flow.id} className="flex cursor-pointer items-center gap-2 px-1 py-1 text-sm">
                  <Checkbox
                    checked={form.flowIds.includes(flow.id)}
                    onCheckedChange={() => { toggle("flowIds", flow.id); }}
                    data-testid={`group-flow-${flow.id}`}
                  />
                  {flow.name}
                  <Badge variant="outline">
                    {flow.flow_type === "change_review" ? t("adminGroups.flowKind.change") : t("adminGroups.flowKind.query")}
                  </Badge>
                </label>
              ))}
              </div>
            </ScrollArea>
          </div>

          {errorText !== null && (
            <Alert variant="destructive" data-testid="group-form-error">
              <AlertTitle>{t("adminGroups.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={createGroup.isPending || replaceGroup.isPending}
            disabled={form.name.trim() === ""}
            onClick={() => void submit()}
            data-testid="group-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
