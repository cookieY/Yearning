import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Ban, Check, ShieldOff, X } from "lucide-react";
import type { QueryAccessRequest, QueryGrant } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { grantDominatesCandidate } from "@/features/query/query-sql-utils";
import { useRevokeGrant } from "@/features/query/use-query-domain";
import { QUERY_GRANT_TONE, QUERY_REQUEST_TONE } from "@/features/query/query-state-tone";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ListToolbar } from "@/shared/components/page-layout";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { PendingButton } from "@/shared/components/pending-button";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardTitle } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { Label } from "@/shared/components/ui/label";
import { SheetDescription, SheetHeader, SheetTitle } from "@/shared/components/ui/sheet";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { Textarea } from "@/shared/components/ui/textarea";
import { formatTimestamp } from "@/shared/lib/timezones";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";

export interface ConsolidatedGrantEntry {
  grant: QueryGrant;
  superseded: QueryGrant[];
}

export function consolidateActiveGrantEntries(
  grants: QueryGrant[],
  requests: QueryAccessRequest[],
): ConsolidatedGrantEntry[] {
  const requestByGrantId = new Map<string, QueryAccessRequest>();
  for (const req of requests) {
    if (req.grant_id !== null && req.grant_id !== undefined) {
      requestByGrantId.set(req.grant_id, req);
    }
  }
  const revocable = grants.filter((grant) => grant.state === "active" && grant.can_revoke === true);
  const primaries = revocable.filter(
    (candidate) =>
      !revocable.some((primary) => grantDominatesCandidate(primary, candidate, requestByGrantId)),
  );
  const entryMap = new Map<string, ConsolidatedGrantEntry>(
    primaries.map((grant) => [grant.id, { grant, superseded: [] }]),
  );
  for (const candidate of revocable) {
    if (entryMap.has(candidate.id)) continue;
    const owner = primaries.find((primary) =>
      grantDominatesCandidate(primary, candidate, requestByGrantId),
    );
    if (owner !== undefined) {
      entryMap.get(owner.id)?.superseded.push(candidate);
    }
  }
  return [...entryMap.values()];
}

export function normalizeReason(reason: string | null | undefined): string | null {
  if (reason === null || reason === undefined || reason.trim() === "") {
    return null;
  }
  return reason;
}

export function formatRequestDatasources(row: QueryAccessRequest): string {
  const labels = row.datasource_names?.length ? row.datasource_names : row.datasource_ids;
  return labels.join(" · ");
}

export function RequestStatusBadge({
  row,
  grant,
}: {
  row: QueryAccessRequest;
  grant: QueryGrant | undefined;
}) {
  const { t } = useTranslation();
  const showGrantTerminal =
    row.state === "grant_active" && grant !== undefined && grant.state !== "active";
  return showGrantTerminal ? (
    <StatusBadge tone={QUERY_GRANT_TONE[grant.state]}>
      {t(`query.grant.state.${grant.state}`)}
    </StatusBadge>
  ) : (
    <StatusBadge tone={QUERY_REQUEST_TONE[row.state]}>
      {t(`query.request.state.${row.state}`)}
    </StatusBadge>
  );
}

export function QueryAccessDetailBody({
  row,
  grant,
  onDecide,
}: {
  row: QueryAccessRequest;
  grant: QueryGrant | undefined;
  onDecide: (decision: "approve" | "reject") => void;
}) {
  const { t } = useTranslation();
  const reasonText = normalizeReason(row.reason);
  return (
    <>
      <SheetHeader>
        <SheetTitle>{t("approvals.queryAccess.detailTitle")}</SheetTitle>
        <SheetDescription>{t("approvals.queryAccess.detailDescription")}</SheetDescription>
      </SheetHeader>
      <div className="flex flex-col gap-4 px-5 pr-6 pb-6">
        <RequestDetailRow label={t("approvals.queryAccess.column.requester")}>
          <span className="font-mono font-semibold text-foreground">
            {row.applicant_name ?? row.requester_user_id}
          </span>
        </RequestDetailRow>
        <RequestDetailRow label={t("approvals.queryAccess.column.state")}>
          <RequestStatusBadge row={row} grant={grant} />
        </RequestDetailRow>
        <RequestDetailRow label={t("approvals.queryAccess.column.datasources")}>
          <span className="font-mono text-xs break-all">{formatRequestDatasources(row)}</span>
        </RequestDetailRow>
        <RequestDetailRow label={t("approvals.queryAccess.requestedUntil")}>
          {row.requested_until ? formatTimestamp(row.requested_until) : "—"}
        </RequestDetailRow>
        <RequestDetailRow label={t("approvals.queryAccess.column.submittedAt")}>
          {formatTimestamp(row.created_at)}
        </RequestDetailRow>
        <RequestDetailRow label={t("approvals.queryAccess.reason")}>
          <div
            className="rounded-md border bg-muted/40 p-3 text-sm whitespace-pre-wrap break-words"
            data-testid="query-access-detail-reason"
          >
            {reasonText ?? (
              <span className="text-muted-foreground">{t("approvals.queryAccess.noReason")}</span>
            )}
          </div>
        </RequestDetailRow>
        {row.state === "access_pending" && row.can_decide === true && (
          <div className="flex items-center gap-2 pt-2">
            <Button
              size="sm"
              onClick={() => {
                onDecide("approve");
              }}
              data-testid="query-access-detail-approve"
            >
              <Check /> {t("approvals.queryAccess.approve")}
            </Button>
            <Button
              size="sm"
              variant="destructive"
              onClick={() => {
                onDecide("reject");
              }}
              data-testid="query-access-detail-reject"
            >
              <X /> {t("approvals.queryAccess.reject")}
            </Button>
          </div>
        )}
      </div>
    </>
  );
}

export function DecisionRequestSummary({ row }: { row: QueryAccessRequest }) {
  const { t } = useTranslation();
  const reasonText = normalizeReason(row.reason);
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-muted/40 p-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="font-mono font-semibold text-foreground">
          {row.applicant_name ?? row.requester_user_id}
        </span>
        {row.requested_until && (
          <span className="text-muted-foreground">
            {t("approvals.queryAccess.requestedUntil")}: {formatTimestamp(row.requested_until)}
          </span>
        )}
      </div>
      <div className="text-muted-foreground font-mono text-xs break-all">
        {formatRequestDatasources(row)}
      </div>
      <div className="flex flex-col gap-1 pt-1">
        <span className="text-muted-foreground text-xs">{t("approvals.queryAccess.reason")}</span>
        <p
          className="max-h-40 overflow-y-auto whitespace-pre-wrap break-words text-foreground"
          data-testid="query-access-decision-reason"
        >
          {reasonText ?? (
            <span className="text-muted-foreground">{t("approvals.queryAccess.noReason")}</span>
          )}
        </p>
      </div>
    </div>
  );
}

function RequestDetailRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-muted-foreground text-xs">{label}</span>
      <div className="text-sm">{children}</div>
    </div>
  );
}

export function GrantRevocationCard({ entries }: { entries: ConsolidatedGrantEntry[] }) {
  const { t } = useTranslation();
  const revoke = useRevokeGrant();
  const [revoking, setRevoking] = useState<ConsolidatedGrantEntry | null>(null);
  const [reason, setReason] = useState("");
  const [errorText, setErrorText] = useState<string | null>(null);
  const [grantTerms, setGrantTerms] = useState<SearchTerm[]>([]);
  const [grantLive, setGrantLive] = useState<SearchTerm | null>(null);
  const grantSearchAccessors = useMemo<SearchAccessor<ConsolidatedGrantEntry>[]>(
    () => [
      {
        key: "grant_owner",
        label: t("approvals.queryAccess.column.grantOwner"),
        value: ({ grant }) => `${grant.holder_name ?? ""} ${grant.requester_user_id}`,
      },
      {
        key: "state",
        label: t("approvals.queryAccess.column.state"),
        value: ({ grant }) => `${grant.state} ${t(`query.grant.state.${grant.state}`)}`,
        options: [{ value: "active", label: t("query.grant.state.active") }],
      },
      {
        key: "expires_at",
        label: t("approvals.queryAccess.column.expiresAt"),
        value: ({ grant }) =>
          grant.expires_at === null || grant.expires_at === undefined
            ? ""
            : formatTimestamp(grant.expires_at),
        input: "date",
      },
    ],
    [t],
  );
  const effectiveGrantTerms =
    grantLive === null ? grantTerms : [...grantTerms, grantLive];
  const filteredEntries = filterBySearchTerms(
    entries,
    effectiveGrantTerms,
    grantSearchAccessors,
  );
  const grantsBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(filteredEntries.map((entry) => entry.grant.id)),
  );

  const submit = async () => {
    if (revoking === null) return;
    setErrorText(null);
    try {
      await revoke.mutateAsync({
        grantId: revoking.grant.id,
        version: revoking.grant.version,
        reason,
      });
      for (const sib of revoking.superseded) {
        await revoke.mutateAsync({ grantId: sib.id, version: sib.version, reason });
      }
      appToast.success(t("approvals.queryAccess.revoke"));
      setRevoking(null);
      setReason("");
    } catch (error) {
      const errText = describeErrorText(describeError(error, "revokeQueryGrant"));
      setErrorText(errText);
      appToast.error(errText);
    }
  };

  return (
    <Card className="overflow-visible">
      <ListToolbar fill>
        <CardTitle className="flex shrink-0 items-center gap-2">
          <ShieldOff aria-hidden />
          {t("approvals.queryAccess.grantsCard")}
        </CardTitle>
        <GlobalSearchBar
          fields={grantSearchAccessors}
          terms={grantTerms}
          onTermsChange={setGrantTerms}
          onLiveChange={setGrantLive}
          placeholder={t("approvals.queryAccess.grantsSearchPlaceholder")}
          testId="query-grants-search"
          className="w-full min-w-0 flex-1"
        />
      </ListToolbar>
      <CardContent className="px-0!">
        {filteredEntries.length === 0 && effectiveGrantTerms.length > 0 ? (
          <EmptyState
            filtered
            title={t("table.emptySearch")}
            testId="query-grants-search-empty"
          />
        ) : entries.length === 0 ? (
          <EmptyState
            title={t("approvals.queryAccess.grantsEmpty")}
            description={t("approvals.queryAccess.grantsEmptyDescription")}
          />
        ) : (
          <Table data-testid="query-access-grants-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t("approvals.queryAccess.column.grantOwner")}</TableHead>
                <TableHead>{t("approvals.queryAccess.column.state")}</TableHead>
                <TableHead>{t("approvals.queryAccess.column.expiresAt")}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody ref={grantsBodyRef}>
              {filteredEntries.map((entry) => {
                const { grant } = entry;
                return (
                  <TableRow key={grant.id} data-row-key={grant.id} data-testid={`query-access-grant-${grant.id}`}>
                    <TableCell className="font-mono font-semibold text-foreground">{grant.holder_name ?? grant.requester_user_id}</TableCell>
                    <TableCell>
                      <StatusBadge tone="emerald">{t("query.grant.state.active")}</StatusBadge>
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {grant.expires_at === null || grant.expires_at === undefined
                        ? "—"
                        : formatTimestamp(grant.expires_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <TableRowActions testId={`query-grant-actions-${grant.id}`}>
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => {
                            setErrorText(null);
                            setReason("");
                            setRevoking(entry);
                          }}
                          data-testid={`query-access-revoke-${grant.id}`}
                        >
                          <Ban /> {t("approvals.queryAccess.revoke")}
                        </DropdownMenuItem>
                      </TableRowActions>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>

      <Dialog open={revoking !== null} onOpenChange={(open) => { if (!open) setRevoking(null); }}>
        <DialogContent data-testid="query-access-revoke-dialog">
          <DialogHeader>
            <DialogTitle>{t("approvals.queryAccess.revokeTitle")}</DialogTitle>
            <DialogDescription>{t("approvals.queryAccess.revokeDescription")}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-access-revoke-reason">{t("approvals.queryAccess.revokeReason")}</Label>
            <Textarea
              id="query-access-revoke-reason"
              value={reason}
              onChange={(event) => { setReason(event.target.value); }}
              maxLength={4096}
              data-testid="query-access-revoke-reason"
            />
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="query-access-revoke-error">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setRevoking(null); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant="destructive"
              disabled={reason.trim() === ""}
              pending={revoke.isPending}
              onClick={() => void submit()}
              data-testid="query-access-revoke-confirm"
            >
              {t("approvals.queryAccess.revoke")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
