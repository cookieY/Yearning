import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Pencil, Plus, ShieldCheck, Trash2, UserRound } from "lucide-react";
import type { User } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import {
  CreateUserDialog,
  DeleteUserDialog,
  EditUserDialog,
} from "@/routes/admin/admin-user-dialogs";
import { describeError, describeErrorText } from "@/shared/api/error-display";
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
import { Card, CardContent } from "@/shared/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import {
  useDeletionImpact,
  useDeleteUser,
  useUsers,
} from "@/features/admin/use-admin";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";

/**
 * User Management (route /admin/users; migration contract §2 maps legacy
 * /manager/user here, P102/P105). v4 model: create (username/display_name/
 * email/password), edit display_name/email only, delete behind a full
 * impact preview. There is NO role management, NO department/recorder
 * column (P102) and NO disabled state (P105: users have deletion but no
 * disable); the builtin admin row is immutable (1103). The legacy
 * per-user permission-group binding moved to the permission-group form.
 */

export default function AdminUsersPage() {
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
  // §18.73 + owner feedback 2026-11-05: free-text or server-indexed column
  // terms drive the server `q` parameter. The account-kind chip is a typed server
  // filter (RCP-20261105-ADMIN-LIST-FIELD-FILTERS), so cross-page narrowing
  // happens server-side; `filterBySearchTerms` only narrows further.
  const serverQ = useMemo(
    () =>
      [...effectiveTerms]
        .reverse()
        .find(
          (term) =>
            term.field === null ||
            term.field === "username" ||
            term.field === "displayName" ||
            term.field === "email",
        )
        ?.text.trim() ?? "",
    [effectiveTerms],
  );
  const kindParam = useMemo<"builtin_admin" | "local_user" | undefined>(() => {
    const KIND_VALUES = { [t("adminUsers.builtinAdmin")]: "builtin_admin", [t("adminUsers.localUser")]: "local_user" } as const;
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "kind" && candidate.text.trim() !== "");
    return term === undefined ? undefined : KIND_VALUES[term.text as keyof typeof KIND_VALUES];
  }, [effectiveTerms, t]);
  const usersQuery = useUsers(isAdmin, {
    limit: pager.pageSize,
    after: pager.after,
    q: serverQ || undefined,
    kind: kindParam,
  });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const usersLoaded = !usersQuery.isPending && !usersQuery.isError;
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<User | null>(null);

  const searchAccessors = useMemo<SearchAccessor<User>[]>(
    () => [
      {
        key: "username",
        label: t("adminUsers.column.username"),
        value: (user) => user.username,
      },
      {
        key: "displayName",
        label: t("adminUsers.column.displayName"),
        value: (user) => user.display_name ?? "",
      },
      {
        key: "email",
        label: t("adminUsers.column.email"),
        value: (user) => user.email ?? "",
      },
      {
        key: "kind",
        label: t("adminUsers.column.kind"),
        value: (user) =>
          user.is_builtin_admin ? `builtin_admin ${t("adminUsers.builtinAdmin")}` : `local_user ${t("adminUsers.localUser")}`,
        options: [
          { value: "builtin_admin", label: t("adminUsers.builtinAdmin") },
          { value: "local_user", label: t("adminUsers.localUser") },
        ],
      },
    ],
    [t],
  );
  const hasSearchFilter = effectiveTerms.some((term) => term.text.trim() !== "");
  const users = filterBySearchTerms(
    usersQuery.data?.items ?? [],
    effectiveTerms,
    searchAccessors,
  );
  // Server-side / client-side search: the same row may stay on screen but change position.
  const usersBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(users.map((user) => user.id)),
  );

  return (
    <PageLayout data-testid="admin-users-page">
      <PageHeader
        title={t("adminUsers.title")}
        description={t("adminUsers.description")}
        icon={UserRound}
      />

      <Card className="overflow-visible">
        <ListToolbar
          busy={usersQuery.isFetching && !usersQuery.isPending}
          fill
          actions={
            <Button onClick={() => { setCreateOpen(true); }} data-testid="admin-users-create">
              <Plus />
              {t("adminUsers.create")}
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
            placeholder={t("adminUsers.searchPlaceholder")}
            testId="admin-users-search"
            className="w-full min-w-0 flex-1"
          />
        </ListToolbar>
        <CardContent className="px-0!">
          {usersQuery.isPending ? (
            <LoadingState variant="table" columns={5} />
          ) : usersQuery.error !== null ? (
            <ErrorState error={usersQuery.error} operationId="listUsers" onRetry={() => void usersQuery.refetch()} />
          ) : (
            <>
              {users.length === 0 ? (
                <EmptyState
                  title={hasSearchFilter ? t("table.emptySearch") : t("adminUsers.empty")}
                  filtered={hasSearchFilter}
                  testId="admin-users-empty"
                />
              ) : (
              <Table data-testid="admin-users-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("adminUsers.column.username")}</TableHead>
                    <TableHead>{t("adminUsers.column.displayName")}</TableHead>
                    <TableHead>{t("adminUsers.column.email")}</TableHead>
                    <TableHead>{t("adminUsers.column.kind")}</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody ref={usersBodyRef} stale={usersQuery.isFetching && usersQuery.isPlaceholderData}>
                  {users.map((user) => (
                    <UserRow key={user.id} user={user} onEdit={() => { setEditing(user); }} />
                  ))}
                </TableBody>
              </Table>
              )}
              {usersLoaded ? (
                <TablePagination
                  busy={usersQuery.isFetching && usersQuery.isPlaceholderData}
                  page={pager.pageNo}
                  hasMore={usersQuery.data.page.has_more}
                  isFirst={pager.isFirst}
                  onPrev={pager.goPrev}
                  onJumpPage={pager.goToPage}
                  onNext={() => {
                    pager.pushCursor(usersQuery.data.page.next_cursor);
                  }}
                  pageSize={pager.pageSize}
                  onPageSizeChange={pager.setPageSize}
                  rowsOnPage={usersQuery.data.items.length}
                  testIdPrefix="users"
                />
              ) : null}
            </>
          )}
        </CardContent>
      </Card>

      <CreateUserDialog open={createOpen} onClose={() => { setCreateOpen(false); }} />
      {editing !== null ? (
        <EditUserDialog key={editing.id} user={editing} onClose={() => { setEditing(null); }} />
      ) : null}
    </PageLayout>
  );
}

function UserRow({ user, onEdit }: { user: User; onEdit: () => void }) {
  const { t } = useTranslation();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const impactQuery = useDeletionImpact(user.id, deleteOpen);
  const deleteUser = useDeleteUser();
  const [errorText, setErrorText] = useState<string | null>(null);

  return (
    <>
      <TableRow data-testid={`admin-user-row-${user.id}`} data-row-key={user.id}>
        <TableCell>
          <button
            type="button"
            onClick={onEdit}
            data-testid={`admin-user-profile-${user.id}`}
            className="text-left font-semibold text-foreground hover:underline focus-visible:outline-none focus-visible:underline"
          >
            {user.username}
          </button>
        </TableCell>
        <TableCell className="font-semibold text-foreground">{user.display_name}</TableCell>
        <TableCell className="text-muted-foreground">{user.email ?? "—"}</TableCell>
        <TableCell>
          <div className="flex flex-wrap items-center gap-1.5">
            {user.is_builtin_admin ? (
              <Badge variant="secondary" className="gap-0.5" data-testid={`admin-user-role-${user.id}`}>
                <ShieldCheck className="size-3" />
                {t("adminUsers.builtinAdmin")}
              </Badge>
            ) : user.role === "admin" ? (
              <Badge variant="secondary" className="gap-0.5" data-testid={`admin-user-role-${user.id}`}>
                <ShieldCheck className="size-3" />
                {t("adminUsers.roleAdmin")}
              </Badge>
            ) : (
              <Badge variant="outline" data-testid={`admin-user-role-${user.id}`}>
                {t("adminUsers.localUser")}
              </Badge>
            )}
          </div>
        </TableCell>
        <TableCell className="text-right">
          <TableRowActions testId={`admin-user-actions-${user.id}`}>
            <DropdownMenuItem onClick={onEdit} data-testid={`admin-user-edit-${user.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            {!user.is_builtin_admin && (
              <DropdownMenuItem
                variant="destructive"
                onClick={() => {
                  setErrorText(null);
                  setDeleteOpen(true);
                }}
                data-testid={`admin-user-delete-${user.id}`}
              >
                <Trash2 /> {t("common.delete")}
              </DropdownMenuItem>
            )}
          </TableRowActions>
        </TableCell>
      </TableRow>

      <DeleteUserDialog
        user={user}
        open={deleteOpen}
        impact={impactQuery.data ?? null}
        loading={impactQuery.isPending}
        submitting={deleteUser.isPending}
        errorText={errorText}
        onCancel={() => { setDeleteOpen(false); }}
        onConfirm={async () => {
          setErrorText(null);
          try {
            await deleteUser.mutateAsync({ userId: user.id, version: user.version });
            toast.success(t("common.delete"), { description: user.username });
            setDeleteOpen(false);
          } catch (error) {
            const msg = describeErrorText(describeError(error, "deleteUser"));
            setErrorText(msg);
            toast.error(msg);
          }
        }}
      />
    </>
  );
}
