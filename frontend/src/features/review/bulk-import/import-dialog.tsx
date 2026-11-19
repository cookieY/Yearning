import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { SqlDigest } from "@/features/review/bulk-import/sql-digest";
import { useBulkImport } from "@/features/review/bulk-import/use-bulk-import";
import { PendingButton } from "@/shared/components/pending-button";
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
import { Progress } from "@/shared/components/ui/progress";
import { AlertTriangle, Ban, CircleCheck, FileUp, TriangleAlert } from "lucide-react";

/**
 * Bulk SQL import dialog (frontend PRD F5 items 6/7): incremental file read
 * with progress and cancel, the local digest summary, and hard pre-validation
 * of the 32 MiB file / 512 KiB statement limits. Confirming hands the single
 * in-memory SQL copy to the page, which uploads it through the generated
 * client — the server remains the final judge.
 */

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${String(bytes)} B`;
}

export interface ImportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: (text: string, digest: SqlDigest) => void;
  uploading?: boolean;
  /** File dropped on the page dropzone: read as soon as the dialog opens. */
  initialFile?: File | null;
}

export function ImportDialog({ open, onOpenChange, onConfirm, uploading = false, initialFile = null }: ImportDialogProps) {
  const { t } = useTranslation();
  const importer = useBulkImport();
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const phase = importer.phase;
  const startImport = importer.start;

  useEffect(() => {
    if (open && initialFile !== null) void startImport(initialFile);
  }, [open, initialFile, startImport]);

  const close = (next: boolean): void => {
    if (!next) importer.reset();
    onOpenChange(next);
  };

  const confirm = (): void => {
    if (phase.kind !== "ready") return;
    const text = importer.takeText();
    if (text === null) return;
    importer.reset();
    onConfirm(text, phase.digest);
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent data-testid="bulk-import-dialog">
        <DialogHeader>
          <DialogTitle>{t("precheck.bulk.import.title")}</DialogTitle>
          <DialogDescription>{t("precheck.bulk.import.description")}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {phase.kind === "idle" && (
            <div className="flex flex-col items-center gap-3 rounded-md border border-dashed p-8">
              <FileUp className="text-muted-foreground size-8" aria-hidden />
              <p className="text-muted-foreground text-sm">{t("precheck.bulk.import.pickHint")}</p>
              <Button
                variant="outline"
                onClick={() => { fileInputRef.current?.click(); }}
                data-testid="bulk-import-browse"
              >
                {t("precheck.bulk.import.browse")}
              </Button>
              <p className="text-muted-foreground text-xs">
                {t("precheck.bulk.import.limitsHint")}
              </p>
            </div>
          )}

          {phase.kind === "reading" && (
            <div className="space-y-2" data-testid="bulk-import-reading">
              <p className="text-sm">
                {t("precheck.bulk.import.reading", {
                  name: importer.fileName ?? "",
                  percent: Math.round((phase.bytes / Math.max(phase.totalBytes, 1)) * 100),
                })}
              </p>
              <Progress value={(phase.bytes / Math.max(phase.totalBytes, 1)) * 100} />
              <p className="text-muted-foreground text-xs">
                {formatBytes(phase.bytes)} / {formatBytes(phase.totalBytes)}
              </p>
              <Button variant="outline" size="sm" onClick={importer.cancel} data-testid="bulk-import-cancel">
                <Ban className="size-3.5" aria-hidden />
                {t("precheck.bulk.import.cancel")}
              </Button>
            </div>
          )}

          {phase.kind === "cancelled" && (
            <Alert data-testid="bulk-import-cancelled">
              <Ban className="size-4" aria-hidden />
              <AlertTitle>{t("precheck.bulk.import.cancelledTitle")}</AlertTitle>
              <AlertDescription>{t("precheck.bulk.import.cancelledDescription")}</AlertDescription>
            </Alert>
          )}

          {phase.kind === "error" && (
            <Alert variant="destructive" data-testid="bulk-import-error">
              <TriangleAlert className="size-4" aria-hidden />
              <AlertTitle>{t("precheck.bulk.import.errorTitle")}</AlertTitle>
              <AlertDescription>{phase.message}</AlertDescription>
            </Alert>
          )}

          {(phase.kind === "ready" || phase.kind === "blocked") && (
            <div className="space-y-3" data-testid="bulk-import-summary">
              {phase.digest !== null && (
                <dl className="grid grid-cols-2 gap-2 text-sm">
                  <div>
                    <dt className="text-muted-foreground text-xs">{t("precheck.bulk.import.sizeLabel")}</dt>
                    <dd>{formatBytes(phase.digest.sizeBytes)}</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground text-xs">{t("precheck.bulk.import.statementsLabel")}</dt>
                    <dd data-testid="import-summary-statements">{phase.digest.statementCount}</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground text-xs">{t("precheck.bulk.import.groupsLabel")}</dt>
                    <dd>{phase.digest.groupCount}</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground text-xs">{t("precheck.bulk.import.tablesLabel")}</dt>
                    <dd>{phase.digest.tableCount}</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground text-xs">{t("precheck.bulk.import.coverageLabel")}</dt>
                    <dd>{Math.round(phase.digest.coverageRatio * 100)}%</dd>
                  </div>
                  <div>
                    <dt className="text-muted-foreground text-xs">{t("precheck.bulk.import.anomaliesLabel")}</dt>
                    <dd>{phase.digest.anomalyCount}</dd>
                  </div>
                </dl>
              )}

              {phase.digest?.truncated && (
                <Alert>
                  <AlertTriangle className="size-4" aria-hidden />
                  <AlertTitle>{t("precheck.bulk.import.truncatedTitle")}</AlertTitle>
                  <AlertDescription>{t("precheck.bulk.import.truncatedDescription")}</AlertDescription>
                </Alert>
              )}

              {phase.kind === "blocked" ? (
                phase.blocks.map((block) => (
                  <Alert key={block.key} variant="destructive" data-testid={`bulk-import-block-${block.key}`}>
                    <TriangleAlert className="size-4" aria-hidden />
                    <AlertTitle>{t(`precheck.bulk.import.block.${block.key}.title`)}</AlertTitle>
                    <AlertDescription>
                      {t(`precheck.bulk.import.block.${block.key}.description`, {
                        limit: block.limit,
                        actual: block.actual,
                        count: block.count ?? 0,
                        samples: (block.samples ?? []).join(", "),
                      })}
                    </AlertDescription>
                  </Alert>
                ))
              ) : (
                <Alert data-testid="bulk-import-ok">
                  <CircleCheck className="size-4" aria-hidden />
                  <AlertTitle>{t("precheck.bulk.import.okTitle")}</AlertTitle>
                  <AlertDescription>{t("precheck.bulk.import.okDescription")}</AlertDescription>
                </Alert>
              )}
            </div>
          )}

          <input
            ref={fileInputRef}
            type="file"
            accept=".sql,.txt,text/plain"
            className="hidden"
            onChange={(event) => {
              const file = event.target.files?.[0];
              event.target.value = "";
              if (file !== undefined) void importer.start(file);
            }}
            data-testid="bulk-import-input"
            aria-label={t("precheck.bulk.import.pickHint")}
          />
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => { close(false); }} disabled={uploading}>
            {t("common.cancel")}
          </Button>
          {(phase.kind === "ready" || phase.kind === "blocked" || phase.kind === "cancelled" || phase.kind === "error") && (
            <Button
              variant="outline"
              onClick={() => { importer.reset(); }}
              disabled={uploading}
              data-testid="bulk-import-again"
            >
              {t("precheck.bulk.import.pickAgain")}
            </Button>
          )}
          <PendingButton
            pending={uploading}
            onClick={confirm}
            disabled={phase.kind !== "ready" || uploading}
            data-testid="bulk-import-confirm"
          >
            {t("precheck.bulk.import.confirm")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
