import { useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  CredentialWrite,
  Datasource,
  DatasourceWrite,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateDatasource,
  useReplaceDatasource,
} from "@/features/admin/use-admin";
import {
  CREDENTIAL_GROUPS,
  CredentialPurposeRow,
  type DatasourceFormState,
  ENGINE_OPTIONS,
  formForCreate,
  formForEdit,
  TLSMaterialSection,
} from "@/routes/admin/admin-datasource-credentials-tls";
import { describeError } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

export function DatasourceFormDialog({
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
  const [errorDetail, setErrorDetail] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);

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
  // The port is minimum 1 / maximum 65535 (clearing it decodes to 0).
  const port = Number(form.port);
  const portInvalid = !Number.isInteger(port) || port < 1 || port > 65535;
  const basicsInvalid =
    form.name === "" || form.host === "" || portInvalid;
  const anyPurposeIncluded = CREDENTIAL_GROUPS.some((purpose) => form.purposes[purpose].included);
  const formValid =
    !tlsPairingBroken && !tlsEmpty && !purposeInvalid && !basicsInvalid && anyPurposeIncluded;

  const missingItems: string[] = [];
  if (form.name === "") missingItems.push(t("admin.datasources.name"));
  if (form.host === "") missingItems.push(t("admin.datasources.host"));
  if (portInvalid) missingItems.push(t("admin.datasources.port"));
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
    setErrorDetail(null);
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
      setErrorDetail(display.detail);
      setErrorRequestId(display.requestId);
      appToast.error(
        t(display.messageKey),
        display.detail !== null ? { description: display.detail } : undefined,
      );
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
      <DialogContent
        className="sm:max-w-2xl"
        viewportClassName={errorKey !== null ? "max-h-[calc(500px-16.5rem)]" : undefined}
      >
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
              <p className="text-muted-foreground text-xs">
                {t("admin.datasources.databaseNameHint")}
              </p>
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
          {!formValid ? (
            <p className="text-destructive text-xs font-medium" data-testid="ds-form-invalid-hint">
              {missingItems.length > 0
                ? t("admin.datasources.formMissingSummary", { items: missingItems.join(", ") })
                : t("admin.datasources.formInvalidHint")}
            </p>
          ) : null}
        </div>
        <DialogFooter className="flex-col items-stretch gap-3 sm:flex-col">
          {errorKey !== null ? (
            <Alert variant="destructive" data-testid="ds-form-error">
              <AlertTitle>{t(errorKey)}</AlertTitle>
              {errorDetail !== null || errorRequestId !== null ? (
                <AlertDescription className="space-y-1">
                  {errorDetail !== null ? (
                    <p data-testid="ds-form-error-detail">{errorDetail}</p>
                  ) : null}
                  {errorRequestId !== null ? <p>{`request_id: ${errorRequestId}`}</p> : null}
                </AlertDescription>
              ) : null}
            </Alert>
          ) : null}
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <PendingButton pending={submitting} disabled={!formValid} onClick={submit} data-testid="ds-submit">
              {t("common.save")}
            </PendingButton>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
