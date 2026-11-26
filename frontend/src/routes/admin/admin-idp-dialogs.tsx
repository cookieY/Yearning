import { useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound } from "lucide-react";
import type { IdentityProvider } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateIdentityProvider,
  useReplaceIdentityProvider,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Checkbox } from "@/shared/components/ui/checkbox";
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
import { NumberInput } from "@/shared/components/ui/number-input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

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

export function IdentityProviderFormDialog({
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
              }}
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
