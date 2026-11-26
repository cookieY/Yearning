import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, ShieldCheck, X } from "lucide-react";
import type { PermissionGroup } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import {
  GroupFormDialog,
  GroupRow,
} from "@/routes/admin/admin-permission-group-dialogs";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import {
  useFlows,
  usePermissionGroups,
  useUsers,
} from "@/features/admin/use-admin";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";

/**
 * Permission Group Management (route /admin/permission-groups; migration contract §2 maps
 * legacy /manager/policy here, §3 field mapping: the three legacy
 * per-datasource transfer fields are DELETED — a group grants
 * members plus change/query FLOWS (P101: flows, never datasources or
 * workflow actors). The per-user effective permission preview derives from
 * the groups' server facts client-side (member ∪ granted flows). There is
 * deliberately no reviewer/executor authorization entry here.
 */

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
          group.enabled ? `enabled ${t("adminGroups.enabled")}` : `disabled ${t("adminGroups.disabled")}`,
        options: [
          { value: "enabled", label: t("adminGroups.enabled") },
          { value: "disabled", label: t("adminGroups.disabled") },
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
