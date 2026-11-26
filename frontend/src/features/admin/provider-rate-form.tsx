import type { Dispatch, ReactElement, SetStateAction } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Wand2 } from "lucide-react";
import type { SettingsNamespace } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";
import { timezoneOptions } from "@/shared/lib/timezones";

export function minuteToHM(minute: number): string {
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

export const EMPTY_WINDOW = {
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
export const PRICE_PATTERN = /^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$/;

export function emptyRateDraft() {
  return {
    currency: "CNY",
    timezone: "Asia/Shanghai",
    effective_from: "",
    effective_until: "",
    windows: [{ ...EMPTY_WINDOW }],
  };
}

export type RateDraft = ReturnType<typeof emptyRateDraft>;

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

export function ProviderRateForm({
  draft,
  setDraft,
  fieldErrors,
  templateNotice,
  setTemplateNotice,
  errorText,
  budgetSettingsData,
  pending,
  onSubmit,
  onCancel,
}: {
  draft: RateDraft;
  setDraft: Dispatch<SetStateAction<RateDraft>>;
  fieldErrors: Record<string, string>;
  templateNotice: string | null;
  setTemplateNotice: (notice: string | null) => void;
  errorText: string | null;
  budgetSettingsData: SettingsNamespace | undefined;
  pending: boolean;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();

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
        const data = budgetSettingsData;
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

  return (
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
          onClick={onSubmit}
          disabled={pending}
          data-testid="rate-submit"
        >
          {t("common.save")}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={onCancel}
        >
          {t("common.cancel")}
        </Button>
      </div>
    </div>
  );
}
