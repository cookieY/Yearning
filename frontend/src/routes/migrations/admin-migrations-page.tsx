import i18next from "@/shared/i18n";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { CircleCheck, CircleDashed, DatabaseZap, ShieldAlert } from "lucide-react";
import type { LegacyMigrationCandidate, LegacyMigrationRun } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardTitle } from "@/shared/components/ui/card";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { useApproveMigrationRun, useConfirmMigrationCandidate, useMigrationRuns } from "@/features/admin/use-admin";

/**
 * Migration Review Workbench (route /admin/migrations — migration-mode ONLY; PRD F10 /
 * migration contract §8 machine gates). The page renders the dry-run
 * reconciliation (per-table read/written/excluded/quarantined/failed with
 * reconciliation verdicts), the status counters and the candidate mappings:
 * every candidate is confirmed individually against its target definition
 * hash before the final approval, which requires typing the exact
 * `APPROVE <run_uuid>` phrase and the canonical manifest hash.
 *
 * There is deliberately NO "Start Apply" control anywhere: approval never
 * starts Apply — only the offline migration command may (M001). The
 * approval section says so explicitly.
 */

function describeErrorText(display: { messageKey: string; requestId: string | null }): string {
  // Translated through the shared i18n bundle so users never see raw keys.
  const text = i18next.t(display.messageKey);
  return display.requestId === null ? text : `${text} (${display.requestId})`;
}

export default function AdminMigrationsPage() {
  const { t } = useTranslation();
  // §18.82: backend cursor pagination (default 20 rows/page).
  const pager = useCursorPage(20);
  const runsQuery = useMigrationRuns(true, { limit: pager.pageSize, after: pager.after });
  const [selectedId, setSelectedId] = useState<string>("");

  const runs = runsQuery.data.items;
  const selected: LegacyMigrationRun | null =
    runs.find((run) => run.id === selectedId) ?? runs[0] ?? null;

  return (
    <PageLayout data-testid="admin-migrations-page">
      <PageHeader
        icon={DatabaseZap}
        title={t("adminMigrations.title")}
        description={t("adminMigrations.description")}
      />
      <Alert data-testid="migration-no-apply-note">
        <ShieldAlert />
        <AlertTitle>{t("adminMigrations.noApplyTitle")}</AlertTitle>
        <AlertDescription>{t("adminMigrations.noApplyDescription")}</AlertDescription>
      </Alert>

      {runsQuery.isPending && (
        <Card>
          <LoadingState variant="page" />
        </Card>
      )}
      {runsQuery.error !== null && (
        <ErrorState error={runsQuery.error} operationId="listLegacyMigrationRuns" onRetry={() => void runsQuery.refetch()} />
      )}

      {!runsQuery.isPending && runsQuery.error === null && selected === null && (
        <EmptyState title={t("adminMigrations.empty")} testId="admin-migrations-empty" />
      )}

      {!runsQuery.isPending && runsQuery.error === null && (
        <TablePagination
          busy={runsQuery.isFetching && runsQuery.isPlaceholderData}
          page={pager.pageNo}
          hasMore={runsQuery.data.page.has_more}
          isFirst={pager.isFirst}
          onPrev={pager.goPrev}
          onJumpPage={pager.goToPage}
          onNext={() => { pager.pushCursor(runsQuery.data.page.next_cursor); }}
          pageSize={pager.pageSize}
          onPageSizeChange={pager.setPageSize}
          rowsOnPage={runsQuery.data.items.length}
          testIdPrefix="migrations"
        />
      )}
      {selected !== null && (
        <>
          {runs.length > 1 && (
            <div className="flex flex-wrap gap-2">
              {runs.map((run) => (
                <Button
                  key={run.id}
                  size="sm"
                  variant={run.id === selected.id ? "default" : "outline"}
                  onClick={() => { setSelectedId(run.id); }}
                  data-testid={`migration-run-select-${run.id}`}
                >
                  {run.id.slice(0, 8)} · {run.state}
                </Button>
              ))}
            </div>
          )}
          <RunDetail run={selected} />
        </>
      )}
    </PageLayout>
  );
}

function RunDetail({ run }: { run: LegacyMigrationRun }) {
  const { t } = useTranslation();
  const confirmCandidate = useConfirmMigrationCandidate();
  const approveRun = useApproveMigrationRun();
  const [phrase, setPhrase] = useState("");
  const [errorText, setErrorText] = useState<string | null>(null);

  const allConfirmed = run.candidates.every((candidate) => candidate.confirmed);
  const approvable = run.state === "awaiting_confirmation" && allConfirmed;

  const confirm = (candidate: LegacyMigrationCandidate, confirmed: boolean) => {
    setErrorText(null);
    confirmCandidate.mutate(
      {
        runId: run.id,
        candidateId: candidate.candidate_id,
        version: run.version,
        body: {
          confirmed,
          target_definition_hash: candidate.target_definition_hash,
        },
      },
      {
        onSuccess: () => {
          appToast.success(t("common.savedToast"));
        },
        onError: (error) => {
          const msg = describeErrorText(describeError(error, "confirmLegacyMigrationCandidate"));
          setErrorText(msg);
          appToast.error(msg);
        },
      },
    );
  };

  const approve = () => {
    setErrorText(null);
    approveRun.mutate(
      {
        runId: run.id,
        version: run.version,
        body: {
          manifest_hash: run.manifest_hash ?? "",
          confirmation_phrase: phrase,
        },
      },
      {
        onSuccess: () => {
          appToast.success(t("common.savedToast"));
        },
        onError: (error) => {
          const msg = describeErrorText(describeError(error, "approveLegacyMigrationRun"));
          setErrorText(msg);
          appToast.error(msg);
        },
      },
    );
  };

  return (
    <>
      <Card>
        <ListToolbar>
          <div className="flex min-w-0 flex-col gap-1">
            <CardTitle className="flex flex-wrap items-center gap-2">
              <DatabaseZap aria-hidden />
              {t("adminMigrations.run.title", { id: run.id })}
              <Badge variant={run.state === "approved" ? "secondary" : run.state === "failed" ? "destructive" : "outline"}>
                {t(`adminMigrations.state.${run.state}`)}
              </Badge>
            </CardTitle>
            <CardDescription>
              {t("adminMigrations.run.meta", {
                hash: run.manifest_hash ?? "—",
                active: run.active_work_count,
                unknown: run.unknown_status_count,
                ambiguous: run.ambiguous_status_count,
              })}
            </CardDescription>
          </div>
        </ListToolbar>
        <CardContent className="px-0!">
          <Table data-testid="migration-table-results">
            <TableHeader>
              <TableRow>
                <TableHead>{t("adminMigrations.table.column.source")}</TableHead>
                <TableHead>{t("adminMigrations.table.column.read")}</TableHead>
                <TableHead>{t("adminMigrations.table.column.written")}</TableHead>
                <TableHead>{t("adminMigrations.table.column.excluded")}</TableHead>
                <TableHead>{t("adminMigrations.table.column.quarantined")}</TableHead>
                <TableHead>{t("adminMigrations.table.column.failed")}</TableHead>
                <TableHead>{t("adminMigrations.table.column.verdict")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {run.table_results.map((result) => (
                <TableRow key={result.source_table} data-testid={`migration-table-result-${result.source_table}`}>
                  <TableCell className="font-mono font-semibold text-foreground">{result.source_table}</TableCell>
                  <TableCell className="tabular-nums">{result.read}</TableCell>
                  <TableCell className="tabular-nums">{result.written}</TableCell>
                  <TableCell className="tabular-nums">{result.excluded}</TableCell>
                  <TableCell className="tabular-nums">{result.quarantined}</TableCell>
                  <TableCell className="tabular-nums">{result.failed}</TableCell>
                  <TableCell>
                    <StatusBadge tone={result.reconciliation_passed ? "emerald" : "red"}>
                      {result.reconciliation_passed
                        ? t("adminMigrations.table.passed")
                        : t("adminMigrations.table.failed")}
                    </StatusBadge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <ListToolbar>
          <div className="flex min-w-0 flex-col gap-1">
            <CardTitle>{t("adminMigrations.candidates.title")}</CardTitle>
            <CardDescription>{t("adminMigrations.candidates.description")}</CardDescription>
          </div>
        </ListToolbar>
        <CardContent className="flex flex-col gap-3">
          {run.candidates.map((candidate) => (
            <div key={candidate.candidate_id} className="flex flex-col gap-2 rounded-lg border p-3" data-testid={`migration-candidate-${candidate.candidate_id}`}>
              <div className="flex flex-wrap items-center gap-2">
                {candidate.confirmed ? (
                  <CircleCheck className="size-4 text-muted-foreground" />
                ) : (
                  <CircleDashed className="size-4" />
                )}
                <Badge variant="outline">{candidate.kind}</Badge>
                <Badge
                  variant={
                    candidate.risk === "possible_expansion" ? "destructive" : candidate.risk === "unmapped" ? "outline" : "secondary"
                  }
                >
                  {t(`adminMigrations.candidate.risk.${candidate.risk}`)}
                </Badge>
                {candidate.confirmed_by !== null && candidate.confirmed_by !== undefined && (
                  <span className="text-muted-foreground text-xs">
                    {t("adminMigrations.candidates.confirmedBy", {
                      by: candidate.confirmed_by,
                      at: formatTimestamp(candidate.confirmed_at),
                    })}
                  </span>
                )}
              </div>
              <div className="text-muted-foreground font-mono text-xs break-all">
                {t("adminMigrations.candidates.hash", { hash: candidate.target_definition_hash })}
              </div>
              <div className="text-xs">
                <span className="text-muted-foreground">{t("adminMigrations.candidates.sources")}: </span>
                {candidate.source_refs.join(", ")}
              </div>
              <div className="flex flex-wrap gap-1 text-xs">
                {candidate.coverage_added.map((item) => (
                  <Badge key={`add-${item}`} variant="secondary">
                    + {item}
                  </Badge>
                ))}
                {candidate.coverage_missing.map((item) => (
                  <Badge key={`miss-${item}`} variant="destructive">
                    − {item}
                  </Badge>
                ))}
              </div>
              <div>
                <PendingButton
                  size="sm"
                  variant={candidate.confirmed ? "outline" : "default"}
                  pending={confirmCandidate.isPending}
                  disabled={run.state !== "awaiting_confirmation"}
                  onClick={() => { confirm(candidate, !candidate.confirmed); }}
                  data-testid={`migration-candidate-confirm-${candidate.candidate_id}`}
                >
                  {candidate.confirmed ? t("adminMigrations.candidates.unconfirm") : t("adminMigrations.candidates.confirm")}
                </PendingButton>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      <Card>
        <ListToolbar>
          <div className="flex min-w-0 flex-col gap-1">
            <CardTitle>{t("adminMigrations.approval.title")}</CardTitle>
            <CardDescription>{t("adminMigrations.approval.description")}</CardDescription>
          </div>
        </ListToolbar>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label>{t("adminMigrations.approval.manifestHash")}</Label>
            <code className="bg-muted/50 block rounded p-2 font-mono text-xs break-all" data-testid="migration-approval-hash">
              {run.manifest_hash ?? "—"}
            </code>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="migration-phrase">{t("adminMigrations.approval.phrase")}</Label>
            <Input
              id="migration-phrase"
              value={phrase}
              onChange={(event) => { setPhrase(event.target.value); }}
              className="font-mono"
              data-testid="migration-phrase"
            />
            <p className="text-muted-foreground font-mono text-xs">APPROVE {run.id}</p>
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="migration-approval-error">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <div className="flex items-center gap-2">
            <PendingButton
              variant="destructive"
              pending={approveRun.isPending}
              disabled={!approvable || phrase !== `APPROVE ${run.id}`}
              onClick={approve}
              data-testid="migration-approve"
            >
              {t("adminMigrations.approval.submit")}
            </PendingButton>
            {!allConfirmed && (
              <span className="text-muted-foreground text-xs">{t("adminMigrations.approval.pendingCandidates")}</span>
            )}
          </div>
        </CardContent>
      </Card>
    </>
  );
}
