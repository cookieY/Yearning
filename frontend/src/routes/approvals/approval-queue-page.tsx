import { useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import type { ChangeOrder } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useApprovalQueue } from "@/features/orders/use-approvals";
import { frozenExecutorStageFor, myApprovalStepFor, ORDER_STATES } from "@/features/orders/order-state";
import { OrderStateBadge } from "@/features/orders/order-state-badge";
import { useSession } from "@/features/auth/session-provider";
import { useMyAccessRequests } from "@/features/query/use-query-domain";
import { ApprovalTabBadge, QueryAccessApprovalsContent } from "@/routes/approvals/query-access-page";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { Button } from "@/shared/components/ui/button";
import {
  GlobalSearchBar,
  filterBySearchTerms,
  type SearchAccessor,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { Card, CardContent } from "@/shared/components/ui/card";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { ArrowRight, Inbox, Stamp } from "lucide-react";

/**
 * Approval Workspace queue (route /approvals/changes; migration contract §2 maps
 * the legacy /server/order/audit/list here). §18.58 owner ruling: the queue
 * keeps EVERY order where the current user is a frozen approval actor —
 * decided steps stay listed with their outcome (Order Status / Submitter columns); the
 * decision itself still happens on the order detail page. §18.60: my-step
 * column narrows to Reviewed by Me — Yes only when my frozen step carries my own
 * decision (approved/rejected), No otherwise.
 * Opening or operating this page never creates a Review Run — the reviewer
 * consumes the frozen submission review (R003, gate: opening/operating the approval page never creates
 * a Review Run).
 */

import { formatTimestamp } from "@/shared/lib/timezones";

function QueueRow({ order, userId }: { order: ChangeOrder; userId: string | undefined }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const mine = myApprovalStepFor(order, userId);
  // §18.60: reviewed = my frozen step carries my own decision (approved/rejected);
  // pending turn / active / skipped / invalid do not count as "reviewed by me".
  const iHaveReviewed = mine !== null && (mine.stepState === "approved" || mine.stepState === "rejected");
  // Keyboard/AT users open rows through the explicit Open Detail button (a
  // real button element); the row-level onClick is a pointer convenience
  // only, mirroring the mine-page rows.
  return (
    <TableRow
      className="cursor-pointer"
      onClick={() => { void navigate(`/approvals/orders/${order.id}`); }}
      data-testid="approval-queue-row"
      data-row-key={order.id}
    >
      <TableCell className="font-semibold text-foreground">{order.display_number}</TableCell>
      <TableCell className="max-w-56 truncate text-foreground">{order.title}</TableCell>
      <TableCell className="font-mono">
        {mine === null ? "—" : mine.datasourceName}
      </TableCell>
      <TableCell>
        {/* owner 2026-11-04: unify state badges — the queue itself is the baseline style, switching to
            OrderStateBadge so all order surfaces share a single definition. */}
        <OrderStateBadge
          state={order.state}
          data-testid={`approval-row-state-${order.id}`}
        />
      </TableCell>
      <TableCell>
        {order.submitter_display_name ?? order.submitter_username ?? "—"}
      </TableCell>
      <TableCell>
        {mine === null ? (
          "—"
        ) : (
          <span data-testid={`approval-reviewed-${order.id}`}>
            {iHaveReviewed ? t("approvals.queue.reviewedYes") : t("approvals.queue.reviewedNo")}
          </span>
        )}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {formatTimestamp(order.submitted_at)}
      </TableCell>
      <TableCell className="text-right">
        {/* Reference ruling 2026-10-24: quiet right-aligned affordance; the
            row itself navigates and this echoes it. */}
        <Button
          variant="ghost"
          size="icon-sm"
          className="text-muted-foreground"
          aria-label={t("approvals.queue.open")}
          onClick={(event) => {
            event.stopPropagation();
            void navigate(`/approvals/orders/${order.id}`);
          }}
          data-testid={`open-approval-${order.display_number}`}
        >
          <ArrowRight aria-hidden />
        </Button>
      </TableCell>
    </TableRow>
  );
}

function usePendingApprovalTabCounts() {
  const session = useSession();
  const authenticated = session.status === "authenticated";
  const userId = session.user?.id;
  const ordersQuery = useApprovalQueue(authenticated);
  const requestsQuery = useMyAccessRequests(authenticated, {
    limit: 50,
    after: "",
    state: "access_pending",
    scope: "reviewer",
  });

  const pendingOrdersCount = useMemo(() => {
    if (userId === undefined) return 0;
    return (ordersQuery.data ?? []).filter((order) => {
      const mine = myApprovalStepFor(order, userId);
      return (mine !== null && mine.stepState === "active") || frozenExecutorStageFor(order, userId) !== null;
    }).length;
  }, [ordersQuery.data, userId]);

  const pendingQueryAccessCount = useMemo(
    () =>
      requestsQuery.data.items.filter(
        (item) => item.state === "access_pending" && item.can_decide === true,
      ).length,
    [requestsQuery.data.items],
  );

  return { pendingOrdersCount, pendingQueryAccessCount };
}

export function ApprovalsWorkspacePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { pendingOrdersCount, pendingQueryAccessCount } = usePendingApprovalTabCounts();
  const routeTab: "orders" | "query-access" = location.pathname.endsWith("/query-access")
    ? "query-access"
    : "orders";
  const [prevRouteTab, setPrevRouteTab] = useState<"orders" | "query-access">(routeTab);
  const [activeTab, setActiveTab] = useState<"orders" | "query-access">(routeTab);
  if (prevRouteTab !== routeTab) {
    setPrevRouteTab(routeTab);
    setActiveTab(routeTab);
  }
  const isQueryAccess = activeTab === "query-access";
  const selectTab = (next: "orders" | "query-access"): void => {
    setActiveTab(next);
    const targetPath = next === "query-access" ? "/approvals/query-access" : "/approvals/changes";
    if (location.pathname !== targetPath) {
      void navigate(targetPath);
    }
  };

  return (
    <PageLayout data-testid={isQueryAccess ? "query-access-approvals" : "approval-queue-page"}>
      <PageHeader
        icon={isQueryAccess ? Stamp : Inbox}
        title={isQueryAccess ? t("approvals.queryAccess.title") : t("approvals.queue.title")}
      />
      <Tabs
        value={activeTab}
        onValueChange={(next) => {
          if (next === "orders" || next === "query-access") {
            selectTab(next as "orders" | "query-access");
          }
        }}
      >
        <TabsList className="flex w-full justify-start overflow-x-auto overflow-y-hidden">
          <TabsTrigger
            value="orders"
            onClick={() => {
              selectTab("orders");
            }}
            data-testid="approvals-tab-orders"
          >
            {t("approvals.queryAccess.ordersTab")}
            <ApprovalTabBadge count={pendingOrdersCount} testId="approvals-tab-orders-badge" />
          </TabsTrigger>
          <TabsTrigger
            value="query-access"
            onClick={() => {
              selectTab("query-access");
            }}
            data-testid="approvals-tab-query"
          >
            {t("approvals.queryAccess.queryTab")}
            <ApprovalTabBadge count={pendingQueryAccessCount} testId="approvals-tab-query-badge" />
          </TabsTrigger>
        </TabsList>
      </Tabs>
      <TransitionPanel activeIndex={isQueryAccess ? 1 : 0} keepMounted>
        <ApprovalQueueContent />
        <QueryAccessApprovalsContent />
      </TransitionPanel>
    </PageLayout>
  );
}

function ApprovalQueueContent() {
  const { t } = useTranslation();
  const session = useSession();
  const queueQuery = useApprovalQueue(true);
  // ECS-style global search (owner feedback 2026-11-04): one left-aligned box
  // covering order number / title / order status / submitter / datasource — free text matches any field,
  // field-scoped chips narrow one field; the uncommitted input text filters
  // live and Enter commits it as a chip. Filtering stays client-side.
  const [terms, setTerms] = useState<SearchTerm[]>([]);
  const [live, setLive] = useState<SearchTerm | null>(null);
  const [pageNo, setPageNo] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const searchAccessors = useMemo<SearchAccessor<ChangeOrder>[]>(
    () => [
      {
        key: "number",
        label: t("approvals.queue.column.number"),
        value: (order) => order.display_number,
      },
      {
        key: "title",
        label: t("approvals.queue.column.title"),
        value: (order) => order.title ?? "",
      },
      {
        key: "state",
        label: t("approvals.queue.column.state"),
        // Raw enum + localized label so either spelling matches a chip.
        value: (order) => `${order.state} ${t(`orders.state.${order.state}`)}`,
        options: ORDER_STATES.map((state) => ({ value: state, label: t(`orders.state.${state}`) })),
      },
      {
        key: "submitter",
        label: t("approvals.queue.column.submitter"),
        value: (order) => order.submitter_display_name ?? order.submitter_username ?? "",
      },
      {
        key: "datasource",
        label: t("approvals.queue.column.datasource"),
        value: (order) => order.stages.map((stage) => stage.datasource_name).join(" "),
      },
      {
        key: "submitted_at",
        label: t("approvals.queue.column.submittedAt"),
        value: (order) => formatTimestamp(order.submitted_at),
        input: "date",
      },
    ],
    [t],
  );
  const effectiveTerms = live === null ? terms : [...terms, live];
  // The queue matches on order number, title, state, submitter and stage
  // datasource names — the fields an approver uses to locate the order to
  // work on.
  const queue = filterBySearchTerms(queueQuery.data ?? [], effectiveTerms, searchAccessors);
  // The queue arrives as one live 200-row package (event-fed, migration
  // contract §14.3), so paging is client-side slicing — same pattern as the
  // query result grid. A fresh search restarts the walk at page 1.
  const startRow = (pageNo - 1) * pageSize;
  const visibleQueue = queue.slice(startRow, startRow + pageSize);
  const hasMore = startRow + visibleQueue.length < queue.length;
  // Search is client-side filtering, and peer decisions remove rows from the queue in real time — both change the row set,
  // so use FLIP to tween position changes of surviving rows (same pattern as the pending table in query-access).
  const queueBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(visibleQueue.map((order) => order.id)),
  );

  return (
    <div className="flex flex-col gap-5">
      {/* overflow-visible (review R2 H1): keep the search bar's suggestion
          popup unclipped when the queue is empty or has a single row. */}
        <Card className="overflow-visible">
          <ListToolbar
            busy={queueQuery.isFetching && queueQuery.isPlaceholderData}
            fill
          >
            {/* Owner feedback 2026-11-04: the search box sits on the LEFT of the
                toolbar (Aliyun ECS console style) instead of a right-edge
                column filter. Owner feedback 2026-11-05: it fills the toolbar —
                no narrow max-width. */}
            <GlobalSearchBar
              fields={searchAccessors}
              terms={terms}
              onTermsChange={(next) => {
                setTerms(next);
                setPageNo(1);
              }}
              onLiveChange={(next) => {
                setLive(next);
                setPageNo(1);
              }}
              placeholder={t("approvals.queue.searchPlaceholder")}
              testId="approval-queue-search"
              className="w-full min-w-0 flex-1"
            />
          </ListToolbar>
          <CardContent className="px-0!">
            {queueQuery.isPending ? (
              <LoadingState variant="table" columns={8} />
            ) : queueQuery.isError ? (
              <ErrorState
                error={queueQuery.error}
                operationId="listChangeOrders"
                onRetry={() => void queueQuery.refetch()}
              />
            ) : queue.length === 0 && effectiveTerms.length > 0 ? (
              <EmptyState filtered title={t("table.emptySearch")} testId="approval-queue-search-empty" />
            ) : queueQuery.data.length === 0 ? (
              <EmptyState title={t("approvals.queue.empty")} testId="approval-queue-empty" />
            ) : (
              <>
                <Table data-testid="approval-queue-table">
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("approvals.queue.column.number")}</TableHead>
                      <TableHead>{t("approvals.queue.column.title")}</TableHead>
                      <TableHead>{t("approvals.queue.column.datasource")}</TableHead>
                      <TableHead>{t("approvals.queue.column.state")}</TableHead>
                      <TableHead>{t("approvals.queue.column.submitter")}</TableHead>
                      <TableHead>{t("approvals.queue.column.reviewed")}</TableHead>
                      <TableHead>{t("approvals.queue.column.submittedAt")}</TableHead>
                      <TableHead />
                    </TableRow>
                  </TableHeader>
                  <TableBody ref={queueBodyRef}>
                    {visibleQueue.map((order) => (
                      <QueueRow key={order.id} order={order} userId={session.user?.id} />
                    ))}
                  </TableBody>
                </Table>
                <TablePagination
                  busy={queueQuery.isFetching && queueQuery.isPlaceholderData}
                  page={pageNo}
                  hasMore={hasMore}
                  isFirst={pageNo <= 1}
                  onPrev={() => { setPageNo((current) => Math.max(1, current - 1)); }}
                  onNext={() => { setPageNo((current) => current + 1); }}
                  onJumpPage={(target) => { setPageNo(target); }}
                  pageSize={pageSize}
                  onPageSizeChange={(size) => { setPageSize(size); setPageNo(1); }}
                  rowsOnPage={visibleQueue.length}
                  testIdPrefix="approval-queue-footer"
                />
              </>
            )}
          </CardContent>
        </Card>
        <p className="text-muted-foreground text-xs">{t("approvals.queue.frozenNote")}</p>
    </div>
  );
}
