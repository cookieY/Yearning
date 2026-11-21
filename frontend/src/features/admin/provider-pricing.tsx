import { useState, type ReactElement } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Wand2 } from "lucide-react";
import type {
  AiCallUsage,
  AiProviderRateVersion,
  CreateAiProviderRateVersionRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useAiProviderRateVersions,
  useAiUsageCalls,
  useAiUsageSummaryToday,
  useCreateAiProviderRateVersion,
  useSettingsNamespace,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import i18next from "@/shared/i18n";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { formatTimestamp, timezoneOptions } from "@/shared/lib/timezones";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { useCursorPage } from "@/shared/lib/cursor-page";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";

/**
 * S008 reference pricing panel (FE package 2026-10-31): rate version list + new version form +
 * cost stats/usage ledger + persistent reference disclaimer (UI-02: amounts are called reference cost, never
 * misleading "actual deduction" terms). Reference template = input/output reference prices in global settings;
 * applying is an explicit button action (PRD: template application requires explicit action and does not overwrite filled values).
 */

export const RATE_REFERENCE_DECLARATION_KEY = "admin.providers.pricing.declaration";

function minuteToHM(minute: number): string {
  const h = Math.floor(minute / 60);
  const m = minute % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
}

/**
 * Parse a user-typed day-time into a minute-of-day, contract bound to
 * ≤1440 ("24:00" is the representable full-day end; <input type="time">
 * can never hold it). Accepts partial typing ("9", "9:", "9:3") as
 * progressive feedback and returns null for blank/garbled input so the
 * caller can keep the previous value instead of writing NaN.
 */
function parseDayMinute(text: string): number | null {
  if (text.trim() === "") return null;
  const match = /^(\d{1,2})(?::(\d{0,2}))?$/.exec(text.trim());
  if (match === null) return null;
  const h = Number(match[1]);
  const m = match[2] === undefined || match[2] === "" ? 0 : Number(match[2]);
  if (Number.isNaN(h) || Number.isNaN(m)) return null;
  return Math.min(h * 60 + m, 1440);
}

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

const EMPTY_WINDOW = {
  start_minute: 0,
  end_minute: 1440,
  input_price: "",
  output_price: "",
  cached_read_price: "",
  cached_write_price: "",
};

/** Contract price format (OpenAPI AiProviderRateWindow): non-negative fixed-point decimal, <=12 integer digits
 * + optional <=6 fractional digits ("0", "6", "2.34" are all valid); cached_* may be omitted (empty string =
 * unconfigured), input/output are required. */
const PRICE_PATTERN = /^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$/;

function emptyRateDraft() {
  return {
    currency: "CNY",
    timezone: "Asia/Shanghai",
    effective_from: "",
    effective_until: "",
    windows: [{ ...EMPTY_WINDOW }],
  };
}

type RateDraft = ReturnType<typeof emptyRateDraft>;

/** Price input: accessible name (aria-label, placeholder does not act as label) + per-field error
 * targeting (PRD §6). */
function PriceInput({ testId, label, value, onChange, error }: {
  testId: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  error: string | null;
}): ReactElement {
  return (
    <div className="flex flex-col gap-1">
      <Input
        aria-label={label}
        placeholder={label}
        value={value}
        onChange={(event) => { onChange(event.target.value); }}
        aria-invalid={error !== null}
        data-testid={testId}
        className="font-mono text-xs"
      />
      {error !== null && <p className="text-destructive text-xs">{error}</p>}
    </div>
  );
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

  const updateWindow = (index: number, patch: Partial<RateDraft["windows"][number]>): void => {
    const current = draft.windows[index];
    if (current === undefined) return;
    const windows = [...draft.windows];
    windows[index] = { ...current, ...patch };
    setDraft({ ...draft, windows });
  };

  const priceError = (index: number, field: string): string | null =>
    fieldErrors[`price-${String(index)}-${field}`] ?? null;

  const templateButton = (
    <Button
      type="button"
      size="sm"
      variant="outline"
      onClick={() => {
        // Explicit action: global reference price (minor -> major unit * 1e-2), fills empty fields only without overwriting existing values.
        const data = budgetSettingsQuery.data;
        // SettingsValue is a five-namespace union; narrow ai-budget via the `in` operator.
        const settings = data !== undefined && "input_price_per_million_minor" in data.settings
          ? data.settings
          : null;
        if (settings === null) return;
        // Currency guard: template prices are denominated in the settings currency; cross-currency import = wrong currency label
        // (PRD §3.3; backend enable endpoint RATE_CURRENCY_MISMATCH). Refuse import and explain.
        if (settings.currency !== draft.currency) {
          setTemplateNotice(
            t("admin.providers.pricing.templateCurrencyMismatch", {
              template: settings.currency,
              draft: draft.currency,
            }),
          );
          return;
        }
        setTemplateNotice(null);
        setDraft((current) => {
          const windows = current.windows.map((w, index) =>
            index === 0
              ? {
                  ...w,
                  input_price:
                    w.input_price === "" && settings.input_price_per_million_minor > 0
                      ? (settings.input_price_per_million_minor / 100).toFixed(2)
                      : w.input_price,
                  output_price:
                    w.output_price === "" && settings.output_price_per_million_minor > 0
                      ? (settings.output_price_per_million_minor / 100).toFixed(2)
                      : w.output_price,
                }
              : w,
          );
          return { ...current, windows };
        });
      }}
      data-testid="rate-apply-template"
    >
      <Wand2 aria-hidden />
      {t("admin.providers.pricing.applyTemplate")}
    </Button>
  );

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
        <div className="flex flex-col gap-3 rounded-lg border p-3" data-testid="provider-rate-form">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rate-currency">{t("admin.providers.pricing.currency")}</Label>
              <Select
                value={draft.currency}
                onValueChange={(value) => {
                  if (value !== "CNY" && value !== "USD") return;
                  // Switching currency invalidates any existing template conflict notice.
                  setTemplateNotice(null);
                  setDraft({ ...draft, currency: value });
                }}
              >
                <SelectTrigger id="rate-currency" data-testid="rate-currency">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="CNY">CNY</SelectItem>
                  <SelectItem value="USD">USD</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="rate-timezone">{t("admin.providers.pricing.timezone")}</Label>
              {/* Owner feedback 2026-11-05: the billing zone is picked from the
                  IANA list, not typed (server still validates the name). */}
              <Select
                value={draft.timezone}
                onValueChange={(value) => {
                  if (typeof value !== "string") return;
                  setDraft({ ...draft, timezone: value });
                }}
              >
                <SelectTrigger
                  id="rate-timezone"
                  className="w-full"
                  aria-invalid={fieldErrors.timezone !== undefined}
                  data-testid="rate-timezone"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false}>
                  <ScrollArea viewportClassName="max-h-60">
                    {timezoneOptions(draft.timezone).map((zone) => (
                      <SelectItem key={zone} value={zone}>{zone}</SelectItem>
                    ))}
                  </ScrollArea>
                </SelectContent>
              </Select>
              {fieldErrors.timezone !== undefined && (
                <p className="text-destructive text-xs">{fieldErrors.timezone}</p>
              )}
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="rate-from">{t("admin.providers.pricing.effectiveFrom")}</Label>
              <Input
                id="rate-from"
                type="date"
                value={draft.effective_from}
                onChange={(event) => { setDraft({ ...draft, effective_from: event.target.value }); }}
                data-testid="rate-from"
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="rate-until">{t("admin.providers.pricing.effectiveUntil")}</Label>
              <Input
                id="rate-until"
                type="date"
                value={draft.effective_until}
                onChange={(event) => { setDraft({ ...draft, effective_until: event.target.value }); }}
                aria-invalid={fieldErrors.effectiveUntil !== undefined}
                data-testid="rate-until"
              />
              {fieldErrors.effectiveUntil !== undefined && (
                <p className="text-destructive text-xs">{fieldErrors.effectiveUntil}</p>
              )}
            </div>
          </div>

          <div className="flex items-center justify-between">
            <p className="text-muted-foreground text-xs font-medium">
              {t("admin.providers.pricing.window")}
              <span className="ml-2">{t("admin.providers.pricing.unit")}</span>
            </p>
            {templateButton}
          </div>
          <p className="text-muted-foreground -mt-2 text-xs">{t("admin.providers.pricing.templateHint")}</p>
          {templateNotice !== null && (
            <Alert className="-mt-1" data-testid="rate-template-notice">
              <AlertDescription>{templateNotice}</AlertDescription>
            </Alert>
          )}

          {draft.windows.map((window, index) => (
            <div key={index} className="flex flex-col gap-2 rounded-md border p-2.5" data-testid={`rate-window-${String(index)}`}>
              <div className="flex items-center gap-2">
                <Input
                  aria-label={t("admin.providers.pricing.window")}
                  type="time"
                  value={minuteToHM(window.start_minute)}
                  onChange={(event) => {
                    const [h, m] = event.target.value.split(":").map(Number);
                    if (h === undefined || m === undefined || Number.isNaN(h) || Number.isNaN(m)) return;
                    const windows = [...draft.windows];
                    windows[index] = { ...window, start_minute: h * 60 + m };
                    setDraft({ ...draft, windows });
                  }}
                  data-testid={`rate-window-start-${String(index)}`}
                  className="w-24 font-mono text-xs"
                />
                <span className="text-muted-foreground text-xs">→</span>
                {/* end_minute contract upper bound is 1440 (full-day end of half-open interval [0,1440)):
                    type=time cannot hold "24:00" (browsers clear it as an unreadable control), so the end time
                    uses a text control with numeric parsing; start time keeps the time control (0..1439 is always
                    representable). */}
                <Input
                  aria-label={`${t("admin.providers.pricing.window")} end`}
                  type="text"
                  inputMode="numeric"
                  value={minuteToHM(window.end_minute)}
                  onChange={(event) => {
                    const minute = parseDayMinute(event.target.value);
                    if (minute === null) return;
                    const windows = [...draft.windows];
                    windows[index] = { ...window, end_minute: minute };
                    setDraft({ ...draft, windows });
                  }}
                  data-testid={`rate-window-end-${String(index)}`}
                  className="w-24 font-mono text-xs"
                />
                {index === 0 && (
                  <span className="text-muted-foreground text-xs">{t("admin.providers.pricing.baseWindow")}</span>
                )}
                <span className="flex-1" />
                {draft.windows.length > 1 && (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => { setDraft({ ...draft, windows: draft.windows.filter((_, i) => i !== index) }); }}
                    aria-label={t("admin.providers.pricing.removeWindow")}
                  >
                    ×
                  </Button>
                )}
              </div>
              <div className="grid gap-2 sm:grid-cols-4">
                <PriceInput
                  testId={`rate-input-price-${String(index)}`}
                  label={t("admin.providers.pricing.inputPrice")}
                  value={window.input_price}
                  onChange={(value) => { updateWindow(index, { input_price: value }); }}
                  error={priceError(index, "input_price")}
                />
                <PriceInput
                  testId={`rate-output-price-${String(index)}`}
                  label={t("admin.providers.pricing.outputPrice")}
                  value={window.output_price}
                  onChange={(value) => { updateWindow(index, { output_price: value }); }}
                  error={priceError(index, "output_price")}
                />
                <PriceInput
                  testId={`rate-cached-read-${String(index)}`}
                  label={t("admin.providers.pricing.cachedRead")}
                  value={window.cached_read_price}
                  onChange={(value) => { updateWindow(index, { cached_read_price: value }); }}
                  error={priceError(index, "cached_read_price")}
                />
                <PriceInput
                  testId={`rate-cached-write-${String(index)}`}
                  label={t("admin.providers.pricing.cachedWrite")}
                  value={window.cached_write_price}
                  onChange={(value) => { updateWindow(index, { cached_write_price: value }); }}
                  error={priceError(index, "cached_write_price")}
                />
              </div>
            </div>
          ))}
          <p className="text-muted-foreground text-xs">{t("admin.providers.pricing.crossMidnight")}</p>

          {draft.windows.length < 24 && (
            <div>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => {
                  const last = draft.windows[draft.windows.length - 1];
                  setDraft({
                    ...draft,
                    windows: [...draft.windows, { ...EMPTY_WINDOW, ...(last !== undefined ? { start_minute: last.end_minute % 1440 } : {}) }],
                  });
                }}
                data-testid="rate-add-window"
              >
                <Plus aria-hidden />
                {t("admin.providers.pricing.addWindow")}
              </Button>
            </div>
          )}

          {errorText !== null && (
            <Alert variant="destructive" data-testid="rate-form-error">
              <AlertTitle>{t("admin.providers.pricing.createFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <div className="flex gap-2">
            <Button
              type="button"
              size="sm"
              onClick={submit}
              disabled={createMutation.isPending}
              data-testid="rate-submit"
            >
              {t("common.save")}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => { setShowForm(false); setErrorText(null); setTemplateNotice(null); }}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

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
