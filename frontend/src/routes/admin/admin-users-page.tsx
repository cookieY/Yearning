import i18next from "@/shared/i18n";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Pencil, Plus, ShieldCheck, Trash2, UserRound } from "lucide-react";
import type { User, UserDeletionImpact } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { Card, CardContent } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import {
  useCreateUser,
  useDeletionImpact,
  useDeleteUser,
  useUpdateUser,
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

interface UserFormState {
  username: string;
  displayName: string;
  email: string;
  role: "user" | "admin";
  password: string;
  confirmPassword: string;
}

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
          user.is_builtin_admin ? t("adminUsers.builtinAdmin") : t("adminUsers.localUser"),
        options: [
          { label: t("adminUsers.builtinAdmin") },
          { label: t("adminUsers.localUser") },
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
        <TableCell className="font-semibold text-foreground">{user.username}</TableCell>
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

function DeleteUserDialog({
  user,
  open,
  impact,
  loading,
  submitting,
  errorText,
  onCancel,
  onConfirm,
}: {
  user: User;
  open: boolean;
  impact: UserDeletionImpact | null;
  loading: boolean;
  submitting: boolean;
  errorText: string | null;
  onCancel: () => void;
  onConfirm: () => Promise<void>;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onCancel(); }}>
      <DialogContent data-testid="admin-user-delete-dialog">
        <DialogHeader>
          <DialogTitle>{t("adminUsers.delete.title", { name: user.username })}</DialogTitle>
          <DialogDescription>{t("adminUsers.delete.description")}</DialogDescription>
        </DialogHeader>
        {loading && <LoadingState variant="inline" />}
        {impact !== null && (
          <div className="flex flex-col gap-2 text-sm" data-testid="admin-user-delete-impact">
            <ImpactRow label={t("adminUsers.delete.flowReferences")} value={impact.flow_actor_references} />
            <ImpactRow label={t("adminUsers.delete.groupMemberships")} value={impact.permission_group_memberships} />
            <ImpactRow label={t("adminUsers.delete.activeGrants")} value={impact.active_query_grants} />
            <ImpactRow label={t("adminUsers.delete.activeSessions")} value={impact.active_query_sessions} />
            <p className="text-muted-foreground text-xs">{t("adminUsers.delete.snapshotsPreserved")}</p>
            {impact.blockers.length > 0 && (
              <Alert variant="destructive">
                <AlertTitle>{t("adminUsers.delete.blocked")}</AlertTitle>
                <AlertDescription>
                  <ul className="list-disc pl-4">
                    {impact.blockers.map((blocker) => {
                      // §18.54: blocker codes are a backend/mock string set;
                      // render the translated sentence, raw code only as an
                      // unknown-code fallback.
                      const key = `adminUsers.delete.blocker.${blocker.code}`;
                      const text = i18next.exists(key) ? i18next.t(key) : blocker.code;
                      return (
                        <li key={blocker.code}>
                          {text} × {blocker.count}
                        </li>
                      );
                    })}
                  </ul>
                </AlertDescription>
              </Alert>
            )}
          </div>
        )}
        {errorText !== null && (
          <Alert variant="destructive" data-testid="admin-user-delete-error">
            <AlertDescription>{errorText}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={submitting}
            disabled={loading || submitting || (impact !== null && !impact.can_delete)}
            onClick={() => void onConfirm()}
            data-testid="admin-user-delete-confirm"
          >
            {t("adminUsers.delete.confirm")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ImpactRow({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex flex-row items-center justify-between gap-4">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="tabular-nums text-sm">{value}</span>
    </div>
  );
}

function CreateUserDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const createUser = useCreateUser();
  const [form, setForm] = useState<UserFormState>({
    username: "",
    displayName: "",
    email: "",
    role: "user",
    password: "",
    confirmPassword: "",
  });
  const [errorText, setErrorText] = useState<string | null>(null);

  // §18.54: the initial password must be retyped before the form can submit.
  const confirmMismatch =
    form.confirmPassword !== "" && form.confirmPassword !== form.password;
  // Email is optional; when filled it must look like an address, and the
  // failure must be visible instead of a silently disabled submit (§18.54).
  const emailInvalid =
    form.email.trim() !== "" && !/.+@.+\..+/.test(form.email);

  const valid =
    form.username.trim() !== "" &&
    form.displayName.trim() !== "" &&
    form.password.length >= 12 &&
    form.confirmPassword !== "" &&
    !confirmMismatch &&
    !emailInvalid;

  const submit = async () => {
    setErrorText(null);
    try {
      await createUser.mutateAsync({
        username: form.username,
        display_name: form.displayName,
        email: form.email.trim() === "" ? null : form.email,
        role: form.role,
        password: form.password,
      });
      setForm({ username: "", displayName: "", email: "", role: "user", password: "", confirmPassword: "" });
      toast.success(t("common.createdToast"), { description: form.username });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, "createUser"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent data-testid="admin-user-create-dialog">
        <DialogHeader>
          <DialogTitle>{t("adminUsers.createTitle")}</DialogTitle>
          <DialogDescription>{t("adminUsers.createDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-username">{t("adminUsers.column.username")}</Label>
            <Input
              id="user-create-username"
              value={form.username}
              onChange={(event) => { setForm({ ...form, username: event.target.value }); }}
              maxLength={64}
              data-testid="user-create-username"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-display">{t("adminUsers.column.displayName")}</Label>
            <Input
              id="user-create-display"
              value={form.displayName}
              onChange={(event) => { setForm({ ...form, displayName: event.target.value }); }}
              maxLength={128}
              data-testid="user-create-display"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-email">{t("adminUsers.column.email")}</Label>
            <Input
              id="user-create-email"
              type="email"
              value={form.email}
              onChange={(event) => { setForm({ ...form, email: event.target.value }); }}
              aria-invalid={emailInvalid || undefined}
              data-testid="user-create-email"
            />
            {emailInvalid && (
              <p className="text-destructive text-xs" data-testid="user-create-email-invalid">
                {t("adminUsers.emailInvalid")}
              </p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-role">{t("adminUsers.roleLabel")}</Label>
            <Select
              value={form.role}
              onValueChange={(value) => {
                if (value === "user" || value === "admin") {
                  setForm({ ...form, role: value });
                }
              }}
            >
              <SelectTrigger id="user-create-role" data-testid="user-create-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">{t("adminUsers.roleUser")}</SelectItem>
                <SelectItem value="admin">{t("adminUsers.roleAdmin")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-password">{t("adminUsers.password")}</Label>
            <PasswordInput
              id="user-create-password"
              autoComplete="new-password"
              value={form.password}
              onChange={(event) => { setForm({ ...form, password: event.target.value }); }}
              data-testid="user-create-password"
            />
            <p className="text-muted-foreground text-xs">{t("adminUsers.passwordHint")}</p>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-create-confirm-password">{t("adminUsers.confirmPassword")}</Label>
            <PasswordInput
              id="user-create-confirm-password"
              autoComplete="new-password"
              value={form.confirmPassword}
              onChange={(event) => { setForm({ ...form, confirmPassword: event.target.value }); }}
              aria-invalid={confirmMismatch || undefined}
              data-testid="user-create-confirm-password"
            />
            {confirmMismatch && (
              <p className="text-destructive text-xs" data-testid="user-create-confirm-mismatch">
                {t("adminUsers.confirmMismatch")}
              </p>
            )}
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="admin-user-create-error">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton pending={createUser.isPending} disabled={!valid} onClick={() => void submit()} data-testid="user-create-submit">
            {t("adminUsers.create")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function EditUserDialog({ user, onClose }: { user: User; onClose: () => void }) {
  const { t } = useTranslation();
  const updateUser = useUpdateUser();
  const [displayName, setDisplayName] = useState(() => user.display_name ?? "");
  const [email, setEmail] = useState(() => user.email ?? "");
  const [role, setRole] = useState<"user" | "admin">(() =>
    user.is_builtin_admin || user.role === "admin" ? "admin" : "user",
  );
  const [errorText, setErrorText] = useState<string | null>(null);

  const emailInvalid = email.trim() !== "" && !/.+@.+\..+/.test(email);

  const submit = async () => {
    setErrorText(null);
    try {
      await updateUser.mutateAsync({
        userId: user.id,
        version: user.version,
        body: {
          display_name: displayName,
          email: email.trim() === "" ? null : email,
          role: user.is_builtin_admin ? undefined : role,
        },
      });
      toast.success(t("common.savedToast"), { description: user.username });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, "updateUser"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent data-testid="admin-user-edit-dialog">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <UserRound className="size-4" />
            {t("adminUsers.editTitle", { name: user.username })}
          </DialogTitle>
          <DialogDescription>{t("adminUsers.editDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-edit-display">{t("adminUsers.column.displayName")}</Label>
            <Input
              id="user-edit-display"
              value={displayName}
              onChange={(event) => { setDisplayName(event.target.value); }}
              maxLength={128}
              data-testid="user-edit-display"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-edit-email">{t("adminUsers.column.email")}</Label>
            <Input
              id="user-edit-email"
              type="email"
              value={email}
              onChange={(event) => { setEmail(event.target.value); }}
              aria-invalid={emailInvalid || undefined}
              data-testid="user-edit-email"
            />
            {emailInvalid && (
              <p className="text-destructive text-xs" data-testid="user-edit-email-invalid">
                {t("adminUsers.emailInvalid")}
              </p>
            )}
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="user-edit-role">{t("adminUsers.roleLabel")}</Label>
            <Select
              value={role}
              disabled={user.is_builtin_admin}
              onValueChange={(value) => {
                if (value === "user" || value === "admin") {
                  setRole(value);
                }
              }}
            >
              <SelectTrigger id="user-edit-role" data-testid="user-edit-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">{t("adminUsers.roleUser")}</SelectItem>
                <SelectItem value="admin">{t("adminUsers.roleAdmin")}</SelectItem>
              </SelectContent>
            </Select>
            {user.is_builtin_admin && (
              <p className="text-muted-foreground text-xs">{t("adminUsers.builtinRoleLocked")}</p>
            )}
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="admin-user-edit-error">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={updateUser.isPending}
            disabled={displayName.trim() === "" || emailInvalid}
            onClick={() => void submit()}
            data-testid="user-edit-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
