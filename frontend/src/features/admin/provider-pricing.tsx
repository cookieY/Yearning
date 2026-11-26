import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import type {
  AiProviderRateVersion,
  CreateAiProviderRateVersionRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  emptyRateDraft,
  minuteToHM,
  PRICE_PATTERN,
  ProviderRateForm,
  type RateDraft,
} from "@/features/admin/provider-rate-form";
import {
  useAiProviderRateVersions,
  useCreateAiProviderRateVersion,
  useSettingsNamespace,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import i18next from "@/shared/i18n";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
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

export { ProviderUsagePanel } from "@/features/admin/provider-usage-panel";

/**
 * S008 reference pricing panel (FE package 2026-10-31): rate version list + new version form +
 * cost stats/usage ledger + persistent reference disclaimer (UI-02: amounts are called reference cost, never
 * misleading "actual deduction" terms). Reference template = input/output reference prices in global settings;
 * applying is an explicit button action (PRD: template application requires explicit action and does not overwrite filled values).
 */

export const RATE_REFERENCE_DECLARATION_KEY = "admin.providers.pricing.declaration";

function effectiveText(version: AiProviderRateVersion): string {
  const until = version.effective_until ?? i18next.t("admin.providers.pricing.openEnded");
  return `${version.effective_from.slice(0, 10)} → ${until.slice(0, 10)}`;
}

type RateVersionStatus = "current" | "future" | "expired";

/** Encapsulate clock read inside a function (react-compiler purity rule does not trace across function boundaries, same pattern as
 * evidence-sheet.remainingDays): determination must use real current time. */
function currentNowMs(): number {
  return Date.now();
}

/** PRD §6: currently effective and next effective versions are readable. Open version (effective_until
 * omitted or null on wire) compares with upper bound +infinity; the future version with the earliest from is marked
 * next effective, while other past/future versions carry no badge (effective period text is self-explanatory). */
function versionStatus(version: AiProviderRateVersion, nowMs: number): RateVersionStatus {
  const from = Date.parse(version.effective_from);
  const until = version.effective_until == null
    ? Number.POSITIVE_INFINITY
    : Date.parse(version.effective_until);
  if (from <= nowMs && nowMs < until) return "current";
  return from > nowMs ? "future" : "expired";
}

export function ProviderPricingPanel({ providerId }: { providerId: string }) {
  const { t } = useTranslation();
  const versionsQuery = useAiProviderRateVersions(providerId, true);
  // Reference template source: input/output reference prices from global settings (filled only on explicit button action).
  const budgetSettingsQuery = useSettingsNamespace("ai-budget", true);
  const versions = versionsQuery.data ?? [];
  const createMutation = useCreateAiProviderRateVersion();
  const [draft, setDraft] = useState<RateDraft>(emptyRateDraft);
  const [showForm, setShowForm] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);
  // PRD §6 "save errors target specific fields": client mirror validation runs first (locally determinable errors
  // do not send requests); server VALIDATION_FAILED also carries field names (RCP-20261102-S008
  // meta.fields), merged into this state on onError to target the slot.
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  // Template cross-currency conflict notice (when an explicit action is refused, the reason must be explained, never silently ignored).
  const [templateNotice, setTemplateNotice] = useState<string | null>(null);
  // Date-aware badge: currently effective is determined by real time; earliest from among future versions = next
  // effective (list order is not guaranteed ascending by from, so cannot pick the first future by position).
  const nowMs = currentNowMs();
  const statuses = versions.map((version) => versionStatus(version, nowMs));
  let nextIndex = -1;
  let nextFrom = Number.POSITIVE_INFINITY;
  statuses.forEach((status, index) => {
    if (status !== "future") return;
    const from = Date.parse(versions[index]?.effective_from ?? "");
    if (from < nextFrom) {
      nextFrom = from;
      nextIndex = index;
    }
  });

  const submit = () => {
    setErrorText(null);
    setFieldErrors({});
    if (draft.effective_from === "") {
      setErrorText(t("admin.providers.pricing.effectiveFrom"));
      return;
    }
    // Client field validation (mirroring contract): currency is constrained by Select; timezone is required; until is not earlier
    // than from; four price fields are targeted per-field by PRICE_PATTERN (cached may be empty).
    // Timezone is now a Select (owner 2026-11-05), so normal operation cannot produce an empty value; this
    // branch remains as a defense in case the draft carries an empty string when the server holds an empty timezone.
    const errors: Record<string, string> = {};
    if (draft.timezone.trim() === "") {
      errors.timezone = t("admin.providers.pricing.timezoneRequired");
    }
    if (draft.effective_until !== "" && draft.effective_until < draft.effective_from) {
      errors.effectiveUntil = t("admin.providers.pricing.untilBeforeFrom");
    }
    draft.windows.forEach((w, index) => {
      for (const field of ["input_price", "output_price"] as const) {
        if (!PRICE_PATTERN.test(w[field])) {
          errors[`price-${String(index)}-${field}`] = t("admin.providers.pricing.invalidPrice");
        }
      }
      for (const field of ["cached_read_price", "cached_write_price"] as const) {
        if (w[field] !== "" && !PRICE_PATTERN.test(w[field])) {
          errors[`price-${String(index)}-${field}`] = t("admin.providers.pricing.invalidPrice");
        }
      }
    });
    if (Object.keys(errors).length > 0) {
      setFieldErrors(errors);
      return;
    }
    const body: CreateAiProviderRateVersionRequest = {
      currency: draft.currency === "USD" ? "USD" : "CNY",
      timezone: draft.timezone,
      // Anchor the picked calendar date at UTC midnight: a local-midnight
      // Date would serialize via toISOString one day early for every
      // timezone east of UTC (the deployment default is Asia/Shanghai).
      effective_from: new Date(`${draft.effective_from}T00:00:00Z`).toISOString(),
      ...(draft.effective_until === ""
        ? {}
        : { effective_until: new Date(`${draft.effective_until}T00:00:00Z`).toISOString() }),
      windows: draft.windows.map((w) => ({
        start_minute: w.start_minute,
        end_minute: w.end_minute,
        input_price: w.input_price,
        output_price: w.output_price,
        ...(w.cached_read_price === "" ? {} : { cached_read_price: w.cached_read_price }),
        ...(w.cached_write_price === "" ? {} : { cached_write_price: w.cached_write_price }),
      })),
    };
    createMutation.mutate(
      { providerId, body },
      {
        onSuccess: () => {
          appToast.success(t("common.createdToast"));
          setShowForm(false);
          setDraft(emptyRateDraft());
          setTemplateNotice(null);
        },
        onError: (error) => {
          appToast.error(describeErrorText(describeError(error, "createAiProviderRateVersion")));
          // RCP-20261102-S008: server VALIDATION_FAILED carries field targeting
          // (meta.fields). Fields with inline slots (timezone/effective_until/
          // prices) map to their slots; fields without slots (currency/windows/effective_from)
          // keep the form-level notice. Client mirror validation remains the first line of defense (locally determinable
          // errors do not send requests), while server fields cover residual cases.
          const server = describeError(error, "createAiProviderRateVersion").fields;
          const firstOf = (messages: string[] | undefined): string | undefined =>
            messages !== undefined && messages.length > 0 ? messages[0] : undefined;
          const merged: Record<string, string> = {};
          const timezoneMessage = firstOf(server.timezone);
          if (timezoneMessage !== undefined) {
            merged.timezone = timezoneMessage;
          }
          const untilMessage = firstOf(server.effective_until);
          if (untilMessage !== undefined) {
            merged.effectiveUntil = untilMessage;
          }
          draft.windows.forEach((w, index) => {
            for (const field of ["input_price", "output_price", "cached_read_price", "cached_write_price"] as const) {
              const message = firstOf(server[field]);
              if (message === undefined) continue;
              if (field.startsWith("cached") && w[field] === "") continue;
              merged[`price-${String(index)}-${field}`] = message;
            }
          });
          if (Object.keys(merged).length > 0) setFieldErrors(merged);
          const mapped = new Set(["timezone", "effective_until", "input_price", "output_price",
            "cached_read_price", "cached_write_price"]);
          const unmapped = Object.keys(server).filter((field) => !mapped.has(field));
          if (Object.keys(merged).length === 0 || unmapped.length > 0) {
            setErrorText(describeErrorText(describeError(error, "createAiProviderRateVersion")));
          }
        },
      },
    );
  };

  return (
    <div className="flex flex-col gap-4" data-testid="provider-pricing-panel">
      {/* Persistent disclaimer is a static note rather than a dynamic alert: do not occupy role=alert, reserving alert semantics for
          real form errors and action feedback (such as template currency conflict). */}
      <div
        className="rounded-lg border border-border/60 bg-muted/30 px-2.5 py-2 text-muted-foreground text-xs"
        data-testid="provider-pricing-declaration"
      >
        {t("admin.providers.pricing.declaration")}
      </div>

      {versionsQuery.isPending ? (
        <LoadingState variant="table" columns={5} />
      ) : versionsQuery.isError ? (
        <ErrorState
          error={versionsQuery.error}
          operationId="listAiProviderRateVersions"
          onRetry={() => { void versionsQuery.refetch(); }}
        />
      ) : versions.length === 0 ? (
        <p className="text-muted-foreground text-sm">{t("admin.providers.pricing.empty")}</p>
      ) : (
        <Table density="compact" data-testid="provider-rate-table">
          <TableHeader>
            <TableRow>
              <TableHead>{t("admin.providers.pricing.version")}</TableHead>
              <TableHead>{t("admin.providers.pricing.currency")}</TableHead>
              <TableHead>{t("admin.providers.pricing.timezone")}</TableHead>
              <TableHead>{t("admin.providers.pricing.effective")}</TableHead>
              <TableHead>{t("admin.providers.pricing.windows")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {versions.map((version, index) => (
              <TableRow key={version.id} data-testid={`provider-rate-row-${version.id}`}>
                <TableCell className="font-semibold">v{version.version}</TableCell>
                <TableCell>
                  <Badge variant="secondary">{version.currency}</Badge>
                </TableCell>
                <TableCell className="font-mono text-xs">{version.timezone}</TableCell>
                <TableCell className="text-xs">
                  {effectiveText(version)}
                  {statuses[index] === "current" ? (
                    <Badge variant="outline" className="ml-1.5">
                      {t("admin.providers.pricing.current")}
                    </Badge>
                  ) : index === nextIndex ? (
                    <Badge variant="outline" className="ml-1.5">
                      {t("admin.providers.pricing.nextEffective")}
                    </Badge>
                  ) : null}
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {version.windows
                    .map((w) => `${minuteToHM(w.start_minute)}–${minuteToHM(w.end_minute)}`)
                    .join(", ")}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {!showForm && (
        <div>
          <Button
            type="button"
            size="sm"
            onClick={() => { setShowForm(true); }}
            data-testid="rate-new-version"
          >
            <Plus aria-hidden />
            {t("admin.providers.pricing.newVersion")}
          </Button>
        </div>
      )}

      {showForm && (
        <ProviderRateForm
          draft={draft}
          setDraft={setDraft}
          fieldErrors={fieldErrors}
          templateNotice={templateNotice}
          setTemplateNotice={setTemplateNotice}
          errorText={errorText}
          budgetSettingsData={budgetSettingsQuery.data}
          pending={createMutation.isPending}
          onSubmit={submit}
          onCancel={() => { setShowForm(false); setErrorText(null); setTemplateNotice(null); }}
        />
      )}
    </div>
  );
}
