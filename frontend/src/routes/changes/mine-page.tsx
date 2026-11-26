import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { useTranslation } from "react-i18next";
import type {
  ChangeDraft,
  ChangeOrder,
  ChangeOrderState,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useMyChangeOrders,
  useMyDrafts,
  useOrderDatasourceOptions,
  type OrderListFilters,
  type OrderListScope,
} from "@/features/orders/use-orders";
import { OrderStateBadge } from "@/features/orders/order-state-badge";
import { QueryOrdersTab } from "@/routes/changes/query-orders-tab";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardTitle } from "@/shared/components/ui/card";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import {
  GlobalSearchBar,
  type SearchFieldDef,
  type SearchTerm,
} from "@/shared/components/global-search-bar";
import { ORDER_STATES } from "@/features/orders/order-state";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TAB_PANEL_VARIANTS, TransitionPanel } from "@/shared/components/ui/transition-panel";
import { ArrowRight, FileStack } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { deleteChangeDraft } from "@/api/generated/client/change-drafts/change-drafts";
import { describeError } from "@/shared/api/error-display";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { DeleteDraftDialog, MineDraftsPopover } from "./mine-drafts-popover";

/**
 * My Orders (route /changes/mine; migration contract §2: drafts and submitted orders are separated;
 * UI spec §5.2 double-tab shell). The audit tab splits personal drafts from
 * submitted orders; the query tab is the unified query-order view the query
 * domain (F10) delivers — it stays visibly reserved, not silently missing.
 * Legacy list continuity (orderTable.vue): work_id->order number, remark->title,
 * date->submitted time, status->state badge; type/delay/real_name columns have no v4
 * counterpart and are dropped per the field-mapping contract.
 *
 * Filtering (FE-F6-ORDER-FILTER, RCP-20260831-ORDER-LIST-FILTER): state /
 * keyword / datasource / submitted-date range are server-side query params —
 * the toolbar composes them per the §7.1 list baseline (toolbar search
 * input); the keyword is debounced so each keystroke is not a request.
 */

const ALL_STATES = "all" as const;
const ALL_DATASOURCES = "all" as const;
const KEYWORD_DEBOUNCE_MS = 300;

function OrderRow({ order, detailBasePath }: {
  order: ChangeOrder;
  detailBasePath: "/changes/orders" | "/approvals/orders";
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <TableRow
      className="cursor-pointer"
      onClick={() => { void navigate(`${detailBasePath}/${order.id}`); }}
      data-testid="mine-order-row"
      data-row-key={order.id}
    >
      <TableCell className="font-semibold text-foreground">{order.display_number}</TableCell>
      <TableCell className="max-w-56 truncate text-foreground">{order.title}</TableCell>
      <TableCell>
        <OrderStateBadge state={order.state} />
      </TableCell>
      <TableCell className="text-muted-foreground">
        {order.current_stage_position === null
          ? "—"
          : t("orders.list.stagePosition", { position: order.current_stage_position })}
      </TableCell>
      <TableCell className="text-muted-foreground">
        {formatTimestamp(order.submitted_at)}
      </TableCell>
      <TableCell className="text-right">
        {/* Reference ruling 2026-10-24: a navigation table ends with a quiet
            right-aligned affordance, not a labeled button; the row itself
            navigates and this echoes it. */}
        <Button
          variant="ghost"
          size="icon-sm"
          className="text-muted-foreground"
          aria-label={t("orders.list.open")}
          onClick={(event) => {
            event.stopPropagation();
            void navigate(`${detailBasePath}/${order.id}`);
          }}
          data-testid={`open-order-${order.display_number}`}
        >
          <ArrowRight aria-hidden />
        </Button>
      </TableCell>
    </TableRow>
  );
}

interface FilterState {
  keywordInput: string;
  q: string;
  /** "all" sentinel or a ChangeOrderState value. */
  state: string;
  /** "all" sentinel or an exact stage datasource_name. */
  datasource: string;
  submittedFrom: string;
}

const INITIAL_FILTERS: FilterState = {
  keywordInput: "",
  q: "",
  state: ALL_STATES,
  datasource: ALL_DATASOURCES,
  submittedFrom: "",
};

function hasActiveFilters(filters: FilterState): boolean {
  return (
    filters.q !== "" ||
    filters.state !== ALL_STATES ||
    filters.datasource !== ALL_DATASOURCES ||
    filters.submittedFrom !== ""
  );
}

export default function MinePage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [draftsOpen, setDraftsOpen] = useState(false);

  const [filters, setFilters] = useState<FilterState>(INITIAL_FILTERS);

  const patchFilters = (patch: Partial<FilterState>): void => {
    setFilters((previous) => ({ ...previous, ...patch }));
  };

  // ECS-style global search (owner feedback 2026-11-04 + 2026-11-05): the ONE
  // search box owns every server filter — free text maps to the q param
  // (matches order number + title server-side), and state / datasource / submitted-from chips
  // map to the state / datasource / submitted_from params (owner feedback:
  // submitted-until is not needed in the search box). The old datasource Select
  // and date pickers are gone; the bar's clear-all resets all params in one
  // gesture. Mapping happens in the event handlers (not an effect):
  // onTermsChange fires before the bar's trailing onLiveChange, and the ref
  // keeps the committed any-chip visible to both handlers within one batch.
  const [searchTerms, setSearchTerms] = useState<SearchTerm[]>([]);
  const committedTermsRef = useRef<SearchTerm[]>([]);
  const lastAnyText = (terms: readonly SearchTerm[]): string =>
    [...terms].reverse().find((term) => term.field === null)?.text ?? "";
  const textOf = (terms: readonly SearchTerm[], field: string): string =>
    [...terms].reverse().find((term) => term.field === field)?.text ?? "";
  const stateOfTerms = (terms: readonly SearchTerm[]): string => {
    const stateTerm = [...terms].reverse().find((term) => term.field === "state");
    if (stateTerm === undefined) return ALL_STATES;
    if (stateTerm.value !== undefined) return ORDER_STATES.find((c) => c === stateTerm.value) ?? ALL_STATES;
    return ORDER_STATES.find((candidate) => t(`orders.state.${candidate}`) === stateTerm.text) ?? ALL_STATES;
  };
  const handleTermsChange = (next: SearchTerm[]): void => {
    committedTermsRef.current = next;
    setSearchTerms(next);
    patchFilters({
      keywordInput: lastAnyText(next),
      state: stateOfTerms(next),
      datasource: textOf(next, "datasource") || ALL_DATASOURCES,
      submittedFrom: textOf(next, "submitted_from"),
    });
  };
  const handleLiveChange = (live: SearchTerm | null): void => {
    // Live any-field text wins while typing; a scoped draft (state value) is
    // chip-only; an emptied input falls back to the committed any-chip.
    const keyword =
      live !== null && live.field === null ? live.text : lastAnyText(committedTermsRef.current);
    patchFilters({ keywordInput: keyword });
  };

  // The keyword param is debounced; the select/date inputs commit directly.
  useEffect(() => {
    const timer = setTimeout(() => {
      setFilters((previous) =>
        previous.q === previous.keywordInput.trim()
          ? previous
          : { ...previous, q: previous.keywordInput.trim() },
      );
    }, KEYWORD_DEBOUNCE_MS);
    return () => { clearTimeout(timer); };
  }, [filters.keywordInput]);

  const serverFilters: OrderListFilters = {
    q: filters.q === "" ? undefined : filters.q,
    state: filters.state === ALL_STATES ? undefined : (filters.state as ChangeOrderState),
    datasource: filters.datasource === ALL_DATASOURCES ? undefined : filters.datasource,
    submitted_from: filters.submittedFrom === "" ? undefined : filters.submittedFrom,
  };

  // F-UX-004: arriving from the workspace "Pending My Execution" card with ?scope=execution switches the same
  // relation-scoped read to the executor pending view (presentation filter, see useMyChangeOrders).
  const [searchParams] = useSearchParams();
  const orderScope: OrderListScope = searchParams.get("scope") === "execution" ? "execution" : "submitter";

  const draftsQuery = useMyDrafts(true);
  const ordersPager = useCursorPage(20);
  const ordersQuery = useMyChangeOrders(
    true,
    serverFilters,
    { limit: ordersPager.pageSize, after: ordersPager.after },
    orderScope,
  );
  const ordersPageMeta = ordersQuery.data?.page;
  const orderItems = ordersQuery.data?.items ?? [];
  // Rows remaining in the list after filtering changed position: record old positions and tween instead of jumping.
  const ordersBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(orderItems.map((order) => order.id)),
  );
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const ordersLoaded = !ordersQuery.isPending && !ordersQuery.isError;
  // keepPreviousData initially shows rows from the previous page/keyword; mark the table body as stale to prevent clicking old rows.
  const ordersStale = ordersQuery.isFetching && ordersQuery.isPlaceholderData;
  const datasourceOptionsQuery = useOrderDatasourceOptions(true);
  const searchFields = useMemo<SearchFieldDef[]>(
    () => [
      {
        key: "state",
        label: t("orders.list.column.state"),
        options: ORDER_STATES.map((state) => ({ value: state, label: t(`orders.state.${state}`) })),
      },
      {
        key: "datasource",
        label: t("orders.list.filter.datasourceField"),
        options: (datasourceOptionsQuery.data ?? []).map((name) => ({ label: name })),
      },
      { key: "submitted_from", label: t("orders.list.filter.from"), input: "date" },
    ],
    [t, datasourceOptionsQuery.data],
  );

  const draftsError = draftsQuery.error ?? ordersQuery.error;

  // Draft deletion (owner ruling 2026-10-24): the drafts card is the surface —
  // confirm dialog at card level, DELETE with the row's If-Match version.
  const [pendingDelete, setPendingDelete] = useState<ChangeDraft | null>(null);
  const queryClient = useQueryClient();
  const deleteDraftMutation = useMutation({
    mutationFn: (draft: ChangeDraft) =>
      deleteChangeDraft(draft.id, {
        headers: { "If-Match": `"${String(draft.version)}"` },
      }),
    onSuccess: () => {
      toast.success(t("orders.list.draftsDeleted"));
      setPendingDelete(null);
      void queryClient.invalidateQueries({ queryKey: ["change-drafts", "mine"] });
    },
    onError: (error) => {
      const display = describeError(error, "deleteChangeDraft");
      toast.error(t(display.messageKey));
    },
  });
  const retry = (): void => {
    void draftsQuery.refetch();
    void ordersQuery.refetch();
  };
  // tabs-02 reference (owner ruling 2026-10-24): a controlled tab pair so the
  // content switch runs through TransitionPanel's slide+blur motion.
  const [mineTab, setMineTab] = useState<"audit" | "query">("audit");

  return (
    <PageLayout data-testid="mine-page">
      <PageHeader
        icon={FileStack}
        title={t("orders.list.title")}
        description={t("orders.list.description")}
      />
      <Tabs
        value={mineTab}
        onValueChange={(next) => { setMineTab(next === "query" ? "query" : "audit"); }}
      >
        <TabsList className="flex w-full justify-start overflow-x-auto overflow-y-hidden">
          <TabsTrigger value="audit" data-testid="tab-audit-orders">
            {t("orders.list.auditTab")}
          </TabsTrigger>
          <TabsTrigger value="query" data-testid="tab-query-orders">
            {t("orders.list.queryTab")}
          </TabsTrigger>
        </TabsList>
      </Tabs>
      <TransitionPanel
        activeIndex={mineTab === "audit" ? 0 : 1}
        transition={{ duration: 0.2, ease: "easeInOut" }}
        variants={TAB_PANEL_VARIANTS}
        keepMounted
      >
        <div className="flex flex-col gap-5">
          {draftsError !== null && (
            <ErrorState error={draftsError} operationId="listChangeOrders" onRetry={retry} />
          )}
          {draftsError === null && (draftsQuery.isPending || ordersQuery.isPending) && (
            <LoadingState variant="table" columns={6} />
          )}
          {draftsError === null && !draftsQuery.isPending && !ordersQuery.isPending && (
            <>
              {/* Delete confirmation (owner ruling 2026-10-24): the draft,
               * its SQL and its review records go in one transaction — the
               * dialog says so before the irreversible call. */}
              <DeleteDraftDialog
                pendingDelete={pendingDelete}
                isPending={deleteDraftMutation.isPending}
                onCancel={() => { setPendingDelete(null); }}
                onConfirm={(draft) => { deleteDraftMutation.mutate(draft); }}
              />
              {/* overflow-visible (review R2 H1): the search bar's suggestion
                  listbox and inline calendar extend below the card in empty or
                  1-row states; the default Card overflow-hidden would clip
                  late-month calendar days and make them unclickable. */}
              <Card className="overflow-visible">
                <ListToolbar
                  busy={ordersStale}
                  fill
                  actions={
                    <MineDraftsPopover
                      open={draftsOpen}
                      onOpenChange={setDraftsOpen}
                      drafts={draftsQuery.data ?? []}
                      onSelectDraft={(selected) => {
                        setDraftsOpen(false);
                        void navigate(`/changes/drafts/${selected.id}`);
                      }}
                      onRequestDelete={(target) => {
                        setDraftsOpen(false);
                        setPendingDelete(target);
                      }}
                      onNewDraft={() => {
                        setDraftsOpen(false);
                        void navigate("/changes/new");
                      }}
                    />
                  }
                >
                  <CardTitle>{t("orders.list.ordersCard")}</CardTitle>
                  {/* Owner feedback 2026-11-04 + 2026-11-05: ONE full-width
                      ECS-style search box replaces every filter control —
                      order number / title free text via the server q param, plus state /
                      datasource enum chips and submitted-from calendar chip mapped
                      to their server params (submitted-until is intentionally
                      omitted). The bar's clear-all is the single reset
                      affordance. */}
                  <GlobalSearchBar
                    fields={searchFields}
                    terms={searchTerms}
                    onTermsChange={handleTermsChange}
                    onLiveChange={handleLiveChange}
                    placeholder={t("orders.list.filter.searchPlaceholder")}
                    testId="filter-keyword"
                    className="w-full min-w-0 flex-1"
                  />
                </ListToolbar>
                <CardContent className="flex flex-col gap-3 px-0!">
                  {(ordersQuery.data?.items ?? []).length === 0 ? (
                    <EmptyState
                      filtered={hasActiveFilters(filters)}
                      title={
                        hasActiveFilters(filters)
                          ? t("orders.list.filter.emptyFiltered")
                          : orderScope === "execution"
                            ? t("orders.list.executionEmpty")
                            : t("orders.list.ordersEmpty")
                      }
                      testId="orders-empty"
                    />
                  ) : (
                    <Table data-testid="mine-orders-table">
                      <TableHeader>
                        <TableRow>
                          <TableHead>{t("orders.list.column.number")}</TableHead>
                          <TableHead>{t("orders.list.column.title")}</TableHead>
                          <TableHead>{t("orders.list.column.state")}</TableHead>
                          <TableHead>{t("orders.list.column.stage")}</TableHead>
                          <TableHead>{t("orders.list.column.submittedAt")}</TableHead>
                          <TableHead />
                        </TableRow>
                      </TableHeader>
                      <TableBody ref={ordersBodyRef} stale={ordersStale}>
                        {orderItems.map((order) => (
                          <OrderRow
                            key={order.id}
                            order={order}
                            detailBasePath={orderScope === "execution" ? "/approvals/orders" : "/changes/orders"}
                          />
                        ))}
                      </TableBody>
                    </Table>
                  )}
                  {ordersLoaded ? (
                    <TablePagination
                      busy={ordersStale}
                      page={ordersPager.pageNo}
                      hasMore={ordersPageMeta?.has_more ?? false}
                      isFirst={ordersPager.pageNo === 1}
                      onPrev={() => {
                        ordersPager.goPrev();
                      }}
                      onJumpPage={ordersPager.goToPage}
                      onNext={() => {
                        ordersPager.pushCursor(ordersPageMeta?.next_cursor);
                      }}
                      pageSize={ordersPager.pageSize}
                      onPageSizeChange={ordersPager.setPageSize}
                      rowsOnPage={orderItems.length}
                      testIdPrefix="mine-orders"
                    />
                  ) : null}
                </CardContent>
              </Card>
            </>
          )}
        </div>
        <div>
          <QueryOrdersTab />
        </div>
      </TransitionPanel>
    </PageLayout>
  );
}
