import { useTranslation } from "react-i18next";
import type { Datasource } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { PasswordInput } from "@/shared/components/ui/password-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Textarea } from "@/shared/components/ui/textarea";

export const CREDENTIAL_GROUPS = ["readonly", "execution"] as const;
export type CredentialGroup = (typeof CREDENTIAL_GROUPS)[number];

/** The three explicit credential edit modes of the B13 contract; "keep" is
 * only legal for a group that already has a stored credential. */
export type CredentialMode = "replace" | "reuse" | "keep";

export const ENGINE_OPTIONS = [
  { value: "mysql", modes: ["mysql"] },
  { value: "postgresql", modes: ["postgresql"] },
  { value: "tidb", modes: ["mysql"] },
  { value: "oceanbase", modes: ["mysql"] },
  { value: "polardb", modes: ["mysql", "postgresql"] },
] as const;

export interface PurposeFormState {
  included: boolean;
  mode: CredentialMode;
  username: string;
  password: string;
  /** Source group for reuse mode (the other credential group with a stored
   * credential; the old row's secret is reused under the new username). */
  reuseSource: CredentialGroup | "";
}

export interface TLSFormState {
  /** Checked = submit a DatasourceTLSWrite block; unchecked = submit null
   * (full-replacement semantics: removal + plaintext). */
  enabled: boolean;
  caPem: string;
  clientCertPem: string;
  clientKeyPem: string;
}

/** Credential-class vocabulary of the connection-test purpose (§18.62,
 * aligned with the list's Read-Only/Read-Write badge): the select speaks two classes,
 * submit resolves them onto the contract's purpose enum — read-write probes
 * the execution credential, read-only probes review (falling back to query
 * when the payload keeps no review credential). */
export type TestCredentialKind = "readonly" | "readwrite";

export interface DatasourceFormState {
  name: string;
  engine: string;
  compatibilityMode: string;
  deploymentKind: string;
  host: string;
  port: string;
  databaseName: string;
  enabled: boolean;
  /** §18.61: connection-test purpose is persisted on the datasource — the table's "Test Connection" action selects
   * the stored credential by this attribute; changes are consolidated into this form (full-replacement semantics, written on submit). §18.62:
   * the form presents Read-Only / Read-Write states and resolves them to the contract enum on submit. */
  testKind: TestCredentialKind;
  purposes: Record<CredentialGroup, PurposeFormState>;
  tls: TLSFormState;
}

export function hasStoredCredentialGroup(
  storedStatus: Record<string, boolean> | null,
  group: CredentialGroup,
): boolean {
  if (storedStatus === null) return false;
  if (group === "readonly") {
    return storedStatus.review === true || storedStatus.query === true;
  }
  return storedStatus.execution === true;
}

function freshPurposes(): Record<CredentialGroup, PurposeFormState> {
  return {
    readonly: { included: true, mode: "replace", username: "", password: "", reuseSource: "" },
    execution: { included: false, mode: "replace", username: "", password: "", reuseSource: "" },
  };
}

export function formForCreate(): DatasourceFormState {
  return {
    name: "",
    engine: "mysql",
    compatibilityMode: "mysql",
    deploymentKind: "native",
    host: "",
    port: "3306",
    databaseName: "",
    enabled: true,
    testKind: "readonly",
    purposes: freshPurposes(),
    tls: { enabled: false, caPem: "", clientCertPem: "", clientKeyPem: "" },
  };
}

export function formForEdit(ds: Datasource): DatasourceFormState {
  const purposes = freshPurposes();
  for (const group of CREDENTIAL_GROUPS) {
    const configured = hasStoredCredentialGroup(ds.credential_status, group);
    purposes[group].included = configured;
    // Configured rows default to keep — the no-op that preserves the stored
    // credential; unconfigured rows can only replace.
    purposes[group].mode = configured ? "keep" : "replace";
  }
  return {
    name: ds.name,
    engine: ds.engine,
    compatibilityMode: ds.compatibility_mode,
    deploymentKind: ds.deployment_kind,
    host: ds.host,
    port: String(ds.port),
    databaseName: ds.database_name ?? "",
    enabled: ds.enabled,
    testKind: ds.test_purpose === "execution" ? "readwrite" : "readonly",
    purposes,
    // Materials never echo; the checkbox reflects the current state only.
    tls: { enabled: ds.tls_verified, caPem: "", clientCertPem: "", clientKeyPem: "" },
  };
}

export function CredentialPurposeRow({
  purpose,
  state,
  storedStatus,
  onChange,
}: {
  purpose: CredentialGroup;
  state: PurposeFormState;
  /** Stored credential presence on the edited row (null on create): keep
   * and reuse resolve OLD stored rows only, so candidates come from the
   * view's credential_status — a group newly added in the same payload
   * cannot source a stored secret. */
  storedStatus: Record<string, boolean> | null;
  onChange: (next: PurposeFormState) => void;
}) {
  const { t } = useTranslation();
  const configured = hasStoredCredentialGroup(storedStatus, purpose);
  const reuseSources = CREDENTIAL_GROUPS.filter(
    (candidate) => candidate !== purpose && hasStoredCredentialGroup(storedStatus, candidate),
  );
  return (
    <div className="rounded-lg border p-3" data-testid={`credential-row-${purpose}`}>
      <div className="mb-2 flex items-center gap-2">
        <input
          type="checkbox"
          id={`credential-include-${purpose}`}
          checked={state.included}
          onChange={(event) => { onChange({ ...state, included: event.target.checked }); }}
          className="size-4"
          data-testid={`credential-include-${purpose}`}
        />
        <Label htmlFor={`credential-include-${purpose}`} className="cursor-pointer">
          {t(`admin.datasources.purpose.${purpose}`)}
        </Label>
        {configured ? (
          <Badge variant="secondary">{t("admin.datasources.credentialConfigured")}</Badge>
        ) : null}
      </div>
      {state.included ? (
        <div className="flex flex-col gap-2">
          <div className="flex flex-col gap-1">
            <Label>{t("admin.datasources.credentialMode")}</Label>
            <Select
              value={state.mode}
              onValueChange={(next) => {
                if (next === null) return;
                // keep hides username/password entirely; switching away
                // starts from empty values so no stale secret input rides
                // along into the payload.
                onChange({
                  ...state,
                  mode: next,
                  username: next === "keep" ? "" : state.username,
                  password: next === "replace" ? state.password : "",
                  reuseSource: next === "reuse" ? state.reuseSource : "",
                });
              }}
              items={{
                replace: t("admin.datasources.modeReplace"),
                ...(reuseSources.length > 0
                  ? { reuse: t("admin.datasources.modeReuse") }
                  : {}),
                ...(configured ? { keep: t("admin.datasources.modeKeep") } : {}),
              }}
            >
              <SelectTrigger data-testid={`credential-mode-${purpose}`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="replace">{t("admin.datasources.modeReplace")}</SelectItem>
                {reuseSources.length > 0 ? (
                  <SelectItem value="reuse">{t("admin.datasources.modeReuse")}</SelectItem>
                ) : null}
                {configured ? (
                  <SelectItem value="keep">{t("admin.datasources.modeKeep")}</SelectItem>
                ) : null}
              </SelectContent>
            </Select>
          </div>
          {state.mode === "keep" ? (
            <p className="text-muted-foreground text-xs" data-testid={`credential-keep-note-${purpose}`}>
              {t("admin.datasources.keepNote")}
            </p>
          ) : null}
          {state.mode !== "keep" ? (
            <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={`credential-username-${purpose}`}>
                  {t("admin.datasources.username")}
                  <span className="text-destructive" aria-hidden>*</span>
                </Label>
                <Input
                  id={`credential-username-${purpose}`}
                  value={state.username}
                  onChange={(event) => { onChange({ ...state, username: event.target.value }); }}
                  autoComplete="off"
                  data-testid={`credential-username-${purpose}`}
                />
              </div>
              {state.mode === "replace" ? (
                <div className="flex flex-col gap-1">
                  <Label htmlFor={`credential-password-${purpose}`}>
                    {t("admin.datasources.password")}
                    <span className="text-destructive" aria-hidden>*</span>
                  </Label>
                  <PasswordInput
                    id={`credential-password-${purpose}`}
                    value={state.password}
                    onChange={(event) => { onChange({ ...state, password: event.target.value }); }}
                    autoComplete="new-password"
                    data-testid={`credential-password-${purpose}`}
                  />
                </div>
              ) : null}
              {state.mode === "reuse" ? (
                <div className="flex flex-col gap-1">
                  <Label>
                    {t("admin.datasources.reuseFrom")}
                    <span className="text-destructive" aria-hidden>*</span>
                  </Label>
                  <Select
                    value={state.reuseSource === "" ? null : state.reuseSource}
                    onValueChange={(next) => {
                      if (next === null) return;
                      onChange({ ...state, reuseSource: next });
                    }}
                  >
                    <SelectTrigger data-testid={`credential-reuse-${purpose}`}>
                      <SelectValue placeholder={t("admin.datasources.reusePlaceholder")} />
                    </SelectTrigger>
                    <SelectContent>
                      {reuseSources.map((candidate) => (
                        <SelectItem key={candidate} value={candidate}>
                          {t("admin.datasources.reuseCopyOf", { purpose: t(`admin.datasources.purpose.${candidate}`) })}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              ) : null}
            </div>
          ) : null}
          {state.mode === "reuse" ? (
            <p className="text-xs text-[var(--risk-warning)]">
              {t("admin.datasources.reuseRiskHint")}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** TLS material section (declared DatasourceTLSWrite). Write-only PEM
 * textareas that never prefill; the client mirrors the pairing rule
 * (cert+key together) and the backend stays authoritative on PEM shape. */
export function TLSMaterialSection({
  state,
  wasVerified,
  onChange,
}: {
  state: TLSFormState;
  /** tls_verified of the edited row: unchecking on a verified row is the
   * declared removal path and must say so before the submit. */
  wasVerified: boolean;
  onChange: (next: TLSFormState) => void;
}) {
  const { t } = useTranslation();
  const pairingBroken =
    state.enabled && (state.clientCertPem === "") !== (state.clientKeyPem === "");
  const noMaterial =
    state.enabled && state.caPem === "" && state.clientCertPem === "" && state.clientKeyPem === "";
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <input
          type="checkbox"
          id="ds-tls-enabled"
          checked={state.enabled}
          onChange={(event) => { onChange({ ...state, enabled: event.target.checked }); }}
          className="size-4"
          data-testid="ds-tls-enabled"
        />
        <Label htmlFor="ds-tls-enabled" className="cursor-pointer">
          {t("admin.datasources.tlsTitle")}
        </Label>
        {wasVerified ? (
          <Badge variant="secondary">{t("admin.datasources.tlsVerifiedBadge")}</Badge>
        ) : null}
      </div>
      {!state.enabled && wasVerified ? (
        <Alert variant="destructive" data-testid="ds-tls-removal-warning">
          <AlertTitle>{t("admin.datasources.tlsRemovalTitle")}</AlertTitle>
          <AlertDescription>{t("admin.datasources.tlsRemovalBody")}</AlertDescription>
        </Alert>
      ) : null}
      {state.enabled ? (
        <>
          <p className="text-muted-foreground text-xs" data-testid="ds-tls-hint">
            {t("admin.datasources.tlsHint")}
          </p>
          <div className="flex flex-col gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-tls-ca">{t("admin.datasources.tlsCa")}</Label>
              <Textarea
                id="ds-tls-ca"
                rows={3}
                value={state.caPem}
                onChange={(event) => { onChange({ ...state, caPem: event.target.value }); }}
                autoComplete="off"
                spellCheck={false}
                className="font-mono text-xs"
                placeholder="-----BEGIN CERTIFICATE-----"
                data-testid="ds-tls-ca"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-tls-cert">{t("admin.datasources.tlsCert")}</Label>
              <Textarea
                id="ds-tls-cert"
                rows={3}
                value={state.clientCertPem}
                onChange={(event) => { onChange({ ...state, clientCertPem: event.target.value }); }}
                autoComplete="off"
                spellCheck={false}
                className="font-mono text-xs"
                placeholder="-----BEGIN CERTIFICATE-----"
                data-testid="ds-tls-cert"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-tls-key">{t("admin.datasources.tlsKey")}</Label>
              <Textarea
                id="ds-tls-key"
                rows={3}
                value={state.clientKeyPem}
                onChange={(event) => { onChange({ ...state, clientKeyPem: event.target.value }); }}
                autoComplete="off"
                spellCheck={false}
                className="font-mono text-xs"
                placeholder="-----BEGIN PRIVATE KEY-----"
                data-testid="ds-tls-key"
              />
              <p className="text-muted-foreground text-xs">{t("admin.datasources.tlsPairHint")}</p>
            </div>
            {noMaterial ? (
              <p className="text-xs text-destructive" data-testid="ds-tls-empty-error">
                {t("admin.datasources.tlsEmptyError")}
              </p>
            ) : null}
            {pairingBroken ? (
              <p className="text-xs text-destructive" data-testid="ds-tls-pair-error">
                {t("admin.datasources.tlsPairError")}
              </p>
            ) : null}
          </div>
        </>
      ) : null}
    </div>
  );
}
