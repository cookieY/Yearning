import { StatusBadge } from "@/shared/components/status/status-badge";
import { QUERY_REQUEST_TONE, QUERY_SESSION_TONE, QUERY_GRANT_TONE } from "@/features/query/query-state-tone";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ShieldCheck, Terminal, Undo2 } from "lucide-react";
import type { QueryAccessRequest, QueryGrant, QuerySession } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { useMyGrants, useQueryOrders } from "@/features/query/use-query-domain";
import {
  WithdrawQueryAccessDialog,
  type WithdrawQueryAccessTarget,
} from "@/features/query/withdraw-query-access-dialog";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
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
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardTitle } from "@/shared/components/ui/card";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/shared/components/ui/sheet";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { TablePagination } from "@/shared/components/ui/table-pagination";

/**
 * Query Orders Tab (UI spec §5.2 — the unified view inside My Orders). Rows come
 * from whichever server objects exist for the current user: access requests
 * and grants when query approval produced them, session records otherwise.
 * The detail sheet shows the frozen datasource set, the request state, the
 * grant state and — when revoked — the revocation reason. Operator and
 * time of revocation are NOT in the frozen read contract; the gap is
 * recorded in the migration contract §17 as RCP-pending.
 *
 * §18.73 → RCP-20261021: the merged view moved server-side —
 * GET /query/orders unions access requests and sessions with a unified
 * (created_at, id) keyset cursor; the tab consumes one server page
 * (default 20/page) instead of merging two bounded fetches locally.
 */

type QueryOrderRow =
  | { kind: "request"; id: string; request: QueryAccessRequest; grant: QueryGrant | null }
  | { kind: "session"; id: string; session: QuerySession };

export function QueryOrdersTab() {
  const { t } = useTranslation();
  const session = useSession();
  const authenticated = session.status === "authenticated";
  // RCP-20261021: combined endpoint server-side pagination (both union and sorting happen in GET /query/orders).
  const pager = useCursorPage(20);
  const ordersQuery = useQueryOrders(authenticated, {
    limit: pager.pageSize,
    after: pager.after,
  });
  const grantsQuery = useMyGrants(authenticated);
  const [detail, setDetail] = useState<QueryOrderRow | null>(null);
  const [withdrawTarget, setWithdrawTarget] = useState<WithdrawQueryAccessTarget | null>(null);
  const [terms, setTerms] = useState<SearchTerm[]>([]);
  const [live, setLive] = useState<SearchTerm | null>(null);
  const currentUserId = session.user?.id;

  const entries = ordersQuery.data.items;
  const grants = grantsQuery.data ?? [];
  const grantById = new Map(grants.map((grant) => [grant.id, grant]));

  const rows: QueryOrderRow[] = entries.flatMap((entry): QueryOrderRow[] => {
    if (entry.kind === "access_request" && entry.request !== undefined) {
      const request = entry.request;
      return [{
        kind: "request" as const,
        id: entry.id,
        request,
        grant: request.grant_id === null || request.grant_id === undefined ? null : grantById.get(request.grant_id) ?? null,
      }];
    }
    if (entry.kind === "session" && entry.session !== undefined) {
      return [{ kind: "session" as const, id: entry.id, session: entry.session }];
    }
    return [];
  });

  const searchAccessors = useMemo<SearchAccessor<QueryOrderRow>[]>(() => [
    {
      key: "kind",
      label: t("orders.query.column.kind"),
      value: (row) =>
        row.kind === "request"
          ? `request access_request ${t("orders.query.kind.request")}`
          : `session ${t("orders.query.kind.session")}`,
      options: [
        { value: "request", label: t("orders.query.kind.request") },
        { value: "session", label: t("orders.query.kind.session") },
      ],
    },
    {
      key: "datasources",
      label: t("orders.query.column.datasources"),
      value: (row) =>
        row.kind === "request"
          ? [...(row.request.datasource_names ?? []), ...row.request.datasource_ids].join(" ")
          : row.session.capabilities
              .map((capability) => `${capability.datasource_name} ${capability.datasource_id}`)
              .join(" "),
    },
    {
      key: "state",
      label: t("orders.query.column.state"),
      value: (row) =>
        row.kind === "request"
          ? `${row.request.state} ${t(`query.request.state.${row.request.state}`)}`
          : `${row.session.state} ${t(`query.session.state.${row.session.state}`)}`,
      options: [
        { value: "access_pending", label: t("query.request.state.access_pending") },
        { value: "grant_active", label: t("query.request.state.grant_active") },
        { value: "rejected", label: t("query.request.state.rejected") },
        { value: "withdrawn", label: t("query.request.state.withdrawn") },
        { value: "active", label: t("query.session.state.active") },
        { value: "revoked", label: t("query.session.state.revoked") },
        { value: "expired", label: t("query.session.state.expired") },
        { value: "closed", label: t("query.session.state.closed") },
      ],
    },
    {
      key: "terminal",
      label: t("orders.query.column.terminal"),
      value: (row) =>
        row.kind === "request" && row.grant !== null
          ? `${row.grant.state} ${t(`query.grant.state.${row.grant.state}`)}`
          : "",
      options: (["active", "expired", "revoked"] as const).map((state) => ({
        value: state,
        label: t(`query.grant.state.${state}`),
      })),
    },
    {
      key: "created_at",
      label: t("orders.query.column.createdAt"),
      value: (row) =>
        formatTimestamp(row.kind === "request" ? row.request.created_at : row.session.created_at),
      input: "date",
    },
  ], [t]);

  const effectiveTerms = live === null ? terms : [...terms, live];
  const filteredRows = filterBySearchTerms(rows, effectiveTerms, searchAccessors);

  // Cursor pagination now keeps previous-page data; tween when row set changes and disable clicking stale rows while fetching.
  const ordersStale = ordersQuery.isFetching && ordersQuery.isPlaceholderData;
  const ordersBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(filteredRows.map((row) => row.id)),
  );

  const loadError = ordersQuery.error ?? grantsQuery.error;

  return (
    <Card className="overflow-visible">
      <ListToolbar busy={ordersStale} fill>
        <div className="flex min-w-0 shrink-0 flex-col gap-1">
          <CardTitle>{t("orders.query.title")}</CardTitle>
          <CardDescription>{t("orders.query.description")}</CardDescription>
        </div>
        <GlobalSearchBar
          fields={searchAccessors}
          terms={terms}
          onTermsChange={setTerms}
          onLiveChange={setLive}
          placeholder={t("orders.query.searchPlaceholder")}
          testId="orders-query-search"
          className="w-full min-w-0 flex-1"
        />
      </ListToolbar>
      <CardContent className="px-0!">
        {loadError !== null && (
          <ErrorState
            error={loadError}
            operationId="listQueryOrders"
            onRetry={() => {
              void ordersQuery.refetch();
              void grantsQuery.refetch();
            }}
          />
        )}
        {loadError === null && ordersQuery.isPending && (
          <LoadingState variant="table" columns={6} />
        )}
        {loadError === null && !ordersQuery.isPending && rows.length === 0 && (
          <EmptyState title={t("orders.query.empty")} testId="orders-query-empty" />
        )}
        {loadError === null && !ordersQuery.isPending && rows.length > 0 && filteredRows.length === 0 && (
          <EmptyState filtered title={t("table.emptySearch")} testId="orders-query-search-empty" />
        )}
        {loadError === null && !ordersQuery.isPending && filteredRows.length > 0 && (
          <Table data-testid="orders-query-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t("orders.query.column.kind")}</TableHead>
                <TableHead>{t("orders.query.column.datasources")}</TableHead>
                <TableHead>{t("orders.query.column.state")}</TableHead>
                <TableHead>{t("orders.query.column.createdAt")}</TableHead>
                <TableHead>{t("orders.query.column.terminal")}</TableHead>
                <TableHead className="w-24 text-right" />
              </TableRow>
            </TableHeader>
            <TableBody ref={ordersBodyRef} stale={ordersStale}>
              {filteredRows.map((row) => (
                <TableRow
                  key={row.id}
                  data-row-key={row.id}
                  className="cursor-pointer"
                  onClick={() => { setDetail(row); }}
                  data-testid={`orders-query-row-${row.id}`}
                >
                  <TableCell>
                    {row.kind === "request" ? (
                      <Badge variant="outline">{t("orders.query.kind.request")}</Badge>
                    ) : (
                      <Badge variant="outline">
                        <Terminal className="size-3" />
                        {t("orders.query.kind.session")}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="max-w-72 truncate font-mono font-semibold text-foreground">
                    {row.kind === "request"
                      ? formatRequestDatasources(row.request)
                      : row.session.capabilities.map((capability) => capability.datasource_name).join(" · ")}
                  </TableCell>
                  <TableCell>
                    <StatusBadge tone={row.kind === "request" ? QUERY_REQUEST_TONE[row.request.state] : QUERY_SESSION_TONE[row.session.state]}>
                      {row.kind === "request"
                        ? t(`query.request.state.${row.request.state}`)
                        : t(`query.session.state.${row.session.state}`)}
                    </StatusBadge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {formatTimestamp(row.kind === "request" ? row.request.created_at : row.session.created_at)}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {row.kind === "request" && row.grant !== null
                      ? <StatusBadge tone={QUERY_GRANT_TONE[row.grant.state]}>{t(`query.grant.state.${row.grant.state}`)}</StatusBadge>
                      : "—"}
                  </TableCell>
                  <TableCell className="text-right">
                    {row.kind === "request" &&
                      row.request.state === "access_pending" &&
                      currentUserId !== undefined &&
                      row.request.requester_user_id === currentUserId && (
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={(event) => {
                            event.stopPropagation();
                            setWithdrawTarget({
                              label: formatRequestDatasources(row.request),
                              request: row.request,
                            });
                          }}
                          data-testid={`orders-query-withdraw-${row.request.id}`}
                        >
                          <Undo2 aria-hidden />
                          {t("query.entry.withdraw")}
                        </Button>
                      )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {loadError === null && !ordersQuery.isPending && (
          <TablePagination
            busy={ordersStale}
            page={pager.pageNo}
            hasMore={ordersQuery.data.page.has_more}
            isFirst={pager.isFirst}
            onPrev={pager.goPrev}
          onJumpPage={pager.goToPage}
            onNext={() => { pager.pushCursor(ordersQuery.data.page.next_cursor); }}
            pageSize={pager.pageSize}
            onPageSizeChange={pager.setPageSize}
            rowsOnPage={ordersQuery.data.items.length}
            testIdPrefix="orders-query"
          />
        )}
      </CardContent>

      <Sheet open={detail !== null} onOpenChange={(open) => { if (!open) setDetail(null); }}>
        {/* Unified scrollbars (owner 2026-11-03): detail Sheet scrolling uses ScrollArea overlay thumb. */}
        <SheetContent className="w-[480px] sm:max-w-[480px]" data-testid="orders-query-detail">
          <ScrollArea className="min-h-0 flex-1">
            <div>
          {detail !== null && (
            <>
              <SheetHeader>
                <SheetTitle>{t("orders.query.detail.title")}</SheetTitle>
                <SheetDescription>{t("orders.query.detail.description")}</SheetDescription>
              </SheetHeader>
              <div className="flex flex-col gap-4 px-5 pr-6 pb-6">
                {detail.kind === "request" ? (
                  <>
                    <DetailRow label={t("orders.query.detail.state")}>
                      <Badge variant="secondary">{t(`query.request.state.${detail.request.state}`)}</Badge>
                    </DetailRow>
                    <DetailRow label={t("orders.query.detail.datasources")}>
                      <span className="font-mono text-xs break-all">{formatRequestDatasources(detail.request)}</span>
                    </DetailRow>
                    <DetailRow label={t("orders.query.detail.createdAt")}>
                      {formatTimestamp(detail.request.created_at)}
                    </DetailRow>
                    {detail.request.state === "access_pending" &&
                      currentUserId !== undefined &&
                      detail.request.requester_user_id === currentUserId && (
                        <div className="pt-2">
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => {
                              setWithdrawTarget({
                                label: formatRequestDatasources(detail.request),
                                request: detail.request,
                              });
                            }}
                            data-testid="orders-query-detail-withdraw"
                          >
                            <Undo2 aria-hidden />
                            {t("query.entry.withdraw")}
                          </Button>
                        </div>
                      )}
                    {detail.grant !== null && (
                      <>
                        <DetailRow label={t("orders.query.detail.grantState")}>
                          <Badge variant="secondary">{t(`query.grant.state.${detail.grant.state}`)}</Badge>
                        </DetailRow>
                        <DetailRow label={t("orders.query.detail.expiresAt")}>
                          {formatTimestamp(detail.grant.expires_at)}
                        </DetailRow>
                        {detail.grant.state === "revoked" && detail.grant.revoked_reason !== null && (
                          <div
                            className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-xs"
                            data-testid="orders-query-revoke-reason"
                          >
                            <div className="flex items-center gap-1 font-medium">
                              <ShieldCheck className="size-3.5" />
                              {t("orders.query.detail.revoked")}
                            </div>
                            <p className="text-muted-foreground mt-1">{detail.grant.revoked_reason}</p>
                            <p className="text-muted-foreground mt-1">{t("orders.query.detail.revokedMetaPending")}</p>
                          </div>
                        )}
                      </>
                    )}
                  </>
                ) : (
                  <>
                    <DetailRow label={t("orders.query.detail.state")}>
                      <Badge variant="secondary">{t(`query.session.state.${detail.session.state}`)}</Badge>
                    </DetailRow>
                    <DetailRow label={t("orders.query.detail.datasources")}>
                      <span className="font-mono text-xs break-all">
                        {detail.session.capabilities.map((capability) => capability.datasource_name).join(" · ")}
                      </span>
                    </DetailRow>
                    <DetailRow label={t("orders.query.detail.createdAt")}>
                      {formatTimestamp(detail.session.created_at)}
                    </DetailRow>
                  </>
                )}
              </div>
            </>
          )}
            </div>
          </ScrollArea>
        </SheetContent>
      </Sheet>
      <WithdrawQueryAccessDialog
        target={withdrawTarget}
        onClose={() => { setWithdrawTarget(null); }}
        onWithdrawn={(updated) => {
          setDetail((current) =>
            current !== null && current.kind === "request" && current.request.id === updated.id
              ? { ...current, request: updated }
              : current,
          );
        }}
      />
    </Card>
  );
}

function formatRequestDatasources(request: QueryAccessRequest): string {
  const labels =
    request.datasource_names !== undefined && request.datasource_names.length > 0
      ? request.datasource_names
      : request.datasource_ids;
  return labels.join(" · ");
}

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-muted-foreground text-xs">{label}</span>
      <div className="text-sm">{children}</div>
    </div>
  );
}
