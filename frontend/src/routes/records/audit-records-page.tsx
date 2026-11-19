import { StatusBadge } from "@/shared/components/status/status-badge";
import type { StatusTone } from "@/shared/lib/status-tone";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ScrollText } from "lucide-react";
import { listAuditEvents } from "@/api/generated/client/administration/administration";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { TableSearchInput } from "@/shared/components/table-search-input";
import type { AuditEvent } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
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
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { useState } from "react";

/**
 * Audit Records (route /records; migration contract §2 maps legacy
 * /comptroller/order/record here). The declared contract exposes the audit
 * list only to the builtin admin (audit PRD §4: accessible to admin only) with
 * cursor pagination and NO filter parameters — the legacy time/user/action
 * filters have no declared surface, which is recorded as an RCP-pending
 * contract gap in the migration contract §17 rather than invented here.
 * Non-admin sessions get the honest no-visible-audit-scope state; the read stays
 * append-only (90-day retention shown per row via expires_at).
 */

const OUTCOME_TONE: Record<string, StatusTone> = { succeeded: "emerald", denied: "amber", failed: "red" };

export default function AuditRecordsPage() {
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const [detail, setDetail] = useState<AuditEvent | null>(null);

  const pager = useCursorPage(20);
  const eventsQuery = useQuery({
    queryKey: ["admin", "audit-events", pager.after, pager.pageSize],
    queryFn: async () => {
      const response = (await listAuditEvents({
        limit: pager.pageSize,
        after: pager.after || undefined,
      })) as unknown as {
        items?: AuditEvent[];
        page?: { has_more?: boolean; next_cursor?: string | null };
      };
      return {
        items: response.items ?? [],
        hasMore: response.page?.has_more ?? false,
        nextCursor: response.page?.next_cursor ?? null,
      };
    },
    enabled: session.status === "authenticated" && isAdmin,
    retry: false,
    placeholderData: keepPreviousData,
  });
  // Client-side search over the loaded page: the contract declares no filter
  // parameters for the audit list (§17 RCP-pending gap), so the search only
  // narrows what the server already returned. Matches time, actor, action,
  // resource and outcome — the visible row fields.
  const [search, setSearch] = useState("");
  const keyword = search.trim().toLowerCase();
  const auditItems = (eventsQuery.data?.items ?? []).filter(
    (event) =>
      keyword === "" ||
      event.occurred_at.toLowerCase().includes(keyword) ||
      (event.actor_username_snapshot ?? "").toLowerCase().includes(keyword) ||
      event.action.toLowerCase().includes(keyword) ||
      event.event_type.toLowerCase().includes(keyword) ||
      event.resource_type.toLowerCase().includes(keyword) ||
      (event.resource_id ?? "").toLowerCase().includes(keyword) ||
      event.outcome.toLowerCase().includes(keyword),
  );
  // Search narrows rows within the loaded page; position changes after narrowing should also be tweened.
  const auditBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(auditItems.map((event) => event.id)),
  );
  const auditNext = eventsQuery.data?.nextCursor ?? null;
  const auditHasMore = eventsQuery.data?.hasMore ?? false;

  return (
    <PageLayout data-testid="records-page">
      <PageHeader
        icon={ScrollText}
        title={t("records.title")}
        description={t("records.description")}
      />

      {!isAdmin && (
        <div className="rounded-xl border">
          <EmptyState
            title={t("records.noScopeTitle")}
            description={t("records.noScopeDescription")}
            testId="records-no-scope"
          />
        </div>
      )}

      {isAdmin && (
        <Card>
          <ListToolbar
            busy={eventsQuery.isFetching && eventsQuery.isPlaceholderData}
            search={<TableSearchInput value={search} onChange={setSearch} testId="records-search" />}
          >
            <div className="flex min-w-0 flex-col gap-1">
              <CardTitle className="flex items-center gap-2">
                <ScrollText aria-hidden />
                {t("records.card")}
              </CardTitle>
              <CardDescription>{t("records.cardDescription")}</CardDescription>
            </div>
          </ListToolbar>
          <CardContent className="px-0!">
            {eventsQuery.isPending && <LoadingState variant="table" columns={5} />}
            {eventsQuery.error !== null && (
              <div className="p-5">
                <ErrorState
                  error={eventsQuery.error}
                  operationId="listAuditEvents"
                  onRetry={() => void eventsQuery.refetch()}
                />
              </div>
            )}
            {!eventsQuery.isPending && eventsQuery.error === null && auditItems.length === 0 && (
              <EmptyState
                filtered={keyword !== ""}
                title={keyword !== "" ? t("table.emptySearch") : t("records.empty")}
                testId="records-empty"
              />
            )}
            {!eventsQuery.isPending && eventsQuery.error === null && auditItems.length > 0 && (
              <>
                <Table data-testid="records-table">
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("records.column.time")}</TableHead>
                      <TableHead>{t("records.column.actor")}</TableHead>
                      <TableHead>{t("records.column.action")}</TableHead>
                      <TableHead>{t("records.column.resource")}</TableHead>
                      <TableHead>{t("records.column.outcome")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody ref={auditBodyRef} stale={eventsQuery.isFetching && eventsQuery.isPlaceholderData}>
                    {auditItems.map((event) => (
                      <TableRow
                        key={event.id}
                        data-row-key={event.id}
                        className="cursor-pointer"
                        onClick={() => { setDetail(event); }}
                        data-testid={`records-row-${event.id}`}
                      >
                        <TableCell className="text-muted-foreground whitespace-nowrap tabular-nums">
                          {formatTimestamp(event.occurred_at)}
                        </TableCell>
                        <TableCell className="font-semibold text-foreground">
                          {event.actor_username_snapshot ?? event.actor_kind}
                        </TableCell>
                        <TableCell className="font-mono">{event.action}</TableCell>
                        <TableCell className="font-mono">
                          {event.resource_type}
                          {event.resource_id == null ? "" : ` \u00b7 ${event.resource_id.slice(0, 8)}`}
                        </TableCell>
                        <TableCell>
                          <StatusBadge tone={OUTCOME_TONE[event.outcome] ?? "stone"}>
                            {t(`records.outcome.${event.outcome}`)}
                          </StatusBadge>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
                <TablePagination
                  busy={eventsQuery.isFetching && eventsQuery.isPlaceholderData}
                  page={pager.pageNo}
                  hasMore={auditHasMore}
                  isFirst={pager.pageNo === 1}
                  onPrev={() => {
                    pager.goPrev();
                  }}
                  onJumpPage={pager.goToPage}
                  onNext={() => {
                    pager.pushCursor(auditNext);
                  }}
                  pageSize={pager.pageSize}
                  onPageSizeChange={pager.setPageSize}
                  rowsOnPage={auditItems.length}
                  testIdPrefix="audit"
                />
              </>
            )}
          </CardContent>
        </Card>
      )}

      <Sheet open={detail !== null} onOpenChange={(open) => { if (!open) setDetail(null); }}>
        {/* Unified scrollbars (owner 2026-11-03): both the detail Sheet and metadata snippet scrolling use
            ScrollArea overlay thumb (previously native overflow bars). */}
        <SheetContent className="w-[480px] sm:max-w-[480px]" data-testid="records-detail">
          <ScrollArea className="min-h-0 flex-1">
            <div>
          {detail !== null && (
            <>
              <SheetHeader>
                <SheetTitle className="font-mono text-base">{detail.action}</SheetTitle>
                <SheetDescription>{formatTimestamp(detail.occurred_at)}</SheetDescription>
              </SheetHeader>
              <div className="flex flex-col gap-3 px-5 pr-6 pb-6 text-sm">
                <Row label={t("records.detail.eventType")} value={detail.event_type} />
                <Row label={t("records.detail.actor")} value={detail.actor_username_snapshot ?? detail.actor_kind} />
                <Row label={t("records.detail.resource")} value={`${detail.resource_type}${detail.resource_id === null ? "" : ` · ${String(detail.resource_id)}`}`} />
                <Row label={t("records.detail.outcome")} value={t(`records.outcome.${detail.outcome}`)} />
                <Row label={t("records.detail.requestId")} value={detail.request_id ?? "—"} />
                <Row label={t("records.detail.expiresAt")} value={formatTimestamp(detail.expires_at)} />
                {/* Structured metadata; secret changes only ever show "changed"
                    server-side (audit PRD §5). */}
                <div className="flex flex-col gap-1">
                  <span className="text-muted-foreground text-xs">{t("records.detail.metadata")}</span>
                  <ScrollArea className="bg-muted/50 rounded-md">
                    <pre className="p-2 text-xs whitespace-pre">
                      {JSON.stringify(detail.metadata ?? {}, null, 2)}
                    </pre>
                  </ScrollArea>
                </div>
              </div>
            </>
          )}
            </div>
          </ScrollArea>
        </SheetContent>
      </Sheet>
    </PageLayout>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="font-mono text-xs break-all">{value}</span>
    </div>
  );
}
