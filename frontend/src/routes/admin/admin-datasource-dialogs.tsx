import { useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  Datasource,
  UpdateDatasourceDiagnosticsConfigRequestIntervalSeconds,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useDatasourceDiagnosticsConfig,
  useDeleteDatasource,
  useUpdateDatasourceDiagnosticsConfig,
} from "@/features/admin/use-admin";
import { describeError } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { LoadingState } from "@/shared/components/status/loading-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Label } from "@/shared/components/ui/label";
import { NumberInput } from "@/shared/components/ui/number-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

export function DeleteConfirmDialog({
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

const DIAGNOSTICS_INTERVAL_OPTIONS: UpdateDatasourceDiagnosticsConfigRequestIntervalSeconds[] = [
  0, 60, 90, 120, 300,
];

type DiagnosticsProbeStatusKey =
  | "ready"
  | "stats_disabled"
  | "insufficient_privilege"
  | "unsupported_engine"
  | "no_samples"
  | "collection_failed";

function knownDiagnosticsProbeState(raw: string | null | undefined): DiagnosticsProbeStatusKey {
  switch (raw) {
    case "ready":
    case "stats_disabled":
    case "insufficient_privilege":
    case "unsupported_engine":
    case "no_samples":
    case "collection_failed":
      return raw;
    default:
      return "collection_failed";
  }
}

export function DatasourceDiagnosticsConfigDialog({
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
  const hasCompletedProbe = configQuery.data?.last_probe_at != null;
  const probeState = configQuery.data
    ? knownDiagnosticsProbeState(configQuery.data.last_probe_state)
    : null;

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
              {hasCompletedProbe && probeState !== null ? (
                <Badge variant="secondary" data-testid="ds-diag-probe-state">
                  {t(`diagnostics.probeState.${probeState}`)}
                </Badge>
              ) : null}
            </div>
            {hasCompletedProbe && probeState !== null && probeState !== "ready" ? (
              <Alert data-testid="ds-diag-probe-hint">
                <AlertDescription>
                  {t(`diagnostics.probeHint.${probeState}`)}
                </AlertDescription>
              </Alert>
            ) : null}
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
