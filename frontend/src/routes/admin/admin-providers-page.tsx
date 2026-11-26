import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Bot, Cpu, Info, KeyRound, ListOrdered, Pencil, PlugZap, Plus, Trash2 } from "lucide-react";
import type { AiProvider } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useAiProviders,
  useTestAiProviderConnection,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import {
  ProviderDeleteDialog,
  ProviderFormDialog,
} from "@/routes/admin/admin-ai-provider-dialogs";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { Card } from "@/shared/components/ui/card";
import { cn } from "@/shared/lib/utils";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";

/**
 * Provider Management (route /admin/review-engine/providers; UI spec §5.3 puts
 * provider administration in the review-engine group). The list renders the
 * primary/backup order exactly as the runtime consumes it —
 * selection_priority ascending (backend provider/chain.go:3), index 0 is
 * the primary. The API key is write-only: create requires it once, replace
 * keeps the stored key unless a new one is typed, and every read face
 * exposes only api_key_configured. The privacy/output contract hashes are
 * NOT form fields: the platform derives both from code constants
 * (provider contract_hash.go) and pins them per row at write time —
 * identical fingerprints across providers are what authorizes fallback
 * (chain.go gate) (R008/R005 boundary: no per-order budget here — that
 * is the ai-budget settings namespace).
 */

function ProviderCard({
  provider,
  ordinal,
  onEdit,
  onAskDelete,
}: {
  provider: AiProvider;
  ordinal: number;
  onEdit: () => void;
  onAskDelete: () => void;
}) {
  const { t } = useTranslation();
  const testMutation = useTestAiProviderConnection();
  // RCP-20261017 item 019: connection test returns a synchronous {ok} result — pending/success/failure states
  // render directly; failure carries request_id and upstream refusal reason (same channel as §18.75-A), with no
  // Task polling path anymore (the old Task shape left success silent and failure without detail).
  const [testOk, setTestOk] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorDetail, setErrorDetail] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);

  return (
    <div
      data-testid={`provider-row-${provider.id}`}
      data-row-key={provider.id}
      className="flex min-w-0 flex-col gap-2.5 rounded-xl border border-border bg-card p-4"
    >
      <div className="flex min-w-0 items-center gap-2.5">
        <div
          className={cn(
            "flex size-8 shrink-0 items-center justify-center rounded-lg",
            provider.enabled ? "bg-tone-emerald/10 text-tone-emerald" : "bg-muted text-muted-foreground",
          )}
        >
          <Cpu className="size-4" aria-hidden />
        </div>
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-foreground" title={provider.name}>{provider.name}</span>
        <div className="shrink-0">
          <TableRowActions testId={`provider-actions-${provider.id}`}>
            <DropdownMenuItem
              disabled={testMutation.isPending}
              onClick={() => {
                setTestOk(false);
                setErrorKey(null);
                setErrorDetail(null);
                setErrorRequestId(null);
                testMutation.mutate(
                  { id: provider.id },
                  {
                    onSuccess: () => {
                      setTestOk(true);
                      appToast.success(t("admin.providers.testOk"), {
                        description: provider.name,
                      });
                    },
                    onError: (error) => {
                      const display = describeError(error, "testAiProviderConnection");
                      setErrorKey(display.messageKey);
                      setErrorDetail(display.detail);
                      setErrorRequestId(display.requestId);
                      appToast.error(t(display.messageKey));
                    },
                  },
                );
              }}
              data-testid={`provider-test-${provider.id}`}
            >
              <PlugZap /> {t("admin.datasources.testConnection")}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={onEdit} data-testid={`provider-edit-${provider.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onClick={onAskDelete} data-testid={`provider-delete-${provider.id}`}>
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </TableRowActions>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant={ordinal === 0 ? "default" : "outline"} className="h-5 px-2" data-testid={`provider-role-${provider.id}`}>
          {ordinal === 0 ? t("admin.providers.primary") : t("admin.providers.backup")}
        </Badge>
        <StatusBadge
          tone={provider.enabled ? "emerald" : "stone"}
          data-testid={`provider-enabled-${provider.id}`}
        >
          {provider.enabled ? t("admin.providers.enabled") : t("admin.providers.disabled")}
        </StatusBadge>
        <StatusBadge
          tone={provider.api_key_configured ? "emerald" : "red"}
          data-testid={`provider-apikey-${provider.id}`}
        >
          <KeyRound className="mr-1 size-3" />
          {provider.api_key_configured
            ? t("admin.providers.configured")
            : t("admin.providers.notConfigured")}
        </StatusBadge>
      </div>
      <div className="flex min-w-0 flex-col gap-0.5 font-mono text-xs">
        <span className="truncate text-foreground/90" title={provider.model_name}>{provider.model_name}</span>
        <span className="truncate text-muted-foreground" title={provider.base_url}>{provider.base_url}</span>
        <span className="flex min-w-0 items-baseline justify-between gap-2 text-muted-foreground">
          <span className="truncate" title={provider.provider_kind}>{provider.provider_kind}</span>
          <span className="shrink-0 tabular-nums">{t("admin.providers.priority")} {provider.selection_priority}</span>
        </span>
      </div>
      {testMutation.isPending ? (
        <p className="text-muted-foreground text-xs" data-testid={`provider-test-running-${provider.id}`}>
          {t("admin.datasources.testRunning")}
        </p>
      ) : null}
      {testOk && !testMutation.isPending ? (
        <p className="text-xs text-[var(--risk-safe)]" data-testid={`provider-test-ok-${provider.id}`}>
          {t("admin.providers.testOk")}
        </p>
      ) : null}
      {errorKey !== null ? (
        <div>
          <p className="text-xs text-destructive">{t(errorKey)}</p>
          {errorDetail !== null ? (
            <p className="break-all font-mono text-xs text-destructive" data-testid={`provider-test-error-detail-${provider.id}`}>
              {errorDetail}
            </p>
          ) : null}
          {errorRequestId !== null ? (
            <p className="text-muted-foreground text-xs">request_id: {errorRequestId}</p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

export default function AdminProvidersPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const [search, setSearch] = useState("");
  // §18.82: backend cursor pagination (default 20 rows/page); search changes reset to page 1.
  const pager = useCursorPage(20);
  const query = useAiProviders(enabled, search.trim() || undefined, {
    limit: pager.pageSize,
    after: pager.after,
  });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const providersLoaded = !query.isPending && !query.isError;
  const [editing, setEditing] = useState<AiProvider | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  // §18.73: search goes through server-side q (name / BaseURL / model substring); keyword is used only to
  // distinguish empty-state copy.
  const keyword = search.trim();
  const providers = query.data?.items ?? [];
  // §18.73: search narrows server-side; tween whole-row position changes via FLIP when the row set changes.
  const providersBodyRef = useFlipReorder<HTMLDivElement>(
    JSON.stringify(providers.map((provider) => provider.id)),
  );
  const [deleting, setDeleting] = useState<AiProvider | null>(null);

  return (
    <PageLayout>
      {/* 2026-11-04 regression back to shared visual language: the custom in-Card header from 22e79fd dropped
          data-slot="page-header" (24px h1) and the ListToolbar slot,
          caught by the visual-consistency gate in CI; restore the PageHeader shell
          while keeping the notification area and Provider card grid intact. */}
      <PageHeader
        title={t("admin.providers.title")}
        description={t("admin.providers.description")}
        icon={Bot}
        actions={
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
            data-testid="provider-create"
          >
            <Plus /> {t("admin.providers.createTitle")}
          </Button>
        }
      />
      <Card data-testid="admin-providers-panel" className="gap-0 py-0">
        <div className="grid grid-cols-1 gap-3 border-b border-border p-5 md:grid-cols-2">
          <div className="flex items-start gap-3 rounded-xl border border-tone-sky/30 bg-tone-sky/5 p-3.5 text-[13px] text-tone-sky">
            <Info className="mt-0.5 size-4 shrink-0" aria-hidden />
            <div className="flex flex-col gap-1">
              <p className="text-sm font-medium">{t("admin.providers.notice.keyTitle")}</p>
              <p className="leading-relaxed">{t("admin.providers.notice.keyBody")}</p>
            </div>
          </div>
          <div className="flex items-start gap-3 rounded-xl border border-border p-3.5 text-[13px] text-muted-foreground">
            <ListOrdered className="mt-0.5 size-4 shrink-0" aria-hidden />
            <p className="leading-relaxed">{t("admin.providers.notice.orderBody")}</p>
          </div>
        </div>
        <ListToolbar
          className="pt-5"
          busy={query.isFetching && query.isPlaceholderData}
          fill
        >
          <TableSearchInput
            value={search}
            onChange={(value) => {
              setSearch(value);
              pager.reset();
            }}
            testId="admin-providers-search"
          />
        </ListToolbar>
        <section className="flex flex-col gap-4 p-5">
          {query.isPending ? (
            <LoadingState variant="panel" />
          ) : query.isError ? (
            <ErrorState error={query.error} operationId="listAiProviders" onRetry={() => void query.refetch()} />
          ) : providers.length === 0 && keyword !== "" ? (
            <EmptyState
              title={t("table.emptySearch")}
              filtered
              testId="admin-providers-search-empty"
            />
          ) : providers.length === 0 ? (
            <EmptyState
              title={t("admin.providers.emptyTitle")}
              description={t("admin.providers.emptyDescription")}
            />
          ) : (
            <div
              ref={providersBodyRef}
              aria-busy={query.isFetching && query.isPlaceholderData}
              className={cn(
                "grid grid-cols-1 gap-3 transition-opacity duration-150 ease-out sm:grid-cols-2 lg:grid-cols-4",
                query.isFetching && query.isPlaceholderData && "opacity-60",
              )}
            >
              {providers.map((provider, index) => (
                <ProviderCard
                  key={provider.id}
                  provider={provider}
                  ordinal={index}
                  onEdit={() => {
                    setEditing(provider);
                    setDialogOpen(true);
                  }}
                  onAskDelete={() => { setDeleting(provider); }}
                />
              ))}
            </div>
          )}
        </section>
        {providersLoaded ? (
          <TablePagination
            busy={query.isFetching && query.isPlaceholderData}
            page={pager.pageNo}
            hasMore={query.data.page.has_more}
            isFirst={pager.isFirst}
            onPrev={pager.goPrev}
            onJumpPage={pager.goToPage}
            onNext={() => { pager.pushCursor(query.data.page.next_cursor); }}
            pageSize={pager.pageSize}
            onPageSizeChange={pager.setPageSize}
            rowsOnPage={query.data.items.length}
            testIdPrefix="providers"
          />
        ) : null}
      </Card>
      {deleting !== null ? (
        <ProviderDeleteDialog
          key={deleting.id}
          provider={deleting}
          onClose={() => { setDeleting(null); }}
        />
      ) : null}
      {dialogOpen ? (
        <ProviderFormDialog
          key={editing?.id ?? "new"}
          editing={editing}
          onClose={() => {
            setDialogOpen(false);
            setEditing(null);
          }}
        />
      ) : null}
    </PageLayout>
  );
}
