import { Fragment, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import { Activity, Check, Database, Minus, Pencil, PlugZap, Plus, Settings2, Trash2, X } from "lucide-react";
import type {
  CredentialWrite,
  Datasource,
  DatasourceWrite,
  UpdateDatasourceDiagnosticsConfigRequestIntervalSeconds,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useAdminTask,
  useCreateDatasource,
  useDatasourceCapabilities,
  useDatasourceDiagnosticsConfig,
  useDatasources,
  useDeleteDatasource,
  useReplaceDatasource,
  useTestDatasourceConnection,
  useUpdateDatasourceDiagnosticsConfig,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
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
import { NumberInput } from "@/shared/components/ui/number-input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { Label } from "@/shared/components/ui/label";
import { Textarea } from "@/shared/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import {
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
} from "@/shared/components/ui/dropdown-menu";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";

/**
 * Datasource Management (route /admin/datasources; migration contract §2 maps the
 * legacy /manager/db here with the §3 field reorganization). The form carries
 * Engine / Compatibility Mode / Deployment Type / Version Constraint and the Review/Query/Execution purpose
 * credentials; the legacy flow_id/rule_id/principal/is_query/SSL-file fields
 * are consciously gone. Secrets are write-only: the API never echoes a
 * credential or TLS material, the form never prefills one, and every
 * credential row edits in exactly ONE explicit mode (B13 contract):
 * replace (username + fresh password), reuse (username + another purpose's
 * stored secret) or keep (only offered on a row that already has a stored
 * credential; the payload then carries reuse_credential_purpose equal to the
 * row's own purpose and no username at all).
 *
 * TLS material is the declared DatasourceTLSWrite surface: CA / client cert
 * / client key are write-only PEM textareas, the cert and key must be
 * supplied together, and the block replaces fully — submitting without the
 * block removes every stored material and returns the datasource to
 * plaintext, so keeping verified TLS means re-entering the full material on
 * every save. The list surfaces tls_verified (verified-vs-plaintext) and
 * nothing about the material itself.
 */

const CREDENTIAL_GROUPS = ["readonly", "execution"] as const;
type CredentialGroup = (typeof CREDENTIAL_GROUPS)[number];

/** The three explicit credential edit modes of the B13 contract; "keep" is
 * only legal for a group that already has a stored credential. */
type CredentialMode = "replace" | "reuse" | "keep";

const ENGINE_OPTIONS = [
  { value: "mysql", modes: ["mysql"] },
  { value: "postgresql", modes: ["postgresql"] },
  { value: "tidb", modes: ["mysql"] },
  { value: "oceanbase", modes: ["mysql"] },
  { value: "polardb", modes: ["mysql", "postgresql"] },
] as const;

interface PurposeFormState {
  included: boolean;
  mode: CredentialMode;
  username: string;
  password: string;
  /** Source group for reuse mode (the other credential group with a stored
   * credential; the old row's secret is reused under the new username). */
  reuseSource: CredentialGroup | "";
}

interface TLSFormState {
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
type TestCredentialKind = "readonly" | "readwrite";

interface DatasourceFormState {
  name: string;
  engine: string;
  compatibilityMode: string;
  deploymentKind: string;
  host: string;
  port: string;
  databaseName: string;
  versionConstraint: string;
  enabled: boolean;
  /** §18.61: connection-test purpose is persisted on the datasource — the table's "Test Connection" action selects
   * the stored credential by this attribute; changes are consolidated into this form (full-replacement semantics, written on submit). §18.62:
   * the form presents Read-Only / Read-Write states and resolves them to the contract enum on submit. */
  testKind: TestCredentialKind;
  purposes: Record<CredentialGroup, PurposeFormState>;
  tls: TLSFormState;
}

function hasStoredCredentialGroup(
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

function formForCreate(): DatasourceFormState {
  return {
    name: "",
    engine: "mysql",
    compatibilityMode: "mysql",
    deploymentKind: "native",
    host: "",
    port: "3306",
    databaseName: "",
    versionConstraint: "",
    enabled: true,
    testKind: "readonly",
    purposes: freshPurposes(),
    tls: { enabled: false, caPem: "", clientCertPem: "", clientKeyPem: "" },
  };
}

function formForEdit(ds: Datasource): DatasourceFormState {
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
    versionConstraint: "",
    enabled: ds.enabled,
    testKind: ds.test_purpose === "execution" ? "readwrite" : "readonly",
    purposes,
    // Materials never echo; the checkbox reflects the current state only.
    tls: { enabled: ds.tls_verified, caPem: "", clientCertPem: "", clientKeyPem: "" },
  };
}

function CredentialPurposeRow({
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
function TLSMaterialSection({
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

function DatasourceFormDialog({
  editing,
  onClose,
}: {
  editing: Datasource | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  // The dialog mounts per open (parent keys it by row), so the form state
  // initializes from the edited row without an effect-driven reset.
  const [form, setForm] = useState<DatasourceFormState>(() =>
    editing === null ? formForCreate() : formForEdit(editing),
  );
  const createMutation = useCreateDatasource();
  const replaceMutation = useReplaceDatasource();
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);
  const [versionConstraintTouched, setVersionConstraintTouched] = useState(false);

  const engineModes: string[] = [
    ...(ENGINE_OPTIONS.find((option) => option.value === form.engine)?.modes ?? ["mysql"]),
  ];
  const modeLocked = form.engine !== "polardb";

  const mutation = editing === null ? createMutation : replaceMutation;
  const submitting = mutation.isPending;

  // Client-side mirrors of the declared write rules: a reuse row needs its
  // source picked, a replace row needs username+password, keep carries
  // neither, an enabled TLS block needs material with cert/key paired, the
  // base fields are minLength 1, and at least one purpose must stay in the
  // full-replacement payload (minItems 1). The backend stays authoritative —
  // these only keep obviously-invalid payloads from leaving the form.
  const tlsPairingBroken =
    form.tls.enabled && (form.tls.clientCertPem === "") !== (form.tls.clientKeyPem === "");
  const tlsEmpty =
    form.tls.enabled &&
    form.tls.caPem === "" &&
    form.tls.clientCertPem === "" &&
    form.tls.clientKeyPem === "";
  const purposeInvalid = CREDENTIAL_GROUPS.some((purpose) => {
    const state = form.purposes[purpose];
    if (!state.included) return false;
    if (state.mode === "keep") return false;
    if (state.mode === "reuse") return state.reuseSource === "" || state.username === "";
    return state.username === "" || state.password === "";
  });
  // The view never echoes version_constraint, so an edit must re-type it;
  // the port is minimum 1 / maximum 65535 (clearing it decodes to 0).
  const port = Number(form.port);
  const portInvalid = !Number.isInteger(port) || port < 1 || port > 65535;
  const versionConstraintMissing = form.versionConstraint.trim() === "";
  const showVersionConstraintError =
    versionConstraintMissing &&
    (versionConstraintTouched || editing !== null || (form.name.trim() !== "" && form.host.trim() !== ""));
  const basicsInvalid =
    form.name === "" || form.host === "" || versionConstraintMissing || portInvalid;
  const anyPurposeIncluded = CREDENTIAL_GROUPS.some((purpose) => form.purposes[purpose].included);
  const formValid =
    !tlsPairingBroken && !tlsEmpty && !purposeInvalid && !basicsInvalid && anyPurposeIncluded;

  const missingItems: string[] = [];
  if (form.name === "") missingItems.push(t("admin.datasources.name"));
  if (form.host === "") missingItems.push(t("admin.datasources.host"));
  if (portInvalid) missingItems.push(t("admin.datasources.port"));
  if (versionConstraintMissing) missingItems.push(t("admin.datasources.versionConstraint"));
  if (!anyPurposeIncluded) {
    missingItems.push(t("admin.datasources.missingPurpose"));
  } else {
    for (const purpose of CREDENTIAL_GROUPS) {
      const state = form.purposes[purpose];
      if (!state.included || state.mode === "keep") continue;
      const invalid =
        state.mode === "reuse"
          ? state.reuseSource === "" || state.username === ""
          : state.username === "" || state.password === "";
      if (invalid) {
        missingItems.push(
          t("admin.datasources.missingPurposeCredential", {
            purpose: t(`admin.datasources.purpose.${purpose}`),
          }),
        );
      }
    }
  }
  if (tlsEmpty || tlsPairingBroken) {
    missingItems.push(t("admin.datasources.missingTls"));
  }

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    const credentials: CredentialWrite[] = [];
    const storedStatus = editing?.credential_status ?? null;
    const hasStoredReview = storedStatus?.review === true;
    const hasStoredQuery = storedStatus?.query === true;

    const readonlyState = form.purposes.readonly;
    if (readonlyState.included) {
      if (readonlyState.mode === "keep") {
        // Keep whichever read-only credential rows exist on the server without
        // echoing usernames or passwords.
        if (hasStoredReview) {
          credentials.push({ purpose: "review", reuse_credential_purpose: "review" });
        }
        if (hasStoredQuery) {
          credentials.push({ purpose: "query", reuse_credential_purpose: "query" });
        }
      } else if (readonlyState.mode === "reuse") {
        if (readonlyState.reuseSource === "execution") {
          credentials.push(
            {
              purpose: "review",
              username: readonlyState.username,
              reuse_credential_purpose: "execution",
            },
            {
              purpose: "query",
              username: readonlyState.username,
              reuse_credential_purpose: "execution",
            },
          );
        }
      } else {
        credentials.push(
          {
            purpose: "review",
            username: readonlyState.username,
            password: { value: readonlyState.password },
          },
          {
            purpose: "query",
            username: readonlyState.username,
            password: { value: readonlyState.password },
          },
        );
      }
    }

    const executionState = form.purposes.execution;
    if (executionState.included) {
      if (executionState.mode === "keep") {
        credentials.push({ purpose: "execution", reuse_credential_purpose: "execution" });
      } else if (executionState.mode === "reuse") {
        if (executionState.reuseSource === "readonly") {
          const sourcePurpose = hasStoredReview ? "review" : "query";
          credentials.push({
            purpose: "execution",
            username: executionState.username,
            reuse_credential_purpose: sourcePurpose,
          });
        }
      } else {
        credentials.push({
          purpose: "execution",
          username: executionState.username,
          password: { value: executionState.password },
        });
      }
    }

    const hasReviewCred = credentials.some((item) => item.purpose === "review");
    const hasQueryCred = credentials.some((item) => item.purpose === "query");
    const resolvedTestPurpose: DatasourceWrite["test_purpose"] =
      form.testKind === "readwrite"
        ? "execution"
        : hasReviewCred
          ? "review"
          : hasQueryCred
            ? "query"
            : "execution";

    const tls = form.tls.enabled
      ? {
          ca_pem: form.tls.caPem === "" ? null : { value: form.tls.caPem },
          client_cert_pem: form.tls.clientCertPem === "" ? null : { value: form.tls.clientCertPem },
          client_key_pem: form.tls.clientKeyPem === "" ? null : { value: form.tls.clientKeyPem },
        }
      : null;
    const write: DatasourceWrite = {
      name: form.name,
      engine: form.engine as DatasourceWrite["engine"],
      compatibility_mode: form.compatibilityMode as DatasourceWrite["compatibility_mode"],
      deployment_kind: form.deploymentKind as DatasourceWrite["deployment_kind"],
      host: form.host,
      port: Number(form.port),
      database_name: form.databaseName === "" ? null : form.databaseName,
      version_constraint: form.versionConstraint,
      enabled: form.enabled,
      test_purpose: resolvedTestPurpose,
      credentials,
      // Always explicit: null is the declared removal path, so an unchecked
      // box can never "accidentally" keep stale material.
      tls,
    };
    const onWriteError = (operationId: string) => (error: unknown) => {
      const display = describeError(error, operationId);
      setErrorKey(display.messageKey);
      setErrorRequestId(display.requestId);
      appToast.error(t(display.messageKey));
    };
    if (editing === null) {
      createMutation.mutate(write, {
        onSuccess: () => {
          appToast.success(t("admin.datasources.createTitle"), {
            description: write.name,
          });
          onClose();
        },
        onError: onWriteError("createDatasource"),
      });
    } else {
      replaceMutation.mutate(
        { id: editing.id, version: editing.version, write },
        {
          onSuccess: () => {
            appToast.success(t("admin.datasources.editTitle"), {
              description: write.name,
            });
            onClose();
          },
          onError: onWriteError("replaceDatasource"),
        },
      );
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {editing === null ? t("admin.datasources.createTitle") : t("admin.datasources.editTitle")}
          </DialogTitle>
          <DialogDescription>{t("admin.datasources.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-name">
                {t("admin.datasources.name")}
                <span className="text-destructive" aria-hidden>*</span>
              </Label>
              <Input
                id="ds-name"
                value={form.name}
                onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
                data-testid="ds-name"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label>{t("admin.datasources.engine")}</Label>
              <Select
                value={form.engine}
                onValueChange={(next) => {
                  if (next === null) return;
                  const modes: string[] = [
                    ...(ENGINE_OPTIONS.find((option) => option.value === next)?.modes ?? []),
                  ];
                  const nextMode = modes.includes(form.compatibilityMode)
                    ? form.compatibilityMode
                    : (modes[0] ?? "mysql");
                  setForm({ ...form, engine: next, compatibilityMode: nextMode });
                }}
              >
                <SelectTrigger data-testid="ds-engine">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ENGINE_OPTIONS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.value}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label>{t("admin.datasources.compatibilityMode")}</Label>
              <Select
                value={form.compatibilityMode}
                onValueChange={(next) => {
                  if (next === null || modeLocked) return;
                  setForm({ ...form, compatibilityMode: next });
                }}
              >
                <SelectTrigger data-testid="ds-compat-mode" aria-disabled={modeLocked}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {engineModes.map((mode) => (
                    <SelectItem key={mode} value={mode}>
                      {mode}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label>{t("admin.datasources.deploymentKind")}</Label>
              <Select
                value={form.deploymentKind}
                onValueChange={(next) => {
                  if (next === null) return;
                  setForm({ ...form, deploymentKind: next });
                }}
              >
                <SelectTrigger data-testid="ds-deployment-kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="native">native</SelectItem>
                  <SelectItem value="cloud">cloud</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-host">
                {t("admin.datasources.host")}
                <span className="text-destructive" aria-hidden>*</span>
              </Label>
              <Input
                id="ds-host"
                value={form.host}
                onChange={(event) => { setForm({ ...form, host: event.target.value }); }}
                data-testid="ds-host"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-port">
                {t("admin.datasources.port")}
                <span className="text-destructive" aria-hidden>*</span>
              </Label>
              <NumberInput
                id="ds-port"
                min={1}
                max={65535}
                value={form.port}
                onChange={(event) => { setForm({ ...form, port: event.target.value }); }}
                data-testid="ds-port"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-database-name">{t("admin.datasources.databaseName")}</Label>
              <Input
                id="ds-database-name"
                value={form.databaseName}
                onChange={(event) => { setForm({ ...form, databaseName: event.target.value }); }}
                data-testid="ds-database-name"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-version-constraint">
                {t("admin.datasources.versionConstraint")}
                <span className="text-destructive" aria-hidden>*</span>
              </Label>
              <Input
                id="ds-version-constraint"
                value={form.versionConstraint}
                onChange={(event) => { setForm({ ...form, versionConstraint: event.target.value }); }}
                onBlur={() => { setVersionConstraintTouched(true); }}
                placeholder={t("admin.datasources.versionConstraintPlaceholder")}
                aria-required="true"
                aria-invalid={showVersionConstraintError ? true : undefined}
                aria-describedby={showVersionConstraintError ? "ds-version-constraint-error" : "ds-version-constraint-hint"}
                data-testid="ds-version-constraint"
              />
              {showVersionConstraintError ? (
                <p
                  id="ds-version-constraint-error"
                  className="text-destructive text-xs"
                  data-testid="ds-version-constraint-error"
                >
                  {t("admin.datasources.versionConstraintRequired")}
                </p>
              ) : (
                <p
                  id="ds-version-constraint-hint"
                  className="text-muted-foreground text-xs"
                  data-testid="ds-version-constraint-hint"
                >
                  {t("admin.datasources.versionConstraintHint")}
                </p>
              )}
            </div>
          </div>
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="ds-enabled"
              checked={form.enabled}
              onChange={(event) => { setForm({ ...form, enabled: event.target.checked }); }}
              className="size-4"
              data-testid="ds-enabled"
            />
            <Label htmlFor="ds-enabled" className="cursor-pointer">
              {t("admin.datasources.enabled")}
            </Label>
          </div>
          {/* §18.61 owner ruling: the connection-test purpose is a persisted
              datasource attribute changed HERE — never a per-row select in
              the table. The list's Test Connection button probes with this
              purpose's stored credential. §18.62: the select speaks the
              same Read-Only / Read-Write credential-class vocabulary as the list badge. */}
          <div className="flex flex-col gap-1">
            <Label>{t("admin.datasources.testPurpose")}</Label>
            <Select
              value={form.testKind}
              onValueChange={(next) => {
                if (next !== null) setForm({ ...form, testKind: next });
              }}
              items={{
                readonly: t("admin.datasources.testCredentialReadOnly"),
                readwrite: t("admin.datasources.testCredentialReadWrite"),
              }}
            >
              <SelectTrigger data-testid="ds-test-purpose" className="w-64">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="readonly">{t("admin.datasources.testCredentialReadOnly")}</SelectItem>
                <SelectItem value="readwrite">{t("admin.datasources.testCredentialReadWrite")}</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-xs">{t("admin.datasources.testPurposeHint")}</p>
          </div>
          <div className="flex flex-col gap-2">
            <Label>{t("admin.datasources.credentialsTitle")}</Label>
            {/* §18.63: purpose row labels already include their Read-Only / Read-Write class (e.g. Review (Read-Only)),
                so the section hint no longer interpolates the label. */}
            <p className="text-muted-foreground text-xs">
              {t("admin.datasources.credentialsHint")}
            </p>
            {CREDENTIAL_GROUPS.map((purpose) => (
              <CredentialPurposeRow
                key={purpose}
                purpose={purpose}
                state={form.purposes[purpose]}
                storedStatus={editing !== null ? editing.credential_status : null}
                onChange={(next) => { setForm({ ...form, purposes: { ...form.purposes, [purpose]: next } }); }}
              />
            ))}
          </div>
          <TLSMaterialSection
            state={form.tls}
            wasVerified={editing !== null && editing.tls_verified}
            onChange={(next) => { setForm({ ...form, tls: next }); }}
          />
          {errorKey !== null ? (
            <Alert variant="destructive" data-testid="ds-form-error">
              <AlertTitle>{t(errorKey)}</AlertTitle>
              <AlertDescription>
                {errorRequestId !== null ? `request_id: ${errorRequestId}` : null}
              </AlertDescription>
            </Alert>
          ) : null}
          {!formValid ? (
            <p className="text-destructive text-xs font-medium" data-testid="ds-form-invalid-hint">
              {missingItems.length > 0
                ? t("admin.datasources.formMissingSummary", { items: missingItems.join(", ") })
                : t("admin.datasources.formInvalidHint")}
            </p>
          ) : null}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton pending={submitting} disabled={!formValid} onClick={submit} data-testid="ds-submit">
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function shortFingerprint(fingerprint: string): string {
  if (fingerprint.length <= 16) return fingerprint;
  return `${fingerprint.slice(0, 8)}\u2026${fingerprint.slice(-6)}`;
}

function CapabilityMatrixRow({
  datasourceId,
  probeKey,
  onClose,
}: {
  datasourceId: string;
  probeKey: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const capabilitiesQuery = useDatasourceCapabilities(datasourceId, probeKey, datasourceId !== "");
  if (capabilitiesQuery.isPending || capabilitiesQuery.isError) return null;
  const capabilities = capabilitiesQuery.data;
  if (capabilities === null) {
    return (
      <TableRow
        className="bg-muted/25 hover:bg-muted/25 h-auto"
        data-testid={`ds-capabilities-row-${datasourceId}`}
      >
        <TableCell colSpan={6} className="px-5 py-2 whitespace-normal">
          <div className="flex items-center justify-between gap-2">
            <p className="text-muted-foreground text-[11px] leading-4" data-testid="ds-capabilities-waiting">
              {t("admin.datasources.testRunning")}
            </p>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-muted-foreground hover:text-foreground size-5 shrink-0"
              onClick={onClose}
              aria-label={t("common.close")}
              data-testid="ds-capabilities-close"
            >
              <X className="size-3" aria-hidden />
            </Button>
          </div>
        </TableCell>
      </TableRow>
    );
  }
  const capabilityLabel = (name: string): string =>
    t(`admin.datasources.capabilityLabels.${name}`, { defaultValue: name });
  const unavailable = Object.entries(capabilities.capabilities).filter(([, ok]) => !ok);
  return (
    <TableRow
      className="bg-muted/25 hover:bg-muted/25 h-auto"
      data-testid={`ds-capabilities-row-${datasourceId}`}
    >
      <TableCell colSpan={6} className="px-5 py-2 whitespace-normal">
        <div className="flex flex-col gap-1.5" data-testid="ds-capabilities">
          <div className="flex items-center justify-between gap-3">
            <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] leading-4">
              <span className="font-medium">
                {t("admin.datasources.capabilitiesTitle")}
              </span>
              <span aria-hidden>·</span>
              <span className="font-mono">
                {t("admin.datasources.detectedVersion", { version: capabilities.detected_version })}
              </span>
              <span aria-hidden>·</span>
              <span
                className="font-mono"
                title={capabilities.identity_fingerprint}
              >
                {t("admin.datasources.identityFingerprint")}:{" "}
                {shortFingerprint(capabilities.identity_fingerprint)}
              </span>
              <span className="text-border hidden sm:inline" aria-hidden>|</span>
              <div className="flex flex-wrap items-center gap-1">
                {Object.entries(capabilities.capabilities).map(([name, ok]) =>
                  ok ? (
                    <StatusBadge
                      key={name}
                      tone="emerald"
                      className="h-[18px] px-1.5 py-0 text-[11px] leading-none"
                      data-testid={`ds-capability-${name}`}
                    >
                      <Check className="size-2.5" aria-hidden />
                      {capabilityLabel(name)} · {t("admin.datasources.capabilityOk")}
                    </StatusBadge>
                  ) : (
                    <StatusBadge
                      key={name}
                      tone="stone"
                      className="h-[18px] border-dashed px-1.5 py-0 text-[11px] leading-none"
                      data-testid={`ds-capability-${name}`}
                    >
                      <Minus className="size-2.5" aria-hidden />
                      {capabilityLabel(name)} · {t("admin.datasources.capabilityUnavailable")}
                    </StatusBadge>
                  ),
                )}
              </div>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-muted-foreground hover:text-foreground size-5 shrink-0"
              onClick={onClose}
              aria-label={t("common.close")}
              data-testid="ds-capabilities-close"
            >
              <X className="size-3" aria-hidden />
            </Button>
          </div>
          {unavailable.length > 0 ? (
            <div className="border-tone-amber/30 bg-tone-amber/12 text-tone-amber flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5 rounded px-2.5 py-1 text-[11px] leading-4 border">
              <span className="font-medium">{t("admin.datasources.unavailableTitle")}</span>
              <span className="text-foreground/80">
                {t("admin.datasources.unavailableExplanation", {
                  capabilities: unavailable.map(([name]) => capabilityLabel(name)).join(", "),
                })}
              </span>
            </div>
          ) : null}
        </div>
      </TableCell>
    </TableRow>
  );
}

function DatasourceRow({
  datasource,
  onEdit,
  onDelete,
  onConfigureDiagnostics,
  onTestStarted,
}: {
  datasource: Datasource;
  onEdit: () => void;
  onDelete: () => void;
  onConfigureDiagnostics: () => void;
  onTestStarted: (taskId: string) => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  // Test state hoisted from the old inline-button strip: the actions live in
  // the ⋯ menu now (spec §203 consolidation), while the async feedback stays visible
  // beside the kebab.
  const [taskId, setTaskId] = useState<string | null>(null);
  const testMutation = useTestDatasourceConnection();
  const taskQuery = useAdminTask(taskId ?? "", taskId !== null);
  const [testErrorKey, setTestErrorKey] = useState<string | null>(null);
  // §18.76 supplement (owner ruling following the Provider refusal passthrough path): synchronous refusal reason uses meta.detail;
  // async Task failure uses task.error.detail (surfacing the real Probe failure reason instead of generic copy).
  const [testErrorDetail, setTestErrorDetail] = useState<string | null>(null);

  const referenced = (datasource.referenced_by_flow_count ?? 0) > 0;
  const testBusy =
    testMutation.isPending ||
    taskQuery.data?.state === "running" ||
    taskQuery.data?.state === "queued";

  const runTest = () => {
    setTestErrorKey(null);
    setTestErrorDetail(null);
    // §18.61: probe purpose reads the persisted test_purpose from the datasource (changed via edit form);
    // the table no longer renders a purpose selector.
    testMutation.mutate(
      { id: datasource.id, purpose: datasource.test_purpose },
      {
        onSuccess: (task) => {
          setTaskId(task.id);
          onTestStarted(task.id);
          appToast.info(t("admin.datasources.testConnection"), {
            description: datasource.name,
          });
        },
        onError: (error) => {
          const display = describeError(error, "testDatasourceConnection");
          setTestErrorKey(display.messageKey);
          setTestErrorDetail(display.detail);
          appToast.error(t(display.messageKey));
        },
      },
    );
  };

  return (
    <TableRow data-testid={`ds-row-${datasource.id}`} data-row-key={datasource.id}>
      <TableCell className="font-semibold text-foreground">
        {datasource.name}
        {datasource.enabled ? null : (
          <StatusBadge tone="stone" className="ml-2">
            {t("admin.datasources.disabled")}
          </StatusBadge>
        )}
      </TableCell>
      <TableCell className="font-mono">
        {datasource.engine}/{datasource.compatibility_mode}
      </TableCell>
      <TableCell className="font-mono">
        {/* Owner feedback 2026-11-04 (align admin table row height with approval orders table): host and
            TLS badge sit on ONE line — the stacked layout pushed rows to
            ~81px against the 64px approval-queue reference. */}
        <span className="flex flex-wrap items-center gap-2">
          <span className="truncate">
            {datasource.host}:{datasource.port}
          </span>
          <StatusBadge
            tone={datasource.tls_verified ? "emerald" : "amber"}
            data-testid={`ds-tls-${datasource.tls_verified ? "verified" : "plaintext"}-${datasource.id}`}
          >
            {datasource.tls_verified
              ? t("admin.datasources.tlsVerifiedBadge")
              : t("admin.datasources.tlsPlaintextBadge")}
          </StatusBadge>
        </span>
      </TableCell>
      <TableCell>
        {/* §18.58 owner ruling: the credential state is a single Read-Only / Read-Write
            badge (read-write = execution credential stored); the per-purpose
            detail and every credential change live in the edit dialog (B13
            keep/replace/reuse), never in the table. */}
        <Badge
          variant={datasource.credential_status.execution === true ? "secondary" : "outline"}
          data-testid={`ds-cred-mode-${datasource.credential_status.execution === true ? "readwrite" : "readonly"}`}
        >
          {datasource.credential_status.execution === true
            ? t("admin.datasources.credModeReadWrite")
            : t("admin.datasources.credModeReadOnly")}
        </Badge>
      </TableCell>
      <TableCell className="text-muted-foreground tabular-nums">
        {t("admin.datasources.referencedBy", { count: datasource.referenced_by_flow_count ?? 0 })}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex flex-col items-end gap-1">
          <TableRowActions testId={`ds-actions-${datasource.id}`}>
            <DropdownMenuItem
              onClick={() => {
                void navigate(`/admin/diagnostics?datasource_id=${encodeURIComponent(datasource.id)}`);
              }}
              data-testid={`ds-open-diagnostics-${datasource.id}`}
            >
              <Activity /> {t("diagnostics.openFromDatasource")}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={onConfigureDiagnostics}
              data-testid={`ds-diagnostics-config-${datasource.id}`}
            >
              <Settings2 /> {t("diagnostics.configButton")}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={runTest}
              disabled={testBusy}
              data-testid={`ds-test-button-${datasource.id}`}
            >
              <PlugZap /> {t("admin.datasources.testConnection")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={onEdit} data-testid={`ds-edit-${datasource.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            {/* §18.58: a referenced datasource cannot be deleted (1107, can only be disabled)
                — the disabled menu item carries the reason as a trailing
                muted line (tooltips do not travel into menu portals). */}
            {referenced ? (
              <>
                <DropdownMenuItem disabled data-testid={`ds-delete-${datasource.id}`}>
                  <Trash2 /> {t("common.delete")}
                </DropdownMenuItem>
                <DropdownMenuGroup><DropdownMenuLabel>{t("admin.datasources.deleteBlockedHint")}</DropdownMenuLabel></DropdownMenuGroup>
              </>
            ) : (
              <DropdownMenuItem
                variant="destructive"
                onClick={onDelete}
                data-testid={`ds-delete-${datasource.id}`}
              >
                <Trash2 /> {t("common.delete")}
              </DropdownMenuItem>
            )}
          </TableRowActions>
          {taskQuery.data?.state === "queued" || taskQuery.data?.state === "running" ? (
            <p className="text-muted-foreground text-xs">{t("admin.datasources.testRunning")}</p>
          ) : null}
          {/* Owner live verification: test success previously gave zero feedback (old code only rendered running/failed states) —
              add the success line, symmetrical with the new refusal reason rendering. */}
          {taskQuery.data?.state === "succeeded" ? (
            <p
              className="text-xs text-[var(--risk-safe)]"
              data-testid={`ds-test-ok-${datasource.id}`}
            >
              {t("admin.datasources.testOk")}
            </p>
          ) : null}
          {taskQuery.data?.state === "failed" ? (
            <div className="flex flex-col items-end text-right">
              <p className="text-xs text-destructive">{t("admin.datasources.testFailed")}</p>
              {taskQuery.data.error?.detail ? (
                <p
                  className="break-all font-mono text-xs text-destructive"
                  data-testid={`ds-test-error-detail-${datasource.id}`}
                >
                  {taskQuery.data.error.detail}
                </p>
              ) : null}
            </div>
          ) : null}
          {testErrorKey !== null ? (
            <div className="flex flex-col items-end text-right">
              <p className="text-xs text-destructive">{t(testErrorKey)}</p>
              {testErrorDetail !== null ? (
                <p
                  className="break-all font-mono text-xs text-destructive"
                  data-testid={`ds-test-error-detail-${datasource.id}`}
                >
                  {testErrorDetail}
                </p>
              ) : null}
            </div>
          ) : null}
        </div>
      </TableCell>
    </TableRow>
  );
}

function DeleteConfirmDialog({
  datasource,
  onClose,
}: {
  datasource: Datasource;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const deleteMutation = useDeleteDatasource();
  const [errorKey, setErrorKey] = useState<string | null>(null);
  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("admin.datasources.deleteTitle")}</DialogTitle>
          <DialogDescription>{t("admin.datasources.deleteDescription", { name: datasource.name })}</DialogDescription>
        </DialogHeader>
        {errorKey !== null ? (
          <Alert variant="destructive" data-testid="ds-delete-error">
            <AlertTitle>{t(errorKey)}</AlertTitle>
            <AlertDescription>{t("admin.datasources.deleteErrorHint")}</AlertDescription>
          </Alert>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={deleteMutation.isPending}
            onClick={() => { deleteMutation.mutate(
                { id: datasource.id, version: datasource.version },
                {
                  onSuccess: () => {
                    appToast.success(t("common.delete"), {
                      description: datasource.name,
                    });
                    onClose();
                  },
                  onError: (error) => {
                    const display = describeError(error, "deleteDatasource");
                    setErrorKey(display.messageKey);
                    appToast.error(t(display.messageKey));
                  },
                },
              ); }
            }
            data-testid="ds-delete-confirm"
          >
            {t("common.delete")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Closed set of engines: search condition options and the server-side engine filter param share the same mapping
 * (RCP-20261105-ADMIN-LIST-FIELD-FILTERS). */
const ENGINE_VALUES = {
  mysql: "mysql", postgresql: "postgresql", tidb: "tidb", oceanbase: "oceanbase", polardb: "polardb",
} as const;

export default function AdminDatasourcesPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const pager = useCursorPage(20);
  const [terms, setTerms] = useState<SearchTerm[]>([]);
  const [live, setLive] = useState<SearchTerm | null>(null);
  const effectiveTerms = useMemo(
    () => (live === null ? terms : [...terms, live]),
    [live, terms],
  );
  // §18.73 + RCP-20261105: free text / name / endpoint go through server-side q (q covers name/host);
  // engine and credential mode are valued server-side column filters (engine / credential_mode),
  // performing cross-page narrowing on the server. Composite display strings like flow reference counts are no longer searchable fields.
  const serverQ = useMemo(
    () =>
      [...effectiveTerms]
        .reverse()
        .find((term) => term.field === null || term.field === "name" || term.field === "endpoint")
        ?.text.trim() ?? "",
    [effectiveTerms],
  );
  const engineParam = useMemo<(typeof ENGINE_VALUES)[keyof typeof ENGINE_VALUES] | undefined>(() => {
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "engine" && candidate.text.trim() !== "");
    return term === undefined ? undefined : ENGINE_VALUES[term.text as keyof typeof ENGINE_VALUES];
  }, [effectiveTerms]);
  const credentialModeParam = useMemo<"read_write" | "read_only" | undefined>(() => {
    const MODES = { [t("admin.datasources.credModeReadWrite")]: "read_write", [t("admin.datasources.credModeReadOnly")]: "read_only" } as const;
    const term = [...effectiveTerms].reverse().find((candidate) => candidate.field === "credentialStatus" && candidate.text.trim() !== "");
    return term === undefined ? undefined : MODES[term.text as keyof typeof MODES];
  }, [effectiveTerms, t]);

  const query = useDatasources(enabled, {
    limit: pager.pageSize,
    after: pager.after,
    q: serverQ || undefined,
    engine: engineParam,
    credentialMode: credentialModeParam,
  });
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Datasource | null>(null);
  const [deleting, setDeleting] = useState<Datasource | null>(null);
  const [configuringDiagnostics, setConfiguringDiagnostics] = useState<Datasource | null>(null);
  // Expanded capability row: datasource id + the probe task id that keyed
  // the latest capabilities fetch (a re-test starts a fresh key).
  const [probe, setProbe] = useState<{ datasourceId: string; taskId: string } | null>(null);
  const searchAccessors = useMemo<SearchAccessor<Datasource>[]>(
    () => [
      {
        key: "name",
        label: t("admin.datasources.name"),
        value: (ds) => `${ds.name} ${ds.enabled ? "" : t("admin.datasources.disabled")}`,
      },
      {
        // Engines form a closed set: options map directly to the server-side engine filter parameter (RCP-20261105).
        key: "engine",
        label: t("admin.datasources.engine"),
        value: (ds) => ds.engine,
        options: Object.keys(ENGINE_VALUES).map((engine) => ({ label: engine })),
      },
      {
        key: "endpoint",
        label: t("admin.datasources.endpoint"),
        value: (ds) =>
          `${ds.host}:${String(ds.port)} ${
            ds.tls_verified
              ? t("admin.datasources.tlsVerifiedBadge")
              : t("admin.datasources.tlsPlaintextBadge")
          }`,
      },
      {
        key: "credentialStatus",
        label: t("admin.datasources.credentialStatus"),
        value: (ds) =>
          ds.credential_status.execution === true
            ? t("admin.datasources.credModeReadWrite")
            : t("admin.datasources.credModeReadOnly"),
        options: [
          { label: t("admin.datasources.credModeReadWrite") },
          { label: t("admin.datasources.credModeReadOnly") },
        ],
      },
    ],
    [t],
  );
  const hasSearchFilter = effectiveTerms.some((term) => term.text.trim() !== "");
  const datasources = filterBySearchTerms(
    query.data?.items ?? [],
    effectiveTerms,
    searchAccessors,
  );
  // Tween whole-row position changes when search/pagination alters the row set; capability probe rows carry no data-row-key and do not participate.
  const datasourcesBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(datasources.map((datasource) => datasource.id)),
  );

  return (
    <PageLayout>
      <PageHeader
        title={t("admin.datasources.title")}
        description={t("admin.datasources.description")}
        icon={Database}
      />
      <Card className="overflow-visible">
        <ListToolbar
          busy={query.isFetching && !query.isPending}
          fill
          actions={
            <Button
              onClick={() => {
                setEditing(null);
                setDialogOpen(true);
              }}
              data-testid="ds-create"
            >
              <Plus /> {t("admin.datasources.createTitle")}
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
            placeholder={t("admin.datasources.searchPlaceholder")}
            testId="admin-datasources-search"
            className="w-full min-w-0 flex-1"
          />
        </ListToolbar>
        <CardContent className="px-0!">
          {query.isPending ? (
            <LoadingState variant="table" columns={6} />
          ) : query.isError ? (
            <ErrorState error={query.error} operationId="listDatasources" onRetry={() => void query.refetch()} />
          ) : datasources.length === 0 && hasSearchFilter ? (
            <EmptyState
              title={t("table.emptySearch")}
              filtered
              testId="admin-datasources-search-empty"
            />
          ) : datasources.length === 0 ? (
            <EmptyState
              title={t("admin.datasources.emptyTitle")}
              description={t("admin.datasources.emptyDescription")}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("admin.datasources.name")}</TableHead>
                  <TableHead>{t("admin.datasources.engine")}</TableHead>
                  <TableHead>{t("admin.datasources.endpoint")}</TableHead>
                  <TableHead>{t("admin.datasources.credentialStatus")}</TableHead>
                  <TableHead>{t("admin.datasources.references")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody ref={datasourcesBodyRef} stale={query.isFetching && query.isPlaceholderData}>
                {datasources.map((datasource) => (
                  <Fragment key={datasource.id}>
                    <DatasourceRow
                      datasource={datasource}
                      onEdit={() => {
                        setEditing(datasource);
                        setDialogOpen(true);
                      }}
                      onDelete={() => { setDeleting(datasource); }}
                      onConfigureDiagnostics={() => { setConfiguringDiagnostics(datasource); }}
                      onTestStarted={(taskId) => { setProbe({ datasourceId: datasource.id, taskId }); }}
                    />
                    {probe !== null && probe.datasourceId === datasource.id ? (
                      <CapabilityMatrixRow
                        datasourceId={datasource.id}
                        probeKey={probe.taskId}
                        onClose={() => { setProbe(null); }}
                      />
                    ) : null}
                  </Fragment>
                ))}
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
              testIdPrefix="datasources"
            />
          ) : null}
        </CardContent>
      </Card>
      {dialogOpen ? (
        <DatasourceFormDialog
          key={editing?.id ?? "new"}
          editing={editing}
          onClose={() => {
            setDialogOpen(false);
            setEditing(null);
          }}
        />
      ) : null}
      {deleting !== null ? (
        <DeleteConfirmDialog
          key={deleting.id}
          datasource={deleting}
          onClose={() => { setDeleting(null); }}
        />
      ) : null}
      {configuringDiagnostics !== null ? (
        <DatasourceDiagnosticsConfigDialog
          key={configuringDiagnostics.id}
          datasource={configuringDiagnostics}
          onClose={() => { setConfiguringDiagnostics(null); }}
        />
      ) : null}
    </PageLayout>
  );
}

const DIAGNOSTICS_INTERVAL_OPTIONS: UpdateDatasourceDiagnosticsConfigRequestIntervalSeconds[] = [
  0, 60, 90, 120, 300,
];

function DatasourceDiagnosticsConfigDialog({
  datasource,
  onClose,
}: {
  datasource: Datasource;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const configQuery = useDatasourceDiagnosticsConfig(datasource.id, true);
  const updateConfig = useUpdateDatasourceDiagnosticsConfig();
  const [initializedForVersion, setInitializedForVersion] = useState<number | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [intervalSeconds, setIntervalSeconds] =
    useState<UpdateDatasourceDiagnosticsConfigRequestIntervalSeconds>(60);
  const [topN, setTopN] = useState("200");
  const [resumeFromPause, setResumeFromPause] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);

  if (configQuery.data !== undefined && initializedForVersion !== configQuery.data.version) {
    setInitializedForVersion(configQuery.data.version);
    setEnabled(configQuery.data.enabled);
    setIntervalSeconds(configQuery.data.interval_seconds);
    setTopN(String(configQuery.data.top_n));
    setResumeFromPause(configQuery.data.state === "paused_on_failure");
  }

  const parsedTopN = Number(topN);
  const topNValid = Number.isInteger(parsedTopN) && parsedTopN >= 50 && parsedTopN <= 500;

  const submit = () => {
    if (configQuery.data === undefined || !topNValid) return;
    setErrorKey(null);
    updateConfig.mutate(
      {
        datasourceId: datasource.id,
        version: configQuery.data.version,
        body: {
          enabled,
          interval_seconds: intervalSeconds,
          top_n: parsedTopN,
          resume_from_pause: resumeFromPause || undefined,
        },
      },
      {
        onSuccess: () => {
          appToast.success(t("common.save"), {
            description: datasource.name,
          });
          onClose();
        },
        onError: (error) => {
          const display = describeError(error, "updateDatasourceDiagnosticsConfig");
          setErrorKey(display.messageKey);
          appToast.error(t(display.messageKey));
        },
      },
    );
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent data-testid="ds-diagnostics-config-dialog">
        <DialogHeader>
          <DialogTitle>
            {t("diagnostics.configTitle", { name: datasource.name })}
          </DialogTitle>
          <DialogDescription>{t("diagnostics.configDescription")}</DialogDescription>
        </DialogHeader>
        {configQuery.isPending ? (
          <LoadingState variant="inline" />
        ) : configQuery.isError ? (
          <ErrorState
            error={configQuery.error}
            operationId="getDatasourceDiagnosticsConfig"
            onRetry={() => void configQuery.refetch()}
          />
        ) : (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <Badge variant="outline" data-testid="ds-diag-circuit-state">
                {t(`diagnostics.circuitState.${configQuery.data.state}`)}
              </Badge>
              <Badge variant="secondary" data-testid="ds-diag-probe-state">
                {t(`diagnostics.probeState.${configQuery.data.last_probe_state}`)}
              </Badge>
            </div>
            {configQuery.data.paused_reason ? (
              <Alert variant="destructive" data-testid="ds-diag-paused-reason">
                <AlertDescription>{configQuery.data.paused_reason}</AlertDescription>
              </Alert>
            ) : null}
            <div className="flex items-center gap-2">
              <input
                type="checkbox"
                id="ds-diag-enabled"
                checked={enabled}
                onChange={(event) => { setEnabled(event.target.checked); }}
                className="size-4"
                data-testid="ds-diag-enabled"
              />
              <Label htmlFor="ds-diag-enabled" className="cursor-pointer">
                {t("diagnostics.configEnabled")}
              </Label>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-diag-interval">{t("diagnostics.configInterval")}</Label>
              <Select
                value={String(intervalSeconds)}
                onValueChange={(next) => {
                  const num = Number(next) as UpdateDatasourceDiagnosticsConfigRequestIntervalSeconds;
                  if (DIAGNOSTICS_INTERVAL_OPTIONS.includes(num)) {
                    setIntervalSeconds(num);
                  }
                }}
              >
                <SelectTrigger id="ds-diag-interval" data-testid="ds-diag-interval">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {DIAGNOSTICS_INTERVAL_OPTIONS.map((sec) => (
                    <SelectItem key={sec} value={String(sec)}>
                      {sec === 0
                        ? t("diagnostics.intervalManualOnly")
                        : t("diagnostics.intervalSeconds", { seconds: sec })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="ds-diag-topn">{t("diagnostics.configTopN")}</Label>
              <NumberInput
                id="ds-diag-topn"
                min={50}
                max={500}
                value={topN}
                onChange={(event) => { setTopN(event.target.value); }}
                data-testid="ds-diag-topn"
              />
            </div>
            {configQuery.data.state === "paused_on_failure" ||
            configQuery.data.state === "degraded" ? (
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="ds-diag-resume"
                  checked={resumeFromPause}
                  onChange={(event) => { setResumeFromPause(event.target.checked); }}
                  className="size-4"
                  data-testid="ds-diag-resume"
                />
                <Label htmlFor="ds-diag-resume" className="cursor-pointer">
                  {t("diagnostics.configResumeFromPause")}
                </Label>
              </div>
            ) : null}
            {errorKey !== null ? (
              <Alert variant="destructive" data-testid="ds-diag-config-error">
                <AlertDescription>{t(errorKey)}</AlertDescription>
              </Alert>
            ) : null}
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={updateConfig.isPending}
            disabled={configQuery.data === undefined || !topNValid}
            onClick={submit}
            data-testid="ds-diag-config-save"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
