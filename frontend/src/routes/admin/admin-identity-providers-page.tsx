import { useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, Pencil, Plus, PlugZap, Trash2 } from "lucide-react";
import type { IdentityProvider } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { NumberInput } from "@/shared/components/ui/number-input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { Label } from "@/shared/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { Spinner } from "@/shared/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import {
  useCreateIdentityProvider,
  useDeleteIdentityProvider,
  useIdentityProviders,
  useReplaceIdentityProvider,
  useTestIdentityProviderConnection,
} from "@/features/admin/use-admin";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";

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

interface LdapDraft {
  provider_key: string;
  display_name: string;
  enabled: boolean;
  host: string;
  port: number;
  transport: "ldaps" | "starttls";
  server_name: string;
  bind_dn: string;
  base_dn: string;
  user_filter: string;
  username_attribute: string;
  display_name_attribute: string;
  email_attribute: string;
  secret: string;
}

interface OidcDraft {
  provider_key: string;
  display_name: string;
  enabled: boolean;
  issuer_url: string;
  client_id: string;
  scopes: string;
  username_claim: string;
  display_name_claim: string;
  email_claim: string;
  secret: string;
}

const EMPTY_LDAP: LdapDraft = {
  provider_key: "ldap",
  display_name: "",
  enabled: false,
  host: "",
  port: 636,
  transport: "ldaps",
  server_name: "",
  bind_dn: "",
  base_dn: "",
  user_filter: "(&(objectClass=organizationalPerson)(uid={username}))",
  username_attribute: "uid",
  display_name_attribute: "cn",
  email_attribute: "mail",
  secret: "",
};

const EMPTY_OIDC: OidcDraft = {
  provider_key: "",
  display_name: "",
  enabled: false,
  issuer_url: "",
  client_id: "",
  scopes: "openid",
  username_claim: "preferred_username",
  display_name_claim: "name",
  email_claim: "email",
  secret: "",
};

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
                      <TableRow key={provider.id} data-testid={`admin-idp-row-${provider.id}`} data-row-key={provider.id}>
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
                          <DeleteProviderButton
                            provider={provider}
                            onEdit={() => { setEditing(provider); }}
                          />
                        </TableCell>
                      </TableRow>
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
        <ProviderFormDialog
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

function DeleteProviderButton({ provider, onEdit }: { provider: IdentityProvider; onEdit: () => void }) {
  const { t } = useTranslation();
  const deleteProvider = useDeleteIdentityProvider();
  const testConnection = useTestIdentityProviderConnection();
  const [open, setOpen] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);
  const [testTaskId, setTestTaskId] = useState<string | null>(null);
  return (
    <div className="flex flex-col items-end gap-1">
      <TableRowActions testId={`admin-idp-actions-${provider.id}`}>
        {/* Async connection test (contract §2: "async connection test"); the returned
            task id is surfaced below the kebab for the generic task read route. */}
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
                setErrorText(null);
                setTestTaskId(resolvedId);
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
          onClick={() => { setOpen(true); }}
          data-testid={`admin-idp-delete-${provider.id}`}
        >
          <Trash2 /> {t("common.delete")}
        </DropdownMenuItem>
      </TableRowActions>
      {testTaskId !== null && (
        <span className="text-muted-foreground font-mono text-xs" data-testid={`admin-idp-test-task-${provider.id}`}>
          {t("adminIdp.testTask", { id: testTaskId })}
        </span>
      )}
      {errorText !== null && (
        <span className="text-destructive text-xs" data-testid={`admin-idp-test-error-${provider.id}`}>
          {errorText}
        </span>
      )}
      <Dialog open={open} onOpenChange={(next) => { if (!next) setOpen(false); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("adminIdp.delete.title", { key: provider.provider_key })}</DialogTitle>
            <DialogDescription>{t("adminIdp.delete.description")}</DialogDescription>
          </DialogHeader>
          {errorText !== null && (
            <Alert variant="destructive">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setOpen(false); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant="destructive"
              pending={deleteProvider.isPending}
              onClick={() => { deleteProvider.mutate(
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
                      setErrorText(errText);
                      appToast.error(errText);
                    },
                  },
                ); }
              }
            >
              {t("common.delete")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function initialLdapDraft(editing: IdentityProvider | null): LdapDraft {
  if (editing !== null && editing.provider_kind === "ldap" && "host" in editing.configuration) {
    const configuration = editing.configuration;
    return {
      ...EMPTY_LDAP,
      provider_key: editing.provider_key,
      display_name: editing.display_name,
      enabled: editing.enabled,
      host: configuration.host,
      port: configuration.port,
      transport: configuration.transport,
      server_name: configuration.server_name,
      bind_dn: configuration.bind_dn,
      base_dn: configuration.base_dn,
      user_filter: configuration.user_filter,
      username_attribute: configuration.username_attribute,
      display_name_attribute: configuration.display_name_attribute,
      email_attribute: configuration.email_attribute ?? "",
      secret: "",
    };
  }
  return EMPTY_LDAP;
}

function initialOidcDraft(editing: IdentityProvider | null): OidcDraft {
  if (editing !== null && editing.provider_kind !== "ldap" && "issuer_url" in editing.configuration) {
    const configuration = editing.configuration;
    return {
      ...EMPTY_OIDC,
      provider_key: editing.provider_key,
      display_name: editing.display_name,
      enabled: editing.enabled,
      issuer_url: configuration.issuer_url,
      client_id: configuration.client_id,
      scopes: configuration.scopes.join(" "),
      username_claim: configuration.username_claim,
      display_name_claim: configuration.display_name_claim ?? "",
      email_claim: configuration.email_claim ?? "",
      secret: "",
    };
  }
  return EMPTY_OIDC;
}

function ProviderFormDialog({
  kind,
  editing,
  onClose,
}: {
  kind: "ldap" | "oidc";
  editing: IdentityProvider | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const createProvider = useCreateIdentityProvider();
  const replaceProvider = useReplaceIdentityProvider();
  const [ldapDraft, setLdapDraft] = useState<LdapDraft>(() => initialLdapDraft(editing));
  const [oidcDraft, setOidcDraft] = useState<OidcDraft>(() => initialOidcDraft(editing));
  const [errorText, setErrorText] = useState<string | null>(null);

  const submit = async () => {
    setErrorText(null);
    try {
      if (kind === "ldap") {
        const body = {
          provider_key: ldapDraft.provider_key,
          provider_kind: "ldap" as const,
          display_name: ldapDraft.display_name,
          enabled: ldapDraft.enabled,
          configuration: {
            host: ldapDraft.host,
            port: ldapDraft.port,
            transport: ldapDraft.transport,
            server_name: ldapDraft.server_name,
            bind_dn: ldapDraft.bind_dn,
            base_dn: ldapDraft.base_dn,
            user_filter: ldapDraft.user_filter,
            username_attribute: ldapDraft.username_attribute,
            display_name_attribute: ldapDraft.display_name_attribute,
            email_attribute: ldapDraft.email_attribute,
            connect_timeout_ms: 5000,
            bind_timeout_ms: 5000,
            search_timeout_ms: 5000,
          },
          bind_password: ldapDraft.secret === "" && editing !== null ? undefined : { value: ldapDraft.secret },
        };
        if (editing === null) await createProvider.mutateAsync(body);
        else
          await replaceProvider.mutateAsync({
            providerId: editing.id,
            version: editing.version,
            body: {
              provider_kind: "ldap",
              display_name: body.display_name,
              enabled: body.enabled,
              configuration: body.configuration,
              bind_password: body.bind_password,
            },
          });
      } else {
        const body = {
          provider_key: oidcDraft.provider_key,
          provider_kind: "oidc" as const,
          display_name: oidcDraft.display_name,
          enabled: oidcDraft.enabled,
          configuration: {
            issuer_url: oidcDraft.issuer_url,
            client_id: oidcDraft.client_id,
            scopes: oidcDraft.scopes.split(/\s+/).filter((scope) => scope !== ""),
            username_claim: oidcDraft.username_claim,
            display_name_claim: oidcDraft.display_name_claim,
            email_claim: oidcDraft.email_claim,
            connect_timeout_ms: 5000,
            request_timeout_ms: 10000,
          },
        };
        if (editing === null) {
          if (oidcDraft.secret === "") {
            setErrorText(t("adminIdp.formSecretRequired"));
            return;
          }
          await createProvider.mutateAsync({
            provider_key: body.provider_key,
            provider_kind: "oidc",
            display_name: body.display_name,
            enabled: body.enabled,
            configuration: body.configuration,
            client_secret: { value: oidcDraft.secret },
          });
        } else {
          const secretInput = oidcDraft.secret === "" ? undefined : { value: oidcDraft.secret };
          await replaceProvider.mutateAsync({
            providerId: editing.id,
            version: editing.version,
            body: {
              provider_kind: "oidc",
              display_name: body.display_name,
              enabled: body.enabled,
              configuration: body.configuration,
              client_secret: secretInput,
            },
          });
        }
      }
      appToast.success(t("common.save"));
      onClose();
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, editing === null ? "createIdentityProvider" : "replaceIdentityProvider"),
      );
      setErrorText(errText);
      appToast.error(errText);
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRound className="size-4" />
            {editing === null
              ? kind === "ldap"
                ? t("adminIdp.createLdap")
                : t("adminIdp.createOidc")
              : t("adminIdp.editTitle", { key: editing.provider_key })}
          </DialogTitle>
          <DialogDescription>{t("adminIdp.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {kind === "ldap" ? (
            <>
              <TextField label={t("adminIdp.field.displayName")} value={ldapDraft.display_name} onChange={(value) => { setLdapDraft({ ...ldapDraft, display_name: value }); }} testId="idp-ldap-display-name" />
              <TextField label={t("adminIdp.field.host")} value={ldapDraft.host} onChange={(value) => { setLdapDraft({ ...ldapDraft, host: value }); }} testId="idp-ldap-host" />
              <div className="flex flex-col gap-2">
                <Label htmlFor="idp-ldap-port">{t("adminIdp.field.port")}</Label>
                <NumberInput id="idp-ldap-port" min={1} max={65535} value={ldapDraft.port} onChange={(event) => { setLdapDraft({ ...ldapDraft, port: Number(event.target.value) }); }} data-testid="idp-ldap-port" />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="idp-ldap-transport">{t("adminIdp.field.transport")}</Label>
                <Select
                  value={ldapDraft.transport}
                  onValueChange={(value) => {
                    if (value !== "ldaps" && value !== "starttls") return;
                    setLdapDraft({ ...ldapDraft, transport: value });
                  }}
                >
                  <SelectTrigger
                    id="idp-ldap-transport"
                    className="w-48"
                    data-testid="idp-ldap-transport"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ldaps">ldaps</SelectItem>
                    <SelectItem value="starttls">starttls</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">{t("adminIdp.field.transportHint")}</p>
              </div>
              <TextField label={t("adminIdp.field.serverName")} value={ldapDraft.server_name} onChange={(value) => { setLdapDraft({ ...ldapDraft, server_name: value }); }} testId="idp-ldap-server-name" />
              <TextField label={t("adminIdp.field.bindDn")} value={ldapDraft.bind_dn} onChange={(value) => { setLdapDraft({ ...ldapDraft, bind_dn: value }); }} testId="idp-ldap-bind-dn" />
              <TextField label={t("adminIdp.field.baseDn")} value={ldapDraft.base_dn} onChange={(value) => { setLdapDraft({ ...ldapDraft, base_dn: value }); }} testId="idp-ldap-base-dn" />
              <TextField label={t("adminIdp.field.userFilter")} value={ldapDraft.user_filter} onChange={(value) => { setLdapDraft({ ...ldapDraft, user_filter: value }); }} testId="idp-ldap-user-filter" />
              <TextField label={t("adminIdp.field.usernameAttribute")} value={ldapDraft.username_attribute} onChange={(value) => { setLdapDraft({ ...ldapDraft, username_attribute: value }); }} testId="idp-ldap-username-attribute" />
              <TextField label={t("adminIdp.field.displayNameAttribute")} value={ldapDraft.display_name_attribute} onChange={(value) => { setLdapDraft({ ...ldapDraft, display_name_attribute: value }); }} testId="idp-ldap-display-name-attribute" />
              <TextField label={t("adminIdp.field.emailAttribute")} value={ldapDraft.email_attribute} onChange={(value) => { setLdapDraft({ ...ldapDraft, email_attribute: value }); }} testId="idp-ldap-email-attribute" />
            </>
          ) : (
            <>
              <TextField label={t("adminIdp.field.providerKey")} value={oidcDraft.provider_key} onChange={(value) => { setOidcDraft({ ...oidcDraft, provider_key: value }); }} testId="idp-oidc-key" disabled={editing !== null} />
              <TextField label={t("adminIdp.field.displayName")} value={oidcDraft.display_name} onChange={(value) => { setOidcDraft({ ...oidcDraft, display_name: value }); }} testId="idp-oidc-display-name" />
              <TextField label={t("adminIdp.field.issuerUrl")} value={oidcDraft.issuer_url} onChange={(value) => { setOidcDraft({ ...oidcDraft, issuer_url: value }); }} testId="idp-oidc-issuer" />
              <TextField label={t("adminIdp.field.clientId")} value={oidcDraft.client_id} onChange={(value) => { setOidcDraft({ ...oidcDraft, client_id: value }); }} testId="idp-oidc-client-id" />
              <TextField label={t("adminIdp.field.scopes")} value={oidcDraft.scopes} onChange={(value) => { setOidcDraft({ ...oidcDraft, scopes: value }); }} testId="idp-oidc-scopes" />
              <TextField label={t("adminIdp.field.usernameClaim")} value={oidcDraft.username_claim} onChange={(value) => { setOidcDraft({ ...oidcDraft, username_claim: value }); }} testId="idp-oidc-username-claim" />
              <TextField label={t("adminIdp.field.displayNameClaim")} value={oidcDraft.display_name_claim} onChange={(value) => { setOidcDraft({ ...oidcDraft, display_name_claim: value }); }} testId="idp-oidc-display-name-claim" />
              <TextField label={t("adminIdp.field.emailClaim")} value={oidcDraft.email_claim} onChange={(value) => { setOidcDraft({ ...oidcDraft, email_claim: value }); }} testId="idp-oidc-email-claim" />
              <p className="text-muted-foreground text-xs">{t("adminIdp.field.callbackNote")}</p>
            </>
          )}

          <div className="flex items-center gap-2">
            <Checkbox
              id="idp-enabled"
              checked={kind === "ldap" ? ldapDraft.enabled : oidcDraft.enabled}
              onCheckedChange={(checked) => {
                if (kind === "ldap") {
                  setLdapDraft({ ...ldapDraft, enabled: checked });
                } else {
                  setOidcDraft({ ...oidcDraft, enabled: checked });
                }
              }}
              data-testid="idp-enabled"
            />
            <Label htmlFor="idp-enabled" className="cursor-pointer">
              {t("adminIdp.field.enabled")}
            </Label>
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="idp-secret">{t("adminIdp.field.secret")}</Label>
            <PasswordInput
              id="idp-secret"
              value={kind === "ldap" ? ldapDraft.secret : oidcDraft.secret}
              onChange={(event) => {
                if (kind === "ldap") setLdapDraft({ ...ldapDraft, secret: event.target.value });
                else setOidcDraft({ ...oidcDraft, secret: event.target.value });
              }
              }
              data-testid="idp-secret"
            />
            <p className="text-muted-foreground text-xs">
              {editing === null
                ? t("adminIdp.field.secretCreateHint")
                : t("adminIdp.field.secretEditHint")}
            </p>
          </div>

          {errorText !== null && (
            <Alert variant="destructive" data-testid="idp-form-error">
              <AlertTitle>{t("adminIdp.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={createProvider.isPending || replaceProvider.isPending}
            disabled={editing === null && (kind === "ldap" ? ldapDraft.secret : oidcDraft.secret) === ""}
            onClick={() => void submit()}
            data-testid="idp-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TextField({
  label,
  value,
  onChange,
  testId,
  disabled = false,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  testId: string;
  disabled?: boolean;
}) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={testId}>{label}</Label>
      <Input id={testId} value={value} onChange={(event) => { onChange(event.target.value); }} data-testid={testId} disabled={disabled} />
    </div>
  );
}
