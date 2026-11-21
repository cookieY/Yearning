import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { EyeOff, X } from "lucide-react";
import type { DatasourceColumn, QueryGrant } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { describeError } from "@/shared/api/error-display";
import { describeQueryErrorText, todayLocal } from "@/features/query/query-error-text";
import {
  EXPORT_PAGE_SIZE,
  fetchPage,
  runSelect,
} from "@/features/query/use-query-domain";
import {
  downloadXlsx,
  exportExecutionToXlsx,
  type ExportProgress,
} from "@/features/query/xlsx-export";
import { PendingButton } from "@/shared/components/pending-button";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { DatePicker } from "@/shared/components/ui/date-picker";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Label } from "@/shared/components/ui/label";
import { Progress } from "@/shared/components/ui/progress";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { Textarea } from "@/shared/components/ui/textarea";

export interface ExportContext {
  datasourceId: string;
  schemaName: string;
  sql: string;
}

export interface InspectTarget {
  datasourceId: string;
  schema: string;
  table: string;
}

export function TableStructureDialog({
  target,
  isPending,
  error,
  columns,
  onClose,
}: {
  target: InspectTarget | null;
  isPending: boolean;
  error: unknown;
  columns: DatasourceColumn[] | undefined;
  onClose: () => void;
}) {
  const { t } = useTranslation();

  return (
    <Dialog open={target !== null} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono text-sm">
            {target === null ? "" : `${target.schema}.${target.table}`}
          </DialogTitle>
          <DialogDescription>{t("query.structure.description")}</DialogDescription>
        </DialogHeader>
        <ScrollArea data-testid="query-table-structure" viewportClassName="max-h-96">
          <div>
            {isPending && <LoadingState variant="table" columns={8} />}
            {error !== null && <p className="text-destructive p-2 text-xs">{t("query.tree.loadFailed")}</p>}
            {columns !== undefined && (
              <Table density="compact">
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-10">#</TableHead>
                    <TableHead>{t("query.structure.column")}</TableHead>
                    <TableHead>{t("query.structure.type")}</TableHead>
                    <TableHead className="w-14">{t("query.structure.key")}</TableHead>
                    <TableHead className="w-20">{t("query.structure.nullable")}</TableHead>
                    <TableHead>{t("query.structure.default")}</TableHead>
                    <TableHead>{t("query.structure.extra")}</TableHead>
                    <TableHead>{t("query.structure.comment")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {columns.map((column) => (
                    <TableRow key={column.column_name} data-testid={`query-structure-${column.column_name}`}>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">{column.ordinal}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {column.column_name}
                        {column.masked && (
                          <EyeOff className="ml-1 inline size-3 text-muted-foreground" aria-label={t("query.result.maskedColumns")} />
                        )}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{column.data_type}</TableCell>
                      <TableCell className="text-xs">
                        {column.column_key ? (
                          <Badge variant={column.column_key === "PRI" ? "default" : "secondary"} className="px-1.5 font-mono text-[10px]">
                            {column.column_key}
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-xs">{column.nullable ? "NULL" : "NOT NULL"}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {column.column_default || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {column.extra || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                      <TableCell className="text-xs">
                        {column.comment || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}

export function CloseSessionDialog({
  open,
  reason,
  onReason,
  onCancel,
  onSubmit,
  submitting,
}: {
  open: boolean;
  reason: string;
  onReason: (value: string) => void;
  onCancel: () => void;
  onSubmit: () => void;
  submitting: boolean;
}) {
  const { t } = useTranslation();
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onCancel(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("query.exit.title")}</DialogTitle>
          <DialogDescription>{t("query.exit.description")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2">
          <Label htmlFor="query-exit-reason">{t("query.exit.reason")}</Label>
          <Textarea
            id="query-exit-reason"
            value={reason}
            onChange={(event) => { onReason(event.target.value); }}
            maxLength={4096}
            data-testid="query-exit-reason"
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={submitting}
            onClick={() => { onSubmit(); }}
            data-testid="query-exit-confirm"
          >
            {t("query.exit.confirm")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function defaultRenewUntil(grant: QueryGrant | null): string {
  // Default strictly extends the current grant expiry in UTC calendar digits (OBS-009).
  const base =
    grant === null || grant.expires_at === null || grant.expires_at === undefined
      ? new Date()
      : new Date(Date.parse(grant.expires_at));
  const extended = new Date(base.getTime() + 24 * 60 * 60 * 1000);
  return `${String(extended.getUTCFullYear())}-${String(extended.getUTCMonth() + 1).padStart(2, "0")}-${String(extended.getUTCDate()).padStart(2, "0")}`;
}

/** Renewal request dialog; mounted conditionally per open by parent so initial state stays clean. */
export function RenewGrantDialog({
  grant,
  submitting,
  error,
  onCancel,
  onSubmit,
}: {
  grant: QueryGrant | null;
  submitting: boolean;
  error: string | null;
  onCancel: () => void;
  onSubmit: (input: { requested_until: string; reason: string }) => void;
}) {
  const { t } = useTranslation();
  const [until, setUntil] = useState(() => defaultRenewUntil(grant));
  const [untilTime, setUntilTime] = useState("23:59");
  const [reason, setReason] = useState("");
  const valid = until !== "" && untilTime !== "" && reason.trim() !== "";

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onCancel(); }}>
      <DialogContent data-testid="query-renew-dialog">
        <DialogHeader>
          <DialogTitle>{t("query.workspace.renewTitle")}</DialogTitle>
          <DialogDescription>{t("query.workspace.renewDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-renew-until">{t("query.apply.until")}</Label>
            <DatePicker
              value={until}
              onChange={(next) => { setUntil(next); }}
              time={untilTime}
              onTimeChange={(next) => { setUntilTime(next); }}
              timeLabel={t("query.apply.untilTime")}
              timeId="query-renew-until-time"
              timeTestId="query-renew-until-time"
              testId="query-renew-until"
              ariaLabel={t("query.apply.until")}
              disableBefore={todayLocal()}
              className="w-full"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-renew-reason">{t("query.apply.reason")}</Label>
            <Textarea
              id="query-renew-reason"
              value={reason}
              onChange={(event) => { setReason(event.target.value); }}
              maxLength={4096}
              data-testid="query-renew-reason"
            />
          </div>
          {error !== null && (
            <Alert variant="destructive" data-testid="query-renew-error">
              <AlertTitle>{t("query.workspace.renewFailed")}</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            onClick={() => {
              onSubmit({ requested_until: `${until}T${untilTime}:00Z`, reason });
            }}
            pending={submitting}
            disabled={!valid}
            data-testid="query-renew-submit"
          >
            {t("query.workspace.renewSubmit")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function ExportDialog({
  context,
  sessionId,
  onClose,
}: {
  context: ExportContext;
  sessionId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [progress, setProgress] = useState<ExportProgress | null>(() => ({
    pagesRead: 0,
    rowsExported: 0,
    finished: false,
    failure: null,
  }));
  const [done, setDone] = useState<{ rows: number; executionId: string } | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const startedRef = useRef(false);
  const userCancelledRef = useRef(false);
  const mountedRef = useRef(false);
  const tRef = useRef(t);

  useEffect(() => {
    tRef.current = t;
  }, [t]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const closeExport = () => {
    userCancelledRef.current = true;
    onClose();
  };

  useEffect(() => {
    if (startedRef.current) return;
    startedRef.current = true;
    const isCancelled = () => userCancelledRef.current || !mountedRef.current;
    // Open a dedicated export execution with page_size=1 so only purpose=export continuations carry full pages (SEC-0031).
    void (async () => {
      try {
        const first = await runSelect(sessionId, {
          datasource_id: context.datasourceId,
          schema_name: context.schemaName,
          sql: context.sql,
          timeout_ms: 30000,
          page_size: 1,
        });
        if (isCancelled()) {
          if (mountedRef.current) {
            setFailure(tRef.current("query.export.failedWithRange", { message: "cancelled", rows: 0 }));
          }
          return;
        }
        const executionId = first.execution_id;
        await exportExecutionToXlsx(
          first,
          (cursor) => fetchPage(executionId, cursor, "export", EXPORT_PAGE_SIZE),
          {
            onProgress: (nextProgress) => {
              if (!mountedRef.current) return;
              setProgress(nextProgress);
            },
            onDone: (bytes, rows) => {
              if (isCancelled()) return;
              downloadXlsx(bytes, `query-export-${executionId}.xlsx`);
              setDone({ rows, executionId });
            },
            onFailure: (message, rows) => {
              if (!mountedRef.current) return;
              setFailure(tRef.current("query.export.failedWithRange", { message, rows }));
            },
          },
          isCancelled,
        );
      } catch (error) {
        if (!mountedRef.current) return;
        setFailure(describeQueryErrorText(describeError(error, "executeSelect")));
      }
    })();
  }, [context, sessionId]);

  return (
    <Dialog open onOpenChange={(next) => { if (!next) closeExport(); }}>
      <DialogContent data-testid="query-export-dialog">
        <DialogHeader>
          <DialogTitle>{t("query.export.title")}</DialogTitle>
          <DialogDescription>{t("query.export.description")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {progress !== null && !done && failure === null && (
            <>
              <Progress value={progress.finished ? 100 : Math.min(95, progress.pagesRead * 10)} />
              <p className="text-muted-foreground text-xs" data-testid="query-export-progress">
                {t("query.export.progress", { pages: progress.pagesRead, rows: progress.rowsExported })}
              </p>
            </>
          )}
          {done !== null && (
            <p className="text-xs" data-testid="query-export-done">
              {t("query.export.done", { rows: done.rows })}
            </p>
          )}
          {done === null && progress === null && <LoadingState variant="inline" />}
          {failure !== null && (
            <Alert variant="destructive" data-testid="query-export-failure">
              <AlertDescription>{failure}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          {done === null && failure === null && (
            <Button
              variant="outline"
              onClick={() => {
                userCancelledRef.current = true;
              }}
              data-testid="query-export-cancel"
            >
              <X />
              {t("query.export.cancel")}
            </Button>
          )}
          <Button onClick={closeExport} data-testid="query-export-close">{t("common.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
