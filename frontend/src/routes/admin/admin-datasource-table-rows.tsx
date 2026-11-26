import { Fragment, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { Activity, Check, Minus, Pencil, PlugZap, Settings2, Trash2, X } from "lucide-react";
import type { Datasource } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useAdminTask,
  useDatasourceCapabilities,
  useTestDatasourceConnection,
} from "@/features/admin/use-admin";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { TableRowErrorRow } from "@/shared/components/table-row-error-row";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
} from "@/shared/components/ui/dropdown-menu";
import { TableCell, TableRow } from "@/shared/components/ui/table";

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

export function DatasourceRow({
  datasource,
  probeKey,
  onCloseProbe,
  onEdit,
  onDelete,
  onConfigureDiagnostics,
  onTestStarted,
}: {
  datasource: Datasource;
  probeKey: string | null;
  onCloseProbe: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onConfigureDiagnostics: () => void;
  onTestStarted: (taskId: string) => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  // Test state hoisted from the old inline-button strip: the actions live in
  // the ⋯ menu now (spec §203 consolidation), while the async feedback stays visible
  // beside the kebab and failures expand a full-width diagnostic sub-row.
  const [taskId, setTaskId] = useState<string | null>(null);
  const testMutation = useTestDatasourceConnection();
  const taskQuery = useAdminTask(taskId ?? "", taskId !== null);
  const [testErrorKey, setTestErrorKey] = useState<string | null>(null);
  // §18.76 supplement (owner ruling following the Provider refusal passthrough path): synchronous refusal reason uses meta.detail;
  // async Task failure uses task.error.detail (surfacing the real Probe failure reason instead of generic copy).
  const [testErrorDetail, setTestErrorDetail] = useState<string | null>(null);

  const referenced = (datasource.referenced_by_flow_count ?? 0) > 0;
  const taskPollError = taskQuery.isError ? describeError(taskQuery.error, "getTask") : null;
  const testBusy =
    testMutation.isPending ||
    (!taskQuery.isError &&
      (taskQuery.data?.state === "running" || taskQuery.data?.state === "queued"));

  const rowError =
    testErrorKey !== null
      ? {
          summary: t(testErrorKey),
          detail: testErrorDetail,
          testId: `ds-test-error-${datasource.id}`,
          detailTestId: `ds-test-error-detail-${datasource.id}`,
        }
      : taskPollError !== null
        ? {
            summary: t(taskPollError.messageKey),
            detail: taskPollError.detail,
            testId: `ds-test-poll-error-${datasource.id}`,
            detailTestId: `ds-test-error-detail-${datasource.id}`,
          }
        : !taskQuery.isError && taskQuery.data?.state === "failed"
          ? {
              summary: t("admin.datasources.testFailed"),
              detail: taskQuery.data.error?.detail ?? null,
              testId: `ds-test-error-${datasource.id}`,
              detailTestId: `ds-test-error-detail-${datasource.id}`,
            }
          : null;

  const clearTestError = () => {
    setTestErrorKey(null);
    setTestErrorDetail(null);
    setTaskId(null);
    onCloseProbe();
  };

  const runTest = () => {
    setTaskId(null);
    setTestErrorKey(null);
    setTestErrorDetail(null);
    onCloseProbe();
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
    <Fragment>
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
        <div className="flex items-center justify-end gap-2">
          {!taskQuery.isError && (taskQuery.data?.state === "queued" || taskQuery.data?.state === "running") ? (
            <p className="text-muted-foreground text-xs" data-testid={`ds-test-running-${datasource.id}`}>
              {t("admin.datasources.testRunning")}
            </p>
          ) : null}
          {/* Owner live verification: test success previously gave zero feedback (old code only rendered running/failed states) —
              add the success line, symmetrical with the expanded capability matrix and diagnostic error rows. */}
          {!taskQuery.isError && taskQuery.data?.state === "succeeded" ? (
            <p
              className="text-xs text-[var(--risk-safe)]"
              data-testid={`ds-test-ok-${datasource.id}`}
            >
              {t("admin.datasources.testOk")}
            </p>
          ) : null}
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
        </div>
      </TableCell>
    </TableRow>
    {rowError !== null ? (
      <TableRowErrorRow
        colSpan={6}
        summary={rowError.summary}
        detail={rowError.detail}
        testId={rowError.testId}
        detailTestId={rowError.detailTestId}
        copyTestId={`ds-test-error-copy-${datasource.id}`}
        closeTestId={`ds-test-error-close-${datasource.id}`}
        onClose={clearTestError}
      />
    ) : null}
    {rowError === null && probeKey !== null ? (
      <CapabilityMatrixRow
        datasourceId={datasource.id}
        probeKey={probeKey}
        onClose={onCloseProbe}
      />
    ) : null}
    </Fragment>
  );
}
