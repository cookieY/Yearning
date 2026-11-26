import { useTranslation } from "react-i18next";
import type { AiCallUsage } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useAiUsageCalls,
  useAiUsageSummaryToday,
} from "@/features/admin/use-admin";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";

function basisLabel(basis: AiCallUsage["calc_basis"]): string {
  switch (basis) {
    case "full_detail": return "admin.providers.pricing.basisFull";
    case "partial_detail": return "admin.providers.pricing.basisPartial";
    case "base_only": return "admin.providers.pricing.basisBase";
    default: return "admin.providers.pricing.basisNone";
  }
}

// The OpenAPI doc comment says "null means unknown" while the generated
// type only marks the field optional, and the shared mock world already
// sends explicit null on this shape — the defense covers both encodings.
// Either way the UI must show the not-reported mark, never a literal
// "null" (and never a fabricated zero cost: Number(null) is 0).
function tokenText(value: number | undefined | null, notReported: string): string {
  return value == null ? notReported : String(value);
}

export function ProviderUsagePanel({ providerId }: { providerId: string }) {
  const { t } = useTranslation();
  const summaryQuery = useAiUsageSummaryToday(true);
  // Ledger uses contract cursor pagination (limit <= 200, contract cap); provider filtering is executed
  // server-side (RCP-20261102-S008 added provider_id param) — has_more is the honest boundary of this
  // provider stream, without lossy client-side filtering of whole pages.
  const pager = useCursorPage(200);
  const callsQuery = useAiUsageCalls(true, {
    providerId, limit: pager.pageSize, after: pager.after,
  });
  const notReported = t("admin.providers.pricing.notReported");
  // reference_amount is already a currency-unit decimal string on the wire
  // (OpenAPI: "Bounded decimal reference cost in currency units"; backend
  // formats numeric(24,8) verbatim). Any float round-trip — let alone the
  // old /1e9 nano misread — loses precision, so the string passes through
  // untouched and only the empty string means "not computable".
  const summary = (summaryQuery.data ?? []).map((row) => ({
    ...row,
    amount: row.reference_amount === "" ? null : row.reference_amount,
  }));
  const calls = callsQuery.data?.items ?? [];
  const callsLoaded = !callsQuery.isPending && !callsQuery.isError;

  return (
    <div className="flex flex-col gap-4" data-testid="provider-usage-panel">
      {/* Persistent disclaimer is a static note rather than a dynamic alert: do not occupy role=alert. */}
      <div className="rounded-lg border border-border/60 bg-muted/30 px-2.5 py-2 text-muted-foreground text-xs">
        {t("admin.providers.pricing.usageDeclaration")}
      </div>

      <div>
        <p className="text-foreground text-sm font-semibold">{t("admin.providers.pricing.usage")}</p>
        <p className="text-muted-foreground text-xs">{t("admin.providers.pricing.usageSummary")}</p>
        <p className="text-muted-foreground text-xs">{t("admin.providers.pricing.summaryScope")}</p>
        {summaryQuery.isPending ? (
          <LoadingState variant="inline" />
        ) : summaryQuery.isError ? (
          <ErrorState
            error={summaryQuery.error}
            operationId="listAiUsageSummary"
            onRetry={() => { void summaryQuery.refetch(); }}
          />
        ) : summary.length === 0 ? (
          <p className="text-muted-foreground mt-1 text-xs">{t("admin.providers.pricing.notComputable")}</p>
        ) : (
          <ul className="mt-1.5 flex flex-col gap-1" data-testid="provider-usage-summary">
            {summary.map((row) => (
              <li key={row.currency} className="text-xs">
                <span className="font-mono font-semibold">{row.currency}</span>
                <span className="text-muted-foreground">
                  {" "}
                  · {t("admin.providers.pricing.amount")}: {row.amount === null ? t("admin.providers.pricing.notComputable") : row.amount}
                  {" "}
                  · {t("admin.providers.pricing.basis")}: {row.computable_calls}/{row.calls}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div>
        <p className="text-foreground text-sm font-semibold">{t("admin.providers.pricing.calls")}</p>
        {callsQuery.isPending ? (
          <LoadingState variant="table" columns={8} />
        ) : callsQuery.isError ? (
          <ErrorState
            error={callsQuery.error}
            operationId="listAiUsageCalls"
            onRetry={() => { void callsQuery.refetch(); }}
          />
        ) : calls.length === 0 ? (
          <p className="text-muted-foreground mt-1 text-xs">{t("admin.providers.pricing.empty")}</p>
        ) : (
          <>
          <Table density="compact" className="mt-1.5" data-testid="provider-usage-calls">
            <TableHeader>
              <TableRow>
                <TableHead>{t("admin.providers.pricing.callTime")}</TableHead>
                <TableHead>{t("admin.providers.pricing.stageTurn")}</TableHead>
                <TableHead>{t("admin.providers.pricing.params")}</TableHead>
                <TableHead>{t("admin.providers.pricing.rateSource")}</TableHead>
                <TableHead>{t("admin.providers.pricing.tokens")}</TableHead>
                <TableHead>{t("admin.providers.pricing.reasoningTokens")}</TableHead>
                <TableHead>{t("admin.providers.pricing.amount")}</TableHead>
                <TableHead>{t("admin.providers.pricing.basis")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {calls.map((call) => (
                <TableRow key={call.id} data-testid={`provider-usage-row-${call.id}`}>
                  <TableCell className="font-mono text-xs">{formatTimestamp(call.sent_at)}</TableCell>
                  <TableCell className="text-xs tabular-nums">
                    S{String(call.stage_position)}/T{String(call.model_turn)}#{String(call.call_index)}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {call.reasoning_effort ?? "—"} / {tokenText(call.max_tokens, "—")} / {call.temperature ?? "—"}
                  </TableCell>
                  <TableCell className="text-xs">
                    {call.rate_source === "none"
                      ? t("admin.providers.pricing.none")
                      : call.rate_source === "global_settings"
                        ? t("admin.providers.pricing.globalSettings")
                        : t("admin.providers.pricing.rateVersion")}
                    {call.currency != null ? ` (${call.currency})` : ""}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {tokenText(call.input_tokens, notReported)} / {tokenText(call.output_tokens, notReported)}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {tokenText(call.cached_tokens, notReported)} / {tokenText(call.cache_write_tokens, notReported)}
                    {call.reasoning_tokens != null
                      ? ` (R ${String(call.reasoning_tokens)})`
                      : ""}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {call.reference_amount == null || call.reference_amount === ""
                      ? t("admin.providers.pricing.notComputable")
                      : call.reference_amount}
                  </TableCell>
                  <TableCell className="text-xs">{t(basisLabel(call.calc_basis))}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {callsLoaded && (
            <TablePagination
              testIdPrefix="provider-usage-calls"
              busy={callsQuery.isFetching && callsQuery.isPlaceholderData}
              page={pager.pageNo}
              hasMore={callsQuery.data.page.has_more}
              isFirst={pager.isFirst}
              onPrev={pager.goPrev}
              onJumpPage={pager.goToPage}
              onNext={() => { pager.pushCursor(callsQuery.data.page.next_cursor); }}
              pageSize={pager.pageSize}
              onPageSizeChange={pager.setPageSize}
              rowsOnPage={calls.length}
            />
          )}
          </>
        )}
      </div>
    </div>
  );
}
