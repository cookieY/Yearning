import { Fragment, useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, Pencil, Plus, PlugZap, Trash2 } from "lucide-react";
import type { IdentityProvider } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { IdentityProviderFormDialog } from "@/routes/admin/admin-idp-dialogs";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Spinner } from "@/shared/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import {
  useDeleteIdentityProvider,
  useIdentityProviders,
  useTestIdentityProviderConnection,
} from "@/features/admin/use-admin";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { TableRowErrorRow } from "@/shared/components/table-row-error-row";

/**
 * Identity Provider Management (route /admin/identity-providers; migration contract §2
 * maps the legacy LDAP settings block here; PRD F9 item 7 delivered by
 * F10). LDAP is a singleton (create refused while one exists); OIDC is a
 * list. Secrets are never prefilled: the view carries secret_configured only — create
 * requires a secret, replace keeps (omit) / replaces (new value) / clears
 * (null), and OIDC secrets cannot be cleared while the provider is enabled.
 * The OIDC callback address is server-generated and not part of this
 * surface. LDAP transport is ldaps/starttls only — there is no plaintext
 * transport and no skip-verify option in the declared contract.
 */

function LdapConnectionTest({ providerId }: { providerId: string }) {
  const { t } = useTranslation();
  const testConnection = useTestIdentityProviderConnection();
  const [taskId, setTaskId] = useState<string | null>(null);
  const [errorText, setErrorText] = useState<string | null>(null);
  return (
    <div className="flex flex-col gap-0.5">
      {/* Async connection test (contract §2: "async connection test") — the same
          hook the OIDC table rows use. */}
      <PendingButton
        size="sm"
        variant="outline"
        pending={testConnection.isPending}
        onClick={() => {
          testConnection.mutate(providerId, {
            onSuccess: (task) => {
              const outcome = "outcome" in task && typeof task.outcome === "string" ? task.outcome : undefined;
              const resolvedId =
                typeof task.id === "string" && task.id.length > 0
                  ? task.id
                  : (outcome ?? "succeeded");
              setErrorText(null);
              setTaskId(resolvedId);
              appToast.info(t("adminIdp.testConnection"), {
                description: t("adminIdp.testTask", { id: resolvedId }),
              });
            },
            onError: (error) => {
              const msg = describeErrorText(describeError(error, "testIdentityProviderConnection"));
              setErrorText(msg);
              appToast.error(msg);
            },
          });
        }}
        data-testid={`admin-idp-test-${providerId}`}
      >
        {t("adminIdp.testConnection")}
      </PendingButton>
      {taskId !== null && (
        <span className="text-muted-foreground font-mono text-xs" data-testid={`admin-idp-test-task-${providerId}`}>
          {t("adminIdp.testTask", { id: taskId })}
        </span>
      )}
      {errorText !== null && (
        <span className="text-destructive text-xs" data-testid={`admin-idp-test-error-${providerId}`}>
          {errorText}
        </span>
      )}
    </div>
  );
}

export default function AdminIdentityProvidersPage() {
  const { t } = useTranslation();

  return (
    <PageLayout data-testid="admin-idp-page">
      <PageHeader
        title={t("adminIdp.title")}
        description={t("adminIdp.description")}
        icon={KeyRound}
      />
      <AdminIdentityProvidersContent />
    </PageLayout>
  );
}

export function AdminIdentityProvidersContent({ embedded = false }: { embedded?: boolean }) {
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const pager = useCursorPage(20);
  const providersQuery = useIdentityProviders(isAdmin, { limit: pager.pageSize, after: pager.after });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const providersLoaded = !providersQuery.isPending && !providersQuery.isError;
  const [editing, setEditing] = useState<IdentityProvider | null>(null);
  const [createKind, setCreateKind] = useState<"ldap" | "oidc" | null>(null);

  const providers = providersQuery.data?.items ?? [];
  const ldap = providers.find((provider) => provider.provider_kind === "ldap") ?? null;
  const oidcProviders = providers.filter((provider) => provider.provider_kind === "oidc");
  // Both pagination and create/delete change this row set; only OIDC rows participate (LDAP is a singleton card).
  const oidcBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(oidcProviders.map((provider) => provider.id)),
  );

  return (
    <div className="flex flex-col gap-5" data-testid={embedded ? "settings-general-idp" : undefined}>
      {providersQuery.isPending && (
        <LoadingState variant={embedded ? "panel" : "page"} columns={embedded ? 2 : undefined} />
      )}
      {providersQuery.error !== null && (
        <ErrorState error={providersQuery.error} operationId="listIdentityProviders" onRetry={() => void providersQuery.refetch()} />
      )}

      {!providersQuery.isPending && providersQuery.error === null && (
        <>
          <Card>
            <CardHeader className="pb-3">
              <CardTitle>{t("adminIdp.ldapCard")}</CardTitle>
              <CardDescription>{t("adminIdp.ldapDescription")}</CardDescription>
              <CardAction>
                <Button variant="outline" disabled={ldap !== null} onClick={() => { setCreateKind("ldap"); }} data-testid="admin-idp-create-ldap">
                  <Plus />
                  {t("adminIdp.createLdap")}
                </Button>
              </CardAction>
            </CardHeader>
            <CardContent>
              {ldap === null ? (
                <EmptyState title={t("adminIdp.ldapEmpty")} testId="admin-idp-ldap-empty" />
              ) : (
                <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border px-3 py-2" data-testid={`admin-idp-row-${ldap.id}`}>
                  <div className="flex min-w-0 flex-col">
                    <span className="text-sm font-medium">{ldap.display_name}</span>
                    <span className="text-muted-foreground truncate font-mono text-xs">
                      {"host" in ldap.configuration
                        ? `${ldap.configuration.host}:${String(ldap.configuration.port)} · ${ldap.configuration.transport}`
                        : ""}
                    </span>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={ldap.enabled ? "secondary" : "outline"}>
                      {ldap.enabled ? t("adminIdp.enabled") : t("adminIdp.disabled")}
                    </Badge>
                    <Badge variant="outline">{ldap.secret_configured ? t("adminIdp.secretConfigured") : t("adminIdp.secretMissing")}</Badge>
                    <LdapConnectionTest providerId={ldap.id} />
                    <Button size="sm" variant="outline" onClick={() => { setEditing(ldap); }} data-testid={`admin-idp-edit-${ldap.id}`}>
                      {t("common.edit")}
                    </Button>
                  </div>
                </div>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="border-b pb-3">
              <CardTitle>{t("adminIdp.oidcCard")}</CardTitle>
              <CardDescription>{t("adminIdp.oidcDescription")}</CardDescription>
              <CardAction>
                <Button onClick={() => { setCreateKind("oidc"); }} data-testid="admin-idp-create-oidc">
                  <Plus />
                  {t("adminIdp.createOidc")}
                </Button>
              </CardAction>
            </CardHeader>
            <CardContent className="px-0!">
              {oidcProviders.length === 0 ? (
                <EmptyState title={t("adminIdp.oidcEmpty")} testId="admin-idp-oidc-empty" />
              ) : (
                <>
                <Table data-testid="admin-idp-oidc-table">
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("adminIdp.column.key")}</TableHead>
                      <TableHead>{t("adminIdp.column.issuer")}</TableHead>
                      <TableHead>{t("adminIdp.column.state")}</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody ref={oidcBodyRef} stale={providersQuery.isFetching && providersQuery.isPlaceholderData}>
                    {oidcProviders.map((provider) => (
                      <OidcProviderRow
                        key={provider.id}
                        provider={provider}
                        onEdit={() => { setEditing(provider); }}
                      />
                    ))}
                  </TableBody>
                </Table>
                </>
              )}
              {providersLoaded ? (
                <TablePagination
                  busy={providersQuery.isFetching && providersQuery.isPlaceholderData}
                  page={pager.pageNo}
                  hasMore={providersQuery.data.page.has_more}
                  isFirst={pager.isFirst}
                  onPrev={pager.goPrev}
                  onJumpPage={pager.goToPage}
                  onNext={() => {
                    pager.pushCursor(providersQuery.data.page.next_cursor);
                  }}
                  pageSize={pager.pageSize}
                  onPageSizeChange={pager.setPageSize}
                  rowsOnPage={providersQuery.data.items.length}
                  testIdPrefix="idp"
                />
              ) : null}
            </CardContent>
          </Card>
        </>
      )}

      {createKind !== null || editing !== null ? (
        <IdentityProviderFormDialog
          key={editing?.id ?? `create-${createKind ?? "oidc"}`}
          kind={editing?.provider_kind ?? createKind ?? "oidc"}
          editing={editing}
          onClose={() => {
            setEditing(null);
            setCreateKind(null);
          }}
        />
      ) : null}
    </div>
  );
}

function OidcProviderRow({ provider, onEdit }: { provider: IdentityProvider; onEdit: () => void }) {
  const { t } = useTranslation();
  const deleteProvider = useDeleteIdentityProvider();
  const testConnection = useTestIdentityProviderConnection();
  const [open, setOpen] = useState(false);
  const [testErrorText, setTestErrorText] = useState<string | null>(null);
  const [deleteErrorText, setDeleteErrorText] = useState<string | null>(null);
  const [testTaskId, setTestTaskId] = useState<string | null>(null);
  return (
    <Fragment>
      <TableRow data-testid={`admin-idp-row-${provider.id}`} data-row-key={provider.id}>
        <TableCell className="font-mono font-semibold text-foreground">{provider.provider_key}</TableCell>
        <TableCell className="max-w-72 truncate font-mono">
          {(provider.configuration as { issuer_url?: string }).issuer_url ?? ""}
        </TableCell>
        <TableCell>
          <StatusBadge tone={provider.enabled ? "emerald" : "stone"}>
            {provider.enabled ? t("adminIdp.enabled") : t("adminIdp.disabled")}
          </StatusBadge>
        </TableCell>
        <TableCell className="text-right">
          <div className="flex items-center justify-end gap-2">
            {testTaskId !== null && (
              <span className="text-muted-foreground font-mono text-xs" data-testid={`admin-idp-test-task-${provider.id}`}>
                {t("adminIdp.testTask", { id: testTaskId })}
              </span>
            )}
            <TableRowActions testId={`admin-idp-actions-${provider.id}`}>
              {/* Async connection test (contract §2: "async connection test"); the returned
                  task id is surfaced beside the kebab for the generic task read route. */}
              <DropdownMenuItem
                disabled={testConnection.isPending}
                aria-busy={testConnection.isPending || undefined}
                onClick={() => {
                  testConnection.mutate(provider.id, {
                    onSuccess: (task) => {
                      const outcome = "outcome" in task && typeof task.outcome === "string" ? task.outcome : undefined;
                      const resolvedId =
                        typeof task.id === "string" && task.id.length > 0
                          ? task.id
                          : (outcome ?? "succeeded");
                      setTestErrorText(null);
                      setTestTaskId(resolvedId);
                      appToast.info(t("adminIdp.testConnection"), {
                        description: t("adminIdp.testTask", { id: resolvedId }),
                      });
                    },
                    onError: (error) => {
                      const msg = describeErrorText(describeError(error, "testIdentityProviderConnection"));
                      setTestErrorText(msg);
                      appToast.error(msg);
                    },
                  });
                }}
                data-testid={`admin-idp-test-${provider.id}`}
              >
                {testConnection.isPending ? <Spinner aria-hidden /> : <PlugZap />}
                {t("adminIdp.testConnection")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={onEdit} data-testid={`admin-idp-edit-${provider.id}`}>
                <Pencil /> {t("common.edit")}
              </DropdownMenuItem>
              <DropdownMenuItem
                variant="destructive"
                disabled={provider.enabled}
                onClick={() => {
                  setDeleteErrorText(null);
                  setOpen(true);
                }}
                data-testid={`admin-idp-delete-${provider.id}`}
              >
                <Trash2 /> {t("common.delete")}
              </DropdownMenuItem>
            </TableRowActions>
          </div>
          <Dialog open={open} onOpenChange={(next) => { if (!next) setOpen(false); }}>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>{t("adminIdp.delete.title", { key: provider.provider_key })}</DialogTitle>
                <DialogDescription>{t("adminIdp.delete.description")}</DialogDescription>
              </DialogHeader>
              {deleteErrorText !== null && (
                <Alert variant="destructive">
                  <AlertDescription>{deleteErrorText}</AlertDescription>
                </Alert>
              )}
              <DialogFooter>
                <Button variant="outline" onClick={() => { setOpen(false); }}>
                  {t("common.cancel")}
                </Button>
                <PendingButton
                  variant="destructive"
                  pending={deleteProvider.isPending}
                  onClick={() => {
                    deleteProvider.mutate(
                      { providerId: provider.id, version: provider.version },
                      {
                        onSuccess: () => {
                          appToast.success(t("common.delete"), {
                            description: provider.display_name,
                          });
                          setOpen(false);
                        },
                        onError: (error) => {
                          const errText = describeErrorText(describeError(error, "deleteIdentityProvider"));
                          setDeleteErrorText(errText);
                          appToast.error(errText);
                        },
                      },
                    );
                  }}
                >
                  {t("common.delete")}
                </PendingButton>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </TableCell>
      </TableRow>
      {testErrorText !== null ? (
        <TableRowErrorRow
          colSpan={4}
          summary={testErrorText}
          testId={`admin-idp-test-error-${provider.id}`}
          copyTestId={`admin-idp-test-error-copy-${provider.id}`}
          closeTestId={`admin-idp-test-error-close-${provider.id}`}
          onClose={() => { setTestErrorText(null); }}
        />
      ) : null}
    </Fragment>
  );
}
