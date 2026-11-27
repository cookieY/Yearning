import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, Eye, Stamp, X } from "lucide-react";
import type { QueryAccessRequest } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ListToolbar } from "@/shared/components/page-layout";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardTitle } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Sheet, SheetContent } from "@/shared/components/ui/sheet";
import { Textarea } from "@/shared/components/ui/textarea";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import {
  useDecideAccessRequest,
  useMyAccessRequests,
  useMyGrants,
} from "@/features/query/use-query-domain";
import {
  DecisionRequestSummary,
  GrantRevocationCard,
  QueryAccessDetailBody,
  RequestStatusBadge,
  consolidateActiveGrantEntries,
  formatRequestDatasources,
  normalizeReason,
} from "./query-access-sections";

/**
 * Query Access Approval (route /approvals/query-access; migration contract §2 maps
 * legacy /server/query/list here — "approve datasource access eligibility, never SQL", Q001).
 * The page queries listQueryAccessRequests with scope="reviewer" so only frozen-reviewer
 * rows enter the queue, and gates Approve/Reject strictly on server-computed can_decide
 * (active-step frozen approver eligibility) without client-side guessing. Decisions approve
 * DATASOURCE ACCESS eligibility (never SQL); final approval creates the grant server-side (Q003).
 *
 * Active grants render in GrantRevocationCard only when server-computed can_revoke is true
 * (frozen reviewer or builtin admin, Q004) — revocation terminates access, never reads it.
 */

export function ApprovalTabBadge({ count, testId }: { count: number; testId: string }) {
  if (count <= 0) return null;
  return (
    <span
      data-testid={testId}
      className="bg-destructive text-primary-foreground -mt-2 -ml-0.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] leading-none font-semibold tabular-nums"
    >
      {count > 99 ? "99+" : String(count)}
    </span>
  );
}

export function QueryAccessApprovalsContent() {
  const { t } = useTranslation();
  const session = useSession();
  const authenticated = session.status === "authenticated";
  // RCP-20261021: pending view uses server-side state filtering + backend cursor pagination
  // (previously client-side filtering silently hid pending items when total rows exceeded limit).
  const pager = useCursorPage(20);
  const requestsQuery = useMyAccessRequests(authenticated, {
    limit: pager.pageSize,
    after: pager.after,
    scope: "reviewer",
  });
  const grantsQuery = useMyGrants(authenticated);
  // §18.82: capture before the exhaustive conditional so narrowing does not leak into pagination guards.
  const requestsLoaded = !requestsQuery.isPending && !requestsQuery.isError;
  const decide = useDecideAccessRequest();

  const [inspecting, setInspecting] = useState<QueryAccessRequest | null>(null);
  const [deciding, setDeciding] = useState<QueryAccessRequest | null>(null);
  const [decision, setDecision] = useState<"approve" | "reject">("approve");
  const [comment, setComment] = useState("");
  const [errorText, setErrorText] = useState<string | null>(null);

  const openDecisionDialog = (row: QueryAccessRequest, nextDecision: "approve" | "reject") => {
    setInspecting(null);
    setErrorText(null);
    setComment("");
    setDecision(nextDecision);
    setDeciding(row);
  };

  const pending = requestsQuery.data.items;
  const grants = useMemo(() => grantsQuery.data ?? [], [grantsQuery.data]);
  const grantById = useMemo(() => new Map(grants.map((grant) => [grant.id, grant])), [grants]);
  const activeGrantEntries = useMemo(
    () => consolidateActiveGrantEntries(grants, pending),
    [grants, pending],
  );
  const anyLoading = requestsQuery.isPending || grantsQuery.isPending;
  const [pendingTerms, setPendingTerms] = useState<SearchTerm[]>([]);
  const [pendingLive, setPendingLive] = useState<SearchTerm | null>(null);
  const pendingSearchAccessors = useMemo<SearchAccessor<QueryAccessRequest>[]>(
    () => [
      {
        key: "requester",
        label: t("approvals.queryAccess.column.requester"),
        value: (row) => `${row.applicant_name ?? ""} ${row.requester_user_id}`,
      },
      {
        key: "datasources",
        label: t("approvals.queryAccess.column.datasources"),
        value: (row) => [...(row.datasource_names ?? []), ...row.datasource_ids].join(" "),
      },
      {
        key: "reason",
        label: t("approvals.queryAccess.column.reason"),
        value: (row) => row.reason ?? "",
      },
      {
        key: "state",
        label: t("approvals.queryAccess.column.state"),
        value: (row) => {
          const grant = row.grant_id == null ? undefined : grantById.get(row.grant_id);
          const showGrantTerminal =
            row.state === "grant_active" && grant !== undefined && grant.state !== "active";
          return showGrantTerminal
            ? `${grant.state} ${t(`query.grant.state.${grant.state}`)}`
            : `${row.state} ${t(`query.request.state.${row.state}`)}`;
        },
        options: [
          { value: "access_pending", label: t("query.request.state.access_pending") },
          { value: "grant_active", label: t("query.request.state.grant_active") },
          { value: "rejected", label: t("query.request.state.rejected") },
          { value: "expired", label: t("query.grant.state.expired") },
          { value: "revoked", label: t("query.grant.state.revoked") },
        ],
      },
      {
        key: "submitted_at",
        label: t("approvals.queryAccess.column.submittedAt"),
        value: (row) => formatTimestamp(row.created_at),
        input: "date",
      },
    ],
    [t, grantById],
  );
  const effectivePendingTerms =
    pendingLive === null ? pendingTerms : [...pendingTerms, pendingLive];
  const filteredPending = filterBySearchTerms(
    pending,
    effectivePendingTerms,
    pendingSearchAccessors,
  );
  // Decisions remove a row from this list, so every row below it shifts up.
  // The ids give the body something to follow; the plain table is the visual
  // result of a decision, not a decorative entrance.
  // Page-scoped and unambiguous: ids are UUIDs today, but `join("|")` would
  // silently collide if a future id embeds the separator. Paging replaces the
  // whole set, so nothing is shared across pages — no reorder is expected
  // there and none is animated.
  const pendingBodyRef = useFlipReorder<HTMLTableSectionElement>(
    `p${String(pager.pageNo)}:${JSON.stringify(filteredPending.map((row) => row.id))}`
  );

  const submitDecision = async () => {
    if (deciding === null) return;
    setErrorText(null);
    try {
      await decide.mutateAsync({
        requestId: deciding.id,
        version: deciding.version,
        body: { decision, comment: comment.trim() === "" ? undefined : comment },
      });
      appToast.success(
        decision === "approve"
          ? t("approvals.queryAccess.approve")
          : t("approvals.queryAccess.reject"),
      );
      setDeciding(null);
      setComment("");
    } catch (error) {
      const errText = describeErrorText(describeError(error, "decideQueryAccess"));
      setErrorText(errText);
      appToast.error(errText);
    }
  };

  const loadError = requestsQuery.error ?? grantsQuery.error;

  return (
    <div className="flex flex-col gap-5">
      {loadError !== null && (
        <ErrorState
          error={loadError}
          operationId="listQueryAccessRequests"
          onRetry={() => {
            void requestsQuery.refetch();
            void grantsQuery.refetch();
          }}
        />
      )}
      {loadError === null && anyLoading && (
        <Card>
          <LoadingState variant="table" columns={6} />
        </Card>
      )}

      {loadError === null && !anyLoading && (
        <>
          <Card className="overflow-visible">
            <ListToolbar
              busy={requestsQuery.isFetching && requestsQuery.isPlaceholderData}
              fill
            >
              <CardTitle className="flex shrink-0 items-center gap-2">
                <Stamp aria-hidden />
                {t("approvals.queryAccess.pendingCard")}
              </CardTitle>
              <GlobalSearchBar
                fields={pendingSearchAccessors}
                terms={pendingTerms}
                onTermsChange={setPendingTerms}
                onLiveChange={setPendingLive}
                placeholder={t("approvals.queryAccess.searchPlaceholder")}
                testId="query-access-search"
                className="w-full min-w-0 flex-1"
              />
            </ListToolbar>
            <CardContent className="px-0!">
              {filteredPending.length === 0 && effectivePendingTerms.length > 0 ? (
                <EmptyState
                  filtered
                  title={t("table.emptySearch")}
                  testId="query-access-search-empty"
                />
              ) : pending.length === 0 ? (
                <EmptyState
                  title={t("approvals.queryAccess.pendingEmpty")}
                  testId="query-access-pending-empty"
                />
              ) : (
                <Table data-testid="query-access-pending-table">
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("approvals.queryAccess.column.requester")}</TableHead>
                      <TableHead>{t("approvals.queryAccess.column.datasources")}</TableHead>
                      <TableHead>{t("approvals.queryAccess.column.reason")}</TableHead>
                      <TableHead>{t("approvals.queryAccess.column.state")}</TableHead>
                      <TableHead>{t("approvals.queryAccess.column.submittedAt")}</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody ref={pendingBodyRef} stale={requestsQuery.isFetching && requestsQuery.isPlaceholderData}>
                    {filteredPending.map((row) => {
                      const grant = row.grant_id == null ? undefined : grantById.get(row.grant_id);
                      const reasonText = normalizeReason(row.reason);
                      return (
                        <TableRow
                          key={row.id}
                          data-row-key={row.id}
                          className="cursor-pointer"
                          onClick={() => {
                            setInspecting(row);
                          }}
                          data-testid={`query-access-pending-${row.id}`}
                        >
                          <TableCell className="font-mono font-semibold text-foreground">{row.applicant_name ?? row.requester_user_id}</TableCell>
                          <TableCell className="max-w-72 truncate font-mono">
                            {formatRequestDatasources(row)}
                          </TableCell>
                          <TableCell
                            className="max-w-72 truncate text-muted-foreground"
                            title={reasonText ?? undefined}
                            data-testid={`query-access-reason-${row.id}`}
                          >
                            {reasonText ?? "—"}
                          </TableCell>
                          <TableCell>
                            <RequestStatusBadge row={row} grant={grant} />
                          </TableCell>
                          <TableCell className="text-muted-foreground">
                            {formatTimestamp(row.created_at)}
                          </TableCell>
                          <TableCell className="text-right">
                            <div
                              className="flex items-center justify-end gap-1"
                              onClick={(event) => {
                                event.stopPropagation();
                              }}
                            >
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                className="text-muted-foreground"
                                aria-label={t("approvals.queryAccess.viewDetails")}
                                onClick={() => {
                                  setInspecting(row);
                                }}
                                data-testid={`query-access-detail-${row.id}`}
                              >
                                <Eye aria-hidden />
                              </Button>
                              {row.state === "access_pending" && row.can_decide === true && (
                                <TableRowActions testId={`query-access-actions-${row.id}`}>
                                  <DropdownMenuItem
                                    onClick={() => {
                                      openDecisionDialog(row, "approve");
                                    }}
                                    data-testid={`query-access-approve-${row.id}`}
                                  >
                                    <Check /> {t("approvals.queryAccess.approve")}
                                  </DropdownMenuItem>
                                  <DropdownMenuItem
                                    variant="destructive"
                                    onClick={() => {
                                      openDecisionDialog(row, "reject");
                                    }}
                                    data-testid={`query-access-reject-${row.id}`}
                                  >
                                    <X /> {t("approvals.queryAccess.reject")}
                                  </DropdownMenuItem>
                                </TableRowActions>
                              )}
                            </div>
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              )}
              {requestsLoaded ? (
                <TablePagination
                  busy={requestsQuery.isFetching && requestsQuery.isPlaceholderData}
                  page={pager.pageNo}
                  hasMore={requestsQuery.data.page.has_more}
                  isFirst={pager.isFirst}
                  onPrev={pager.goPrev}
          onJumpPage={pager.goToPage}
                  onNext={() => { pager.pushCursor(requestsQuery.data.page.next_cursor); }}
                  pageSize={pager.pageSize}
                  onPageSizeChange={pager.setPageSize}
                  rowsOnPage={filteredPending.length}
                  testIdPrefix="query-access-approvals"
                />
              ) : null}
            </CardContent>
          </Card>

          <GrantRevocationCard entries={activeGrantEntries} />
          <p className="text-muted-foreground text-xs">{t("approvals.queryAccess.description")}</p>
        </>
      )}

      <Sheet open={inspecting !== null} onOpenChange={(open) => { if (!open) setInspecting(null); }}>
        <SheetContent className="w-[480px] sm:max-w-[480px]" data-testid="query-access-detail-sheet">
          <ScrollArea className="min-h-0 flex-1">
            {inspecting !== null && (
              <QueryAccessDetailBody
                row={inspecting}
                grant={inspecting.grant_id == null ? undefined : grantById.get(inspecting.grant_id)}
                onDecide={(nextDecision) => {
                  openDecisionDialog(inspecting, nextDecision);
                }}
              />
            )}
          </ScrollArea>
        </SheetContent>
      </Sheet>

      <Dialog open={deciding !== null} onOpenChange={(open) => { if (!open) setDeciding(null); }}>
        <DialogContent data-testid="query-access-decision-dialog">
          <DialogHeader>
            <DialogTitle>
              {decision === "approve"
                ? t("approvals.queryAccess.approveTitle")
                : t("approvals.queryAccess.rejectTitle")}
            </DialogTitle>
            <DialogDescription>
              {decision === "approve"
                ? t("approvals.queryAccess.approveDescription")
                : t("approvals.queryAccess.rejectDescription")}
            </DialogDescription>
          </DialogHeader>
          {deciding !== null && <DecisionRequestSummary row={deciding} />}
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-access-comment">{t("approvals.queryAccess.comment")}</Label>
            <Textarea
              id="query-access-comment"
              value={comment}
              onChange={(event) => { setComment(event.target.value); }}
              maxLength={4096}
              data-testid="query-access-comment"
            />
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="query-access-decision-error">
              <AlertTitle>{t("approvals.queryAccess.decisionFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setDeciding(null); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant={decision === "reject" ? "destructive" : "default"}
              pending={decide.isPending}
              onClick={() => void submitDecision()}
              data-testid="query-access-decision-confirm"
            >
              {decision === "approve"
                ? t("approvals.queryAccess.approve")
                : t("approvals.queryAccess.reject")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
